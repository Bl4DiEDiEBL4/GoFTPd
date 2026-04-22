package master

import (
	"fmt"
	"hash/crc32"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
}

// NewBridge creates a new Bridge adapter.
func NewBridge(sm *SlaveManager) *Bridge {
	b := &Bridge{sm: sm}
	rdb, err := NewRaceDB("userdata/race.db")
	if err != nil {
		log.Printf("[Bridge] Race DB disabled: %v", err)
		return b
	}
	b.raceDB = rdb
	if err := b.raceDB.Reconcile(sm.GetVFS()); err != nil {
		log.Printf("[Bridge] Race DB reconcile failed: %v", err)
	}
	return b
}

// Ensure Bridge implements MasterBridge at compile time.
var _ core.MasterBridge = (*Bridge)(nil)
var _ plugin.MasterBridge = (*Bridge)(nil)

// ListDir returns directory entries from the master's VFS.
func (b *Bridge) ListDir(dirPath string) []core.MasterFileEntry {
	vfsFiles := b.sm.GetVFS().ListDirectory(dirPath)
	log.Printf("[Bridge] ListDir(%s) -> %d entries from VFS", dirPath, len(vfsFiles))
	entries := make([]core.MasterFileEntry, 0, len(vfsFiles))
	for _, f := range vfsFiles {
		entries = append(entries, core.MasterFileEntry{
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
		})
	}
	return entries
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
func (b *Bridge) UploadFile(filePath string, clientData net.Conn, owner, group string) (int64, uint32, error) {
	slave := b.sm.SelectSlaveForUpload(filePath)
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

	slaveAddr := fmt.Sprintf("%s:%d", slave.GetPASVIP(), transferResp.Info.Port)
	log.Printf("[Bridge] Connecting to slave %s at %s for upload of %s", slave.Name(), slaveAddr, filePath)

	// Connect to slave's data port
	slaveConn, err := net.DialTimeout("tcp", slaveAddr, 10*time.Second)
	if err != nil {
		return 0, 0, fmt.Errorf("connect to slave data port: %w", err)
	}

	// Tell slave to receive the file
	recvIdx, err := IssueReceive(slave, filePath, 'I', 0, "master",
		transferResp.Info.TransferIndex, 0, 0)
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

	if err != nil {
		log.Printf("[Bridge] Upload bridge error: %v (wrote %d bytes)", err, written)
		return written, checksum, fmt.Errorf("upload bridge: %w", err)
	}

	log.Printf("[Bridge] Uploaded %s to slave %s (%d bytes, %dms, CRC=%08X)", filePath, slave.Name(), written, xferTime, checksum)

	// Add file to VFS with transfer timing and checksum
	b.sm.GetVFS().AddFile(filePath, VFSFile{
		Path:         filePath,
		Size:         written,
		IsDir:        false,
		LastModified: time.Now().Unix(),
		SlaveName:    slave.Name(),
		Owner:        owner,
		Group:        group,
		XferTime:     xferTime,
		Checksum:     checksum,
	})

	if b.raceDB != nil {
		if err := b.raceDB.RecordUpload(filePath, owner, group, written, xferTime, checksum); err != nil {
			log.Printf("[Bridge] Race DB upload sync failed for %s: %v", filePath, err)
		}
	}

	return written, checksum, nil
}

// DownloadFile routes a download from a slave to the FTP client.
//
// Flow ( RETR):
//  1. Find which slave has the file
//  2. Tell slave to LISTEN
//  3. Connect from master to slave's data port
//  4. Tell slave to SEND the file
//  5. Bridge data: read from slave, write to clientData
func (b *Bridge) DownloadFile(filePath string, clientData net.Conn) error {
	slave := b.sm.SelectSlaveForDownload(filePath)
	if slave == nil {
		return fmt.Errorf("file not found on any available slave: %s", filePath)
	}

	// Tell slave to listen
	listenIdx, err := IssueListen(slave, false, false)
	if err != nil {
		return fmt.Errorf("issue listen to %s: %w", slave.Name(), err)
	}

	resp, err := slave.FetchResponse(listenIdx, 60*time.Second)
	if err != nil {
		return fmt.Errorf("slave %s listen failed: %w", slave.Name(), err)
	}

	transferResp, ok := resp.(*protocol.AsyncResponseTransfer)
	if !ok {
		return fmt.Errorf("unexpected response from slave")
	}

	slaveAddr := fmt.Sprintf("%s:%d", slave.GetPASVIP(), transferResp.Info.Port)
	log.Printf("[Bridge] Connecting to slave %s at %s for download of %s", slave.Name(), slaveAddr, filePath)

	slaveConn, err := net.DialTimeout("tcp", slaveAddr, 10*time.Second)
	if err != nil {
		return fmt.Errorf("connect to slave data port: %w", err)
	}

	// Tell slave to send the file
	sendIdx, err := IssueSend(slave, filePath, 'I', 0, "master",
		transferResp.Info.TransferIndex, 0, 0)
	if err != nil {
		slaveConn.Close()
		return fmt.Errorf("issue send: %w", err)
	}

	_, err = slave.FetchResponse(sendIdx, 60*time.Second)
	if err != nil {
		slaveConn.Close()
		return fmt.Errorf("send ack: %w", err)
	}

	// Bridge: slave -> client
	written, err := io.Copy(clientData, slaveConn)
	slaveConn.Close()

	if err != nil {
		log.Printf("[Bridge] Download bridge error: %v (wrote %d bytes)", err, written)
		return fmt.Errorf("download bridge: %w", err)
	}

	log.Printf("[Bridge] Downloaded %s from slave %s (%d bytes)", filePath, slave.Name(), written)
	return nil
}

// DeleteFile deletes from all slaves and VFS.
func (b *Bridge) DeleteFile(filePath string) error {
	vfsFile := b.sm.GetVFS().GetFile(filePath)
	err := b.sm.DeleteFile(filePath)
	if err != nil {
		return err
	}
	if b.raceDB != nil {
		isDir := vfsFile != nil && vfsFile.IsDir
		if derr := b.raceDB.DeletePath(filepath.Clean(filePath), isDir); derr != nil {
			log.Printf("[Bridge] Race DB delete sync failed for %s: %v", filePath, derr)
		}
	}
	return nil
}

// RenameFile renames on all slaves and VFS.
func (b *Bridge) RenameFile(from, toDir, toName string) {
	vfsFile := b.sm.GetVFS().GetFile(from)
	toPath := filepath.Join(toDir, toName)
	b.sm.RenameFile(from, toDir, toName)
	if b.raceDB != nil {
		isDir := vfsFile != nil && vfsFile.IsDir
		if err := b.raceDB.RenamePath(filepath.Clean(from), filepath.Clean(toPath), isDir); err != nil {
			log.Printf("[Bridge] Race DB rename sync failed from %s to %s: %v", from, toPath, err)
		}
	}
}

// MakeDir creates a directory in the VFS and physically on the slave.
func (b *Bridge) MakeDir(dirPath, owner, group string) {
	// 1. Create in Master VFS
	b.sm.MakeDirectory(dirPath, owner, group)

	// 2. Tell the slave to actually create the folder on its physical disk.
	// This ensures empty directories survive the 'remerge' process on restart.
	slave := b.sm.SelectSlaveForDownload(filepath.Dir(dirPath))
	if slave == nil {
		slave = b.sm.SelectSlaveForUpload(dirPath)
	}

	if slave != nil {
		index, err := IssueMakeDir(slave, dirPath)
		if err == nil {
			_, _ = slave.FetchResponse(index, 30*time.Second)
		}
	}
}

func (b *Bridge) Symlink(linkPath, targetPath string) error {
	b.sm.GetVFS().AddSymlink(linkPath, targetPath)
	var lastErr error
	for _, slave := range b.sm.GetAvailableSlaves() {
		targetArg := strings.TrimPrefix(filepath.ToSlash(filepath.Clean(targetPath)), "/")
		index, err := IssueSymlink(slave, linkPath, targetArg)
		if err != nil {
			lastErr = err
			continue
		}
		if _, err := slave.FetchResponse(index, 30*time.Second); err != nil {
			lastErr = err
		}
	}
	return lastErr
}

func (b *Bridge) Chmod(path string, mode uint32) error {
	b.sm.GetVFS().Chmod(path, mode)
	var lastErr error
	for _, slave := range b.sm.GetAvailableSlaves() {
		index, err := IssueChmod(slave, path, mode)
		if err != nil {
			lastErr = err
			continue
		}
		if _, err := slave.FetchResponse(index, 30*time.Second); err != nil {
			lastErr = err
		}
	}
	return lastErr
}

// GetFileSize returns file size, or -1 if not found.
func (b *Bridge) GetFileSize(filePath string) int64 {
	f := b.sm.GetVFS().GetFile(filePath)
	if f == nil {
		return -1
	}
	return f.Size
}

// FileExists checks if a path exists in the VFS.
func (b *Bridge) FileExists(filePath string) bool {
	return b.sm.GetVFS().FileExists(filePath)
}

// ReadFile reads a small file from a slave (for .message/.imdb display).
func (b *Bridge) ReadFile(filePath string) ([]byte, error) {
	slave := b.sm.SelectSlaveForDownload(filePath)
	if slave == nil {
		return nil, fmt.Errorf("file not found: %s", filePath)
	}

	index, err := IssueReadFile(slave, filePath)
	if err != nil {
		return nil, fmt.Errorf("issue readFile: %w", err)
	}

	resp, err := slave.FetchResponse(index, 30*time.Second)
	if err != nil {
		return nil, err
	}

	if fc, ok := resp.(*protocol.AsyncResponseFileContent); ok {
		return fc.Content, nil
	}

	return nil, fmt.Errorf("unexpected response type: %T", resp)
}

func (b *Bridge) ProbeMediaInfo(filePath, binary string, timeoutSeconds int) (map[string]string, error) {
	if strings.TrimSpace(binary) == "" {
		binary = "mediainfo"
	}
	if timeoutSeconds <= 0 {
		timeoutSeconds = 20
	}
	var lastErr error
	for _, slave := range b.candidateSlavesForPath(filePath) {
		index, err := IssueMediaInfo(slave, filePath, binary, timeoutSeconds)
		if err != nil {
			lastErr = fmt.Errorf("issue mediainfo to %s: %w", slave.Name(), err)
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
func (b *Bridge) GetSFVInfo(sfvPath string) ([]core.SFVEntryInfo, error) {
	var lastErr error
	for _, slave := range b.candidateSlavesForPath(sfvPath) {
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
			return entries, nil
		}

		lastErr = fmt.Errorf("%s: unexpected response type: %T", slave.Name(), resp)
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("sfv not found: %s", sfvPath)
}

// WriteFile writes a small file to a slave.
func (b *Bridge) WriteFile(filePath string, content []byte) error {
	slave := b.sm.SelectSlaveForDownload(filePath)
	if slave == nil {
		slave = b.sm.SelectSlaveForUpload(filePath)
	}
	if slave == nil {
		return fmt.Errorf("no available slave")
	}

	index, err := IssueWriteFile(slave, filePath, string(content))
	if err != nil {
		return fmt.Errorf("issue writeFile: %w", err)
	}

	_, err = slave.FetchResponse(index, 30*time.Second)
	if err != nil {
		return err
	}

	// Add to VFS
	b.sm.GetVFS().AddFile(filePath, VFSFile{
		Path:         filePath,
		Size:         int64(len(content)),
		IsDir:        false,
		LastModified: time.Now().Unix(),
		SlaveName:    slave.Name(),
	})

	return nil
}

func (b *Bridge) CreateSparseFile(filePath string, size int64, owner, group string) error {
	if size < 0 {
		return fmt.Errorf("invalid sparse file size: %d", size)
	}
	slave := b.sm.SelectSlaveForDownload(filePath)
	if slave == nil {
		slave = b.sm.SelectSlaveForUpload(filePath)
	}
	if slave == nil {
		return fmt.Errorf("no available slave")
	}

	index, err := IssueCreateSparseFile(slave, filePath, size)
	if err != nil {
		return fmt.Errorf("issue createSparseFile: %w", err)
	}
	if _, err := slave.FetchResponse(index, 30*time.Second); err != nil {
		return err
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
	return nil
}

func (b *Bridge) ChecksumFile(filePath string) (uint32, error) {
	var lastErr error
	for _, slave := range b.candidateSlavesForPath(filePath) {
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

func (b *Bridge) candidateSlavesForPath(filePath string) []*RemoteSlave {
	out := make([]*RemoteSlave, 0, 1)
	seen := map[string]bool{}
	if slave := b.sm.SelectSlaveForDownload(filePath); slave != nil {
		out = append(out, slave)
		seen[slave.Name()] = true
	}
	for _, slave := range b.sm.GetAvailableSlaves() {
		if slave == nil || seen[slave.Name()] {
			continue
		}
		out = append(out, slave)
		seen[slave.Name()] = true
	}
	return out
}

func (b *Bridge) MarkFileMissing(filePath string) error {
	b.sm.GetVFS().DeleteFile(filePath)
	if b.raceDB != nil {
		return b.raceDB.DeletePath(filepath.Clean(filePath), false)
	}
	return nil
}

func (b *Bridge) SyncPresentFile(filePath string, checksum uint32) error {
	f := b.sm.GetVFS().GetFile(filePath)
	if f == nil || f.IsDir {
		return fmt.Errorf("file not found: %s", filePath)
	}
	if b.raceDB != nil {
		return b.raceDB.RecordUpload(filePath, f.Owner, f.Group, f.Size, f.XferTime, checksum)
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
	v.b.MakeDir(dirPath, owner, group)

	return nil
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
func (b *Bridge) CacheSFV(dirPath string, sfvName string, entries []core.SFVEntryInfo) {
	sfvMap := make(map[string]uint32, len(entries))
	for _, e := range entries {
		sfvMap[e.FileName] = e.CRC32
	}
	b.sm.GetVFS().SetSFVData(dirPath, sfvName, sfvMap)
	if b.raceDB != nil {
		if err := b.raceDB.SaveSFV(filepath.Clean(dirPath), sfvName, sfvMap); err != nil {
			log.Printf("[Bridge] Race DB SFV sync failed for %s: %v", dirPath, err)
		}
	}
	log.Printf("[Bridge] Cached SFV for %s: %d entries", dirPath, len(entries))
}

// GetVFSRaceStats returns race statistics for a directory,
// counting ONLY files that are listed in the cached SFV data.
func (b *Bridge) GetVFSRaceStats(dirPath string) ([]core.VFSRaceUser, []core.VFSRaceGroup, int64, int, int) {
	if b.raceDB != nil {
		return b.raceDB.GetRaceStats(filepath.Clean(dirPath))
	}

	users, groups, totalBytes, present, total := b.sm.GetVFS().GetRaceStats(dirPath)

	coreUsers := make([]core.VFSRaceUser, len(users))
	for i, u := range users {
		coreUsers[i] = core.VFSRaceUser{
			Name:      u.Name,
			Group:     u.Group,
			Files:     u.Files,
			Bytes:     u.Bytes,
			Speed:     u.Speed,
			PeakSpeed: u.PeakSpeed,
			SlowSpeed: u.SlowSpeed,
			Percent:   u.Percent,
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

// GetRaceWallClockSeconds returns wall-clock race duration (first file start
// to last file end) in seconds. 0 if race db unavailable or dir unknown.
func (b *Bridge) GetRaceWallClockSeconds(dirPath string) int64 {
	if b.raceDB == nil {
		return 0
	}
	return b.raceDB.GetRaceWallClockSeconds(filepath.Clean(dirPath))
}

// GetSFVData returns cached SFV entries for a directory.
func (b *Bridge) GetSFVData(dirPath string) map[string]uint32 {
	meta := b.sm.GetVFS().GetSFVData(dirPath)
	if meta == nil {
		return nil
	}
	return meta.SFVEntries
}

func (b *Bridge) SearchDirs(query string, limit int) []core.VFSSearchResult {
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
func (b *Bridge) SlaveListenForPassthrough(uploadPath string, encrypted bool) (string, int, int32, string, error) {
	slave := b.sm.SelectSlaveForUpload(uploadPath)
	if slave == nil {
		return "", 0, 0, "", fmt.Errorf("no available slave")
	}

	listenIdx, err := IssueListen(slave, encrypted, false)
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
func (b *Bridge) SlaveListenForDownloadPassthrough(filePath string, encrypted bool) (string, int, int32, string, error) {
	slave := b.sm.SelectSlaveForDownload(filePath)
	if slave == nil {
		return "", 0, 0, "", fmt.Errorf("file not found on any available slave: %s", filePath)
	}

	listenIdx, err := IssueListen(slave, encrypted, false)
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
func (b *Bridge) SlaveReceivePassthrough(filePath string, transferIdx int32, slaveName string, owner, group string) (int64, uint32, int64, error) {
	slave := b.sm.GetSlave(slaveName)
	if slave == nil {
		return 0, 0, 0, fmt.Errorf("slave %s not found", slaveName)
	}

	slave.IncActiveTransfers()
	defer slave.DecActiveTransfers()

	recvIdx, err := IssueReceive(slave, filePath, 'I', 0, "master", transferIdx, 0, 0)
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
		return 0, 0, 0, err
	}
	if status.Error != "" {
		return status.Transferred, status.Checksum, status.Elapsed, fmt.Errorf("%s", status.Error)
	}

	b.sm.GetVFS().AddFile(filePath, VFSFile{
		Path:         filePath,
		Size:         status.Transferred,
		IsDir:        false,
		LastModified: time.Now().Unix(),
		SlaveName:    slaveName,
		Owner:        owner,
		Group:        group,
		XferTime:     status.Elapsed,
		Checksum:     status.Checksum,
	})

	log.Printf("[Passthrough] Upload %s on %s (%d bytes, %dms, CRC=%08X)",
		filePath, slaveName, status.Transferred, status.Elapsed, status.Checksum)

	if b.raceDB != nil {
		if err := b.raceDB.RecordUpload(filePath, owner, group, status.Transferred, status.Elapsed, status.Checksum); err != nil {
			log.Printf("[Passthrough] RaceDB record failed: %v", err)
		}
	}

	return status.Transferred, status.Checksum, status.Elapsed, nil
}

// SlaveSendPassthrough tells a slave to send a file (client already connected directly).
func (b *Bridge) SlaveSendPassthrough(filePath string, transferIdx int32, slaveName string) error {
	slave := b.sm.GetSlave(slaveName)
	if slave == nil {
		return fmt.Errorf("slave %s not found", slaveName)
	}

	sendIdx, err := IssueSend(slave, filePath, 'I', 0, "master", transferIdx, 0, 0)
	if err != nil {
		return fmt.Errorf("issue send: %w", err)
	}

	_, err = slave.FetchResponse(sendIdx, 60*time.Second)
	if err != nil {
		return fmt.Errorf("send ack: %w", err)
	}

	status, err := slave.WaitTransferStatus(transferIdx, 2*time.Hour)
	if err != nil {
		return err
	}
	if status.Error != "" {
		return fmt.Errorf("%s", status.Error)
	}

	return nil
}

// SlaveConnectAndReceive tells a slave to connect out to a remote address (PORT mode passthrough)
// and receive a file. The slave connects directly to the remote site — master doesn't touch the data.
func (b *Bridge) SlaveConnectAndReceive(filePath, remoteAddr, owner, group string) (int64, uint32, int64, error) {
	slave := b.sm.SelectSlaveForUpload(filePath)
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
	connectIdx, err := IssueConnect(slave, remoteIP, remotePort, true, false)
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
	recvIdx, err := IssueReceive(slave, filePath, 'I', 0, remoteAddr,
		transferResp.Info.TransferIndex, 0, 0)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("issue receive: %w", err)
	}

	// Wait for receive ACK
	_, err = slave.FetchResponse(recvIdx, 60*time.Second)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("receive ack: %w", err)
	}

	// Wait for transfer to complete
	rt, ok := slave.GetTransfer(transferResp.Info.TransferIndex)
	if !ok {
		for i := 0; i < 600; i++ {
			time.Sleep(500 * time.Millisecond)
			rt, ok = slave.GetTransfer(transferResp.Info.TransferIndex)
			if ok && rt.IsFinished() {
				break
			}
			if !ok {
				break
			}
		}
	}

	if rt != nil {
		for !rt.IsFinished() {
			time.Sleep(100 * time.Millisecond)
		}
		status := rt.GetStatus()

		b.sm.GetVFS().AddFile(filePath, VFSFile{
			Path:         filePath,
			Size:         status.Transferred,
			IsDir:        false,
			LastModified: time.Now().Unix(),
			SlaveName:    slave.Name(),
			Owner:        owner,
			Group:        group,
			XferTime:     status.Elapsed,
			Checksum:     status.Checksum,
		})

		log.Printf("[Passthrough-PORT] Upload %s on %s (%d bytes, %dms, CRC=%08X)",
			filePath, slave.Name(), status.Transferred, status.Elapsed, status.Checksum)

		if b.raceDB != nil {
			if err := b.raceDB.RecordUpload(filePath, owner, group, status.Transferred, status.Elapsed, status.Checksum); err != nil {
				log.Printf("[Passthrough-PORT] RaceDB record failed: %v", err)
			}
		}

		return status.Transferred, status.Checksum, status.Elapsed, nil
	}

	return 0, 0, 0, fmt.Errorf("transfer status not available")
}
