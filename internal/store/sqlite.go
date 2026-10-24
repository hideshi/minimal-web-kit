package store

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"minimal-web-kit/internal/domain"

	_ "modernc.org/sqlite"
)

//go:embed migrations/001_init.sql
var initSQL string

type SQLiteStore struct {
	db *sql.DB
}

// NewSQLiteStore は指定されたパスのSQLiteデータベースを初期化して返します。
// ファイルの権限を0600とし、secure_deleteを有効化します。
func NewSQLiteStore(dbPath string) (*SQLiteStore, error) {
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create db dir: %w", err)
	}

	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		f, err := os.OpenFile(dbPath, os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			return nil, fmt.Errorf("create db file with 0600: %w", err)
		}
		_ = f.Close()
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite db: %w", err)
	}

	pragmas := []string{
		"PRAGMA secure_delete = ON;",
		"PRAGMA foreign_keys = ON;",
		"PRAGMA journal_mode = WAL;",
		"PRAGMA busy_timeout = 5000;",
	}
	for _, p := range pragmas {
		if _, err := db.Exec(p); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("exec pragma %q: %w", p, err)
		}
	}

	if _, err := db.Exec(initSQL); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("run init migration: %w", err)
	}

	return &SQLiteStore{db: db}, nil
}

func (s *SQLiteStore) GetSessionOwner(ctx context.Context, tokenHash string) (domain.OwnerID, error) {
	var ownerID string
	err := s.db.QueryRowContext(ctx,
		"SELECT owner_id FROM sessions WHERE token_hash = ?", tokenHash,
	).Scan(&ownerID)

	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("query session: %w", err)
	}

	now := time.Now().Unix()
	_, _ = s.db.ExecContext(ctx, "UPDATE sessions SET last_seen_at = ? WHERE token_hash = ?", now, tokenHash)
	return domain.OwnerID(ownerID), nil
}

func (s *SQLiteStore) CreateSession(ctx context.Context, tokenHash string, ownerID domain.OwnerID) error {
	now := time.Now().Unix()
	_, err := s.db.ExecContext(ctx,
		"INSERT INTO sessions (token_hash, owner_id, created_at, last_seen_at) VALUES (?, ?, ?, ?)",
		tokenHash, string(ownerID), now, now,
	)
	if err != nil {
		return fmt.Errorf("insert session: %w", err)
	}
	return nil
}

func (s *SQLiteStore) UpdateSessionSeen(ctx context.Context, tokenHash string) error {
	now := time.Now().Unix()
	_, err := s.db.ExecContext(ctx, "UPDATE sessions SET last_seen_at = ? WHERE token_hash = ?", now, tokenHash)
	return err
}

func (s *SQLiteStore) DeleteSession(ctx context.Context, tokenHash string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM sessions WHERE token_hash = ?", tokenHash)
	return err
}

func (s *SQLiteStore) DeleteAllData(ctx context.Context, ownerID domain.OwnerID) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM sessions WHERE owner_id = ?", string(ownerID))
	return err
}

func (s *SQLiteStore) PurgeExpiredSessions(ctx context.Context, maxAge time.Duration) (int64, error) {
	threshold := time.Now().Add(-maxAge).Unix()
	res, err := s.db.ExecContext(ctx, "DELETE FROM sessions WHERE last_seen_at < ?", threshold)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *SQLiteStore) Close() error {
	return s.db.Close()
}
