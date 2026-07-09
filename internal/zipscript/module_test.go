package zipscript

import (
	"testing"

	"weaveftpd/internal/user"
)

func boolPtr(v bool) *bool { return &v }

type testMediaInfoProvider struct {
	data map[string]map[string]string
}

func (p testMediaInfoProvider) GetDirMediaInfo(dirPath string) map[string]string {
	return p.data[dirPath]
}

func TestUsesReleaseCheckEntryRequiresExactReleaseDepth(t *testing.T) {
	cfg := Config{
		Enabled: true,
		Sections: SectionsConfig{
			ReleaseCheck: []string{"/APPS/*", "/MP3/*/*"},
		},
	}

	if UsesReleaseCheckEntry(cfg, "/APPS") {
		t.Fatalf("section root should not count as a release entry")
	}
	if !UsesReleaseCheckEntry(cfg, "/APPS/Some.Release-GRP") {
		t.Fatalf("direct release path should match")
	}
	if UsesReleaseCheckEntry(cfg, "/APPS/Some.Release-GRP/CD1") {
		t.Fatalf("descendant path should not count as the release entry itself")
	}
	if UsesReleaseCheckEntry(cfg, "/MP3/0424") {
		t.Fatalf("dated container should not count as a release entry")
	}
	if !UsesReleaseCheckEntry(cfg, "/MP3/0424/Artist-Album-2026-GRP") {
		t.Fatalf("dated release path should match")
	}
}

func TestRequestContainerMatchesConfiguredRequestChildPattern(t *testing.T) {
	cfg := Config{
		Enabled: true,
		Sections: SectionsConfig{
			SFV:          []string{"/REQUESTS/*/*"},
			Zip:          []string{"/REQUESTS/*/*"},
			ReleaseCheck: []string{"/REQUESTS/*/*"},
		},
	}
	dirPath := "/REQUESTS/REQ-Space.Haven.Linux-rG"

	if !UsesSFV(cfg, dirPath) || !UsesSFVEntry(cfg, dirPath) {
		t.Fatalf("expected request container to inherit SFV mode from /REQUESTS/*/*")
	}
	if !UsesZip(cfg, dirPath) || !UsesZipEntry(cfg, dirPath) {
		t.Fatalf("expected request container to inherit ZIP mode from /REQUESTS/*/*")
	}
	if !UsesReleaseCheck(cfg, dirPath) || !UsesReleaseCheckEntry(cfg, dirPath) {
		t.Fatalf("expected request container to inherit release-check mode from /REQUESTS/*/*")
	}
	if !UsesReleaseCheck(cfg, "/REQUESTS/FILLED-Space.Haven.Linux-rG") {
		t.Fatalf("expected filled request container to keep release-check mode")
	}
}

func TestStatusMarkersRequireRealContentForNoSFVAndNoNFO(t *testing.T) {
	cfg := Config{
		Enabled: true,
		Sections: SectionsConfig{
			ReleaseCheck: []string{"/REQUESTS/*/*"},
		},
		Incomplete: IncompleteConfig{
			Enabled:        true,
			NoSFVIndicator: "[no-sfv]-%0",
			NFOIndicator:   "[no-nfo]-%0",
		},
	}

	empty := BuildStatusMarkerEntries(cfg, "/REQUESTS", []StatusMarkerRelease{{
		Name: "REQ-Space.Haven.Linux-rG",
		Path: "/REQUESTS/REQ-Space.Haven.Linux-rG",
	}})
	if len(empty) != 0 {
		t.Fatalf("did not expect empty request placeholder markers, got %#v", empty)
	}

	withFile := BuildStatusMarkerEntries(cfg, "/REQUESTS", []StatusMarkerRelease{{
		Name:      "REQ-Space.Haven.Linux-rG",
		Path:      "/REQUESTS/REQ-Space.Haven.Linux-rG",
		FileCount: 1,
	}})
	if len(withFile) != 2 {
		t.Fatalf("expected no-sfv and no-nfo markers once files exist, got %#v", withFile)
	}
}

func TestStatusMarkersDoNotCreateNoSFVForZipSections(t *testing.T) {
	cfg := Config{
		Enabled: true,
		Sections: SectionsConfig{
			SFV:          []string{"/0DAY/*/*"},
			Zip:          []string{"/0DAY/*/*"},
			ReleaseCheck: []string{"/0DAY/*/*"},
		},
		Incomplete: IncompleteConfig{
			Enabled:        true,
			NoSFVIndicator: "[no-sfv]-%0",
			NFOIndicator:   "[no-nfo]-%0",
		},
	}

	got := BuildStatusMarkerEntries(cfg, "/0DAY/0516", []StatusMarkerRelease{{
		Name:      "Tool.v1.0-WOW",
		Path:      "/0DAY/0516/Tool.v1.0-WOW",
		FileCount: 1,
	}})

	if len(got) != 1 || got[0].Name != "[no-nfo]-Tool.v1.0-WOW" {
		t.Fatalf("expected only no-nfo marker for zip section, got %#v", got)
	}
}

func TestStatusMarkersIgnoreProgressWithoutCurrentContent(t *testing.T) {
	cfg := Config{
		Enabled: true,
		Sections: SectionsConfig{
			SFV: []string{"/X265/*"},
		},
		Incomplete: IncompleteConfig{
			Enabled:        true,
			Indicator:      "[incomplete]-%0",
			NoSFVIndicator: "[no-sfv]-%0",
			NFOIndicator:   "[no-nfo]-%0",
		},
	}

	stale := BuildStatusMarkerEntries(cfg, "/X265", []StatusMarkerRelease{{
		Name:  "Old.Release-GRP",
		Path:  "/X265/Old.Release-GRP",
		Total: 10,
	}})
	if len(stale) != 0 {
		t.Fatalf("did not expect stale total-only progress markers, got %#v", stale)
	}

	current := BuildStatusMarkerEntries(cfg, "/X265", []StatusMarkerRelease{{
		Name:    "Current.Release-GRP",
		Path:    "/X265/Current.Release-GRP",
		HasSFV:  true,
		Present: 1,
		Total:   10,
	}})
	if len(current) != 2 {
		t.Fatalf("expected current incomplete/no-nfo markers, got %#v", current)
	}
}

func TestAudioSortLinksSeparateBySectionByDefault(t *testing.T) {
	cfg := Config{
		Enabled: true,
		Audio: AudioConfig{
			Enabled:    true,
			GenrePath:  "/music.by.genre",
			ArtistPath: "/music.by.artist",
			YearPath:   "/music.by.year",
			GroupPath:  "/music.by.group",
			Sort: AudioSortConfig{
				Genre:  true,
				Artist: true,
				Year:   true,
				Group:  true,
			},
		},
	}
	cfg.ApplyDefaults()
	fields := map[string]string{
		"genre":  "House",
		"artist": "Some Artist",
		"year":   "2026",
	}

	links := AudioSortLinks(cfg, "/MP3/0426/Artist-Album-2026-GRP", fields)
	want := map[string]bool{
		"/music.by.genre/MP3/House/Artist-Album-2026-GRP":        true,
		"/music.by.artist/MP3/Some_Artist/Artist-Album-2026-GRP": true,
		"/music.by.year/MP3/2026/Artist-Album-2026-GRP":          true,
		"/music.by.group/MP3/GRP/Artist-Album-2026-GRP":          true,
	}

	if len(links) != len(want) {
		t.Fatalf("len(links) = %d, want %d", len(links), len(want))
	}
	for _, link := range links {
		if !want[link.LinkPath] {
			t.Fatalf("unexpected link path %q", link.LinkPath)
		}
	}
}

func TestAudioSortLinksCanDisableSectionBuckets(t *testing.T) {
	separate := false
	cfg := Config{
		Enabled: true,
		Audio: AudioConfig{
			Enabled:   true,
			GenrePath: "/music.by.genre",
			Sort: AudioSortConfig{
				Genre:             true,
				SeparateBySection: &separate,
			},
		},
	}
	fields := map[string]string{"genre": "House"}

	links := AudioSortLinks(cfg, "/FLAC/0426/Artist-Album-2026-GRP", fields)
	if len(links) != 1 {
		t.Fatalf("len(links) = %d, want 1", len(links))
	}
	if got, want := links[0].LinkPath, "/music.by.genre/House/Artist-Album-2026-GRP"; got != want {
		t.Fatalf("LinkPath = %q, want %q", got, want)
	}
}

func TestCompleteStatusNameUsesMusicMetadataForAffilPredir(t *testing.T) {
	cfg := Config{
		Enabled: true,
		Race: RaceConfig{
			CompleteBanner:     true,
			MusicCompleteGenre: true,
		},
	}
	media := testMediaInfoProvider{
		data: map[string]map[string]string{
			"/groups/GRP1/Artist-Album-2026-GRP": {
				"filename": "01-track.mp3",
				"genre":    "DANCE",
				"year":     "2026-04-28",
			},
		},
	}

	got := CompleteStatusName(cfg, "WeaveFTPd", "/groups/GRP1/Artist-Album-2026-GRP", 123, 12, media)
	want := "[WeaveFTPd] - ( 123M 12F - COMPLETE - DANCE 2026 ) - [WeaveFTPd]"
	if got != want {
		t.Fatalf("CompleteStatusName = %q, want %q", got, want)
	}
}

func TestCompleteStatusNameIgnoresUnusableMusicMetadata(t *testing.T) {
	cfg := Config{
		Enabled: true,
		Race: RaceConfig{
			CompleteBanner:     true,
			MusicCompleteGenre: true,
		},
	}
	media := testMediaInfoProvider{
		data: map[string]map[string]string{
			"/MP3/0503/Artist-Album-2026-GRP": {
				"genre": "DANCE",
			},
		},
	}

	got := CompleteStatusName(cfg, "WeaveFTPd", "/MP3/0503/Artist-Album-2026-GRP", 123, 12, media)
	want := "[WeaveFTPd] - ( 123M 12F - COMPLETE ) - [WeaveFTPd]"
	if got != want {
		t.Fatalf("CompleteStatusName = %q, want %q", got, want)
	}
}

func TestIgnoredReleaseSubdirsIncludeSpamByDefault(t *testing.T) {
	cfg := Config{Enabled: true}

	if !IsIgnoredReleaseSubdir(cfg, "/MP3/0503/Artist-Album-2026-GRP/Spam") {
		t.Fatalf("expected Spam to be ignored by default")
	}
}

func TestIgnoredReleaseSubdirNeverTriggersRaceEnd(t *testing.T) {
	announce := true
	cfg := Config{
		Enabled: true,
		Race: RaceConfig{
			Enabled:         true,
			AnnounceSubdirs: &announce,
		},
		Sections: SectionsConfig{
			SFV: []string{"/MP3/*/*", "/MP3/*/*/*"},
		},
	}

	sfvEntries := map[string]uint32{
		"01-track.mp3": 1234,
	}

	if CanTriggerRaceEndForDir(cfg, "/MP3/0503/Artist-Album-2026-GRP/Spam", sfvEntries, "01-track.mp3") {
		t.Fatalf("expected ignored subdir to never trigger race end")
	}
	if RaceEndDirEligible(cfg, "/MP3/0503/Artist-Album-2026-GRP/Spam") {
		t.Fatalf("expected ignored subdir to never be race-end eligible")
	}
}

// TestRaceEndDirEligibleAndCanTriggerRaceEndForDirShareGuards locks in that
// RaceEndDirEligible and CanTriggerRaceEndForDir agree on every dir-level
// guard (ignored subdir, race-disabled dir, race-disabled globally), since
// both now delegate to the same raceEndDirGuardsPass helper. A future change
// to one guard set without the other would show up here as a mismatch.
func TestRaceEndDirEligibleAndCanTriggerRaceEndForDirShareGuards(t *testing.T) {
	announce := true
	baseCfg := Config{
		Enabled: true,
		Race: RaceConfig{
			Enabled:         true,
			AnnounceSubdirs: &announce,
		},
		Sections: SectionsConfig{
			SFV: []string{"/MP3/*"},
		},
	}
	sfvEntries := map[string]uint32{"01-track.mp3": 1234}

	cases := []struct {
		name    string
		cfg     Config
		dirPath string
		want    bool
	}{
		{"eligible race dir", baseCfg, "/MP3/Artist-Album-2026-GRP", true},
		{"race disabled on dir", func() Config { c := baseCfg; c.Sections.NoCheck = []string{"/MP3/*"}; return c }(), "/MP3/Artist-Album-2026-GRP", false},
		{"race disabled globally", func() Config { c := baseCfg; c.Race.Enabled = false; return c }(), "/MP3/Artist-Album-2026-GRP", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := RaceEndDirEligible(c.cfg, c.dirPath); got != c.want {
				t.Fatalf("RaceEndDirEligible() = %v, want %v", got, c.want)
			}
			gotTrigger := CanTriggerRaceEndForDir(c.cfg, c.dirPath, sfvEntries, "01-track.mp3")
			if c.want && !gotTrigger {
				t.Fatalf("CanTriggerRaceEndForDir() = false, want dir-level guards to pass (SFV-listed file)")
			}
			if !c.want && gotTrigger {
				t.Fatalf("CanTriggerRaceEndForDir() = true, want dir-level guards to reject like RaceEndDirEligible")
			}
		})
	}
}

func TestValidateUploadDeniesSFVWhenMatchingSubdirExists(t *testing.T) {
	cfg := Config{
		Enabled: true,
		Sections: SectionsConfig{
			SFV: []string{"/X264-HD-1080P/*"},
		},
		SFV: SFVConfig{
			DenySubdir:        true,
			DenySubdirInclude: ".*",
			DenySubdirExclude: "(?i)^sample$",
		},
	}

	err := ValidateUpload(cfg, nil, "/X264-HD-1080P/Some.Release-GRP", "release.sfv", nil, []string{"Proof"}, nil)
	if err == nil {
		t.Fatalf("expected sfv upload to be denied when matching subdir exists")
	}

	err = ValidateUpload(cfg, nil, "/X264-HD-1080P/Some.Release-GRP", "release.sfv", nil, []string{"Sample"}, nil)
	if err != nil {
		t.Fatalf("expected excluded subdir not to block sfv upload, got %v", err)
	}
}

func TestValidateUploadNoExtHonorsAllowNoExt(t *testing.T) {
	cfg := Config{
		Enabled: true,
		Sections: SectionsConfig{
			SFV: []string{"/0DAY/*/*"},
		},
		SFV: SFVConfig{
			AllowNoExt: boolPtr(false),
		},
	}

	if err := ValidateUpload(cfg, nil, "/0DAY/0505/Some.Release-GRP", "README", nil, nil, nil); err == nil {
		t.Fatalf("expected extensionless upload to be denied by default")
	}

	cfg.SFV.AllowNoExt = boolPtr(true)
	if err := ValidateUpload(cfg, nil, "/0DAY/0505/Some.Release-GRP", "README", nil, nil, nil); err != nil {
		t.Fatalf("expected extensionless upload to be allowed when configured, got %v", err)
	}
}

func TestValidateUploadRestrictFilesHonorsSetting(t *testing.T) {
	cfg := Config{
		Enabled: true,
		SFV: SFVConfig{
			ForceFirst:    true,
			RestrictFiles: boolPtr(false),
			PathCheck:     []string{"*"},
			Users:         []string{"*"},
		},
	}
	sfvEntries := map[string]uint32{
		"release.sfv": 1,
		"good.rar":    2,
	}
	if err := ValidateUpload(cfg, nil, "/X265/Some.Release-GRP", "bad.rar", []string{"release.sfv"}, nil, sfvEntries); err != nil {
		t.Fatalf("expected unlisted payload to be allowed when restrict_files is disabled, got %v", err)
	}
	cfg.SFV.RestrictFiles = boolPtr(true)
	if err := ValidateUpload(cfg, nil, "/X265/Some.Release-GRP", "bad.rar", []string{"release.sfv"}, nil, sfvEntries); err == nil {
		t.Fatalf("expected unlisted payload to be denied when restrict_files is enabled")
	}
}

func TestValidateUploadBlocksConfiguredRaceContainers(t *testing.T) {
	cfg := Config{
		Enabled: true,
		Sections: SectionsConfig{
			SFV: []string{"/TV-1080P/*", "/0DAY/*/*"},
			Zip: []string{"/EBOOKS/*/*"},
		},
		AllowedFiles: AllowedFilesConfig{
			AllowedTypes: []string{"sfv", "nfo", "rar", "zip"},
		},
	}

	for _, dirPath := range []string{"/TV-1080P", "/0DAY", "/0DAY/0516", "/EBOOKS", "/EBOOKS/0516"} {
		if err := ValidateUpload(cfg, nil, dirPath, "release.r00", nil, nil, nil); err == nil {
			t.Fatalf("expected upload into configured container %s to be blocked", dirPath)
		}
	}
	if err := ValidateUpload(cfg, nil, "/TV-1080P/Real.Release-GRP", "release.r00", nil, nil, nil); err != nil {
		t.Fatalf("expected upload into release dir to be allowed, got %v", err)
	}
	if err := ValidateUpload(cfg, nil, "/TV-1080P/Real.Release-GRP/Sample", "sample.r00", nil, nil, nil); err != nil {
		t.Fatalf("expected upload into release subdir to be allowed, got %v", err)
	}
}

func TestValidateUploadBlocksExactSectionRootRaceContainer(t *testing.T) {
	cfg := Config{
		Enabled: true,
		Sections: SectionsConfig{
			SFV: []string{"/TV-1080P"},
		},
		AllowedFiles: AllowedFilesConfig{
			AllowedTypes: []string{"sfv", "nfo", "rar"},
		},
	}

	if err := ValidateUpload(cfg, nil, "/TV-1080P", "release.r00", nil, nil, nil); err == nil {
		t.Fatalf("expected upload into exact section root to be blocked")
	}
	if err := ValidateUpload(cfg, nil, "/TV-1080P/Real.Release-GRP", "release.r00", nil, nil, nil); err != nil {
		t.Fatalf("expected upload into release dir to be allowed, got %v", err)
	}
}

func TestValidateUploadAllowsRequestWrapperContainer(t *testing.T) {
	cfg := Config{
		Enabled: true,
		Sections: SectionsConfig{
			SFV: []string{"/REQUESTS/*/*"},
		},
		AllowedFiles: AllowedFilesConfig{
			AllowedTypes: []string{"sfv", "nfo", "rar"},
		},
	}

	if err := ValidateUpload(cfg, nil, "/REQUESTS/REQ-Direct.Release-GRP", "release.r00", nil, nil, nil); err != nil {
		t.Fatalf("expected direct request wrapper upload to remain allowed, got %v", err)
	}
}

func TestRequestMixedZipAndSFVSectionsAllowAudioAndZip(t *testing.T) {
	cfg := Config{
		Enabled: true,
		Sections: SectionsConfig{
			SFV: []string{"/REQUESTS/*/*"},
			Zip: []string{"/REQUESTS/*/*"},
		},
		Race: RaceConfig{
			Enabled: true,
		},
		AllowedFiles: AllowedFilesConfig{
			AllowedTypes: []string{"sfv", "nfo", "rar", "zip"},
		},
	}
	dirPath := "/REQUESTS/REQ-music_release-WOW/Cruel_Division-Carcosa-WEB-2026-SDR"

	if err := ValidateUpload(cfg, nil, dirPath, "04-cruel_division-calcification.mp3", nil, nil, nil); err != nil {
		t.Fatalf("expected request audio upload to be accepted by SFV mode, got %v", err)
	}
	if err := ValidateUpload(cfg, nil, dirPath, "release.zip", nil, nil, nil); err != nil {
		t.Fatalf("expected request zip upload to still use zip mode, got %v", err)
	}
	if !CanTriggerRaceEndForDir(cfg, dirPath, map[string]uint32{"04-cruel_division-calcification.mp3": 1}, "04-cruel_division-calcification.mp3") {
		t.Fatalf("expected listed request audio payload to trigger SFV race end checks")
	}
	if !CanTriggerRaceEndForDir(cfg, dirPath, nil, "release.zip") {
		t.Fatalf("expected request zip payload to trigger zip race end checks")
	}
}

func TestRequestAudioCheckIgnoresSectionNameForRequestedAudio(t *testing.T) {
	cfg := Config{
		Enabled: true,
		Audio: AudioConfig{
			Enabled:    true,
			Sections:   []string{"MP3", "FLAC"},
			Extensions: []string{"mp3", "flac"},
		},
	}
	dirPath := "/REQUESTS/REQ-music_release-WOW/Cruel_Division-Carcosa-WEB-2026-SDR"

	if !AudioCheckEnabled(cfg, dirPath, "04-cruel_division-calcification.mp3") {
		t.Fatalf("expected request mp3 upload to run audio checks")
	}
	if AudioCheckEnabled(cfg, dirPath, "sample.mkv") {
		t.Fatalf("did not expect non-audio request payload to run audio checks")
	}
}

func TestValidateUploadSfvFirstPathIgnoreSkipsEnforcement(t *testing.T) {
	cfg := Config{
		Enabled: true,
		Sections: SectionsConfig{
			SFV: []string{"/X264-HD-1080P/*", "/X264-HD-1080P/*/*"},
		},
		SFV: SFVConfig{
			ForceFirst: true,
			PathCheck:  []string{"*"},
			PathIgnore: []string{"*/Subs", "*/Cover", "*/Covers"},
			Users:      []string{"*"},
		},
	}

	if err := ValidateUpload(cfg, &user.User{Name: "u"}, "/X264-HD-1080P/Some.Release-GRP/Subs", "subs.rar", nil, nil, nil); err != nil {
		t.Fatalf("expected sfv-first ignore path to skip enforcement, got %v", err)
	}
}

func TestValidateUploadSfvFirstUsersCanExcludeGroup(t *testing.T) {
	cfg := Config{
		Enabled: true,
		Sections: SectionsConfig{
			SFV: []string{"/0DAY/*/*"},
		},
		SFV: SFVConfig{
			ForceFirst: true,
			PathCheck:  []string{"*"},
			Users:      []string{"!=SiteOP", "*"},
		},
	}

	siteop := &user.User{Name: "admin", PrimaryGroup: "SiteOP", Groups: map[string]int{"SiteOP": 0}}
	if err := ValidateUpload(cfg, siteop, "/0DAY/0506/Some.Release-GRP", "payload.rar", nil, nil, nil); err != nil {
		t.Fatalf("expected excluded group to bypass sfv-first, got %v", err)
	}

	normal := &user.User{Name: "normal", PrimaryGroup: "Users", Groups: map[string]int{"Users": 0}}
	if err := ValidateUpload(cfg, normal, "/0DAY/0506/Some.Release-GRP", "payload.rar", nil, nil, nil); err == nil {
		t.Fatalf("expected normal user to still be blocked by sfv-first")
	}
}

func TestValidateUploadAllowsConfiguredExtraFileOutsideSFV(t *testing.T) {
	cfg := Config{
		Enabled: true,
		Sections: SectionsConfig{
			SFV: []string{"/X264-HD-1080P/*"},
		},
		SFV: SFVConfig{
			ForceFirst:    true,
			RestrictFiles: boolPtr(true),
			PathCheck:     []string{"*"},
			Users:         []string{"*"},
		},
		AllowedFiles: AllowedFilesConfig{
			AllowedTypes: []string{"mp4"},
		},
	}

	if err := ValidateUpload(cfg, nil, "/X264-HD-1080P/Some.Release-GRP", "sample.mp4", nil, nil, nil); err != nil {
		t.Fatalf("expected configured extra file to bypass sfv-first before sfv, got %v", err)
	}
	if err := ValidateUpload(cfg, nil, "/X264-HD-1080P/Some.Release-GRP", "sample.mp4", []string{"release.sfv"}, nil, map[string]uint32{"movie.r00": 1}); err != nil {
		t.Fatalf("expected configured extra file to bypass sfv restrict-files after sfv, got %v", err)
	}
}

func TestAllowedOutsideSFVDefaultsMatchDrFTPDStyleExtras(t *testing.T) {
	cfg := Config{}
	cfg.ApplyDefaults()

	for _, name := range []string{"info.nfo", "sample.mp4", "preview.avi", "proof.jpg", "notes.txt", "readme"} {
		got := AllowedOutsideSFVForDir(cfg, "/X264-HD-1080P/Release-GRP", name)
		if name == "readme" {
			if !got {
				t.Fatalf("expected extensionless file to be allowed by default")
			}
			continue
		}
		if !got {
			t.Fatalf("expected %q to be allowed outside sfv by default", name)
		}
	}

	if AllowedOutsideSFVForDir(cfg, "/X264-HD-1080P/Release-GRP", "payload.r00") {
		t.Fatalf("did not expect multipart payload to be allowed outside sfv")
	}
}

func TestValidateUploadBacksOffWhenSfvFileExistsButIsUnreadable(t *testing.T) {
	cfg := Config{
		Enabled: true,
		Sections: SectionsConfig{
			SFV: []string{"/X264-HD-1080P/*"},
		},
		SFV: SFVConfig{
			ForceFirst:    true,
			DenyDoubleSFV: true,
			RestrictFiles: boolPtr(true),
			PathCheck:     []string{"*"},
			Users:         []string{"*"},
		},
	}

	if err := ValidateUpload(cfg, nil, "/X264-HD-1080P/Some.Release-GRP", "payload.r00", []string{"release.sfv"}, nil, nil); err != nil {
		t.Fatalf("expected payload to be allowed when sfv file exists but is not readable, got %v", err)
	}
	if err := ValidateUpload(cfg, nil, "/X264-HD-1080P/Some.Release-GRP", "other.sfv", []string{"release.sfv"}, nil, nil); err != nil {
		t.Fatalf("expected second sfv not to be blocked when existing sfv is unreadable, got %v", err)
	}
}

func TestValidateUploadReadableSfvStillEnforcesRestrictAndDoubleSfv(t *testing.T) {
	cfg := Config{
		Enabled: true,
		Sections: SectionsConfig{
			SFV: []string{"/X264-HD-1080P/*"},
		},
		SFV: SFVConfig{
			ForceFirst:    true,
			DenyDoubleSFV: true,
			RestrictFiles: boolPtr(true),
			PathCheck:     []string{"*"},
			Users:         []string{"*"},
		},
	}

	sfvEntries := map[string]uint32{"good.r00": 1}
	if err := ValidateUpload(cfg, nil, "/X264-HD-1080P/Some.Release-GRP", "bad.r00", []string{"release.sfv"}, nil, sfvEntries); err == nil {
		t.Fatalf("expected readable sfv to enforce restrict_files")
	}
	if err := ValidateUpload(cfg, nil, "/X264-HD-1080P/Some.Release-GRP", "other.sfv", []string{"release.sfv"}, nil, sfvEntries); err == nil {
		t.Fatalf("expected readable sfv to block a second sfv")
	}
}

func TestApplyDefaultsEnableDrFTPDStyleListAndCwdToggles(t *testing.T) {
	cfg := Config{}
	cfg.ApplyDefaults()

	if cfg.List.StatusBarEnabled == nil || !*cfg.List.StatusBarEnabled {
		t.Fatalf("expected statusbar_enabled default to true")
	}
	if cfg.List.StatusBarDirectory == nil || !*cfg.List.StatusBarDirectory {
		t.Fatalf("expected statusbar_directory default to true")
	}
	if cfg.List.MissingFiles == nil || !*cfg.List.MissingFiles {
		t.Fatalf("expected missing_files_enabled default to true")
	}
	if cfg.Zip.CWDDIZInfo == nil || !*cfg.Zip.CWDDIZInfo {
		t.Fatalf("expected cwd_diz_info default to true")
	}
	if cfg.Race.CWDRaceStats == nil || !*cfg.Race.CWDRaceStats {
		t.Fatalf("expected cwd_race_stats default to true")
	}
	if cfg.Race.STORRaceStats == nil || !*cfg.Race.STORRaceStats {
		t.Fatalf("expected stor_race_stats default to true")
	}
	if cfg.Race.CWDZipRaceStats == nil || !*cfg.Race.CWDZipRaceStats {
		t.Fatalf("expected cwd_zip_race_stats default to true")
	}
	if cfg.Race.STORZipRaceStats == nil || !*cfg.Race.STORZipRaceStats {
		t.Fatalf("expected stor_zip_race_stats default to true")
	}
	if cfg.Audio.CWDMP3Info == nil || !*cfg.Audio.CWDMP3Info {
		t.Fatalf("expected cwd_mp3_info default to true")
	}
	if cfg.Audio.STORMP3Info == nil || !*cfg.Audio.STORMP3Info {
		t.Fatalf("expected stor_mp3_info default to true")
	}
	if cfg.Audio.CWDFLACInfo == nil || !*cfg.Audio.CWDFLACInfo {
		t.Fatalf("expected cwd_flac_info default to true")
	}
	if cfg.Audio.STORFLACInfo == nil || !*cfg.Audio.STORFLACInfo {
		t.Fatalf("expected stor_flac_info default to true")
	}
}

func TestShowStatusBarAndMissingFilePoliciesRespectOverrides(t *testing.T) {
	cfg := Config{
		Enabled: true,
		Sections: SectionsConfig{
			SFV: []string{"/X265/*"},
		},
		List: ListConfig{
			StatusBarEnabled:   boolPtr(false),
			StatusBarDirectory: boolPtr(false),
			MissingFiles:       boolPtr(false),
		},
	}

	if ShowStatusBarForDir(cfg, "/X265/Release-GRP") {
		t.Fatalf("expected status bar to be disabled")
	}
	if StatusBarDirectoryForDir(cfg, "/X265/Release-GRP") {
		t.Fatalf("expected status bar not to render as directory when disabled")
	}
	if ShowMissingFilesForDir(cfg, "/X265/Release-GRP") {
		t.Fatalf("expected missing files to be disabled")
	}

	cfg.List.StatusBarEnabled = boolPtr(true)
	cfg.List.StatusBarDirectory = boolPtr(false)
	cfg.List.MissingFiles = boolPtr(true)

	if !ShowStatusBarForDir(cfg, "/X265/Release-GRP") {
		t.Fatalf("expected status bar to be enabled")
	}
	if StatusBarDirectoryForDir(cfg, "/X265/Release-GRP") {
		t.Fatalf("expected status bar to render as a file when configured false")
	}
	if !ShowMissingFilesForDir(cfg, "/X265/Release-GRP") {
		t.Fatalf("expected missing files to be enabled")
	}
	if ShowStatusBarForDir(cfg, "/X265") {
		t.Fatalf("expected section root status bar to stay disabled")
	}
	if RaceStatsOnCWDForDir(cfg, "/X265") {
		t.Fatalf("expected section root CWD race stats to stay disabled")
	}
}

func TestShowZipDIZAndAudioInfoPoliciesRespectOverrides(t *testing.T) {
	cfg := Config{
		Enabled: true,
		Sections: SectionsConfig{
			Zip: []string{"/0DAY/*/*"},
		},
		Zip: ZipConfig{
			CWDDIZInfo: boolPtr(false),
		},
		Audio: AudioConfig{
			Enabled:      true,
			CWDMP3Info:   boolPtr(false),
			STORMP3Info:  boolPtr(true),
			CWDFLACInfo:  boolPtr(true),
			STORFLACInfo: boolPtr(false),
		},
	}

	if ShowZipDIZOnCWDForDir(cfg, "/0DAY/0506/Zip.Release-GRP") {
		t.Fatalf("expected zip cwd diz info to be disabled")
	}
	cfg.Zip.CWDDIZInfo = boolPtr(true)
	if ShowZipDIZOnCWDForDir(cfg, "/0DAY/0506") {
		t.Fatalf("expected dated zip container CWD diz info to stay disabled")
	}
	if !ShowZipDIZOnCWDForDir(cfg, "/0DAY/0506/Zip.Release-GRP") {
		t.Fatalf("expected zip release CWD diz info to be enabled")
	}
	cfg.Zip.CWDDIZInfo = boolPtr(false)
	if ShowAudioInfoOnCWDForDir(cfg, "/MP3/0506/Artist-Album-GRP", map[string]string{"filename": "01-track.mp3"}) {
		t.Fatalf("expected mp3 cwd info to be disabled")
	}
	if !ShowAudioInfoOnSTORForDir(cfg, "/MP3/0506/Artist-Album-GRP", map[string]string{"filename": "01-track.mp3"}) {
		t.Fatalf("expected mp3 stor info to be enabled")
	}
	if !ShowAudioInfoOnCWDForDir(cfg, "/FLAC/0506/Artist-Album-GRP", map[string]string{"filename": "01-track.flac"}) {
		t.Fatalf("expected flac cwd info to be enabled")
	}
	if ShowAudioInfoOnSTORForDir(cfg, "/FLAC/0506/Artist-Album-GRP", map[string]string{"filename": "01-track.flac"}) {
		t.Fatalf("expected flac stor info to be disabled")
	}
}
