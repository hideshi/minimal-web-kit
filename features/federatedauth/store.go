package federatedauth

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"time"

	"minimal-web-kit/internal/federation"
	"minimal-web-kit/internal/store"
)

const (
	sessionIdle = 2 * time.Hour
	sessionMax  = 12 * time.Hour
)

var (
	errNoSession   = errors.New("federatedauth: no session")
	errDisabled    = errors.New("federatedauth: account disabled")
	errNotFound    = errors.New("federatedauth: account not found")
	errInvalidRole = errors.New("federatedauth: role must be admin or site_admin")
)

// accountSession は、有効なセッションとその利用者です。
type accountSession struct {
	AccountID int64
	Subject   string
	Role      string
	Epochs    federation.Epochs
}

// LogValue は、識別子と世代をログに出さないようにします。
func (s accountSession) LogValue() slog.Value {
	return slog.GroupValue(slog.Int64("account_id", s.AccountID))
}

type sessionStore struct {
	db     *sql.DB
	issuer string
	now    func() time.Time
}

// login は利用者の局所の行を作り（初回はサイト管理者）、セッションを発行して、Cookieに入れる生のトークンを返します。
// 寿命は発行から12時間と、認証基盤での本人確認から12時間の早い方です。
func (s *sessionStore) login(ctx context.Context, subject string, epochs federation.Epochs, authTime int64) (string, error) {
	now := s.now()
	expires := min(now.Add(sessionMax).Unix(), authTime+int64(sessionMax/time.Second))
	if expires <= now.Unix() {
		return "", errNoSession
	}
	token := federation.Token()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()
	// 先に書き込み、読み取りから書き込みへの切り替えで競合しないようにする
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO federated_accounts (issuer, sub, created_at) VALUES (?, ?, ?) ON CONFLICT (issuer, sub) DO NOTHING",
		s.issuer, subject, now.Unix()); err != nil {
		return "", err
	}
	var id int64
	var disabled sql.NullInt64
	if err := tx.QueryRowContext(ctx, "SELECT id, disabled_at FROM federated_accounts WHERE issuer = ? AND sub = ?", s.issuer, subject).Scan(&id, &disabled); err != nil {
		return "", err
	}
	if disabled.Valid {
		return "", errDisabled
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO federated_sessions (token_hash, account_id, account_epoch, client_epoch, grant_epoch, auth_time, created_at, last_seen_at, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		federation.Hash(token), id, epochs.Account, epochs.Client, epochs.Grant, authTime, now.Unix(), now.Unix(), expires); err != nil {
		return "", err
	}
	return token, tx.Commit()
}

// session は、期限内で、利用者が停止されていないセッションを返します。ロールは要求ごとに読みます。無ければ errNoSession です。
func (s *sessionStore) session(ctx context.Context, token string) (accountSession, error) {
	now := s.now()
	var a accountSession
	err := s.db.QueryRowContext(ctx,
		`SELECT a.id, a.sub, a.role, t.account_epoch, t.client_epoch, t.grant_epoch
		 FROM federated_sessions t JOIN federated_accounts a ON a.id = t.account_id
		 WHERE t.token_hash = ? AND a.issuer = ? AND a.disabled_at IS NULL AND t.expires_at > ? AND t.last_seen_at > ?`,
		federation.Hash(token), s.issuer, now.Unix(), now.Add(-sessionIdle).Unix(),
	).Scan(&a.AccountID, &a.Subject, &a.Role, &a.Epochs.Account, &a.Epochs.Client, &a.Epochs.Grant)
	if errors.Is(err, sql.ErrNoRows) {
		return accountSession{}, errNoSession
	}
	return a, err
}

// touch は最終操作の日時を更新します。
func (s *sessionStore) touch(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, "UPDATE federated_sessions SET last_seen_at = ? WHERE token_hash = ?", s.now().Unix(), federation.Hash(token))
	return err
}

// deleteSession はセッションを削除します。
func (s *sessionStore) deleteSession(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM federated_sessions WHERE token_hash = ?", federation.Hash(token))
	return err
}

// purge は期限切れのセッションを削除します。
func (s *sessionStore) purge(ctx context.Context) error {
	now := s.now()
	_, err := s.db.ExecContext(ctx, "DELETE FROM federated_sessions WHERE expires_at <= ? OR last_seen_at <= ?", now.Unix(), now.Add(-sessionIdle).Unix())
	return err
}

// setRole は局所の利用者のロールを変えます。行が無ければ errNotFound です（先に1度ログインしてもらう）。
func (s *sessionStore) setRole(ctx context.Context, subject, role string) error {
	if role != "admin" && role != "site_admin" {
		return errInvalidRole
	}
	res, err := s.db.ExecContext(ctx, "UPDATE federated_accounts SET role = ? WHERE issuer = ? AND sub = ?", role, s.issuer, subject)
	return store.AffectedOne(res, err, errNotFound)
}

// setDisabled は局所の利用者を停止・再開します。停止ではセッションも同じトランザクションで削除します。
func (s *sessionStore) setDisabled(ctx context.Context, subject string, disabled bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var at any
	if disabled {
		at = s.now().Unix()
	}
	res, err := tx.ExecContext(ctx, "UPDATE federated_accounts SET disabled_at = ? WHERE issuer = ? AND sub = ?", at, s.issuer, subject)
	if err := store.AffectedOne(res, err, errNotFound); err != nil {
		return err
	}
	if disabled {
		if _, err := tx.ExecContext(ctx, "DELETE FROM federated_sessions WHERE account_id = (SELECT id FROM federated_accounts WHERE issuer = ? AND sub = ?)", s.issuer, subject); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// deleteAccount は局所の利用者を削除します（セッションも一緒に消える）。IDは再利用しません。
func (s *sessionStore) deleteAccount(ctx context.Context, subject string) error {
	res, err := s.db.ExecContext(ctx, "DELETE FROM federated_accounts WHERE issuer = ? AND sub = ?", s.issuer, subject)
	return store.AffectedOne(res, err, errNotFound)
}
