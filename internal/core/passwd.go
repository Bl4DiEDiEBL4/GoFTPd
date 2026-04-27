package core

import (
	"bufio"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
	"golang.org/x/crypto/pbkdf2"
)

// LoadPasswdFile reads standard /etc/passwd
func LoadPasswdFile(path string) (map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	passwds := make(map[string]string)
	scanner := bufio.NewScanner(file)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		parts := strings.Split(line, ":")
		if len(parts) >= 2 {
			username := parts[0]
			hash := parts[1]
			passwds[username] = hash
		}
	}

	return passwds, scanner.Err()
}

// VerifyPassword checks plaintext password against a hash.
// Supported format:
//   - bcrypt: $2a$/$2b$/$2y$
//   - glFTPD PBKDF2-HMAC-SHA1: $<8 hex salt>$<40 hex digest>
//
// Everything else is rejected. Older builds accepted unknown $-prefixed
// hashes, but that effectively let any password through for those accounts.
func VerifyPassword(plaintext, hash string) bool {
	if strings.HasPrefix(hash, "$2") {
		return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plaintext)) == nil
	}
	if verifyLegacyGlftpdHash(plaintext, hash) {
		return true
	}
	return false
}

func verifyLegacyGlftpdHash(plaintext, hash string) bool {
	if len(hash) != 50 || !strings.HasPrefix(hash, "$") {
		return false
	}
	parts := strings.Split(hash, "$")
	if len(parts) != 3 || parts[0] != "" || len(parts[1]) != 8 || len(parts[2]) != 40 {
		return false
	}
	salt, err := hex.DecodeString(parts[1])
	if err != nil || len(salt) != 4 {
		return false
	}
	want, err := hex.DecodeString(parts[2])
	if err != nil || len(want) != sha1.Size {
		return false
	}
	got := pbkdf2.Key([]byte(plaintext), salt, 100, sha1.Size, sha1.New)
	return hmac.Equal(got, want)
}

// HashPassword creates a bcrypt hash (default cost 10).
func HashPassword(plaintext string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(plaintext), 10)
	if err != nil {
		return "", err
	}
	return string(h), nil
}

// LoadGroupFile reads standard /etc/group file (groupname:desc:gid:slots)
func LoadGroupFile(path string) map[string]int {
	groupMap := make(map[string]int)
	file, err := os.Open(path)
	if err != nil {
		return groupMap
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		parts := strings.Split(line, ":")
		if len(parts) >= 3 {
			groupName := parts[0]
			var gid int
			fmt.Sscanf(parts[2], "%d", &gid)
			groupMap[groupName] = gid
		}
	}
	return groupMap
}

// AddGroupToFile appends a group to /etc/group file
func AddGroupToFile(groupName string, desc string, gid int) error {
	file, err := os.OpenFile("etc/group", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer file.Close()

	line := fmt.Sprintf("%s:%s:%d:\n", groupName, desc, gid)
	_, err = file.WriteString(line)
	return err
}

// Password and user management functions

// Min helper function

// GetUsernameByUID looks up username from UID in passwd file
func GetUsernameByUID(uid int, config *Config) string {
	file, err := os.Open(config.PasswdFile)
	if err != nil {
		return fmt.Sprintf("%d", uid)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Split(line, ":")
		if len(parts) >= 1 {
			// Try both: exact UID match and username as fallback
			if len(parts) >= 3 {
				var fileUID int
				fmt.Sscanf(parts[2], "%d", &fileUID)
				if fileUID == uid {
					return parts[0]
				}
			}
		}
	}
	return fmt.Sprintf("%d", uid)
}

// GetGroupnameByGID looks up groupname from GID using the groupMap
func GetGroupnameByGID(gid int, groupMap map[string]int) string {
	// Reverse lookup: find groupname that maps to this GID
	for groupName, groupGID := range groupMap {
		if groupGID == gid {
			return groupName
		}
	}
	return fmt.Sprintf("%d", gid)
}

// AddUserToPasswd appends a new user entry to the passwd file.
// If the user already exists, it replaces the hash.
func AddUserToPasswd(username, hash, path string) error {
	existing, _ := os.ReadFile(path)
	lines := strings.Split(string(existing), "\n")

	newLine := fmt.Sprintf("%s:%s:100:300:%s:/site:/bin/false", username, hash, time.Now().Format("02-01-06"))

	found := false
	for i, line := range lines {
		if strings.HasPrefix(line, username+":") {
			lines[i] = newLine
			found = true
			break
		}
	}
	if !found {
		// Remove trailing empty line before appending
		for len(lines) > 0 && lines[len(lines)-1] == "" {
			lines = lines[:len(lines)-1]
		}
		lines = append(lines, newLine)
	}

	return os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0600)
}

// RemoveUserFromPasswd removes a user entry from the passwd file.
func RemoveUserFromPasswd(username, path string) error {
	existing, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := strings.Split(string(existing), "\n")

	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		if !strings.HasPrefix(line, username+":") {
			kept = append(kept, line)
		}
	}

	return os.WriteFile(path, []byte(strings.Join(kept, "\n")), 0600)
}

// RenameUserInPasswd renames a passwd entry while preserving the existing hash and fields.
func RenameUserInPasswd(oldUsername, newUsername, path string) error {
	existing, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := strings.Split(string(existing), "\n")
	changed := false
	for i, line := range lines {
		if !strings.HasPrefix(line, oldUsername+":") {
			continue
		}
		parts := strings.Split(line, ":")
		if len(parts) == 0 {
			continue
		}
		parts[0] = newUsername
		lines[i] = strings.Join(parts, ":")
		changed = true
		break
	}
	if !changed {
		return fmt.Errorf("user %s not found in passwd", oldUsername)
	}
	return os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0600)
}
