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
	// セッション管理
	GetOrCreateSession(ctx context.Context, tokenHash string) (domain.OwnerID, error)
	UpdateSessionSeen(ctx context.Context, tokenHash string) error
	DeleteSession(ctx context.Context, tokenHash string) error

	// メモ管理
	ListMemos(ctx context.Context, ownerID domain.OwnerID) ([]*domain.Memo, error)
	GetMemo(ctx context.Context, ownerID domain.OwnerID, id string) (*domain.Memo, error)
	CreateMemo(ctx context.Context, memo *domain.Memo) error
	UpdateMemo(ctx context.Context, memo *domain.Memo) error
	DeleteMemo(ctx context.Context, ownerID domain.OwnerID, id string) error

	// ライフサイクル管理（データ全破棄・放置セッションGC）
	DeleteAllData(ctx context.Context, ownerID domain.OwnerID) error
	PurgeExpiredSessions(ctx context.Context, maxAge time.Duration) (int64, error)

	Close() error
}
