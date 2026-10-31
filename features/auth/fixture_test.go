package auth

import (
	"bytes"
	"context"
	"database/sql"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"minimal-web-kit/features"
	"minimal-web-kit/internal/store"
	"minimal-web-kit/internal/web"
)

// newTestDB はCoreと同じ設定のSQLiteを開き、管理者認証のマイグレーションを適用します。
func newTestDB(t *testing.T) (*store.SQLiteStore, *sql.DB) {
	t.Helper()
	s, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := Migrate(s.DB()); err != nil {
		t.Fatal(err)
	}
	return s, s.DB()
}

// notesFeature は、管理者認証の口（features.Admin）を使う機能の見本です（テスト用）。
type notesFeature struct {
	admin features.Admin
	saved []string
}

func (n *notesFeature) Name() string                                              { return "notes" }
func (n *notesFeature) Init(db *sql.DB) error                                     { return nil }
func (n *notesFeature) RegisterRoutes(mux *http.ServeMux, sm *web.SessionManager) {}
func (n *notesFeature) SetAdmin(a features.Admin) error                           { n.admin = a; return nil }
func (n *notesFeature) AdminNavLinks() []web.NavLink {
	return []web.NavLink{{Label: "メモの管理", Href: "/admin/notes"}}
}
func (n *notesFeature) RegisterIsolatedRoutes(mux *web.IsolatedMux) {
	mux.HandleFunc("GET /admin/notes", n.admin.Page(func(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
		data := s.PageData("メモの管理", "/admin/notes")
		_, _ = io.WriteString(w, "notes page csrf="+s.CSRFToken()+" nav="+data["AdminNav"].([]web.NavLink)[0].Href)
	}))
	mux.HandleFunc("GET /admin/notes/settings", n.admin.AdminPage(func(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
		_, _ = io.WriteString(w, "settings for "+string(s.Role()))
	}))
	mux.HandleFunc("POST /admin/notes/settings", n.admin.AdminForm(func(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
		_, _ = io.WriteString(w, "settings saved")
	}))
	mux.HandleFunc("POST /admin/notes", n.admin.Form(func(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
		n.saved = append(n.saved, r.PostForm.Get("text"))
		_, _ = io.WriteString(w, "saved")
	}))
}

type fixture struct {
	feature *Feature
	notes   *notesFeature
	db      *sql.DB
	routes  http.Handler
	logs    *strings.Builder
	// withRoutes は、経路を足した新しいハンドラを作ります。
	withRoutes func(extra func(*web.IsolatedMux)) (http.Handler, error)
}

// newFixture は、Coreのルーティングに管理者認証と、それを使う見本の機能を組み込んだ状態を作ります。
// 起動処理（internal/app）と同じ順で、管理者認証と管理画面のナビを配ります。
func newFixture(t *testing.T) *fixture {
	t.Helper()
	t.Setenv("AUTH_ORIGIN", "")
	t.Setenv("AUTH_RP_ID", "")
	sqlite, db := newTestDB(t)
	logs := &strings.Builder{}
	logger := slog.New(slog.NewTextHandler(logs, nil))
	f := NewFeature(logger)
	if err := f.Init(db); err != nil {
		t.Fatal(err)
	}
	notes := &notesFeature{}
	f.SetAdminNavLinks(notes.AdminNavLinks())
	if err := notes.SetAdmin(f.Admin()); err != nil {
		t.Fatal(err)
	}
	sm := web.NewSessionManager(sqlite, false)
	withRoutes := func(extra func(*web.IsolatedMux)) (http.Handler, error) {
		h, err := web.NewHandler(sqlite, sm, logger)
		if err != nil {
			return nil, err
		}
		return h.RoutesWithIsolated(nil, nil, func(mux *web.IsolatedMux) {
			f.RegisterIsolatedRoutes(mux)
			notes.RegisterIsolatedRoutes(mux)
			extra(mux)
		})
	}
	routes, err := withRoutes(func(*web.IsolatedMux) {})
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{feature: f, notes: notes, db: db, routes: routes, logs: logs, withRoutes: withRoutes}
}

func (fx *fixture) do(t *testing.T, method, target string, headers map[string]string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	fx.routes.ServeHTTP(rec, req)
	return rec
}

// postForm は管理画面のフォーム送信を再現します。site が空なら Sec-Fetch-Site を送りません。
func (fx *fixture) postForm(t *testing.T, path, body, site string, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if site != "" {
		req.Header.Set("Sec-Fetch-Site", site)
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	fx.routes.ServeHTTP(rec, req)
	return rec
}

// userID は、アカウントの WebAuthn の user.id（ログイン時の userHandle）を返します。
func (fx *fixture) userID(t *testing.T, accountID int64) []byte {
	t.Helper()
	var id []byte
	if err := fx.db.QueryRow("SELECT webauthn_user_id FROM auth_accounts WHERE id = ?", accountID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
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

// Purgeは期限切れの管理者セッションと使用済み・期限切れトークンだけを削除し、パスキーを残すこと。
// CoreのセッションGCとセッション破棄でも、管理者認証のデータが消えないこと。
// 不変条件: AUTH-INV-16
func TestPurge_KeepsPasskeysAndCoreGCIndependent(t *testing.T) {
	ctx := context.Background()
	s, db := newTestDB(t)
	f := &Feature{db: db, now: time.Now}
	now := time.Now()

	mustExec(t, db, "INSERT INTO auth_accounts (id, name, role, webauthn_user_id, created_at) VALUES (1, '管理者', 'admin', x'00', ?)", now.Unix())
	mustExec(t, db, "INSERT INTO auth_credentials (id, account_id, public_key, alg, label, created_at) VALUES (x'01', 1, x'02', -7, 'key', ?)", now.Unix())
	session := "INSERT INTO auth_sessions (token_hash, account_id, created_at, last_seen_at, expires_at) VALUES (?, 1, ?, ?, ?)"
	mustExec(t, db, session, "active", now.Unix(), now.Unix(), now.Add(time.Hour).Unix())
	mustExec(t, db, session, "idle", now.Unix(), now.Add(-3*time.Hour).Unix(), now.Add(time.Hour).Unix())
	mustExec(t, db, session, "expired", now.Unix(), now.Unix(), now.Add(-time.Second).Unix())
	token := "INSERT INTO auth_setup_tokens (token_hash, account_id, expires_at, used_at) VALUES (?, 1, ?, ?)"
	mustExec(t, db, token, "valid", now.Add(time.Minute).Unix(), nil)
	mustExec(t, db, token, "used", now.Add(time.Minute).Unix(), now.Unix())
	mustExec(t, db, token, "old", now.Add(-time.Minute).Unix(), nil)

	if err := f.Purge(ctx); err != nil {
		t.Fatal(err)
	}
	assertRows(t, db, "SELECT COUNT(*) FROM auth_sessions", 1)
	assertRows(t, db, "SELECT COUNT(*) FROM auth_sessions WHERE token_hash = 'active'", 1)
	assertRows(t, db, "SELECT COUNT(*) FROM auth_setup_tokens", 1)
	assertRows(t, db, "SELECT COUNT(*) FROM auth_setup_tokens WHERE token_hash = 'valid'", 1)
	assertRows(t, db, "SELECT COUNT(*) FROM auth_credentials", 1)

	// Coreの匿名セッションの破棄・GCは、管理者認証のデータに影響しない
	owner := store.OwnerID("owner-x")
	if err := s.CreateSession(ctx, "core-hash", owner); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteAllData(ctx, owner); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PurgeExpiredSessions(ctx, time.Nanosecond); err != nil {
		t.Fatal(err)
	}
	assertRows(t, db, "SELECT COUNT(*) FROM auth_credentials", 1)
	assertRows(t, db, "SELECT COUNT(*) FROM auth_accounts", 1)
}

func mustExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
}
