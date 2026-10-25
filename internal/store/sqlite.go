package store

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"minimal-web-kit/internal/domain"

	_ "modernc.org/sqlite"
)

//go:embed migrations/001_core.sql
var coreSQL string

const (
	// MaxSessionAgeSec はセッションの最大保持期間（30日）です。
	MaxSessionAgeSec = 30 * 24 * 60 * 60
)

type SQLiteStore struct {
	db *sql.DB
}

// NewSQLiteStore は指定されたパスのSQLiteデータベースを初期化して返します。
// ファイル権限を0600とし、DSNパラメータで全接続に対してforeign_keysとsecure_deleteを確実に強制します。
func NewSQLiteStore(dbPath string) (*SQLiteStore, error) {
	var dsn string
	if dbPath == ":memory:" || strings.HasPrefix(dbPath, "file::memory:") {
		dsn = "file:mem?mode=memory&cache=shared&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=secure_delete(1)"
	} else {
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
		dsn = fmt.Sprintf("file:%s?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=secure_delete(1)", dbPath)
	}

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite db: %w", err)
	}

	// WALジャーナルモードの設定
	if !strings.Contains(dsn, "mode=memory") {
		if _, err := db.Exec("PRAGMA journal_mode = WAL;"); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("set WAL journal mode: %w", err)
		}
	}

	// コアマイグレーションを実行
	if _, err := db.Exec(coreSQL); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("run core migration: %w", err)
	}

	return &SQLiteStore{db: db}, nil
}

func (s *SQLiteStore) DB() *sql.DB {
	return s.db
}

func (s *SQLiteStore) GetSessionOwner(ctx context.Context, tokenHash string) (domain.OwnerID, error) {
	var ownerID string
	var lastSeenAt int64
	err := s.db.QueryRowContext(ctx,
		"SELECT owner_id, last_seen_at FROM sessions WHERE token_hash = ?", tokenHash,
	).Scan(&ownerID, &lastSeenAt)

	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("query session: %w", err)
	}

	// 有効期限の厳格チェック: 最終アクセスから30日を超過している場合は失効
	now := time.Now().Unix()
	if now-lastSeenAt > MaxSessionAgeSec {
		// 失効したセッションを削除して ErrNotFound を返す（復活を阻止）
		_, _ = s.db.ExecContext(ctx, "DELETE FROM sessions WHERE token_hash = ?", tokenHash)
		return "", ErrNotFound
	}

	// 最終アクセス時刻の更新
	if _, err := s.db.ExecContext(ctx, "UPDATE sessions SET last_seen_at = ? WHERE token_hash = ?", now, tokenHash); err != nil {
		return "", fmt.Errorf("update session last_seen_at: %w", err)
	}

	return domain.OwnerID(ownerID), nil
}

func (s *SQLiteStore) CreateSession(ctx context.Context, tokenHash string, ownerID domain.OwnerID) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now().Unix()

	// 1. owners テーブルにレコードを作成（存在しなければ）
	if _, err := tx.ExecContext(ctx,
		"INSERT OR IGNORE INTO owners (id, created_at) VALUES (?, ?)",
		string(ownerID), now,
	); err != nil {
		return fmt.Errorf("insert owner: %w", err)
	}

	// 2. sessions テーブルにレコードを作成
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO sessions (token_hash, owner_id, created_at, last_seen_at) VALUES (?, ?, ?, ?)",
		tokenHash, string(ownerID), now, now,
	); err != nil {
		return fmt.Errorf("insert session: %w", err)
	}

	return tx.Commit()
}

func (s *SQLiteStore) DeleteSession(ctx context.Context, tokenHash string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM sessions WHERE token_hash = ?", tokenHash)
	return err
}

func (s *SQLiteStore) DeleteAllData(ctx context.Context, ownerID domain.OwnerID) error {
	// ON DELETE CASCADE により、owners を削除するだけでセッションも全機能データも自動連動削除される
	_, err := s.db.ExecContext(ctx, "DELETE FROM owners WHERE id = ?", string(ownerID))
	return err
}

func (s *SQLiteStore) PurgeExpiredSessions(ctx context.Context, maxAge time.Duration) (int64, error) {
	threshold := time.Now().Add(-maxAge).Unix()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	// 1. 期限切れのセッションだけを削除（同じ owner の有効なセッションは残す）
	res, err := tx.ExecContext(ctx, "DELETE FROM sessions WHERE last_seen_at < ?", threshold)
	if err != nil {
		return 0, err
	}
	count, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}

	// 2. 有効なセッションが1件も残っていない owner を削除（CASCADEにより紐づく全データも削除される）
	if _, err := tx.ExecContext(ctx,
		"DELETE FROM owners WHERE NOT EXISTS (SELECT 1 FROM sessions WHERE sessions.owner_id = owners.id)",
	); err != nil {
		return 0, err
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return count, nil
}

func (s *SQLiteStore) Close() error {
	return s.db.Close()
}
