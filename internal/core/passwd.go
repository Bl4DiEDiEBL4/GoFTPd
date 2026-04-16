package core

import (
	"bufio"
	"crypto/md5"
	"crypto/subtle"
	"fmt"
	"os"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
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
// Supported formats:
//   - bcrypt: $2a$/$2b$/$2y$
//   - Apache MD5 (apr1): $apr1$salt$hash
//   - Plaintext (no $ prefix) — strongly discouraged, dev only
// Unknown $-prefixed formats are REJECTED.
func VerifyPassword(plaintext, hash string) bool {
	// bcrypt
	if strings.HasPrefix(hash, "$2") {
		return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plaintext)) == nil
	}
	
	// Apache MD5 (apr1)
	if strings.HasPrefix(hash, "$apr1$") {
		return verifyApr1(plaintext, hash)
	}
	
	// Plaintext fallback (dev only — no $ prefix)
	if !strings.HasPrefix(hash, "$") {
		return subtle.ConstantTimeCompare([]byte(hash), []byte(plaintext)) == 1
	}
	
	// Unknown $-prefixed format — REJECT
	return false
}

// HashPassword creates a bcrypt hash (default cost 10).
func HashPassword(plaintext string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(plaintext), 10)
	if err != nil {
		return "", err
	}
	return string(h), nil
}

// verifyApr1 verifies an Apache MD5 crypt hash ($apr1$salt$hash)
// Implements Apache's modified MD5 crypt scheme
func verifyApr1(password, hash string) bool {
	parts := strings.SplitN(hash, "$", 4)
	if len(parts) != 4 || parts[1] != "apr1" {
		return false
	}
	salt := parts[2]
	expected := parts[3]
	
	computed := apr1Crypt(password, salt)
	return subtle.ConstantTimeCompare([]byte(computed), []byte(expected)) == 1
}

// apr1Crypt implements the Apache MD5 crypt algorithm
func apr1Crypt(password, salt string) string {
	pw := []byte(password)
	s := []byte(salt)
	
	// Alt sum = md5(password + salt + password)
	altSum := md5.Sum(append(append(append([]byte{}, pw...), s...), pw...))
	
	// Initial context: password + "$apr1$" + salt
	ctx := md5.New()
	ctx.Write(pw)
	ctx.Write([]byte("$apr1$"))
	ctx.Write(s)
	
	// Add altSum bytes, one byte per password char, cycling through altSum
	for i := len(pw); i > 0; i -= 16 {
		n := i
		if n > 16 {
			n = 16
		}
		ctx.Write(altSum[:n])
	}
	
	// Weird bit manipulation
	for i := len(pw); i > 0; i >>= 1 {
		if i&1 == 1 {
			ctx.Write([]byte{0})
		} else {
			ctx.Write(pw[:1])
		}
	}
	
	sum := ctx.Sum(nil)
	
	// 1000 iterations of mixing
	for i := 0; i < 1000; i++ {
		ctx := md5.New()
		if i&1 == 1 {
			ctx.Write(pw)
		} else {
			ctx.Write(sum)
		}
		if i%3 != 0 {
			ctx.Write(s)
		}
		if i%7 != 0 {
			ctx.Write(pw)
		}
		if i&1 == 1 {
			ctx.Write(sum)
		} else {
			ctx.Write(pw)
		}
		sum = ctx.Sum(nil)
	}
	
	// Custom base64 encoding (Apache/crypt style, not standard)
	return apr1Base64(sum)
}

// apr1Base64 encodes bytes using the apr1/crypt base64 alphabet
func apr1Base64(src []byte) string {
	const alphabet = "./0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	
	// Apache's specific byte ordering
	order := [][3]int{
		{0, 6, 12},
		{1, 7, 13},
		{2, 8, 14},
		{3, 9, 15},
		{4, 10, 5},
	}
	
	var out strings.Builder
	for _, o := range order {
		v := uint32(src[o[0]])<<16 | uint32(src[o[1]])<<8 | uint32(src[o[2]])
		for i := 0; i < 4; i++ {
			out.WriteByte(alphabet[v&0x3f])
			v >>= 6
		}
	}
	// Last byte (index 11)
	v := uint32(src[11])
	for i := 0; i < 2; i++ {
		out.WriteByte(alphabet[v&0x3f])
		v >>= 6
	}
	
	return out.String()
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
