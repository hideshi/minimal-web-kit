package oidcop

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"database/sql"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"minimal-web-kit/features/auth"
	"minimal-web-kit/features/auth/authtest"
	"minimal-web-kit/internal/store"
)

func TestValidAuthorizeQuery(t *testing.T) {
	base := func() url.Values {
		return url.Values{
			"scope": {"openid"}, "response_type": {"code"}, "state": {strings.Repeat("s", 16)}, "nonce": {strings.Repeat("n", 16)},
			"code_challenge": {strings.Repeat("A", 43)}, "code_challenge_method": {"S256"},
			"client_id": {"c"}, "redirect_uri": {"https://b.example/cb"},
		}
	}
	if !validAuthorizeQuery(base()) {
		t.Fatal("valid query rejected")
	}
	for name, change := range map[string]func(url.Values){
		"no challenge":    func(q url.Values) { q.Del("code_challenge") },
		"no method":       func(q url.Values) { q.Del("code_challenge_method") },
		"plain":           func(q url.Values) { q.Set("code_challenge_method", "plain") },
		"short challenge": func(q url.Values) { q.Set("code_challenge", "abc") },
		"padded":          func(q url.Values) { q.Set("code_challenge", strings.Repeat("A", 42)+"=") },
		"not canonical":   func(q url.Values) { q.Set("code_challenge", strings.Repeat("A", 42)+"B") },
		"short state":     func(q url.Values) { q.Set("state", "s") },
		"long nonce":      func(q url.Values) { q.Set("nonce", strings.Repeat("n", 257)) },
		"other scope":     func(q url.Values) { q.Set("scope", "openid profile") },
		"token flow":      func(q url.Values) { q.Set("response_type", "token") },
		"fragment mode":   func(q url.Values) { q.Set("response_mode", "fragment") },
		"max_age":         func(q url.Values) { q.Set("max_age", "0") },
		"repeated":        func(q url.Values) { q.Add("client_id", "d") },
	} {
		q := base()
		change(q)
		if validAuthorizeQuery(q) {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestBufferedResponse_HoldsUntilSent(t *testing.T) {
	r := newBufferedResponse()
	r.Header().Set("Location", "x")
	r.WriteHeader(302)
	_, _ = r.Write([]byte("body"))
	if r.status != 302 || r.body.String() != "body" || r.Header().Get("Location") != "x" {
		t.Fatalf("%+v", r)
	}
}

type exchangeFixture struct {
	f        *Feature
	db       *sql.DB
	client   auth.FederationClient
	secret   string
	verifier string
	session  string
	code     string
}

func privateFile(t *testing.T, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// newExchangeFixture は、コードを発行済みの状態を作ります。
func newExchangeFixture(t *testing.T) *exchangeFixture {
	t.Helper()
	t.Setenv("AUTH_ORIGIN", "https://a.example")
	t.Setenv("AUTH_RP_ID", "a.example")
	t.Setenv("SECURE_COOKIE", "true")
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	s, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "a.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	f := NewWithConfig(slog.New(slog.NewTextHandler(io.Discard, nil)), Config{
		Issuer:     "https://a.example",
		KeyFile:    privateFile(t, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})),
		CryptoFile: privateFile(t, make([]byte, 32)),
	})
	if err := f.Init(s.DB()); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	client, secret, err := f.store.CreateClient(ctx, "アプリB", "https://b.example/cb")
	if err != nil {
		t.Fatal(err)
	}
	cookie, account := authtest.NewAccountSession(t, s.DB(), "member")
	if _, err := f.store.Grant(ctx, client.ID, account); err != nil {
		t.Fatal(err)
	}
	verifier := strings.Repeat("v", 43)
	sum := sha256.Sum256([]byte(verifier))
	if err := f.store.NewRequest(ctx, "flow", auth.FederationRequest{
		ID: "request", Client: client.ID, Redirect: client.Redirect, State: "state", Nonce: "nonce",
		Challenge: base64.RawURLEncoding.EncodeToString(sum[:]),
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Bind(ctx, "request", cookie.Value); err != nil {
		t.Fatal(err)
	}
	if err := f.store.SaveCode(ctx, "request", "the-code"); err != nil {
		t.Fatal(err)
	}
	return &exchangeFixture{f: f, db: s.DB(), client: client, secret: secret, verifier: verifier, session: cookie.Value, code: "the-code"}
}

func (x *exchangeFixture) request() *http.Request {
	form := url.Values{"grant_type": {"authorization_code"}, "code": {x.code}, "redirect_uri": {x.client.Redirect}, "code_verifier": {x.verifier}}
	r := httptest.NewRequest(http.MethodPost, "/oidc/token", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.SetBasicAuth(url.QueryEscape(x.client.ID), url.QueryEscape(x.secret))
	return r
}

func (x *exchangeFixture) consumed(t *testing.T) bool {
	t.Helper()
	var at int64
	if err := x.db.QueryRow("SELECT consumed_at FROM auth_oidc_requests WHERE request_id = 'request'").Scan(&at); err != nil {
		t.Fatal(err)
	}
	return at > 0
}

// 署名に失敗したら成功の応答を送らず、コードの消費も巻き戻すこと。
//
// 不変条件: AUTH-INV-18
func TestToken_SigningFailureRollsBack(t *testing.T) {
	x := newExchangeFixture(t)
	good := x.f.sdk.signing.key
	wrong, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	x.f.sdk.signing.key = wrong
	w := httptest.NewRecorder()
	x.f.token(w, x.request())
	if w.Code == http.StatusOK || strings.Contains(w.Body.String(), "id_token") {
		t.Fatalf("failed signing returned %d %s", w.Code, w.Body.String())
	}
	if x.consumed(t) {
		t.Fatal("the code stayed consumed after a failed exchange")
	}
	x.f.sdk.signing.key = good
	w = httptest.NewRecorder()
	x.f.token(w, x.request())
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "id_token") {
		t.Fatalf("retry: %d %s", w.Code, w.Body.String())
	}
}

type brokenWriter struct{ header http.Header }

func (b *brokenWriter) Header() http.Header       { return b.header }
func (b *brokenWriter) WriteHeader(int)           {}
func (b *brokenWriter) Write([]byte) (int, error) { return 0, errors.New("connection lost") }

// 応答の送信に失敗しても、同じコードは再利用させないこと。
//
// 不変条件: AUTH-INV-18
func TestToken_SendFailureKeepsCodeConsumed(t *testing.T) {
	x := newExchangeFixture(t)
	x.f.token(&brokenWriter{header: http.Header{}}, x.request())
	if !x.consumed(t) {
		t.Fatal("the code can be exchanged again after the response was lost")
	}
	w := httptest.NewRecorder()
	x.f.token(w, x.request())
	if w.Code != http.StatusBadRequest {
		t.Fatalf("second exchange: %d", w.Code)
	}
}

// 期限の切れたコードは、束縛が合っていても受け付けず、世代も変えないこと。
//
// 不変条件: AUTH-INV-18
func TestToken_ExpiredCode(t *testing.T) {
	x := newExchangeFixture(t)
	if _, err := x.db.Exec("UPDATE auth_oidc_requests SET code_expires_at = 1"); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	x.f.token(w, x.request())
	if w.Code != http.StatusBadRequest || x.consumed(t) {
		t.Fatalf("expired code: %d consumed=%v", w.Code, x.consumed(t))
	}
}

// 発行の後で世代が進んだら、行が残っていても、コードを交換しないこと。
//
// 不変条件: AUTH-INV-17, AUTH-INV-18
func TestToken_ChangedEpochRejected(t *testing.T) {
	x := newExchangeFixture(t)
	if _, err := x.db.Exec("UPDATE auth_oidc_grants SET grant_epoch = grant_epoch + 1"); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	x.f.token(w, x.request())
	if w.Code != http.StatusBadRequest || x.consumed(t) {
		t.Fatalf("exchange after the epoch changed: %d consumed=%v", w.Code, x.consumed(t))
	}
}

// 「ログインする」の二度押しでは、後のコードだけが使え、前のコードは使えないこと。
//
// 不変条件: AUTH-INV-18
func TestApprove_SecondCodeReplacesFirst(t *testing.T) {
	x := newExchangeFixture(t)
	ctx := context.Background()
	if err := x.f.store.Bind(ctx, "request", x.session); err != nil {
		t.Fatal(err)
	}
	if err := x.f.store.SaveCode(ctx, "request", "second-code"); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	x.f.token(w, x.request())
	if w.Code != http.StatusBadRequest {
		t.Fatalf("first code: %d", w.Code)
	}
	x.code = "second-code"
	w = httptest.NewRecorder()
	x.f.token(w, x.request())
	if w.Code != http.StatusOK {
		t.Fatalf("second code: %d %s", w.Code, w.Body.String())
	}
}
