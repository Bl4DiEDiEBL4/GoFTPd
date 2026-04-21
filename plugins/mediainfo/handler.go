package mediainfo

import (
	"log"
	"path"
	"strings"
	"sync"

	"goftpd/internal/plugin"
)

type Handler struct {
	svc             *plugin.Services
	enabled         bool
	debug           bool
	binary          string
	timeoutSeconds  int
	sections        []string
	audioExtensions map[string]bool
	videoExtensions map[string]bool
	sampleOnly      bool
	jobs            chan job
	stopCh          chan struct{}
	stopOnce        sync.Once
}

type job struct {
	eventType string
	filePath  string
	fileName  string
	relPath   string
	relName   string
	section   string
	size      int64
	speed     float64
}

func New() *Handler {
	return &Handler{
		jobs:   make(chan job, 128),
		stopCh: make(chan struct{}),
	}
}

func (h *Handler) Name() string { return "mediainfo" }

func (h *Handler) Init(svc *plugin.Services, cfg map[string]interface{}) error {
	h.svc = svc
	h.enabled = boolConfig(cfg, "enabled", false)
	h.debug = boolConfig(cfg, "debug", svc != nil && svc.Debug)
	h.binary = stringConfig(cfg, "binary", "mediainfo")
	h.timeoutSeconds = intConfig(cfg, "timeout_seconds", 20)
	h.sections = stringSliceConfig(cfg, "sections")
	h.sampleOnly = boolConfig(cfg, "sample_only", true)
	h.audioExtensions = extensionSet(stringSliceConfigDefault(cfg, "audio_extensions", []string{"flac", "mp3", "m4a", "wav"}))
	h.videoExtensions = extensionSet(stringSliceConfigDefault(cfg, "video_extensions", []string{"mkv", "mp4", "avi", "m2ts"}))
	if h.timeoutSeconds <= 0 {
		h.timeoutSeconds = 20
	}
	if h.enabled {
		go h.worker()
	}
	if h.debug {
		log.Printf("[MEDIAINFO] initialized enabled=%v sections=%v", h.enabled, h.sections)
	}
	return nil
}

func (h *Handler) OnEvent(evt *plugin.Event) error {
	if !h.enabled || evt == nil || evt.Type != plugin.EventUpload {
		return nil
	}
	if h.svc == nil || h.svc.Bridge == nil || h.svc.EmitEvent == nil {
		return nil
	}
	if len(h.sections) > 0 && !matchSection(evt.Section, h.sections) {
		return nil
	}

	ext := strings.TrimPrefix(strings.ToLower(path.Ext(evt.Filename)), ".")
	eventType := ""
	if h.audioExtensions[ext] {
		eventType = "AUDIOINFO"
	} else if h.videoExtensions[ext] {
		if h.sampleOnly && !isSamplePath(evt.Path) {
			return nil
		}
		eventType = "MEDIAINFO"
	}
	if eventType == "" {
		return nil
	}

	relPath := releasePath(evt.Path)
	j := job{
		eventType: eventType,
		filePath:  evt.Path,
		fileName:  evt.Filename,
		relPath:   relPath,
		relName:   path.Base(relPath),
		section:   evt.Section,
		size:      evt.Size,
		speed:     evt.Speed,
	}
	select {
	case h.jobs <- j:
	default:
		log.Printf("[MEDIAINFO] job queue full, dropping %s", evt.Path)
	}
	return nil
}

func (h *Handler) Stop() error {
	h.stopOnce.Do(func() { close(h.stopCh) })
	return nil
}

func (h *Handler) worker() {
	for {
		select {
		case <-h.stopCh:
			return
		case j := <-h.jobs:
			h.probe(j)
		}
	}
}

func (h *Handler) probe(j job) {
	fields, err := h.svc.Bridge.ProbeMediaInfo(j.filePath, h.binary, h.timeoutSeconds)
	if err != nil {
		if h.debug {
			log.Printf("[MEDIAINFO] %s failed: %v", j.filePath, err)
		}
		return
	}
	fields["filename"] = j.fileName
	fields["filepath"] = j.filePath
	fields["path"] = j.relPath
	fields["relname"] = j.relName
	fields["section"] = j.section
	h.svc.EmitEvent(j.eventType, j.relPath, j.relName, j.section, j.size, j.speed, fields)
}

func releasePath(filePath string) string {
	clean := path.Clean(filePath)
	dir := path.Dir(clean)
	if strings.EqualFold(path.Base(dir), "sample") || strings.EqualFold(path.Base(dir), "samples") {
		return path.Dir(dir)
	}
	return dir
}

func isSamplePath(filePath string) bool {
	lower := strings.ToLower(filePath)
	return strings.Contains(lower, "/sample/") || strings.Contains(lower, "/samples/") || strings.Contains(lower, ".sample.")
}

func matchSection(section string, patterns []string) bool {
	section = strings.ToLower(strings.TrimSpace(section))
	for _, pat := range patterns {
		pat = strings.ToLower(strings.TrimSpace(pat))
		if pat != "" && strings.Contains(section, pat) {
			return true
		}
	}
	return false
}

func extensionSet(exts []string) map[string]bool {
	out := make(map[string]bool, len(exts))
	for _, ext := range exts {
		ext = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(ext)), ".")
		if ext != "" {
			out[ext] = true
		}
	}
	return out
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

func stringSliceConfigDefault(cfg map[string]interface{}, key string, fallback []string) []string {
	out := stringSliceConfig(cfg, key)
	if len(out) == 0 {
		return fallback
	}
	return out
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
