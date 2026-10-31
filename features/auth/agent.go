package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"net/url"
	"strings"
	"time"

	"minimal-web-kit/features"
	"minimal-web-kit/internal/federation"
)

// エージェント（MCP等）のOAuthの保存です。アカウント・利用許可・世代は共通認証の表をそのまま使い、
// OIDCのログインの要求とコードには触れません。OIDCのライブラリに依存しません。

const (
	agentRequestTTL = 5 * time.Minute
	agentCodeTTL    = time.Minute
	// agentTokenTTL は固定の期限です。更新の手段は持たず、切れたら利用者が認可し直します。
	agentTokenTTL = 8 * time.Hour
	// 未完了の認可要求と、1つの利用許可で同時に持てるトークンの上限。
	agentMaxRequests = 256
	agentMaxTokens   = 16
)

// AgentRequest は、エージェントの認可要求です。state はログに出しません。
type AgentRequest struct {
	ID, Client, Redirect, State, Challenge, Resource, Scope string
}

// loopbackHost は、公開クライアントの戻り先に許すホストかを返します。
func loopbackHost(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

// validAgentRedirect は、登録する戻り先を検査します。ループバックのHTTPで、ポートは書きません。
func validAgentRedirect(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && len(raw) <= 2048 && u.Scheme == "http" && loopbackHost(u.Hostname()) && u.Port() == "" &&
		u.User == nil && u.RawQuery == "" && u.Fragment == "" && strings.HasPrefix(u.Path, "/")
}

// agentRedirectMatches は、要求の戻り先が登録と同じスキーム・ホスト・パスかを確かめます。ポートだけ可変です（RFC 8252）。
func agentRedirectMatches(registered, requested string) bool {
	r, err := url.Parse(registered)
	if err != nil {
		return false
	}
	q, err := url.Parse(requested)
	if err != nil || len(requested) > 2048 {
		return false
	}
	return q.Scheme == r.Scheme && q.Hostname() == r.Hostname() && q.Path == r.Path && q.Port() != "" &&
		q.User == nil && q.RawQuery == "" && q.Fragment == "" && q.Opaque == ""
}

// CreateAgentClient は、秘密値を持たない公開クライアントを登録します。種類は登録で決まり、要求の内容では変わりません。
func (s *FederationStore) CreateAgentClient(ctx context.Context, label, redirect string) (FederationClient, error) {
	return s.addAgentClient(ctx, label, redirect, 0)
}

// addAgentClient は、owner が0でなければ、同じトランザクションでそのアカウントに利用を許可します。
func (s *FederationStore) addAgentClient(ctx context.Context, label, redirect string, owner int64) (FederationClient, error) {
	if !validAgentRedirect(redirect) || label == "" || len(label) > 100 {
		return FederationClient{}, ErrInvalidInput
	}
	id, err := newToken()
	if err != nil {
		return FederationClient{}, err
	}
	err = s.Atomic(ctx, func(ctx context.Context) error {
		x := s.executor(ctx)
		if _, err := x.ExecContext(ctx, "INSERT INTO auth_oidc_clients (client_id, label, redirect_uri) VALUES (?, ?, ?)", id, label, redirect); err != nil {
			return err
		}
		if _, err := x.ExecContext(ctx, "INSERT INTO auth_agent_clients (client_id) VALUES (?)", id); err != nil {
			return err
		}
		if owner == 0 {
			return nil
		}
		_, err := s.grantIn(ctx, id, owner)
		return err
	})
	return FederationClient{id, label, redirect}, err
}

// AgentClients は、公開クライアントを停止中も含めて返します。OIDCの連携先は含みません。
func (s *FederationStore) AgentClients(ctx context.Context) ([]features.AgentClient, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT c.client_id, c.label, c.redirect_uri, c.enabled FROM auth_oidc_clients c
		 JOIN auth_agent_clients a USING (client_id) ORDER BY c.label, c.client_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []features.AgentClient{}
	for rows.Next() {
		var c features.AgentClient
		if err := rows.Scan(&c.ID, &c.Label, &c.Redirect, &c.Enabled); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	for i := range out {
		if out[i].Accounts, err = s.agentAccounts(ctx, out[i].ID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *FederationStore) agentAccounts(ctx context.Context, id string) ([]features.AgentAccount, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT a.id, a.name, COALESCE(g.allowed, 0) FROM auth_accounts a
		 LEFT JOIN auth_federated_subjects f ON f.account_id = a.id
		 LEFT JOIN auth_oidc_grants g ON g.sub = f.sub AND g.client_id = ?
		 WHERE a.disabled_at IS NULL ORDER BY a.id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []features.AgentAccount
	for rows.Next() {
		var v features.AgentAccount
		if err := rows.Scan(&v.ID, &v.Name, &v.Allowed); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// AgentClientDetail は、公開クライアント1件を、停止中も含めて返します。無ければ features.ErrAgentNotFound です。
func (s *FederationStore) AgentClientDetail(ctx context.Context, id string) (features.AgentClient, error) {
	var c features.AgentClient
	err := s.executor(ctx).QueryRowContext(ctx,
		`SELECT c.client_id, c.label, c.redirect_uri, c.enabled FROM auth_oidc_clients c
		 JOIN auth_agent_clients a USING (client_id) WHERE c.client_id = ?`, id).Scan(&c.ID, &c.Label, &c.Redirect, &c.Enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return c, features.ErrAgentNotFound
	} else if err != nil {
		return c, err
	}
	c.Accounts, err = s.agentAccounts(ctx, id)
	return c, err
}

// AgentClient は、有効な公開クライアントを返します。OIDCの連携先は返しません。無ければ sql.ErrNoRows です。
func (s *FederationStore) AgentClient(ctx context.Context, id string) (FederationClient, error) {
	var c FederationClient
	err := s.executor(ctx).QueryRowContext(ctx,
		`SELECT c.client_id, c.label, c.redirect_uri FROM auth_oidc_clients c
		 JOIN auth_agent_clients a USING (client_id) WHERE c.client_id = ? AND c.enabled = 1`, id,
	).Scan(&c.ID, &c.Label, &c.Redirect)
	return c, err
}

// NewAgentRequest は、未認証の認可要求を保存します。
func (s *FederationStore) NewAgentRequest(ctx context.Context, flowToken string, r AgentRequest) error {
	return s.Atomic(ctx, func(ctx context.Context) error {
		x := s.executor(ctx)
		now := s.now()
		if _, err := x.ExecContext(ctx, "DELETE FROM auth_agent_requests WHERE consumed_at = 0 AND expires_at <= ?", now.Unix()); err != nil {
			return err
		}
		var n int
		if err := x.QueryRowContext(ctx, "SELECT COUNT(*) FROM auth_agent_requests WHERE consumed_at = 0").Scan(&n); err != nil {
			return err
		}
		if n >= agentMaxRequests {
			return ErrFederationBusy
		}
		_, err := x.ExecContext(ctx,
			`INSERT INTO auth_agent_requests (request_id, flow_hash, client_id, redirect_uri, state, challenge, resource, scope, expires_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			r.ID, hashToken(flowToken), r.Client, r.Redirect, r.State, r.Challenge, r.Resource, r.Scope, now.Add(agentRequestTTL).Unix())
		return err
	})
}

// AgentFlow は、ブラウザの手続きのCookieに対応する、進行中の要求を返します。
func (s *FederationStore) AgentFlow(ctx context.Context, flowToken string) (AgentRequest, error) {
	var r AgentRequest
	err := s.executor(ctx).QueryRowContext(ctx,
		`SELECT request_id, client_id, redirect_uri, state, challenge, resource, scope FROM auth_agent_requests
		 WHERE flow_hash = ? AND expires_at > ? AND consumed_at = 0`,
		hashToken(flowToken), s.now().Unix()).Scan(&r.ID, &r.Client, &r.Redirect, &r.State, &r.Challenge, &r.Resource, &r.Scope)
	return r, err
}

// RemoveAgentFlow は、ブラウザの進行中の要求を消します。
func (s *FederationStore) RemoveAgentFlow(ctx context.Context, flowToken string) error {
	_, err := s.executor(ctx).ExecContext(ctx, "DELETE FROM auth_agent_requests WHERE flow_hash = ? AND consumed_at = 0", hashToken(flowToken))
	return err
}

// ApproveAgent は、管理者セッションの本人を要求に結びつけ、世代を固定して、一回限りのコードを返します。
// 二度押しでは新しいコードだけが使えます。停止中・許可なしなら sql.ErrNoRows です。
func (s *FederationStore) ApproveAgent(ctx context.Context, requestID, sessionToken string) (string, error) {
	code, err := newToken()
	if err != nil {
		return "", err
	}
	err = s.Atomic(ctx, func(ctx context.Context) error {
		x := s.executor(ctx)
		now := s.now()
		var client string
		if err := x.QueryRowContext(ctx, "SELECT client_id FROM auth_agent_requests WHERE request_id = ?", requestID).Scan(&client); err != nil {
			return err
		}
		var subject string
		err := x.QueryRowContext(ctx,
			`SELECT f.sub FROM auth_sessions t
			 JOIN auth_accounts a ON a.id = t.account_id
			 JOIN auth_federated_subjects f ON f.account_id = a.id
			 WHERE t.token_hash = ? AND a.disabled_at IS NULL AND t.expires_at > ? AND t.last_seen_at > ?`,
			hashToken(sessionToken), now.Unix(), now.Add(-adminSessionIdle).Unix()).Scan(&subject)
		if err != nil {
			return err
		}
		epochs, err := s.Epochs(ctx, client, subject)
		if err != nil {
			return err
		}
		res, err := x.ExecContext(ctx,
			`UPDATE auth_agent_requests SET sub = ?, account_epoch = ?, client_epoch = ?, grant_epoch = ?, code_hash = ?, code_expires_at = ?,
			 expires_at = MAX(expires_at, ?)
			 WHERE request_id = ? AND expires_at > ? AND consumed_at = 0`,
			subject, epochs.Account, epochs.Client, epochs.Grant, hashToken(code), now.Add(agentCodeTTL).Unix(), now.Add(agentCodeTTL).Unix(), requestID, now.Unix())
		return affectedOne(res, err, ErrFederationInvalid)
	})
	return code, err
}

// AgentGrant は、発行したトークンの内容です。
type AgentGrant struct {
	Token, Scope string
	ExpiresIn    int64
}

// ExchangeAgentCode は、コードをアクセストークンに換えます。クライアント・戻り先・resource・PKCEがすべて一致してはじめて、
// コードの状態を見ます。公開クライアントの client_id は秘密ではないので、不一致の要求では何も変えません。
// 一致したうえでの再利用は、そのコードから発行したトークンを取り消し、利用許可の世代は進めません（AUTH-INV-22）。
func (s *FederationStore) ExchangeAgentCode(ctx context.Context, clientID, code, redirect, verifier, resource string) (AgentGrant, error) {
	var grant AgentGrant
	token, err := newToken()
	if err != nil {
		return grant, err
	}
	err = s.Atomic(ctx, func(ctx context.Context) error {
		x := s.executor(ctx)
		var id, client, stored, challenge, res, scope, subject string
		var e federation.Epochs
		var codeExpires, consumed int64
		err := x.QueryRowContext(ctx,
			`SELECT request_id, client_id, redirect_uri, challenge, resource, scope, sub, account_epoch, client_epoch, grant_epoch, code_expires_at, consumed_at
			 FROM auth_agent_requests WHERE code_hash = ?`, hashToken(code)).
			Scan(&id, &client, &stored, &challenge, &res, &scope, &subject, &e.Account, &e.Client, &e.Grant, &codeExpires, &consumed)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrFederationInvalid
		} else if err != nil {
			return err
		}
		sum := sha256.Sum256([]byte(verifier))
		computed := base64.RawURLEncoding.EncodeToString(sum[:])
		if len(verifier) < 43 || len(verifier) > 128 || client != clientID || stored != redirect || res != resource ||
			subtle.ConstantTimeCompare([]byte(computed), []byte(challenge)) != 1 {
			return ErrFederationInvalid
		}
		now := s.now()
		if consumed > 0 {
			if _, err := x.ExecContext(ctx, "DELETE FROM auth_agent_tokens WHERE request_id = ?", id); err != nil {
				return err
			}
			return ErrFederationReplay
		}
		if codeExpires <= now.Unix() {
			return ErrFederationInvalid
		}
		epochs, err := s.Epochs(ctx, clientID, subject)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && epochs != e) {
			return ErrFederationInvalid
		} else if err != nil {
			return err
		}
		if _, err := x.ExecContext(ctx, "DELETE FROM auth_agent_tokens WHERE client_id = ? AND sub = ? AND expires_at <= ?", clientID, subject, now.Unix()); err != nil {
			return err
		}
		var held int
		if err := x.QueryRowContext(ctx, "SELECT COUNT(*) FROM auth_agent_tokens WHERE client_id = ? AND sub = ?", clientID, subject).Scan(&held); err != nil {
			return err
		}
		if held >= agentMaxTokens {
			return ErrFederationBusy
		}
		// 消費の記録は、再利用を検出できるよう、トークンの期限まで残す
		update, err := x.ExecContext(ctx,
			"UPDATE auth_agent_requests SET consumed_at = ?, expires_at = ? WHERE request_id = ? AND consumed_at = 0",
			now.Unix(), now.Add(agentTokenTTL).Unix(), id)
		if err := affectedOne(update, err, ErrFederationInvalid); err != nil {
			return err
		}
		if _, err := x.ExecContext(ctx,
			`INSERT INTO auth_agent_tokens (token_hash, request_id, client_id, sub, resource, scope, account_epoch, client_epoch, grant_epoch, created_at, expires_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			hashToken(token), id, clientID, subject, res, scope, e.Account, e.Client, e.Grant, now.Unix(), now.Add(agentTokenTTL).Unix()); err != nil {
			return err
		}
		grant = AgentGrant{Token: token, Scope: scope, ExpiresIn: int64(agentTokenTTL.Seconds())}
		return nil
	})
	if err != nil {
		return AgentGrant{}, err
	}
	return grant, nil
}

// VerifyAgentToken は、トークンを要求ごとに確かめます。期限・resource・固定した世代のどれかが違えば
// features.ErrBearerInvalid を返します。ほかのエラーは確認不能で、呼び出し側は実行してはいけません。
func (s *FederationStore) VerifyAgentToken(ctx context.Context, token, resource string) (features.AgentPrincipal, error) {
	var p features.AgentPrincipal
	if token == "" || len(token) > 256 {
		return p, features.ErrBearerInvalid
	}
	x := s.executor(ctx)
	var res, scope, subject string
	var e federation.Epochs
	var expires int64
	err := x.QueryRowContext(ctx,
		`SELECT client_id, sub, resource, scope, account_epoch, client_epoch, grant_epoch, expires_at FROM auth_agent_tokens WHERE token_hash = ?`,
		hashToken(token)).Scan(&p.Client, &subject, &res, &scope, &e.Account, &e.Client, &e.Grant, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return features.AgentPrincipal{}, features.ErrBearerInvalid
	} else if err != nil {
		return features.AgentPrincipal{}, err
	}
	if expires <= s.now().Unix() || res != resource {
		return features.AgentPrincipal{}, features.ErrBearerInvalid
	}
	epochs, err := s.Epochs(ctx, p.Client, subject)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && epochs != e) {
		return features.AgentPrincipal{}, features.ErrBearerInvalid
	} else if err != nil {
		return features.AgentPrincipal{}, err
	}
	var role string
	err = x.QueryRowContext(ctx,
		`SELECT a.id, a.role FROM auth_federated_subjects f JOIN auth_accounts a ON a.id = f.account_id WHERE f.sub = ?`, subject).Scan(&p.AccountID, &role)
	if errors.Is(err, sql.ErrNoRows) {
		return features.AgentPrincipal{}, features.ErrBearerInvalid
	} else if err != nil {
		return features.AgentPrincipal{}, err
	}
	p.Role = features.Role(role)
	p.Scopes = strings.Fields(scope)
	p.Generation = e.Account + "." + e.Client + "." + e.Grant
	return p, nil
}

// RevokeAgentToken は、呼び出し元が持つトークンを取り消します（テストと運用の口）。
func (s *FederationStore) RevokeAgentToken(ctx context.Context, token string) error {
	_, err := s.executor(ctx).ExecContext(ctx, "DELETE FROM auth_agent_tokens WHERE token_hash = ?", hashToken(token))
	return err
}

// purgeAgent は、期限切れの要求とトークンを消します。
func purgeAgent(ctx context.Context, db *sql.DB, now time.Time) error {
	for _, q := range []string{"DELETE FROM auth_agent_requests WHERE expires_at <= ?", "DELETE FROM auth_agent_tokens WHERE expires_at <= ?"} {
		if _, err := db.ExecContext(ctx, q, now.Unix()); err != nil {
			return err
		}
	}
	return nil
}
