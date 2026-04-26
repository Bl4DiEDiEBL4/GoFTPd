package zipscript

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type MediaInfoProvider interface {
	GetDirMediaInfo(dirPath string) map[string]string
}

type AudioSortLink struct {
	DirPath  string
	LinkPath string
	Target   string
}

var scenePayloadExts = map[string]bool{
	"rar":  true,
	"zip":  true,
	"sfv":  true,
	"nfo":  true,
	"diz":  true,
	"txt":  true,
	"log":  true,
	"m3u":  true,
	"cue":  true,
	"jpg":  true,
	"jpeg": true,
	"png":  true,
	"gif":  true,
}

func Enabled(cfg Config) bool {
	return cfg.Enabled
}

func UsesSFV(cfg Config, dirPath string) bool {
	if !cfg.Enabled {
		return false
	}
	if pathMatchesAny(dirPath, cfg.Sections.NoCheck) {
		return false
	}
	if len(cfg.Sections.SFV) == 0 {
		return true
	}
	return pathMatchesAny(dirPath, cfg.Sections.SFV)
}

func UsesZip(cfg Config, dirPath string) bool {
	if !cfg.Enabled {
		return false
	}
	if pathMatchesAny(dirPath, cfg.Sections.NoCheck) {
		return false
	}
	return pathMatchesAny(dirPath, cfg.Sections.Zip)
}

func UsesRace(cfg Config, dirPath string) bool {
	return UsesSFV(cfg, dirPath) || UsesZip(cfg, dirPath)
}

func UsesReleaseCheck(cfg Config, dirPath string) bool {
	if !cfg.Enabled {
		return false
	}
	if pathMatchesAny(dirPath, cfg.Sections.NoCheck) {
		return false
	}
	if len(cfg.Sections.ReleaseCheck) == 0 {
		return UsesRace(cfg, dirPath)
	}
	return pathMatchesAny(dirPath, cfg.Sections.ReleaseCheck)
}

// UsesReleaseCheckEntry reports whether dirPath matches a configured
// release_check pattern as an actual release entry, not merely as a parent or
// descendant covered by the broader pathMatchesAny semantics.
func UsesReleaseCheckEntry(cfg Config, dirPath string) bool {
	if !cfg.Enabled {
		return false
	}
	if pathMatchesAny(dirPath, cfg.Sections.NoCheck) {
		return false
	}
	if len(cfg.Sections.ReleaseCheck) == 0 {
		return UsesRaceEntry(cfg, dirPath)
	}
	return pathMatchesAnyExact(dirPath, cfg.Sections.ReleaseCheck)
}

func UsesCleanup(cfg Config, dirPath string) bool {
	return UsesReleaseCheck(cfg, dirPath)
}

func UsesRaceEntry(cfg Config, dirPath string) bool {
	return UsesSFVEntry(cfg, dirPath) || UsesZipEntry(cfg, dirPath)
}

func UsesSFVEntry(cfg Config, dirPath string) bool {
	if !cfg.Enabled {
		return false
	}
	if pathMatchesAny(dirPath, cfg.Sections.NoCheck) {
		return false
	}
	if len(cfg.Sections.SFV) == 0 {
		return true
	}
	return pathMatchesAnyExact(dirPath, cfg.Sections.SFV)
}

func UsesZipEntry(cfg Config, dirPath string) bool {
	if !cfg.Enabled {
		return false
	}
	if pathMatchesAny(dirPath, cfg.Sections.NoCheck) {
		return false
	}
	return pathMatchesAnyExact(dirPath, cfg.Sections.Zip)
}

func AnnounceReleaseSubdirs(cfg Config) bool {
	if cfg.Race.AnnounceSubdirs == nil {
		return true
	}
	return *cfg.Race.AnnounceSubdirs
}

func IsIgnoredReleaseSubdir(cfg Config, dirPath string) bool {
	name := strings.TrimSpace(path.Base(path.Clean(dirPath)))
	if name == "" || name == "." || name == "/" {
		return false
	}
	items := cfg.Sections.IgnoredReleaseSubdirs
	if len(items) == 0 {
		items = []string{"Sample", "Proof", "Subs", "Subtitles", "Covers"}
	}
	for _, item := range items {
		if strings.EqualFold(strings.TrimSpace(item), name) {
			return true
		}
	}
	return false
}

func ReleaseSubdirLabel(cfg Config, dirPath string) string {
	if !IsIgnoredReleaseSubdir(cfg, dirPath) {
		return ""
	}
	return strings.TrimSpace(path.Base(path.Clean(dirPath)))
}

func IncompleteEnabled(cfg Config) bool {
	return cfg.Enabled && cfg.Incomplete.Enabled
}

func IncompleteIndicator(cfg Config, fallback string) string {
	if !cfg.Enabled {
		return fallback
	}
	if strings.TrimSpace(cfg.Incomplete.Indicator) != "" {
		return strings.TrimSpace(cfg.Incomplete.Indicator)
	}
	return fallback
}

func NFOIndicator(cfg Config) string {
	if !IncompleteEnabled(cfg) {
		return ""
	}
	return strings.TrimSpace(cfg.Incomplete.NFOIndicator)
}

func NoSFVIndicator(cfg Config) string {
	if !IncompleteEnabled(cfg) {
		return ""
	}
	return strings.TrimSpace(cfg.Incomplete.NoSFVIndicator)
}

func CDIndicator(cfg Config) string {
	if !IncompleteEnabled(cfg) {
		return ""
	}
	return strings.TrimSpace(cfg.Incomplete.CDIndicator)
}

func MarkEmptyDirsOnRescan(cfg Config) bool {
	return cfg.Enabled && cfg.Incomplete.Enabled && cfg.Incomplete.MarkEmptyDirsOnRescan
}

func ValidateUpload(cfg Config, dirPath, fileName string, existingNames []string, sfvEntries map[string]uint32) error {
	if !UsesRace(cfg, dirPath) {
		return nil
	}

	lowerName := strings.ToLower(strings.TrimSpace(fileName))
	isSFV := strings.HasSuffix(lowerName, ".sfv")
	listedInSFV := false
	if sfvEntries != nil {
		_, listedInSFV = sfvEntries[raceEntryKey(fileName)]
	}
	isPayload := IsRacePayloadFileForDir(cfg, dirPath, fileName) || listedInSFV
	if UsesZip(cfg, dirPath) {
		if strings.EqualFold(path.Base(strings.ReplaceAll(strings.TrimSpace(fileName), "\\", "/")), "file_id.diz") {
			return errors.New("zipscript: diz-file is not allowed here")
		}
		if !IsAllowedTypeForDir(cfg, dirPath, fileName) {
			return fmt.Errorf("zipscript: file type %q is not allowed here", normalizedExt(fileName))
		}
		return nil
	}
	hasDirSFV := len(sfvEntries) > 0
	for _, name := range existingNames {
		if strings.HasSuffix(strings.ToLower(strings.TrimSpace(name)), ".sfv") {
			hasDirSFV = true
			if isSFV && cfg.SFV.DenyDoubleSFV {
				return errors.New("zipscript: .sfv already exists in this release")
			}
		}
	}

	if !hasDirSFV && cfg.SFV.ForceFirst && isPayload && !isSFV {
		return errors.New("zipscript: upload the .sfv before payload files in this release")
	}

	if hasDirSFV && !isSFV && isPayload {
		if !listedInSFV {
			return fmt.Errorf("zipscript: %q is not listed in the .sfv", fileName)
		}
	}

	if listedInSFV {
		return nil
	}

	if !IsAllowedTypeForDir(cfg, dirPath, fileName) {
		return fmt.Errorf("zipscript: file type %q is not allowed here", normalizedExt(fileName))
	}

	return nil
}

func IsIgnoredType(cfg Config, fileName string) bool {
	ext := normalizedExt(fileName)
	if ext == "" {
		return false
	}
	for _, item := range cfg.AllowedFiles.IgnoredTypes {
		if strings.EqualFold(strings.TrimSpace(item), ext) {
			return true
		}
	}
	return false
}

func IsAllowedType(cfg Config, fileName string) bool {
	return IsAllowedTypeForDir(cfg, "", fileName)
}

func IsAllowedTypeForDir(cfg Config, dirPath, fileName string) bool {
	ext := normalizedExt(fileName)
	if ext == "" {
		return true
	}
	if isPrimaryAudioPayload(dirPath, ext) {
		return true
	}
	if scenePayloadExts[ext] || isSceneMultipartExt(ext) {
		return true
	}
	if IsIgnoredType(cfg, fileName) {
		return true
	}
	if len(cfg.AllowedFiles.AllowedTypes) == 0 {
		return true
	}
	for _, item := range cfg.AllowedFiles.AllowedTypes {
		if strings.EqualFold(strings.TrimSpace(item), ext) {
			return true
		}
	}
	return false
}

func isPrimaryAudioPayload(dirPath, ext string) bool {
	section := strings.ToUpper(strings.Trim(path.Clean(dirPath), "/"))
	if idx := strings.Index(section, "/"); idx >= 0 {
		section = section[:idx]
	}
	switch section {
	case "MP3":
		return ext == "mp3"
	case "FLAC":
		return ext == "flac"
	default:
		return false
	}
}

func ExpectedFileLabel(cfg Config, dirPath string) string {
	if !RaceEnabled(cfg, dirPath) {
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
	return IsRacePayloadFileForDir(cfg, "", fileName)
}

func IsRacePayloadFileForDir(cfg Config, dirPath, fileName string) bool {
	if dirPath != "" && !RaceEnabled(cfg, dirPath) {
		return false
	}
	if dirPath == "" && (!cfg.Enabled || !cfg.Race.Enabled) {
		return false
	}
	name := strings.ToLower(strings.TrimSpace(fileName))
	if strings.HasSuffix(name, ".rar") || isSceneMultipartExt(normalizedExt(name)) {
		return true
	}
	if UsesZip(cfg, dirPath) && regexp.MustCompile(`(?i)\.(zip|z\d\d)$`).MatchString(name) {
		return true
	}
	return isMediaInfoFile(name)
}

func isSceneMultipartExt(ext string) bool {
	ext = strings.ToLower(strings.TrimSpace(ext))
	return len(ext) == 3 && ext[0] >= 'r' && ext[0] <= 'z' && ext[1] >= '0' && ext[1] <= '9' && ext[2] >= '0' && ext[2] <= '9'
}

func CanTriggerRaceEnd(cfg Config, sfvEntries map[string]uint32, fileName string) bool {
	return CanTriggerRaceEndForDir(cfg, "", sfvEntries, fileName)
}

func CanTriggerRaceEndForDir(cfg Config, dirPath string, sfvEntries map[string]uint32, fileName string) bool {
	if dirPath != "" && IsIgnoredReleaseSubdir(cfg, dirPath) && !AnnounceReleaseSubdirs(cfg) {
		return false
	}
	if dirPath != "" && !RaceEnabled(cfg, dirPath) {
		return false
	}
	if dirPath == "" && (!cfg.Enabled || !cfg.Race.Enabled) {
		return false
	}
	name := raceEntryKey(fileName)
	if UsesZip(cfg, dirPath) {
		return regexp.MustCompile(`(?i)\.(zip|z\d\d)$`).MatchString(name)
	}
	if strings.HasSuffix(name, ".sfv") {
		return true
	}
	_, ok := sfvEntries[name]
	return ok
}

func MediaInfoGraceDelay(cfg Config, fileName string) time.Duration {
	return MediaInfoGraceDelayForDir(cfg, "", fileName)
}

func MediaInfoGraceDelayForDir(cfg Config, dirPath, fileName string) time.Duration {
	if dirPath != "" && !RaceEnabled(cfg, dirPath) {
		return 0
	}
	if dirPath == "" && (!cfg.Enabled || !cfg.Race.Enabled) {
		return 0
	}
	if isMediaInfoFile(fileName) {
		return 2500 * time.Millisecond
	}
	return 0
}

func CompleteStatusName(cfg Config, siteName, dirPath string, totalMB float64, totalFiles int, media MediaInfoProvider) string {
	if !cfg.Enabled || !cfg.Race.CompleteBanner {
		return ""
	}
	extra := ""
	if cfg.Race.MusicCompleteGenre {
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
	return ShouldDeleteZeroByteForDir(cfg, "")
}

func ShouldDeleteBadCRC(cfg Config) bool {
	return ShouldDeleteBadCRCForDir(cfg, "")
}

func ShouldDeleteZeroByteForDir(cfg Config, dirPath string) bool {
	if dirPath != "" && !UsesRace(cfg, dirPath) {
		return false
	}
	return cfg.Enabled && cfg.SFV.IgnoreZeroSize
}

func ShouldDeleteBadCRCForDir(cfg Config, dirPath string) bool {
	if dirPath != "" && !UsesSFV(cfg, dirPath) {
		return false
	}
	return cfg.Enabled && cfg.SFV.DeleteBadCRC
}

func RaceEnabled(cfg Config, dirPath string) bool {
	return cfg.Enabled && cfg.Race.Enabled && UsesRace(cfg, dirPath)
}

func AudioCheckEnabled(cfg Config, dirPath, fileName string) bool {
	if !cfg.Enabled || !cfg.Audio.Enabled {
		return false
	}
	section := strings.ToUpper(strings.Trim(path.Clean(dirPath), "/"))
	if idx := strings.Index(section, "/"); idx >= 0 {
		section = section[:idx]
	}
	if section != "MP3" && section != "FLAC" {
		return false
	}
	ext := normalizedExt(fileName)
	return ext == "mp3" || ext == "flac" || ext == "m4a" || ext == "wav"
}

func ValidateAudioRelease(cfg Config, fields map[string]string) []string {
	if !cfg.Enabled || !cfg.Audio.Enabled || len(fields) == 0 {
		return nil
	}
	var reasons []string

	if cfg.Audio.CBRCheck {
		mode := strings.ToUpper(strings.TrimSpace(firstNonEmpty(fields, "bitrate_mode")))
		if mode != "CBR" {
			reasons = append(reasons, "audio is not CBR")
		}
		if len(cfg.Audio.AllowedConstantBitrates) > 0 {
			bitrate := parseBitrateKbps(firstNonEmpty(fields, "bitrate"))
			if bitrate > 0 && !intInSlice(bitrate, cfg.Audio.AllowedConstantBitrates) {
				reasons = append(reasons, fmt.Sprintf("bitrate %dkbps is not allowed", bitrate))
			}
		}
	}

	if cfg.Audio.YearCheck && len(cfg.Audio.AllowedYears) > 0 {
		year := normalizeYearForBanner(firstNonEmpty(fields, "year"))
		if year != "" {
			if n, err := strconv.Atoi(year); err == nil && !intInSlice(n, cfg.Audio.AllowedYears) {
				reasons = append(reasons, fmt.Sprintf("year %d is not allowed", n))
			}
		}
	}

	genre := strings.TrimSpace(firstNonEmpty(fields, "genre", "g_genre"))
	if cfg.Audio.BannedGenreCheck && genre != "" && stringInSliceFold(genre, cfg.Audio.BannedGenres) {
		reasons = append(reasons, fmt.Sprintf("genre %q is banned", genre))
	}
	if cfg.Audio.AllowedGenreCheck && len(cfg.Audio.AllowedGenres) > 0 && genre != "" && !stringInSliceFold(genre, cfg.Audio.AllowedGenres) {
		reasons = append(reasons, fmt.Sprintf("genre %q is not allowed", genre))
	}

	return reasons
}

func AudioSortLinks(cfg Config, releasePath string, fields map[string]string) []AudioSortLink {
	if !cfg.Enabled || !cfg.Audio.Enabled || len(fields) == 0 {
		return nil
	}
	relName := strings.TrimSpace(path.Base(path.Clean(releasePath)))
	if relName == "" || relName == "." || relName == "/" {
		return nil
	}
	var out []AudioSortLink
	add := func(enabled bool, basePath, bucket string) {
		basePath = audioSortBasePath(cfg, releasePath, basePath)
		bucket = sanitizeSortBucket(bucket)
		if !enabled || basePath == "/" || bucket == "" {
			return
		}
		dirPath := path.Join(basePath, bucket)
		out = append(out, AudioSortLink{
			DirPath:  dirPath,
			LinkPath: path.Join(dirPath, relName),
			Target:   normalizePath(releasePath),
		})
	}

	add(cfg.Audio.Sort.Genre, cfg.Audio.GenrePath, firstNonEmpty(fields, "genre", "g_genre"))
	add(cfg.Audio.Sort.Artist, cfg.Audio.ArtistPath, firstNonEmpty(fields, "artist", "g_performer", "g_album_performer", "g_track_name"))
	add(cfg.Audio.Sort.Year, cfg.Audio.YearPath, normalizeYearForBanner(firstNonEmpty(fields, "year", "g_recordeddate", "g_recorded_date")))
	add(cfg.Audio.Sort.Group, cfg.Audio.GroupPath, releaseGroupName(relName))
	return out
}

func audioSortBasePath(cfg Config, releasePath, basePath string) string {
	basePath = normalizePath(basePath)
	if basePath == "/" {
		return basePath
	}
	if cfg.Audio.Sort.SeparateBySection == nil || *cfg.Audio.Sort.SeparateBySection {
		if section, _ := SectionInfoFromPath(releasePath); strings.TrimSpace(section) != "" {
			basePath = path.Join(basePath, strings.ToUpper(strings.TrimSpace(section)))
		}
	}
	return basePath
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

func pathMatchesAny(dirPath string, patterns []string) bool {
	cleanPath := normalizePath(dirPath)
	for _, pattern := range patterns {
		pattern = normalizePattern(pattern)
		if pattern == "" {
			continue
		}
		if ok, _ := path.Match(pattern, cleanPath); ok {
			return true
		}
		if strings.HasSuffix(pattern, "/*") {
			base := strings.TrimSuffix(pattern, "/*")
			if cleanPath == base || strings.HasPrefix(cleanPath, base+"/") {
				return true
			}
		}
	}
	return false
}

func pathMatchesAnyExact(dirPath string, patterns []string) bool {
	cleanPath := normalizePath(dirPath)
	for _, pattern := range patterns {
		pattern = normalizePattern(pattern)
		if pattern == "" {
			continue
		}
		if ok, _ := path.Match(pattern, cleanPath); ok {
			return true
		}
	}
	return false
}

func normalizePath(p string) string {
	p = strings.ReplaceAll(strings.TrimSpace(p), "\\", "/")
	if p == "" {
		return "/"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return path.Clean(p)
}

func normalizePattern(p string) string {
	p = strings.ReplaceAll(strings.TrimSpace(p), "\\", "/")
	if p == "" {
		return ""
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return p
}

func sanitizeSortBucket(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	var b strings.Builder
	lastUnderscore := false
	for _, r := range name {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			lastUnderscore = false
		case r == '-' || r == '_' || r == '.' || r == ' ':
			if !lastUnderscore {
				b.WriteByte('_')
				lastUnderscore = true
			}
		}
	}
	return strings.Trim(b.String(), "_")
}

func normalizedExt(name string) string {
	base := strings.ToLower(strings.TrimSpace(path.Base(name)))
	if base == "" {
		return ""
	}
	if strings.HasPrefix(base, ".") && strings.Count(base, ".") == 1 {
		return strings.TrimPrefix(base, ".")
	}
	if idx := strings.LastIndexByte(base, '.'); idx >= 0 && idx < len(base)-1 {
		return base[idx+1:]
	}
	return ""
}

func parseBitrateKbps(value string) int {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return 0
	}
	replacer := strings.NewReplacer(" ", "", ",", "", "kbps", "", "kbit/s", "", "kb/s", "", "bps", "", "bit/s", "")
	value = replacer.Replace(value)
	if n, err := strconv.Atoi(value); err == nil {
		if n >= 1000 {
			return n / 1000
		}
		return n
	}
	return 0
}

func intInSlice(v int, values []int) bool {
	for _, item := range values {
		if item == v {
			return true
		}
	}
	return false
}

func stringInSliceFold(v string, values []string) bool {
	v = strings.TrimSpace(v)
	for _, item := range values {
		if strings.EqualFold(v, strings.TrimSpace(item)) {
			return true
		}
	}
	return false
}

func releaseGroupName(relName string) string {
	relName = strings.TrimSpace(relName)
	if idx := strings.LastIndex(relName, "-"); idx >= 0 && idx < len(relName)-1 {
		return relName[idx+1:]
	}
	return ""
}
