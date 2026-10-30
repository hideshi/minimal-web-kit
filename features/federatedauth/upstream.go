package federatedauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	jose "github.com/go-jose/go-jose/v4"
	"golang.org/x/oauth2"

	"minimal-web-kit/internal/federation"
)

const (
	checkTimeout   = 2 * time.Second
	keysTimeout    = 2 * time.Second
	tokenTimeout   = 5 * time.Second
	maxConcurrent  = 32
	breakerPause   = 5 * time.Second
	clockTolerance = 2 * time.Minute
	keyRefetchGap  = 30 * time.Second
)

// checkResult は照会の結果です。無効（正しい形式の active:false）だけがセッションを破棄します。
type checkResult int

const (
	checkActive checkResult = iota
	checkInactive
	checkUnavailable
)

// identity は、検証済みのIDトークンから得た本人の情報です。
type identity struct {
	Subject  string
	Epochs   federation.Epochs
	AuthTime int64
}

var errIdentity = errors.New("federatedauth: identity rejected")

// upstream は認証基盤との通信です。接続先は設定した3つのURLに固定し、利用者の入力やHostヘッダーで決めません。
type upstream struct {
	issuer   string
	clientID string
	secret   string
	checkURL string
	oauth    oauth2.Config
	token    *http.Client
	checks   *http.Client
	verifier *oidc.IDTokenVerifier
	slots    chan struct{}
	mu       sync.Mutex
	pauseEnd time.Time
	now      func() time.Time
}

func newUpstream(cfg Config, secret string, now func() time.Time) *upstream {
	keysURL := cfg.Issuer + "/oidc/keys"
	tokenURL := cfg.Issuer + "/oidc/token"
	checkURL := cfg.Issuer + "/oidc/check"
	keys := &keySet{url: keysURL, client: federation.Client([]string{keysURL}, keysTimeout, 64<<10, cfg.Transport), now: now}
	return &upstream{
		issuer:   cfg.Issuer,
		clientID: cfg.ClientID,
		secret:   secret,
		checkURL: checkURL,
		oauth: oauth2.Config{
			ClientID:     cfg.ClientID,
			ClientSecret: secret,
			RedirectURL:  cfg.Origin + "/admin/sso/callback",
			Scopes:       []string{oidc.ScopeOpenID},
			// 自動判定で秘密値を本文へ送らせない
			Endpoint: oauth2.Endpoint{AuthURL: cfg.Issuer + "/admin/oidc/authorize", TokenURL: tokenURL, AuthStyle: oauth2.AuthStyleInHeader},
		},
		token:  federation.Client([]string{tokenURL}, tokenTimeout, 32<<10, cfg.Transport),
		checks: federation.Client([]string{checkURL}, checkTimeout, 8<<10, cfg.Transport),
		verifier: oidc.NewVerifier(cfg.Issuer, keys, &oidc.Config{
			ClientID:             cfg.ClientID,
			SupportedSigningAlgs: []string{oidc.ES256},
			Now:                  func() time.Time { return now().Add(-clockTolerance) },
		}),
		slots: make(chan struct{}, maxConcurrent),
		now:   now,
	}
}

// authCodeURL は認可要求のURLを作ります。
func (u *upstream) authCodeURL(state, nonce, verifier string) string {
	return u.oauth.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier), oidc.Nonce(nonce))
}

// exchange はコードを交換し、IDトークンを検証して本人の情報を返します。
// アクセストークンとIDトークンの生の値は保存しません。
func (u *upstream) exchange(ctx context.Context, code, verifier, nonceHash string) (identity, error) {
	ctx = context.WithValue(ctx, oauth2.HTTPClient, u.token)
	tok, err := u.oauth.Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return identity{}, errIdentity
	}
	raw, ok := tok.Extra("id_token").(string)
	if !ok {
		return identity{}, errIdentity
	}
	idToken, err := u.verifier.Verify(ctx, raw)
	if err != nil {
		return identity{}, errIdentity
	}
	var claims struct {
		AuthTime int64             `json:"auth_time"`
		Azp      string            `json:"azp"`
		Epochs   federation.Epochs `json:"mwk_auth"`
	}
	if err := idToken.Claims(&claims); err != nil {
		return identity{}, errIdentity
	}
	now := u.now()
	if len(idToken.Audience) != 1 || idToken.Issuer != u.issuer || idToken.Subject == "" || federation.Hash(idToken.Nonce) != nonceHash ||
		(claims.Azp != "" && claims.Azp != u.clientID) || idToken.IssuedAt.After(now.Add(clockTolerance)) ||
		claims.AuthTime <= 0 || claims.AuthTime > now.Add(clockTolerance).Unix() || !claims.Epochs.Valid() {
		return identity{}, errIdentity
	}
	if idToken.AccessTokenHash != "" && idToken.VerifyAccessToken(tok.AccessToken) != nil {
		return identity{}, errIdentity
	}
	return identity{Subject: idToken.Subject, Epochs: claims.Epochs, AuthTime: claims.AuthTime}, nil
}

// check は、ログイン時の世代が今も有効かを認証基盤に問い合わせます。結果は要求をまたいでキャッシュしません。
// 同時32件を超えたときと、障害の後の5秒間は、問い合わせずに確認不能とします（待ち行列・再試行なし）。
func (u *upstream) check(ctx context.Context, subject string, epochs federation.Epochs) checkResult {
	u.mu.Lock()
	paused := u.now().Before(u.pauseEnd)
	u.mu.Unlock()
	if paused {
		return checkUnavailable
	}
	select {
	case u.slots <- struct{}{}:
		defer func() { <-u.slots }()
	default:
		return checkUnavailable
	}
	body, err := json.Marshal(federation.CheckRequest{Subject: subject, Epochs: epochs})
	if err != nil {
		return checkUnavailable
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.checkURL, bytes.NewReader(body))
	if err != nil {
		return checkUnavailable
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(url.QueryEscape(u.clientID), url.QueryEscape(u.secret))
	res, err := u.checks.Do(req)
	if err != nil {
		if ctx.Err() == nil {
			u.pause()
		}
		return checkUnavailable
	}
	defer res.Body.Close()
	if res.StatusCode >= 500 {
		u.pause()
		return checkUnavailable
	}
	if res.StatusCode != http.StatusOK {
		return checkUnavailable
	}
	mediaType, _, err := mime.ParseMediaType(res.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return checkUnavailable
	}
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		return checkUnavailable
	}
	var answer federation.CheckResponse
	if err := federation.Decode(raw, &answer); err != nil {
		return checkUnavailable
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return checkUnavailable
	}
	// 構造体のゼロ値では、項目の欠落やnullと、明示の false を区別できない。
	// セッションを消してよいのは、active:false の1項目だけの応答です。
	if len(fields) == 1 && string(fields["active"]) == "false" {
		return checkInactive
	}
	if string(fields["active"]) != "true" {
		return checkUnavailable
	}
	if answer.Issuer != u.issuer || answer.Subject != subject || answer.ClientID != u.clientID || answer.Auth == nil || *answer.Auth != epochs {
		return checkUnavailable
	}
	return checkActive
}

func (u *upstream) pause() {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.pauseEnd = u.now().Add(breakerPause)
}

// keySet は、認証基盤の公開鍵を持つ go-oidc の KeySet です。未知の鍵の取得は同時1件で、成功の後は30秒あけます。
type keySet struct {
	url    string
	client *http.Client
	now    func() time.Time
	mu     sync.Mutex
	keys   []jose.JSONWebKey
	nextAt time.Time
}

// VerifySignature は、ES256の署名を検証して中身を返します。
func (k *keySet) VerifySignature(ctx context.Context, jwt string) ([]byte, error) {
	sig, err := jose.ParseSigned(jwt, []jose.SignatureAlgorithm{jose.ES256})
	if err != nil || len(sig.Signatures) != 1 {
		return nil, errIdentity
	}
	key, ok := k.find(ctx, sig.Signatures[0].Header.KeyID)
	if !ok {
		return nil, errIdentity
	}
	return sig.Verify(key)
}

func (k *keySet) find(ctx context.Context, id string) (*jose.JSONWebKey, bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if key, ok := k.lookup(id); ok {
		return key, true
	}
	if k.now().Before(k.nextAt) {
		return nil, false
	}
	keys, err := k.fetch(ctx)
	if err != nil {
		k.nextAt = k.now().Add(breakerPause)
		return nil, false
	}
	k.keys = keys
	k.nextAt = k.now().Add(keyRefetchGap)
	return k.lookup(id)
}

func (k *keySet) lookup(id string) (*jose.JSONWebKey, bool) {
	for i := range k.keys {
		if k.keys[i].KeyID == id && id != "" {
			return &k.keys[i], true
		}
	}
	return nil, false
}

func (k *keySet) fetch(ctx context.Context) ([]jose.JSONWebKey, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, k.url, nil)
	if err != nil {
		return nil, err
	}
	res, err := k.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, errIdentity
	}
	var set jose.JSONWebKeySet
	if err := json.NewDecoder(res.Body).Decode(&set); err != nil {
		return nil, err
	}
	return set.Keys, nil
}
