package top

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"weaveftpd/sitebot/internal/event"
	"weaveftpd/sitebot/internal/plugin"
	tmpl "weaveftpd/sitebot/internal/template"
)

type uploaderStat struct {
	User  string
	Group string
	Files int64
	Bytes int64
}

type Plugin struct {
	debug           bool
	usersDir        string
	location        *time.Location
	channels        []string
	replyTarget     string
	defaultCount    int
	maxCount        int
	excludedUsers   map[string]struct{}
	excludedGroups  map[string]struct{}
	autoEnabled     bool
	autoInterval    time.Duration
	autoOnlyNonZero bool
	theme           *tmpl.Theme
	mu              sync.RWMutex
	asyncEmit       func(outType, text, section, relpath string)
	autoStop        chan struct{}
	autoStopped     chan struct{}
}

func New() *Plugin {
	return &Plugin{
		usersDir:        "../etc/users",
		location:        time.Local,
		channels:        []string{"#weaveftpd"},
		replyTarget:     "channel",
		defaultCount:    10,
		maxCount:        25,
		autoInterval:    8 * time.Hour,
		autoOnlyNonZero: true,
	}
}

func (p *Plugin) Name() string { return "Top" }

func (p *Plugin) SetAsyncEmitter(fn func(outType, text, section, relpath string)) {
	p.asyncEmit = fn
}

func (p *Plugin) Initialize(config map[string]interface{}) error {
	if debug, ok := config["debug"].(bool); ok {
		p.debug = debug
	}
	if raw, ok := config["timezone"]; ok {
		if s, ok := raw.(string); ok && strings.TrimSpace(s) != "" {
			if loc, err := time.LoadLocation(strings.TrimSpace(s)); err == nil {
				p.location = loc
			} else if p.debug {
				log.Printf("[Top] invalid timezone %q: %v", s, err)
			}
		}
	}
	if themeFile, ok := config["theme_file"].(string); ok && strings.TrimSpace(themeFile) != "" {
		th, err := tmpl.LoadTheme(themeFile)
		if err == nil {
			p.theme = th
		}
	}

	cfg := plugin.ConfigSection(config, "top")
	if s, ok := stringConfig(cfg, config, "users_dir", "top_users_dir"); ok && strings.TrimSpace(s) != "" {
		p.usersDir = strings.TrimSpace(s)
	}
	if raw, ok := configValueOK(cfg, config, "channels", "top_channels"); ok {
		p.channels = plugin.ToStringSlice(raw, p.channels)
	}
	if s, ok := stringConfig(cfg, config, "reply_target", "top_reply_target"); ok && strings.TrimSpace(s) != "" {
		p.replyTarget = strings.ToLower(strings.TrimSpace(s))
	}
	if n := intConfig(configValue(cfg, config, "default_count", "top_default_count"), p.defaultCount); n > 0 {
		p.defaultCount = n
	}
	if n := intConfig(configValue(cfg, config, "max_count", "top_max_count"), p.maxCount); n > 0 {
		p.maxCount = n
	}
	if p.defaultCount > p.maxCount {
		p.defaultCount = p.maxCount
	}
	if raw, ok := configValueOK(cfg, config, "excluded_users", "top_excluded_users"); ok {
		p.excludedUsers = lowerStringSet(plugin.ToStringSlice(raw, nil))
	}
	if raw, ok := configValueOK(cfg, config, "excluded_groups", "top_excluded_groups"); ok {
		p.excludedGroups = lowerStringSet(plugin.ToStringSlice(raw, nil))
	}
	if b, ok := boolConfig(configValue(cfg, config, "auto_enabled", "top_auto_enabled")); ok {
		p.autoEnabled = b
	}
	if n := intConfig(configValue(cfg, config, "auto_interval_hours", "top_auto_interval_hours"), 0); n > 0 {
		p.autoInterval = time.Duration(n) * time.Hour
	}
	if b, ok := boolConfig(configValue(cfg, config, "auto_only_nonzero", "top_auto_only_nonzero")); ok {
		p.autoOnlyNonZero = b
	}

	if p.autoEnabled && p.autoInterval > 0 {
		p.startAutoAnnounce()
	}
	return nil
}

func (p *Plugin) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.autoStop != nil {
		close(p.autoStop)
		stopCh := p.autoStopped
		p.autoStop = nil
		p.autoStopped = nil
		if stopCh != nil {
			<-stopCh
		}
	}
	return nil
}

func (p *Plugin) OnEvent(evt *event.Event) ([]plugin.Output, error) {
	if evt.Type != event.EventCommand {
		return nil, nil
	}
	if strings.ToLower(strings.TrimSpace(evt.Data["command"])) != "top" {
		return nil, nil
	}
	if !p.channelAllowed(evt) {
		return nil, nil
	}

	count, err := p.resolveCount(strings.TrimSpace(evt.Data["args"]))
	if err != nil {
		vars := map[string]string{
			"response": err.Error(),
			"user":     evt.User,
			"channel":  evt.Data["channel"],
		}
		return p.reply(evt, p.render("TOPCMD_ERROR", vars, "TOP: "+err.Error())), nil
	}
	lines, err := p.buildLines(count, true)
	if err != nil {
		vars := map[string]string{
			"response": err.Error(),
			"user":     evt.User,
			"channel":  evt.Data["channel"],
		}
		return p.reply(evt, p.render("TOPCMD_ERROR", vars, "TOP: "+err.Error())), nil
	}
	return p.replies(evt, lines...), nil
}

func (p *Plugin) resolveCount(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return p.defaultCount, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("usage: !top [%d-%d]", 1, p.maxCount)
	}
	if n > p.maxCount {
		n = p.maxCount
	}
	return n, nil
}

func (p *Plugin) buildLines(limit int, includeEmptyMessage bool) ([]string, error) {
	stats, totalFiles, totalBytes, err := p.loadDayUploadStats()
	if err != nil {
		return nil, err
	}
	if len(stats) == 0 {
		if !includeEmptyMessage {
			return nil, nil
		}
		return []string{p.render("TOPCMD_EMPTY", map[string]string{
			"response": "No uploads recorded for today.",
		}, "TOP: No uploads recorded for today.")}, nil
	}
	if limit <= 0 || limit > len(stats) {
		limit = len(stats)
	}

	lines := make([]string, 0, limit+2)
	lines = append(lines, p.render("TOPCMD_HEADER", map[string]string{
		"count": strconv.Itoa(len(stats)),
	}, fmt.Sprintf("TOP UPLOADERS FOR THE DAY: [ %d Users ]", len(stats))))

	for idx, stat := range stats[:limit] {
		lines = append(lines, p.render("TOPCMD_ENTRY", map[string]string{
			"rank":     fmt.Sprintf("%02d", idx+1),
			"user":     stat.User,
			"group":    stat.Group,
			"files":    strconv.FormatInt(stat.Files, 10),
			"size":     formatBytes(stat.Bytes),
			"response": stat.User,
		}, fmt.Sprintf("[%02d] %s/%s - (%d Files) - (%s)", idx+1, stat.User, stat.Group, stat.Files, formatBytes(stat.Bytes))))
	}

	lines = append(lines, p.render("TOPCMD_TOTAL", map[string]string{
		"files": strconv.FormatInt(totalFiles, 10),
		"size":  formatBytes(totalBytes),
	}, fmt.Sprintf("TOTAL UPLOADS FOR THE DAY: ( %d Files ) - ( %s )", totalFiles, formatBytes(totalBytes))))
	return lines, nil
}

func (p *Plugin) loadDayUploadStats() ([]uploaderStat, int64, int64, error) {
	dirEntries, err := os.ReadDir(p.usersDir)
	if err != nil {
		return nil, 0, 0, err
	}

	stats := make([]uploaderStat, 0, len(dirEntries))
	var totalFiles int64
	var totalBytes int64

	now := time.Now()
	if p.location != nil {
		now = now.In(p.location)
	}

	for _, entry := range dirEntries {
		name := strings.TrimSpace(entry.Name())
		if name == "" || strings.HasPrefix(name, ".") || entry.IsDir() {
			continue
		}
		stat, err := parseDayUploadSnapshot(filepath.Join(p.usersDir, name), name, now)
		if err != nil {
			if p.debug {
				log.Printf("[Top] skipping %s: %v", filepath.Join(p.usersDir, name), err)
			}
			continue
		}
		if p.isExcluded(stat) {
			continue
		}
		if p.autoOnlyNonZero && stat.Files == 0 && stat.Bytes == 0 {
			continue
		}
		if stat.Files == 0 && stat.Bytes == 0 {
			continue
		}
		stats = append(stats, stat)
		totalFiles += stat.Files
		totalBytes += stat.Bytes
	}

	sort.Slice(stats, func(i, j int) bool {
		if stats[i].Bytes != stats[j].Bytes {
			return stats[i].Bytes > stats[j].Bytes
		}
		if stats[i].Files != stats[j].Files {
			return stats[i].Files > stats[j].Files
		}
		return strings.ToLower(stats[i].User) < strings.ToLower(stats[j].User)
	})
	return stats, totalFiles, totalBytes, nil
}

func (p *Plugin) isExcluded(stat uploaderStat) bool {
	if _, ok := p.excludedUsers[strings.ToLower(strings.TrimSpace(stat.User))]; ok {
		return true
	}
	if _, ok := p.excludedGroups[strings.ToLower(strings.TrimSpace(stat.Group))]; ok {
		return true
	}
	return false
}

func parseDayUploadSnapshot(path, username string, now time.Time) (uploaderStat, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return uploaderStat{}, err
	}
	var fileModTime int64
	if st, err := os.Stat(path); err == nil {
		fileModTime = st.ModTime().Unix()
	}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	var (
		foundDayUp   bool
		files        int64
		bytes        int64
		lastLogin    int64
		periodAnchor int64
		group        = "Unknown"
	)
	for _, line := range lines {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "DAYUP "):
			fields := strings.Fields(line)
			if len(fields) < 3 {
				return uploaderStat{}, fmt.Errorf("short DAYUP line")
			}
			var err error
			files, err = strconv.ParseInt(fields[1], 10, 64)
			if err != nil {
				return uploaderStat{}, err
			}
			bytes, err = strconv.ParseInt(fields[2], 10, 64)
			if err != nil {
				return uploaderStat{}, err
			}
			foundDayUp = true
		case strings.HasPrefix(line, "PRIMARY_GROUP "):
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				group = fields[1]
			}
		case strings.HasPrefix(line, "GROUP "):
			fields := strings.Fields(line)
			if len(fields) >= 2 && group == "Unknown" {
				group = fields[1]
			}
		case strings.HasPrefix(line, "TIME "):
			fields := strings.Fields(line)
			if len(fields) >= 3 {
				if ts, err := strconv.ParseInt(fields[2], 10, 64); err == nil {
					lastLogin = ts
				}
			}
			if len(fields) >= 6 {
				if ts, err := strconv.ParseInt(fields[5], 10, 64); err == nil {
					periodAnchor = ts
				}
			}
		}
	}
	if !foundDayUp {
		return uploaderStat{}, fmt.Errorf("no DAYUP line")
	}
	anchor := periodAnchor
	if anchor <= 0 {
		anchor = fileModTime
	}
	if anchor <= 0 {
		anchor = lastLogin
	}
	if anchor > 0 {
		prev := time.Unix(anchor, 0).In(now.Location())
		if prev.Year() != now.Year() || prev.YearDay() != now.YearDay() {
			return uploaderStat{User: username, Group: group}, nil
		}
	}
	return uploaderStat{User: username, Group: group, Files: files, Bytes: bytes}, nil
}

func (p *Plugin) startAutoAnnounce() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.autoStop != nil || p.autoInterval <= 0 {
		return
	}
	stop := make(chan struct{})
	stopped := make(chan struct{})
	p.autoStop = stop
	p.autoStopped = stopped

	go func(interval time.Duration) {
		defer close(stopped)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				p.emitAutoTop()
			case <-stop:
				return
			}
		}
	}(p.autoInterval)
}

func (p *Plugin) emitAutoTop() {
	if p.asyncEmit == nil {
		return
	}
	lines, err := p.buildLines(p.defaultCount, false)
	if err != nil || len(lines) == 0 {
		if err != nil && p.debug {
			log.Printf("[Top] auto announce failed: %v", err)
		}
		return
	}
	for _, line := range lines {
		p.asyncEmit("TOP", line, "", "")
	}
}

func (p *Plugin) channelAllowed(evt *event.Event) bool {
	if len(p.channels) == 0 {
		return true
	}
	channel := strings.TrimSpace(evt.Data["channel"])
	for _, allowed := range p.channels {
		if strings.EqualFold(strings.TrimSpace(allowed), channel) {
			return true
		}
	}
	return false
}

func (p *Plugin) replies(evt *event.Event, lines ...string) []plugin.Output {
	target := strings.TrimSpace(evt.Data["channel"])
	noticeReply := false
	switch {
	case strings.HasPrefix(p.replyTarget, "#"):
		target = p.replyTarget
	case p.replyTarget == "notice":
		target = evt.User
		noticeReply = true
	case p.replyTarget == "pm":
		target = evt.User
	default:
		if target == "" {
			target = evt.User
		}
	}
	out := make([]plugin.Output, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		out = append(out, plugin.Output{Type: "COMMAND", Target: target, Notice: noticeReply, Text: line})
	}
	return out
}

func (p *Plugin) reply(evt *event.Event, text string) []plugin.Output {
	return p.replies(evt, text)
}

func (p *Plugin) render(key string, vars map[string]string, fallback string) string {
	if p.theme != nil {
		if raw, ok := p.theme.Announces[key]; ok && raw != "" {
			return tmpl.Render(raw, vars)
		}
	}
	return fallback
}

func formatBytes(bytes int64) string {
	if bytes < 0 {
		bytes = 0
	}
	const (
		kb = 1024
		mb = 1024 * 1024
		gb = 1024 * 1024 * 1024
		tb = 1024 * 1024 * 1024 * 1024
		pb = 1024 * 1024 * 1024 * 1024 * 1024
	)
	value := float64(bytes)
	switch {
	case bytes >= pb:
		return fmt.Sprintf("%.2f PB", value/float64(pb))
	case bytes >= tb:
		return fmt.Sprintf("%.2f TB", value/float64(tb))
	case bytes >= gb:
		return fmt.Sprintf("%.2f GB", value/float64(gb))
	case bytes >= mb:
		return fmt.Sprintf("%.2f MB", value/float64(mb))
	case bytes >= kb:
		return fmt.Sprintf("%.2f KB", value/float64(kb))
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}

func configValue(section, flat map[string]interface{}, sectionKey, flatKey string) interface{} {
	raw, _ := configValueOK(section, flat, sectionKey, flatKey)
	return raw
}

func configValueOK(section, flat map[string]interface{}, sectionKey, flatKey string) (interface{}, bool) {
	if raw, ok := section[sectionKey]; ok {
		return raw, true
	}
	raw, ok := flat[flatKey]
	return raw, ok
}

func stringConfig(section, flat map[string]interface{}, sectionKey, flatKey string) (string, bool) {
	raw, ok := configValueOK(section, flat, sectionKey, flatKey)
	if !ok {
		return "", false
	}
	s, ok := raw.(string)
	return s, ok
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
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return n
		}
	}
	return fallback
}

func boolConfig(raw interface{}) (bool, bool) {
	switch v := raw.(type) {
	case bool:
		return v, true
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "true", "yes", "1", "on":
			return true, true
		case "false", "no", "0", "off":
			return false, true
		}
	}
	return false, false
}

func lowerStringSet(values []string) map[string]struct{} {
	out := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value != "" {
			out[value] = struct{}{}
		}
	}
	return out
}
