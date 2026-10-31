package auth

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	"minimal-web-kit/internal/store"
)

const (
	oldRoleList = "('admin', 'member')"
	newRoleList = "('admin', 'member', 'user')"
)

// accountsSQL は auth_accounts の定義を返します。表が無ければ空文字です。
func accountsSQL(ctx context.Context, q interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}) (string, error) {
	var text string
	err := q.QueryRowContext(ctx, "SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'auth_accounts'").Scan(&text)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return text, err
}

// migrateRoles は、ロールの CHECK に一般ユーザーが無い表を作り直します。新規のDBと移行済みのDBでは何もしません。
//
// features/contact/migrate.go の手順は写せません。auth_credentials 等が ON DELETE CASCADE で参照しているため、
// 外部キーが有効なまま表を落とすと、パスキーとログイン状態が消えます。SQLiteの公式の手順に従い、
// 接続を1本に固定して、トランザクションの外で外部キーを止めます。
// 表を落とすとトリガーも消えるので、同じトランザクションで migration.sql を流して作り直します。
// 採番は、削除済みのIDを再利用しないよう、旧表の値を引き継ぎます。
// 不変条件: AUTH-INV-27
func migrateRoles(ctx context.Context, db *sql.DB) error {
	current, err := accountsSQL(ctx, db)
	if err != nil {
		return err
	}
	if current == "" || strings.Contains(current, newRoleList) {
		return nil
	}
	path, err := store.BackupBeforeMigration(ctx, db, "auth-roles")
	if err != nil {
		return fmt.Errorf("backup before migration: %w", err)
	}
	if path != "" {
		slog.Info("auth migration: backup written", slog.String("file", filepath.Base(path)))
	}

	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := setForeignKeys(ctx, conn, false); err != nil {
		return err
	}
	defer func() {
		if err := setForeignKeys(context.Background(), conn, true); err != nil {
			// 外部キーの止まった接続をプールへ返さない
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
	}()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()
	if err := rebuildAccounts(ctx, conn); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return err
	}
	committed = true
	return nil
}

// setForeignKeys は接続の外部キーの設定を変え、読み返して確かめます。トランザクションの中では効きません。
func setForeignKeys(ctx context.Context, conn *sql.Conn, on bool) error {
	want := 0
	if on {
		want = 1
	}
	if _, err := conn.ExecContext(ctx, fmt.Sprintf("PRAGMA foreign_keys = %d", want)); err != nil {
		return err
	}
	var got int
	if err := conn.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&got); err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("foreign_keys is %d, want %d", got, want)
	}
	return nil
}

func rebuildAccounts(ctx context.Context, conn *sql.Conn) error {
	current, err := accountsSQL(ctx, conn) // 別のプロセスが先に移行した場合に備えて、書き込みの権利を得てから読み直す
	if err != nil {
		return err
	}
	if current == "" || strings.Contains(current, newRoleList) {
		return nil
	}
	const head = "CREATE TABLE auth_accounts"
	if !strings.HasPrefix(current, head) || strings.Count(current, oldRoleList) != 1 {
		return errors.New("auth migration: unexpected definition of auth_accounts")
	}
	create := "CREATE TABLE auth_accounts_new" + strings.TrimPrefix(strings.Replace(current, oldRoleList, newRoleList, 1), head)

	var seq sql.NullInt64
	err = conn.QueryRowContext(ctx, "SELECT seq FROM sqlite_sequence WHERE name = 'auth_accounts'").Scan(&seq)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	for _, q := range []string{
		create,
		"INSERT INTO auth_accounts_new SELECT * FROM auth_accounts",
		"DROP TABLE auth_accounts",
		"ALTER TABLE auth_accounts_new RENAME TO auth_accounts",
		"DELETE FROM sqlite_sequence WHERE name = 'auth_accounts'",
	} {
		if _, err := conn.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	if seq.Valid {
		if _, err := conn.ExecContext(ctx, "INSERT INTO sqlite_sequence (name, seq) VALUES ('auth_accounts', ?)", seq.Int64); err != nil {
			return err
		}
	}
	if _, err := conn.ExecContext(ctx, migrationSQL); err != nil { // 表と一緒に消えたトリガーを作り直す
		return err
	}
	return checkAccountReferences(ctx, conn)
}

// checkAccountReferences は、アカウントを指す行が全て親を持つことを確かめます。
func checkAccountReferences(ctx context.Context, conn *sql.Conn) error {
	var broken int
	err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM pragma_foreign_key_check WHERE parent = 'auth_accounts'").Scan(&broken)
	if err != nil {
		return err
	}
	if broken > 0 {
		return fmt.Errorf("auth migration: %d rows lost their account", broken)
	}
	return nil
}
