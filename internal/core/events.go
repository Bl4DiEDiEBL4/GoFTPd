package core

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path"
	"strings"
	"sync"
	"syscall"
	"time"

	pluginpkg "goftpd/internal/plugin"
	"goftpd/internal/zipscript"
)

// EventType describes a daemon event published for external consumers such as the sitebot.
type EventType string

const (
	EventUpload        EventType = "UPLOAD"
	EventDownload      EventType = "DOWNLOAD"
	EventDelete        EventType = "DELETE"
	EventNuke          EventType = "NUKE"
	EventRaceEnd       EventType = "RACEEND"    // COMPLETE line only
	EventRaceStats     EventType = "RACESTATS"  // STATS_HOF + STATS_SPEEDS
	EventRaceUser      EventType = "RACEUSER"   // one per racer in HOF
	EventRaceFooter    EventType = "RACEFOOTER" // STATS_END line
	EventNewUser       EventType = "NEWUSER"
	EventMKDir         EventType = "MKDIR"
	EventRMDir         EventType = "RMDIR"
	EventRename        EventType = "RENAME"
	EventUnnuke        EventType = "UNNUKE"
	EventInvite        EventType = "INVITE"
	EventDiskStatus    EventType = "DISKSTATUS"
	EventSpeedtest     EventType = "SPEEDTEST"
	EventPre           EventType = "PRE"           // release pre'd — relname, section, group, files, mbytes
	EventPreBW         EventType = "PREBW"         // race-bw totals after pre
	EventPreBWUser     EventType = "PREBWUSER"     // per-user race-bw after pre
	EventPreBWInterval EventType = "PREBWINTERVAL" // interval snapshots after pre
)

// Event is the daemon-side event payload written to the event FIFO as JSON lines.
type Event struct {
	Type      EventType         `json:"type"`
	Timestamp time.Time         `json:"timestamp"`
	User      string            `json:"user,omitempty"`
	Group     string            `json:"group,omitempty"`
	Section   string            `json:"section,omitempty"`
	Filename  string            `json:"filename,omitempty"`
	Size      int64             `json:"size,omitempty"`
	Speed     float64           `json:"speed,omitempty"`
	Path      string            `json:"path,omitempty"`
	Data      map[string]string `json:"data,omitempty"`
}

// EventSink consumes events.
type EventSink interface {
	Publish(Event) error
	Close() error
}

var completedRaceMu sync.Mutex
var completedRaces = map[string]time.Time{}

// EventDispatcher fans out events to all registered sinks.
type EventDispatcher struct {
	sinks []EventSink
	debug bool
	mu    sync.RWMutex
}

// NewEventDispatcher creates an empty dispatcher.
func NewEventDispatcher(debug bool) *EventDispatcher {
	return &EventDispatcher{debug: debug, sinks: make([]EventSink, 0)}
}

// AddSink registers a sink.
func (d *EventDispatcher) AddSink(s EventSink) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.sinks = append(d.sinks, s)
}

// Emit publishes an event to all sinks.
func (d *EventDispatcher) Emit(evt Event) {
	d.mu.RLock()
	sinks := append([]EventSink(nil), d.sinks...)
	debug := d.debug
	d.mu.RUnlock()

	for _, sink := range sinks {
		if err := sink.Publish(evt); err != nil && debug {
			log.Printf("[EVENT] publish %s failed: %v", evt.Type, err)
		}
	}
}

// Close closes all sinks.
func (d *EventDispatcher) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, sink := range d.sinks {
		if err := sink.Close(); err != nil && d.debug {
			log.Printf("[EVENT] sink close failed: %v", err)
		}
	}
	d.sinks = nil
	return nil
}

// JSONLineFileSink writes each event as a JSON object on a single line.
// Writes happen through a buffered channel and background goroutine so that
// a slow consumer (e.g. the sitebot being flood-throttled by IRC) never
// blocks the FTP daemon's hot path.
type JSONLineFileSink struct {
	path    string
	file    *os.File
	mu      sync.Mutex
	queue   chan []byte
	started bool
}

// NewJSONLineFileSink creates a JSON-lines sink that can target a FIFO or a regular file.
func NewJSONLineFileSink(path string) (*JSONLineFileSink, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("empty event sink path")
	}
	return &JSONLineFileSink{
		path:  path,
		queue: make(chan []byte, 1024),
	}, nil
}

func (s *JSONLineFileSink) ensureWriter() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return
	}
	s.started = true
	go s.writer()
}

// writer is the background goroutine that drains the queue into the FIFO.
// If the sitebot isn't reading (FIFO buffer full), writes here block — but
// that only blocks this goroutine, not the FTP session calling Publish().
func (s *JSONLineFileSink) writer() {
	for line := range s.queue {
		// Keep trying to open the FIFO until a reader is present.
		// Once open, stays open across subsequent events.
		for {
			if err := s.ensureOpen(); err != nil {
				// No reader yet — wait briefly and retry. Events keep
				// queuing behind us; if the queue fills up, Publish() drops
				// newest so we don't grow unbounded.
				time.Sleep(500 * time.Millisecond)
				continue
			}
			break
		}
		if _, err := s.file.Write(line); err != nil {
			_ = s.file.Close()
			s.mu.Lock()
			s.file = nil
			s.mu.Unlock()
		}
	}
}

func (s *JSONLineFileSink) ensureOpen() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file != nil {
		return nil
	}
	// O_WRONLY|O_NONBLOCK: returns ENXIO immediately if no reader, instead of blocking.
	// Once opened we switch back to blocking mode so writes don't return EAGAIN.
	f, err := os.OpenFile(s.path, os.O_WRONLY|os.O_APPEND|syscall.O_NONBLOCK, 0644)
	if err != nil {
		return err
	}
	// Switch the fd to blocking mode so Write() waits for pipe space rather than EAGAIN.
	if err := syscall.SetNonblock(int(f.Fd()), false); err != nil {
		_ = f.Close()
		return err
	}
	s.file = f
	return nil
}

// Publish enqueues the event for the writer goroutine. If the queue is full
// (sitebot is way behind), the event is dropped rather than blocking the caller.
func (s *JSONLineFileSink) Publish(evt Event) error {
	s.ensureWriter()
	b, err := json.Marshal(evt)
	if err != nil {
		return err
	}
	line := append(b, '\n')
	select {
	case s.queue <- line:
		return nil
	default:
		// Queue full — sitebot is not keeping up. Drop silently rather than
		// stall the FTP session. Events are cosmetic (IRC announces).
		return nil
	}
}

// Close closes the underlying file handle.
func (s *JSONLineFileSink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file != nil {
		err := s.file.Close()
		s.file = nil
		return err
	}
	return nil
}

func getOrInitEventDispatcher(cfg *Config) *EventDispatcher {
	if cfg == nil {
		return nil
	}
	if cfg.EventDispatcher != nil {
		return cfg.EventDispatcher
	}
	d := NewEventDispatcher(cfg.Debug)
	if strings.TrimSpace(cfg.EventFIFO) != "" {
		sink, err := NewJSONLineFileSink(cfg.EventFIFO)
		if err != nil {
			if cfg.Debug {
				log.Printf("[EVENT] Failed to create sink %s: %v", cfg.EventFIFO, err)
			}
		} else {
			d.AddSink(sink)
		}
	}
	cfg.EventDispatcher = d
	return d
}

func PublishEvent(cfg *Config, evt Event) {
	d := getOrInitEventDispatcher(cfg)
	if d != nil {
		d.Emit(evt)
	}
}

func sectionFromPath(p string) string {
	cleaned := path.Clean("/" + strings.TrimSpace(p))
	if cleaned == "/" || cleaned == "." {
		return "DEFAULT"
	}
	parts := strings.Split(strings.TrimPrefix(cleaned, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		return "DEFAULT"
	}
	return strings.ToUpper(parts[0])
}

func fileNameFromPath(p string) string {
	base := path.Base(path.Clean(p))
	if base == "/" || base == "." {
		return ""
	}
	return base
}

func (s *Session) emitEvent(evtType EventType, eventPath, fileName string, size int64, speed float64, data map[string]string) {
	if s == nil || s.Config == nil {
		return
	}
	cleanedPath := path.Clean(eventPath)
	if s.eventPathIsPrivate(cleanedPath) {
		if s.Config.Debug {
			log.Printf("[EVENT] suppressed %s for private path %s", evtType, cleanedPath)
		}
		return
	}
	d := getOrInitEventDispatcher(s.Config)
	if d == nil {
		return
	}
	userName := ""
	groupName := ""
	if s.User != nil {
		userName = s.User.Name
		groupName = s.User.PrimaryGroup
	}
	if fileName == "" {
		fileName = fileNameFromPath(eventPath)
	}
	section := sectionFromPath(eventPath)

	evt := Event{
		Type:      evtType,
		Timestamp: time.Now(),
		User:      userName,
		Group:     groupName,
		Section:   section,
		Filename:  fileName,
		Size:      size,
		Speed:     speed,
		Path:      cleanedPath,
		Data:      data,
	}
	d.Emit(evt)

	// Also notify registered plugins. We translate the internal EventType
	// to the plugin.Event* constants (same string values by convention).
	if s.Config.PluginManager != nil {
		extra := make(map[string]interface{}, len(data))
		for k, v := range data {
			extra[k] = v
		}
		if s.Config.Debug {
			log.Printf("[PLUGIN-DISPATCH] %s path=%s filename=%s section=%s", evtType, cleanedPath, fileName, section)
		}
		s.Config.PluginManager.Dispatch(&pluginpkg.Event{
			Type:     string(evtType),
			User:     s.User,
			Path:     cleanedPath,
			Filename: fileName,
			Size:     size,
			Speed:    speed,
			Section:  section,
			Extra:    extra,
		})
	} else if s.Config.Debug {
		log.Printf("[PLUGIN-DISPATCH] %s — PluginManager is nil, event not dispatched", evtType)
	}
}

// emitRaceEnd fires a sequence of discrete events for the race-end sequence —
// one for the COMPLETE line, one for the HOF header+speeds, one per user in
// the Hall of Fame, and one for the footer. Each event is a separate FIFO
// write and a separate IRC PRIVMSG, matching pzs-ng behavior.
//
// The xferMs argument is the duration of the LAST file only (the one that
// completed the race) and is ignored for aggregate speed calculation — we
// use max(user.DurationMs) as the wall-clock span instead, which is the
// effective critical-path time across all racers.
func emitRaceEnd(s *Session, users []VFSRaceUser, totalBytes int64, total int, xferMs int64) {
	if s == nil {
		return
	}
	users = trimRaceUsers(s.Config, users)
	if !markRaceCompleteOnce(s.CurrentDir, totalBytes, total) {
		if s.Config != nil && s.Config.Debug {
			log.Printf("[RACE] duplicate complete suppressed for %s", s.CurrentDir)
		}
		return
	}

	// Prefer wall-clock race duration from the race DB (first file start to
	// last file end). Summing per-file durations (u.DurationMs) overcounts
	// heavily when uploads run in parallel — a 52s race with 8 parallel
	// threads can easily sum to 400s+ of "transfer time". pzs-ng uses
	// wall-clock for STATS_SPEED totals too.
	var raceDurationMs int64
	if s.Config.Mode == "master" && s.MasterManager != nil {
		if bridge, ok := s.MasterManager.(MasterBridge); ok {
			if ms := bridge.GetRaceWallClockMilliseconds(s.CurrentDir); ms > 0 {
				raceDurationMs = ms
			}
		}
	}

	// Fallback: longest per-user active transfer time. Good for sequential
	// uploaders, overcounts for parallel — but better than last-file xferMs.
	if raceDurationMs == 0 {
		for _, u := range users {
			if u.DurationMs > raceDurationMs {
				raceDurationMs = u.DurationMs
			}
		}
	}
	if raceDurationMs == 0 {
		raceDurationMs = xferMs // last-ditch fallback
	}
	if raceDurationMs < 1 {
		raceDurationMs = 1
	}

	durSec := float64(raceDurationMs) / 1000.0
	avgMB := 0.0
	if durSec > 0 {
		avgMB = (float64(totalBytes) / 1024.0 / 1024.0) / durSec
	}
	rel := path.Base(s.CurrentDir)
	common := map[string]string{
		"relname":    rel,
		"t_files":    fmt.Sprintf("%dF", total),
		"t_mbytes":   fmt.Sprintf("%.0fMB", float64(totalBytes)/1024.0/1024.0),
		"t_duration": formatRaceDuration(raceDurationMs),
		"t_avgspeed": fmt.Sprintf("%.2fMB/s", avgMB),
		"u_count":    fmt.Sprintf("%d", len(users)),
	}
	if subdir := zipscript.ReleaseSubdirLabel(s.Config.Zipscript, s.CurrentDir); subdir != "" {
		common["release_subdir"] = subdir
		common["release_name"] = path.Base(path.Dir(s.CurrentDir))
		if !zipscript.AnnounceReleaseSubdirs(s.Config.Zipscript) {
			common["skip_release_announce"] = "true"
		}
	}

	// COMPLETE line
	s.emitEvent(EventRaceEnd, s.CurrentDir, rel, totalBytes, avgMB, copyMap(common))

	if len(users) == 0 {
		return
	}

	// Figure out slowest/fastest based on each user's peak single-file speed
	slowest, fastest := users[0], users[0]
	for _, u := range users {
		if userSlowSpeed(u) < userSlowSpeed(slowest) {
			slowest = u
		}
		if u.PeakSpeed > fastest.PeakSpeed {
			fastest = u
		}
	}

	// HOF header + speeds line (RACESTATS)
	statsData := copyMap(common)
	statsData["u_slowest_name"] = slowest.Name
	statsData["u_slowest_speed"] = fmt.Sprintf("%.2fMB/s", userSlowSpeed(slowest)/1024.0/1024.0)
	statsData["u_fastest_name"] = fastest.Name
	statsData["u_fastest_speed"] = fmt.Sprintf("%.2fMB/s", fastest.PeakSpeed/1024.0/1024.0)
	s.emitEvent(EventRaceStats, s.CurrentDir, rel, totalBytes, avgMB, statsData)

	// One event per racer in HOF
	for i, u := range users {
		uData := copyMap(common)
		uData["u_rank"] = fmt.Sprintf("%d", i+1)
		uData["u_racer_name"] = u.Name
		uData["u_racer_group"] = u.Group
		uData["u_racer_files"] = fmt.Sprintf("%d", u.Files)
		uData["u_racer_mb"] = fmt.Sprintf("%.1f", float64(u.Bytes)/1024.0/1024.0)
		uData["u_racer_pct"] = fmt.Sprintf("%d", u.Percent)
		uData["u_racer_speed"] = fmt.Sprintf("%.2fMB/s", u.Speed/1024.0/1024.0)
		s.emitEvent(EventRaceUser, s.CurrentDir, rel, u.Bytes, u.Speed/1024.0/1024.0, uData)
	}

	// Footer
	s.emitEvent(EventRaceFooter, s.CurrentDir, rel, totalBytes, avgMB, copyMap(common))

	section, sectionRoot := zipscript.SectionInfoFromPath(s.CurrentDir)
	zipscript.RunOnCompleteHook(s.Config.Zipscript, zipscript.CompleteHookContext{
		DirPath:       s.CurrentDir,
		RelName:       rel,
		ReleaseName:   common["release_name"],
		ReleaseSubdir: common["release_subdir"],
		Section:       section,
		SectionRoot:   sectionRoot,
		TotalBytes:    totalBytes,
		TotalFiles:    total,
		DurationMs:    raceDurationMs,
		Duration:      common["t_duration"],
		AvgSpeedMB:    avgMB,
		UserCount:     len(users),
		Data:          copyMap(common),
	})
}

func (s *Session) eventPathIsPrivate(eventPath string) bool {
	if s == nil || s.ACLEngine == nil || s.Config == nil || strings.TrimSpace(eventPath) == "" || eventPath == "." || eventPath == "/" {
		return false
	}
	aclBase := strings.TrimSpace(s.Config.ACLBasePath)
	if aclBase == "" {
		return false
	}
	aclPath := path.Join(aclBase, eventPath)
	return s.ACLEngine.MatchesRulePath("privpath", aclPath)
}

func formatRaceDuration(ms int64) string {
	if ms < 1 {
		ms = 1
	}
	if ms < 1000 {
		return fmt.Sprintf("%.4fs", float64(ms)/1000.0)
	}
	if ms%1000 != 0 {
		return fmt.Sprintf("%.4fs", float64(ms)/1000.0)
	}
	return fmt.Sprintf("%ds", ms/1000)
}

func userSlowSpeed(u VFSRaceUser) float64 {
	if u.SlowSpeed > 0 {
		return u.SlowSpeed
	}
	return u.PeakSpeed
}

func markRaceCompleteOnce(dirPath string, totalBytes int64, total int) bool {
	key := fmt.Sprintf("%s|%d|%d", path.Clean(dirPath), totalBytes, total)
	now := time.Now()

	completedRaceMu.Lock()
	defer completedRaceMu.Unlock()
	for k, seenAt := range completedRaces {
		if now.Sub(seenAt) > 24*time.Hour {
			delete(completedRaces, k)
		}
	}
	if _, ok := completedRaces[key]; ok {
		return false
	}
	completedRaces[key] = now
	return true
}

func copyMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
