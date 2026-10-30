package browser

// 共通認証を、別サイトのA・BとChromiumの実際のCookie・CSP・WebAuthnで通します。
// パスキーは DevTools の仮想の認証器で作り、webauthn.js と既存の画面をそのまま使います。

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/webauthn"
	"github.com/chromedp/chromedp"

	"minimal-web-kit/features"
	"minimal-web-kit/features/auth"
	"minimal-web-kit/features/auth/oidcop"
	"minimal-web-kit/features/federatedauth"
	"minimal-web-kit/internal/app"
	"minimal-web-kit/internal/store"
	"minimal-web-kit/internal/web"
)

// protectedNotes は、Bの管理画面の見本です。
type protectedNotes struct{ admin features.Admin }

func (n *protectedNotes) Name() string                                              { return "notes" }
func (n *protectedNotes) Init(*sql.DB) error                                        { return nil }
func (n *protectedNotes) RegisterRoutes(mux *http.ServeMux, sm *web.SessionManager) {}
func (n *protectedNotes) SetAdmin(a features.Admin) error                           { n.admin = a; return nil }
func (n *protectedNotes) RegisterIsolatedRoutes(mux *web.IsolatedMux) {
	mux.HandleFunc("GET /admin/notes", n.admin.Page(func(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<!doctype html><html lang="ja"><title>notes</title><h1 id="notes">保護された画面</h1>`)
	}))
}

func listen(t *testing.T) (net.Listener, int) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return ln, ln.Addr().(*net.TCPAddr).Port
}

func serveTLS(t *testing.T, ln net.Listener, s *store.SQLiteStore, reg *features.Registry) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, f := range reg.All() {
		if err := f.Init(s.DB()); err != nil {
			t.Fatalf("init %s: %v", f.Name(), err)
		}
	}
	routes, err := app.BuildRoutes(s, reg, true, logger)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(routes)
	_ = srv.Listener.Close()
	srv.Listener = ln
	srv.StartTLS()
	t.Cleanup(srv.Close)
}

func secretFile(t *testing.T, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func openDB(t *testing.T, name string) *store.SQLiteStore {
	t.Helper()
	s, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), name))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// federatedSites は、別サイトの a.test と b.test を起動します。
func federatedSites(t *testing.T) (aOrigin, bOrigin string, aDB *sql.DB, fed *auth.FederationStore, clientID string) {
	t.Helper()
	aListener, aPort := listen(t)
	bListener, bPort := listen(t)
	aOrigin, bOrigin = fmt.Sprintf("https://a.test:%d", aPort), fmt.Sprintf("https://b.test:%d", bPort)
	t.Setenv("AUTH_ORIGIN", aOrigin)
	t.Setenv("AUTH_RP_ID", "a.test")
	t.Setenv("SECURE_COOKIE", "true")

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	crypto := make([]byte, 32)
	if _, err := rand.Read(crypto); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	a := openDB(t, "a.db")
	aRegistry := features.NewRegistry()
	aRegistry.Register(oidcop.NewWithConfig(logger, oidcop.Config{
		Issuer:     aOrigin,
		KeyFile:    secretFile(t, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})),
		CryptoFile: secretFile(t, crypto),
	}))
	serveTLS(t, aListener, a, aRegistry)

	fed = auth.NewFederationStore(a.DB())
	client, secret, err := fed.CreateClient(context.Background(), "業務アプリB", bOrigin+"/admin/sso/callback")
	if err != nil {
		t.Fatal(err)
	}
	// Bのサーバーは a.test を解決できないため、接続先だけループバックに固定する
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, aListener.Addr().String())
		},
	}
	bRegistry := features.NewRegistry()
	bRegistry.Register(federatedauth.NewWithConfig(logger, federatedauth.Config{
		Origin: bOrigin, Issuer: aOrigin, ClientID: client.ID, SecretFile: secretFile(t, []byte(secret)), Transport: transport,
	}))
	bRegistry.Register(&protectedNotes{})
	serveTLS(t, bListener, openDB(t, "b.db"), bRegistry)
	return aOrigin, bOrigin, a.DB(), fed, client.ID
}

// newFederatedBrowser は、a.test・b.test をループバックへ向け、仮想の認証器を持つChromeを起動します。
func newFederatedBrowser(t *testing.T) *browser {
	t.Helper()
	opts := append(chromedp.DefaultExecAllocatorOptions[:], chromedp.ExecPath(chromePath(t)), chromedp.UserDataDir(t.TempDir()),
		chromedp.Flag("host-resolver-rules", "MAP a.test 127.0.0.1,MAP b.test 127.0.0.1"),
		chromedp.Flag("ignore-certificate-errors", true))
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), opts...)
	ctx, cancel := chromedp.NewContext(allocCtx)
	t.Cleanup(func() {
		cancel()
		cancelAlloc()
	})
	b := &browser{ctx: ctx}
	messages := chromedp.Console(ctx)
	go func() {
		for m, err := range messages {
			if err != nil {
				return
			}
			if m.Type == chromedp.ConsoleError || m.IsException() {
				b.mu.Lock()
				b.errors = append(b.errors, m.String())
				b.mu.Unlock()
			}
		}
	}()
	b.do(t, chromedp.Func(func(ctx context.Context, target *chromedp.Target) error {
		if _, err := cdp.Call(ctx, target, webauthn.Enable, webauthn.EnableParams{}); err != nil {
			return err
		}
		_, err := cdp.Call(ctx, target, webauthn.AddVirtualAuthenticator, webauthn.AddVirtualAuthenticatorParams{Options: &webauthn.VirtualAuthenticatorOptions{
			Protocol: webauthn.AuthenticatorProtocolCtap2, Transport: webauthn.AuthenticatorTransportInternal,
			HasResidentKey: true, HasUserVerification: true, IsUserVerified: true, AutomaticPresenceSimulation: true,
		}})
		return err
	}))
	return b
}

func (b *browser) waitLong(t *testing.T, expr string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if evaluate[bool](t, b, "!!("+expr+")") {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s at %s: %s", expr, evaluate[string](t, b, "location.href"), evaluate[string](t, b, "document.body ? document.body.innerText : ''"))
}

// 実際のパスキーでAにログインして、別サイトのBへログインし、Aで停止すると次の要求からBが拒否すること。
// 別サイトの遷移でCookieが届き、CSPの form-action を緩めずに往復できることも確かめます。
// ユースケース: UC-FEDAUTH-2, UC-FEDAUTH-3
func TestFederatedLogin_PasskeyRoundTripAndStop(t *testing.T) {
	b := newFederatedBrowser(t)
	aOrigin, bOrigin, aDB, fed, clientID := federatedSites(t)
	ctx := context.Background()

	// 最初の管理者のパスキーを登録する
	token, err := auth.Setup(ctx, aDB, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	b.do(t, chromedp.Navigate(auth.SetupURL(aOrigin, token)))
	b.waitFor(t, `document.getElementById("setup-form") && !document.getElementById("setup-form").hidden`)
	b.do(t, chromedp.SetValue("#passkey-label", "仮想の認証器"), chromedp.Click(`#setup-form button[type=submit]`))
	b.waitLong(t, `location.pathname === "/admin/login"`)

	var accountID int64
	if err := aDB.QueryRow("SELECT id FROM auth_accounts").Scan(&accountID); err != nil {
		t.Fatal(err)
	}
	if _, err := fed.Grant(ctx, clientID, accountID); err != nil {
		t.Fatal(err)
	}

	// Bの保護された画面を開くと、ログインの案内へ移る
	b.do(t, chromedp.Navigate(bOrigin+"/admin/notes"))
	b.waitFor(t, `location.pathname === "/admin/login"`)
	b.do(t, chromedp.Click(`form[action="/admin/sso/start"] button`))

	// 中継のページから自動でAの継続ページへ移る。ここにはStrict Cookieが付かない
	b.waitLong(t, `location.host.startsWith("a.test") && document.querySelector('a[href="/admin/oidc/continue"]')`)
	b.do(t, chromedp.Click(`a[href="/admin/oidc/continue"]`))

	// 未ログインなので既存のパスキーの画面へ移り、ログイン後は手続きへ戻る
	b.waitFor(t, `location.pathname === "/admin/login" && !document.getElementById("login-button").hidden`)
	b.do(t, chromedp.Click("#login-button"))
	b.waitLong(t, `location.pathname === "/admin/oidc/continue" && document.querySelector('button[value="allow"]')`)
	b.do(t, chromedp.Click(`button[value="allow"]`))

	// 中継のページを経て、Bの元の画面へ戻る
	b.waitLong(t, `location.host.startsWith("b.test") && document.getElementById("notes")`)
	if got := evaluate[string](t, b, "location.pathname"); got != "/admin/notes" {
		t.Fatalf("returned to %s", got)
	}
	b.noErrors(t)

	// 一度ログインすれば、再読み込みでも使える
	b.do(t, chromedp.Reload())
	b.waitFor(t, `document.getElementById("notes")`)

	// Aで停止すると、次の要求からBは拒否する
	if _, err := aDB.Exec("UPDATE auth_accounts SET disabled_at = ? WHERE id = ?", time.Now().Unix(), accountID); err != nil {
		t.Fatal(err)
	}
	b.do(t, chromedp.Reload())
	b.waitFor(t, `location.pathname === "/admin/login" && document.querySelector('form[action="/admin/sso/start"]')`)

	// 再開しても、古いログインは復活しない
	if _, err := aDB.Exec("UPDATE auth_accounts SET disabled_at = NULL WHERE id = ?", accountID); err != nil {
		t.Fatal(err)
	}
	b.do(t, chromedp.Navigate(bOrigin+"/admin/notes"))
	b.waitFor(t, `location.pathname === "/admin/login"`)
	if strings.Contains(evaluate[string](t, b, "document.body.innerText"), "保護された画面") {
		t.Fatal("old login revived")
	}
}
