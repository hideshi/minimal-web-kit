package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

// signalFixture は、端末へ知らせてよい設定（RP ID がオリジンのホストと一致し、localhost でない）の状態を作ります。
func signalFixture(t *testing.T) *fixture {
	t.Helper()
	fx := newFixture(t)
	fx.feature.cfg.Origin = "http://admin.example.test"
	fx.feature.cfg.RPID = "admin.example.test"
	return fx
}

// loginErr はログインの応答の状態コードと符号を返します。
func loginErr(t *testing.T, fx *fixture, a *fakeAuthenticator, userID []byte) (int, string) {
	t.Helper()
	rec := fx.loginViaHTTP(t, a, userID)
	code, _ := decode(t, rec)["error"].(string)
	return rec.Code, code
}

func TestSignal_SettingCondition(t *testing.T) {
	for _, c := range []struct {
		origin, rpID string
		want         bool
	}{
		{"https://admin.example.com", "admin.example.com", true},
		{"https://admin.example.com:8443", "admin.example.com", true},
		{"https://admin.example.com", "example.com", false},
		{"http://localhost:8080", "localhost", false},
		{"http://myapp.localhost:8080", "myapp.localhost", false},
	} {
		if got := (Config{Origin: c.origin, RPID: c.rpID}).signalAllowed(); got != c.want {
			t.Errorf("%s %s: got %v", c.origin, c.rpID, got)
		}
	}
}

// 見つからない・無効のパスキーだけを「無い」と答え、ほかの拒否、壊れた行、DBの失敗では答えない。
// 不変条件: AUTH-INV-29
func TestSignal_UnknownCredentialOnlyWhenMissingOrDisabled(t *testing.T) {
	fx := signalFixture(t)
	a, _ := fx.loggedIn(t)
	userID := fx.userID(t, 1)

	// 見つからない
	if status, code := loginErr(t, fx, newFakeAuthenticator(t, algES256), userID); status != http.StatusNotFound || code != "credential_unknown" {
		t.Fatalf("missing: %d %s", status, code)
	}
	if errorMessages["credential_unknown"] != errorMessages["login_rejected"] {
		t.Fatal("the message must not differ from login_rejected")
	}
	if !strings.Contains(fx.logs.String(), "reason=credential_unavailable") {
		t.Fatal("the reject reason must be logged as before")
	}

	// 署名の失敗（同じIDで別の鍵）
	forged := newFakeAuthenticator(t, algES256)
	forged.credID = a.credID
	forged.count = a.count
	if status, code := loginErr(t, fx, forged, userID); status != http.StatusUnauthorized || code != "login_rejected" {
		t.Fatalf("bad signature: %d %s", status, code)
	}
	// userHandle の不一致
	if status, code := loginErr(t, fx, a, []byte("0123456789abcdef")); status != http.StatusUnauthorized || code != "login_rejected" {
		t.Fatalf("user handle mismatch: %d %s", status, code)
	}
	// アカウントの停止（再開すれば使えるので「無い」と言わない）
	mustExec(t, fx.db, "UPDATE auth_accounts SET disabled_at = 1 WHERE id = 1")
	if status, code := loginErr(t, fx, a, userID); status != http.StatusUnauthorized || code != "login_rejected" {
		t.Fatalf("disabled account: %d %s", status, code)
	}
	mustExec(t, fx.db, "UPDATE auth_accounts SET disabled_at = NULL WHERE id = 1")
	// 壊れた行
	mustExec(t, fx.db, "UPDATE auth_credentials SET sign_count = -1")
	if status, code := loginErr(t, fx, a, userID); status != http.StatusUnauthorized || code != "login_rejected" {
		t.Fatalf("corrupt row: %d %s", status, code)
	}
	if !strings.Contains(fx.logs.String(), "reason=credential_corrupt") {
		t.Fatal("a corrupt row must be logged with its own reason")
	}
	mustExec(t, fx.db, "UPDATE auth_credentials SET sign_count = ?", int64(a.count))
	// 無効のパスキー（別の有効なパスキーがDBにある状態で）
	other := newFakeAuthenticator(t, algES256)
	if err := fx.feature.store.addCredential(context.Background(), 1, newCredential{ID: other.credID, PublicKey: other.spki(), Alg: algES256}, "予備"); err != nil {
		t.Fatal(err)
	}
	mustExec(t, fx.db, "UPDATE auth_credentials SET disabled_at = 1 WHERE id = ?", a.credID)
	if status, code := loginErr(t, fx, a, userID); status != http.StatusNotFound || code != "credential_unknown" {
		t.Fatalf("disabled passkey: %d %s", status, code)
	}
}

// 不変条件: AUTH-INV-29
func TestSignal_StoreFailureIsNotUnknown(t *testing.T) {
	fx := signalFixture(t)
	a, _ := fx.loggedIn(t)
	userID := fx.userID(t, 1)
	mustExec(t, fx.db, "ALTER TABLE auth_accounts RENAME TO auth_accounts_gone")
	status, code := loginErr(t, fx, a, userID)
	if status != http.StatusInternalServerError || code != "internal_error" {
		t.Fatalf("store failure: %d %s", status, code)
	}
	if !strings.Contains(fx.logs.String(), "auth load passkey failed") {
		t.Fatal("the store failure must be logged")
	}
}

// 設定の条件を外れるサイト、有効なパスキーの無いDBでは「無い」と答えない。
// 不変条件: AUTH-INV-29
func TestSignal_UnknownCredentialNeedsSafeSetting(t *testing.T) {
	unknown := func(fx *fixture) (int, string) {
		return loginErr(t, fx, newFakeAuthenticator(t, algES256), []byte("0123456789abcdef"))
	}
	// localhost（既定）
	fx := newFixture(t)
	fx.loggedIn(t)
	if status, code := unknown(fx); status != http.StatusUnauthorized || code != "login_rejected" {
		t.Fatalf("localhost: %d %s", status, code)
	}
	// 上位ドメインを RP ID にしている
	fx = signalFixture(t)
	fx.feature.cfg.RPID = "example.test"
	fx.loggedIn(t)
	if status, code := unknown(fx); status != http.StatusUnauthorized || code != "login_rejected" {
		t.Fatalf("parent RP ID: %d %s", status, code)
	}
	// 有効なパスキーの無いDB（空のDB、無効のパスキーだけのDB、全体の復旧の後）
	fx = signalFixture(t)
	if status, code := unknown(fx); status != http.StatusUnauthorized || code != "login_rejected" {
		t.Fatalf("empty DB: %d %s", status, code)
	}
	fx.loggedIn(t)
	mustExec(t, fx.db, "UPDATE auth_credentials SET disabled_at = 1")
	if status, code := unknown(fx); status != http.StatusUnauthorized || code != "login_rejected" {
		t.Fatalf("only disabled passkeys: %d %s", status, code)
	}
	mustExec(t, fx.db, "UPDATE auth_credentials SET disabled_at = NULL")
	if _, err := Recover(context.Background(), fx.db, time.Now(), RecoverOptions{AccountID: 1}); err != nil {
		t.Fatal(err)
	}
	if status, code := unknown(fx); status != http.StatusUnauthorized || code != "login_rejected" {
		t.Fatalf("after recover: %d %s", status, code)
	}
}

// 署名を確かめた本人に、本人の有効なパスキーだけを渡す。無効のもの・他人のものは入れない。
// 不変条件: AUTH-INV-30
func TestSignal_AcceptedListOnLoginAndPasskeyPage(t *testing.T) {
	fx := signalFixture(t)
	ctx := context.Background()
	a, cookie := fx.loggedIn(t)
	userID := fx.userID(t, 1)
	spare := newFakeAuthenticator(t, algES256)
	disabled := newFakeAuthenticator(t, algES256)
	for _, k := range []*fakeAuthenticator{spare, disabled} {
		if err := fx.feature.store.addCredential(ctx, 1, newCredential{ID: k.credID, PublicKey: k.spki(), Alg: algES256}, "予備"); err != nil {
			t.Fatal(err)
		}
	}
	mustExec(t, fx.db, "UPDATE auth_credentials SET disabled_at = 1 WHERE id = ?", disabled.credID)
	otherAccount := newAccount(t, fx.feature.store, "B", roleSiteAdmin)
	others := newFakeAuthenticator(t, algES256)
	if err := fx.feature.store.addCredential(ctx, otherAccount, newCredential{ID: others.credID, PublicKey: others.spki(), Alg: algES256}, "B"); err != nil {
		t.Fatal(err)
	}

	rec := fx.loginViaHTTP(t, a, userID)
	if rec.Code != http.StatusOK {
		t.Fatalf("login: %d %s", rec.Code, rec.Body.String())
	}
	accepted, ok := decode(t, rec)["accepted"].(map[string]any)
	if !ok {
		t.Fatal("the login response must carry the accepted list")
	}
	var ids []string
	for _, v := range accepted["credentialIds"].([]any) {
		ids = append(ids, v.(string))
	}
	want := []string{b64(a.credID), b64(spare.credID)}
	sort.Strings(want)
	sort.Strings(ids)
	if accepted["rpId"] != "admin.example.test" || accepted["userId"] != b64(userID) || strings.Join(ids, " ") != strings.Join(want, " ") {
		t.Fatalf("accepted: %v", accepted)
	}

	page := fx.do(t, http.MethodGet, "/admin/passkeys", nil, cookie).Body.String()
	m := regexp.MustCompile(`data-credential-ids="([^"]*)"`).FindStringSubmatch(page)
	if m == nil || !strings.Contains(page, `data-rp-id="admin.example.test"`) || !strings.Contains(page, `data-user-id="`+b64(userID)+`"`) {
		t.Fatalf("the page must embed the accepted list: %s", page)
	}
	embedded := strings.Split(m[1], " ")
	sort.Strings(embedded)
	if strings.Join(embedded, " ") != strings.Join(want, " ") {
		t.Fatalf("only the own active passkeys must be embedded: %v", embedded)
	}

	// 有効なパスキーが無ければ、空の一覧を渡さない
	mustExec(t, fx.db, "UPDATE auth_credentials SET disabled_at = 1 WHERE account_id = 1")
	if _, ok := fx.feature.acceptedSignal(httptest.NewRequest(http.MethodGet, "/", nil), 1); ok {
		t.Fatal("an empty list must not be signaled")
	}
}

// 設定の条件を外れるサイトでは、一覧を渡さず、登録でも知らせさせない。
// 不変条件: AUTH-INV-29, AUTH-INV-30
func TestSignal_NothingOnUnsafeSetting(t *testing.T) {
	fx := newFixture(t)
	a, cookie := fx.loggedIn(t)
	rec := fx.loginViaHTTP(t, a, fx.userID(t, 1))
	if _, ok := decode(t, rec)["accepted"]; ok || rec.Code != http.StatusOK {
		t.Fatalf("localhost must not get the list: %d %s", rec.Code, rec.Body.String())
	}
	if page := fx.do(t, http.MethodGet, "/admin/passkeys", nil, cookie).Body.String(); strings.Contains(page, "data-credential-ids") {
		t.Fatal("localhost must not embed the list")
	}
	headers := map[string]string{"Sec-Fetch-Site": "same-origin", "Content-Type": "application/json", "X-CSRF-Token": fx.feature.csrfToken(cookie.Value)}
	begin := fx.postJSON(t, "/admin/webauthn/register/begin", map[string]string{}, headers, cookie)
	if decode(t, begin)["signalUnknown"] != false {
		t.Fatalf("register begin must not allow signals on localhost: %s", begin.Body.String())
	}

	fx = signalFixture(t)
	_, cookie = fx.loggedIn(t)
	headers["X-CSRF-Token"] = fx.feature.csrfToken(cookie.Value)
	begin = fx.postJSON(t, "/admin/webauthn/register/begin", map[string]string{}, headers, cookie)
	if decode(t, begin)["signalUnknown"] != true {
		t.Fatalf("register begin must allow signals: %s", begin.Body.String())
	}
}
