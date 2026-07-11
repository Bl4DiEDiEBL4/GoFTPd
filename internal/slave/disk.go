//go:build !windows

package slave

import "golang.org/x/sys/unix"

const (
	defaultFileOwner = "WeaveFTPd"
	defaultFileGroup = "WeaveFTPd"
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
