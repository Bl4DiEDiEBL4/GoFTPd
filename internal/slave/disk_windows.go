//go:build windows

package slave

import "golang.org/x/sys/windows"

const (
	defaultFileOwner = "WeaveFTPd"
	defaultFileGroup = "WeaveFTPd"
)

// getDiskSpace returns available and total space for a filesystem path.
func getDiskSpace(path string) (available int64, capacity int64) {
	ptr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, 0
	}
	var free, total, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(ptr, &free, &total, &totalFree); err != nil {
		return 0, 0
	}
	return int64(free), int64(total)
}
