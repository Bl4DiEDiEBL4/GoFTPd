package autonuke

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"weaveftpd/internal/core"
	"weaveftpd/internal/plugin"
)

type testBridge struct {
	entries map[string][]plugin.FileEntry
	sfvData map[string]map[string]uint32
	deleted []string
}

func (b *testBridge) PluginListDir(path string) []plugin.FileEntry {
	return append([]plugin.FileEntry(nil), b.entries[path]...)
}
func (b *testBridge) MakeDir(path, owner, group string) error   { return nil }
func (b *testBridge) Symlink(linkPath, targetPath string) error { return nil }
func (b *testBridge) VFSSymlink(linkPath, targetPath string) error {
	return nil
}
func (b *testBridge) Chmod(path string, mode uint32) error { return nil }
func (b *testBridge) CreateSparseFile(path string, size int64, owner, group string) error {
	return nil
}
func (b *testBridge) DeleteFile(path string) error {
	b.deleted = append(b.deleted, path)
	return nil
}
func (b *testBridge) RenameFile(from, toDir, toName string) error   { return nil }
func (b *testBridge) RelocatePath(from, toDir, toName string) error { return nil }
func (b *testBridge) RelocatePathToSlave(from, toDir, toName, targetSlave string) error {
	return nil
}
func (b *testBridge) ScrubReleaseRaceMetadata(dirPath, owner, group string) error { return nil }
func (b *testBridge) WriteFile(path string, content []byte) error                 { return nil }
func (b *testBridge) ReadFile(path string) ([]byte, error)                        { return nil, nil }
func (b *testBridge) ProbeMediaInfo(path, binary string, timeoutSeconds int) (map[string]string, error) {
	return nil, nil
}
func (b *testBridge) CacheMediaInfo(path string, fields map[string]string) {}
func (b *testBridge) FileExists(path string) bool                          { return false }
func (b *testBridge) GetFileSize(path string) int64                        { return -1 }
func (b *testBridge) GetSFVData(dirPath string) map[string]uint32 {
	if b.sfvData == nil {
		return nil
	}
	return b.sfvData[dirPath]
}
func (b *testBridge) GetRequestData(dirPath string) ([]plugin.RequestRecord, []plugin.RequestFillRecord) {
	return nil, nil
}
func (b *testBridge) SetRequestData(dirPath string, requests []plugin.RequestRecord, fills []plugin.RequestFillRecord) {
}
func (b *testBridge) GetDirMediaInfo(dirPath string) map[string]string { return nil }
func (b *testBridge) PluginGetVFSRaceStats(dirPath string) (users []plugin.RaceUser, groups []plugin.RaceGroup, totalBytes int64, present int, total int) {
	return nil, nil, 0, 0, 0
}
func (b *testBridge) PluginGetVFSReleaseStats(dirPath string) (users []plugin.RaceUser, groups []plugin.RaceGroup, totalBytes int64, present int, total int) {
	return b.PluginGetVFSRaceStats(dirPath)
}

func TestLoadConfigNormalizesSections(t *testing.T) {
	cfg := loadConfig(map[string]interface{}{
		"sections": []interface{}{
			"/GAMES",
			"/FOREIGN/TV-FR/*",
			"/MP3/MMDD",
		},
	})

	if len(cfg.Sections) != 3 {
		t.Fatalf("expected 3 sections, got %d", len(cfg.Sections))
	}
	if cfg.Sections[0] != "/GAMES/*" {
		t.Fatalf("unexpected first section: %q", cfg.Sections[0])
	}
	if cfg.Sections[1] != "/FOREIGN/TV-FR/*" {
		t.Fatalf("unexpected second section: %q", cfg.Sections[1])
	}
	if cfg.Sections[2] != "/MP3/MMDD/*" {
		t.Fatalf("unexpected third section: %q", cfg.Sections[2])
	}
}

func TestIsProtectedBaseMatchesSectionBases(t *testing.T) {
	h := &Handler{
		cfg: config{
			Sections: []string{"/GAMES/*", "/FOREIGN/TV-FR/*"},
		},
	}

	if !h.isProtectedBase("/GAMES") {
		t.Fatalf("expected /GAMES to be treated as a protected base")
	}
	if !h.isProtectedBase("/FOREIGN/TV-FR") {
		t.Fatalf("expected /FOREIGN/TV-FR to be treated as a protected base")
	}
	if h.isProtectedBase("/FOREIGN/TV-FR/Some.Release-GRP") {
		t.Fatalf("release dir should not be treated as the protected base itself")
	}
}

func TestIsDatedBucketName(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{name: "0502", want: true},
		{name: "20260502", want: true},
		{name: "2026-05-02", want: true},
		{name: "18-2026", want: true},
		{name: "2026-18", want: true},
		{name: "Some.Release-GRP", want: false},
		{name: "TV-DE", want: false},
	}
	for _, tc := range cases {
		if got := isDatedBucketName(tc.name); got != tc.want {
			t.Fatalf("isDatedBucketName(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestIsProtectedBaseWithDatedSectionPattern(t *testing.T) {
	h := &Handler{
		cfg: config{
			Sections: []string{
				"/TV-1080P/*",
				"/FOREIGN/TV-FR/*",
				"/XXX-0DAY/MMDD/*",
			},
		},
	}
	today := time.Now().Format("0102")

	cases := []struct {
		path string
		want bool
	}{
		{path: "/TV-1080P", want: true},
		{path: "/FOREIGN/TV-FR", want: true},
		{path: "/XXX-0DAY", want: true},
		{path: "/TV-1080P/Some.Release-GRP", want: false},
		{path: "/XXX-0DAY/" + today, want: false},
	}

	for _, tc := range cases {
		if got := h.isProtectedBase(tc.path); got != tc.want {
			t.Fatalf("isProtectedBase(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestAppendHistoryWritesJSONL(t *testing.T) {
	tmp := t.TempDir()
	h := &Handler{cfg: config{StateDir: tmp}}
	rel := releaseCandidate{
		Path:    "/TV-1080P/Example.Release-GRP",
		Name:    "Example.Release-GRP",
		Section: "TV-1080P",
		Owner:   "N0pe",
	}

	h.appendHistory("nuked", rel, "Empty", "multiplier=x10")

	data, err := os.ReadFile(filepath.Join(tmp, "history.jsonl"))
	if err != nil {
		t.Fatalf("read history.jsonl: %v", err)
	}
	text := string(data)
	for _, want := range []string{`"action":"nuked"`, `"path":"/TV-1080P/Example.Release-GRP"`, `"reason":"Empty"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("history output missing %s: %s", want, text)
		}
	}
}

func TestWarnEmitsAutonukeWarnEvent(t *testing.T) {
	tmp := t.TempDir()
	var gotType, gotPath, gotFilename, gotSection string
	var gotData map[string]string
	h := &Handler{
		cfg: config{StateDir: tmp},
		svc: &plugin.Services{
			EmitEvent: func(eventType, path, filename, section string, size int64, speed float64, data map[string]string) {
				gotType = eventType
				gotPath = path
				gotFilename = filename
				gotSection = section
				gotData = data
			},
		},
	}
	rel := releaseCandidate{
		Path:    "/0DAY/Example.Release-GRP",
		Name:    "Example.Release-GRP",
		Section: "0DAY",
		Owner:   "test0r",
	}

	h.warn(rel, "incomplete", "ANUKEINC", "Incomplete", 30, 60, "2 files missing")

	if gotType != string(core.EventAutonukeWarn) {
		t.Fatalf("event type = %q, want %q", gotType, core.EventAutonukeWarn)
	}
	if gotPath != rel.Path || gotFilename != rel.Name || gotSection != rel.Section {
		t.Fatalf("event target = (%q, %q, %q), want (%q, %q, %q)", gotPath, gotFilename, gotSection, rel.Path, rel.Name, rel.Section)
	}
	if gotData["tag"] != "ANUKEINC" || gotData["reason"] != "Incomplete" || gotData["warn_after"] != "30 minutes" || gotData["nuke_after"] != "60 minutes" {
		t.Fatalf("unexpected event data: %#v", gotData)
	}
	if !strings.Contains(gotData["message"], "ANUKEINC: [0DAY] Example.Release-GRP") {
		t.Fatalf("message missing release warning: %q", gotData["message"])
	}
}

func TestIncompleteWarningPersistsAcrossScans(t *testing.T) {
	tmp := t.TempDir()
	rel := releaseCandidate{
		Path:    "/TV-1080P/Example.Release-GRP",
		Name:    "Example.Release-GRP",
		Section: "TV-1080P",
		Owner:   "unknown",
		ModTime: time.Now().Add(-2 * time.Hour).Unix(),
	}

	warnEvents := 0
	h := &Handler{
		cfg: config{
			StateDir:         tmp,
			NukedPrefix:      "[NUKED]-",
			ApprovalMarkers:  []string{".approved"},
			CheckCompleteDir: true,
			Empty:            timedRule{Enabled: false},
			HalfEmpty:        timedPayloadRule{timedRule: timedRule{Enabled: false}},
			Incomplete: incompleteRule{
				timedRule: timedRule{
					Enabled:         true,
					WarnEnabled:     true,
					WarnAfterMin:    30,
					NukeAfterMin:    240,
					WarnTag:         "ANUKEINC",
					WarnDescription: "Incomplete",
					Reason:          "Incomplete",
				},
			},
		},
		svc: &plugin.Services{
			Bridge: &testBridge{
				entries: map[string][]plugin.FileEntry{
					rel.Path: {
						{Name: "present.mkv", IsDir: false},
						{Name: "example.sfv", IsDir: false},
					},
				},
				sfvData: map[string]map[string]uint32{
					rel.Path: {
						"present.mkv": 0,
						"missing.r00": 0,
					},
				},
			},
			EmitEvent: func(eventType, path, filename, section string, size int64, speed float64, data map[string]string) {
				if eventType == string(core.EventAutonukeWarn) {
					warnEvents++
				}
			},
		},
	}

	h.processRelease(rel)
	h.processRelease(rel)

	if warnEvents != 1 {
		t.Fatalf("warn events = %d, want 1", warnEvents)
	}
	if _, err := os.Stat(h.warningFile(rel, "incomplete")); err != nil {
		t.Fatalf("warning file missing after repeated scan: %v", err)
	}
}

func TestPatternRulePathNormalizesWildcardSectionBase(t *testing.T) {
	rules := patternRulesValue(map[string]interface{}{
		"rules": []interface{}{
			map[string]interface{}{
				"path":        "/GAMES/*",
				"patterns":    []interface{}{"_NSW"},
				"multiplier":  10,
				"description": "Banned release naming",
			},
		},
	}, "rules")

	if len(rules) != 1 {
		t.Fatalf("expected one rule, got %+v", rules)
	}
	if rules[0].BasePath != "/GAMES" {
		t.Fatalf("BasePath = %q, want /GAMES", rules[0].BasePath)
	}
	if !pathHasBase("/GAMES/Who_Needs_a_Hero_NSW-BREWS", "/GAMES/*") {
		t.Fatalf("wildcard rule path should match release under /GAMES")
	}
}

func TestBannedRuleTakesPriorityOverIncomplete(t *testing.T) {
	tmp := t.TempDir()
	rel := releaseCandidate{
		Path:    "/GAMES/Bad.Release_NSW-GRP",
		Name:    "Bad.Release_NSW-GRP",
		Section: "GAMES",
		Owner:   "test0r",
		ModTime: time.Now().Add(-2 * time.Hour).Unix(),
	}

	var siteArgs string
	h := &Handler{
		cfg: config{
			StateDir:         tmp,
			NukedPrefix:      "[NUKED]-",
			ApprovalMarkers:  []string{".approved"},
			CheckCompleteDir: true,
			Empty:            timedRule{Enabled: false},
			HalfEmpty:        timedPayloadRule{timedRule: timedRule{Enabled: false}},
			Incomplete: incompleteRule{
				timedRule: timedRule{
					Enabled:      true,
					NukeAfterMin: 1,
					Multiplier:   10,
					Reason:       "Incomplete",
				},
			},
			Banned: timedPatternRules{
				Enabled:      true,
				NukeAfterMin: 1,
				DefaultMulti: 3,
				Rules: []patternRule{
					{BasePath: "/GAMES/*", Patterns: []string{"_NSW"}, Description: "bad tag"},
				},
			},
		},
		svc: &plugin.Services{Bridge: &testBridge{
			entries: map[string][]plugin.FileEntry{
				rel.Path: {
					{Name: "present.r00", IsDir: false},
				},
			},
			sfvData: map[string]map[string]uint32{
				rel.Path: {"missing.r01": 0},
			},
		}},
		siteRunner: func(args string) ([]string, error) {
			siteArgs = args
			return []string{"200 OK"}, nil
		},
	}

	h.processRelease(rel)

	if !strings.Contains(siteArgs, "NUKE "+rel.Path+" x3 -Auto- bad tag: banned word _NSW") {
		t.Fatalf("expected bad-tag nuke to win, got SITE args %q", siteArgs)
	}
	data, err := os.ReadFile(filepath.Join(tmp, "history.jsonl"))
	if err != nil {
		t.Fatalf("read history.jsonl: %v", err)
	}
	text := string(data)
	if strings.Contains(text, "Incomplete") {
		t.Fatalf("incomplete rule should not win over banned tag: %s", text)
	}
	if !strings.Contains(text, `"reason":"bad tag: banned word _NSW"`) {
		t.Fatalf("history should record bad tag reason: %s", text)
	}
}

func TestBannedRuleRunsBeforeCompleteMarkerSkip(t *testing.T) {
	tmp := t.TempDir()
	rel := releaseCandidate{
		Path:    "/GAMES/Who_Needs_a_Hero_NSW-BREWS",
		Name:    "Who_Needs_a_Hero_NSW-BREWS",
		Section: "GAMES",
		Owner:   "test0r",
		ModTime: time.Now().Add(-2 * time.Hour).Unix(),
	}

	var siteArgs string
	h := &Handler{
		cfg: config{
			StateDir:         tmp,
			NukedPrefix:      "[NUKED]-",
			ApprovalMarkers:  []string{".approved"},
			CheckCompleteDir: true,
			Empty:            timedRule{Enabled: false},
			HalfEmpty:        timedPayloadRule{timedRule: timedRule{Enabled: false}},
			Incomplete:       incompleteRule{timedRule: timedRule{Enabled: false}},
			Banned: timedPatternRules{
				Enabled:      true,
				NukeAfterMin: 1,
				DefaultMulti: 10,
				Rules: []patternRule{
					{BasePath: "/GAMES/*", Patterns: []string{"_NSW"}, Description: "Banned release naming"},
				},
			},
		},
		svc: &plugin.Services{Bridge: &testBridge{
			entries: map[string][]plugin.FileEntry{
				rel.Path: {
					{Name: "complete", IsDir: true},
				},
			},
		}},
		siteRunner: func(args string) ([]string, error) {
			siteArgs = args
			return []string{"200 OK"}, nil
		},
	}

	h.processRelease(rel)

	want := "NUKE " + rel.Path + " x10 -Auto- Banned release naming: banned word _NSW"
	if !strings.Contains(siteArgs, want) {
		t.Fatalf("expected complete bad-tag release to be nuked with %q, got %q", want, siteArgs)
	}
}

func TestExpandPatternSkipsSymlinkCandidates(t *testing.T) {
	h := &Handler{
		svc: &plugin.Services{
			Bridge: &testBridge{
				entries: map[string][]plugin.FileEntry{
					"/MP3": {
						{Name: "!Today_MP3", IsDir: true, IsSymlink: true},
						{Name: "0512", IsDir: true},
					},
					"/MP3/0512": {
						{Name: "Real.Release-2026-GRP", IsDir: true},
						{Name: "!Linked.Release", IsDir: true, IsSymlink: true},
					},
				},
			},
		},
	}

	got := h.expandPattern("/MP3/*")
	if len(got) != 1 {
		t.Fatalf("expected only one real release candidate, got %+v", got)
	}
	if got[0].Path != "/MP3/0512/Real.Release-2026-GRP" {
		t.Fatalf("unexpected release candidate: %+v", got[0])
	}
}

func TestTimedNukeReason(t *testing.T) {
	tests := []struct {
		name    string
		base    string
		minutes int
		want    string
	}{
		{name: "empty with minutes", base: "Empty", minutes: 240, want: "Empty after 240 minutes"},
		{name: "singular minute", base: "Incomplete", minutes: 1, want: "Incomplete after 1 minute"},
		{name: "no minutes", base: "Half-Empty", minutes: 0, want: "Half-Empty"},
		{name: "blank base", base: "", minutes: 60, want: "Autonuke after 60 minutes"},
	}
	for _, tt := range tests {
		if got := timedNukeReason(tt.base, tt.minutes); got != tt.want {
			t.Fatalf("%s: timedNukeReason(%q, %d) = %q, want %q", tt.name, tt.base, tt.minutes, got, tt.want)
		}
	}
}

func TestCleanupOldNukesDeletesBridgePathAfterSiteWipe(t *testing.T) {
	tmp := t.TempDir()
	old := time.Now().Add(-2 * time.Hour).Unix()
	bridge := &testBridge{entries: map[string][]plugin.FileEntry{
		"/TV-1080P": {
			{Name: "[NUKED]-Old.Release-GRP", IsDir: true, ModTime: old},
		},
	}}
	var siteArgs []string
	var gotType string
	var gotData map[string]string
	h := &Handler{
		svc: &plugin.Services{
			Bridge: bridge,
			EmitEvent: func(eventType, path, filename, section string, size int64, speed float64, data map[string]string) {
				gotType = eventType
				gotData = data
			},
		},
		cfg: config{
			StateDir:    tmp,
			NukedPrefix: "[NUKED]-",
			Sections:    []string{"/TV-1080P/*"},
			DeleteNukes: deleteRule{DeleteAfterMin: 30},
		},
		siteRunner: func(args string) ([]string, error) {
			siteArgs = append(siteArgs, args)
			return []string{"200 Wiped /TV-1080P/[NUKED]-Old.Release-GRP."}, nil
		},
	}

	h.cleanupOldNukes()

	want := "/TV-1080P/[NUKED]-Old.Release-GRP"
	if len(siteArgs) != 1 || siteArgs[0] != "WIPE "+want {
		t.Fatalf("siteArgs = %#v, want WIPE %s", siteArgs, want)
	}
	if len(bridge.deleted) != 1 || bridge.deleted[0] != want {
		t.Fatalf("deleted = %#v, want %s", bridge.deleted, want)
	}
	if gotType != string(core.EventAutonukeDelete) {
		t.Fatalf("event type = %q, want %q", gotType, core.EventAutonukeDelete)
	}
	if !strings.Contains(gotData["message"], "deleted old nuked release") {
		t.Fatalf("cleanup announce missing message: %#v", gotData)
	}
	if gotData["relname"] != "Old.Release-GRP" {
		t.Fatalf("cleanup relname = %q, want Old.Release-GRP", gotData["relname"])
	}
}

func TestDisplayReleaseNameUsesConfiguredPrefix(t *testing.T) {
	if got := displayReleaseName("NUKED-Old.Release-GRP", "NUKED-"); got != "Old.Release-GRP" {
		t.Fatalf("custom prefix result = %q", got)
	}
	if got := displayReleaseName("[GROUP]-Legit.Release", "[NUKED]-"); got != "[GROUP]-Legit.Release" {
		t.Fatalf("legitimate bracketed release was altered: %q", got)
	}
}
