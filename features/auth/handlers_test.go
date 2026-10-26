package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

var sameOriginHeaders = map[string]string{"Sec-Fetch-Site": "same-origin", "Content-Type": "application/json"}

func (fx *fixture) postJSON(t *testing.T, path string, body any, headers map[string]string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(b)))
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

func decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("invalid JSON (%d): %s", rec.Code, rec.Body.String())
	}
	return m
}

// registerViaHTTP は登録トークンを使って、HTTP経由でパスキーを登録します。
func (fx *fixture) registerViaHTTP(t *testing.T, a *fakeAuthenticator, token string) *httptest.ResponseRecorder {
	t.Helper()
	rec := fx.postJSON(t, "/admin/webauthn/register/begin", map[string]string{"token": token}, sameOriginHeaders)
	if rec.Code != http.StatusOK {
		t.Fatalf("register begin: %d %s", rec.Code, rec.Body.String())
	}
	opts := decode(t, rec)
	cd := clientDataJSON("webauthn.create", opts["challenge"].(string), fx.feature.cfg.Origin, false)
	return fx.postJSON(t, "/admin/webauthn/register/finish", map[string]any{
		"token":              token,
		"label":              "テスト用の認証器",
		"rawId":              b64(a.credID),
		"clientDataJSON":     b64(cd),
		"authenticatorData":  b64(authData(fx.feature.cfg.RPID, a.flags|flagAT, a.count, a.credID)),
		"publicKey":          b64(a.spki()),
		"publicKeyAlgorithm": a.alg,
	}, sameOriginHeaders)
}

// loginViaHTTP はHTTP経由でログインし、応答を返します。
func (fx *fixture) loginViaHTTP(t *testing.T, a *fakeAuthenticator, userID []byte) *httptest.ResponseRecorder {
	t.Helper()
	rec := fx.postJSON(t, "/admin/webauthn/login/begin", map[string]any{}, sameOriginHeaders)
	if rec.Code != http.StatusOK {
		t.Fatalf("login begin: %d", rec.Code)
	}
	opts := decode(t, rec)
	a.count++
	cd := clientDataJSON("webauthn.get", opts["challenge"].(string), fx.feature.cfg.Origin, false)
	ad := authData(fx.feature.cfg.RPID, a.flags, a.count, nil)
	return fx.postJSON(t, "/admin/webauthn/login/finish", map[string]any{
		"rawId":             b64(a.credID),
		"clientDataJSON":    b64(cd),
		"authenticatorData": b64(ad),
		"signature":         b64(a.sign(ad, cd)),
		"userHandle":        b64(userID),
	}, sameOriginHeaders)
}

func adminCookie(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == adminCookieName && c.MaxAge > 0 {
			return c
		}
	}
	t.Fatalf("admin cookie not set: %v", rec.Header().Values("Set-Cookie"))
	return nil
}

// 不変条件: AUTH-INV-14
func TestAdmin_SetupLoginLogoutFlow(t *testing.T) {
	fx := newFixture(t)
	ctx := context.Background()
	token, err := Setup(ctx, fx.db, time.Now())
	if err != nil {
		t.Fatal(err)
	}

	// 登録画面はJSを許可するCSPで返り、トークンはサーバーに送られない（#以降）
	rec := fx.do(t, http.MethodGet, "/admin/setup", nil)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Security-Policy") != authPageCSP {
		t.Fatalf("setup page: %d %s", rec.Code, rec.Header().Get("Content-Security-Policy"))
	}
	if !strings.Contains(rec.Body.String(), `<script src="/admin/static/webauthn.js" defer></script>`) {
		t.Fatal("setup page must load the approved script")
	}

	a := newFakeAuthenticator(t, algES256)
	rec = fx.registerViaHTTP(t, a, token)
	if rec.Code != http.StatusOK || decode(t, rec)["next"] != "/admin/login?registered=1" {
		t.Fatalf("register finish: %d %s", rec.Code, rec.Body.String())
	}
	// 登録直後はセッションを発行しない（登録したパスキーでログインしてもらう）
	if len(rec.Header().Values("Set-Cookie")) != 0 {
		t.Fatal("registration must not start an admin session")
	}
	// トークンは使用済み
	if again := fx.postJSON(t, "/admin/webauthn/register/begin", map[string]string{"token": token}, sameOriginHeaders); again.Code != http.StatusForbidden || decode(t, again)["error"] != "setup_token_invalid" {
		t.Fatal("used setup token must be rejected")
	}

	userID, _ := fx.userID(t, 1), error(nil)
	rec = fx.loginViaHTTP(t, a, userID)
	if rec.Code != http.StatusOK || decode(t, rec)["next"] != "/admin" {
		t.Fatalf("login: %d %s", rec.Code, rec.Body.String())
	}
	cookie := adminCookie(t, rec)
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/admin" || cookie.Secure {
		t.Fatalf("unexpected cookie attributes: %+v", cookie)
	}

	// /admin は他の機能の管理画面（ここでは見本の /admin/notes）へ。その画面はJSを許可しない
	if rec := fx.do(t, http.MethodGet, "/admin", nil, cookie); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/notes" {
		t.Fatalf("admin home: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	rec = fx.do(t, http.MethodGet, "/admin/notes", nil, cookie)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Header().Get("Content-Security-Policy"), "script-src 'none'") {
		t.Fatalf("dashboard: %d %s", rec.Code, rec.Header().Get("Content-Security-Policy"))
	}
	if strings.Contains(rec.Body.String(), "<script") {
		t.Fatal("dashboard must not load scripts")
	}
	csrf := fx.feature.csrfToken(cookie.Value)
	if !strings.Contains(rec.Body.String(), csrf) {
		t.Fatal("dashboard must embed the CSRF token for logout")
	}
	// ログイン中にログイン画面を開くと管理画面へ
	if rec := fx.do(t, http.MethodGet, "/admin/login", nil, cookie); rec.Code != http.StatusSeeOther {
		t.Fatalf("login page while logged in: %d", rec.Code)
	}

	// CSRFトークンなし・別サイトからのログアウトは拒否
	logout := func(form url.Values, site string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/admin/logout", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Sec-Fetch-Site", site)
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		fx.routes.ServeHTTP(rec, req)
		return rec
	}
	if rec := logout(url.Values{}, "same-origin"); rec.Code != http.StatusForbidden {
		t.Fatal("logout without CSRF token must be rejected")
	}
	if rec := logout(url.Values{"csrf_token": {csrf}}, "cross-site"); rec.Code != http.StatusForbidden {
		t.Fatal("cross-site logout must be rejected")
	}
	if rec := logout(url.Values{"csrf_token": {csrf}}, "same-origin"); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/login" {
		t.Fatalf("logout: %d", rec.Code)
	}
	if rec := fx.do(t, http.MethodGet, "/admin/notes", nil, cookie); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/login" {
		t.Fatal("session must be invalid after logout")
	}
}

// 不変条件: AUTH-INV-14
func TestAdmin_RequiresSessionAndSameOrigin(t *testing.T) {
	fx := newFixture(t)
	if rec := fx.do(t, http.MethodGet, "/admin", nil); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/login" {
		t.Fatalf("unauthenticated dashboard: %d", rec.Code)
	}
	if rec := fx.do(t, http.MethodGet, "/admin/notes", nil, &http.Cookie{Name: adminCookieName, Value: "forged"}); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/login" {
		t.Fatal("forged cookie must be rejected")
	}
	for name, headers := range map[string]map[string]string{
		"no Sec-Fetch-Site": {"Content-Type": "application/json"},
		"cross-site":        {"Content-Type": "application/json", "Sec-Fetch-Site": "cross-site"},
		"same-site":         {"Content-Type": "application/json", "Sec-Fetch-Site": "same-site"},
		"origin mismatch":   {"Content-Type": "application/json", "Sec-Fetch-Site": "same-origin", "Origin": "https://evil.example"},
	} {
		if rec := fx.postJSON(t, "/admin/webauthn/login/begin", map[string]any{}, headers); rec.Code != http.StatusForbidden {
			t.Errorf("%s: got %d", name, rec.Code)
		}
	}
	// Referrer-Policy: no-referrer により null になる同一オリジンの送信は通す
	if rec := fx.postJSON(t, "/admin/webauthn/login/begin", map[string]any{}, map[string]string{"Content-Type": "application/json", "Sec-Fetch-Site": "same-origin", "Origin": "null"}); rec.Code != http.StatusOK {
		t.Fatalf("same-origin request with Origin: null must be accepted, got %d", rec.Code)
	}
	if rec := fx.postJSON(t, "/admin/webauthn/login/begin", map[string]any{}, map[string]string{"Sec-Fetch-Site": "same-origin", "Content-Type": "text/plain"}); rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("non-JSON body must be rejected, got %d", rec.Code)
	}
	big := strings.Repeat("a", maxWebAuthnBody+1)
	if rec := fx.postJSON(t, "/admin/webauthn/login/finish", map[string]string{"x": big}, sameOriginHeaders); rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized body must be rejected, got %d", rec.Code)
	}
	// 登録は、登録トークンもセッションも無ければ拒否
	if rec := fx.postJSON(t, "/admin/webauthn/register/begin", map[string]string{}, sameOriginHeaders); rec.Code != http.StatusForbidden {
		t.Fatalf("registration without authorization: %d", rec.Code)
	}
}

// 不変条件: AUTH-INV-9
func TestAdmin_SyncedPasskeyIsRejectedWithGuidance(t *testing.T) {
	fx := newFixture(t)
	token, err := Setup(context.Background(), fx.db, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	a := newFakeAuthenticator(t, algES256)
	a.flags |= flagBE
	rec := fx.registerViaHTTP(t, a, token)
	body := decode(t, rec)
	if rec.Code != http.StatusBadRequest || body["error"] != "synced_credential" || !strings.Contains(body["message"].(string), "Windows Hello") {
		t.Fatalf("synced passkey: %d %v", rec.Code, body)
	}
	if !strings.Contains(fx.logs.String(), "reason=synced_credential") {
		t.Fatal("the rejection reason must be logged for diagnosis")
	}
	// 拒否された登録ではトークンを消費しない
	if _, err := fx.feature.store.checkSetupToken(context.Background(), token); err != nil {
		t.Fatal("rejected registration must keep the token")
	}
}

// AUTH_ALLOW_SYNCED_PASSKEYS=true なら同期型を登録・ログインでき、画面の案内も変わる。
// 許可を外すと、登録済みの同期型ではログインできなくなる（ログに理由が出る）。
// 不変条件: AUTH-INV-9
func TestAdmin_SyncedPasskeyAllowedBySetting(t *testing.T) {
	if rec := newFixture(t).do(t, http.MethodGet, "/admin/setup", nil); !strings.Contains(rec.Body.String(), "同期されるパスキーは登録できません") {
		t.Fatal("default setup page must say synced passkeys are rejected")
	}
	t.Setenv("AUTH_ALLOW_SYNCED_PASSKEYS", "true")
	fx := newFixture(t)
	if rec := fx.do(t, http.MethodGet, "/admin/setup", nil); !strings.Contains(rec.Body.String(), "同期されるパスキーも使えます") {
		t.Fatal("setup page must say synced passkeys are allowed")
	}
	if rec := fx.do(t, http.MethodGet, "/admin/login", nil); !strings.Contains(rec.Body.String(), "同期されるパスキー等") {
		t.Fatal("login page must mention synced passkeys")
	}
	token, err := Setup(context.Background(), fx.db, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	a := newFakeAuthenticator(t, algES256)
	a.flags |= flagBE | flagBS
	if rec := fx.registerViaHTTP(t, a, token); rec.Code != http.StatusOK {
		t.Fatalf("synced registration: %d %s", rec.Code, rec.Body.String())
	}
	userID := fx.userID(t, 1)
	for range 2 {
		if rec := fx.loginViaHTTP(t, a, userID); rec.Code != http.StatusOK {
			t.Fatalf("synced login: %d %s", rec.Code, rec.Body.String())
		}
	}
	fx.feature.cfg.AllowSynced = false
	if rec := fx.loginViaHTTP(t, a, userID); rec.Code == http.StatusOK || !strings.Contains(fx.logs.String(), "reason=synced_credential") {
		t.Fatalf("synced login after disabling the setting: %d", rec.Code)
	}
}

// 不変条件: AUTH-INV-8
func TestAdmin_CloneSuspectedDisablesAndRevokes(t *testing.T) {
	fx := newFixture(t)
	ctx := context.Background()
	token, _ := Setup(ctx, fx.db, time.Now())
	a := newFakeAuthenticator(t, algES256)
	if rec := fx.registerViaHTTP(t, a, token); rec.Code != http.StatusOK {
		t.Fatal(rec.Body.String())
	}
	userID, _ := fx.userID(t, 1), error(nil)
	first := adminCookie(t, fx.loginViaHTTP(t, a, userID))

	// 複製したキーが先に進めたカウンタより小さい値で署名した状態を作る
	a.count = 0
	rec := fx.loginViaHTTP(t, a, userID)
	body := decode(t, rec)
	if rec.Code != http.StatusUnauthorized || body["error"] != "login_rejected" {
		t.Fatalf("clone login: %d %v", rec.Code, body)
	}
	if strings.Contains(body["message"].(string), "複製") {
		t.Fatal("the login screen must not reveal clone detection")
	}
	if !strings.Contains(fx.logs.String(), "auth passkey disabled") {
		t.Fatal("disabling must be logged")
	}
	if rec := fx.do(t, http.MethodGet, "/admin/notes", nil, first); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/login" {
		t.Fatal("all admin sessions must be revoked")
	}
	if _, err := fx.feature.store.getCredential(ctx, a.credID); err == nil {
		t.Fatal("passkey must be disabled")
	}
}

// 不変条件: AUTH-INV-13
func TestAdmin_LoginRejectsUnknownCredentialAndLogsNoSecrets(t *testing.T) {
	fx := newFixture(t)
	a := newFakeAuthenticator(t, algES256)
	rec := fx.loginViaHTTP(t, a, []byte("0123456789abcdef"))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unknown credential: %d", rec.Code)
	}
	logs := fx.logs.String()
	if strings.Contains(logs, b64(a.credID)) || strings.Contains(logs, "challenge\"") {
		t.Fatal("credential IDs must not be logged")
	}
}

func TestAdmin_StaticScript(t *testing.T) {
	fx := newFixture(t)
	rec := fx.do(t, http.MethodGet, "/admin/static/webauthn.js", nil)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "text/javascript; charset=utf-8" || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("script: %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
}
