package auth

import (
	"bytes"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"html/template"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"time"

	"minimal-web-kit/features"
	"minimal-web-kit/internal/store"
	"minimal-web-kit/internal/web"
	"minimal-web-kit/internal/web/guard"
	"minimal-web-kit/internal/web/notice"
)

//go:embed static/webauthn.js
var staticFS embed.FS

//go:embed views/*.html
var viewsFS embed.FS

const (
	adminCookieName = "admin_session"
	// WebAuthnのPOST本文の上限（Coreの1MBより小さくする）
	maxWebAuthnBody = 64 << 10
	webauthnTimeout = 120000 // ミリ秒

	// 認証画面だけ、自サイトのJSを許可するCSP。他のページはCoreの script-src 'none' のまま。
	authPageCSP = "default-src 'self'; style-src 'self'; script-src 'self'; connect-src 'self'; form-action 'self'; frame-ancestors 'none'; object-src 'none'; base-uri 'self';"
)

// 利用者に返す固定のメッセージ。入力値や内部の詳細は含めない。
var errorMessages = map[string]string{
	"bad_request":           "リクエストの形式が正しくありません。ページを再読み込みしてやり直してください。",
	"forbidden":             "このリクエストは受け付けられません。管理画面を開き直してください。",
	"setup_token_invalid":   "登録用のURLが無効か、期限切れです。システム管理者に登録URLを発行し直してもらってください（最初のシステム管理者は、サーバーで make auth-setup を実行し直してください）。",
	"synced_credential":     "このサイトでは、同期型のパスキー（スマートフォン、Googleパスワードマネージャー、iCloudキーチェーンなど）を登録できない設定です。Windows Helloやセキュリティキーなど、この端末だけに保存される認証器を使ってください。",
	"user_not_verified":     "指紋・顔・PINによる本人確認が必要です。認証器の設定を確認してください。",
	"public_key_invalid":    "この認証器の鍵の種類には対応していません。別の認証器を使ってください。",
	"credential_exists":     "この認証器は登録済みです。",
	"label_invalid":         "端末名は1〜50文字で入力してください。",
	"registration_rejected": "登録できませんでした。ページを開き直して、もう一度お試しください。",
	"login_rejected":        "このパスキーではログインできません。別のパスキーでログインするか、システム管理者に登録URLを発行し直してもらってください。",
	"internal_error":        "処理に失敗しました。もう一度お試しください。",
	"busy":                  "一時的に処理できませんでした。少し待ってから、もう一度お試しください。",
}

func parseTemplates() map[string]*template.Template {
	pages := map[string]*template.Template{}
	for _, page := range []string{
		"admin_setup.html", "admin_login.html", "admin_passkeys.html", "admin_passkey_delete.html", "admin_data_notice.html",
		"admin_agent_flow.html", "admin_accounts.html", "admin_account_form.html", "admin_account_created.html", "admin_account.html", "admin_account_delete.html", "admin_account_passkey_delete.html",
	} {
		pages[page] = template.Must(web.ParseWithComponents(viewsFS, "views/admin_layout.html", "views/account_parts.html", "views/"+page))
	}
	return pages
}

func (f *Feature) requestID(w http.ResponseWriter) slog.Attr {
	return web.RequestIDAttr(w)
}

func (f *Feature) renderAdmin(w http.ResponseWriter, page string, data map[string]any, withScript bool) {
	f.renderAdminStatus(w, http.StatusOK, page, data, withScript)
}

func (f *Feature) renderAdminStatus(w http.ResponseWriter, status int, page string, data map[string]any, withScript bool) {
	var buf bytes.Buffer
	if err := f.pages[page].ExecuteTemplate(&buf, "layout", data); err != nil {
		f.logger.Error("auth render admin failed", f.requestID(w))
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	if withScript {
		w.Header().Set("Content-Security-Policy", authPageCSP)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}

func (f *Feature) writeError(w http.ResponseWriter, status int, code string) {
	f.writeJSON(w, status, map[string]string{"error": code, "message": errorMessages[code]})
}

// writeFailed は、画面の送信の書き込みの失敗を返します。競合は再試行できるので503にします。
func (f *Feature) writeFailed(w http.ResponseWriter, event string, err error) {
	if store.Busy(err) {
		f.logger.Warn(event, f.requestID(w))
		web.WriteBusy(w)
		return
	}
	f.logger.Error(event, f.requestID(w))
	http.Error(w, "Internal Server Error", http.StatusInternalServerError)
}

// writeStoreError は、WebAuthnのAPIの書き込みの失敗を返します。競合は再試行できるので503にします。
func (f *Feature) writeStoreError(w http.ResponseWriter, err error) {
	if store.Busy(err) {
		w.Header().Set("Retry-After", "1")
		f.writeError(w, http.StatusServiceUnavailable, "busy")
		return
	}
	f.writeError(w, http.StatusInternalServerError, "internal_error")
}

// --- 同一オリジン・CSRF・セッション ---

// sameOrigin は、管理画面へのPOSTが同じオリジンから送られたことを確認します（guard.SameOrigin）。
func (f *Feature) sameOrigin(r *http.Request) bool {
	return guard.SameOrigin(r, f.cfg.Origin)
}

func (f *Feature) csrfToken(sessionToken string) string {
	return f.csrf.Token(sessionToken)
}

func (f *Feature) validCSRF(sessionToken, token string) bool {
	return f.csrf.Valid(sessionToken, token)
}

// adminSession は有効な管理者セッションのトークンを返します。無ければ空文字を返します。
func (f *Feature) adminSession(r *http.Request) string {
	token, _ := f.currentAccount(r)
	return token
}

// currentAccount は、有効な管理者セッションのトークンとアカウントを返します。無ければトークンは空文字です。
// アカウントが停止中・削除済みなら、セッションの行が残っていても無効として扱います。
func (f *Feature) currentAccount(r *http.Request) (string, sessionAccount) {
	c, err := r.Cookie(adminCookieName)
	if err != nil || c.Value == "" {
		return "", sessionAccount{}
	}
	a, err := f.store.validateSession(r.Context(), c.Value)
	if err != nil {
		return "", sessionAccount{}
	}
	return c.Value, a
}

func (f *Feature) setAdminCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     adminCookieName,
		Value:    token,
		Path:     "/admin",
		MaxAge:   int(adminSessionMax / time.Second),
		HttpOnly: true,
		Secure:   f.cfg.SecureCookie,
		SameSite: http.SameSiteStrictMode,
	})
}

func (f *Feature) clearAdminCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     adminCookieName,
		Value:    "",
		Path:     "/admin",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   f.cfg.SecureCookie,
		SameSite: http.SameSiteStrictMode,
	})
}

// --- 画面 ---

// session は、ログイン中のアカウントの要求に付く情報です（features.AdminSession）。
type session struct {
	f         *Feature
	token     string
	accountID int64
	role      string
	disabled  bool   // 複製の疑いで無効化されたパスキーがあるか（システム管理者は全アカウント、他のロールは自分の分）
	path      string // 要求のパス。ナビの今いるページの印に使う
}

func (s session) CSRFToken() string   { return s.f.csrfToken(s.token) }
func (s session) AccountID() int64    { return s.accountID }
func (s session) Role() features.Role { return features.Role(s.role) }
func (s session) IsAdmin() bool       { return s.role == roleAdmin }

// PageData は、管理画面のレイアウト（views/admin_layout.html）に渡す値を返します。
// current は画面が属するナビのリンクです。その一覧自体なら aria-current="page"、配下の画面なら "true" になります。
func (s session) PageData(title, current string) map[string]any {
	return map[string]any{
		"Title":            title,
		"Current":          current,
		"RequestPath":      s.path,
		"AdminNav":         s.f.adminNav(s.role),
		"LoggedIn":         true,
		"IsAdmin":          s.IsAdmin(),
		"RoleLabel":        roleLabel(s.role),
		"CSRFToken":        s.CSRFToken(),
		"DisabledPasskeys": s.disabled,
		"Now":              s.f.now(),
	}
}

// adminNav は管理画面のナビです。他の機能のリンク（例: お知らせの管理）の後に、パスキーの管理を置きます。
// システム管理者には、アカウントの管理も出します。一般ユーザーには、AllRoles のリンクだけを出します。
func (f *Feature) adminNav(role string) []web.NavLink {
	isAdmin := role == roleAdmin
	var nav []web.NavLink
	for _, l := range f.navLinks {
		if web.NavVisible(l, features.Role(role).CanManageSite(), isAdmin) {
			nav = append(nav, l)
		}
	}
	nav = append(nav, web.NavLink{Label: "パスキーの管理", Href: "/admin/passkeys"}, web.NavLink{Label: "保存する情報", Href: "/admin/data-notice"})
	if isAdmin {
		nav = append(nav, web.NavLink{Label: "アカウントの管理", Href: "/admin/accounts"})
	}
	return nav
}

// loadSession は、有効な管理者セッションを読み込みます。無ければ ok=false です。
func (f *Feature) loadSession(w http.ResponseWriter, r *http.Request) (session, bool) {
	token, a := f.currentAccount(r)
	if token == "" {
		return session{}, false
	}
	scope := a.AccountID
	if a.Role == roleAdmin {
		scope = 0
	}
	disabled, err := f.store.countDisabledCredentials(r.Context(), scope)
	if err != nil {
		f.logger.Error("auth count disabled passkeys failed", f.requestID(w))
	}
	return session{f: f, token: token, accountID: a.AccountID, role: a.Role, disabled: disabled > 0, path: r.URL.Path}, true
}

// page は、ログイン中のアカウントだけが見られる画面（GET）を守ります。未ログインならログイン画面へ移します。
func (f *Feature) page(next func(w http.ResponseWriter, r *http.Request, s session)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		web.NoTransform(w) // 管理画面は前段に書き換えさせない（受信箱のメールアドレス等）
		s, ok := f.loadSession(w, r)
		if !ok {
			http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
			return
		}
		next(w, r, s)
	}
}

// form は、管理画面のフォーム送信（POST）に、同一オリジン・管理者セッション・CSRFトークンの確認を掛けます。
func (f *Feature) form(next func(w http.ResponseWriter, r *http.Request, s session)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		web.NoTransform(w)
		if !f.sameOrigin(r) {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		s, ok := f.loadSession(w, r)
		if !ok {
			// 期限切れ等。送信内容は保存せず、ログインし直してもらう
			http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Bad Request", http.StatusBadRequest)
			return
		}
		if !f.validCSRF(s.token, r.PostForm.Get("csrf_token")) {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		next(w, r, s)
	}
}

// siteOnly は、各機能の管理画面を使えないロールに403を返します（page・form の内側で使う）。
// 許可の一覧で判定するので、未知のロールも通しません。
func siteOnly(next func(w http.ResponseWriter, r *http.Request, s session)) func(w http.ResponseWriter, r *http.Request, s session) {
	return func(w http.ResponseWriter, r *http.Request, s session) {
		if !s.Role().CanManageSite() {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		next(w, r, s)
	}
}

// adminOnly は、ロールがシステム管理者でなければ403を返します（page・form の内側で使う）。
func adminOnly(next func(w http.ResponseWriter, r *http.Request, s session)) func(w http.ResponseWriter, r *http.Request, s session) {
	return func(w http.ResponseWriter, r *http.Request, s session) {
		if !s.IsAdmin() {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		next(w, r, s)
	}
}

// adminAPI は、他の機能に渡す features.Admin の実装です。
type adminAPI struct{ f *Feature }

func toSession(next func(w http.ResponseWriter, r *http.Request, s features.AdminSession)) func(w http.ResponseWriter, r *http.Request, s session) {
	return func(w http.ResponseWriter, r *http.Request, s session) { next(w, r, s) }
}

func (a adminAPI) Page(next func(w http.ResponseWriter, r *http.Request, s features.AdminSession)) http.HandlerFunc {
	return a.f.page(siteOnly(toSession(next)))
}

func (a adminAPI) Form(next func(w http.ResponseWriter, r *http.Request, s features.AdminSession)) http.HandlerFunc {
	return a.f.form(siteOnly(toSession(next)))
}

func (a adminAPI) AdminPage(next func(w http.ResponseWriter, r *http.Request, s features.AdminSession)) http.HandlerFunc {
	return a.f.page(adminOnly(toSession(next)))
}

func (a adminAPI) AdminForm(next func(w http.ResponseWriter, r *http.Request, s features.AdminSession)) http.HandlerFunc {
	return a.f.form(adminOnly(toSession(next)))
}

func (a adminAPI) AccountPage(next func(w http.ResponseWriter, r *http.Request, s features.AdminSession)) http.HandlerFunc {
	return a.f.page(toSession(next))
}

func (a adminAPI) AccountForm(next func(w http.ResponseWriter, r *http.Request, s features.AdminSession)) http.HandlerFunc {
	return a.f.form(toSession(next))
}

func (a adminAPI) Layout() web.Layout {
	return web.Layout{FS: viewsFS, File: "views/admin_layout.html"}
}

// handleAdminHome は、管理画面の最初のページ（他の機能の管理画面、無ければパスキーの管理）へ移します。
func (f *Feature) handleAdminHome(w http.ResponseWriter, r *http.Request) {
	token, a := f.currentAccount(r)
	if token == "" {
		http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, f.adminNav(a.Role)[0].Href, http.StatusSeeOther)
}

// handleDataNotice は、ログインして使う人について保存する情報を出します。公開のプライバシーポリシーには載せない内容です。
func (f *Feature) handleDataNotice(w http.ResponseWriter, r *http.Request, s session) {
	data := s.PageData("保存する情報", "/admin/data-notice")
	data["Notices"] = f.accountNotices
	f.renderAdmin(w, "admin_data_notice.html", data, false)
}

func (f *Feature) handleSetupPage(w http.ResponseWriter, r *http.Request) {
	web.NoTransform(w)
	f.renderAdmin(w, "admin_setup.html", map[string]any{"Title": "パスキーの登録", "LabelField": labelField("仕事用PCのWindows Hello"), "AllowSynced": f.cfg.AllowSynced}, true)
}

func (f *Feature) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	web.NoTransform(w)
	if f.adminSession(r) != "" {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	f.renderAdmin(w, "admin_login.html", map[string]any{
		"AllowSynced": f.cfg.AllowSynced,
		"Title":       "ログイン",
		"Registered":  r.URL.Query().Get("registered") == "1",
	}, true)
}

func (f *Feature) handleLogout(w http.ResponseWriter, r *http.Request, s session) {
	if err := f.store.deleteSession(r.Context(), s.token); err != nil {
		f.logger.Error("auth logout failed", f.requestID(w))
	}
	f.clearAdminCookie(w)
	f.logger.Info("auth logout", f.requestID(w))
	http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
}

func (f *Feature) handleStaticJS(w http.ResponseWriter, r *http.Request) {
	js, err := staticFS.ReadFile("static/webauthn.js")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	_, _ = w.Write(js)
}

// --- WebAuthn API（認証画面のJSから呼ぶ） ---

// jsonEndpoint は、同一オリジン・JSON形式・本文サイズを確認してから本文を読み込みます。
func (f *Feature) jsonEndpoint(next func(w http.ResponseWriter, r *http.Request, body []byte)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !f.sameOrigin(r) {
			f.writeError(w, http.StatusForbidden, "forbidden")
			return
		}
		if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
			f.writeError(w, http.StatusUnsupportedMediaType, "bad_request")
			return
		}
		var buf bytes.Buffer
		if _, err := buf.ReadFrom(http.MaxBytesReader(w, r.Body, maxWebAuthnBody)); err != nil {
			f.writeError(w, http.StatusRequestEntityTooLarge, "bad_request")
			return
		}
		next(w, r, buf.Bytes())
	}
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// b64Field はJSONのbase64url（パディングなし）文字列です。
type b64Field []byte

func (b *b64Field) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		*b = nil
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(s)
	if err != nil {
		return err
	}
	*b = raw
	return nil
}

// registrationTarget は、パスキーを登録するアカウントです。
type registrationTarget struct {
	AccountID      int64
	Name           string
	WebAuthnUserID []byte
	viaSession     bool // ログイン中の追加（登録トークンではない）
}

// registrationAuth は登録の認可を確認し、登録先のアカウントを返します。
// 登録トークン（結びつくアカウントに登録する）、またはログイン中の管理者セッション＋CSRFトークン（自分に登録する）が必要です。
func (f *Feature) registrationAuth(r *http.Request, setupToken string) (registrationTarget, bool) {
	if setupToken != "" {
		t, err := f.store.checkSetupToken(r.Context(), setupToken)
		if err != nil {
			return registrationTarget{}, false
		}
		return registrationTarget{AccountID: t.AccountID, Name: t.Name, WebAuthnUserID: t.WebAuthnUserID}, true
	}
	token, a := f.currentAccount(r)
	if token == "" || !f.validCSRF(token, r.Header.Get("X-CSRF-Token")) {
		return registrationTarget{}, false
	}
	acc, err := f.store.accountForRegistration(r.Context(), a.AccountID)
	if err != nil {
		return registrationTarget{}, false
	}
	acc.viaSession = true
	return acc, true
}

func (f *Feature) handleRegisterBegin(w http.ResponseWriter, r *http.Request, body []byte) {
	var req struct {
		Token string `json:"token"`
	}
	if json.Unmarshal(body, &req) != nil {
		f.writeError(w, http.StatusBadRequest, "bad_request")
		return
	}
	target, ok := f.registrationAuth(r, req.Token)
	if !ok {
		code := "forbidden"
		if req.Token != "" {
			code = "setup_token_invalid"
		}
		f.writeError(w, http.StatusForbidden, code)
		return
	}
	challenge, err := f.challenges.issue(purposeRegister)
	if err != nil {
		f.writeError(w, http.StatusInternalServerError, "internal_error")
		return
	}
	// 同じ認証器の重複登録を防ぐのは、登録先のアカウントのパスキーだけ（他のアカウントのIDは渡さない）
	ids, err := f.store.credentialIDs(r.Context(), target.AccountID)
	if err != nil {
		f.writeError(w, http.StatusInternalServerError, "internal_error")
		return
	}
	exclude := []map[string]string{}
	for _, id := range ids {
		exclude = append(exclude, map[string]string{"type": "public-key", "id": b64(id)})
	}
	f.writeJSON(w, http.StatusOK, map[string]any{
		"challenge": challenge,
		"rp":        map[string]string{"id": f.cfg.RPID, "name": "minimal-web-kit"},
		// user.name は固定の識別子、displayName は呼び名（認証器のアカウント選択に表示される）
		"user":        map[string]string{"id": b64(target.WebAuthnUserID), "name": "account-" + strconv.FormatInt(target.AccountID, 10), "displayName": target.Name},
		"accountName": target.Name,
		"pubKeyCredParams": []map[string]any{
			{"type": "public-key", "alg": algES256},
			{"type": "public-key", "alg": algEdDSA},
			{"type": "public-key", "alg": algRS256},
		},
		"authenticatorSelection": map[string]string{"residentKey": "required", "userVerification": "required"},
		"attestation":            "none",
		"timeout":                webauthnTimeout,
		"excludeCredentials":     exclude,
	})
}

// registrationErrorCode は検証エラーを、利用者に返す固定のコードに変換します。
func registrationErrorCode(err error) string {
	switch {
	case errors.Is(err, ErrSyncedCredential):
		return "synced_credential"
	case errors.Is(err, ErrUserNotVerified):
		return "user_not_verified"
	case errors.Is(err, ErrPublicKeyInvalid):
		return "public_key_invalid"
	case errors.Is(err, ErrCredentialExists):
		return "credential_exists"
	case errors.Is(err, ErrSetupTokenInvalid):
		return "setup_token_invalid"
	case errors.Is(err, ErrInvalidInput):
		return "label_invalid"
	}
	return "registration_rejected"
}

func (f *Feature) handleRegisterFinish(w http.ResponseWriter, r *http.Request, body []byte) {
	var req struct {
		Token              string   `json:"token"`
		Label              string   `json:"label"`
		RawID              b64Field `json:"rawId"`
		ClientDataJSON     b64Field `json:"clientDataJSON"`
		AuthenticatorData  b64Field `json:"authenticatorData"`
		PublicKey          b64Field `json:"publicKey"`
		PublicKeyAlgorithm int64    `json:"publicKeyAlgorithm"`
	}
	if json.Unmarshal(body, &req) != nil {
		f.writeError(w, http.StatusBadRequest, "bad_request")
		return
	}
	target, ok := f.registrationAuth(r, req.Token)
	if !ok {
		code := "forbidden"
		if req.Token != "" {
			code = "setup_token_invalid"
		}
		f.writeError(w, http.StatusForbidden, code)
		return
	}
	if _, err := normalizeLabel(req.Label); err != nil {
		f.writeError(w, http.StatusBadRequest, "label_invalid")
		return
	}
	cred, err := verifyRegistration(f.rp(), f.challenges, registrationResponse{
		RawID:              req.RawID,
		ClientDataJSON:     req.ClientDataJSON,
		AuthenticatorData:  req.AuthenticatorData,
		PublicKey:          req.PublicKey,
		PublicKeyAlgorithm: req.PublicKeyAlgorithm,
	})
	if err == nil {
		if target.viaSession {
			err = f.store.addCredential(r.Context(), target.AccountID, cred, req.Label)
		} else {
			err = f.store.registerWithSetupToken(r.Context(), req.Token, cred, req.Label)
		}
	}
	if err != nil {
		code := registrationErrorCode(err)
		f.logger.Warn("auth passkey registration rejected", f.requestID(w), slog.String("reason", code))
		f.writeError(w, http.StatusBadRequest, code)
		return
	}
	f.logger.Info("auth passkey registered", f.requestID(w), slog.Int64("account_id", target.AccountID))
	next := "/admin/login?registered=1"
	if target.viaSession {
		next = notice.URL("/admin/passkeys", "added")
	}
	f.writeJSON(w, http.StatusOK, map[string]string{"next": next})
}

func (f *Feature) handleLoginBegin(w http.ResponseWriter, r *http.Request, body []byte) {
	challenge, err := f.challenges.issue(purposeLogin)
	if err != nil {
		f.writeError(w, http.StatusInternalServerError, "internal_error")
		return
	}
	f.writeJSON(w, http.StatusOK, map[string]any{
		"challenge":        challenge,
		"rpId":             f.cfg.RPID,
		"userVerification": "required",
		"timeout":          webauthnTimeout,
		"allowCredentials": []any{},
	})
}

func (f *Feature) handleLoginFinish(w http.ResponseWriter, r *http.Request, body []byte) {
	var req struct {
		RawID             b64Field `json:"rawId"`
		ClientDataJSON    b64Field `json:"clientDataJSON"`
		AuthenticatorData b64Field `json:"authenticatorData"`
		Signature         b64Field `json:"signature"`
		UserHandle        b64Field `json:"userHandle"`
	}
	if json.Unmarshal(body, &req) != nil {
		f.writeError(w, http.StatusBadRequest, "bad_request")
		return
	}
	reject := func(reason string) {
		f.logger.Warn("auth login rejected", f.requestID(w), slog.String("reason", reason))
		f.writeError(w, http.StatusUnauthorized, "login_rejected")
	}
	cred, err := f.store.getCredential(r.Context(), req.RawID)
	if err != nil {
		reject("credential_unavailable")
		return
	}
	count, err := verifyAssertion(f.rp(), f.challenges, assertionResponse{
		RawID:             req.RawID,
		ClientDataJSON:    req.ClientDataJSON,
		AuthenticatorData: req.AuthenticatorData,
		Signature:         req.Signature,
		UserHandle:        req.UserHandle,
	}, cred.storedCredential, cred.WebAuthnUserID)
	// 判定の順: 署名の検証 → 署名カウンタ（複製の疑い） → アカウントの停止。停止中でも複製の疑いの検出は働かせる
	if errors.Is(err, ErrCloneSuspected) {
		if derr := f.store.disableCredential(r.Context(), cred.ID, cred.AccountID); derr != nil {
			f.logger.Error("auth disable passkey failed", f.requestID(w))
		}
		f.logger.Warn("auth passkey disabled", f.requestID(w), slog.Int64("account_id", cred.AccountID))
		reject("clone_suspected")
		return
	}
	if err != nil {
		reject(verificationReason(err))
		return
	}
	if cred.AccountDisabled {
		reject("account_disabled")
		return
	}
	token, err := f.store.login(r.Context(), cred.ID, cred.AccountID, count)
	if errors.Is(err, ErrSignCountConflict) {
		reject("sign_count_conflict")
		return
	} else if errors.Is(err, ErrSessionInvalid) {
		reject("account_disabled")
		return
	} else if err != nil {
		f.writeStoreError(w, err)
		return
	}
	f.setAdminCookie(w, token)
	f.logger.Info("auth login succeeded", f.requestID(w), slog.Int64("account_id", cred.AccountID))
	next := "/admin"
	if f.loginNext != nil {
		next = f.loginNext(r)
	}
	f.writeJSON(w, http.StatusOK, map[string]string{"next": next})
}

// verificationReason はログに記録する固定の理由を返します（値は含めない）。
func verificationReason(err error) string {
	for reason, target := range map[string]error{
		"challenge_invalid":    ErrChallengeInvalid,
		"challenge_used":       ErrChallengeUsed,
		"client_data_invalid":  ErrClientDataInvalid,
		"origin_mismatch":      ErrOriginMismatch,
		"auth_data_invalid":    ErrAuthDataInvalid,
		"rp_id_mismatch":       ErrRPIDMismatch,
		"user_not_verified":    ErrUserNotVerified,
		"synced_credential":    ErrSyncedCredential,
		"credential_id":        ErrCredentialIDInvalid,
		"public_key_invalid":   ErrPublicKeyInvalid,
		"signature_invalid":    ErrSignatureInvalid,
		"user_handle_mismatch": ErrUserHandleMismatch,
	} {
		if errors.Is(err, target) {
			return reason
		}
	}
	return "other"
}

func (f *Feature) rp() rpConfig {
	return rpConfig{ID: f.cfg.RPID, Origin: f.cfg.Origin, AllowSynced: f.cfg.AllowSynced}
}

func (f *Feature) registerRoutes(mux *web.IsolatedMux) {
	mux.HandleFunc("GET /admin", f.handleAdminHome)
	mux.HandleFunc("GET /admin/setup", f.handleSetupPage)
	mux.HandleFunc("GET /admin/login", f.handleLoginPage)
	mux.HandleFunc("POST /admin/logout", f.form(f.handleLogout))
	mux.HandleFunc("GET /admin/data-notice", f.page(f.handleDataNotice))
	mux.HandleFunc("GET /admin/static/webauthn.js", f.handleStaticJS)
	mux.HandleFunc("POST /admin/webauthn/register/begin", f.jsonEndpoint(f.handleRegisterBegin))
	mux.HandleFunc("POST /admin/webauthn/register/finish", f.jsonEndpoint(f.handleRegisterFinish))
	mux.HandleFunc("POST /admin/webauthn/login/begin", f.jsonEndpoint(f.handleLoginBegin))
	mux.HandleFunc("POST /admin/webauthn/login/finish", f.jsonEndpoint(f.handleLoginFinish))
	f.registerPasskeyRoutes(mux)
	f.registerAccountRoutes(mux)
}

func (f *Feature) writeJSON(w http.ResponseWriter, status int, v any) {
	web.WriteJSON(w, f.logger, "auth encode json failed", status, v)
}
