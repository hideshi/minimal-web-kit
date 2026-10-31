package auth

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"minimal-web-kit/features"
	"minimal-web-kit/internal/federation"
	"minimal-web-kit/internal/web"
)

const agentFlowCookie = "agent_flow"

// agentAuth は、エージェントのOAuthの経路です。features/mcp のような利用側が、AgentAuth を通して有効にします。
type agentAuth struct {
	f       *Feature
	cfg     features.AgentAuthConfig
	limiter federation.Limiter
}

// AgentAuth は、エージェントの認証の口を返します。使う機能を登録したときだけ経路が増えます（features.AgentAuthProvider）。
// 管理者認証の本体で動くので、OIDCのライブラリは要りません。httpsの AUTH_ORIGIN か、手元の http://localhost に限ります。
func (f *Feature) AgentAuth(cfg features.AgentAuthConfig) (features.AgentAuth, error) {
	if _, err := federation.Origin(f.cfg.Origin); err != nil && !strings.HasPrefix(f.cfg.Origin, "http://localhost") {
		return nil, errors.New("auth: agent auth needs an https AUTH_ORIGIN")
	}
	if !strings.HasPrefix(cfg.Path, "/") || strings.ContainsAny(cfg.Path, "?#") || len(cfg.Scopes) == 0 {
		return nil, errors.New("auth: invalid agent resource")
	}
	a := &agentAuth{f: f, cfg: cfg}
	f.agentEnabled = true
	previous := f.loginNext
	f.loginNext = func(r *http.Request) string {
		if _, err := a.flow(r); err == nil {
			return "/admin/agent/continue"
		}
		if previous != nil {
			return previous(r)
		}
		return "/admin"
	}
	return a, nil
}

// Resource は features.AgentAuth の実装です。
func (a *agentAuth) Resource() string { return a.f.cfg.Origin + a.cfg.Path }

// Origin は features.AgentAuth の実装です。
func (a *agentAuth) Origin() string { return a.f.cfg.Origin }

func (a *agentAuth) store() *FederationStore { return NewFederationStore(a.f.db) }

// VerifyBearer は features.AgentAuth の実装です。
func (a *agentAuth) VerifyBearer(ctx context.Context, token, resource string) (features.AgentPrincipal, error) {
	return a.store().VerifyAgentToken(ctx, token, resource)
}

// AuthorizationServers は、保護リソースの情報に載せる認可サーバーを返します。
func (a *agentAuth) AuthorizationServers() []string { return []string{a.f.cfg.Origin} }

// RegisterAgentRoutes は、認可サーバーの経路を足します。CORSは許可しません。
func (a *agentAuth) RegisterAgentRoutes(mux *web.IsolatedMux) {
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", a.metadata)
	mux.HandleFunc("POST /agent/token", a.token)
	mux.HandleFunc("GET /admin/agent/authorize", a.authorize)
	mux.HandleFunc("GET /admin/agent/continue", a.f.Admin().Page(a.continueFlow))
	mux.HandleFunc("POST /admin/agent/approve", a.f.Admin().Form(a.approve))
}

func (a *agentAuth) scopeNames() []string {
	names := make([]string, 0, len(a.cfg.Scopes))
	for _, s := range a.cfg.Scopes {
		names = append(names, s.Name)
	}
	return names
}

// metadata は、実装している項目だけを広告します。
func (a *agentAuth) metadata(w http.ResponseWriter, r *http.Request) {
	issuer := a.f.cfg.Origin
	federation.JSON(w, http.StatusOK, map[string]any{
		"issuer":                                         issuer,
		"authorization_endpoint":                         issuer + "/admin/agent/authorize",
		"token_endpoint":                                 issuer + "/agent/token",
		"response_types_supported":                       []string{"code"},
		"grant_types_supported":                          []string{"authorization_code"},
		"code_challenge_methods_supported":               []string{"S256"},
		"token_endpoint_auth_methods_supported":          []string{"none"},
		"scopes_supported":                               a.scopeNames(),
		"authorization_response_iss_parameter_supported": true,
	})
}

// 認可要求で受け付ける項目。未対応の項目は無視せず拒否します。
var agentAuthorizeParams = map[string]bool{
	"client_id": true, "redirect_uri": true, "response_type": true, "scope": true,
	"state": true, "code_challenge": true, "code_challenge_method": true, "resource": true,
}

// normalizeScope は、要求のスコープを確かめ、定義の順にそろえて返します。無効なら空です。
func (a *agentAuth) normalizeScope(raw string) string {
	asked := map[string]bool{}
	fields := strings.Fields(raw)
	for _, s := range fields {
		asked[s] = true
	}
	var kept []string
	for _, name := range a.scopeNames() {
		if asked[name] {
			kept = append(kept, name)
			delete(asked, name)
		}
	}
	if len(asked) > 0 || len(kept) == 0 || len(fields) != len(kept) {
		return ""
	}
	return strings.Join(kept, " ")
}

// validQuery は、値に立ち入らずに形だけを確かめます。
func (a *agentAuth) validQuery(q url.Values) bool {
	for key, values := range q {
		if !agentAuthorizeParams[key] || len(values) != 1 {
			return false
		}
	}
	state, challenge := q.Get("state"), q.Get("code_challenge")
	if q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" || len(state) < 8 || len(state) > 512 ||
		q.Get("resource") != a.Resource() {
		return false
	}
	b, err := base64.RawURLEncoding.DecodeString(challenge)
	return err == nil && len(b) == 32 && base64.RawURLEncoding.EncodeToString(b) == challenge
}

func (a *agentAuth) setFlowCookie(w http.ResponseWriter, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: agentFlowCookie, Value: value, Path: "/admin", MaxAge: maxAge, HttpOnly: true, Secure: a.f.cfg.SecureCookie, SameSite: http.SameSiteStrictMode})
}

func (a *agentAuth) flow(r *http.Request) (AgentRequest, error) {
	c, err := r.Cookie(agentFlowCookie)
	if err != nil {
		return AgentRequest{}, ErrFederationInvalid
	}
	return a.store().AgentFlow(r.Context(), c.Value)
}

func (a *agentAuth) render(w http.ResponseWriter, status int, data map[string]any) {
	web.NoTransform(w)
	a.f.renderAdminStatus(w, status, "admin_agent_flow.html", data, false)
}

func (a *agentAuth) fail(w http.ResponseWriter, status int, message string) {
	a.render(w, status, map[string]any{"Title": "AIエージェントの接続", "Message": message})
}

// authorize は、別のアプリから届く認可要求を検査して保存します。ここには管理者のStrict Cookieが付かないため、ログイン状態を判定しません。
// クライアントか戻り先が不正なら、そのURLへ転送しません。
func (a *agentAuth) authorize(w http.ResponseWriter, r *http.Request) {
	if !a.limiter.Allow() {
		a.fail(w, http.StatusTooManyRequests, "混み合っています。少し待ってから、もう一度お試しください。")
		return
	}
	q := r.URL.Query()
	scope := a.normalizeScope(q.Get("scope"))
	if len(r.URL.RawQuery) > 8192 || scope == "" || !a.validQuery(q) {
		a.fail(w, http.StatusBadRequest, "接続の要求が正しくありません。")
		return
	}
	s := a.store()
	client, err := s.AgentClient(r.Context(), q.Get("client_id"))
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !agentRedirectMatches(client.Redirect, q.Get("redirect_uri"))) {
		a.fail(w, http.StatusBadRequest, "接続の要求が正しくありません。")
		return
	} else if err != nil {
		a.fail(w, http.StatusServiceUnavailable, "一時的に処理できませんでした。少し待ってから、もう一度お試しください。")
		return
	}
	if c, err := r.Cookie(agentFlowCookie); err == nil {
		if err := s.RemoveAgentFlow(r.Context(), c.Value); err != nil {
			a.fail(w, http.StatusServiceUnavailable, "一時的に処理できませんでした。少し待ってから、もう一度お試しください。")
			return
		}
	}
	flow := federation.Token()
	err = s.NewAgentRequest(r.Context(), flow, AgentRequest{
		ID: federation.Token(), Client: client.ID, Redirect: q.Get("redirect_uri"), State: q.Get("state"),
		Challenge: q.Get("code_challenge"), Resource: q.Get("resource"), Scope: scope,
	})
	if errors.Is(err, ErrFederationBusy) {
		a.fail(w, http.StatusTooManyRequests, "混み合っています。少し待ってから、もう一度お試しください。")
		return
	} else if err != nil {
		a.fail(w, http.StatusServiceUnavailable, "一時的に処理できませんでした。少し待ってから、もう一度お試しください。")
		return
	}
	a.setFlowCookie(w, flow, int(agentRequestTTL.Seconds()))
	a.render(w, http.StatusOK, map[string]any{"Title": "AIエージェントの接続", "Entry": true})
}

// continueFlow は、同じサイトの操作でStrict Cookieが付いた状態で、同意の画面を出します。未ログインならパスキーの画面へ移ります。
func (a *agentAuth) continueFlow(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	req, err := a.flow(r)
	if err != nil {
		a.fail(w, http.StatusBadRequest, "接続の手続きが無効か、期限切れです。AIエージェントから、やり直してください。")
		return
	}
	client, err := a.store().AgentClient(r.Context(), req.Client)
	if err != nil {
		a.fail(w, http.StatusForbidden, "このクライアントは利用できません。")
		return
	}
	asked := strings.Fields(req.Scope)
	var labels []string
	for _, def := range a.cfg.Scopes {
		for _, name := range asked {
			if name == def.Name {
				labels = append(labels, def.Label)
			}
		}
	}
	data := s.PageData("AIエージェントの接続", "")
	data["RequestID"] = req.ID
	data["ClientLabel"] = client.Label
	data["Resource"] = req.Resource
	data["Redirect"] = req.Redirect
	data["Scopes"] = labels
	a.render(w, http.StatusOK, data)
}

// returnTo は、クライアントへ戻るページを返します。CSPの form-action はフォーム送信後の別サイトへの転送を止めるため、
// POSTの応答を303にせず、Refreshヘッダーと通常のリンクで移動します。
func (a *agentAuth) returnTo(w http.ResponseWriter, location string) {
	w.Header().Set("Refresh", "0; url="+location)
	a.render(w, http.StatusOK, map[string]any{"Title": "AIエージェントへ戻ります", "Next": location})
}

func (a *agentAuth) location(redirect string, values url.Values) (string, error) {
	u, err := url.Parse(redirect)
	if err != nil {
		return "", err
	}
	values.Set("iss", a.f.cfg.Origin)
	u.RawQuery = values.Encode()
	return u.String(), nil
}

// deny は、検証済みの戻り先へ access_denied を返します。
func (a *agentAuth) deny(w http.ResponseWriter, r *http.Request, q AgentRequest) {
	if c, err := r.Cookie(agentFlowCookie); err == nil {
		if err := a.store().RemoveAgentFlow(r.Context(), c.Value); err != nil {
			a.f.logger.Error("agent remove flow failed", web.RequestIDAttr(w))
		}
	}
	a.setFlowCookie(w, "", -1)
	location, err := a.location(q.Redirect, url.Values{"error": {"access_denied"}, "state": {q.State}})
	if err != nil {
		a.fail(w, http.StatusBadRequest, "接続の要求が正しくありません。")
		return
	}
	a.returnTo(w, location)
}

// approve は、本人の操作を確かめて世代を固定し、一回限りのコードを発行します。
func (a *agentAuth) approve(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	q, err := a.flow(r)
	if err != nil {
		a.fail(w, http.StatusBadRequest, "接続の手続きが無効か、期限切れです。AIエージェントから、やり直してください。")
		return
	}
	// 画面に出した要求だけを承認できる。別のタブで新しい要求が始まっていたら、見ていない内容を承認させない（AUTH-INV-25）
	if r.PostForm.Get("request_id") != q.ID {
		a.fail(w, http.StatusBadRequest, "接続の手続きが変わりました。AIエージェントから、やり直してください。")
		return
	}
	if r.PostForm.Get("decision") != "allow" {
		a.deny(w, r, q)
		return
	}
	session, err := r.Cookie(adminCookieName)
	if err != nil {
		a.fail(w, http.StatusForbidden, "管理画面にログインし直してください。")
		return
	}
	code, err := a.store().ApproveAgent(r.Context(), q.ID, session.Value)
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, ErrFederationInvalid) {
		a.deny(w, r, q) // 許可がない・停止中
		return
	} else if err != nil {
		a.fail(w, http.StatusServiceUnavailable, "一時的に処理できませんでした。少し待ってから、もう一度お試しください。")
		return
	}
	location, err := a.location(q.Redirect, url.Values{"code": {code}, "state": {q.State}})
	if err != nil {
		a.fail(w, http.StatusBadRequest, "接続の要求が正しくありません。")
		return
	}
	a.setFlowCookie(w, "", -1)
	a.returnTo(w, location)
}

func oauthError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Cache-Control", "no-store")
	federation.JSON(w, status, map[string]string{"error": code})
}

// token は、コードをトークンに換えます。公開クライアントなので秘密値は見ません。
// 登録の無いクライアントや、秘密値を持つ連携先は受け付けません。コミットの後で応答を送ります。
func (a *agentAuth) token(w http.ResponseWriter, r *http.Request) {
	if !a.limiter.Allow() {
		oauthError(w, http.StatusTooManyRequests, "temporarily_unavailable")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	if !federation.Form(r) || r.PostForm.Get("grant_type") != "authorization_code" || r.Header.Get("Authorization") != "" {
		oauthError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	for key := range r.PostForm {
		switch key {
		case "grant_type", "code", "redirect_uri", "code_verifier", "client_id", "resource":
		default:
			oauthError(w, http.StatusBadRequest, "invalid_request")
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	s := a.store()
	client := r.PostForm.Get("client_id")
	if _, err := s.AgentClient(ctx, client); errors.Is(err, sql.ErrNoRows) {
		oauthError(w, http.StatusUnauthorized, "invalid_client")
		return
	} else if err != nil {
		oauthError(w, http.StatusServiceUnavailable, "temporarily_unavailable")
		return
	}
	grant, err := s.ExchangeAgentCode(ctx, client, r.PostForm.Get("code"), r.PostForm.Get("redirect_uri"), r.PostForm.Get("code_verifier"), r.PostForm.Get("resource"))
	if errors.Is(err, ErrFederationInvalid) || errors.Is(err, ErrFederationReplay) {
		oauthError(w, http.StatusBadRequest, "invalid_grant")
		return
	} else if errors.Is(err, ErrFederationBusy) {
		oauthError(w, http.StatusTooManyRequests, "temporarily_unavailable")
		return
	} else if err != nil {
		oauthError(w, http.StatusServiceUnavailable, "temporarily_unavailable")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	federation.JSON(w, http.StatusOK, map[string]any{
		"access_token": grant.Token, "token_type": "Bearer", "scope": grant.Scope, "expires_in": grant.ExpiresIn,
	})
}
