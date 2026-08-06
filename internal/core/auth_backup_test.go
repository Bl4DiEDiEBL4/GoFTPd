package core

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func TestAuthStateBackupOnceCreatesDaemonBackups(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)

	mustMkdir(t, filepath.Join("etc", "users"))
	mustMkdir(t, filepath.Join("etc", "groups"))
	mustWrite(t, filepath.Join("etc", "passwd"), "Finity:hash:100:300:0:/site:/bin/false\n", 0600)
	mustWrite(t, filepath.Join("etc", "group"), "iND::300:\n", 0644)
	mustWrite(t, filepath.Join("etc", "permissions.yml"), "rules: []\n", 0644)
	mustWrite(t, filepath.Join("etc", "affils.yml"), "affils: []\n", 0644)
	mustWrite(t, filepath.Join("etc", "slave_masks.txt"), "SLAVE1 127.0.0.1\n", 0644)
	mustWrite(t, filepath.Join("etc", "users", "Finity"), "USER test\nGROUP iND 0\n", 0600)
	mustWrite(t, filepath.Join("etc", "groups", "iND"), "GROUP iND\n", 0644)

	cfg := &Config{Mode: "master", PasswdFile: filepath.Join("etc", "passwd")}
	if err := authStateBackupOnce(cfg); err != nil {
		t.Fatalf("authStateBackupOnce() error = %v", err)
	}

	snap := latestBackupDir(t)
	assertFile(t, filepath.Join(snap, "etc", "passwd"), "Finity:hash:100:300:0:/site:/bin/false\n")
	assertFile(t, filepath.Join(snap, "etc", "group"), "iND::300:\n")
	assertFile(t, filepath.Join(snap, "etc", "permissions.yml"), "rules: []\n")
	assertFile(t, filepath.Join(snap, "etc", "affils.yml"), "affils: []\n")
	assertFile(t, filepath.Join(snap, "etc", "slave_masks.txt"), "SLAVE1 127.0.0.1\n")
	assertFile(t, filepath.Join(snap, "etc", "users", "Finity"), "USER test\nGROUP iND 0\n")
	assertFile(t, filepath.Join(snap, "etc", "groups", "iND"), "GROUP iND\n")
}

// latestBackupDir returns the newest snapshot directory under backupRootDir.
func latestBackupDir(t *testing.T) string {
	t.Helper()
	entries, err := os.ReadDir(backupRootDir)
	if err != nil {
		t.Fatalf("ReadDir(%s) error = %v", backupRootDir, err)
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, e.Name())
		}
	}
	if len(dirs) == 0 {
		t.Fatalf("no backup snapshot created under %s", backupRootDir)
	}
	sort.Strings(dirs)
	return filepath.Join(backupRootDir, dirs[len(dirs)-1])
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0755); err != nil {
		t.Fatalf("MkdirAll(%s) error = %v", path, err)
	}
}

func mustWrite(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", path, err)
	}
}

func assertFile(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s) error = %v", path, err)
	}
	if string(got) != want {
		t.Fatalf("%s = %q, want %q", path, string(got), want)
	}
}
