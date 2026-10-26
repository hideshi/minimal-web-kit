// Package authtest は、管理者認証（features/auth）を使う機能のテストのための道具です。
// パスキーの操作を省いて管理者セッションを作り、管理画面のCSRFトークンを取り出します。本番のコードでは使いません。
package authtest

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"
	"time"
)

// CookieName は管理者セッションのCookie名です。
const CookieName = "admin_session"

// NewSession は、管理者セッションをDBに直接作り、そのCookieを返します（管理者認証のマイグレーション適用後に使う）。
func NewSession(t testing.TB, db *sql.DB) *http.Cookie {
	t.Helper()
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	token := hex.EncodeToString(raw)
	sum := sha256.Sum256([]byte(token))
	now := time.Now()
	if _, err := db.Exec(
		"INSERT INTO auth_sessions (token_hash, created_at, last_seen_at, expires_at) VALUES (?, ?, ?, ?)",
		hex.EncodeToString(sum[:]), now.Unix(), now.Unix(), now.Add(12*time.Hour).Unix(),
	); err != nil {
		t.Fatal(err)
	}
	return &http.Cookie{Name: CookieName, Value: token}
}

var csrfPattern = regexp.MustCompile(`name="csrf_token" value="([0-9a-f]+)"`)

// CSRFToken は、ログイン中に page を開いて、管理画面に埋め込まれたCSRFトークンを取り出します。
func CSRFToken(t testing.TB, routes http.Handler, page string, cookie *http.Cookie) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, page, nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	routes.ServeHTTP(rec, req)
	m := csrfPattern.FindStringSubmatch(rec.Body.String())
	if m == nil {
		t.Fatalf("no CSRF token in %s (status %d)", page, rec.Code)
	}
	return m[1]
}
