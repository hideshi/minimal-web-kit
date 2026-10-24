package memo

import (
	"log/slog"
	"time"

	"minimal-web-kit/internal/domain"
)

// Memo は利用者が作成したプライベートメモを表します。
type Memo struct {
	ID        string         `json:"id"`
	OwnerID   domain.OwnerID `json:"-"`
	Title     string         `json:"title"`
	Content   string         `json:"content"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
}

// LogValue は slog.LogValuer の実装です。本文を伏せてログ契約を守ります。
func (m Memo) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("id", m.ID),
		slog.String("owner_id", string(m.OwnerID)),
		slog.Int("title_len", len(m.Title)),
		slog.Int("content_len", len(m.Content)),
		slog.Time("created_at", m.CreatedAt),
	)
}
