package cms

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"minimal-web-kit/internal/domain"
	"minimal-web-kit/internal/store"
)

// newTestDB はCoreと同じ設定のSQLiteを開き、CMSのマイグレーションを適用します。
func newTestDB(t *testing.T) (*store.SQLiteStore, *sql.DB) {
	t.Helper()
	s, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "cms.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := Migrate(s.DB()); err != nil {
		t.Fatal(err)
	}
	return s, s.DB()
}

func TestMigrate_IdempotentAndKeepsData(t *testing.T) {
	_, db := newTestDB(t)
	posts := NewPostStore(db)
	id, err := posts.Create(context.Background(), "title", "body")
	if err != nil {
		t.Fatal(err)
	}
	var userID []byte
	if err := db.QueryRow("SELECT webauthn_user_id FROM cms_settings WHERE id = 1").Scan(&userID); err != nil || len(userID) != 16 {
		t.Fatalf("webauthn user id: %v len=%d", err, len(userID))
	}
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	var again []byte
	if err := db.QueryRow("SELECT webauthn_user_id FROM cms_settings WHERE id = 1").Scan(&again); err != nil || string(again) != string(userID) {
		t.Fatal("re-running the migration must keep the WebAuthn user id")
	}
	if _, err := posts.Get(context.Background(), id); err != nil {
		t.Fatal("re-running the migration must keep posts")
	}
}

func TestPostStore_PublishLifecycle(t *testing.T) {
	ctx := context.Background()
	_, db := newTestDB(t)
	posts := NewPostStore(db)
	clock := time.Unix(1_700_000_000, 0)
	posts.now = func() time.Time { return clock }

	id, err := posts.Create(ctx, "  お知らせ  ", "一段落目\r\n\r\n二段落目")
	if err != nil {
		t.Fatal(err)
	}
	p, err := posts.Get(ctx, id)
	if err != nil || p.Title != "お知らせ" || p.Body != "一段落目\n\n二段落目" || p.Published || !p.PublishedAt.IsZero() {
		t.Fatalf("created post: %+v err=%v", p, err)
	}
	if _, err := posts.GetPublished(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Fatal("drafts must not be visible publicly")
	}

	if err := posts.Publish(ctx, id); err != nil {
		t.Fatal(err)
	}
	first, err := posts.GetPublished(ctx, id)
	if err != nil || !first.Published || !first.PublishedAt.Equal(clock) {
		t.Fatalf("published post: %+v err=%v", first, err)
	}

	// 非公開に戻して再公開しても、公開日時は維持する
	clock = clock.Add(time.Hour)
	if err := posts.Unpublish(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := posts.GetPublished(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Fatal("unpublished posts must not be visible publicly")
	}
	if err := posts.Publish(ctx, id); err != nil {
		t.Fatal(err)
	}
	again, _ := posts.GetPublished(ctx, id)
	if !again.PublishedAt.Equal(first.PublishedAt) {
		t.Fatal("republishing must keep the original published_at")
	}
	// 未来の公開日時は予約公開になり、その日時を過ぎるまで公開側に出ない
	if err := posts.UpdateWithDate(ctx, id, "お知らせ", "", clock.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := posts.GetPublished(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Fatal("a scheduled post must not be visible before published_at")
	}
	if list, _ := posts.ListPublished(ctx, 10, 0); len(list) != 0 {
		t.Fatal("a scheduled post must not be listed before published_at")
	}
	if p, _ := posts.Get(ctx, id); !p.ScheduledAt(clock) {
		t.Fatal("the post must be reported as scheduled")
	}
	clock = clock.Add(time.Minute)
	if _, err := posts.GetPublished(ctx, id); err != nil {
		t.Fatal("a scheduled post must be visible once published_at is reached")
	}
	if list, _ := posts.ListPublished(ctx, 10, 0); len(list) != 1 {
		t.Fatal("a scheduled post must be listed once published_at is reached")
	}
	newDate := time.Date(2023, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := posts.SetPublishedAt(ctx, id, newDate); err != nil {
		t.Fatal(err)
	}
	if p, _ := posts.Get(ctx, id); !p.PublishedAt.Equal(newDate) {
		t.Fatal("explicit published_at update failed")
	}

	if err := posts.Delete(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := posts.Get(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Fatal("deleted post still exists")
	}
	for _, err := range []error{posts.Delete(ctx, id), posts.Publish(ctx, id), posts.Update(ctx, id, "t", "b")} {
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("operations on a missing post must return ErrNotFound, got %v", err)
		}
	}
	// 削除済みのIDは再利用しない
	next, err := posts.Create(ctx, "next", "")
	if err != nil || next == id {
		t.Fatalf("deleted id was reused: %d", next)
	}
}

func TestPostStore_ListPublishedOrder(t *testing.T) {
	ctx := context.Background()
	_, db := newTestDB(t)
	posts := NewPostStore(db)
	clock := time.Unix(1_700_000_000, 0)
	posts.now = func() time.Time { return clock }

	var ids []int64
	for i := 0; i < 4; i++ {
		id, err := posts.Create(ctx, "post", "")
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	// ids[0]: 古い公開、ids[1]とids[2]: 同時刻に公開、ids[3]: 下書き
	if err := posts.Publish(ctx, ids[0]); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(time.Minute)
	for _, id := range ids[1:3] {
		if err := posts.Publish(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	list, err := posts.ListPublished(ctx, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	var got []int64
	for _, p := range list {
		got = append(got, p.ID)
	}
	want := []int64{ids[2], ids[1], ids[0]}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("order = %v, want %v (published_at DESC, id DESC, drafts excluded)", got, want)
	}
	page2, err := posts.ListPublished(ctx, 2, 2)
	if err != nil || len(page2) != 1 || page2[0].ID != ids[0] {
		t.Fatalf("offset paging failed: %+v", page2)
	}
	all, err := posts.ListAll(ctx, 10, 0)
	if err != nil || len(all) != 4 {
		t.Fatalf("admin list must include drafts: %d", len(all))
	}
}

func TestPostStore_RejectsInvalidInput(t *testing.T) {
	ctx := context.Background()
	_, db := newTestDB(t)
	posts := NewPostStore(db)
	cases := map[string][2]string{
		"empty title":      {"   ", "body"},
		"newline in title": {"a\nb", "body"},
		"long title":       {strings.Repeat("あ", maxTitleRunes+1), "body"},
		"long body":        {"t", strings.Repeat("あ", maxBodyRunes+1)},
		"invalid utf8":     {"t", "\xff"},
		"control char":     {"t", "a\x00b"},
		"escape char":      {"t\x1b", "body"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := posts.Create(ctx, c[0], c[1]); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("expected ErrInvalidInput, got %v", err)
			}
		})
	}
	if _, err := posts.Create(ctx, strings.Repeat("あ", maxTitleRunes), strings.Repeat("あ", maxBodyRunes)); err != nil {
		t.Fatalf("maximum lengths must be accepted: %v", err)
	}
}

// Purgeは期限切れの管理者セッションと使用済み・期限切れトークンだけを削除し、記事・パスキーを残すこと。
// CoreのセッションGCとセッション破棄でも、CMSのデータが消えないこと。
func TestPurge_KeepsContentAndCoreGCIndependent(t *testing.T) {
	ctx := context.Background()
	s, db := newTestDB(t)
	f := &Feature{db: db, now: time.Now}
	now := time.Now()

	postID, err := NewPostStore(db).Create(ctx, "keep", "")
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, "INSERT INTO cms_credentials (id, public_key, alg, label, created_at) VALUES (x'01', x'02', -7, 'key', ?)", now.Unix())
	mustExec(t, db, "INSERT INTO cms_admin_sessions VALUES ('active', ?, ?, ?)", now.Unix(), now.Unix(), now.Add(time.Hour).Unix())
	mustExec(t, db, "INSERT INTO cms_admin_sessions VALUES ('idle', ?, ?, ?)", now.Unix(), now.Add(-3*time.Hour).Unix(), now.Add(time.Hour).Unix())
	mustExec(t, db, "INSERT INTO cms_admin_sessions VALUES ('expired', ?, ?, ?)", now.Unix(), now.Unix(), now.Add(-time.Second).Unix())
	mustExec(t, db, "INSERT INTO cms_setup_tokens VALUES ('valid', ?, NULL)", now.Add(time.Minute).Unix())
	mustExec(t, db, "INSERT INTO cms_setup_tokens VALUES ('used', ?, ?)", now.Add(time.Minute).Unix(), now.Unix())
	mustExec(t, db, "INSERT INTO cms_setup_tokens VALUES ('old', ?, NULL)", now.Add(-time.Minute).Unix())

	if err := f.Purge(ctx); err != nil {
		t.Fatal(err)
	}
	assertRows(t, db, "SELECT COUNT(*) FROM cms_admin_sessions", 1)
	assertRows(t, db, "SELECT COUNT(*) FROM cms_admin_sessions WHERE token_hash = 'active'", 1)
	assertRows(t, db, "SELECT COUNT(*) FROM cms_setup_tokens", 1)
	assertRows(t, db, "SELECT COUNT(*) FROM cms_setup_tokens WHERE token_hash = 'valid'", 1)
	assertRows(t, db, "SELECT COUNT(*) FROM cms_credentials", 1)

	// Coreの匿名セッションの破棄・GCは、CMSのデータに影響しない
	owner := domain.OwnerID("owner-x")
	if err := s.CreateSession(ctx, "core-hash", owner); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteAllData(ctx, owner); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PurgeExpiredSessions(ctx, time.Nanosecond); err != nil {
		t.Fatal(err)
	}
	if _, err := NewPostStore(db).Get(ctx, postID); err != nil {
		t.Fatal("Core session deletion must not delete CMS posts")
	}
	assertRows(t, db, "SELECT COUNT(*) FROM cms_credentials", 1)
}

func mustExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
}

func assertRows(t *testing.T, db *sql.DB, query string, want int) {
	t.Helper()
	var got int
	if err := db.QueryRow(query).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("%s = %d, want %d", query, got, want)
	}
}
