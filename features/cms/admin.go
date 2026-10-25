package cms

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"html/template"
	"log/slog"
	"mime"
	"net/http"
	"time"

	"minimal-web-kit/internal/web"
)

//go:embed static/webauthn.js
var staticFS embed.FS

const (
	adminCookieName = "cms_admin"
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
	"setup_token_invalid":   "登録用のURLが無効か、期限切れです。サーバーで make cms-setup（パスキーを失った場合は make cms-recover）を実行し、表示されたURLを開き直してください。",
	"synced_credential":     "同期型のパスキー（スマートフォン、Googleパスワードマネージャー、iCloudキーチェーンなど）は登録できません。Windows Helloやセキュリティキーなど、この端末だけに保存される認証器を使ってください。",
	"user_not_verified":     "指紋・顔・PINによる本人確認が必要です。認証器の設定を確認してください。",
	"public_key_invalid":    "この認証器の鍵の種類には対応していません。別の認証器を使ってください。",
	"credential_exists":     "この認証器は登録済みです。",
	"label_invalid":         "端末名は1〜50文字で入力してください。",
	"registration_rejected": "登録できませんでした。ページを開き直して、もう一度お試しください。",
	"login_rejected":        "このパスキーではログインできません。別のパスキーでログインするか、サーバーで make cms-recover を実行してください。",
	"internal_error":        "処理に失敗しました。もう一度お試しください。",
}

func parseAdminTemplates() map[string]*template.Template {
	pages := map[string]*template.Template{}
	for _, page := range []string{
		"admin_setup.html", "admin_login.html",
		"admin_posts.html", "admin_post_form.html", "admin_post_delete.html",
		"admin_passkeys.html", "admin_passkey_delete.html",
	} {
		pages[page] = template.Must(template.ParseFS(viewsFS, "views/admin_layout.html", "views/"+page))
	}
	return pages
}

func (f *Feature) requestID(w http.ResponseWriter) slog.Attr {
	return slog.String("request_id", w.Header().Get("X-Request-ID"))
}

func (f *Feature) renderAdmin(w http.ResponseWriter, page string, data map[string]any, withScript bool) {
	f.renderAdminStatus(w, http.StatusOK, page, data, withScript)
}

func (f *Feature) renderAdminStatus(w http.ResponseWriter, status int, page string, data map[string]any, withScript bool) {
	var buf bytes.Buffer
	if err := f.adminPages[page].ExecuteTemplate(&buf, "layout", data); err != nil {
		f.logger.Error("cms render admin failed", f.requestID(w))
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

// --- 同一オリジン・CSRF・セッション ---

// sameOrigin は、管理画面へのPOSTが同じオリジンから送られたことを確認します。
// Coreが Referrer-Policy: no-referrer を送るため、同一オリジンでも Origin は null になり得ます。
// そのため Sec-Fetch-Site を必須とし、Origin が null 以外で届いた場合は一致も確認します。
func (f *Feature) sameOrigin(r *http.Request) bool {
	if r.Header.Get("Sec-Fetch-Site") != "same-origin" {
		return false
	}
	origin := r.Header.Get("Origin")
	return origin == "" || origin == "null" || origin == f.cfg.Origin
}

func (f *Feature) csrfToken(sessionToken string) string {
	m := hmac.New(sha256.New, f.csrfKey)
	m.Write([]byte(sessionToken))
	return hex.EncodeToString(m.Sum(nil))
}

func (f *Feature) validCSRF(sessionToken, token string) bool {
	return sessionToken != "" && token != "" && hmac.Equal([]byte(f.csrfToken(sessionToken)), []byte(token))
}

// adminSession は有効な管理者セッションのトークンを返します。無ければ空文字を返します。
func (f *Feature) adminSession(r *http.Request) string {
	c, err := r.Cookie(adminCookieName)
	if err != nil || c.Value == "" {
		return ""
	}
	if err := f.admin.validateSession(r.Context(), c.Value); err != nil {
		return ""
	}
	return c.Value
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

// adminPage は、ログイン中の管理画面に共通のデータを用意します。
// 未ログインならログイン画面へリダイレクトし、ok=false を返します。
func (f *Feature) adminPage(w http.ResponseWriter, r *http.Request, title, nav string) (data map[string]any, session string, ok bool) {
	session = f.adminSession(r)
	if session == "" {
		http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
		return nil, "", false
	}
	disabled, err := f.admin.countDisabledCredentials(r.Context())
	if err != nil {
		f.logger.Error("cms count disabled passkeys failed", f.requestID(w))
	}
	return map[string]any{
		"Title":            title,
		"Nav":              nav,
		"LoggedIn":         true,
		"CSRFToken":        f.csrfToken(session),
		"DisabledPasskeys": disabled > 0,
		"Now":              f.now(),
	}, session, true
}

// adminForm は、管理画面のフォーム送信（POST）に、同一オリジン・管理者セッション・CSRFトークンの確認を掛けます。
func (f *Feature) adminForm(next func(w http.ResponseWriter, r *http.Request, session string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !f.sameOrigin(r) {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		session := f.adminSession(r)
		if session == "" {
			// 期限切れ等。送信内容は保存せず、ログインし直してもらう
			http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Bad Request", http.StatusBadRequest)
			return
		}
		if !f.validCSRF(session, r.PostForm.Get("csrf_token")) {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		next(w, r, session)
	}
}

func (f *Feature) handleAdminHome(w http.ResponseWriter, r *http.Request) {
	if f.adminSession(r) == "" {
		http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin/posts", http.StatusSeeOther)
}

func (f *Feature) handleSetupPage(w http.ResponseWriter, r *http.Request) {
	f.renderAdmin(w, "admin_setup.html", map[string]any{"Title": "パスキーの登録"}, true)
}

func (f *Feature) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	if f.adminSession(r) != "" {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	f.renderAdmin(w, "admin_login.html", map[string]any{
		"Title":      "管理画面へのログイン",
		"Registered": r.URL.Query().Get("registered") == "1",
	}, true)
}

func (f *Feature) handleLogout(w http.ResponseWriter, r *http.Request, session string) {
	if err := f.admin.deleteSession(r.Context(), session); err != nil {
		f.logger.Error("cms logout failed", f.requestID(w))
	}
	f.clearAdminCookie(w)
	f.logger.Info("cms logout", f.requestID(w))
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

// registrationAuth は登録の認可を確認します。登録トークン、またはログイン中の管理者セッション＋CSRFトークンが必要です。
func (f *Feature) registrationAuth(r *http.Request, setupToken string) (session string, ok bool) {
	if setupToken != "" {
		return "", f.admin.checkSetupToken(r.Context(), setupToken) == nil
	}
	session = f.adminSession(r)
	return session, session != "" && f.validCSRF(session, r.Header.Get("X-CSRF-Token"))
}

func (f *Feature) handleRegisterBegin(w http.ResponseWriter, r *http.Request, body []byte) {
	var req struct {
		Token string `json:"token"`
	}
	if json.Unmarshal(body, &req) != nil {
		f.writeError(w, http.StatusBadRequest, "bad_request")
		return
	}
	if _, ok := f.registrationAuth(r, req.Token); !ok {
		code := "forbidden"
		if req.Token != "" {
			code = "setup_token_invalid"
		}
		f.writeError(w, http.StatusForbidden, code)
		return
	}
	userID, err := f.admin.webAuthnUserID(r.Context())
	if err != nil {
		f.logger.Error("cms load webauthn user id failed", f.requestID(w))
		f.writeError(w, http.StatusInternalServerError, "internal_error")
		return
	}
	challenge, err := f.challenges.issue(purposeRegister)
	if err != nil {
		f.writeError(w, http.StatusInternalServerError, "internal_error")
		return
	}
	exclude := []map[string]string{}
	rows, err := f.db.QueryContext(r.Context(), "SELECT id FROM cms_credentials")
	if err != nil {
		f.writeError(w, http.StatusInternalServerError, "internal_error")
		return
	}
	defer rows.Close()
	for rows.Next() {
		var id []byte
		if rows.Scan(&id) == nil {
			exclude = append(exclude, map[string]string{"type": "public-key", "id": b64(id)})
		}
	}
	f.writeJSON(w, http.StatusOK, map[string]any{
		"challenge": challenge,
		"rp":        map[string]string{"id": f.cfg.RPID, "name": "minimal-web-kit CMS"},
		"user":      map[string]string{"id": b64(userID), "name": "admin", "displayName": "管理者"},
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
	session, ok := f.registrationAuth(r, req.Token)
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
		if session != "" {
			err = f.admin.addCredential(r.Context(), cred, req.Label)
		} else {
			err = f.admin.registerWithSetupToken(r.Context(), req.Token, cred, req.Label)
		}
	}
	if err != nil {
		code := registrationErrorCode(err)
		f.logger.Warn("cms passkey registration rejected", f.requestID(w), slog.String("reason", code))
		f.writeError(w, http.StatusBadRequest, code)
		return
	}
	f.logger.Info("cms passkey registered", f.requestID(w))
	next := "/admin/login?registered=1"
	if session != "" {
		next = "/admin/passkeys?notice=added"
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
		f.logger.Warn("cms login rejected", f.requestID(w), slog.String("reason", reason))
		f.writeError(w, http.StatusUnauthorized, "login_rejected")
	}
	cred, err := f.admin.getCredential(r.Context(), req.RawID)
	if err != nil {
		reject("credential_unavailable")
		return
	}
	userID, err := f.admin.webAuthnUserID(r.Context())
	if err != nil {
		f.writeError(w, http.StatusInternalServerError, "internal_error")
		return
	}
	count, err := verifyAssertion(f.rp(), f.challenges, assertionResponse{
		RawID:             req.RawID,
		ClientDataJSON:    req.ClientDataJSON,
		AuthenticatorData: req.AuthenticatorData,
		Signature:         req.Signature,
		UserHandle:        req.UserHandle,
	}, cred, userID)
	if errors.Is(err, ErrCloneSuspected) {
		if derr := f.admin.disableCredential(r.Context(), cred.ID); derr != nil {
			f.logger.Error("cms disable passkey failed", f.requestID(w))
		}
		f.logger.Warn("cms passkey disabled", f.requestID(w))
		reject("clone_suspected")
		return
	}
	if err != nil {
		reject(verificationReason(err))
		return
	}
	if err := f.admin.recordLogin(r.Context(), cred.ID, count); err != nil {
		reject("sign_count_conflict")
		return
	}
	token, err := f.admin.createSession(r.Context())
	if err != nil {
		f.writeError(w, http.StatusInternalServerError, "internal_error")
		return
	}
	f.setAdminCookie(w, token)
	f.logger.Info("cms login succeeded", f.requestID(w))
	f.writeJSON(w, http.StatusOK, map[string]string{"next": "/admin"})
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
	return rpConfig{ID: f.cfg.RPID, Origin: f.cfg.Origin}
}

func newCSRFKey() []byte {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		panic("cms: generate csrf key: " + err.Error())
	}
	return key
}

func (f *Feature) registerAdminRoutes(mux *web.IsolatedMux) {
	mux.HandleFunc("GET /admin", f.handleAdminHome)
	mux.HandleFunc("GET /admin/setup", f.handleSetupPage)
	mux.HandleFunc("GET /admin/login", f.handleLoginPage)
	mux.HandleFunc("POST /admin/logout", f.adminForm(f.handleLogout))
	mux.HandleFunc("GET /admin/static/webauthn.js", f.handleStaticJS)
	mux.HandleFunc("POST /admin/webauthn/register/begin", f.jsonEndpoint(f.handleRegisterBegin))
	mux.HandleFunc("POST /admin/webauthn/register/finish", f.jsonEndpoint(f.handleRegisterFinish))
	mux.HandleFunc("POST /admin/webauthn/login/begin", f.jsonEndpoint(f.handleLoginBegin))
	mux.HandleFunc("POST /admin/webauthn/login/finish", f.jsonEndpoint(f.handleLoginFinish))
	f.registerPostAdminRoutes(mux)
	f.registerPasskeyAdminRoutes(mux)
}
