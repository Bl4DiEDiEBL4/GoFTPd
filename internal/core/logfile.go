package core

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// rotatingLog is an io.Writer that writes to a file and rotates it daily.
// The active file is always `<basePath>` (e.g. logs/goftpd.log). At midnight
// (local time), the previous day's file is renamed to `<basePath>.YYYY-MM-DD`
// and any rotated file older than `deleteAfterDays` is deleted.
type rotatingLog struct {
	mu              sync.Mutex
	basePath        string
	deleteAfterDays int
	f               *os.File
	day             string
}

type filteredConsoleWriter struct {
	target io.Writer
}

func startupColorEnabled() bool {
	if os.Getenv("TERM") == "" || strings.EqualFold(os.Getenv("TERM"), "dumb") {
		return false
	}
	info, err := os.Stderr.Stat()
	if err != nil {
		return false
	}
	return (info.Mode() & os.ModeCharDevice) != 0
}

func PrintStartupBanner(subtitle string) {
	reset := ""
	bold := ""
	cyan := ""
	green := ""
	yellow := ""
	if startupColorEnabled() {
		reset = "\033[0m"
		bold = "\033[1m"
		cyan = "\033[36m"
		green = "\033[32m"
		yellow = "\033[33m"
	}
	lines := []struct {
		color string
		text  string
	}{
		{cyan + bold, "=============================================================="},
		{cyan + bold, "   ______      ________________  ____ "},
		{green + bold, "  / ____/___  / ____/_  __/ __ \\/ __ \\"},
		{green + bold, " / / __/ __ \\/ /_    / / / /_/ / / / /"},
		{yellow + bold, "/ /_/ / /_/ / __/   / / / ____/ /_/ / "},
		{yellow + bold, "\\____/\\____/_/     /_/ /_/   /_____/  "},
		{cyan + bold, "=============================================================="},
	}
	for _, line := range lines {
		fmt.Fprintf(os.Stderr, "%s%s%s\n", line.color, line.text, reset)
	}
	if strings.TrimSpace(subtitle) != "" {
		fmt.Fprintf(os.Stderr, "%s%s%s\n\n", yellow, subtitle, reset)
	} else {
		fmt.Fprintln(os.Stderr)
	}
}

func newRotatingLog(basePath string, deleteAfterDays int) (*rotatingLog, error) {
	if deleteAfterDays < 1 {
		deleteAfterDays = 1
	}
	if err := os.MkdirAll(filepath.Dir(basePath), 0755); err != nil {
		return nil, err
	}
	r := &rotatingLog{basePath: basePath, deleteAfterDays: deleteAfterDays}
	if err := r.open(); err != nil {
		return nil, err
	}
	r.purgeOld()
	return r, nil
}

func (r *rotatingLog) open() error {
	f, err := os.OpenFile(r.basePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	r.f = f
	r.day = time.Now().Format("2006-01-02")
	return nil
}

func (r *rotatingLog) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	today := time.Now().Format("2006-01-02")
	if today != r.day {
		_ = r.rotate(today)
	}
	return r.f.Write(p)
}

func (r *rotatingLog) rotate(newDay string) error {
	if r.f != nil {
		_ = r.f.Close()
	}
	archived := r.basePath + "." + r.day
	_ = os.Rename(r.basePath, archived)

	if err := r.open(); err != nil {
		return err
	}
	r.day = newDay
	r.purgeOld()
	return nil
}

func (r *rotatingLog) purgeOld() {
	dir := filepath.Dir(r.basePath)
	base := filepath.Base(r.basePath) + "."
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().AddDate(0, 0, -r.deleteAfterDays)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if len(name) <= len(base) || name[:len(base)] != base {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(dir, name))
		}
	}
}

func (w filteredConsoleWriter) Write(p []byte) (int, error) {
	if shouldWriteConsoleLog(p) {
		_, err := w.target.Write(p)
		return len(p), err
	}
	return len(p), nil
}

func shouldWriteConsoleLog(p []byte) bool {
	line := strings.ToLower(string(p))
	allowMarkers := []string{
		"[startup]",
		"[master]",
		"[slavemanager]",
		"[slave]",
		"[plugins] plugin loaded:",
		"[plugins] plugin load complete",
		"listening on port",
		"waiting for slaves",
		"connected from",
		"[rehash]",
		"starting goftpd",
	}
	for _, marker := range allowMarkers {
		if strings.Contains(line, marker) {
			return true
		}
	}
	markers := []string{
		" error",
		"[error]",
		" failed",
		"[fatal]",
		" fatal",
		" panic",
		"[panic]",
		" warning",
		"[warn]",
		"[warning]",
	}
	for _, marker := range markers {
		if strings.Contains(line, marker) {
			return true
		}
	}
	return false
}

// InstallFileLogger redirects the standard log package to a rotating file,
// and optionally mirrors to stderr. When debug is false, console output is
// filtered down to warnings/errors while the file still receives the full log.
func InstallFileLogger(logFilePath string, deleteAfterDays int, alsoConsole bool, debug bool) error {
	if logFilePath == "" {
		return nil
	}
	r, err := newRotatingLog(logFilePath, deleteAfterDays)
	if err != nil {
		return err
	}
	if alsoConsole {
		console := io.Writer(os.Stderr)
		if !debug {
			console = filteredConsoleWriter{target: os.Stderr}
		}
		log.SetOutput(io.MultiWriter(console, r))
		if debug {
			log.Printf("[LOG] File logging enabled: %s (delete after %d days, console mirrored)", logFilePath, deleteAfterDays)
		}
	} else {
		log.SetOutput(r)
		if debug {
			log.Printf("[LOG] File logging enabled: %s (delete after %d days, console disabled)", logFilePath, deleteAfterDays)
		}
	}
	return nil
}

// InstallConsoleLogger keeps logging on stderr only. When debug is false, only
// warnings/errors are shown.
func InstallConsoleLogger(debug bool) {
	if debug {
		log.SetOutput(os.Stderr)
		return
	}
	log.SetOutput(filteredConsoleWriter{target: os.Stderr})
}
