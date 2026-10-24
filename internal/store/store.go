package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"minimal-web-kit/internal/domain"
)

var (
	ErrNotFound = errors.New("record not found")
)

// SessionStore はコアセッションおよび全データライフサイクルを担当するインターフェースです。
type SessionStore interface {
	GetSessionOwner(ctx context.Context, tokenHash string) (domain.OwnerID, error)
	CreateSession(ctx context.Context, tokenHash string, ownerID domain.OwnerID) error
	DeleteSession(ctx context.Context, tokenHash string) error

	// ライフサイクル管理（CASCADE削除により全機能のデータも自動削除）
	DeleteAllData(ctx context.Context, ownerID domain.OwnerID) error
	PurgeExpiredSessions(ctx context.Context, maxAge time.Duration) (int64, error)

	// 機能（Feature）側のストアで利用するためのDBハンドル
	DB() *sql.DB
	Close() error
}
