package master

import (
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"goftpd/internal/core"
	"goftpd/internal/protocol"
	"goftpd/internal/zipscript"
)

// SlaveManager listens for slave connections and manages all RemoteSlave objects.
//

const (
	vfsFilePath = "userdata/vfs.dat"
)

type releaseRaceWindow struct {
	StartMs     int64
	EndMs       int64
	UpdatedAtMs int64
}

type backgroundRemergeConfig struct {
	interval     time.Duration
	initialDelay time.Duration
	stagger      time.Duration
	basePath     string
	rootsOnly    bool
	skipBusy     bool
}

type SlaveManager struct {
	listenHost       string
	listenPort       int
	tlsEnabled       bool
	tlsCert          string
	tlsKey           string
	heartbeatTimeout time.Duration
	authFailLimit    int
	authFailWindow   time.Duration
	authBanDuration  time.Duration
	authAllowNets    []*net.IPNet
	authDenyFile     string
	authDenyEntries  []authNetworkEntry

	slaves   map[string]*RemoteSlave
	slavesMu sync.RWMutex

	// Per-slave routing policies (section affinity, weights). Keyed by slave name.
	// Populated from master config via SetSlavePolicies(). Empty = no restrictions.
	policies   map[string]SlaveRoutePolicy
	policiesMu sync.RWMutex

	// Section/root directories that should exist on matching writable slaves.
	bootstrapDirs   []string
	bootstrapDirsMu sync.RWMutex

	excludePaths   []string
	excludePathsMu sync.RWMutex

	// Virtual File System: master-side file index
	vfs *VirtualFileSystem

	releaseStateMu       sync.RWMutex
	releaseMedia         map[string]map[string]string
	releaseAnnouncements map[string]map[string]bool
	releaseRaceWindows   map[string]*releaseRaceWindow
	statusMarkerMu       sync.RWMutex
	statusMarkerCfg      statusMarkerConfig

	remergeMode       atomic.Value
	manualRemergeMode atomic.Value

	listener        net.Listener
	running         atomic.Bool
	diskStatusHook  func(name string, status protocol.DiskStatus, online, available bool, sections []string)
	securityHook    func(ip, remoteAddr, action, reason string, strikes, limit int, bannedUntil time.Time)
	authMu          sync.Mutex
	authState       map[string]*slaveAuthState
	remergeCRCJobs  sync.Map
	remergeSFVJobs  sync.Map
	remergeCRCSem   chan struct{}
	remergePauseAt  atomic.Int64
	remergeResumeAt atomic.Int64

	enableRemergeChecksums atomic.Bool

	backgroundRemergeMu   sync.RWMutex
	backgroundRemerge     backgroundRemergeConfig
	backgroundRemergeWake chan struct{}

	startupCachedSlaves sync.Map
}

type slaveAuthState struct {
	Strikes     int
	FirstSeen   time.Time
	BannedUntil time.Time
}

type authNetworkEntry struct {
	Raw string
	Net *net.IPNet
}

type AuthBanSnapshot struct {
	IP          string
	Strikes     int
	BannedUntil time.Time
}

// SlaveRoutePolicy is the runtime form of SlavePolicy from config.
type SlaveRoutePolicy struct {
	Sections []string // uppercased for matching
	Paths    []string // glob patterns
	Weight   int      // >= 1
	ReadOnly bool     // scan/download only; never selected for uploads
}

func NewSlaveManager(host string, port int, tlsEnabled bool, tlsCert, tlsKey string, heartbeatTimeout time.Duration) *SlaveManager {
	if heartbeatTimeout <= 0 {
		heartbeatTimeout = 60 * time.Second
	}
	sm := &SlaveManager{
		listenHost:            host,
		listenPort:            port,
		tlsEnabled:            tlsEnabled,
		tlsCert:               tlsCert,
		tlsKey:                tlsKey,
		heartbeatTimeout:      heartbeatTimeout,
		slaves:                make(map[string]*RemoteSlave),
		policies:              make(map[string]SlaveRoutePolicy),
		vfs:                   NewVirtualFileSystem(),
		releaseMedia:          make(map[string]map[string]string),
		releaseAnnouncements:  make(map[string]map[string]bool),
		releaseRaceWindows:    make(map[string]*releaseRaceWindow),
		authState:             make(map[string]*slaveAuthState),
		remergeCRCSem:         make(chan struct{}, 16),
		backgroundRemergeWake: make(chan struct{}, 1),
	}
	sm.remergeMode.Store("off")
	sm.manualRemergeMode.Store("instant")
	sm.remergePauseAt.Store(250)
	sm.remergeResumeAt.Store(50)
	return sm
}

func (sm *SlaveManager) SetDiskStatusHook(fn func(name string, status protocol.DiskStatus, online, available bool, sections []string)) {
	sm.diskStatusHook = fn
}

func (sm *SlaveManager) SetSecurityHook(fn func(ip, remoteAddr, action, reason string, strikes, limit int, bannedUntil time.Time)) {
	sm.securityHook = fn
}

func (sm *SlaveManager) ConfigureAuthGuard(limit int, window, banDuration time.Duration) {
	sm.authMu.Lock()
	defer sm.authMu.Unlock()
	sm.authFailLimit = limit
	sm.authFailWindow = window
	sm.authBanDuration = banDuration
}

func (sm *SlaveManager) ConfigureAuthAllowlist(values []string) error {
	nets := make([]*net.IPNet, 0, len(values))
	for _, raw := range values {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if strings.Contains(raw, "/") {
			_, network, err := net.ParseCIDR(raw)
			if err != nil {
				return fmt.Errorf("invalid slave allowlist entry %q: %w", raw, err)
			}
			nets = append(nets, network)
			continue
		}
		ip := net.ParseIP(raw)
		if ip == nil {
			return fmt.Errorf("invalid slave allowlist IP %q", raw)
		}
		bits := 32
		if ip.To4() == nil {
			bits = 128
		}
		nets = append(nets, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
	}
	sm.authMu.Lock()
	sm.authAllowNets = nets
	sm.authMu.Unlock()
	return nil
}

func (sm *SlaveManager) ConfigureAuthDenylistFile(filePath string) error {
	sm.authMu.Lock()
	defer sm.authMu.Unlock()

	sm.authDenyFile = strings.TrimSpace(filePath)
	if sm.authDenyFile == "" {
		sm.authDenyEntries = nil
		return nil
	}
	return sm.loadAuthDenylistLocked()
}

func (sm *SlaveManager) publishDiskStatus(rs *RemoteSlave) {
	if sm.diskStatusHook == nil || rs == nil {
		return
	}
	sm.diskStatusHook(rs.Name(), rs.GetDiskStatus(), rs.IsOnline(), rs.IsAvailable(), sm.policySections(rs.Name()))
}

func (sm *SlaveManager) PublishAllDiskStatuses() {
	for _, rs := range sm.GetAllSlaves() {
		sm.publishDiskStatus(rs)
	}
}

func (sm *SlaveManager) policySections(slaveName string) []string {
	policy, ok := sm.getPolicy(slaveName)
	if !ok || len(policy.Sections) == 0 {
		return []string{"*"}
	}
	out := make([]string, len(policy.Sections))
	copy(out, policy.Sections)
	return out
}

// SetSlavePolicies configures per-slave routing rules (section affinity + weights).
// Call once at startup after loading config. Re-calling replaces all policies.
func (sm *SlaveManager) SetSlavePolicies(policies map[string]SlaveRoutePolicy) {
	sm.policiesMu.Lock()
	defer sm.policiesMu.Unlock()
	sm.policies = make(map[string]SlaveRoutePolicy, len(policies))
	for name, p := range policies {
		if p.Weight < 1 {
			p.Weight = 1
		}
		upSections := make([]string, len(p.Sections))
		for i, s := range p.Sections {
			upSections[i] = strings.ToUpper(strings.TrimSpace(s))
		}
		p.Sections = upSections
		sm.policies[name] = p
	}
}

func (sm *SlaveManager) SetProtectedDirs(paths []string) {
	sm.vfs.SetProtectedDirs(paths)
}

func (sm *SlaveManager) SetHiddenPaths(paths []string) {
	sm.vfs.SetHiddenPaths(paths)
}

func (sm *SlaveManager) SetExcludePaths(paths []string) {
	sm.excludePathsMu.Lock()
	defer sm.excludePathsMu.Unlock()

	seen := make(map[string]bool, len(paths))
	sm.excludePaths = sm.excludePaths[:0]
	for _, p := range paths {
		p = normalizeBootstrapDir(p)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		sm.excludePaths = append(sm.excludePaths, p)
	}
	sm.vfs.SetExcludePaths(paths)
}

func (sm *SlaveManager) SetEnableRemergeChecksums(enabled bool) {
	sm.enableRemergeChecksums.Store(enabled)
}

func (sm *SlaveManager) SetRemergeFlowControl(pauseThreshold, resumeThreshold int) {
	if pauseThreshold <= 0 {
		pauseThreshold = 250
	}
	if resumeThreshold < 0 {
		resumeThreshold = 0
	}
	if resumeThreshold >= pauseThreshold {
		resumeThreshold = pauseThreshold / 2
	}
	sm.remergePauseAt.Store(int64(pauseThreshold))
	sm.remergeResumeAt.Store(int64(resumeThreshold))
}

func (sm *SlaveManager) ConfigureBackgroundRemerge(interval, initialDelay, stagger time.Duration, basePath string, rootsOnly bool, skipBusy bool) {
	if interval < 0 {
		interval = 0
	}
	if initialDelay < 0 {
		initialDelay = 0
	}
	if stagger < 0 {
		stagger = 0
	}
	basePath = path.Clean("/" + strings.TrimSpace(basePath))
	if basePath == "." || basePath == "" {
		basePath = "/"
	}

	sm.backgroundRemergeMu.Lock()
	sm.backgroundRemerge = backgroundRemergeConfig{
		interval:     interval,
		initialDelay: initialDelay,
		stagger:      stagger,
		basePath:     basePath,
		rootsOnly:    rootsOnly,
		skipBusy:     skipBusy,
	}
	sm.backgroundRemergeMu.Unlock()
	sm.notifyBackgroundRemergeLoop()
}

func (sm *SlaveManager) backgroundRemergeSnapshot() backgroundRemergeConfig {
	sm.backgroundRemergeMu.RLock()
	defer sm.backgroundRemergeMu.RUnlock()
	cfg := sm.backgroundRemerge
	if cfg.basePath == "" {
		cfg.basePath = "/"
	}
	return cfg
}

func (sm *SlaveManager) notifyBackgroundRemergeLoop() {
	if sm.backgroundRemergeWake == nil {
		return
	}
	select {
	case sm.backgroundRemergeWake <- struct{}{}:
	default:
	}
}

func (sm *SlaveManager) SetRemergeMode(mode string) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	switch mode {
	case "instant":
		sm.remergeMode.Store(mode)
	default:
		sm.remergeMode.Store("off")
	}
}

func (sm *SlaveManager) SetManualRemergeMode(mode string) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	switch mode {
	case "off":
		sm.manualRemergeMode.Store("off")
	default:
		sm.manualRemergeMode.Store("instant")
	}
}

func (sm *SlaveManager) GetRemergeFlowControl() (pauseThreshold, resumeThreshold int) {
	pauseThreshold = int(sm.remergePauseAt.Load())
	resumeThreshold = int(sm.remergeResumeAt.Load())
	if pauseThreshold <= 0 {
		pauseThreshold = 250
	}
	if resumeThreshold < 0 || resumeThreshold >= pauseThreshold {
		resumeThreshold = pauseThreshold / 2
	}
	return pauseThreshold, resumeThreshold
}

func (sm *SlaveManager) GetRemergeMode() string {
	if raw := sm.remergeMode.Load(); raw != nil {
		if mode, ok := raw.(string); ok && strings.TrimSpace(mode) != "" {
			return mode
		}
	}
	return "off"
}

func (sm *SlaveManager) GetManualRemergeMode() string {
	if raw := sm.manualRemergeMode.Load(); raw != nil {
		if mode, ok := raw.(string); ok && strings.TrimSpace(mode) != "" {
			return mode
		}
	}
	return "instant"
}

func (sm *SlaveManager) getExcludePaths() []string {
	sm.excludePathsMu.RLock()
	defer sm.excludePathsMu.RUnlock()
	return append([]string(nil), sm.excludePaths...)
}

func (sm *SlaveManager) SetBootstrapDirs(paths []string) {
	sm.bootstrapDirsMu.Lock()
	defer sm.bootstrapDirsMu.Unlock()

	seen := make(map[string]bool, len(paths))
	sm.bootstrapDirs = sm.bootstrapDirs[:0]
	for _, p := range paths {
		p = normalizeBootstrapDir(p)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		sm.bootstrapDirs = append(sm.bootstrapDirs, p)
	}
}

func (sm *SlaveManager) EnsureBootstrapDirs() {
	for _, rs := range sm.GetAllSlaves() {
		sm.ensureBootstrapDirsOnSlave(rs)
	}
}

func (sm *SlaveManager) getPolicy(name string) (SlaveRoutePolicy, bool) {
	sm.policiesMu.RLock()
	defer sm.policiesMu.RUnlock()
	p, ok := sm.policies[name]
	return p, ok
}

// Start begins listening for slave connections.
// ().
func (sm *SlaveManager) Start() error {
	// Load saved VFS from disk (if exists)
	sm.vfs.LoadFromDisk(vfsFilePath)
	for _, slaveName := range sm.vfs.SlaveNames() {
		sm.startupCachedSlaves.Store(slaveName, struct{}{})
	}

	var listener net.Listener
	var err error

	addr := fmt.Sprintf("%s:%d", sm.listenHost, sm.listenPort)

	if sm.tlsEnabled && sm.tlsCert != "" && sm.tlsKey != "" {
		cert, err := tls.LoadX509KeyPair(sm.tlsCert, sm.tlsKey)
		if err != nil {
			return fmt.Errorf("failed to load TLS cert: %w", err)
		}
		tlsCfg := &tls.Config{Certificates: []tls.Certificate{cert}}
		listener, err = tls.Listen("tcp", addr, tlsCfg)
		if err != nil {
			return fmt.Errorf("failed to listen TLS: %w", err)
		}
	} else {
		listener, err = net.Listen("tcp", addr)
		if err != nil {
			return fmt.Errorf("failed to listen: %w", err)
		}
	}

	sm.listener = listener
	sm.running.Store(true)

	log.Printf("[SlaveManager] Listening for slaves on %s", addr)

	go sm.acceptLoop()
	go sm.vfsPersistLoop()
	go sm.diskStatusLoop()
	go sm.backgroundRemergeLoop()

	return nil
}

func (sm *SlaveManager) acceptLoop() {
	for sm.running.Load() {
		conn, err := sm.listener.Accept()
		if err != nil {
			if sm.running.Load() {
				log.Printf("[SlaveManager] Accept error: %v", err)
			}
			continue
		}

		ip, remoteAddr := splitRemoteAddr(conn.RemoteAddr())
		if denied, by := sm.isAuthExplicitlyDenied(ip); denied {
			log.Printf("[SlaveManager] Denied slave connection from %s (denylist %s)", remoteAddr, by)
			sm.publishSecurityEvent(ip, remoteAddr, "denylist", fmt.Sprintf("slave control IP matched denylist entry %s", by), 0, time.Time{})
			conn.Close()
			continue
		}
		if !sm.isAuthAllowed(ip) {
			log.Printf("[SlaveManager] Denied slave connection from %s (not in allowlist)", remoteAddr)
			sm.publishSecurityEvent(ip, remoteAddr, "deny", "slave control IP not in allowlist", 0, time.Time{})
			conn.Close()
			continue
		}
		if banned, until := sm.isAuthBanned(ip); banned {
			log.Printf("[SlaveManager] Blocked banned slave connection from %s until %s", remoteAddr, until.Format(time.RFC3339))
			conn.Close()
			continue
		}

		log.Printf("[SlaveManager] Accepted connection from %s", remoteAddr)
		go sm.handleSlaveConnection(conn)
	}
}

// handleSlaveConnection processes a new slave connection.
// () inner loop.
func (sm *SlaveManager) handleSlaveConnection(conn net.Conn) {
	stream := protocol.NewObjectStream(conn)
	ip, remoteAddr := splitRemoteAddr(conn.RemoteAddr())

	// Read slave name (: RemoteSlave.getSlaveNameFromObjectInput)
	conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	obj, err := stream.ReadObject()
	if err != nil {
		log.Printf("[SlaveManager] Failed to read slave name: %v", err)
		sm.recordAuthFailure(ip, remoteAddr, fmt.Sprintf("failed to read slave name: %v", err))
		conn.Close()
		return
	}
	conn.SetReadDeadline(time.Time{})

	slaveName, ok := obj.(string)
	if !ok || slaveName == "" {
		log.Printf("[SlaveManager] Invalid slave name from %s", conn.RemoteAddr())
		sm.recordAuthFailure(ip, remoteAddr, "invalid slave name")
		stream.WriteObject(&protocol.AsyncCommand{Index: "error", Name: "error", Args: []string{"invalid slave name"}})
		conn.Close()
		return
	}

	sm.slavesMu.Lock()
	existing, exists := sm.slaves[slaveName]
	if exists && existing.IsOnline() {
		sm.slavesMu.Unlock()
		log.Printf("[SlaveManager] Slave %s already online, rejecting", slaveName)
		stream.WriteObject(&protocol.AsyncCommand{Index: "", Name: "error", Args: []string{"Already online"}})
		conn.Close()
		return
	}

	rs := NewRemoteSlave(slaveName, conn, stream, sm.heartbeatTimeout, func(name string) {
		sm.vfs.ClearSlave(name)
	})
	sm.slaves[slaveName] = rs
	sm.slavesMu.Unlock()

	log.Printf("[SlaveManager] Slave '%s' connected from %s", slaveName, conn.RemoteAddr())
	sm.clearAuthState(ip)

	// Read initial disk status from slave
	conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	dsObj, err := stream.ReadObject()
	conn.SetReadDeadline(time.Time{})
	if err != nil {
		log.Printf("[SlaveManager] Failed to read disk status from %s: %v", slaveName, err)
		rs.SetOffline("failed to read disk status")
		return
	}
	if ds, ok := dsObj.(*protocol.AsyncResponseDiskStatus); ok {
		rs.diskMu.Lock()
		rs.diskStatus = ds.Status
		rs.diskMu.Unlock()
		log.Printf("[SlaveManager] Slave %s disk: %dMB free / %dMB total",
			slaveName, ds.Status.SpaceAvailable/1024/1024, ds.Status.SpaceCapacity/1024/1024)
		sm.publishDiskStatus(rs)
	}

	// Start remerge ()
	rs.remerging.Store(true)
	go sm.initializeSlaveAfterConnect(rs, false)

	// Start the main read loop (())
	rs.Run(sm)
}

func (sm *SlaveManager) publishSecurityEvent(ip, remoteAddr, action, reason string, strikes int, bannedUntil time.Time) {
	if sm.securityHook == nil {
		return
	}
	sm.securityHook(ip, remoteAddr, action, reason, strikes, sm.authFailLimit, bannedUntil)
}

func (sm *SlaveManager) recordAuthFailure(ip, remoteAddr, reason string) {
	if strings.TrimSpace(ip) == "" {
		return
	}
	now := time.Now()

	sm.authMu.Lock()
	state := sm.authState[ip]
	if state == nil {
		state = &slaveAuthState{}
		sm.authState[ip] = state
	}
	if sm.authFailWindow > 0 && !state.FirstSeen.IsZero() && now.Sub(state.FirstSeen) > sm.authFailWindow {
		state.Strikes = 0
		state.FirstSeen = time.Time{}
		state.BannedUntil = time.Time{}
	}
	if state.FirstSeen.IsZero() {
		state.FirstSeen = now
	}
	state.Strikes++
	strikes := state.Strikes
	bannedUntil := time.Time{}
	action := "fail"
	if sm.authFailLimit > 0 && sm.authBanDuration > 0 && strikes >= sm.authFailLimit {
		state.BannedUntil = now.Add(sm.authBanDuration)
		bannedUntil = state.BannedUntil
		action = "ban"
	}
	sm.authMu.Unlock()

	if action == "ban" {
		log.Printf("[SlaveManager] Banned slave source %s for %s after %d failed handshake(s): %s", remoteAddr, sm.authBanDuration, strikes, reason)
	} else {
		log.Printf("[SlaveManager] Slave auth failure from %s (%d/%d): %s", remoteAddr, strikes, sm.authFailLimit, reason)
	}
	sm.publishSecurityEvent(ip, remoteAddr, action, reason, strikes, bannedUntil)
}

func (sm *SlaveManager) clearAuthState(ip string) {
	if strings.TrimSpace(ip) == "" {
		return
	}
	sm.authMu.Lock()
	delete(sm.authState, ip)
	sm.authMu.Unlock()
}

func (sm *SlaveManager) isAuthBanned(ip string) (bool, time.Time) {
	if strings.TrimSpace(ip) == "" {
		return false, time.Time{}
	}
	now := time.Now()
	sm.authMu.Lock()
	defer sm.authMu.Unlock()
	state := sm.authState[ip]
	if state == nil {
		return false, time.Time{}
	}
	if !state.BannedUntil.IsZero() {
		if now.Before(state.BannedUntil) {
			return true, state.BannedUntil
		}
		state.BannedUntil = time.Time{}
		state.Strikes = 0
		state.FirstSeen = time.Time{}
	}
	if sm.authFailWindow > 0 && !state.FirstSeen.IsZero() && now.Sub(state.FirstSeen) > sm.authFailWindow {
		state.Strikes = 0
		state.FirstSeen = time.Time{}
	}
	return false, time.Time{}
}

func (sm *SlaveManager) isAuthAllowed(ip string) bool {
	if strings.TrimSpace(ip) == "" {
		return false
	}
	parsed := net.ParseIP(strings.TrimSpace(ip))
	if parsed == nil {
		return false
	}
	sm.authMu.Lock()
	nets := append([]*net.IPNet(nil), sm.authAllowNets...)
	sm.authMu.Unlock()
	if len(nets) == 0 {
		return true
	}
	for _, network := range nets {
		if network != nil && network.Contains(parsed) {
			return true
		}
	}
	return false
}

func (sm *SlaveManager) isAuthExplicitlyDenied(ip string) (bool, string) {
	if strings.TrimSpace(ip) == "" {
		return false, ""
	}
	parsed := net.ParseIP(strings.TrimSpace(ip))
	if parsed == nil {
		return false, ""
	}
	sm.authMu.Lock()
	entries := append([]authNetworkEntry(nil), sm.authDenyEntries...)
	sm.authMu.Unlock()
	for _, entry := range entries {
		if entry.Net != nil && entry.Net.Contains(parsed) {
			return true, entry.Raw
		}
	}
	return false, ""
}

func (sm *SlaveManager) ListAuthDenyEntries() []string {
	sm.authMu.Lock()
	defer sm.authMu.Unlock()
	out := make([]string, 0, len(sm.authDenyEntries))
	for _, entry := range sm.authDenyEntries {
		out = append(out, entry.Raw)
	}
	sort.Strings(out)
	return out
}

func (sm *SlaveManager) AddAuthDenyEntry(raw string) (string, error) {
	entry, err := parseAuthNetworkEntry(raw)
	if err != nil {
		return "", err
	}
	sm.authMu.Lock()
	defer sm.authMu.Unlock()
	for _, existing := range sm.authDenyEntries {
		if strings.EqualFold(existing.Raw, entry.Raw) {
			return entry.Raw, nil
		}
	}
	sm.authDenyEntries = append(sm.authDenyEntries, entry)
	sort.Slice(sm.authDenyEntries, func(i, j int) bool {
		return sm.authDenyEntries[i].Raw < sm.authDenyEntries[j].Raw
	})
	if err := sm.saveAuthDenylistLocked(); err != nil {
		return "", err
	}
	return entry.Raw, nil
}

func (sm *SlaveManager) RemoveAuthDenyEntry(raw string) (bool, error) {
	entry, err := parseAuthNetworkEntry(raw)
	if err != nil {
		return false, err
	}
	sm.authMu.Lock()
	defer sm.authMu.Unlock()
	out := sm.authDenyEntries[:0]
	removed := false
	for _, existing := range sm.authDenyEntries {
		if strings.EqualFold(existing.Raw, entry.Raw) {
			removed = true
			continue
		}
		out = append(out, existing)
	}
	sm.authDenyEntries = out
	if !removed {
		return false, nil
	}
	if err := sm.saveAuthDenylistLocked(); err != nil {
		return false, err
	}
	return true, nil
}

func (sm *SlaveManager) ListAuthTempBans() []AuthBanSnapshot {
	now := time.Now()
	sm.authMu.Lock()
	defer sm.authMu.Unlock()
	out := make([]AuthBanSnapshot, 0, len(sm.authState))
	for ip, state := range sm.authState {
		if state == nil || state.BannedUntil.IsZero() || !now.Before(state.BannedUntil) {
			continue
		}
		out = append(out, AuthBanSnapshot{
			IP:          ip,
			Strikes:     state.Strikes,
			BannedUntil: state.BannedUntil,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].IP < out[j].IP
	})
	return out
}

func (sm *SlaveManager) loadAuthDenylistLocked() error {
	sm.authDenyEntries = nil
	if sm.authDenyFile == "" {
		return nil
	}
	data, err := os.ReadFile(sm.authDenyFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		entry, err := parseAuthNetworkEntry(line)
		if err != nil {
			return fmt.Errorf("%s: %w", sm.authDenyFile, err)
		}
		sm.authDenyEntries = append(sm.authDenyEntries, entry)
	}
	sort.Slice(sm.authDenyEntries, func(i, j int) bool {
		return sm.authDenyEntries[i].Raw < sm.authDenyEntries[j].Raw
	})
	return nil
}

func (sm *SlaveManager) saveAuthDenylistLocked() error {
	if sm.authDenyFile == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(sm.authDenyFile), 0755); err != nil {
		return err
	}
	var lines []string
	lines = append(lines, "# Slave control denylist: exact IPs or CIDRs, one per line.")
	for _, entry := range sm.authDenyEntries {
		lines = append(lines, entry.Raw)
	}
	content := strings.Join(lines, "\n") + "\n"
	return os.WriteFile(sm.authDenyFile, []byte(content), 0644)
}

func parseAuthNetworkEntry(raw string) (authNetworkEntry, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return authNetworkEntry{}, fmt.Errorf("empty denylist entry")
	}
	if strings.Contains(raw, "/") {
		_, network, err := net.ParseCIDR(raw)
		if err != nil {
			return authNetworkEntry{}, fmt.Errorf("invalid denylist entry %q: %w", raw, err)
		}
		return authNetworkEntry{Raw: network.String(), Net: network}, nil
	}
	ip := net.ParseIP(raw)
	if ip == nil {
		return authNetworkEntry{}, fmt.Errorf("invalid denylist IP %q", raw)
	}
	bits := 32
	if ip.To4() == nil {
		bits = 128
	}
	return authNetworkEntry{
		Raw: ip.String(),
		Net: &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)},
	}, nil
}

func splitRemoteAddr(addr net.Addr) (ip string, raw string) {
	if addr == nil {
		return "", ""
	}
	raw = strings.TrimSpace(addr.String())
	host, _, err := net.SplitHostPort(raw)
	if err == nil {
		return strings.TrimSpace(host), raw
	}
	return raw, raw
}

func cloneSlaveStringMap(src map[string]string) map[string]string {
	if len(src) == 0 {
		return nil
	}
	out := make(map[string]string, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

func (sm *SlaveManager) SetReleaseMediaInfo(dirPath string, fields map[string]string) {
	if sm == nil {
		return
	}
	cleanDirPath := filepath.Clean(dirPath)
	sm.releaseStateMu.Lock()
	defer sm.releaseStateMu.Unlock()
	if len(fields) == 0 {
		delete(sm.releaseMedia, cleanDirPath)
		return
	}
	sm.releaseMedia[cleanDirPath] = cloneSlaveStringMap(fields)
}

func (sm *SlaveManager) ResetReleaseRaceWindow(dirPath string) {
	if sm == nil {
		return
	}
	cleanDirPath := filepath.Clean(dirPath)
	sm.releaseStateMu.Lock()
	defer sm.releaseStateMu.Unlock()
	delete(sm.releaseRaceWindows, cleanDirPath)
}

func (sm *SlaveManager) StartReleaseRaceWindowAt(dirPath string, startMs int64) {
	if sm == nil {
		return
	}
	if startMs <= 0 {
		startMs = time.Now().UnixMilli()
	}
	cleanDirPath := filepath.Clean(dirPath)
	sm.releaseStateMu.Lock()
	defer sm.releaseStateMu.Unlock()
	window := sm.releaseRaceWindows[cleanDirPath]
	if window == nil {
		sm.releaseRaceWindows[cleanDirPath] = &releaseRaceWindow{
			StartMs:     startMs,
			EndMs:       startMs,
			UpdatedAtMs: startMs,
		}
		return
	}
	if window.StartMs <= 0 {
		window.StartMs = startMs
	}
	if window.EndMs < window.StartMs {
		window.EndMs = window.StartMs
	}
	if window.UpdatedAtMs < startMs {
		window.UpdatedAtMs = startMs
	}
}

func (sm *SlaveManager) NoteRacePayloadTransferAt(dirPath string, durationMs int64, endMs int64) {
	if sm == nil || durationMs <= 0 {
		return
	}
	cleanDirPath := filepath.Clean(dirPath)
	if endMs <= 0 {
		endMs = time.Now().UnixMilli()
	}
	startMs := endMs - durationMs
	if startMs < 1 {
		startMs = 1
	}
	sm.releaseStateMu.Lock()
	defer sm.releaseStateMu.Unlock()
	window := sm.releaseRaceWindows[cleanDirPath]
	if window == nil {
		sm.releaseRaceWindows[cleanDirPath] = &releaseRaceWindow{
			StartMs:     startMs,
			EndMs:       endMs,
			UpdatedAtMs: endMs,
		}
		return
	}
	if window.StartMs <= 0 || startMs < window.StartMs {
		window.StartMs = startMs
	}
	if endMs > window.EndMs {
		window.EndMs = endMs
	}
	window.UpdatedAtMs = endMs
}

func (sm *SlaveManager) NoteRacePayloadTransfer(dirPath, fileName string, durationMs int64) {
	sm.NoteRacePayloadTransferAt(dirPath, durationMs, 0)
}

func (sm *SlaveManager) GetReleaseRaceWindowMilliseconds(dirPath string) int64 {
	if sm == nil {
		return 0
	}
	cleanDirPath := filepath.Clean(dirPath)
	sm.releaseStateMu.RLock()
	window := sm.releaseRaceWindows[cleanDirPath]
	sm.releaseStateMu.RUnlock()
	if window == nil {
		return 0
	}
	if window.EndMs <= window.StartMs || window.StartMs <= 0 {
		return 0
	}
	return window.EndMs - window.StartMs
}

func (sm *SlaveManager) GetReleaseMediaInfo(dirPath string) map[string]string {
	if sm == nil {
		return nil
	}
	cleanDirPath := filepath.Clean(dirPath)
	sm.releaseStateMu.RLock()
	defer sm.releaseStateMu.RUnlock()
	return cloneSlaveStringMap(sm.releaseMedia[cleanDirPath])
}

func (sm *SlaveManager) ClaimReleaseAnnouncement(dirPath, key string) bool {
	if sm == nil {
		return false
	}
	cleanDirPath := filepath.Clean(dirPath)
	key = strings.TrimSpace(key)
	if key == "" {
		return false
	}
	sm.releaseStateMu.Lock()
	defer sm.releaseStateMu.Unlock()
	if sm.releaseAnnouncements[cleanDirPath] == nil {
		sm.releaseAnnouncements[cleanDirPath] = make(map[string]bool)
	}
	if sm.releaseAnnouncements[cleanDirPath][key] {
		return false
	}
	sm.releaseAnnouncements[cleanDirPath][key] = true
	return true
}

func (sm *SlaveManager) InvalidateReleaseStateForPath(filePath string, isDir bool) {
	if sm == nil {
		return
	}
	cleanPath := filepath.Clean(filePath)
	sm.releaseStateMu.Lock()
	defer sm.releaseStateMu.Unlock()
	delete(sm.releaseMedia, cleanPath)
	delete(sm.releaseAnnouncements, cleanPath)
	delete(sm.releaseRaceWindows, cleanPath)
	if isDir {
		prefix := strings.TrimRight(filepath.ToSlash(cleanPath), "/") + "/"
		for key := range sm.releaseMedia {
			if key == cleanPath || strings.HasPrefix(filepath.ToSlash(key), prefix) {
				delete(sm.releaseMedia, key)
			}
		}
		for key := range sm.releaseAnnouncements {
			if key == cleanPath || strings.HasPrefix(filepath.ToSlash(key), prefix) {
				delete(sm.releaseAnnouncements, key)
			}
		}
		for key := range sm.releaseRaceWindows {
			if key == cleanPath || strings.HasPrefix(filepath.ToSlash(key), prefix) {
				delete(sm.releaseRaceWindows, key)
			}
		}
	}
}

func (sm *SlaveManager) RenameReleaseState(from, to string, isDir bool) {
	if sm == nil {
		return
	}
	from = filepath.Clean(from)
	to = filepath.Clean(to)
	sm.releaseStateMu.Lock()
	defer sm.releaseStateMu.Unlock()
	if media, ok := sm.releaseMedia[from]; ok {
		sm.releaseMedia[to] = cloneSlaveStringMap(media)
		delete(sm.releaseMedia, from)
	}
	if announced, ok := sm.releaseAnnouncements[from]; ok {
		copied := make(map[string]bool, len(announced))
		for k, v := range announced {
			copied[k] = v
		}
		sm.releaseAnnouncements[to] = copied
		delete(sm.releaseAnnouncements, from)
	}
	if window, ok := sm.releaseRaceWindows[from]; ok {
		copyWindow := *window
		sm.releaseRaceWindows[to] = &copyWindow
		delete(sm.releaseRaceWindows, from)
	}
	if !isDir {
		return
	}
	fromPrefix := strings.TrimRight(filepath.ToSlash(from), "/") + "/"
	for key, media := range sm.releaseMedia {
		if strings.HasPrefix(filepath.ToSlash(key), fromPrefix) {
			newKey := to + key[len(from):]
			sm.releaseMedia[newKey] = cloneSlaveStringMap(media)
			delete(sm.releaseMedia, key)
		}
	}
	for key, announced := range sm.releaseAnnouncements {
		if strings.HasPrefix(filepath.ToSlash(key), fromPrefix) {
			newKey := to + key[len(from):]
			copied := make(map[string]bool, len(announced))
			for k, v := range announced {
				copied[k] = v
			}
			sm.releaseAnnouncements[newKey] = copied
			delete(sm.releaseAnnouncements, key)
		}
	}
	for key, window := range sm.releaseRaceWindows {
		if strings.HasPrefix(filepath.ToSlash(key), fromPrefix) {
			newKey := to + key[len(from):]
			copyWindow := *window
			sm.releaseRaceWindows[newKey] = &copyWindow
			delete(sm.releaseRaceWindows, key)
		}
	}
}

// initializeSlaveAfterConnect triggers remerge and marks slave available.
// Uses "instant online" approach : slave is marked available
// immediately and remerge runs in the background. Files appear in LIST
// as they are indexed.
func (sm *SlaveManager) initializeSlaveAfterConnect(rs *RemoteSlave, forceInstantOnline bool) {
	sm.initializeSlaveRemerge(rs, "/", false, false, forceInstantOnline)
}

func (sm *SlaveManager) initializeSlaveRemerge(rs *RemoteSlave, basePath string, rootsOnly bool, scoped bool, forceInstantOnline bool) {
	mode := sm.GetRemergeMode()
	instantOnline := forceInstantOnline || mode == "instant"
	basePath = path.Clean("/" + strings.TrimSpace(basePath))
	if basePath == "." || basePath == "" {
		basePath = "/"
	}
	if !scoped {
		if _, useCachedVFS := sm.startupCachedSlaves.LoadAndDelete(rs.name); useCachedVFS {
			log.Printf("[SlaveManager] Reusing cached VFS for slave %s on startup; skipping initial remerge", rs.name)
			rs.remerging.Store(false)
			rs.SetAvailable(true)
			sm.publishDiskStatus(rs)
			sm.ensureBootstrapDirsOnSlave(rs)
			return
		}
	}

	log.Printf("[SlaveManager] Starting remerge for slave %s (mode=%s base=%s rootsOnly=%v scoped=%v)", rs.name, mode, basePath, rootsOnly, scoped)

	rs.remerging.Store(true)
	if instantOnline {
		rs.SetAvailable(true)
		sm.publishDiskStatus(rs)
		log.Printf("[SlaveManager] Slave %s is now AVAILABLE (remerge running in background)", rs.name)
	} else {
		rs.SetAvailable(false)
		sm.publishDiskStatus(rs)
	}

	sm.ensureBootstrapDirsOnSlave(rs)

	// Only full slave remerges should mark everything unseen and purge at the end.
	if !scoped {
		sm.vfs.MarkAllUnseen(rs.name)
	} else if !rootsOnly {
		sm.vfs.MarkSubtreeUnseen(rs.name, basePath)
	}

	index, err := IssueRemerge(rs, basePath, false, 0, time.Now().UnixMilli(), instantOnline, rootsOnly, sm.getExcludePaths())
	if err != nil {
		log.Printf("[SlaveManager] Failed to issue remerge to %s: %v", rs.name, err)
		// Don't take offline - slave is still connected, just no file index yet.
		rs.remerging.Store(false)
		if !instantOnline {
			rs.SetAvailable(false)
			sm.publishDiskStatus(rs)
		}
		return
	}

	// Wait for remerge with no timeout (0 = use default actualTimeout per response,
	// but we pass a very long timeout so large sites can finish)
	remergeComplete := false
	_, err = rs.FetchResponse(index, 60*time.Minute)
	if err != nil {
		log.Printf("[SlaveManager] Remerge did not complete for %s: %v (slave stays online)", rs.name, err)
	} else {
		if err := rs.WaitForRemergeDrain(60 * time.Minute); err != nil {
			log.Printf("[SlaveManager] Remerge queue did not drain for %s: %v", rs.name, err)
		} else {
			remergeComplete = true
			log.Printf("[SlaveManager] Remerge complete for slave %s", rs.name)

			if !scoped {
				// Purge files that were physically deleted from the slave.
				sm.vfs.PurgeUnseen(rs.name)
				log.Printf("[SlaveManager] Ghost files purged for %s", rs.name)
			} else if !rootsOnly {
				sm.vfs.PurgeUnseenSubtree(rs.name, basePath)
				log.Printf("[SlaveManager] Scoped ghost files purged for %s at %s", rs.name, basePath)
			}

			// Persist the VFS after remerge and purge complete.
			if err := sm.vfs.SaveToDisk(vfsFilePath); err != nil {
				log.Printf("[SlaveManager] Error saving VFS after remerge: %v", err)
			}
		}
	}
	rs.remerging.Store(false)
	if !instantOnline && remergeComplete {
		rs.SetAvailable(true)
	}
	sm.publishDiskStatus(rs)
}

// ProcessRemerge handles incoming remerge data from a slave.
func (sm *SlaveManager) ProcessRemerge(rs *RemoteSlave, resp *protocol.AsyncResponseRemerge) {
	log.Printf("[SlaveManager] Remerge from %s: dir=%s files=%d", rs.name, resp.Path, len(resp.Files))

	sfvPaths := make([]string, 0, 1)
	touchedReleases := make(map[string]struct{}, 8)
	touchedParents := make(map[string]struct{}, 2)
	statusCfg := sm.statusMarkerConfig().Zipscript
	sm.noteTouchedStatusMarkerRelease(statusCfg, resp.Path, true, touchedReleases)
	for _, inode := range resp.Files {
		fullPath := resp.Path
		if fullPath == "/" {
			fullPath = "/" + inode.Name
		} else {
			fullPath = resp.Path + "/" + inode.Name
		}
		if sm.vfs.IsExcludedPath(fullPath) {
			continue
		}
		if zipscript.IsStatusMarkerName(sm.statusMarkerConfig().Zipscript, inode.Name) {
			continue
		}

		// Keep trusted FTP owner/group metadata instead of replacing it with OS ownership.
		owner := inode.Owner
		group := inode.Group
		if existingFile := sm.vfs.GetFile(fullPath); existingFile != nil {
			// ALWAYS trust the Master's VFS owner over the Slave's physical OS owner.
			// This prevents the Slave OS (GoFTPd/ftp) from wiping out real FTP users (N0pe) on restart.
			if !isWeakMetadataValue(existingFile.Owner) {
				owner = existingFile.Owner
				group = existingFile.Group
			}
		}

		sm.vfs.AddFile(fullPath, VFSFile{
			Path:         fullPath,
			Size:         inode.Size,
			IsDir:        inode.IsDir,
			IsSymlink:    inode.IsSymlink,
			LinkTarget:   inode.LinkTarget,
			LastModified: inode.LastModified,
			SlaveName:    rs.name,
			Owner:        owner,
			Group:        group,
			Seen:         true,
		})
		touchedParents[path.Dir(fullPath)] = struct{}{}
		sm.noteTouchedStatusMarkerRelease(statusCfg, fullPath, inode.IsDir, touchedReleases)
		if !inode.IsDir && strings.HasSuffix(strings.ToLower(inode.Name), ".sfv") {
			sfvPaths = append(sfvPaths, fullPath)
		}
		if sm.shouldRefreshRemergeChecksum(fullPath, inode) {
			sm.scheduleRemergeChecksumRefresh(rs, fullPath)
		}
	}
	for _, sfvPath := range sfvPaths {
		sm.scheduleRemergeSFVParse(rs, sfvPath)
	}

	// Each remerge response is a complete snapshot of one physical directory.
	// Prune stale direct children now so slow/background remerges update the VFS
	// as they walk instead of leaving ghosts until the full tree finishes.
	sm.vfs.PurgeUnseenChildren(rs.name, resp.Path)

	touchedParents[path.Clean(resp.Path)] = struct{}{}
	for dirPath := range touchedParents {
		sm.pruneStaleStatusMarkersForDir(dirPath)
	}
	for releasePath := range touchedReleases {
		sm.syncStatusMarkersForRelease(releasePath)
	}
}

func (sm *SlaveManager) shouldRefreshRemergeChecksum(filePath string, inode protocol.LightRemoteInode) bool {
	if !sm.enableRemergeChecksums.Load() {
		return false
	}
	if inode.IsDir || inode.IsSymlink || inode.Size <= 0 {
		return false
	}
	meta := sm.vfs.GetSFVData(path.Dir(filePath))
	if meta == nil || len(meta.SFVEntries) == 0 {
		return false
	}
	if _, ok := meta.SFVEntries[strings.ToLower(path.Base(filePath))]; !ok {
		return false
	}
	current := sm.vfs.GetFile(filePath)
	return current != nil && current.Checksum == 0
}

func (sm *SlaveManager) scheduleRemergeChecksumRefresh(rs *RemoteSlave, filePath string) {
	if rs == nil || !rs.IsOnline() {
		return
	}
	jobKey := rs.Name() + "|" + filepath.Clean(filePath)
	if _, loaded := sm.remergeCRCJobs.LoadOrStore(jobKey, struct{}{}); loaded {
		return
	}
	go func() {
		defer sm.remergeCRCJobs.Delete(jobKey)
		if sm.remergeCRCSem != nil {
			sm.remergeCRCSem <- struct{}{}
			defer func() { <-sm.remergeCRCSem }()
		}

		index, err := IssueChecksum(rs, filePath)
		if err != nil {
			return
		}
		resp, err := rs.FetchResponse(index, 30*time.Second)
		if err != nil {
			return
		}
		checksumResp, ok := resp.(*protocol.AsyncResponseChecksum)
		if !ok {
			return
		}
		meta := sm.vfs.GetSFVData(path.Dir(filePath))
		if meta != nil && len(meta.SFVEntries) > 0 {
			if expectedCRC, exists := meta.SFVEntries[strings.ToLower(path.Base(filePath))]; exists && expectedCRC != 0 && checksumResp.Checksum != expectedCRC {
				log.Printf("[SlaveManager] Remerge CRC mismatch for %s on %s: got %08X expected %08X - keeping file and marking it unverified",
					filePath, rs.Name(), checksumResp.Checksum, expectedCRC)
				sm.vfs.UpdateFileVerification(filePath, checksumResp.Checksum)
				sm.SyncStatusMarkersForPath(filePath, false)
				return
			}
		}
		sm.vfs.UpdateFileVerification(filePath, checksumResp.Checksum)
		sm.SyncStatusMarkersForPath(filePath, false)
	}()
}

func (sm *SlaveManager) scheduleRemergeSFVParse(rs *RemoteSlave, sfvPath string) {
	if rs == nil || !rs.IsOnline() {
		return
	}
	jobKey := rs.Name() + "|" + filepath.Clean(sfvPath)
	if _, loaded := sm.remergeSFVJobs.LoadOrStore(jobKey, struct{}{}); loaded {
		return
	}
	go func() {
		defer sm.remergeSFVJobs.Delete(jobKey)

		index, err := IssueSFVFile(rs, sfvPath)
		if err != nil {
			return
		}
		resp, err := rs.FetchResponse(index, 30*time.Second)
		if err != nil {
			return
		}
		sfvResp, ok := resp.(*protocol.AsyncResponseSFVInfo)
		if !ok {
			return
		}

		dirPath := path.Dir(sfvPath)
		sfvName := path.Base(sfvPath)
		sfvMap := make(map[string]uint32, len(sfvResp.Entries))
		for _, entry := range sfvResp.Entries {
			sfvMap[strings.ToLower(path.Base(entry.FileName))] = entry.CRC32
		}
		sm.vfs.SetSFVDataWithChecksum(dirPath, sfvName, sfvResp.Checksum, sfvMap)
		sm.syncMissingMarkersAfterRemerge(rs, dirPath, sfvMap)
		sm.syncStatusMarkersForRelease(dirPath)

		for _, child := range sm.vfs.ListDirectory(dirPath) {
			if child == nil || child.IsDir || child.Size <= 0 {
				continue
			}
			nameKey := strings.ToLower(path.Base(child.Path))
			if _, ok := sfvMap[nameKey]; !ok {
				continue
			}
			if sm.enableRemergeChecksums.Load() && child.Checksum == 0 {
				sm.scheduleRemergeChecksumRefresh(rs, child.Path)
			}
		}
	}()
}

func (sm *SlaveManager) syncMissingMarkersAfterRemerge(rs *RemoteSlave, dirPath string, sfvMap map[string]uint32) {
	if sm == nil || rs == nil || len(sfvMap) == 0 {
		return
	}
	if !zipscript.ShowMissingFilesForDir(sm.statusMarkerConfig().Zipscript, dirPath) {
		return
	}
	status, ok := sm.vfs.GetReleaseStatus(dirPath)
	if !ok || status.Kind != "sfv" || len(status.ExpectedFiles) == 0 {
		return
	}

	createPaths, deletePaths := missingMarkerSyncPaths(dirPath, status, sm.vfs.ListDirectory(dirPath))
	for _, missingPath := range deletePaths {
		sm.vfs.DeleteFile(missingPath)
		go func(missing string) {
			index, err := IssueDelete(rs, missing)
			if err != nil {
				return
			}
			if _, err := rs.FetchResponse(index, 30*time.Second); err != nil {
				log.Printf("[SlaveManager] stale missing marker delete failed for %s: %v", missing, err)
			}
		}(missingPath)
	}
	for _, missingPath := range createPaths {
		index, err := IssueWriteFile(rs, missingPath, "")
		if err != nil {
			log.Printf("[SlaveManager] missing marker create failed for %s: %v", missingPath, err)
			continue
		}
		resp, err := rs.FetchResponse(index, 30*time.Second)
		if err != nil {
			log.Printf("[SlaveManager] missing marker create failed for %s: %v", missingPath, err)
			continue
		}
		if errResp, ok := resp.(*protocol.AsyncResponseError); ok {
			log.Printf("[SlaveManager] missing marker create failed for %s: %s", missingPath, errResp.Message)
			continue
		}
		sm.vfs.AddFile(missingPath, VFSFile{
			Path:         missingPath,
			Size:         0,
			LastModified: time.Now().Unix(),
			SlaveName:    rs.Name(),
			Seen:         true,
		})
	}
}

func missingMarkerSyncPaths(dirPath string, status core.ReleaseStatus, entries []*VFSFile) (createPaths []string, deletePaths []string) {
	filesByName := make(map[string]*VFSFile)
	for _, entry := range entries {
		if entry == nil || entry.IsDir {
			continue
		}
		filesByName[strings.ToLower(path.Base(entry.Path))] = entry
	}
	missingSet := make(map[string]bool, len(status.MissingFiles))
	for _, fileName := range status.MissingFiles {
		missingSet[strings.ToLower(path.Base(fileName))] = true
	}
	for _, expectedName := range status.ExpectedFiles {
		fileName := path.Base(expectedName)
		realEntry := filesByName[strings.ToLower(fileName)]
		missingName := fileName + "-MISSING"
		missingEntry := filesByName[strings.ToLower(missingName)]
		if realEntry != nil && !missingSet[strings.ToLower(fileName)] {
			if missingEntry != nil {
				deletePaths = append(deletePaths, missingEntry.Path)
			}
			continue
		}
		if missingEntry == nil {
			createPaths = append(createPaths, path.Join(dirPath, missingName))
		}
	}
	sort.Strings(createPaths)
	sort.Strings(deletePaths)
	return createPaths, deletePaths
}

// --- Slave Access ---

func (sm *SlaveManager) GetSlave(name string) *RemoteSlave {
	sm.slavesMu.RLock()
	defer sm.slavesMu.RUnlock()
	if rs := sm.slaves[name]; rs != nil {
		return rs
	}
	for slaveName, rs := range sm.slaves {
		if strings.EqualFold(slaveName, name) {
			return rs
		}
	}
	return nil
}

// StartRemerge starts a full background VFS refresh for one connected slave.
// Existing VFS entries for that slave are marked unseen before the scan and
// purged when the slave reports completion.
func (sm *SlaveManager) StartRemerge(name string) error {
	rs := sm.GetSlave(name)
	if rs == nil {
		return fmt.Errorf("unknown slave %s", name)
	}
	if !rs.IsOnline() {
		return fmt.Errorf("slave %s is offline", rs.Name())
	}
	if !rs.remerging.CompareAndSwap(false, true) {
		return fmt.Errorf("slave %s is already remerging", rs.Name())
	}
	go sm.initializeSlaveAfterConnect(rs, sm.GetManualRemergeMode() == "instant")
	return nil
}

func (sm *SlaveManager) StartRemergePath(name, basePath string, rootsOnly bool) error {
	rs := sm.GetSlave(name)
	if rs == nil {
		return fmt.Errorf("unknown slave %s", name)
	}
	if !rs.IsOnline() {
		return fmt.Errorf("slave %s is offline", rs.Name())
	}
	if !rs.remerging.CompareAndSwap(false, true) {
		return fmt.Errorf("slave %s is already remerging", rs.Name())
	}
	go sm.initializeSlaveRemerge(rs, basePath, rootsOnly, true, sm.GetManualRemergeMode() == "instant")
	return nil
}

// StartRemergeAll starts a full background VFS refresh for every online slave.
func (sm *SlaveManager) StartRemergeAll() (int, []string) {
	var errs []string
	started := 0
	slaves := sm.GetAllSlaves()
	if len(slaves) == 0 {
		return 0, []string{"no slaves connected"}
	}
	for _, rs := range slaves {
		if err := sm.StartRemerge(rs.Name()); err != nil {
			errs = append(errs, err.Error())
			continue
		}
		started++
	}
	return started, errs
}

func (sm *SlaveManager) StartRemergeAllPath(basePath string, rootsOnly bool) (int, []string) {
	var errs []string
	started := 0
	slaves := sm.GetAllSlaves()
	if len(slaves) == 0 {
		return 0, []string{"no slaves connected"}
	}
	for _, rs := range slaves {
		if err := sm.StartRemergePath(rs.Name(), basePath, rootsOnly); err != nil {
			errs = append(errs, err.Error())
			continue
		}
		started++
	}
	return started, errs
}

func (sm *SlaveManager) ensureBootstrapDirsOnSlave(rs *RemoteSlave) {
	if rs == nil || !rs.IsOnline() || sm.IsSlaveReadOnly(rs.Name()) {
		return
	}
	dirs := sm.getBootstrapDirsForSlave(rs.Name())
	for _, dirPath := range dirs {
		lastModified := time.Now().Unix()
		if existing := sm.vfs.GetFile(dirPath); existing != nil && existing.IsDir && existing.LastModified > 0 {
			lastModified = existing.LastModified
		}
		sm.vfs.AddFile(dirPath, VFSFile{
			Path:         dirPath,
			IsDir:        true,
			LastModified: lastModified,
			Owner:        "GoFTPd",
			Group:        "GoFTPd",
			Seen:         true,
		})
		index, err := IssueMakeDirAllRoots(rs, dirPath)
		if err != nil {
			log.Printf("[SlaveManager] Bootstrap mkdir %s on %s failed: %v", dirPath, rs.Name(), err)
			continue
		}
		if _, err := rs.FetchResponse(index, 30*time.Second); err != nil {
			log.Printf("[SlaveManager] Bootstrap mkdir %s on %s failed: %v", dirPath, rs.Name(), err)
		}
	}
}

func (sm *SlaveManager) getBootstrapDirsForSlave(slaveName string) []string {
	sm.bootstrapDirsMu.RLock()
	dirs := append([]string(nil), sm.bootstrapDirs...)
	sm.bootstrapDirsMu.RUnlock()

	if len(dirs) == 0 {
		return nil
	}
	policy, hasPolicy := sm.getPolicy(slaveName)
	if !hasPolicy || (len(policy.Sections) == 0 && len(policy.Paths) == 0) {
		return dirs
	}

	out := make([]string, 0, len(dirs))
	for _, dirPath := range dirs {
		if slavePolicyAccepts(policy, sectionFromUploadPath(dirPath), dirPath) {
			out = append(out, dirPath)
		}
	}
	return out
}

func normalizeBootstrapDir(p string) string {
	p = strings.TrimSpace(p)
	if p == "" || strings.ContainsAny(p, "*?[]") {
		return ""
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	p = path.Clean(p)
	if p == "." || p == "/" {
		return ""
	}
	return p
}

func (sm *SlaveManager) GetAvailableSlaves() []*RemoteSlave {
	sm.slavesMu.RLock()
	defer sm.slavesMu.RUnlock()

	var result []*RemoteSlave
	for _, rs := range sm.slaves {
		if rs.IsAvailable() {
			result = append(result, rs)
		}
	}
	return result
}

func (sm *SlaveManager) GetWritableAvailableSlaves() []*RemoteSlave {
	slaves := sm.GetAvailableSlaves()
	result := make([]*RemoteSlave, 0, len(slaves))
	for _, rs := range slaves {
		if !sm.IsSlaveReadOnly(rs.Name()) {
			result = append(result, rs)
		}
	}
	return result
}

func (sm *SlaveManager) IsSlaveReadOnly(name string) bool {
	policy, ok := sm.getPolicy(name)
	return ok && policy.ReadOnly
}

func (sm *SlaveManager) GetAllSlaves() []*RemoteSlave {
	sm.slavesMu.RLock()
	defer sm.slavesMu.RUnlock()

	var result []*RemoteSlave
	for _, rs := range sm.slaves {
		result = append(result, rs)
	}
	return result
}

func (sm *SlaveManager) GetVFS() *VirtualFileSystem {
	return sm.vfs
}

// SelectSlaveForUpload picks the best slave for an incoming upload.
//
// Selection order:
//  1. Filter slaves to those whose policy matches the upload path/section.
//     (A slave with no policy accepts everything.)
//  2. From eligible slaves, pick the one with the lowest "load score":
//     score = activeTransfers / weight
//     Lower score = less busy relative to capacity.
//  3. Tie-break on most free disk space.
//
// uploadPath may be empty (e.g. legacy callers); in that case all available
// slaves are considered and section affinity is skipped.
func (sm *SlaveManager) SelectSlaveForUpload(uploadPath string) *RemoteSlave {
	if preferred := sm.selectOwnedAncestorSlaveForUpload(uploadPath); preferred != nil {
		return preferred
	}

	slaves := sm.GetAvailableSlaves()
	if len(slaves) == 0 {
		return nil
	}

	section := sectionFromUploadPath(uploadPath)
	eligible := make([]*RemoteSlave, 0, len(slaves))
	weights := make(map[string]int, len(slaves))

	for _, rs := range slaves {
		if !slaveCanStorePath(rs, uploadPath) {
			continue
		}
		policy, hasPolicy := sm.getPolicy(rs.Name())
		if hasPolicy && policy.ReadOnly {
			continue
		}
		if !hasPolicy {
			// No policy = accepts everything
			eligible = append(eligible, rs)
			weights[rs.Name()] = 1
			continue
		}
		if slavePolicyAccepts(policy, section, uploadPath) {
			eligible = append(eligible, rs)
			weights[rs.Name()] = policy.Weight
		}
	}

	// Fallback: if policies excluded everyone, use all writable available
	// slaves. Read-only archive slaves must never receive uploads.
	if len(eligible) == 0 {
		for _, rs := range slaves {
			if !slaveCanStorePath(rs, uploadPath) {
				continue
			}
			if policy, ok := sm.getPolicy(rs.Name()); ok && policy.ReadOnly {
				continue
			}
			eligible = append(eligible, rs)
			weights[rs.Name()] = 1
		}
	}
	if len(eligible) == 0 {
		return nil
	}

	var best *RemoteSlave
	var bestScore float64
	var bestFree int64
	for _, rs := range eligible {
		w := weights[rs.Name()]
		if w < 1 {
			w = 1
		}
		score := float64(rs.ActiveTransfers()) / float64(w)
		ds := rs.GetDiskStatus()
		if best == nil {
			best = rs
			bestScore = score
			bestFree = ds.SpaceAvailable
			continue
		}
		if score < bestScore || (score == bestScore && ds.SpaceAvailable > bestFree) {
			best = rs
			bestScore = score
			bestFree = ds.SpaceAvailable
		}
	}
	return best
}

func (sm *SlaveManager) selectOwnedAncestorSlaveForUpload(uploadPath string) *RemoteSlave {
	if sm == nil || sm.vfs == nil {
		return nil
	}
	cleanPath := path.Clean("/" + strings.TrimSpace(uploadPath))
	if cleanPath == "/" || cleanPath == "." {
		return nil
	}
	for dirPath := path.Dir(cleanPath); ; dirPath = path.Dir(dirPath) {
		entry := sm.vfs.GetFile(dirPath)
		if entry != nil && entry.IsDir && !entry.IsSymlink && strings.TrimSpace(entry.SlaveName) != "" {
			rs := sm.GetSlave(entry.SlaveName)
			if rs != nil && rs.IsAvailable() && !sm.IsSlaveReadOnly(rs.Name()) && slaveCanStorePath(rs, cleanPath) {
				return rs
			}
		}
		if dirPath == "/" || dirPath == "." {
			break
		}
	}
	return nil
}

func slaveCanStorePath(rs *RemoteSlave, uploadPath string) bool {
	if rs == nil {
		return false
	}
	uploadPath = path.Clean("/" + strings.TrimSpace(uploadPath))
	if uploadPath == "." || uploadPath == "/" || strings.TrimSpace(uploadPath) == "" {
		return true
	}
	status := rs.GetDiskStatus()
	if len(status.Roots) == 0 {
		// Older or not-yet-refreshed slaves did not advertise per-root mount
		// paths. Keep them eligible for compatibility and let the slave reject
		// impossible writes if needed.
		return true
	}
	for _, root := range status.Roots {
		mountPath := path.Clean("/" + strings.TrimSpace(root.MountPath))
		if mountPath == "." || mountPath == "" {
			mountPath = "/"
		}
		if mountPath == "/" || uploadPath == mountPath || strings.HasPrefix(uploadPath, mountPath+"/") {
			return true
		}
	}
	return false
}

// sectionFromUploadPath returns the first path component uppercased,
// e.g. "/TV-1080P/Foo.Bar.Baz" -> "TV-1080P".
func sectionFromUploadPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" || p == "/" {
		return ""
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	parts := strings.Split(strings.TrimPrefix(p, "/"), "/")
	if len(parts) == 0 {
		return ""
	}
	return strings.ToUpper(parts[0])
}

// slavePolicyAccepts returns true if the given section or path is allowed
// by the slave's policy. If Sections and Paths are both empty, everything
// is allowed (acts like no policy at all).
func slavePolicyAccepts(policy SlaveRoutePolicy, section, uploadPath string) bool {
	if len(policy.Sections) == 0 && len(policy.Paths) == 0 {
		return true
	}
	if section != "" {
		for _, s := range policy.Sections {
			if s == section {
				return true
			}
		}
	}
	if uploadPath != "" {
		normalizedUploadPath := strings.ToLower(uploadPath)
		for _, pat := range policy.Paths {
			pat = strings.TrimSpace(pat)
			if pat == "" {
				continue
			}
			normalizedPattern := strings.ToLower(pat)
			if ok, _ := path.Match(normalizedPattern, normalizedUploadPath); ok {
				return true
			}
			// Also allow prefix-style patterns like "/TV-1080P/*".
			if strings.HasSuffix(normalizedPattern, "/*") {
				prefix := strings.TrimSuffix(normalizedPattern, "*")
				if strings.HasPrefix(normalizedUploadPath, prefix) {
					return true
				}
			}
		}
	}
	return false
}

// SelectSlaveForDownload picks a slave that has the file.
func (sm *SlaveManager) SelectSlaveForDownload(path string) *RemoteSlave {
	file := sm.vfs.GetFile(path)
	if file == nil {
		return nil
	}

	rs := sm.GetSlave(file.SlaveName)
	if rs != nil && rs.IsAvailable() {
		return rs
	}

	return nil
}

// DeleteOnAllSlaves deletes a path on all slaves ().
func (sm *SlaveManager) DeleteOnAllSlaves(path string) {
	targets := sm.deleteTargetsForPath(path)
	for _, rs := range targets {
		go func(slave *RemoteSlave) {
			index, err := IssueDelete(slave, path)
			if err != nil {
				log.Printf("[SlaveManager] Delete issue error on %s for %s: %v", slave.name, path, err)
				return
			}
			_, err = slave.FetchResponse(index, 5*time.Minute)
			if err != nil {
				if isIgnorableDeleteError(err) {
					return
				}
				log.Printf("[SlaveManager] Delete response error on %s for %s: %v", slave.name, path, err)
			}
		}(rs)
	}

	sm.vfs.DeleteFile(path)
}

func (sm *SlaveManager) deleteTargetsForPath(path string) []*RemoteSlave {
	file := sm.vfs.GetFile(path)
	if file != nil && strings.TrimSpace(file.SlaveName) != "" && !file.IsSymlink {
		if rs := sm.GetSlave(file.SlaveName); rs != nil && rs.IsAvailable() && !sm.IsSlaveReadOnly(rs.Name()) {
			return []*RemoteSlave{rs}
		}
	}
	return sm.GetWritableAvailableSlaves()
}

func isIgnorableDeleteError(err error) bool {
	if err == nil {
		return false
	}
	lower := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(lower, "delete failed: path not found on slave") ||
		strings.Contains(lower, "file not found")
}

func (sm *SlaveManager) Stop() {
	sm.running.Store(false)
	sm.notifyBackgroundRemergeLoop()

	// Save VFS to disk before shutting down
	if err := sm.vfs.SaveToDisk(vfsFilePath); err != nil {
		log.Printf("[SlaveManager] Error saving VFS: %v", err)
	}

	if sm.listener != nil {
		sm.listener.Close()
	}
	sm.slavesMu.RLock()
	for _, rs := range sm.slaves {
		if rs.IsOnline() {
			// Don't send shutdown - slaves should keep running and reconnect
			rs.SetOffline("master shutdown")
		}
	}
	sm.slavesMu.RUnlock()
}

// vfsPersistLoop saves the VFS to disk.
func (sm *SlaveManager) vfsPersistLoop() {
	// Save often enough to survive crashes, but not so often that large VFS
	// snapshots compete with hot upload/race paths during busy periods.
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	for sm.running.Load() {
		<-ticker.C
		if sm.vfs.Count() > 0 {
			if err := sm.vfs.SaveToDisk(vfsFilePath); err != nil {
				log.Printf("[SlaveManager] Error saving VFS: %v", err)
			}
		}
	}
}

func (sm *SlaveManager) diskStatusLoop() {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	for sm.running.Load() {
		<-ticker.C
		sm.PublishAllDiskStatuses()
	}
}

func (sm *SlaveManager) backgroundRemergeLoop() {
	var nextRun time.Time
	for sm.running.Load() {
		cfg := sm.backgroundRemergeSnapshot()
		if cfg.interval <= 0 {
			nextRun = time.Time{}
			sm.waitBackgroundRemergeLoop(30 * time.Second)
			continue
		}

		if nextRun.IsZero() {
			delay := cfg.initialDelay
			if delay <= 0 {
				delay = cfg.interval
			}
			nextRun = time.Now().Add(delay)
			log.Printf("[SlaveManager] Background remerge enabled: interval=%s first_run_in=%s base=%s rootsOnly=%v stagger=%s skipBusy=%v",
				cfg.interval, delay, cfg.basePath, cfg.rootsOnly, cfg.stagger, cfg.skipBusy)
		}

		if wait := time.Until(nextRun); wait > 0 {
			if !sm.waitBackgroundRemergeLoop(wait) {
				nextRun = time.Time{}
			}
			continue
		}

		sm.runBackgroundRemergeRound(cfg)
		nextRun = time.Now().Add(cfg.interval)
	}
}

func (sm *SlaveManager) waitBackgroundRemergeLoop(delay time.Duration) bool {
	if delay <= 0 {
		return true
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-sm.backgroundRemergeWake:
		return false
	}
}

func (sm *SlaveManager) runBackgroundRemergeRound(cfg backgroundRemergeConfig) {
	slaves := sm.GetAllSlaves()
	if len(slaves) == 0 {
		log.Printf("[SlaveManager] Background remerge skipped: no slaves connected")
		return
	}

	started := 0
	skipped := 0
	log.Printf("[SlaveManager] Background remerge round starting: slaves=%d base=%s rootsOnly=%v skipBusy=%v", len(slaves), cfg.basePath, cfg.rootsOnly, cfg.skipBusy)
	for i, rs := range slaves {
		if !sm.running.Load() {
			return
		}
		if rs == nil || !rs.IsOnline() {
			skipped++
			continue
		}
		if rs.IsRemerging() {
			skipped++
			log.Printf("[SlaveManager] Background remerge skipped for %s: remerge already running", rs.Name())
			continue
		}
		if cfg.skipBusy && rs.ActiveTransfers() > 0 {
			skipped++
			log.Printf("[SlaveManager] Background remerge skipped for %s: %d active transfer(s)", rs.Name(), rs.ActiveTransfers())
			continue
		}

		var err error
		if cfg.rootsOnly || cfg.basePath != "/" {
			err = sm.StartRemergePath(rs.Name(), cfg.basePath, cfg.rootsOnly)
		} else {
			err = sm.StartRemerge(rs.Name())
		}
		if err != nil {
			skipped++
			log.Printf("[SlaveManager] Background remerge skipped for %s: %v", rs.Name(), err)
		} else {
			started++
			log.Printf("[SlaveManager] Background remerge started for %s", rs.Name())
		}

		if cfg.stagger > 0 && i < len(slaves)-1 {
			if !sm.waitBackgroundRemergeLoop(cfg.stagger) {
				return
			}
		}
	}
	log.Printf("[SlaveManager] Background remerge round queued: started=%d skipped=%d", started, skipped)
}
