package pretime

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"goftpd/internal/plugin"
)

func TestLookupSQLiteCustomFields(t *testing.T) {
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "releases.db")
	db := openSQLiteForTest(t, dbPath)
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE pretimes (rls TEXT PRIMARY KEY, unixts INTEGER)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO pretimes (rls, unixts) VALUES (?, ?)`, "Test.Release-GRP", 1714200000); err != nil {
		t.Fatalf("insert row: %v", err)
	}

	ts, err := lookupSQL("sqlite3", dbPath, "pretimes", "rls", "unixts", "Test.Release-GRP")
	if err != nil {
		t.Fatalf("lookupSQL: %v", err)
	}
	if ts != 1714200000 {
		t.Fatalf("expected 1714200000, got %d", ts)
	}
}

func TestSQLPlaceholderByDriver(t *testing.T) {
	if got := sqlPlaceholder("sqlite3", 1); got != "?" {
		t.Fatalf("expected sqlite placeholder ?, got %q", got)
	}
	if got := sqlPlaceholder("mysql", 1); got != "?" {
		t.Fatalf("expected mysql placeholder ?, got %q", got)
	}
	if got := sqlPlaceholder("postgres", 2); got != "$2" {
		t.Fatalf("expected postgres placeholder $2, got %q", got)
	}
}

func TestProcessJobEmitsOldPreTime(t *testing.T) {
	h := New()
	h.debug = true
	h.sqlite.Enabled = true
	h.sqlite.Path = filepath.Join(t.TempDir(), "releases.db")
	h.sqlite.Table = "releases"
	h.sqlite.ReleaseField = "release"
	h.sqlite.UnixTimeField = "timestamp_unix"
	db := openSQLiteForTest(t, h.sqlite.Path)
	if _, err := db.Exec(`CREATE TABLE releases (release TEXT PRIMARY KEY, timestamp_unix INTEGER)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	oldUnix := time.Now().Add(-2 * time.Hour).Unix()
	if _, err := db.Exec(`INSERT INTO releases (release, timestamp_unix) VALUES (?, ?)`, "Old.Release-GRP", oldUnix); err != nil {
		t.Fatalf("insert row: %v", err)
	}
	_ = db.Close()

	type emitted struct {
		evtType string
		path    string
		file    string
		section string
		data    map[string]string
	}
	var got emitted
	h.svc = &plugin.Services{
		EmitEvent: func(eventType, p, filename, section string, size int64, speed float64, data map[string]string) {
			got = emitted{evtType: eventType, path: p, file: filename, section: section, data: data}
		},
	}
	h.processJob(job{
		path:    "/0DAY/2026-04-27/Old.Release-GRP",
		relname: "Old.Release-GRP",
		section: "0DAY",
		user:    "N0pe",
		group:   "Admin",
	})

	if got.evtType != eventOldPreTime {
		t.Fatalf("expected %s, got %s", eventOldPreTime, got.evtType)
	}
	if got.data["provider"] != "sqlite" {
		t.Fatalf("expected sqlite provider, got %q", got.data["provider"])
	}
	if got.data["relname"] != "Old.Release-GRP" {
		t.Fatalf("unexpected relname %q", got.data["relname"])
	}
	if got.data["preage"] == "" {
		t.Fatalf("expected preage to be set")
	}
}

func TestIsReleaseDirSupportsDatedLayout(t *testing.T) {
	if !isReleaseDir("/0DAY/2026-04-27/Test.Release-GRP", "0DAY") {
		t.Fatalf("expected dated release dir to match")
	}
	h := New()
	if h.shouldHandle(&plugin.Event{
		Type:     plugin.EventMKDir,
		Path:     "/0DAY/2026-04-27/Sample",
		Filename: "Sample",
		Section:  "0DAY",
	}) {
		t.Fatalf("did not expect helper subdir to be handled as a release root")
	}
}

func openSQLiteForTest(t *testing.T, dbPath string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.Ping(); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "requires cgo") || strings.Contains(strings.ToLower(err.Error()), "this is a stub") {
			t.Skipf("sqlite3 driver unavailable in this test environment: %v", err)
		}
		t.Fatalf("ping sqlite: %v", err)
	}
	return db
}
