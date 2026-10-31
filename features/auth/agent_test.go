package auth

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"minimal-web-kit/features"
	"minimal-web-kit/features/auth/authtest"
)

const (
	agentResource = "http://localhost:8080/mcp"
	agentVerifier = "verifier-verifier-verifier-verifier-verifier-0001"
	agentRedirect = "http://localhost:18767/callback"
)

func agentChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

type agentFixture struct {
	fx      *fixture
	store   *FederationStore
	auth    features.AgentAuth
	client  FederationClient
	account int64
	cookie  *http.Cookie
}

// newAgentFixture は、管理者認証の本体だけでエージェントの経路を有効にします（OIDCのライブラリは使わない）。
func newAgentFixture(t *testing.T) *agentFixture {
	t.Helper()
	fx := newFixture(t)
	a, err := newAgentAuthForTest(fx.feature)
	if err != nil {
		t.Fatal(err)
	}
	store := NewFederationStore(fx.db)
	client, err := store.CreateAgentClient(context.Background(), "テスト用エージェント", "http://localhost/callback")
	if err != nil {
		t.Fatal(err)
	}
	cookie, account := authtest.NewAccountSession(t, fx.db, "member")
	if _, err := store.Grant(context.Background(), client.ID, account); err != nil {
		t.Fatal(err)
	}
	return &agentFixture{fx: fx, store: store, auth: a, client: client, account: account, cookie: cookie}
}

func newAgentAuthForTest(f *Feature) (features.AgentAuth, error) {
	return f.AgentAuth(features.AgentAuthConfig{Path: "/mcp", Scopes: []features.AgentScope{
		{Name: "mcp:read", Label: "内容を読む"}, {Name: "mcp:write", Label: "内容を変更する"},
	}})
}

// issue は、認可要求から本人の承認までを保存処理で進め、コードを返します。
func (a *agentFixture) issue(t *testing.T, scope string) (string, AgentRequest) {
	t.Helper()
	ctx := context.Background()
	r := AgentRequest{ID: strings.Repeat("r", 20) + time.Now().Format("150405.000000000"), Client: a.client.ID, Redirect: agentRedirect, State: "state-state", Challenge: agentChallenge(agentVerifier), Resource: agentResource, Scope: scope}
	if err := a.store.NewAgentRequest(ctx, r.ID+"flow", r); err != nil {
		t.Fatal(err)
	}
	code, err := a.store.ApproveAgent(ctx, r.ID, a.cookie.Value)
	if err != nil {
		t.Fatal(err)
	}
	return code, r
}

func (a *agentFixture) exchange(t *testing.T, code string) AgentGrant {
	t.Helper()
	g, err := a.store.ExchangeAgentCode(context.Background(), a.client.ID, code, agentRedirect, agentVerifier, agentResource)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

// TestAgentCode_BoundAndSingleUse は、束縛の不一致では何も変えず、一致した再利用だけがそのトークンを取り消すことを確かめます。
// 不変条件: AUTH-INV-22
func TestAgentCode_BoundAndSingleUse(t *testing.T) {
	a := newAgentFixture(t)
	ctx := context.Background()
	code, _ := a.issue(t, "mcp:read mcp:write")
	other, err := a.store.CreateAgentClient(ctx, "別", "http://localhost/callback")
	if err != nil {
		t.Fatal(err)
	}
	for name, try := range map[string][4]string{
		"client":   {other.ID, agentRedirect, agentVerifier, agentResource},
		"redirect": {a.client.ID, "http://localhost:1/callback", agentVerifier, agentResource},
		"verifier": {a.client.ID, agentRedirect, "verifier-verifier-verifier-verifier-verifier-9999", agentResource},
		"resource": {a.client.ID, agentRedirect, agentVerifier, "http://localhost:8080/other"},
	} {
		if _, err := a.store.ExchangeAgentCode(ctx, try[0], code, try[1], try[2], try[3]); !errors.Is(err, ErrFederationInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	epochBefore := a.grantEpoch(t)
	g := a.exchange(t, code)
	if _, err := a.store.VerifyAgentToken(ctx, g.Token, agentResource); err != nil {
		t.Fatal(err)
	}
	// 秘密でない client_id だけの再送は、何も変えない
	if _, err := a.store.ExchangeAgentCode(ctx, a.client.ID, code, agentRedirect, "verifier-verifier-verifier-verifier-verifier-9999", agentResource); !errors.Is(err, ErrFederationInvalid) {
		t.Fatal(err)
	}
	if _, err := a.store.VerifyAgentToken(ctx, g.Token, agentResource); err != nil {
		t.Fatal("unbound replay changed state:", err)
	}
	if _, err := a.store.ExchangeAgentCode(ctx, a.client.ID, code, agentRedirect, agentVerifier, agentResource); !errors.Is(err, ErrFederationReplay) {
		t.Fatal(err)
	}
	if _, err := a.store.VerifyAgentToken(ctx, g.Token, agentResource); !errors.Is(err, features.ErrBearerInvalid) {
		t.Fatal("replay kept the token:", err)
	}
	if got := a.grantEpoch(t); got != epochBefore {
		t.Fatal("replay advanced the grant epoch")
	}
}

func (a *agentFixture) grantEpoch(t *testing.T) int {
	t.Helper()
	var n int
	if err := a.fx.db.QueryRow("SELECT grant_epoch FROM auth_oidc_grants WHERE client_id = ?", a.client.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestAgentCode_Expires はコードの期限を確かめます。
// 不変条件: AUTH-INV-22
func TestAgentCode_Expires(t *testing.T) {
	a := newAgentFixture(t)
	code, _ := a.issue(t, "mcp:read")
	a.store.now = func() time.Time { return time.Now().Add(agentCodeTTL + time.Second) }
	if _, err := a.store.ExchangeAgentCode(context.Background(), a.client.ID, code, agentRedirect, agentVerifier, agentResource); !errors.Is(err, ErrFederationInvalid) {
		t.Fatal(err)
	}
}

// TestAgentToken_FollowsEpochs は、停止・取り消し・クライアントの停止・別のリソース・期限切れで直ちに無効になり、
// 再開しても古いトークンが戻らないことを確かめます。
// 不変条件: AUTH-INV-23
func TestAgentToken_FollowsEpochs(t *testing.T) {
	ctx := context.Background()
	cases := map[string]func(t *testing.T, a *agentFixture){
		"revoke grant": func(t *testing.T, a *agentFixture) {
			if err := a.store.Revoke(ctx, a.client.ID, a.account); err != nil {
				t.Fatal(err)
			}
			if _, err := a.store.Grant(ctx, a.client.ID, a.account); err != nil {
				t.Fatal(err)
			}
		},
		"disable client": func(t *testing.T, a *agentFixture) {
			if err := a.store.SetClientEnabled(ctx, a.client.ID, false); err != nil {
				t.Fatal(err)
			}
			if err := a.store.SetClientEnabled(ctx, a.client.ID, true); err != nil {
				t.Fatal(err)
			}
		},
		"stop account": func(t *testing.T, a *agentFixture) {
			if _, err := a.fx.db.Exec("UPDATE auth_accounts SET disabled_at = 1 WHERE id = ?", a.account); err != nil {
				t.Fatal(err)
			}
			if _, err := a.fx.db.Exec("UPDATE auth_accounts SET disabled_at = NULL WHERE id = ?", a.account); err != nil {
				t.Fatal(err)
			}
		},
		"expired": func(t *testing.T, a *agentFixture) {
			a.store.now = func() time.Time { return time.Now().Add(agentTokenTTL + time.Second) }
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			a := newAgentFixture(t)
			code, _ := a.issue(t, "mcp:read mcp:write")
			g := a.exchange(t, code)
			p, err := a.store.VerifyAgentToken(ctx, g.Token, agentResource)
			if err != nil {
				t.Fatal(err)
			}
			if p.AccountID != a.account || p.Client != a.client.ID || p.Role != features.RoleMember || !p.HasScope("mcp:write") || p.Generation == "" {
				t.Fatalf("%+v", p)
			}
			if _, err := a.store.VerifyAgentToken(ctx, g.Token, "http://localhost:8080/other"); !errors.Is(err, features.ErrBearerInvalid) {
				t.Fatal("other resource accepted:", err)
			}
			change(t, a)
			if _, err := a.store.VerifyAgentToken(ctx, g.Token, agentResource); !errors.Is(err, features.ErrBearerInvalid) {
				t.Fatal("token survived:", err)
			}
		})
	}
}

// TestAgentToken_ReadsRoleEachTime は、ロールを要求ごとに読むことを確かめます。
// 不変条件: AUTH-INV-23
func TestAgentToken_ReadsRoleEachTime(t *testing.T) {
	a := newAgentFixture(t)
	ctx := context.Background()
	code, _ := a.issue(t, "mcp:read")
	g := a.exchange(t, code)
	if _, err := a.fx.db.Exec("UPDATE auth_accounts SET role = 'admin' WHERE id = ?", a.account); err != nil {
		t.Fatal(err)
	}
	p, err := a.store.VerifyAgentToken(ctx, g.Token, agentResource)
	if err != nil || p.Role != features.RoleAdmin {
		t.Fatal(p, err)
	}
}

// TestAgentToken_UnverifiableIsNotValid は、DBで確かめられないときに、有効と扱わないことを確かめます。
// 不変条件: AUTH-INV-23
func TestAgentToken_UnverifiableIsNotValid(t *testing.T) {
	a := newAgentFixture(t)
	code, _ := a.issue(t, "mcp:read")
	g := a.exchange(t, code)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p, err := a.store.VerifyAgentToken(ctx, g.Token, agentResource)
	if err == nil || p.AccountID != 0 {
		t.Fatal(p, err)
	}
}

// TestAgentClient_KindComesFromRegistration は、OIDCの連携先がエージェントの経路で使えないことと、戻り先の規則を確かめます。
// 不変条件: AUTH-INV-24
func TestAgentClient_KindComesFromRegistration(t *testing.T) {
	a := newAgentFixture(t)
	ctx := context.Background()
	oidc, _, err := a.store.CreateClient(ctx, "OIDC", "https://b.example/cb")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.store.AgentClient(ctx, oidc.ID); err == nil {
		t.Fatal("confidential client treated as an agent client")
	}
	for _, bad := range []string{"https://b.example/cb", "http://example.com/cb", "http://localhost:1/cb", "http://localhost/cb?x=1", "http://localhost/cb#f", "http://u@localhost/cb", "http://localhost"} {
		if _, err := a.store.CreateAgentClient(ctx, "x", bad); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("%s: %v", bad, err)
		}
	}
	for requested, want := range map[string]bool{
		"http://localhost:1234/callback":   true,
		"http://localhost/callback":        false,
		"http://127.0.0.1:1234/callback":   false,
		"https://localhost:1234/callback":  false,
		"http://localhost:1234/other":      false,
		"http://localhost:1234/callback?a": false,
		"http://evil@localhost:1/callback": false,
		"http://localhost.evil:1/callback": false,
	} {
		if got := agentRedirectMatches("http://localhost/callback", requested); got != want {
			t.Errorf("%s: %v", requested, got)
		}
	}
}

// agentHTTP は、経路をCoreの独立経路として組み込みます。
func agentHTTP(t *testing.T, a *agentFixture) http.Handler {
	t.Helper()
	h, err := a.fx.withRoutes(a.auth.RegisterAgentRoutes)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestAgentHTTP_AuthorizeToToken(t *testing.T) {
	a := newAgentFixture(t)
	h := agentHTTP(t, a)
	q := url.Values{"client_id": {a.client.ID}, "redirect_uri": {agentRedirect}, "response_type": {"code"}, "scope": {"mcp:write mcp:read"},
		"state": {"state-state"}, "code_challenge": {agentChallenge(agentVerifier)}, "code_challenge_method": {"S256"}, "resource": {agentResource}}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/agent/authorize?"+q.Encode(), nil))
	if rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	var flow *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == agentFlowCookie {
			flow = c
		}
	}
	if flow == nil || !flow.HttpOnly || flow.SameSite != http.SameSiteStrictMode {
		t.Fatalf("%+v", flow)
	}
	get := httptest.NewRequest(http.MethodGet, "/admin/agent/continue", nil)
	get.AddCookie(a.cookie)
	get.AddCookie(flow)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, get)
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, "テスト用エージェント") || !strings.Contains(body, "内容を変更する") || !strings.Contains(body, agentResource) {
		t.Fatal(rec.Code, body)
	}
	post := httptest.NewRequest(http.MethodPost, "/admin/agent/approve", strings.NewReader(url.Values{"csrf_token": {hiddenValue(body, "csrf_token")}, "request_id": {hiddenValue(body, "request_id")}, "decision": {"allow"}}.Encode()))
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	post.Header.Set("Origin", "http://localhost:8080")
	post.Header.Set("Sec-Fetch-Site", "same-origin")
	post.AddCookie(a.cookie)
	post.AddCookie(flow)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, post)
	refresh := rec.Header().Get("Refresh")
	if rec.Code != 200 || !strings.HasPrefix(refresh, "0; url="+agentRedirect+"?") {
		t.Fatal(rec.Code, refresh, rec.Body.String())
	}
	back, err := url.Parse(strings.TrimPrefix(refresh, "0; url="))
	if err != nil || back.Query().Get("state") != "state-state" || back.Query().Get("iss") != "http://localhost:8080" {
		t.Fatal(back, err)
	}
	form := url.Values{"grant_type": {"authorization_code"}, "code": {back.Query().Get("code")}, "redirect_uri": {agentRedirect}, "code_verifier": {agentVerifier}, "client_id": {a.client.ID}, "resource": {agentResource}}
	tok := httptest.NewRequest(http.MethodPost, "/agent/token", strings.NewReader(form.Encode()))
	tok.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, tok)
	var got struct {
		AccessToken string `json:"access_token"`
		Scope       string `json:"scope"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if rec.Code != 200 || !strings.Contains(rec.Header().Get("Cache-Control"), "no-store") || json.Unmarshal(rec.Body.Bytes(), &got) != nil {
		t.Fatal(rec.Code, rec.Body.String())
	}
	if got.Scope != "mcp:read mcp:write" || got.ExpiresIn != int64(agentTokenTTL.Seconds()) {
		t.Fatalf("%+v", got)
	}
	if _, err := a.auth.VerifyBearer(context.Background(), got.AccessToken, agentResource); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(a.fx.logs.String(), got.AccessToken) || strings.Contains(a.fx.logs.String(), back.Query().Get("code")) {
		t.Fatal("secret in logs")
	}
}

// TestAgentHTTP_RejectsBadRequests は、不正な要求を保存せず、戻り先へ転送しないことを確かめます。
// 不変条件: AUTH-INV-24
func TestAgentHTTP_RejectsBadRequests(t *testing.T) {
	a := newAgentFixture(t)
	h := agentHTTP(t, a)
	base := func() url.Values {
		return url.Values{"client_id": {a.client.ID}, "redirect_uri": {agentRedirect}, "response_type": {"code"}, "scope": {"mcp:read"},
			"state": {"state-state"}, "code_challenge": {agentChallenge(agentVerifier)}, "code_challenge_method": {"S256"}, "resource": {agentResource}}
	}
	for name, change := range map[string]func(url.Values){
		"unknown client": func(q url.Values) { q.Set("client_id", "nope") },
		"bad redirect":   func(q url.Values) { q.Set("redirect_uri", "http://evil.example:1/callback") },
		"plain pkce":     func(q url.Values) { q.Set("code_challenge_method", "plain") },
		"bad challenge":  func(q url.Values) { q.Set("code_challenge", "abc") },
		"other resource": func(q url.Values) { q.Set("resource", "http://localhost:8080/other") },
		"no resource":    func(q url.Values) { q.Del("resource") },
		"other scope":    func(q url.Values) { q.Set("scope", "openid") },
		"mixed scope":    func(q url.Values) { q.Set("scope", "mcp:read openid") },
		"repeated scope": func(q url.Values) { q.Set("scope", "mcp:read mcp:read") },
		"extra param":    func(q url.Values) { q.Set("prompt", "none") },
		"repeated param": func(q url.Values) { q.Add("state", "other-other") },
		"short state":    func(q url.Values) { q.Set("state", "s") },
	} {
		q := base()
		change(q)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/agent/authorize?"+q.Encode(), nil))
		if rec.Code != 400 || rec.Header().Get("Location") != "" || rec.Header().Get("Refresh") != "" {
			t.Errorf("%s: %d", name, rec.Code)
		}
	}
	var n int
	if err := a.fx.db.QueryRow("SELECT COUNT(*) FROM auth_agent_requests").Scan(&n); err != nil || n != 0 {
		t.Fatal(n, err)
	}
	// 秘密値を持つ連携先は、トークンの経路で使えない
	oidc, _, err := a.store.CreateClient(context.Background(), "OIDC", "https://b.example/cb")
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{"grant_type": {"authorization_code"}, "code": {"x"}, "redirect_uri": {agentRedirect}, "code_verifier": {agentVerifier}, "client_id": {oidc.ID}, "resource": {agentResource}}
	tok := httptest.NewRequest(http.MethodPost, "/agent/token", strings.NewReader(form.Encode()))
	tok.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, tok)
	if rec.Code != 401 {
		t.Fatal(rec.Code)
	}
}

func TestAgentHTTP_Metadata(t *testing.T) {
	a := newAgentFixture(t)
	rec := httptest.NewRecorder()
	agentHTTP(t, a).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/.well-known/oauth-authorization-server", nil))
	var m map[string]any
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &m) != nil {
		t.Fatal(rec.Code, rec.Body.String())
	}
	if m["issuer"] != "http://localhost:8080" || m["authorization_endpoint"] != "http://localhost:8080/admin/agent/authorize" {
		t.Fatalf("%v", m)
	}
	if _, ok := m["registration_endpoint"]; ok {
		t.Fatal("unimplemented endpoint advertised")
	}
}

func TestPurge_RemovesExpiredAgentData(t *testing.T) {
	a := newAgentFixture(t)
	code, _ := a.issue(t, "mcp:read")
	a.exchange(t, code)
	a.fx.feature.now = func() time.Time { return time.Now().Add(agentTokenTTL + time.Minute) }
	if err := a.fx.feature.Purge(context.Background()); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := a.fx.db.QueryRow("SELECT (SELECT COUNT(*) FROM auth_agent_tokens) + (SELECT COUNT(*) FROM auth_agent_requests)").Scan(&n); err != nil || n != 0 {
		t.Fatal(n, err)
	}
}

func hiddenValue(body, name string) string {
	marker := `name="` + name + `" value="`
	rest := body[strings.Index(body, marker)+len(marker):]
	return rest[:strings.Index(rest, `"`)]
}

// TestAgentHTTP_ApprovesOnlyTheShownRequest は、画面を開いた後に新しい要求が始まっても、見ていない要求を承認できないことを確かめます。
// 不変条件: AUTH-INV-25
func TestAgentHTTP_ApprovesOnlyTheShownRequest(t *testing.T) {
	a := newAgentFixture(t)
	h := agentHTTP(t, a)
	query := func(scope string) string {
		return url.Values{"client_id": {a.client.ID}, "redirect_uri": {agentRedirect}, "response_type": {"code"}, "scope": {scope},
			"state": {"state-state"}, "code_challenge": {agentChallenge(agentVerifier)}, "code_challenge_method": {"S256"}, "resource": {agentResource}}.Encode()
	}
	start := func(scope string) *http.Cookie {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/agent/authorize?"+query(scope), nil))
		for _, c := range rec.Result().Cookies() {
			if c.Name == agentFlowCookie {
				return c
			}
		}
		t.Fatal("no flow cookie")
		return nil
	}
	first := start("mcp:read")
	get := httptest.NewRequest(http.MethodGet, "/admin/agent/continue", nil)
	get.AddCookie(a.cookie)
	get.AddCookie(first)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, get)
	shown := rec.Body.String()
	second := start("mcp:read mcp:write")
	post := httptest.NewRequest(http.MethodPost, "/admin/agent/approve", strings.NewReader(url.Values{"csrf_token": {hiddenValue(shown, "csrf_token")}, "request_id": {hiddenValue(shown, "request_id")}, "decision": {"allow"}}.Encode()))
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	post.Header.Set("Origin", "http://localhost:8080")
	post.Header.Set("Sec-Fetch-Site", "same-origin")
	post.AddCookie(a.cookie)
	post.AddCookie(second)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, post)
	if rec.Code != 400 || rec.Header().Get("Refresh") != "" {
		t.Fatal(rec.Code, rec.Header())
	}
	var issued int
	if err := a.fx.db.QueryRow("SELECT COUNT(*) FROM auth_agent_requests WHERE code_hash IS NOT NULL").Scan(&issued); err != nil || issued != 0 {
		t.Fatal(issued, err)
	}
}

// TestAgentRows_RemovedWithTheLogin は、停止・取り消し等で、要求とトークンの行も同じトランザクションで消えることを確かめます。
// 不変条件: AUTH-INV-17
func TestAgentRows_RemovedWithTheLogin(t *testing.T) {
	ctx := context.Background()
	for name, change := range map[string]func(t *testing.T, a *agentFixture){
		"revoke":  func(t *testing.T, a *agentFixture) { _ = a.store.Revoke(ctx, a.client.ID, a.account) },
		"disable": func(t *testing.T, a *agentFixture) { _ = a.store.SetClientEnabled(ctx, a.client.ID, false) },
		"all":     func(t *testing.T, a *agentFixture) { _ = a.store.RevokeAll(ctx) },
		"stop": func(t *testing.T, a *agentFixture) {
			if _, err := a.fx.db.Exec("UPDATE auth_accounts SET disabled_at = 1 WHERE id = ?", a.account); err != nil {
				t.Fatal(err)
			}
		},
		"delete": func(t *testing.T, a *agentFixture) {
			if _, err := a.fx.db.Exec("DELETE FROM auth_accounts WHERE id = ?", a.account); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			a := newAgentFixture(t)
			code, _ := a.issue(t, "mcp:read")
			a.exchange(t, code)
			change(t, a)
			var n int
			if err := a.fx.db.QueryRow("SELECT (SELECT COUNT(*) FROM auth_agent_tokens) + (SELECT COUNT(*) FROM auth_agent_requests)").Scan(&n); err != nil || n != 0 {
				t.Fatal(n, err)
			}
		})
	}
}

// TestAgentCode_SurvivesTheRequestDeadline は、要求の期限の直前に承認したコードが、掃除で消えないことを確かめます。
// 不変条件: AUTH-INV-22
func TestAgentCode_SurvivesTheRequestDeadline(t *testing.T) {
	a := newAgentFixture(t)
	code, r := a.issue(t, "mcp:read")
	if _, err := a.fx.db.Exec("UPDATE auth_agent_requests SET expires_at = ? WHERE request_id = ?", time.Now().Add(time.Second).Unix(), r.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.store.ApproveAgent(context.Background(), r.ID, a.cookie.Value); err != nil {
		t.Fatal(err)
	}
	var left int64
	if err := a.fx.db.QueryRow("SELECT expires_at FROM auth_agent_requests WHERE request_id = ?", r.ID).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left < time.Now().Add(agentCodeTTL-5*time.Second).Unix() {
		t.Fatal("request ends before the code")
	}
	_ = code
}

// TestLoginClient_ExcludesAgentClients は、エージェントの公開クライアントがOIDCのログインに使えないことを確かめます。
// 不変条件: AUTH-INV-24
func TestLoginClient_ExcludesAgentClients(t *testing.T) {
	a := newAgentFixture(t)
	ctx := context.Background()
	if _, err := a.store.LoginClient(ctx, a.client.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal(err)
	}
	oidc, _, err := a.store.CreateClient(ctx, "OIDC", "https://b.example/cb")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.store.LoginClient(ctx, oidc.ID); err != nil {
		t.Fatal(err)
	}
}
