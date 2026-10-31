package auth

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"minimal-web-kit/features"
	"minimal-web-kit/internal/web/form"
)

const (
	roleAdmin  = string(features.RoleAdmin)
	roleMember = string(features.RoleMember)
	roleUser   = string(features.RoleUser)

	maxAccountNameRunes = 50
	// firstAdminName は、auth-setup・auth-recover が作る最初のシステム管理者アカウントの呼び名です。
	firstAdminName = "管理者"
	// inviteTokenTTL は、管理画面で発行する登録URLの有効期限です（本人に渡すため、auth-setup より長い）。
	inviteTokenTTL = 24 * time.Hour
)

var (
	// ErrNameTaken は、同じ呼び名のアカウントがあることを示します。
	ErrNameTaken = errors.New("auth: account name already used")
	// ErrSelfChange は、自分自身のロール変更・停止・削除を拒否したことを示します。
	ErrSelfChange = errors.New("auth: cannot change own account")
	// ErrLastAdmin は、有効なシステム管理者が0人になる操作を拒否したことを示します。
	ErrLastAdmin = errors.New("auth: at least one active admin is required")
	// ErrOldSchema は、アカウント導入前の形式の表が残っていることを示します。
	ErrOldSchema = errors.New("auth: old schema found; run auth-recover -reset-schema")
	// ErrChooseAccount は、auth-recover で登録し直すアカウントの指定が必要なことを示します。
	ErrChooseAccount = errors.New("auth: choose an admin account with -account <id>")
)

// effectiveAdminElsewhere は、指定したアカウント以外に「有効なシステム管理者」（ロールがシステム管理者・停止中でない・有効なパスキーが1件以上）が
// いるかを判定するSQLの条件です。引数として除外するアカウントIDを1つ取ります（AUTH-INV-1）。
const effectiveAdminElsewhere = `(SELECT COUNT(*) FROM auth_accounts a
	WHERE a.role = 'admin' AND a.disabled_at IS NULL AND a.id != ?
	  AND EXISTS (SELECT 1 FROM auth_credentials c WHERE c.account_id = a.id AND c.disabled_at IS NULL)) > 0`

func normalizeAccountName(name string) (string, error) {
	name, err := form.Line(name, maxAccountNameRunes, true)
	if err != nil {
		return "", ErrInvalidInput
	}
	return name, nil
}

func validRole(role string) bool { return role == roleAdmin || role == roleMember || role == roleUser }

func newWebAuthnUserID() ([]byte, error) {
	id := make([]byte, 16)
	_, err := rand.Read(id)
	return id, err
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE")
}

// insertAccount はアカウントを作り、IDを返します。
func insertAccount(ctx context.Context, x execer, name, role string, now time.Time) (int64, error) {
	name, err := normalizeAccountName(name)
	if err != nil {
		return 0, err
	}
	if !validRole(role) {
		return 0, ErrInvalidInput
	}
	userID, err := newWebAuthnUserID()
	if err != nil {
		return 0, err
	}
	res, err := x.ExecContext(ctx,
		"INSERT INTO auth_accounts (name, role, webauthn_user_id, created_at) VALUES (?, ?, ?, ?)",
		name, role, userID, now.Unix())
	if isUniqueViolation(err) {
		return 0, ErrNameTaken
	} else if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// createAccountWithToken は、アカウントを作り、その登録トークン（24時間）を発行します。1つのトランザクションで行います。
func (s *adminStore) createAccountWithToken(ctx context.Context, name, role string) (int64, string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = tx.Rollback() }()
	now := s.now()
	id, err := insertAccount(ctx, tx, name, role, now)
	if err != nil {
		return 0, "", err
	}
	token, err := issueSetupToken(ctx, tx, id, inviteTokenTTL, now)
	if err != nil {
		return 0, "", err
	}
	return id, token, tx.Commit()
}

// accountInfo はアカウントの一覧・詳細に表示する項目です。
type accountInfo struct {
	ID             int64
	Name           string
	Role           string
	CreatedAt      time.Time
	DisabledAt     time.Time // 有効ならゼロ値
	LastLoginAt    time.Time // 未ログインならゼロ値
	ActivePasskeys int
	DisabledKeys   int
	TokenExpiresAt time.Time // 未使用の登録URLの期限。無ければゼロ値
	TokenUsedAt    time.Time // 最後に使われた登録URLの使用日時。無ければゼロ値
}

func (a accountInfo) Disabled() bool { return !a.DisabledAt.IsZero() }
func (a accountInfo) IsAdmin() bool  { return a.Role == roleAdmin }

// RoleLabel は画面に出すロールの名前です。
func (a accountInfo) RoleLabel() string { return roleLabel(a.Role) }

func roleLabel(role string) string {
	switch role {
	case roleAdmin:
		return "システム管理者"
	case roleMember:
		return "サイト管理者"
	}
	return "一般ユーザー"
}

func unixOrZero(v sql.NullInt64) time.Time {
	if !v.Valid {
		return time.Time{}
	}
	return time.Unix(v.Int64, 0).In(time.Local)
}

const accountInfoQuery = `SELECT a.id, a.name, a.role, a.created_at, a.disabled_at, a.last_login_at,
	(SELECT COUNT(*) FROM auth_credentials c WHERE c.account_id = a.id AND c.disabled_at IS NULL),
	(SELECT COUNT(*) FROM auth_credentials c WHERE c.account_id = a.id AND c.disabled_at IS NOT NULL),
	(SELECT MAX(t.expires_at) FROM auth_setup_tokens t WHERE t.account_id = a.id AND t.used_at IS NULL AND t.expires_at > ?),
	(SELECT MAX(t.used_at) FROM auth_setup_tokens t WHERE t.account_id = a.id)
	FROM auth_accounts a`

func scanAccountInfo(row rowScanner) (accountInfo, error) {
	var a accountInfo
	var created int64
	var disabled, lastLogin, tokenExp, tokenUsed sql.NullInt64
	if err := row.Scan(&a.ID, &a.Name, &a.Role, &created, &disabled, &lastLogin, &a.ActivePasskeys, &a.DisabledKeys, &tokenExp, &tokenUsed); err != nil {
		return accountInfo{}, err
	}
	a.CreatedAt = time.Unix(created, 0).In(time.Local)
	a.DisabledAt = unixOrZero(disabled)
	a.LastLoginAt = unixOrZero(lastLogin)
	a.TokenExpiresAt = unixOrZero(tokenExp)
	a.TokenUsedAt = unixOrZero(tokenUsed)
	return a, nil
}

// listAccounts はアカウントを作成順に返します。
func (s *adminStore) listAccounts(ctx context.Context) ([]accountInfo, error) {
	rows, err := s.db.QueryContext(ctx, accountInfoQuery+" ORDER BY a.id", s.now().Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []accountInfo
	for rows.Next() {
		a, err := scanAccountInfo(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, a)
	}
	return list, rows.Err()
}

func (s *adminStore) getAccount(ctx context.Context, id int64) (accountInfo, error) {
	a, err := scanAccountInfo(s.db.QueryRowContext(ctx, accountInfoQuery+" WHERE a.id = ?", s.now().Unix(), id))
	if errors.Is(err, sql.ErrNoRows) {
		return accountInfo{}, ErrNotFound
	}
	return a, err
}

// changeAccount は、他のアカウントのロール変更・停止・再開・削除を行います。
// 自分自身は変更できず、有効なシステム管理者が0人になる操作は1つの文の条件で拒否します。
// 変更したアカウントの管理者セッションは、同じトランザクションで削除します（AUTH-INV-2・AUTH-INV-6）。
func (s *adminStore) changeAccount(ctx context.Context, actorID, targetID int64, query string, args ...any) error {
	if actorID == targetID {
		return ErrSelfChange
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, query, args...)
	if err := affectedOne(res, err, ErrLastAdmin); err != nil {
		if errors.Is(err, ErrLastAdmin) {
			var exists int
			if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM auth_accounts WHERE id = ?", targetID).Scan(&exists); err != nil {
				return err
			}
			if exists == 0 {
				return ErrNotFound
			}
		}
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM auth_sessions WHERE account_id = ?", targetID); err != nil {
		return err
	}
	return tx.Commit()
}

// setRole は他のアカウントのロールを変えます。システム管理者を降格して有効なシステム管理者が0人になる場合は拒否します。
func (s *adminStore) setRole(ctx context.Context, actorID, targetID int64, role string) error {
	if !validRole(role) {
		return ErrInvalidInput
	}
	return s.changeAccount(ctx, actorID, targetID,
		"UPDATE auth_accounts SET role = ? WHERE id = ? AND (role != 'admin' OR ? = 'admin' OR "+effectiveAdminElsewhere+")",
		role, targetID, role, targetID)
}

// disableAccount は他のアカウントを停止します。
func (s *adminStore) disableAccount(ctx context.Context, actorID, targetID int64) error {
	return s.changeAccount(ctx, actorID, targetID,
		"UPDATE auth_accounts SET disabled_at = COALESCE(disabled_at, ?) WHERE id = ? AND (role != 'admin' OR "+effectiveAdminElsewhere+")",
		s.now().Unix(), targetID, targetID)
}

// enableAccount は停止中のアカウントを再開します。
func (s *adminStore) enableAccount(ctx context.Context, actorID, targetID int64) error {
	return s.changeAccount(ctx, actorID, targetID, "UPDATE auth_accounts SET disabled_at = NULL WHERE id = ?", targetID)
}

// deleteAccount は他のアカウントを削除します（パスキー・セッション・登録トークンも削除される）。
func (s *adminStore) deleteAccount(ctx context.Context, actorID, targetID int64) error {
	return s.changeAccount(ctx, actorID, targetID,
		"DELETE FROM auth_accounts WHERE id = ? AND (role != 'admin' OR "+effectiveAdminElsewhere+")",
		targetID, targetID)
}

// reissueToken は他のアカウント（または自分）の登録トークン（24時間）を発行し直します。
// resetKeys なら、そのアカウントのパスキーとセッションを削除し、WebAuthnの user.id を作り直します（紛失・盗難時）。
// 有効なシステム管理者が0人になる場合は、resetKeys を拒否します。
func (s *adminStore) reissueToken(ctx context.Context, targetID int64, resetKeys bool) (string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()
	var role string
	var disabled sql.NullInt64
	err = tx.QueryRowContext(ctx, "SELECT role, disabled_at FROM auth_accounts WHERE id = ?", targetID).Scan(&role, &disabled)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	} else if err != nil {
		return "", err
	}
	if disabled.Valid {
		return "", ErrSetupTokenInvalid
	}
	if resetKeys {
		if role == roleAdmin {
			var ok bool
			if err := tx.QueryRowContext(ctx, "SELECT "+effectiveAdminElsewhere, targetID).Scan(&ok); err != nil {
				return "", err
			}
			if !ok {
				return "", ErrLastAdmin
			}
		}
		if err := resetAccountKeys(ctx, tx, targetID); err != nil {
			return "", err
		}
	}
	token, err := issueSetupToken(ctx, tx, targetID, inviteTokenTTL, s.now())
	if err != nil {
		return "", err
	}
	return token, tx.Commit()
}

// resetAccountKeys は、アカウントのパスキー・セッション・登録トークンを削除し、WebAuthnの user.id を作り直します。
// 盗まれた認証器に残る古いパスキーを、確実に使えなくするためです。
func resetAccountKeys(ctx context.Context, x execer, accountID int64) error {
	userID, err := newWebAuthnUserID()
	if err != nil {
		return err
	}
	for _, q := range []string{
		"DELETE FROM auth_credentials WHERE account_id = ?",
		"DELETE FROM auth_sessions WHERE account_id = ?",
		"DELETE FROM auth_setup_tokens WHERE account_id = ?",
	} {
		if _, err := x.ExecContext(ctx, q, accountID); err != nil {
			return err
		}
	}
	_, err = x.ExecContext(ctx, "UPDATE auth_accounts SET webauthn_user_id = ? WHERE id = ?", userID, accountID)
	return err
}

// --- 初回登録・復旧・スキーマ（サーバーの実行ファイルのサブコマンドから呼ぶ） ---

// detectOldSchema は、アカウント導入前の形式の表が残っているかを調べます。
func detectOldSchema(ctx context.Context, db *sql.DB) (bool, error) {
	var settings int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'auth_settings'").Scan(&settings); err != nil {
		return false, err
	}
	if settings > 0 {
		return true, nil
	}
	var credentials, accountCol int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'auth_credentials'").Scan(&credentials); err != nil {
		return false, err
	}
	if credentials == 0 {
		return false, nil
	}
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM pragma_table_info('auth_credentials') WHERE name = 'account_id'").Scan(&accountCol); err != nil {
		return false, err
	}
	return accountCol == 0, nil
}

// ResetSchema は、管理者認証の表だけを削除して作り直します（古い形式から移るとき）。他の機能の表には触れません。
func ResetSchema(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, t := range []string{"auth_agent_tokens", "auth_agent_requests", "auth_agent_clients", "auth_oidc_requests", "auth_oidc_client_secrets", "auth_oidc_grants", "auth_oidc_clients", "auth_federated_subjects", "auth_setup_tokens", "auth_sessions", "auth_credentials", "auth_settings", "auth_accounts"} {
		if _, err := tx.ExecContext(ctx, "DROP TABLE IF EXISTS "+t); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return Migrate(db)
}

// firstAdmin は、最初のシステム管理者として使うアカウント（最も古いシステム管理者）を返し、無ければ作ります。
func firstAdmin(ctx context.Context, tx *sql.Tx, now time.Time) (int64, error) {
	var id int64
	err := tx.QueryRowContext(ctx, "SELECT id FROM auth_accounts WHERE role = 'admin' ORDER BY id LIMIT 1").Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	return insertAccount(ctx, tx, firstAdminName, roleAdmin, now)
}

// Setup は、有効なパスキーが1件も無い場合だけ、最初のシステム管理者アカウントの登録トークン（15分）を発行して返します。
// システム管理者アカウントが無ければ作り、あれば最も古いシステム管理者に結びつけます。登録前なら何度でも発行し直せます。
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
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM auth_credentials WHERE disabled_at IS NULL").Scan(&n); err != nil {
		return "", err
	}
	if n > 0 {
		return "", ErrAlreadySetUp
	}
	id, err := firstAdmin(ctx, tx, now)
	if err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE auth_accounts SET disabled_at = NULL WHERE id = ?", id); err != nil {
		return "", err
	}
	token, err := issueSetupToken(ctx, tx, id, setupTokenTTL, now)
	if err != nil {
		return "", err
	}
	return token, tx.Commit()
}

// RecoverOptions は auth-recover の指定です。
type RecoverOptions struct {
	// AccountID は登録し直すシステム管理者アカウントです。0なら、システム管理者アカウントが無い場合だけ新しく作ります。
	AccountID int64
	// Only なら、AccountID のアカウントだけを初期化します（他のアカウントは変えない）。
	Only bool
}

// Recover は、パスキー・セッション・登録トークンを削除し、登録し直すアカウントの WebAuthn の user.id を作り直してから、
// その登録トークン（15分）を発行して返します。停止中なら再開します。アカウント（呼び名・ロール）は残します。
// Only でなければ全アカウントの鍵を消します（乗っ取りが疑われるとき）。
func Recover(ctx context.Context, db *sql.DB, now time.Time, opt RecoverOptions) (string, error) {
	if err := Migrate(db); err != nil {
		return "", err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()
	id := opt.AccountID
	if id == 0 {
		if opt.Only {
			return "", ErrChooseAccount
		}
		var admins int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM auth_accounts WHERE role = 'admin'").Scan(&admins); err != nil {
			return "", err
		}
		if admins > 0 {
			return "", ErrChooseAccount
		}
		if id, err = insertAccount(ctx, tx, firstAdminName, roleAdmin, now); err != nil {
			return "", err
		}
	} else {
		var role string
		err := tx.QueryRowContext(ctx, "SELECT role FROM auth_accounts WHERE id = ?", id).Scan(&role)
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrNotFound
		} else if err != nil {
			return "", err
		}
		// 全体の復旧では、登録し直すのはシステム管理者に限る（他のアカウントは、そのシステム管理者が登録URLを再発行する）
		if !opt.Only && role != roleAdmin {
			return "", fmt.Errorf("%w: account %d is not an admin", ErrChooseAccount, id)
		}
	}
	if opt.Only {
		if err := resetAccountKeys(ctx, tx, id); err != nil {
			return "", err
		}
	} else {
		// 乗っ取りが疑われる全体の復旧では、全アカウントの連携先のログインも失効させる（AUTH-INV-17）
		for _, q := range []string{
			"DELETE FROM auth_credentials", "DELETE FROM auth_sessions", "DELETE FROM auth_setup_tokens",
			"UPDATE auth_federated_subjects SET account_epoch = account_epoch + 1 WHERE deleted_at IS NULL",
			"DELETE FROM auth_oidc_requests",
			"DELETE FROM auth_agent_requests",
			"DELETE FROM auth_agent_tokens",
		} {
			if _, err := tx.ExecContext(ctx, q); err != nil {
				return "", err
			}
		}
		if err := resetAccountKeys(ctx, tx, id); err != nil {
			return "", err
		}
	}
	if _, err := tx.ExecContext(ctx, "UPDATE auth_accounts SET disabled_at = NULL WHERE id = ?", id); err != nil {
		return "", err
	}
	token, err := issueSetupToken(ctx, tx, id, setupTokenTTL, now)
	if err != nil {
		return "", err
	}
	return token, tx.Commit()
}

// AccountSummary は、auth-recover で選ぶためのアカウントの一覧の1行です（呼び名は端末に表示するだけで、ログには出さない）。
type AccountSummary struct {
	ID       int64
	Name     string
	Role     string
	Disabled bool
}

// ListAccounts はアカウントの一覧を返します（auth-recover で選ぶため）。
func ListAccounts(ctx context.Context, db *sql.DB) ([]AccountSummary, error) {
	if err := Migrate(db); err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, "SELECT id, name, role, disabled_at IS NOT NULL FROM auth_accounts ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []AccountSummary
	for rows.Next() {
		var a AccountSummary
		if err := rows.Scan(&a.ID, &a.Name, &a.Role, &a.Disabled); err != nil {
			return nil, err
		}
		list = append(list, a)
	}
	return list, rows.Err()
}

// accountForRegistration は、ログイン中のアカウントが自分にパスキーを追加するときの登録先を返します。
func (s *adminStore) accountForRegistration(ctx context.Context, accountID int64) (registrationTarget, error) {
	var t registrationTarget
	err := s.db.QueryRowContext(ctx,
		"SELECT id, name, webauthn_user_id FROM auth_accounts WHERE id = ? AND disabled_at IS NULL", accountID,
	).Scan(&t.AccountID, &t.Name, &t.WebAuthnUserID)
	if errors.Is(err, sql.ErrNoRows) {
		return registrationTarget{}, ErrSessionInvalid
	}
	return t, err
}
