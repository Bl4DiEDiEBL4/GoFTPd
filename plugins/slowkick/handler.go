package slowkick

import (
	"fmt"
	"log"
	"path"
	"strings"
	"sync"
	"time"

	"goftpd/internal/plugin"
	"goftpd/internal/user"
)

type candidate struct {
	SessionID uint64
	User      string
	Path      string
	Direction string
	FirstSeen time.Time
}

type Handler struct {
	svc                   *plugin.Services
	interval              time.Duration
	monitorUploads        bool
	monitorDownloads      bool
	uploadVerifyDelay     time.Duration
	downloadVerifyDelay   time.Duration
	minUploadSpeedBytes   float64
	minDownloadSpeedBytes float64
	minUsersOnline        int
	excludeUsers          map[string]struct{}
	excludeGroups         map[string]struct{}
	excludePaths          []string
	excludeExtensions     map[string]struct{}
	announceWarn          bool
	announceKick          bool
	tempbanAfterKick      bool
	tempbanDuration       time.Duration
	debug                 bool
	stopCh                chan struct{}
	wg                    sync.WaitGroup
	mu                    sync.Mutex
	candidates            map[uint64]candidate
	tempBans              map[string]time.Time
}

func New() *Handler {
	return &Handler{
		interval:              5 * time.Second,
		monitorUploads:        true,
		monitorDownloads:      true,
		uploadVerifyDelay:     20 * time.Second,
		downloadVerifyDelay:   20 * time.Second,
		minUploadSpeedBytes:   25 * 1024,
		minDownloadSpeedBytes: 50 * 1024,
		minUsersOnline:        2,
		excludeUsers:          map[string]struct{}{},
		excludeGroups:         map[string]struct{}{},
		excludePaths:          []string{"/PRE", "/REQUESTS", "/SPEEDTEST"},
		excludeExtensions:     lowerSet([]string{"sfv"}),
		stopCh:                make(chan struct{}),
		candidates:            map[uint64]candidate{},
		tempBans:              map[string]time.Time{},
	}
}

func (h *Handler) Name() string { return "slowkick" }

func (h *Handler) Init(svc *plugin.Services, cfg map[string]interface{}) error {
	h.svc = svc
	h.interval = durationSecondsConfig(cfg, "interval_seconds", 5)
	h.monitorUploads = boolConfig(cfg, "monitor_uploads", true)
	h.monitorDownloads = boolConfig(cfg, "monitor_downloads", true)
	h.uploadVerifyDelay = durationSecondsConfig(cfg, "verify_upload_seconds", 20)
	h.downloadVerifyDelay = durationSecondsConfig(cfg, "verify_download_seconds", 20)
	h.minUploadSpeedBytes = float64(intConfig(cfg["min_upload_speed_kbps"], 25) * 1024)
	h.minDownloadSpeedBytes = float64(intConfig(cfg["min_download_speed_kbps"], 50) * 1024)
	h.minUsersOnline = intConfig(cfg["min_users_online"], 2)
	h.excludeUsers = lowerSet(stringSliceConfig(cfg["exclude_users"]))
	h.excludeGroups = lowerSet(stringSliceConfig(cfg["exclude_groups"]))
	if paths := stringSliceConfig(cfg["exclude_paths"]); len(paths) > 0 {
		h.excludePaths = normalizePaths(paths)
	}
	if exts := stringSliceConfig(cfg["exclude_extensions"]); len(exts) > 0 {
		h.excludeExtensions = lowerSet(normalizeExtensions(exts))
	}
	h.announceWarn = boolConfig(cfg, "announce_warn", true)
	h.announceKick = boolConfig(cfg, "announce_kick", true)
	h.tempbanAfterKick = boolConfig(cfg, "tempban_after_kick", true)
	h.tempbanDuration = durationSecondsConfig(cfg, "tempban_seconds", 15)
	h.debug = boolConfig(cfg, "debug", svc != nil && svc.Debug)
	if h.uploadVerifyDelay < h.interval {
		h.uploadVerifyDelay = h.interval
	}
	if h.downloadVerifyDelay < h.interval {
		h.downloadVerifyDelay = h.interval
	}
	if svc == nil || svc.ListActiveSessions == nil || svc.DisconnectSession == nil || svc.AbortTransfer == nil {
		h.logf("disabled: required live session callbacks are not available")
		return nil
	}
	h.wg.Add(1)
	go h.loop()
	h.logf(
		"initialized uploads=%v downloads=%v up_min=%.1fKB/s down_min=%.1fKB/s up_verify=%s down_verify=%s min_users=%d tempban=%v tempban_seconds=%d",
		h.monitorUploads,
		h.monitorDownloads,
		h.minUploadSpeedBytes/1024.0,
		h.minDownloadSpeedBytes/1024.0,
		h.uploadVerifyDelay,
		h.downloadVerifyDelay,
		h.minUsersOnline,
		h.tempbanAfterKick,
		int(h.tempbanDuration/time.Second),
	)
	return nil
}

func (h *Handler) OnEvent(evt *plugin.Event) error { return nil }

func (h *Handler) ValidateLogin(u *user.User, remoteIP string) error {
	if u == nil || !h.tempbanAfterKick || h.tempbanDuration <= 0 {
		return nil
	}
	if _, excluded := h.excludeUsers[strings.ToLower(strings.TrimSpace(u.Name))]; excluded {
		return nil
	}
	if _, excluded := h.excludeGroups[strings.ToLower(strings.TrimSpace(u.PrimaryGroup))]; excluded {
		return nil
	}
	if until, ok := h.activeTempBan(u.Name, time.Now()); ok {
		remaining := int(time.Until(until).Seconds())
		if remaining < 1 {
			remaining = 1
		}
		return fmt.Errorf("temporarily banned after slow transfer, retry in %ds", remaining)
	}
	return nil
}

func (h *Handler) Stop() error {
	select {
	case <-h.stopCh:
	default:
		close(h.stopCh)
	}
	h.wg.Wait()
	return nil
}

func (h *Handler) loop() {
	defer h.wg.Done()
	ticker := time.NewTicker(h.interval)
	defer ticker.Stop()

	for {
		h.evaluate(time.Now())
		select {
		case <-h.stopCh:
			return
		case <-ticker.C:
		}
	}
}

func (h *Handler) evaluate(now time.Time) {
	if h.svc == nil || h.svc.ListActiveSessions == nil {
		return
	}
	h.pruneExpiredTempBans(now)
	sessions := h.svc.ListActiveSessions()
	liveStats := []plugin.LiveTransferStat(nil)
	if h.svc.GetLiveTransferStats != nil {
		liveStats = h.svc.GetLiveTransferStats()
	}

	loggedIn := 0
	for _, snap := range sessions {
		if snap.LoggedIn && strings.TrimSpace(snap.User) != "" {
			loggedIn++
		}
	}
	if loggedIn < h.minUsersOnline {
		h.clearCandidates()
		return
	}

	active := make(map[uint64]struct{})
	for _, snap := range sessions {
		policy, ok := h.transferPolicy(snap)
		if !ok || !h.shouldCheckSession(snap) {
			continue
		}
		liveStat, hasLiveStat := matchedLiveStat(snap, liveStats)
		speed := currentTransferSpeedBytes(snap, liveStat, hasLiveStat)
		if h.shouldSkipFreshTransfer(snap, liveStat, hasLiveStat, speed, now) {
			h.removeCandidate(snap.ID)
			continue
		}
		if h.debug {
			h.logf("check dir=%s user=%s group=%s path=%s speed=%.1fKB/s", policy.direction, snap.User, snap.PrimaryGroup, snap.TransferPath, speed/1024.0)
		}
		if speed > policy.minSpeedBytes {
			h.removeCandidate(snap.ID)
			continue
		}
		active[snap.ID] = struct{}{}
		cand, exists := h.getCandidate(snap.ID)
		if !exists || cand.Path != snap.TransferPath || !strings.EqualFold(cand.User, snap.User) || !strings.EqualFold(cand.Direction, policy.direction) {
			cand = candidate{
				SessionID: snap.ID,
				User:      snap.User,
				Path:      snap.TransferPath,
				Direction: policy.direction,
				FirstSeen: now,
			}
			if h.announceWarn {
				h.emitSlowEvent(policy.warnEvent, snap, speed, policy)
				h.logf("slow %s warning: %s in %s at %.1fKB/s, verifying again in %s", policy.direction, snap.User, snap.TransferPath, speed/1024.0, policy.verifyDelay)
			}
			h.setCandidate(cand)
			continue
		}
		if now.Sub(cand.FirstSeen) < policy.verifyDelay {
			continue
		}
		reason := fmt.Sprintf("slow %s: %.1fKB/s below %.1fKB/s", policy.direction, speed/1024.0, policy.minSpeedBytes/1024.0)
		if snap.TransferSlaveName != "" && snap.TransferSlaveIdx != 0 {
			h.svc.AbortTransfer(snap.TransferSlaveName, snap.TransferSlaveIdx, reason)
		}
		h.svc.DisconnectSession(snap.ID)
		if h.tempbanAfterKick && h.tempbanDuration > 0 {
			h.setTempBan(snap.User, now.Add(h.tempbanDuration))
		}
		if h.announceKick {
			h.emitSlowEvent(policy.kickEvent, snap, speed, policy)
			h.logf("kicked %s for slow %s in %s at %.1fKB/s", snap.User, policy.direction, snap.TransferPath, speed/1024.0)
		}
		h.removeCandidate(snap.ID)
	}
	h.pruneCandidates(active)
}

func (h *Handler) shouldSkipFreshTransfer(snap plugin.ActiveSession, liveStat plugin.LiveTransferStat, hasLiveStat bool, speed float64, now time.Time) bool {
	if !transferHasMoved(snap, liveStat, hasLiveStat) {
		return true
	}
	if speed > 0 {
		return false
	}
	if snap.TransferStartedAt.IsZero() {
		return true
	}
	age := now.Sub(snap.TransferStartedAt)
	if age < 0 {
		return true
	}
	return age < h.interval
}

func (h *Handler) shouldCheckSession(snap plugin.ActiveSession) bool {
	if !snap.LoggedIn {
		return false
	}
	if strings.TrimSpace(snap.User) == "" || strings.TrimSpace(snap.TransferPath) == "" {
		return false
	}
	if _, excluded := h.excludeUsers[strings.ToLower(strings.TrimSpace(snap.User))]; excluded {
		return false
	}
	if _, excluded := h.excludeGroups[strings.ToLower(strings.TrimSpace(snap.PrimaryGroup))]; excluded {
		return false
	}
	cleanPath := strings.ToLower(path.Clean("/" + strings.TrimSpace(snap.TransferPath)))
	for _, prefix := range h.excludePaths {
		if cleanPath == prefix || strings.HasPrefix(cleanPath, prefix+"/") {
			return false
		}
	}
	ext := strings.TrimPrefix(strings.ToLower(path.Ext(strings.TrimSpace(snap.TransferPath))), ".")
	if ext != "" {
		if _, excluded := h.excludeExtensions[ext]; excluded {
			return false
		}
	}
	return true
}

type transferPolicy struct {
	direction     string
	minSpeedBytes float64
	verifyDelay   time.Duration
	warnEvent     string
	kickEvent     string
}

func (h *Handler) transferPolicy(snap plugin.ActiveSession) (transferPolicy, bool) {
	switch strings.ToLower(strings.TrimSpace(snap.TransferDirection)) {
	case "upload":
		if !h.monitorUploads {
			return transferPolicy{}, false
		}
		return transferPolicy{
			direction:     "upload",
			minSpeedBytes: h.minUploadSpeedBytes,
			verifyDelay:   h.uploadVerifyDelay,
			warnEvent:     "SLOWUPLOADWARN",
			kickEvent:     "SLOWUPLOADKICK",
		}, true
	case "download":
		if !h.monitorDownloads {
			return transferPolicy{}, false
		}
		return transferPolicy{
			direction:     "download",
			minSpeedBytes: h.minDownloadSpeedBytes,
			verifyDelay:   h.downloadVerifyDelay,
			warnEvent:     "SLOWDOWNLOADWARN",
			kickEvent:     "SLOWDOWNLOADKICK",
		}, true
	default:
		return transferPolicy{}, false
	}
}

func currentTransferSpeedBytes(snap plugin.ActiveSession, liveStat plugin.LiveTransferStat, hasLiveStat bool) float64 {
	if hasLiveStat {
		return liveStat.SpeedBytes
	}
	if snap.TransferStartedAt.IsZero() || snap.TransferBytes <= 0 {
		return 0
	}
	seconds := time.Since(snap.TransferStartedAt).Seconds()
	if seconds <= 0 {
		return 0
	}
	return float64(snap.TransferBytes) / seconds
}

func transferHasMoved(snap plugin.ActiveSession, liveStat plugin.LiveTransferStat, hasLiveStat bool) bool {
	if hasLiveStat {
		return liveStat.Transferred > 0
	}
	return snap.TransferBytes > 0
}

func matchedLiveStat(snap plugin.ActiveSession, liveStats []plugin.LiveTransferStat) (plugin.LiveTransferStat, bool) {
	if snap.TransferSlaveName == "" || snap.TransferSlaveIdx == 0 {
		return plugin.LiveTransferStat{}, false
	}
	for _, stat := range liveStats {
		if !strings.EqualFold(stat.SlaveName, snap.TransferSlaveName) {
			continue
		}
		if stat.TransferIndex != snap.TransferSlaveIdx {
			continue
		}
		if !strings.EqualFold(stat.Direction, snap.TransferDirection) {
			continue
		}
		return stat, true
	}
	return plugin.LiveTransferStat{}, false
}

func (h *Handler) clearCandidates() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.candidates = map[uint64]candidate{}
}

func (h *Handler) getCandidate(id uint64) (candidate, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	c, ok := h.candidates[id]
	return c, ok
}

func (h *Handler) setCandidate(c candidate) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.candidates[c.SessionID] = c
}

func (h *Handler) removeCandidate(id uint64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.candidates, id)
}

func (h *Handler) setTempBan(username string, until time.Time) {
	username = strings.ToLower(strings.TrimSpace(username))
	if username == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.tempBans[username] = until
}

func (h *Handler) activeTempBan(username string, now time.Time) (time.Time, bool) {
	username = strings.ToLower(strings.TrimSpace(username))
	if username == "" {
		return time.Time{}, false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	until, ok := h.tempBans[username]
	if !ok {
		return time.Time{}, false
	}
	if !until.After(now) {
		delete(h.tempBans, username)
		return time.Time{}, false
	}
	return until, true
}

func (h *Handler) pruneExpiredTempBans(now time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for username, until := range h.tempBans {
		if !until.After(now) {
			delete(h.tempBans, username)
		}
	}
}

func (h *Handler) pruneCandidates(active map[uint64]struct{}) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for id := range h.candidates {
		if _, ok := active[id]; !ok {
			delete(h.candidates, id)
		}
	}
}

func (h *Handler) logf(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	if h.svc != nil && h.svc.Logger != nil {
		h.svc.Logger.Printf("[SLOWKICK] %s", msg)
		return
	}
	log.Printf("[SLOWKICK] %s", msg)
}

func (h *Handler) emitSlowEvent(eventType string, snap plugin.ActiveSession, speed float64, policy transferPolicy) {
	if h.svc == nil || h.svc.EmitEvent == nil {
		return
	}
	data := map[string]string{
		"username":         strings.TrimSpace(snap.User),
		"group":            strings.TrimSpace(snap.PrimaryGroup),
		"direction":        policy.direction,
		"speed_kbps":       fmt.Sprintf("%.2f", speed/1024.0),
		"min_speed_kbps":   fmt.Sprintf("%.2f", policy.minSpeedBytes/1024.0),
		"verify_seconds":   fmt.Sprintf("%d", int(policy.verifyDelay/time.Second)),
		"min_users_online": fmt.Sprintf("%d", h.minUsersOnline),
		"slave_name":       strings.TrimSpace(snap.TransferSlaveName),
		"transfer_index":   fmt.Sprintf("%d", snap.TransferSlaveIdx),
		"session_id":       fmt.Sprintf("%d", snap.ID),
	}
	if h.tempbanAfterKick && h.tempbanDuration > 0 && strings.Contains(eventType, "KICK") {
		data["tempban_seconds"] = fmt.Sprintf("%d", int(h.tempbanDuration/time.Second))
	}
	h.svc.EmitEvent(eventType, snap.TransferPath, path.Base(strings.TrimSpace(snap.TransferPath)), "", 0, speed/(1024.0*1024.0), data)
}

func boolConfig(cfg map[string]interface{}, key string, fallback bool) bool {
	if raw, ok := cfg[key].(bool); ok {
		return raw
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
	default:
		return fallback
	}
}

func durationSecondsConfig(cfg map[string]interface{}, key string, fallback int) time.Duration {
	return time.Duration(intConfig(cfg[key], fallback)) * time.Second
}

func stringSliceConfig(raw interface{}) []string {
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
	default:
		return nil
	}
}

func lowerSet(values []string) map[string]struct{} {
	out := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value != "" {
			out[value] = struct{}{}
		}
	}
	return out
}

func normalizePaths(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ToLower(path.Clean("/" + strings.TrimSpace(value)))
		if value == "" || value == "." {
			continue
		}
		out = append(out, value)
	}
	return out
}

func normalizeExtensions(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(strings.ToLower(value))
		value = strings.TrimPrefix(value, ".")
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}
