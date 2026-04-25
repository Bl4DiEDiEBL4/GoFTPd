package master

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

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

CREATE TABLE IF NOT EXISTS release_media (
    release_id INTEGER NOT NULL,
    field_key TEXT NOT NULL,
    field_value TEXT NOT NULL DEFAULT '',
    PRIMARY KEY(release_id, field_key),
    FOREIGN KEY(release_id) REFERENCES releases(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_release_media_release_id ON release_media(release_id);
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
		fileName = raceDBFileKey(fileName)
		if fileName == "" {
			continue
		}
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

func (r *RaceDB) SaveMediaInfo(dirPath string, fields map[string]string) error {
	releaseID, err := r.getOrCreateReleaseID(dirPath)
	if err != nil {
		return err
	}

	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM release_media WHERE release_id = ?`, releaseID); err != nil {
		return err
	}

	for key, value := range fields {
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" || value == "" {
			continue
		}
		if _, err := tx.Exec(`
            INSERT INTO release_media(release_id, field_key, field_value)
            VALUES (?, ?, ?)
        `, releaseID, key, value); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (r *RaceDB) RecordUpload(filePath, owner, group string, size int64, durationMs int64, checksum uint32) error {
	dirPath := filepath.Dir(filePath)
	fileName := raceDBFileKey(filepath.Base(filePath))
	if fileName == "" {
		return nil
	}

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
	fileName := raceDBFileKey(filepath.Base(path))
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
	oldName = raceDBFileKey(oldName)
	newName = raceDBFileKey(newName)

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

	sfvRows, err := r.db.Query(`
        SELECT rel.path, rel.sfv_name, rf.filename, rf.expected_crc32
        FROM releases rel
        JOIN release_files rf ON rf.release_id = rel.id
        WHERE rel.sfv_name <> ''
          AND rf.is_expected = 1
        ORDER BY rel.path, rf.filename
    `)
	if err != nil {
		return err
	}
	defer sfvRows.Close()

	sfvNameByPath := make(map[string]string)
	sfvByPath := make(map[string]map[string]uint32)
	for sfvRows.Next() {
		var dirPath, sfvName, fileName string
		var expectedCRC int64
		if err := sfvRows.Scan(&dirPath, &sfvName, &fileName, &expectedCRC); err != nil {
			return err
		}
		if sfvByPath[dirPath] == nil {
			sfvByPath[dirPath] = make(map[string]uint32)
		}
		sfvNameByPath[dirPath] = sfvName
		fileName = raceDBFileKey(fileName)
		if fileName != "" {
			sfvByPath[dirPath][fileName] = uint32(expectedCRC)
		}
	}
	if err := sfvRows.Err(); err != nil {
		return err
	}
	for dirPath, entries := range sfvByPath {
		if len(entries) > 0 {
			vfs.SetSFVData(dirPath, sfvNameByPath[dirPath], entries)
		}
	}

	mediaRows, err := r.db.Query(`
        SELECT rel.path, rm.field_key, rm.field_value
        FROM releases rel
        JOIN release_media rm ON rm.release_id = rel.id
        ORDER BY rel.path, rm.field_key
    `)
	if err != nil {
		return err
	}
	defer mediaRows.Close()

	mediaByPath := make(map[string]map[string]string)
	for mediaRows.Next() {
		var dirPath, key, value string
		if err := mediaRows.Scan(&dirPath, &key, &value); err != nil {
			return err
		}
		if mediaByPath[dirPath] == nil {
			mediaByPath[dirPath] = make(map[string]string)
		}
		mediaByPath[dirPath][key] = value
	}
	if err := mediaRows.Err(); err != nil {
		return err
	}
	for dirPath, fields := range mediaByPath {
		if len(fields) > 0 {
			vfs.SetMediaInfo(dirPath, fields)
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
        FROM (
            SELECT LOWER(e.filename) AS filename_key, MAX(p.size_bytes) AS size_bytes
            FROM release_files e
            JOIN release_files p
              ON p.release_id = e.release_id
             AND p.is_present = 1
             AND LOWER(p.filename) = LOWER(e.filename)
            WHERE e.release_id = (SELECT id FROM releases WHERE path = ?)
              AND e.is_expected = 1
            GROUP BY LOWER(e.filename)
        )
    `, dirPath).Scan(&present, &totalBytes); err != nil {
		log.Printf("[RaceDB] present query failed for %s: %v", dirPath, err)
		return nil, nil, 0, 0, 0
	}

	userRows, err := r.db.Query(`
        SELECT uploader, grp, COUNT(*), COALESCE(SUM(size_bytes),0), COALESCE(SUM(duration_ms),0)
        FROM release_files p
        WHERE p.release_id = (SELECT id FROM releases WHERE path = ?)
          AND p.is_present = 1
          AND EXISTS (
              SELECT 1 FROM release_files e
              WHERE e.release_id = p.release_id
                AND e.is_expected = 1
                AND LOWER(e.filename) = LOWER(p.filename)
          )
        GROUP BY uploader, grp
        ORDER BY COALESCE(SUM(size_bytes),0) DESC, COUNT(*) DESC, uploader ASC
    `, dirPath)
	if err != nil {
		log.Printf("[RaceDB] user stats query failed for %s: %v", dirPath, err)
		return nil, nil, 0, 0, 0
	}
	defer userRows.Close()

	var users []core.VFSRaceUser
	var userDurations []int64
	for userRows.Next() {
		var u core.VFSRaceUser
		var durationMs int64
		if err := userRows.Scan(&u.Name, &u.Group, &u.Files, &u.Bytes, &durationMs); err != nil {
			log.Printf("[RaceDB] user row scan failed for %s: %v", dirPath, err)
			return nil, nil, 0, 0, 0
		}
		users = append(users, u)
		userDurations = append(userDurations, durationMs)
	}
	if err := userRows.Err(); err != nil {
		log.Printf("[RaceDB] user rows iteration failed for %s: %v", dirPath, err)
		return nil, nil, 0, 0, 0
	}
	_ = userRows.Close()

	for i := range users {
		u := &users[i]
		durationMs := userDurations[i]
		u.DurationMs = durationMs
		if durationMs > 0 {
			u.Speed = float64(u.Bytes) / (float64(durationMs) / 1000.0)
		}
		var peakBytes, peakMs sql.NullInt64
		err := r.db.QueryRow(`
            SELECT size_bytes, duration_ms FROM release_files
            WHERE release_id = (SELECT id FROM releases WHERE path = ?)
              AND uploader = ?
              AND is_present = 1
              AND EXISTS (
                  SELECT 1 FROM release_files e
                  WHERE e.release_id = release_files.release_id
                    AND e.is_expected = 1
                    AND LOWER(e.filename) = LOWER(release_files.filename)
              )
              AND duration_ms > 0
            ORDER BY (CAST(size_bytes AS REAL) / CAST(duration_ms AS REAL)) DESC
            LIMIT 1
        `, dirPath, u.Name).Scan(&peakBytes, &peakMs)
		if err != nil && err != sql.ErrNoRows {
			log.Printf("[RaceDB] peak query failed for %s user=%s: %v", dirPath, u.Name, err)
		}
		if peakBytes.Valid && peakMs.Valid && peakMs.Int64 > 0 {
			u.PeakSpeed = float64(peakBytes.Int64) / (float64(peakMs.Int64) / 1000.0)
		}
		var slowBytes, slowMs sql.NullInt64
		err = r.db.QueryRow(`
            SELECT size_bytes, duration_ms FROM release_files
            WHERE release_id = (SELECT id FROM releases WHERE path = ?)
              AND uploader = ?
              AND is_present = 1
              AND EXISTS (
                  SELECT 1 FROM release_files e
                  WHERE e.release_id = release_files.release_id
                    AND e.is_expected = 1
                    AND LOWER(e.filename) = LOWER(release_files.filename)
              )
              AND duration_ms > 0
            ORDER BY (CAST(size_bytes AS REAL) / CAST(duration_ms AS REAL)) ASC
            LIMIT 1
        `, dirPath, u.Name).Scan(&slowBytes, &slowMs)
		if err != nil && err != sql.ErrNoRows {
			log.Printf("[RaceDB] slowest query failed for %s user=%s: %v", dirPath, u.Name, err)
		}
		if slowBytes.Valid && slowMs.Valid && slowMs.Int64 > 0 {
			u.SlowSpeed = float64(slowBytes.Int64) / (float64(slowMs.Int64) / 1000.0)
		}
		if total > 0 {
			u.Percent = (u.Files * 100) / total
			if u.Percent > 100 {
				u.Percent = 100
			}
		}
	}

	groupRows, err := r.db.Query(`
        SELECT grp, COUNT(*), COALESCE(SUM(size_bytes),0), COALESCE(SUM(duration_ms),0)
        FROM release_files p
        WHERE p.release_id = (SELECT id FROM releases WHERE path = ?)
          AND p.is_present = 1
          AND EXISTS (
              SELECT 1 FROM release_files e
              WHERE e.release_id = p.release_id
                AND e.is_expected = 1
                AND LOWER(e.filename) = LOWER(p.filename)
          )
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

func raceDBFileKey(name string) string {
	name = strings.TrimSpace(filepath.ToSlash(name))
	name = strings.TrimPrefix(name, "./")
	return strings.ToLower(name)
}

// GetRaceWallClockMilliseconds returns the wall-clock duration of a release
// race in milliseconds. SQLite timestamps here are second-granular, so use each
// file's stored duration_ms to reconstruct the earliest transfer start. This
// keeps very fast races from being rounded up to 1s.
func (r *RaceDB) GetRaceWallClockMilliseconds(dirPath string) int64 {
	if r == nil || r.db == nil {
		return 0
	}
	var startMs, endMs sql.NullInt64
	err := r.db.QueryRow(`
        SELECT
            COALESCE(MIN(CASE
                WHEN f.duration_ms > 0 THEN (f.updated_at * 1000) - f.duration_ms
                ELSE rel.created_at * 1000
            END), rel.created_at * 1000),
            COALESCE(MAX(f.updated_at * 1000), rel.created_at * 1000)
        FROM releases rel
        LEFT JOIN release_files f ON f.release_id = rel.id AND f.is_present = 1
        WHERE rel.path = ?
    `, dirPath).Scan(&startMs, &endMs)
	if err != nil || !startMs.Valid || !endMs.Valid {
		return 0
	}
	d := endMs.Int64 - startMs.Int64
	if d < 1 {
		d = 1
	}
	return d
}

func (r *RaceDB) SearchDirs(query string, limit int) []core.VFSSearchResult {
	if r == nil || r.db == nil {
		return nil
	}

	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return nil
	}
	if limit <= 0 {
		limit = 100
	}

	rows, err := r.db.Query(`
        SELECT
            rel.path,
            COALESCE(SUM(CASE WHEN rf.is_present = 1 THEN 1 ELSE 0 END), 0) AS present_files,
            COALESCE(SUM(CASE WHEN rf.is_present = 1 THEN rf.size_bytes ELSE 0 END), 0) AS present_bytes,
            MAX(
                COALESCE(
                    CASE WHEN rf.updated_at > 0 THEN rf.updated_at END,
                    rel.updated_at,
                    rel.created_at
                )
            ) AS mod_time
        FROM releases rel
        LEFT JOIN release_files rf ON rf.release_id = rel.id
        WHERE LOWER(rel.path) LIKE ?
        GROUP BY rel.id, rel.path
        ORDER BY LOWER(rel.path) ASC
        LIMIT ?
    `, "%"+query+"%", limit)
	if err != nil {
		log.Printf("[RaceDB] search query failed for %q: %v", query, err)
		return nil
	}
	defer rows.Close()

	results := make([]core.VFSSearchResult, 0, limit)
	for rows.Next() {
		var res core.VFSSearchResult
		if err := rows.Scan(&res.Path, &res.Files, &res.Bytes, &res.ModTime); err != nil {
			log.Printf("[RaceDB] search row scan failed for %q: %v", query, err)
			return nil
		}
		results = append(results, res)
	}
	if err := rows.Err(); err != nil {
		log.Printf("[RaceDB] search rows iteration failed for %q: %v", query, err)
		return nil
	}
	return results
}
