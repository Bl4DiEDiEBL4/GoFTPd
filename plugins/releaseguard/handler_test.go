package releaseguard

import (
	"testing"

	"goftpd/internal/plugin"
	"goftpd/internal/user"
)

type testBridge struct {
	entries map[string][]plugin.FileEntry
}

func (b *testBridge) PluginListDir(dir string) []plugin.FileEntry {
	return append([]plugin.FileEntry(nil), b.entries[dir]...)
}
func (b *testBridge) MakeDir(path, owner, group string) {}
func (b *testBridge) Symlink(linkPath, targetPath string) error { return nil }
func (b *testBridge) Chmod(path string, mode uint32) error { return nil }
func (b *testBridge) CreateSparseFile(path string, size int64, owner, group string) error {
	return nil
}
func (b *testBridge) DeleteFile(path string) error { return nil }
func (b *testBridge) RenameFile(from, toDir, toName string) {}
func (b *testBridge) WriteFile(path string, content []byte) error { return nil }
func (b *testBridge) ReadFile(path string) ([]byte, error) { return nil, nil }
func (b *testBridge) ProbeMediaInfo(path, binary string, timeoutSeconds int) (map[string]string, error) {
	return nil, nil
}
func (b *testBridge) CacheMediaInfo(path string, fields map[string]string) {}
func (b *testBridge) FileExists(path string) bool { return false }
func (b *testBridge) GetFileSize(path string) int64 { return -1 }
func (b *testBridge) PluginGetVFSRaceStats(dirPath string) (users []plugin.RaceUser, groups []plugin.RaceGroup, totalBytes int64, present int, total int) {
	return nil, nil, 0, 0, 0
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
