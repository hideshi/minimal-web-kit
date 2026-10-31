package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"minimal-web-kit/internal/federation"
)

// 共通認証の提供側の保存と失効です。OIDCのライブラリに依存せず、通常の構成でも停止等の失効を続けます。
// プロトコルの処理は features/auth/oidcop に置きます。

const (
	// 認可要求の寿命とコードの寿命。
	federationRequestTTL = 5 * time.Minute
	federationCodeTTL    = time.Minute
	// 交換済みコードの記録と、削除済みアカウントの墓標を残す長さ（最長のセッション寿命と時計のずれ）。
	federationTombstoneTTL = adminSessionMax + 2*time.Minute
	// 秘密値の更新の猶予。
	federationSecretGrace = 24 * time.Hour
	// 同時に進行できる未完了の認可要求の上限。
	federationMaxRequests = 1024
	// 現用の秘密値の期限（期限なしを表す）。
	federationSecretCurrent = int64(1 << 62)
)

var (
	// ErrFederationInvalid は、連携の要求を受け付けないことを表します。理由は外へ区別しません。
	ErrFederationInvalid = errors.New("auth: federation rejected")
	// ErrFederationReplay は、認証済みのクライアントが交換済みのコードを送ったことを表します。失効はコミットします。
	ErrFederationReplay = errors.New("auth: authenticated code reuse")
	// ErrFederationBusy は、未完了の認可要求が上限に達していることを表します。
	ErrFederationBusy = errors.New("auth: too many federation requests")
)

// FederationClient は連携先の登録です。
type FederationClient struct{ ID, Label, Redirect string }

// FederationRequest は、認可要求とそのコードの状態です。state・nonceはログに出しません。
type FederationRequest struct {
	ID, Client, Redirect, State, Nonce, Challenge, Subject string
	federation.Epochs
	AuthTime, Expires, CodeExpires, Consumed int64
}

// LogValue は、要求の中身をログに出さないようにします。
func (FederationRequest) LogValue() slog.Value { return slog.StringValue("[redacted]") }

// FederationStore は連携の表を読み書きします。連携先が使うトークンの交換は、Atomic の中で行います。
type FederationStore struct {
	db  *sql.DB
	now func() time.Time
}

// NewFederationStore は、マイグレーション適用済みのDBの保存処理を返します。
func NewFederationStore(db *sql.DB) *FederationStore {
	return &FederationStore{db: db, now: time.Now}
}

type federationConnKey struct{}

func (s *FederationStore) executor(ctx context.Context) execer {
	if c, ok := ctx.Value(federationConnKey{}).(*sql.Conn); ok {
		return c
	}
	return s.db
}

// Atomic は、専用の接続で BEGIN IMMEDIATE を発行し、fn の保存処理を1つのトランザクションにします。
// ctx を渡した保存処理は同じ接続を使います。ErrFederationReplay は意図した失効なので、エラーでもコミットします。
// 保持の間、他の書き込みは最大で busy_timeout（5秒）待ちます。
func (s *FederationStore) Atomic(ctx context.Context, fn func(ctx context.Context) error) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_, _ = conn.ExecContext(cleanup, "ROLLBACK")
	}()
	fnErr := fn(context.WithValue(ctx, federationConnKey{}, conn))
	if fnErr != nil && !errors.Is(fnErr, ErrFederationReplay) {
		return fnErr
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return err
	}
	committed = true
	return fnErr
}

// Client は有効な連携先を返します。無ければ sql.ErrNoRows です。
func (s *FederationStore) Client(ctx context.Context, id string) (FederationClient, error) {
	var c FederationClient
	err := s.executor(ctx).QueryRowContext(ctx,
		"SELECT client_id, label, redirect_uri FROM auth_oidc_clients WHERE client_id = ? AND enabled = 1", id,
	).Scan(&c.ID, &c.Label, &c.Redirect)
	return c, err
}

// LoginClient は、OIDCのログインに使える連携先を返します。エージェントの公開クライアントは含めません（AUTH-INV-24）。
func (s *FederationStore) LoginClient(ctx context.Context, id string) (FederationClient, error) {
	var c FederationClient
	err := s.executor(ctx).QueryRowContext(ctx,
		`SELECT client_id, label, redirect_uri FROM auth_oidc_clients c WHERE client_id = ? AND enabled = 1
		 AND NOT EXISTS (SELECT 1 FROM auth_agent_clients a WHERE a.client_id = c.client_id)`, id,
	).Scan(&c.ID, &c.Label, &c.Redirect)
	return c, err
}

// Authenticate は連携先の資格情報を確かめます。秘密値は256ビットの乱数で、ハッシュで照合します。
func (s *FederationStore) Authenticate(ctx context.Context, id, secret string) error {
	if len(id) > 128 || len(secret) > 256 || secret == "" {
		return ErrFederationInvalid
	}
	var n int
	err := s.executor(ctx).QueryRowContext(ctx,
		`SELECT COUNT(*) FROM auth_oidc_client_secrets k JOIN auth_oidc_clients c USING (client_id)
		 WHERE k.client_id = ? AND c.enabled = 1 AND k.secret_hash = ? AND k.expires_at > ?`,
		id, hashToken(secret), s.now().Unix()).Scan(&n)
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrFederationInvalid
	}
	return nil
}

// CreateClient は連携先を登録し、1度だけ表示する秘密値を返します。戻り先はHTTPSで、クエリと断片を持てません。
func (s *FederationStore) CreateClient(ctx context.Context, label, redirect string) (FederationClient, string, error) {
	u, err := url.Parse(redirect)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		len(redirect) > 2048 || label == "" || len(label) > 100 {
		return FederationClient{}, "", ErrInvalidInput
	}
	id, err := newToken()
	if err != nil {
		return FederationClient{}, "", err
	}
	secret, err := newToken()
	if err != nil {
		return FederationClient{}, "", err
	}
	err = s.Atomic(ctx, func(ctx context.Context) error {
		x := s.executor(ctx)
		if _, err := x.ExecContext(ctx, "INSERT INTO auth_oidc_clients (client_id, label, redirect_uri) VALUES (?, ?, ?)", id, label, redirect); err != nil {
			return err
		}
		_, err := x.ExecContext(ctx, "INSERT INTO auth_oidc_client_secrets (client_id, secret_hash, expires_at) VALUES (?, ?, ?)", id, hashToken(secret), federationSecretCurrent)
		return err
	})
	return FederationClient{id, label, redirect}, secret, err
}

// SetClientEnabled は連携先を停止・再開します。どちらでも世代を進め、古いログインを復活させません。
func (s *FederationStore) SetClientEnabled(ctx context.Context, id string, enabled bool) error {
	return s.Atomic(ctx, func(ctx context.Context) error {
		x := s.executor(ctx)
		res, err := x.ExecContext(ctx, "UPDATE auth_oidc_clients SET enabled = ?, client_epoch = client_epoch + 1 WHERE client_id = ?", enabled, id)
		if err := affectedOne(res, err, ErrNotFound); err != nil {
			return err
		}
		for _, q := range []string{"DELETE FROM auth_oidc_requests WHERE client_id = ?", "DELETE FROM auth_agent_requests WHERE client_id = ?", "DELETE FROM auth_agent_tokens WHERE client_id = ?"} {
			if _, err := x.ExecContext(ctx, q, id); err != nil {
				return err
			}
		}
		return nil
	})
}

// RotateSecret は秘密値を更新し、新しい値を返します。通常は旧値を24時間だけ併用します。
// compromised なら旧値を直ちに無効にし、世代を進めて既存のログインも失効させます。
func (s *FederationStore) RotateSecret(ctx context.Context, id string, compromised bool) (string, error) {
	secret, err := newToken()
	if err != nil {
		return "", err
	}
	err = s.Atomic(ctx, func(ctx context.Context) error {
		x := s.executor(ctx)
		if _, err := s.Client(ctx, id); errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		queries := [][]any{
			{"DELETE FROM auth_oidc_client_secrets WHERE client_id = ? AND expires_at < ?", id, federationSecretCurrent},
			{"UPDATE auth_oidc_client_secrets SET expires_at = ? WHERE client_id = ?", s.now().Add(federationSecretGrace).Unix(), id},
		}
		if compromised {
			queries = [][]any{
				{"DELETE FROM auth_oidc_client_secrets WHERE client_id = ?", id},
				{"UPDATE auth_oidc_clients SET client_epoch = client_epoch + 1 WHERE client_id = ?", id},
				{"DELETE FROM auth_oidc_requests WHERE client_id = ?", id},
			}
		}
		for _, q := range queries {
			if _, err := x.ExecContext(ctx, q[0].(string), q[1:]...); err != nil {
				return err
			}
		}
		_, err := x.ExecContext(ctx, "INSERT INTO auth_oidc_client_secrets (client_id, secret_hash, expires_at) VALUES (?, ?, ?)", id, hashToken(secret), federationSecretCurrent)
		return err
	})
	return secret, err
}

// RevokeAll は、署名鍵の漏洩やバックアップからの巻き戻しのときに、全連携先の世代を進めて既存のログインを失効させます。
func (s *FederationStore) RevokeAll(ctx context.Context) error {
	return s.Atomic(ctx, func(ctx context.Context) error {
		x := s.executor(ctx)
		if _, err := x.ExecContext(ctx, "UPDATE auth_oidc_clients SET client_epoch = client_epoch + 1"); err != nil {
			return err
		}
		for _, q := range []string{"DELETE FROM auth_oidc_requests", "DELETE FROM auth_agent_requests", "DELETE FROM auth_agent_tokens"} {
			if _, err := x.ExecContext(ctx, q); err != nil {
				return err
			}
		}
		return nil
	})
}

// Grant は、アカウントに連携先の利用を許可し、そのアカウントの識別子（sub）を返します。識別子は初回に作ります。
// 取り消し済みの許可を戻すときは世代を進め、古いログインを復活させません。
func (s *FederationStore) Grant(ctx context.Context, clientID string, accountID int64) (string, error) {
	var subject string
	err := s.Atomic(ctx, func(ctx context.Context) error {
		var err error
		subject, err = s.grantIn(ctx, clientID, accountID)
		return err
	})
	return subject, err
}

// grantIn は Atomic の中で利用を許可します。
func (s *FederationStore) grantIn(ctx context.Context, clientID string, accountID int64) (string, error) {
	var subject string
	err := func() error {
		x := s.executor(ctx)
		if _, err := s.Client(ctx, clientID); errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		var active int
		if err := x.QueryRowContext(ctx, "SELECT COUNT(*) FROM auth_accounts WHERE id = ? AND disabled_at IS NULL", accountID).Scan(&active); err != nil {
			return err
		}
		if active != 1 {
			return ErrNotFound
		}
		candidate, err := newToken()
		if err != nil {
			return err
		}
		if _, err := x.ExecContext(ctx, "INSERT OR IGNORE INTO auth_federated_subjects (sub, account_id) VALUES (?, ?)", candidate, accountID); err != nil {
			return err
		}
		if err := x.QueryRowContext(ctx, "SELECT sub FROM auth_federated_subjects WHERE account_id = ?", accountID).Scan(&subject); err != nil {
			return err
		}
		_, err = x.ExecContext(ctx,
			`INSERT INTO auth_oidc_grants (client_id, sub) VALUES (?, ?)
			 ON CONFLICT (client_id, sub) DO UPDATE SET allowed = 1, grant_epoch = grant_epoch + 1 WHERE allowed = 0`,
			clientID, subject)
		return err
	}()
	return subject, err
}

// Revoke は利用許可を取り消します。行は世代のために残し、進行中の要求は消します。
func (s *FederationStore) Revoke(ctx context.Context, clientID string, accountID int64) error {
	return s.Atomic(ctx, func(ctx context.Context) error {
		x := s.executor(ctx)
		var subject string
		err := x.QueryRowContext(ctx, "SELECT sub FROM auth_federated_subjects WHERE account_id = ?", accountID).Scan(&subject)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		res, err := x.ExecContext(ctx, "UPDATE auth_oidc_grants SET allowed = 0, grant_epoch = grant_epoch + 1 WHERE client_id = ? AND sub = ? AND allowed = 1", clientID, subject)
		if err := affectedOne(res, err, ErrNotFound); err != nil {
			return err
		}
		for _, q := range []string{"DELETE FROM auth_oidc_requests WHERE client_id = ? AND sub = ?", "DELETE FROM auth_agent_requests WHERE client_id = ? AND sub = ?", "DELETE FROM auth_agent_tokens WHERE client_id = ? AND sub = ?"} {
			if _, err := x.ExecContext(ctx, q, clientID, subject); err != nil {
				return err
			}
		}
		return nil
	})
}

// Epochs は、有効なアカウント・連携先・利用許可の最新の世代を、1つの文で読みます。無効なら sql.ErrNoRows です。
func (s *FederationStore) Epochs(ctx context.Context, clientID, subject string) (federation.Epochs, error) {
	var account, client, grant int64
	err := s.executor(ctx).QueryRowContext(ctx,
		`SELECT s.account_epoch, c.client_epoch, g.grant_epoch
		 FROM auth_federated_subjects s
		 JOIN auth_accounts a ON a.id = s.account_id
		 JOIN auth_oidc_grants g ON g.sub = s.sub
		 JOIN auth_oidc_clients c ON c.client_id = g.client_id
		 WHERE s.sub = ? AND c.client_id = ? AND a.disabled_at IS NULL AND c.enabled = 1 AND g.allowed = 1 AND s.deleted_at IS NULL`,
		subject, clientID).Scan(&account, &client, &grant)
	if err != nil {
		return federation.Epochs{}, err
	}
	return federation.Epochs{
		Account: strconv.FormatInt(account, 10),
		Client:  strconv.FormatInt(client, 10),
		Grant:   strconv.FormatInt(grant, 10),
	}, nil
}

// NewRequest は、未認証の認可要求を保存します。同じブラウザの古い要求は失効させます。
func (s *FederationStore) NewRequest(ctx context.Context, flowToken string, r FederationRequest) error {
	return s.Atomic(ctx, func(ctx context.Context) error {
		x := s.executor(ctx)
		now := s.now()
		if _, err := x.ExecContext(ctx, "DELETE FROM auth_oidc_requests WHERE consumed_at = 0 AND expires_at <= ?", now.Unix()); err != nil {
			return err
		}
		var n int
		if err := x.QueryRowContext(ctx, "SELECT COUNT(*) FROM auth_oidc_requests WHERE consumed_at = 0").Scan(&n); err != nil {
			return err
		}
		if n >= federationMaxRequests {
			return ErrFederationBusy
		}
		_, err := x.ExecContext(ctx,
			`INSERT INTO auth_oidc_requests (request_id, flow_hash, client_id, redirect_uri, state, nonce, challenge, expires_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			r.ID, hashToken(flowToken), r.Client, r.Redirect, r.State, r.Nonce, r.Challenge, now.Add(federationRequestTTL).Unix())
		return err
	})
}

const federationRequestColumns = `request_id, client_id, redirect_uri, state, nonce, challenge, sub,
	account_epoch, client_epoch, grant_epoch, auth_time, expires_at, code_expires_at, consumed_at`

func scanFederationRequest(row *sql.Row) (FederationRequest, error) {
	var q FederationRequest
	err := row.Scan(&q.ID, &q.Client, &q.Redirect, &q.State, &q.Nonce, &q.Challenge, &q.Subject,
		&q.Account, &q.Epochs.Client, &q.Grant, &q.AuthTime, &q.Expires, &q.CodeExpires, &q.Consumed)
	return q, err
}

// Request は認可要求を返します。
func (s *FederationStore) Request(ctx context.Context, id string) (FederationRequest, error) {
	return scanFederationRequest(s.executor(ctx).QueryRowContext(ctx,
		"SELECT "+federationRequestColumns+" FROM auth_oidc_requests WHERE request_id = ?", id))
}

// Flow は、ブラウザの手続きのCookieに対応する、進行中の要求を返します。
func (s *FederationStore) Flow(ctx context.Context, flowToken string) (FederationRequest, error) {
	return scanFederationRequest(s.executor(ctx).QueryRowContext(ctx,
		"SELECT "+federationRequestColumns+" FROM auth_oidc_requests WHERE flow_hash = ? AND expires_at > ? AND consumed_at = 0",
		hashToken(flowToken), s.now().Unix()))
}

// Bind は、管理者セッションの本人を要求に結びつけ、その時点の世代を固定します。
// 「ログインする」の二度押しに備え、未交換のコードがあれば無効にします（新しいコードだけが使える）。
// 停止中・許可なしなら sql.ErrNoRows です。トランザクションの中で読み、停止との順序を1つに決めます。
func (s *FederationStore) Bind(ctx context.Context, requestID, sessionToken string) error {
	return s.Atomic(ctx, func(ctx context.Context) error {
		x := s.executor(ctx)
		r, err := s.Request(ctx, requestID)
		if err != nil {
			return err
		}
		now := s.now()
		var subject string
		var authTime int64
		err = x.QueryRowContext(ctx,
			`SELECT f.sub, t.created_at FROM auth_sessions t
			 JOIN auth_accounts a ON a.id = t.account_id
			 JOIN auth_federated_subjects f ON f.account_id = a.id
			 WHERE t.token_hash = ? AND a.disabled_at IS NULL AND t.expires_at > ? AND t.last_seen_at > ?`,
			hashToken(sessionToken), now.Unix(), now.Add(-adminSessionIdle).Unix()).Scan(&subject, &authTime)
		if err != nil {
			return err
		}
		epochs, err := s.Epochs(ctx, r.Client, subject)
		if err != nil {
			return err
		}
		res, err := x.ExecContext(ctx,
			`UPDATE auth_oidc_requests SET sub = ?, account_epoch = ?, client_epoch = ?, grant_epoch = ?, auth_time = ?, code_hash = NULL, code_expires_at = 0
			 WHERE request_id = ? AND expires_at > ? AND consumed_at = 0`,
			subject, epochs.Account, epochs.Client, epochs.Grant, authTime, requestID, now.Unix())
		return affectedOne(res, err, ErrFederationInvalid)
	})
}

// SaveCode はコードのハッシュを保存します（60秒）。固定した世代が最新でなければ拒否します。
func (s *FederationStore) SaveCode(ctx context.Context, requestID, code string) error {
	r, err := s.Request(ctx, requestID)
	if err != nil {
		return err
	}
	epochs, err := s.Epochs(ctx, r.Client, r.Subject)
	if err != nil || epochs != r.Epochs {
		return ErrFederationInvalid
	}
	now := s.now()
	res, err := s.executor(ctx).ExecContext(ctx,
		`UPDATE auth_oidc_requests SET code_hash = ?, code_expires_at = ?
		 WHERE request_id = ? AND consumed_at = 0 AND code_hash IS NULL AND expires_at > ?`,
		hashToken(code), now.Add(federationCodeTTL).Unix(), requestID, now.Unix())
	return affectedOne(res, err, ErrFederationInvalid)
}

// ConsumeCode は、Atomic の中で、クライアント認証の後に呼びます。コードの持ち主・戻り先・PKCEを確かめた後でだけ、
// 交換済みのコードの再利用を失効の根拠にします。未認証の再送や束縛の不一致は、何も変えません（AUTH-INV-18）。
func (s *FederationStore) ConsumeCode(ctx context.Context, clientID, code, redirect, verifier string) (FederationRequest, error) {
	if _, ok := ctx.Value(federationConnKey{}).(*sql.Conn); !ok {
		return FederationRequest{}, ErrFederationInvalid
	}
	x := s.executor(ctx)
	r, err := scanFederationRequest(x.QueryRowContext(ctx,
		"SELECT "+federationRequestColumns+" FROM auth_oidc_requests WHERE code_hash = ?", hashToken(code)))
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrFederationInvalid
	} else if err != nil {
		return r, err
	}
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	if len(verifier) < 43 || len(verifier) > 128 || r.Client != clientID || r.Redirect != redirect ||
		subtle.ConstantTimeCompare([]byte(challenge), []byte(r.Challenge)) != 1 {
		return r, ErrFederationInvalid
	}
	if r.Consumed > 0 {
		if _, err := x.ExecContext(ctx, "UPDATE auth_oidc_grants SET grant_epoch = grant_epoch + 1 WHERE client_id = ? AND sub = ?", clientID, r.Subject); err != nil {
			return r, err
		}
		return r, ErrFederationReplay
	}
	now := s.now()
	if r.CodeExpires <= now.Unix() {
		return r, ErrFederationInvalid
	}
	epochs, err := s.Epochs(ctx, clientID, r.Subject)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && epochs != r.Epochs) {
		return r, ErrFederationInvalid
	} else if err != nil {
		return r, err
	}
	res, err := x.ExecContext(ctx,
		"UPDATE auth_oidc_requests SET consumed_at = ?, expires_at = ? WHERE request_id = ? AND consumed_at = 0",
		now.Unix(), now.Add(federationTombstoneTTL).Unix(), r.ID)
	return r, affectedOne(res, err, ErrFederationInvalid)
}

// RemoveFlow は、ブラウザの進行中の要求を消します。
func (s *FederationStore) RemoveFlow(ctx context.Context, flowToken string) error {
	_, err := s.executor(ctx).ExecContext(ctx, "DELETE FROM auth_oidc_requests WHERE flow_hash = ? AND consumed_at = 0", hashToken(flowToken))
	return err
}

// Purge は、期限切れの要求・秘密値と、保持期間を過ぎた墓標を消します（利用許可は外部キーで一緒に消える）。
func (s *FederationStore) Purge(ctx context.Context) error {
	now := s.now()
	for _, q := range []struct {
		query string
		at    time.Time
	}{
		{"DELETE FROM auth_oidc_requests WHERE expires_at <= ?", now},
		{"DELETE FROM auth_oidc_client_secrets WHERE expires_at <= ?", now},
		{"DELETE FROM auth_federated_subjects WHERE deleted_at IS NOT NULL AND deleted_at <= ?", now.Add(-federationTombstoneTTL)},
	} {
		if _, err := s.db.ExecContext(ctx, q.query, q.at.Unix()); err != nil {
			return err
		}
	}
	return nil
}

// SetFederationLoginNext は、ログイン完了後の移動先を決める関数を設定します（提供機能だけが使う）。
// 関数はサーバーが決めた固定の相対パスだけを返します。
func (f *Feature) SetFederationLoginNext(next func(*http.Request) string) {
	f.loginNext = next
}
