package slave

import (
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// getDiskSpace returns available and total space for a filesystem path.
func getDiskSpace(path string) (available int64, capacity int64) {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return 0, 0
	}
	available = int64(stat.Bavail) * int64(stat.Bsize)
	capacity = int64(stat.Blocks) * int64(stat.Bsize)
	return
}

// getFileOwner returns the owner for a file.
// On the slave we don't have FTP user mapping, so we return the
// numeric UID as a string. The master's VFS stores the real FTP
// username for files uploaded through GoFTPd.
func getFileOwner(info os.FileInfo) string {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		// If UID is 0 (root) or a system user, just return "ftp"
		if stat.Uid == 0 || stat.Uid >= 65534 {
			return "GoFTPd"
		}
		return "GoFTPd" // We can't map OS UIDs to FTP usernames on the slave
	}
	return "GoFTPd"
}

// getFileGroup returns the group for a file.
func getFileGroup(info os.FileInfo) string {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		_ = stat
	}
	return "GoFTPd"
}
