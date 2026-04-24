package core

import (
	"fmt"
	"log"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"goftpd/internal/user"
)

func (s *Session) HandleSiteNuke(args []string) bool {
	// Parse: SITE NUKE <dir> <x multiplier> [reason]
	if len(args) < 2 {
		fmt.Fprintf(s.Conn, "501 Usage: SITE NUKE <dir> <xN> [reason]\r\n")
		return false
	}

	target := args[0]
	multiplierStr := strings.TrimPrefix(strings.ToLower(args[1]), "x")
	multiplier, err := strconv.Atoi(multiplierStr)
	if err != nil || multiplier <= 0 {
		fmt.Fprintf(s.Conn, "501 Invalid multiplier (use x1, x2, x10, etc).\r\n")
		return false
	}

	// Check max multiplier
	if multiplier > s.Config.NukeMaxMultiplier {
		fmt.Fprintf(s.Conn, "550 Multiplier exceeds max (%d).\r\n", s.Config.NukeMaxMultiplier)
		return false
	}

	reason := "No reason"
	if len(args) > 2 {
		reason = strings.Join(args[2:], " ")
	}

	if bridge, ok := s.masterBridge(); ok {
		return s.handleSiteNukeVFS(bridge, target, multiplier, reason)
	}

	// Get target directory
	targetPath := target
	if !strings.HasPrefix(targetPath, "/") {
		targetPath = path.Join(s.CurrentDir, targetPath)
	}
	aclPath := path.Join(s.Config.ACLBasePath, path.Clean(targetPath))
	if !s.ACLEngine.CanPerform(s.User, "NUKE", aclPath) {
		fmt.Fprintf(s.Conn, "550 Insufficient flags.\r\n")
		return false
	}
	fullPath := filepath.Join(s.Config.StoragePath, s.CurrentDir, target)
	dirName := filepath.Base(fullPath)

	// Scan files and collect uploader info
	entries, err := os.ReadDir(fullPath)
	if err != nil {
		fmt.Fprintf(s.Conn, "550 Cannot read directory.\r\n")
		return false
	}

	// Map: username -> total_bytes
	uploaderBytes := make(map[string]int64)

	for _, entry := range entries {
		if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			continue
		}

		// Get file owner
		stat := info.Sys().(*syscall.Stat_t)
		username := GetUsernameByUID(int(stat.Uid), s.Config)
		uploaderBytes[username] += info.Size()
	}

	// Apply nuke to each uploader
	now := time.Now().Unix()
	totalNuked := int64(0)
	nukeeLine := FormatNukees(BuildNukeUserStats(uploaderBytes), []string{"goftpd", s.User.Name})

	for username, bytes := range uploaderBytes {
		// Load user
		u, err := user.LoadUser(username, s.GroupMap)
		if err != nil {
			continue
		}

		// Calculate credits to remove using user's ratio
		// User normally gets: bytes * ratio
		// Nuke removes: (bytes * ratio) * multiplier
		baseCredits := bytes * int64(u.Ratio)
		nukedCredits := baseCredits * int64(multiplier)

		// Remove credits
		u.Credits -= nukedCredits
		if u.Credits < 0 {
			u.Credits = 0
		}

		// Update NUKE stats
		u.NukeStat.Meta = now     // Last nuke timestamp
		u.NukeStat.Files += 1     // Times nuked
		u.NukeStat.Bytes += bytes // Total bytes nuked

		// Save user
		if err := u.Save(); err == nil && s.Config.Debug {
			log.Printf("[NUKE] Updated %s: -%d credits (ratio %d), %d times nuked",
				username, nukedCredits, u.Ratio, u.NukeStat.Files)
		}

		totalNuked += nukedCredits
	}

	// Rename directory
	newName := fmt.Sprintf("[NUKED]-%s", dirName)
	newPath := filepath.Join(filepath.Dir(fullPath), newName)

	if err := os.Rename(fullPath, newPath); err != nil {
		fmt.Fprintf(s.Conn, "550 Rename failed: %v\r\n", err)
		return false
	}

	s.emitEvent(EventNuke, path.Join(s.CurrentDir, target), dirName, 0, 0, map[string]string{
		"multiplier": strconv.Itoa(multiplier),
		"reason":     reason,
		"users":      strconv.Itoa(len(uploaderBytes)),
		"nukees":     nukeeLine,
	})
	fmt.Fprintf(s.Conn, "200 Nuked: x%d multiplier, %d MB, %d users affected, %d credits removed. Reason: %s\r\n",
		multiplier, len(uploaderBytes), len(uploaderBytes), totalNuked, reason)
	return true
}

func (s *Session) HandleSiteUnnuke(args []string) bool {
	// Parse: SITE UNNUKE <dir> [reason]
	if len(args) < 1 {
		fmt.Fprintf(s.Conn, "501 Usage: SITE UNNUKE <dir> [reason]\r\n")
		return false
	}

	target := args[0]
	if bridge, ok := s.masterBridge(); ok {
		return s.handleSiteUnnukeVFS(bridge, target)
	}

	targetPath := target
	if !strings.HasPrefix(targetPath, "/") {
		targetPath = path.Join(s.CurrentDir, targetPath)
	}
	aclPath := path.Join(s.Config.ACLBasePath, path.Clean(targetPath))
	if !s.ACLEngine.CanPerform(s.User, "UNNUKE", aclPath) {
		fmt.Fprintf(s.Conn, "550 Insufficient flags.\r\n")
		return false
	}
	fullPath := filepath.Join(s.Config.StoragePath, s.CurrentDir, target)
	dirName := filepath.Base(fullPath)

	// Check if directory is nuked
	if !strings.HasPrefix(dirName, "[NUKED]-") {
		fmt.Fprintf(s.Conn, "550 Directory is not nuked.\r\n")
		return false
	}

	// Extract original name
	originalName := strings.TrimPrefix(dirName, "[NUKED]-")
	newPath := filepath.Join(filepath.Dir(fullPath), originalName)

	// Scan files to restore credits
	entries, err := os.ReadDir(fullPath)
	if err != nil {
		fmt.Fprintf(s.Conn, "550 Cannot read directory.\r\n")
		return false
	}

	uploaderBytes := make(map[string]int64)
	for _, entry := range entries {
		if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			continue
		}

		stat := info.Sys().(*syscall.Stat_t)
		username := GetUsernameByUID(int(stat.Uid), s.Config)
		uploaderBytes[username] += info.Size()
	}

	// Restore credits to each uploader
	totalRestored := int64(0)

	for username, bytes := range uploaderBytes {
		u, err := user.LoadUser(username, s.GroupMap)
		if err != nil {
			continue
		}

		// Calculate credits to restore using user's ratio and max multiplier
		// Restore: (bytes * ratio) * max_multiplier (worst case)
		baseCredits := bytes * int64(u.Ratio)
		nukedCredits := baseCredits * int64(s.Config.NukeMaxMultiplier)

		u.Credits += nukedCredits

		// Update NUKE stats - clear them
		u.NukeStat = user.StatLine{}

		if err := u.Save(); err == nil && s.Config.Debug {
			log.Printf("[UNNUKE] Restored %s: +%d credits (ratio %d)", username, nukedCredits, u.Ratio)
		}

		totalRestored += nukedCredits
	}

	// Rename directory back
	if err := os.Rename(fullPath, newPath); err != nil {
		fmt.Fprintf(s.Conn, "550 Rename failed: %v\r\n", err)
		return false
	}

	s.emitEvent(EventUnnuke, newPath, originalName, 0, 0, map[string]string{
		"users": strconv.Itoa(len(uploaderBytes)),
	})
	fmt.Fprintf(s.Conn, "200 Unnuked: %d MB, %d users affected, %d credits restored.\r\n",
		len(uploaderBytes), len(uploaderBytes), totalRestored)
	return true
}

func (s *Session) handleSiteNukeVFS(bridge MasterBridge, target string, multiplier int, reason string) bool {
	dirPath, ok := s.resolveSiteDir(bridge, target, false)
	if !ok {
		return false
	}
	dirName := path.Base(dirPath)
	if strings.HasPrefix(dirName, "[NUKED]-") {
		fmt.Fprintf(s.Conn, "550 Directory is already nuked: %s\r\n", dirPath)
		return false
	}
	aclPath := path.Join(s.Config.ACLBasePath, dirPath)
	if !s.ACLEngine.CanPerform(s.User, "NUKE", aclPath) {
		fmt.Fprintf(s.Conn, "550 Insufficient flags for %s.\r\n", dirPath)
		return false
	}

	uploaderBytes := VFSUploaderBytes(bridge.ListDir(dirPath))
	nukeeLine := FormatNukees(BuildNukeUserStats(uploaderBytes), []string{"goftpd", s.User.Name})
	result, err := PerformSystemNuke(bridge, s.GroupMap, dirPath, multiplier, reason, "[NUKED]-")
	if err != nil {
		fmt.Fprintf(s.Conn, "550 Nuke failed: %v\r\n", err)
		return false
	}

	s.emitEvent(EventNuke, dirPath, dirName, 0, 0, map[string]string{
		"multiplier": strconv.Itoa(multiplier),
		"reason":     reason,
		"users":      strconv.Itoa(result.UsersAffected),
		"nukees":     nukeeLine,
	})
	fmt.Fprintf(s.Conn, "200 Nuked %s: x%d multiplier, %d MB, %d users affected, %d credits removed. Reason: %s\r\n",
		dirPath, multiplier, BytesToMB(SumBytes(VFSUploaderBytes(bridge.ListDir(result.NewPath)))), result.UsersAffected, result.TotalCreditsRemoved, reason)
	return true
}

func (s *Session) handleSiteUnnukeVFS(bridge MasterBridge, target string) bool {
	dirPath, ok := s.resolveSiteDir(bridge, target, true)
	if !ok {
		return false
	}
	dirName := path.Base(dirPath)
	if !strings.HasPrefix(dirName, "[NUKED]-") {
		fmt.Fprintf(s.Conn, "550 Directory is not nuked: %s\r\n", dirPath)
		return false
	}
	aclPath := path.Join(s.Config.ACLBasePath, dirPath)
	if !s.ACLEngine.CanPerform(s.User, "UNNUKE", aclPath) {
		fmt.Fprintf(s.Conn, "550 Insufficient flags for %s.\r\n", dirPath)
		return false
	}

	uploaderBytes := VFSUploaderBytes(bridge.ListDir(dirPath))
	totalRestored := ApplyUnnukeCredits(s.GroupMap, uploaderBytes, s.Config.NukeMaxMultiplier)
	originalName := strings.TrimPrefix(dirName, "[NUKED]-")
	bridge.RenameFile(dirPath, path.Dir(dirPath), originalName)

	newPath := path.Join(path.Dir(dirPath), originalName)
	s.emitEvent(EventUnnuke, newPath, originalName, 0, 0, map[string]string{
		"users": strconv.Itoa(len(uploaderBytes)),
	})
	fmt.Fprintf(s.Conn, "200 Unnuked %s: %d MB, %d users affected, %d credits restored.\r\n",
		dirPath, BytesToMB(SumBytes(uploaderBytes)), len(uploaderBytes), totalRestored)
	return true
}

func (s *Session) masterBridge() (MasterBridge, bool) {
	if s.Config == nil || s.Config.Mode != "master" || s.MasterManager == nil {
		return nil, false
	}
	bridge, ok := s.MasterManager.(MasterBridge)
	return bridge, ok
}

func (s *Session) resolveSiteDir(bridge MasterBridge, target string, wantNuked bool) (string, bool) {
	target = strings.TrimSpace(target)
	if target == "" {
		fmt.Fprintf(s.Conn, "550 Missing directory.\r\n")
		return "", false
	}
	candidate := target
	if !strings.HasPrefix(candidate, "/") {
		candidate = path.Join(s.CurrentDir, candidate)
	}
	candidate = path.Clean(candidate)
	if bridge.FileExists(candidate) {
		if s.dirNukeStateOK(candidate, wantNuked) {
			return candidate, true
		}
	}

	query := strings.TrimPrefix(path.Base(target), "[NUKED]-")
	results := bridge.SearchDirs(query, siteSearchLimit)
	matches := make([]VFSSearchResult, 0, len(results))
	for _, result := range results {
		if !s.dirNukeStateOK(result.Path, wantNuked) {
			continue
		}
		base := path.Base(result.Path)
		cleanBase := strings.TrimPrefix(base, "[NUKED]-")
		if strings.EqualFold(base, target) || strings.EqualFold(cleanBase, target) || strings.EqualFold(cleanBase, query) {
			matches = append(matches, result)
		}
	}
	if len(matches) == 1 {
		return path.Clean(matches[0].Path), true
	}
	if len(matches) == 0 {
		fmt.Fprintf(s.Conn, "550 Directory not found. Try SITE SEARCH %s\r\n", query)
		return "", false
	}
	fmt.Fprintf(s.Conn, "550- Multiple matches for %s; use the full path:\r\n", query)
	for i, match := range matches {
		if i >= 10 {
			fmt.Fprintf(s.Conn, "550- ... and %d more\r\n", len(matches)-i)
			break
		}
		fmt.Fprintf(s.Conn, "550- %s\r\n", match.Path)
	}
	fmt.Fprintf(s.Conn, "550 Ambiguous directory.\r\n")
	return "", false
}

func (s *Session) dirNukeStateOK(dirPath string, wantNuked bool) bool {
	isNuked := strings.HasPrefix(path.Base(dirPath), "[NUKED]-")
	return isNuked == wantNuked
}
