package core

import (
	"encoding/json"
	"fmt"
	"math"
	"path"
	"sort"
	"strings"
	"time"

	"weaveftpd/internal/user"
)

// NukeeRecord is what was actually taken from one user by a nuke, so an unnuke
// can restore exactly that, even when the penalty exceeded the user's balance.
type NukeeRecord struct {
	Bytes   int64 `json:"b"`
	Credits int64 `json:"c"`
}

func EncodeNukeeRecords(m map[string]NukeeRecord) string {
	if len(m) == 0 {
		return ""
	}
	data, err := json.Marshal(m)
	if err != nil {
		return ""
	}
	return string(data)
}

func DecodeNukeeRecords(s string) map[string]NukeeRecord {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	var m map[string]NukeeRecord
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return nil
	}
	return m
}

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
	NukeeRecords        map[string]NukeeRecord
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

func vfsUploaderBytesRecursive(bridge MasterBridge, dirPath string, maxDepth int) map[string]int64 {
	uploaderBytes := make(map[string]int64)
	if bridge == nil || maxDepth < 0 {
		return uploaderBytes
	}
	for _, entry := range bridge.ListDir(dirPath) {
		if strings.HasPrefix(entry.Name, ".") {
			continue
		}
		if entry.IsDir {
			if maxDepth == 0 {
				continue
			}
			childPath := path.Join(dirPath, entry.Name)
			for user, bytes := range vfsUploaderBytesRecursive(bridge, childPath, maxDepth-1) {
				uploaderBytes[user] += bytes
			}
			continue
		}
		owner := strings.TrimSpace(entry.Owner)
		if owner == "" {
			owner = "unknown"
		}
		if entry.Size > 0 {
			uploaderBytes[owner] += entry.Size
		}
	}
	return uploaderBytes
}

func DirUploaderBytes(bridge MasterBridge, dirPath string) map[string]int64 {
	if bridge != nil {
		if users, _, _, _, _ := bridge.GetVFSRaceStats(dirPath); len(users) > 0 {
			uploaderBytes := make(map[string]int64, len(users))
			for _, u := range users {
				if strings.TrimSpace(u.Name) == "" || u.Bytes <= 0 {
					continue
				}
				uploaderBytes[u.Name] += u.Bytes
			}
			if len(uploaderBytes) > 0 {
				return uploaderBytes
			}
		}
		if uploaderBytes := VFSUploaderBytes(bridge.ListDir(dirPath)); len(uploaderBytes) > 0 {
			return uploaderBytes
		}
		return vfsUploaderBytesRecursive(bridge, dirPath, 8)
	}
	return nil
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

func nukeCreditPenalty(bytes int64, ratio int, multiplier int) int64 {
	if bytes <= 0 || ratio <= 0 || multiplier <= 0 {
		return 0
	}
	r := int64(ratio)
	m := int64(multiplier)
	if bytes > math.MaxInt64/r {
		return math.MaxInt64
	}
	base := bytes * r
	if base > math.MaxInt64/m {
		return math.MaxInt64
	}
	return base * m
}

func saturatingCreditAdd(current, delta int64) int64 {
	if delta <= 0 {
		return current
	}
	if current > math.MaxInt64-delta {
		return math.MaxInt64
	}
	return current + delta
}

func clampCreditRemoval(balance, penalty int64) int64 {
	if balance <= 0 || penalty <= 0 {
		return 0
	}
	if penalty > balance {
		return balance
	}
	return penalty
}

func ApplyNukeCredits(groupMap map[string]int, uploaderBytes map[string]int64, multiplier int) (int64, map[string]NukeeRecord) {
	now := time.Now().Unix()
	totalNuked := int64(0)
	records := make(map[string]NukeeRecord, len(uploaderBytes))
	for username, bytes := range uploaderBytes {
		var nukedCredits int64
		if _, err := user.MutateAndSave(username, groupMap, func(u *user.User) error {
			penalty := nukeCreditPenalty(bytes, u.Ratio, multiplier)
			// Record what was actually removed (penalty clamped to the balance) so
			// the unnuke restores exactly this much, not a recomputed amount.
			nukedCredits = clampCreditRemoval(u.Credits, penalty)
			u.Credits -= nukedCredits
			u.NukeStat.Meta = now
			u.NukeStat.Files++
			u.NukeStat.Bytes += bytes
			return nil
		}); err != nil {
			continue
		}
		totalNuked = saturatingCreditAdd(totalNuked, nukedCredits)
		records[username] = NukeeRecord{Bytes: bytes, Credits: nukedCredits}
	}
	return totalNuked, records
}

// ApplyUnnukeCredits restores exactly what was removed, using the per-user
// records captured at nuke time. Lifetime nuke stats have only THIS nuke's
// contribution subtracted (the old code wiped the whole struct).
func ApplyUnnukeCredits(groupMap map[string]int, records map[string]NukeeRecord) int64 {
	totalRestored := int64(0)
	for username, rec := range records {
		if _, err := user.MutateAndSave(username, groupMap, func(u *user.User) error {
			u.Credits = saturatingCreditAdd(u.Credits, rec.Credits)
			if u.NukeStat.Files > 0 {
				u.NukeStat.Files--
			}
			u.NukeStat.Bytes -= rec.Bytes
			if u.NukeStat.Bytes < 0 {
				u.NukeStat.Bytes = 0
			}
			return nil
		}); err != nil {
			continue
		}
		totalRestored = saturatingCreditAdd(totalRestored, rec.Credits)
	}
	return totalRestored
}

// ApplyUnnukeCreditsRecompute is the fallback for nukes recorded before per-user
// records existed: it recomputes the restore from current ratios using the
// actual nuke multiplier (never the max). Not perfectly exact if a user's ratio
// or balance changed since the nuke, but no longer over-restores at max x.
func ApplyUnnukeCreditsRecompute(groupMap map[string]int, uploaderBytes map[string]int64, multiplier int) int64 {
	if multiplier <= 0 {
		multiplier = 1
	}
	totalRestored := int64(0)
	for username, bytes := range uploaderBytes {
		var restored int64
		if _, err := user.MutateAndSave(username, groupMap, func(u *user.User) error {
			restored = nukeCreditPenalty(bytes, u.Ratio, multiplier)
			u.Credits = saturatingCreditAdd(u.Credits, restored)
			if u.NukeStat.Files > 0 {
				u.NukeStat.Files--
			}
			u.NukeStat.Bytes -= bytes
			if u.NukeStat.Bytes < 0 {
				u.NukeStat.Bytes = 0
			}
			return nil
		}); err != nil {
			continue
		}
		totalRestored = saturatingCreditAdd(totalRestored, restored)
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
	uploaderBytes := DirUploaderBytes(bridge, dirPath)
	totalNuked, records := ApplyNukeCredits(groupMap, uploaderBytes, multiplier)
	newName := nukedPrefix + releaseName
	if err := bridge.RenameFile(dirPath, path.Dir(dirPath), newName); err != nil {
		// Rename failed: undo the credit penalty so we don't leave users docked
		// for a release that was never marked nuked (which would also let a retry
		// double-penalize).
		ApplyUnnukeCredits(groupMap, records)
		return nil, fmt.Errorf("failed to rename %s for nuke: %w", dirPath, err)
	}
	return &NukeResult{
		DirPath:             dirPath,
		NewPath:             path.Join(path.Dir(dirPath), newName),
		ReleaseName:         releaseName,
		Multiplier:          multiplier,
		Reason:              reason,
		UserStats:           BuildNukeUserStats(uploaderBytes),
		UsersAffected:       len(uploaderBytes),
		TotalCreditsRemoved: totalNuked,
		NukeeRecords:        records,
	}, nil
}
