package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"minimal-web-kit/features/auth/authtest"
)

func TestAccounts_CreateAndValidate(t *testing.T) {
	ctx := context.Background()
	s, db, _ := newAdminFixture(t)
	id, token, err := s.createAccountWithToken(ctx, "  編集担当A ", roleMember)
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.getAccount(ctx, id)
	if err != nil || a.Name != "編集担当A" || a.Role != roleMember || a.TokenExpiresAt.IsZero() {
		t.Fatalf("account = %+v, err=%v", a, err)
	}
	if target, err := s.checkSetupToken(ctx, token); err != nil || target.AccountID != id {
		t.Fatal("the invite token must belong to the new account")
	}
	if _, _, err := s.createAccountWithToken(ctx, "編集担当A", roleAdmin); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("duplicate name: %v", err)
	}
	for _, tc := range []struct{ name, role string }{{"", roleMember}, {"a\nb", roleMember}, {strings.Repeat("あ", maxAccountNameRunes+1), roleMember}, {"B", "owner"}} {
		if _, _, err := s.createAccountWithToken(ctx, tc.name, tc.role); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("%q/%q must be rejected: %v", tc.name, tc.role, err)
		}
	}
	assertRows(t, db, "SELECT COUNT(*) FROM auth_accounts", 1)
	assertRows(t, db, "SELECT COUNT(*) FROM auth_setup_tokens", 1)
}

// 有効な管理者（停止中でなく、有効なパスキーを持つ管理者）が0人になる操作と、自分自身への操作は拒否すること。
// 不変条件: AUTH-INV-1, AUTH-INV-2, AUTH-INV-6
func TestAccounts_KeepsAnEffectiveAdmin(t *testing.T) {
	ctx := context.Background()
	s, db, _ := newAdminFixture(t)
	me := newAccount(t, s, "管理者", roleAdmin)
	_ = s.addCredential(ctx, me, testCredential("me"), "key")
	pending := newAccount(t, s, "未登録の管理者", roleAdmin) // パスキー未登録の管理者は数えない
	member := newAccount(t, s, "メンバー", roleMember)

	for name, err := range map[string]error{
		"role":    s.setRole(ctx, me, me, roleMember),
		"disable": s.disableAccount(ctx, me, me),
		"delete":  s.deleteAccount(ctx, me, me),
	} {
		if !errors.Is(err, ErrSelfChange) {
			t.Errorf("self %s: %v", name, err)
		}
	}
	// pending から見ると、me が唯一の有効な管理者
	if err := s.setRole(ctx, pending, me, roleMember); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("demoting the last effective admin: %v", err)
	}
	if err := s.disableAccount(ctx, pending, me); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("disabling the last effective admin: %v", err)
	}
	if err := s.deleteAccount(ctx, pending, me); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("deleting the last effective admin: %v", err)
	}
	if _, err := s.reissueToken(ctx, me, true); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("resetting the last effective admin's keys: %v", err)
	}
	if err := s.setRole(ctx, me, 999, roleMember); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown account: %v", err)
	}

	// 他のアカウントへの操作は、そのアカウントのログイン状態を解除する
	session := newSession(t, s, member)
	if err := s.setRole(ctx, me, member, roleAdmin); err != nil {
		t.Fatal(err)
	}
	if _, err := s.validateSession(ctx, session); err == nil {
		t.Fatal("changing the role must revoke the account's sessions")
	}
	mine := newSession(t, s, me)
	if err := s.disableAccount(ctx, me, pending); err != nil {
		t.Fatal(err)
	}
	if err := s.enableAccount(ctx, me, pending); err != nil {
		t.Fatal(err)
	}
	if _, err := s.validateSession(ctx, mine); err != nil {
		t.Fatal("the actor's session must be kept")
	}
	if err := s.deleteAccount(ctx, me, member); err != nil {
		t.Fatal(err)
	}
	assertRows(t, db, "SELECT COUNT(*) FROM auth_accounts", 2)
}

// 不変条件: AUTH-INV-1, AUTH-INV-5, AUTH-INV-6, AUTH-INV-7
func TestAccounts_ReissueToken(t *testing.T) {
	ctx := context.Background()
	s, db, _ := newAdminFixture(t)
	me := newAccount(t, s, "管理者", roleAdmin)
	_ = s.addCredential(ctx, me, testCredential("me"), "key")
	member := newAccount(t, s, "メンバー", roleMember)
	_ = s.addCredential(ctx, member, testCredential("m"), "key")
	session := newSession(t, s, member)

	old, err := s.reissueToken(ctx, member, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.validateSession(ctx, session); err != nil {
		t.Fatal("reissuing without reset must keep sessions and passkeys")
	}
	token, err := s.reissueToken(ctx, member, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.checkSetupToken(ctx, old); err == nil {
		t.Fatal("reissuing must invalidate the previous token")
	}
	if _, err := s.validateSession(ctx, session); err == nil {
		t.Fatal("reset must revoke sessions")
	}
	assertRows(t, db, "SELECT COUNT(*) FROM auth_credentials", 1)
	if _, err := s.checkSetupToken(ctx, token); err != nil {
		t.Fatal(err)
	}
	if err := s.disableAccount(ctx, me, member); err != nil {
		t.Fatal(err)
	}
	if _, err := s.reissueToken(ctx, member, false); !errors.Is(err, ErrSetupTokenInvalid) {
		t.Fatal("a disabled account must not get a token")
	}
	if _, err := s.reissueToken(ctx, 999, false); !errors.Is(err, ErrNotFound) {
		t.Fatal("unknown account must be rejected")
	}
}

var setupTokenPattern = regexp.MustCompile(`/admin/setup#token=([A-Za-z0-9_-]+)`)

// 管理者がアカウントを作って登録URLを渡し、メンバーがパスキーを登録してログインするまで。
// メンバーはアカウントの管理を使えず、停止されるとログイン状態が解除されること。
// 不変条件: AUTH-INV-2, AUTH-INV-5, AUTH-INV-7, AUTH-INV-10, AUTH-INV-13
// ユースケース: UC-AUTH-4, UC-AUTH-6, UC-AUTH-7, UC-AUTH-8
func TestAccountsHTTP_InviteMemberAndRoles(t *testing.T) {
	fx := newFixture(t)
	_, admin := fx.loggedIn(t)

	rec := fx.do(t, http.MethodGet, "/admin/accounts", nil, admin)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "管理者") || rec.Header().Get("Content-Security-Policy") == authPageCSP {
		t.Fatalf("accounts page: %d", rec.Code)
	}
	if !strings.Contains(fx.do(t, http.MethodGet, "/admin/notes", nil, admin).Body.String(), "nav=/admin/notes") {
		t.Fatal("other features must stay first in the nav")
	}
	if rec := fx.do(t, http.MethodGet, "/admin/accounts/new", nil, admin); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `name="name"`) {
		t.Fatalf("new account page: %d", rec.Code)
	}

	// 作成すると、その応答だけに登録URLを表示する
	rec = fx.postForm(t, "/admin/accounts", withCSRF(fx, admin, url.Values{"name": {"<b>編集担当A</b>"}, "role": {roleMember}}), "same-origin", admin)
	m := setupTokenPattern.FindStringSubmatch(rec.Body.String())
	if rec.Code != http.StatusOK || m == nil {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "&lt;b&gt;編集担当A&lt;/b&gt;") {
		t.Fatal("names must be escaped")
	}
	if rec := fx.postForm(t, "/admin/accounts", withCSRF(fx, admin, url.Values{"name": {"<b>編集担当A</b>"}, "role": {roleMember}}), "same-origin", admin); rec.Code != http.StatusBadRequest {
		t.Fatalf("duplicate name: %d", rec.Code)
	}
	if strings.Contains(fx.logs.String(), "編集担当A") || strings.Contains(fx.logs.String(), m[1]) {
		t.Fatal("names and tokens must not be logged")
	}

	// メンバーが登録URLでパスキーを登録し、ログインする
	member := newFakeAuthenticator(t, algES256)
	begin := fx.postJSON(t, "/admin/webauthn/register/begin", map[string]string{"token": m[1]}, sameOriginHeaders)
	if decode(t, begin)["accountName"] != "<b>編集担当A</b>" {
		t.Fatal("register begin must return the account name to show")
	}
	if rec := fx.registerViaHTTP(t, member, m[1]); rec.Code != http.StatusOK {
		t.Fatalf("member register: %d %s", rec.Code, rec.Body.String())
	}
	assertRows(t, fx.db, "SELECT COUNT(*) FROM auth_credentials WHERE account_id = 2", 1)
	// 他のアカウントの userHandle ではログインできない
	if rec := fx.loginViaHTTP(t, member, fx.userID(t, 1)); rec.Code != http.StatusUnauthorized {
		t.Fatalf("login with another account's user handle: %d", rec.Code)
	}
	memberCookie := adminCookie(t, fx.loginViaHTTP(t, member, fx.userID(t, 2)))

	// メンバーは各機能の管理画面を使えるが、アカウントの管理は使えない
	if rec := fx.do(t, http.MethodGet, "/admin/notes", nil, memberCookie); rec.Code != http.StatusOK {
		t.Fatalf("member notes: %d", rec.Code)
	}
	for _, path := range []string{"/admin/accounts", "/admin/accounts/new", "/admin/accounts/1"} {
		if rec := fx.do(t, http.MethodGet, path, nil, memberCookie); rec.Code != http.StatusForbidden {
			t.Errorf("member GET %s: %d", path, rec.Code)
		}
	}
	if rec := fx.postForm(t, "/admin/accounts/1/role", withCSRF(fx, memberCookie, url.Values{"role": {roleMember}}), "same-origin", memberCookie); rec.Code != http.StatusForbidden {
		t.Fatalf("member POST role: %d", rec.Code)
	}
	rec = fx.do(t, http.MethodGet, "/admin/passkeys", nil, memberCookie)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "/admin/accounts") {
		t.Fatal("members must see their passkeys without the accounts link")
	}
	if !strings.Contains(fx.do(t, http.MethodGet, "/admin/passkeys", nil, admin).Body.String(), `href="/admin/accounts"`) {
		t.Fatal("admins must see the accounts link")
	}

	// 自分自身は変更できない
	if rec := fx.postForm(t, "/admin/accounts/1/disable", withCSRF(fx, admin, nil), "same-origin", admin); rec.Code != http.StatusConflict {
		t.Fatalf("self disable: %d", rec.Code)
	}
	// 停止すると、メンバーのログイン状態が解除され、ログインもできない
	rec = fx.postForm(t, "/admin/accounts/2/disable", withCSRF(fx, admin, nil), "same-origin", admin)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/accounts/2?notice=disabled" {
		t.Fatalf("disable: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if rec := fx.do(t, http.MethodGet, "/admin/notes", nil, memberCookie); rec.Code != http.StatusSeeOther {
		t.Fatal("a disabled member's session must be revoked")
	}
	if rec := fx.loginViaHTTP(t, member, fx.userID(t, 2)); rec.Code != http.StatusUnauthorized {
		t.Fatalf("a disabled member must not log in: %d", rec.Code)
	}
	if rec := fx.postForm(t, "/admin/accounts/2/setup-url", withCSRF(fx, admin, nil), "same-origin", admin); rec.Code != http.StatusConflict {
		t.Fatalf("setup URL for a disabled account: %d", rec.Code)
	}

	// 再開して登録URLを発行し直し、パスキーの削除とアカウントの削除まで
	if rec := fx.postForm(t, "/admin/accounts/2/enable", withCSRF(fx, admin, nil), "same-origin", admin); rec.Code != http.StatusSeeOther {
		t.Fatalf("enable: %d", rec.Code)
	}
	rec = fx.postForm(t, "/admin/accounts/2/setup-url", withCSRF(fx, admin, url.Values{"reset_keys": {"1"}}), "same-origin", admin)
	if rec.Code != http.StatusOK || setupTokenPattern.FindString(rec.Body.String()) == "" {
		t.Fatalf("reissue: %d", rec.Code)
	}
	assertRows(t, fx.db, "SELECT COUNT(*) FROM auth_credentials WHERE account_id = 2", 0)
	if rec := fx.postForm(t, "/admin/accounts/1/setup-url", withCSRF(fx, admin, url.Values{"reset_keys": {"1"}}), "same-origin", admin); rec.Code != http.StatusConflict {
		t.Fatalf("resetting own keys: %d", rec.Code)
	}
	if rec := fx.do(t, http.MethodGet, "/admin/accounts/2", nil, admin); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "未使用") {
		t.Fatalf("account page must show the unused URL: %d", rec.Code)
	}
	if rec := fx.do(t, http.MethodGet, "/admin/accounts/2/delete", nil, admin); rec.Code != http.StatusOK {
		t.Fatalf("delete confirm: %d", rec.Code)
	}
	if rec := fx.postForm(t, "/admin/accounts/2/delete", withCSRF(fx, admin, nil), "same-origin", admin); rec.Code != http.StatusSeeOther {
		t.Fatalf("delete: %d", rec.Code)
	}
	assertRows(t, fx.db, "SELECT COUNT(*) FROM auth_accounts", 1)
	if rec := fx.do(t, http.MethodGet, "/admin/accounts/2", nil, admin); rec.Code != http.StatusNotFound {
		t.Fatalf("deleted account: %d", rec.Code)
	}
	// 番号は1通りの書き方だけ（先頭のゼロ・符号は、存在するアカウント1でも404）
	for _, raw := range []string{"abc", "01", "+1"} {
		if rec := fx.do(t, http.MethodGet, "/admin/accounts/"+raw, nil, admin); rec.Code != http.StatusNotFound {
			t.Fatalf("invalid id %q: %d", raw, rec.Code)
		}
	}
}

// 管理者は他のアカウントのロールを変えられ、変更はログインし直した後の権限に効くこと。
//
// 不変条件: AUTH-INV-5, AUTH-INV-6
// ユースケース: UC-AUTH-5
func TestAccountsHTTP_ChangeRole(t *testing.T) {
	fx := newFixture(t)
	_, admin := fx.loggedIn(t)
	rec := fx.postForm(t, "/admin/accounts", withCSRF(fx, admin, url.Values{"name": {"編集担当"}, "role": {roleMember}}), "same-origin", admin)
	m := setupTokenPattern.FindStringSubmatch(rec.Body.String())
	if m == nil {
		t.Fatalf("create: %d", rec.Code)
	}
	member := newFakeAuthenticator(t, algES256)
	if rec := fx.registerViaHTTP(t, member, m[1]); rec.Code != http.StatusOK {
		t.Fatalf("register: %d", rec.Code)
	}
	login := func() *http.Cookie { return adminCookie(t, fx.loginViaHTTP(t, member, fx.userID(t, 2))) }
	before := login()
	if rec := fx.do(t, http.MethodGet, "/admin/accounts", nil, before); rec.Code != http.StatusForbidden {
		t.Fatalf("member before the change: %d", rec.Code)
	}

	if rec := fx.postForm(t, "/admin/accounts/2/role", withCSRF(fx, admin, url.Values{"role": {roleAdmin}}), "same-origin", admin); rec.Code != http.StatusSeeOther {
		t.Fatalf("promote: %d", rec.Code)
	}
	assertRows(t, fx.db, "SELECT COUNT(*) FROM auth_accounts WHERE id = 2 AND role = 'admin'", 1)
	if rec := fx.do(t, http.MethodGet, "/admin/notes", nil, before); rec.Code != http.StatusSeeOther {
		t.Fatalf("the old session must end with the role change: %d", rec.Code)
	}
	if rec := fx.do(t, http.MethodGet, "/admin/accounts", nil, login()); rec.Code != http.StatusOK {
		t.Fatalf("admin after the change: %d", rec.Code)
	}

	if rec := fx.postForm(t, "/admin/accounts/2/role", withCSRF(fx, admin, url.Values{"role": {roleMember}}), "same-origin", admin); rec.Code != http.StatusSeeOther {
		t.Fatalf("demote: %d", rec.Code)
	}
	if rec := fx.do(t, http.MethodGet, "/admin/accounts", nil, login()); rec.Code != http.StatusForbidden {
		t.Fatalf("member after the demotion: %d", rec.Code)
	}
}

// 管理者は、他のアカウントのパスキーを削除できる（最後の有効なパスキーは除く）。
// 不変条件: AUTH-INV-3, AUTH-INV-4
func TestAccountsHTTP_DeleteAnotherAccountsPasskey(t *testing.T) {
	fx := newFixture(t)
	ctx := context.Background()
	_, admin := fx.loggedIn(t)
	s := fx.feature.store
	id, _, err := s.createAccountWithToken(ctx, "B", roleMember)
	if err != nil {
		t.Fatal(err)
	}
	_ = s.addCredential(ctx, id, testCredential("b1"), "b1")
	_ = s.addCredential(ctx, id, testCredential("b2"), "b2")
	path := "/admin/accounts/2/passkeys/" + b64([]byte("b1")) + "/delete"
	if rec := fx.postForm(t, path, withCSRF(fx, admin, nil), "same-origin", admin); rec.Code != http.StatusSeeOther {
		t.Fatalf("delete passkey: %d", rec.Code)
	}
	if rec := fx.postForm(t, "/admin/accounts/2/passkeys/"+b64([]byte("b2"))+"/delete", withCSRF(fx, admin, nil), "same-origin", admin); rec.Code != http.StatusConflict {
		t.Fatalf("deleting the last passkey: %d", rec.Code)
	}
	// 自分のパスキーを、他のアカウントの経路で消すことはできない
	mine := mustFirstCredential(t, fx)
	if rec := fx.postForm(t, "/admin/accounts/2/passkeys/"+mine.Param()+"/delete", withCSRF(fx, admin, nil), "same-origin", admin); rec.Code != http.StatusNotFound {
		t.Fatalf("another account's passkey via the wrong route: %d", rec.Code)
	}
}

// 他の機能の AdminPage・AdminForm は管理者だけを通し、Page・Form は両方のロールを通すこと。
// 停止中のアカウントのセッションは、行が残っていても、どれも通らないこと。
// 不変条件: AUTH-INV-5
func TestAdminAPI_RolesAndDisabledAccounts(t *testing.T) {
	fx := newFixture(t)
	admin := authtest.NewSessionWithRole(t, fx.db, roleAdmin)
	member := authtest.NewSessionWithRole(t, fx.db, roleMember)

	if rec := fx.do(t, http.MethodGet, "/admin/notes/settings", nil, admin); rec.Code != http.StatusOK || rec.Body.String() != "settings for admin" {
		t.Fatalf("admin AdminPage: %d %s", rec.Code, rec.Body.String())
	}
	if rec := fx.postForm(t, "/admin/notes/settings", withCSRF(fx, admin, nil), "same-origin", admin); rec.Code != http.StatusOK {
		t.Fatalf("admin AdminForm: %d", rec.Code)
	}
	if rec := fx.do(t, http.MethodGet, "/admin/notes/settings", nil, member); rec.Code != http.StatusForbidden {
		t.Fatalf("member AdminPage: %d", rec.Code)
	}
	if rec := fx.postForm(t, "/admin/notes/settings", withCSRF(fx, member, nil), "same-origin", member); rec.Code != http.StatusForbidden {
		t.Fatalf("member AdminForm: %d", rec.Code)
	}
	if rec := fx.do(t, http.MethodGet, "/admin/notes", nil, member); rec.Code != http.StatusOK {
		t.Fatalf("member Page: %d", rec.Code)
	}
	if rec := fx.do(t, http.MethodGet, "/admin/notes/settings", nil); rec.Code != http.StatusSeeOther {
		t.Fatalf("AdminPage without session: %d", rec.Code)
	}

	// セッションの行を残したまま停止しても、要求ごとの確認で弾く
	mustExec(t, fx.db, "UPDATE auth_accounts SET disabled_at = 1")
	for _, path := range []string{"/admin/notes", "/admin/notes/settings", "/admin/passkeys"} {
		if rec := fx.do(t, http.MethodGet, path, nil, admin); rec.Code != http.StatusSeeOther {
			t.Errorf("disabled GET %s: %d", path, rec.Code)
		}
	}
	for _, path := range []string{"/admin/notes", "/admin/notes/settings"} {
		if rec := fx.postForm(t, path, withCSRF(fx, admin, url.Values{"text": {"x"}}), "same-origin", admin); rec.Code != http.StatusSeeOther {
			t.Errorf("disabled POST %s: %d", path, rec.Code)
		}
	}
	if len(fx.notes.saved) != 0 {
		t.Fatal("a disabled account must not reach the feature")
	}
}

// busyError は、SQLiteの書き込みの競合のエラーの見本です（SQLITE_BUSY）。
type busyError struct{}

func (busyError) Error() string { return "database is locked" }
func (busyError) Code() int     { return 5 }

// 書き込みの競合は503と Retry-After で返し、他の失敗は500のままにすること。JSONのAPIも同じ。
func TestWriteFailed_BusyInvitesRetry(t *testing.T) {
	fx := newFixture(t)
	busy := fmt.Errorf("save: %w", busyError{})
	for name, write := range map[string]func(w http.ResponseWriter, err error){
		"page": func(w http.ResponseWriter, err error) { fx.feature.writeFailed(w, "auth account action failed", err) },
		"api":  func(w http.ResponseWriter, err error) { fx.feature.writeStoreError(w, err) },
	} {
		w := httptest.NewRecorder()
		write(w, busy)
		if w.Code != http.StatusServiceUnavailable || w.Header().Get("Retry-After") == "" {
			t.Errorf("%s busy: %d %v", name, w.Code, w.Header())
		}
		w = httptest.NewRecorder()
		write(w, errors.New("broken"))
		if w.Code != http.StatusInternalServerError {
			t.Errorf("%s other failure: %d", name, w.Code)
		}
	}
}
