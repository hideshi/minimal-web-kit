package browser

// パスキーの状態を端末へ知らせる（Signal API）を、実際の webauthn.js と仮想の認証器で確かめます。
// Signal API の関数は、ページのスクリプトより前に、呼ばれた引数を記録する関数へ差し替えます（端末の状態は見ない）。

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/webauthn"
	"github.com/chromedp/chromedp"

	"minimal-web-kit/features"
	"minimal-web-kit/features/auth"
)

// signalRecorder は、Signal API の呼び出しを sessionStorage に記録します。
// sessionStorage の "no-signal" があれば、関数を消して、対応していないブラウザを再現します。
const signalRecorder = `(() => {
  if (!window.PublicKeyCredential) return;
  if (sessionStorage.getItem("no-signal")) {
    delete PublicKeyCredential.signalUnknownCredential;
    delete PublicKeyCredential.signalAllAcceptedCredentials;
    return;
  }
  // 少し遅れて記録する。webauthn.js が完了を待たずに移動すると、記録が残らない
  const record = (name) => (arg) => new Promise((resolve) => setTimeout(() => {
    const calls = JSON.parse(sessionStorage.getItem("signals") || "[]");
    calls.push(location.pathname + " " + (name === "unknown" ? "unknown:" + arg.rpId + "/" + arg.credentialId
      : "accepted:" + arg.rpId + "/" + arg.userId + "/" + arg.allAcceptedCredentialIds.join(",")));
    sessionStorage.setItem("signals", JSON.stringify(calls));
    resolve();
  }, 200));
  PublicKeyCredential.signalUnknownCredential = record("unknown");
  PublicKeyCredential.signalAllAcceptedCredentials = record("accepted");
})();`

// signals は、記録した呼び出しを返し、記録を消します。
func signals(t *testing.T, b *browser) []string {
	t.Helper()
	return evaluate[[]string](t, b, `(() => {
	  const calls = JSON.parse(sessionStorage.getItem("signals") || "[]");
	  sessionStorage.removeItem("signals");
	  return calls;
	})()`)
}

func cdpDo[P, R any](t *testing.T, b *browser, c cdp.Command[P, R], params P) R {
	t.Helper()
	var res R
	b.do(t, chromedp.Func(func(ctx context.Context, target *chromedp.Target) error {
		var err error
		res, err = cdp.Call(ctx, target, c, params)
		return err
	}))
	return res
}

// tryLogin は、ログインの画面でパスキーを使います。rewrites は、押す前に入れる応答の差し替えです。
func tryLogin(t *testing.T, b *browser, origin string, rewrites ...string) {
	t.Helper()
	cdpDo(t, b, network.ClearBrowserCookies, cdp.Empty{})
	b.do(t, chromedp.Navigate(origin+"/admin/login"))
	b.waitFor(t, `!document.getElementById("login-button").hidden`)
	for _, r := range rewrites {
		evaluate[bool](t, b, r)
	}
	b.do(t, chromedp.Click("#login-button"))
}

// rewrite は、path への fetch の応答を、respond（(本物のfetch, url, opts) => Promise<Response>）で差し替える式です。
// サーバーが返さない応答（符号の無い404、空の一覧等）でも、webauthn.js が知らせないことを確かめるために使います。
func rewrite(path, respond string) string {
	return `(() => {
	  const real = window.fetch;
	  const respond = ` + respond + `;
	  window.fetch = (url, opts) => url === "` + path + `" ? respond(real, url, opts) : real(url, opts);
	  return true;
	})()`
}

// editJSON は、本物の応答のJSONを mutate（data を書き換える文）で変えて返す、rewrite の respond です。
func editJSON(mutate string) string {
	return `async (real, url, opts) => {
	  const res = await real(url, opts);
	  const data = await res.json();
	  ` + mutate + `;
	  return new Response(JSON.stringify(data), { status: res.status, headers: { "Content-Type": "application/json" } });
	}`
}

// fixed は、サーバーへ送らずに決まった応答を返す、rewrite の respond です。
func fixed(status int, body string) string {
	return fmt.Sprintf(`() => Promise.resolve(new Response(%q, { status: %d, headers: { "Content-Type": "application/json" } }))`, body, status)
}

// statusIncludes は、状態の表示に text が出たか。
func statusIncludes(text string) string {
	return `document.getElementById("webauthn-status").textContent.includes("` + text + `")`
}

// submitSetup は、登録の画面で端末名を入れて送ります。
func submitSetup(t *testing.T, b *browser, setupURL, label string, rewrites ...string) {
	t.Helper()
	// 前の差し替えを残さないよう、別の文書を挟んで開き直す
	b.do(t, chromedp.Navigate("about:blank"), chromedp.Navigate(setupURL))
	b.waitFor(t, `document.getElementById("setup-form") && !document.getElementById("setup-form").hidden`)
	for _, r := range rewrites {
		evaluate[bool](t, b, r)
	}
	b.do(t, chromedp.SetValue("#passkey-label", label), chromedp.Click(`#setup-form button[type=submit]`))
}

// loggedIn は、ログインの後の画面へ移ったか（管理画面のホームは、機能が少ないとパスキー管理へ移る）。
const loggedIn = `location.pathname.startsWith("/admin") && location.pathname !== "/admin/login"`

const loginFailed = `document.getElementById("webauthn-status").textContent.includes("このパスキーではログインできません")`

// ログインの成功・パスキー管理の画面で有効な一覧を、「無い」と答えたログインと同期型を断った登録で、そのパスキーを知らせる。
// ほかの拒否では知らせず、関数の無いブラウザでも今までどおり動く。
// 不変条件: AUTH-INV-29, AUTH-INV-30
func TestPasskeySignal_CallsOnlyWhereTheServerAllows(t *testing.T) {
	b := newWebAuthnBrowser(t)
	cdpDo(t, b, page.AddScriptToEvaluateOnNewDocument, page.AddScriptToEvaluateOnNewDocumentParams{Source: signalRecorder})
	device := b.addAuthenticator(t, false)
	ln, port := listen(t)
	origin := fmt.Sprintf("https://a.test:%d", port)
	t.Setenv("AUTH_ORIGIN", origin)
	t.Setenv("AUTH_RP_ID", "a.test")
	t.Setenv("SECURE_COOKIE", "true")
	s := openDB(t, "a.db")
	reg := features.NewRegistry()
	reg.Register(auth.NewFeature(slog.New(slog.NewTextHandler(io.Discard, nil))))
	serveTLS(t, ln, s, reg)
	db := s.DB()
	ctx := context.Background()
	b64 := base64.RawURLEncoding.EncodeToString

	token, err := auth.Setup(ctx, db, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	b.do(t, chromedp.Navigate(auth.SetupURL(origin, token)))
	b.waitFor(t, `document.getElementById("setup-form") && !document.getElementById("setup-form").hidden`)
	b.do(t, chromedp.SetValue("#passkey-label", "仮想の認証器"), chromedp.Click(`#setup-form button[type=submit]`))
	b.waitLong(t, `location.pathname === "/admin/login"`)
	var accountID int64
	var userID, credID []byte
	if err := db.QueryRow("SELECT a.id, a.webauthn_user_id, c.id FROM auth_accounts a JOIN auth_credentials c ON c.account_id = a.id").Scan(&accountID, &userID, &credID); err != nil {
		t.Fatal(err)
	}
	accepted := "accepted:a.test/" + b64(userID) + "/" + b64(credID)
	if got := signals(t, b); len(got) != 0 {
		t.Fatalf("registration must not signal: %v", got)
	}

	// 関数の無いブラウザでも、ログインとパスキー管理の画面が動く
	evaluate[bool](t, b, `(sessionStorage.setItem("no-signal", "1"), true)`)
	tryLogin(t, b, origin)
	b.waitLong(t, loggedIn)
	b.do(t, chromedp.Navigate(origin+"/admin/passkeys"))
	b.waitFor(t, `!document.getElementById("passkey-add-form").hidden`)
	b.noErrors(t)
	evaluate[bool](t, b, `(sessionStorage.removeItem("no-signal"), true)`)

	// ログインの成功で、本人の有効な一覧を知らせてから移る
	tryLogin(t, b, origin)
	b.waitLong(t, loggedIn)
	b.do(t, chromedp.Navigate(origin+"/admin/passkeys"))
	b.waitFor(t, `!document.getElementById("passkey-add-form").hidden && sessionStorage.getItem("signals")`)
	if got := signals(t, b); len(got) == 0 || got[0] != "/admin/login "+accepted {
		t.Fatalf("login: %v, want %s", got, accepted)
	}
	// パスキー管理の画面を開くと、同じ一覧を知らせる
	b.do(t, chromedp.Reload())
	b.waitFor(t, `!document.getElementById("passkey-add-form").hidden && sessionStorage.getItem("signals")`)
	if got := signals(t, b); len(got) != 1 || got[0] != "/admin/passkeys "+accepted {
		t.Fatalf("passkeys page: %v, want %s", got, accepted)
	}
	b.noErrors(t)

	// 符号の無い404（前段のプロキシ等）では知らせない
	tryLogin(t, b, origin, rewrite("/admin/webauthn/login/finish", fixed(404, "not found")))
	b.waitLong(t, statusIncludes("処理に失敗しました"))
	if got := signals(t, b); len(got) != 0 {
		t.Fatalf("a 404 without the code must not signal: %v", got)
	}
	// ログインの応答の一覧が空なら、ログインの画面では知らせない（移った先の画面の分だけ）
	tryLogin(t, b, origin, rewrite("/admin/webauthn/login/finish", editJSON("data.accepted.credentialIds = []")))
	b.waitLong(t, loggedIn)
	b.waitFor(t, `!document.getElementById("passkey-add-form").hidden && sessionStorage.getItem("signals")`)
	for _, got := range signals(t, b) {
		if !strings.HasPrefix(got, "/admin/passkeys ") {
			t.Fatalf("an empty list must not be signaled: %s", got)
		}
	}

	// アカウントの停止（401 の login_rejected）では知らせない
	if _, err := db.Exec("UPDATE auth_accounts SET disabled_at = ? WHERE id = ?", time.Now().Unix(), accountID); err != nil {
		t.Fatal(err)
	}
	tryLogin(t, b, origin)
	b.waitLong(t, loginFailed)
	if got := signals(t, b); len(got) != 0 {
		t.Fatalf("a disabled account must not signal: %v", got)
	}
	if _, err := db.Exec("UPDATE auth_accounts SET disabled_at = NULL WHERE id = ?", accountID); err != nil {
		t.Fatal(err)
	}

	// サーバーで消したパスキー（DBには別の有効なパスキーがある）では、そのパスキーを知らせる
	if _, err := db.Exec("INSERT INTO auth_credentials (id, account_id, public_key, alg, sign_count, label, created_at) VALUES (?, ?, ?, -7, 0, '予備', ?)",
		[]byte("spare-credential"), accountID, []byte("spki"), time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("DELETE FROM auth_credentials WHERE id = ?", credID); err != nil {
		t.Fatal(err)
	}
	tryLogin(t, b, origin)
	b.waitLong(t, loginFailed)
	b.waitFor(t, `sessionStorage.getItem("signals")`)
	if got := signals(t, b); len(got) != 1 || got[0] != "/admin/login unknown:a.test/"+b64(credID) {
		t.Fatalf("unknown passkey: %v", got)
	}

	// 同期型を断った登録では、作られたパスキーを知らせる
	cdpDo(t, b, webauthn.RemoveVirtualAuthenticator, webauthn.RemoveVirtualAuthenticatorParams{AuthenticatorID: device})
	b.addAuthenticator(t, true)
	token, err = auth.Recover(ctx, db, time.Now(), auth.RecoverOptions{AccountID: accountID, Only: true})
	if err != nil {
		t.Fatal(err)
	}
	setupURL := auth.SetupURL(origin, token)
	// サーバーが許さないとき（signalUnknown: false）は知らせない
	submitSetup(t, b, setupURL, "同期型", rewrite("/admin/webauthn/register/begin", editJSON("data.signalUnknown = false")))
	b.waitLong(t, statusIncludes("同期型のパスキー"))
	if got := signals(t, b); len(got) != 0 {
		t.Fatalf("must not signal when the server does not allow: %v", got)
	}
	// 同期型以外の登録の失敗（作った後の端末名の誤り、登録済みの認証器）では知らせない
	submitSetup(t, b, setupURL, strings.Repeat("あ", 51))
	b.waitLong(t, statusIncludes("端末名は1〜50文字"))
	if got := signals(t, b); len(got) != 0 {
		t.Fatalf("label_invalid must not signal: %v", got)
	}
	submitSetup(t, b, setupURL, "同期型", rewrite("/admin/webauthn/register/finish", fixed(400, `{"error":"credential_exists","message":"この認証器は登録済みです。"}`)))
	b.waitLong(t, statusIncludes("登録済み"))
	if got := signals(t, b); len(got) != 0 {
		t.Fatalf("credential_exists must not signal: %v", got)
	}
	submitSetup(t, b, setupURL, "同期型")
	b.waitLong(t, statusIncludes("同期型のパスキー"))
	b.waitFor(t, `sessionStorage.getItem("signals")`)
	if got := signals(t, b); len(got) != 1 || !strings.HasPrefix(got[0], "/admin/setup unknown:a.test/") || strings.HasSuffix(got[0], "/"+b64(credID)) {
		t.Fatalf("synced registration: %v", got)
	}
}
