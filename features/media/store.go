package media

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"minimal-web-kit/features"
	"minimal-web-kit/internal/store"
)

var (
	// ErrNotFound は、画像が無いことを示します。
	ErrNotFound = errors.New("media: not found")
	// ErrInUse は、記事が使っている画像を消そうとしたことを示します。
	ErrInUse = errors.New("media: in use")
	// ErrFull は、全体の容量の上限を超えることを示します。
	ErrFull = errors.New("media: storage limit reached")
)

// Item は画像1枚の情報です。本体は持ちません。
type Item struct {
	ID, Format, Alt string
	Width, Height   int
	Bytes           int64
	CreatedAt       time.Time
	Uses            int
}

// File は、公開のURLに使うファイル名です。
func (i Item) File() string { return i.ID + "." + i.Format }

// Src は、公開の経路です。
func (i Item) Src() string { return "/media/" + i.File() }

// AdminSrc は、管理者の経路です。
func (i Item) AdminSrc() string { return "/admin/media/" + i.ID + "/image" }

func (i Item) image() features.MediaImage {
	return features.MediaImage{File: i.File(), Src: i.Src(), AdminSrc: i.AdminSrc(), Alt: i.Alt, Width: i.Width, Height: i.Height}
}

// Ref は、画像を使う記事です。
type Ref struct{ Owner, Key string }

// Store は画像の読み書きを担当します。
type Store struct {
	db    *sql.DB
	now   func() time.Time
	limit int64 // 全体の容量の上限（バイト）
}

const itemColumns = "id, format, width, height, bytes, alt, created_at"

func scanItem(row interface{ Scan(...any) error }, extra ...any) (Item, error) {
	var it Item
	var created int64
	dest := append([]any{&it.ID, &it.Format, &it.Width, &it.Height, &it.Bytes, &it.Alt, &created}, extra...)
	if err := row.Scan(dest...); err != nil {
		return it, err
	}
	it.CreatedAt = time.Unix(created, 0).In(time.Local)
	return it, nil
}

func newID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// Add は画像を保存します。合計の確認と保存は同じトランザクションで行い、同時の送信でも上限を超えません（MEDIA-INV-6）。
func (s *Store) Add(ctx context.Context, p processed, alt string) (Item, error) {
	id, err := newID()
	if err != nil {
		return Item{}, err
	}
	it := Item{ID: id, Format: p.format, Width: p.width, Height: p.height, Bytes: int64(len(p.data)), Alt: alt, CreatedAt: s.now()}
	err = store.Atomic(ctx, s.db, func(c *sql.Conn) error {
		var total int64
		if err := c.QueryRowContext(ctx, "SELECT COALESCE(SUM(bytes), 0) FROM media_items").Scan(&total); err != nil {
			return err
		}
		if total+it.Bytes > s.limit {
			return ErrFull
		}
		if _, err := c.ExecContext(ctx, "INSERT INTO media_items ("+itemColumns+") VALUES (?, ?, ?, ?, ?, ?, ?)",
			it.ID, it.Format, it.Width, it.Height, it.Bytes, it.Alt, it.CreatedAt.Unix()); err != nil {
			return err
		}
		_, err := c.ExecContext(ctx, "INSERT INTO media_blobs (id, data) VALUES (?, ?)", it.ID, p.data)
		return err
	})
	return it, err
}

// Total は保存している画像の合計のバイト数です。
func (s *Store) Total(ctx context.Context) (int64, error) {
	var total int64
	err := s.db.QueryRowContext(ctx, "SELECT COALESCE(SUM(bytes), 0) FROM media_items").Scan(&total)
	return total, err
}

// List は新しい順の一覧を、使われている数と一緒に返します。
func (s *Store) List(ctx context.Context, limit, offset int) ([]Item, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+itemColumns+`, (SELECT COUNT(*) FROM media_refs r WHERE r.media_id = media_items.id)
		 FROM media_items ORDER BY created_at DESC, id LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Item
	for rows.Next() {
		var uses int
		it, err := scanItem(rows, &uses)
		if err != nil {
			return nil, err
		}
		it.Uses = uses
		out = append(out, it)
	}
	return out, rows.Err()
}

// Get は画像の情報を返します。
func (s *Store) Get(ctx context.Context, id string) (Item, error) {
	it, err := scanItem(s.db.QueryRowContext(ctx, "SELECT "+itemColumns+" FROM media_items WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return it, ErrNotFound
	}
	return it, err
}

// Blob は本体を返します。読み終えて接続を返してから送れるよう、バイト列で返します（遅い閲覧者がDBを保持しない）。
func (s *Store) Blob(ctx context.Context, id string) ([]byte, error) {
	var data []byte
	err := s.db.QueryRowContext(ctx, "SELECT data FROM media_blobs WHERE id = ?", id).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return data, err
}

// UpdateAlt は説明を変えます。
func (s *Store) UpdateAlt(ctx context.Context, id, alt string) error {
	res, err := s.db.ExecContext(ctx, "UPDATE media_items SET alt = ? WHERE id = ?", alt, id)
	return store.AffectedOne(res, err, ErrNotFound)
}

// Refs は画像を使う記事を返します。
func (s *Store) Refs(ctx context.Context, id string) ([]Ref, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT owner, ref_key FROM media_refs WHERE media_id = ? ORDER BY owner, ref_key", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Ref
	for rows.Next() {
		var r Ref
		if err := rows.Scan(&r.Owner, &r.Key); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Delete は画像を消します。使われていれば ErrInUse を返します。判定と削除は同じトランザクションです（MEDIA-INV-5）。
func (s *Store) Delete(ctx context.Context, id string) error {
	return store.Atomic(ctx, s.db, func(c *sql.Conn) error {
		var uses int
		if err := c.QueryRowContext(ctx, "SELECT COUNT(*) FROM media_refs WHERE media_id = ?", id).Scan(&uses); err != nil {
			return err
		}
		if uses > 0 {
			return ErrInUse
		}
		res, err := c.ExecContext(ctx, "DELETE FROM media_items WHERE id = ?", id)
		return store.AffectedOne(res, err, ErrNotFound)
	})
}

func idOf(file string) string {
	id, _, _ := strings.Cut(file, ".")
	return id
}

// replaceRefs は、呼び出し元のトランザクションの中で参照を置き換えます。トランザクションは始めません。
func replaceRefs(ctx context.Context, c *sql.Conn, owner, key string, files []string) error {
	if _, err := c.ExecContext(ctx, "DELETE FROM media_refs WHERE owner = ? AND ref_key = ?", owner, key); err != nil {
		return err
	}
	for _, file := range files {
		id, format, _ := strings.Cut(file, ".")
		var exists int
		err := c.QueryRowContext(ctx, "SELECT 1 FROM media_items WHERE id = ? AND format = ?", id, format).Scan(&exists)
		if errors.Is(err, sql.ErrNoRows) {
			return features.ErrMediaMissing
		}
		if err != nil {
			return err
		}
		if _, err := c.ExecContext(ctx, "INSERT OR IGNORE INTO media_refs (media_id, owner, ref_key) VALUES (?, ?, ?)", id, owner, key); err != nil {
			return err
		}
	}
	return nil
}

// Rebuild は、機能の全ての参照を作り直します。消えた画像への参照は飛ばします。
func (s *Store) Rebuild(ctx context.Context, owner string, refs map[string][]string) error {
	return store.Atomic(ctx, s.db, func(c *sql.Conn) error {
		if _, err := c.ExecContext(ctx, "DELETE FROM media_refs WHERE owner = ?", owner); err != nil {
			return err
		}
		for key, files := range refs {
			for _, file := range files {
				id, format, _ := strings.Cut(file, ".")
				if _, err := c.ExecContext(ctx,
					`INSERT OR IGNORE INTO media_refs (media_id, owner, ref_key)
					 SELECT id, ?, ? FROM media_items WHERE id = ? AND format = ?`, owner, key, id, format); err != nil {
					return err
				}
			}
		}
		return nil
	})
}
