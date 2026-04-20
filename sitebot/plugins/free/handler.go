package free

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"goftpd/sitebot/internal/event"
	"goftpd/sitebot/internal/plugin"
)

type Plugin struct {
	mu          sync.RWMutex
	slaves      map[string]diskStatus
	replyTarget string
	staleAfter  time.Duration
}

type diskStatus struct {
	Name      string
	Free      int64
	Total     int64
	Online    bool
	Available bool
	Sections  []string
	Updated   time.Time
}

func New() *Plugin {
	return &Plugin{
		slaves:      map[string]diskStatus{},
		replyTarget: "channel",
		staleAfter:  10 * time.Minute,
	}
}

func (p *Plugin) Name() string { return "Free" }

func (p *Plugin) Initialize(config map[string]interface{}) error {
	cfg := plugin.ConfigSection(config, "free")
	if s, ok := stringConfig(cfg, config, "reply_target", "free_reply_target"); ok && strings.TrimSpace(s) != "" {
		p.replyTarget = strings.ToLower(strings.TrimSpace(s))
	}
	if n := intConfig(configValue(cfg, config, "stale_after_seconds", "free_stale_after_seconds"), 0); n > 0 {
		p.staleAfter = time.Duration(n) * time.Second
	}
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

	p.mu.Lock()
	p.slaves[name] = diskStatus{
		Name:      name,
		Free:      freeBytes,
		Total:     totalBytes,
		Online:    online,
		Available: available,
		Sections:  sections,
		Updated:   time.Now(),
	}
	p.mu.Unlock()
}

func (p *Plugin) show(evt *event.Event) []plugin.Output {
	p.mu.RLock()
	statuses := make([]diskStatus, 0, len(p.slaves))
	for _, st := range p.slaves {
		statuses = append(statuses, st)
	}
	p.mu.RUnlock()

	if len(statuses) == 0 {
		return p.reply(evt, "DF: No slave disk status received yet.")
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
		lines = append(lines, fmt.Sprintf("DF: %-12s %8s free / %8s total (%5.1f%% free) [%s]",
			st.Name, humanBytes(st.Free), humanBytes(st.Total), percentFree(st.Free, st.Total), state))
	}
	if len(lines) == 0 {
		return p.reply(evt, fmt.Sprintf("DF: No slave matched %q.", filter))
	}
	if filter == "" && len(lines) > 1 {
		lines = append(lines, fmt.Sprintf("DF: %-12s %8s free / %8s total (%5.1f%% free)",
			"TOTAL", humanBytes(totalFree), humanBytes(totalCap), percentFree(totalFree, totalCap)))
	}
	return p.replies(evt, lines...)
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
