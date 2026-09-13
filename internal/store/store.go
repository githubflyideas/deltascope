package store

import (
	"database/sql"
	"fmt"
)

type Store struct{ db *sql.DB }

func Open(path string) (*Store, error) {
	db, err := sql.Open(driverName, path+dsnParams)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	// No schema of its own any more: accounts live in the -user flag and the
	// only tables in this file belong to internal/state, which creates them.
	// Open still exists because that package reuses this connection.
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to open the database: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// DropLegacyUsers removes the users table that 3.7.8 and earlier kept password
// hashes in, and reports whether there was one.
//
// Accounts are now declared on the command line and never written down, so that
// table is not merely unused: it is a credential at rest in a file nobody
// reviews any more, on a host where the operator has been told there is no
// stored password. Deleting the rows would leave the bytes sitting in a free
// page, so this vacuums afterwards -- once, only on the upgrade run, because
// the table is gone the next time round.
//
// Failure is not fatal to the caller: monitoring data is unaffected either way,
// and a database opened read-only or on a full disk should still serve.
func (s *Store) DropLegacyUsers() (bool, error) {
	var name string
	err := s.db.QueryRow(
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'users'`).Scan(&name)
	if err != nil {
		// sql.ErrNoRows is the normal case: no legacy table, nothing to do.
		return false, nil
	}
	if _, err := s.db.Exec(`DROP TABLE users`); err != nil {
		return false, fmt.Errorf("failed to drop the legacy users table: %w", err)
	}
	if _, err := s.db.Exec(`VACUUM`); err != nil {
		return true, fmt.Errorf("dropped the legacy users table but could not VACUUM, "+
			"so the old password hashes may remain in free pages of the database file: %w", err)
	}
	return true, nil
}

// DB exposes the underlying connection, so the state-snapshot store can reuse the same database file.
func (s *Store) DB() *sql.DB { return s.db }
