// Package store 是 Hub 的 SQLite 持久层。只用标准 SQL 与 database/sql，便于将来切换 Postgres（D12）。
package store

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schema string

// ErrNotFound 表示记录不存在。
var ErrNotFound = errors.New("记录不存在")

// Store 封装数据库连接。
type Store struct {
	db *sql.DB
}

// Open 打开（必要时创建）数据库并应用结构。
func Open(path string) (*Store, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// SQLite 单写者：一个连接即可避免锁竞争。
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("应用数据库结构失败：%w", err)
	}
	return &Store{db: db}, nil
}

// Close 关闭数据库。
func (s *Store) Close() error { return s.db.Close() }

// DB 暴露底层连接，供测试与维护命令使用。
func (s *Store) DB() *sql.DB { return s.db }

func now() string { return ts(time.Now()) }

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func parseTS(s string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}

func parseNullTS(s sql.NullString) *time.Time {
	if !s.Valid || s.String == "" {
		return nil
	}
	t := parseTS(s.String)
	return &t
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// tx 在事务中执行 fn。
func (s *Store) tx(ctx context.Context, fn func(*sql.Tx) error) error {
	t, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(t); err != nil {
		_ = t.Rollback()
		return err
	}
	return t.Commit()
}

// GetSetting 读取设置项，不存在时返回空串。
func (s *Store) GetSetting(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

// SetSetting 写入设置项。
func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO settings(key, value) VALUES(?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// CreateWebSession 记录 Web 登录会话。
func (s *Store) CreateWebSession(ctx context.Context, tokenHash string, ttl time.Duration) error {
	n := time.Now()
	_, err := s.db.ExecContext(ctx, `INSERT INTO web_sessions(token_hash, created_at, expires_at) VALUES(?, ?, ?)`, tokenHash, ts(n), ts(n.Add(ttl)))
	return err
}

// ValidWebSession 判断 Web 会话是否有效。
func (s *Store) ValidWebSession(ctx context.Context, tokenHash string) (bool, error) {
	var exp string
	err := s.db.QueryRowContext(ctx, `SELECT expires_at FROM web_sessions WHERE token_hash = ?`, tokenHash).Scan(&exp)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return time.Now().Before(parseTS(exp)), nil
}

// DeleteWebSession 注销 Web 会话；allSessions 为真时清空全部（改密码时使用）。
func (s *Store) DeleteWebSession(ctx context.Context, tokenHash string, allSessions bool) error {
	if allSessions {
		_, err := s.db.ExecContext(ctx, `DELETE FROM web_sessions`)
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM web_sessions WHERE token_hash = ?`, tokenHash)
	return err
}
