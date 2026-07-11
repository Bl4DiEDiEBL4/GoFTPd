//go:build !windows

// Package sqlitedriver selects the SQLite database/sql driver per platform:
// mattn/go-sqlite3 (CGO, fastest) on unix, modernc.org/sqlite (pure Go, no
// mingw needed) on Windows. Open databases with sql.Open(sqlitedriver.Name, dsn).
// PRAGMAs must be executed as statements, not DSN parameters. DSN parameter
// syntax differs between the two drivers.
package sqlitedriver

import _ "github.com/mattn/go-sqlite3"

// Name is the registered database/sql driver name for this platform.
const Name = "sqlite3"
