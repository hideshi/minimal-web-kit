package oidcop

// SDKのStorageの実装です。認可要求の作成は oidcop.go の authorize が行い、SDKの広い処理には任せません。

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/zitadel/oidc/v3/pkg/oidc"
	"github.com/zitadel/oidc/v3/pkg/op"
	"minimal-web-kit/features/auth"
	"minimal-web-kit/internal/federation"
)

type request struct{ auth.FederationRequest }

func (r request) GetID() string          { return r.ID }
func (r request) GetACR() string         { return "" }
func (r request) GetAMR() []string       { return []string{"hwk", "user"} }
func (r request) GetAudience() []string  { return []string{r.Client} }
func (r request) GetAuthTime() time.Time { return time.Unix(r.AuthTime, 0) }
func (r request) GetClientID() string    { return r.Client }
func (r request) GetCodeChallenge() *oidc.CodeChallenge {
	return &oidc.CodeChallenge{Challenge: r.Challenge, Method: oidc.CodeChallengeMethodS256}
}
func (r request) GetNonce() string                   { return r.Nonce }
func (r request) GetRedirectURI() string             { return r.Redirect }
func (r request) GetResponseType() oidc.ResponseType { return oidc.ResponseTypeCode }
func (r request) GetResponseMode() oidc.ResponseMode { return oidc.ResponseModeQuery }
func (r request) GetScopes() []string                { return []string{oidc.ScopeOpenID} }
func (r request) GetState() string                   { return r.State }
func (r request) GetSubject() string                 { return r.Subject }
func (r request) Done() bool                         { return r.Subject != "" }

type client struct{ auth.FederationClient }

func (c client) GetID() string                       { return c.ID }
func (c client) RedirectURIs() []string              { return []string{c.Redirect} }
func (c client) PostLogoutRedirectURIs() []string    { return nil }
func (c client) ApplicationType() op.ApplicationType { return op.ApplicationTypeWeb }
func (c client) AuthMethod() oidc.AuthMethod         { return oidc.AuthMethodBasic }
func (c client) ResponseTypes() []oidc.ResponseType {
	return []oidc.ResponseType{oidc.ResponseTypeCode}
}
func (c client) GrantTypes() []oidc.GrantType        { return []oidc.GrantType{oidc.GrantTypeCode} }
func (c client) LoginURL(string) string              { return "/admin/oidc/continue" }
func (c client) AccessTokenType() op.AccessTokenType { return op.AccessTokenTypeBearer }
func (c client) IDTokenLifetime() time.Duration      { return 5 * time.Minute }
func (c client) DevMode() bool                       { return false }
func (c client) RestrictAdditionalIdTokenScopes() func([]string) []string {
	return func(s []string) []string { return s }
}
func (c client) RestrictAdditionalAccessTokenScopes() func([]string) []string {
	return func([]string) []string { return nil }
}
func (c client) IsScopeAllowed(s string) bool         { return s == oidc.ScopeOpenID }
func (c client) IDTokenUserinfoClaimsAssertion() bool { return true }
func (c client) ClockSkew() time.Duration             { return 0 }

type signingKey struct {
	key *ecdsa.PrivateKey
	id  string
}

func (k signingKey) SignatureAlgorithm() jose.SignatureAlgorithm { return jose.ES256 }
func (k signingKey) Key() any                                    { return k.key }
func (k signingKey) ID() string                                  { return k.id }

type publicKey struct{ jose.JSONWebKey }

func (k publicKey) ID() string { return k.KeyID }
func (k publicKey) Algorithm() jose.SignatureAlgorithm {
	return jose.SignatureAlgorithm(k.JSONWebKey.Algorithm)
}
func (k publicKey) Use() string { return "sig" }
func (k publicKey) Key() any    { return k.JSONWebKey.Key }

type exchangeKey struct{}

type storage struct {
	store   *auth.FederationStore
	signing signingKey
	public  []op.Key
}

func (s *storage) CreateAuthRequest(ctx context.Context, q *oidc.AuthRequest, _ string) (op.AuthRequest, error) {
	return nil, errors.New("explicit flow creation required")
}
func (s *storage) AuthRequestByID(ctx context.Context, id string) (op.AuthRequest, error) {
	r, err := s.store.Request(ctx, id)
	return request{r}, err
}
func (s *storage) AuthRequestByCode(ctx context.Context, code string) (op.AuthRequest, error) {
	r, ok := ctx.Value(exchangeKey{}).(request)
	if !ok {
		return nil, auth.ErrFederationInvalid
	}
	return r, nil
}
func (s *storage) SaveAuthCode(ctx context.Context, id, code string) error {
	return s.store.SaveCode(ctx, id, code)
}

// DeleteAuthRequest は何もしません。再利用の検出に要る、消費済みの記録を残します。
func (s *storage) DeleteAuthRequest(context.Context, string) error { return nil }

// CreateAccessToken は保存しないランダムなIDを返します。これを受け付ける経路はありません。
func (s *storage) CreateAccessToken(ctx context.Context, r op.TokenRequest) (string, time.Time, error) {
	if _, ok := ctx.Value(exchangeKey{}).(request); !ok {
		return "", time.Time{}, auth.ErrFederationInvalid
	}
	return federation.Token(), time.Now().Add(5 * time.Minute), nil
}
func (s *storage) CreateAccessAndRefreshTokens(context.Context, op.TokenRequest, string) (string, string, time.Time, error) {
	return "", "", time.Time{}, auth.ErrFederationInvalid
}
func (s *storage) TokenRequestByRefreshToken(context.Context, string) (op.RefreshTokenRequest, error) {
	return nil, auth.ErrFederationInvalid
}
func (s *storage) TerminateSession(context.Context, string, string) error {
	return auth.ErrFederationInvalid
}
func (s *storage) RevokeToken(context.Context, string, string, string) *oidc.Error {
	return oidc.ErrInvalidRequest()
}
func (s *storage) GetRefreshTokenInfo(context.Context, string, string) (string, string, error) {
	return "", "", op.ErrInvalidRefreshToken
}
func (s *storage) SigningKey(context.Context) (op.SigningKey, error) { return s.signing, nil }
func (s *storage) SignatureAlgorithms(context.Context) ([]jose.SignatureAlgorithm, error) {
	return []jose.SignatureAlgorithm{jose.ES256}, nil
}
func (s *storage) KeySet(context.Context) ([]op.Key, error) { return s.public, nil }
func (s *storage) GetClientByClientID(ctx context.Context, id string) (op.Client, error) {
	c, err := s.store.LoginClient(ctx, id)
	return client{c}, err
}
func (s *storage) AuthorizeClientIDSecret(ctx context.Context, id, secret string) error {
	return s.store.Authenticate(ctx, id, secret)
}
func (s *storage) SetUserinfoFromScopes(context.Context, *oidc.UserInfo, string, string, []string) error {
	return nil
}
func (s *storage) SetUserinfoFromRequest(ctx context.Context, u *oidc.UserInfo, r op.IDTokenRequest, _ []string) error {
	q, ok := r.(request)
	if !ok {
		return auth.ErrFederationInvalid
	}
	// SDKは、この値でIDトークンのclaimを上書きするため、subも入れる
	u.Subject = q.Subject
	u.AppendClaims(federation.ClaimName, q.Epochs)
	return nil
}
func (s *storage) SetUserinfoFromToken(context.Context, *oidc.UserInfo, string, string, string) error {
	return auth.ErrFederationInvalid
}
func (s *storage) SetIntrospectionFromToken(context.Context, *oidc.IntrospectionResponse, string, string, string) error {
	return auth.ErrFederationInvalid
}
func (s *storage) GetPrivateClaimsFromScopes(context.Context, string, string, []string) (map[string]any, error) {
	return nil, auth.ErrFederationInvalid
}
func (s *storage) GetKeyByIDAndClientID(context.Context, string, string) (*jose.JSONWebKey, error) {
	return nil, auth.ErrFederationInvalid
}
func (s *storage) ValidateJWTProfileScopes(context.Context, string, []string) ([]string, error) {
	return nil, auth.ErrFederationInvalid
}
func (s *storage) Health(context.Context) error { return nil }
