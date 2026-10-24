package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	_ "embed"
	"encoding/hex"
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

	// ファイルが存在しない場合は 0600 で作成
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

	// PRAGMAの設定
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

	// マイグレーション実行
	if _, err := db.Exec(initSQL); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("run init migration: %w", err)
	}

	return &SQLiteStore{db: db}, nil
}

func (s *SQLiteStore) GetOrCreateSession(ctx context.Context, tokenHash string) (domain.OwnerID, error) {
	var ownerID string
	err := s.db.QueryRowContext(ctx,
		"SELECT owner_id FROM sessions WHERE token_hash = ?", tokenHash,
	).Scan(&ownerID)

	if err == nil {
		now := time.Now().Unix()
		_, _ = s.db.ExecContext(ctx, "UPDATE sessions SET last_seen_at = ? WHERE token_hash = ?", now, tokenHash)
		return domain.OwnerID(ownerID), nil
	}

	if !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("query session: %w", err)
	}

	// セッション新規発行
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("rand owner id: %w", err)
	}
	newOwnerID := hex.EncodeToString(b)
	now := time.Now().Unix()

	_, err = s.db.ExecContext(ctx,
		"INSERT INTO sessions (token_hash, owner_id, created_at, last_seen_at) VALUES (?, ?, ?, ?)",
		tokenHash, newOwnerID, now, now,
	)
	if err != nil {
		return "", fmt.Errorf("insert session: %w", err)
	}

	return domain.OwnerID(newOwnerID), nil
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

func (s *SQLiteStore) ListMemos(ctx context.Context, ownerID domain.OwnerID) ([]*domain.Memo, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT id, owner_id, title, content, created_at, updated_at FROM memos WHERE owner_id = ? ORDER BY updated_at DESC",
		string(ownerID),
	)
	if err != nil {
		return nil, fmt.Errorf("query memos: %w", err)
	}
	defer rows.Close()

	var memos []*domain.Memo
	for rows.Next() {
		var m domain.Memo
		var oID string
		var cAt, uAt int64
		if err := rows.Scan(&m.ID, &oID, &m.Title, &m.Content, &cAt, &uAt); err != nil {
			return nil, fmt.Errorf("scan memo: %w", err)
		}
		m.OwnerID = domain.OwnerID(oID)
		m.CreatedAt = time.Unix(cAt, 0)
		m.UpdatedAt = time.Unix(uAt, 0)
		memos = append(memos, &m)
	}
	return memos, rows.Err()
}

func (s *SQLiteStore) GetMemo(ctx context.Context, ownerID domain.OwnerID, id string) (*domain.Memo, error) {
	var m domain.Memo
	var oID string
	var cAt, uAt int64
	err := s.db.QueryRowContext(ctx,
		"SELECT id, owner_id, title, content, created_at, updated_at FROM memos WHERE owner_id = ? AND id = ?",
		string(ownerID), id,
	).Scan(&m.ID, &oID, &m.Title, &m.Content, &cAt, &uAt)

	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("query memo: %w", err)
	}

	m.OwnerID = domain.OwnerID(oID)
	m.CreatedAt = time.Unix(cAt, 0)
	m.UpdatedAt = time.Unix(uAt, 0)
	return &m, nil
}

func (s *SQLiteStore) CreateMemo(ctx context.Context, memo *domain.Memo) error {
	_, err := s.db.ExecContext(ctx,
		"INSERT INTO memos (id, owner_id, title, content, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)",
		memo.ID, string(memo.OwnerID), memo.Title, memo.Content, memo.CreatedAt.Unix(), memo.UpdatedAt.Unix(),
	)
	if err != nil {
		return fmt.Errorf("insert memo: %w", err)
	}
	return nil
}

func (s *SQLiteStore) UpdateMemo(ctx context.Context, memo *domain.Memo) error {
	res, err := s.db.ExecContext(ctx,
		"UPDATE memos SET title = ?, content = ?, updated_at = ? WHERE owner_id = ? AND id = ?",
		memo.Title, memo.Content, memo.UpdatedAt.Unix(), string(memo.OwnerID), memo.ID,
	)
	if err != nil {
		return fmt.Errorf("update memo: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *SQLiteStore) DeleteMemo(ctx context.Context, ownerID domain.OwnerID, id string) error {
	res, err := s.db.ExecContext(ctx,
		"DELETE FROM memos WHERE owner_id = ? AND id = ?",
		string(ownerID), id,
	)
	if err != nil {
		return fmt.Errorf("delete memo: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *SQLiteStore) DeleteAllData(ctx context.Context, ownerID domain.OwnerID) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, "DELETE FROM memos WHERE owner_id = ?", string(ownerID)); err != nil {
		return fmt.Errorf("delete user memos: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM sessions WHERE owner_id = ?", string(ownerID)); err != nil {
		return fmt.Errorf("delete user sessions: %w", err)
	}

	return tx.Commit()
}

func (s *SQLiteStore) PurgeExpiredSessions(ctx context.Context, maxAge time.Duration) (int64, error) {
	threshold := time.Now().Add(-maxAge).Unix()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	// 期限切れのセッションの所有者IDを取得
	rows, err := tx.QueryContext(ctx, "SELECT DISTINCT owner_id FROM sessions WHERE last_seen_at < ?", threshold)
	if err != nil {
		return 0, fmt.Errorf("query expired owners: %w", err)
	}
	defer rows.Close()

	var owners []string
	for rows.Next() {
		var o string
		if err := rows.Scan(&o); err != nil {
			return 0, err
		}
		owners = append(owners, o)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}

	var deletedCount int64
	for _, o := range owners {
		// 孤立したメモを物理削除
		if _, err := tx.ExecContext(ctx, "DELETE FROM memos WHERE owner_id = ?", o); err != nil {
			return 0, fmt.Errorf("purge memos: %w", err)
		}
		res, err := tx.ExecContext(ctx, "DELETE FROM sessions WHERE owner_id = ?", o)
		if err != nil {
			return 0, fmt.Errorf("purge sessions: %w", err)
		}
		n, _ := res.RowsAffected()
		deletedCount += n
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return deletedCount, nil
}

func (s *SQLiteStore) Close() error {
	return s.db.Close()
}
