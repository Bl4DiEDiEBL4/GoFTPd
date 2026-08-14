package core

import (
	"net"
	"time"
)

// TransferReadyFunc is called once a passthrough PORT transfer has a slave-side
// transfer identity, allowing live BW to match the control session to slave stats.
type TransferReadyFunc func(slaveName string, transferIdx int32)

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
	// Returns final size, checksum, and the slave-measured transfer time in
	// milliseconds (pure data time; use this for announced speeds instead of
	// wall-clock around the call, which includes slave setup round trips).
	UploadFile(filePath string, clientData net.Conn, owner, group string, position int64, transferType byte) (int64, uint32, int64, error)

	// DownloadFile routes a download from a slave to the FTP client data connection.
	// The bridge finds which slave has the file, tells it to send, then bridges data.
	// For full-file RETR, it returns the streamed CRC32 checksum when available.
	DownloadFile(filePath string, clientData net.Conn, username, primaryGroup string, position int64, transferType byte) (uint32, error)

	// DeleteFile deletes a file on all slaves and from the VFS.
	DeleteFile(filePath string) error

	// RenameFile renames on the owning slave and in VFS.
	RenameFile(from, toDir, toName string) error

	// MakeDir creates a directory on the owning/selected slave and in the VFS.
	MakeDir(dirPath, owner, group string) error

	// Symlink creates or replaces a symbolic link in VFS/slaves.
	Symlink(linkPath, targetPath string) error

	// Chmod changes permissions in VFS/slaves.
	Chmod(path string, mode uint32) error

	// CreateSparseFile creates a file with the requested logical size without
	// streaming bytes through the master.
	CreateSparseFile(filePath string, size int64, owner, group string) error

	// GetFileSize returns file size from VFS, or -1 if not found.
	GetFileSize(filePath string) int64

	// GetPathEntry returns a single VFS entry for an exact path.
	GetPathEntry(filePath string) (MasterFileEntry, bool)

	// FileExists checks if a path exists in the VFS.
	FileExists(filePath string) bool

	// ResolvePath returns the canonical VFS path after following any symlink
	// segments that exist in the master's VFS.
	ResolvePath(filePath string) string

	// ReadFile reads a small file from a slave (for .message/.imdb display).
	ReadFile(filePath string) ([]byte, error)
	ReadZipEntry(archivePath, entryName string) ([]byte, error)
	CheckZipIntegrity(archivePath string) (bool, error)

	// GetSFVInfo asks a slave to parse an SFV file and return filename→CRC32 entries.
	GetSFVInfo(sfvPath string) (SFVInfo, error)

	// WriteFile writes a small file to a slave (for .message generation).
	WriteFile(filePath string, content []byte) error

	// ChecksumFile returns the CRC32 of a file from the slave that owns it.
	ChecksumFile(filePath string) (uint32, error)

	// GetKnownChecksum returns the cached VFS CRC32 for a file if known.
	GetKnownChecksum(filePath string) (uint32, bool)

	// MarkFileMissing removes a file from VFS/RaceDB presence without requiring it to exist.
	MarkFileMissing(filePath string) error

	// SyncPresentFile refreshes RaceDB presence for an existing VFS file.
	SyncPresentFile(filePath string, checksum uint32) error

	// SyncReleaseRaceStats persists one completed SFV-tracked release into the
	// cold race/search store in one batch. Live/incomplete races should stay on
	// the in-memory/VFS hot path instead of writing per-file to the DB.
	SyncReleaseRaceStats(dirPath string) error

	// CacheSFV caches parsed SFV entries on a VFS directory for race tracking.
	CacheSFV(dirPath string, sfvName string, info SFVInfo)

	// GetVFSRaceStats returns race statistics computed from VFS metadata.
	GetVFSRaceStats(dirPath string) (users []VFSRaceUser, groups []VFSRaceGroup, totalBytes int64, present int, total int)

	// GetRaceWallClockMilliseconds returns the wall-clock race duration (first
	// file start to last file end) in milliseconds. Used for accurate aggregate
	// speed in STATS — summing per-file durations overcounts when uploads run
	// in parallel.
	GetRaceWallClockMilliseconds(dirPath string) int64

	// NoteRacePayloadTransfer records one tracked payload transfer into the
	// current live race window for the release. This lets COMPLETE timing prefer
	// the active run over historical DB timestamps.
	NoteRacePayloadTransfer(dirPath, fileName string, durationMs int64)

	// NoteRacePayloadTransferAt records one tracked payload transfer using the
	// actual upload completion timestamp captured on the hot path, so live race
	// windows are not distorted by queued post-hook timing.
	NoteRacePayloadTransferAt(dirPath, fileName string, durationMs int64, endMs int64)

	// GetSFVData returns cached SFV entries for a directory (filename->CRC32 map).
	// Returns nil if no SFV is cached for this directory.
	GetSFVData(dirPath string) map[string]uint32

	// GetZipExpectedParts returns cached file_id.diz-derived zip part count for
	// a directory when valid according to current VFS state.
	GetZipExpectedParts(dirPath string) (expected int, ok bool)

	// CacheZipExpectedParts stores or clears the file_id.diz-derived expected zip
	// part count for a directory. dizChecksum is the CRC32 of the current
	// file_id.diz content when known. expected<=0 clears the cached zip metadata.
	CacheZipExpectedParts(dirPath string, expected int, dizChecksum uint32)

	// GetVerifiedSFVPresentFiles returns the expected SFV filenames that are
	// currently present and checksum-valid according to live VFS state.
	GetVerifiedSFVPresentFiles(dirPath string) map[string]bool

	// GetReleaseStatus returns unified live completeness/missing state for one
	// release directory.
	GetReleaseStatus(dirPath string) (ReleaseStatus, bool)

	// GetDirMediaInfo returns cached release-level media probe fields for a directory.
	GetDirMediaInfo(dirPath string) map[string]string

	// ProbeMediaInfo runs the built-in media probe for a file and returns normalized key/value output.
	ProbeMediaInfo(filePath, binary string, timeoutSeconds int) (map[string]string, error)

	// CacheMediaInfo stores release-level media probe fields for a directory.
	CacheMediaInfo(dirPath string, fields map[string]string)

	// ClaimReleaseMetadataAnnouncement atomically marks one release-scoped
	// metadata announcement as emitted. It returns true only for the first
	// successful claimant for a given dirPath+key pair.
	ClaimReleaseMetadataAnnouncement(dirPath, key string) bool

	// SearchDirs searches the master's VFS for directories matching query.
	SearchDirs(query string, limit int) []VFSSearchResult

	// StartRemergeJobs starts all configured master-side remerge jobs for one slave.
	StartRemergeJobs(slaveName string) (started int, errors []string)

	// StartRemergeJob starts one configured remerge job, optionally scoped to a
	// VFS subtree such as /ARCHiVE/BLURAY.
	StartRemergeJob(slaveName, jobName, overridePath string) (started int, errors []string)

	// StartRemergeAllJobs starts configured master-side remerge jobs for every
	// online slave.
	StartRemergeAllJobs() (started int, errors []string)

	// StartRemergeAllJob starts one configured remerge job on every online slave.
	StartRemergeAllJob(jobName, overridePath string) (started int, errors []string)

	// StopRemerge asks a slave to abort its active background VFS refresh.
	StopRemerge(slaveName string) error

	// StopRemergeAll asks every online remerging slave to stop its refresh.
	StopRemergeAll() (stopped int, errors []string)

	// SyncStatusMarkersForPath refreshes zipscript/VFS status markers for a
	// path and its parent context after rescan, remerge, or file mutations.
	SyncStatusMarkersForPath(filePath string, isDir bool)

	// Slave-port auth guards: persistent denylist plus current temp bans.
	ListSlaveAuthDenyEntries() []string
	AddSlaveAuthDenyEntry(entry string) (string, error)
	RemoveSlaveAuthDenyEntry(entry string) (bool, error)
	ClearSlaveAuthTempBan(entry string) (bool, error)
	ListSlaveAuthTempBans() []SlaveAuthBanInfo

	// Per-slave IP mask allowlist: the fallback authentication mechanism for
	// the master-slave link when mTLS isn't configured (SITE SLAVE <name> ADDMASK/DELMASK/MASKS).
	AddSlaveMask(name, mask string) error
	RemoveSlaveMask(name, mask string) (bool, error)
	ListSlaveMasks(name string) []string
	ListAllSlaveMasks() map[string][]string

	// GetLiveTransferStats asks connected slaves for current live transfer counters.
	GetLiveTransferStats() []LiveTransferStat

	// GetAggregateDiskUsage returns writable slave free/total bytes for global
	// banner-style stats. ok=false means no usable slave disk stats were available.
	GetAggregateDiskUsage() (freeBytes int64, totalBytes int64, ok bool)

	// RunOnSlaveCommand runs a command on the owning or requested slave.
	RunOnSlaveCommand(dirPath, command string, args []string, env map[string]string, timeoutSeconds int, preferredSlave string) (string, error)

	// Passthrough PORT: tell slave to connect out to remote address and receive file.
	SlaveConnectAndReceive(filePath, remoteAddr, owner, group string, position int64, encrypted bool, sslClientMode bool, transferType byte, onReady TransferReadyFunc) (int64, uint32, int64, error)

	// Passthrough PORT: tell the owning slave to connect out to remote address and send file.
	SlaveConnectAndSend(filePath, remoteAddr, username, primaryGroup string, position int64, encrypted bool, sslClientMode bool, transferType byte, onReady TransferReadyFunc) (uint32, int64, error)

	// Passthrough: ask a slave to listen and return its IP:port + transfer index.
	// sslClientMode selects the TLS role for secure FXP passive sockets.
	SlaveListenForPassthrough(uploadPath string, encrypted bool, sslClientMode bool) (slaveIP string, port int, transferIdx int32, slaveName string, err error)

	// Passthrough: ask the slave that owns filePath to listen for a download.
	// sslClientMode selects the TLS role for secure FXP passive sockets.
	SlaveListenForDownloadPassthrough(filePath string, encrypted bool, sslClientMode bool) (slaveIP string, port int, transferIdx int32, slaveName string, err error)

	// Passthrough: tell slave to receive a file, wait for completion, return size/checksum
	SlaveReceivePassthrough(filePath string, transferIdx int32, slaveName string, owner, group string, position int64, transferType byte) (int64, uint32, int64, error)

	// Passthrough: tell slave to send a file, wait for completion.
	SlaveSendPassthrough(filePath string, transferIdx int32, slaveName string, username, primaryGroup string, position int64, transferType byte) (checksum uint32, elapsedMs int64, err error)
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
	XferTime   int64
}

// VFSSearchResult is one SITE SEARCH directory result from the master's VFS.
type VFSSearchResult struct {
	Path    string
	Files   int
	Bytes   int64
	ModTime int64
}

type ReleaseProgressStat struct {
	Path    string
	Present int
	Total   int
	HasSFV  bool
}

type ReleaseChildFacts struct {
	Path         string
	VisibleCount int
	FileCount    int
	HasSFV       bool
	HasNFO       bool
}

type ReleaseStatus struct {
	Path          string
	Kind          string
	VisibleCount  int
	FileCount     int
	HasSFV        bool
	HasNFO        bool
	Present       int
	Total         int
	TotalBytes    int64
	ExpectedFiles []string
	MissingFiles  []string
}

// SFVEntryInfo is a filename→CRC32 pair from a parsed SFV file.
type SFVEntryInfo struct {
	FileName string
	CRC32    uint32
}

// SFVInfo is parsed SFV metadata plus the checksum of the SFV file itself.
type SFVInfo struct {
	Entries  []SFVEntryInfo
	Checksum uint32
	Size     int64
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

type LiveTransferStat struct {
	SlaveName     string
	TransferIndex int32
	Direction     string
	Path          string
	StartedAt     time.Time
	Transferred   int64
	SpeedBytes    float64
}

type SlaveAuthBanInfo struct {
	IP          string
	Strikes     int
	BannedUntil time.Time
}
