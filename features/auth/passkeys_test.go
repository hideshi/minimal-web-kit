package auth

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func withCSRF(fx *fixture, cookie *http.Cookie, form url.Values) string {
	if form == nil {
		form = url.Values{}
	}
	form.Set("csrf_token", fx.feature.csrfToken(cookie.Value))
	return form.Encode()
}

// loggedIn は登録トークンでパスキーを登録してログインし、認証器と管理者Cookieを返します。
func (fx *fixture) loggedIn(t *testing.T) (*fakeAuthenticator, *http.Cookie) {
	t.Helper()
	ctx := context.Background()
	token, err := Setup(ctx, fx.db, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	a := newFakeAuthenticator(t, algES256)
	if rec := fx.registerViaHTTP(t, a, token); rec.Code != http.StatusOK {
		t.Fatalf("register: %d %s", rec.Code, rec.Body.String())
	}
	userID, _ := fx.userID(t, 1), error(nil)
	return a, adminCookie(t, fx.loginViaHTTP(t, a, userID))
}

func mustFirstCredential(t *testing.T, fx *fixture) credentialInfo {
	t.Helper()
	creds, err := fx.feature.store.listCredentials(context.Background(), 1)
	if err != nil || len(creds) == 0 {
		t.Fatalf("no credentials: %v", err)
	}
	return creds[0]
}

// 不変条件: AUTH-INV-4, AUTH-INV-13, AUTH-INV-14
// ユースケース: UC-AUTH-3
func TestAdminPasskeys_AddRenameDelete(t *testing.T) {
	fx := newFixture(t)
	ctx := context.Background()
	first, cookie := fx.loggedIn(t)
	userID, _ := fx.userID(t, 1), error(nil)
	// 別の端末でもログインしておく
	other := adminCookie(t, fx.loginViaHTTP(t, first, userID))

	// パスキー管理画面は、追加にWebAuthnを使うため認証画面と同じCSPで返る
	rec := fx.do(t, http.MethodGet, "/admin/passkeys", nil, cookie)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Security-Policy") != authPageCSP {
		t.Fatalf("passkeys page: %d %s", rec.Code, rec.Header().Get("Content-Security-Policy"))
	}
	if !strings.Contains(rec.Body.String(), "有効なパスキーが1つだけです") {
		t.Fatal("a single active passkey must be warned about")
	}

	// 最後の有効なパスキーは削除できない
	firstParam := b64(first.credID)
	if rec := fx.postForm(t, "/admin/passkeys/"+firstParam+"/delete", withCSRF(fx, cookie, nil), "same-origin", cookie); rec.Code != http.StatusConflict {
		t.Fatalf("deleting the last passkey: %d", rec.Code)
	}

	// ログイン中の追加は、セッションとCSRFトークン（ヘッダー）が必要
	second := newFakeAuthenticator(t, algEdDSA)
	headers := map[string]string{"Sec-Fetch-Site": "same-origin", "Content-Type": "application/json"}
	if rec := fx.postJSON(t, "/admin/webauthn/register/begin", map[string]string{}, headers, cookie); rec.Code != http.StatusForbidden {
		t.Fatalf("adding without CSRF header: %d", rec.Code)
	}
	headers["X-CSRF-Token"] = fx.feature.csrfToken(cookie.Value)
	rec = fx.postJSON(t, "/admin/webauthn/register/begin", map[string]string{}, headers, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("add begin: %d %s", rec.Code, rec.Body.String())
	}
	opts := decode(t, rec)
	cd := clientDataJSON("webauthn.create", opts["challenge"].(string), fx.feature.cfg.Origin, false)
	rec = fx.postJSON(t, "/admin/webauthn/register/finish", map[string]any{
		"label":              "<i>予備のキー</i>",
		"rawId":              b64(second.credID),
		"clientDataJSON":     b64(cd),
		"authenticatorData":  b64(authData(fx.feature.cfg.RPID, second.flags|flagAT, second.count, second.credID)),
		"publicKey":          b64(second.spki()),
		"publicKeyAlgorithm": second.alg,
	}, headers, cookie)
	if rec.Code != http.StatusOK || decode(t, rec)["next"] != "/admin/passkeys?notice=added" {
		t.Fatalf("add finish: %d %s", rec.Code, rec.Body.String())
	}
	rec = fx.do(t, http.MethodGet, "/admin/passkeys", nil, cookie)
	if !strings.Contains(rec.Body.String(), "&lt;i&gt;予備のキー&lt;/i&gt;") || strings.Contains(rec.Body.String(), "<i>予備") {
		t.Fatal("labels must be escaped")
	}

	// 名前の変更
	if rec := fx.postForm(t, "/admin/passkeys/"+firstParam+"/label", withCSRF(fx, cookie, url.Values{"label": {""}}), "same-origin", cookie); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty label: %d", rec.Code)
	}
	if rec := fx.postForm(t, "/admin/passkeys/"+firstParam+"/label", withCSRF(fx, cookie, url.Values{"label": {"仕事用PC"}}), "same-origin", cookie); rec.Code != http.StatusSeeOther {
		t.Fatalf("rename: %d", rec.Code)
	}
	if c, _ := fx.feature.store.getCredentialInfo(ctx, 1, first.credID); c.Label != "仕事用PC" {
		t.Fatalf("label not updated: %q", c.Label)
	}

	// 削除すると、現在のセッション以外のログイン状態を解除する
	if rec := fx.do(t, http.MethodGet, "/admin/passkeys/"+firstParam+"/delete", nil, cookie); rec.Code != http.StatusOK {
		t.Fatalf("delete confirm: %d", rec.Code)
	}
	rec = fx.postForm(t, "/admin/passkeys/"+firstParam+"/delete", withCSRF(fx, cookie, nil), "same-origin", cookie)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/passkeys?notice=removed" {
		t.Fatalf("delete: %d", rec.Code)
	}
	if rec := fx.do(t, http.MethodGet, "/admin/notes", nil, cookie); rec.Code != http.StatusOK {
		t.Fatal("the current session must be kept")
	}
	if rec := fx.do(t, http.MethodGet, "/admin/notes", nil, other); rec.Code != http.StatusSeeOther {
		t.Fatal("other sessions must be revoked")
	}
	if rec := fx.loginViaHTTP(t, first, userID); rec.Code != http.StatusUnauthorized {
		t.Fatal("a deleted passkey must not log in")
	}
	if strings.Contains(fx.logs.String(), "予備のキー") || strings.Contains(fx.logs.String(), "仕事用PC") {
		t.Fatal("labels must not be logged")
	}
}

func TestAdminPasskeys_DisabledPasskeyIsShownAndDeletable(t *testing.T) {
	fx := newFixture(t)
	ctx := context.Background()
	first, _ := fx.loggedIn(t)
	userID, _ := fx.userID(t, 1), error(nil)

	second := newFakeAuthenticator(t, algES256)
	if err := fx.feature.store.addCredential(ctx, 1, newCredential{ID: second.credID, PublicKey: second.spki(), Alg: second.alg}, "予備"); err != nil {
		t.Fatal(err)
	}
	if err := fx.feature.store.disableCredential(ctx, first.credID, 1); err != nil {
		t.Fatal(err)
	}
	cookie := adminCookie(t, fx.loginViaHTTP(t, second, userID))

	// 全画面の上部に警告を出し、管理画面に無効化の表示を出す
	rec := fx.do(t, http.MethodGet, "/admin/passkeys", nil, cookie)
	if !strings.Contains(rec.Body.String(), "複製の疑いで無効化されたパスキーがあります") {
		t.Fatal("the warning must be shown on admin pages")
	}
	rec = fx.do(t, http.MethodGet, "/admin/passkeys", nil, cookie)
	if !strings.Contains(rec.Body.String(), "無効（複製の疑い）") {
		t.Fatal("the disabled passkey must be marked")
	}
	// 無効化済みのパスキーは、有効なパスキーが1つでも削除できる
	rec = fx.postForm(t, "/admin/passkeys/"+b64(first.credID)+"/delete", withCSRF(fx, cookie, nil), "same-origin", cookie)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("delete disabled passkey: %d", rec.Code)
	}
	rec = fx.do(t, http.MethodGet, "/admin/passkeys", nil, cookie)
	if strings.Contains(rec.Body.String(), "複製の疑いで無効化されたパスキーがあります") {
		t.Fatal("the warning must disappear after deletion")
	}
}

// 不変条件: AUTH-INV-3, AUTH-INV-4
func TestAdminStore_DeleteCredentialKeepsOneActive(t *testing.T) {
	s, _, _ := newAdminFixture(t)
	ctx := context.Background()
	account := newAccount(t, s, "管理者", roleAdmin)
	other := newAccount(t, s, "B", roleMember)
	if err := s.addCredential(ctx, other, testCredential("other"), "他のキー"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b"} {
		if err := s.addCredential(ctx, account, testCredential(id), "キー"+id); err != nil {
			t.Fatal(err)
		}
	}
	// 並行して両方を削除しても、有効なパスキーは1つ残る
	errs := make(chan error, 2)
	for _, id := range []string{"a", "b"} {
		go func(id string) { errs <- s.deleteCredential(ctx, account, []byte(id), "") }(id)
	}
	ok := 0
	for range 2 {
		if err := <-errs; err == nil {
			ok++
		} else if err != ErrLastCredential {
			t.Fatal(err)
		}
	}
	creds, _ := s.listCredentials(ctx, account)
	if ok != 1 || len(creds) != 1 {
		t.Fatalf("deleted %d, remaining %d", ok, len(creds))
	}
	if err := s.deleteCredential(ctx, account, []byte("missing"), ""); err != ErrNotFound {
		t.Fatalf("missing credential: %v", err)
	}
	// 他のアカウントのパスキーは、見つからない扱い（削除も名前の変更もできない）
	if err := s.deleteCredential(ctx, account, []byte("other"), ""); err != ErrNotFound {
		t.Fatalf("another account's credential: %v", err)
	}
	if err := s.renameCredential(ctx, account, []byte("other"), "x"); err != ErrNotFound {
		t.Fatalf("renaming another account's credential: %v", err)
	}
}

// 他の機能（見本の notes）は、管理者認証の口（Page・Form）で管理画面を守れること。
// 不変条件: AUTH-INV-14
func TestAdminAPI_PageAndFormProtectOtherFeatures(t *testing.T) {
	fx := newFixture(t)
	_, cookie := fx.loggedIn(t)

	if rec := fx.do(t, http.MethodGet, "/admin/notes", nil); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/login" {
		t.Fatalf("page without session: %d", rec.Code)
	}
	rec := fx.do(t, http.MethodGet, "/admin/notes", nil, cookie)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "csrf="+fx.feature.csrfToken(cookie.Value)) {
		t.Fatalf("page with session: %d %s", rec.Code, rec.Body.String())
	}
	// 他の機能のリンクが、管理画面のナビの先頭に来る
	if !strings.Contains(rec.Body.String(), "nav=/admin/notes") {
		t.Fatal("other features must come first in the admin nav")
	}
	if rec := fx.do(t, http.MethodGet, "/admin", nil, cookie); rec.Header().Get("Location") != "/admin/notes" {
		t.Fatalf("admin home must open the first feature: %s", rec.Header().Get("Location"))
	}

	for name, tc := range map[string]struct {
		body, site string
		cookie     *http.Cookie
		want       int
	}{
		"no CSRF token":     {url.Values{"text": {"x"}}.Encode(), "same-origin", cookie, http.StatusForbidden},
		"cross-site":        {withCSRF(fx, cookie, url.Values{"text": {"x"}}), "cross-site", cookie, http.StatusForbidden},
		"no Sec-Fetch-Site": {withCSRF(fx, cookie, url.Values{"text": {"x"}}), "", cookie, http.StatusForbidden},
		"no session":        {withCSRF(fx, cookie, url.Values{"text": {"x"}}), "same-origin", nil, http.StatusSeeOther},
	} {
		if rec := fx.postForm(t, "/admin/notes", tc.body, tc.site, tc.cookie); rec.Code != tc.want {
			t.Errorf("%s: got %d, want %d", name, rec.Code, tc.want)
		}
	}
	if len(fx.notes.saved) != 0 {
		t.Fatal("rejected requests must not reach the feature")
	}
	if rec := fx.postForm(t, "/admin/notes", withCSRF(fx, cookie, url.Values{"text": {"ok"}}), "same-origin", cookie); rec.Code != http.StatusOK || len(fx.notes.saved) != 1 {
		t.Fatalf("valid form: %d %v", rec.Code, fx.notes.saved)
	}
}
