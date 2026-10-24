package store

import (
	"context"
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
	sessions map[string]*memorySession // tokenHash -> session
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
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
	sess.lastSeenAt = time.Now()
	return sess.ownerID, nil
}

func (s *MemoryStore) CreateSession(ctx context.Context, tokenHash string, ownerID domain.OwnerID) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	s.sessions[tokenHash] = &memorySession{
		ownerID:    ownerID,
		createdAt:  now,
		lastSeenAt: now,
	}
	return nil
}

func (s *MemoryStore) UpdateSessionSeen(ctx context.Context, tokenHash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sess, ok := s.sessions[tokenHash]; ok {
		sess.lastSeenAt = time.Now()
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
	var count int64

	for hash, sess := range s.sessions {
		if sess.lastSeenAt.Before(threshold) {
			delete(s.sessions, hash)
			count++
		}
	}

	return count, nil
}

func (s *MemoryStore) Close() error {
	return nil
}
