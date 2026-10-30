// minimal-web-kit: bundled-feature federatedauth

// Package federatedauth は、別のシステムの認証基盤（features/auth/oidcop）でログインする管理者認証です。
// features.Admin を提供するため、お知らせ等の管理画面はそのまま使えます。アカウントとパスキーは持ちません。
// 保護された要求のたびに認証基盤へ有効性を確かめ、確認できない間は操作を拒否します。
package federatedauth

import (
	"bytes"
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"minimal-web-kit/features"
	"minimal-web-kit/internal/federation"
	"minimal-web-kit/internal/web"
	"minimal-web-kit/internal/web/guard"
)

//go:embed migration.sql
var migrationSQL string

//go:embed views/*.html
var viewsFS embed.FS

const (
	sessionCookieName = "federated_session"
	attemptCookieName = "sso_attempt"
	exchangeTimeout   = 5 * time.Second
)

// Config は利用側の設定です。
type Config struct {
	Origin     string // このシステムのオリジン（AUTH_ORIGIN）
	Issuer     string // 認証基盤のオリジン（OIDC_ISSUER）
	ClientID   string // 認証基盤で登録した連携先のID（OIDC_CLIENT_ID）
	SecretFile string // クライアント秘密値のファイル（OIDC_CLIENT_SECRET_FILE）。所有者だけが読める
	// Transport は通信の下層です。検証で自己署名の証明書を信頼するために使います。既定は http.DefaultTransport です。
	Transport http.RoundTripper
}

func configFromEnv() Config {
	return Config{
		Origin:     os.Getenv("AUTH_ORIGIN"),
		Issuer:     os.Getenv("OIDC_ISSUER"),
		ClientID:   os.Getenv("OIDC_CLIENT_ID"),
		SecretFile: os.Getenv("OIDC_CLIENT_SECRET_FILE"),
	}
}

// Feature は同梱機能の外部認証です。管理者認証（features/auth）と同時には登録できません（BuildRoutes が拒否する）。
type Feature struct {
	logger   *slog.Logger
	cfg      Config
	store    *sessionStore
	upstream *upstream
	attempts *attempts
	csrf     *guard.CSRF
	pages    map[string]*template.Template
	navLinks []web.NavLink
	now      func() time.Time
}

// NewFeature は環境変数から設定を読む機能を返します。
func NewFeature(logger *slog.Logger) *Feature {
	return &Feature{logger: logger, csrf: guard.NewCSRF(), now: time.Now}
}

// NewWithConfig は設定を明示した機能を返します（検証用）。
func NewWithConfig(logger *slog.Logger, cfg Config) *Feature {
	f := NewFeature(logger)
	f.cfg = cfg
	return f
}

// Name は機能の名前を返します。
func (f *Feature) Name() string { return "federatedauth" }

// Init は設定を検証し、マイグレーションを適用します。不備があれば、値を含めないエラーで起動を止めます。
func (f *Feature) Init(db *sql.DB) error {
	if f.cfg.Issuer == "" {
		f.cfg = configFromEnv()
	}
	if _, err := federation.Origin(f.cfg.Origin); err != nil {
		return fmt.Errorf("AUTH_ORIGIN: %w", err)
	}
	if _, err := federation.Origin(f.cfg.Issuer); err != nil {
		return fmt.Errorf("OIDC_ISSUER: %w", err)
	}
	if f.cfg.ClientID == "" {
		return errors.New("OIDC_CLIENT_ID is required")
	}
	secret, err := federation.SecretFile(f.cfg.SecretFile)
	if err != nil {
		return fmt.Errorf("OIDC_CLIENT_SECRET_FILE: %w", err)
	}
	value := strings.TrimSpace(string(secret))
	if len(value) < 32 {
		return errors.New("OIDC_CLIENT_SECRET_FILE: secret too short")
	}
	if _, err := db.Exec(migrationSQL); err != nil {
		return fmt.Errorf("federatedauth migration: %w", err)
	}
	f.store = &sessionStore{db: db, issuer: f.cfg.Issuer, now: f.now}
	f.upstream = newUpstream(f.cfg, value, f.now)
	f.attempts = newAttempts(f.now)
	f.pages, err = web.ParsePages(f.Admin().Layout(), viewsFS, "home.html", "login.html", "redirect.html", "unavailable.html")
	return err
}

// RegisterRoutes はCoreの経路に何も登録しません（経路はすべて独立経路）。
func (f *Feature) RegisterRoutes(mux *http.ServeMux, sm *web.SessionManager) {}

// RegisterIsolatedRoutes は、ログインの経路を登録します。
func (f *Feature) RegisterIsolatedRoutes(mux *web.IsolatedMux) {
	mux.HandleFunc("GET /admin", f.page(f.handleHome))
	mux.HandleFunc("GET /admin/login", f.handleLogin)
	mux.HandleFunc("POST /admin/sso/start", f.handleStart)
	mux.HandleFunc("GET /admin/sso/callback", f.handleCallback)
	mux.HandleFunc("POST /admin/logout", f.handleLogout)
}

// SetAdminNavLinks は、他の機能の管理画面へのリンクを、管理画面のナビに加えます。
func (f *Feature) SetAdminNavLinks(links []web.NavLink) { f.navLinks = links }

// Purge は期限切れのセッションを削除します。
func (f *Feature) Purge(ctx context.Context) error {
	if f.store == nil {
		return nil
	}
	return f.store.purge(ctx)
}

// PrivacyNotices は、保存する情報をプライバシーポリシーに載せます。内容は docs/data-inventory.md の9節と揃えます。
func (f *Feature) PrivacyNotices() []web.PrivacyNotice {
	return []web.PrivacyNotice{{
		Title: "管理画面の利用者について保存する情報",
		Lead:  "管理画面のログインは、別のシステムの認証基盤で行います。このシステムには氏名・メールアドレス・パスキーを保存しません。",
		Rows: []web.PrivacyRow{
			{Kind: "利用者", Items: "認証基盤のURL、認証基盤が発行したランダムな識別子、ロール、作成・停止の日時", Purpose: "利用者の識別と権限の判定", Retention: "運用者が削除するまで"},
			{Kind: "ログイン状態", Items: "Cookie（federated_session）のランダムなトークンと、そのSHA-256ハッシュ、失効の世代、本人確認・発行・最終操作の日時", Purpose: "ログイン状態の維持と、認証基盤での停止の反映", Retention: "無操作2時間または本人確認から12時間。ログアウト時等にも削除"},
			{Kind: "ログインの手続き", Items: "Cookie（sso_attempt）のランダムなトークン、一時的な検証用の値、戻り先（サーバーのメモリのみ）", Purpose: "ログインの要求の偽造の防止", Retention: "5分。再起動でも消える"},
		},
	}}
}

// session は、ログイン中の利用者の要求に付く情報です（features.AdminSession）。
type session struct {
	f     *Feature
	token string
	data  accountSession
	path  string
}

func (s session) AccountID() int64    { return s.data.AccountID }
func (s session) Role() features.Role { return features.Role(s.data.Role) }
func (s session) IsAdmin() bool       { return s.Role() == features.RoleAdmin }
func (s session) CSRFToken() string   { return s.f.csrf.Token(s.token) }

// PageData は、管理画面のレイアウトに渡す値を返します。
func (s session) PageData(title, current string) map[string]any {
	var nav []web.NavLink
	for _, l := range s.f.navLinks {
		if !l.AdminOnly || s.IsAdmin() {
			nav = append(nav, l)
		}
	}
	return map[string]any{
		"Title":       title,
		"Current":     current,
		"RequestPath": s.path,
		"AdminNav":    nav,
		"LoggedIn":    true,
		"IsAdmin":     s.IsAdmin(),
		"CSRFToken":   s.CSRFToken(),
		"Now":         s.f.now(),
	}
}

func (f *Feature) render(w http.ResponseWriter, status int, page string, data map[string]any) {
	web.NoTransform(w)
	var buf bytes.Buffer
	if err := f.pages[page].ExecuteTemplate(&buf, "layout", data); err != nil {
		f.logger.Error("federated render failed", web.RequestIDAttr(w))
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}

func (f *Feature) unavailable(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "5")
	f.render(w, http.StatusServiceUnavailable, "unavailable.html", map[string]any{"Title": "一時的に利用できません"})
}

// setCookie はCookieを設定します。life が0ならCookieを消します。
func setCookie(w http.ResponseWriter, name, value, path string, life time.Duration) {
	maxAge := int(life / time.Second)
	if life == 0 {
		maxAge = -1
	}
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: path, MaxAge: maxAge, HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
}

// redirectLogin はログインの案内へ移します。GETなら開いていたページへ戻れるようにします。POSTの内容は再送しません。
func redirectLogin(w http.ResponseWriter, r *http.Request) {
	target := "/admin/login"
	if next := r.URL.RequestURI(); r.Method == http.MethodGet && validNext(next) {
		target += "?" + url.Values{"next": {next}}.Encode()
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// authorize は、このシステムのセッションと、認証基盤での有効性を確かめます。
// 正しい形式の無効の応答だけがセッションを消します。確認できなければ503で、セッションは残します（FEDAUTH-INV-1, FEDAUTH-INV-2）。
func (f *Feature) authorize(w http.ResponseWriter, r *http.Request, token string, s accountSession) bool {
	switch f.upstream.check(r.Context(), s.Subject, s.Epochs) {
	case checkActive:
		if err := f.store.touch(r.Context(), token); err != nil {
			f.logger.Error("federated touch failed", web.RequestIDAttr(w))
		}
		return true
	case checkInactive:
		if err := f.store.deleteSession(r.Context(), token); err != nil {
			f.logger.Error("federated delete session failed", web.RequestIDAttr(w))
		}
		setCookie(w, sessionCookieName, "", "/admin", 0)
		f.logger.Info("federated session revoked", web.RequestIDAttr(w), slog.Int64("account_id", s.AccountID))
		redirectLogin(w, r)
		return false
	}
	f.logger.Warn("federated check unavailable", web.RequestIDAttr(w))
	f.unavailable(w)
	return false
}

// current はCookieの有効なセッションを返します。
func (f *Feature) current(r *http.Request) (string, accountSession, error) {
	c, err := r.Cookie(sessionCookieName)
	if err != nil || c.Value == "" {
		return "", accountSession{}, errNoSession
	}
	s, err := f.store.session(r.Context(), c.Value)
	return c.Value, s, err
}

func (f *Feature) page(next func(w http.ResponseWriter, r *http.Request, s features.AdminSession)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		web.NoTransform(w)
		token, s, err := f.current(r)
		if errors.Is(err, errNoSession) {
			redirectLogin(w, r)
			return
		} else if err != nil {
			f.unavailable(w)
			return
		}
		if f.authorize(w, r, token, s) {
			next(w, r, session{f: f, token: token, data: s, path: r.URL.Path})
		}
	}
}

// localForm は、同一オリジン・セッション・CSRFを確かめます。認証基盤には問い合わせません。
func (f *Feature) localForm(w http.ResponseWriter, r *http.Request) (string, accountSession, bool) {
	web.NoTransform(w)
	if !guard.SameOrigin(r, f.cfg.Origin) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return "", accountSession{}, false
	}
	token, s, err := f.current(r)
	if errors.Is(err, errNoSession) {
		redirectLogin(w, r)
		return "", accountSession{}, false
	} else if err != nil {
		f.unavailable(w)
		return "", accountSession{}, false
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return "", accountSession{}, false
	}
	if !f.csrf.Valid(token, r.PostForm.Get("csrf_token")) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return "", accountSession{}, false
	}
	return token, s, true
}

func (f *Feature) form(next func(w http.ResponseWriter, r *http.Request, s features.AdminSession)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, s, ok := f.localForm(w, r)
		if ok && f.authorize(w, r, token, s) {
			next(w, r, session{f: f, token: token, data: s, path: r.URL.Path})
		}
	}
}

func adminOnly(next func(w http.ResponseWriter, r *http.Request, s features.AdminSession)) func(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	return func(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
		if !s.IsAdmin() {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		next(w, r, s)
	}
}

// adminAPI は、他の機能に渡す features.Admin の実装です。
type adminAPI struct{ f *Feature }

func (a adminAPI) Page(next func(w http.ResponseWriter, r *http.Request, s features.AdminSession)) http.HandlerFunc {
	return a.f.page(next)
}

func (a adminAPI) Form(next func(w http.ResponseWriter, r *http.Request, s features.AdminSession)) http.HandlerFunc {
	return a.f.form(next)
}

func (a adminAPI) AdminPage(next func(w http.ResponseWriter, r *http.Request, s features.AdminSession)) http.HandlerFunc {
	return a.f.page(adminOnly(next))
}

func (a adminAPI) AdminForm(next func(w http.ResponseWriter, r *http.Request, s features.AdminSession)) http.HandlerFunc {
	return a.f.form(adminOnly(next))
}

func (a adminAPI) Layout() web.Layout {
	return web.Layout{FS: viewsFS, File: "views/admin_layout.html"}
}

// Admin は、管理画面を持つ他の機能に、ログインの確認とCSRFの守りを提供します。
func (f *Feature) Admin() features.Admin { return adminAPI{f} }

// handleHome は、最初の管理画面へ移します。他に管理画面が無ければ、ログイン中であることを表示します。
func (f *Feature) handleHome(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	if nav, _ := s.PageData("", "")["AdminNav"].([]web.NavLink); len(nav) > 0 {
		http.Redirect(w, r, nav[0].Href, http.StatusSeeOther)
		return
	}
	f.render(w, http.StatusOK, "home.html", s.PageData("管理画面", ""))
}

func (f *Feature) handleLogin(w http.ResponseWriter, r *http.Request) {
	web.NoTransform(w)
	if _, _, err := f.current(r); err == nil {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	next := r.URL.Query().Get("next")
	if !validNext(next) {
		next = "/admin"
	}
	f.renderLogin(w, http.StatusOK, next, "")
}

func (f *Feature) renderLogin(w http.ResponseWriter, status int, next, message string) {
	f.render(w, status, "login.html", map[string]any{"Title": "管理画面へのログイン", "Next": next, "Message": message})
}

// goTo は、認証基盤へ進むページを返します。CSPの form-action 'self' はフォーム送信後の別サイトへの転送を止めるため、
// POSTの応答を303にせず、Refreshヘッダーと通常のリンクで移動します。CSPは緩めません。
func (f *Feature) goTo(w http.ResponseWriter, location string) {
	w.Header().Set("Refresh", "0; url="+location)
	f.render(w, http.StatusOK, "redirect.html", map[string]any{"Title": "認証の画面へ移動します", "Next": location})
}

// handleStart は、ログインの試行を作ります。未ログインなのでCSRFトークンは無く、同一オリジンの検査で守ります。
// 既にログイン中ならセッションを置き換えません（別人のログインへの切り替えを防ぐ）。
func (f *Feature) handleStart(w http.ResponseWriter, r *http.Request) {
	web.NoTransform(w)
	if !guard.SameOrigin(r, f.cfg.Origin) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	if _, _, err := f.current(r); err == nil {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	next := r.PostForm.Get("next")
	if !validNext(next) {
		next = "/admin"
	}
	cookie := federation.Token()
	if c, err := r.Cookie(attemptCookieName); err == nil {
		if a, ok := f.attempts.recent(c.Value); ok {
			f.goTo(w, a.authURL)
			return
		}
		cookie = c.Value
	}
	state, nonce, verifier := federation.Token(), federation.Token(), federation.Token()
	location := f.upstream.authCodeURL(state, nonce, verifier)
	a := &attempt{stateHash: federation.Hash(state), nonceHash: federation.Hash(nonce), verifier: verifier, next: next, authURL: location}
	if err := f.attempts.add(cookie, a); err != nil {
		f.renderLogin(w, http.StatusTooManyRequests, next, "混み合っています。少し待ってから、もう一度お試しください。")
		return
	}
	setCookie(w, attemptCookieName, cookie, "/admin/sso", attemptTTL)
	f.goTo(w, location)
}

// handleCallback は、stateと試行のCookieを照合して試行を一回限りで取り出し、コード交換・IDトークンの検証・初回の照会の後でセッションを作ります。
func (f *Feature) handleCallback(w http.ResponseWriter, r *http.Request) {
	web.NoTransform(w)
	setCookie(w, attemptCookieName, "", "/admin/sso", 0)
	q := r.URL.Query()
	c, err := r.Cookie(attemptCookieName)
	// ログイン中はセッションを置き換えない（植えられたCookieで別人に切り替えさせない）
	if _, _, sessionErr := f.current(r); sessionErr == nil {
		if err == nil {
			f.attempts.discard(c.Value)
		}
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	if err != nil || len(q["state"]) != 1 || len(q["code"]) > 1 || len(q["error"]) > 1 {
		f.renderLogin(w, http.StatusBadRequest, "/admin", "ログインの手続きが無効か、期限切れです。もう一度ログインしてください。")
		return
	}
	a, ok := f.attempts.take(c.Value, q.Get("state"))
	if !ok {
		f.renderLogin(w, http.StatusBadRequest, "/admin", "ログインの手続きが無効か、期限切れです。もう一度ログインしてください。")
		return
	}
	if q.Get("code") == "" {
		f.renderLogin(w, http.StatusForbidden, a.next, "ログインは中止されたか、許可されませんでした。")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), exchangeTimeout)
	defer cancel()
	id, err := f.upstream.exchange(ctx, q.Get("code"), a.verifier, a.nonceHash)
	if err != nil {
		f.logger.Warn("federated login rejected", web.RequestIDAttr(w))
		f.renderLogin(w, http.StatusBadGateway, a.next, "認証の結果を確認できませんでした。もう一度ログインしてください。")
		return
	}
	switch f.upstream.check(ctx, id.Subject, id.Epochs) {
	case checkInactive:
		f.renderLogin(w, http.StatusForbidden, a.next, "ログインは許可されませんでした。")
		return
	case checkUnavailable:
		f.unavailable(w)
		return
	}
	token, err := f.store.login(ctx, id.Subject, id.Epochs, id.AuthTime)
	if errors.Is(err, errDisabled) || errors.Is(err, errNoSession) {
		f.renderLogin(w, http.StatusForbidden, a.next, "ログインは許可されませんでした。")
		return
	} else if err != nil {
		f.unavailable(w)
		return
	}
	setCookie(w, sessionCookieName, token, "/admin", sessionMax)
	f.logger.Info("federated login succeeded", web.RequestIDAttr(w))
	http.Redirect(w, r, a.next, http.StatusSeeOther)
}

// handleLogout は、このシステムのセッションだけを消します。認証基盤の障害中でもログアウトできます。
func (f *Feature) handleLogout(w http.ResponseWriter, r *http.Request) {
	token, _, ok := f.localForm(w, r)
	if !ok {
		return
	}
	if err := f.store.deleteSession(r.Context(), token); err != nil {
		f.logger.Error("federated logout failed", web.RequestIDAttr(w))
	}
	setCookie(w, sessionCookieName, "", "/admin", 0)
	http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
}
