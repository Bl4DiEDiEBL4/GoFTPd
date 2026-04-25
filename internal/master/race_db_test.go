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
