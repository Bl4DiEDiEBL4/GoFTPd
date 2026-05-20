package master

import (
	"fmt"
	"hash/crc32"
	"io"
	"log"
	"net"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"goftpd/internal/core"
	"goftpd/internal/plugin"
	"goftpd/internal/protocol"
)

// Bridge implements core.MasterBridge by wrapping a SlaveManager.
// It's the glue between the FTP command layer (core) and the slave management layer (master).
type Bridge struct {
	sm     *SlaveManager
	raceDB *RaceDB
	nukeDB *core.NukeHistoryDB

	cacheMu                sync.Mutex
	readFileCache          map[string]cachedReadFileResult
	liveTransferStatsCache []core.LiveTransferStat
	liveTransferStatsAt    time.Time
	transferSpeedPolicy    func(username, primaryGroup, transferPath, direction string) (int64, int64, int64)
}

func configureBridgeDataSocket(conn net.Conn) {
	if tcpConn, ok := conn.(*net.TCPConn); ok {
		_ = tcpConn.SetNoDelay(true)
	}
}

type cachedReadFileResult struct {
	content []byte
	errText string
	expires time.Time
}

var zipPayloadNameRE = regexp.MustCompile(`(?i)\.(zip|z\d\d)$`)

const (
	readFileCacheTTL          = 2 * time.Second
	liveTransferStatsCacheTTL = 1 * time.Second
)

func isZipMainArchivePath(filePath string) bool {
	return strings.HasSuffix(strings.ToLower(strings.TrimSpace(path.Base(filePath))), ".zip")
}

// NewBridge creates a new Bridge adapter.
func NewBridge(sm *SlaveManager) *Bridge {
	b := &Bridge{
		sm:            sm,
		readFileCache: make(map[string]cachedReadFileResult),
	}
	ndb, err := core.GetNukeHistoryDB(false)
	if err != nil {
		log.Printf("[Bridge] Nuke DB disabled: %v", err)
	} else {
		b.nukeDB = ndb
	}
	rdb, err := NewRaceDB("userdata/race.db")
	if err != nil {
		log.Printf("[Bridge] Race DB disabled: %v", err)
		return b
	}
	b.raceDB = rdb
	if err := b.raceDB.Reconcile(sm.GetVFS()); err != nil {
		log.Printf("[Bridge] Race DB reconcile failed: %v", err)
	}
	if hydrated, err := b.raceDB.HydrateVFS(sm.GetVFS()); err != nil {
		log.Printf("[Bridge] Race DB VFS hydrate failed: %v", err)
	} else if hydrated > 0 {
		log.Printf("[Bridge] Hydrated %d persisted race file entries into VFS", hydrated)
	}
	return b
}

func (b *Bridge) SetTransferSpeedPolicy(fn func(username, primaryGroup, transferPath, direction string) (int64, int64, int64)) {
	if b == nil {
		return
	}
	b.transferSpeedPolicy = fn
}

func (b *Bridge) transferSpeedLimits(username, primaryGroup, transferPath, direction string) (int64, int64, int64) {
	if b == nil || b.transferSpeedPolicy == nil {
		return 0, 0, 0
	}
	return b.transferSpeedPolicy(username, primaryGroup, transferPath, direction)
}

func (b *Bridge) StartRemerge(slaveName string) error {
	return b.sm.StartRemerge(slaveName)
}

func (b *Bridge) StartRemergeAll() (int, []string) {
	return b.sm.StartRemergeAll()
}

func (b *Bridge) StartRemergePath(slaveName, basePath string, rootsOnly bool) error {
	return b.sm.StartRemergePath(slaveName, basePath, rootsOnly)
}

func (b *Bridge) StartRemergeAllPath(basePath string, rootsOnly bool) (int, []string) {
	return b.sm.StartRemergeAllPath(basePath, rootsOnly)
}

func (b *Bridge) GetLiveTransferStats() []core.LiveTransferStat {
	return b.getLiveTransferStats(true)
}

func (b *Bridge) getLiveTransferStats(useCache bool) []core.LiveTransferStat {
	if b == nil {
		return nil
	}
	if useCache {
		b.cacheMu.Lock()
		if !b.liveTransferStatsAt.IsZero() && time.Since(b.liveTransferStatsAt) < liveTransferStatsCacheTTL {
			cached := append([]core.LiveTransferStat(nil), b.liveTransferStatsCache...)
			b.cacheMu.Unlock()
			return cached
		}
		b.cacheMu.Unlock()
	}

	var out []core.LiveTransferStat
	for _, slave := range b.sm.GetAllSlaves() {
		if slave == nil || !slave.IsOnline() {
			continue
		}
		idx, err := IssueTransferStats(slave)
		if err != nil {
			continue
		}
		resp, err := slave.FetchResponse(idx, 5*time.Second)
		if err != nil {
			continue
		}
		statsResp, ok := resp.(*protocol.AsyncResponseTransferStats)
		if !ok {
			continue
		}
		for _, stat := range statsResp.Stats {
			direction := ""
			switch stat.Direction {
			case 'R':
				direction = "upload"
			case 'S':
				direction = "download"
			}
			if direction == "" {
				continue
			}
			var startedAt time.Time
			if stat.StartedUnixMs > 0 {
				startedAt = time.UnixMilli(stat.StartedUnixMs)
			}
			out = append(out, core.LiveTransferStat{
				SlaveName:     slave.Name(),
				TransferIndex: stat.TransferIndex,
				Direction:     direction,
				Path:          stat.Path,
				StartedAt:     startedAt,
				Transferred:   stat.Transferred,
				SpeedBytes:    float64(stat.SpeedBytes),
			})
		}
	}
	b.cacheMu.Lock()
	b.liveTransferStatsCache = append([]core.LiveTransferStat(nil), out...)
	b.liveTransferStatsAt = time.Now()
	b.cacheMu.Unlock()
	return out
}

func (b *Bridge) GetAggregateDiskUsage() (freeBytes int64, totalBytes int64, ok bool) {
	if b == nil || b.sm == nil {
		return 0, 0, false
	}
	for _, slave := range b.sm.GetAllSlaves() {
		if slave == nil || !slave.IsOnline() {
			continue
		}
		if b.sm.IsSlaveReadOnly(slave.Name()) {
			continue
		}
		status := slave.GetDiskStatus()
		if status.SpaceCapacity <= 0 {
			continue
		}
		freeBytes += status.SpaceAvailable
		totalBytes += status.SpaceCapacity
		ok = true
	}
	return freeBytes, totalBytes, ok
}

func (b *Bridge) AbortTransfer(slaveName string, transferIndex int32, reason string) bool {
	if b == nil || b.sm == nil || transferIndex == 0 {
		return false
	}
	slave := b.sm.GetSlave(strings.TrimSpace(slaveName))
	if slave == nil || !slave.IsOnline() {
		return false
	}
	if strings.TrimSpace(reason) == "" {
		reason = "aborted by slowkick"
	}
	IssueAbort(slave, transferIndex, reason)
	return true
}

func (b *Bridge) ListSlaveAuthDenyEntries() []string {
	if b == nil || b.sm == nil {
		return nil
	}
	return b.sm.ListAuthDenyEntries()
}

func (b *Bridge) AddSlaveAuthDenyEntry(entry string) (string, error) {
	if b == nil || b.sm == nil {
		return "", fmt.Errorf("master not initialized")
	}
	return b.sm.AddAuthDenyEntry(entry)
}

func (b *Bridge) RemoveSlaveAuthDenyEntry(entry string) (bool, error) {
	if b == nil || b.sm == nil {
		return false, fmt.Errorf("master not initialized")
	}
	return b.sm.RemoveAuthDenyEntry(entry)
}

func (b *Bridge) ListSlaveAuthTempBans() []core.SlaveAuthBanInfo {
	if b == nil || b.sm == nil {
		return nil
	}
	snaps := b.sm.ListAuthTempBans()
	out := make([]core.SlaveAuthBanInfo, 0, len(snaps))
	for _, snap := range snaps {
		out = append(out, core.SlaveAuthBanInfo{
			IP:          snap.IP,
			Strikes:     snap.Strikes,
			BannedUntil: snap.BannedUntil,
		})
	}
	return out
}

func (b *Bridge) RunOnSlaveCommand(dirPath, command string, args []string, env map[string]string, timeoutSeconds int, preferredSlave string) (string, error) {
	slave := b.resolveSlaveForDir(dirPath, preferredSlave)
	if slave == nil {
		return "", fmt.Errorf("no available slave found for %s", dirPath)
	}
	index, err := IssueRunCommand(slave, command, args, env, timeoutSeconds, dirPath)
	if err != nil {
		return "", err
	}
	resp, err := slave.FetchResponse(index, time.Duration(timeoutSeconds+5)*time.Second)
	if err != nil {
		return "", err
	}
	if errResp, ok := resp.(*protocol.AsyncResponseError); ok {
		return "", fmt.Errorf("%s: %s", slave.Name(), errResp.Message)
	}
	if result, ok := resp.(*protocol.AsyncResponseCommandResult); ok {
		return result.Output, nil
	}
	return "", fmt.Errorf("%s: unexpected response type: %T", slave.Name(), resp)
}

func (b *Bridge) resolveSlaveForDir(dirPath, preferredSlave string) *RemoteSlave {
	if strings.TrimSpace(preferredSlave) != "" {
		slave := b.sm.GetSlave(preferredSlave)
		if slave != nil && slave.IsOnline() {
			return slave
		}
	}

	entries := b.ListDir(dirPath)
	for _, entry := range entries {
		if strings.TrimSpace(entry.Slave) == "" {
			continue
		}
		if slave := b.sm.GetSlave(entry.Slave); slave != nil && slave.IsOnline() {
			return slave
		}
	}

	for _, slaveName := range b.sm.GetVFS().GetSlavesForPath(dirPath) {
		if slave := b.sm.GetSlave(slaveName); slave != nil && slave.IsOnline() {
			return slave
		}
	}
	return nil
}

// Ensure Bridge implements MasterBridge at compile time.
var _ core.MasterBridge = (*Bridge)(nil)
var _ plugin.MasterBridge = (*Bridge)(nil)

// ListDir returns directory entries from the master's VFS.
func (b *Bridge) ListDir(dirPath string) []core.MasterFileEntry {
	vfsFiles := b.sm.GetVFS().ListDirectory(dirPath)
	entries := make([]core.MasterFileEntry, 0, len(vfsFiles)+3)
	for _, f := range vfsFiles {
		if f == nil {
			continue
		}
		entries = append(entries, masterEntryFromVFSFile(f))
	}
	entries = append(entries, b.virtualNukeEntries(dirPath)...)
	sort.SliceStable(entries, func(i, j int) bool {
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})
	return entries
}

func masterEntryFromVFSFile(f *VFSFile) core.MasterFileEntry {
	if f == nil {
		return core.MasterFileEntry{}
	}
	return core.MasterFileEntry{
		Name:       filepath.Base(f.Path),
		Size:       f.Size,
		IsDir:      f.IsDir,
		IsSymlink:  f.IsSymlink,
		LinkTarget: f.LinkTarget,
		Mode:       f.Mode,
		ModTime:    f.LastModified,
		Owner:      f.Owner,
		Group:      f.Group,
		Slave:      f.SlaveName,
		XferTime:   f.XferTime,
	}
}

func (b *Bridge) virtualNukeEntries(dirPath string) []core.MasterFileEntry {
	if b == nil || b.nukeDB == nil {
		return nil
	}
	cleanDirPath := path.Clean(dirPath)
	if !strings.HasPrefix(strings.ToUpper(path.Base(cleanDirPath)), "[NUKED]-") {
		return nil
	}
	entry, err := b.nukeDB.FindActiveByPath(cleanDirPath)
	if err != nil || entry == nil {
		return nil
	}
	return nukeVirtualEntriesFromHistory(entry)
}

func nukeVirtualEntriesFromHistory(entry *core.NukeHistoryEntry) []core.MasterFileEntry {
	if entry == nil {
		return nil
	}
	now := entry.NukedAt
	if now <= 0 {
		now = time.Now().Unix()
	}
	owner := strings.TrimSpace(entry.NukedBy)
	if owner == "" {
		owner = "goftpd"
	}
	group := "NUKED"
	multiplier := entry.Multiplier
	if multiplier <= 0 {
		multiplier = 1
	}
	out := make([]core.MasterFileEntry, 0, 3)
	if name := nukeVirtualEntryName("!NUKE", fmt.Sprintf("x%d", multiplier), entry.Reason); name != "" {
		out = append(out, core.MasterFileEntry{
			Name:    name,
			IsDir:   true,
			ModTime: now,
			Owner:   owner,
			Group:   group,
		})
	}
	if nukees := strings.TrimSpace(entry.Nukees); nukees != "" {
		if name := nukeVirtualEntryName("!NUKEES", nukees); name != "" {
			out = append(out, core.MasterFileEntry{
				Name:    name,
				IsDir:   true,
				ModTime: now,
				Owner:   owner,
				Group:   group,
			})
		}
	}
	if name := nukeVirtualEntryName("!NUKER", owner); name != "" {
		out = append(out, core.MasterFileEntry{
			Name:    name,
			IsDir:   true,
			ModTime: now,
			Owner:   owner,
			Group:   group,
		})
	}
	return out
}

func nukeVirtualEntryName(parts ...string) string {
	cleaned := make([]string, 0, len(parts))
	replacer := strings.NewReplacer("/", "-", "\\", "-", ":", "-", "\x00", "", "\r", " ", "\n", " ")
	for _, part := range parts {
		part = strings.TrimSpace(replacer.Replace(part))
		part = strings.Join(strings.Fields(part), " ")
		if part != "" {
			cleaned = append(cleaned, part)
		}
	}
	if len(cleaned) == 0 {
		return ""
	}
	name := strings.Join(cleaned, " - ")
	if len(name) > 200 {
		name = strings.TrimSpace(name[:200])
	}
	return name
}

func (b *Bridge) PluginListDir(dirPath string) []plugin.FileEntry {
	coreEntries := b.ListDir(dirPath)
	entries := make([]plugin.FileEntry, 0, len(coreEntries))
	for _, e := range coreEntries {
		entries = append(entries, plugin.FileEntry{
			Name:       e.Name,
			Size:       e.Size,
			IsDir:      e.IsDir,
			IsSymlink:  e.IsSymlink,
			LinkTarget: e.LinkTarget,
			Mode:       e.Mode,
			ModTime:    e.ModTime,
			Owner:      e.Owner,
			Group:      e.Group,
			Slave:      e.Slave,
		})
	}
	return entries
}

// UploadFile routes an upload from the FTP client to a slave.
//
// Flow ( STOR):
//  1. Select best slave
//  2. Tell slave to LISTEN (open passive data port)
//  3. Connect from master to slave's data port
//  4. Tell slave to RECEIVE the file
//  5. Bridge data: read from clientData, write to slave connection
//
// In  the FTP client connects directly to the slave's PASV port.
// Here we bridge through the master since the client already connected to us.
// A PRET-based optimization (redirect client to slave) can be added later.
func (b *Bridge) UploadFile(filePath string, clientData net.Conn, owner, group string, position int64, transferType byte) (int64, uint32, error) {
	var slave *RemoteSlave
	if position > 0 {
		slave = b.sm.SelectSlaveForDownload(filePath)
		if slave == nil {
			return 0, 0, fmt.Errorf("resume target not found: %s", filePath)
		}
	} else {
		slave = b.sm.SelectSlaveForUpload(filePath)
	}
	if slave == nil {
		return 0, 0, fmt.Errorf("no available slave")
	}

	slave.IncActiveTransfers()
	defer slave.DecActiveTransfers()

	// Tell slave to listen
	listenIdx, err := IssueListen(slave, false, false)
	if err != nil {
		return 0, 0, fmt.Errorf("issue listen to %s: %w", slave.Name(), err)
	}

	resp, err := slave.FetchResponse(listenIdx, 60*time.Second)
	if err != nil {
		return 0, 0, fmt.Errorf("slave %s listen failed: %w", slave.Name(), err)
	}

	transferResp, ok := resp.(*protocol.AsyncResponseTransfer)
	if !ok {
		return 0, 0, fmt.Errorf("unexpected response from slave")
	}

	slaveAddr := net.JoinHostPort(slave.GetPASVIP(), strconv.Itoa(transferResp.Info.Port))
	log.Printf("[Bridge] Connecting to slave %s at %s for upload of %s", slave.Name(), slaveAddr, filePath)

	// Connect to slave's data port
	slaveConn, err := net.DialTimeout("tcp", slaveAddr, 10*time.Second)
	if err != nil {
		return 0, 0, fmt.Errorf("connect to slave data port: %w", err)
	}
	configureBridgeDataSocket(slaveConn)

	// Tell slave to receive the file
	minSpeed, maxSpeed, graceSeconds := b.transferSpeedLimits(owner, group, filePath, "upload")
	recvIdx, err := IssueReceive(slave, filePath, transferType, position, "master",
		transferResp.Info.TransferIndex, minSpeed, maxSpeed, graceSeconds)
	if err != nil {
		slaveConn.Close()
		return 0, 0, fmt.Errorf("issue receive: %w", err)
	}

	// Wait for receive acknowledgement
	_, err = slave.FetchResponse(recvIdx, 60*time.Second)
	if err != nil {
		slaveConn.Close()
		return 0, 0, fmt.Errorf("receive ack: %w", err)
	}

	// Bridge: client -> slave with CRC32 calculation
	bridgeStart := time.Now()
	h := crc32.NewIEEE()
	tee := io.TeeReader(clientData, h)
	written, err := io.Copy(slaveConn, tee)
	xferTime := time.Since(bridgeStart).Milliseconds()
	checksum := h.Sum32()
	slaveConn.Close()

	status, statusErr := slave.WaitTransferStatus(transferResp.Info.TransferIndex, 2*time.Hour)
	if err != nil {
		if statusErr == nil && status.Error != "" {
			b.reconcileFailedUploadState(filePath, slave)
			return status.Transferred, status.Checksum, fmt.Errorf("%s", status.Error)
		}
		if statusErr != nil {
			b.reconcileFailedUploadState(filePath, slave)
		}
		log.Printf("[Bridge] Upload bridge error: %v (wrote %d bytes)", err, written)
		return written, checksum, fmt.Errorf("upload bridge: %w", err)
	}
	if statusErr != nil {
		b.reconcileFailedUploadState(filePath, slave)
		return written, checksum, statusErr
	}
	if status.Error != "" {
		b.reconcileFailedUploadState(filePath, slave)
		return status.Transferred, status.Checksum, fmt.Errorf("%s", status.Error)
	}

	finalSize := finalUploadFileSize(status, position)
	xferTime = status.Elapsed
	checksum = status.Checksum
	if position > 0 {
		finalChecksum, err := b.ChecksumFile(filePath)
		if err != nil {
			return written, checksum, fmt.Errorf("resume checksum: %w", err)
		}
		checksum = finalChecksum
	}

	log.Printf("[Bridge] Uploaded %s to slave %s (%d bytes, %dms, CRC=%08X, offset=%d)", filePath, slave.Name(), finalSize, xferTime, checksum, position)

	// Add file to VFS with transfer timing and checksum
	b.sm.GetVFS().AddFile(filePath, VFSFile{
		Path:         filePath,
		Size:         finalSize,
		IsDir:        false,
		LastModified: time.Now().Unix(),
		SlaveName:    slave.Name(),
		Owner:        owner,
		Group:        group,
		XferTime:     xferTime,
		Checksum:     checksum,
	})
	b.recordUploadMetadata(filePath, owner, group, finalSize, xferTime, checksum)
	b.sm.SyncStatusMarkersForPath(filePath, false)

	return finalSize, checksum, nil
}

func finalUploadFileSize(status protocol.TransferStatus, position int64) int64 {
	if status.FileSize > 0 {
		return status.FileSize
	}
	if position > 0 {
		return status.Transferred + position
	}
	return status.Transferred
}

func (b *Bridge) recordUploadMetadata(filePath, owner, group string, size int64, durationMs int64, checksum uint32) {
	if b == nil || b.raceDB == nil || size <= 0 || durationMs <= 0 {
		return
	}
	if err := b.raceDB.RecordUpload(filepath.Clean(filePath), owner, group, size, durationMs, checksum); err != nil {
		log.Printf("[Bridge] Race DB record upload failed for %s: %v", filePath, err)
	}
}

// DownloadFile routes a download from a slave to the FTP client.
//
// Flow ( RETR):
//  1. Find which slave has the file
//  2. Tell slave to LISTEN
//  3. Connect from master to slave's data port
//  4. Tell slave to SEND the file
//  5. Bridge data: read from slave, write to clientData
func (b *Bridge) DownloadFile(filePath string, clientData net.Conn, username, primaryGroup string, position int64, transferType byte) (uint32, error) {
	slave := b.sm.SelectSlaveForDownload(filePath)
	if slave == nil {
		return 0, fmt.Errorf("file not found on any available slave: %s", filePath)
	}

	// Tell slave to listen
	listenIdx, err := IssueListen(slave, false, false)
	if err != nil {
		return 0, fmt.Errorf("issue listen to %s: %w", slave.Name(), err)
	}

	resp, err := slave.FetchResponse(listenIdx, 60*time.Second)
	if err != nil {
		return 0, fmt.Errorf("slave %s listen failed: %w", slave.Name(), err)
	}

	transferResp, ok := resp.(*protocol.AsyncResponseTransfer)
	if !ok {
		return 0, fmt.Errorf("unexpected response from slave")
	}

	slaveAddr := net.JoinHostPort(slave.GetPASVIP(), strconv.Itoa(transferResp.Info.Port))
	log.Printf("[Bridge] Connecting to slave %s at %s for download of %s", slave.Name(), slaveAddr, filePath)

	slaveConn, err := net.DialTimeout("tcp", slaveAddr, 10*time.Second)
	if err != nil {
		return 0, fmt.Errorf("connect to slave data port: %w", err)
	}
	configureBridgeDataSocket(slaveConn)

	// Tell slave to send the file
	minSpeed, maxSpeed, graceSeconds := b.transferSpeedLimits(username, primaryGroup, filePath, "download")
	sendIdx, err := IssueSend(slave, filePath, transferType, position, "master",
		transferResp.Info.TransferIndex, minSpeed, maxSpeed, graceSeconds)
	if err != nil {
		slaveConn.Close()
		return 0, fmt.Errorf("issue send: %w", err)
	}

	_, err = slave.FetchResponse(sendIdx, 60*time.Second)
	if err != nil {
		slaveConn.Close()
		b.reconcileUnavailableDownloadState(filePath, slave)
		return 0, fmt.Errorf("send ack: %w", err)
	}

	// Bridge: slave -> client
	written, err := io.Copy(clientData, slaveConn)
	slaveConn.Close()
	status, statusErr := slave.WaitTransferStatus(transferResp.Info.TransferIndex, 2*time.Hour)

	if err != nil {
		log.Printf("[Bridge] Download bridge error: %v (wrote %d bytes)", err, written)
		return 0, fmt.Errorf("download bridge: %w", err)
	}
	if statusErr != nil {
		b.reconcileUnavailableDownloadState(filePath, slave)
		return 0, statusErr
	}
	if status.Error != "" {
		b.reconcileUnavailableDownloadState(filePath, slave)
		return 0, fmt.Errorf("%s", status.Error)
	}

	log.Printf("[Bridge] Downloaded %s from slave %s (%d bytes, offset=%d)", filePath, slave.Name(), written, position)
	if position > 0 {
		return 0, nil
	}
	return status.Checksum, nil
}

// DeleteFile deletes from all slaves and VFS.
func (b *Bridge) DeleteFile(filePath string) error {
	vfsFile := b.sm.GetVFS().GetFile(filePath)
	err := b.sm.DeleteFile(filePath)
	if err != nil {
		return err
	}
	b.cleanupZipDizAfterDelete(filePath)
	if b.raceDB != nil {
		isDir := vfsFile != nil && vfsFile.IsDir
		if derr := b.raceDB.DeletePath(filepath.Clean(filePath), isDir); derr != nil {
			log.Printf("[Bridge] Race DB delete sync failed for %s: %v", filePath, derr)
		}
	}
	b.invalidateReadFileCache(filePath)
	b.sm.InvalidateReleaseStateForPath(filePath, vfsFile != nil && vfsFile.IsDir)
	b.sm.SyncStatusMarkersForPath(filePath, vfsFile != nil && vfsFile.IsDir)
	return nil
}

func (b *Bridge) cleanupZipDizAfterDelete(filePath string) {
	if b == nil || b.sm == nil {
		return
	}
	if !isZipMainArchivePath(filePath) {
		return
	}
	dirPath := path.Clean(path.Dir(filePath))
	entries := b.ListDir(dirPath)
	for _, entry := range entries {
		if entry.IsDir || entry.IsSymlink {
			continue
		}
		if strings.HasSuffix(strings.ToLower(strings.TrimSpace(entry.Name)), ".zip") {
			return
		}
	}
	dizPath := path.Join(dirPath, "file_id.diz")
	if b.sm.GetVFS().FileExists(dizPath) {
		if err := b.sm.DeleteFile(dizPath); err != nil && !strings.Contains(strings.ToLower(err.Error()), "not found") {
			log.Printf("[Bridge] zip diz cleanup failed for %s: %v", dirPath, err)
		}
	}
}

// RenameFile renames on the owning slave and VFS.
func (b *Bridge) RenameFile(from, toDir, toName string) error {
	vfsFile := b.sm.GetVFS().GetFile(from)
	toPath := filepath.Join(toDir, toName)
	if err := b.sm.RenameFile(from, toDir, toName); err != nil {
		return err
	}
	if isZipMainArchivePath(from) && !isZipMainArchivePath(toPath) {
		b.cleanupZipDizAfterDelete(from)
	}
	if b.raceDB != nil {
		isDir := vfsFile != nil && vfsFile.IsDir
		if err := b.raceDB.RenamePath(filepath.Clean(from), filepath.Clean(toPath), isDir); err != nil {
			log.Printf("[Bridge] Race DB rename sync failed from %s to %s: %v", from, toPath, err)
		}
	}
	b.sm.RenameReleaseState(from, toPath, vfsFile != nil && vfsFile.IsDir)
	b.sm.SyncStatusMarkersForPath(from, vfsFile != nil && vfsFile.IsDir)
	b.sm.SyncStatusMarkersForPath(toPath, vfsFile != nil && vfsFile.IsDir)
	return nil
}

func (b *Bridge) RelocatePath(from, toDir, toName string) error {
	return b.RelocatePathToSlave(from, toDir, toName, "")
}

func (b *Bridge) RelocatePathToSlave(from, toDir, toName, targetSlave string) error {
	file := b.sm.GetVFS().GetFile(from)
	if file == nil {
		return fmt.Errorf("path not found: %s", from)
	}
	if file.SlaveName == "" {
		return fmt.Errorf("path has no owning slave: %s", from)
	}
	rs := b.sm.GetSlave(file.SlaveName)
	if rs == nil || !rs.IsAvailable() {
		return fmt.Errorf("owning slave unavailable: %s", file.SlaveName)
	}
	toPath := filepath.Clean(path.Join(toDir, toName))
	var destSlave *RemoteSlave
	if strings.TrimSpace(targetSlave) != "" {
		destSlave = b.sm.GetSlave(strings.TrimSpace(targetSlave))
		if destSlave == nil || !destSlave.IsAvailable() {
			return fmt.Errorf("requested destination slave unavailable: %s", strings.TrimSpace(targetSlave))
		}
		if b.sm.IsSlaveReadOnly(destSlave.Name()) {
			return fmt.Errorf("requested destination slave is read-only: %s", destSlave.Name())
		}
	} else {
		destSlave = b.sm.SelectSlaveForUpload(toPath)
	}
	if destSlave == nil || !destSlave.IsAvailable() {
		return fmt.Errorf("no available destination slave for %s", toPath)
	}
	if destSlave.Name() != rs.Name() {
		if err := b.relocateAcrossSlaves(file, rs, destSlave, from, toPath); err != nil {
			return err
		}
		b.sm.GetVFS().RelocateFile(from, toPath, destSlave.Name())
		if b.raceDB != nil {
			isDir := file.IsDir
			if err := b.raceDB.RenamePath(filepath.Clean(from), filepath.Clean(toPath), isDir); err != nil {
				log.Printf("[Bridge] Race DB rename sync failed %s -> %s: %v", from, toPath, err)
			}
		}
		b.sm.RenameReleaseState(from, toPath, file.IsDir)
		b.sm.SyncStatusMarkersForPath(from, file.IsDir)
		b.sm.SyncStatusMarkersForPath(toPath, file.IsDir)
		return nil
	}
	index, err := IssueRelocate(rs, from, toDir, toName)
	if err != nil {
		return err
	}
	resp, err := rs.FetchResponse(index, 30*time.Minute)
	if err != nil {
		return err
	}
	if errResp, ok := resp.(*protocol.AsyncResponseError); ok {
		return fmt.Errorf("%s", errResp.Message)
	}
	b.sm.GetVFS().RenameFile(from, toPath)
	if b.raceDB != nil {
		isDir := file.IsDir
		if err := b.raceDB.RenamePath(filepath.Clean(from), filepath.Clean(toPath), isDir); err != nil {
			log.Printf("[Bridge] Race DB rename sync failed %s -> %s: %v", from, toPath, err)
		}
	}
	b.sm.RenameReleaseState(from, toPath, file.IsDir)
	b.sm.SyncStatusMarkersForPath(from, file.IsDir)
	b.sm.SyncStatusMarkersForPath(toPath, file.IsDir)
	return nil
}

func (b *Bridge) relocateAcrossSlaves(file *VFSFile, sourceSlave, destSlave *RemoteSlave, from, toPath string) error {
	if err := b.createDestDirsOnSlave(destSlave, file, from, toPath); err != nil {
		return err
	}

	filesToCopy := b.collectRelocateFiles(file, from, toPath)
	for _, item := range filesToCopy {
		if err := b.copyFileBetweenSlaves(sourceSlave, destSlave, item.from, item.to, item.owner, item.group, item.size); err != nil {
			_ = b.deletePathOnSlave(destSlave, toPath)
			return err
		}
	}

	if err := b.deletePathOnSlave(sourceSlave, from); err != nil {
		_ = b.deletePathOnSlave(destSlave, toPath)
		return err
	}
	return nil
}

type relocateFileItem struct {
	from  string
	to    string
	owner string
	group string
	size  int64
}

func (b *Bridge) createDestDirsOnSlave(destSlave *RemoteSlave, file *VFSFile, from, toPath string) error {
	if file == nil {
		return fmt.Errorf("nil relocate source")
	}
	if file.IsDir {
		if err := b.makeDirOnSlave(destSlave, toPath); err != nil {
			return err
		}
	}
	fromPrefix := strings.TrimRight(filepath.ToSlash(filepath.Clean(from)), "/") + "/"
	toPrefix := strings.TrimRight(filepath.ToSlash(filepath.Clean(toPath)), "/") + "/"
	for pathKey, vf := range b.sm.GetVFS().GetAllFiles() {
		if vf == nil || !vf.IsDir {
			continue
		}
		cleanPath := filepath.ToSlash(filepath.Clean(pathKey))
		if !strings.HasPrefix(cleanPath, fromPrefix) {
			continue
		}
		rel := strings.TrimPrefix(cleanPath, fromPrefix)
		destDir := filepath.ToSlash(filepath.Clean(toPrefix + rel))
		if err := b.makeDirOnSlave(destSlave, destDir); err != nil {
			return err
		}
	}
	return nil
}

func (b *Bridge) collectRelocateFiles(file *VFSFile, from, toPath string) []relocateFileItem {
	items := []relocateFileItem{}
	addItem := func(src, dst string, vf *VFSFile) {
		if vf == nil || vf.IsDir {
			return
		}
		items = append(items, relocateFileItem{
			from:  filepath.ToSlash(filepath.Clean(src)),
			to:    filepath.ToSlash(filepath.Clean(dst)),
			owner: vf.Owner,
			group: vf.Group,
			size:  vf.Size,
		})
	}
	if file != nil && !file.IsDir {
		addItem(from, toPath, file)
		sort.Slice(items, func(i, j int) bool { return items[i].from < items[j].from })
		return items
	}

	fromPrefix := strings.TrimRight(filepath.ToSlash(filepath.Clean(from)), "/") + "/"
	toPrefix := strings.TrimRight(filepath.ToSlash(filepath.Clean(toPath)), "/") + "/"
	for pathKey, vf := range b.sm.GetVFS().GetAllFiles() {
		if vf == nil || vf.IsDir {
			continue
		}
		cleanPath := filepath.ToSlash(filepath.Clean(pathKey))
		if !strings.HasPrefix(cleanPath, fromPrefix) {
			continue
		}
		rel := strings.TrimPrefix(cleanPath, fromPrefix)
		addItem(cleanPath, toPrefix+rel, vf)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].from < items[j].from })
	return items
}

func (b *Bridge) makeDirOnSlave(slave *RemoteSlave, dirPath string) error {
	index, err := IssueMakeDir(slave, dirPath)
	if err != nil {
		return fmt.Errorf("mkdir on %s failed: %w", slave.Name(), err)
	}
	if _, err := slave.FetchResponse(index, 30*time.Second); err != nil {
		return fmt.Errorf("mkdir on %s failed: %w", slave.Name(), err)
	}
	return nil
}

func (b *Bridge) writableAvailableSlaveByName(slaveName string) (*RemoteSlave, error) {
	if b == nil || b.sm == nil {
		return nil, fmt.Errorf("master not initialized")
	}
	name := strings.TrimSpace(slaveName)
	if name == "" {
		return nil, fmt.Errorf("slave name is empty")
	}
	slave := b.sm.GetSlave(name)
	if slave == nil || !slave.IsAvailable() {
		return nil, fmt.Errorf("requested slave unavailable: %s", name)
	}
	if b.sm.IsSlaveReadOnly(slave.Name()) {
		return nil, fmt.Errorf("requested slave is read-only: %s", slave.Name())
	}
	return slave, nil
}

func (b *Bridge) resolveOwningSlave(path string) (*RemoteSlave, *VFSFile, error) {
	if b == nil || b.sm == nil {
		return nil, nil, fmt.Errorf("master not initialized")
	}
	vfsPath := filepath.ToSlash(filepath.Clean(path))
	file := b.sm.GetVFS().GetFile(vfsPath)
	if file == nil {
		return nil, nil, fmt.Errorf("path not found: %s", vfsPath)
	}
	if strings.TrimSpace(file.SlaveName) == "" {
		return nil, file, nil
	}
	if b.sm.IsSlaveReadOnly(file.SlaveName) {
		return nil, file, fmt.Errorf("path is on read-only slave: %s", vfsPath)
	}
	slave := b.sm.GetSlave(file.SlaveName)
	if slave == nil || !slave.IsAvailable() {
		return nil, file, fmt.Errorf("owning slave unavailable: %s", file.SlaveName)
	}
	return slave, file, nil
}

func (b *Bridge) selectWritableSlaveForCreate(path string) (*RemoteSlave, error) {
	if b == nil || b.sm == nil {
		return nil, fmt.Errorf("master not initialized")
	}
	vfsPath := filepath.ToSlash(filepath.Clean(path))
	if slave, file, err := b.resolveOwningSlave(vfsPath); file != nil {
		if err != nil {
			return nil, err
		}
		if slave != nil && slaveCanStorePath(slave, vfsPath) {
			return slave, nil
		}
		if file.IsDir || file.IsSymlink {
			return nil, fmt.Errorf("path has no owning slave: %s", vfsPath)
		}
	}
	parent := filepath.ToSlash(filepath.Clean(filepath.Dir(vfsPath)))
	if parent != "." && parent != "/" {
		if slave, file, err := b.resolveOwningSlave(parent); file != nil {
			if err != nil {
				return nil, err
			}
			if slave != nil && slaveCanStorePath(slave, vfsPath) {
				return slave, nil
			}
			if !file.IsDir || file.IsSymlink {
				return nil, fmt.Errorf("parent path has no owning slave: %s", parent)
			}
		}
	}
	slave := b.sm.SelectSlaveForUpload(vfsPath)
	if slave == nil {
		return nil, fmt.Errorf("no available slave")
	}
	return slave, nil
}

func (b *Bridge) deletePathOnSlave(slave *RemoteSlave, filePath string) error {
	index, err := IssueDelete(slave, filePath)
	if err != nil {
		return fmt.Errorf("delete on %s failed: %w", slave.Name(), err)
	}
	if _, err := slave.FetchResponse(index, 5*time.Minute); err != nil {
		return fmt.Errorf("delete on %s failed: %w", slave.Name(), err)
	}
	return nil
}

func (b *Bridge) copyFileBetweenSlaves(sourceSlave, destSlave *RemoteSlave, fromPath, toPath, owner, group string, size int64) error {
	listenIdx, err := IssueListen(sourceSlave, false, false)
	if err != nil {
		return fmt.Errorf("source listen failed for %s: %w", fromPath, err)
	}
	listenResp, err := sourceSlave.FetchResponse(listenIdx, 60*time.Second)
	if err != nil {
		return fmt.Errorf("source listen failed for %s: %w", fromPath, err)
	}
	transferResp, ok := listenResp.(*protocol.AsyncResponseTransfer)
	if !ok {
		return fmt.Errorf("unexpected source listen response for %s: %T", fromPath, listenResp)
	}

	remoteAddr := fmt.Sprintf("%s:%d", sourceSlave.GetPASVIP(), transferResp.Info.Port)
	connectIdx, err := IssueConnect(destSlave, sourceSlave.GetPASVIP(), transferResp.Info.Port, false, false)
	if err != nil {
		IssueAbort(sourceSlave, transferResp.Info.TransferIndex, "archive connect failed")
		return fmt.Errorf("destination connect failed for %s: %w", toPath, err)
	}
	connectResp, err := destSlave.FetchResponse(connectIdx, 60*time.Second)
	if err != nil {
		IssueAbort(sourceSlave, transferResp.Info.TransferIndex, "archive connect failed")
		return fmt.Errorf("destination connect failed for %s: %w", toPath, err)
	}
	destTransferResp, ok := connectResp.(*protocol.AsyncResponseTransfer)
	if !ok {
		IssueAbort(sourceSlave, transferResp.Info.TransferIndex, "archive connect failed")
		return fmt.Errorf("unexpected destination connect response for %s: %T", toPath, connectResp)
	}

	sendIdx, err := IssueSend(sourceSlave, fromPath, 'I', 0, remoteAddr, transferResp.Info.TransferIndex, 0, 0, 0)
	if err != nil {
		IssueAbort(sourceSlave, transferResp.Info.TransferIndex, "archive send setup failed")
		IssueAbort(destSlave, destTransferResp.Info.TransferIndex, "archive send setup failed")
		return fmt.Errorf("source send failed for %s: %w", fromPath, err)
	}
	recvIdx, err := IssueReceive(destSlave, toPath, 'I', 0, remoteAddr, destTransferResp.Info.TransferIndex, 0, 0, 0)
	if err != nil {
		IssueAbort(sourceSlave, transferResp.Info.TransferIndex, "archive receive setup failed")
		IssueAbort(destSlave, destTransferResp.Info.TransferIndex, "archive receive setup failed")
		return fmt.Errorf("destination receive failed for %s: %w", toPath, err)
	}
	if _, err := sourceSlave.FetchResponse(sendIdx, 60*time.Second); err != nil {
		IssueAbort(sourceSlave, transferResp.Info.TransferIndex, "archive send ack failed")
		IssueAbort(destSlave, destTransferResp.Info.TransferIndex, "archive send ack failed")
		return fmt.Errorf("source send ack failed for %s: %w", fromPath, err)
	}
	if _, err := destSlave.FetchResponse(recvIdx, 60*time.Second); err != nil {
		IssueAbort(sourceSlave, transferResp.Info.TransferIndex, "archive receive ack failed")
		IssueAbort(destSlave, destTransferResp.Info.TransferIndex, "archive receive ack failed")
		return fmt.Errorf("destination receive ack failed for %s: %w", toPath, err)
	}

	sourceStatus, sourceErr := sourceSlave.WaitTransferStatus(transferResp.Info.TransferIndex, 2*time.Hour)
	destStatus, destErr := destSlave.WaitTransferStatus(destTransferResp.Info.TransferIndex, 2*time.Hour)
	if sourceErr != nil {
		IssueAbort(destSlave, destTransferResp.Info.TransferIndex, "archive source transfer failed")
		return fmt.Errorf("source transfer failed for %s: %w", fromPath, sourceErr)
	}
	if destErr != nil {
		IssueAbort(sourceSlave, transferResp.Info.TransferIndex, "archive destination transfer failed")
		return fmt.Errorf("destination transfer failed for %s: %w", toPath, destErr)
	}
	if sourceStatus.Error != "" {
		return fmt.Errorf("source transfer error for %s: %s", fromPath, sourceStatus.Error)
	}
	if destStatus.Error != "" {
		return fmt.Errorf("destination transfer error for %s: %s", toPath, destStatus.Error)
	}
	destSize := finalUploadFileSize(destStatus, 0)
	if size > 0 && destSize != size {
		return fmt.Errorf("archive copy size mismatch for %s: expected %d got %d", toPath, size, destSize)
	}
	return nil
}

// MakeDir creates a directory on a selected slave and then records it in VFS.
func (b *Bridge) MakeDir(dirPath, owner, group string) error {
	var slave *RemoteSlave
	if parent := b.sm.GetVFS().GetFile(path.Dir(dirPath)); parent != nil && strings.TrimSpace(parent.SlaveName) != "" {
		slave = b.sm.GetSlave(parent.SlaveName)
		if slave != nil && (!slave.IsAvailable() || b.sm.IsSlaveReadOnly(slave.Name())) {
			slave = nil
		}
	}
	if slave == nil {
		slave = b.sm.SelectSlaveForUpload(dirPath)
	}
	if slave == nil {
		return fmt.Errorf("no available slave for mkdir: %s", dirPath)
	}
	if err := b.makeDirOnSlave(slave, dirPath); err != nil {
		return err
	}
	b.sm.MakeDirectoryOnSlave(dirPath, owner, group, slave.Name())
	b.sm.SyncStatusMarkersForPath(dirPath, true)
	return nil
}

func (b *Bridge) MakeDirOnSlave(dirPath, owner, group, slaveName string) error {
	if strings.TrimSpace(slaveName) == "" {
		return b.MakeDir(dirPath, owner, group)
	}
	slave, err := b.writableAvailableSlaveByName(slaveName)
	if err != nil {
		return err
	}
	dirPath = filepath.ToSlash(filepath.Clean(dirPath))
	if err := b.makeDirOnSlave(slave, dirPath); err != nil {
		return err
	}
	b.sm.MakeDirectoryOnSlave(dirPath, owner, group, slave.Name())
	b.sm.SyncStatusMarkersForPath(dirPath, true)
	return nil
}

func (b *Bridge) Symlink(linkPath, targetPath string) error {
	linkPath = filepath.ToSlash(filepath.Clean(linkPath))
	targetPath = filepath.ToSlash(filepath.Clean(targetPath))
	targetResolved := b.ResolvePath(targetPath)

	slave, targetFile, err := b.resolveOwningSlave(targetResolved)
	if err != nil {
		return err
	}
	if targetFile == nil {
		return fmt.Errorf("symlink target not found: %s", targetPath)
	}
	if slave != nil {
		parentDir := filepath.ToSlash(filepath.Clean(filepath.Dir(linkPath)))
		if parentDir != "." && parentDir != "/" {
			if err := b.makeDirOnSlave(slave, parentDir); err != nil {
				return err
			}
		}
		targetArg := strings.TrimPrefix(filepath.ToSlash(filepath.Clean(targetPath)), "/")
		index, err := IssueSymlink(slave, linkPath, targetArg)
		if err != nil {
			return err
		}
		resp, err := slave.FetchResponse(index, 30*time.Second)
		if err != nil {
			return err
		}
		if errResp, ok := resp.(*protocol.AsyncResponseError); ok {
			return fmt.Errorf("%s", errResp.Message)
		}
	}
	b.sm.GetVFS().AddSymlink(linkPath, targetPath)
	return nil
}

func (b *Bridge) VFSSymlink(linkPath, targetPath string) error {
	linkPath = filepath.ToSlash(filepath.Clean(linkPath))
	targetPath = filepath.ToSlash(filepath.Clean(targetPath))
	b.sm.GetVFS().AddSymlink(linkPath, targetPath)
	return nil
}

func (b *Bridge) SyncStatusMarkersForPath(filePath string, isDir bool) {
	if b == nil || b.sm == nil {
		return
	}
	b.sm.SyncStatusMarkersForPath(filePath, isDir)
}

func (b *Bridge) Chmod(path string, mode uint32) error {
	path = filepath.ToSlash(filepath.Clean(path))
	slave, file, err := b.resolveOwningSlave(path)
	if err != nil {
		return err
	}
	if file == nil {
		return fmt.Errorf("path not found: %s", path)
	}
	if slave != nil {
		index, err := IssueChmod(slave, path, mode)
		if err != nil {
			return err
		}
		resp, err := slave.FetchResponse(index, 30*time.Second)
		if err != nil {
			return err
		}
		if errResp, ok := resp.(*protocol.AsyncResponseError); ok {
			return fmt.Errorf("%s", errResp.Message)
		}
	}
	b.sm.GetVFS().Chmod(path, mode)
	return nil
}

// GetFileSize returns file size, or -1 if not found.
func (b *Bridge) GetFileSize(filePath string) int64 {
	f := b.sm.GetVFS().GetFile(filePath)
	if f == nil {
		return -1
	}
	return f.Size
}

func (b *Bridge) GetPathEntry(filePath string) (core.MasterFileEntry, bool) {
	f := b.sm.GetVFS().GetFile(filePath)
	if f == nil {
		return core.MasterFileEntry{}, false
	}
	return masterEntryFromVFSFile(f), true
}

func (b *Bridge) ResolvePath(filePath string) string {
	if b == nil || b.sm == nil || b.sm.GetVFS() == nil {
		return filepath.ToSlash(filepath.Clean(filePath))
	}
	return b.sm.GetVFS().ResolvePath(filePath)
}

// FileExists checks if a path exists in the VFS.
func (b *Bridge) FileExists(filePath string) bool {
	return b.sm.GetVFS().FileExists(filePath)
}

func (b *Bridge) GetKnownChecksum(filePath string) (uint32, bool) {
	f := b.sm.GetVFS().GetFile(filePath)
	if f == nil || f.IsDir || f.Checksum == 0 {
		return 0, false
	}
	return f.Checksum, true
}

// ReadFile reads a small file from a slave (for .message/.imdb display).
func (b *Bridge) ReadFile(filePath string) ([]byte, error) {
	filePath = filepath.ToSlash(filepath.Clean(filePath))
	b.cacheMu.Lock()
	if cached, ok := b.readFileCache[filePath]; ok && time.Now().Before(cached.expires) {
		content := append([]byte(nil), cached.content...)
		errText := cached.errText
		b.cacheMu.Unlock()
		if errText != "" {
			return nil, fmt.Errorf("%s", errText)
		}
		return content, nil
	}
	b.cacheMu.Unlock()

	candidates, candidateErr := b.candidateSlavesForPath(filePath)
	if candidateErr != nil {
		return nil, candidateErr
	}
	var lastErr error
	for _, slave := range candidates {
		index, err := IssueReadFile(slave, filePath)
		if err != nil {
			lastErr = fmt.Errorf("issue readFile to %s: %w", slave.Name(), err)
			continue
		}

		resp, err := slave.FetchResponse(index, 30*time.Second)
		if err != nil {
			lastErr = fmt.Errorf("%s: %w", slave.Name(), err)
			continue
		}
		if errResp, ok := resp.(*protocol.AsyncResponseError); ok {
			lastErr = fmt.Errorf("%s: %s", slave.Name(), errResp.Message)
			continue
		}
		if fc, ok := resp.(*protocol.AsyncResponseFileContent); ok {
			b.cacheReadFileResult(filePath, fc.Content, nil)
			return fc.Content, nil
		}
		lastErr = fmt.Errorf("%s: unexpected response type: %T", slave.Name(), resp)
	}
	if lastErr != nil {
		b.cacheReadFileResult(filePath, nil, lastErr)
		return nil, lastErr
	}
	err := fmt.Errorf("file not found: %s", filePath)
	b.cacheReadFileResult(filePath, nil, err)
	return nil, err
}

func (b *Bridge) ReadZipEntry(archivePath, entryName string) ([]byte, error) {
	candidates, candidateErr := b.candidateSlavesForPath(archivePath)
	if candidateErr != nil {
		return nil, candidateErr
	}
	var lastErr error
	for _, slave := range candidates {
		index, err := IssueReadZipEntry(slave, archivePath, entryName)
		if err != nil {
			lastErr = fmt.Errorf("issue readZipEntry to %s: %w", slave.Name(), err)
			continue
		}

		resp, err := slave.FetchResponse(index, 30*time.Second)
		if err != nil {
			lastErr = fmt.Errorf("%s: %w", slave.Name(), err)
			continue
		}
		if errResp, ok := resp.(*protocol.AsyncResponseError); ok {
			lastErr = fmt.Errorf("%s: %s", slave.Name(), errResp.Message)
			continue
		}
		if fc, ok := resp.(*protocol.AsyncResponseZipEntryContent); ok {
			return fc.Content, nil
		}
		lastErr = fmt.Errorf("%s: unexpected response type: %T", slave.Name(), resp)
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("file not found: %s", archivePath)
}

func (b *Bridge) CheckZipIntegrity(archivePath string) (bool, error) {
	candidates, candidateErr := b.candidateSlavesForPath(archivePath)
	if candidateErr != nil {
		return false, candidateErr
	}
	var lastErr error
	for _, slave := range candidates {
		index, err := IssueZipIntegrity(slave, archivePath)
		if err != nil {
			lastErr = fmt.Errorf("issue zipIntegrity to %s: %w", slave.Name(), err)
			continue
		}

		resp, err := slave.FetchResponse(index, 2*time.Minute)
		if err != nil {
			lastErr = fmt.Errorf("%s: %w", slave.Name(), err)
			continue
		}
		if errResp, ok := resp.(*protocol.AsyncResponseError); ok {
			lastErr = fmt.Errorf("%s: %s", slave.Name(), errResp.Message)
			continue
		}
		if zr, ok := resp.(*protocol.AsyncResponseZipIntegrity); ok {
			return zr.OK, nil
		}
		lastErr = fmt.Errorf("%s: unexpected response type: %T", slave.Name(), resp)
	}
	if lastErr != nil {
		return false, lastErr
	}
	return false, fmt.Errorf("file not found: %s", archivePath)
}

func (b *Bridge) ProbeMediaInfo(filePath, binary string, timeoutSeconds int) (map[string]string, error) {
	if timeoutSeconds <= 0 {
		timeoutSeconds = 20
	}
	candidates, candidateErr := b.candidateSlavesForPath(filePath)
	if candidateErr != nil {
		return nil, candidateErr
	}
	var lastErr error
	for _, slave := range candidates {
		index, err := IssueMediaInfo(slave, filePath, binary, timeoutSeconds)
		if err != nil {
			lastErr = fmt.Errorf("issue media probe to %s: %w", slave.Name(), err)
			continue
		}
		resp, err := slave.FetchResponse(index, time.Duration(timeoutSeconds+5)*time.Second)
		if err != nil {
			lastErr = fmt.Errorf("%s: %w", slave.Name(), err)
			continue
		}
		if errResp, ok := resp.(*protocol.AsyncResponseError); ok {
			lastErr = fmt.Errorf("%s: %s", slave.Name(), errResp.Message)
			continue
		}
		if mi, ok := resp.(*protocol.AsyncResponseMediaInfo); ok {
			return mi.Fields, nil
		}
		lastErr = fmt.Errorf("%s: unexpected response type: %T", slave.Name(), resp)
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("file not found: %s", filePath)
}

// GetSFVInfo asks a slave to parse an SFV file and return the entries.
func (b *Bridge) GetSFVInfo(sfvPath string) (core.SFVInfo, error) {
	candidates, candidateErr := b.candidateSlavesForPath(sfvPath)
	if candidateErr != nil {
		return core.SFVInfo{}, candidateErr
	}
	var lastErr error
	for _, slave := range candidates {
		index, err := IssueSFVFile(slave, sfvPath)
		if err != nil {
			lastErr = fmt.Errorf("issue sfvFile to %s: %w", slave.Name(), err)
			continue
		}

		resp, err := slave.FetchResponse(index, 30*time.Second)
		if err != nil {
			lastErr = fmt.Errorf("%s: %w", slave.Name(), err)
			continue
		}
		if errResp, ok := resp.(*protocol.AsyncResponseError); ok {
			lastErr = fmt.Errorf("%s: %s", slave.Name(), errResp.Message)
			continue
		}

		if sfv, ok := resp.(*protocol.AsyncResponseSFVInfo); ok {
			entries := make([]core.SFVEntryInfo, len(sfv.Entries))
			for i, e := range sfv.Entries {
				entries[i] = core.SFVEntryInfo{FileName: e.FileName, CRC32: e.CRC32}
			}
			return core.SFVInfo{Entries: entries, Checksum: sfv.Checksum}, nil
		}

		lastErr = fmt.Errorf("%s: unexpected response type: %T", slave.Name(), resp)
	}
	if lastErr != nil {
		return core.SFVInfo{}, lastErr
	}
	return core.SFVInfo{}, fmt.Errorf("sfv not found: %s", sfvPath)
}

// WriteFile writes a small file to a slave.
func (b *Bridge) WriteFile(filePath string, content []byte) error {
	filePath = filepath.ToSlash(filepath.Clean(filePath))
	slave, err := b.selectWritableSlaveForCreate(filePath)
	if err != nil {
		return err
	}
	return b.writeFileOnSlave(filePath, content, slave)
}

func (b *Bridge) WriteFileOnSlave(filePath string, content []byte, slaveName string) error {
	if strings.TrimSpace(slaveName) == "" {
		return b.WriteFile(filePath, content)
	}
	filePath = filepath.ToSlash(filepath.Clean(filePath))
	slave, err := b.writableAvailableSlaveByName(slaveName)
	if err != nil {
		return err
	}
	return b.writeFileOnSlave(filePath, content, slave)
}

func (b *Bridge) writeFileOnSlave(filePath string, content []byte, slave *RemoteSlave) error {
	index, err := IssueWriteFile(slave, filePath, string(content))
	if err != nil {
		return fmt.Errorf("issue writeFile: %w", err)
	}

	resp, err := slave.FetchResponse(index, 30*time.Second)
	if err != nil {
		return err
	}
	if errResp, ok := resp.(*protocol.AsyncResponseError); ok {
		return fmt.Errorf("%s", errResp.Message)
	}

	// Add to VFS
	b.sm.GetVFS().AddFile(filePath, VFSFile{
		Path:         filePath,
		Size:         int64(len(content)),
		IsDir:        false,
		LastModified: time.Now().Unix(),
		SlaveName:    slave.Name(),
		Checksum:     crc32.ChecksumIEEE(content),
	})
	b.cacheReadFileResult(filePath, content, nil)
	b.sm.SyncStatusMarkersForPath(filePath, false)

	return nil
}

func (b *Bridge) cacheReadFileResult(filePath string, content []byte, err error) {
	if b == nil {
		return
	}
	cached := cachedReadFileResult{
		content: append([]byte(nil), content...),
		expires: time.Now().Add(readFileCacheTTL),
	}
	if err != nil {
		cached.errText = err.Error()
	}
	b.cacheMu.Lock()
	if b.readFileCache == nil {
		b.readFileCache = make(map[string]cachedReadFileResult)
	}
	b.readFileCache[filePath] = cached
	b.cacheMu.Unlock()
}

func (b *Bridge) invalidateReadFileCache(filePath string) {
	if b == nil {
		return
	}
	filePath = filepath.ToSlash(filepath.Clean(filePath))
	b.cacheMu.Lock()
	delete(b.readFileCache, filePath)
	b.cacheMu.Unlock()
}

func (b *Bridge) CreateSparseFile(filePath string, size int64, owner, group string) error {
	if size < 0 {
		return fmt.Errorf("invalid sparse file size: %d", size)
	}
	filePath = filepath.ToSlash(filepath.Clean(filePath))
	slave, err := b.selectWritableSlaveForCreate(filePath)
	if err != nil {
		return err
	}

	index, err := IssueCreateSparseFile(slave, filePath, size)
	if err != nil {
		return fmt.Errorf("issue createSparseFile: %w", err)
	}
	resp, err := slave.FetchResponse(index, 30*time.Second)
	if err != nil {
		return err
	}
	if errResp, ok := resp.(*protocol.AsyncResponseError); ok {
		return fmt.Errorf("%s", errResp.Message)
	}

	b.sm.GetVFS().AddFile(filePath, VFSFile{
		Path:         filePath,
		Size:         size,
		IsDir:        false,
		LastModified: time.Now().Unix(),
		SlaveName:    slave.Name(),
		Owner:        owner,
		Group:        group,
	})
	b.sm.SyncStatusMarkersForPath(filePath, false)
	return nil
}

func (b *Bridge) ChecksumFile(filePath string) (uint32, error) {
	candidates, candidateErr := b.candidateSlavesForPath(filePath)
	if candidateErr != nil {
		return 0, candidateErr
	}
	var lastErr error
	for _, slave := range candidates {
		index, err := IssueChecksum(slave, filePath)
		if err != nil {
			lastErr = fmt.Errorf("issue checksum to %s: %w", slave.Name(), err)
			continue
		}
		resp, err := slave.FetchResponse(index, 10*time.Minute)
		if err != nil {
			lastErr = fmt.Errorf("%s: %w", slave.Name(), err)
			continue
		}
		if errResp, ok := resp.(*protocol.AsyncResponseError); ok {
			lastErr = fmt.Errorf("%s: %s", slave.Name(), errResp.Message)
			continue
		}
		if checksum, ok := resp.(*protocol.AsyncResponseChecksum); ok {
			return checksum.Checksum, nil
		}
		lastErr = fmt.Errorf("%s: unexpected response type: %T", slave.Name(), resp)
	}
	if lastErr != nil {
		return 0, lastErr
	}
	return 0, fmt.Errorf("file not found: %s", filePath)
}

func (b *Bridge) candidateSlavesForPath(filePath string) ([]*RemoteSlave, error) {
	cleanPath := filepath.ToSlash(filepath.Clean(filePath))
	pathsToCheck := []string{cleanPath}
	if resolved := b.ResolvePath(cleanPath); resolved != "" && resolved != cleanPath {
		pathsToCheck = append(pathsToCheck, resolved)
	}
	for _, candidatePath := range pathsToCheck {
		slave, file, err := b.resolveOwningSlave(candidatePath)
		if file == nil {
			continue
		}
		if err != nil {
			return nil, err
		}
		if slave != nil {
			return []*RemoteSlave{slave}, nil
		}
		return nil, fmt.Errorf("path has no owning slave: %s", cleanPath)
	}

	out := make([]*RemoteSlave, 0, 1)
	seen := map[string]bool{}
	for _, slave := range b.sm.GetAvailableSlaves() {
		if slave == nil || seen[slave.Name()] {
			continue
		}
		out = append(out, slave)
		seen[slave.Name()] = true
	}
	return out, nil
}

func (b *Bridge) MarkFileMissing(filePath string) error {
	b.sm.GetVFS().DeleteFile(filePath)
	var err error
	if b.raceDB != nil {
		err = b.raceDB.DeletePath(filepath.Clean(filePath), false)
	}
	b.sm.InvalidateReleaseStateForPath(filePath, false)
	b.sm.SyncStatusMarkersForPath(filePath, false)
	return err
}

func (b *Bridge) reconcileFailedUploadState(filePath string, slave *RemoteSlave) {
	if b == nil || slave == nil {
		return
	}
	index, err := IssueChecksum(slave, filePath)
	if err != nil {
		return
	}
	resp, err := slave.FetchResponse(index, 30*time.Second)
	if err != nil {
		return
	}
	if _, ok := resp.(*protocol.AsyncResponseChecksum); ok {
		return
	}
	if errResp, ok := resp.(*protocol.AsyncResponseError); ok && isDefinitiveMissingResponse(errResp.Message) {
		_ = b.MarkFileMissing(filePath)
	}
}

func (b *Bridge) reconcileUnavailableDownloadState(filePath string, slave *RemoteSlave) {
	if b == nil || slave == nil {
		return
	}
	index, err := IssueChecksum(slave, filePath)
	if err != nil {
		return
	}
	resp, err := slave.FetchResponse(index, 30*time.Second)
	if err != nil {
		return
	}
	if _, ok := resp.(*protocol.AsyncResponseChecksum); ok {
		return
	}
	if errResp, ok := resp.(*protocol.AsyncResponseError); ok && isDefinitiveMissingResponse(errResp.Message) {
		_ = b.MarkFileMissing(filePath)
	}
}

func isDefinitiveMissingResponse(message string) bool {
	lower := strings.ToLower(strings.TrimSpace(message))
	return strings.Contains(lower, "file not found")
}

func (b *Bridge) SyncPresentFile(filePath string, checksum uint32) error {
	f := b.sm.GetVFS().GetFile(filePath)
	if f == nil || f.IsDir {
		return fmt.Errorf("file not found: %s", filePath)
	}
	b.sm.GetVFS().UpdateFileVerification(filePath, checksum)
	if b.raceDB != nil {
		dirPath := filepath.Dir(filePath)
		meta := b.sm.GetVFS().GetSFVData(dirPath)
		if meta != nil && len(meta.SFVEntries) > 0 {
			if verified := b.sm.GetVFS().GetVerifiedSFVPresentFilesFiltered(dirPath, b.liveUploadingRaceKeysForDir(filepath.Clean(dirPath))); len(verified) == len(meta.SFVEntries) {
				return b.SyncReleaseRaceStats(dirPath)
			}
		}
	}
	return nil
}

// =====================================================================
// VFS ADAPTER FOR PLUGINS (Implements zipscript.FileSystem implicitly)
// =====================================================================

func (b *Bridge) PluginFS() *VFSAdapter {
	return &VFSAdapter{b: b}
}

type VFSAdapter struct {
	b *Bridge
}

func (v *VFSAdapter) ReadDir(dirPath string) ([]os.DirEntry, error) {
	entries := v.b.ListDir(dirPath)
	var result []os.DirEntry
	for _, e := range entries {
		result = append(result, vfsDirEntry{
			info: vfsFileInfo{
				name:    e.Name,
				size:    e.Size,
				isDir:   e.IsDir,
				modTime: time.Unix(e.ModTime, 0),
			},
		})
	}
	return result, nil
}

func (v *VFSAdapter) ReadFile(filePath string) ([]byte, error) {
	return v.b.ReadFile(filePath)
}

func (v *VFSAdapter) Stat(filePath string) (os.FileInfo, error) {
	f := v.b.sm.GetVFS().GetFile(filePath)
	if f == nil {
		return nil, os.ErrNotExist
	}
	return vfsFileInfo{
		name:    filepath.Base(f.Path),
		size:    f.Size,
		isDir:   f.IsDir,
		modTime: time.Unix(f.LastModified, 0),
	}, nil
}

func (v *VFSAdapter) WriteFile(filePath string, data []byte, perm os.FileMode) error {
	return v.b.WriteFile(filePath, data)
}

func (v *VFSAdapter) Remove(filePath string) error {
	return v.b.DeleteFile(filePath)
}

func (v *VFSAdapter) RemoveAll(filePath string) error {
	return v.b.DeleteFile(filePath)
}

func (v *VFSAdapter) MkdirAll(dirPath string, perm os.FileMode) error {
	parentDir := filepath.Dir(dirPath)
	owner := "GoFTPd"
	group := "GoFTPd"

	// Inherit owner/group from the parent directory (e.g., the Release folder)
	parentFile := v.b.sm.GetVFS().GetFile(parentDir)
	if parentFile != nil {
		if parentFile.Owner != "" {
			owner = parentFile.Owner
			group = parentFile.Group
		}
	}

	// Triggers VFS creation AND network broadcast to the slave
	return v.b.MakeDir(dirPath, owner, group)
}

func (v *VFSAdapter) Symlink(oldname, newname string) error {
	return v.b.Symlink(newname, oldname)
}

// --- Virtual File Info & DirEntry Structs ---

type vfsFileInfo struct {
	name    string
	size    int64
	isDir   bool
	modTime time.Time
}

func (f vfsFileInfo) Name() string { return f.name }
func (f vfsFileInfo) Size() int64  { return f.size }
func (f vfsFileInfo) Mode() os.FileMode {
	if f.isDir {
		return os.ModeDir | 0755
	}
	return 0644
}
func (f vfsFileInfo) ModTime() time.Time { return f.modTime }
func (f vfsFileInfo) IsDir() bool        { return f.isDir }
func (f vfsFileInfo) Sys() any           { return nil }

type vfsDirEntry struct {
	info vfsFileInfo
}

func (d vfsDirEntry) Name() string               { return d.info.name }
func (d vfsDirEntry) IsDir() bool                { return d.info.isDir }
func (d vfsDirEntry) Type() os.FileMode          { return d.info.Mode().Type() }
func (d vfsDirEntry) Info() (os.FileInfo, error) { return d.info, nil }

// CacheSFV stores parsed SFV entries on the VFS directory and persists them.
func (b *Bridge) CacheSFV(dirPath string, sfvName string, info core.SFVInfo) {
	sfvMap := make(map[string]uint32, len(info.Entries))
	for _, e := range info.Entries {
		sfvMap[e.FileName] = e.CRC32
	}
	b.sm.GetVFS().SetSFVDataWithChecksum(dirPath, sfvName, info.Checksum, sfvMap)
	if b.raceDB != nil {
		if err := b.raceDB.SaveSFV(filepath.Clean(dirPath), sfvName, sfvMap); err != nil {
			log.Printf("[Bridge] Race DB SFV sync failed for %s: %v", dirPath, err)
		}
	}
	log.Printf("[Bridge] Cached SFV for %s: %d entries", dirPath, len(info.Entries))
}

func (b *Bridge) CacheMediaInfo(dirPath string, fields map[string]string) {
	cleanDirPath := filepath.Clean(dirPath)
	b.sm.SetReleaseMediaInfo(cleanDirPath, fields)
	if b.raceDB != nil {
		if err := b.raceDB.SaveMediaInfo(cleanDirPath, fields); err != nil {
			log.Printf("[Bridge] Race DB media probe sync failed for %s: %v", cleanDirPath, err)
		}
	}
}

func (b *Bridge) ClaimReleaseMetadataAnnouncement(dirPath, key string) bool {
	return b.sm.ClaimReleaseAnnouncement(dirPath, key)
}

// GetVFSRaceStats returns race statistics for a directory,
// counting ONLY files that are listed in the cached SFV data.
func (b *Bridge) GetVFSRaceStats(dirPath string) ([]core.VFSRaceUser, []core.VFSRaceGroup, int64, int, int) {
	cleanDirPath := filepath.Clean(dirPath)
	excludeKeys := b.liveUploadingRaceKeysForDir(cleanDirPath)
	users, groups, totalBytes, present, total := b.sm.GetVFS().GetRaceStatsFiltered(dirPath, excludeKeys)
	return convertRaceStats(users, groups, totalBytes, present, total)
}

func convertRaceStats(users []RaceUserStat, groups []RaceGroupStat, totalBytes int64, present int, total int) ([]core.VFSRaceUser, []core.VFSRaceGroup, int64, int, int) {
	coreUsers := make([]core.VFSRaceUser, len(users))
	for i, u := range users {
		coreUsers[i] = core.VFSRaceUser{
			Name:       u.Name,
			Group:      u.Group,
			Files:      u.Files,
			Bytes:      u.Bytes,
			Speed:      u.Speed,
			PeakSpeed:  u.PeakSpeed,
			SlowSpeed:  u.SlowSpeed,
			Percent:    u.Percent,
			DurationMs: u.DurationMs,
		}
	}
	coreGroups := make([]core.VFSRaceGroup, len(groups))
	for i, g := range groups {
		coreGroups[i] = core.VFSRaceGroup{
			Name:    g.Name,
			Files:   g.Files,
			Bytes:   g.Bytes,
			Speed:   g.Speed,
			Percent: g.Percent,
		}
	}

	return coreUsers, coreGroups, totalBytes, present, total
}

func (b *Bridge) GetVFSRaceStatsFresh(dirPath string) ([]core.VFSRaceUser, []core.VFSRaceGroup, int64, int, int) {
	cleanDirPath := filepath.Clean(dirPath)
	excludeKeys := b.liveUploadingRaceKeysForDirFresh(cleanDirPath)
	users, groups, totalBytes, present, total := b.sm.GetVFS().GetRaceStatsFiltered(dirPath, excludeKeys)

	coreUsers := make([]core.VFSRaceUser, len(users))
	for i, u := range users {
		coreUsers[i] = core.VFSRaceUser{
			Name:       u.Name,
			Group:      u.Group,
			Files:      u.Files,
			Bytes:      u.Bytes,
			Speed:      u.Speed,
			PeakSpeed:  u.PeakSpeed,
			SlowSpeed:  u.SlowSpeed,
			Percent:    u.Percent,
			DurationMs: u.DurationMs,
		}
	}
	coreGroups := make([]core.VFSRaceGroup, len(groups))
	for i, g := range groups {
		coreGroups[i] = core.VFSRaceGroup{
			Name:    g.Name,
			Files:   g.Files,
			Bytes:   g.Bytes,
			Speed:   g.Speed,
			Percent: g.Percent,
		}
	}

	return coreUsers, coreGroups, totalBytes, present, total
}

func (b *Bridge) PluginGetVFSRaceStats(dirPath string) ([]plugin.RaceUser, []plugin.RaceGroup, int64, int, int) {
	coreUsers, coreGroups, totalBytes, present, total := b.GetVFSRaceStats(dirPath)
	users := make([]plugin.RaceUser, 0, len(coreUsers))
	for _, u := range coreUsers {
		users = append(users, plugin.RaceUser{
			Name:    u.Name,
			Group:   u.Group,
			Files:   u.Files,
			Bytes:   u.Bytes,
			Speed:   u.Speed,
			Percent: u.Percent,
		})
	}
	groups := make([]plugin.RaceGroup, 0, len(coreGroups))
	for _, g := range coreGroups {
		groups = append(groups, plugin.RaceGroup{
			Name:    g.Name,
			Files:   g.Files,
			Bytes:   g.Bytes,
			Speed:   g.Speed,
			Percent: g.Percent,
		})
	}
	return users, groups, totalBytes, present, total
}

// GetRaceWallClockMilliseconds returns wall-clock race duration (first file
// start to last file end) in milliseconds. 0 if race db unavailable or dir
// unknown.
func (b *Bridge) GetRaceWallClockMilliseconds(dirPath string) int64 {
	return b.sm.GetReleaseRaceWindowMilliseconds(filepath.Clean(dirPath))
}

func (b *Bridge) StartReleaseRaceWindow(dirPath string, startMs int64) {
	if b == nil || b.sm == nil {
		return
	}
	b.sm.StartReleaseRaceWindowAt(filepath.Clean(dirPath), startMs)
}

func (b *Bridge) NoteRacePayloadTransfer(dirPath, fileName string, durationMs int64) {
	b.NoteRacePayloadTransferAt(dirPath, fileName, durationMs, 0)
}

func (b *Bridge) NoteRacePayloadTransferAt(dirPath, fileName string, durationMs int64, endMs int64) {
	cleanDirPath := filepath.Clean(dirPath)
	if endMs <= 0 && b != nil && b.sm != nil {
		if f := b.sm.GetVFS().GetFile(path.Join(cleanDirPath, fileName)); f != nil && f.LastModified > 0 {
			endMs = f.LastModified * 1000
		}
	}
	b.sm.NoteRacePayloadTransferAt(cleanDirPath, durationMs, endMs)
}

func (b *Bridge) SyncReleaseRaceStats(dirPath string) error {
	if b == nil || b.raceDB == nil || b.sm == nil {
		return nil
	}
	cleanDirPath := filepath.Clean(dirPath)
	meta := b.sm.GetVFS().GetSFVData(cleanDirPath)
	if meta == nil || len(meta.SFVEntries) == 0 {
		return nil
	}

	verified := b.sm.GetVFS().GetVerifiedSFVPresentFilesFiltered(cleanDirPath, b.liveUploadingRaceKeysForDir(cleanDirPath))
	if len(verified) == 0 {
		return nil
	}

	files := make(map[string]ReleaseFileRecord, len(verified))
	for _, entry := range b.ListDir(cleanDirPath) {
		if entry.IsDir {
			continue
		}
		key := raceDBFileKey(entry.Name)
		if !verified[key] {
			continue
		}
		f := b.sm.GetVFS().GetFile(path.Join(cleanDirPath, entry.Name))
		if f == nil || f.IsDir {
			continue
		}
		files[key] = ReleaseFileRecord{
			FileName:   key,
			Owner:      f.Owner,
			Group:      f.Group,
			SizeBytes:  f.Size,
			DurationMs: f.XferTime,
			Checksum:   f.Checksum,
		}
	}
	return b.raceDB.ReplaceReleaseFiles(cleanDirPath, meta.SFVName, meta.SFVEntries, files)
}

// GetSFVData returns cached SFV entries for a directory.
func (b *Bridge) GetSFVData(dirPath string) map[string]uint32 {
	meta := b.sm.GetVFS().GetSFVData(dirPath)
	if meta == nil {
		return nil
	}
	return meta.SFVEntries
}

func (b *Bridge) GetReleaseStatus(dirPath string) (core.ReleaseStatus, bool) {
	if b == nil || b.sm == nil || b.sm.GetVFS() == nil {
		return core.ReleaseStatus{}, false
	}
	return b.sm.GetVFS().GetReleaseStatus(dirPath)
}

func (b *Bridge) GetZipExpectedParts(dirPath string) (int, bool) {
	if b == nil || b.sm == nil || b.sm.GetVFS() == nil {
		return 0, false
	}
	return b.sm.GetVFS().GetZipExpectedParts(dirPath)
}

func (b *Bridge) CacheZipExpectedParts(dirPath string, expected int, dizChecksum uint32) {
	if b == nil || b.sm == nil || b.sm.GetVFS() == nil {
		return
	}
	b.sm.GetVFS().CacheZipExpectedParts(dirPath, expected, dizChecksum)
}

func (b *Bridge) GetRequestData(dirPath string) ([]plugin.RequestRecord, []plugin.RequestFillRecord) {
	if b == nil || b.sm == nil || b.sm.GetVFS() == nil {
		return nil, nil
	}
	return b.sm.GetVFS().GetRequestData(dirPath)
}

func (b *Bridge) SetRequestData(dirPath string, requests []plugin.RequestRecord, fills []plugin.RequestFillRecord) {
	if b == nil || b.sm == nil || b.sm.GetVFS() == nil {
		return
	}
	b.sm.GetVFS().SetRequestData(dirPath, requests, fills)
}

func (b *Bridge) GetVerifiedSFVPresentFiles(dirPath string) map[string]bool {
	if b == nil || b.sm == nil {
		return nil
	}
	cleanDirPath := filepath.Clean(dirPath)
	return b.sm.GetVFS().GetVerifiedSFVPresentFilesFiltered(cleanDirPath, b.liveUploadingRaceKeysForDir(cleanDirPath))
}

func (b *Bridge) liveUploadingRaceKeysForDir(dirPath string) map[string]bool {
	byDir := b.liveUploadingRaceKeysByDir(filepath.Clean(dirPath), true)
	return byDir[filepath.Clean(dirPath)]
}

func (b *Bridge) liveUploadingRaceKeysForDirFresh(dirPath string) map[string]bool {
	byDir := b.liveUploadingRaceKeysByDir(filepath.Clean(dirPath), false)
	return byDir[filepath.Clean(dirPath)]
}

func (b *Bridge) liveUploadingRaceKeysByDir(rootDir string, useCache bool) map[string]map[string]bool {
	if b == nil {
		return nil
	}
	rootDir = filepath.Clean(rootDir)
	rootPrefix := strings.TrimRight(filepath.ToSlash(rootDir), "/") + "/"
	out := make(map[string]map[string]bool)
	for _, stat := range b.getLiveTransferStats(useCache) {
		if stat.Direction != "upload" {
			continue
		}
		cleanPath := filepath.Clean(stat.Path)
		parentDir := filepath.Clean(filepath.Dir(cleanPath))
		parentSlash := filepath.ToSlash(parentDir)
		if parentDir != rootDir && !strings.HasPrefix(parentSlash+"/", rootPrefix) {
			continue
		}
		key := raceFileKey(filepath.Base(cleanPath))
		if out[parentDir] == nil {
			out[parentDir] = make(map[string]bool)
		}
		out[parentDir][key] = true
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (b *Bridge) GetDirMediaInfo(dirPath string) map[string]string {
	cleanDirPath := filepath.Clean(dirPath)
	if cached := b.sm.GetReleaseMediaInfo(cleanDirPath); len(cached) > 0 {
		return cached
	}
	if b.raceDB == nil {
		return nil
	}
	fields := b.raceDB.GetMediaInfo(cleanDirPath)
	if len(fields) == 0 {
		return nil
	}
	b.sm.SetReleaseMediaInfo(cleanDirPath, fields)
	return fields
}

func (b *Bridge) SearchDirs(query string, limit int) []core.VFSSearchResult {
	if b.raceDB != nil {
		if results := b.raceDB.SearchDirs(query, limit); len(results) > 0 {
			return results
		}
	}
	vfsResults := b.sm.GetVFS().SearchDirs(query, limit)
	results := make([]core.VFSSearchResult, 0, len(vfsResults))
	for _, r := range vfsResults {
		results = append(results, core.VFSSearchResult{
			Path:    r.Path,
			Files:   r.Files,
			Bytes:   r.Bytes,
			ModTime: r.ModTime,
		})
	}
	return results
}

// SlaveListenForPassthrough asks a slave to open a listener for direct client connection.
// uploadPath may be empty if the eventual upload path is not yet known (pure PASV);
// pass it when possible so section-affinity routing picks the right slave.
// Returns the slave's public IP, the port it's listening on, and the transfer index.
func (b *Bridge) SlaveListenForPassthrough(uploadPath string, encrypted bool, sslClientMode bool) (string, int, int32, string, error) {
	slave := b.sm.SelectSlaveForUpload(uploadPath)
	if slave == nil {
		return "", 0, 0, "", fmt.Errorf("no available slave")
	}

	listenIdx, err := IssueListen(slave, encrypted, sslClientMode)
	if err != nil {
		return "", 0, 0, "", fmt.Errorf("issue listen to %s: %w", slave.Name(), err)
	}

	resp, err := slave.FetchResponse(listenIdx, 60*time.Second)
	if err != nil {
		return "", 0, 0, "", fmt.Errorf("slave %s listen failed: %w", slave.Name(), err)
	}

	transferResp, ok := resp.(*protocol.AsyncResponseTransfer)
	if !ok {
		return "", 0, 0, "", fmt.Errorf("unexpected response from slave")
	}

	return slave.GetPASVIP(), transferResp.Info.Port, transferResp.Info.TransferIndex, slave.Name(), nil
}

// SlaveListenForDownloadPassthrough asks the slave that owns filePath to open
// a listener for direct client download.
func (b *Bridge) SlaveListenForDownloadPassthrough(filePath string, encrypted bool, sslClientMode bool) (string, int, int32, string, error) {
	slave := b.sm.SelectSlaveForDownload(filePath)
	if slave == nil {
		return "", 0, 0, "", fmt.Errorf("file not found on any available slave: %s", filePath)
	}

	listenIdx, err := IssueListen(slave, encrypted, sslClientMode)
	if err != nil {
		return "", 0, 0, "", fmt.Errorf("issue listen to %s: %w", slave.Name(), err)
	}

	resp, err := slave.FetchResponse(listenIdx, 60*time.Second)
	if err != nil {
		return "", 0, 0, "", fmt.Errorf("slave %s listen failed: %w", slave.Name(), err)
	}

	transferResp, ok := resp.(*protocol.AsyncResponseTransfer)
	if !ok {
		return "", 0, 0, "", fmt.Errorf("unexpected response from slave")
	}

	return slave.GetPASVIP(), transferResp.Info.Port, transferResp.Info.TransferIndex, slave.Name(), nil
}

// SlaveReceivePassthrough tells a slave to receive a file (client already connected directly).
// Waits for the slave to finish and returns size, checksum, duration.
func (b *Bridge) SlaveReceivePassthrough(filePath string, transferIdx int32, slaveName string, owner, group string, position int64, transferType byte) (int64, uint32, int64, error) {
	slave := b.sm.GetSlave(slaveName)
	if slave == nil {
		return 0, 0, 0, fmt.Errorf("slave %s not found", slaveName)
	}

	slave.IncActiveTransfers()
	defer slave.DecActiveTransfers()

	minSpeed, maxSpeed, graceSeconds := b.transferSpeedLimits(owner, group, filePath, "upload")
	recvIdx, err := IssueReceive(slave, filePath, transferType, position, "master", transferIdx, minSpeed, maxSpeed, graceSeconds)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("issue receive: %w", err)
	}

	// Wait for receive ACK
	_, err = slave.FetchResponse(recvIdx, 60*time.Second)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("receive ack: %w", err)
	}

	// Wait for transfer to complete — poll the RemoteTransfer status
	status, err := slave.WaitTransferStatus(transferIdx, 2*time.Hour)
	if err != nil {
		b.reconcileFailedUploadState(filePath, slave)
		return 0, 0, 0, err
	}
	if status.Error != "" {
		b.reconcileFailedUploadState(filePath, slave)
		return status.Transferred, status.Checksum, status.Elapsed, fmt.Errorf("%s", status.Error)
	}

	finalSize := finalUploadFileSize(status, position)
	finalChecksum := status.Checksum
	if position > 0 {
		if checksum, err := b.ChecksumFile(filePath); err == nil {
			finalChecksum = checksum
		}
	}

	b.sm.GetVFS().AddFile(filePath, VFSFile{
		Path:         filePath,
		Size:         finalSize,
		IsDir:        false,
		LastModified: time.Now().Unix(),
		SlaveName:    slaveName,
		Owner:        owner,
		Group:        group,
		XferTime:     status.Elapsed,
		Checksum:     finalChecksum,
	})
	b.recordUploadMetadata(filePath, owner, group, finalSize, status.Elapsed, finalChecksum)
	b.sm.SyncStatusMarkersForPath(filePath, false)

	log.Printf("[Passthrough] Upload %s on %s (%d bytes, %dms, CRC=%08X)",
		filePath, slaveName, finalSize, status.Elapsed, finalChecksum)

	return finalSize, finalChecksum, status.Elapsed, nil
}

// SlaveSendPassthrough tells a slave to send a file (client already connected directly).
func (b *Bridge) SlaveSendPassthrough(filePath string, transferIdx int32, slaveName string, username, primaryGroup string, position int64, transferType byte) (uint32, int64, error) {
	slave := b.sm.GetSlave(slaveName)
	if slave == nil {
		return 0, 0, fmt.Errorf("slave %s not found", slaveName)
	}

	minSpeed, maxSpeed, graceSeconds := b.transferSpeedLimits(username, primaryGroup, filePath, "download")
	sendIdx, err := IssueSend(slave, filePath, transferType, position, "master", transferIdx, minSpeed, maxSpeed, graceSeconds)
	if err != nil {
		return 0, 0, fmt.Errorf("issue send: %w", err)
	}

	_, err = slave.FetchResponse(sendIdx, 60*time.Second)
	if err != nil {
		b.reconcileUnavailableDownloadState(filePath, slave)
		return 0, 0, fmt.Errorf("send ack: %w", err)
	}

	status, err := slave.WaitTransferStatus(transferIdx, 2*time.Hour)
	if err != nil {
		b.reconcileUnavailableDownloadState(filePath, slave)
		return 0, 0, err
	}
	if status.Error != "" {
		b.reconcileUnavailableDownloadState(filePath, slave)
		return 0, 0, fmt.Errorf("%s", status.Error)
	}

	if position > 0 {
		return 0, status.Elapsed, nil
	}
	return status.Checksum, status.Elapsed, nil
}

// SlaveConnectAndReceive tells a slave to connect out to a remote address (PORT mode passthrough)
// and receive a file. The slave connects directly to the remote site — master doesn't touch the data.
func (b *Bridge) SlaveConnectAndReceive(filePath, remoteAddr, owner, group string, position int64, encrypted bool, sslClientMode bool, transferType byte) (int64, uint32, int64, error) {
	var slave *RemoteSlave
	if position > 0 {
		slave = b.sm.SelectSlaveForDownload(filePath)
	} else {
		slave = b.sm.SelectSlaveForUpload(filePath)
	}
	if slave == nil {
		return 0, 0, 0, fmt.Errorf("no available slave")
	}

	slave.IncActiveTransfers()
	defer slave.DecActiveTransfers()

	// Parse remote address
	parts := strings.SplitN(remoteAddr, ":", 2)
	if len(parts) != 2 {
		return 0, 0, 0, fmt.Errorf("invalid remote address: %s", remoteAddr)
	}
	remoteIP := parts[0]
	remotePort, _ := strconv.Atoi(parts[1])

	// Tell slave to connect out to the remote address (with TLS for FXP)
	// sslClientHandshake=false: slave acts as TLS SERVER (other site did CPSV, is TLS client)
	connectIdx, err := IssueConnect(slave, remoteIP, remotePort, encrypted, sslClientMode)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("issue connect to %s: %w", slave.Name(), err)
	}

	resp, err := slave.FetchResponse(connectIdx, 60*time.Second)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("slave %s connect failed: %w", slave.Name(), err)
	}

	transferResp, ok := resp.(*protocol.AsyncResponseTransfer)
	if !ok {
		return 0, 0, 0, fmt.Errorf("unexpected response from slave")
	}

	// Tell slave to receive the file on this connection
	minSpeed, maxSpeed, graceSeconds := b.transferSpeedLimits(owner, group, filePath, "upload")
	recvIdx, err := IssueReceive(slave, filePath, transferType, position, remoteAddr,
		transferResp.Info.TransferIndex, minSpeed, maxSpeed, graceSeconds)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("issue receive: %w", err)
	}

	// Wait for receive ACK
	_, err = slave.FetchResponse(recvIdx, 60*time.Second)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("receive ack: %w", err)
	}

	status, err := slave.WaitTransferStatus(transferResp.Info.TransferIndex, 2*time.Hour)
	if err != nil {
		b.reconcileFailedUploadState(filePath, slave)
		return 0, 0, 0, err
	}
	if status.Error != "" {
		b.reconcileFailedUploadState(filePath, slave)
		return status.Transferred, status.Checksum, status.Elapsed, fmt.Errorf("%s", status.Error)
	}

	finalSize := finalUploadFileSize(status, position)
	finalChecksum := status.Checksum
	if position > 0 {
		if checksum, err := b.ChecksumFile(filePath); err == nil {
			finalChecksum = checksum
		}
	}

	b.sm.GetVFS().AddFile(filePath, VFSFile{
		Path:         filePath,
		Size:         finalSize,
		IsDir:        false,
		LastModified: time.Now().Unix(),
		SlaveName:    slave.Name(),
		Owner:        owner,
		Group:        group,
		XferTime:     status.Elapsed,
		Checksum:     finalChecksum,
	})
	b.recordUploadMetadata(filePath, owner, group, finalSize, status.Elapsed, finalChecksum)
	b.sm.SyncStatusMarkersForPath(filePath, false)

	log.Printf("[Passthrough-PORT] Upload %s on %s (%d bytes, %dms, CRC=%08X)",
		filePath, slave.Name(), finalSize, status.Elapsed, finalChecksum)

	return finalSize, finalChecksum, status.Elapsed, nil
}

// SlaveConnectAndSend tells the owning slave to connect out to a remote address (PORT mode passthrough)
// and send a file directly. The master only orchestrates the control flow.
func (b *Bridge) SlaveConnectAndSend(filePath, remoteAddr, username, primaryGroup string, position int64, encrypted bool, sslClientMode bool, transferType byte) (uint32, int64, error) {
	slave := b.sm.SelectSlaveForDownload(filePath)
	if slave == nil {
		return 0, 0, fmt.Errorf("file not found on any available slave: %s", filePath)
	}

	slave.IncActiveTransfers()
	defer slave.DecActiveTransfers()

	parts := strings.SplitN(remoteAddr, ":", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("invalid remote address: %s", remoteAddr)
	}
	remoteIP := parts[0]
	remotePort, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, 0, fmt.Errorf("invalid remote address: %s", remoteAddr)
	}

	connectIdx, err := IssueConnect(slave, remoteIP, remotePort, encrypted, sslClientMode)
	if err != nil {
		return 0, 0, fmt.Errorf("issue connect to %s: %w", slave.Name(), err)
	}

	resp, err := slave.FetchResponse(connectIdx, 60*time.Second)
	if err != nil {
		return 0, 0, fmt.Errorf("slave %s connect failed: %w", slave.Name(), err)
	}

	transferResp, ok := resp.(*protocol.AsyncResponseTransfer)
	if !ok {
		return 0, 0, fmt.Errorf("unexpected response from slave")
	}

	minSpeed, maxSpeed, graceSeconds := b.transferSpeedLimits(username, primaryGroup, filePath, "download")
	sendIdx, err := IssueSend(slave, filePath, transferType, position, remoteAddr, transferResp.Info.TransferIndex, minSpeed, maxSpeed, graceSeconds)
	if err != nil {
		IssueAbort(slave, transferResp.Info.TransferIndex, "download send setup failed")
		return 0, 0, fmt.Errorf("issue send: %w", err)
	}
	if _, err := slave.FetchResponse(sendIdx, 60*time.Second); err != nil {
		IssueAbort(slave, transferResp.Info.TransferIndex, "download send ack failed")
		b.reconcileUnavailableDownloadState(filePath, slave)
		return 0, 0, fmt.Errorf("send ack: %w", err)
	}

	status, err := slave.WaitTransferStatus(transferResp.Info.TransferIndex, 2*time.Hour)
	if err != nil {
		b.reconcileUnavailableDownloadState(filePath, slave)
		return 0, 0, err
	}
	if status.Error != "" {
		b.reconcileUnavailableDownloadState(filePath, slave)
		return 0, 0, fmt.Errorf("%s", status.Error)
	}
	if position > 0 {
		return 0, status.Elapsed, nil
	}
	return status.Checksum, status.Elapsed, nil
}
