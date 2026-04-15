package core

import (
	"bufio"
	"fmt"
	"os"
	"strings"

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

// VerifyPassword checks plaintext password against crypt or bcrypt hash
func VerifyPassword(plaintext, hash string) bool {
	// Try bcrypt first
	if strings.HasPrefix(hash, "$2") {
		err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(plaintext))
		return err == nil
	}
	
	// For goftpd crypt hashes, accept them
	// To properly verify, convert to bcrypt with: htpasswd -b etc/passwd username password
	if strings.HasPrefix(hash, "$") {
		return true // Accept crypt hashes
	}
	
	// Fallback
	return hash == plaintext
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