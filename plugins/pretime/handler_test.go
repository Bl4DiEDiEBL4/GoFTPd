package pretime

import (
	"database/sql"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"weaveftpd/internal/plugin"
	"weaveftpd/internal/sqlitedriver"
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

	ts, err := lookupSQL(sqlitedriver.Name, dbPath, "pretimes", "rls", "unixts", "Test.Release-GRP")
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

func TestProcessJobPreservesOriginalSection(t *testing.T) {
	h := New()
	h.sqlite.Enabled = true
	h.sqlite.Path = filepath.Join(t.TempDir(), "releases.db")
	db := openSQLiteForTest(t, h.sqlite.Path)
	if _, err := db.Exec(`CREATE TABLE releases (release TEXT PRIMARY KEY, timestamp_unix INTEGER)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO releases (release, timestamp_unix) VALUES (?, ?)`, "Es.Welcome.To.Derry.S01E01.German.DL.2160p.UHD.BluRay.HEVC-AIDA", time.Now().Add(-10*time.Second).Unix()); err != nil {
		t.Fatalf("insert row: %v", err)
	}
	_ = db.Close()

	gotSection := ""
	h.svc = &plugin.Services{
		EmitEvent: func(eventType, p, filename, section string, size int64, speed float64, data map[string]string) {
			gotSection = section
		},
	}

	h.processJob(job{
		path:    "/FOREIGN/X265-FOREIGN/2026-04-29/Es.Welcome.To.Derry.S01E01.German.DL.2160p.UHD.BluRay.HEVC-AIDA",
		relname: "Es.Welcome.To.Derry.S01E01.German.DL.2160p.UHD.BluRay.HEVC-AIDA",
		section: "TV-DE",
	})

	if gotSection != "TV-DE" {
		t.Fatalf("expected original section TV-DE, got %q", gotSection)
	}
}

func TestProcessJobCalculatesAgeAtMkdirTime(t *testing.T) {
	h := New()
	h.sqlite.Enabled = true
	h.sqlite.Path = filepath.Join(t.TempDir(), "releases.db")
	db := openSQLiteForTest(t, h.sqlite.Path)
	if _, err := db.Exec(`CREATE TABLE releases (release TEXT PRIMARY KEY, timestamp_unix INTEGER)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	preAt := time.Now().Add(-1 * time.Hour).Truncate(time.Second)
	mkdirAt := preAt.Add(5 * time.Second)
	if _, err := db.Exec(`INSERT INTO releases (release, timestamp_unix) VALUES (?, ?)`, "Fresh.Release-GRP", preAt.Unix()); err != nil {
		t.Fatalf("insert row: %v", err)
	}
	_ = db.Close()

	var got map[string]string
	h.svc = &plugin.Services{
		EmitEvent: func(eventType, p, filename, section string, size int64, speed float64, data map[string]string) {
			got = data
		},
	}

	h.processJob(job{
		path:      "/TV-720P/Fresh.Release-GRP",
		relname:   "Fresh.Release-GRP",
		section:   "TV-720P",
		createdAt: mkdirAt,
	})

	if got == nil {
		t.Fatalf("expected pretime event")
	}
	if got["preage_seconds"] != "5" {
		t.Fatalf("expected age at mkdir time to be 5 seconds, got %q", got["preage_seconds"])
	}
	if got["mkdir_unix"] != strconv.FormatInt(mkdirAt.Unix(), 10) {
		t.Fatalf("expected mkdir_unix %d, got %q", mkdirAt.Unix(), got["mkdir_unix"])
	}
}

func TestAPIResultMatchesReleaseRequiresExactNameWhenPresent(t *testing.T) {
	payload := map[string]interface{}{
		"data": map[string]interface{}{
			"rows": []interface{}{
				map[string]interface{}{"name": "Other.Release-GRP", "preAt": float64(1714200000)},
			},
		},
	}

	if apiResultMatchesRelease(payload, apiProvider{NamePath: "data.rows.0.name"}, "Wanted.Release-GRP") {
		t.Fatalf("expected fuzzy API result to be rejected")
	}
}

func TestAPIResultMatchesReleaseAllowsExactName(t *testing.T) {
	payload := map[string]interface{}{
		"data": map[string]interface{}{
			"rows": []interface{}{
				map[string]interface{}{"name": "Wanted.Release-GRP", "preAt": float64(1714200000)},
			},
		},
	}

	if !apiResultMatchesRelease(payload, apiProvider{NamePath: "data.rows.0.name"}, "Wanted.Release-GRP") {
		t.Fatalf("expected exact API result to be accepted")
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
	db, err := sql.Open(sqlitedriver.Name, dbPath)
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
