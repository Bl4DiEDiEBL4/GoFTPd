//go:build windows

package core

import "golang.org/x/sys/windows"

func freeSpaceMBForPath(path string) (uint64, bool) {
	ptr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, false
	}
	var free, total, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(ptr, &free, &total, &totalFree); err != nil {
		return 0, false
	}
	return free / 1024 / 1024, true
}
