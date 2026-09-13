package store

import (
	"path/filepath"
	"testing"
)

// The users table 3.7.8 and earlier wrote held real PBKDF2 hashes, and accounts
// are no longer stored at all, so an upgraded install must not keep carrying a
// credential in a file nobody looks at any more. Two properties: the table is
// gone, and the run says so only the first time -- a claim of "removed the
// legacy users table" on every start would be noise in the journal and would
// also mean the drop had not worked.
func TestDropLegacyUsers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if _, err := s.DB().Exec(
		`CREATE TABLE users (username TEXT PRIMARY KEY, password_hash TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(
		`INSERT INTO users VALUES ('admin', 'pbkdf2$600000$c2FsdA$aGFzaA')`); err != nil {
		t.Fatal(err)
	}

	dropped, err := s.DropLegacyUsers()
	if err != nil {
		t.Fatal(err)
	}
	if !dropped {
		t.Error("a database with a users table reported nothing to drop")
	}
	var n int
	if err := s.DB().QueryRow(
		`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'users'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Error("the users table survived")
	}

	dropped, err = s.DropLegacyUsers()
	if err != nil {
		t.Fatal(err)
	}
	if dropped {
		t.Error("the second run claims it dropped the table again")
	}
}

// A database this version created has no users table, and Open must not treat
// that as a problem or log an upgrade that did not happen.
func TestDropLegacyUsersOnAFreshDatabase(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "fresh.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	dropped, err := s.DropLegacyUsers()
	if err != nil {
		t.Fatal(err)
	}
	if dropped {
		t.Error("a fresh database reported a legacy users table")
	}
}
