package dupe

import (
	"database/sql"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// DupeChecker tracks duplicate releases
type DupeChecker struct {
	db    *sql.DB
	mu    sync.RWMutex
	debug bool
}

// NewDupeChecker creates a new dupe checker with SQLite database
func NewDupeChecker(dbPath string, debug bool) (*DupeChecker, error) {
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open dupe db: %v", err)
	}

	// Test connection
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("failed to ping dupe db: %v", err)
	}

	d := &DupeChecker{
		db:    db,
		debug: debug,
	}

	// Initialize schema
	if err := d.initSchema(); err != nil {
		return nil, err
	}

	return d, nil
}

// initSchema creates the dupe table if it doesn't exist
func (d *DupeChecker) initSchema() error {
	schema := `
	CREATE TABLE IF NOT EXISTS dupes (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		release_name TEXT NOT NULL UNIQUE,
		group_name TEXT NOT NULL,
		uploader TEXT,
		upload_time INTEGER,
		file_count INTEGER,
		total_bytes INTEGER
	);

	CREATE INDEX IF NOT EXISTS idx_release ON dupes(release_name);
	CREATE INDEX IF NOT EXISTS idx_group ON dupes(group_name);
	`

	for _, stmt := range strings.Split(schema, ";") {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		if _, err := d.db.Exec(stmt); err != nil {
			return fmt.Errorf("schema error: %v", err)
		}
	}

	return nil
}

// IsDupe checks if a release name is already uploaded
func (d *DupeChecker) IsDupe(releaseName string) (bool, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	var count int
	err := d.db.QueryRow("SELECT COUNT(*) FROM dupes WHERE release_name = ?", releaseName).Scan(&count)
	if err != nil && err != sql.ErrNoRows {
		return false, err
	}

	return count > 0, nil
}

// AddDupe records a new release in the dupe database
func (d *DupeChecker) AddDupe(releaseName, groupName, uploader string, fileCount int, totalBytes int64) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	_, err := d.db.Exec(
		"INSERT INTO dupes (release_name, group_name, uploader, upload_time, file_count, total_bytes) VALUES (?, ?, ?, ?, ?, ?)",
		releaseName, groupName, uploader, time.Now().Unix(), fileCount, totalBytes,
	)

	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint") {
			return fmt.Errorf("release already exists")
		}
		return err
	}

	if d.debug {
		log.Printf("[DUPE] Added: %s by %s/%s", releaseName, uploader, groupName)
	}

	return nil
}

// GetDupeInfo returns details about a dupe release
func (d *DupeChecker) GetDupeInfo(releaseName string) (groupName, uploader string, err error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	err = d.db.QueryRow(
		"SELECT group_name, uploader FROM dupes WHERE release_name = ?",
		releaseName,
	).Scan(&groupName, &uploader)

	if err == sql.ErrNoRows {
		return "", "", nil
	}

	return groupName, uploader, err
}

// Clear removes a release from dupes (for undupe/reset)
func (d *DupeChecker) Clear(releaseName string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	res, err := d.db.Exec("DELETE FROM dupes WHERE release_name = ?", releaseName)
	if err != nil {
		return err
	}
	if rows, err := res.RowsAffected(); err == nil && rows == 0 {
		return sql.ErrNoRows
	}

	if d.debug {
		log.Printf("[DUPE] Cleared: %s", releaseName)
	}

	return nil
}

// Close closes the database
func (d *DupeChecker) Close() error {
	return d.db.Close()
}
