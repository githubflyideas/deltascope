// Package store 用 SQLite 存放本地用户凭证。
// 这是整个项目唯一的外部依赖(modernc.org/sqlite,纯 Go 实现,
// 免 CGO,保证 CGO_ENABLED=0 静态编译)。
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct{ db *sql.DB }

var ErrNotFound = errors.New("用户不存在")

// Open 打开(必要时创建)数据库并初始化 schema。
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // 单机低并发,串行化写入最稳
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

// UpsertUser 创建或更新用户口令哈希。
func (s *Store) UpsertUser(username, pwhash string) error {
	_, err := s.db.Exec(`
INSERT INTO users (username, pwhash, created_at) VALUES (?, ?, ?)
ON CONFLICT(username) DO UPDATE SET pwhash = excluded.pwhash`,
		username, pwhash, time.Now().UTC().Format(time.RFC3339))
	return err
}

// PasswordHash 返回用户口令哈希。
func (s *Store) PasswordHash(username string) (string, error) {
	var h string
	err := s.db.QueryRow(`SELECT pwhash FROM users WHERE username = ?`, username).Scan(&h)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return h, err
}

// DeleteUser 删除用户。
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

// ListUsers 列出全部用户名。
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
