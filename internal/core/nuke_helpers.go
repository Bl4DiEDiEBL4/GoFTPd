package core

import (
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"goftpd/internal/user"
)

type NukeUserStat struct {
	User  string
	Bytes int64
}

type NukeResult struct {
	DirPath             string
	NewPath             string
	ReleaseName         string
	Multiplier          int
	Reason              string
	UserStats           []NukeUserStat
	UsersAffected       int
	TotalCreditsRemoved int64
}

func VFSUploaderBytes(entries []MasterFileEntry) map[string]int64 {
	uploaderBytes := make(map[string]int64)
	for _, entry := range entries {
		if entry.IsDir || strings.HasPrefix(entry.Name, ".") {
			continue
		}
		owner := entry.Owner
		if owner == "" {
			owner = "unknown"
		}
		uploaderBytes[owner] += entry.Size
	}
	return uploaderBytes
}

func BuildNukeUserStats(uploaderBytes map[string]int64) []NukeUserStat {
	stats := make([]NukeUserStat, 0, len(uploaderBytes))
	for username, bytes := range uploaderBytes {
		stats = append(stats, NukeUserStat{User: username, Bytes: bytes})
	}
	sort.Slice(stats, func(i, j int) bool {
		if stats[i].Bytes == stats[j].Bytes {
			return strings.ToLower(stats[i].User) < strings.ToLower(stats[j].User)
		}
		return stats[i].Bytes > stats[j].Bytes
	})
	return stats
}

func FormatNukees(stats []NukeUserStat, excludes []string) string {
	excludeSet := make(map[string]bool, len(excludes))
	for _, name := range excludes {
		name = strings.ToLower(strings.TrimSpace(name))
		if name != "" {
			excludeSet[name] = true
		}
	}
	parts := make([]string, 0, len(stats))
	for _, stat := range stats {
		if stat.Bytes <= 0 {
			continue
		}
		if excludeSet[strings.ToLower(stat.User)] {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s (%.1fMB)", stat.User, float64(stat.Bytes)/1024.0/1024.0))
	}
	return strings.Join(parts, ", ")
}

func ApplyNukeCredits(groupMap map[string]int, uploaderBytes map[string]int64, multiplier int) int64 {
	now := time.Now().Unix()
	totalNuked := int64(0)
	for username, bytes := range uploaderBytes {
		u, err := user.LoadUser(username, groupMap)
		if err != nil {
			continue
		}
		nukedCredits := bytes * int64(u.Ratio) * int64(multiplier)
		u.Credits -= nukedCredits
		if u.Credits < 0 {
			u.Credits = 0
		}
		u.NukeStat.Meta = now
		u.NukeStat.Files++
		u.NukeStat.Bytes += bytes
		_ = u.Save()
		totalNuked += nukedCredits
	}
	return totalNuked
}

func ApplyUnnukeCredits(groupMap map[string]int, uploaderBytes map[string]int64, maxMultiplier int) int64 {
	totalRestored := int64(0)
	for username, bytes := range uploaderBytes {
		u, err := user.LoadUser(username, groupMap)
		if err != nil {
			continue
		}
		restored := bytes * int64(u.Ratio) * int64(maxMultiplier)
		u.Credits += restored
		u.NukeStat = user.StatLine{}
		_ = u.Save()
		totalRestored += restored
	}
	return totalRestored
}

func SumBytes(values map[string]int64) int64 {
	total := int64(0)
	for _, n := range values {
		total += n
	}
	return total
}

func BytesToMB(bytes int64) int64 {
	return bytes / 1024 / 1024
}

func PerformSystemNuke(bridge MasterBridge, groupMap map[string]int, dirPath string, multiplier int, reason string, nukedPrefix string) (*NukeResult, error) {
	dirPath = path.Clean(dirPath)
	releaseName := path.Base(dirPath)
	if strings.HasPrefix(releaseName, nukedPrefix) {
		return nil, fmt.Errorf("directory is already nuked: %s", dirPath)
	}
	uploaderBytes := VFSUploaderBytes(bridge.ListDir(dirPath))
	totalNuked := ApplyNukeCredits(groupMap, uploaderBytes, multiplier)
	newName := nukedPrefix + releaseName
	bridge.RenameFile(dirPath, path.Dir(dirPath), newName)
	return &NukeResult{
		DirPath:             dirPath,
		NewPath:             path.Join(path.Dir(dirPath), newName),
		ReleaseName:         releaseName,
		Multiplier:          multiplier,
		Reason:              reason,
		UserStats:           BuildNukeUserStats(uploaderBytes),
		UsersAffected:       len(uploaderBytes),
		TotalCreditsRemoved: totalNuked,
	}, nil
}
