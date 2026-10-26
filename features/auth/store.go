package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"minimal-web-kit/internal/web/form"
)

// 管理者セッション・登録トークン・パスキーの保存を担当します。
// 生のトークンはDBに保存せず、SHA-256のハッシュだけを保存します。
// パスキー・セッション・登録トークンの読み書きは、すべてアカウント（account_id）単位で行います。

var (
	// ErrSessionInvalid は管理者セッションが存在しない、失効している、またはアカウントが停止中であることを示します。
	ErrSessionInvalid = errors.New("auth: admin session invalid")
	// ErrSetupTokenInvalid は登録トークンが存在しない、使用済み、失効している、またはアカウントが停止中であることを示します。
	ErrSetupTokenInvalid = errors.New("auth: setup token invalid")
	// ErrCredentialExists は同じcredential IDのパスキーが登録済みであることを示します。
	ErrCredentialExists = errors.New("auth: credential already registered")
	// ErrCredentialUnavailable はパスキーが存在しない、または無効化済みであることを示します。
	ErrCredentialUnavailable = errors.New("auth: credential unavailable")
	// ErrSignCountConflict は並行ログインにより署名カウンタを更新できなかったことを示します。
	ErrSignCountConflict = errors.New("auth: sign count update conflict")
	// ErrAlreadySetUp は有効なパスキーが登録済みのため、初回登録の手順を使えないことを示します。
	ErrAlreadySetUp = errors.New("auth: passkeys already registered; use auth-recover")
	// ErrNotFound はアカウントまたはパスキーが存在しないことを示します。
	ErrNotFound = errors.New("auth: not found")
	// ErrInvalidInput は入力（端末名・呼び名等）が制約を満たさないことを示します。
	ErrInvalidInput = errors.New("auth: invalid input")
	// ErrLastCredential は、アカウントの最後の有効なパスキーは削除できないことを示します。
	ErrLastCredential = errors.New("auth: cannot delete the last active passkey")
)

const maxLabelRunes = 50

type adminStore struct {
	db  *sql.DB
	now func() time.Time
}

func newAdminStore(db *sql.DB, now func() time.Time) *adminStore {
	return &adminStore{db: db, now: now}
}

// newToken は32バイトの乱数をbase64url（パディングなし）で返します。
func newToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func hashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// execer は *sql.DB と *sql.Tx の共通部分です。
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func affectedOne(res sql.Result, err error, ifNone error) error {
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ifNone
	}
	return nil
}

// --- 管理者セッション ---

// createSession は、アカウントの管理者セッションを発行し、Cookieに入れる生のトークンを返します。
func createSession(ctx context.Context, x execer, accountID int64, now time.Time) (string, error) {
	token, err := newToken()
	if err != nil {
		return "", err
	}
	_, err = x.ExecContext(ctx,
		"INSERT INTO auth_sessions (token_hash, account_id, created_at, last_seen_at, expires_at) VALUES (?, ?, ?, ?, ?)",
		hashToken(token), accountID, now.Unix(), now.Unix(), now.Add(adminSessionMax).Unix())
	if err != nil {
		return "", err
	}
	return token, nil
}

// sessionAccount は、セッションのアカウントです。ロールは要求ごとにアカウントから読みます。
type sessionAccount struct {
	AccountID int64
	Role      string
}

// validateSession は、無操作・絶対期限の両方で有効なこと、アカウントが存在し停止中でないことを確認し、
// 最終アクセス日時を更新します。失効していれば削除して ErrSessionInvalid を返します。
func (s *adminStore) validateSession(ctx context.Context, token string) (sessionAccount, error) {
	if token == "" {
		return sessionAccount{}, ErrSessionInvalid
	}
	hash := hashToken(token)
	now := s.now()
	res, err := s.db.ExecContext(ctx,
		`UPDATE auth_sessions SET last_seen_at = ?
		 WHERE token_hash = ? AND expires_at > ? AND last_seen_at > ?
		   AND EXISTS (SELECT 1 FROM auth_accounts a WHERE a.id = auth_sessions.account_id AND a.disabled_at IS NULL)`,
		now.Unix(), hash, now.Unix(), now.Add(-adminSessionIdle).Unix())
	if err := affectedOne(res, err, ErrSessionInvalid); err != nil {
		if errors.Is(err, ErrSessionInvalid) {
			if _, derr := s.db.ExecContext(ctx, "DELETE FROM auth_sessions WHERE token_hash = ?", hash); derr != nil {
				return sessionAccount{}, derr
			}
		}
		return sessionAccount{}, err
	}
	var a sessionAccount
	err = s.db.QueryRowContext(ctx,
		"SELECT a.id, a.role FROM auth_sessions s JOIN auth_accounts a ON a.id = s.account_id WHERE s.token_hash = ?",
		hash).Scan(&a.AccountID, &a.Role)
	if errors.Is(err, sql.ErrNoRows) {
		return sessionAccount{}, ErrSessionInvalid
	}
	return a, err
}

func (s *adminStore) deleteSession(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM auth_sessions WHERE token_hash = ?", hashToken(token))
	return err
}

// --- 登録トークン ---

// issueSetupToken は、アカウントの未使用の登録トークンを無効化してから、新しいトークンを発行します。
// 他のアカウントの登録トークンには触れません。
func issueSetupToken(ctx context.Context, x execer, accountID int64, ttl time.Duration, now time.Time) (string, error) {
	if _, err := x.ExecContext(ctx, "DELETE FROM auth_setup_tokens WHERE account_id = ? AND used_at IS NULL", accountID); err != nil {
		return "", err
	}
	token, err := newToken()
	if err != nil {
		return "", err
	}
	if _, err := x.ExecContext(ctx,
		"INSERT INTO auth_setup_tokens (token_hash, account_id, expires_at) VALUES (?, ?, ?)",
		hashToken(token), accountID, now.Add(ttl).Unix()); err != nil {
		return "", err
	}
	return token, nil
}

// setupTarget は、登録トークンが結びつくアカウントです。
type setupTarget struct {
	AccountID      int64
	Name           string
	WebAuthnUserID []byte
}

// checkSetupToken は、登録トークンが未使用・有効期限内で、アカウントが停止中でないことを確認し、
// 結びつくアカウントを返します。消費はしません。
func (s *adminStore) checkSetupToken(ctx context.Context, token string) (setupTarget, error) {
	if token == "" {
		return setupTarget{}, ErrSetupTokenInvalid
	}
	var t setupTarget
	err := s.db.QueryRowContext(ctx,
		`SELECT a.id, a.name, a.webauthn_user_id FROM auth_setup_tokens t JOIN auth_accounts a ON a.id = t.account_id
		 WHERE t.token_hash = ? AND t.used_at IS NULL AND t.expires_at > ? AND a.disabled_at IS NULL`,
		hashToken(token), s.now().Unix()).Scan(&t.AccountID, &t.Name, &t.WebAuthnUserID)
	if errors.Is(err, sql.ErrNoRows) {
		return setupTarget{}, ErrSetupTokenInvalid
	}
	return t, err
}

// --- パスキー ---

func normalizeLabel(label string) (string, error) {
	label, err := form.Line(label, maxLabelRunes, true)
	if err != nil {
		return "", ErrInvalidInput
	}
	return label, nil
}

func insertCredential(ctx context.Context, x execer, accountID int64, c newCredential, label string, now time.Time) error {
	_, err := x.ExecContext(ctx,
		"INSERT INTO auth_credentials (id, account_id, public_key, alg, sign_count, label, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)",
		c.ID, accountID, c.PublicKey, c.Alg, int64(c.SignCount), label, now.Unix())
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") || strings.Contains(err.Error(), "PRIMARY KEY") {
			return ErrCredentialExists
		}
		return err
	}
	return nil
}

// registerWithSetupToken は、登録トークンの消費と、そのアカウントへのパスキーの保存を1つのトランザクションで行います。
// どちらかが失敗すれば両方を取り消し、トークンは未使用のまま残ります。
func (s *adminStore) registerWithSetupToken(ctx context.Context, token string, c newCredential, label string) error {
	label, err := normalizeLabel(label)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := s.now()
	var accountID int64
	err = tx.QueryRowContext(ctx,
		`UPDATE auth_setup_tokens SET used_at = ?
		 WHERE token_hash = ? AND used_at IS NULL AND expires_at > ?
		   AND EXISTS (SELECT 1 FROM auth_accounts a WHERE a.id = auth_setup_tokens.account_id AND a.disabled_at IS NULL)
		 RETURNING account_id`,
		now.Unix(), hashToken(token), now.Unix()).Scan(&accountID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrSetupTokenInvalid
	} else if err != nil {
		return err
	}
	if err := insertCredential(ctx, tx, accountID, c, label, now); err != nil {
		return err
	}
	return tx.Commit()
}

// addCredential は、ログイン中のアカウントが自分のパスキーを追加します。
func (s *adminStore) addCredential(ctx context.Context, accountID int64, c newCredential, label string) error {
	label, err := normalizeLabel(label)
	if err != nil {
		return err
	}
	return insertCredential(ctx, s.db, accountID, c, label, s.now())
}

// loginCredential は、ログインに使うパスキーと、その持ち主のアカウントです。
type loginCredential struct {
	storedCredential
	AccountID       int64
	WebAuthnUserID  []byte
	AccountDisabled bool
}

// getCredential は有効なパスキーと持ち主のアカウントを返します。存在しない・無効化済みなら ErrCredentialUnavailable を返します。
// アカウントの停止は、署名の検証と複製の疑いの判定の後に、呼び出し側が確かめます。
func (s *adminStore) getCredential(ctx context.Context, id []byte) (loginCredential, error) {
	var c loginCredential
	var count int64
	var disabledAt, accountDisabledAt sql.NullInt64
	err := s.db.QueryRowContext(ctx,
		`SELECT c.id, c.public_key, c.alg, c.sign_count, c.disabled_at, a.id, a.webauthn_user_id, a.disabled_at
		 FROM auth_credentials c JOIN auth_accounts a ON a.id = c.account_id WHERE c.id = ?`, id,
	).Scan(&c.ID, &c.PublicKey, &c.Alg, &count, &disabledAt, &c.AccountID, &c.WebAuthnUserID, &accountDisabledAt)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && disabledAt.Valid) {
		return loginCredential{}, ErrCredentialUnavailable
	}
	if err != nil {
		return loginCredential{}, err
	}
	if count < 0 || count > int64(^uint32(0)) {
		return loginCredential{}, ErrCredentialUnavailable
	}
	c.SignCount = uint32(count)
	c.AccountDisabled = accountDisabledAt.Valid
	return c, nil
}

// login は、署名カウンタ・パスキーの最終使用日時・アカウントの最終ログイン日時の更新と、
// セッションの作成を1つのトランザクションで行い、セッションのトークンを返します。
// カウンタは単調増加のみ許し、並行ログインで小さい値が後から書かれた場合は ErrSignCountConflict を返します。
func (s *adminStore) login(ctx context.Context, credID []byte, accountID int64, count uint32) (string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()
	now := s.now()
	res, err := tx.ExecContext(ctx,
		`UPDATE auth_credentials SET sign_count = ?, last_used_at = ?
		 WHERE id = ? AND account_id = ? AND disabled_at IS NULL AND (sign_count < ? OR (sign_count = 0 AND ? = 0))`,
		int64(count), now.Unix(), credID, accountID, int64(count), int64(count))
	if err := affectedOne(res, err, ErrSignCountConflict); err != nil {
		return "", err
	}
	res, err = tx.ExecContext(ctx,
		"UPDATE auth_accounts SET last_login_at = ? WHERE id = ? AND disabled_at IS NULL", now.Unix(), accountID)
	if err := affectedOne(res, err, ErrSessionInvalid); err != nil {
		return "", err
	}
	token, err := createSession(ctx, tx, accountID, now)
	if err != nil {
		return "", err
	}
	return token, tx.Commit()
}

// disableCredential は複製の疑いがあるパスキーを無効化し、そのアカウントの管理者セッションをすべて削除します。
// 複製したキーで先にログインした者のセッションを、見分けずに確実に失効させるためです。
func (s *adminStore) disableCredential(ctx context.Context, id []byte, accountID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx,
		"UPDATE auth_credentials SET disabled_at = ? WHERE id = ? AND account_id = ? AND disabled_at IS NULL",
		s.now().Unix(), id, accountID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM auth_sessions WHERE account_id = ?", accountID); err != nil {
		return err
	}
	return tx.Commit()
}

// credentialInfo はパスキー管理画面に表示する項目です。公開鍵は含めません。
type credentialInfo struct {
	ID         []byte
	Label      string
	CreatedAt  time.Time
	LastUsedAt time.Time // 未使用ならゼロ値
	DisabledAt time.Time // 有効ならゼロ値
}

// Param は経路の {id} に使う、credential IDのbase64url表現です。
func (c credentialInfo) Param() string { return b64(c.ID) }

func (c credentialInfo) Disabled() bool { return !c.DisabledAt.IsZero() }

func scanCredentialInfo(row rowScanner) (credentialInfo, error) {
	var c credentialInfo
	var createdAt int64
	var lastUsedAt, disabledAt sql.NullInt64
	if err := row.Scan(&c.ID, &c.Label, &createdAt, &lastUsedAt, &disabledAt); err != nil {
		return credentialInfo{}, err
	}
	c.CreatedAt = time.Unix(createdAt, 0).In(time.Local)
	if lastUsedAt.Valid {
		c.LastUsedAt = time.Unix(lastUsedAt.Int64, 0).In(time.Local)
	}
	if disabledAt.Valid {
		c.DisabledAt = time.Unix(disabledAt.Int64, 0).In(time.Local)
	}
	return c, nil
}

const credentialInfoColumns = "id, label, created_at, last_used_at, disabled_at"

// listCredentials は、アカウントのパスキーを、無効化済みも含めて登録順に返します。
func (s *adminStore) listCredentials(ctx context.Context, accountID int64) ([]credentialInfo, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT "+credentialInfoColumns+" FROM auth_credentials WHERE account_id = ? ORDER BY created_at, id", accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []credentialInfo
	for rows.Next() {
		c, err := scanCredentialInfo(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, c)
	}
	return list, rows.Err()
}

// credentialIDs は、アカウントのパスキーのIDを返します（登録時の excludeCredentials 用）。
func (s *adminStore) credentialIDs(ctx context.Context, accountID int64) ([][]byte, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id FROM auth_credentials WHERE account_id = ?", accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids [][]byte
	for rows.Next() {
		var id []byte
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// getCredentialInfo は、アカウントのパスキーを返します。他のアカウントのパスキーなら ErrNotFound です。
func (s *adminStore) getCredentialInfo(ctx context.Context, accountID int64, id []byte) (credentialInfo, error) {
	c, err := scanCredentialInfo(s.db.QueryRowContext(ctx,
		"SELECT "+credentialInfoColumns+" FROM auth_credentials WHERE id = ? AND account_id = ?", id, accountID))
	if errors.Is(err, sql.ErrNoRows) {
		return credentialInfo{}, ErrNotFound
	}
	return c, err
}

// renameCredential は、アカウントのパスキーの端末名を変更します。
func (s *adminStore) renameCredential(ctx context.Context, accountID int64, id []byte, label string) error {
	label, err := normalizeLabel(label)
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, "UPDATE auth_credentials SET label = ? WHERE id = ? AND account_id = ?", label, id, accountID)
	return affectedOne(res, err, ErrNotFound)
}

// deleteCredential は、アカウントのパスキーを削除し、そのアカウントの管理者セッションを keepSession 以外すべて削除します。
// 紛失した端末でログイン中のセッションを残さないためです。アカウントの最後の有効なパスキーは削除できません。
// 残数の確認と削除は1つの文で行い、並行して削除しても有効なパスキーが0件にならないようにします。
func (s *adminStore) deleteCredential(ctx context.Context, accountID int64, id []byte, keepSession string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx,
		`DELETE FROM auth_credentials WHERE id = ? AND account_id = ? AND (disabled_at IS NOT NULL OR
			(SELECT COUNT(*) FROM auth_credentials WHERE account_id = ? AND disabled_at IS NULL AND id != ?) > 0)`,
		id, accountID, accountID, id)
	if err := affectedOne(res, err, ErrLastCredential); err != nil {
		if errors.Is(err, ErrLastCredential) {
			var exists int
			if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM auth_credentials WHERE id = ? AND account_id = ?", id, accountID).Scan(&exists); err != nil {
				return err
			}
			if exists == 0 {
				return ErrNotFound
			}
		}
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM auth_sessions WHERE account_id = ? AND token_hash != ?", accountID, hashToken(keepSession)); err != nil {
		return err
	}
	return tx.Commit()
}

// countDisabledCredentials は無効化済みのパスキーの数を返します（管理画面上部の警告用）。
// accountID が0なら全アカウントの分（管理者向け）、それ以外はそのアカウントの分です。
func (s *adminStore) countDisabledCredentials(ctx context.Context, accountID int64) (int, error) {
	var n int
	if accountID == 0 {
		err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM auth_credentials WHERE disabled_at IS NOT NULL").Scan(&n)
		return n, err
	}
	err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM auth_credentials WHERE account_id = ? AND disabled_at IS NOT NULL", accountID).Scan(&n)
	return n, err
}

// SetupURL は登録トークンをフラグメントに入れた登録画面のURLを返します。
// フラグメントはサーバーにもRefererにも送られません。
func SetupURL(origin, token string) string {
	return origin + "/admin/setup#token=" + token
}

type rowScanner interface {
	Scan(dest ...any) error
}
