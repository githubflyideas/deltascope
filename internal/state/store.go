package state

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// Store persists snapshots. Reuses the *sql.DB opened by the main program.
type Store struct{ db *sql.DB }

func NewStore(db *sql.DB) (*Store, error) {
	if _, err := db.Exec(`
CREATE TABLE IF NOT EXISTS snapshots (
    id     INTEGER PRIMARY KEY AUTOINCREMENT,
    taken  TEXT NOT NULL,
    host   TEXT NOT NULL,
    body   TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_snapshots_taken ON snapshots(taken);
CREATE TABLE IF NOT EXISTS markers (
    name    TEXT PRIMARY KEY,
    taken   TEXT NOT NULL,
    body    TEXT NOT NULL
);`); err != nil {
		return nil, err
	}
	return &Store{db: db}, nil
}

// SaveMarker saves a baseline snapshot under a name (used by verify start/report).
func (s *Store) SaveMarker(name string, snap Snapshot) error {
	body, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT OR REPLACE INTO markers (name, taken, body) VALUES (?, ?, ?)`,
		name, snap.Taken.UTC().Format(time.RFC3339), string(body))
	return err
}

// LoadMarker retrieves a named baseline snapshot.
func (s *Store) LoadMarker(name string) (Snapshot, error) {
	var body string
	if err := s.db.QueryRow(`SELECT body FROM markers WHERE name = ?`, name).Scan(&body); err != nil {
		if err == sql.ErrNoRows {
			return Snapshot{}, fmt.Errorf("baseline %q not found, run deltascope verify start -name %s first", name, name)
		}
		return Snapshot{}, err
	}
	var snap Snapshot
	if err := json.Unmarshal([]byte(body), &snap); err != nil {
		return Snapshot{}, err
	}
	return snap, nil
}

func (s *Store) Save(snap Snapshot) error {
	body, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO snapshots (taken, host, body) VALUES (?, ?, ?)`,
		snap.Taken.UTC().Format(time.RFC3339), snap.Host, string(body))
	return err
}

// Latest returns the most recent snapshot.
func (s *Store) Latest() (Snapshot, error) {
	return s.queryOne(`SELECT body FROM snapshots ORDER BY taken DESC LIMIT 1`)
}

// Before returns the most recent snapshot at or before t.
func (s *Store) Before(t time.Time) (Snapshot, error) {
	return s.queryOne(`SELECT body FROM snapshots WHERE taken <= ? ORDER BY taken DESC LIMIT 1`,
		t.UTC().Format(time.RFC3339))
}

// NearestBefore returns the most recent snapshot at or before t; if none, returns the earliest one.
func (s *Store) NearestBefore(t time.Time) (Snapshot, error) {
	snap, err := s.Before(t)
	if err == nil {
		return snap, nil
	}
	return s.queryOne(`SELECT body FROM snapshots ORDER BY taken ASC LIMIT 1`)
}

func (s *Store) queryOne(q string, args ...any) (Snapshot, error) {
	var body string
	if err := s.db.QueryRow(q, args...).Scan(&body); err != nil {
		if err == sql.ErrNoRows {
			return Snapshot{}, fmt.Errorf("no matching snapshot")
		}
		return Snapshot{}, err
	}
	var snap Snapshot
	if err := json.Unmarshal([]byte(body), &snap); err != nil {
		return Snapshot{}, err
	}
	return snap, nil
}

// List returns snapshot times and hosts, newest first.
func (s *Store) List(limit int) ([]Snapshot, error) {
	rows, err := s.db.Query(`SELECT body FROM snapshots ORDER BY taken DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Snapshot
	for rows.Next() {
		var body string
		if err := rows.Scan(&body); err != nil {
			return nil, err
		}
		var snap Snapshot
		if err := json.Unmarshal([]byte(body), &snap); err == nil {
			snap.Sections = nil
			out = append(out, snap)
		}
	}
	return out, rows.Err()
}

// Prune deletes snapshots older than the retention period.
func (s *Store) Prune(keepDays int) (int64, error) {
	cutoff := time.Now().UTC().AddDate(0, 0, -keepDays).Format(time.RFC3339)
	res, err := s.db.Exec(`DELETE FROM snapshots WHERE taken < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
