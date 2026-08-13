package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVerifyPasswordSupportsLegacyGlftpdHash(t *testing.T) {
	hash := "$01020304$d631702386055e6797948aa58b4551b2ba70492a"
	if !VerifyPassword("secret", hash) {
		t.Fatalf("VerifyPassword() = false, want true for valid legacy glFTPD hash")
	}
	if VerifyPassword("wrong", hash) {
		t.Fatalf("VerifyPassword() = true, want false for wrong password")
	}
}

func TestUpgradeLegacyPasswordHashRewritesToBcrypt(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	passwdPath := filepath.Join(dir, "passwd")
	legacy := "$01020304$d631702386055e6797948aa58b4551b2ba70492a"
	if err := os.WriteFile(passwdPath, []byte(
		"glftpd:$2a$10$5s9xY0wDp6PtErspTWlyAOlU5/LtZDu5Wu.1SLcerN.aY6WpipNuC:0:5:0:/:/bin/false\n"+
			"Finity:"+legacy+":100:300:0:/site:/bin/false\n",
	), 0600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	upgraded, err := UpgradeLegacyPasswordHash("Finity", "secret", legacy, passwdPath)
	if err != nil {
		t.Fatalf("UpgradeLegacyPasswordHash() error = %v", err)
	}
	if !upgraded {
		t.Fatalf("UpgradeLegacyPasswordHash() upgraded = false, want true")
	}

	passwds, err := LoadPasswdFile(passwdPath)
	if err != nil {
		t.Fatalf("LoadPasswdFile() error = %v", err)
	}
	got := passwds["Finity"]
	if !strings.HasPrefix(got, "$2") {
		t.Fatalf("upgraded hash = %q, want bcrypt", got)
	}
	if !VerifyPassword("secret", got) {
		t.Fatalf("VerifyPassword() = false, want true for upgraded bcrypt hash")
	}
	if _, ok := passwds["glftpd"]; !ok {
		t.Fatalf("expected unrelated passwd entries to remain present after upgrade")
	}
}

func TestAddUserToPasswdPreservesExistingFields(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	passwdPath := filepath.Join(dir, "passwd")
	if err := os.WriteFile(passwdPath, []byte("Finity:oldhash:123:456:glftpd:/glroot:/bin/bash\n"), 0600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if err := AddUserToPasswd("Finity", "newhash", passwdPath); err != nil {
		t.Fatalf("AddUserToPasswd() error = %v", err)
	}

	data, err := os.ReadFile(passwdPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	got := strings.TrimSpace(string(data))
	want := "Finity:newhash:123:456:glftpd:/glroot:/bin/bash"
	if got != want {
		t.Fatalf("passwd line = %q, want %q", got, want)
	}
}

func TestAddUserToPasswdDoesNotCreateDashBackup(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	passwdPath := filepath.Join(dir, "passwd")
	original := "Finity:oldhash:123:456:glftpd:/glroot:/bin/bash\n"
	if err := os.WriteFile(passwdPath, []byte(original), 0600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if err := AddUserToPasswd("Finity", "newhash", passwdPath); err != nil {
		t.Fatalf("AddUserToPasswd() error = %v", err)
	}

	if _, err := os.Stat(passwdPath + "-"); !os.IsNotExist(err) {
		t.Fatalf("legacy passwd- backup exists or stat failed: %v", err)
	}
	data, err := os.ReadFile(passwdPath)
	if err != nil {
		t.Fatalf("ReadFile(passwd) error = %v", err)
	}
	got := strings.TrimSpace(string(data))
	want := "Finity:newhash:123:456:glftpd:/glroot:/bin/bash"
	if got != want {
		t.Fatalf("passwd line = %q, want %q", got, want)
	}
}

func TestAddUserToPasswdUsesStableDefaultsForNewUsers(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	passwdPath := filepath.Join(dir, "passwd")

	if err := AddUserToPasswd("Finity", "newhash", passwdPath); err != nil {
		t.Fatalf("AddUserToPasswd() error = %v", err)
	}

	data, err := os.ReadFile(passwdPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	got := strings.TrimSpace(string(data))
	want := "Finity:newhash:100:300:0:/site:/bin/false"
	if got != want {
		t.Fatalf("passwd line = %q, want %q", got, want)
	}
}

func TestPasswdHelpersRejectInvalidUsernames(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	passwdPath := filepath.Join(dir, "passwd")
	if err := os.WriteFile(passwdPath, []byte("Finity:hash:100:300:0:/site:/bin/false\n"), 0600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	for _, name := range []string{"../probe", `..\probe`, "bad:name", "bad name"} {
		if err := AddUserToPasswd(name, "hash", passwdPath); err == nil {
			t.Fatalf("AddUserToPasswd(%q) succeeded, want error", name)
		}
		if err := RemoveUserFromPasswd(name, passwdPath); err == nil {
			t.Fatalf("RemoveUserFromPasswd(%q) succeeded, want error", name)
		}
	}
	if err := RenameUserInPasswd("Finity", "bad:name", passwdPath); err == nil {
		t.Fatalf("RenameUserInPasswd() with invalid new username succeeded, want error")
	}
}

func TestRenameUserInPasswdRejectsExistingDestination(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	passwdPath := filepath.Join(dir, "passwd")
	if err := os.WriteFile(passwdPath, []byte(
		"olduser:hash:100:300:0:/site:/bin/false\n"+
			"newuser:hash:101:300:0:/site:/bin/false\n",
	), 0600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if err := RenameUserInPasswd("olduser", "newuser", passwdPath); err == nil {
		t.Fatalf("RenameUserInPasswd() succeeded, want duplicate destination error")
	}

	data, err := os.ReadFile(passwdPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if strings.Count(string(data), "newuser:") != 1 || !strings.Contains(string(data), "olduser:") {
		t.Fatalf("passwd changed after rejected rename:\n%s", string(data))
	}
}

func TestAddGroupToFileRejectsInvalidGroupNameAndSanitizesDescription(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldwd) })
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	if err := os.MkdirAll("etc", 0755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	if err := AddGroupToFile("../evil", "desc", 300); err == nil {
		t.Fatalf("AddGroupToFile() with invalid group succeeded, want error")
	}
	if err := AddGroupToFile("iND", "scene:group", 300); err != nil {
		t.Fatalf("AddGroupToFile() error = %v", err)
	}
	data, err := os.ReadFile(filepath.Join("etc", "group"))
	if err != nil {
		t.Fatalf("ReadFile(group) error = %v", err)
	}
	if got, want := strings.TrimSpace(string(data)), "iND:scene group:300:"; got != want {
		t.Fatalf("group line = %q, want %q", got, want)
	}
}

func TestAddGroupToFileDoesNotCreateDashBackup(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldwd) })
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	if err := os.MkdirAll("etc", 0755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	original := "NoGroup:NoGroup:100:\n"
	if err := os.WriteFile(filepath.Join("etc", "group"), []byte(original), 0644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if err := AddGroupToFile("iND", "iND", 300); err != nil {
		t.Fatalf("AddGroupToFile() error = %v", err)
	}

	if _, err := os.Stat(filepath.Join("etc", "group-")); !os.IsNotExist(err) {
		t.Fatalf("legacy group- backup exists or stat failed: %v", err)
	}
	data, err := os.ReadFile(filepath.Join("etc", "group"))
	if err != nil {
		t.Fatalf("ReadFile(group) error = %v", err)
	}
	got := string(data)
	want := original + "iND:iND:300:\n"
	if got != want {
		t.Fatalf("group = %q, want %q", got, want)
	}
}

func TestHashAndVerifyPasswordWithExclamationMark(t *testing.T) {
	t.Helper()
	plaintext := "test!pass!123"

	hash, err := HashPassword(plaintext)
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}
	if !strings.HasPrefix(hash, "$2") {
		t.Fatalf("hash = %q, want bcrypt hash", hash)
	}
	if !VerifyPassword(plaintext, hash) {
		t.Fatalf("VerifyPassword() = false, want true for password containing !")
	}
	if VerifyPassword("testpass123", hash) {
		t.Fatalf("VerifyPassword() = true, want false for password without !")
	}
}
