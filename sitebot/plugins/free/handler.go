package free

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"weaveftpd/sitebot/internal/event"
	"weaveftpd/sitebot/internal/plugin"
	tmpl "weaveftpd/sitebot/internal/template"
)

type Plugin struct {
	mu              sync.RWMutex
	slaves          map[string]diskStatus
	groups          []namedGroup
	replyTarget     string
	staleAfter      time.Duration
	showSlaveLines  bool
	showNamedGroups bool
	showTotal       bool
	theme           *tmpl.Theme
}

type diskRootStatus struct {
	Path      string
	MountPath string
	Free      int64
	Total     int64
}

type diskStatus struct {
	Name      string
	Free      int64
	Total     int64
	Online    bool
	Available bool
	Sections  []string
	Roots     []diskRootStatus
	Updated   time.Time
}

type namedGroup struct {
	Name       string
	MountPaths []string
	Paths      []string
}

var rememberedStatusStore = struct {
	sync.RWMutex
	slaves map[string]diskStatus
}{
	slaves: map[string]diskStatus{},
}

func New() *Plugin {
	return &Plugin{
		slaves:          rememberedDiskStatuses(),
		replyTarget:     "channel",
		staleAfter:      10 * time.Minute,
		showSlaveLines:  true,
		showNamedGroups: true,
		showTotal:       true,
	}
}

func (p *Plugin) Name() string { return "Free" }

func (p *Plugin) Initialize(config map[string]interface{}) error {
	if themeFile, ok := config["theme_file"].(string); ok && strings.TrimSpace(themeFile) != "" {
		th, err := tmpl.LoadTheme(themeFile)
		if err == nil {
			p.theme = th
		}
	}

	cfg := plugin.ConfigSection(config, "free")
	if s, ok := stringConfig(cfg, config, "reply_target", "free_reply_target"); ok && strings.TrimSpace(s) != "" {
		p.replyTarget = strings.ToLower(strings.TrimSpace(s))
	}
	if n := intConfig(configValue(cfg, config, "stale_after_seconds", "free_stale_after_seconds"), 0); n > 0 {
		p.staleAfter = time.Duration(n) * time.Second
	}
	if b, ok := boolConfig(configValue(cfg, config, "show_slave_lines", "free_show_slave_lines")); ok {
		p.showSlaveLines = b
	}
	if b, ok := boolConfig(configValue(cfg, config, "show_named_groups", "free_show_named_groups")); ok {
		p.showNamedGroups = b
	}
	if b, ok := boolConfig(configValue(cfg, config, "show_total", "free_show_total")); ok {
		p.showTotal = b
	}
	p.groups = parseNamedGroups(configValue(cfg, config, "named_groups", "free_named_groups"))
	return nil
}

func (p *Plugin) Close() error { return nil }

func (p *Plugin) OnEvent(evt *event.Event) ([]plugin.Output, error) {
	switch evt.Type {
	case event.EventDiskStatus:
		p.update(evt)
	case event.EventCommand:
		cmd := strings.ToLower(strings.TrimSpace(evt.Data["command"]))
		if cmd == "df" || cmd == "free" {
			return p.show(evt), nil
		}
	}
	return nil, nil
}

func (p *Plugin) update(evt *event.Event) {
	name := strings.TrimSpace(evt.Data["slave"])
	if name == "" {
		return
	}
	freeBytes, _ := strconv.ParseInt(evt.Data["free_bytes"], 10, 64)
	totalBytes, _ := strconv.ParseInt(evt.Data["total_bytes"], 10, 64)
	online, _ := strconv.ParseBool(evt.Data["online"])
	available, _ := strconv.ParseBool(evt.Data["available"])
	sections := splitCSV(evt.Data["sections"])
	if len(sections) == 0 {
		sections = []string{"*"}
	}
	roots := parseRootStatuses(evt.Data["roots_json"])
	status := diskStatus{
		Name:      name,
		Free:      freeBytes,
		Total:     totalBytes,
		Online:    online,
		Available: available,
		Sections:  sections,
		Roots:     roots,
		Updated:   time.Now(),
	}

	p.mu.Lock()
	p.slaves[name] = cloneDiskStatus(status)
	p.mu.Unlock()
	rememberDiskStatus(status)
}

func (p *Plugin) show(evt *event.Event) []plugin.Output {
	p.mu.RLock()
	statuses := make([]diskStatus, 0, len(p.slaves))
	for _, st := range p.slaves {
		statuses = append(statuses, st)
	}
	p.mu.RUnlock()

	if len(statuses) == 0 {
		return p.reply(evt, p.render("DF_EMPTY", nil, "DF: No slave disk status received yet."))
	}
	sort.Slice(statuses, func(i, j int) bool {
		return strings.ToLower(statuses[i].Name) < strings.ToLower(statuses[j].Name)
	})

	filter := strings.ToLower(strings.TrimSpace(evt.Data["args"]))
	lines := []string{}
	var totalFree, totalCap int64
	now := time.Now()
	for _, st := range statuses {
		if filter != "" && !st.matches(filter) {
			continue
		}
		totalFree += st.Free
		totalCap += st.Total
		state := "online"
		if !st.Online {
			state = "offline"
		} else if !st.Available {
			state = "remerging"
		}
		if p.staleAfter > 0 && now.Sub(st.Updated) > p.staleAfter {
			state += ", stale"
		}
		if p.showSlaveLines {
			vars := diskVars(st, state, now)
			lines = append(lines, p.render("DF_SLAVE", vars, fmt.Sprintf("DF: %-12s %8s free / %8s total (%5.1f%% free) [%s]",
				st.Name, humanBytes(st.Free), humanBytes(st.Total), percentFree(st.Free, st.Total), state)))
		}
	}
	if p.showNamedGroups {
		for _, group := range p.groups {
			freeBytes, totalBytes, ok := aggregateNamedGroup(statuses, group)
			if !ok {
				continue
			}
			if filter != "" && !matchesNamedGroup(group, filter) {
				continue
			}
			lines = append(lines, p.render("DF_GROUP", map[string]string{
				"name":        group.Name,
				"free":        humanBytes(freeBytes),
				"total":       humanBytes(totalBytes),
				"free_pct":    fmt.Sprintf("%.1f", percentFree(freeBytes, totalBytes)),
				"used_pct":    fmt.Sprintf("%.1f", 100-percentFree(freeBytes, totalBytes)),
				"free_bytes":  strconv.FormatInt(freeBytes, 10),
				"total_bytes": strconv.FormatInt(totalBytes, 10),
				"mount_paths": strings.Join(group.MountPaths, ","),
				"paths":       strings.Join(group.Paths, ","),
			}, fmt.Sprintf("DF: %-12s %8s free / %8s total (%5.1f%% free)", group.Name, humanBytes(freeBytes), humanBytes(totalBytes), percentFree(freeBytes, totalBytes))))
		}
	}
	if p.showTotal && filter == "" && totalCap > 0 && (len(lines) > 1 || (!p.showSlaveLines && !p.showNamedGroups)) {
		vars := map[string]string{
			"slave":       "TOTAL",
			"name":        "TOTAL",
			"free":        humanBytes(totalFree),
			"total":       humanBytes(totalCap),
			"free_pct":    fmt.Sprintf("%.1f", percentFree(totalFree, totalCap)),
			"used_pct":    fmt.Sprintf("%.1f", 100-percentFree(totalFree, totalCap)),
			"free_bytes":  strconv.FormatInt(totalFree, 10),
			"total_bytes": strconv.FormatInt(totalCap, 10),
			"count":       strconv.Itoa(len(lines)),
		}
		lines = append(lines, p.render("DF_TOTAL", vars, fmt.Sprintf("DF: %-12s %8s free / %8s total (%5.1f%% free)",
			"TOTAL", humanBytes(totalFree), humanBytes(totalCap), percentFree(totalFree, totalCap))))
	}
	if len(lines) == 0 {
		return p.reply(evt, p.render("DF_NOMATCH", map[string]string{"filter": filter}, fmt.Sprintf("DF: No slave matched %q.", filter)))
	}
	return p.replies(evt, lines...)
}

func (p *Plugin) render(key string, vars map[string]string, fallback string) string {
	if p.theme != nil {
		if raw, ok := p.theme.Announces[key]; ok && raw != "" {
			return tmpl.Render(raw, vars)
		}
	}
	return fallback
}

func diskVars(st diskStatus, state string, now time.Time) map[string]string {
	stateColored := state
	switch {
	case strings.HasPrefix(state, "online"):
		stateColored = "\x0303" + state + "\x03"
	case strings.HasPrefix(state, "offline"):
		stateColored = "\x0304" + state + "\x03"
	case strings.HasPrefix(state, "remerging"):
		stateColored = "\x0307" + state + "\x03"
	}
	return map[string]string{
		"slave":         st.Name,
		"name":          st.Name,
		"free":          humanBytes(st.Free),
		"total":         humanBytes(st.Total),
		"free_pct":      fmt.Sprintf("%.1f", percentFree(st.Free, st.Total)),
		"used_pct":      fmt.Sprintf("%.1f", 100-percentFree(st.Free, st.Total)),
		"state":         state,
		"state_colored": stateColored,
		"sections":      strings.Join(st.Sections, ","),
		"age":           formatAge(now.Sub(st.Updated)),
		"free_bytes":    strconv.FormatInt(st.Free, 10),
		"total_bytes":   strconv.FormatInt(st.Total, 10),
	}
}

func (st diskStatus) matches(filter string) bool {
	filter = strings.ToLower(strings.TrimSpace(filter))
	if filter == "" {
		return true
	}
	if strings.Contains(strings.ToLower(st.Name), filter) {
		return true
	}
	for _, section := range st.Sections {
		section = strings.ToLower(strings.TrimSpace(section))
		if section == "*" || strings.Contains(section, filter) {
			return true
		}
	}
	return false
}

func (p *Plugin) replies(evt *event.Event, lines ...string) []plugin.Output {
	target := strings.TrimSpace(evt.Data["channel"])
	noticeReply := false
	if p.replyTarget == "notice" || target == "" {
		target = evt.User
		noticeReply = true
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

func percentFree(free, total int64) float64 {
	if total <= 0 {
		return 0
	}
	return (float64(free) / float64(total)) * 100
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	units := []string{"KB", "MB", "GB", "TB", "PB"}
	f := float64(n)
	for _, suffix := range units {
		f /= unit
		if f < unit {
			return fmt.Sprintf("%.1f%s", f, suffix)
		}
	}
	return fmt.Sprintf("%.1fEB", f/unit)
}

func formatAge(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
}

func splitCSV(s string) []string {
	out := []string{}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func parseRootStatuses(raw string) []diskRootStatus {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var payload []map[string]string
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return nil
	}
	out := make([]diskRootStatus, 0, len(payload))
	for _, item := range payload {
		freeBytes, _ := strconv.ParseInt(strings.TrimSpace(item["free_bytes"]), 10, 64)
		totalBytes, _ := strconv.ParseInt(strings.TrimSpace(item["total_bytes"]), 10, 64)
		out = append(out, diskRootStatus{
			Path:      strings.TrimSpace(item["path"]),
			MountPath: cleanMountPath(item["mount_path"]),
			Free:      freeBytes,
			Total:     totalBytes,
		})
	}
	return out
}

func parseNamedGroups(raw interface{}) []namedGroup {
	items, ok := raw.([]interface{})
	if !ok {
		return nil
	}
	out := make([]namedGroup, 0, len(items))
	for _, item := range items {
		cfg, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		name, _ := cfg["name"].(string)
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		mountPathCfg := plugin.ToStringSlice(cfg["mount_paths"], nil)
		mountPaths := make([]string, 0, len(mountPathCfg))
		for _, p := range mountPathCfg {
			p = cleanMountPath(p)
			if p != "" {
				mountPaths = append(mountPaths, p)
			}
		}
		pathCfg := plugin.ToStringSlice(cfg["paths"], nil)
		paths := make([]string, 0, len(pathCfg))
		for _, p := range pathCfg {
			p = cleanRootPath(p)
			if p != "" {
				paths = append(paths, p)
			}
		}
		if len(mountPaths) == 0 && len(paths) == 0 {
			continue
		}
		out = append(out, namedGroup{Name: name, MountPaths: mountPaths, Paths: paths})
	}
	return out
}

func cleanMountPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	p = strings.ReplaceAll(p, "\\", "/")
	p = strings.TrimRight(p, "/")
	if p == "" {
		return "/"
	}
	return p
}

func cleanRootPath(p string) string {
	p = strings.TrimSpace(strings.ReplaceAll(p, "\\", "/"))
	if p == "" {
		return ""
	}
	if len(p) > 1 {
		p = strings.TrimRight(p, "/")
	}
	return p
}

func aggregateNamedGroup(statuses []diskStatus, group namedGroup) (int64, int64, bool) {
	var freeBytes, totalBytes int64
	var matched bool
	for _, st := range statuses {
		for _, root := range st.Roots {
			if rootMatchesGroup(root, group) {
				freeBytes += root.Free
				totalBytes += root.Total
				matched = true
			}
		}
	}
	return freeBytes, totalBytes, matched
}

func rootMatchesGroup(root diskRootStatus, group namedGroup) bool {
	rootMount := cleanMountPath(root.MountPath)
	rootPath := cleanRootPath(root.Path)
	for _, mountPath := range group.MountPaths {
		if rootMount == mountPath {
			return true
		}
	}
	for _, path := range group.Paths {
		if rootPath == path {
			return true
		}
	}
	return false
}

func matchesNamedGroup(group namedGroup, filter string) bool {
	filter = strings.ToLower(strings.TrimSpace(filter))
	if filter == "" {
		return true
	}
	if strings.Contains(strings.ToLower(group.Name), filter) {
		return true
	}
	for _, mountPath := range group.MountPaths {
		if strings.Contains(strings.ToLower(mountPath), filter) {
			return true
		}
	}
	for _, rootPath := range group.Paths {
		if strings.Contains(strings.ToLower(rootPath), filter) {
			return true
		}
	}
	return false
}

func rememberedDiskStatuses() map[string]diskStatus {
	rememberedStatusStore.RLock()
	defer rememberedStatusStore.RUnlock()
	out := make(map[string]diskStatus, len(rememberedStatusStore.slaves))
	for name, status := range rememberedStatusStore.slaves {
		out[name] = cloneDiskStatus(status)
	}
	return out
}

func rememberDiskStatus(status diskStatus) {
	rememberedStatusStore.Lock()
	defer rememberedStatusStore.Unlock()
	rememberedStatusStore.slaves[status.Name] = cloneDiskStatus(status)
}

func cloneDiskStatus(status diskStatus) diskStatus {
	status.Sections = append([]string(nil), status.Sections...)
	status.Roots = append([]diskRootStatus(nil), status.Roots...)
	return status
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
		case "1", "true", "yes", "on":
			return true, true
		case "0", "false", "no", "off":
			return false, true
		}
	}
	return false, false
}
