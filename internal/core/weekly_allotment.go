package core

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"weaveftpd/internal/sqlitedriver"
	"weaveftpd/internal/user"
)

var weeklyAllotmentSchema = `
CREATE TABLE IF NOT EXISTS weekly_allotment (
	username TEXT PRIMARY KEY,
	week_key TEXT NOT NULL,
	applied_at INTEGER NOT NULL
);
`

func weeklyAllotmentDBPath() string {
	return filepath.Join("userdata", "weekly_allotment.db")
}

func getWeeklyAllotmentDB() (*sql.DB, error) {
	if err := os.MkdirAll("userdata", 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open(sqlitedriver.Name, weeklyAllotmentDBPath())
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(weeklyAllotmentSchema); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func currentISOWeekKey(now time.Time) string {
	year, week := now.ISOWeek()
	return fmt.Sprintf("%04d-%02d", year, week)
}

func applyWeeklyAllotmentIfDue(u *user.User, now time.Time) error {
	if u == nil || u.WeeklyAllotment <= 0 {
		return nil
	}
	db, err := getWeeklyAllotmentDB()
	if err != nil {
		return err
	}
	defer db.Close()

	weekKey := currentISOWeekKey(now)
	var existing string
	err = db.QueryRow(`SELECT week_key FROM weekly_allotment WHERE username = ?`, u.Name).Scan(&existing)
	if err == nil && existing == weekKey {
		return nil
	}
	if err != nil && err != sql.ErrNoRows {
		return err
	}

	updated, err := user.MutateAndSave(u.Name, nil, func(current *user.User) error {
		current.Credits = current.WeeklyAllotment
		return nil
	})
	if err != nil {
		return err
	}
	if updated != nil {
		u.Credits = updated.Credits
		u.WeeklyAllotment = updated.WeeklyAllotment
	}
	_, err = db.Exec(`
INSERT INTO weekly_allotment(username, week_key, applied_at)
VALUES(?, ?, ?)
ON CONFLICT(username) DO UPDATE SET
	week_key = excluded.week_key,
	applied_at = excluded.applied_at
`, u.Name, weekKey, now.Unix())
	return err
}
