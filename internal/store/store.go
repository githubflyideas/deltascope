package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

type Store struct{ db *sql.DB }

var ErrNotFound = errors.New("用户不存在")

func Open(path string) (*Store, error) {
	db, err := sql.Open(driverName, path+dsnParams)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`
CREATE TABLE IF NOT EXISTS users (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    username   TEXT NOT NULL UNIQUE,
    pwhash     TEXT NOT NULL,
    created_at TEXT NOT NULL
);`); err != nil {
		db.Close()
		return nil, fmt.Errorf("初始化 schema 失败: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) UpsertUser(username, pwhash string) error {
	_, err := s.db.Exec(`
INSERT INTO users (username, pwhash, created_at) VALUES (?, ?, ?)
ON CONFLICT(username) DO UPDATE SET pwhash = excluded.pwhash`,
		username, pwhash, time.Now().UTC().Format(time.RFC3339))
	return err
}

func (s *Store) PasswordHash(username string) (string, error) {
	var h string
	err := s.db.QueryRow(`SELECT pwhash FROM users WHERE username = ?`, username).Scan(&h)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return h, err
}

func (s *Store) DeleteUser(username string) error {
	res, err := s.db.Exec(`DELETE FROM users WHERE username = ?`, username)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) ListUsers() ([]string, error) {
	rows, err := s.db.Query(`SELECT username FROM users ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// DB 暴露底层连接,供同库的状态快照存储复用同一个数据库文件。
func (s *Store) DB() *sql.DB { return s.db }
