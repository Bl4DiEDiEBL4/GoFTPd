package releaseguard

import (
	"testing"

	"weaveftpd/internal/plugin"
	"weaveftpd/internal/user"
)

type testBridge struct {
	entries map[string][]plugin.FileEntry
}

func (b *testBridge) PluginListDir(dir string) []plugin.FileEntry {
	return append([]plugin.FileEntry(nil), b.entries[dir]...)
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
func (b *testBridge) DeleteFile(path string) error                                      { return nil }
func (b *testBridge) RenameFile(from, toDir, toName string) error                       { return nil }
func (b *testBridge) RelocatePath(from, toDir, toName string) error                     { return nil }
func (b *testBridge) RelocatePathToSlave(from, toDir, toName, targetSlave string) error { return nil }
func (b *testBridge) ScrubReleaseRaceMetadata(dirPath, owner, group string) error       { return nil }
func (b *testBridge) WriteFile(path string, content []byte) error                       { return nil }
func (b *testBridge) ReadFile(path string) ([]byte, error)                              { return nil, nil }
func (b *testBridge) ProbeMediaInfo(path, binary string, timeoutSeconds int) (map[string]string, error) {
	return nil, nil
}
func (b *testBridge) CacheMediaInfo(path string, fields map[string]string) {}
func (b *testBridge) GetDirMediaInfo(dirPath string) map[string]string     { return nil }
func (b *testBridge) FileExists(path string) bool                          { return false }
func (b *testBridge) GetFileSize(path string) int64                        { return -1 }
func (b *testBridge) GetSFVData(dirPath string) map[string]uint32          { return nil }
func (b *testBridge) GetRequestData(dirPath string) ([]plugin.RequestRecord, []plugin.RequestFillRecord) {
	return nil, nil
}
func (b *testBridge) SetRequestData(dirPath string, requests []plugin.RequestRecord, fills []plugin.RequestFillRecord) {
}
func (b *testBridge) PluginGetVFSRaceStats(dirPath string) (users []plugin.RaceUser, groups []plugin.RaceGroup, totalBytes int64, present int, total int) {
	return nil, nil, 0, 0, 0
}
func (b *testBridge) PluginGetVFSReleaseStats(dirPath string) (users []plugin.RaceUser, groups []plugin.RaceGroup, totalBytes int64, present int, total int) {
	return b.PluginGetVFSRaceStats(dirPath)
}

func newPluginForTest(t *testing.T, cfg map[string]interface{}, entries map[string][]plugin.FileEntry) *Plugin {
	t.Helper()
	p := New()
	if err := p.Init(&plugin.Services{Bridge: &testBridge{entries: entries}}, cfg); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	return p
}

func TestValidateMKDirRejectsSameNameInParent(t *testing.T) {
	p := newPluginForTest(t, map[string]interface{}{}, map[string][]plugin.FileEntry{
		"/MP3/0424": {
			{Name: "Artist-Album-2026-GRP", IsDir: true},
		},
	})
	err := p.ValidateMKDir(&user.User{Name: "tester"}, "/MP3/0424/Artist-Album-2026-GRP")
	if err == nil {
		t.Fatal("expected duplicate parent name to be rejected")
	}
}

func TestValidateMKDirRejectsBannedGroup(t *testing.T) {
	p := newPluginForTest(t, map[string]interface{}{
		"deny_groups": []interface{}{
			map[string]interface{}{"path": "/", "pattern": "-BAD$"},
		},
	}, nil)
	err := p.ValidateMKDir(&user.User{Name: "tester"}, "/0DAY/0424/Some.Release-2026-BAD")
	if err == nil {
		t.Fatal("expected banned group to be rejected")
	}
}

func TestValidateMKDirRejectsScopedBluRayRule(t *testing.T) {
	p := newPluginForTest(t, map[string]interface{}{
		"deny_dirs": []interface{}{
			map[string]interface{}{"path": "/TV-1080P", "pattern": `(?i)bluray`},
		},
	}, nil)
	err := p.ValidateMKDir(&user.User{Name: "tester"}, "/TV-1080P/The.Disastrous.Life.Of.Saiki.K.E15.1080p.BluRay.x264-URANiME")
	if err == nil {
		t.Fatal("expected scoped BluRay release to be rejected")
	}
}

func TestValidateMKDirRejectsSeriesInBluraySection(t *testing.T) {
	p := newPluginForTest(t, map[string]interface{}{
		"deny_dirs": []interface{}{
			map[string]interface{}{"path": "/BLURAY", "pattern": `(?i)(^|[._ -])S[0-9]{2}[ED][0-9]{2}([._ -]|$)`},
		},
	}, nil)
	if err := p.ValidateMKDir(&user.User{Name: "tester"}, "/BLURAY/Show.Name.S01D01.COMPLETE.BLURAY-GRP"); err == nil {
		t.Fatal("expected S01D01 release to be rejected in BLURAY")
	}
	if err := p.ValidateMKDir(&user.User{Name: "tester"}, "/TV-BLURAY/Show.Name.S01D01.COMPLETE.BLURAY-GRP"); err != nil {
		t.Fatalf("expected S01D01 release to pass in TV-BLURAY, got %v", err)
	}
}

func TestValidateMKDirEmitsRejectEvent(t *testing.T) {
	var gotType string
	var gotPath string
	var gotSection string
	var gotData map[string]string
	p := New()
	err := p.Init(&plugin.Services{
		Bridge: &testBridge{},
		EmitEvent: func(eventType, eventPath, filename, section string, size int64, speed float64, data map[string]string) {
			gotType = eventType
			gotPath = eventPath
			gotSection = section
			gotData = data
		},
	}, map[string]interface{}{
		"deny_dirs": []interface{}{
			map[string]interface{}{"path": "/TV-1080P", "pattern": `(?i)bluray`},
		},
	})
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	err = p.ValidateMKDir(&user.User{Name: "steel", PrimaryGroup: "iND"}, "/TV-1080P/The.Disastrous.Life.Of.Saiki.K.E15.1080p.BluRay.x264-URANiME")
	if err == nil {
		t.Fatal("expected release to be rejected")
	}
	if gotType != "CUSTOM" {
		t.Fatalf("event type = %q, want CUSTOM", gotType)
	}
	if gotPath != "/TV-1080P/The.Disastrous.Life.Of.Saiki.K.E15.1080p.BluRay.x264-URANiME" {
		t.Fatalf("event path = %q", gotPath)
	}
	if gotSection != "TV-1080P" {
		t.Fatalf("event section = %q, want TV-1080P", gotSection)
	}
	if gotData["announce_type"] != "RELEASEGUARD" || gotData["username"] != "steel" || gotData["rule_pattern"] != "(?i)bluray" {
		t.Fatalf("unexpected event data: %#v", gotData)
	}
}

func TestValidateMKDirCanDisableRejectAnnounce(t *testing.T) {
	emitted := false
	p := New()
	err := p.Init(&plugin.Services{
		Bridge: &testBridge{},
		EmitEvent: func(eventType, eventPath, filename, section string, size int64, speed float64, data map[string]string) {
			emitted = true
		},
	}, map[string]interface{}{
		"announce_rejects": false,
		"deny_dirs": []interface{}{
			map[string]interface{}{"path": "/TV-1080P", "pattern": `(?i)bluray`},
		},
	})
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	err = p.ValidateMKDir(&user.User{Name: "steel", PrimaryGroup: "iND"}, "/TV-1080P/The.Disastrous.Life.Of.Saiki.K.E15.1080p.BluRay.x264-URANiME")
	if err == nil {
		t.Fatal("expected release to be rejected")
	}
	if emitted {
		t.Fatal("expected reject event not to be emitted when announce_rejects is false")
	}
}

func TestReloadConfigUpdatesDenyRules(t *testing.T) {
	p := newPluginForTest(t, map[string]interface{}{}, nil)
	target := "/TV-1080P/The.Disastrous.Life.Of.Saiki.K.E15.1080p.BluRay.x264-URANiME"
	if err := p.ValidateMKDir(&user.User{Name: "tester"}, target); err != nil {
		t.Fatalf("expected release to pass before reload, got %v", err)
	}
	if err := p.ReloadConfig(map[string]interface{}{
		"deny_dirs": []interface{}{
			map[string]interface{}{"path": "/TV-1080P", "pattern": `(?i)bluray`},
		},
	}); err != nil {
		t.Fatalf("ReloadConfig failed: %v", err)
	}
	if err := p.ValidateMKDir(&user.User{Name: "tester"}, target); err == nil {
		t.Fatal("expected release to be rejected after reload")
	}
}

func TestValidateMKDirAppliesAllowRulesByScope(t *testing.T) {
	p := newPluginForTest(t, map[string]interface{}{
		"allow_dirs": []interface{}{
			map[string]interface{}{"path": "/MP3", "pattern": `.+-(WEB|CD)-\d{4}-[A-Z0-9]+$`},
		},
	}, nil)

	if err := p.ValidateMKDir(&user.User{Name: "tester"}, "/MP3/0424/Artist-Album-WEB-2026-GRP"); err != nil {
		t.Fatalf("expected allowed MP3 release to pass, got %v", err)
	}
	if err := p.ValidateMKDir(&user.User{Name: "tester"}, "/MP3/0424/Artist_Album"); err == nil {
		t.Fatal("expected disallowed MP3 release name to be rejected")
	}
	if err := p.ValidateMKDir(&user.User{Name: "tester"}, "/X265/Movie.Title.2026-GRP"); err != nil {
		t.Fatalf("expected non-scoped path to bypass allow rule, got %v", err)
	}
}
