package master

import (
	"path/filepath"
	"testing"
)

func TestRaceDBReconcileRestoresMediaInfo(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "race.db")

	db, err := NewRaceDB(dbPath)
	if err != nil {
		t.Fatalf("NewRaceDB() error = %v", err)
	}

	dirPath := "/MP3/0424/Test.Release-2026-GRP"
	fields := map[string]string{
		"genre": "Synthpop",
		"year":  "2026",
	}

	if err := db.SaveMediaInfo(dirPath, fields); err != nil {
		t.Fatalf("SaveMediaInfo() error = %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	db, err = NewRaceDB(dbPath)
	if err != nil {
		t.Fatalf("NewRaceDB() reopen error = %v", err)
	}
	defer db.Close()

	vfs := NewVirtualFileSystem()
	vfs.AddFile(dirPath, VFSFile{Path: dirPath, IsDir: true, Seen: true})

	if err := db.Reconcile(vfs); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	got := vfs.GetMediaInfo(dirPath)
	if got["genre"] != "Synthpop" {
		t.Fatalf("genre after reconcile = %q, want %q", got["genre"], "Synthpop")
	}
	if got["year"] != "2026" {
		t.Fatalf("year after reconcile = %q, want %q", got["year"], "2026")
	}
}
