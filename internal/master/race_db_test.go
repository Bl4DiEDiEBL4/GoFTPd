package master

import (
	"path/filepath"
	"testing"
)

func TestRaceDBSearchDirsReturnsReleaseResults(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "race.db")
	rdb, err := NewRaceDB(dbPath)
	if err != nil {
		t.Fatalf("NewRaceDB failed: %v", err)
	}
	defer rdb.Close()

	if err := rdb.SaveSFV("/site/MP3/Release-One", "release.sfv", map[string]uint32{
		"01-track.mp3": 1,
		"02-track.mp3": 2,
	}); err != nil {
		t.Fatalf("SaveSFV release one failed: %v", err)
	}
	if err := rdb.RecordUpload("/site/MP3/Release-One/01-track.mp3", "steel", "Admin", 100, 1000, 1); err != nil {
		t.Fatalf("RecordUpload release one track 1 failed: %v", err)
	}
	if err := rdb.RecordUpload("/site/MP3/Release-One/02-track.mp3", "steel", "Admin", 200, 1000, 2); err != nil {
		t.Fatalf("RecordUpload release one track 2 failed: %v", err)
	}

	if err := rdb.SaveSFV("/site/FLAC/Another-Release", "another.sfv", map[string]uint32{
		"01-track.flac": 3,
	}); err != nil {
		t.Fatalf("SaveSFV release two failed: %v", err)
	}
	if err := rdb.RecordUpload("/site/FLAC/Another-Release/01-track.flac", "n0pe", "Admin", 300, 1000, 3); err != nil {
		t.Fatalf("RecordUpload release two failed: %v", err)
	}

	results := rdb.SearchDirs("release", 10)
	if len(results) != 2 {
		t.Fatalf("expected 2 search results, got %d", len(results))
	}
	if results[0].Path != "/site/FLAC/Another-Release" {
		t.Fatalf("expected alphabetical first result, got %s", results[0].Path)
	}
	if results[1].Path != "/site/MP3/Release-One" {
		t.Fatalf("expected second result /site/MP3/Release-One, got %s", results[1].Path)
	}
	if results[1].Files != 2 {
		t.Fatalf("expected 2 present files for release one, got %d", results[1].Files)
	}
	if results[1].Bytes != 300 {
		t.Fatalf("expected 300 bytes for release one, got %d", results[1].Bytes)
	}
}

func TestRaceDBSearchDirsRespectsLimit(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "race.db")
	rdb, err := NewRaceDB(dbPath)
	if err != nil {
		t.Fatalf("NewRaceDB failed: %v", err)
	}
	defer rdb.Close()

	for _, dir := range []string{
		"/site/MP3/Alpha-Release",
		"/site/MP3/Beta-Release",
		"/site/MP3/Gamma-Release",
	} {
		if err := rdb.SaveSFV(dir, "release.sfv", map[string]uint32{"01-track.mp3": 1}); err != nil {
			t.Fatalf("SaveSFV %s failed: %v", dir, err)
		}
	}

	results := rdb.SearchDirs("release", 2)
	if len(results) != 2 {
		t.Fatalf("expected 2 limited search results, got %d", len(results))
	}
	if results[0].Path != "/site/MP3/Alpha-Release" || results[1].Path != "/site/MP3/Beta-Release" {
		t.Fatalf("unexpected limited result order: %+v", results)
	}
}

func TestRaceDBSearchDirsMatchesAllQueryTokens(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "race.db")
	rdb, err := NewRaceDB(dbPath)
	if err != nil {
		t.Fatalf("NewRaceDB failed: %v", err)
	}
	defer rdb.Close()

	releasePath := "/site/MP3/Lana_Del_Rey-First_Light-SINGLE-OST-WEB-2026-ENRiCH"
	if err := rdb.SaveSFV(releasePath, "release.sfv", map[string]uint32{
		"01-track.mp3": 1,
	}); err != nil {
		t.Fatalf("SaveSFV failed: %v", err)
	}

	results := rdb.SearchDirs("lana rey", 10)
	if len(results) != 1 {
		t.Fatalf("expected 1 result for multi-token query, got %d", len(results))
	}
	if results[0].Path != releasePath {
		t.Fatalf("expected %s, got %s", releasePath, results[0].Path)
	}
}

func TestRaceDBReconcileDoesNotPurgeWhenVFSIsEmpty(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "race.db")
	rdb, err := NewRaceDB(dbPath)
	if err != nil {
		t.Fatalf("NewRaceDB failed: %v", err)
	}
	defer rdb.Close()

	releasePath := "/site/TV/Some.Release-GRP"
	if err := rdb.SaveSFV(releasePath, "release.sfv", map[string]uint32{
		"file.r00": 1,
	}); err != nil {
		t.Fatalf("SaveSFV failed: %v", err)
	}

	if err := rdb.Reconcile(NewVirtualFileSystem()); err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	results := rdb.SearchDirs("Some.Release", 10)
	if len(results) != 1 {
		t.Fatalf("expected release to remain after reconcile, got %d results", len(results))
	}
	if results[0].Path != releasePath {
		t.Fatalf("expected release path %s, got %s", releasePath, results[0].Path)
	}
}

func TestRecordUploadDoesNotRewriteExistingPresentRaceWinner(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "race.db")
	rdb, err := NewRaceDB(dbPath)
	if err != nil {
		t.Fatalf("NewRaceDB failed: %v", err)
	}
	defer rdb.Close()

	releasePath := "/site/MP3/Stable.Release-GRP"
	if err := rdb.SaveSFV(releasePath, "release.sfv", map[string]uint32{
		"01-track.mp3": 1,
	}); err != nil {
		t.Fatalf("SaveSFV failed: %v", err)
	}

	filePath := releasePath + "/01-track.mp3"
	if err := rdb.RecordUpload(filePath, "first", "GRP1", 100, 1000, 1); err != nil {
		t.Fatalf("first RecordUpload failed: %v", err)
	}
	if err := rdb.RecordUpload(filePath, "second", "GRP2", 200, 2000, 2); err != nil {
		t.Fatalf("second RecordUpload failed: %v", err)
	}

	users, _, totalBytes, present, total := rdb.GetRaceStats(releasePath)
	if total != 1 || present != 1 {
		t.Fatalf("expected complete 1/1 release, got present=%d total=%d", present, total)
	}
	if totalBytes != 100 {
		t.Fatalf("expected original file size to remain 100 bytes, got %d", totalBytes)
	}
	if len(users) != 1 {
		t.Fatalf("expected one race user, got %d", len(users))
	}
	if users[0].Name != "first" || users[0].Group != "GRP1" {
		t.Fatalf("expected original winner first/GRP1 to remain, got %s/%s", users[0].Name, users[0].Group)
	}
}

func TestRaceDBScrubReleaseRaceMetadataKeepsCompletenessButHidesRacers(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "race.db")
	rdb, err := NewRaceDB(dbPath)
	if err != nil {
		t.Fatalf("NewRaceDB failed: %v", err)
	}
	defer rdb.Close()

	releasePath := "/GAMES/release"
	if err := rdb.SaveSFV(releasePath, "release.sfv", map[string]uint32{"file.r00": 0x1234}); err != nil {
		t.Fatalf("SaveSFV failed: %v", err)
	}
	if err := rdb.RecordUpload(releasePath+"/file.r00", "racer", "iND", 100, 1000, 0x1234); err != nil {
		t.Fatalf("RecordUpload failed: %v", err)
	}

	users, _, _, present, total := rdb.GetRaceStats(releasePath)
	if len(users) != 1 || present != 1 || total != 1 {
		t.Fatalf("expected visible race stats before scrub, users=%d present=%d total=%d", len(users), present, total)
	}

	if err := rdb.ScrubReleaseRaceMetadata(releasePath, "PRE", "PRE"); err != nil {
		t.Fatalf("ScrubReleaseRaceMetadata failed: %v", err)
	}

	users, groups, totalBytes, present, total := rdb.GetRaceStats(releasePath)
	if len(users) != 0 || len(groups) != 0 {
		t.Fatalf("expected scrubbed race stats to hide users/groups, users=%v groups=%v", users, groups)
	}
	if present != 1 || total != 1 || totalBytes != 100 {
		t.Fatalf("expected completeness to survive scrub, present=%d total=%d bytes=%d", present, total, totalBytes)
	}

	vfs := NewVirtualFileSystem()
	vfs.AddFile(releasePath, VFSFile{IsDir: true, Seen: true})
	vfs.AddFile(releasePath+"/file.r00", VFSFile{Size: 100, Seen: true, Owner: "WeaveFTPd", Group: "WeaveFTPd", Checksum: 0x1234})
	if hydrated, err := rdb.HydrateVFS(vfs); err != nil || hydrated != 0 {
		t.Fatalf("HydrateVFS hydrated=%d err=%v", hydrated, err)
	}
	file := vfs.GetFile(releasePath + "/file.r00")
	if file.Owner != "WeaveFTPd" || file.Group != "WeaveFTPd" {
		t.Fatalf("expected scrubbed race DB rows not to hydrate race owner/group, got %s/%s", file.Owner, file.Group)
	}
	if file.XferTime != 0 {
		t.Fatalf("expected scrubbed transfer time to stay 0, got %d", file.XferTime)
	}
}

func TestReplaceReleaseFilesPreservesExistingRaceMetadataFromWeakRescan(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "race.db")
	rdb, err := NewRaceDB(dbPath)
	if err != nil {
		t.Fatalf("NewRaceDB failed: %v", err)
	}
	defer rdb.Close()

	releasePath := "/site/X265/Stable.Release-GRP"
	entries := map[string]uint32{"file.r00": 1}
	if err := rdb.SaveSFV(releasePath, "release.sfv", entries); err != nil {
		t.Fatalf("SaveSFV failed: %v", err)
	}
	if err := rdb.RecordUpload(releasePath+"/file.r00", "Neptun", "iND", 100, 1000, 1); err != nil {
		t.Fatalf("RecordUpload failed: %v", err)
	}

	err = rdb.ReplaceReleaseFiles(releasePath, "release.sfv", entries, map[string]ReleaseFileRecord{
		"file.r00": {
			FileName:   "file.r00",
			Owner:      "WeaveFTPd",
			Group:      "root",
			SizeBytes:  100,
			DurationMs: 0,
			Checksum:   1,
		},
	})
	if err != nil {
		t.Fatalf("ReplaceReleaseFiles failed: %v", err)
	}

	users, groups, totalBytes, present, total := rdb.GetRaceStats(releasePath)
	if total != 1 || present != 1 || totalBytes != 100 {
		t.Fatalf("expected complete release, got present=%d total=%d bytes=%d", present, total, totalBytes)
	}
	if len(users) != 1 || users[0].Name != "Neptun" || users[0].Group != "iND" || users[0].DurationMs != 1000 {
		t.Fatalf("expected original user metadata to survive weak rescan, got %+v", users)
	}
	if len(groups) != 1 || groups[0].Name != "iND" || groups[0].Files != 1 {
		t.Fatalf("expected original group metadata to survive weak rescan, got %+v", groups)
	}
}

func TestRaceDBGetRaceStatsUsesNormalizedFilenameKeys(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "race.db")
	rdb, err := NewRaceDB(dbPath)
	if err != nil {
		t.Fatalf("NewRaceDB failed: %v", err)
	}
	defer rdb.Close()

	releasePath := "/site/MP3/MixedCase.Release-GRP"
	if err := rdb.SaveSFV(releasePath, "release.sfv", map[string]uint32{
		"01-Track.MP3": 1,
	}); err != nil {
		t.Fatalf("SaveSFV failed: %v", err)
	}
	if err := rdb.RecordUpload(releasePath+"/01-track.mp3", "steel", "GRP", 1234, 1000, 1); err != nil {
		t.Fatalf("RecordUpload failed: %v", err)
	}

	users, groups, totalBytes, present, total := rdb.GetRaceStats(releasePath)
	if total != 1 || present != 1 {
		t.Fatalf("expected present=1 total=1, got present=%d total=%d", present, total)
	}
	if totalBytes != 1234 {
		t.Fatalf("expected totalBytes=1234, got %d", totalBytes)
	}
	if len(users) != 1 || users[0].Name != "steel" || users[0].Files != 1 {
		t.Fatalf("unexpected user stats: %+v", users)
	}
	if len(groups) != 1 || groups[0].Name != "GRP" || groups[0].Files != 1 {
		t.Fatalf("unexpected group stats: %+v", groups)
	}
	if users[0].Speed != 1234 {
		t.Fatalf("expected single-file pzs-ng user speed 1234 bytes/s, got %f", users[0].Speed)
	}
	if groups[0].Speed != 1234 {
		t.Fatalf("expected single-file pzs-ng group speed 1234 bytes/s, got %f", groups[0].Speed)
	}
}

func TestRaceDBGetRaceStatsUsesAggregateUserSpeeds(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "race.db")
	rdb, err := NewRaceDB(dbPath)
	if err != nil {
		t.Fatalf("NewRaceDB failed: %v", err)
	}
	defer rdb.Close()

	releasePath := "/site/MP3/Aggregate.Speed-GRP"
	if err := rdb.SaveSFV(releasePath, "release.sfv", map[string]uint32{
		"01-track.mp3": 1,
		"02-track.mp3": 2,
	}); err != nil {
		t.Fatalf("SaveSFV failed: %v", err)
	}
	if err := rdb.RecordUpload(releasePath+"/01-track.mp3", "steel", "GRP", 100, 1000, 1); err != nil {
		t.Fatalf("RecordUpload track 1 failed: %v", err)
	}
	if err := rdb.RecordUpload(releasePath+"/02-track.mp3", "steel", "GRP", 200, 4000, 2); err != nil {
		t.Fatalf("RecordUpload track 2 failed: %v", err)
	}

	users, groups, _, present, total := rdb.GetRaceStats(releasePath)
	if total != 2 || present != 2 {
		t.Fatalf("expected complete release, got present=%d total=%d", present, total)
	}
	if len(users) != 1 || len(groups) != 1 {
		t.Fatalf("unexpected stats shape users=%+v groups=%+v", users, groups)
	}
	if users[0].DurationMs != 5000 {
		t.Fatalf("expected summed duration 5000ms, got %d", users[0].DurationMs)
	}
	if users[0].Speed != 150 {
		t.Fatalf("expected aggregate user speed 150 bytes/s, got %f", users[0].Speed)
	}
	if users[0].PeakSpeed != 100 {
		t.Fatalf("expected peak user speed 100 bytes/s, got %f", users[0].PeakSpeed)
	}
	if users[0].SlowSpeed != 50 {
		t.Fatalf("expected slow user speed 50 bytes/s, got %f", users[0].SlowSpeed)
	}
	if groups[0].Speed != 150 {
		t.Fatalf("expected aggregate group speed 150 bytes/s, got %f", groups[0].Speed)
	}
}

func TestRaceDBGetRaceStatsIgnoresChecksumMismatches(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "race.db")
	rdb, err := NewRaceDB(dbPath)
	if err != nil {
		t.Fatalf("NewRaceDB failed: %v", err)
	}
	defer rdb.Close()

	releasePath := "/site/X265/Bad.Release-GRP"
	if err := rdb.SaveSFV(releasePath, "release.sfv", map[string]uint32{
		"good.r00": 1,
		"bad.r01":  2,
	}); err != nil {
		t.Fatalf("SaveSFV failed: %v", err)
	}
	if err := rdb.RecordUpload(releasePath+"/good.r00", "u1", "G1", 100, 1000, 1); err != nil {
		t.Fatalf("RecordUpload good failed: %v", err)
	}
	if err := rdb.RecordUpload(releasePath+"/bad.r01", "u1", "G1", 200, 1000, 999); err != nil {
		t.Fatalf("RecordUpload bad failed: %v", err)
	}

	users, groups, totalBytes, present, total := rdb.GetRaceStats(releasePath)
	if total != 2 || present != 1 {
		t.Fatalf("expected only checksum-valid file to count, got present=%d total=%d", present, total)
	}
	if totalBytes != 100 {
		t.Fatalf("expected only good file bytes to count, got %d", totalBytes)
	}
	if len(users) != 1 || users[0].Files != 1 {
		t.Fatalf("expected one valid user file, got %+v", users)
	}
	if len(groups) != 1 || groups[0].Files != 1 {
		t.Fatalf("expected one valid group file, got %+v", groups)
	}
}

func TestRaceDBHydrateVFSIncludesZipStyleUploadsWithoutExpectedManifest(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "race.db")
	rdb, err := NewRaceDB(dbPath)
	if err != nil {
		t.Fatalf("NewRaceDB failed: %v", err)
	}
	defer rdb.Close()

	releasePath := "/site/0DAY/Zip.Release-GRP"
	filePath := releasePath + "/part1.zip"
	if err := rdb.RecordUpload(filePath, "steel", "iND", 4096, 1500, 0x12345678); err != nil {
		t.Fatalf("RecordUpload failed: %v", err)
	}

	vfs := NewVirtualFileSystem()
	vfs.AddFile("/site", VFSFile{IsDir: true, Seen: true})
	vfs.AddFile("/site/0DAY", VFSFile{IsDir: true, Seen: true})
	vfs.AddFile(releasePath, VFSFile{IsDir: true, Seen: true})
	vfs.AddFile(filePath, VFSFile{
		Path:         filePath,
		Size:         1024,
		Seen:         true,
		Owner:        "WeaveFTPd",
		Group:        "root",
		LastModified: 1,
	})

	hydrated, err := rdb.HydrateVFS(vfs)
	if err != nil {
		t.Fatalf("HydrateVFS failed: %v", err)
	}
	if hydrated != 1 {
		t.Fatalf("expected 1 hydrated file, got %d", hydrated)
	}

	got := vfs.GetFile(filePath)
	if got == nil {
		t.Fatalf("expected hydrated file to remain present in VFS")
	}
	if got.Owner != "steel" || got.Group != "iND" {
		t.Fatalf("expected hydrated owner/group steel/iND, got %s/%s", got.Owner, got.Group)
	}
	if got.Size != 4096 {
		t.Fatalf("expected hydrated size 4096, got %d", got.Size)
	}
	if got.XferTime != 1500 {
		t.Fatalf("expected hydrated xfertime 1500, got %d", got.XferTime)
	}
	if got.Checksum != 0x12345678 {
		t.Fatalf("expected hydrated checksum 0x12345678, got %08X", got.Checksum)
	}
}
