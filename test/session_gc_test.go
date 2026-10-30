package test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"minimal-web-kit/internal/store"
)

// TestPurgeExpiredSessions_KeepsActiveOwner は、期限切れセッションの削除が
// 同じ owner の有効なセッションとデータを巻き込まないことを検証します。
func TestPurgeExpiredSessions_KeepsActiveOwner(t *testing.T) {
	ctx := context.Background()
	s, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "gc.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()
	db := s.DB()

	// Featureデータを想定した、owner に CASCADE で紐づくテーブル
	if _, err := db.Exec(`CREATE TABLE notes (
		id INTEGER PRIMARY KEY,
		owner_id TEXT NOT NULL REFERENCES owners(id) ON DELETE CASCADE
	)`); err != nil {
		t.Fatalf("create notes: %v", err)
	}

	mixed := store.OwnerID("owner-mixed")
	stale := store.OwnerID("owner-stale")
	for _, c := range []struct {
		hash  string
		owner store.OwnerID
	}{
		{"hash-mixed-old", mixed},
		{"hash-mixed-new", mixed},
		{"hash-stale", stale},
	} {
		if err := s.CreateSession(ctx, c.hash, c.owner); err != nil {
			t.Fatalf("create session %s: %v", c.hash, err)
		}
	}
	for _, o := range []store.OwnerID{mixed, stale} {
		if _, err := db.Exec("INSERT INTO notes (owner_id) VALUES (?)", string(o)); err != nil {
			t.Fatalf("insert note: %v", err)
		}
	}

	old := time.Now().Add(-31 * 24 * time.Hour).Unix()
	if _, err := db.Exec("UPDATE sessions SET last_seen_at = ? WHERE token_hash IN ('hash-mixed-old', 'hash-stale')", old); err != nil {
		t.Fatalf("age sessions: %v", err)
	}

	count, err := s.PurgeExpiredSessions(ctx, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 expired sessions purged, got %d", count)
	}

	if got, err := s.GetSessionOwner(ctx, "hash-mixed-new"); err != nil || got != mixed {
		t.Errorf("active session must survive purge: owner=%q err=%v", got, err)
	}
	assertCount(t, db, "SELECT COUNT(*) FROM owners WHERE id = ?", string(mixed), 1)
	assertCount(t, db, "SELECT COUNT(*) FROM notes WHERE owner_id = ?", string(mixed), 1)
	assertCount(t, db, "SELECT COUNT(*) FROM owners WHERE id = ?", string(stale), 0)
	assertCount(t, db, "SELECT COUNT(*) FROM notes WHERE owner_id = ?", string(stale), 0)
}

// TestPurgeExpiredSessions_RemovesOrphanOwner は、照会時に失効処理されて
// セッションを失った owner も、GCで削除されることを検証します。
func TestPurgeExpiredSessions_RemovesOrphanOwner(t *testing.T) {
	ctx := context.Background()
	s, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "gc.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()
	db := s.DB()

	owner := store.OwnerID("owner-orphan")
	if err := s.CreateSession(ctx, "hash-orphan", owner); err != nil {
		t.Fatalf("create session: %v", err)
	}
	old := time.Now().Add(-31 * 24 * time.Hour).Unix()
	if _, err := db.Exec("UPDATE sessions SET last_seen_at = ?", old); err != nil {
		t.Fatalf("age session: %v", err)
	}
	if _, err := s.GetSessionOwner(ctx, "hash-orphan"); err == nil {
		t.Fatalf("expired session must be rejected")
	}
	assertCount(t, db, "SELECT COUNT(*) FROM owners WHERE id = ?", string(owner), 1)

	if _, err := s.PurgeExpiredSessions(ctx, 30*24*time.Hour); err != nil {
		t.Fatalf("purge: %v", err)
	}
	assertCount(t, db, "SELECT COUNT(*) FROM owners WHERE id = ?", string(owner), 0)
}

// TestMemoryStorePurge_KeepsActiveSession はメモリ実装でも同じ振る舞いであることを検証します。
func TestMemoryStorePurge_KeepsActiveSession(t *testing.T) {
	ctx := context.Background()
	s := store.NewMemoryStore()
	owner := store.OwnerID("owner-mixed")
	if err := s.CreateSession(ctx, "hash-old", owner); err != nil {
		t.Fatalf("create session: %v", err)
	}
	time.Sleep(20 * time.Millisecond)
	if err := s.CreateSession(ctx, "hash-new", owner); err != nil {
		t.Fatalf("create session: %v", err)
	}

	count, err := s.PurgeExpiredSessions(ctx, 10*time.Millisecond)
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 expired session purged, got %d", count)
	}
	if got, err := s.GetSessionOwner(ctx, "hash-new"); err != nil || got != owner {
		t.Errorf("active session must survive purge: owner=%q err=%v", got, err)
	}
}

func assertCount(t *testing.T, db *sql.DB, query, arg string, want int) {
	t.Helper()
	var got int
	if err := db.QueryRow(query, arg).Scan(&got); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	if got != want {
		t.Errorf("%s [%s]: got %d, want %d", query, arg, got, want)
	}
}
