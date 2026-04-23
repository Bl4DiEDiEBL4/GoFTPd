package zipscript

import (
	"fmt"
	"path"
	"regexp"
	"strings"
	"time"
)

type MediaInfoProvider interface {
	GetDirMediaInfo(dirPath string) map[string]string
}

func Enabled(cfg Config) bool {
	return cfg.Enabled
}

func ExpectedFileLabel(cfg Config, dirPath string) string {
	if !cfg.Enabled || !cfg.RaceStats {
		return "file(s)"
	}
	section := strings.ToUpper(strings.Trim(path.Clean(dirPath), "/"))
	if idx := strings.Index(section, "/"); idx >= 0 {
		section = section[:idx]
	}
	switch section {
	case "MP3", "FLAC":
		return "track(s)"
	default:
		return "file(s)"
	}
}

func IsRacePayloadFile(cfg Config, fileName string) bool {
	if !cfg.Enabled || !cfg.RaceStats {
		return false
	}
	name := strings.ToLower(strings.TrimSpace(fileName))
	if regexp.MustCompile(`(?i)\.(rar|r\d\d)$`).MatchString(name) {
		return true
	}
	return isMediaInfoFile(name)
}

func CanTriggerRaceEnd(cfg Config, sfvEntries map[string]uint32, fileName string) bool {
	if !cfg.Enabled || !cfg.RaceStats {
		return false
	}
	name := raceEntryKey(fileName)
	if strings.HasSuffix(name, ".sfv") {
		return true
	}
	_, ok := sfvEntries[name]
	return ok
}

func MediaInfoGraceDelay(cfg Config, fileName string) time.Duration {
	if !cfg.Enabled || !cfg.RaceStats {
		return 0
	}
	if isMediaInfoFile(fileName) {
		return 2500 * time.Millisecond
	}
	return 0
}

func CompleteStatusName(cfg Config, siteName, dirPath string, totalMB float64, totalFiles int, media MediaInfoProvider) string {
	if !cfg.Enabled || !cfg.CompleteBanner {
		return ""
	}
	extra := ""
	if cfg.MusicCompleteGenre {
		extra = musicCompleteExtra(dirPath, media)
	}
	if extra != "" {
		return fmt.Sprintf("[%s] - ( %.0fM %dF - COMPLETE - %s ) - [%s]",
			siteName, totalMB, totalFiles, extra, siteName)
	}
	return fmt.Sprintf("[%s] - ( %.0fM %dF - COMPLETE ) - [%s]",
		siteName, totalMB, totalFiles, siteName)
}

func ShouldDeleteZeroByte(cfg Config) bool {
	return cfg.Enabled && cfg.IgnoreZeroSize
}

func ShouldDeleteBadCRC(cfg Config) bool {
	return cfg.Enabled && cfg.DeleteBadCRC
}

func isMediaInfoFile(fileName string) bool {
	name := strings.ToLower(strings.TrimSpace(fileName))
	for _, suffix := range []string{".mp3", ".flac", ".m4a", ".wav", ".mkv", ".mp4", ".avi", ".m2ts"} {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return false
}

func raceEntryKey(fileName string) string {
	name := strings.TrimSpace(path.Base(strings.ReplaceAll(fileName, "\\", "/")))
	return strings.ToLower(name)
}

func musicCompleteExtra(dirPath string, media MediaInfoProvider) string {
	if media == nil || !isMusicSection(dirPath) {
		return ""
	}
	info := media.GetDirMediaInfo(dirPath)
	if len(info) == 0 {
		return ""
	}
	genre := firstNonEmpty(info, "genre", "g_genre")
	year := firstNonEmpty(info, "year", "g_recordeddate", "g_recorded_date", "g_originalreleaseddate", "g_original_released_date")
	year = normalizeYearForBanner(year)
	switch {
	case genre != "" && year != "":
		return genre + " " + year
	case genre != "":
		return genre
	default:
		return year
	}
}

func isMusicSection(dirPath string) bool {
	section := strings.ToUpper(strings.Trim(path.Clean(dirPath), "/"))
	if idx := strings.Index(section, "/"); idx >= 0 {
		section = section[:idx]
	}
	return section == "MP3" || section == "FLAC"
}

func firstNonEmpty(values map[string]string, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(values[key]); value != "" {
			return value
		}
	}
	return ""
}

func normalizeYearForBanner(value string) string {
	value = strings.TrimSpace(value)
	if len(value) >= 4 {
		year := value[:4]
		allDigits := true
		for _, r := range year {
			if r < '0' || r > '9' {
				allDigits = false
				break
			}
		}
		if allDigits {
			return year
		}
	}
	return value
}
