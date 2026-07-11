//go:build cgosqlite

package store

import _ "github.com/mattn/go-sqlite3"

const (
	driverName = "sqlite3"
	dsnParams  = "?_journal_mode=WAL&_busy_timeout=5000"
)
