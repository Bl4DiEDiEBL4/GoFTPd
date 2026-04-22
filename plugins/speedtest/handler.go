package speedtest

import (
	"fmt"
	"log"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"goftpd/internal/plugin"
)

type Handler struct {
	svc     *plugin.Services
	enabled bool
	dir     string
	files   []int
	debug   bool
	once    sync.Once
	stopCh  chan struct{}
}

func New() *Handler {
	return &Handler{
		dir:   "/SPEEDTEST",
		files: []int{100, 500, 1000},
		stopCh: make(chan struct{}),
	}
}

func (h *Handler) Name() string { return "speedtest" }

func (h *Handler) Init(svc *plugin.Services, cfg map[string]interface{}) error {
	h.svc = svc
	h.enabled = boolConfig(cfg, "enabled", false)
	h.dir = normalizeDir(stringConfig(cfg, "dir", "/SPEEDTEST"))
	if files := intSliceConfig(cfg, "files_mb"); len(files) > 0 {
		h.files = files
	}
	h.debug = boolConfig(cfg, "debug", svc != nil && svc.Debug)

	if h.enabled {
		h.once.Do(func() {
			go h.ensureFiles()
		})
	}
	if h.debug {
		log.Printf("[SPEEDTEST] initialized enabled=%v dir=%s files=%vMB", h.enabled, h.dir, h.files)
	}
	return nil
}

func (h *Handler) OnEvent(evt *plugin.Event) error {
	if !h.enabled || evt == nil || (evt.Type != plugin.EventUpload && evt.Type != plugin.EventDownload) {
		return nil
	}
	if !h.inSpeedtestDir(evt.Path) {
		return nil
	}
	user := "unknown"
	if evt.User != nil && strings.TrimSpace(evt.User.Name) != "" {
		user = evt.User.Name
	}
	action := "downloaded"
	if evt.Type == plugin.EventUpload {
		action = "uploaded"
	}
	sizeMB := int64(evt.Size / 1024 / 1024)
	if sizeMB <= 0 {
		sizeMB = int64(parseLeadingMB(evt.Filename))
	}
	speed := fmt.Sprintf("%.2fMB/s", evt.Speed)

	if h.svc != nil && h.svc.EmitEvent != nil {
		h.svc.EmitEvent("SPEEDTEST", evt.Path, evt.Filename, "SPEEDTEST", evt.Size, evt.Speed, map[string]string{
			"nick":      user,
			"action":    action,
			"size_mb":   fmt.Sprintf("%d", sizeMB),
			"speed_mbs": speed,
		})
	}
	return nil
}

func (h *Handler) Stop() error {
	select {
	case <-h.stopCh:
	default:
		close(h.stopCh)
	}
	return nil
}

func (h *Handler) ensureFiles() {
	if h.svc == nil || h.svc.Bridge == nil {
		if h.debug {
			log.Printf("[SPEEDTEST] bridge unavailable, skipping file creation")
		}
		return
	}
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for attempt := 0; attempt < 60; attempt++ {
		if h.createMissingFiles() {
			return
		}
		select {
		case <-h.stopCh:
			return
		case <-ticker.C:
		}
	}
}

func (h *Handler) createMissingFiles() bool {
	h.svc.Bridge.MakeDir(h.dir, "GoFTPd", "GoFTPd")
	complete := true
	for _, mb := range h.files {
		if mb <= 0 {
			continue
		}
		filePath := path.Join(h.dir, fmt.Sprintf("%dMB.dat", mb))
		size := int64(mb) * 1024 * 1024
		if h.svc.Bridge.GetFileSize(filePath) == size {
			continue
		}
		complete = false
		if err := h.svc.Bridge.CreateSparseFile(filePath, size, "GoFTPd", "GoFTPd"); err != nil {
			if h.debug {
				log.Printf("[SPEEDTEST] create %s failed: %v", filePath, err)
			}
			continue
		}
		if h.debug {
			log.Printf("[SPEEDTEST] created %s", filePath)
		}
	}
	return complete
}

func (h *Handler) inSpeedtestDir(filePath string) bool {
	clean := strings.ToLower(path.Clean("/" + strings.TrimSpace(filePath)))
	dir := strings.ToLower(path.Clean(h.dir))
	return clean == dir || strings.HasPrefix(clean, dir+"/")
}

func normalizeDir(dir string) string {
	dir = strings.TrimSpace(strings.ReplaceAll(dir, "\\", "/"))
	if dir == "" {
		return "/SPEEDTEST"
	}
	if !strings.HasPrefix(dir, "/") {
		dir = "/" + dir
	}
	return path.Clean(dir)
}

func parseLeadingMB(name string) int {
	name = strings.TrimSpace(strings.ToLower(name))
	name = strings.TrimSuffix(name, ".dat")
	name = strings.TrimSuffix(name, "mb")
	n, _ := strconv.Atoi(name)
	return n
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

func intSliceConfig(cfg map[string]interface{}, key string) []int {
	raw, ok := cfg[key]
	if !ok {
		return nil
	}
	switch v := raw.(type) {
	case []int:
		return append([]int(nil), v...)
	case []interface{}:
		out := make([]int, 0, len(v))
		for _, item := range v {
			switch n := item.(type) {
			case int:
				out = append(out, n)
			case int64:
				out = append(out, int(n))
			case float64:
				out = append(out, int(n))
			case string:
				if parsed, err := strconv.Atoi(strings.TrimSpace(n)); err == nil {
					out = append(out, parsed)
				}
			}
		}
		return out
	case string:
		parts := strings.Split(v, ",")
		out := make([]int, 0, len(parts))
		for _, part := range parts {
			if n, err := strconv.Atoi(strings.TrimSpace(part)); err == nil {
				out = append(out, n)
			}
		}
		return out
	default:
		return nil
	}
}
