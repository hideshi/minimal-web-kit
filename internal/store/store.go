package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

var (
	ErrNotFound = errors.New("record not found")
)

// OwnerID は利用者の所有権を一意に識別する型です。
// 氏名やメールアドレス等の個人情報は含まず、ランダムな識別子を使用します。
type OwnerID string

// SessionStore はコアセッションおよび全データライフサイクルを担当するインターフェースです。
type SessionStore interface {
	GetSessionOwner(ctx context.Context, tokenHash string) (OwnerID, error)
	CreateSession(ctx context.Context, tokenHash string, ownerID OwnerID) error
	DeleteSession(ctx context.Context, tokenHash string) error

	// ライフサイクル管理（CASCADE削除により全機能のデータも自動削除）
	DeleteAllData(ctx context.Context, ownerID OwnerID) error
	PurgeExpiredSessions(ctx context.Context, maxAge time.Duration) (int64, error)

	// 機能（Feature）側のストアで利用するためのDBハンドル
	DB() *sql.DB
	Close() error
}
