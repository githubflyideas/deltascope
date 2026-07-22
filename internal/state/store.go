package state

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// Store 持久化快照。复用主程序打开的 *sql.DB。
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

// SaveMarker 以名字保存一份基线快照(用于 verify 发布前后对账)。
func (s *Store) SaveMarker(name string, snap Snapshot) error {
	body, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT OR REPLACE INTO markers (name, taken, body) VALUES (?, ?, ?)`,
		name, snap.Taken.UTC().Format(time.RFC3339), string(body))
	return err
}

// LoadMarker 取回命名基线快照。
func (s *Store) LoadMarker(name string) (Snapshot, error) {
	var body string
	if err := s.db.QueryRow(`SELECT body FROM markers WHERE name = ?`, name).Scan(&body); err != nil {
		if err == sql.ErrNoRows {
			return Snapshot{}, fmt.Errorf("找不到基线 %q, 请先运行 deltascope verify start -name %s", name, name)
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

// Latest 返回最近一次快照。
func (s *Store) Latest() (Snapshot, error) {
	return s.queryOne(`SELECT body FROM snapshots ORDER BY taken DESC LIMIT 1`)
}

// Before 返回不晚于 t 的最近一次快照。
func (s *Store) Before(t time.Time) (Snapshot, error) {
	return s.queryOne(`SELECT body FROM snapshots WHERE taken <= ? ORDER BY taken DESC LIMIT 1`,
		t.UTC().Format(time.RFC3339))
}

// NearestBefore 返回不晚于 t 的最近快照;若无则返回最早的一份。
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
			return Snapshot{}, fmt.Errorf("无匹配的快照")
		}
		return Snapshot{}, err
	}
	var snap Snapshot
	if err := json.Unmarshal([]byte(body), &snap); err != nil {
		return Snapshot{}, err
	}
	return snap, nil
}

// List 返回快照时间与主机,最新在前。
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

// Prune 删除早于保留期的快照。
func (s *Store) Prune(keepDays int) (int64, error) {
	cutoff := time.Now().UTC().AddDate(0, 0, -keepDays).Format(time.RFC3339)
	res, err := s.db.Exec(`DELETE FROM snapshots WHERE taken < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
