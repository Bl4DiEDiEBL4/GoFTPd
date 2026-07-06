package zipscript

import (
	"fmt"
	"path"
	"strings"
)

func ProgressBar(present, total, width int) string {
	if total <= 0 {
		total = 1
	}
	if width <= 0 {
		width = 20
	}
	filled := (present * width) / total
	if filled < 0 {
		filled = 0
	}
	if filled > width {
		filled = width
	}
	var b strings.Builder
	b.WriteByte('[')
	for i := 0; i < width; i++ {
		if i < filled {
			b.WriteByte('#')
		} else {
			b.WriteByte(':')
		}
	}
	b.WriteByte(']')
	return b.String()
}

func IsIncompleteMarkerName(pattern, name string) bool {
	if strings.TrimSpace(pattern) == "" {
		return strings.HasPrefix(strings.ToLower(name), "[incomplete]")
	}
	return IsStatusMarkerName(Config{
		Enabled: true,
		Incomplete: IncompleteConfig{
			Enabled:        true,
			Indicator:      pattern,
			NoSFVIndicator: pattern,
			NFOIndicator:   pattern,
			CDIndicator:    pattern,
		},
	}, name)
}

func FirstNonEmptyMap(values map[string]string, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(values[key]); value != "" {
			return value
		}
	}
	return ""
}

func AudioDisplayField(values map[string]string, keys ...string) string {
	value := strings.TrimSpace(FirstNonEmptyMap(values, keys...))
	if value == "" {
		return "unknown"
	}
	return value
}

// box-drawing characters (CP437 single bytes), the classic glftpd/pzs-ng
// announce styling that scene FTP clients render.
const (
	bxTL = "\xDA" // top-left
	bxTR = "\xBF" // top-right
	bxBL = "\xC0" // bottom-left
	bxBR = "\xD9" // bottom-right
	bxH  = "\xC4" // horizontal
	bxV  = "\xB3" // vertical
	bxLT = "\xC3" // left T
	bxRT = "\xB4" // right T
	bxTT = "\xC2" // top T
	bxBT = "\xC1" // bottom T
)

// boxText left-aligns s into a w-wide cell (rune-aware truncate/pad).
func boxText(s string, w int) string {
	r := []rune(s)
	if len(r) > w {
		return string(r[:w])
	}
	return string(r) + strings.Repeat(" ", w-len(r))
}

// boxCenter centers s in a w-wide cell.
func boxCenter(s string, w int) string {
	r := []rune(s)
	if len(r) >= w {
		return string(r[:w])
	}
	left := (w - len(r)) / 2
	return strings.Repeat(" ", left) + string(r) + strings.Repeat(" ", w-len(r)-left)
}

// boxRight right-aligns s into a w-wide cell.
func boxRight(s string, w int) string {
	r := []rune(s)
	if len(r) >= w {
		return string(r[:w])
	}
	return strings.Repeat(" ", w-len(r)) + string(r)
}

// BoxInnerWidth matches the CWD race-stats banner.
const BoxInnerWidth = 70

func BuildAudioInfoBox(dirPath string, fields map[string]string, version string) []string {
	if len(fields) == 0 {
		return nil
	}
	section := strings.ToUpper(strings.Trim(path.Clean(dirPath), "/"))
	if idx := strings.Index(section, "/"); idx >= 0 {
		section = section[:idx]
	}
	var title string
	switch section {
	case "MP3":
		title = "M P 3   I N F O"
	case "FLAC":
		title = "F L A C   I N F O"
	default:
		return nil
	}
	artist := AudioDisplayField(fields, "artist", "g_performer", "g_album_performer")
	album := AudioDisplayField(fields, "album", "g_album")
	genre := AudioDisplayField(fields, "genre", "g_genre")
	year := NormalizeAudioYearForStatus(AudioDisplayField(fields, "year", "g_recordeddate", "g_recorded_date"))

	const full = BoxInnerWidth // 70 inner columns
	const lw = 34              // left column width
	const rw = full - 1 - lw   // 35 right column width

	return []string{
		bxTL + strings.Repeat(bxH, full) + bxTR,
		bxV + boxCenter(title, full) + bxV,
		bxV + boxRight("WeaveFTPd v"+version+" ", full) + bxV,
		bxLT + strings.Repeat(bxH, lw) + bxTT + strings.Repeat(bxH, rw) + bxRT,
		bxV + boxText("  Artist : "+artist, lw) + bxV + boxText(" Genre : "+genre, rw) + bxV,
		bxV + boxText("  Album  : "+album, lw) + bxV + boxText(" Year  : "+year, rw) + bxV,
		bxBL + strings.Repeat(bxH, lw) + bxBT + strings.Repeat(bxH, rw) + bxBR,
	}
}

func BuildAudioInfoLines(dirPath string, fields map[string]string, isStor bool) []string {
	if len(fields) == 0 {
		return nil
	}
	section := strings.ToUpper(strings.Trim(path.Clean(dirPath), "/"))
	if idx := strings.Index(section, "/"); idx >= 0 {
		section = section[:idx]
	}
	switch section {
	case "MP3":
		lines := []string{
			fmt.Sprintf("MP3 INFO: Artist: %s :: Album: %s :: Genre: %s :: Year: %s",
				AudioDisplayField(fields, "artist", "g_performer", "g_album_performer"),
				AudioDisplayField(fields, "album", "g_album"),
				AudioDisplayField(fields, "genre", "g_genre"),
				AudioDisplayField(fields, "year", "g_recordeddate", "g_recorded_date")),
		}
		if isStor {
			lines = append(lines,
				fmt.Sprintf("MP3 INFO: Track: %s :: Title: %s :: Bitrate: %s :: Freq: %s :: Mode: %s :: Runtime: %s",
					AudioDisplayField(fields, "track", "g_track_name_position"),
					AudioDisplayField(fields, "title", "g_track_name"),
					AudioDisplayField(fields, "bitrate"),
					AudioDisplayField(fields, "samplerate", "sampling_rate"),
					AudioDisplayField(fields, "stereomode", "channel_s"),
					AudioDisplayField(fields, "runtime", "duration")),
			)
		}
		return trimEmptyAudioLines(lines)
	case "FLAC":
		lines := []string{
			fmt.Sprintf("FLAC INFO: Artist: %s :: Album: %s :: Genre: %s :: Year: %s",
				AudioDisplayField(fields, "artist", "g_performer", "g_album_performer"),
				AudioDisplayField(fields, "album", "g_album"),
				AudioDisplayField(fields, "genre", "g_genre"),
				AudioDisplayField(fields, "year", "g_recordeddate", "g_recorded_date")),
		}
		if isStor {
			lines = append(lines,
				fmt.Sprintf("FLAC INFO: Track: %s :: Title: %s :: Freq: %s :: Channels: %s :: Runtime: %s",
					AudioDisplayField(fields, "track", "g_track_name_position"),
					AudioDisplayField(fields, "title", "g_track_name"),
					AudioDisplayField(fields, "samplerate", "sampling_rate"),
					AudioDisplayField(fields, "channels", "channel_s"),
					AudioDisplayField(fields, "runtime", "duration")),
			)
		}
		return trimEmptyAudioLines(lines)
	default:
		return nil
	}
}

func NormalizeAudioYearForStatus(value string) string {
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

func RaceStatusEligibleDir(dirPath string) bool {
	cleaned := path.Clean("/" + strings.TrimSpace(dirPath))
	if cleaned == "/" || cleaned == "." {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(cleaned, "/"), "/")
	if len(parts) < 2 {
		return false
	}
	switch strings.ToUpper(strings.TrimSpace(parts[0])) {
	case "FOREIGN", "PRE", "ARCHIVE":
		return len(parts) >= 3
	}
	return true
}

func trimEmptyAudioLines(lines []string) []string {
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		normalized := strings.ReplaceAll(line, " ::  :: ", " :: ")
		normalized = strings.TrimSpace(normalized)
		if normalized != "" && !strings.HasSuffix(normalized, "INFO:") {
			out = append(out, normalized)
		}
	}
	return out
}
