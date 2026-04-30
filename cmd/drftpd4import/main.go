package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

type tuple struct {
	Files int64
	Bytes int64
	Meta  int64
}

type groupRecord struct {
	Name       string
	GID        int
	GroupSlots int
	LeechSlots int
	Admins     []string
}

type userRecord struct {
	Username     string
	UID          int
	GID          int
	Group        string
	Groups       []string
	Flags        string
	Tagline      string
	Credits      int64
	Ratio        int
	Added        int64
	LastSeen     int64
	Masks        []string
	Stats        map[string]tuple
	PasswdHash   string
	PasswordMode string
}

var digitsOnly = regexp.MustCompile(`^\d+$`)

const (
	passNone   = 0
	passBcrypt = 7
	passGlftpd = 8
)

func main() {
	userdata := flag.String("userdata", "", "DrFTPD v4 userdata root")
	outRoot := flag.String("out", "", "Output directory root")
	importLog := flag.String("log", "", "Import log path")
	existingPasswd := flag.String("existing-passwd", "", "Existing GoFTPd passwd path")
	existingGroup := flag.String("existing-group", "", "Existing GoFTPd group path")
	flag.Parse()

	if strings.TrimSpace(*userdata) == "" || strings.TrimSpace(*outRoot) == "" || strings.TrimSpace(*importLog) == "" {
		fail("missing required flags")
	}

	usersDir := filepath.Join(*userdata, "users", "javabeans")
	groupsDir := filepath.Join(*userdata, "groups", "javabeans")
	if info, err := os.Stat(usersDir); err != nil || !info.IsDir() {
		fail("users dir not found: %s", usersDir)
	}

	etcDir := filepath.Join(*outRoot, "etc")
	outUsersDir := filepath.Join(etcDir, "users")
	outGroupsDir := filepath.Join(etcDir, "groups")
	must(os.MkdirAll(outUsersDir, 0o755))
	must(os.MkdirAll(outGroupsDir, 0o755))

	existingGroupIDs := loadExistingGroupIDs(*existingGroup)
	existingUserIDs := loadExistingUserIDs(*existingPasswd)

	nextGID := 300
	for _, gid := range existingGroupIDs {
		if gid >= nextGID {
			nextGID = gid + 1
		}
	}
	nextUID := 1000
	for _, uid := range existingUserIDs {
		if uid >= nextUID {
			nextUID = uid + 1
		}
	}

	groups := make(map[string]*groupRecord)
	ensureGroup := func(name string) string {
		name = strings.TrimSpace(name)
		if name == "" {
			name = "NoGroup"
		}
		if _, ok := groups[name]; !ok {
			gid := nextGID
			if existing, ok := existingGroupIDs[name]; ok {
				gid = existing
			} else {
				nextGID++
			}
			groups[name] = &groupRecord{
				Name:       name,
				GID:        gid,
				GroupSlots: -1,
				LeechSlots: 0,
			}
		}
		return name
	}

	if info, err := os.Stat(groupsDir); err == nil && info.IsDir() {
		groupEntries, err := os.ReadDir(groupsDir)
		must(err)
		for _, entry := range groupEntries {
			if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".json") {
				continue
			}
			src := filepath.Join(groupsDir, entry.Name())
			raw, err := os.ReadFile(src)
			must(err)
			var data map[string]any
			must(json.Unmarshal(raw, &data))
			name := ensureGroup(strings.TrimSpace(stringValue(data["_groupname"])))
			rec := groups[name]
			keyed := extractKeyedMap(data["_data"])
			rec.GroupSlots = safeInt(keyed["groupslots"], rec.GroupSlots)
			rec.LeechSlots = safeInt(keyed["leechslots"], rec.LeechSlots)
			rec.Admins = dedupeStrings(extractAdminStrings(data["_admins"]))
		}
	}

	var users []userRecord
	entries, err := os.ReadDir(usersDir)
	must(err)
	for _, entry := range entries {
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".json") {
			continue
		}
		src := filepath.Join(usersDir, entry.Name())
		raw, err := os.ReadFile(src)
		must(err)

		var data map[string]any
		must(json.Unmarshal(raw, &data))

		username := strings.TrimSpace(stringValue(data["_username"]))
		if username == "" {
			username = strings.TrimSpace(strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name())))
		}
		if username == "" {
			continue
		}

		primaryGroup := ensureGroup(stringValue(data["_group"]))
		var secondaryGroups []string
		if arr, ok := data["_groups"].([]any); ok {
			seen := map[string]struct{}{}
			for _, item := range arr {
				group := ensureGroup(stringValue(item))
				if group == primaryGroup {
					continue
				}
				if _, ok := seen[group]; ok {
					continue
				}
				seen[group] = struct{}{}
				secondaryGroups = append(secondaryGroups, group)
			}
			sort.Strings(secondaryGroups)
		}

		keyed := extractKeyedMap(data["_data"])
		ratio := safeInt(keyed["ratio"], 3)
		if ratio < 0 {
			ratio = 0
		}
		credits := safeInt64(data["_credits"], 0)
		added := epochFromValue(keyed["created"])
		lastSeen := epochFromValue(keyed["lastseen"])
		tagline := strings.TrimSpace(stringValue(keyed["tagline"]))
		if tagline == "" {
			tagline = "Imported from DrFTPD v4"
		}

		masks := dedupeStrings(flattenMasks(data["_hostMasks"]))
		stats := map[string]tuple{
			"ALLUP":   statTuple(data["_uploadedBytes"], data["_uploadedFiles"], 0),
			"DAYUP":   statTuple(data["_uploadedBytes"], data["_uploadedFiles"], 1),
			"WKUP":    statTuple(data["_uploadedBytes"], data["_uploadedFiles"], 2),
			"MONTHUP": statTuple(data["_uploadedBytes"], data["_uploadedFiles"], 3),
			"ALLDN":   statTuple(data["_downloadedBytes"], data["_downloadedFiles"], 0),
			"DAYDN":   statTuple(data["_downloadedBytes"], data["_downloadedFiles"], 1),
			"WKDN":    statTuple(data["_downloadedBytes"], data["_downloadedFiles"], 2),
			"MONTHDN": statTuple(data["_downloadedBytes"], data["_downloadedFiles"], 3),
		}

		flags := "3"
		allGroups := append([]string{primaryGroup}, secondaryGroups...)
		for _, group := range allGroups {
			switch strings.ToLower(group) {
			case "siteop":
				flags = addFlag(flags, '1')
			case "deleted":
				flags = addFlag(flags, '6')
			}
		}

		hash, mode := derivePassword(data)
		uid := nextUID
		if existing, ok := existingUserIDs[username]; ok {
			uid = existing
		} else {
			nextUID++
		}

		users = append(users, userRecord{
			Username:     username,
			UID:          uid,
			GID:          groups[primaryGroup].GID,
			Group:        primaryGroup,
			Groups:       secondaryGroups,
			Flags:        flags,
			Tagline:      tagline,
			Credits:      credits,
			Ratio:        ratio,
			Added:        added,
			LastSeen:     lastSeen,
			Masks:        masks,
			Stats:        stats,
			PasswdHash:   hash,
			PasswordMode: mode,
		})
	}

	groupNames := make([]string, 0, len(groups))
	for name := range groups {
		groupNames = append(groupNames, name)
	}
	sort.Strings(groupNames)
	sort.Slice(users, func(i, j int) bool {
		return strings.ToLower(users[i].Username) < strings.ToLower(users[j].Username)
	})

	writeGroupFile(filepath.Join(etcDir, "group"), groupNames, groups)
	writePasswdFile(filepath.Join(etcDir, "passwd"), users)
	writeGroupRecords(outGroupsDir, groupNames, groups)
	writeUserRecords(outUsersDir, users)
	writeImportLog(*importLog, usersDir, groupsDir, users, groupNames)
}

func derivePassword(data map[string]any) (string, string) {
	stored := strings.TrimSpace(stringValue(data["_password"]))
	if stored == "" {
		return "!drftpd-reset-required!", "reset-required"
	}
	encryption := safeInt(data["_encryption"], passNone)
	switch encryption {
	case passNone:
		hash, err := bcrypt.GenerateFromPassword([]byte(stored), 10)
		if err != nil {
			return "!drftpd-reset-required!", "reset-required"
		}
		return string(hash), "bcrypt-from-plaintext"
	case passBcrypt:
		if strings.HasPrefix(stored, "$2") {
			return stored, "bcrypt-preserved"
		}
		return "!drftpd-reset-required!", "reset-required"
	case passGlftpd:
		if strings.HasPrefix(stored, "$") {
			return stored, "glftpd-preserved"
		}
		return "!drftpd-reset-required!", "reset-required"
	default:
		return "!drftpd-reset-required!", fmt.Sprintf("reset-required-enc-%d", encryption)
	}
}

func writeGroupFile(path string, groups []string, records map[string]*groupRecord) {
	f, err := os.Create(path)
	must(err)
	defer f.Close()
	w := bufio.NewWriter(f)
	for _, group := range groups {
		_, err = fmt.Fprintf(w, "%s:%s:%d:\n", group, "Imported from DrFTPD v4", records[group].GID)
		must(err)
	}
	must(w.Flush())
}

func writePasswdFile(path string, users []userRecord) {
	f, err := os.Create(path)
	must(err)
	defer f.Close()
	w := bufio.NewWriter(f)
	for _, user := range users {
		_, err = fmt.Fprintf(w, "%s:%s:%d:%d:drftpd4:/site:/bin/false\n", user.Username, user.PasswdHash, user.UID, user.GID)
		must(err)
	}
	must(w.Flush())
}

func writeGroupRecords(dir string, groups []string, records map[string]*groupRecord) {
	for _, group := range groups {
		rec := records[group]
		path := filepath.Join(dir, group)
		f, err := os.Create(path)
		must(err)
		_, err = fmt.Fprintf(f, "GROUP %s\nSLOTS %d %d 0 0\nGROUPNFO Imported from DrFTPD v4\nSIMULT 0\n", group, rec.GroupSlots, rec.LeechSlots)
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
		must(err)
	}
}

func writeUserRecords(dir string, users []userRecord) {
	order := []string{"ALLUP", "ALLDN", "WKUP", "WKDN", "DAYUP", "DAYDN", "MONTHUP", "MONTHDN"}
	for _, user := range users {
		path := filepath.Join(dir, user.Username)
		f, err := os.Create(path)
		must(err)
		w := bufio.NewWriter(f)
		mustf(w, "USER Imported from DrFTPD v4\n")
		mustf(w, "GENERAL 0,0 -1 0 0\n")
		mustf(w, "LOGINS 2 0 -1 -1\n")
		mustf(w, "TIMEFRAME 0 0\n")
		mustf(w, "FLAGS %s\n", user.Flags)
		mustf(w, "TAGLINE %s\n", user.Tagline)
		mustf(w, "HOMEDIR /\n")
		mustf(w, "DIR /\n")
		mustf(w, "ADDED %d\n", user.Added)
		mustf(w, "EXPIRES 0\n")
		mustf(w, "CREDITS %d\n", user.Credits)
		mustf(w, "RATIO %d\n", user.Ratio)
		mustf(w, "UPLOADSLOTS 0\n")
		mustf(w, "DOWNLOADSLOTS 0\n")
		for _, key := range order {
			stat := user.Stats[key]
			mustf(w, "%s %d %d %d\n", key, stat.Files, stat.Bytes, stat.Meta)
		}
		mustf(w, "NUKE 0 0 0\n")
		mustf(w, "TIME 0 %d 0 0\n", user.LastSeen)
		mustf(w, "PRIMARY_GROUP %s\n", user.Group)
		mustf(w, "GROUP %s 0\n", user.Group)
		for _, group := range user.Groups {
			mustf(w, "GROUP %s 0\n", group)
		}
		for _, mask := range user.Masks {
			mustf(w, "IP %s\n", mask)
		}
		must(w.Flush())
		must(f.Close())
	}
}

func writeImportLog(path, usersDir, groupsDir string, users []userRecord, groups []string) {
	f, err := os.Create(path)
	must(err)
	defer f.Close()
	w := bufio.NewWriter(f)
	mustf(w, "mode: drftpd4\n")
	mustf(w, "users_source: %s\n", usersDir)
	mustf(w, "groups_source: %s\n", groupsDir)
	mustf(w, "imported_users: %d\n", len(users))
	mustf(w, "imported_groups: %d\n", len(groups))
	for _, user := range users {
		allGroups := append([]string{user.Group}, user.Groups...)
		mustf(w, "user:%s uid:%d gid:%d primary:%s groups:%s password:%s\n", user.Username, user.UID, user.GID, user.Group, strings.Join(allGroups, ","), user.PasswordMode)
	}
	must(w.Flush())
}

func loadExistingGroupIDs(path string) map[string]int {
	result := make(map[string]int)
	lines := readLines(path)
	for _, line := range lines {
		parts := strings.Split(line, ":")
		if len(parts) < 3 || strings.TrimSpace(parts[0]) == "" {
			continue
		}
		if gid, err := strconv.Atoi(strings.TrimSpace(parts[2])); err == nil {
			result[parts[0]] = gid
		}
	}
	return result
}

func loadExistingUserIDs(path string) map[string]int {
	result := make(map[string]int)
	lines := readLines(path)
	for _, line := range lines {
		parts := strings.Split(line, ":")
		if len(parts) < 3 || strings.TrimSpace(parts[0]) == "" {
			continue
		}
		if uid, err := strconv.Atoi(strings.TrimSpace(parts[2])); err == nil {
			result[parts[0]] = uid
		}
	}
	return result
}

func readLines(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var lines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		lines = append(lines, line)
	}
	return lines
}

func flattenMasks(value any) []string {
	var out []string
	var walk func(any)
	walk = func(v any) {
		switch t := v.(type) {
		case map[string]any:
			host := ""
			for _, key := range []string{"_hostMask", "_hostmask", "_mask", "mask", "hostMask"} {
				if s := strings.TrimSpace(stringValue(t[key])); s != "" {
					host = s
					break
				}
			}
			ident := ""
			for _, key := range []string{"_ident", "ident"} {
				if s := strings.TrimSpace(stringValue(t[key])); s != "" {
					ident = s
					break
				}
			}
			if host != "" {
				if ident != "" && ident != "*" {
					out = append(out, ident+"@"+host)
				} else {
					out = append(out, host)
				}
			}
			for _, child := range t {
				walk(child)
			}
		case []any:
			for _, child := range t {
				walk(child)
			}
		}
	}
	walk(value)
	return out
}

func extractAdminStrings(value any) []string {
	arr, ok := value.([]any)
	if !ok {
		return nil
	}
	var out []string
	for _, item := range arr {
		name := strings.TrimSpace(stringValue(item))
		if name != "" {
			out = append(out, name)
			continue
		}
		if m, ok := item.(map[string]any); ok {
			for _, key := range []string{"_username", "username", "name"} {
				if s := strings.TrimSpace(stringValue(m[key])); s != "" {
					out = append(out, s)
					break
				}
			}
		}
	}
	return out
}

func extractKeyedMap(value any) map[string]any {
	result := make(map[string]any)
	data, ok := value.(map[string]any)
	if !ok {
		return result
	}
	keys, ok := data["@keys"].([]any)
	if !ok {
		return result
	}
	items, ok := data["@items"].([]any)
	if !ok {
		return result
	}
	for i, rawKey := range keys {
		if i >= len(items) {
			break
		}
		meta, ok := rawKey.(map[string]any)
		if !ok {
			continue
		}
		keyName := strings.ToLower(strings.TrimSpace(stringValue(meta["_key"])))
		if keyName == "" {
			continue
		}
		item := items[i]
		if itemMap, ok := item.(map[string]any); ok {
			if value, ok := itemMap["value"]; ok {
				result[keyName] = value
				continue
			}
		}
		result[keyName] = item
	}
	return result
}

func statTuple(bytesValue, filesValue any, index int) tuple {
	bytesArr, ok1 := bytesValue.([]any)
	filesArr, ok2 := filesValue.([]any)
	if !ok1 || !ok2 || index >= len(bytesArr) || index >= len(filesArr) {
		return tuple{}
	}
	return tuple{
		Files: safeInt64(filesArr[index], 0),
		Bytes: safeInt64(bytesArr[index], 0),
		Meta:  0,
	}
}

func epochFromValue(value any) int64 {
	switch v := value.(type) {
	case nil:
		return 0
	case float64:
		return int64(v)
	case int:
		return int64(v)
	case int64:
		return v
	case string:
		text := strings.TrimSpace(v)
		if text == "" {
			return 0
		}
		if digitsOnly.MatchString(text) {
			n, err := strconv.ParseInt(text, 10, 64)
			if err == nil {
				return n
			}
		}
		if strings.HasSuffix(text, "Z") {
			text = strings.TrimSuffix(text, "Z") + "+00:00"
		}
		if t, err := time.Parse(time.RFC3339, text); err == nil {
			return t.Unix()
		}
		return 0
	case map[string]any:
		for _, key := range []string{"value", "_time", "time", "millis", "epoch", "timestamp"} {
			if raw, ok := v[key]; ok {
				return epochFromValue(raw)
			}
		}
		return 0
	default:
		return 0
	}
}

func safeInt(value any, fallback int) int {
	return int(safeInt64(value, int64(fallback)))
}

func safeInt64(value any, fallback int64) int64 {
	switch v := value.(type) {
	case nil:
		return fallback
	case bool:
		if v {
			return 1
		}
		return 0
	case float64:
		return int64(v)
	case float32:
		return int64(v)
	case int:
		return int64(v)
	case int64:
		return v
	case json.Number:
		if n, err := v.Int64(); err == nil {
			return n
		}
		if f, err := v.Float64(); err == nil {
			return int64(f)
		}
		return fallback
	case string:
		text := strings.TrimSpace(v)
		if text == "" {
			return fallback
		}
		if n, err := strconv.ParseInt(text, 10, 64); err == nil {
			return n
		}
		if f, err := strconv.ParseFloat(text, 64); err == nil {
			return int64(f)
		}
		return fallback
	default:
		return fallback
	}
}

func stringValue(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return v
	case float64:
		return strconv.FormatInt(int64(v), 10)
	case bool:
		if v {
			return "true"
		}
		return "false"
	default:
		return fmt.Sprintf("%v", v)
	}
}

func dedupeStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func addFlag(flags string, flag rune) string {
	if strings.ContainsRune(flags, flag) {
		return flags
	}
	return flags + string(flag)
}

func must(err error) {
	if err != nil {
		fail("%v", err)
	}
}

func mustf(w *bufio.Writer, format string, args ...any) {
	_, err := fmt.Fprintf(w, format, args...)
	must(err)
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
