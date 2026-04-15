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

	// CacheSFV caches parsed SFV entries on a VFS directory for race tracking.
	CacheSFV(dirPath string, sfvName string, entries []SFVEntryInfo)

	// GetVFSRaceStats returns race statistics computed from VFS metadata.
	GetVFSRaceStats(dirPath string) (users []VFSRaceUser, groups []VFSRaceGroup, totalBytes int64, present int, total int)

	// GetSFVData returns cached SFV entries for a directory (filename->CRC32 map).
	// Returns nil if no SFV is cached for this directory.
	GetSFVData(dirPath string) map[string]uint32
}

// MasterFileEntry is a file/dir entry returned by MasterBridge.ListDir.
type MasterFileEntry struct {
	Name    string
	Size    int64
	IsDir   bool
	ModTime int64
	Owner   string
	Group   string
	Slave   string
}

// SFVEntryInfo is a filename→CRC32 pair from a parsed SFV file.
type SFVEntryInfo struct {
	FileName string
	CRC32    uint32
}

// VFSRaceUser holds per-user race stats from VFS.
type VFSRaceUser struct {
	Name    string
	Group   string
	Files   int
	Bytes   int64
	Speed   float64
	Percent int
}

// VFSRaceGroup holds per-group race stats from VFS.
type VFSRaceGroup struct {
	Name    string
	Files   int
	Bytes   int64
	Speed   float64
	Percent int
}
