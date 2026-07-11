//go:build windows

package main

import (
	"os"
	"syscall"
)

func signalTargets() []os.Signal {
	return []os.Signal{syscall.SIGINT, syscall.SIGTERM}
}

func isRehashSignal(sig os.Signal) bool {
	return false
}
