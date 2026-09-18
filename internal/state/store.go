package state

import (
	"context"
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

// Ping reports whether the database behind this store is reachable, for the
// readiness probe.
//
// It deliberately does not read the snapshots table. An empty store is a new
// install, not an unhealthy one, and a probe that treats "no rows yet" as not
// ready would hold a fresh deployment out of service until the first snapshot
// lands 15 minutes later.
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

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

// Stamp identifies one stored snapshot by row id and capture time, without
// its body.
//
// The split matters for the timeline: a body is ~200 KB of JSON and a week of
// history is ~1000 of them, so nothing may load them all. Reading the grid of
// capture times is cheap (it comes off idx_snapshots_taken), and the timeline
// then loads only the handful of bodies its bisect actually lands on.
type Stamp struct {
	ID    int64
	Taken time.Time
}

// Stamps lists snapshots captured strictly between from and to, oldest first.
//
// Both ends are exclusive because the callers already hold them: the timeline
// is given the A and B snapshots and wants the interior probe points. Ties on
// taken are broken by id so the order is total -- two snapshots can share a
// second (a manual statediff landing on the scheduler's tick), and a bisect
// over a non-deterministic order would return a different answer per call.
func (s *Store) Stamps(from, to time.Time) ([]Stamp, error) {
	rows, err := s.db.Query(`
		SELECT id, taken FROM snapshots
		WHERE taken > ? AND taken < ?
		ORDER BY taken ASC, id ASC`,
		from.UTC().Format(time.RFC3339), to.UTC().Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Stamp
	for rows.Next() {
		var (
			id    int64
			taken string
		)
		if err := rows.Scan(&id, &taken); err != nil {
			return nil, err
		}
		t, perr := time.Parse(time.RFC3339, taken)
		if perr != nil {
			continue // unparseable row: skip the probe, do not fail the report
		}
		out = append(out, Stamp{ID: id, Taken: t})
	}
	return out, rows.Err()
}

// ByID loads one snapshot body by row id.
func (s *Store) ByID(id int64) (Snapshot, error) {
	return s.queryOne(`SELECT body FROM snapshots WHERE id = ?`, id)
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

// Nearest returns the snapshot whose timestamp is closest to t, within
// the given tolerance. Used to line up snapshot-based windows with the
// continuous time series from PCP.
func (s *Store) Nearest(t time.Time, tolerance time.Duration) (Snapshot, error) {
	lo := t.Add(-tolerance).UTC().Format(time.RFC3339)
	hi := t.Add(tolerance).UTC().Format(time.RFC3339)
	target := t.UTC().Format(time.RFC3339)
	return s.queryOne(`
		SELECT body FROM snapshots
		WHERE taken BETWEEN ? AND ?
		ORDER BY ABS(JULIANDAY(taken) - JULIANDAY(?)) ASC
		LIMIT 1`, lo, hi, target)
}

// Count returns how many snapshots are stored.
func (s *Store) Count() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM snapshots`).Scan(&n)
	return n, err
}
