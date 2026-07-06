package zipscript

import (
	"path"
	"sort"
	"strings"
)

// CreateM3UEnabled reports whether weaveftpd should auto-generate an .m3u playlist
// from the SFV for a completed MP3 release in dirPath. It is on by default
// (matching glftpd/pzs-ng's create_m3u) and turns off only when
// audio.create_m3u is explicitly false or the section is not MP3.
func CreateM3UEnabled(cfg Config, dirPath string) bool {
	if !cfg.Enabled {
		return false
	}
	if cfg.Audio.CreateM3U != nil && !*cfg.Audio.CreateM3U {
		return false
	}
	section, _ := SectionInfoFromPath(dirPath)
	if len(cfg.Audio.Sections) > 0 {
		for _, pattern := range cfg.Audio.Sections {
			if strings.Contains(strings.ToUpper(strings.TrimSpace(pattern)), "MP3") &&
				matchesAnySectionName(section, []string{pattern}) {
				return true
			}
		}
		return false
	}
	switch strings.ToUpper(strings.TrimSpace(section)) {
	case "MP3":
		return true
	default:
		return false
	}
}

// BuildReleaseM3U generates the .m3u playlist for a completed MP3 release,
// mirroring pzs-ng's create_m3u-from-sfv. It lists .mp3 files that are both
// present in the directory and listed in the SFV, in case-correct, name-sorted
// order. The playlist takes the SFV's basename with a .m3u extension. ok is
// false when there is no SFV data, no MP3 entries, or the release already
// contains an .m3u (left untouched).
func BuildReleaseM3U(_ Config, sfvName string, dirFileNames []string, sfvEntries map[string]uint32) (m3uName string, body []byte, ok bool) {
	sfvName = strings.TrimSpace(path.Base(sfvName))
	if sfvName == "" || len(sfvEntries) == 0 {
		return "", nil, false
	}

	audio := make([]string, 0, len(dirFileNames))
	for _, name := range dirFileNames {
		base := strings.TrimSpace(path.Base(name))
		if base == "" {
			continue
		}
		if strings.EqualFold(normalizedExt(base), "m3u") {
			// A playlist already exists; never overwrite the release's own m3u.
			return "", nil, false
		}
		if !m3uTrackAllowed(base) {
			continue
		}
		if _, listed := sfvEntries[raceEntryKey(base)]; !listed {
			continue
		}
		audio = append(audio, base)
	}
	if len(audio) == 0 {
		return "", nil, false
	}
	sort.Slice(audio, func(i, j int) bool {
		return strings.ToLower(audio[i]) < strings.ToLower(audio[j])
	})

	base := sfvName
	if idx := strings.LastIndex(strings.ToLower(base), ".sfv"); idx > 0 {
		base = base[:idx]
	}
	if base == "" {
		return "", nil, false
	}
	// DOS line endings: scene .m3u files are CRLF-terminated.
	return base + ".m3u", []byte(strings.Join(audio, "\r\n") + "\r\n"), true
}

func m3uTrackAllowed(fileName string) bool {
	return strings.EqualFold(normalizedExt(fileName), "mp3")
}
