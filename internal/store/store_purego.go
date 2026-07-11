//go:build !cgosqlite

package store

import _ "modernc.org/sqlite"

const (
	driverName = "sqlite"
	dsnParams  = "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
)
