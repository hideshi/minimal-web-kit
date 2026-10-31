package federatedauth

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"

	"minimal-web-kit/internal/federation"
	"minimal-web-kit/internal/store"
)

var testEpochs = federation.Epochs{Account: "1", Client: "2", Grant: "3"}

func TestValidNext(t *testing.T) {
	for _, next := range []string{"/admin", "/admin/news", "/admin/news/1/edit", "/admin/news?page=2", "/admin?x=1"} {
		if !validNext(next) {
			t.Errorf("%q rejected", next)
		}
	}
	for _, next := range []string{
		"", "/", "/other", "/administrator", "//evil.example", "/admin//evil", "/admin/../other", "/admin/%2e%2e/x",
		`/admin\evil`, "/admin/a\nb", "/admin/login", "/admin/sso/callback", "https://evil.example/admin", "/admin/a#f",
	} {
		if validNext(next) {
			t.Errorf("%q accepted", next)
		}
	}
}

// 試行は一回限りで、Cookieとstateの両方が合うときだけ取り出せ、期限で失効すること。
//
// 不変条件: FEDAUTH-INV-3
func TestAttempts_OneTimeAndExpiring(t *testing.T) {
	now := time.Now()
	s := newAttempts(func() time.Time { return now })
	add := func(cookie, state string) {
		t.Helper()
		if err := s.add(cookie, &attempt{stateHash: federation.Hash(state)}); err != nil {
			t.Fatal(err)
		}
	}
	add("c", "s")
	if _, ok := s.take("other", "s"); ok {
		t.Fatal("wrong cookie accepted")
	}
	if _, ok := s.take("c", "wrong"); ok {
		t.Fatal("wrong state accepted")
	}
	if _, ok := s.take("c", "s"); ok {
		t.Fatal("a failed attempt must be gone")
	}
	add("c", "s")
	if _, ok := s.take("c", "s"); !ok {
		t.Fatal("valid attempt rejected")
	}
	if _, ok := s.take("c", "s"); ok {
		t.Fatal("attempt reused")
	}
	add("c", "s")
	now = now.Add(attemptTTL)
	if _, ok := s.take("c", "s"); ok {
		t.Fatal("expired attempt accepted")
	}
}

// 満杯のときは最古の試行を捨てて、新しいログインを止めないこと。受付の超過は新しい試行を作らないこと。
func TestAttempts_EvictOldestAndLimit(t *testing.T) {
	now := time.Now()
	s := newAttempts(func() time.Time { return now })
	for i := range maxAttempts {
		s.items[federation.Hash(fmt.Sprint(i))] = &attempt{started: now.Add(time.Duration(i) * time.Millisecond), stateHash: federation.Hash("s")}
	}
	if err := s.add("new", &attempt{stateHash: federation.Hash("s")}); err != nil {
		t.Fatal(err)
	}
	if len(s.items) != maxAttempts {
		t.Fatalf("size %d", len(s.items))
	}
	if _, ok := s.items[federation.Hash("0")]; ok {
		t.Fatal("the oldest attempt was not evicted")
	}
	if _, ok := s.take("new", "s"); !ok {
		t.Fatal("new attempt lost")
	}
	limited := newAttempts(func() time.Time { return now })
	var rejected int
	for i := range 40 {
		if errors.Is(limited.add(fmt.Sprint(i), &attempt{}), errAttemptLimited) {
			rejected++
		}
	}
	if rejected == 0 || len(limited.items)+rejected != 40 {
		t.Fatalf("rejected %d, kept %d", rejected, len(limited.items))
	}
}

// セッションは、無操作2時間と、本人確認から12時間で失効すること。停止するとセッションも消えること。
//
// 不変条件: FEDAUTH-INV-4
func TestSessions_Expiry(t *testing.T) {
	s, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "b.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.DB().Exec(migrationSQL); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	st := &sessionStore{db: s.DB(), issuer: "https://a.example", now: func() time.Time { return now }}
	ctx := context.Background()

	if _, err := st.login(ctx, "sub", testEpochs, now.Add(-13*time.Hour).Unix()); !errors.Is(err, errNoSession) {
		t.Fatalf("old authentication: %v", err)
	}
	token, err := st.login(ctx, "sub", testEpochs, now.Add(-11*time.Hour).Unix())
	if err != nil {
		t.Fatal(err)
	}
	got, err := st.session(ctx, token)
	if err != nil || got.Role != "site_admin" || got.Epochs != testEpochs {
		t.Fatalf("%+v %v", got, err)
	}
	now = now.Add(time.Hour + time.Second) // 本人確認から12時間を超える
	if _, err := st.session(ctx, token); !errors.Is(err, errNoSession) {
		t.Fatalf("absolute expiry: %v", err)
	}

	now = time.Now()
	token, err = st.login(ctx, "sub", testEpochs, now.Unix())
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(sessionIdle)
	if _, err := st.session(ctx, token); !errors.Is(err, errNoSession) {
		t.Fatalf("idle expiry: %v", err)
	}
	if err := st.purge(ctx); err != nil {
		t.Fatal(err)
	}

	token, err = st.login(ctx, "sub", testEpochs, now.Unix())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.setRole(ctx, "sub", "owner"); !errors.Is(err, errInvalidRole) {
		t.Fatal(err)
	}
	if err := st.setRole(ctx, "unknown", "admin"); !errors.Is(err, errNotFound) {
		t.Fatal(err)
	}
	if err := st.setDisabled(ctx, "sub", true); err != nil {
		t.Fatal(err)
	}
	if _, err := st.session(ctx, token); !errors.Is(err, errNoSession) {
		t.Fatal(err)
	}
	if _, err := st.login(ctx, "sub", testEpochs, now.Unix()); !errors.Is(err, errDisabled) {
		t.Fatalf("disabled login: %v", err)
	}
}

func newTestUpstream(t *testing.T, handler http.HandlerFunc, now *time.Time) (*upstream, *httptest.Server) {
	t.Helper()
	srv := httptest.NewTLSServer(handler)
	t.Cleanup(srv.Close)
	u := newUpstream(Config{Origin: "https://b.example", Issuer: srv.URL, ClientID: "client", Transport: srv.Client().Transport}, "secret", func() time.Time { return *now })
	return u, srv
}

// 照会の結果は、正しい形式の無効だけが無効で、それ以外はすべて確認不能として扱うこと。
//
// 不変条件: FEDAUTH-INV-1, FEDAUTH-INV-2
func TestUpstreamCheck_Table(t *testing.T) {
	var issuer string
	active := func() string {
		return fmt.Sprintf(`{"active":true,"iss":%q,"sub":"sub","client_id":"client","mwk_auth":{"account_epoch":"1","client_epoch":"2","grant_epoch":"3"}}`, issuer)
	}
	cases := []struct {
		name   string
		status int
		ctype  string
		body   func() string
		want   checkResult
	}{
		{"active", 200, "application/json", active, checkActive},
		{"active with charset", 200, "application/json; charset=utf-8", active, checkActive},
		{"inactive", 200, "application/json", func() string { return `{"active":false}` }, checkInactive},
		{"inactive with extras", 200, "application/json", func() string { return `{"active":false,"sub":"x"}` }, checkUnavailable},
		{"other subject", 200, "application/json", func() string { return strings.Replace(active(), `"sub":"sub"`, `"sub":"x"`, 1) }, checkUnavailable},
		{"other client", 200, "application/json", func() string { return strings.Replace(active(), `"client_id":"client"`, `"client_id":"x"`, 1) }, checkUnavailable},
		{"other issuer", 200, "application/json", func() string { return strings.Replace(active(), issuer, "https://x.example", 1) }, checkUnavailable},
		{"other epoch", 200, "application/json", func() string { return strings.Replace(active(), `"grant_epoch":"3"`, `"grant_epoch":"4"`, 1) }, checkUnavailable},
		{"no epochs", 200, "application/json", func() string { return fmt.Sprintf(`{"active":true,"iss":%q,"sub":"sub","client_id":"client"}`, issuer) }, checkUnavailable},
		{"empty object", 200, "application/json", func() string { return `{}` }, checkUnavailable},
		{"null body", 200, "application/json", func() string { return `null` }, checkUnavailable},
		{"null active", 200, "application/json", func() string { return `{"active":null}` }, checkUnavailable},
		{"empty extras", 200, "application/json", func() string { return `{"active":false,"sub":""}` }, checkUnavailable},
		{"inactive with spaces", 200, "application/json", func() string { return "{ \"active\" : false }\n" }, checkInactive},
		{"unknown field", 200, "application/json", func() string { return `{"active":false,"x":1}` }, checkUnavailable},
		{"duplicate key", 200, "application/json", func() string { return `{"active":false,"active":true}` }, checkUnavailable},
		{"broken body", 200, "application/json", func() string { return `{` }, checkUnavailable},
		{"not json type", 200, "text/html", func() string { return `{"active":false}` }, checkUnavailable},
		{"no content", 204, "application/json", func() string { return "" }, checkUnavailable},
		{"401", 401, "application/json", func() string { return `{"active":false}` }, checkUnavailable},
		{"403", 403, "application/json", func() string { return `{"active":false}` }, checkUnavailable},
		{"429", 429, "application/json", func() string { return `{"active":false}` }, checkUnavailable},
		{"500", 500, "application/json", func() string { return `{"active":false}` }, checkUnavailable},
		{"too large", 200, "application/json", func() string { return strings.Repeat(" ", 9000) + `{"active":false}` }, checkUnavailable},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			now := time.Now()
			u, srv := newTestUpstream(t, func(w http.ResponseWriter, r *http.Request) {
				id, secret, _ := r.BasicAuth()
				if id != "client" || secret != "secret" || r.URL.Path != "/oidc/check" {
					t.Errorf("request %q %q %q", id, secret, r.URL.Path)
				}
				w.Header().Set("Content-Type", c.ctype)
				w.WriteHeader(c.status)
				_, _ = io.WriteString(w, c.body())
			}, &now)
			issuer = srv.URL
			if got := u.check(context.Background(), "sub", testEpochs); got != c.want {
				t.Fatalf("got %d, want %d", got, c.want)
			}
		})
	}
}

// 障害の後の5秒は問い合わせず、肯定の結果はキャッシュせず、同時照会の上限で待たないこと。
//
// 不変条件: FEDAUTH-INV-2
func TestUpstreamCheck_BreakerAndNoCaching(t *testing.T) {
	var calls atomic.Int64
	var failing atomic.Bool
	now := time.Now()
	var issuer string
	u, srv := newTestUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if failing.Load() {
			http.Error(w, "down", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"active":true,"iss":%q,"sub":"sub","client_id":"client","mwk_auth":{"account_epoch":"1","client_epoch":"2","grant_epoch":"3"}}`, issuer)
	}, &now)
	issuer = srv.URL
	ctx := context.Background()
	for range 3 {
		if u.check(ctx, "sub", testEpochs) != checkActive {
			t.Fatal("not active")
		}
	}
	if calls.Load() != 3 {
		t.Fatalf("positive result was cached: %d calls", calls.Load())
	}
	failing.Store(true)
	if u.check(ctx, "sub", testEpochs) != checkUnavailable {
		t.Fatal("failure not reported")
	}
	calls.Store(0)
	if u.check(ctx, "sub", testEpochs) != checkUnavailable || calls.Load() != 0 {
		t.Fatalf("breaker did not hold: %d calls", calls.Load())
	}
	failing.Store(false)
	now = now.Add(breakerPause)
	if u.check(ctx, "sub", testEpochs) != checkActive {
		t.Fatal("not recovered after the pause")
	}
	for range maxConcurrent {
		u.slots <- struct{}{}
	}
	calls.Store(0)
	if u.check(ctx, "sub", testEpochs) != checkUnavailable || calls.Load() != 0 {
		t.Fatal("concurrency limit did not reject")
	}
}

// 公開鍵の取得は、未知の鍵でも最短30秒あけ、既知の鍵には影響しないこと。
func TestKeySet_RefetchGap(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	jwk := jose.JSONWebKey{Key: &key.PublicKey, KeyID: "known", Algorithm: "ES256", Use: "sig"}
	var fetches atomic.Int64
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetches.Add(1)
		federation.JSON(w, 200, jose.JSONWebKeySet{Keys: []jose.JSONWebKey{jwk}})
	}))
	defer srv.Close()
	now := time.Now()
	url := srv.URL + "/keys"
	k := &keySet{url: url, client: federation.Client([]string{url}, time.Second, 64<<10, srv.Client().Transport), now: func() time.Time { return now }}
	sign := func(id string) string {
		signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: key}, (&jose.SignerOptions{}).WithHeader("kid", id))
		if err != nil {
			t.Fatal(err)
		}
		object, err := signer.Sign([]byte("payload"))
		if err != nil {
			t.Fatal(err)
		}
		compact, err := object.CompactSerialize()
		if err != nil {
			t.Fatal(err)
		}
		return compact
	}
	ctx := context.Background()
	if _, err := k.VerifySignature(ctx, sign("known")); err != nil {
		t.Fatal(err)
	}
	for range 5 {
		if _, err := k.VerifySignature(ctx, sign("unknown")); err == nil {
			t.Fatal("unknown key accepted")
		}
	}
	if fetches.Load() != 1 {
		t.Fatalf("%d fetches within the gap", fetches.Load())
	}
	if _, err := k.VerifySignature(ctx, sign("known")); err != nil {
		t.Fatalf("known key blocked by the gap: %v", err)
	}
	now = now.Add(keyRefetchGap)
	if _, err := k.VerifySignature(ctx, sign("unknown")); err == nil {
		t.Fatal("unknown key accepted")
	}
	if fetches.Load() != 2 {
		t.Fatalf("%d fetches after the gap", fetches.Load())
	}
}

// exchange は、署名が正しくても、issuer・audience・nonce・期限・世代・at_hash が合わないIDトークンを拒否すること。
//
// 不変条件: FEDAUTH-INV-3
// 不変条件: FEDAUTH-INV-8
func TestUpstreamExchange_RejectsBadIDTokens(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	other, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	const accessToken = "access-token"
	sum := sha256.Sum256([]byte(accessToken))
	atHash := base64.RawURLEncoding.EncodeToString(sum[:16])
	nonce := "the-nonce-value"
	now := time.Now()
	var issuer string
	claims := func() map[string]any {
		return map[string]any{
			"iss": issuer, "sub": "sub", "aud": []string{"client"}, "azp": "client", "nonce": nonce,
			"iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix(), "auth_time": now.Unix(), "at_hash": atHash,
			"mwk_auth": map[string]string{"account_epoch": "1", "client_epoch": "2", "grant_epoch": "3"},
		}
	}
	sign := func(t *testing.T, signingKey any, kid string, c map[string]any) string {
		t.Helper()
		signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: signingKey}, (&jose.SignerOptions{}).WithHeader("kid", kid))
		if err != nil {
			t.Fatal(err)
		}
		payload, err := json.Marshal(c)
		if err != nil {
			t.Fatal(err)
		}
		object, err := signer.Sign(payload)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := object.CompactSerialize()
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	var idToken string
	var tokenForms []url.Values
	mux := http.NewServeMux()
	mux.HandleFunc("POST /oidc/token", func(w http.ResponseWriter, r *http.Request) {
		if id, secret, _ := r.BasicAuth(); id != "client" || secret != "secret" {
			t.Errorf("credentials %q %q", id, secret)
		}
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		tokenForms = append(tokenForms, r.PostForm)
		federation.JSON(w, http.StatusOK, map[string]any{"access_token": accessToken, "token_type": "Bearer", "expires_in": 300, "id_token": idToken})
	})
	mux.HandleFunc("GET /oidc/keys", func(w http.ResponseWriter, r *http.Request) {
		federation.JSON(w, http.StatusOK, jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "key", Algorithm: "ES256", Use: "sig"}}})
	})
	srv := httptest.NewTLSServer(mux)
	defer srv.Close()
	issuer = srv.URL
	u := newUpstream(Config{Origin: "https://b.example", Issuer: srv.URL, ClientID: "client", Transport: srv.Client().Transport}, "secret", func() time.Time { return now })
	ctx := context.Background()

	idToken = sign(t, key, "key", claims())
	got, err := u.exchange(ctx, "code", "verifier", federation.Hash(nonce))
	if err != nil {
		t.Fatalf("valid token: %v", err)
	}
	if got.Subject != "sub" || got.Epochs != testEpochs {
		t.Fatalf("%+v", got)
	}
	if form := tokenForms[0]; form.Get("code_verifier") != "verifier" || form.Get("client_secret") != "" {
		t.Fatalf("token request: %v", form)
	}

	for name, change := range map[string]func(c map[string]any){
		"other issuer":     func(c map[string]any) { c["iss"] = "https://evil.example" },
		"other audience":   func(c map[string]any) { c["aud"] = []string{"other"} },
		"two audiences":    func(c map[string]any) { c["aud"] = []string{"client", "other"} },
		"other azp":        func(c map[string]any) { c["azp"] = "other" },
		"other nonce":      func(c map[string]any) { c["nonce"] = "another-nonce" },
		"no nonce":         func(c map[string]any) { delete(c, "nonce") },
		"no subject":       func(c map[string]any) { delete(c, "sub") },
		"expired":          func(c map[string]any) { c["exp"] = now.Add(-3 * time.Minute).Unix() },
		"issued later":     func(c map[string]any) { c["iat"] = now.Add(5 * time.Minute).Unix() },
		"no auth_time":     func(c map[string]any) { delete(c, "auth_time") },
		"future auth_time": func(c map[string]any) { c["auth_time"] = now.Add(5 * time.Minute).Unix() },
		"no epochs":        func(c map[string]any) { delete(c, "mwk_auth") },
		"bad epoch": func(c map[string]any) {
			c["mwk_auth"] = map[string]string{"account_epoch": "01", "client_epoch": "2", "grant_epoch": "3"}
		},
		"other at_hash": func(c map[string]any) { c["at_hash"] = "AAAAAAAAAAAAAAAAAAAAAA" },
	} {
		c := claims()
		change(c)
		idToken = sign(t, key, "key", c)
		if _, err := u.exchange(ctx, "code", "verifier", federation.Hash(nonce)); !errors.Is(err, errIdentity) {
			t.Errorf("%s: %v", name, err)
		}
	}

	idToken = sign(t, other, "key", claims())
	if _, err := u.exchange(ctx, "code", "verifier", federation.Hash(nonce)); !errors.Is(err, errIdentity) {
		t.Errorf("wrong signature: %v", err)
	}
	idToken = "not-a-token"
	if _, err := u.exchange(ctx, "code", "verifier", federation.Hash(nonce)); !errors.Is(err, errIdentity) {
		t.Errorf("malformed token: %v", err)
	}
}
