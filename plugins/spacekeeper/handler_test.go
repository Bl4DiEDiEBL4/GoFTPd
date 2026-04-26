package spacekeeper

import (
	"path"
	"testing"
	"time"

	"goftpd/internal/plugin"
)

type testBridge struct {
	tree      map[string][]plugin.FileEntry
	raceStats map[string]struct {
		present int
		total   int
	}
	deleted []string
}

func (b *testBridge) PluginListDir(dir string) []plugin.FileEntry {
	return append([]plugin.FileEntry(nil), b.tree[cleanAbs(dir)]...)
}
func (b *testBridge) MakeDir(path, owner, group string)                                   {}
func (b *testBridge) Symlink(linkPath, targetPath string) error                           { return nil }
func (b *testBridge) Chmod(path string, mode uint32) error                                { return nil }
func (b *testBridge) CreateSparseFile(path string, size int64, owner, group string) error { return nil }
func (b *testBridge) DeleteFile(path string) error {
	b.deleted = append(b.deleted, cleanAbs(path))
	return nil
}
func (b *testBridge) RenameFile(from, toDir, toName string) {}
func (b *testBridge) RelocatePath(from, toDir, toName string) error {
	b.deleted = append(b.deleted, cleanAbs(path.Join(toDir, toName)))
	return nil
}
func (b *testBridge) RelocatePathToSlave(from, toDir, toName, targetSlave string) error {
	b.deleted = append(b.deleted, cleanAbs(path.Join(toDir, toName)))
	return nil
}
func (b *testBridge) WriteFile(path string, content []byte) error { return nil }
func (b *testBridge) ReadFile(path string) ([]byte, error)        { return nil, nil }
func (b *testBridge) ProbeMediaInfo(path, binary string, timeoutSeconds int) (map[string]string, error) {
	return nil, nil
}
func (b *testBridge) CacheMediaInfo(path string, fields map[string]string) {}
func (b *testBridge) FileExists(path string) bool                          { return false }
func (b *testBridge) GetFileSize(path string) int64                        { return 0 }
func (b *testBridge) GetSFVData(dirPath string) map[string]uint32          { return nil }
func (b *testBridge) PluginGetVFSRaceStats(dirPath string) ([]plugin.RaceUser, []plugin.RaceGroup, int64, int, int) {
	stats := b.raceStats[cleanAbs(dirPath)]
	return nil, nil, 0, stats.present, stats.total
}

func TestParseRulesAcceptsGiBThresholds(t *testing.T) {
	rules := parseRules([]interface{}{
		map[string]interface{}{
			"name":            "0day-clean",
			"slave":           "SLAVE1",
			"action":          "delete_oldest",
			"paths":           []interface{}{"/0DAY/*/*"},
			"trigger_free_gb": float64(50),
			"target_free_gb":  float64(80),
		},
	})
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
	if rules[0].TriggerFreeBytes != 50*1024*1024*1024 {
		t.Fatalf("unexpected trigger bytes: %d", rules[0].TriggerFreeBytes)
	}
	if rules[0].TargetFreeBytes != 80*1024*1024*1024 {
		t.Fatalf("unexpected target bytes: %d", rules[0].TargetFreeBytes)
	}
}

func TestEvaluateDeletesOldestEligibleCandidate(t *testing.T) {
	now := time.Now().Add(-2 * time.Hour).Unix()
	bridge := &testBridge{
		tree: map[string][]plugin.FileEntry{
			"/0DAY": {
				{Name: "2026-04-26", IsDir: true, ModTime: now},
			},
			"/0DAY/2026-04-26": {
				{Name: "old-release", IsDir: true, ModTime: now},
				{Name: "new-release", IsDir: true, ModTime: time.Now().Unix()},
			},
			"/0DAY/2026-04-26/old-release": {
				{Name: "file1.zip", Size: 2 * 1024 * 1024 * 1024, Slave: "SLAVE1"},
			},
			"/0DAY/2026-04-26/new-release": {
				{Name: "file2.zip", Size: 3 * 1024 * 1024 * 1024, Slave: "SLAVE1"},
			},
		},
		raceStats: map[string]struct {
			present int
			total   int
		}{
			"/0DAY/2026-04-26/old-release": {present: 10, total: 10},
			"/0DAY/2026-04-26/new-release": {present: 10, total: 10},
		},
	}
	h := &Handler{
		svc: &plugin.Services{
			Bridge: bridge,
			ListSlaveStates: func() []plugin.SlaveState {
				return []plugin.SlaveState{
					{Name: "SLAVE1", Available: true, FreeBytes: 40 * 1024 * 1024 * 1024},
				}
			},
			ListActiveSessions: func() []plugin.ActiveSession { return nil },
		},
		rules: []rule{
			{
				Name:             "0day-clean",
				Slave:            "SLAVE1",
				Action:           "delete_oldest",
				Paths:            []string{"/0DAY/*/*"},
				TriggerFreeBytes: 50 * 1024 * 1024 * 1024,
				TargetFreeBytes:  60 * 1024 * 1024 * 1024,
				MinAge:           10 * time.Minute,
				SkipIncomplete:   true,
				SkipActiveRaces:  true,
				MaxActions:       1,
			},
		},
		enableFreeSpace: true,
		inflight:        map[string]time.Time{},
	}

	h.evaluate(time.Now())

	if len(bridge.deleted) != 1 {
		t.Fatalf("expected 1 delete, got %d", len(bridge.deleted))
	}
	if bridge.deleted[0] != "/0DAY/2026-04-26/old-release" {
		t.Fatalf("expected oldest release to be deleted, got %s", bridge.deleted[0])
	}
}

func TestParseRulesRequiresThresholdsForArchiveOldest(t *testing.T) {
	rules := parseRules([]interface{}{
		map[string]interface{}{
			"name":        "0day-archive",
			"slave":       "SLAVE1",
			"action":      "archive_oldest",
			"destination": "/ARCHiVE/0DAY",
			"paths":       []interface{}{"/0DAY/*/*"},
		},
	})
	if len(rules) != 0 {
		t.Fatalf("expected archive_oldest without thresholds to be rejected, got %d rule(s)", len(rules))
	}
}

func TestArchiveOldestWaitsForLowSpaceThreshold(t *testing.T) {
	now := time.Now().Add(-2 * time.Hour).Unix()
	bridge := &testBridge{
		tree: map[string][]plugin.FileEntry{
			"/0DAY": {
				{Name: "2026-04-26", IsDir: true, ModTime: now},
			},
			"/0DAY/2026-04-26": {
				{Name: "old-release", IsDir: true, ModTime: now},
			},
			"/0DAY/2026-04-26/old-release": {
				{Name: "file1.zip", Size: 2 * 1024 * 1024 * 1024, Slave: "SLAVE1"},
			},
		},
		raceStats: map[string]struct {
			present int
			total   int
		}{
			"/0DAY/2026-04-26/old-release": {present: 10, total: 10},
		},
	}
	h := &Handler{
		svc: &plugin.Services{
			Bridge: bridge,
			ListSlaveStates: func() []plugin.SlaveState {
				return []plugin.SlaveState{
					{Name: "SLAVE1", Available: true, FreeBytes: 70 * 1024 * 1024 * 1024},
				}
			},
			ListActiveSessions: func() []plugin.ActiveSession { return nil },
		},
		rules: []rule{
			{
				Name:             "0day-archive",
				Slave:            "SLAVE1",
				Action:           "archive_oldest",
				Paths:            []string{"/0DAY/*/*"},
				Destination:      "/ARCHiVE/0DAY",
				TriggerFreeBytes: 40 * 1024 * 1024 * 1024,
				TargetFreeBytes:  60 * 1024 * 1024 * 1024,
				MinAge:           10 * time.Minute,
				SkipIncomplete:   true,
				SkipActiveRaces:  true,
				MaxActions:       1,
			},
		},
		enableArchive: true,
		inflight:      map[string]time.Time{},
	}

	h.evaluate(time.Now())

	if len(bridge.deleted) != 0 {
		t.Fatalf("expected no archive work when free space is above trigger, got %v", bridge.deleted)
	}
}
