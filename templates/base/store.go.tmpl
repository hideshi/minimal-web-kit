package store

import (
	"context"
	"errors"
	"time"

	"minimal-web-kit/internal/domain"
)

var (
	ErrNotFound = errors.New("record not found")
)

// Store はデータの永続化を担当するインターフェースです。
// Handler層は生SQLやDBハンドルに直接依存せず、このインターフェースを経由します。
type Store interface {
	// セッション・所有者管理
	GetSessionOwner(ctx context.Context, tokenHash string) (domain.OwnerID, error)
	CreateSession(ctx context.Context, tokenHash string, ownerID domain.OwnerID) error
	UpdateSessionSeen(ctx context.Context, tokenHash string) error
	DeleteSession(ctx context.Context, tokenHash string) error

	// ライフサイクル管理（データ全破棄・放置セッションGC）
	DeleteAllData(ctx context.Context, ownerID domain.OwnerID) error
	PurgeExpiredSessions(ctx context.Context, maxAge time.Duration) (int64, error)

	Close() error
}
