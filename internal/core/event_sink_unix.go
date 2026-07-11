//go:build !windows

package core

import (
	"os"
	"syscall"
)

func openEventSinkFile(path string) (*os.File, error) {
	// O_WRONLY|O_NONBLOCK returns ENXIO immediately if no FIFO reader exists.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|syscall.O_NONBLOCK, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.SetNonblock(int(f.Fd()), false); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}
