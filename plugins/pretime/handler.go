package pretime

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"goftpd/internal/plugin"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/lib/pq"
	_ "github.com/mattn/go-sqlite3"
)

const (
	eventNewPreTime = "NEWPRETIME"
	eventOldPreTime = "OLDPRETIME"
)

var (
	dateDirRE          = regexp.MustCompile(`^\d{4}$|^\d{8}$|^\d{4}-\d{2}-\d{2}$`)
	fieldIdentifierRE  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	tableIdentifierRE  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)*$`)
	defaultIgnoreGlobs = []string{
		"cd[0-9]*", "disc[0-9]*", "disk[0-9]*", "dvd[0-9]*",
		"codec", "cover", "covers", "extra", "extras", "featurettes",
		"proof", "sample", "samples", "sub", "subs", "subtitles",
		"vobsub", "vobsubs",
	}
)

type Handler struct {
	svc           *plugin.Services
	debug         bool
	lateMinutes   int
	sections      []string
	paths         []string
	ignoreDirs    []string
	providerOrder []string

	sqlite   sqliteConfig
	mysql    mysqlConfig
	postgres postgresConfig
	api      apiConfig

	client   *http.Client
	jobs     chan job
	stopCh   chan struct{}
	stopOnce sync.Once
	seenMu   sync.Mutex
	seen     map[string]time.Time
}

type sqliteConfig struct {
	Enabled       bool
	Path          string
	Table         string
	ReleaseField  string
	UnixTimeField string
}

type mysqlConfig struct {
	Enabled       bool
	DSN           string
	Table         string
	ReleaseField  string
	UnixTimeField string
}

type postgresConfig struct {
	Enabled       bool
	DSN           string
	Table         string
	ReleaseField  string
	UnixTimeField string
}

type apiConfig struct {
	Enabled        bool
	TimeoutSeconds int
	Providers      []apiProvider
}

type apiProvider struct {
	Name         string
	URL          string
	UnixTimePath string
}

type job struct {
	path    string
	relname string
	section string
	user    string
	group   string
}

func New() *Handler {
	return &Handler{
		lateMinutes:   10,
		ignoreDirs:    append([]string(nil), defaultIgnoreGlobs...),
		providerOrder: []string{"sqlite", "mysql", "postgres", "api"},
		sqlite: sqliteConfig{
			Path:          "plugins/pretime/releases.db",
			Table:         "releases",
			ReleaseField:  "release",
			UnixTimeField: "timestamp_unix",
		},
		mysql: mysqlConfig{
			Table:         "releases",
			ReleaseField:  "release",
			UnixTimeField: "timestamp_unix",
		},
		postgres: postgresConfig{
			Table:         "releases",
			ReleaseField:  "release",
			UnixTimeField: "timestamp_unix",
		},
		api: apiConfig{
			Enabled:        true,
			TimeoutSeconds: 5,
			Providers: []apiProvider{
				{
					Name:         "predb.club",
					URL:          "https://predb.club/api/v1/?q=%22{release}%22",
					UnixTimePath: "data.rows.0.preAt",
				},
				{
					Name:         "predb.net",
					URL:          "https://api.predb.net/?release={release}",
					UnixTimePath: "data.0.pretime",
				},
			},
		},
		client: &http.Client{Timeout: 5 * time.Second},
		jobs:   make(chan job, 128),
		stopCh: make(chan struct{}),
		seen:   make(map[string]time.Time),
	}
}

func (h *Handler) Name() string { return "pretime" }

func (h *Handler) Init(svc *plugin.Services, cfg map[string]interface{}) error {
	h.svc = svc
	if b, ok := cfg["debug"].(bool); ok {
		h.debug = b
	} else if svc != nil {
		h.debug = svc.Debug
	}
	if n := intConfig(cfg["late_minutes"], 0); n > 0 {
		h.lateMinutes = n
	}
	if sections := toStringSlice(cfg["sections"]); len(sections) > 0 {
		h.sections = sections
	}
	if paths := toStringSlice(cfg["paths"]); len(paths) > 0 {
		h.paths = paths
	}
	if ignores := toStringSlice(cfg["ignore_dirs"]); len(ignores) > 0 {
		h.ignoreDirs = ignores
	}
	if order := toStringSlice(cfg["provider_order"]); len(order) > 0 {
		h.providerOrder = order
	}

	sqliteCfg := configSection(cfg, "sqlite")
	h.sqlite.Enabled = boolConfig(sqliteCfg["enabled"], true)
	if s := stringConfig(sqliteCfg["path"], ""); strings.TrimSpace(s) != "" {
		h.sqlite.Path = strings.TrimSpace(s)
	}
	if s := stringConfig(sqliteCfg["table"], ""); strings.TrimSpace(s) != "" {
		h.sqlite.Table = strings.TrimSpace(s)
	}
	if s := stringConfig(sqliteCfg["release_field"], ""); strings.TrimSpace(s) != "" {
		h.sqlite.ReleaseField = strings.TrimSpace(s)
	}
	if s := stringConfig(sqliteCfg["unixtime_field"], ""); strings.TrimSpace(s) != "" {
		h.sqlite.UnixTimeField = strings.TrimSpace(s)
	}

	mysqlCfg := configSection(cfg, "mysql")
	h.mysql.Enabled = boolConfig(mysqlCfg["enabled"], false)
	if s := stringConfig(mysqlCfg["dsn"], ""); strings.TrimSpace(s) != "" {
		h.mysql.DSN = strings.TrimSpace(s)
	}
	if s := stringConfig(mysqlCfg["table"], ""); strings.TrimSpace(s) != "" {
		h.mysql.Table = strings.TrimSpace(s)
	}
	if s := stringConfig(mysqlCfg["release_field"], ""); strings.TrimSpace(s) != "" {
		h.mysql.ReleaseField = strings.TrimSpace(s)
	}
	if s := stringConfig(mysqlCfg["unixtime_field"], ""); strings.TrimSpace(s) != "" {
		h.mysql.UnixTimeField = strings.TrimSpace(s)
	}

	postgresCfg := configSection(cfg, "postgres")
	h.postgres.Enabled = boolConfig(postgresCfg["enabled"], false)
	if s := stringConfig(postgresCfg["dsn"], ""); strings.TrimSpace(s) != "" {
		h.postgres.DSN = strings.TrimSpace(s)
	}
	if s := stringConfig(postgresCfg["table"], ""); strings.TrimSpace(s) != "" {
		h.postgres.Table = strings.TrimSpace(s)
	}
	if s := stringConfig(postgresCfg["release_field"], ""); strings.TrimSpace(s) != "" {
		h.postgres.ReleaseField = strings.TrimSpace(s)
	}
	if s := stringConfig(postgresCfg["unixtime_field"], ""); strings.TrimSpace(s) != "" {
		h.postgres.UnixTimeField = strings.TrimSpace(s)
	}

	apiCfg := configSection(cfg, "api")
	h.api.Enabled = boolConfig(apiCfg["enabled"], h.api.Enabled)
	if n := intConfig(apiCfg["timeout_seconds"], 0); n > 0 {
		h.api.TimeoutSeconds = n
	}
	if providers := decodeAPIProviders(apiCfg["providers"]); len(providers) > 0 {
		h.api.Providers = providers
	}
	if h.api.TimeoutSeconds > 0 {
		h.client.Timeout = time.Duration(h.api.TimeoutSeconds) * time.Second
	}

	go h.worker()
	h.logf("initialized sections=%v paths=%v providers=%v", h.sections, h.paths, h.providerOrder)
	return nil
}

func (h *Handler) Stop() error {
	h.stopOnce.Do(func() { close(h.stopCh) })
	return nil
}

func (h *Handler) OnEvent(evt *plugin.Event) error {
	if evt == nil || evt.Type != plugin.EventMKDir {
		return nil
	}
	if !h.shouldHandle(evt) {
		return nil
	}

	h.seenMu.Lock()
	if ts, ok := h.seen[evt.Path]; ok && time.Since(ts) < 10*time.Minute {
		h.seenMu.Unlock()
		return nil
	}
	h.seen[evt.Path] = time.Now()
	if len(h.seen) > 2000 {
		cutoff := time.Now().Add(-30 * time.Minute)
		for k, seenAt := range h.seen {
			if seenAt.Before(cutoff) {
				delete(h.seen, k)
			}
		}
	}
	h.seenMu.Unlock()

	j := job{
		path:    evt.Path,
		relname: evt.Filename,
		section: evt.Section,
	}
	if evt.User != nil {
		j.user = evt.User.Name
		j.group = evt.User.PrimaryGroup
	}
	select {
	case h.jobs <- j:
	default:
		h.logf("job queue full, dropping %s", evt.Path)
	}
	return nil
}

func (h *Handler) worker() {
	for {
		select {
		case <-h.stopCh:
			return
		case j := <-h.jobs:
			h.processJob(j)
		}
	}
}

func (h *Handler) processJob(j job) {
	if h.svc == nil || h.svc.EmitEvent == nil {
		return
	}
	preUnix, provider, err := h.lookupRelease(j.relname)
	if err != nil {
		h.logf("lookup %s failed: %v", j.relname, err)
		return
	}
	if preUnix <= 0 {
		return
	}

	preAt := time.Unix(preUnix, 0)
	age := time.Since(preAt)
	if age < 0 {
		age = 0
	}
	eventType := eventNewPreTime
	if age > time.Duration(h.lateMinutes)*time.Minute {
		eventType = eventOldPreTime
	}

	data := map[string]string{
		"relname":        j.relname,
		"pretime_unix":   strconv.FormatInt(preUnix, 10),
		"preage_seconds": strconv.FormatInt(int64(age.Seconds()), 10),
		"preage":         formatPreAge(age),
		"predate":        preAt.Format("2006-01-02"),
		"pretime":        preAt.Format("15:04:05"),
		"provider":       provider,
		"u_name":         j.user,
		"g_name":         j.group,
	}
	h.svc.EmitEvent(eventType, j.path, j.relname, j.section, 0, 0, data)
	h.logf("found pretime for %s via %s -> %s ago", j.relname, provider, data["preage"])
}

func (h *Handler) shouldHandle(evt *plugin.Event) bool {
	if evt == nil {
		return false
	}
	if !isReleaseDir(evt.Path, evt.Section) {
		return false
	}
	if matchAnyGlob(strings.ToLower(strings.TrimSpace(evt.Filename)), h.ignoreDirs) {
		return false
	}
	if len(h.paths) > 0 && matchAnyPath(evt.Path, h.paths) {
		return true
	}
	if len(h.sections) > 0 {
		return matchSection(evt.Section, h.sections)
	}
	return len(h.paths) == 0
}

func (h *Handler) lookupRelease(release string) (int64, string, error) {
	var errs []string
	for _, provider := range h.providerOrder {
		switch strings.ToLower(strings.TrimSpace(provider)) {
		case "sqlite":
			if !h.sqlite.Enabled {
				continue
			}
			ts, err := lookupSQL("sqlite3", h.sqlite.Path, h.sqlite.Table, h.sqlite.ReleaseField, h.sqlite.UnixTimeField, release)
			if err == nil && ts > 0 {
				return ts, "sqlite", nil
			}
			if err != nil && h.debug {
				errs = append(errs, "sqlite: "+err.Error())
			}
		case "mysql":
			if !h.mysql.Enabled || strings.TrimSpace(h.mysql.DSN) == "" {
				continue
			}
			ts, err := lookupSQL("mysql", h.mysql.DSN, h.mysql.Table, h.mysql.ReleaseField, h.mysql.UnixTimeField, release)
			if err == nil && ts > 0 {
				return ts, "mysql", nil
			}
			if err != nil && h.debug {
				errs = append(errs, "mysql: "+err.Error())
			}
		case "postgres":
			if !h.postgres.Enabled || strings.TrimSpace(h.postgres.DSN) == "" {
				continue
			}
			ts, err := lookupSQL("postgres", h.postgres.DSN, h.postgres.Table, h.postgres.ReleaseField, h.postgres.UnixTimeField, release)
			if err == nil && ts > 0 {
				return ts, "postgres", nil
			}
			if err != nil && h.debug {
				errs = append(errs, "postgres: "+err.Error())
			}
		case "api":
			if !h.api.Enabled {
				continue
			}
			ts, name, err := h.lookupAPI(release)
			if err == nil && ts > 0 {
				return ts, name, nil
			}
			if err != nil && h.debug {
				errs = append(errs, "api: "+err.Error())
			}
		}
	}
	if len(errs) > 0 {
		return 0, "", errors.New(strings.Join(errs, "; "))
	}
	return 0, "", nil
}

func (h *Handler) lookupAPI(release string) (int64, string, error) {
	var errs []string
	for _, provider := range h.api.Providers {
		if strings.TrimSpace(provider.URL) == "" || strings.TrimSpace(provider.UnixTimePath) == "" {
			continue
		}
		u := buildProviderURL(provider.URL, release)
		resp, err := h.client.Get(u)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s request failed: %v", provider.Name, err))
			continue
		}
		body, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil {
			errs = append(errs, fmt.Sprintf("%s read failed: %v", provider.Name, readErr))
			continue
		}
		if resp.StatusCode != http.StatusOK {
			errs = append(errs, fmt.Sprintf("%s http %d", provider.Name, resp.StatusCode))
			continue
		}
		var payload interface{}
		if err := json.Unmarshal(body, &payload); err != nil {
			errs = append(errs, fmt.Sprintf("%s json failed: %v", provider.Name, err))
			continue
		}
		ts, ok := extractUnixTime(payload, provider.UnixTimePath)
		if ok && ts > 0 {
			name := strings.TrimSpace(provider.Name)
			if name == "" {
				name = "api"
			}
			return ts, name, nil
		}
		errs = append(errs, fmt.Sprintf("%s no unix time at %s", provider.Name, provider.UnixTimePath))
	}
	if len(errs) > 0 {
		return 0, "", errors.New(strings.Join(errs, "; "))
	}
	return 0, "", nil
}

func lookupSQL(driver, dsn, table, releaseField, unixTimeField, release string) (int64, error) {
	if !validFieldIdentifier(releaseField) {
		return 0, fmt.Errorf("invalid release_field %q", releaseField)
	}
	if !validFieldIdentifier(unixTimeField) {
		return 0, fmt.Errorf("invalid unixtime_field %q", unixTimeField)
	}
	if !validTableIdentifier(table) {
		return 0, fmt.Errorf("invalid table %q", table)
	}
	db, err := sql.Open(driver, dsn)
	if err != nil {
		return 0, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	db.SetConnMaxLifetime(30 * time.Second)

	query := fmt.Sprintf(
		"SELECT %s FROM %s WHERE %s = %s LIMIT 1",
		quoteSQLIdentifier(driver, unixTimeField),
		quoteSQLTable(driver, table),
		quoteSQLIdentifier(driver, releaseField),
		sqlPlaceholder(driver, 1),
	)
	var raw interface{}
	if err := db.QueryRow(query, release).Scan(&raw); err != nil {
		if err == sql.ErrNoRows {
			return 0, nil
		}
		return 0, err
	}
	return unixFromAny(raw)
}

func matchSection(section string, allowed []string) bool {
	if section == "" || len(allowed) == 0 {
		return false
	}
	up := strings.ToUpper(section)
	for _, s := range allowed {
		if strings.Contains(up, strings.ToUpper(strings.TrimSpace(s))) {
			return true
		}
	}
	return false
}

func matchAnyPath(p string, patterns []string) bool {
	p = path.Clean("/" + strings.TrimSpace(p))
	for _, pattern := range patterns {
		pattern = path.Clean("/" + strings.TrimSpace(pattern))
		if ok, _ := path.Match(pattern, p); ok {
			return true
		}
	}
	return false
}

func matchAnyGlob(name string, patterns []string) bool {
	for _, pattern := range patterns {
		pattern = strings.ToLower(strings.TrimSpace(pattern))
		if pattern == "" {
			continue
		}
		if ok, _ := path.Match(pattern, name); ok {
			return true
		}
	}
	return false
}

func isReleaseDir(eventPath, section string) bool {
	clean := path.Clean("/" + strings.TrimSpace(eventPath))
	parent := path.Dir(clean)
	sectionName := strings.TrimSpace(section)
	if sectionName == "" {
		return false
	}
	if strings.EqualFold(path.Base(parent), sectionName) {
		return true
	}
	if !dateDirRE.MatchString(path.Base(parent)) {
		return false
	}
	return strings.EqualFold(path.Base(path.Dir(parent)), sectionName)
}

func buildProviderURL(template, release string) string {
	replacer := strings.NewReplacer(
		"{release}", url.QueryEscape(release),
		"{release_raw}", release,
	)
	return replacer.Replace(template)
}

func extractUnixTime(payload interface{}, pathExpr string) (int64, bool) {
	current := payload
	pathExpr = strings.ReplaceAll(strings.TrimSpace(pathExpr), "[", ".")
	pathExpr = strings.ReplaceAll(pathExpr, "]", "")
	for _, part := range strings.Split(pathExpr, ".") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		switch node := current.(type) {
		case map[string]interface{}:
			next, ok := node[part]
			if !ok {
				return 0, false
			}
			current = next
		case []interface{}:
			idx, err := strconv.Atoi(part)
			if err != nil || idx < 0 || idx >= len(node) {
				return 0, false
			}
			current = node[idx]
		default:
			return 0, false
		}
	}
	ts, err := unixFromAny(current)
	return ts, err == nil && ts > 0
}

func unixFromAny(raw interface{}) (int64, error) {
	switch v := raw.(type) {
	case int64:
		return v, nil
	case int32:
		return int64(v), nil
	case int:
		return int64(v), nil
	case float64:
		return int64(v), nil
	case float32:
		return int64(v), nil
	case []byte:
		return strconv.ParseInt(strings.TrimSpace(string(v)), 10, 64)
	case string:
		return strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	case json.Number:
		return v.Int64()
	default:
		return 0, fmt.Errorf("unsupported unix time type %T", raw)
	}
}

func formatPreAge(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	days := int(d / (24 * time.Hour))
	hours := int((d % (24 * time.Hour)) / time.Hour)
	minutes := int((d % time.Hour) / time.Minute)
	seconds := int((d % time.Minute) / time.Second)
	switch {
	case days > 0:
		if minutes > 0 {
			return fmt.Sprintf("%dd %dh %dm", days, hours, minutes)
		}
		return fmt.Sprintf("%dd %dh", days, hours)
	case hours > 0:
		if seconds > 0 && minutes == 0 {
			return fmt.Sprintf("%dh %ds", hours, seconds)
		}
		return fmt.Sprintf("%dh %dm", hours, minutes)
	default:
		return fmt.Sprintf("%dm %ds", minutes, seconds)
	}
}

func decodeAPIProviders(raw interface{}) []apiProvider {
	switch v := raw.(type) {
	case []apiProvider:
		return v
	case []interface{}:
		out := make([]apiProvider, 0, len(v))
		for _, item := range v {
			if m, ok := item.(map[string]interface{}); ok {
				p := apiProvider{
					Name:         strings.TrimSpace(stringConfig(m["name"], "")),
					URL:          strings.TrimSpace(stringConfig(m["url"], "")),
					UnixTimePath: strings.TrimSpace(stringConfig(m["unix_time_path"], "")),
				}
				if p.URL != "" && p.UnixTimePath != "" {
					out = append(out, p)
				}
			}
		}
		return out
	default:
		return nil
	}
}

func configSection(cfg map[string]interface{}, key string) map[string]interface{} {
	raw, ok := cfg[key]
	if !ok {
		return map[string]interface{}{}
	}
	switch v := raw.(type) {
	case map[string]interface{}:
		return v
	case map[interface{}]interface{}:
		out := make(map[string]interface{}, len(v))
		for k, val := range v {
			ks, ok := k.(string)
			if !ok {
				continue
			}
			out[ks] = val
		}
		return out
	default:
		return map[string]interface{}{}
	}
}

func toStringSlice(raw interface{}) []string {
	switch v := raw.(type) {
	case []string:
		return v
	case []interface{}:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
		return out
	case string:
		if strings.TrimSpace(v) == "" {
			return nil
		}
		parts := strings.Split(v, ",")
		out := make([]string, 0, len(parts))
		for _, part := range parts {
			part = strings.TrimSpace(part)
			if part != "" {
				out = append(out, part)
			}
		}
		return out
	default:
		return nil
	}
}

func stringConfig(raw interface{}, fallback string) string {
	if s, ok := raw.(string); ok {
		return s
	}
	return fallback
}

func intConfig(raw interface{}, fallback int) int {
	switch v := raw.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err == nil {
			return n
		}
	}
	return fallback
}

func boolConfig(raw interface{}, fallback bool) bool {
	switch v := raw.(type) {
	case bool:
		return v
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "true", "1", "yes", "y", "on":
			return true
		case "false", "0", "no", "n", "off":
			return false
		}
	}
	return fallback
}

func validFieldIdentifier(name string) bool {
	return fieldIdentifierRE.MatchString(strings.TrimSpace(name))
}

func validTableIdentifier(name string) bool {
	return tableIdentifierRE.MatchString(strings.TrimSpace(name))
}

func quoteSQLIdentifier(driver, name string) string {
	name = strings.TrimSpace(name)
	if strings.EqualFold(strings.TrimSpace(driver), "postgres") {
		return `"` + name + `"`
	}
	return "`" + name + "`"
}

func quoteSQLTable(driver, name string) string {
	parts := strings.Split(strings.TrimSpace(name), ".")
	for i, part := range parts {
		parts[i] = quoteSQLIdentifier(driver, part)
	}
	return strings.Join(parts, ".")
}

func sqlPlaceholder(driver string, index int) string {
	if strings.EqualFold(strings.TrimSpace(driver), "postgres") {
		return fmt.Sprintf("$%d", index)
	}
	return "?"
}

func (h *Handler) logf(format string, args ...interface{}) {
	if !h.debug {
		return
	}
	log.Printf("[PRETIME] "+format, args...)
}
