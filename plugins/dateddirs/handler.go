package dateddirs

import (
	"log"
	"strings"
	"sync"
	"time"

	"goftpd/internal/plugin"
)

type Handler struct {
	svc                  *plugin.Services
	enabled              bool
	sections             []string
	format               string
	todaySymlink         bool
	symlinkPrefix        string
	readOnlyAfterMinutes int
	debug                bool

	mu        sync.Mutex
	lastDay   string
	stopCh    chan struct{}
	stopOnce  sync.Once
	startOnce sync.Once
}

func New() *Handler {
	return &Handler{
		stopCh: make(chan struct{}),
	}
}

func (h *Handler) Name() string { return "dateddirs" }

func (h *Handler) Init(svc *plugin.Services, cfg map[string]interface{}) error {
	h.svc = svc
	h.enabled = boolConfig(cfg, "enabled", false)
	h.sections = stringSliceConfig(cfg, "sections")
	h.format = normalizeFormat(stringConfig(cfg, "format", "0102"))
	h.todaySymlink = boolConfig(cfg, "today_symlink", true)
	h.symlinkPrefix = stringConfig(cfg, "symlink_prefix", "!Today_")
	h.readOnlyAfterMinutes = intConfig(cfg, "readonly_after_minutes", 60)
	h.debug = boolConfig(cfg, "debug", svc != nil && svc.Debug)
	if h.readOnlyAfterMinutes <= 0 {
		h.readOnlyAfterMinutes = 60
	}
	if h.enabled {
		h.startOnce.Do(func() { go h.loop() })
	}
	if h.debug {
		log.Printf("[DATEDDIRS] initialized enabled=%v sections=%v", h.enabled, h.sections)
	}
	return nil
}

func (h *Handler) OnEvent(evt *plugin.Event) error { return nil }

func (h *Handler) Stop() error {
	h.stopOnce.Do(func() { close(h.stopCh) })
	return nil
}

func (h *Handler) loop() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	h.apply(time.Now())
	for {
		select {
		case <-h.stopCh:
			return
		case now := <-ticker.C:
			h.apply(now)
		}
	}
}

func (h *Handler) apply(now time.Time) {
	if h.svc == nil || h.svc.Bridge == nil || !h.enabled {
		return
	}
	today := now.Format(h.format)
	yesterday := now.AddDate(0, 0, -1).Format(h.format)

	h.mu.Lock()
	lastDay := h.lastDay
	announce := lastDay != "" && lastDay != today
	h.mu.Unlock()

	for _, section := range h.sections {
		section = strings.Trim(strings.TrimSpace(section), "/")
		if section == "" {
			continue
		}

		todayPath := "/" + section + "/" + today
		h.svc.Bridge.MakeDir(todayPath, "GoFTPd", "GoFTPd")

		linkPath := ""
		if h.todaySymlink {
			linkPath = "/" + h.symlinkPrefix + section
			if err := h.svc.Bridge.Symlink(linkPath, todayPath); err != nil && h.debug {
				log.Printf("[DATEDDIRS] symlink %s -> %s failed: %v", linkPath, todayPath, err)
			}
		}

		if announce && h.svc.EmitEvent != nil {
			h.svc.EmitEvent("NEWDAY", todayPath, today, section, 0, 0, map[string]string{
				"date":     today,
				"dirpath":  todayPath,
				"linkpath": linkPath,
				"symlink":  boolString(h.todaySymlink),
			})
		}

		if minutesSinceMidnight(now) >= h.readOnlyAfterMinutes {
			_ = h.svc.Bridge.Chmod("/"+section+"/"+yesterday, 0555)
		}
	}

	h.mu.Lock()
	h.lastDay = today
	h.mu.Unlock()
}

func normalizeFormat(format string) string {
	if strings.TrimSpace(format) == "" {
		return "0102"
	}
	return strings.NewReplacer("%Y", "2006", "%y", "06", "%m", "01", "%d", "02").Replace(format)
}

func minutesSinceMidnight(t time.Time) int {
	return t.Hour()*60 + t.Minute()
}

func boolString(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

func stringConfig(cfg map[string]interface{}, key, fallback string) string {
	if raw, ok := cfg[key].(string); ok && strings.TrimSpace(raw) != "" {
		return strings.TrimSpace(raw)
	}
	return fallback
}

func boolConfig(cfg map[string]interface{}, key string, fallback bool) bool {
	if raw, ok := cfg[key].(bool); ok {
		return raw
	}
	return fallback
}

func intConfig(cfg map[string]interface{}, key string, fallback int) int {
	switch v := cfg[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	default:
		return fallback
	}
}

func stringSliceConfig(cfg map[string]interface{}, key string) []string {
	raw, ok := cfg[key]
	if !ok {
		return nil
	}
	switch v := raw.(type) {
	case []string:
		return append([]string(nil), v...)
	case []interface{}:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
		return out
	case string:
		parts := strings.Split(v, ",")
		out := make([]string, 0, len(parts))
		for _, part := range parts {
			if part = strings.TrimSpace(part); part != "" {
				out = append(out, part)
			}
		}
		return out
	default:
		return nil
	}
}
