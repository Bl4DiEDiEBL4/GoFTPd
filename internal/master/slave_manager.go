package master

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
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

	"weaveftpd/internal/core"
	"weaveftpd/internal/protocol"
	"weaveftpd/internal/tlsutil"
	"weaveftpd/internal/zipscript"
)

// SlaveManager listens for slave connections and manages all RemoteSlave objects.
//

const (
	vfsFilePath                         = "userdata/vfs.dat"
	defaultRemergeChecksumThreads       = 1
	maxRemergeChecksumThreads           = 32
	remergeChecksumResponseWaitDuration = 24 * time.Hour
	defaultRemergeResponseWaitDuration  = 60 * time.Minute
	defaultMountedRemergeWaitDuration   = 24 * time.Hour
)

type releaseRaceWindow struct {
	StartMs     int64
	EndMs       int64
	UpdatedAtMs int64
	HasPayload  bool // true once a real file transfer (not just the mkdir seed) set StartMs
}

type backgroundRemergeConfig struct {
	initialDelay time.Duration
	stagger      time.Duration
	jobs         []backgroundRemergeJob
}

type backgroundRemergeJob struct {
	slaveName              string
	name                   string
	interval               time.Duration
	basePath               string
	rootMode               string
	mountPaths             []string
	excludePaths           []string
	delayMS                int
	pauseOnActiveTransfers int
	timeout                time.Duration
	skipBusy               bool
}

type RemergeStatus struct {
	Action          string
	Status          string
	Slave           string
	Job             string
	Path            string
	Roots           string
	Message         string
	Duration        time.Duration
	ActiveTransfers int
	StartedScans    int
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

	// mTLS for the master-slave link. When slaveCACert is set, the listener
	// requires and verifies a client certificate whose CN must match the
	// claimed slave name (see handleSlaveConnection). When unset, masks
	// below are the fallback (and mandatory) authentication mechanism.
	slaveCACert string
	slaveCAPool *x509.CertPool

	masks slaveMasks

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
	remergeHook     func(RemergeStatus)
	authMu          sync.Mutex
	authState       map[string]*slaveAuthState
	remergeCRCJobs  sync.Map
	remergeSFVJobs  sync.Map
	remergeCRCMu    sync.RWMutex
	remergeCRCSem   chan struct{}
	remergeCRCSlots atomic.Int64
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
	Sections    []string                // uppercased for matching
	Paths       []string                // glob patterns
	Weight      int                     // >= 1
	ReadOnly    bool                    // scan/download only; never selected for uploads
	RemergeJobs []SlaveRemergeJobPolicy // background remerge jobs for this slave
}

type SlaveRemergeJobPolicy struct {
	Name                   string
	Enabled                bool
	Interval               time.Duration
	Path                   string
	Roots                  string
	MountPaths             []string
	ExcludePaths           []string
	DelayMS                int
	PauseOnActiveTransfers int
	Timeout                time.Duration
	SkipBusy               bool
}

type remergeCommandOptions struct {
	delayMS                int
	pauseOnActiveTransfers int
	excludePaths           []string
	jobName                string
	timeout                time.Duration
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
		remergeCRCSem:         make(chan struct{}, defaultRemergeChecksumThreads),
		backgroundRemergeWake: make(chan struct{}, 1),
	}
	sm.masks.byName = make(map[string][]string)
	sm.remergeCRCSlots.Store(defaultRemergeChecksumThreads)
	sm.remergeMode.Store("off")
	sm.manualRemergeMode.Store("instant")
	sm.remergePauseAt.Store(250)
	sm.remergeResumeAt.Store(50)
	return sm
}

func (sm *SlaveManager) SetDiskStatusHook(fn func(name string, status protocol.DiskStatus, online, available bool, sections []string)) {
	sm.diskStatusHook = fn
}

func (sm *SlaveManager) SetRemergeStatusHook(fn func(RemergeStatus)) {
	sm.remergeHook = fn
}

func (sm *SlaveManager) publishRemergeStatus(status RemergeStatus) {
	if sm == nil || sm.remergeHook == nil {
		return
	}
	sm.remergeHook(status)
}

func formatRemergeDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	d = d.Truncate(time.Second)
	if d < time.Second {
		return "0s"
	}
	return d.String()
}

func isSlaveRemergeAlreadyRunningError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "remerge already running on slave")
}

func isSlaveRemergeStoppedError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "remerge stopped")
}

func isResponseTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "timeout waiting for response")
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

// ConfigureSlaveMTLS sets the CA certificate used to verify slave client
// certificates on the control-port TLS listener. An empty caCertPath
// disables mTLS enforcement (masks become the mandatory fallback in
// handleSlaveConnection).
func (sm *SlaveManager) ConfigureSlaveMTLS(caCertPath string) error {
	caCertPath = strings.TrimSpace(caCertPath)
	if caCertPath == "" {
		sm.slaveCACert = ""
		sm.slaveCAPool = nil
		return nil
	}
	pool, err := tlsutil.LoadCACertPool(caCertPath)
	if err != nil {
		return err
	}
	sm.slaveCACert = caCertPath
	sm.slaveCAPool = pool
	return nil
}

// mTLSActive reports whether slave client certificates are required and
// verified on this listener.
func (sm *SlaveManager) mTLSActive() bool {
	return sm.slaveCACert != ""
}

// buildListenerTLSConfig builds the slave-control listener's TLS config,
// requiring and verifying a slave client certificate when mTLS is active.
// Extracted from Start() so tests can exercise it against a real
// tls.Listen/tls.Dial pair without needing cert files on disk.
func (sm *SlaveManager) buildListenerTLSConfig(cert tls.Certificate) *tls.Config {
	cfg := &tls.Config{Certificates: []tls.Certificate{cert}}
	if sm.mTLSActive() {
		cfg.ClientCAs = sm.slaveCAPool
		cfg.ClientAuth = tls.RequireAndVerifyClientCert
	}
	return cfg
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

func (sm *SlaveManager) hasBackgroundRemergeJobs(slaveName string) bool {
	policy, ok := sm.getPolicy(slaveName)
	if !ok {
		return false
	}
	for _, job := range policy.RemergeJobs {
		if job.Enabled && job.Interval > 0 {
			return true
		}
	}
	return false
}

// SetSlavePolicies configures per-slave routing rules (section affinity + weights).
// Call once at startup after loading config. Re-calling replaces all policies.
func (sm *SlaveManager) SetSlavePolicies(policies map[string]SlaveRoutePolicy) {
	sm.policiesMu.Lock()
	defer sm.policiesMu.Unlock()
	sm.policies = make(map[string]SlaveRoutePolicy, len(policies))
	backgroundJobs := make([]backgroundRemergeJob, 0)
	for name, p := range policies {
		if p.Weight < 1 {
			p.Weight = 1
		}
		upSections := make([]string, len(p.Sections))
		for i, s := range p.Sections {
			upSections[i] = strings.ToUpper(strings.TrimSpace(s))
		}
		p.Sections = upSections
		p.RemergeJobs = normalizeSlaveRemergeJobs(name, p.RemergeJobs)
		for _, job := range p.RemergeJobs {
			if !job.Enabled || job.Interval <= 0 {
				continue
			}
			backgroundJobs = append(backgroundJobs, backgroundRemergeJob{
				slaveName:              name,
				name:                   job.Name,
				interval:               job.Interval,
				basePath:               job.Path,
				rootMode:               normalizeRemergeRootMode(job.Roots),
				mountPaths:             append([]string(nil), job.MountPaths...),
				excludePaths:           normalizeVFSPathList(job.ExcludePaths),
				delayMS:                maxInt(job.DelayMS, 0),
				pauseOnActiveTransfers: maxInt(job.PauseOnActiveTransfers, 0),
				timeout:                job.Timeout,
				skipBusy:               job.SkipBusy,
			})
		}
		sm.policies[name] = p
	}
	sm.setBackgroundRemergeJobs(backgroundJobs)
}

func normalizeSlaveRemergeJobs(slaveName string, jobs []SlaveRemergeJobPolicy) []SlaveRemergeJobPolicy {
	out := make([]SlaveRemergeJobPolicy, 0, len(jobs))
	for i, job := range jobs {
		job.Name = strings.TrimSpace(job.Name)
		if job.Name == "" {
			job.Name = fmt.Sprintf("job%d", i+1)
		}
		job.Roots = normalizeRemergeRootMode(job.Roots)
		if job.Interval < 0 {
			job.Interval = 0
		}
		if job.DelayMS < 0 {
			job.DelayMS = 0
		}
		if job.PauseOnActiveTransfers < 0 {
			job.PauseOnActiveTransfers = 0
		}
		if job.Timeout < 0 {
			job.Timeout = 0
		}
		job.Path = normalizeRemergeJobPath(job.Path)
		job.MountPaths = normalizeVFSPathList(job.MountPaths)
		job.ExcludePaths = normalizeVFSPathList(job.ExcludePaths)
		if job.Enabled && job.Interval <= 0 {
			log.Printf("[SlaveManager] Background remerge job %s/%s is enabled but interval_seconds is 0; job disabled", slaveName, job.Name)
			job.Enabled = false
		}
		out = append(out, job)
	}
	return out
}

func normalizeRemergeRootMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "normal", "site", "root", "roots":
		return "normal"
	case "mounted", "mount", "mounts", "mounted_roots":
		return "mounted"
	case "all", "both", "":
		return "all"
	default:
		return "all"
	}
}

func normalizeRemergeJobPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return "/"
	}
	return path.Clean("/" + p)
}

func remergeResponseWaitDuration(rootMode string, configured time.Duration) time.Duration {
	if configured > 0 {
		return configured
	}
	if normalizeRemergeRootMode(rootMode) == "mounted" {
		return defaultMountedRemergeWaitDuration
	}
	return defaultRemergeResponseWaitDuration
}

func normalizeVFSPathList(paths []string) []string {
	out := make([]string, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if p == "*" {
			if _, ok := seen[p]; !ok {
				seen[p] = struct{}{}
				out = append(out, p)
			}
			continue
		}
		p = path.Clean("/" + p)
		if p == "." || p == "" {
			p = "/"
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	return out
}

func maxInt(v, min int) int {
	if v < min {
		return min
	}
	return v
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

func (sm *SlaveManager) SetRemergeChecksumThreads(threads int) {
	if sm == nil {
		return
	}
	threads = normalizeRemergeChecksumThreads(threads)
	sm.remergeCRCMu.Lock()
	defer sm.remergeCRCMu.Unlock()
	if current := int(sm.remergeCRCSlots.Load()); current == threads && sm.remergeCRCSem != nil {
		return
	}
	sm.remergeCRCSem = make(chan struct{}, threads)
	sm.remergeCRCSlots.Store(int64(threads))
	log.Printf("[SlaveManager] Remerge checksum workers set to %d", threads)
}

func (sm *SlaveManager) RemergeChecksumThreads() int {
	if sm == nil {
		return defaultRemergeChecksumThreads
	}
	if threads := int(sm.remergeCRCSlots.Load()); threads > 0 {
		return threads
	}
	return defaultRemergeChecksumThreads
}

func (sm *SlaveManager) remergeChecksumSemaphore() chan struct{} {
	if sm == nil {
		return nil
	}
	sm.remergeCRCMu.RLock()
	defer sm.remergeCRCMu.RUnlock()
	return sm.remergeCRCSem
}

func normalizeRemergeChecksumThreads(threads int) int {
	if threads <= 0 {
		return defaultRemergeChecksumThreads
	}
	if threads > maxRemergeChecksumThreads {
		return maxRemergeChecksumThreads
	}
	return threads
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

func (sm *SlaveManager) setBackgroundRemergeJobs(jobs []backgroundRemergeJob) {
	sm.backgroundRemergeMu.Lock()
	sm.backgroundRemerge = backgroundRemergeConfig{
		initialDelay: 5 * time.Minute,
		stagger:      60 * time.Second,
		jobs:         append([]backgroundRemergeJob(nil), jobs...),
	}
	sm.backgroundRemergeMu.Unlock()
	sm.notifyBackgroundRemergeLoop()
}

func (sm *SlaveManager) backgroundRemergeSnapshot() backgroundRemergeConfig {
	sm.backgroundRemergeMu.RLock()
	defer sm.backgroundRemergeMu.RUnlock()
	cfg := sm.backgroundRemerge
	cfg.jobs = append([]backgroundRemergeJob(nil), cfg.jobs...)
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
		if sm.mTLSActive() {
			log.Printf("[SlaveManager] mTLS enforced for slave connections (CA: %s)", sm.slaveCACert)
		} else {
			log.Printf("[SlaveManager] master.slave_ca_cert not set - falling back to per-slave IP mask allowlist")
		}
		listener, err = tls.Listen("tcp", addr, sm.buildListenerTLSConfig(cert))
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
		sm.recordHandshakeReadFailure(ip, remoteAddr, err)
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

	// Tie the claimed slave name to a cryptographic or network identity: an
	// unauthenticated name string is not sufficient on its own.
	// If mTLS is configured, the presented client certificate's CN must match slaveName.
	// Otherwise, the connecting IP must match a mask explicitly registered for slaveName.
	// A slave with zero registered masks is refused, not silently trusted.
	if sm.mTLSActive() {
		tlsConn, ok := conn.(*tls.Conn)
		if !ok {
			log.Printf("[SlaveManager] Slave '%s' from %s did not use TLS while mTLS is required", slaveName, conn.RemoteAddr())
			sm.recordAuthFailure(ip, remoteAddr, "mTLS required but connection is not TLS")
			stream.WriteObject(&protocol.AsyncCommand{Index: "error", Name: "error", Args: []string{"mTLS required"}})
			conn.Close()
			return
		}
		if err := tlsutil.VerifyPeerCommonName(tlsConn.ConnectionState(), slaveName); err != nil {
			log.Printf("[SlaveManager] Slave '%s' from %s failed certificate identity check: %v", slaveName, conn.RemoteAddr(), err)
			sm.recordAuthFailure(ip, remoteAddr, "client certificate does not match slave name")
			sm.publishSecurityEvent(ip, remoteAddr, "deny", fmt.Sprintf("client certificate CN mismatch for slave %q", slaveName), 0, time.Time{})
			stream.WriteObject(&protocol.AsyncCommand{Index: "error", Name: "error", Args: []string{"certificate identity mismatch"}})
			conn.Close()
			return
		}
	} else if !sm.slaveMaskAllows(slaveName, ip) {
		log.Printf("[SlaveManager] Slave '%s' from %s has no matching IP mask registered, rejecting", slaveName, conn.RemoteAddr())
		sm.recordAuthFailure(ip, remoteAddr, "no matching IP mask for slave")
		sm.publishSecurityEvent(ip, remoteAddr, "deny", fmt.Sprintf("no IP mask registered for slave %q", slaveName), 0, time.Time{})
		stream.WriteObject(&protocol.AsyncCommand{Index: "error", Name: "error", Args: []string{"no matching IP mask registered for this slave"}})
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
	if !sm.hasBackgroundRemergeJobs(slaveName) {
		log.Printf("[SlaveManager] Slave %s has no slaves[].remerge.jobs configured; background VFS sync is disabled for this slave", slaveName)
	}

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

func (sm *SlaveManager) recordHandshakeReadFailure(ip, remoteAddr string, err error) {
	if isBenignSlaveHandshakeDisconnect(err) {
		return
	}
	reason := fmt.Sprintf("failed to read slave name: %v", err)
	log.Printf("[SlaveManager] Failed to read slave name from %s: %v", remoteAddr, err)
	sm.recordAuthFailure(ip, remoteAddr, reason)
}

func isBenignSlaveHandshakeDisconnect(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) || errors.Is(err, os.ErrDeadlineExceeded) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "broken pipe") ||
		strings.Contains(msg, "connection reset by peer") ||
		strings.Contains(msg, "use of closed network connection") ||
		strings.Contains(msg, "connection closed") ||
		strings.Contains(msg, "connection aborted") ||
		strings.Contains(msg, "forcibly closed")
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

func (sm *SlaveManager) ClearAuthTempBan(raw string) (bool, error) {
	entry, err := parseAuthNetworkEntry(raw)
	if err != nil {
		return false, err
	}
	sm.authMu.Lock()
	defer sm.authMu.Unlock()
	removed := false
	for ip := range sm.authState {
		parsed := net.ParseIP(ip)
		if parsed == nil {
			continue
		}
		if entry.Net != nil && entry.Net.Contains(parsed) {
			delete(sm.authState, ip)
			removed = true
		}
	}
	return removed, nil
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
			HasPayload:  true,
		}
		return
	}
	// The first real payload replaces the mkdir seed: the seed only marks when
	// the dir was created, not when data actually started flowing. After that,
	// keep the earliest payload start so the window is first-file to last-file.
	if !window.HasPayload || window.StartMs <= 0 || startMs < window.StartMs {
		window.StartMs = startMs
	}
	window.HasPayload = true
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
	sm.initializeSlaveRemerge(rs, "/", "all", false, forceInstantOnline, remergeCommandOptions{})
}

func (sm *SlaveManager) initializeSlaveRemerge(rs *RemoteSlave, basePath string, rootMode string, scoped bool, forceInstantOnline bool, opts remergeCommandOptions) {
	mode := sm.GetRemergeMode()
	instantOnline := forceInstantOnline || mode == "instant"
	rootMode = normalizeRemergeRootMode(rootMode)
	basePath = path.Clean("/" + strings.TrimSpace(basePath))
	if basePath == "." || basePath == "" {
		basePath = "/"
	}
	if !scoped {
		if _, useCachedVFS := sm.startupCachedSlaves.LoadAndDelete(rs.name); useCachedVFS {
			log.Printf("[SlaveManager] Reusing cached VFS for slave %s on startup; skipping initial remerge", rs.name)
			rs.clearActiveRemerge("")
			rs.SetAvailable(true)
			sm.publishDiskStatus(rs)
			sm.ensureBootstrapDirsOnSlave(rs)
			return
		}
	}

	startedAt := time.Now()
	jobName := strings.TrimSpace(opts.jobName)
	if jobName == "" {
		if scoped {
			jobName = "manual"
		} else {
			jobName = "startup"
		}
	}
	log.Printf("[SlaveManager] Requesting remerge for slave %s (mode=%s base=%s roots=%s scoped=%v delay_ms=%d pause_on_active_transfers=%d)",
		rs.name, mode, basePath, rootMode, scoped, opts.delayMS, opts.pauseOnActiveTransfers)
	responseWait := remergeResponseWaitDuration(rootMode, opts.timeout)

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
	} else if rootMode != "normal" {
		sm.vfs.MarkSubtreeUnseen(rs.name, basePath)
	}

	excludePaths := append(sm.getExcludePaths(), opts.excludePaths...)
	index, err := IssueRemerge(rs, basePath, instantOnline, rootMode, RemergeCommandSettings{
		DelayMS:                opts.delayMS,
		PauseOnActiveTransfers: opts.pauseOnActiveTransfers,
	}, excludePaths)
	if err != nil {
		log.Printf("[SlaveManager] Failed to issue remerge to %s: %v", rs.name, err)
		sm.publishRemergeStatus(RemergeStatus{
			Action:   "failed",
			Status:   "failed",
			Slave:    rs.name,
			Job:      jobName,
			Path:     basePath,
			Roots:    rootMode,
			Message:  fmt.Sprintf("remerge failed to start for %s job=%s path=%s: %v", rs.name, jobName, basePath, err),
			Duration: time.Since(startedAt),
		})
		// Don't take offline - slave is still connected, just no file index yet.
		rs.clearActiveRemerge("")
		if !instantOnline {
			rs.SetAvailable(false)
			sm.publishDiskStatus(rs)
		}
		return
	}
	rs.setActiveRemerge(index)
	sm.publishRemergeStatus(RemergeStatus{
		Action:  "requested",
		Status:  "requested",
		Slave:   rs.name,
		Job:     jobName,
		Path:    basePath,
		Roots:   rootMode,
		Message: fmt.Sprintf("remerge requested for %s job=%s path=%s roots=%s", rs.name, jobName, basePath, rootMode),
	})

	// Wait for the final remerge response. Directory snapshots are processed
	// while the slave scans; this wait only controls when the job is considered
	// done or failed.
	remergeComplete := false
	keepRemerging := false
	_, err = rs.FetchResponse(index, responseWait)
	if err != nil {
		action := "failed"
		status := "failed"
		message := fmt.Sprintf("remerge did not complete for %s job=%s path=%s: %v", rs.name, jobName, basePath, err)
		if isSlaveRemergeAlreadyRunningError(err) {
			action = "skipped"
			status = "skipped"
			message = fmt.Sprintf("remerge skipped for %s job=%s path=%s: already running on slave", rs.name, jobName, basePath)
			log.Printf("[SlaveManager] Remerge skipped for %s: already running on slave", rs.name)
		} else if isSlaveRemergeStoppedError(err) {
			action = "stopped"
			status = "stopped"
			message = fmt.Sprintf("remerge stopped for %s job=%s path=%s", rs.name, jobName, basePath)
			log.Printf("[SlaveManager] Remerge stopped for %s", rs.name)
		} else {
			log.Printf("[SlaveManager] Remerge did not complete for %s: %v (slave stays online)", rs.name, err)
			if isResponseTimeoutError(err) {
				if rs.markActiveRemergeTimedOut(index) {
					keepRemerging = true
					message = fmt.Sprintf("remerge did not complete for %s job=%s path=%s: %v; stop requested and slave remains marked remerging until it answers", rs.name, jobName, basePath, err)
				}
				if stopErr := IssueRemergeStop(rs); stopErr != nil {
					log.Printf("[SlaveManager] Failed to stop timed-out remerge for %s: %v", rs.name, stopErr)
				}
			}
		}
		sm.publishRemergeStatus(RemergeStatus{
			Action:   action,
			Status:   status,
			Slave:    rs.name,
			Job:      jobName,
			Path:     basePath,
			Roots:    rootMode,
			Message:  message,
			Duration: time.Since(startedAt),
		})
	} else {
		if err := rs.WaitForRemergeDrain(responseWait); err != nil {
			log.Printf("[SlaveManager] Remerge queue did not drain for %s: %v", rs.name, err)
			sm.publishRemergeStatus(RemergeStatus{
				Action:   "failed",
				Status:   "failed",
				Slave:    rs.name,
				Job:      jobName,
				Path:     basePath,
				Roots:    rootMode,
				Message:  fmt.Sprintf("remerge queue did not drain for %s job=%s path=%s: %v", rs.name, jobName, basePath, err),
				Duration: time.Since(startedAt),
			})
		} else {
			remergeComplete = true
			log.Printf("[SlaveManager] Remerge complete for slave %s", rs.name)

			if !scoped {
				// Purge files that were physically deleted from the slave.
				sm.vfs.PurgeUnseen(rs.name)
				log.Printf("[SlaveManager] Ghost files purged for %s", rs.name)
			} else if rootMode != "normal" {
				sm.vfs.PurgeUnseenSubtree(rs.name, basePath)
				log.Printf("[SlaveManager] Scoped ghost files purged for %s at %s", rs.name, basePath)
			}

			// Persist the VFS after remerge and purge complete.
			if err := sm.vfs.SaveToDisk(vfsFilePath); err != nil {
				log.Printf("[SlaveManager] Error saving VFS after remerge: %v", err)
			}
			sm.publishRemergeStatus(RemergeStatus{
				Action:   "finished",
				Status:   "done",
				Slave:    rs.name,
				Job:      jobName,
				Path:     basePath,
				Roots:    rootMode,
				Message:  fmt.Sprintf("remerge complete for %s job=%s path=%s roots=%s in %s", rs.name, jobName, basePath, rootMode, formatRemergeDuration(time.Since(startedAt))),
				Duration: time.Since(startedAt),
			})
		}
	}
	if !keepRemerging {
		rs.clearActiveRemerge(index)
	}
	if !instantOnline && remergeComplete {
		rs.SetAvailable(true)
	}
	sm.publishDiskStatus(rs)
}

// ProcessRemerge handles incoming remerge data from a slave.
func (sm *SlaveManager) ProcessRemerge(rs *RemoteSlave, resp *protocol.AsyncResponseRemerge) {
	if sm == nil || rs == nil || resp == nil {
		return
	}
	resp.Path = path.Clean("/" + strings.TrimSpace(resp.Path))
	if resp.Path != "/" && resp.Path != "." && !sm.vfs.IsExcludedPath(resp.Path) {
		sm.vfs.AddFile(resp.Path, VFSFile{
			Path:         resp.Path,
			IsDir:        true,
			LastModified: resp.LastModified,
			SlaveName:    rs.name,
			Seen:         true,
		})
	}

	sfvPaths := make([]string, 0, 1)
	touchedReleases := make(map[string]struct{}, 8)
	touchedParents := make(map[string]struct{}, 2)
	presentChildren := make(map[string]struct{}, len(resp.Files))
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
		presentChildren[fullPath] = struct{}{}
		if zipscript.IsStatusMarkerName(sm.statusMarkerConfig().Zipscript, inode.Name) {
			continue
		}

		// Keep trusted FTP owner/group metadata instead of replacing it with OS ownership.
		owner := inode.Owner
		group := inode.Group
		if existingFile := sm.vfs.GetFile(fullPath); existingFile != nil {
			// ALWAYS trust the Master's VFS owner over the Slave's physical OS owner.
			// This prevents the Slave OS (WeaveFTPd/ftp) from wiping out real FTP users (N0pe) on restart.
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
	}
	for _, sfvPath := range sfvPaths {
		sm.scheduleRemergeSFVParse(rs, sfvPath)
	}

	// Each remerge response is a complete snapshot of one physical directory.
	// Prune stale direct children now so slow/background remerges update the VFS
	// as they walk instead of leaving ghosts until the full tree finishes.
	if resp.PruneChildren {
		sm.vfs.PurgeMissingChildren(rs.name, resp.Path, presentChildren, resp.SkippedSubtrees)
	}

	touchedParents[path.Clean(resp.Path)] = struct{}{}
	for dirPath := range touchedParents {
		sm.pruneStaleStatusMarkersForDir(dirPath)
	}
	for releasePath := range touchedReleases {
		sm.syncStatusMarkersForRelease(releasePath)
	}
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
		sm.vfs.HydrateRaceFile(sfvPath, "", "", sfvResp.Size, 0, sfvResp.Checksum)
		sm.syncMissingMarkersAfterRemerge(rs, dirPath, sfvMap)
		sm.syncStatusMarkersForRelease(dirPath)
		sm.scheduleRemergeReleaseChecksumRefresh(rs, dirPath, sfvMap)
	}()
}

func (sm *SlaveManager) scheduleRemergeReleaseChecksumRefresh(rs *RemoteSlave, dirPath string, sfvMap map[string]uint32) {
	if sm == nil || rs == nil || !rs.IsOnline() || !sm.enableRemergeChecksums.Load() || len(sfvMap) == 0 {
		return
	}
	dirPath = path.Clean("/" + strings.TrimSpace(dirPath))
	if dirPath == "." || dirPath == "" {
		return
	}
	jobKey := rs.Name() + "|" + filepath.Clean(dirPath)
	if _, loaded := sm.remergeCRCJobs.LoadOrStore(jobKey, struct{}{}); loaded {
		return
	}
	sfvCopy := make(map[string]uint32, len(sfvMap))
	for name, checksum := range sfvMap {
		sfvCopy[name] = checksum
	}
	go func() {
		defer sm.remergeCRCJobs.Delete(jobKey)
		sem := sm.remergeChecksumSemaphore()
		if sem != nil {
			sem <- struct{}{}
			defer func() { <-sem }()
		}
		sm.refreshRemergeReleaseChecksums(rs, dirPath, sfvCopy)
	}()
}

func (sm *SlaveManager) refreshRemergeReleaseChecksums(rs *RemoteSlave, dirPath string, sfvMap map[string]uint32) {
	targets := sm.remergeChecksumTargets(dirPath, sfvMap)
	if len(targets) == 0 {
		return
	}
	log.Printf("[SlaveManager] Remerge CRC refresh started for %s on %s (%d file(s), workers=%d)",
		dirPath, rs.Name(), len(targets), sm.RemergeChecksumThreads())
	for _, target := range targets {
		if rs == nil || !rs.IsOnline() {
			return
		}
		index, err := IssueChecksum(rs, target.filePath)
		if err != nil {
			return
		}
		resp, err := rs.FetchResponse(index, remergeChecksumResponseWaitDuration)
		if err != nil {
			log.Printf("[SlaveManager] Remerge CRC refresh stopped for %s on %s: %v", target.filePath, rs.Name(), err)
			return
		}
		checksumResp, ok := resp.(*protocol.AsyncResponseChecksum)
		if !ok {
			continue
		}
		if target.expectedCRC != 0 && checksumResp.Checksum != target.expectedCRC {
			log.Printf("[SlaveManager] Remerge CRC mismatch for %s on %s: got %08X expected %08X - keeping file and marking it unverified",
				target.filePath, rs.Name(), checksumResp.Checksum, target.expectedCRC)
		}
		sm.vfs.UpdateFileVerification(target.filePath, checksumResp.Checksum)
		sm.SyncStatusMarkersForPath(target.filePath, false)
	}
	log.Printf("[SlaveManager] Remerge CRC refresh complete for %s on %s", dirPath, rs.Name())
}

type remergeChecksumTarget struct {
	filePath    string
	expectedCRC uint32
}

func (sm *SlaveManager) remergeChecksumTargets(dirPath string, sfvMap map[string]uint32) []remergeChecksumTarget {
	entries := sm.vfs.ListDirectory(dirPath)
	filesByKey := make(map[string]*VFSFile, len(entries))
	for _, entry := range entries {
		if entry == nil || entry.IsDir || entry.IsSymlink || entry.Size <= 0 {
			continue
		}
		filesByKey[raceFileKey(path.Base(entry.Path))] = entry
	}
	keys := make([]string, 0, len(sfvMap))
	for fileName := range sfvMap {
		keys = append(keys, fileName)
	}
	sort.Strings(keys)
	targets := make([]remergeChecksumTarget, 0, len(keys))
	for _, fileName := range keys {
		entry := filesByKey[raceFileKey(fileName)]
		if entry == nil || entry.Checksum != 0 {
			continue
		}
		targets = append(targets, remergeChecksumTarget{
			filePath:    entry.Path,
			expectedCRC: sfvMap[fileName],
		})
	}
	return targets
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

func (sm *SlaveManager) StartRemergeJobs(name string) (int, []string) {
	rs := sm.GetSlave(name)
	if rs == nil {
		sm.publishRemergeStatus(RemergeStatus{
			Action:  "skipped",
			Status:  "skipped",
			Slave:   name,
			Message: fmt.Sprintf("remerge skipped: unknown slave %s", name),
		})
		return 0, []string{fmt.Sprintf("unknown slave %s", name)}
	}
	if !rs.IsOnline() {
		sm.publishRemergeStatus(RemergeStatus{
			Action:  "skipped",
			Status:  "skipped",
			Slave:   rs.Name(),
			Message: fmt.Sprintf("remerge skipped for %s: slave is offline", rs.Name()),
		})
		return 0, []string{fmt.Sprintf("slave %s is offline", rs.Name())}
	}
	if rs.IsRemerging() {
		sm.publishRemergeStatus(RemergeStatus{
			Action:  "skipped",
			Status:  "skipped",
			Slave:   rs.Name(),
			Message: fmt.Sprintf("remerge skipped for %s: already remerging", rs.Name()),
		})
		return 0, []string{fmt.Sprintf("slave %s is already remerging", rs.Name())}
	}
	jobs := sm.backgroundRemergeJobsForSlave(rs.Name())
	if len(jobs) == 0 {
		sm.publishRemergeStatus(RemergeStatus{
			Action:  "skipped",
			Status:  "skipped",
			Slave:   rs.Name(),
			Message: fmt.Sprintf("remerge skipped for %s: no slaves[].remerge.jobs configured", rs.Name()),
		})
		return 0, []string{fmt.Sprintf("slave %s has no slaves[].remerge.jobs configured", rs.Name())}
	}
	allSkipBusy := true
	for _, job := range jobs {
		if !job.skipBusy {
			allSkipBusy = false
			break
		}
	}
	if allSkipBusy && rs.ActiveTransfers() > 0 {
		active := int(rs.ActiveTransfers())
		sm.publishRemergeStatus(RemergeStatus{
			Action:          "skipped",
			Status:          "skipped",
			Slave:           rs.Name(),
			ActiveTransfers: active,
			Message:         fmt.Sprintf("remerge skipped for %s: %d active transfer(s)", rs.Name(), active),
		})
		return 0, []string{fmt.Sprintf("slave %s has %d active transfer(s)", rs.Name(), active)}
	}
	stagger := sm.backgroundRemergeSnapshot().stagger
	go func() {
		for _, job := range jobs {
			sm.runBackgroundRemergeJob(job, stagger)
		}
	}()
	return len(jobs), nil
}

func (sm *SlaveManager) StartRemergeJob(name, jobName, overridePath string) (int, []string) {
	rs := sm.GetSlave(name)
	if rs == nil {
		sm.publishRemergeStatus(RemergeStatus{
			Action:  "skipped",
			Status:  "skipped",
			Slave:   name,
			Job:     jobName,
			Message: fmt.Sprintf("remerge skipped: unknown slave %s", name),
		})
		return 0, []string{fmt.Sprintf("unknown slave %s", name)}
	}
	if !rs.IsOnline() {
		sm.publishRemergeStatus(RemergeStatus{
			Action:  "skipped",
			Status:  "skipped",
			Slave:   rs.Name(),
			Job:     jobName,
			Message: fmt.Sprintf("remerge skipped for %s: slave is offline", rs.Name()),
		})
		return 0, []string{fmt.Sprintf("slave %s is offline", rs.Name())}
	}
	if rs.IsRemerging() {
		sm.publishRemergeStatus(RemergeStatus{
			Action:  "skipped",
			Status:  "skipped",
			Slave:   rs.Name(),
			Job:     jobName,
			Message: fmt.Sprintf("remerge skipped for %s job=%s: already remerging", rs.Name(), jobName),
		})
		return 0, []string{fmt.Sprintf("slave %s is already remerging", rs.Name())}
	}
	job, ok := sm.backgroundRemergeJobForSlave(rs.Name(), jobName)
	if !ok {
		sm.publishRemergeStatus(RemergeStatus{
			Action:  "skipped",
			Status:  "skipped",
			Slave:   rs.Name(),
			Job:     jobName,
			Message: fmt.Sprintf("remerge skipped for %s: job %s is not configured", rs.Name(), jobName),
		})
		return 0, []string{fmt.Sprintf("slave %s has no remerge job %q", rs.Name(), jobName)}
	}
	if job.skipBusy && rs.ActiveTransfers() > 0 {
		active := int(rs.ActiveTransfers())
		sm.publishRemergeStatus(RemergeStatus{
			Action:          "skipped",
			Status:          "skipped",
			Slave:           rs.Name(),
			Job:             job.name,
			ActiveTransfers: active,
			Message:         fmt.Sprintf("remerge skipped for %s job=%s: %d active transfer(s)", rs.Name(), job.name, active),
		})
		return 0, []string{fmt.Sprintf("slave %s has %d active transfer(s)", rs.Name(), active)}
	}
	targets, err := sm.manualRemergeTargetsForJob(rs, job, overridePath)
	if err != nil {
		sm.publishRemergeStatus(RemergeStatus{
			Action:  "skipped",
			Status:  "skipped",
			Slave:   rs.Name(),
			Job:     job.name,
			Message: fmt.Sprintf("remerge skipped for %s job=%s: %v", rs.Name(), job.name, err),
		})
		return 0, []string{err.Error()}
	}
	if len(targets) == 0 {
		return 0, []string{fmt.Sprintf("slave %s job %s has no matching paths", rs.Name(), job.name)}
	}
	stagger := sm.backgroundRemergeSnapshot().stagger
	go sm.runBackgroundRemergeJobTargets(job, targets, stagger)
	return len(targets), nil
}

func (sm *SlaveManager) StartRemergeAllJobs() (int, []string) {
	var errs []string
	started := 0
	slaves := sm.GetAllSlaves()
	if len(slaves) == 0 {
		return 0, []string{"no slaves connected"}
	}
	for _, rs := range slaves {
		n, jobErrs := sm.StartRemergeJobs(rs.Name())
		if len(jobErrs) > 0 {
			errs = append(errs, jobErrs...)
			continue
		}
		started += n
	}
	return started, errs
}

func (sm *SlaveManager) StartRemergeAllJob(jobName, overridePath string) (int, []string) {
	var errs []string
	started := 0
	slaves := sm.GetAllSlaves()
	if len(slaves) == 0 {
		return 0, []string{"no slaves connected"}
	}
	for _, rs := range slaves {
		n, jobErrs := sm.StartRemergeJob(rs.Name(), jobName, overridePath)
		if len(jobErrs) > 0 {
			errs = append(errs, jobErrs...)
			continue
		}
		started += n
	}
	return started, errs
}

func (sm *SlaveManager) backgroundRemergeJobsForSlave(slaveName string) []backgroundRemergeJob {
	cfg := sm.backgroundRemergeSnapshot()
	out := make([]backgroundRemergeJob, 0, len(cfg.jobs))
	for _, job := range cfg.jobs {
		if strings.EqualFold(job.slaveName, slaveName) {
			out = append(out, job)
		}
	}
	return out
}

func (sm *SlaveManager) backgroundRemergeJobForSlave(slaveName, jobName string) (backgroundRemergeJob, bool) {
	jobName = strings.TrimSpace(jobName)
	if jobName == "" {
		return backgroundRemergeJob{}, false
	}
	for _, job := range sm.backgroundRemergeJobsForSlave(slaveName) {
		if strings.EqualFold(job.name, jobName) {
			return job, true
		}
	}
	return backgroundRemergeJob{}, false
}

func (sm *SlaveManager) StopRemerge(name string) error {
	rs := sm.GetSlave(name)
	if rs == nil {
		return fmt.Errorf("unknown slave %s", name)
	}
	if !rs.IsOnline() {
		return fmt.Errorf("slave %s is offline", rs.Name())
	}
	if !rs.IsRemerging() {
		return fmt.Errorf("slave %s is not remerging", rs.Name())
	}
	if err := IssueRemergeStop(rs); err != nil {
		return err
	}
	sm.publishRemergeStatus(RemergeStatus{
		Action:  "stopped",
		Status:  "stopping",
		Slave:   rs.Name(),
		Message: fmt.Sprintf("remerge stop requested for %s", rs.Name()),
	})
	return nil
}

func (sm *SlaveManager) StopRemergeAll() (int, []string) {
	var errs []string
	stopped := 0
	slaves := sm.GetAllSlaves()
	if len(slaves) == 0 {
		return 0, []string{"no slaves connected"}
	}
	for _, rs := range slaves {
		if err := sm.StopRemerge(rs.Name()); err != nil {
			errs = append(errs, err.Error())
			continue
		}
		stopped++
	}
	return stopped, errs
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
			Owner:        "WeaveFTPd",
			Group:        "WeaveFTPd",
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
		if sm.anySlaveRemerging() {
			continue
		}
		if sm.vfs.Count() > 0 {
			if err := sm.vfs.SaveToDisk(vfsFilePath); err != nil {
				log.Printf("[SlaveManager] Error saving VFS: %v", err)
			}
		}
	}
}

func (sm *SlaveManager) anySlaveRemerging() bool {
	if sm == nil {
		return false
	}
	sm.slavesMu.RLock()
	defer sm.slavesMu.RUnlock()
	for _, rs := range sm.slaves {
		if rs != nil && rs.IsRemerging() {
			return true
		}
	}
	return false
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
	nextRuns := make(map[string]time.Time)
	for sm.running.Load() {
		cfg := sm.backgroundRemergeSnapshot()
		if len(cfg.jobs) == 0 {
			nextRuns = make(map[string]time.Time)
			sm.waitBackgroundRemergeLoop(30 * time.Second)
			continue
		}

		wait := sm.runDueBackgroundRemergeJobs(cfg, nextRuns)
		sm.waitBackgroundRemergeLoop(wait)
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

func (sm *SlaveManager) runDueBackgroundRemergeJobs(cfg backgroundRemergeConfig, nextRuns map[string]time.Time) time.Duration {
	now := time.Now()
	validKeys := make(map[string]struct{}, len(cfg.jobs))
	minWait := 30 * time.Second
	for _, job := range cfg.jobs {
		if job.interval <= 0 {
			continue
		}
		key := job.key()
		validKeys[key] = struct{}{}
		nextRun, ok := nextRuns[key]
		if !ok {
			delay := cfg.initialDelay
			if delay <= 0 {
				delay = job.interval
			}
			nextRun = now.Add(delay)
			nextRuns[key] = nextRun
			log.Printf("[SlaveManager] Background remerge job enabled: slave=%s job=%s interval=%s first_run_in=%s roots=%s path=%s",
				job.slaveName, job.name, job.interval, delay, job.rootMode, job.basePath)
		}
		if nextRun.After(now) {
			if wait := time.Until(nextRun); wait > 0 && wait < minWait {
				minWait = wait
			}
			continue
		}
		nextRuns[key] = now.Add(job.interval)
		sm.runBackgroundRemergeJob(job, cfg.stagger)
		if cfg.stagger > 0 {
			minWait = cfg.stagger
		}
	}
	for key := range nextRuns {
		if _, ok := validKeys[key]; !ok {
			delete(nextRuns, key)
		}
	}
	return minWait
}

func (sm *SlaveManager) runBackgroundRemergeJob(job backgroundRemergeJob, stagger time.Duration) {
	rs := sm.GetSlave(job.slaveName)
	if rs == nil || !rs.IsOnline() {
		log.Printf("[SlaveManager] Background remerge skipped: slave=%s job=%s is offline", job.slaveName, job.name)
		sm.publishRemergeStatus(RemergeStatus{
			Action:  "skipped",
			Status:  "skipped",
			Slave:   job.slaveName,
			Job:     job.name,
			Path:    job.basePath,
			Roots:   job.rootMode,
			Message: fmt.Sprintf("background remerge skipped for %s job=%s: slave is offline", job.slaveName, job.name),
		})
		return
	}
	targets := sm.expandBackgroundRemergeTargets(rs, job)
	if len(targets) == 0 {
		log.Printf("[SlaveManager] Background remerge skipped: slave=%s job=%s has no matching paths", job.slaveName, job.name)
		sm.publishRemergeStatus(RemergeStatus{
			Action:  "skipped",
			Status:  "skipped",
			Slave:   job.slaveName,
			Job:     job.name,
			Path:    job.basePath,
			Roots:   job.rootMode,
			Message: fmt.Sprintf("background remerge skipped for %s job=%s: no matching paths", job.slaveName, job.name),
		})
		return
	}
	sm.runBackgroundRemergeJobTargets(job, targets, stagger)
}

func (sm *SlaveManager) runBackgroundRemergeJobTargets(job backgroundRemergeJob, targets []backgroundRemergeTarget, stagger time.Duration) {
	rs := sm.GetSlave(job.slaveName)
	if rs == nil || !rs.IsOnline() {
		return
	}
	for i, target := range targets {
		if !sm.running.Load() {
			return
		}
		if rs.IsRemerging() {
			log.Printf("[SlaveManager] Background remerge skipped: slave=%s job=%s path=%s already remerging", job.slaveName, job.name, target.basePath)
			continue
		}
		if job.skipBusy && rs.ActiveTransfers() > 0 {
			active := int(rs.ActiveTransfers())
			log.Printf("[SlaveManager] Background remerge skipped: slave=%s job=%s path=%s has %d active transfer(s)",
				job.slaveName, job.name, target.basePath, active)
			sm.publishRemergeStatus(RemergeStatus{
				Action:          "skipped",
				Status:          "skipped",
				Slave:           job.slaveName,
				Job:             job.name,
				Path:            target.basePath,
				Roots:           target.rootMode,
				ActiveTransfers: active,
				Message:         fmt.Sprintf("background remerge skipped for %s job=%s path=%s: %d active transfer(s)", job.slaveName, job.name, target.basePath, active),
			})
			continue
		}
		if !rs.remerging.CompareAndSwap(false, true) {
			log.Printf("[SlaveManager] Background remerge skipped: slave=%s job=%s path=%s already remerging", job.slaveName, job.name, target.basePath)
			continue
		}
		log.Printf("[SlaveManager] Background remerge requested: slave=%s job=%s path=%s roots=%s", job.slaveName, job.name, target.basePath, target.rootMode)
		sm.initializeSlaveRemerge(rs, target.basePath, target.rootMode, true, true, remergeCommandOptions{
			delayMS:                job.delayMS,
			pauseOnActiveTransfers: job.pauseOnActiveTransfers,
			excludePaths:           job.excludePaths,
			jobName:                job.name,
			timeout:                job.timeout,
		})
		if stagger > 0 && i < len(targets)-1 {
			if !sm.waitBackgroundRemergeLoop(stagger) {
				return
			}
		}
	}
}

type backgroundRemergeTarget struct {
	basePath string
	rootMode string
}

func (sm *SlaveManager) manualRemergeTargetsForJob(rs *RemoteSlave, job backgroundRemergeJob, overridePath string) ([]backgroundRemergeTarget, error) {
	overridePath = strings.TrimSpace(overridePath)
	if overridePath == "" {
		return sm.expandBackgroundRemergeTargets(rs, job), nil
	}
	basePath := normalizeRemergeJobPath(overridePath)
	rootMode := normalizeRemergeRootMode(job.rootMode)
	if err := validateManualRemergePath(rs, job, basePath); err != nil {
		return nil, err
	}
	return []backgroundRemergeTarget{{basePath: basePath, rootMode: rootMode}}, nil
}

func validateManualRemergePath(rs *RemoteSlave, job backgroundRemergeJob, basePath string) error {
	basePath = normalizeRemergeJobPath(basePath)
	rootMode := normalizeRemergeRootMode(job.rootMode)
	if rootMode != "mounted" {
		jobBase := normalizeRemergeJobPath(job.basePath)
		if jobBase != "/" && basePath != jobBase && !strings.HasPrefix(basePath, jobBase+"/") {
			return fmt.Errorf("path %s is outside job %s base path %s", basePath, job.name, jobBase)
		}
		return nil
	}
	allowed := job.mountPaths
	if len(allowed) == 0 || containsWildcard(allowed) {
		allowed = mountedRootPathsFromDiskStatus(rs.GetDiskStatus())
	}
	if len(allowed) == 0 && job.basePath != "/" {
		allowed = []string{job.basePath}
	}
	for _, mountPath := range normalizeVFSPathList(allowed) {
		if mountPath == "*" {
			continue
		}
		if basePath == mountPath || strings.HasPrefix(basePath, mountPath+"/") {
			return nil
		}
	}
	return fmt.Errorf("path %s is outside mounted paths for job %s", basePath, job.name)
}

func (sm *SlaveManager) expandBackgroundRemergeTargets(rs *RemoteSlave, job backgroundRemergeJob) []backgroundRemergeTarget {
	rootMode := normalizeRemergeRootMode(job.rootMode)
	if rootMode != "mounted" {
		return []backgroundRemergeTarget{{basePath: job.basePath, rootMode: rootMode}}
	}
	paths := job.mountPaths
	if len(paths) == 0 || containsWildcard(paths) {
		paths = mountedRootPathsFromDiskStatus(rs.GetDiskStatus())
	}
	if len(paths) == 0 && job.basePath != "/" {
		paths = []string{job.basePath}
	}
	out := make([]backgroundRemergeTarget, 0, len(paths))
	for _, p := range normalizeVFSPathList(paths) {
		if p == "*" {
			continue
		}
		out = append(out, backgroundRemergeTarget{basePath: p, rootMode: "mounted"})
	}
	return out
}

func containsWildcard(paths []string) bool {
	for _, p := range paths {
		if strings.TrimSpace(p) == "*" {
			return true
		}
	}
	return false
}

func mountedRootPathsFromDiskStatus(status protocol.DiskStatus) []string {
	paths := make([]string, 0, len(status.Roots))
	seen := make(map[string]struct{}, len(status.Roots))
	for _, root := range status.Roots {
		p := path.Clean("/" + strings.TrimSpace(root.MountPath))
		if p == "." || p == "" || p == "/" {
			continue
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return paths
}

func (job backgroundRemergeJob) key() string {
	return strings.Join([]string{job.slaveName, job.name, job.rootMode, job.basePath, strings.Join(job.mountPaths, ",")}, "|")
}
