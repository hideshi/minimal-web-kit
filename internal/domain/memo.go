package domain

import (
	"log/slog"
	"time"
)

// OwnerID は利用者の所有権を一意に識別する型です。
// 氏名やメールアドレス等の個人情報は含まず、ランダムな識別子を使用します。
type OwnerID string

// Memo は利用者が作成したプライベートメモを表します。
type Memo struct {
	ID        string    `json:"id"`
	OwnerID   OwnerID   `json:"-"` // JSONエクスポート時にもOwnerIDは露出させない
	Title     string    `json:"title"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// LogValue は slog.LogValuer の実装です。
// メモ本文（Content）や機密情報をログに出力させないための保護契約です。
func (m Memo) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("id", m.ID),
		slog.String("owner_id", string(m.OwnerID)),
		slog.Int("title_len", len(m.Title)),
		slog.Int("content_len", len(m.Content)),
		slog.Time("created_at", m.CreatedAt),
	)
}
