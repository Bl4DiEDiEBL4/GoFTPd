package master

import (
	"errors"
	"io"
	"testing"
	"time"

	"weaveftpd/internal/core"
	"weaveftpd/internal/protocol"
	"weaveftpd/internal/zipscript"
)

func TestSelectSlaveForUploadPrefersOwnedAncestorDirectory(t *testing.T) {
	sm := NewSlaveManager("127.0.0.1", 1099, false, "", "", 60*time.Second)

	local := NewRemoteSlave("LOCAL", nil, nil, 60*time.Second, nil)
	local.available.Store(true)
	local.diskStatus = protocol.DiskStatus{SpaceAvailable: 100}

	other := NewRemoteSlave("OTHER", nil, nil, 60*time.Second, nil)
	other.available.Store(true)
	other.diskStatus = protocol.DiskStatus{SpaceAvailable: 1000}

	sm.slavesMu.Lock()
	sm.slaves[local.Name()] = local
	sm.slaves[other.Name()] = other
	sm.slavesMu.Unlock()

	sm.vfs.AddFile("/REQUESTS", VFSFile{IsDir: true, Seen: true, SlaveName: "LOCAL"})
	sm.vfs.AddFile("/REQUESTS/REQ-Test.Release-GRP", VFSFile{IsDir: true, Seen: true, SlaveName: "LOCAL"})
	sm.vfs.AddFile("/REQUESTS/REQ-Test.Release-GRP/Test.Release-GRP", VFSFile{IsDir: true, Seen: true, SlaveName: "LOCAL"})

	got := sm.SelectSlaveForUpload("/REQUESTS/REQ-Test.Release-GRP/Test.Release-GRP/file.r00")
	if got == nil || got.Name() != "LOCAL" {
		t.Fatalf("expected upload to stick to LOCAL, got %+v", got)
	}
}

func TestSelectSlaveForUploadFallsBackWhenOwnedAncestorSlaveUnavailable(t *testing.T) {
	sm := NewSlaveManager("127.0.0.1", 1099, false, "", "", 60*time.Second)

	local := NewRemoteSlave("LOCAL", nil, nil, 60*time.Second, nil)
	local.available.Store(false)
	local.diskStatus = protocol.DiskStatus{SpaceAvailable: 100}

	other := NewRemoteSlave("OTHER", nil, nil, 60*time.Second, nil)
	other.available.Store(true)
	other.diskStatus = protocol.DiskStatus{SpaceAvailable: 1000}

	sm.slavesMu.Lock()
	sm.slaves[local.Name()] = local
	sm.slaves[other.Name()] = other
	sm.slavesMu.Unlock()

	sm.vfs.AddFile("/REQUESTS", VFSFile{IsDir: true, Seen: true, SlaveName: "LOCAL"})
	sm.vfs.AddFile("/REQUESTS/REQ-Test.Release-GRP", VFSFile{IsDir: true, Seen: true, SlaveName: "LOCAL"})

	got := sm.SelectSlaveForUpload("/REQUESTS/REQ-Test.Release-GRP/file.r00")
	if got == nil || got.Name() != "OTHER" {
		t.Fatalf("expected fallback upload slave OTHER, got %+v", got)
	}
}

func TestSelectSlaveForUploadSkipsSlaveWithoutMatchingRoot(t *testing.T) {
	sm := NewSlaveManager("127.0.0.1", 1099, false, "", "", 60*time.Second)

	archiveOnly := NewRemoteSlave("ARCHIVE", nil, nil, 60*time.Second, nil)
	archiveOnly.available.Store(true)
	archiveOnly.diskStatus = protocol.DiskStatus{
		SpaceAvailable: 2000,
		Roots: []protocol.RootDiskStatus{
			{MountPath: "/ARCHiVE", SpaceAvailable: 2000},
		},
	}

	live := NewRemoteSlave("LIVE", nil, nil, 60*time.Second, nil)
	live.available.Store(true)
	live.diskStatus = protocol.DiskStatus{
		SpaceAvailable: 1000,
		Roots: []protocol.RootDiskStatus{
			{MountPath: "/", SpaceAvailable: 1000},
		},
	}

	sm.slavesMu.Lock()
	sm.slaves[archiveOnly.Name()] = archiveOnly
	sm.slaves[live.Name()] = live
	sm.slavesMu.Unlock()

	got := sm.SelectSlaveForUpload("/TV-1080P/Release-GRP/.tvmaze")
	if got == nil || got.Name() != "LIVE" {
		t.Fatalf("expected upload to skip archive-only slave and use LIVE, got %+v", got)
	}
}

func TestSelectSlaveForUploadIgnoresOwnedAncestorWithoutMatchingRoot(t *testing.T) {
	sm := NewSlaveManager("127.0.0.1", 1099, false, "", "", 60*time.Second)

	archiveOnly := NewRemoteSlave("ARCHIVE", nil, nil, 60*time.Second, nil)
	archiveOnly.available.Store(true)
	archiveOnly.diskStatus = protocol.DiskStatus{
		SpaceAvailable: 2000,
		Roots: []protocol.RootDiskStatus{
			{MountPath: "/ARCHiVE", SpaceAvailable: 2000},
		},
	}

	live := NewRemoteSlave("LIVE", nil, nil, 60*time.Second, nil)
	live.available.Store(true)
	live.diskStatus = protocol.DiskStatus{
		SpaceAvailable: 1000,
		Roots: []protocol.RootDiskStatus{
			{MountPath: "/", SpaceAvailable: 1000},
		},
	}

	sm.slavesMu.Lock()
	sm.slaves[archiveOnly.Name()] = archiveOnly
	sm.slaves[live.Name()] = live
	sm.slavesMu.Unlock()

	sm.vfs.AddFile("/TV-1080P", VFSFile{IsDir: true, Seen: true, SlaveName: "ARCHIVE"})
	sm.vfs.AddFile("/TV-1080P/Release-GRP", VFSFile{IsDir: true, Seen: true, SlaveName: "ARCHIVE"})

	got := sm.SelectSlaveForUpload("/TV-1080P/Release-GRP/.tvmaze")
	if got == nil || got.Name() != "LIVE" {
		t.Fatalf("expected owned ancestor on wrong mount to be ignored, got %+v", got)
	}
}

func TestRemoteSlaveOfflineClearsVFSFiles(t *testing.T) {
	sm := NewSlaveManager("127.0.0.1", 1099, false, "", "", 60*time.Second)
	sm.vfs.AddFile("/X265/release", VFSFile{IsDir: true, Seen: true, SlaveName: "LOCAL"})
	sm.vfs.AddFile("/X265/release/file.r00", VFSFile{Seen: true, SlaveName: "LOCAL", Size: 100})
	sm.vfs.SetSFVData("/X265/release", "release.sfv", map[string]uint32{"file.r00": 1})

	rs := NewRemoteSlave("LOCAL", nil, nil, 60*time.Second, func(name string) {
		sm.vfs.ClearSlave(name)
	})
	rs.SetOffline("test")

	if got := sm.vfs.GetFile("/X265/release/file.r00"); got != nil {
		t.Fatalf("expected offline slave file to be cleared from VFS, got %+v", got)
	}
	if got := sm.vfs.GetFile("/X265/release"); got != nil {
		t.Fatalf("expected offline slave directory to be cleared from VFS, got %+v", got)
	}
	if meta := sm.vfs.GetSFVData("/X265/release"); meta != nil {
		t.Fatalf("expected offline slave metadata to be cleared from VFS, got %+v", meta)
	}
}

func TestRemergeChecksumTargetsOnlyMissingKnownReleaseChecksums(t *testing.T) {
	sm := NewSlaveManager("127.0.0.1", 1099, false, "", "", 60*time.Second)
	sm.vfs.AddFile("/X265/release", VFSFile{IsDir: true, Seen: true, SlaveName: "LOCAL"})
	sm.vfs.AddFile("/X265/release/file.r00", VFSFile{
		Size:      100,
		Seen:      true,
		SlaveName: "LOCAL",
		Checksum:  0,
	})
	sm.vfs.AddFile("/X265/release/file.r01", VFSFile{
		Size:      100,
		Seen:      true,
		SlaveName: "LOCAL",
		Checksum:  2,
	})
	sm.vfs.AddFile("/X265/release/file.nfo", VFSFile{
		Size:      100,
		Seen:      true,
		SlaveName: "LOCAL",
		Checksum:  0,
	})

	targets := sm.remergeChecksumTargets("/X265/release", map[string]uint32{
		"file.r00": 1,
		"file.r01": 2,
		"file.r02": 3,
	})
	if len(targets) != 1 {
		t.Fatalf("targets = %+v, want one unverified known payload", targets)
	}
	if targets[0].filePath != "/X265/release/file.r00" || targets[0].expectedCRC != 1 {
		t.Fatalf("target = %+v, want file.r00 crc 1", targets[0])
	}
}

func TestSetRemergeChecksumThreadsNormalizes(t *testing.T) {
	sm := NewSlaveManager("127.0.0.1", 1099, false, "", "", 60*time.Second)

	sm.SetRemergeChecksumThreads(0)
	if got := sm.RemergeChecksumThreads(); got != defaultRemergeChecksumThreads {
		t.Fatalf("threads = %d, want default %d", got, defaultRemergeChecksumThreads)
	}

	sm.SetRemergeChecksumThreads(4)
	if got := sm.RemergeChecksumThreads(); got != 4 {
		t.Fatalf("threads = %d, want 4", got)
	}

	sm.SetRemergeChecksumThreads(maxRemergeChecksumThreads + 10)
	if got := sm.RemergeChecksumThreads(); got != maxRemergeChecksumThreads {
		t.Fatalf("threads = %d, want cap %d", got, maxRemergeChecksumThreads)
	}
}

func TestProcessRemergePrunesGhostChildrenPerScannedDirectory(t *testing.T) {
	sm := NewSlaveManager("127.0.0.1", 1099, false, "", "", 60*time.Second)
	rs := NewRemoteSlave("LOCAL", nil, nil, 60*time.Second, nil)

	sm.vfs.AddFile("/X265", VFSFile{IsDir: true, Seen: true, SlaveName: "LOCAL"})
	sm.vfs.AddFile("/X265/keep", VFSFile{IsDir: true, Seen: true, SlaveName: "LOCAL"})
	sm.vfs.AddFile("/X265/ghost", VFSFile{IsDir: true, Seen: true, SlaveName: "LOCAL"})
	sm.vfs.AddFile("/X265/ghost/file.r00", VFSFile{Size: 100, Seen: true, SlaveName: "LOCAL"})

	sm.ProcessRemerge(rs, &protocol.AsyncResponseRemerge{
		Path:          "/X265",
		PruneChildren: true,
		Files: []protocol.LightRemoteInode{
			{Name: "keep", IsDir: true, LastModified: time.Now().Unix()},
		},
	})

	if got := sm.vfs.GetFile("/X265/ghost"); got != nil {
		t.Fatalf("expected stale direct child dir to be pruned during remerge, got %+v", got)
	}
	if got := sm.vfs.GetFile("/X265/ghost/file.r00"); got != nil {
		t.Fatalf("expected stale child subtree to be pruned during remerge, got %+v", got)
	}
	if got := sm.vfs.GetFile("/X265/keep"); got == nil {
		t.Fatalf("expected re-seen child dir to remain")
	}
}

func TestProcessRemergeKeepsSkippedMountedSubtree(t *testing.T) {
	sm := NewSlaveManager("127.0.0.1", 1099, false, "", "", 60*time.Second)
	rs := NewRemoteSlave("LOCAL", nil, nil, 60*time.Second, nil)

	sm.vfs.AddFile("/ARCHiVE", VFSFile{IsDir: true, Seen: true, SlaveName: "LOCAL"})
	sm.vfs.AddFile("/ARCHiVE/release", VFSFile{IsDir: true, Seen: true, SlaveName: "LOCAL"})
	sm.vfs.AddFile("/X265", VFSFile{IsDir: true, Seen: true, SlaveName: "LOCAL"})

	sm.ProcessRemerge(rs, &protocol.AsyncResponseRemerge{
		Path: "/",
		Files: []protocol.LightRemoteInode{
			{Name: "X265", IsDir: true, LastModified: time.Now().Unix()},
		},
		SkippedSubtrees: []string{"/ARCHiVE"},
	})

	if got := sm.vfs.GetFile("/ARCHiVE"); got == nil {
		t.Fatalf("expected skipped archive mount to remain")
	}
	if got := sm.vfs.GetFile("/ARCHiVE/release"); got == nil {
		t.Fatalf("expected skipped archive subtree to remain")
	}
}

func TestSetSlavePoliciesConfiguresBackgroundRemergeJobs(t *testing.T) {
	sm := NewSlaveManager("127.0.0.1", 1099, false, "", "", 60*time.Second)

	sm.SetSlavePolicies(map[string]SlaveRoutePolicy{
		"LOCAL": {
			RemergeJobs: []SlaveRemergeJobPolicy{
				{
					Name:                   "site",
					Enabled:                true,
					Interval:               time.Hour,
					Roots:                  "normal",
					Path:                   "SITE",
					ExcludePaths:           []string{"ARCHiVE"},
					DelayMS:                10,
					PauseOnActiveTransfers: 1,
					Timeout:                30 * time.Minute,
					SkipBusy:               true,
				},
				{
					Name:       "archive",
					Enabled:    true,
					Interval:   2 * time.Hour,
					Roots:      "mounted",
					MountPaths: []string{"ARCHiVE", "ARCHiVE"},
				},
			},
		},
	})
	cfg := sm.backgroundRemergeSnapshot()

	if cfg.initialDelay != 5*time.Minute {
		t.Fatalf("initialDelay = %s, want 5m", cfg.initialDelay)
	}
	if cfg.stagger != 60*time.Second {
		t.Fatalf("stagger = %s, want 60s", cfg.stagger)
	}
	if len(cfg.jobs) != 2 {
		t.Fatalf("jobs = %+v, want 2 jobs", cfg.jobs)
	}
	if cfg.jobs[0].slaveName != "LOCAL" || cfg.jobs[0].basePath != "/SITE" || cfg.jobs[0].rootMode != "normal" {
		t.Fatalf("first job = %+v, want LOCAL normal /SITE", cfg.jobs[0])
	}
	if len(cfg.jobs[0].excludePaths) != 1 || cfg.jobs[0].excludePaths[0] != "/ARCHiVE" {
		t.Fatalf("first job excludePaths = %+v, want /ARCHiVE", cfg.jobs[0].excludePaths)
	}
	if cfg.jobs[0].delayMS != 10 || cfg.jobs[0].pauseOnActiveTransfers != 1 || !cfg.jobs[0].skipBusy {
		t.Fatalf("first job throttle fields = %+v", cfg.jobs[0])
	}
	if cfg.jobs[0].timeout != 30*time.Minute {
		t.Fatalf("first job timeout = %s, want 30m", cfg.jobs[0].timeout)
	}
	if cfg.jobs[1].rootMode != "mounted" || len(cfg.jobs[1].mountPaths) != 1 || cfg.jobs[1].mountPaths[0] != "/ARCHiVE" {
		t.Fatalf("second job = %+v, want mounted /ARCHiVE", cfg.jobs[1])
	}
}

func TestRemergeResponseWaitDurationDefaultsMountedJobsLonger(t *testing.T) {
	if got := remergeResponseWaitDuration("normal", 0); got != defaultRemergeResponseWaitDuration {
		t.Fatalf("normal timeout = %s, want %s", got, defaultRemergeResponseWaitDuration)
	}
	if got := remergeResponseWaitDuration("mounted", 0); got != defaultMountedRemergeWaitDuration {
		t.Fatalf("mounted timeout = %s, want %s", got, defaultMountedRemergeWaitDuration)
	}
	if got := remergeResponseWaitDuration("mounted", 2*time.Hour); got != 2*time.Hour {
		t.Fatalf("configured timeout = %s, want 2h", got)
	}
}

func TestIsResponseTimeoutError(t *testing.T) {
	if !isResponseTimeoutError(errors.New("timeout waiting for response 123 from slave LOCAL")) {
		t.Fatalf("expected response timeout to be detected")
	}
	if isResponseTimeoutError(errors.New("slave error: remerge stopped")) {
		t.Fatalf("did not expect non-timeout error to match")
	}
}

func TestProcessRemergeAddsReportedDirectory(t *testing.T) {
	sm := NewSlaveManager("127.0.0.1", 1099, false, "", "", 60*time.Second)
	rs := NewRemoteSlave("LOCAL", nil, nil, 60*time.Second, nil)

	sm.ProcessRemerge(rs, &protocol.AsyncResponseRemerge{
		Path:         "/ARCHiVE",
		LastModified: 123,
	})

	got := sm.vfs.GetFile("/ARCHiVE")
	if got == nil || !got.IsDir {
		t.Fatalf("expected reported directory to be added, got %+v", got)
	}
	if got.LastModified != 123 {
		t.Fatalf("LastModified = %d, want 123", got.LastModified)
	}
}

func TestReleaseRaceWindowStartsAtFirstPayload(t *testing.T) {
	sm := NewSlaveManager("127.0.0.1", 1099, false, "", "", 60*time.Second)
	// The mkdir only seeds the window; the race clock must not start until real
	// data flows. The first payload (start = 2500-100 = 2400, end = 2500)
	// defines the start, so the window is 100ms — not 1500ms from the mkdir seed.
	sm.StartReleaseRaceWindowAt("/X265/release", 1000)
	sm.NoteRacePayloadTransferAt("/X265/release", 100, 2500)

	if got := sm.GetReleaseRaceWindowMilliseconds("/X265/release"); got != 100 {
		t.Fatalf("race window = %dms, want 100ms", got)
	}
}

func TestMarkFileMissingRefreshesStatusMarkers(t *testing.T) {
	sm := NewSlaveManager("127.0.0.1", 1099, false, "", "", 60*time.Second)
	sm.SetStatusMarkerConfig(zipscript.Config{
		Enabled: true,
		Incomplete: zipscript.IncompleteConfig{
			Enabled:        true,
			Indicator:      "[incomplete]-%0",
			NoSFVIndicator: "[no-sfv]-%0",
			NFOIndicator:   "[no-nfo]-%0",
		},
	})

	sm.vfs.AddFile("/X265", VFSFile{IsDir: true, Seen: true, SlaveName: "LOCAL"})
	sm.vfs.AddFile("/X265/release", VFSFile{IsDir: true, Seen: true, SlaveName: "LOCAL"})
	sm.vfs.AddFile("/X265/release/release.sfv", VFSFile{Size: 10, Seen: true, SlaveName: "LOCAL"})
	sm.vfs.AddFile("/X265/release/release.nfo", VFSFile{Size: 10, Seen: true, SlaveName: "LOCAL"})
	sm.vfs.SetSFVData("/X265/release", "release.sfv", map[string]uint32{"file.r00": 1})
	sm.vfs.AddFile("/X265/release/file.r00", VFSFile{
		Size:      100,
		Seen:      true,
		SlaveName: "LOCAL",
		Checksum:  1,
	})
	sm.SyncStatusMarkersForPath("/X265/release", true)
	if got := sm.vfs.GetFile("/X265/[incomplete]-release"); got != nil {
		t.Fatalf("did not expect incomplete marker before missing file, got %+v", got)
	}

	bridge := &Bridge{sm: sm}
	if err := bridge.MarkFileMissing("/X265/release/file.r00"); err != nil {
		t.Fatalf("MarkFileMissing() error = %v", err)
	}

	got, ok := bridge.GetPathEntry("/X265/[incomplete]-release")
	if !ok || !got.IsSymlink || got.LinkTarget != "/X265/release" {
		t.Fatalf("expected incomplete marker to target release, got %+v", got)
	}
	if got := sm.vfs.GetFile("/X265/[incomplete]-release"); got == nil || !got.IsSymlink {
		t.Fatalf("expected marker to be stored in VFS, got %+v", got)
	}
}

func TestMissingMarkerSyncPathsCreatesAndDeletesExpectedMarkers(t *testing.T) {
	createPaths, deletePaths := missingMarkerSyncPaths("/X265/release", core.ReleaseStatus{
		Kind:          "sfv",
		ExpectedFiles: []string{"file.r00", "file.r01", "file.r02"},
		MissingFiles:  []string{"file.r02"},
	}, []*VFSFile{
		{Path: "/X265/release/file.r00", Size: 100},
		{Path: "/X265/release/file.r00-MISSING"},
		{Path: "/X265/release/file.r01-MISSING"},
	})

	if len(deletePaths) != 1 || deletePaths[0] != "/X265/release/file.r00-MISSING" {
		t.Fatalf("expected stale marker delete for present file, got %#v", deletePaths)
	}
	if len(createPaths) != 1 || createPaths[0] != "/X265/release/file.r02-MISSING" {
		t.Fatalf("expected missing marker create for absent file, got %#v", createPaths)
	}
}

func TestCacheZipProgressRefreshesStatusMarkers(t *testing.T) {
	sm := NewSlaveManager("127.0.0.1", 1099, false, "", "", 60*time.Second)
	sm.SetStatusMarkerConfig(zipscript.Config{
		Enabled: true,
		Sections: zipscript.SectionsConfig{
			Zip: []string{"/0DAY/*"},
		},
		Incomplete: zipscript.IncompleteConfig{
			Enabled:        true,
			Indicator:      "[incomplete]-%0",
			NoSFVIndicator: "[no-sfv]-%0",
			NFOIndicator:   "[no-nfo]-%0",
		},
	})

	sm.vfs.AddFile("/0DAY", VFSFile{IsDir: true, Seen: true, SlaveName: "LOCAL"})
	sm.vfs.AddFile("/0DAY/release", VFSFile{IsDir: true, Seen: true, SlaveName: "LOCAL"})
	sm.vfs.AddFile("/0DAY/release/file_id.diz", VFSFile{Size: 32, Seen: true, SlaveName: "LOCAL"})
	sm.vfs.AddFile("/0DAY/release/file.zip", VFSFile{Size: 100, Seen: true, SlaveName: "LOCAL"})

	bridge := &Bridge{sm: sm}
	bridge.CacheZipExpectedParts("/0DAY/release", 2, 123)
	sm.SyncStatusMarkersForPath("/0DAY/release", true)

	got, ok := bridge.GetPathEntry("/0DAY/[incomplete]-release")
	if !ok || !got.IsSymlink || got.LinkTarget != "/0DAY/release" {
		t.Fatalf("expected zip incomplete marker to target release, got %+v", got)
	}
	if got := sm.vfs.GetFile("/0DAY/[incomplete]-release"); got == nil || !got.IsSymlink {
		t.Fatalf("expected marker to be stored in VFS, got %+v", got)
	}
	if noSFV, ok := bridge.GetPathEntry("/0DAY/[no-sfv]-release"); ok {
		t.Fatalf("did not expect no-sfv marker for zip manifest progress, got %+v", noSFV)
	}

	sm.vfs.AddFile("/0DAY/release/file.z01", VFSFile{Size: 100, Seen: true, SlaveName: "LOCAL"})
	sm.SyncStatusMarkersForPath("/0DAY/release", true)
	if got, ok := bridge.GetPathEntry("/0DAY/[incomplete]-release"); ok {
		t.Fatalf("did not expect zip incomplete marker after completion, got %+v", got)
	}

	bridge.CacheZipExpectedParts("/0DAY/release", 0, 0)
	sm.SyncStatusMarkersForPath("/0DAY/release", true)
	if expected, ok := bridge.GetZipExpectedParts("/0DAY/release"); ok || expected != 0 {
		t.Fatalf("did not expect stale zip metadata to remain cached, got expected=%d ok=%v", expected, ok)
	}
}

func TestFileStatusMarkerSyncUpdatesOnlyTouchedRelease(t *testing.T) {
	sm := NewSlaveManager("127.0.0.1", 1099, false, "", "", 60*time.Second)
	sm.SetStatusMarkerConfig(zipscript.Config{
		Enabled: true,
		Incomplete: zipscript.IncompleteConfig{
			Enabled:        true,
			Indicator:      "[incomplete]-%0",
			NoSFVIndicator: "[no-sfv]-%0",
			NFOIndicator:   "[no-nfo]-%0",
		},
	})

	sm.vfs.AddFile("/X265", VFSFile{IsDir: true, Seen: true, SlaveName: "LOCAL"})
	sm.vfs.AddFile("/X265/release", VFSFile{IsDir: true, Seen: true, SlaveName: "LOCAL"})
	sm.vfs.AddFile("/X265/release/release.sfv", VFSFile{Size: 10, Seen: true, SlaveName: "LOCAL"})
	sm.vfs.AddFile("/X265/release/release.nfo", VFSFile{Size: 10, Seen: true, SlaveName: "LOCAL"})
	sm.vfs.SetSFVData("/X265/release", "release.sfv", map[string]uint32{
		"file.r00": 1,
		"file.r01": 2,
	})
	sm.vfs.AddFile("/X265/release/file.r00", VFSFile{Size: 100, Seen: true, SlaveName: "LOCAL", Checksum: 1})
	sm.SyncStatusMarkersForPath("/X265/release", true)
	bridge := &Bridge{sm: sm}
	if got, ok := bridge.GetPathEntry("/X265/[incomplete]-release"); !ok || !got.IsSymlink {
		t.Fatalf("expected incomplete marker before final file")
	}

	sm.vfs.AddFile("/X265/release/file.r01", VFSFile{Size: 100, Seen: true, SlaveName: "LOCAL", Checksum: 2})
	sm.SyncStatusMarkersForPath("/X265/release/file.r01", false)
	if got, ok := bridge.GetPathEntry("/X265/[incomplete]-release"); ok {
		t.Fatalf("did not expect incomplete marker after touched release completed, got %+v", got)
	}
}

func TestStatusMarkersListAndResolveLikeSymlinks(t *testing.T) {
	sm := NewSlaveManager("127.0.0.1", 1099, false, "", "", 60*time.Second)
	sm.SetStatusMarkerConfig(zipscript.Config{
		Enabled: true,
		Incomplete: zipscript.IncompleteConfig{
			Enabled:        true,
			Indicator:      "[incomplete]-%0",
			NoSFVIndicator: "[no-sfv]-%0",
			NFOIndicator:   "[no-nfo]-%0",
		},
	})

	sm.vfs.AddFile("/X265", VFSFile{IsDir: true, Seen: true, SlaveName: "LOCAL"})
	sm.vfs.AddFile("/X265/release", VFSFile{IsDir: true, Seen: true, SlaveName: "LOCAL"})
	sm.vfs.AddFile("/X265/release/release.sfv", VFSFile{Size: 10, Seen: true, SlaveName: "LOCAL"})
	sm.vfs.AddFile("/X265/release/release.nfo", VFSFile{Size: 10, Seen: true, SlaveName: "LOCAL"})
	sm.vfs.AddFile("/X265/release/file.r00", VFSFile{Size: 100, Seen: true, SlaveName: "LOCAL", Checksum: 1})
	sm.vfs.SetSFVData("/X265/release", "release.sfv", map[string]uint32{
		"file.r00": 1,
		"file.r01": 2,
	})
	sm.SyncStatusMarkersForPath("/X265/release", true)

	bridge := &Bridge{sm: sm}
	found := false
	for _, entry := range bridge.ListDir("/X265") {
		if entry.Name == "[incomplete]-release" {
			found = true
			if !entry.IsSymlink || entry.LinkTarget != "/X265/release" {
				t.Fatalf("expected marker symlink to release, got %+v", entry)
			}
		}
	}
	if !found {
		t.Fatalf("expected incomplete marker in ListDir")
	}
	if got := sm.vfs.GetFile("/X265/[incomplete]-release"); got == nil || !got.IsSymlink {
		t.Fatalf("expected marker to be persisted in VFS, got %+v", got)
	}
	if resolved := bridge.ResolvePath("/X265/[incomplete]-release/file.r00"); resolved != "/X265/release/file.r00" {
		t.Fatalf("expected marker path to resolve through target, got %s", resolved)
	}
}

func TestStatusMarkerSyncDeletesRemovedReleaseMarker(t *testing.T) {
	sm := NewSlaveManager("127.0.0.1", 1099, false, "", "", 60*time.Second)
	sm.SetStatusMarkerConfig(zipscript.Config{
		Enabled: true,
		Incomplete: zipscript.IncompleteConfig{
			Enabled:        true,
			Indicator:      "[incomplete]-%0",
			NoSFVIndicator: "[no-sfv]-%0",
			NFOIndicator:   "[no-nfo]-%0",
		},
	})

	sm.vfs.AddFile("/X265", VFSFile{IsDir: true, Seen: true, SlaveName: "LOCAL"})
	sm.vfs.AddFile("/X265/release", VFSFile{IsDir: true, Seen: true, SlaveName: "LOCAL"})
	sm.vfs.SetSFVData("/X265/release", "release.sfv", map[string]uint32{"file.r00": 1})
	sm.vfs.AddFile("/X265/release/release.sfv", VFSFile{Size: 10, Seen: true, SlaveName: "LOCAL"})
	sm.vfs.AddSymlink("/X265/[incomplete]-release", "/X265/release")

	sm.vfs.DeleteFile("/X265/release")
	sm.SyncStatusMarkersForPath("/X265/release", true)
	if got := sm.vfs.GetFile("/X265/[incomplete]-release"); got != nil {
		t.Fatalf("expected marker for removed release to be deleted, got %+v", got)
	}
}

func TestStatusMarkerSyncDeletesMarkerPointingOutsideParent(t *testing.T) {
	sm := NewSlaveManager("127.0.0.1", 1099, false, "", "", 60*time.Second)
	sm.SetStatusMarkerConfig(zipscript.Config{
		Enabled: true,
		Incomplete: zipscript.IncompleteConfig{
			Enabled:        true,
			Indicator:      "[incomplete]-%0",
			NoSFVIndicator: "[no-sfv]-%0",
			NFOIndicator:   "[no-nfo]-%0",
		},
	})

	sm.vfs.AddFile("/X265", VFSFile{IsDir: true, Seen: true, SlaveName: "LOCAL"})
	sm.vfs.AddFile("/ARCHiVE", VFSFile{IsDir: true, Seen: true, SlaveName: "LOCAL"})
	sm.vfs.AddFile("/ARCHiVE/old.release", VFSFile{IsDir: true, Seen: true, SlaveName: "LOCAL"})
	sm.vfs.AddSymlink("/X265/[incomplete]-old.release", "/ARCHiVE/old.release")

	sm.SyncStatusMarkersForPath("/X265", true)
	if got := sm.vfs.GetFile("/X265/[incomplete]-old.release"); got != nil {
		t.Fatalf("expected cross-parent stale marker to be removed, got %+v", got)
	}
}

func TestStatusMarkerSyncSkipsAndPrunesNukedReleaseMarkers(t *testing.T) {
	sm := NewSlaveManager("127.0.0.1", 1099, false, "", "", 60*time.Second)
	sm.SetStatusMarkerConfig(zipscript.Config{
		Enabled: true,
		Incomplete: zipscript.IncompleteConfig{
			Enabled:        true,
			Indicator:      "[incomplete]-%0",
			NoSFVIndicator: "[no-sfv]-%0",
			NFOIndicator:   "[no-nfo]-%0",
		},
	})

	nukedPath := "/X265/[NUKED]-release"
	sm.vfs.AddFile("/X265", VFSFile{IsDir: true, Seen: true, SlaveName: "LOCAL"})
	sm.vfs.AddFile(nukedPath, VFSFile{IsDir: true, Seen: true, SlaveName: "LOCAL"})
	sm.vfs.SetSFVData(nukedPath, "release.sfv", map[string]uint32{
		"file.r00": 1,
		"file.r01": 2,
	})
	sm.vfs.AddFile(nukedPath+"/release.sfv", VFSFile{Size: 10, Seen: true, SlaveName: "LOCAL"})
	sm.vfs.AddFile(nukedPath+"/file.r00", VFSFile{Size: 100, Seen: true, SlaveName: "LOCAL", Checksum: 1})

	sm.SyncStatusMarkersForPath(nukedPath, true)
	if got := sm.vfs.GetFile("/X265/[incomplete]-[NUKED]-release"); got != nil {
		t.Fatalf("expected nuked release not to get incomplete marker, got %+v", got)
	}

	sm.vfs.AddSymlink("/X265/[incomplete]-[NUKED]-release", nukedPath)
	sm.SyncStatusMarkersForPath("/X265", true)
	if got := sm.vfs.GetFile("/X265/[incomplete]-[NUKED]-release"); got != nil {
		t.Fatalf("expected stale nuked incomplete marker to be pruned, got %+v", got)
	}
}

func TestRequestStatusMarkersWorkBeforeReqFill(t *testing.T) {
	sm := NewSlaveManager("127.0.0.1", 1099, false, "", "", 60*time.Second)
	sm.SetStatusMarkerConfig(zipscript.Config{
		Enabled: true,
		Sections: zipscript.SectionsConfig{
			ReleaseCheck: []string{"/REQUESTS/*/*"},
		},
		Incomplete: zipscript.IncompleteConfig{
			Enabled:        true,
			NoSFVIndicator: "[no-sfv]-%0",
			NFOIndicator:   "[no-nfo]-%0",
		},
	})

	sm.vfs.AddFile("/REQUESTS", VFSFile{IsDir: true, Seen: true, SlaveName: "LOCAL"})
	sm.vfs.AddFile("/REQUESTS/REQ-Space.Haven.Linux-rG", VFSFile{IsDir: true, Seen: true, SlaveName: "LOCAL"})
	sm.vfs.AddFile("/REQUESTS/REQ-Space.Haven.Linux-rG/Space.Haven.Linux-rG", VFSFile{IsDir: true, Seen: true, SlaveName: "LOCAL"})
	sm.vfs.AddFile("/REQUESTS/REQ-Space.Haven.Linux-rG/Space.Haven.Linux-rG/file.r00", VFSFile{Size: 100, Seen: true, SlaveName: "LOCAL"})

	sm.SyncStatusMarkersForPath("/REQUESTS/REQ-Space.Haven.Linux-rG/Space.Haven.Linux-rG/file.r00", false)
	bridge := &Bridge{sm: sm}
	got, ok := bridge.GetPathEntry("/REQUESTS/REQ-Space.Haven.Linux-rG/[no-sfv]-Space.Haven.Linux-rG")
	if !ok || !got.IsSymlink || got.LinkTarget != "/REQUESTS/REQ-Space.Haven.Linux-rG/Space.Haven.Linux-rG" {
		t.Fatalf("expected open request child marker before reqfill, got %+v", got)
	}
	if got, ok := bridge.GetPathEntry("/REQUESTS/[no-sfv]-REQ-Space.Haven.Linux-rG"); ok {
		t.Fatalf("did not expect wrapper marker for request containing a release dir, got %+v", got)
	}

	sm.vfs.AddFile("/REQUESTS/REQ-Direct.Release-GRP", VFSFile{IsDir: true, Seen: true, SlaveName: "LOCAL"})
	sm.vfs.AddFile("/REQUESTS/REQ-Direct.Release-GRP/direct.r00", VFSFile{Size: 100, Seen: true, SlaveName: "LOCAL"})
	sm.SyncStatusMarkersForPath("/REQUESTS/REQ-Direct.Release-GRP/direct.r00", false)
	got, ok = bridge.GetPathEntry("/REQUESTS/[no-sfv]-REQ-Direct.Release-GRP")
	if !ok || !got.IsSymlink || got.LinkTarget != "/REQUESTS/REQ-Direct.Release-GRP" {
		t.Fatalf("expected direct request marker before reqfill, got %+v", got)
	}
}

func TestSetRemergeFlowControlNormalizesThresholds(t *testing.T) {
	sm := NewSlaveManager("127.0.0.1", 1099, false, "", "", 60*time.Second)
	sm.SetRemergeFlowControl(0, 999)

	pauseThreshold, resumeThreshold := sm.GetRemergeFlowControl()
	if pauseThreshold != 250 {
		t.Fatalf("expected default pause threshold 250, got %d", pauseThreshold)
	}
	if resumeThreshold != 125 {
		t.Fatalf("expected normalized resume threshold 125, got %d", resumeThreshold)
	}
}

func TestSlaveAuthGuardBansAfterConfiguredFailures(t *testing.T) {
	sm := NewSlaveManager("127.0.0.1", 1099, false, "", "", 60*time.Second)
	sm.ConfigureAuthGuard(2, time.Minute, 10*time.Minute)

	sm.recordAuthFailure("1.2.3.4", "1.2.3.4:1234", "unexpected EOF")
	if banned, _ := sm.isAuthBanned("1.2.3.4"); banned {
		t.Fatalf("IP should not be banned after first failed handshake")
	}

	sm.recordAuthFailure("1.2.3.4", "1.2.3.4:1234", "unexpected EOF")
	if banned, _ := sm.isAuthBanned("1.2.3.4"); !banned {
		t.Fatalf("IP should be banned after reaching the failure limit")
	}
}

func TestSlaveAuthGuardIgnoresBenignHandshakeDisconnects(t *testing.T) {
	sm := NewSlaveManager("127.0.0.1", 1099, false, "", "", 60*time.Second)
	sm.ConfigureAuthGuard(2, time.Minute, 10*time.Minute)

	sm.recordHandshakeReadFailure("1.2.3.4", "1.2.3.4:1234", io.EOF)
	sm.recordHandshakeReadFailure("1.2.3.4", "1.2.3.4:1235", errors.New("write tcp 1.2.3.4:1099->1.2.3.4:1235: write: broken pipe"))

	if banned, _ := sm.isAuthBanned("1.2.3.4"); banned {
		t.Fatalf("benign handshake disconnects should not ban a slave source")
	}
	if state := sm.authState["1.2.3.4"]; state != nil && state.Strikes != 0 {
		t.Fatalf("benign handshake disconnects should not record strikes, got %+v", state)
	}
}

func TestSlaveAuthGuardStillBansMalformedHandshake(t *testing.T) {
	sm := NewSlaveManager("127.0.0.1", 1099, false, "", "", 60*time.Second)
	sm.ConfigureAuthGuard(2, time.Minute, 10*time.Minute)

	err := errors.New("gob: local interface type *interface {} can only be decoded from remote interface type")
	sm.recordHandshakeReadFailure("1.2.3.4", "1.2.3.4:1234", err)
	sm.recordHandshakeReadFailure("1.2.3.4", "1.2.3.4:1235", err)

	if banned, _ := sm.isAuthBanned("1.2.3.4"); !banned {
		t.Fatalf("malformed handshakes should still ban after reaching the failure limit")
	}
}

func TestSlaveAuthGuardClearsOnSuccessfulSlaveLogin(t *testing.T) {
	sm := NewSlaveManager("127.0.0.1", 1099, false, "", "", 60*time.Second)
	sm.ConfigureAuthGuard(2, time.Minute, 10*time.Minute)

	sm.recordAuthFailure("1.2.3.4", "1.2.3.4:1234", "unexpected EOF")
	sm.clearAuthState("1.2.3.4")

	if banned, _ := sm.isAuthBanned("1.2.3.4"); banned {
		t.Fatalf("IP should not remain banned after state is cleared")
	}
}

func TestClearAuthTempBanRemovesActiveBan(t *testing.T) {
	sm := NewSlaveManager("127.0.0.1", 1099, false, "", "", 60*time.Second)
	sm.ConfigureAuthGuard(1, time.Minute, 10*time.Minute)
	sm.recordAuthFailure("1.2.3.4", "1.2.3.4:1234", "invalid slave name")

	removed, err := sm.ClearAuthTempBan("1.2.3.4")
	if err != nil {
		t.Fatalf("ClearAuthTempBan returned error: %v", err)
	}
	if !removed {
		t.Fatalf("expected active temp ban to be cleared")
	}
	if banned, _ := sm.isAuthBanned("1.2.3.4"); banned {
		t.Fatalf("IP should not remain banned after temp ban clear")
	}
}

func TestSlaveAuthAllowlistSupportsExactIPsAndCIDRs(t *testing.T) {
	sm := NewSlaveManager("127.0.0.1", 1099, false, "", "", 60*time.Second)
	if err := sm.ConfigureAuthAllowlist([]string{"1.2.3.4", "10.0.0.0/8"}); err != nil {
		t.Fatalf("ConfigureAuthAllowlist returned error: %v", err)
	}

	if !sm.isAuthAllowed("1.2.3.4") {
		t.Fatalf("exact allowlist IP should be allowed")
	}
	if !sm.isAuthAllowed("10.5.6.7") {
		t.Fatalf("CIDR allowlist IP should be allowed")
	}
	if sm.isAuthAllowed("8.8.8.8") {
		t.Fatalf("non-allowlisted IP should not be allowed")
	}
}

func TestSlaveAuthDenylistAddRemove(t *testing.T) {
	dir := t.TempDir()
	sm := NewSlaveManager("127.0.0.1", 1099, false, "", "", 60*time.Second)
	if err := sm.ConfigureAuthDenylistFile(dir + "/slave_denylist.txt"); err != nil {
		t.Fatalf("ConfigureAuthDenylistFile returned error: %v", err)
	}

	entry, err := sm.AddAuthDenyEntry("1.2.3.4")
	if err != nil {
		t.Fatalf("AddAuthDenyEntry returned error: %v", err)
	}
	if entry != "1.2.3.4" {
		t.Fatalf("unexpected canonical entry %q", entry)
	}

	if denied, _ := sm.isAuthExplicitlyDenied("1.2.3.4"); !denied {
		t.Fatalf("expected IP to be denylisted")
	}

	removed, err := sm.RemoveAuthDenyEntry("1.2.3.4")
	if err != nil {
		t.Fatalf("RemoveAuthDenyEntry returned error: %v", err)
	}
	if !removed {
		t.Fatalf("expected denylist entry to be removed")
	}

	if denied, _ := sm.isAuthExplicitlyDenied("1.2.3.4"); denied {
		t.Fatalf("expected IP to be removed from denylist")
	}
}
