package master

import (
	"fmt"
	"hash/crc32"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"time"

	"goftpd/internal/core"
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

// ListDir returns directory entries from the master's VFS.
func (b *Bridge) ListDir(dirPath string) []core.MasterFileEntry {
	vfsFiles := b.sm.GetVFS().ListDirectory(dirPath)
	log.Printf("[Bridge] ListDir(%s) -> %d entries from VFS", dirPath, len(vfsFiles))
	entries := make([]core.MasterFileEntry, 0, len(vfsFiles))
	for _, f := range vfsFiles {
		entries = append(entries, core.MasterFileEntry{
			Name:    filepath.Base(f.Path),
			Size:    f.Size,
			IsDir:   f.IsDir,
			ModTime: f.LastModified,
			Owner:   f.Owner,
			Group:   f.Group,
			Slave:   f.SlaveName,
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
	slave := b.sm.SelectSlaveForUpload()
	if slave == nil {
		return 0, 0, fmt.Errorf("no available slave")
	}

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
		slave = b.sm.SelectSlaveForUpload()
	}

	if slave != nil {
		// Fire and forget directory creation command to the slave
		_ = slave.SendCommand(&protocol.AsyncCommand{
			Index: "none",
			Name:  "makedir",
			Args:  []string{dirPath},
		})
	}
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

// GetSFVInfo asks a slave to parse an SFV file and return the entries.
func (b *Bridge) GetSFVInfo(sfvPath string) ([]core.SFVEntryInfo, error) {
	slave := b.sm.SelectSlaveForDownload(sfvPath)
	if slave == nil {
		return nil, fmt.Errorf("sfv not found: %s", sfvPath)
	}

	index, err := IssueSFVFile(slave, sfvPath)
	if err != nil {
		return nil, fmt.Errorf("issue sfvFile: %w", err)
	}

	resp, err := slave.FetchResponse(index, 30*time.Second)
	if err != nil {
		return nil, err
	}

	if sfv, ok := resp.(*protocol.AsyncResponseSFVInfo); ok {
		entries := make([]core.SFVEntryInfo, len(sfv.Entries))
		for i, e := range sfv.Entries {
			entries[i] = core.SFVEntryInfo{FileName: e.FileName, CRC32: e.CRC32}
		}
		return entries, nil
	}

	return nil, fmt.Errorf("unexpected response type: %T", resp)
}

// WriteFile writes a small file to a slave.
func (b *Bridge) WriteFile(filePath string, content []byte) error {
	slave := b.sm.SelectSlaveForDownload(filePath)
	if slave == nil {
		slave = b.sm.SelectSlaveForUpload()
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
	return nil
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
			Name:    u.Name,
			Group:   u.Group,
			Files:   u.Files,
			Bytes:   u.Bytes,
			Speed:   u.Speed,
			Percent: u.Percent,
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

// GetSFVData returns cached SFV entries for a directory.
func (b *Bridge) GetSFVData(dirPath string) map[string]uint32 {
	meta := b.sm.GetVFS().GetSFVData(dirPath)
	if meta == nil {
		return nil
	}
	return meta.SFVEntries
}
