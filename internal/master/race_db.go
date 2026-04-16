package master

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"

	_ "github.com/mattn/go-sqlite3"
	"goftpd/internal/core"
)

type RaceDB struct {
	db *sql.DB
}

func NewRaceDB(dbPath string) (*RaceDB, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		return nil, fmt.Errorf("create race db dir: %w", err)
	}

	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open race db: %w", err)
	}

	pragmas := []string{
		"PRAGMA foreign_keys = ON;",
		"PRAGMA journal_mode = WAL;",
		"PRAGMA synchronous = NORMAL;",
		"PRAGMA busy_timeout = 5000;",
	}
	for _, stmt := range pragmas {
		if _, err := db.Exec(stmt); err != nil {
			db.Close()
			return nil, fmt.Errorf("init pragma %q: %w", stmt, err)
		}
	}

	schema := `
CREATE TABLE IF NOT EXISTS releases (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    path TEXT NOT NULL UNIQUE,
    sfv_name TEXT NOT NULL DEFAULT '',
    total_expected INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL DEFAULT (strftime('%s','now')),
    updated_at INTEGER NOT NULL DEFAULT (strftime('%s','now'))
);

CREATE TABLE IF NOT EXISTS release_files (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    release_id INTEGER NOT NULL,
    filename TEXT NOT NULL,
    uploader TEXT NOT NULL DEFAULT '',
    grp TEXT NOT NULL DEFAULT '',
    size_bytes INTEGER NOT NULL DEFAULT 0,
    duration_ms INTEGER NOT NULL DEFAULT 0,
    checksum INTEGER NOT NULL DEFAULT 0,
    expected_crc32 INTEGER NOT NULL DEFAULT 0,
    is_expected INTEGER NOT NULL DEFAULT 0,
    is_present INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL DEFAULT (strftime('%s','now')),
    updated_at INTEGER NOT NULL DEFAULT (strftime('%s','now')),
    UNIQUE(release_id, filename),
    FOREIGN KEY(release_id) REFERENCES releases(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_release_files_release_id ON release_files(release_id);
CREATE INDEX IF NOT EXISTS idx_releases_path ON releases(path);
`
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("init race db schema: %w", err)
	}

	return &RaceDB{db: db}, nil
}

func (r *RaceDB) Close() error {
	if r == nil || r.db == nil {
		return nil
	}
	return r.db.Close()
}

func (r *RaceDB) getOrCreateReleaseID(dirPath string) (int64, error) {
	tx, err := r.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`
        INSERT INTO releases(path, created_at, updated_at)
        VALUES (?, strftime('%s','now'), strftime('%s','now'))
        ON CONFLICT(path) DO UPDATE SET updated_at = strftime('%s','now')
    `, dirPath); err != nil {
		return 0, err
	}

	var releaseID int64
	if err := tx.QueryRow(`SELECT id FROM releases WHERE path = ?`, dirPath).Scan(&releaseID); err != nil {
		return 0, err
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return releaseID, nil
}

func (r *RaceDB) SaveSFV(dirPath, sfvName string, entries map[string]uint32) error {
	releaseID, err := r.getOrCreateReleaseID(dirPath)
	if err != nil {
		return err
	}

	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`
        UPDATE releases
        SET sfv_name = ?, total_expected = ?, updated_at = strftime('%s','now')
        WHERE id = ?
    `, sfvName, len(entries), releaseID); err != nil {
		return err
	}

	for fileName, crc := range entries {
		if _, err := tx.Exec(`
            INSERT INTO release_files(
                release_id, filename, expected_crc32, is_expected, updated_at
            ) VALUES (?, ?, ?, 1, strftime('%s','now'))
            ON CONFLICT(release_id, filename) DO UPDATE SET
                expected_crc32 = excluded.expected_crc32,
                is_expected = 1,
                updated_at = strftime('%s','now')
        `, releaseID, fileName, int64(crc)); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (r *RaceDB) RecordUpload(filePath, owner, group string, size int64, durationMs int64, checksum uint32) error {
	dirPath := filepath.Dir(filePath)
	fileName := filepath.Base(filePath)

	releaseID, err := r.getOrCreateReleaseID(dirPath)
	if err != nil {
		return err
	}

	_, err = r.db.Exec(`
        INSERT INTO release_files(
            release_id, filename, uploader, grp, size_bytes, duration_ms,
            checksum, is_present, updated_at
        ) VALUES (?, ?, ?, ?, ?, ?, ?, 1, strftime('%s','now'))
        ON CONFLICT(release_id, filename) DO UPDATE SET
            uploader = excluded.uploader,
            grp = excluded.grp,
            size_bytes = excluded.size_bytes,
            duration_ms = excluded.duration_ms,
            checksum = excluded.checksum,
            is_present = 1,
            updated_at = strftime('%s','now')
    `, releaseID, fileName, owner, group, size, durationMs, int64(checksum))
	return err
}

func (r *RaceDB) DeletePath(path string, isDir bool) error {
	if isDir {
		_, err := r.db.Exec(`DELETE FROM releases WHERE path = ?`, path)
		return err
	}

	dirPath := filepath.Dir(path)
	fileName := filepath.Base(path)
	_, err := r.db.Exec(`
        UPDATE release_files
        SET is_present = 0,
            size_bytes = 0,
            duration_ms = 0,
            checksum = 0,
            updated_at = strftime('%s','now')
        WHERE release_id = (SELECT id FROM releases WHERE path = ?)
          AND filename = ?
    `, dirPath, fileName)
	return err
}

func (r *RaceDB) RenamePath(from, to string, isDir bool) error {
	if isDir {
		tx, err := r.db.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()

		rows, err := tx.Query(`SELECT path FROM releases WHERE path = ? OR path LIKE ?`, from, from+"/%")
		if err != nil {
			return err
		}
		defer rows.Close()

		var paths []string
		for rows.Next() {
			var p string
			if err := rows.Scan(&p); err != nil {
				return err
			}
			paths = append(paths, p)
		}
		sort.Slice(paths, func(i, j int) bool { return len(paths[i]) > len(paths[j]) })

		for _, oldPath := range paths {
			newPath := to + oldPath[len(from):]
			if _, err := tx.Exec(`UPDATE releases SET path = ?, updated_at = strftime('%s','now') WHERE path = ?`, newPath, oldPath); err != nil {
				return err
			}
		}
		return tx.Commit()
	}

	oldDir, oldName := filepath.Dir(from), filepath.Base(from)
	newDir, newName := filepath.Dir(to), filepath.Base(to)

	oldReleaseID, err := r.getOrCreateReleaseID(oldDir)
	if err != nil {
		return err
	}
	newReleaseID, err := r.getOrCreateReleaseID(newDir)
	if err != nil {
		return err
	}

	_, err = r.db.Exec(`
        UPDATE release_files
        SET release_id = ?, filename = ?, updated_at = strftime('%s','now')
        WHERE release_id = ? AND filename = ?
    `, newReleaseID, newName, oldReleaseID, oldName)
	return err
}

func (r *RaceDB) Reconcile(vfs *VirtualFileSystem) error {
	rows, err := r.db.Query(`SELECT path FROM releases`)
	if err != nil {
		return err
	}
	defer rows.Close()

	var stale []string
	for rows.Next() {
		var dirPath string
		if err := rows.Scan(&dirPath); err != nil {
			return err
		}
		f := vfs.GetFile(dirPath)
		if f == nil || !f.IsDir {
			stale = append(stale, dirPath)
		}
	}

	for _, dirPath := range stale {
		if _, err := r.db.Exec(`DELETE FROM releases WHERE path = ?`, dirPath); err != nil {
			log.Printf("[RaceDB] Failed to purge stale release %s: %v", dirPath, err)
		} else {
			log.Printf("[RaceDB] Purged stale release %s", dirPath)
		}
	}
	return nil
}

func (r *RaceDB) GetRaceStats(dirPath string) ([]core.VFSRaceUser, []core.VFSRaceGroup, int64, int, int) {
	var total int
	if err := r.db.QueryRow(`SELECT total_expected FROM releases WHERE path = ?`, dirPath).Scan(&total); err != nil && err != sql.ErrNoRows {
		log.Printf("[RaceDB] total_expected query failed for %s: %v", dirPath, err)
		return nil, nil, 0, 0, 0
	}

	var present int
	var totalBytes int64
	if err := r.db.QueryRow(`
        SELECT COALESCE(COUNT(*), 0), COALESCE(SUM(size_bytes), 0)
        FROM release_files
        WHERE release_id = (SELECT id FROM releases WHERE path = ?)
          AND is_present = 1
          AND is_expected = 1
    `, dirPath).Scan(&present, &totalBytes); err != nil {
		log.Printf("[RaceDB] present query failed for %s: %v", dirPath, err)
		return nil, nil, 0, 0, 0
	}

	userRows, err := r.db.Query(`
        SELECT uploader, grp, COUNT(*), COALESCE(SUM(size_bytes),0), COALESCE(SUM(duration_ms),0)
        FROM release_files
        WHERE release_id = (SELECT id FROM releases WHERE path = ?)
          AND is_present = 1
          AND is_expected = 1
        GROUP BY uploader, grp
        ORDER BY COALESCE(SUM(size_bytes),0) DESC, COUNT(*) DESC, uploader ASC
    `, dirPath)
	if err != nil {
		log.Printf("[RaceDB] user stats query failed for %s: %v", dirPath, err)
		return nil, nil, 0, 0, 0
	}
	defer userRows.Close()

	var users []core.VFSRaceUser
	for userRows.Next() {
		var u core.VFSRaceUser
		var durationMs int64
		if err := userRows.Scan(&u.Name, &u.Group, &u.Files, &u.Bytes, &durationMs); err != nil {
			log.Printf("[RaceDB] user row scan failed for %s: %v", dirPath, err)
			return nil, nil, 0, 0, 0
		}
		if durationMs > 0 {
			u.Speed = float64(u.Bytes) / (float64(durationMs) / 1000.0)
		}
		if total > 0 {
			u.Percent = (u.Files * 100) / total
			if u.Percent > 100 {
				u.Percent = 100
			}
		}
		users = append(users, u)
	}

	groupRows, err := r.db.Query(`
        SELECT grp, COUNT(*), COALESCE(SUM(size_bytes),0), COALESCE(SUM(duration_ms),0)
        FROM release_files
        WHERE release_id = (SELECT id FROM releases WHERE path = ?)
          AND is_present = 1
          AND is_expected = 1
        GROUP BY grp
        ORDER BY COALESCE(SUM(size_bytes),0) DESC, COUNT(*) DESC, grp ASC
    `, dirPath)
	if err != nil {
		log.Printf("[RaceDB] group stats query failed for %s: %v", dirPath, err)
		return nil, nil, 0, 0, 0
	}
	defer groupRows.Close()

	var groups []core.VFSRaceGroup
	for groupRows.Next() {
		var g core.VFSRaceGroup
		var durationMs int64
		if err := groupRows.Scan(&g.Name, &g.Files, &g.Bytes, &durationMs); err != nil {
			log.Printf("[RaceDB] group row scan failed for %s: %v", dirPath, err)
			return nil, nil, 0, 0, 0
		}
		if durationMs > 0 {
			g.Speed = float64(g.Bytes) / (float64(durationMs) / 1000.0)
		}
		if total > 0 {
			g.Percent = (g.Files * 100) / total
			if g.Percent > 100 {
				g.Percent = 100
			}
		}
		groups = append(groups, g)
	}

	if present > total {
		present = total
	}

	return users, groups, totalBytes, present, total
}