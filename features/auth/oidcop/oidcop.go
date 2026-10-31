// minimal-web-kit: bundled-feature oidcop

// Package oidcop は、管理者認証（features/auth）のアカウントとパスキーで、
// 自社の別システムにログインできるようにするOpenID Connectの提供機能です。
// 登録したときだけOIDCのライブラリがリンクされます。失効と保存は features/auth の本体が持ちます。
package oidcop

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
	"database/sql"
	"embed"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/zitadel/oidc/v3/pkg/op"
	"minimal-web-kit/features"
	"minimal-web-kit/features/auth"
	"minimal-web-kit/internal/federation"
	"minimal-web-kit/internal/web"
)

//go:embed views/*.html
var viewsFS embed.FS

const flowCookieName = "oidc_flow"

// Config は提供機能の設定です。Issuer はオリジン直下のHTTPSのURLに固定します（管理者認証の AUTH_ORIGIN）。
type Config struct {
	Issuer      string
	KeyFile     string // P-256の署名秘密鍵（PEM）。所有者だけが読めるファイル
	CryptoFile  string // SDKのコードの暗号化に使う32バイトの鍵。所有者だけが読めるファイル
	OldKeysFile string // 鍵の更新の間だけ公開し続ける旧公開鍵（JWKS。任意）
}

// Feature は、管理者認証を包んで置き換える機能です。管理者認証の口はそのまま使えます。
type Feature struct {
	*auth.Feature
	logger   *slog.Logger
	cfg      Config
	store    *auth.FederationStore
	sdk      *storage
	provider *op.Provider
	pages    map[string]*template.Template
	limiter  federation.Limiter
}

// NewFeature は環境変数から設定を読む機能を返します。
func NewFeature(logger *slog.Logger) *Feature {
	return &Feature{Feature: auth.NewFeature(logger), logger: logger}
}

// NewWithConfig は設定を明示した機能を返します（検証用）。
func NewWithConfig(logger *slog.Logger, cfg Config) *Feature {
	f := NewFeature(logger)
	f.cfg = cfg
	return f
}

// Init は管理者認証を初期化し、鍵を読みます。不備があれば、値を含めないエラーで起動を止めます。
func (f *Feature) Init(db *sql.DB) error {
	if f.cfg == (Config{}) {
		f.cfg = Config{
			Issuer:      os.Getenv("AUTH_ORIGIN"),
			KeyFile:     os.Getenv("OIDC_SIGNING_KEY_FILE"),
			CryptoFile:  os.Getenv("OIDC_CRYPTO_KEY_FILE"),
			OldKeysFile: os.Getenv("OIDC_OLD_JWKS_FILE"),
		}
	}
	if _, err := federation.Origin(f.cfg.Issuer); err != nil {
		return err
	}
	if err := f.Feature.Init(db); err != nil {
		return err
	}
	signing, public, err := loadKeys(f.cfg)
	if err != nil {
		return err
	}
	crypto, err := federation.SecretFile(f.cfg.CryptoFile)
	if err != nil || len(crypto) != 32 {
		return errors.New("oidcop: 32 byte crypto key file required")
	}
	f.store = auth.NewFederationStore(db)
	f.sdk = &storage{store: f.store, signing: signing, public: public}
	config := &op.Config{CryptoKey: [32]byte(crypto), CodeMethodS256: true, SupportedScopes: []string{"openid"}}
	f.provider, err = op.NewOpenIDProvider(f.cfg.Issuer, config, f.sdk,
		op.WithCustomAuthEndpoint(op.NewEndpoint("/admin/oidc/authorize")),
		op.WithCustomTokenEndpoint(op.NewEndpoint("/oidc/token")),
		op.WithCustomKeysEndpoint(op.NewEndpoint("/oidc/keys")),
		op.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))), // SDKのログに要求の値を出させない
	)
	if err != nil {
		return errors.New("oidcop: provider configuration rejected")
	}
	f.pages, err = web.ParsePages(f.Feature.Admin().Layout(), viewsFS, "flow.html", "flow_return.html", "flow_error.html")
	if err != nil {
		return err
	}
	f.Feature.SetFederationLoginNext(func(r *http.Request) string {
		if _, err := f.flow(r); err == nil {
			return "/admin/oidc/continue"
		}
		return "/admin"
	})
	return nil
}

// loadKeys は署名秘密鍵と、公開する鍵の一覧を読みます。
func loadKeys(cfg Config) (signingKey, []op.Key, error) {
	raw, err := federation.SecretFile(cfg.KeyFile)
	if err != nil {
		return signingKey{}, nil, err
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return signingKey{}, nil, errors.New("oidcop: EC signing key required")
	}
	key, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil || key.Curve != elliptic.P256() {
		return signingKey{}, nil, errors.New("oidcop: P-256 signing key required")
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return signingKey{}, nil, errors.New("oidcop: signing key rejected")
	}
	id := federation.Hash(string(der))[:32]
	public := []op.Key{publicKey{jose.JSONWebKey{Key: &key.PublicKey, KeyID: id, Algorithm: "ES256", Use: "sig"}}}
	if cfg.OldKeysFile != "" {
		old, err := os.ReadFile(cfg.OldKeysFile)
		if err != nil || len(old) > 64<<10 {
			return signingKey{}, nil, errors.New("oidcop: old public keys unreadable")
		}
		var set jose.JSONWebKeySet
		if err := json.Unmarshal(old, &set); err != nil || len(set.Keys) > 4 {
			return signingKey{}, nil, errors.New("oidcop: old public keys invalid")
		}
		for _, k := range set.Keys {
			pub, ok := k.Key.(*ecdsa.PublicKey)
			if !ok || pub.Curve != elliptic.P256() || k.KeyID == "" || k.Algorithm != "ES256" {
				return signingKey{}, nil, errors.New("oidcop: old public keys invalid")
			}
			public = append(public, publicKey{k})
		}
	}
	return signingKey{key, id}, public, nil
}

// RegisterIsolatedRoutes は、管理者認証の経路に、公開する経路だけを1つずつ足します。
// SDKの全体のハンドラ（CORSを含む）は登録しません。CORSは許可しません。
func (f *Feature) RegisterIsolatedRoutes(mux *web.IsolatedMux) {
	f.Feature.RegisterIsolatedRoutes(mux)
	mux.HandleFunc("GET /.well-known/openid-configuration", f.discovery)
	mux.HandleFunc("GET /oidc/keys", func(w http.ResponseWriter, r *http.Request) { op.Keys(w, r, f.sdk) })
	mux.HandleFunc("POST /oidc/token", f.token)
	mux.HandleFunc("POST /oidc/check", f.check)
	mux.HandleFunc("GET /admin/oidc/authorize", f.authorize)
	mux.HandleFunc("GET /admin/oidc/continue", f.Feature.Admin().AccountPage(f.continueFlow))
	mux.HandleFunc("POST /admin/oidc/approve", f.Feature.Admin().AccountForm(f.approve))
}

// PrivacyNotices は、管理者認証の説明に、連携で保存する情報を足します。
func (f *Feature) PrivacyNotices() []web.PrivacyNotice {
	notices := f.Feature.PrivacyNotices()
	notices = append(notices, web.PrivacyNotice{
		Title:    "連携先のシステムへのログインで保存する情報",
		Internal: true,
		Lead:     "管理画面のアカウントで自社の別システムにログインする人について、次の情報を保存します。氏名・メールアドレスは連携先に渡しません。",
		Rows: []web.PrivacyRow{
			{Kind: "連携用の識別子", Items: "ランダムな識別子（連携先に渡す。連携先どうしで共通）、失効の世代", Purpose: "本人の識別と、停止時のログインの無効化", Retention: "アカウントの削除から12時間2分後まで"},
			{Kind: "利用許可", Items: "連携先、許可の有無、失効の世代", Purpose: "連携先ごとの利用の可否の判定", Retention: "連携用の識別子の削除まで"},
			{Kind: "ログインの手続き", Items: "手続きのCookie（oidc_flow）のSHA-256ハッシュ、連携先、戻り先、連携先が発行した一時的な値、ログイン日時", Purpose: "連携先へのログインの完了", Retention: "5分。完了したものは、コードの再利用の検出のため12時間2分"},
		},
	})
	return notices
}

// discovery は、実装している項目だけを広告します（SDKの既定の構成は未公開の経路も含むため使わない）。
func (f *Feature) discovery(w http.ResponseWriter, r *http.Request) {
	issuer := f.cfg.Issuer
	federation.JSON(w, http.StatusOK, map[string]any{
		"issuer":                                issuer,
		"authorization_endpoint":                issuer + "/admin/oidc/authorize",
		"token_endpoint":                        issuer + "/oidc/token",
		"jwks_uri":                              issuer + "/oidc/keys",
		"scopes_supported":                      []string{"openid"},
		"response_types_supported":              []string{"code"},
		"response_modes_supported":              []string{"query"},
		"grant_types_supported":                 []string{"authorization_code"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{"ES256"},
		"token_endpoint_auth_methods_supported": []string{"client_secret_basic"},
		"code_challenge_methods_supported":      []string{"S256"},
		"claims_supported":                      []string{"iss", "sub", "aud", "exp", "iat", "auth_time", "nonce", federation.ClaimName},
	})
}

func (f *Feature) render(w http.ResponseWriter, status int, page string, data map[string]any) {
	web.NoTransform(w)
	var buf bytes.Buffer
	if err := f.pages[page].ExecuteTemplate(&buf, "layout", data); err != nil {
		f.logger.Error("oidc render failed", web.RequestIDAttr(w))
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}

// fail は固定の説明の画面を返します。値は含めません。
func (f *Feature) fail(w http.ResponseWriter, status int, message string) {
	f.render(w, status, "flow_error.html", map[string]any{"Title": "外部アプリへのログイン", "Message": message})
}

// returnTo は、連携先へ戻るページを返します。CSPの form-action 'self' はフォーム送信後の別サイトへの転送を止めるため、
// POSTの応答を303にせず、Refreshヘッダーと通常のリンクで移動します。CSPは緩めません。
func (f *Feature) returnTo(w http.ResponseWriter, location string) {
	w.Header().Set("Refresh", "0; url="+location)
	f.render(w, http.StatusOK, "flow_return.html", map[string]any{"Title": "外部アプリへ戻ります", "Next": location})
}

func flowCookie(w http.ResponseWriter, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: flowCookieName, Value: value, Path: "/admin", MaxAge: maxAge, HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode})
}

func (f *Feature) flow(r *http.Request) (auth.FederationRequest, error) {
	c, err := r.Cookie(flowCookieName)
	if err != nil {
		return auth.FederationRequest{}, auth.ErrFederationInvalid
	}
	return f.store.Flow(r.Context(), c.Value)
}

// 認可要求で受け付ける項目。未対応の prompt・max_age 等は、無視せず拒否します。
var authorizeParams = map[string]bool{
	"client_id": true, "redirect_uri": true, "response_type": true, "response_mode": true, "scope": true,
	"state": true, "nonce": true, "code_challenge": true, "code_challenge_method": true,
}

// validAuthorizeQuery は、SDKの検証の前に、S256のPKCEを全連携先に必須とします。SDKの CodeMethodS256 は広告にすぎません。
func validAuthorizeQuery(q url.Values) bool {
	for key, values := range q {
		if !authorizeParams[key] || len(values) != 1 {
			return false
		}
	}
	state, nonce, challenge := q.Get("state"), q.Get("nonce"), q.Get("code_challenge")
	if q.Get("scope") != "openid" || q.Get("response_type") != "code" || (q.Has("response_mode") && q.Get("response_mode") != "query") {
		return false
	}
	if len(state) < 16 || len(state) > 256 || len(nonce) < 16 || len(nonce) > 256 || q.Get("code_challenge_method") != "S256" {
		return false
	}
	b, err := base64.RawURLEncoding.DecodeString(challenge)
	return err == nil && len(b) == 32 && base64.RawURLEncoding.EncodeToString(b) == challenge
}

// authorize は、別サイトから届く認可要求を検査して保存し、継続ページを返します。
// ここにはシステム管理者のStrict Cookieが付かないため、ログイン状態を判定しません。
func (f *Feature) authorize(w http.ResponseWriter, r *http.Request) {
	web.NoTransform(w)
	if !f.limiter.Allow() {
		f.fail(w, http.StatusTooManyRequests, "混み合っています。少し待ってから、もう一度お試しください。")
		return
	}
	if len(r.URL.RawQuery) > 8192 || !validAuthorizeQuery(r.URL.Query()) {
		f.fail(w, http.StatusBadRequest, "ログインの要求が正しくありません。")
		return
	}
	ctx := op.ContextWithIssuer(r.Context(), f.cfg.Issuer)
	req, err := op.ParseAuthorizeRequest(r, f.provider.Decoder())
	if err != nil {
		f.fail(w, http.StatusBadRequest, "ログインの要求が正しくありません。")
		return
	}
	if _, err := op.ValidateAuthRequest(ctx, req, f.sdk, f.provider.IDTokenHintVerifier(ctx)); err != nil {
		f.fail(w, http.StatusBadRequest, "ログインの要求が正しくありません。")
		return
	}
	if c, err := r.Cookie(flowCookieName); err == nil {
		if err := f.store.RemoveFlow(ctx, c.Value); err != nil {
			f.fail(w, http.StatusServiceUnavailable, "一時的に処理できませんでした。少し待ってから、もう一度お試しください。")
			return
		}
	}
	flow := federation.Token()
	err = f.store.NewRequest(ctx, flow, auth.FederationRequest{
		ID: federation.Token(), Client: req.ClientID, Redirect: req.RedirectURI,
		State: req.State, Nonce: req.Nonce, Challenge: req.CodeChallenge,
	})
	if errors.Is(err, auth.ErrFederationBusy) {
		f.fail(w, http.StatusTooManyRequests, "混み合っています。少し待ってから、もう一度お試しください。")
		return
	} else if err != nil {
		f.fail(w, http.StatusServiceUnavailable, "一時的に処理できませんでした。少し待ってから、もう一度お試しください。")
		return
	}
	flowCookie(w, flow, int((5 * time.Minute).Seconds()))
	f.render(w, http.StatusOK, "flow.html", map[string]any{"Title": "外部アプリへのログイン", "Entry": true})
}

// continueFlow は、同じサイトの操作でStrict Cookieが付いた状態で、ログイン先の確認を出します。未ログインならパスキーの画面へ移ります。
func (f *Feature) continueFlow(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	req, err := f.flow(r)
	if err != nil {
		f.fail(w, http.StatusBadRequest, "ログインの手続きが無効か、期限切れです。ログインするアプリから、やり直してください。")
		return
	}
	client, err := f.store.LoginClient(r.Context(), req.Client)
	if err != nil {
		f.fail(w, http.StatusForbidden, "この連携先は利用できません。")
		return
	}
	data := s.PageData("外部アプリへのログイン", "")
	data["ClientLabel"] = client.Label
	f.render(w, http.StatusOK, "flow.html", data)
}

// deny は、検証済みの戻り先へ access_denied を返します。
func (f *Feature) deny(w http.ResponseWriter, r *http.Request, q auth.FederationRequest) {
	if c, err := r.Cookie(flowCookieName); err == nil {
		if err := f.store.RemoveFlow(r.Context(), c.Value); err != nil {
			f.logger.Error("oidc remove flow failed", web.RequestIDAttr(w))
		}
	}
	flowCookie(w, "", -1)
	location, err := url.Parse(q.Redirect)
	if err != nil {
		f.fail(w, http.StatusBadRequest, "ログインの要求が正しくありません。")
		return
	}
	location.RawQuery = url.Values{"error": {"access_denied"}, "state": {q.State}}.Encode()
	f.returnTo(w, location.String())
}

// approve は、本人の操作を確かめて世代を固定し、一回限りのコードを発行します。
func (f *Feature) approve(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	q, err := f.flow(r)
	if err != nil {
		f.fail(w, http.StatusBadRequest, "ログインの手続きが無効か、期限切れです。ログインするアプリから、やり直してください。")
		return
	}
	if r.PostForm.Get("decision") != "allow" {
		f.deny(w, r, q)
		return
	}
	session, err := r.Cookie("admin_session")
	if err != nil {
		f.fail(w, http.StatusForbidden, "管理画面にログインし直してください。")
		return
	}
	if err := f.store.Bind(r.Context(), q.ID, session.Value); err != nil {
		if errors.Is(err, sql.ErrNoRows) || errors.Is(err, auth.ErrFederationInvalid) {
			f.deny(w, r, q) // 許可がない・停止中
			return
		}
		f.fail(w, http.StatusServiceUnavailable, "一時的に処理できませんでした。少し待ってから、もう一度お試しください。")
		return
	}
	var location string
	err = f.store.Atomic(r.Context(), func(ctx context.Context) error {
		stored, err := f.store.Request(ctx, q.ID)
		if err != nil {
			return err
		}
		res := newBufferedResponse()
		op.AuthResponse(request{stored}, f.provider, res, r.WithContext(op.ContextWithIssuer(ctx, f.cfg.Issuer)))
		location = res.Header().Get("Location")
		// SDKはコードの発行に失敗しても、エラーの転送を返す。コードが無ければロールバックする
		if u, err := url.Parse(location); err != nil || u.Query().Get("code") == "" {
			return auth.ErrFederationInvalid
		}
		return nil
	})
	if errors.Is(err, auth.ErrFederationInvalid) {
		f.deny(w, r, q)
		return
	} else if err != nil {
		f.fail(w, http.StatusServiceUnavailable, "一時的に処理できませんでした。少し待ってから、もう一度お試しください。")
		return
	}
	flowCookie(w, "", -1)
	f.returnTo(w, location)
}

// bufferedResponse は、SDKの応答をコミットの後まで送らないために、メモリに受けます。
type bufferedResponse struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func newBufferedResponse() *bufferedResponse {
	return &bufferedResponse{header: http.Header{}, status: http.StatusOK}
}

func (b *bufferedResponse) Header() http.Header         { return b.header }
func (b *bufferedResponse) Write(p []byte) (int, error) { return b.body.Write(p) }
func (b *bufferedResponse) WriteHeader(status int)      { b.status = status }

// clientCredentials は、Basic認証を解いて資格情報を確かめます。
func (f *Feature) clientCredentials(r *http.Request) (id, secret string, err error) {
	rawID, rawSecret, ok := r.BasicAuth()
	if !ok {
		return "", "", auth.ErrFederationInvalid
	}
	if id, err = url.QueryUnescape(rawID); err != nil {
		return "", "", auth.ErrFederationInvalid
	}
	if secret, err = url.QueryUnescape(rawSecret); err != nil {
		return "", "", auth.ErrFederationInvalid
	}
	return id, secret, f.store.Authenticate(r.Context(), id, secret)
}

// token は、クライアント認証をSDKより先に行い、未認証の要求では何も変えません。
// コードの消費と応答の生成を1つのトランザクションにし、コミットの後で応答を送ります（AUTH-INV-18）。
func (f *Feature) token(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	id, secret, err := f.clientCredentials(r)
	if errors.Is(err, auth.ErrFederationInvalid) {
		federation.JSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid_client"})
		return
	} else if err != nil {
		federation.JSON(w, http.StatusServiceUnavailable, map[string]string{"error": "temporarily_unavailable"})
		return
	}
	if !federation.Form(r) || r.PostForm.Get("grant_type") != "authorization_code" {
		federation.JSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	for key := range r.PostForm {
		if key != "grant_type" && key != "code" && key != "redirect_uri" && key != "code_verifier" {
			federation.JSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	res := newBufferedResponse()
	err = f.store.Atomic(ctx, func(ctx context.Context) error {
		if err := f.store.Authenticate(ctx, id, secret); err != nil {
			return err
		}
		q, err := f.store.ConsumeCode(ctx, id, r.PostForm.Get("code"), r.PostForm.Get("redirect_uri"), r.PostForm.Get("code_verifier"))
		if err != nil {
			return err
		}
		sdkCtx := context.WithValue(op.ContextWithIssuer(ctx, f.cfg.Issuer), exchangeKey{}, request{q})
		op.CodeExchange(res, r.WithContext(sdkCtx), f.provider)
		if res.status != http.StatusOK {
			return auth.ErrFederationInvalid
		}
		return nil
	})
	if errors.Is(err, auth.ErrFederationInvalid) || errors.Is(err, auth.ErrFederationReplay) {
		federation.JSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant"})
		return
	} else if err != nil {
		federation.JSON(w, http.StatusServiceUnavailable, map[string]string{"error": "temporarily_unavailable"})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store, no-transform")
	_, _ = res.body.WriteTo(w)
}

// check は、利用側の毎要求の照会に答えます。無効の理由は外へ区別しません。
func (f *Feature) check(w http.ResponseWriter, r *http.Request) {
	id, _, err := f.clientCredentials(r)
	if errors.Is(err, auth.ErrFederationInvalid) {
		federation.JSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid_client"})
		return
	} else if err != nil {
		http.Error(w, "Service Unavailable", http.StatusServiceUnavailable)
		return
	}
	if strings.Split(r.Header.Get("Content-Type"), ";")[0] != "application/json" {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	body, err := federation.Body(r, 4<<10)
	var q federation.CheckRequest
	if err != nil || federation.Decode(body, &q) != nil || !q.Epochs.Valid() || q.Subject == "" || len(q.Subject) > 128 {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	current, err := f.store.Epochs(r.Context(), id, q.Subject)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "Service Unavailable", http.StatusServiceUnavailable)
		return
	}
	if err != nil || current != q.Epochs {
		federation.JSON(w, http.StatusOK, federation.CheckResponse{Active: false})
		return
	}
	federation.JSON(w, http.StatusOK, federation.CheckResponse{Active: true, Issuer: f.cfg.Issuer, Subject: q.Subject, ClientID: id, Auth: &current})
}
