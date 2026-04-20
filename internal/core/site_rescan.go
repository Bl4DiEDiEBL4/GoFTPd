package core

import (
	"fmt"
	"path"
	"sort"
	"strings"
)

type rescanReleaseResult struct {
	Path    string
	SFV     string
	Total   int
	OK      int
	Missing int
	Bad     int
	MissingFiles []string
	BadFiles     []string
	Errors  []string
}

func (s *Session) HandleSiteRescan(args []string) bool {
	if len(args) == 0 || strings.TrimSpace(strings.Join(args, " ")) == "" {
		fmt.Fprintf(s.Conn, "501 Usage: SITE RESCAN <path|path/*>\r\n")
		return false
	}
	if s.Config.Mode != "master" || s.MasterManager == nil {
		fmt.Fprintf(s.Conn, "550 SITE RESCAN is only available in master mode.\r\n")
		return false
	}
	bridge, ok := s.MasterManager.(MasterBridge)
	if !ok {
		fmt.Fprintf(s.Conn, "550 Master not initialized.\r\n")
		return false
	}

	target := resolveSitePath(s.CurrentDir, strings.TrimSpace(strings.Join(args, " ")))
	aclTarget := strings.TrimSuffix(target, "/*")
	if s.ACLEngine != nil && !s.ACLEngine.CanPerform(s.User, "RESCAN", path.Join(s.Config.ACLBasePath, aclTarget)) {
		fmt.Fprintf(s.Conn, "550 Access Denied: Insufficient flags.\r\n")
		return false
	}
	releases := expandRescanTargets(bridge, target)
	if len(releases) == 0 {
		fmt.Fprintf(s.Conn, "550 No matching release directories for %s\r\n", target)
		return false
	}

	fmt.Fprintf(s.Conn, "200- Starting SFV rescan for %s\r\n", target)
	for _, release := range releases {
		result := s.rescanRelease(bridge, release)
		writeRescanResult(s, result)
	}
	if len(releases) == 1 {
		fmt.Fprintf(s.Conn, "200 Rescan complete: 1 release checked.\r\n")
	} else {
		fmt.Fprintf(s.Conn, "200 Rescan complete: %d releases checked.\r\n", len(releases))
	}
	return false
}

func (s *Session) rescanRelease(bridge MasterBridge, releasePath string) rescanReleaseResult {
	result := rescanReleaseResult{Path: releasePath}
	sfvName, ok := findSFV(bridge, releasePath)
	if !ok {
		result.Errors = append(result.Errors, "no SFV found")
		return result
	}
	result.SFV = sfvName

	sfvPath := path.Join(releasePath, sfvName)
	entries, err := bridge.GetSFVInfo(sfvPath)
	if err != nil {
		result.Errors = append(result.Errors, err.Error())
		return result
	}
	bridge.CacheSFV(releasePath, sfvName, entries)
	result.Total = len(entries)

	for _, entry := range entries {
		filePath := path.Join(releasePath, entry.FileName)
		missingPath := filePath + "-MISSING"

		if bridge.GetFileSize(filePath) < 0 {
			result.Missing++
			result.MissingFiles = append(result.MissingFiles, entry.FileName)
			_ = bridge.MarkFileMissing(filePath)
			if err := bridge.WriteFile(missingPath, []byte{}); err != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("%s missing marker failed: %v", entry.FileName, err))
			}
			continue
		}

		checksum, err := bridge.ChecksumFile(filePath)
		if err != nil || checksum != entry.CRC32 {
			result.Bad++
			result.BadFiles = append(result.BadFiles, entry.FileName)
			if err := bridge.DeleteFile(filePath); err != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("%s delete failed: %v", entry.FileName, err))
			}
			_ = bridge.MarkFileMissing(filePath)
			if err := bridge.WriteFile(missingPath, []byte{}); err != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("%s missing marker failed: %v", entry.FileName, err))
			}
			continue
		}

		result.OK++
		_ = bridge.SyncPresentFile(filePath, checksum)
		if bridge.GetFileSize(missingPath) >= 0 {
			_ = bridge.DeleteFile(missingPath)
		}
	}
	return result
}

func writeRescanResult(s *Session, result rescanReleaseResult) {
	fmt.Fprintf(s.Conn, "200- Rescanning %s\r\n", result.Path)
	if result.SFV != "" {
		fmt.Fprintf(s.Conn, "200- SFV: %s (%d files)\r\n", result.SFV, result.Total)
	}
	for _, errText := range result.Errors {
		fmt.Fprintf(s.Conn, "200- ERROR: %s\r\n", errText)
	}
	if result.SFV != "" {
		fmt.Fprintf(s.Conn, "200- OK: %d Missing: %d Bad: %d\r\n", result.OK, result.Missing, result.Bad)
		for _, fileName := range result.MissingFiles {
			fmt.Fprintf(s.Conn, "200- MISSING: %s\r\n", fileName)
		}
		for _, fileName := range result.BadFiles {
			fmt.Fprintf(s.Conn, "200- BAD: %s\r\n", fileName)
		}
	}
	fmt.Fprintf(s.Conn, "200-  \r\n")
}

func resolveSitePath(currentDir, target string) string {
	if strings.HasPrefix(target, "/") {
		return path.Clean(target)
	}
	return path.Clean(path.Join(currentDir, target))
}

func expandRescanTargets(bridge MasterBridge, target string) []string {
	if strings.HasSuffix(target, "/*") {
		parent := strings.TrimSuffix(target, "/*")
		entries := bridge.ListDir(parent)
		out := make([]string, 0, len(entries))
		for _, entry := range entries {
			if entry.IsDir {
				out = append(out, path.Join(parent, entry.Name))
			}
		}
		sort.Strings(out)
		return out
	}
	return []string{target}
}

func findSFV(bridge MasterBridge, dirPath string) (string, bool) {
	entries := bridge.ListDir(dirPath)
	sfvs := make([]string, 0, 1)
	for _, entry := range entries {
		if entry.IsDir {
			continue
		}
		if strings.HasSuffix(strings.ToLower(entry.Name), ".sfv") {
			sfvs = append(sfvs, entry.Name)
		}
	}
	sort.Strings(sfvs)
	if len(sfvs) == 0 {
		return "", false
	}
	return sfvs[0], true
}
