package test

// 共通認証の結合テストです。提供側Aと利用側Bを、別のHTTPSサーバー・別のDBで動かし、
// 実際のOIDCの往復（認可 → 本人確認 → コード交換 → IDトークンの検証 → 毎要求の照会）を通します。
// パスキーの操作は test/browser で実際のブラウザで確かめます。ここでは管理者セッションをDBに直接作ります。

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"minimal-web-kit/features"
	"minimal-web-kit/features/auth"
	"minimal-web-kit/features/auth/authtest"
	"minimal-web-kit/features/auth/oidcop"
	"minimal-web-kit/features/federatedauth"
	"minimal-web-kit/internal/app"
	"minimal-web-kit/internal/store"
	"minimal-web-kit/internal/web"
)

// safeBuffer は、並行する要求のログを集めます。
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// fedNotes は、Bの管理画面の見本です。features.Admin が差し替わっても動くことを確かめます。
type fedNotes struct {
	admin features.Admin
	saved atomic.Int64
}

func (n *fedNotes) Name() string                                              { return "fednotes" }
func (n *fedNotes) Init(db *sql.DB) error                                     { return nil }
func (n *fedNotes) RegisterRoutes(mux *http.ServeMux, sm *web.SessionManager) {}
func (n *fedNotes) SetAdmin(a features.Admin) error                           { n.admin = a; return nil }
func (n *fedNotes) RegisterIsolatedRoutes(mux *web.IsolatedMux) {
	mux.HandleFunc("GET /admin/notes", n.admin.Page(func(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
		fmt.Fprintf(w, `notes role=%s csrf=<input name="csrf_token" value="%s">`, s.Role(), s.CSRFToken())
	}))
	mux.HandleFunc("POST /admin/notes", n.admin.Form(func(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
		n.saved.Add(1)
		fmt.Fprint(w, "saved")
	}))
	mux.HandleFunc("GET /admin/notes/settings", n.admin.AdminPage(func(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
		fmt.Fprint(w, "settings")
	}))
}

type fedServer struct {
	origin  string
	db      *sql.DB
	server  *httptest.Server
	logs    *safeBuffer
	down    atomic.Bool // true の間、すべての要求に500を返す
	noCheck atomic.Bool // true の間、照会にだけ500を返す
}

func newFedListener(t *testing.T) (net.Listener, string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return ln, fmt.Sprintf("https://localhost:%d", ln.Addr().(*net.TCPAddr).Port)
}

func startFedServer(t *testing.T, ln net.Listener, origin string, s *store.SQLiteStore, reg *features.Registry, logs *safeBuffer) *fedServer {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	for _, f := range reg.All() {
		if err := f.Init(s.DB()); err != nil {
			t.Fatalf("init %s: %v", f.Name(), err)
		}
	}
	routes, err := app.BuildRoutes(s, reg, true, logger)
	if err != nil {
		t.Fatal(err)
	}
	fs := &fedServer{origin: origin, db: s.DB(), logs: logs}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fs.down.Load() || (fs.noCheck.Load() && r.URL.Path == "/oidc/check") {
			http.Error(w, "down", http.StatusInternalServerError)
			return
		}
		routes.ServeHTTP(w, r)
	}))
	_ = srv.Listener.Close()
	srv.Listener = ln
	srv.StartTLS()
	t.Cleanup(srv.Close)
	fs.server = srv
	return fs
}

// client は、テストの自己署名の証明書を信頼するクライアントです（サーバー間の通信に使う）。
func (s *fedServer) client() *http.Client {
	return &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
}

// fedPair は、1組のA・Bと、Bの見本の画面です。
type fedPair struct {
	a, b     *fedServer
	notes    *fedNotes
	clientID string
	secret   string
	store    *auth.FederationStore
	redirect string
}

func writeSecretFile(t *testing.T, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func newSigningKeyFile(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return writeSecretFile(t, "signing.pem", pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}))
}

func newFedPair(t *testing.T) *fedPair {
	t.Helper()
	aListener, aOrigin := newFedListener(t)
	bListener, bOrigin := newFedListener(t)
	t.Setenv("AUTH_ORIGIN", aOrigin)
	t.Setenv("AUTH_RP_ID", "localhost")
	t.Setenv("SECURE_COOKIE", "true")

	crypto := make([]byte, 32)
	if _, err := rand.Read(crypto); err != nil {
		t.Fatal(err)
	}
	aStore, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "a.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { aStore.Close() })
	aLogs := &safeBuffer{}
	aRegistry := features.NewRegistry()
	aRegistry.Register(oidcop.NewWithConfig(slog.New(slog.NewTextHandler(aLogs, nil)), oidcop.Config{
		Issuer: aOrigin, KeyFile: newSigningKeyFile(t), CryptoFile: writeSecretFile(t, "crypto", crypto),
	}))
	a := startFedServer(t, aListener, aOrigin, aStore, aRegistry, aLogs)

	fed := auth.NewFederationStore(aStore.DB())
	redirect := bOrigin + "/admin/sso/callback"
	client, secret, err := fed.CreateClient(context.Background(), "業務アプリB", redirect)
	if err != nil {
		t.Fatal(err)
	}

	bStore, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "b.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bStore.Close() })
	bLogs := &safeBuffer{}
	notes := &fedNotes{}
	bRegistry := features.NewRegistry()
	bRegistry.Register(federatedauth.NewWithConfig(slog.New(slog.NewTextHandler(bLogs, nil)), federatedauth.Config{
		Origin: bOrigin, Issuer: aOrigin, ClientID: client.ID,
		SecretFile: writeSecretFile(t, "client-secret", []byte(secret)),
		Transport:  &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
	}))
	bRegistry.Register(notes)
	b := startFedServer(t, bListener, bOrigin, bStore, bRegistry, bLogs)
	return &fedPair{a: a, b: b, notes: notes, clientID: client.ID, secret: secret, store: fed, redirect: redirect}
}

// fedBrowser はブラウザの代わりです。転送を追わず、同一オリジンのPOSTにはFetch Metadataを付けます。
type fedBrowser struct {
	t      *testing.T
	client *http.Client
}

func newFedBrowser(t *testing.T) *fedBrowser {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &fedBrowser{t: t, client: &http.Client{
		Jar:           jar,
		Transport:     &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (b *fedBrowser) do(method, target string, form url.Values) (*http.Response, string) {
	b.t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequest(method, target, body)
	if err != nil {
		b.t.Fatal(err)
	}
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		req.Header.Set("Origin", "null")
	}
	res, err := b.client.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		b.t.Fatal(err)
	}
	return res, string(raw)
}

func (b *fedBrowser) setCookie(origin string, c *http.Cookie) {
	u, err := url.Parse(origin)
	if err != nil {
		b.t.Fatal(err)
	}
	b.client.Jar.SetCookies(u, []*http.Cookie{{Name: c.Name, Value: c.Value, Path: "/admin", Secure: true}})
}

var (
	csrfField = regexp.MustCompile(`name="csrf_token" value="([^"]+)"`)
	nextLink  = regexp.MustCompile(`href="(https://[^"]+)" class="btn btn-primary"`)
)

func relayTarget(t *testing.T, res *http.Response, body string) string {
	t.Helper()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("relay page: %d %s", res.StatusCode, body)
	}
	match := nextLink.FindStringSubmatch(body)
	if match == nil {
		t.Fatalf("relay page has no link: %s", body)
	}
	target := html.UnescapeString(match[1])
	if got := res.Header.Get("Refresh"); got != "0; url="+target {
		t.Fatalf("Refresh header %q does not match link %q", got, target)
	}
	return target
}

// beginLogin は、Bのログインを始めて認可要求のURLを返します。
func (p *fedPair) beginLogin(t *testing.T, br *fedBrowser) string {
	t.Helper()
	res, body := br.do(http.MethodGet, p.b.origin+"/admin/login", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("login page: %d", res.StatusCode)
	}
	res, body = br.do(http.MethodPost, p.b.origin+"/admin/sso/start", url.Values{"next": {"/admin/notes"}})
	return relayTarget(t, res, body)
}

// approveAtA は、Aの継続ページで「ログインする」を押して、Bのコールバックのurlを返します。
func (p *fedPair) approveAtA(t *testing.T, br *fedBrowser, authURL string) string {
	t.Helper()
	res, body := br.do(http.MethodGet, authURL, nil)
	if res.StatusCode != http.StatusOK || !strings.Contains(body, "/admin/oidc/continue") {
		t.Fatalf("authorize: %d %s", res.StatusCode, body)
	}
	res, body = br.do(http.MethodGet, p.a.origin+"/admin/oidc/continue", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("continue: %d %s", res.StatusCode, body)
	}
	match := csrfField.FindStringSubmatch(body)
	if match == nil {
		t.Fatalf("continue has no csrf: %s", body)
	}
	res, body = br.do(http.MethodPost, p.a.origin+"/admin/oidc/approve", url.Values{"csrf_token": {match[1]}, "decision": {"allow"}})
	return relayTarget(t, res, body)
}

// login は、Aのログイン済みのブラウザで、Bへのログインを完了します。
func (p *fedPair) login(t *testing.T, br *fedBrowser) {
	t.Helper()
	callback := p.approveAtA(t, br, p.beginLogin(t, br))
	res, _ := br.do(http.MethodGet, callback, nil)
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/admin/notes" {
		t.Fatalf("callback: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	res, _ = br.do(http.MethodGet, p.b.origin+"/admin/notes", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("B after login: %d", res.StatusCode)
	}
}

// member は、Aのアカウントとセッション、Bの利用許可を用意したブラウザを返します。
func (p *fedPair) member(t *testing.T) (*fedBrowser, int64, string) {
	t.Helper()
	cookie, id := authtest.NewAccountSession(t, p.a.db, "member")
	sub, err := p.store.Grant(context.Background(), p.clientID, id)
	if err != nil {
		t.Fatal(err)
	}
	br := newFedBrowser(t)
	br.setCookie(p.a.origin, cookie)
	return br, id, sub
}

func (p *fedPair) status(t *testing.T, br *fedBrowser) int {
	t.Helper()
	res, _ := br.do(http.MethodGet, p.b.origin+"/admin/notes", nil)
	return res.StatusCode
}

func countRows(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// 実際の往復でBにログインでき、AのロールをコピーせずBのメンバーになること。停止すると次の要求から拒否されること。
//
// 不変条件: AUTH-INV-17, FEDAUTH-INV-1, FEDAUTH-INV-3, FEDAUTH-INV-4
// ユースケース: UC-FEDAUTH-2
func TestFederatedAuth_LoginThenStopRejectsNextRequest(t *testing.T) {
	p := newFedPair(t)
	adminCookie, adminID := authtest.NewAccountSession(t, p.a.db, "admin")
	if _, err := p.store.Grant(context.Background(), p.clientID, adminID); err != nil {
		t.Fatal(err)
	}
	br := newFedBrowser(t)
	br.setCookie(p.a.origin, adminCookie)
	p.login(t, br)

	res, body := br.do(http.MethodGet, p.b.origin+"/admin/notes", nil)
	if !strings.Contains(body, "role=member") {
		t.Fatalf("Aの管理者がBの管理者になった: %s", body)
	}
	if res, _ = br.do(http.MethodGet, p.b.origin+"/admin/notes/settings", nil); res.StatusCode != http.StatusForbidden {
		t.Fatalf("member at admin page: %d", res.StatusCode)
	}
	csrf := csrfField.FindStringSubmatch(body)[1]
	if res, _ = br.do(http.MethodPost, p.b.origin+"/admin/notes", url.Values{"csrf_token": {csrf}}); res.StatusCode != http.StatusOK {
		t.Fatalf("post: %d", res.StatusCode)
	}

	if _, err := p.a.db.Exec("UPDATE auth_accounts SET disabled_at = ? WHERE id = ?", time.Now().Unix(), adminID); err != nil {
		t.Fatal(err)
	}
	saved := p.notes.saved.Load()
	res, _ = br.do(http.MethodPost, p.b.origin+"/admin/notes", url.Values{"csrf_token": {csrf}})
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/admin/login" {
		t.Fatalf("stopped POST: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	if p.notes.saved.Load() != saved {
		t.Error("stopped user changed data")
	}
	if got := countRows(t, p.b.db, "SELECT COUNT(*) FROM federated_sessions"); got != 0 {
		t.Errorf("sessions after stop: %d", got)
	}
}

// 失効の各操作で、Bの既存のログインが拒否され、操作を戻しても復活しないこと。
//
// 不変条件: AUTH-INV-17, FEDAUTH-INV-1
// ユースケース: UC-FEDAUTH-3
func TestFederatedAuth_RevocationsDoNotRevive(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name   string
		revoke func(t *testing.T, p *fedPair, account int64)
		revive func(t *testing.T, p *fedPair, account int64)
	}{
		{"account stop and restart",
			func(t *testing.T, p *fedPair, id int64) {
				mustExecDB(t, p.a.db, "UPDATE auth_accounts SET disabled_at = 1 WHERE id = ?", id)
			},
			func(t *testing.T, p *fedPair, id int64) {
				mustExecDB(t, p.a.db, "UPDATE auth_accounts SET disabled_at = NULL WHERE id = ?", id)
			}},
		{"grant revoke and regrant",
			func(t *testing.T, p *fedPair, id int64) {
				if err := p.store.Revoke(ctx, p.clientID, id); err != nil {
					t.Fatal(err)
				}
			},
			func(t *testing.T, p *fedPair, id int64) {
				if _, err := p.store.Grant(ctx, p.clientID, id); err != nil {
					t.Fatal(err)
				}
			}},
		{"client disable and enable",
			func(t *testing.T, p *fedPair, id int64) {
				if err := p.store.SetClientEnabled(ctx, p.clientID, false); err != nil {
					t.Fatal(err)
				}
			},
			func(t *testing.T, p *fedPair, id int64) {
				if err := p.store.SetClientEnabled(ctx, p.clientID, true); err != nil {
					t.Fatal(err)
				}
			}},
		{"revoke all",
			func(t *testing.T, p *fedPair, id int64) {
				if err := p.store.RevokeAll(ctx); err != nil {
					t.Fatal(err)
				}
			},
			func(t *testing.T, p *fedPair, id int64) {}},
		{"key reset",
			func(t *testing.T, p *fedPair, id int64) {
				mustExecDB(t, p.a.db, "UPDATE auth_accounts SET webauthn_user_id = ? WHERE id = ?", []byte("new-user-id"), id)
			},
			func(t *testing.T, p *fedPair, id int64) {}},
		{"clone suspicion",
			func(t *testing.T, p *fedPair, id int64) {
				authtest.NewCredential(t, p.a.db, id, "端末")
				mustExecDB(t, p.a.db, "UPDATE auth_credentials SET disabled_at = 1 WHERE account_id = ?", id)
			},
			func(t *testing.T, p *fedPair, id int64) {}},
		{"recover one account",
			func(t *testing.T, p *fedPair, id int64) {
				if _, err := auth.Recover(ctx, p.a.db, time.Now(), auth.RecoverOptions{AccountID: id, Only: true}); err != nil {
					t.Fatal(err)
				}
			},
			func(t *testing.T, p *fedPair, id int64) {}},
		{"recover everyone",
			func(t *testing.T, p *fedPair, id int64) {
				_, other := authtest.NewAccountSession(t, p.a.db, "admin")
				if _, err := auth.Recover(ctx, p.a.db, time.Now(), auth.RecoverOptions{AccountID: other}); err != nil {
					t.Fatal(err)
				}
			},
			func(t *testing.T, p *fedPair, id int64) {}},
		{"delete account",
			func(t *testing.T, p *fedPair, id int64) {
				mustExecDB(t, p.a.db, "DELETE FROM auth_accounts WHERE id = ?", id)
			},
			func(t *testing.T, p *fedPair, id int64) {}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := newFedPair(t)
			br, id, _ := p.member(t)
			p.login(t, br)
			c.revoke(t, p, id)
			c.revive(t, p, id)
			res, _ := br.do(http.MethodGet, p.b.origin+"/admin/notes", nil)
			if res.StatusCode != http.StatusSeeOther {
				t.Fatalf("old login survived: %d", res.StatusCode)
			}
			if !strings.HasPrefix(res.Header.Get("Location"), "/admin/login") {
				t.Fatalf("location %q", res.Header.Get("Location"))
			}
			if got := countRows(t, p.b.db, "SELECT COUNT(*) FROM federated_sessions"); got != 0 {
				t.Errorf("session kept: %d", got)
			}
		})
	}
}

func mustExecDB(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}

// Aのロール変更や通常のログアウトは、Bのログインを失効させないこと。
//
// 不変条件: AUTH-INV-17
func TestFederatedAuth_LocalChangesKeepLogin(t *testing.T) {
	p := newFedPair(t)
	br, id, _ := p.member(t)
	p.login(t, br)
	mustExecDB(t, p.a.db, "UPDATE auth_accounts SET role = 'admin' WHERE id = ?", id)
	mustExecDB(t, p.a.db, "DELETE FROM auth_sessions WHERE account_id = ?", id)
	if got := p.status(t, br); got != http.StatusOK {
		t.Fatalf("B login lost: %d", got)
	}
}

// 認証基盤の障害中は503で、業務の処理をせず、セッションを残すこと。回復すれば再確認で使えること。
//
// 不変条件: FEDAUTH-INV-2
// ユースケース: UC-FEDAUTH-4
func TestFederatedAuth_UnavailableKeepsSession(t *testing.T) {
	p := newFedPair(t)
	br, _, _ := p.member(t)
	p.login(t, br)
	res, body := br.do(http.MethodGet, p.b.origin+"/admin/notes", nil)
	csrf := csrfField.FindStringSubmatch(body)[1]
	p.a.down.Store(true)
	res, _ = br.do(http.MethodPost, p.b.origin+"/admin/notes", url.Values{"csrf_token": {csrf}})
	if res.StatusCode != http.StatusServiceUnavailable || p.notes.saved.Load() != 0 {
		t.Fatalf("while down: %d saved=%d", res.StatusCode, p.notes.saved.Load())
	}
	if got := countRows(t, p.b.db, "SELECT COUNT(*) FROM federated_sessions"); got != 1 {
		t.Fatalf("session lost: %d", got)
	}
	// 回復すれば、短い遮断の後で再確認して使える
	p.a.down.Store(false)
	time.Sleep(5100 * time.Millisecond)
	if got := p.status(t, br); got != http.StatusOK {
		t.Fatalf("after recovery: %d", got)
	}
	// 認証基盤の障害中でもログアウトはできる
	p.a.down.Store(true)
	res, _ = br.do(http.MethodPost, p.b.origin+"/admin/logout", url.Values{"csrf_token": {csrf}})
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("logout while down: %d", res.StatusCode)
	}
	if cookies := res.Cookies(); len(cookies) != 1 || cookies[0].Name != "federated_session" || cookies[0].MaxAge >= 0 {
		t.Fatalf("logout must delete the cookie: %+v", cookies)
	}
	if got := countRows(t, p.b.db, "SELECT COUNT(*) FROM federated_sessions"); got != 0 {
		t.Fatalf("session after logout: %d", got)
	}
}

// exchange はコード交換の要求を送ります。
func (p *fedPair) exchange(t *testing.T, id, secret, code, redirect, verifier string) (int, string) {
	t.Helper()
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirect}, "code_verifier": {verifier}}
	req, err := http.NewRequest(http.MethodPost, p.a.origin+"/oidc/token", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if id != "" {
		req.SetBasicAuth(url.QueryEscape(id), url.QueryEscape(secret))
	}
	res, err := p.a.client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(raw)
}

// manualCode は、Bを通さずに認可要求を作り、コードと verifier を返します。
func (p *fedPair) manualCode(t *testing.T, br *fedBrowser) (code, verifier string) {
	t.Helper()
	verifier = strings.Repeat("v", 43)
	sum := sha256.Sum256([]byte(verifier))
	q := url.Values{
		"client_id": {p.clientID}, "redirect_uri": {p.redirect}, "response_type": {"code"}, "scope": {"openid"},
		"state": {strings.Repeat("s", 16)}, "nonce": {strings.Repeat("n", 16)},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"S256"},
	}
	callback := p.approveAtA(t, br, p.a.origin+"/admin/oidc/authorize?"+q.Encode())
	u, err := url.Parse(callback)
	if err != nil {
		t.Fatal(err)
	}
	return u.Query().Get("code"), verifier
}

// コードは一回限りで、認証済みの連携先が束縛の合うコードを再利用したときだけ失効すること。
// 未認証の再送・別の連携先・PKCEの不一致では、世代が変わらないこと。
//
// 不変条件: AUTH-INV-18
func TestFederatedAuth_CodeReuse(t *testing.T) {
	p := newFedPair(t)
	br, _, _ := p.member(t)
	p.login(t, br)
	code, verifier := p.manualCode(t, br)
	if status, body := p.exchange(t, p.clientID, p.secret, code, p.redirect, verifier); status != http.StatusOK {
		t.Fatalf("first exchange: %d %s", status, body)
	}
	epoch := func() int {
		return countRows(t, p.a.db, "SELECT grant_epoch FROM auth_oidc_grants WHERE client_id = ?", p.clientID)
	}
	before := epoch()
	other, otherSecret, err := p.store.CreateClient(context.Background(), "別の連携先", "https://other.example/cb")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, id, secret, redirect, verifier string
		want                                 int
	}{
		{"no credentials", "", "", p.redirect, verifier, http.StatusUnauthorized},
		{"bad secret", p.clientID, "wrong", p.redirect, verifier, http.StatusUnauthorized},
		{"other client", other.ID, otherSecret, p.redirect, verifier, http.StatusBadRequest},
		{"wrong verifier", p.clientID, p.secret, p.redirect, strings.Repeat("x", 43), http.StatusBadRequest},
		{"wrong redirect", p.clientID, p.secret, "https://evil.example/cb", verifier, http.StatusBadRequest},
	} {
		if status, _ := p.exchange(t, c.id, c.secret, code, c.redirect, c.verifier); status != c.want {
			t.Errorf("%s: status %d, want %d", c.name, status, c.want)
		}
		if got := epoch(); got != before {
			t.Fatalf("%s: grant epoch changed to %d", c.name, got)
		}
	}
	if status, _ := p.exchange(t, p.clientID, p.secret, code, p.redirect, verifier); status != http.StatusBadRequest {
		t.Fatalf("replay: %d", status)
	}
	if got := epoch(); got != before+1 {
		t.Fatalf("replay did not revoke: %d", got)
	}
	if got := p.status(t, br); got != http.StatusSeeOther {
		t.Fatalf("B login after replay: %d", got)
	}
}

// 再利用で世代が進んだ後は、別に発行済みの未交換のコードも使えないこと（世代の照合が最後の守りになる）。
//
// 不変条件: AUTH-INV-18
func TestFederatedAuth_ReplayInvalidatesPendingCodes(t *testing.T) {
	p := newFedPair(t)
	br, _, _ := p.member(t)
	code, verifier := p.manualCode(t, br)
	if status, _ := p.exchange(t, p.clientID, p.secret, code, p.redirect, verifier); status != http.StatusOK {
		t.Fatal(status)
	}
	pending, pendingVerifier := p.manualCode(t, br)
	if status, _ := p.exchange(t, p.clientID, p.secret, code, p.redirect, verifier); status != http.StatusBadRequest {
		t.Fatalf("replay: %d", status)
	}
	if status, _ := p.exchange(t, p.clientID, p.secret, pending, p.redirect, pendingVerifier); status != http.StatusBadRequest {
		t.Fatalf("pending code after the revocation: %d", status)
	}
}

// 認可要求は、S256のPKCEが無ければ、機密クライアントでも拒否すること。戻り先が違えばそこへ転送しないこと。
//
// 不変条件: AUTH-INV-18
func TestFederatedAuth_AuthorizeRejects(t *testing.T) {
	p := newFedPair(t)
	br, _, _ := p.member(t)
	sum := sha256.Sum256([]byte(strings.Repeat("v", 43)))
	base := url.Values{
		"client_id": {p.clientID}, "redirect_uri": {p.redirect}, "response_type": {"code"}, "scope": {"openid"},
		"state": {strings.Repeat("s", 16)}, "nonce": {strings.Repeat("n", 16)},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"S256"},
	}
	for name, change := range map[string]func(q url.Values){
		"no challenge":   func(q url.Values) { q.Del("code_challenge"); q.Del("code_challenge_method") },
		"plain":          func(q url.Values) { q.Set("code_challenge_method", "plain") },
		"bad challenge":  func(q url.Values) { q.Set("code_challenge", "short") },
		"no nonce":       func(q url.Values) { q.Del("nonce") },
		"other redirect": func(q url.Values) { q.Set("redirect_uri", "https://evil.example/cb") },
		"unknown client": func(q url.Values) { q.Set("client_id", "unknown") },
		"prompt":         func(q url.Values) { q.Set("prompt", "none") },
		"double state":   func(q url.Values) { q.Add("state", strings.Repeat("t", 16)) },
		"extra scope":    func(q url.Values) { q.Set("scope", "openid email") },
	} {
		q := url.Values{}
		for k, v := range base {
			q[k] = append([]string{}, v...)
		}
		change(q)
		res, _ := br.do(http.MethodGet, p.a.origin+"/admin/oidc/authorize?"+q.Encode(), nil)
		if res.StatusCode != http.StatusBadRequest || res.Header.Get("Location") != "" {
			t.Errorf("%s: %d %q", name, res.StatusCode, res.Header.Get("Location"))
		}
	}
	if got := countRows(t, p.a.db, "SELECT COUNT(*) FROM auth_oidc_requests"); got != 0 {
		t.Errorf("rejected requests were stored: %d", got)
	}
	// 手続きのCookieが無ければ、要求のIDだけでは続行できない
	res, _ := newFedBrowser(t).do(http.MethodGet, p.a.origin+"/admin/oidc/continue", nil)
	if res.StatusCode != http.StatusSeeOther {
		t.Errorf("continue without session: %d", res.StatusCode)
	}
}

// 利用許可が無い・中止したときは、検証済みの戻り先へ access_denied を返し、Bにログインできないこと。
func TestFederatedAuth_DeniedReturnsToClient(t *testing.T) {
	p := newFedPair(t)
	cookie, _ := authtest.NewAccountSession(t, p.a.db, "member")
	br := newFedBrowser(t)
	br.setCookie(p.a.origin, cookie)
	callback := p.approveAtA(t, br, p.beginLogin(t, br))
	u, err := url.Parse(callback)
	if err != nil {
		t.Fatal(err)
	}
	if u.Query().Get("error") != "access_denied" || u.Query().Get("code") != "" {
		t.Fatalf("callback %q", u.RawQuery)
	}
	res, _ := br.do(http.MethodGet, callback, nil)
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("B callback: %d", res.StatusCode)
	}
	if got := countRows(t, p.b.db, "SELECT COUNT(*) FROM federated_sessions"); got != 0 {
		t.Fatalf("session: %d", got)
	}
}

// BのコールバックはCookieとstateが合わなければ拒否し、一回限りであること。戻り先は管理画面の相対パスに限ること。
//
// 不変条件: FEDAUTH-INV-3
func TestFederatedAuth_CallbackBinding(t *testing.T) {
	p := newFedPair(t)
	br, _, _ := p.member(t)
	callback := p.approveAtA(t, br, p.beginLogin(t, br))

	// 別のブラウザに、コールバックを踏ませても（ログインCSRF）ログインさせない
	other := newFedBrowser(t)
	if res, _ := other.do(http.MethodGet, callback, nil); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("other browser: %d", res.StatusCode)
	}
	u, err := url.Parse(callback)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("state", "tampered-state-value")
	u.RawQuery = q.Encode()
	if res, _ := br.do(http.MethodGet, u.String(), nil); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("tampered state: %d", res.StatusCode)
	}
	if res, _ := br.do(http.MethodGet, callback, nil); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("attempt must be one-time: %d", res.StatusCode)
	}
	if got := countRows(t, p.b.db, "SELECT COUNT(*) FROM federated_sessions"); got != 0 {
		t.Fatalf("session: %d", got)
	}

	// 同じ開始の二度押しは、新しい試行を作らず同じ認可要求へ進む
	first := p.beginLogin(t, br)
	if second := p.beginLogin(t, br); second != first {
		t.Fatal("double submit created another attempt")
	}

	// 開始は同一オリジンだけ
	for _, site := range []string{"", "cross-site", "same-site"} {
		req, _ := http.NewRequest(http.MethodPost, p.b.origin+"/admin/sso/start", strings.NewReader("next=%2Fadmin"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if site != "" {
			req.Header.Set("Sec-Fetch-Site", site)
		}
		res, err := br.client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusForbidden {
			t.Errorf("start from %q: %d", site, res.StatusCode)
		}
	}

	for _, next := range []string{"//evil.example", "https://evil.example/", "/admin//evil", "/admin/%2e%2e/x", `/admin\evil`, "/admin/login", "/other"} {
		res, body := newFedBrowser(t).do(http.MethodGet, p.b.origin+"/admin/login?"+url.Values{"next": {next}}.Encode(), nil)
		if res.StatusCode != http.StatusOK || !strings.Contains(body, `name="next" value="/admin"`) {
			t.Errorf("next %q was kept", next)
		}
	}
}

// コード交換が成功しても、初回の照会で有効と確認できなければ、セッションを作らないこと。
//
// 不変条件: FEDAUTH-INV-1, FEDAUTH-INV-2
func TestFederatedAuth_FirstCheckGuardsLogin(t *testing.T) {
	p := newFedPair(t)
	br, _, _ := p.member(t)
	callback := p.approveAtA(t, br, p.beginLogin(t, br))
	p.a.noCheck.Store(true)
	res, _ := br.do(http.MethodGet, callback, nil)
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("callback while the check fails: %d", res.StatusCode)
	}
	if got := countRows(t, p.b.db, "SELECT COUNT(*) FROM federated_sessions"); got != 0 {
		t.Fatalf("session created without a check: %d", got)
	}
}

// ログイン中は、開始でもコールバックでもセッションを置き換えないこと。
// 別人のログインの結果を踏まされても、今のログインのままであること。
//
// 不変条件: FEDAUTH-INV-3
func TestFederatedAuth_LoggedInSessionIsNotReplaced(t *testing.T) {
	p := newFedPair(t)
	br, _, _ := p.member(t)
	p.login(t, br)
	res, _ := br.do(http.MethodPost, p.b.origin+"/admin/sso/start", url.Values{"next": {"/admin"}})
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/admin" {
		t.Fatalf("start while logged in: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	for _, c := range res.Cookies() {
		if c.Name == "sso_attempt" {
			t.Fatal("start while logged in created an attempt")
		}
	}

	// 別のブラウザで得た攻撃者のコールバックと試行Cookieを、ログイン中のブラウザに植える
	attacker, _, _ := p.member(t)
	callback := p.approveAtA(t, attacker, p.beginLogin(t, attacker))
	target, err := url.Parse(p.b.origin + "/admin/sso/callback")
	if err != nil {
		t.Fatal(err)
	}
	var planted bool
	for _, c := range attacker.client.Jar.Cookies(target) {
		if c.Name == "sso_attempt" {
			br.client.Jar.SetCookies(target, []*http.Cookie{{Name: c.Name, Value: c.Value, Path: "/admin/sso", Secure: true}})
			planted = true
		}
	}
	if !planted {
		t.Fatal("attempt cookie not found")
	}
	res, _ = br.do(http.MethodGet, callback, nil)
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/admin" {
		t.Fatalf("callback while logged in: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	if got := countRows(t, p.b.db, "SELECT COUNT(*) FROM federated_sessions"); got != 1 {
		t.Fatalf("sessions: %d", got)
	}
}

// 公開するのは列挙した経路だけで、未実装の機能を広告せず、CORSを許可しないこと。
func TestFederatedAuth_PublicSurface(t *testing.T) {
	p := newFedPair(t)
	client := p.a.client()
	get := func(path string) (*http.Response, string) {
		res, err := client.Get(p.a.origin + path)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		return res, string(raw)
	}
	res, body := get("/.well-known/openid-configuration")
	if res.StatusCode != http.StatusOK {
		t.Fatal(res.StatusCode)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatal(err)
	}
	if doc["issuer"] != p.a.origin {
		t.Errorf("issuer %v", doc["issuer"])
	}
	for _, key := range []string{"userinfo_endpoint", "introspection_endpoint", "revocation_endpoint", "end_session_endpoint", "device_authorization_endpoint", "check_session_iframe"} {
		if _, ok := doc[key]; ok {
			t.Errorf("%s is advertised", key)
		}
	}
	for _, path := range []string{"/userinfo", "/oauth/introspect", "/revoke", "/end_session", "/device_authorization", "/healthz", "/ready", "/oidc/userinfo"} {
		if res, _ := get(path); res.StatusCode != http.StatusNotFound {
			t.Errorf("%s: %d", path, res.StatusCode)
		}
	}
	res, body = get("/oidc/keys")
	if res.StatusCode != http.StatusOK || strings.Contains(body, `"d"`) {
		t.Errorf("keys: %d %s", res.StatusCode, body)
	}
	if res.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Error("CORS is allowed")
	}
}

// 照会は、資格情報が無ければ401で、形式が違えば400で、無効の理由を外へ区別しないこと。
//
// 不変条件: AUTH-INV-17
func TestFederatedAuth_CheckContract(t *testing.T) {
	p := newFedPair(t)
	_, id, sub := p.member(t)
	post := func(user, pass, body string) (int, string) {
		req, _ := http.NewRequest(http.MethodPost, p.a.origin+"/oidc/check", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if user != "" {
			req.SetBasicAuth(user, pass)
		}
		res, err := p.a.client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		return res.StatusCode, strings.TrimSpace(string(raw))
	}
	valid := fmt.Sprintf(`{"sub":%q,"account_epoch":"1","client_epoch":"1","grant_epoch":"1"}`, sub)
	if status, _ := post("", "", valid); status != http.StatusUnauthorized {
		t.Errorf("no credentials: %d", status)
	}
	if status, _ := post(p.clientID, "wrong", valid); status != http.StatusUnauthorized {
		t.Errorf("bad secret: %d", status)
	}
	if status, body := post(p.clientID, p.secret, valid); status != http.StatusOK || !strings.Contains(body, `"active":true`) {
		t.Errorf("valid: %d %s", status, body)
	}
	for name, body := range map[string]string{
		"duplicate key": `{"sub":"x","sub":"y","account_epoch":"1","client_epoch":"1","grant_epoch":"1"}`,
		"unknown key":   `{"sub":"x","account_epoch":"1","client_epoch":"1","grant_epoch":"1","extra":1}`,
		"leading zero":  `{"sub":"x","account_epoch":"01","client_epoch":"1","grant_epoch":"1"}`,
		"zero":          `{"sub":"x","account_epoch":"0","client_epoch":"1","grant_epoch":"1"}`,
		"number":        `{"sub":"x","account_epoch":1,"client_epoch":"1","grant_epoch":"1"}`,
		"trailing":      valid + `{}`,
		"too big":       `{"sub":"` + strings.Repeat("x", 5000) + `"}`,
	} {
		if status, _ := post(p.clientID, p.secret, body); status != http.StatusBadRequest {
			t.Errorf("%s: %d", name, status)
		}
	}
	unknown := `{"sub":"unknown","account_epoch":"1","client_epoch":"1","grant_epoch":"1"}`
	_, answerUnknown := post(p.clientID, p.secret, unknown)
	mustExecDB(t, p.a.db, "UPDATE auth_accounts SET disabled_at = 1 WHERE id = ?", id)
	_, answerStopped := post(p.clientID, p.secret, valid)
	if answerUnknown != `{"active":false}` || answerStopped != answerUnknown {
		t.Errorf("answers differ: %q %q", answerUnknown, answerStopped)
	}
}

// 停止とコード交換を同時に走らせても、停止の後に新しいログインが有効にならないこと。
// 同じコードの同時交換は、1件だけ成功すること。
//
// 不変条件: AUTH-INV-17, AUTH-INV-18
func TestFederatedAuth_ConcurrentStopAndExchange(t *testing.T) {
	p := newFedPair(t)
	for i := range 12 { // 認可要求の受付の上限（バースト16）の内側で回す
		br, id, sub := p.member(t)
		code, verifier := p.manualCode(t, br)
		var wg sync.WaitGroup
		wg.Add(3)
		statuses := make([]int, 2)
		go func() {
			defer wg.Done()
			statuses[0], _ = p.exchange(t, p.clientID, p.secret, code, p.redirect, verifier)
		}()
		go func() {
			defer wg.Done()
			statuses[1], _ = p.exchange(t, p.clientID, p.secret, code, p.redirect, verifier)
		}()
		go func() {
			defer wg.Done()
			if _, err := p.a.db.Exec("UPDATE auth_accounts SET disabled_at = ? WHERE id = ?", time.Now().Unix(), id); err != nil {
				t.Error(err)
			}
		}()
		wg.Wait()
		if statuses[0] == http.StatusOK && statuses[1] == http.StatusOK {
			t.Fatalf("round %d: the same code was exchanged twice", i)
		}
		if _, err := p.store.Epochs(context.Background(), p.clientID, sub); err != sql.ErrNoRows {
			t.Fatalf("round %d: stopped account is still valid: %v", i, err)
		}
	}
}

// 生のコード・state・トークン・秘密値・識別子が、AのログにもBのログにも出ないこと。
func TestFederatedAuth_LogsHoldNoSecrets(t *testing.T) {
	p := newFedPair(t)
	br, _, sub := p.member(t)
	authURL := p.beginLogin(t, br)
	callback := p.approveAtA(t, br, authURL)
	authQuery, err := url.Parse(authURL)
	if err != nil {
		t.Fatal(err)
	}
	callbackQuery, err := url.Parse(callback)
	if err != nil {
		t.Fatal(err)
	}
	res, _ := br.do(http.MethodGet, callback, nil)
	if res.StatusCode != http.StatusSeeOther {
		t.Fatal(res.StatusCode)
	}
	logs := p.a.logs.String() + p.b.logs.String()
	if logs == "" {
		t.Fatal("no logs captured")
	}
	for name, value := range map[string]string{
		"code": callbackQuery.Query().Get("code"), "state": authQuery.Query().Get("state"),
		"nonce": authQuery.Query().Get("nonce"), "challenge": authQuery.Query().Get("code_challenge"),
		"sub": sub, "client secret": p.secret, "client id": p.clientID,
	} {
		if value == "" {
			t.Fatalf("%s is empty", name)
		}
		if strings.Contains(logs, value) {
			t.Errorf("%s leaked into logs", name)
		}
	}
}

// Aの画面は、通常の管理画面と同じくキャッシュと書き換えを止め、CSPを緩めないこと。
// フォーム送信の後の別サイトへの移動は、303ではなく中継のページで行うこと（form-action 'self' が止めるため）。
func TestFederatedAuth_PagesKeepHeaders(t *testing.T) {
	p := newFedPair(t)
	br, _, _ := p.member(t)
	check := func(label string, res *http.Response) {
		t.Helper()
		if cc := res.Header.Get("Cache-Control"); !strings.Contains(cc, "no-store") || !strings.Contains(cc, "no-transform") {
			t.Errorf("%s: Cache-Control %q", label, cc)
		}
		if csp := res.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "form-action 'self'") || !strings.Contains(csp, "script-src 'none'") {
			t.Errorf("%s: CSP %q", label, csp)
		}
	}
	res, _ := br.do(http.MethodGet, p.b.origin+"/admin/login", nil)
	check("login", res)
	res, body := br.do(http.MethodPost, p.b.origin+"/admin/sso/start", url.Values{"next": {"/admin"}})
	check("start", res)
	if res.StatusCode != http.StatusOK || res.Header.Get("Location") != "" {
		t.Fatalf("start must be a relay page: %d", res.StatusCode)
	}
	authURL := relayTarget(t, res, body)
	res, _ = br.do(http.MethodGet, authURL, nil)
	check("authorize", res)
	var flow *http.Cookie
	for _, c := range res.Cookies() {
		if c.Name == "oidc_flow" {
			flow = c
		}
	}
	if flow == nil || !flow.Secure || !flow.HttpOnly || flow.SameSite != http.SameSiteStrictMode || flow.Path != "/admin" {
		t.Fatalf("flow cookie: %+v", flow)
	}
	res, body = br.do(http.MethodGet, p.a.origin+"/admin/oidc/continue", nil)
	check("continue", res)
	res, body = br.do(http.MethodPost, p.a.origin+"/admin/oidc/approve", url.Values{"csrf_token": {csrfField.FindStringSubmatch(body)[1]}, "decision": {"allow"}})
	check("approve", res)
	if res.StatusCode != http.StatusOK || res.Header.Get("Location") != "" {
		t.Fatalf("approve must be a relay page: %d", res.StatusCode)
	}
	callback := relayTarget(t, res, body)
	res, _ = br.do(http.MethodGet, callback, nil)
	var session *http.Cookie
	for _, c := range res.Cookies() {
		if c.Name == "federated_session" {
			session = c
		}
	}
	if session == nil || !session.Secure || !session.HttpOnly || session.SameSite != http.SameSiteLaxMode || session.Path != "/admin" {
		t.Fatalf("session cookie: %+v", session)
	}
	for _, c := range res.Cookies() {
		if c.Name == "sso_attempt" && c.MaxAge >= 0 {
			t.Fatalf("the attempt cookie must be deleted: %+v", c)
		}
	}
}

// BとAの管理者の提供者を同時に登録すると、起動時に拒否すること。
func TestFederatedAuth_TwoProvidersAreRejected(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	reg := features.NewRegistry()
	reg.Register(auth.NewFeature(logger))
	reg.Register(federatedauth.NewFeature(logger))
	if _, err := app.BuildRoutes(store.NewMemoryStore(), reg, false, logger); err == nil || !strings.Contains(err.Error(), "both") {
		t.Fatalf("two admin providers: %v", err)
	}
}

// 提供機能のコマンドは、連携先と利用許可を管理し、秘密値を標準出力に1度だけ出すこと。
// ユースケース: UC-FEDAUTH-1
func TestFederatedAuth_ProviderCommands(t *testing.T) {
	p := newFedPair(t)
	f := oidcop.NewFeature(slog.New(slog.NewTextHandler(io.Discard, nil)))
	commands := map[string]features.Command{}
	for _, c := range f.Commands() {
		commands[c.Name] = c
	}
	run := func(name string, args ...string) (string, error) {
		var out bytes.Buffer
		err := commands[name].Run(context.Background(), p.a.db, args, strings.NewReader(""), &out)
		return out.String(), err
	}
	out, err := run("oidc-client", "-create", "-label", "別の連携先", "-redirect", "https://c.example/admin/sso/callback")
	if err != nil {
		t.Fatal(err)
	}
	match := regexp.MustCompile(`client_id: (\S+)`).FindStringSubmatch(out)
	if match == nil || !strings.Contains(out, "client_secret: ") {
		t.Fatalf("output: %s", out)
	}
	id := match[1]
	_, account := authtest.NewAccountSession(t, p.a.db, "member")
	if out, err = run("oidc-grant", "-client", id, "-account", fmt.Sprint(account)); err != nil || !strings.HasPrefix(out, "sub: ") {
		t.Fatalf("grant: %q %v", out, err)
	}
	if _, err := run("oidc-grant", "-client", id, "-account", fmt.Sprint(account), "-revoke"); err != nil {
		t.Fatal(err)
	}
	if out, err = run("oidc-client", "-client", id, "-rotate"); err != nil || !strings.HasPrefix(out, "client_secret: ") {
		t.Fatalf("rotate: %q %v", out, err)
	}
	if _, err := run("oidc-client", "-client", id, "-disable"); err != nil {
		t.Fatal(err)
	}
	if got := countRows(t, p.a.db, "SELECT client_epoch FROM auth_oidc_clients WHERE client_id = ?", id); got != 2 {
		t.Errorf("client epoch %d", got)
	}
	if _, err := run("oidc-revoke-all"); err == nil {
		t.Error("revoke-all without -yes must be refused")
	}
	if _, err := run("oidc-revoke-all", "-yes"); err != nil {
		t.Fatal(err)
	}
	if got := countRows(t, p.a.db, "SELECT client_epoch FROM auth_oidc_clients WHERE client_id = ?", p.clientID); got != 2 {
		t.Errorf("revoke-all left the epoch at %d", got)
	}
}

// 利用側のコマンドは、ログイン済みの利用者のロールを変え、認証基盤の確認を迂回しないこと。
// ユースケース: UC-FEDAUTH-5
func TestFederatedAuth_ConsumerCommand(t *testing.T) {
	p := newFedPair(t)
	br, id, sub := p.member(t)
	p.login(t, br)
	t.Setenv("OIDC_ISSUER", p.a.origin)
	f := federatedauth.NewFeature(slog.New(slog.NewTextHandler(io.Discard, nil)))
	command := f.Commands()[0]
	run := func(args ...string) error {
		return command.Run(context.Background(), p.b.db, args, strings.NewReader(""), io.Discard)
	}
	if err := run("-sub", "unknown", "-role", "admin"); err == nil {
		t.Fatal("role for an unknown user must be refused")
	}
	if err := run("-sub", sub, "-role", "admin"); err != nil {
		t.Fatal(err)
	}
	res, _ := br.do(http.MethodGet, p.b.origin+"/admin/notes/settings", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("admin role not applied: %d", res.StatusCode)
	}
	mustExecDB(t, p.a.db, "UPDATE auth_accounts SET disabled_at = 1 WHERE id = ?", id)
	if got := p.status(t, br); got != http.StatusSeeOther {
		t.Fatalf("admin role bypassed the check: %d", got)
	}
}
