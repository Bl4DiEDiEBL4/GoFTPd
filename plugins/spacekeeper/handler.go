package spacekeeper

import (
	"fmt"
	"log"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"goftpd/internal/plugin"
)

type rule struct {
	Name             string
	Slave            string
	Action           string
	DeleteFallback   bool
	Paths            []string
	Destination      string
	TargetSlaves     []string
	TriggerFreeBytes int64
	TargetFreeBytes  int64
	MinAge           time.Duration
	SkipIncomplete   bool
	SkipActiveRaces  bool
	MaxActions       int
}

type candidate struct {
	Path    string
	Bytes   int64
	ModTime int64
}

type archiveTargetPlan struct {
	SlaveName string
	Victims   []candidate
}

type Handler struct {
	svc             *plugin.Services
	interval        time.Duration
	enableFreeSpace bool
	enableArchive   bool
	rules           []rule
	debug           bool
	stopCh          chan struct{}
	wg              sync.WaitGroup

	mu       sync.Mutex
	inflight map[string]time.Time
}

func New() *Handler {
	return &Handler{
		interval:        60 * time.Second,
		enableFreeSpace: true,
		enableArchive:   true,
		stopCh:          make(chan struct{}),
		inflight:        map[string]time.Time{},
	}
}

func (h *Handler) Name() string { return "spacekeeper" }

func (h *Handler) Init(svc *plugin.Services, cfg map[string]interface{}) error {
	h.svc = svc
	h.interval = durationSecondsConfig(cfg, "interval_seconds", 60)
	h.enableFreeSpace = boolConfig(cfg, "enable_freespace_actions", true)
	h.enableArchive = boolConfig(cfg, "enable_archive_actions", true)
	h.debug = boolConfig(cfg, "debug", svc != nil && svc.Debug)
	h.rules = parseRules(cfg["rules"])
	if h.interval < 5*time.Second {
		h.interval = 5 * time.Second
	}
	if svc == nil || svc.Bridge == nil || svc.ListSlaveStates == nil {
		h.logf("disabled: required bridge/slave callbacks are not available")
		return nil
	}
	if len(h.rules) == 0 {
		h.logf("disabled: no rules configured")
		return nil
	}
	h.wg.Add(1)
	go h.loop()
	h.logf("initialized with %d rule(s), interval=%s", len(h.rules), h.interval)
	return nil
}

func (h *Handler) OnEvent(evt *plugin.Event) error { return nil }

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
	if h.svc == nil || h.svc.Bridge == nil || h.svc.ListSlaveStates == nil {
		return
	}
	states := h.svc.ListSlaveStates()
	if len(states) == 0 {
		return
	}
	activeTransfers := h.activeTransferPaths()
	h.pruneInflight(now)

	for _, rule := range h.rules {
		state, ok := findSlaveState(states, rule.Slave)
		if !ok || !state.Available || state.ReadOnly {
			continue
		}
		switch strings.ToLower(rule.Action) {
		case "delete_oldest":
			if !h.enableFreeSpace {
				continue
			}
			h.applyDeleteRule(rule, state, now, activeTransfers)
		case "archive_oldest":
			if !h.enableArchive {
				continue
			}
			h.applyArchiveRule(rule, state, now, activeTransfers)
		default:
			h.logf("rule %q uses unsupported action %q, skipping", rule.Name, rule.Action)
		}
	}
}

func (h *Handler) applyDeleteRule(rule rule, state plugin.SlaveState, now time.Time, activeTransfers []string) {
	if state.FreeBytes >= rule.TriggerFreeBytes {
		return
	}
	estimatedFree := state.FreeBytes
	actions := 0
	for estimatedFree < rule.TargetFreeBytes && actions < rule.MaxActions {
		cand, ok := h.findOldestCandidate(rule, now, activeTransfers)
		if !ok {
			if actions == 0 {
				h.logf("rule %q found nothing eligible on slave %s", rule.Name, rule.Slave)
			}
			break
		}
		if !h.markInflight(cand.Path, now) {
			break
		}
		if err := h.svc.Bridge.DeleteFile(cand.Path); err != nil {
			h.unmarkInflight(cand.Path)
			h.logf("rule %q failed deleting %s: %v", rule.Name, cand.Path, err)
			break
		}
		estimatedFree += cand.Bytes
		actions++
		h.logf(
			"rule %q deleted %s on %s, estimated reclaimed %.2f GiB (free %.2f -> %.2f GiB)",
			rule.Name,
			cand.Path,
			rule.Slave,
			float64(cand.Bytes)/(1024.0*1024.0*1024.0),
			float64(state.FreeBytes)/(1024.0*1024.0*1024.0),
			float64(estimatedFree)/(1024.0*1024.0*1024.0),
		)
	}
}

func (h *Handler) applyArchiveRule(rule rule, state plugin.SlaveState, now time.Time, activeTransfers []string) {
	if state.FreeBytes >= rule.TriggerFreeBytes {
		return
	}
	estimatedFree := state.FreeBytes
	actions := 0
	for estimatedFree < rule.TargetFreeBytes && actions < rule.MaxActions {
		cand, ok := h.findOldestCandidate(rule, now, activeTransfers)
		if !ok {
			if actions == 0 {
				h.logf("rule %q found nothing eligible to archive on slave %s", rule.Name, rule.Slave)
			}
			break
		}
		if !h.markInflight(cand.Path, now) {
			break
		}
		h.startArchiveJob(rule, state, cand)
		estimatedFree += cand.Bytes
		actions++
	}
}

func (h *Handler) archiveCandidate(fromPath, toDir, toName, targetSlave string) error {
	before := h.svc.Bridge.FileExists(fromPath)
	if err := h.svc.Bridge.RelocatePathToSlave(fromPath, toDir, toName, targetSlave); err != nil {
		return err
	}
	targetPath := cleanAbs(path.Join(toDir, toName))
	for i := 0; i < 20; i++ {
		if !before || h.svc.Bridge.FileExists(targetPath) {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	if h.svc.Bridge.FileExists(targetPath) {
		return nil
	}
	return fmt.Errorf("rename did not materialize target %s", targetPath)
}

func (h *Handler) startArchiveJob(rule rule, state plugin.SlaveState, cand candidate) {
	destDir := cleanAbs(rule.Destination)
	destName := path.Base(cand.Path)
	targetPlan, targetErr := h.chooseArchiveTarget(rule, cand.Bytes)
	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		defer h.unmarkInflight(cand.Path)

		err := fmt.Errorf("archive disabled")
		if h.enableArchive {
			if targetErr != nil {
				err = targetErr
			} else {
				for _, victim := range targetPlan.Victims {
					if delErr := h.svc.Bridge.DeleteFile(victim.Path); delErr != nil {
						err = fmt.Errorf("failed freeing archive space on %s by deleting %s: %w", targetPlan.SlaveName, victim.Path, delErr)
						goto archiveDone
					}
					h.logf("rule %q deleted archived release %s on %s to free %.2f GiB", rule.Name, victim.Path, targetPlan.SlaveName, float64(victim.Bytes)/(1024.0*1024.0*1024.0))
				}
				err = h.archiveCandidate(cand.Path, destDir, destName, targetPlan.SlaveName)
			}
		}
	archiveDone:
		if err == nil {
			if targetPlan.SlaveName != "" {
				h.logf("rule %q archived %s to %s/%s via %s", rule.Name, cand.Path, destDir, destName, targetPlan.SlaveName)
			} else {
				h.logf("rule %q archived %s to %s/%s on %s", rule.Name, cand.Path, destDir, destName, state.Name)
			}
			return
		}
		if !rule.DeleteFallback || !h.enableFreeSpace {
			h.logf("rule %q failed moving %s -> %s/%s: %v", rule.Name, cand.Path, destDir, destName, err)
			return
		}
		if delErr := h.svc.Bridge.DeleteFile(cand.Path); delErr != nil {
			h.logf("rule %q archive failed for %s (%v) and delete fallback also failed: %v", rule.Name, cand.Path, err, delErr)
			return
		}
		h.logf("rule %q archive failed for %s, deleted instead and reclaimed %.2f GiB", rule.Name, cand.Path, float64(cand.Bytes)/(1024.0*1024.0*1024.0))
	}()
}

func (h *Handler) chooseArchiveTarget(rule rule, requiredBytes int64) (archiveTargetPlan, error) {
	if len(rule.TargetSlaves) == 0 {
		return archiveTargetPlan{}, nil
	}
	if h.svc == nil || h.svc.ListSlaveStates == nil {
		return archiveTargetPlan{}, fmt.Errorf("archive target selection unavailable")
	}
	states := h.svc.ListSlaveStates()
	activeTransfers := h.activeTransferPaths()
	now := time.Now()
	destPattern := cleanAbs(rule.Destination)
	if destPattern == "/" {
		destPattern = "/*"
	} else {
		destPattern = strings.TrimRight(destPattern, "/") + "/*"
	}

	bestImmediateIdx := -1
	var bestImmediate plugin.SlaveState
	var bestPlanned archiveTargetPlan
	bestVictimCount := 0

	for idx, name := range rule.TargetSlaves {
		state, ok := findSlaveState(states, name)
		if !ok || !state.Available || state.ReadOnly {
			continue
		}

		if state.FreeBytes >= requiredBytes {
			if bestImmediateIdx == -1 || state.FreeBytes > bestImmediate.FreeBytes {
				bestImmediateIdx = idx
				bestImmediate = state
			}
			continue
		}

		if !h.enableFreeSpace {
			continue
		}

		victims, reclaimed, ok := h.planArchiveVictims(rule, state.Name, []string{destPattern}, now, activeTransfers, requiredBytes-state.FreeBytes)
		if !ok {
			continue
		}
		if state.FreeBytes+reclaimed < requiredBytes {
			continue
		}
		if bestPlanned.SlaveName == "" || len(victims) < bestVictimCount || (len(victims) == bestVictimCount && state.FreeBytes > findSlaveStateMust(states, bestPlanned.SlaveName).FreeBytes) {
			bestPlanned = archiveTargetPlan{SlaveName: state.Name, Victims: victims}
			bestVictimCount = len(victims)
		}
	}

	if bestImmediateIdx != -1 {
		return archiveTargetPlan{SlaveName: bestImmediate.Name}, nil
	}
	if bestPlanned.SlaveName != "" {
		return bestPlanned, nil
	}
	return archiveTargetPlan{}, fmt.Errorf("no available archive target in target_slaves")
}

func findSlaveStateMust(states []plugin.SlaveState, slaveName string) plugin.SlaveState {
	state, _ := findSlaveState(states, slaveName)
	return state
}

func (h *Handler) planArchiveVictims(baseRule rule, slaveName string, paths []string, now time.Time, activeTransfers []string, bytesNeeded int64) ([]candidate, int64, bool) {
	if bytesNeeded <= 0 {
		return nil, 0, true
	}
	rule := baseRule
	rule.Slave = slaveName
	rule.Action = "delete_oldest"
	rule.Paths = paths

	victims := []candidate{}
	reclaimed := int64(0)
	blocked := map[string]struct{}{}
	for reclaimed < bytesNeeded {
		cand, ok := h.findOldestCandidateWithBlocked(rule, now, activeTransfers, blocked)
		if !ok {
			return victims, reclaimed, false
		}
		victims = append(victims, cand)
		reclaimed += cand.Bytes
		blocked[cand.Path] = struct{}{}
	}
	return victims, reclaimed, true
}

func (h *Handler) findOldestCandidateWithBlocked(rule rule, now time.Time, activeTransfers []string, blocked map[string]struct{}) (candidate, bool) {
	candidates := map[string]candidate{}
	for _, pattern := range rule.Paths {
		root := literalPrefix(pattern)
		if root == "" {
			root = "/"
		}
		h.walkPatternWithBlocked(rule, pattern, root, now, activeTransfers, blocked, candidates)
	}
	if len(candidates) == 0 {
		return candidate{}, false
	}
	all := make([]candidate, 0, len(candidates))
	for _, cand := range candidates {
		all = append(all, cand)
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].ModTime == all[j].ModTime {
			return all[i].Path < all[j].Path
		}
		return all[i].ModTime < all[j].ModTime
	})
	return all[0], true
}

func (h *Handler) walkPatternWithBlocked(rule rule, pattern, dirPath string, now time.Time, activeTransfers []string, blocked map[string]struct{}, candidates map[string]candidate) {
	dirPath = cleanAbs(dirPath)
	if dirPath == "" {
		return
	}
	for _, entry := range h.svc.Bridge.PluginListDir(dirPath) {
		if !entry.IsDir || entry.IsSymlink {
			continue
		}
		childPath := cleanAbs(path.Join(dirPath, entry.Name))
		if _, skip := blocked[childPath]; skip {
			continue
		}
		if matched, _ := path.Match(pattern, childPath); matched {
			if cand, ok := h.evaluateCandidate(rule, childPath, entry.ModTime, now, activeTransfers); ok {
				existing, exists := candidates[cand.Path]
				if !exists || cand.ModTime < existing.ModTime {
					candidates[cand.Path] = cand
				}
			}
		}
		h.walkPatternWithBlocked(rule, pattern, childPath, now, activeTransfers, blocked, candidates)
	}
}

func (h *Handler) activeTransferPaths() []string {
	if h.svc == nil || h.svc.ListActiveSessions == nil {
		return nil
	}
	snaps := h.svc.ListActiveSessions()
	out := make([]string, 0, len(snaps))
	for _, snap := range snaps {
		p := cleanAbs(snap.TransferPath)
		if !snap.LoggedIn || p == "" || p == "/" {
			continue
		}
		out = append(out, p)
	}
	return out
}

func (h *Handler) findOldestCandidate(rule rule, now time.Time, activeTransfers []string) (candidate, bool) {
	candidates := map[string]candidate{}
	for _, pattern := range rule.Paths {
		root := literalPrefix(pattern)
		if root == "" {
			root = "/"
		}
		h.walkPattern(rule, pattern, root, now, activeTransfers, candidates)
	}
	if len(candidates) == 0 {
		return candidate{}, false
	}
	all := make([]candidate, 0, len(candidates))
	for _, cand := range candidates {
		all = append(all, cand)
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].ModTime == all[j].ModTime {
			return all[i].Path < all[j].Path
		}
		return all[i].ModTime < all[j].ModTime
	})
	return all[0], true
}

func (h *Handler) walkPattern(rule rule, pattern, dirPath string, now time.Time, activeTransfers []string, candidates map[string]candidate) {
	dirPath = cleanAbs(dirPath)
	if dirPath == "" {
		return
	}
	for _, entry := range h.svc.Bridge.PluginListDir(dirPath) {
		if !entry.IsDir || entry.IsSymlink {
			continue
		}
		childPath := cleanAbs(path.Join(dirPath, entry.Name))
		if matched, _ := path.Match(pattern, childPath); matched {
			if cand, ok := h.evaluateCandidate(rule, childPath, entry.ModTime, now, activeTransfers); ok {
				existing, exists := candidates[cand.Path]
				if !exists || cand.ModTime < existing.ModTime {
					candidates[cand.Path] = cand
				}
			}
		}
		h.walkPattern(rule, pattern, childPath, now, activeTransfers, candidates)
	}
}

func (h *Handler) evaluateCandidate(rule rule, dirPath string, modTime int64, now time.Time, activeTransfers []string) (candidate, bool) {
	if h.isInflight(dirPath) {
		return candidate{}, false
	}
	if rule.MinAge > 0 && modTime > 0 && now.Sub(time.Unix(modTime, 0)) < rule.MinAge {
		return candidate{}, false
	}
	if rule.SkipActiveRaces && hasActiveTransferUnder(dirPath, activeTransfers) {
		return candidate{}, false
	}
	if rule.SkipIncomplete {
		_, _, _, present, total := h.svc.Bridge.PluginGetVFSRaceStats(dirPath)
		if total > 0 && present < total {
			return candidate{}, false
		}
	}
	base := path.Base(dirPath)
	if isDateBucketName(base) {
		return candidate{}, false
	}
	if strings.EqualFold(rule.Action, "archive_oldest") {
		destDir := cleanAbs(rule.Destination)
		destPath := cleanAbs(path.Join(destDir, path.Base(dirPath)))
		if destDir == "" || destDir == "/" || h.svc.Bridge.FileExists(destPath) {
			return candidate{}, false
		}
	}
	bytes, ok := h.dirBytesOnSlave(dirPath, rule.Slave)
	if !ok || bytes <= 0 {
		return candidate{}, false
	}
	return candidate{
		Path:    dirPath,
		Bytes:   bytes,
		ModTime: modTime,
	}, true
}

func (h *Handler) dirBytesOnSlave(dirPath, slaveName string) (int64, bool) {
	var total int64
	var found bool
	for _, entry := range h.svc.Bridge.PluginListDir(cleanAbs(dirPath)) {
		childPath := cleanAbs(path.Join(dirPath, entry.Name))
		if entry.IsDir && !entry.IsSymlink {
			childBytes, childFound := h.dirBytesOnSlave(childPath, slaveName)
			total += childBytes
			found = found || childFound
			continue
		}
		if strings.EqualFold(strings.TrimSpace(entry.Slave), strings.TrimSpace(slaveName)) {
			total += entry.Size
			found = true
		}
	}
	return total, found
}

func (h *Handler) pruneInflight(now time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for p, markedAt := range h.inflight {
		if now.Sub(markedAt) > 15*time.Minute {
			delete(h.inflight, p)
		}
	}
}

func (h *Handler) markInflight(p string, now time.Time) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	p = cleanAbs(p)
	if _, exists := h.inflight[p]; exists {
		return false
	}
	h.inflight[p] = now
	return true
}

func (h *Handler) unmarkInflight(p string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.inflight, cleanAbs(p))
}

func (h *Handler) isInflight(p string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, exists := h.inflight[cleanAbs(p)]
	return exists
}

func (h *Handler) logf(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	if h.svc != nil && h.svc.Logger != nil {
		h.svc.Logger.Printf("[SPACEKEEPER] %s", msg)
		return
	}
	log.Printf("[SPACEKEEPER] %s", msg)
}

func findSlaveState(states []plugin.SlaveState, slaveName string) (plugin.SlaveState, bool) {
	for _, state := range states {
		if strings.EqualFold(state.Name, slaveName) {
			return state, true
		}
	}
	return plugin.SlaveState{}, false
}

func hasActiveTransferUnder(dirPath string, activeTransfers []string) bool {
	dirPath = cleanAbs(dirPath)
	for _, p := range activeTransfers {
		if p == dirPath || strings.HasPrefix(p, dirPath+"/") {
			return true
		}
	}
	return false
}

func literalPrefix(pattern string) string {
	pattern = cleanAbs(pattern)
	if pattern == "/" {
		return "/"
	}
	parts := strings.Split(strings.TrimPrefix(pattern, "/"), "/")
	literal := make([]string, 0, len(parts))
	for _, part := range parts {
		if strings.ContainsAny(part, "*?[") {
			break
		}
		literal = append(literal, part)
	}
	if len(literal) == 0 {
		return "/"
	}
	return "/" + path.Join(literal...)
}

func parseRules(raw interface{}) []rule {
	items, ok := raw.([]interface{})
	if !ok {
		return nil
	}
	out := make([]rule, 0, len(items))
	for idx, item := range items {
		cfg, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		paths := normalizePaths(stringSliceConfig(cfg["paths"]))
		r := rule{
			Name:             stringValue(cfg, "name", fmt.Sprintf("rule-%d", idx+1)),
			Slave:            strings.TrimSpace(stringValue(cfg, "slave", "")),
			Action:           strings.ToLower(strings.TrimSpace(stringValue(cfg, "action", "delete_oldest"))),
			DeleteFallback:   boolValue(cfg, "delete_fallback", false),
			Paths:            paths,
			Destination:      cleanAbs(stringValue(cfg, "destination", "")),
			TargetSlaves:     stringSliceConfig(cfg["target_slaves"]),
			TriggerFreeBytes: bytesFromRule(cfg, "trigger_free"),
			TargetFreeBytes:  bytesFromRule(cfg, "target_free"),
			MinAge:           time.Duration(intValue(cfg, "min_age_seconds", 600)) * time.Second,
			SkipIncomplete:   boolValue(cfg, "skip_incomplete", true),
			SkipActiveRaces:  boolValue(cfg, "skip_active_races", true),
			MaxActions:       intValue(cfg, "max_actions_per_cycle", 10),
		}
		if r.Name == "" || r.Slave == "" || len(r.Paths) == 0 {
			continue
		}
		switch r.Action {
		case "delete_oldest":
			if r.TriggerFreeBytes <= 0 || r.TargetFreeBytes <= r.TriggerFreeBytes {
				continue
			}
		case "archive_oldest":
			if r.Destination == "" || r.TriggerFreeBytes <= 0 || r.TargetFreeBytes <= r.TriggerFreeBytes {
				continue
			}
		default:
			continue
		}
		if r.MaxActions < 1 {
			r.MaxActions = 1
		}
		out = append(out, r)
	}
	return out
}

func bytesFromRule(cfg map[string]interface{}, key string) int64 {
	if n := int64Value(cfg, key+"_bytes", 0); n > 0 {
		return n
	}
	if n := int64Value(cfg, key+"_mb", 0); n > 0 {
		return n * 1024 * 1024
	}
	if n := int64Value(cfg, key+"_gb", 0); n > 0 {
		return n * 1024 * 1024 * 1024
	}
	return 0
}

func normalizePaths(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if !strings.HasPrefix(value, "/") {
			value = "/" + value
		}
		value = path.Clean(value)
		if value == "." {
			value = "/"
		}
		out = append(out, value)
	}
	return out
}

func cleanAbs(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	p = path.Clean(p)
	if p == "." {
		return "/"
	}
	return p
}

func isDateBucketName(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	if len(name) == 4 && isDigits(name) {
		return true
	}
	if len(name) == 8 && isDigits(name) {
		return true
	}
	if len(name) == 10 && name[4] == '-' && name[7] == '-' && isDigits(name[0:4]+name[5:7]+name[8:10]) {
		return true
	}
	return false
}

func isDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func durationSecondsConfig(cfg map[string]interface{}, key string, fallback int) time.Duration {
	return time.Duration(intValue(cfg, key, fallback)) * time.Second
}

func boolConfig(cfg map[string]interface{}, key string, fallback bool) bool {
	return boolValue(cfg, key, fallback)
}

func boolValue(cfg map[string]interface{}, key string, fallback bool) bool {
	raw, ok := cfg[key]
	if !ok {
		return fallback
	}
	switch v := raw.(type) {
	case bool:
		return v
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "true", "yes", "1", "on":
			return true
		case "false", "no", "0", "off":
			return false
		}
	}
	return fallback
}

func intValue(cfg map[string]interface{}, key string, fallback int) int {
	raw, ok := cfg[key]
	if !ok {
		return fallback
	}
	switch v := raw.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case string:
		var n int
		if _, err := fmt.Sscanf(strings.TrimSpace(v), "%d", &n); err == nil {
			return n
		}
	}
	return fallback
}

func int64Value(cfg map[string]interface{}, key string, fallback int64) int64 {
	raw, ok := cfg[key]
	if !ok {
		return fallback
	}
	switch v := raw.(type) {
	case int:
		return int64(v)
	case int64:
		return v
	case float64:
		return int64(v)
	case string:
		var n int64
		if _, err := fmt.Sscanf(strings.TrimSpace(v), "%d", &n); err == nil {
			return n
		}
	}
	return fallback
}

func stringValue(cfg map[string]interface{}, key, fallback string) string {
	raw, ok := cfg[key]
	if !ok {
		return fallback
	}
	if s, ok := raw.(string); ok {
		return strings.TrimSpace(s)
	}
	return fallback
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
