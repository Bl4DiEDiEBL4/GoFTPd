package master

import (
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"goftpd/internal/protocol"
)

// SlaveManager listens for slave connections and manages all RemoteSlave objects.
// 

const vfsFilePath = "userdata/vfs.dat"

type SlaveManager struct {
	listenHost string
	listenPort int
	tlsEnabled bool
	tlsCert    string
	tlsKey     string

	slaves     map[string]*RemoteSlave
	slavesMu   sync.RWMutex

	// Per-slave routing policies (section affinity, weights). Keyed by slave name.
	// Populated from master config via SetSlavePolicies(). Empty = no restrictions.
	policies   map[string]SlaveRoutePolicy
	policiesMu sync.RWMutex

	// Virtual File System: master-side file index
	vfs *VirtualFileSystem

	listener       net.Listener
	running        atomic.Bool
	diskStatusHook func(name string, status protocol.DiskStatus, online, available bool, sections []string)
}

// SlaveRoutePolicy is the runtime form of SlavePolicy from config.
type SlaveRoutePolicy struct {
	Sections []string // uppercased for matching
	Paths    []string // glob patterns
	Weight   int      // >= 1
}

func NewSlaveManager(host string, port int, tlsEnabled bool, tlsCert, tlsKey string) *SlaveManager {
	return &SlaveManager{
		listenHost: host,
		listenPort: port,
		tlsEnabled: tlsEnabled,
		tlsCert:    tlsCert,
		tlsKey:     tlsKey,
		slaves:     make(map[string]*RemoteSlave),
		policies:   make(map[string]SlaveRoutePolicy),
		vfs:        NewVirtualFileSystem(),
	}
}

func (sm *SlaveManager) SetDiskStatusHook(fn func(name string, status protocol.DiskStatus, online, available bool, sections []string)) {
	sm.diskStatusHook = fn
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

		log.Printf("[SlaveManager] Accepted connection from %s", conn.RemoteAddr())
		go sm.handleSlaveConnection(conn)
	}
}

// handleSlaveConnection processes a new slave connection.
// () inner loop.
func (sm *SlaveManager) handleSlaveConnection(conn net.Conn) {
	stream := protocol.NewObjectStream(conn)

	// Read slave name (: RemoteSlave.getSlaveNameFromObjectInput)
	conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	obj, err := stream.ReadObject()
	if err != nil {
		log.Printf("[SlaveManager] Failed to read slave name: %v", err)
		conn.Close()
		return
	}
	conn.SetReadDeadline(time.Time{})

	slaveName, ok := obj.(string)
	if !ok || slaveName == "" {
		log.Printf("[SlaveManager] Invalid slave name from %s", conn.RemoteAddr())
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

	rs := NewRemoteSlave(slaveName, conn, stream)
	sm.slaves[slaveName] = rs
	sm.slavesMu.Unlock()

	log.Printf("[SlaveManager] Slave '%s' connected from %s", slaveName, conn.RemoteAddr())

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
	go sm.initializeSlaveAfterConnect(rs)

	// Start the main read loop (())
	rs.Run(sm)
}

// initializeSlaveAfterConnect triggers remerge and marks slave available.
// Uses "instant online" approach : slave is marked available
// immediately and remerge runs in the background. Files appear in LIST
// as they are indexed.
func (sm *SlaveManager) initializeSlaveAfterConnect(rs *RemoteSlave) {
	log.Printf("[SlaveManager] Starting remerge for slave %s (instant online)", rs.name)

	// Mark available BEFORE remerge so FTP clients can connect immediately.
	// Files will appear in listings as the remerge progresses.
	rs.remerging.Store(true)
	rs.SetAvailable(true)
	sm.publishDiskStatus(rs)
	log.Printf("[SlaveManager] Slave %s is now AVAILABLE (remerge running in background)", rs.name)

	// Mark current files unseen before remerge so stale entries can be purged.
	sm.vfs.MarkAllUnseen(rs.name)

	index, err := IssueRemerge(rs, "/", false, 0, time.Now().UnixMilli(), false)
	if err != nil {
		log.Printf("[SlaveManager] Failed to issue remerge to %s: %v", rs.name, err)
		// Don't take offline — slave is still usable, just no file index yet
		rs.remerging.Store(false)
		return
	}

	// Wait for remerge with no timeout (0 = use default actualTimeout per response,
	// but we pass a very long timeout so large sites can finish)
	_, err = rs.FetchResponse(index, 60*time.Minute)
	if err != nil {
		log.Printf("[SlaveManager] Remerge did not complete for %s: %v (slave stays online)", rs.name, err)
	} else {
		log.Printf("[SlaveManager] Remerge complete for slave %s", rs.name)
		
		// Purge files that were physically deleted from the slave.
		sm.vfs.PurgeUnseen(rs.name)
		log.Printf("[SlaveManager] Ghost files purged for %s", rs.name)

		// Persist the VFS after remerge and purge complete.
		if err := sm.vfs.SaveToDisk(vfsFilePath); err != nil {
			log.Printf("[SlaveManager] Error saving VFS after remerge: %v", err)
		}
	}
	rs.remerging.Store(false)
	sm.publishDiskStatus(rs)
}

// ProcessRemerge handles incoming remerge data from a slave.
func (sm *SlaveManager) ProcessRemerge(rs *RemoteSlave, resp *protocol.AsyncResponseRemerge) {
	log.Printf("[SlaveManager] Remerge from %s: dir=%s files=%d", rs.name, resp.Path, len(resp.Files))

	for _, inode := range resp.Files {
		path := resp.Path
		if path == "/" {
			path = "/" + inode.Name
		} else {
			path = resp.Path + "/" + inode.Name
		}

		// Keep trusted FTP owner/group metadata instead of replacing it with OS ownership.
		owner := inode.Owner
		group := inode.Group
		if existingFile := sm.vfs.GetFile(path); existingFile != nil {
			// ALWAYS trust the Master's VFS owner over the Slave's physical OS owner.
			// This prevents the Slave OS (GoFTPd/ftp) from wiping out real FTP users (N0pe) on restart.
			if existingFile.Owner != "" && existingFile.Owner != "GoFTPd" && existingFile.Owner != "ftp" {
				owner = existingFile.Owner
				group = existingFile.Group
			}
		}

		sm.vfs.AddFile(path, VFSFile{
			Path:         path,
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
	}
}

// --- Slave Access ---

func (sm *SlaveManager) GetSlave(name string) *RemoteSlave {
	sm.slavesMu.RLock()
	defer sm.slavesMu.RUnlock()
	return sm.slaves[name]
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
//        score = activeTransfers / weight
//     Lower score = less busy relative to capacity.
//  3. Tie-break on most free disk space.
//
// uploadPath may be empty (e.g. legacy callers); in that case all available
// slaves are considered and section affinity is skipped.
func (sm *SlaveManager) SelectSlaveForUpload(uploadPath string) *RemoteSlave {
	slaves := sm.GetAvailableSlaves()
	if len(slaves) == 0 {
		return nil
	}

	section := sectionFromUploadPath(uploadPath)
	eligible := make([]*RemoteSlave, 0, len(slaves))
	weights := make(map[string]int, len(slaves))

	for _, rs := range slaves {
		policy, hasPolicy := sm.getPolicy(rs.Name())
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

	// Fallback: if policies excluded everyone, use all available slaves.
	// Better to upload somewhere than fail.
	if len(eligible) == 0 {
		eligible = slaves
		for _, rs := range slaves {
			weights[rs.Name()] = 1
		}
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
		for _, pat := range policy.Paths {
			pat = strings.TrimSpace(pat)
			if pat == "" {
				continue
			}
			if ok, _ := path.Match(pat, uploadPath); ok {
				return true
			}
			// Also allow prefix-style patterns like "/TV-1080P/*"
			if strings.HasSuffix(pat, "/*") {
				prefix := strings.TrimSuffix(pat, "*")
				if strings.HasPrefix(strings.ToLower(uploadPath), strings.ToLower(prefix)) {
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
	for _, rs := range sm.GetAvailableSlaves() {
		go func(slave *RemoteSlave) {
			index, err := IssueDelete(slave, path)
			if err != nil {
				log.Printf("[SlaveManager] Delete issue error on %s: %v", slave.name, err)
				return
			}
			_, err = slave.FetchResponse(index, 5*time.Minute)
			if err != nil {
				log.Printf("[SlaveManager] Delete response error on %s: %v", slave.name, err)
			}
		}(rs)
	}

	sm.vfs.DeleteFile(path)
}

// RenameOnAllSlaves renames on all slaves ().
func (sm *SlaveManager) RenameOnAllSlaves(from, toDir, toName string) {
	for _, rs := range sm.GetAvailableSlaves() {
		go func(slave *RemoteSlave) {
			index, err := IssueRename(slave, from, toDir, toName)
			if err != nil {
				return
			}
			slave.FetchResponse(index, 30*time.Second)
		}(rs)
	}
}

func (sm *SlaveManager) Stop() {
	sm.running.Store(false)

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
	// Save frequently so owner/group metadata and recent VFS changes survive restarts.
	ticker := time.NewTicker(10 * time.Second) 
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
