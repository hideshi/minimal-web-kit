package cms

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

var (
	// ErrSessionInvalid は管理者セッションが存在しない、または失効していることを示します。
	ErrSessionInvalid = errors.New("cms: admin session invalid")
	// ErrSetupTokenInvalid は登録トークンが存在しない、使用済み、または失効していることを示します。
	ErrSetupTokenInvalid = errors.New("cms: setup token invalid")
	// ErrCredentialExists は同じcredential IDのパスキーが登録済みであることを示します。
	ErrCredentialExists = errors.New("cms: credential already registered")
	// ErrCredentialUnavailable はパスキーが存在しない、または無効化済みであることを示します。
	ErrCredentialUnavailable = errors.New("cms: credential unavailable")
	// ErrSignCountConflict は並行ログインにより署名カウンタを更新できなかったことを示します。
	ErrSignCountConflict = errors.New("cms: sign count update conflict")
	// ErrAlreadySetUp はパスキーが登録済みのため、初回登録の手順を使えないことを示します。
	ErrAlreadySetUp = errors.New("cms: passkeys already registered; use cms-recover")
	// ErrLastCredential は、最後の有効なパスキーは削除できないことを示します。
	ErrLastCredential = errors.New("cms: cannot delete the last active passkey")
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

// --- 管理者セッション ---

// createSession は管理者セッションを発行し、Cookieに入れる生のトークンを返します。
func (s *adminStore) createSession(ctx context.Context) (string, error) {
	token, err := newToken()
	if err != nil {
		return "", err
	}
	now := s.now()
	_, err = s.db.ExecContext(ctx,
		"INSERT INTO cms_admin_sessions (token_hash, created_at, last_seen_at, expires_at) VALUES (?, ?, ?, ?)",
		hashToken(token), now.Unix(), now.Unix(), now.Add(adminSessionMax).Unix())
	if err != nil {
		return "", err
	}
	return token, nil
}

// validateSession は、無操作・絶対期限の両方で有効なことを確認し、最終アクセス日時を更新します。
// 失効していれば削除して ErrSessionInvalid を返します。
func (s *adminStore) validateSession(ctx context.Context, token string) error {
	if token == "" {
		return ErrSessionInvalid
	}
	hash := hashToken(token)
	now := s.now()
	res, err := s.db.ExecContext(ctx,
		"UPDATE cms_admin_sessions SET last_seen_at = ? WHERE token_hash = ? AND expires_at > ? AND last_seen_at > ?",
		now.Unix(), hash, now.Unix(), now.Add(-adminSessionIdle).Unix())
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 1 {
		return nil
	}
	if _, err := s.db.ExecContext(ctx, "DELETE FROM cms_admin_sessions WHERE token_hash = ?", hash); err != nil {
		return err
	}
	return ErrSessionInvalid
}

func (s *adminStore) deleteSession(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM cms_admin_sessions WHERE token_hash = ?", hashToken(token))
	return err
}

// deleteOtherSessions は、指定したセッション以外の管理者セッションをすべて削除します（パスキー削除時）。
func (s *adminStore) deleteOtherSessions(ctx context.Context, keepToken string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM cms_admin_sessions WHERE token_hash != ?", hashToken(keepToken))
	return err
}

// --- 登録トークン ---

// issueSetupToken は未使用の登録トークンを無効化してから、新しいトークンを発行します。
func issueSetupToken(ctx context.Context, tx *sql.Tx, now time.Time) (string, error) {
	if _, err := tx.ExecContext(ctx, "DELETE FROM cms_setup_tokens WHERE used_at IS NULL"); err != nil {
		return "", err
	}
	token, err := newToken()
	if err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO cms_setup_tokens (token_hash, expires_at) VALUES (?, ?)",
		hashToken(token), now.Add(setupTokenTTL).Unix()); err != nil {
		return "", err
	}
	return token, nil
}

// checkSetupToken は登録トークンが未使用かつ有効期限内であることを確認します。消費はしません。
func (s *adminStore) checkSetupToken(ctx context.Context, token string) error {
	if token == "" {
		return ErrSetupTokenInvalid
	}
	var n int
	err := s.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM cms_setup_tokens WHERE token_hash = ? AND used_at IS NULL AND expires_at > ?",
		hashToken(token), s.now().Unix()).Scan(&n)
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrSetupTokenInvalid
	}
	return nil
}

// --- パスキー ---

func normalizeLabel(label string) (string, error) {
	label, err := form.Line(label, maxLabelRunes, true)
	if err != nil {
		return "", ErrInvalidInput
	}
	return label, nil
}

func insertCredential(ctx context.Context, tx *sql.Tx, c newCredential, label string, now time.Time) error {
	_, err := tx.ExecContext(ctx,
		"INSERT INTO cms_credentials (id, public_key, alg, sign_count, label, created_at) VALUES (?, ?, ?, ?, ?, ?)",
		c.ID, c.PublicKey, c.Alg, int64(c.SignCount), label, now.Unix())
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") || strings.Contains(err.Error(), "PRIMARY KEY") {
			return ErrCredentialExists
		}
		return err
	}
	return nil
}

// registerWithSetupToken は、登録トークンの消費とパスキーの保存を1つのトランザクションで行います。
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
	res, err := tx.ExecContext(ctx,
		"UPDATE cms_setup_tokens SET used_at = ? WHERE token_hash = ? AND used_at IS NULL AND expires_at > ?",
		now.Unix(), hashToken(token), now.Unix())
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n != 1 {
		return ErrSetupTokenInvalid
	}
	if err := insertCredential(ctx, tx, c, label, now); err != nil {
		return err
	}
	return tx.Commit()
}

// addCredential はログイン中の管理者がパスキーを追加します。
func (s *adminStore) addCredential(ctx context.Context, c newCredential, label string) error {
	label, err := normalizeLabel(label)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := insertCredential(ctx, tx, c, label, s.now()); err != nil {
		return err
	}
	return tx.Commit()
}

// getCredential は有効なパスキーを返します。存在しない・無効化済みなら ErrCredentialUnavailable を返します。
func (s *adminStore) getCredential(ctx context.Context, id []byte) (storedCredential, error) {
	var c storedCredential
	var count int64
	var disabledAt sql.NullInt64
	err := s.db.QueryRowContext(ctx,
		"SELECT id, public_key, alg, sign_count, disabled_at FROM cms_credentials WHERE id = ?", id,
	).Scan(&c.ID, &c.PublicKey, &c.Alg, &count, &disabledAt)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && disabledAt.Valid) {
		return storedCredential{}, ErrCredentialUnavailable
	}
	if err != nil {
		return storedCredential{}, err
	}
	if count < 0 || count > int64(^uint32(0)) {
		return storedCredential{}, ErrCredentialUnavailable
	}
	c.SignCount = uint32(count)
	return c, nil
}

// recordLogin は署名カウンタと最終使用日時を更新します。カウンタは単調増加のみ許し、
// 並行ログインで小さい値が後から書かれた場合は ErrSignCountConflict を返します。
func (s *adminStore) recordLogin(ctx context.Context, id []byte, count uint32) error {
	res, err := s.db.ExecContext(ctx,
		"UPDATE cms_credentials SET sign_count = ?, last_used_at = ? WHERE id = ? AND disabled_at IS NULL AND (sign_count < ? OR (sign_count = 0 AND ? = 0))",
		int64(count), s.now().Unix(), id, int64(count), int64(count))
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n != 1 {
		return ErrSignCountConflict
	}
	return nil
}

// disableCredential は複製の疑いがあるパスキーを無効化し、管理者セッションをすべて削除します。
// 複製したキーで先にログインした者のセッションを、見分けずに確実に失効させるためです。
func (s *adminStore) disableCredential(ctx context.Context, id []byte) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx,
		"UPDATE cms_credentials SET disabled_at = ? WHERE id = ? AND disabled_at IS NULL", s.now().Unix(), id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM cms_admin_sessions"); err != nil {
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

// listCredentials は登録済みのパスキーを、無効化済みも含めて登録順に返します。
func (s *adminStore) listCredentials(ctx context.Context) ([]credentialInfo, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+credentialInfoColumns+" FROM cms_credentials ORDER BY created_at, id")
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

func (s *adminStore) getCredentialInfo(ctx context.Context, id []byte) (credentialInfo, error) {
	c, err := scanCredentialInfo(s.db.QueryRowContext(ctx, "SELECT "+credentialInfoColumns+" FROM cms_credentials WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return credentialInfo{}, ErrNotFound
	}
	return c, err
}

// renameCredential はパスキーの端末名を変更します。
func (s *adminStore) renameCredential(ctx context.Context, id []byte, label string) error {
	label, err := normalizeLabel(label)
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, "UPDATE cms_credentials SET label = ? WHERE id = ?", label, id)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return ErrNotFound
	}
	return nil
}

// deleteCredential はパスキーを削除し、現在のセッション以外の管理者セッションをすべて削除します。
// 紛失した端末でログイン中のセッションを残さないためです。最後の有効なパスキーは削除できません。
// 残数の確認と削除は1つの文で行い、並行して削除しても有効なパスキーが0件にならないようにします。
func (s *adminStore) deleteCredential(ctx context.Context, id []byte, keepSession string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx,
		`DELETE FROM cms_credentials WHERE id = ? AND (disabled_at IS NOT NULL OR
			(SELECT COUNT(*) FROM cms_credentials WHERE disabled_at IS NULL AND id != ?) > 0)`,
		id, id)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		var exists int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM cms_credentials WHERE id = ?", id).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			return ErrNotFound
		}
		return ErrLastCredential
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM cms_admin_sessions WHERE token_hash != ?", hashToken(keepSession)); err != nil {
		return err
	}
	return tx.Commit()
}

// countDisabledCredentials は無効化済みのパスキーの数を返します（管理画面上部の警告用）。
func (s *adminStore) countDisabledCredentials(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM cms_credentials WHERE disabled_at IS NOT NULL").Scan(&n)
	return n, err
}

func (s *adminStore) webAuthnUserID(ctx context.Context) ([]byte, error) {
	var id []byte
	err := s.db.QueryRowContext(ctx, "SELECT webauthn_user_id FROM cms_settings WHERE id = 1").Scan(&id)
	return id, err
}

// --- 初回登録と復旧（cmd/kit から呼ぶ） ---

// Setup は、パスキーが1件も登録されていない場合だけ、登録トークンを発行して返します。
func Setup(ctx context.Context, db *sql.DB, now time.Time) (string, error) {
	if err := Migrate(db); err != nil {
		return "", err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()
	var n int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM cms_credentials").Scan(&n); err != nil {
		return "", err
	}
	if n > 0 {
		return "", ErrAlreadySetUp
	}
	token, err := issueSetupToken(ctx, tx, now)
	if err != nil {
		return "", err
	}
	return token, tx.Commit()
}

// Recover は、すべてのパスキー・管理者セッション・登録トークンを削除し、WebAuthnの user.id を
// 作り直してから、新しい登録トークンを発行して返します。認証器の紛失・盗難時に使います。
func Recover(ctx context.Context, db *sql.DB, now time.Time) (string, error) {
	if err := Migrate(db); err != nil {
		return "", err
	}
	userID := make([]byte, 16)
	if _, err := rand.Read(userID); err != nil {
		return "", err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()
	for _, q := range []string{
		"DELETE FROM cms_credentials",
		"DELETE FROM cms_admin_sessions",
		"DELETE FROM cms_setup_tokens",
	} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return "", err
		}
	}
	if _, err := tx.ExecContext(ctx, "UPDATE cms_settings SET webauthn_user_id = ? WHERE id = 1", userID); err != nil {
		return "", err
	}
	token, err := issueSetupToken(ctx, tx, now)
	if err != nil {
		return "", err
	}
	return token, tx.Commit()
}

// SetupURL は登録トークンをフラグメントに入れた登録画面のURLを返します。
// フラグメントはサーバーにもRefererにも送られません。
func SetupURL(origin, token string) string {
	return origin + "/admin/setup#token=" + token
}
