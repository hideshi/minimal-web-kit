package store

import (
	"context"
	"database/sql"
	"sync"
	"time"

	"minimal-web-kit/internal/domain"
)

type memorySession struct {
	ownerID    domain.OwnerID
	createdAt  time.Time
	lastSeenAt time.Time
}

// MemoryStore はテストやDBなし検証用のインメモリ実装です。
type MemoryStore struct {
	mu       sync.RWMutex
	owners   map[domain.OwnerID]time.Time
	sessions map[string]*memorySession // tokenHash -> session
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		owners:   make(map[domain.OwnerID]time.Time),
		sessions: make(map[string]*memorySession),
	}
}

func (s *MemoryStore) GetSessionOwner(ctx context.Context, tokenHash string) (domain.OwnerID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	sess, ok := s.sessions[tokenHash]
	if !ok {
		return "", ErrNotFound
	}

	// 30日（MaxSessionAgeSec）を超過している場合は失効
	if time.Since(sess.lastSeenAt) > 30*24*time.Hour {
		delete(s.sessions, tokenHash)
		return "", ErrNotFound
	}

	sess.lastSeenAt = time.Now()
	return sess.ownerID, nil
}

func (s *MemoryStore) CreateSession(ctx context.Context, tokenHash string, ownerID domain.OwnerID) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	s.owners[ownerID] = now
	s.sessions[tokenHash] = &memorySession{
		ownerID:    ownerID,
		createdAt:  now,
		lastSeenAt: now,
	}
	return nil
}

func (s *MemoryStore) DeleteSession(ctx context.Context, tokenHash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, tokenHash)
	return nil
}

func (s *MemoryStore) DeleteAllData(ctx context.Context, ownerID domain.OwnerID) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.owners, ownerID)
	for hash, sess := range s.sessions {
		if sess.ownerID == ownerID {
			delete(s.sessions, hash)
		}
	}
	return nil
}

func (s *MemoryStore) PurgeExpiredSessions(ctx context.Context, maxAge time.Duration) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	threshold := time.Now().Add(-maxAge)
	expiredOwners := make(map[domain.OwnerID]bool)
	var count int64

	for hash, sess := range s.sessions {
		if sess.lastSeenAt.Before(threshold) {
			expiredOwners[sess.ownerID] = true
			delete(s.sessions, hash)
			count++
		}
	}

	for o := range expiredOwners {
		delete(s.owners, o)
	}

	return count, nil
}

func (s *MemoryStore) DB() *sql.DB {
	return nil
}

func (s *MemoryStore) Close() error {
	return nil
}
