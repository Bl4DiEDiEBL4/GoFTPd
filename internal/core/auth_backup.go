package core

import (
	"bytes"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	authStateBackupInterval = 5 * time.Minute
	backupRootDir           = "backup"
	authStateBackupKeep     = 48
)

var authStateBackupMu sync.Mutex

// StartAuthStateBackup keeps a daemon-local safety copy of mutable account
// state. It is intentionally fixed and master-only: slaves do not own auth.
func StartAuthStateBackup(cfg *Config) {
	if cfg == nil || !strings.EqualFold(strings.TrimSpace(cfg.Mode), "master") {
		return
	}
	log.Printf("[AUTHBACKUP] automatic auth-state backup enabled [interval=%s dir=%s]", authStateBackupInterval, backupRootDir)
	go func() {
		runAuthStateBackup(cfg)
		ticker := time.NewTicker(authStateBackupInterval)
		defer ticker.Stop()
		for range ticker.C {
			runAuthStateBackup(cfg)
		}
	}()
}

func runAuthStateBackup(cfg *Config) {
	if err := authStateBackupOnce(cfg); err != nil {
		log.Printf("[AUTHBACKUP] backup failed: %v", err)
	}
}

func authStateBackupOnce(cfg *Config) error {
	authStateBackupMu.Lock()
	defer authStateBackupMu.Unlock()

	passwdPath := "etc/passwd"
	if cfg != nil && strings.TrimSpace(cfg.PasswdFile) != "" {
		passwdPath = strings.TrimSpace(cfg.PasswdFile)
	}

	snapshotDir := filepath.Join(backupRootDir, time.Now().Format("2006-01-02_150405"))
	tmpDir := snapshotDir + ".tmp"
	_ = os.RemoveAll(tmpDir)
	if err := os.MkdirAll(tmpDir, 0700); err != nil {
		return err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(tmpDir)
		}
	}()

	var errs []string
	copied := 0

	regularFiles := []string{passwdPath, "etc/group", "etc/permissions.yml", "etc/affils.yml", "etc/slave_denylist.txt", "etc/slave_masks.txt"}
	passwdFileMu.RLock()
	for _, src := range regularFiles {
		n, err := backupCopyFileInto(src, tmpDir)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", src, err))
		}
		copied += n
	}
	passwdFileMu.RUnlock()

	for _, dir := range []string{"etc/users", "etc/groups"} {
		n, err := backupCopyDirInto(dir, tmpDir)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", dir, err))
		}
		copied += n
	}

	if copied == 0 {
		return joinBackupErrs(errs)
	}
	if err := os.RemoveAll(snapshotDir); err != nil {
		return err
	}
	if err := os.Rename(tmpDir, snapshotDir); err != nil {
		return err
	}
	cleanup = false

	pruneOldBackups(backupRootDir, authStateBackupKeep)
	return joinBackupErrs(errs)
}

func backupRelPath(p string) string {
	p = filepath.Clean(p)
	if filepath.IsAbs(p) {
		return filepath.Base(p)
	}
	return p
}

func backupCopyFileInto(src, snapshotRoot string) (int, error) {
	st, err := os.Stat(src)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	if st.IsDir() {
		return 0, nil
	}
	data, err := os.ReadFile(src)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return 0, nil
	}
	target := filepath.Join(snapshotRoot, backupRelPath(src))
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return 0, err
	}
	if err := os.WriteFile(target, data, st.Mode().Perm()); err != nil {
		return 0, err
	}
	return 1, nil
}

func backupCopyDirInto(dir, snapshotRoot string) (int, error) {
	st, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	if !st.IsDir() {
		return 0, nil
	}
	dstBase := filepath.Join(snapshotRoot, backupRelPath(dir))
	copied := 0
	err = filepath.WalkDir(dir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				return nil
			}
			return walkErr
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil || rel == "." {
			return err
		}
		if shouldSkipAuthBackupEntry(entry.Name()) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		target := filepath.Join(dstBase, rel)
		if entry.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		if info.Mode().Type() != 0 { // skip symlinks/devices
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return err
		}
		if err := os.WriteFile(target, data, info.Mode().Perm()); err != nil {
			return err
		}
		copied++
		return nil
	})
	return copied, err
}

func pruneOldBackups(root string, keep int) {
	if keep <= 0 {
		return
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	var snaps []string
	for _, e := range entries {
		if e.IsDir() && !strings.HasSuffix(e.Name(), ".tmp") {
			snaps = append(snaps, e.Name())
		}
	}
	if len(snaps) <= keep {
		return
	}
	sort.Strings(snaps)
	for _, name := range snaps[:len(snaps)-keep] {
		_ = os.RemoveAll(filepath.Join(root, name))
	}
}

func joinBackupErrs(errs []string) error {
	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("%s", strings.Join(errs, "; "))
}

func shouldSkipAuthBackupEntry(name string) bool {
	if name == ".backup" {
		return true
	}
	return strings.Contains(name, ".tmp-") || strings.HasSuffix(name, ".tmp")
}
