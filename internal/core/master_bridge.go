package core

import (
	"net"
)

// MasterBridge is the interface that internal/core uses to talk to the master's
// SlaveManager without importing internal/master (which would be circular).
// The SlaveManager implements this interface and is injected via Config.MasterManager.
//
// This mirrors 's approach where the FTP command layer calls into
// GlobalContext -> SlaveManager -> RemoteSlave for all slave operations.
type MasterBridge interface {
	// ListDir returns directory entries from the master's VFS
	ListDir(dirPath string) []MasterFileEntry

	// UploadFile routes an upload from the FTP client data connection to a slave.
	// owner and group are set on the VFS entry for directory listings.
	UploadFile(filePath string, clientData net.Conn, owner, group string) (int64, uint32, error)

	// DownloadFile routes a download from a slave to the FTP client data connection.
	// The bridge finds which slave has the file, tells it to send, then bridges data.
	DownloadFile(filePath string, clientData net.Conn) error

	// DeleteFile deletes a file on all slaves and from the VFS.
	DeleteFile(filePath string) error

	// RenameFile renames on all slaves and in VFS.
	RenameFile(from, toDir, toName string)

	// MakeDir creates a directory in the VFS.
	MakeDir(dirPath, owner, group string)

	// Symlink creates or replaces a symbolic link in VFS/slaves.
	Symlink(linkPath, targetPath string) error

	// Chmod changes permissions in VFS/slaves.
	Chmod(path string, mode uint32) error

	// CreateSparseFile creates a file with the requested logical size without
	// streaming bytes through the master.
	CreateSparseFile(filePath string, size int64, owner, group string) error

	// GetFileSize returns file size from VFS, or -1 if not found.
	GetFileSize(filePath string) int64

	// FileExists checks if a path exists in the VFS.
	FileExists(filePath string) bool

	// ReadFile reads a small file from a slave (for .message/.imdb display).
	ReadFile(filePath string) ([]byte, error)

	// GetSFVInfo asks a slave to parse an SFV file and return filename→CRC32 entries.
	GetSFVInfo(sfvPath string) ([]SFVEntryInfo, error)

	// WriteFile writes a small file to a slave (for .message generation).
	WriteFile(filePath string, content []byte) error

	// ChecksumFile returns the CRC32 of a file from the slave that owns it.
	ChecksumFile(filePath string) (uint32, error)

	// MarkFileMissing removes a file from VFS/RaceDB presence without requiring it to exist.
	MarkFileMissing(filePath string) error

	// SyncPresentFile refreshes RaceDB presence for an existing VFS file.
	SyncPresentFile(filePath string, checksum uint32) error

	// CacheSFV caches parsed SFV entries on a VFS directory for race tracking.
	CacheSFV(dirPath string, sfvName string, entries []SFVEntryInfo)

	// GetVFSRaceStats returns race statistics computed from VFS metadata.
	GetVFSRaceStats(dirPath string) (users []VFSRaceUser, groups []VFSRaceGroup, totalBytes int64, present int, total int)

	// GetRaceWallClockMilliseconds returns the wall-clock race duration (first
	// file start to last file end) in milliseconds. Used for accurate aggregate
	// speed in STATS — summing per-file durations overcounts when uploads run
	// in parallel.
	GetRaceWallClockMilliseconds(dirPath string) int64

	// GetSFVData returns cached SFV entries for a directory (filename->CRC32 map).
	// Returns nil if no SFV is cached for this directory.
	GetSFVData(dirPath string) map[string]uint32

	// GetDirMediaInfo returns cached release-level mediainfo fields for a directory.
	GetDirMediaInfo(dirPath string) map[string]string

	// SearchDirs searches the master's VFS for directories matching query.
	SearchDirs(query string, limit int) []VFSSearchResult

	// StartRemerge starts a full background VFS refresh for one slave.
	StartRemerge(slaveName string) error

	// StartRemergeAll starts a full background VFS refresh for every online slave.
	StartRemergeAll() (started int, errors []string)

	// Passthrough PORT: tell slave to connect out to remote address and receive file
	SlaveConnectAndReceive(filePath, remoteAddr, owner, group string) (int64, uint32, int64, error)

	// Passthrough: ask a slave to listen and return its IP:port + transfer index.
	// sslClientMode selects the TLS role for secure FXP passive sockets.
	SlaveListenForPassthrough(uploadPath string, encrypted bool, sslClientMode bool) (slaveIP string, port int, transferIdx int32, slaveName string, err error)

	// Passthrough: ask the slave that owns filePath to listen for a download.
	// sslClientMode selects the TLS role for secure FXP passive sockets.
	SlaveListenForDownloadPassthrough(filePath string, encrypted bool, sslClientMode bool) (slaveIP string, port int, transferIdx int32, slaveName string, err error)

	// Passthrough: tell slave to receive a file, wait for completion, return size/checksum
	SlaveReceivePassthrough(filePath string, transferIdx int32, slaveName string, owner, group string) (int64, uint32, int64, error)

	// Passthrough: tell slave to send a file, wait for completion
	SlaveSendPassthrough(filePath string, transferIdx int32, slaveName string) error
}

// MasterFileEntry is a file/dir entry returned by MasterBridge.ListDir.
type MasterFileEntry struct {
	Name       string
	Size       int64
	IsDir      bool
	IsSymlink  bool
	LinkTarget string
	Mode       uint32
	ModTime    int64
	Owner      string
	Group      string
	Slave      string
}

// VFSSearchResult is one SITE SEARCH directory result from the master's VFS.
type VFSSearchResult struct {
	Path    string
	Files   int
	Bytes   int64
	ModTime int64
}

// SFVEntryInfo is a filename→CRC32 pair from a parsed SFV file.
type SFVEntryInfo struct {
	FileName string
	CRC32    uint32
}

// VFSRaceUser holds per-user race stats from VFS.
type VFSRaceUser struct {
	Name       string
	Group      string
	Files      int
	Bytes      int64
	Speed      float64
	PeakSpeed  float64
	SlowSpeed  float64
	Percent    int
	DurationMs int64 // sum of file durations for this user (effective transfer time)
}

// VFSRaceGroup holds per-group race stats from VFS.
type VFSRaceGroup struct {
	Name    string
	Files   int
	Bytes   int64
	Speed   float64
	Percent int
}
