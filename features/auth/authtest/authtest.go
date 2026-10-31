// Package authtest は、管理者認証（features/auth）を使う機能のテストのための道具です。
// パスキーの操作を省いて管理者セッションを作り、管理画面のCSRFトークンを取り出します。本番のコードでは使いません。
package authtest

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"
	"time"
)

// CookieName は管理者セッションのCookie名です。
const CookieName = "admin_session"

// NewSession は、ロールがシステム管理者のアカウントと、その管理者セッションをDBに直接作り、Cookieを返します
// （管理者認証のマイグレーション適用後に使う）。
func NewSession(t testing.TB, db *sql.DB) *http.Cookie {
	t.Helper()
	return NewSessionWithRole(t, db, "admin")
}

// NewSessionWithRole は、指定したロール（"admin" または "member"）のアカウントと、その管理者セッションを作ります。
func NewSessionWithRole(t testing.TB, db *sql.DB, role string) *http.Cookie {
	t.Helper()
	cookie, _ := NewAccountSession(t, db, role)
	return cookie
}

// NewAccountSession は NewSessionWithRole と同じものを作り、Cookieに加えてアカウントIDを返します。
func NewAccountSession(t testing.TB, db *sql.DB, role string) (*http.Cookie, int64) {
	t.Helper()
	now := time.Now()
	res, err := db.Exec(
		"INSERT INTO auth_accounts (name, role, webauthn_user_id, created_at) VALUES (?, ?, ?, ?)",
		"テスト用-"+randomHex(t, 8), role, []byte(randomHex(t, 8)), now.Unix())
	if err != nil {
		t.Fatal(err)
	}
	accountID, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	token := randomHex(t, 32)
	sum := sha256.Sum256([]byte(token))
	if _, err := db.Exec(
		"INSERT INTO auth_sessions (token_hash, account_id, created_at, last_seen_at, expires_at) VALUES (?, ?, ?, ?, ?)",
		hex.EncodeToString(sum[:]), accountID, now.Unix(), now.Unix(), now.Add(12*time.Hour).Unix(),
	); err != nil {
		t.Fatal(err)
	}
	return &http.Cookie{Name: CookieName, Value: token}, accountID
}

// NewCredential は、アカウントに見本のパスキーをDBに直接登録し、経路に使う識別子（credential IDのbase64url）を返します。
// 公開鍵は形だけの値で、このパスキーではログインできません。画面の表示を確かめるために使います。
func NewCredential(t testing.TB, db *sql.DB, accountID int64, label string) string {
	t.Helper()
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		"INSERT INTO auth_credentials (id, account_id, public_key, alg, label, created_at) VALUES (?, ?, ?, ?, ?, ?)",
		id, accountID, []byte{0}, -7, label, time.Now().Unix(),
	); err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(id)
}

func randomHex(t testing.TB, n int) string {
	t.Helper()
	raw := make([]byte, n)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(raw)
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
