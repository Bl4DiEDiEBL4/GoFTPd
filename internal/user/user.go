package user

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// StatLine matches userfile format: Files, Bytes, Meta
type StatLine struct {
	Files int64 `yaml:"files"`
	Bytes int64 `yaml:"bytes"`
	Meta  int64 `yaml:"meta"`
}

type User struct {
	Name         string            `yaml:"name"`
	Password     string            `yaml:"password"`
	UID          int               `yaml:"uid"`
	GID          int               `yaml:"gid"`
	Flags        string            `yaml:"flags"`
	Tagline      string            `yaml:"tagline"`
	HomeDir      string            `yaml:"homedir"`
	CurrentDir   string            `yaml:"current_dir"`  // Runtime: current FTP dir
	Added        int64             `yaml:"added"`
	LastLogin    int64             `yaml:"last_login"`
	Expires      int64             `yaml:"expires"`
	Credits      int64             `yaml:"credits"`
	Ratio        int               `yaml:"ratio"`
	LeechRatio   int               `yaml:"leech_ratio"`
	SectRatio    map[string]int    `yaml:"sect_ratio"`
	Groups       map[string]int    `yaml:"groups"`
	PrimaryGroup string            `yaml:"primary_group"` // Primary group for file ownership
	IPs          []string          `yaml:"ips"`
	Colors       map[string]string `yaml:"colors"`
	
	// Throughput Stats (files, bytes, meta)
	AllUp     StatLine          `yaml:"allup"`
	AllDn     StatLine          `yaml:"alldn"`
	WkUp      StatLine          `yaml:"wkup"`
	WkDn      StatLine          `yaml:"wkdn"`
	DayUp     StatLine          `yaml:"dayup"`
	DayDn     StatLine          `yaml:"daydn"`
	MonthUp   StatLine          `yaml:"monthup"`
	MonthDn   StatLine          `yaml:"monthdn"`
	
	// Nuke Stats
	NukeStat  StatLine          `yaml:"nukestat"`
	
	// Section-specific stats (future)
	SectStats map[string]StatLine `yaml:"sect_stats"`
	
	// Slot Configuration
	UploadSlots   int `yaml:"upload_slots"`   // Max concurrent uploads
	DownloadSlots int `yaml:"download_slots"` // Max concurrent downloads
}

// LoadUser reads user file - supports userfile format
func LoadUser(name string, groupMap map[string]int) (*User, error) {
	// Use exact case - usernames are case-sensitive like goftpd
	path := filepath.Join("etc", "users", name)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	fmt.Printf("[DEBUG] Loading user file: %s\n", path)

	// Parse userfile format
	u := &User{
		Name:   name,  // Keep original case
		Groups: make(map[string]int),
		IPs:    []string{},
		UID:    1000,  // default
		GID:    300,   // default
	}
	
	// Load UID/GID from passwd file
	if passwdData, err := os.ReadFile("etc/passwd"); err == nil {
		lines := strings.Split(string(passwdData), "\n")
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			parts := strings.Split(line, ":")
			if len(parts) >= 4 && parts[0] == name {
				fmt.Sscanf(parts[2], "%d", &u.UID)
				fmt.Sscanf(parts[3], "%d", &u.GID)
				break
			}
		}
	}
	
	// Simple goftpd parser inline
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		
		parts := strings.Fields(line)
		if len(parts) == 0 {
			continue
		}
		
		cmd := parts[0]
		switch cmd {
		case "HOMEDIR":
			if len(parts) > 1 {
				u.HomeDir = parts[1]
			}
		case "FLAGS":
			if len(parts) > 1 {
				u.Flags = parts[1]
			}
		case "TAGLINE":
			if len(parts) > 1 {
				u.Tagline = strings.Join(parts[1:], " ")
			}
		case "DIR":
			if len(parts) > 1 {
				u.HomeDir = parts[1]
			}
		case "RATIO":
			if len(parts) > 1 {
				fmt.Sscanf(parts[1], "%d", &u.Ratio)
			}
		case "CREDITS":
			if len(parts) > 1 {
				fmt.Sscanf(parts[1], "%d", &u.Credits)
			}
		case "ALLUP", "ALLDN", "WKUP", "WKDN", "DAYUP", "DAYDN", "MONTHUP", "MONTHDN":
			if len(parts) >= 4 {
				var files, bytes, meta int64
				fmt.Sscanf(parts[1], "%d", &files)
				fmt.Sscanf(parts[2], "%d", &bytes)
				fmt.Sscanf(parts[3], "%d", &meta)
				stat := StatLine{Files: files, Bytes: bytes, Meta: meta}
				
				switch cmd {
				case "ALLUP":
					u.AllUp = stat
				case "ALLDN":
					u.AllDn = stat
				case "WKUP":
					u.WkUp = stat
				case "WKDN":
					u.WkDn = stat
				case "DAYUP":
					u.DayUp = stat
				case "DAYDN":
					u.DayDn = stat
				case "MONTHUP":
					u.MonthUp = stat
				case "MONTHDN":
					u.MonthDn = stat
				}
			}
		case "NUKE":
			if len(parts) >= 4 {
				var last, times, bytes int64
				fmt.Sscanf(parts[1], "%d", &last)
				fmt.Sscanf(parts[2], "%d", &times)
				fmt.Sscanf(parts[3], "%d", &bytes)
				u.NukeStat = StatLine{Files: times, Bytes: bytes, Meta: last}
			}
		case "TIME":
			if len(parts) >= 3 {
				var lastOn int64
				fmt.Sscanf(parts[2], "%d", &lastOn)
				u.LastLogin = lastOn
			}
		case "ADDED":
			if len(parts) > 1 {
				fmt.Sscanf(parts[1], "%d", &u.Added)
			}
		case "EXPIRES":
			if len(parts) > 1 {
				fmt.Sscanf(parts[1], "%d", &u.Expires)
			}
		case "GROUP":
			if len(parts) >= 2 {
				group := parts[1]
				isAdmin := 0
				if len(parts) > 2 {
					fmt.Sscanf(parts[2], "%d", &isAdmin)
				}
				u.Groups[group] = isAdmin
			}
		case "PRIMARY", "PRIMARY_GROUP":
			if len(parts) >= 2 {
				u.PrimaryGroup = parts[1]
			}
		case "IP":
			if len(parts) > 1 {
				u.IPs = append(u.IPs, parts[1])
			}
		case "UPLOADSLOTS":
			if len(parts) > 1 {
				fmt.Sscanf(parts[1], "%d", &u.UploadSlots)
			}
		case "DOWNLOADSLOTS":
			if len(parts) > 1 {
				fmt.Sscanf(parts[1], "%d", &u.DownloadSlots)
			}
		case "TIMEFRAME":
			if len(parts) > 1 {
				fmt.Sscanf(parts[1], "%d", &u.Expires)
			}
		}
	}
	
	if u.HomeDir == "" {
		u.HomeDir = "/"
	}
	u.CurrentDir = u.HomeDir
	
	// Set GID based on primary group
	if groupMap != nil {
		if u.PrimaryGroup != "" {
			// Use explicitly set primary group
			if gid, ok := groupMap[u.PrimaryGroup]; ok {
				u.GID = gid
			}
		} else if len(u.Groups) > 0 {
			// Fallback: use first group (alphabetically sorted for determinism)
			var groups []string
			for g := range u.Groups {
				groups = append(groups, g)
			}
			// Sort for deterministic order
			sort.Strings(groups)
			if gid, ok := groupMap[groups[0]]; ok {
				u.GID = gid
				u.PrimaryGroup = groups[0]
			}
		}
	}
	
	return u, nil
}

// Save writes user back to userfile-format file
func (u *User) Save() error {
	// Use exact case
	path := filepath.Join("etc", "users", u.Name)
	
	dir := filepath.Dir(path)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		os.MkdirAll(dir, 0755)
	}
	
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()

	fmt.Fprintf(file, "USER Added by GoFTPd\n")
	fmt.Fprintf(file, "GENERAL 0,120 -1 0 0\n")
	fmt.Fprintf(file, "LOGINS 0 0 -1 -1\n")
	fmt.Fprintf(file, "TIMEFRAME 0 0\n")
	fmt.Fprintf(file, "FLAGS %s\n", u.Flags)
	fmt.Fprintf(file, "TAGLINE %s\n", u.Tagline)
	fmt.Fprintf(file, "DIR %s\n", u.HomeDir)
	fmt.Fprintf(file, "ADDED %d goftpd\n", u.Added)
	fmt.Fprintf(file, "EXPIRES %d\n", u.Expires)
	fmt.Fprintf(file, "CREDITS %d\n", u.Credits)
	fmt.Fprintf(file, "RATIO %d\n", u.Ratio)
	fmt.Fprintf(file, "ALLUP %d %d %d\n", u.AllUp.Files, u.AllUp.Bytes, u.AllUp.Meta)
	fmt.Fprintf(file, "ALLDN %d %d %d\n", u.AllDn.Files, u.AllDn.Bytes, u.AllDn.Meta)
	fmt.Fprintf(file, "WKUP %d %d %d\n", u.WkUp.Files, u.WkUp.Bytes, u.WkUp.Meta)
	fmt.Fprintf(file, "WKDN %d %d %d\n", u.WkDn.Files, u.WkDn.Bytes, u.WkDn.Meta)
	fmt.Fprintf(file, "DAYUP %d %d %d\n", u.DayUp.Files, u.DayUp.Bytes, u.DayUp.Meta)
	fmt.Fprintf(file, "DAYDN %d %d %d\n", u.DayDn.Files, u.DayDn.Bytes, u.DayDn.Meta)
	fmt.Fprintf(file, "MONTHUP %d %d %d\n", u.MonthUp.Files, u.MonthUp.Bytes, u.MonthUp.Meta)
	fmt.Fprintf(file, "MONTHDN %d %d %d\n", u.MonthDn.Files, u.MonthDn.Bytes, u.MonthDn.Meta)
	fmt.Fprintf(file, "NUKE %d %d %d\n", u.NukeStat.Meta, u.NukeStat.Files, u.NukeStat.Bytes)
	fmt.Fprintf(file, "TIME %d %d 0 0\n", 0, u.LastLogin)

	if u.PrimaryGroup != "" {
		fmt.Fprintf(file, "PRIMARY %s\n", u.PrimaryGroup)
	}

	for group, isAdmin := range u.Groups {
		fmt.Fprintf(file, "GROUP %s %d\n", group, isAdmin)
	}

	for _, ip := range u.IPs {
		fmt.Fprintf(file, "IP %s\n", ip)
	}

	return nil
}

// UpdateStats increments throughput metrics and manages credits
func (u *User) UpdateStats(bytes int64, isUpload bool) {
	if isUpload {
		u.AllUp.Files++
		u.AllUp.Bytes += bytes
		u.WkUp.Files++
		u.WkUp.Bytes += bytes
		u.DayUp.Files++
		u.DayUp.Bytes += bytes
		u.MonthUp.Files++
		u.MonthUp.Bytes += bytes
		
		if u.Ratio > 0 {
			u.Credits += (bytes * int64(u.Ratio))
		}
	} else {
		u.AllDn.Files++
		u.AllDn.Bytes += bytes
		u.WkDn.Files++
		u.WkDn.Bytes += bytes
		u.DayDn.Files++
		u.DayDn.Bytes += bytes
		u.MonthDn.Files++
		u.MonthDn.Bytes += bytes
		
		if u.Ratio > 0 {
			u.Credits -= bytes
			if u.Credits < 0 {
				u.Credits = 0
			}
		}
	}
	u.Save()
}

func (u *User) HasFlag(flag string) bool {
	return strings.Contains(u.Flags, flag)
}

func (u *User) IsInGroup(group string) bool {
	if u.Groups == nil {
		return false
	}
	_, ok := u.Groups[group]
	return ok
}

func (u *User) IsGroupAdmin(group string) bool {
	if u.Groups == nil {
		return false
	}
	val, ok := u.Groups[group]
	return ok && val == 1
}

func (u *User) IsExpired() bool {
	if u.Expires == 0 {
		return false
	}
	return u.Expires < time.Now().Unix()
}


func (u *User) HasEnoughCredits(fileBytes int64) bool {
	if u.Ratio == 0 {
		return true
	}
	return u.Credits >= fileBytes
}

func (u *User) CanDownload(section string, fileBytes int64) bool {
	if !u.HasFlag("3") {
		return false
	}
	return u.HasEnoughCredits(fileBytes)
}

// SaveUser writes the user data back to the userfile in userfile format
func (u *User) SaveUser() error {
	path := filepath.Join("etc", "users", u.Name)
	
	var buf strings.Builder
	
	// Write all fields in userfile format
	buf.WriteString(fmt.Sprintf("HOMEDIR %s\n", u.HomeDir))
	buf.WriteString(fmt.Sprintf("DIR %s\n", u.HomeDir))
	buf.WriteString(fmt.Sprintf("FLAGS %s\n", u.Flags))
	buf.WriteString(fmt.Sprintf("TAGLINE %s\n", u.Tagline))
	buf.WriteString(fmt.Sprintf("RATIO %d\n", u.Ratio))
	buf.WriteString(fmt.Sprintf("CREDITS %d\n", u.Credits))
	buf.WriteString(fmt.Sprintf("UPLOADSLOTS %d\n", u.UploadSlots))
	buf.WriteString(fmt.Sprintf("DOWNLOADSLOTS %d\n", u.DownloadSlots))
	buf.WriteString(fmt.Sprintf("ADDED %d\n", u.Added))
	buf.WriteString(fmt.Sprintf("EXPIRES %d\n", u.Expires))
	buf.WriteString(fmt.Sprintf("TIMEFRAME %d\n", u.Expires))
	
	// Stats
	buf.WriteString(fmt.Sprintf("ALLUP %d %d %d\n", u.AllUp.Files, u.AllUp.Bytes, u.AllUp.Meta))
	buf.WriteString(fmt.Sprintf("ALLDN %d %d %d\n", u.AllDn.Files, u.AllDn.Bytes, u.AllDn.Meta))
	buf.WriteString(fmt.Sprintf("WKUP %d %d %d\n", u.WkUp.Files, u.WkUp.Bytes, u.WkUp.Meta))
	buf.WriteString(fmt.Sprintf("WKDN %d %d %d\n", u.WkDn.Files, u.WkDn.Bytes, u.WkDn.Meta))
	buf.WriteString(fmt.Sprintf("DAYUP %d %d %d\n", u.DayUp.Files, u.DayUp.Bytes, u.DayUp.Meta))
	buf.WriteString(fmt.Sprintf("DAYDN %d %d %d\n", u.DayDn.Files, u.DayDn.Bytes, u.DayDn.Meta))
	buf.WriteString(fmt.Sprintf("MONTHUP %d %d %d\n", u.MonthUp.Files, u.MonthUp.Bytes, u.MonthUp.Meta))
	buf.WriteString(fmt.Sprintf("MONTHDN %d %d %d\n", u.MonthDn.Files, u.MonthDn.Bytes, u.MonthDn.Meta))
	
	// Nuke stats
	buf.WriteString(fmt.Sprintf("NUKE %d %d %d\n", u.NukeStat.Meta, u.NukeStat.Files, u.NukeStat.Bytes))
	
	// Groups
	buf.WriteString(fmt.Sprintf("PRIMARY_GROUP %s\n", u.PrimaryGroup))
	for group := range u.Groups {
		buf.WriteString(fmt.Sprintf("GROUP %s %d\n", group, u.Groups[group]))
	}
	
	// IPs
	for _, ip := range u.IPs {
		buf.WriteString(fmt.Sprintf("IP %s\n", ip))
	}
	
	return os.WriteFile(path, []byte(buf.String()), 0644)
}
