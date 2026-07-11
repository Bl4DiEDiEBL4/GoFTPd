//go:build !windows

package core

import (
	"os"
	"syscall"
)

func fileOwnerUsername(info os.FileInfo, cfg *Config) (string, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", false
	}
	return GetUsernameByUID(int(stat.Uid), cfg), true
}
