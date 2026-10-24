package memo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"minimal-web-kit/internal/domain"
)

var ErrNotFound = errors.New("record not found")

type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

func (s *Store) ListMemos(ctx context.Context, ownerID domain.OwnerID) ([]*Memo, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT id, owner_id, title, content, created_at, updated_at FROM memos WHERE owner_id = ? ORDER BY updated_at DESC",
		string(ownerID),
	)
	if err != nil {
		return nil, fmt.Errorf("query memos: %w", err)
	}
	defer rows.Close()

	var memos []*Memo
	for rows.Next() {
		var m Memo
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

func (s *Store) GetMemo(ctx context.Context, ownerID domain.OwnerID, id string) (*Memo, error) {
	var m Memo
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

func (s *Store) CreateMemo(ctx context.Context, m *Memo) error {
	_, err := s.db.ExecContext(ctx,
		"INSERT INTO memos (id, owner_id, title, content, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)",
		m.ID, string(m.OwnerID), m.Title, m.Content, m.CreatedAt.Unix(), m.UpdatedAt.Unix(),
	)
	if err != nil {
		return fmt.Errorf("insert memo: %w", err)
	}
	return nil
}

func (s *Store) UpdateMemo(ctx context.Context, m *Memo) error {
	res, err := s.db.ExecContext(ctx,
		"UPDATE memos SET title = ?, content = ?, updated_at = ? WHERE owner_id = ? AND id = ?",
		m.Title, m.Content, m.UpdatedAt.Unix(), string(m.OwnerID), m.ID,
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

func (s *Store) DeleteMemo(ctx context.Context, ownerID domain.OwnerID, id string) error {
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
