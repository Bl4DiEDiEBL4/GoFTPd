//go:build !windows

package core

import "syscall"

func freeSpaceMBForPath(path string) (uint64, bool) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, false
	}
	return (stat.Bavail * uint64(stat.Bsize)) / 1024 / 1024, true
}
