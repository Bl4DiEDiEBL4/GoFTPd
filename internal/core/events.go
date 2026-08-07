package core

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	pluginpkg "weaveftpd/internal/plugin"
	"weaveftpd/internal/user"
	"weaveftpd/internal/zipscript"
)

// EventType describes a daemon event published for external consumers such as the sitebot.
type EventType string

const (
	EventUpload           EventType = "UPLOAD"
	EventDownload         EventType = "DOWNLOAD"
	EventDelete           EventType = "DELETE"
	EventNuke             EventType = "NUKE"
	EventAutonukeWarn     EventType = "AUTONUKEWARN"
	EventAutonukeDelete   EventType = "AUTONUKEDELETE"
	EventRaceEnd          EventType = "RACEEND"    // COMPLETE line only
	EventRaceStats        EventType = "RACESTATS"  // STATS_HOF + STATS_SPEEDS
	EventRaceUser         EventType = "RACEUSER"   // one per racer in HOF
	EventRaceGroupHeader  EventType = "RACEGROUPH" // GroupTop header
	EventRaceGroup        EventType = "RACEGROUP"  // one per group in GroupTop
	EventRaceFooter       EventType = "RACEFOOTER" // STATS_END line
	EventNewUser          EventType = "NEWUSER"
	EventUserChange       EventType = "USERCHANGE"
	EventLoginFail        EventType = "LOGINFAIL"
	EventSelfIP           EventType = "SELFIP"
	EventSlaveAuthFail    EventType = "SLAVEAUTHFAIL"
	EventSlowUploadWarn   EventType = "SLOWUPLOADWARN"
	EventSlowUploadKick   EventType = "SLOWUPLOADKICK"
	EventSlowDownloadWarn EventType = "SLOWDOWNLOADWARN"
	EventSlowDownloadKick EventType = "SLOWDOWNLOADKICK"
	EventMKDir            EventType = "MKDIR"
	EventRMDir            EventType = "RMDIR"
	EventRename           EventType = "RENAME"
	EventUnnuke           EventType = "UNNUKE"
	EventInvite           EventType = "INVITE"
	EventDiskStatus       EventType = "DISKSTATUS"
	EventSpeedtest        EventType = "SPEEDTEST"
	EventAudioInfo        EventType = "AUDIOINFO"
	EventMediaInfo        EventType = "MEDIAINFO"
	EventPre              EventType = "PRE"           // release pre'd — relname, section, group, files, mbytes
	EventPreAudioInfo     EventType = "PREAUDIOINFO"  // extra music metadata line for PRE
	EventPreMovieInfo     EventType = "PREMOVIEINFO"  // extra movie metadata line for PRE
	EventPreTVInfo        EventType = "PRETVINFO"     // extra tv metadata line for PRE
	EventPreBW            EventType = "PREBW"         // race-bw totals after pre
	EventPreBWUser        EventType = "PREBWUSER"     // per-user race-bw after pre
	EventPreBWInterval    EventType = "PREBWINTERVAL" // interval snapshots after pre
	EventNewPreTime       EventType = "NEWPRETIME"
	EventOldPreTime       EventType = "OLDPRETIME"
	EventCustom           EventType = "CUSTOM"
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
	f, err := openEventSinkFile(s.path)
	if err != nil {
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
	cfg.rehashMu.Lock()
	defer cfg.rehashMu.Unlock()
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
	section, _ := zipscript.SectionInfoFromPath(p)
	return section
}

func sectionFromPathWithConfig(cfg *Config, p string) string {
	section := sectionFromPath(p)
	if cfg == nil {
		return section
	}
	first := strings.TrimSpace(strings.Split(strings.TrimPrefix(path.Clean("/"+p), "/"), "/")[0])
	if first == "" {
		return section
	}
	prefix := "!Today_"
	if cfg.Plugins != nil {
		if pluginCfg, ok := cfg.Plugins["dateddirs"]; ok {
			if raw, ok := pluginCfg["symlink_prefix"].(string); ok && strings.TrimSpace(raw) != "" {
				prefix = strings.TrimSpace(raw)
			}
		}
	}
	if strings.HasPrefix(strings.ToLower(first), strings.ToLower(prefix)) {
		trimmed := strings.TrimPrefix(first, prefix)
		if trimmed != "" {
			return strings.ToUpper(trimmed)
		}
	}
	return section
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
	section := sectionFromPathWithConfig(s.Config, eventPath)

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
func emitRaceEnd(s *Session, dirPath string, users []VFSRaceUser, groups []VFSRaceGroup, totalBytes int64, total int, raceDurationMs int64, xferMs int64) {
	if s == nil {
		return
	}
	dirPath = path.Clean("/" + strings.TrimSpace(dirPath))
	if dirPath == "/" || dirPath == "." {
		dirPath = s.CurrentDir
	}
	users = trimRaceUsers(s.Config, users)
	if !markRaceCompleteOnce(dirPath, totalBytes, total) {
		if s.Config != nil && s.Config.Debug {
			log.Printf("[RACE] duplicate complete suppressed for %s", dirPath)
		}
		return
	}

	// Prefer wall-clock race duration from the race DB (first file start to
	// last file end). Summing per-file durations (u.DurationMs) overcounts
	// heavily when uploads run in parallel — a 52s race with 8 parallel
	// threads can easily sum to 400s+ of "transfer time". pzs-ng uses
	// wall-clock for STATS_SPEED totals too.
	var hookRunner zipscript.CompleteHookRunner
	if s.Config.Mode == "master" && s.MasterManager != nil {
		if bridge, ok := s.MasterManager.(MasterBridge); ok {
			hookRunner = bridge
			if raceDurationMs <= 0 {
				if ms := bridge.GetRaceWallClockMilliseconds(dirPath); ms > 0 {
					raceDurationMs = ms
				}
			}
			if len(groups) == 0 {
				if _, raceGroups, _, _, _ := bridge.GetVFSRaceStats(dirPath); len(raceGroups) > 0 {
					groups = trimRaceGroups(s.Config, raceGroups)
				}
			} else {
				groups = trimRaceGroups(s.Config, groups)
			}
		}
	}

	// COMPLETE speed is the summed racer throughput (matches pzs-ng's high
	// headline number); fall back to wall-clock throughput only when live user
	// speeds are absent.
	raceDurationMs = chooseRaceDurationMs(raceDurationMs, users, xferMs)

	avgMB := aggregateRaceSpeedMB(users)
	if avgMB <= 0 {
		avgMB = raceSpeedMBForDuration(totalBytes, raceDurationMs)
	}
	rel := zipscript.ReleaseDisplayName(dirPath)
	hookReleaseName := ""
	hookReleaseSubdir := ""
	common := map[string]string{
		"relname":    rel,
		"t_files":    fmt.Sprintf("%dF", total),
		"t_mbytes":   fmt.Sprintf("%.0fMB", float64(totalBytes)/1024.0/1024.0),
		"t_duration": formatRaceDuration(raceDurationMs),
		"t_avgspeed": fmt.Sprintf("%.2fMB/s", avgMB),
		"u_count":    fmt.Sprintf("%d", len(users)),
		"g_count":    fmt.Sprintf("%d", len(groups)),
	}
	if subdir := zipscript.ReleaseSubdirLabel(s.Config.Zipscript, dirPath); subdir != "" {
		common["release_subdir"] = subdir
		common["release_name"] = path.Base(path.Dir(dirPath))
		hookReleaseName = common["release_name"]
		hookReleaseSubdir = subdir
		if zipscript.IsIgnoredReleaseSubdir(s.Config.Zipscript, dirPath) || !zipscript.AnnounceReleaseSubdirs(s.Config.Zipscript) {
			common["skip_release_announce"] = "true"
		}
	} else if zipscript.IsMultiDiscReleaseSubdir(dirPath) {
		hookReleaseName = path.Base(path.Dir(dirPath))
		hookReleaseSubdir = path.Base(dirPath)
	}

	// COMPLETE line
	s.emitEvent(EventRaceEnd, dirPath, rel, totalBytes, avgMB, copyMap(common))

	if len(users) == 0 {
		return
	}

	// Keep the HOF summary consistent with the per-user rows: use the same
	// aggregate uploader race speed for both the header and each user line.
	slowest, fastest := users[0], users[0]
	for _, u := range users {
		if userDisplaySpeed(u) < userDisplaySpeed(slowest) {
			slowest = u
		}
		if userDisplaySpeed(u) > userDisplaySpeed(fastest) {
			fastest = u
		}
	}

	// HOF header + speeds line (RACESTATS)
	statsData := copyMap(common)
	statsData["u_slowest_name"] = slowest.Name
	statsData["u_slowest_speed"] = fmt.Sprintf("%.2fMB/s", userDisplaySpeed(slowest)/1024.0/1024.0)
	statsData["u_fastest_name"] = fastest.Name
	statsData["u_fastest_speed"] = fmt.Sprintf("%.2fMB/s", userDisplaySpeed(fastest)/1024.0/1024.0)
	s.emitEvent(EventRaceStats, dirPath, rel, totalBytes, avgMB, statsData)

	// One event per racer in HOF
	for i, u := range users {
		uData := copyMap(common)
		uData["u_rank"] = fmt.Sprintf("%d", i+1)
		uData["u_racer_name"] = u.Name
		uData["u_racer_group"] = u.Group
		uData["u_racer_files"] = fmt.Sprintf("%d", u.Files)
		uData["u_racer_mb"] = fmt.Sprintf("%.1f", float64(u.Bytes)/1024.0/1024.0)
		uData["u_racer_pct"] = fmt.Sprintf("%d", u.Percent)
		uData["u_racer_speed"] = fmt.Sprintf("%.2fMB/s", userDisplaySpeed(u)/1024.0/1024.0)
		uData["u_alup"] = fmt.Sprintf("%d", userTransferStatsPlace("ALUP", u.Name, s.GroupMap))
		uData["u_wkup"] = fmt.Sprintf("%d", userTransferStatsPlace("WKUP", u.Name, s.GroupMap))
		uData["u_monthup"] = fmt.Sprintf("%d", userTransferStatsPlace("MONTHUP", u.Name, s.GroupMap))
		uData["u_dayup"] = fmt.Sprintf("%d", userTransferStatsPlace("DAYUP", u.Name, s.GroupMap))
		uData["u_aldn"] = fmt.Sprintf("%d", userTransferStatsPlace("ALDN", u.Name, s.GroupMap))
		uData["u_wkdn"] = fmt.Sprintf("%d", userTransferStatsPlace("WKDN", u.Name, s.GroupMap))
		uData["u_monthdn"] = fmt.Sprintf("%d", userTransferStatsPlace("MONTHDN", u.Name, s.GroupMap))
		uData["u_daydn"] = fmt.Sprintf("%d", userTransferStatsPlace("DAYDN", u.Name, s.GroupMap))
		s.emitEvent(EventRaceUser, dirPath, rel, u.Bytes, u.Speed/1024.0/1024.0, uData)
	}

	if len(groups) > 0 {
		s.emitEvent(EventRaceGroupHeader, dirPath, rel, totalBytes, avgMB, copyMap(common))
		for i, g := range groups {
			gData := copyMap(common)
			gData["g_rank"] = fmt.Sprintf("%d", i+1)
			gData["g_name"] = g.Name
			gData["g_files"] = fmt.Sprintf("%d", g.Files)
			gData["g_mb"] = fmt.Sprintf("%.1f", float64(g.Bytes)/1024.0/1024.0)
			gData["g_pct"] = fmt.Sprintf("%d", g.Percent)
			gData["g_speed"] = fmt.Sprintf("%.2fMB/s", g.Speed/1024.0/1024.0)
			s.emitEvent(EventRaceGroup, dirPath, rel, g.Bytes, g.Speed/1024.0/1024.0, gData)
		}
	}

	// Footer
	s.emitEvent(EventRaceFooter, dirPath, rel, totalBytes, avgMB, copyMap(common))

	section, sectionRoot := zipscript.SectionInfoFromPath(dirPath)
	zipscript.RunOnCompleteHook(s.Config.Zipscript, zipscript.CompleteHookContext{
		DirPath:       dirPath,
		RelName:       rel,
		ReleaseName:   hookReleaseName,
		ReleaseSubdir: hookReleaseSubdir,
		Section:       section,
		SectionRoot:   sectionRoot,
		TotalBytes:    totalBytes,
		TotalFiles:    total,
		DurationMs:    raceDurationMs,
		Duration:      common["t_duration"],
		AvgSpeedMB:    avgMB,
		UserCount:     len(users),
		Data:          copyMap(common),
	}, hookRunner)
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
	totalSeconds := ms / 1000
	if ms < 1000 {
		return fmt.Sprintf("%.4fs", float64(ms)/1000.0)
	}
	if totalSeconds < 60 && ms%1000 != 0 {
		return fmt.Sprintf("%.4fs", float64(ms)/1000.0)
	}
	if totalSeconds < 60 {
		return fmt.Sprintf("%ds", totalSeconds)
	}
	hours := totalSeconds / 3600
	minutes := (totalSeconds % 3600) / 60
	seconds := totalSeconds % 60
	if hours > 0 {
		if seconds == 0 {
			return fmt.Sprintf("%dh%dm", hours, minutes)
		}
		return fmt.Sprintf("%dh%dm%ds", hours, minutes, seconds)
	}
	if seconds == 0 {
		return fmt.Sprintf("%dm", minutes)
	}
	return fmt.Sprintf("%dm%ds", minutes, seconds)
}

func chooseRaceDurationMs(raceDurationMs int64, users []VFSRaceUser, xferMs int64) int64 {
	maxUserDurationMs := int64(0)
	for _, u := range users {
		if u.DurationMs > maxUserDurationMs {
			maxUserDurationMs = u.DurationMs
		}
	}

	if raceDurationMs == 0 {
		raceDurationMs = maxUserDurationMs
	}
	if raceDurationMs == 0 {
		raceDurationMs = xferMs
	}
	if raceDurationMs < 1 {
		raceDurationMs = 1
	}
	return raceDurationMs
}

func userDisplaySpeed(u VFSRaceUser) float64 {
	if u.Speed > 0 {
		return u.Speed
	}
	if u.PeakSpeed > 0 {
		return u.PeakSpeed
	}
	return u.SlowSpeed
}

func userTransferStatsPlace(command, username string, groupMap map[string]int) int {
	target, err := user.LoadUser(username, groupMap)
	if err != nil || target == nil {
		return 0
	}
	targetBytes := userTransferStatsBytes(command, target)
	place := 1
	names, err := listUserNames()
	if err != nil {
		return place
	}
	for _, name := range names {
		other, err := user.LoadUser(name, groupMap)
		if err != nil || other == nil {
			continue
		}
		if userTransferStatsBytes(command, other) > targetBytes {
			place++
		}
	}
	return place
}

func userTransferStatsBytes(command string, u *user.User) int64 {
	if u == nil {
		return 0
	}
	switch strings.ToUpper(strings.TrimSpace(command)) {
	case "ALUP":
		return u.AllUp.Bytes
	case "WKUP":
		return u.WkUp.Bytes
	case "DAYUP":
		return u.DayUp.Bytes
	case "MONTHUP":
		return u.MonthUp.Bytes
	case "ALDN":
		return u.AllDn.Bytes
	case "WKDN":
		return u.WkDn.Bytes
	case "DAYDN":
		return u.DayDn.Bytes
	case "MONTHDN":
		return u.MonthDn.Bytes
	default:
		return 0
	}
}

func markRaceCompleteOnce(dirPath string, totalBytes int64, total int) bool {
	key := fmt.Sprintf("%s|%d|%d", path.Clean(dirPath), total, totalBytes)
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

func (s *Session) emitLoginFailure(username, remoteIP, reason string) {
	s.emitLoginFailureWithData(username, remoteIP, reason, nil)
}

func (s *Session) emitLoginFailureWithData(username, remoteIP, reason string, extra map[string]string) {
	if s == nil || s.Config == nil {
		return
	}
	data := map[string]string{
		"username":    strings.TrimSpace(username),
		"remote_ip":   strings.TrimSpace(remoteIP),
		"remote_mask": "*@" + strings.TrimSpace(remoteIP),
		"reason":      strings.TrimSpace(reason),
	}
	for key, value := range extra {
		data[key] = strings.TrimSpace(value)
	}
	s.emitEvent(EventLoginFail, "", "", 0, 0, data)
}

func (s *Session) emitSelfIPChange(username, action, oldIP, newIP string, count int) {
	if s == nil || s.Config == nil {
		return
	}
	data := map[string]string{
		"username": strings.TrimSpace(username),
		"action":   strings.TrimSpace(action),
		"count":    fmt.Sprintf("%d", count),
		"old_ip":   strings.TrimSpace(oldIP),
		"new_ip":   strings.TrimSpace(newIP),
	}
	s.emitEvent(EventSelfIP, "", "", 0, 0, data)
}
