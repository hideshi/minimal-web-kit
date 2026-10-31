package auth

import (
	"context"
	"database/sql"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"minimal-web-kit/internal/store"
)

// oldRolesDB は、一般ユーザーが入る前の形式のファイルのDBを作ります。
func oldRolesDB(t *testing.T) (*sql.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "old.db")
	s, err := store.NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if _, err := s.DB().Exec(strings.ReplaceAll(migrationSQL, newRoleList, oldRoleList)); err != nil {
		t.Fatal(err)
	}
	return s.DB(), path
}

// 不変条件: AUTH-INV-27
func TestMigrateRoles_KeepsAccountsAndTheirData(t *testing.T) {
	ctx := context.Background()
	db, path := oldRolesDB(t)
	s := newAdminStore(db, (&fixedClock{}).now)
	if err := insertForTest(db, "'user'"); err == nil {
		t.Fatal("the old schema must refuse the new role")
	}
	admin := newAccount(t, s, "管理", roleAdmin)
	member := newAccount(t, s, "編集", roleMember)
	last := newAccount(t, s, "最後", roleMember)
	if err := s.addCredential(ctx, member, testCredential("key"), "PC"); err != nil {
		t.Fatal(err)
	}
	newSession(t, s, member)
	if _, err := issueSetupToken(ctx, db, member, inviteTokenTTL, s.now()); err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `INSERT INTO auth_federated_subjects (sub, account_id) VALUES ('sub-member', ?)`, member)
	client, err := NewFederationStore(db).CreateAgentClient(ctx, "接続", "http://localhost/callback")
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `INSERT INTO auth_agent_tokens (token_hash, request_id, client_id, sub, resource, scope, account_epoch, client_epoch, grant_epoch, created_at, expires_at)
		VALUES ('h', 'r', ?, 'sub-member', 'x', 'x', '1', '1', '1', 0, 0)`, client.ID)
	mustExec(t, db, "DELETE FROM auth_accounts WHERE id = ?", last)

	before := dumpAccounts(t, db)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	if after := dumpAccounts(t, db); after != before {
		t.Fatalf("accounts changed:\n%s\n%s", before, after)
	}
	for _, table := range []string{"auth_credentials", "auth_sessions", "auth_setup_tokens"} {
		assertRows(t, db, "SELECT COUNT(*) FROM "+table+" WHERE account_id = "+strconv.FormatInt(member, 10), 1)
	}
	assertRows(t, db, "SELECT COUNT(*) FROM auth_federated_subjects WHERE account_id = "+strconv.FormatInt(member, 10), 1)
	assertRows(t, db, "SELECT COUNT(*) FROM sqlite_master WHERE type = 'trigger' AND tbl_name = 'auth_accounts'", 6)
	assertRows(t, db, "SELECT COUNT(*) FROM sqlite_master WHERE name = 'auth_accounts_new'", 0)

	// 削除済みのIDを再利用しない
	next := newAccount(t, s, "次", roleUser)
	if next <= last {
		t.Fatalf("id %d reused (deleted %d)", next, last)
	}
	// 外部キーは有効のまま、トリガーも働く
	var fk int
	if err := db.QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil || fk != 1 {
		t.Fatalf("foreign_keys = %d, %v", fk, err)
	}
	mustExec(t, db, "UPDATE auth_accounts SET disabled_at = 1 WHERE id = ?", member)
	assertRows(t, db, "SELECT COUNT(*) FROM auth_agent_tokens", 0)
	mustExec(t, db, "DELETE FROM auth_accounts WHERE id = ?", member)
	assertRows(t, db, "SELECT COUNT(*) FROM auth_credentials", 0)
	if _, err := s.db.Exec("UPDATE auth_accounts SET role = 'other' WHERE id = ?", admin); err == nil {
		t.Fatal("unknown roles must be refused")
	}

	// 2回目は何もしない。複製は移行の1回分だけ
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	backups, err := filepath.Glob(filepath.Join(filepath.Dir(path), "pre-migration-auth-roles-*.db"))
	if err != nil || len(backups) != 1 {
		t.Fatalf("backups = %v, %v", backups, err)
	}
}

// 不変条件: AUTH-INV-27
func TestMigrateRoles_NewDatabaseIsNotRebuilt(t *testing.T) {
	_, db := newTestDB(t)
	path := ""
	var seq int
	var name string
	if err := db.QueryRow("PRAGMA database_list").Scan(&seq, &name, &path); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	backups, err := filepath.Glob(filepath.Join(filepath.Dir(path), "pre-migration-*"))
	if err != nil || len(backups) != 0 {
		t.Fatalf("a new database must not be rebuilt: %v, %v", backups, err)
	}
}

func insertForTest(db *sql.DB, role string) error {
	_, err := db.Exec(`INSERT INTO auth_accounts (name, role, webauthn_user_id, created_at) VALUES ('x', ` + role + `, x'01', 0)`)
	return err
}

func dumpAccounts(t *testing.T, db *sql.DB) string {
	t.Helper()
	rows, err := db.Query("SELECT id, name, role, hex(webauthn_user_id), created_at, disabled_at, last_login_at FROM auth_accounts ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var id, created int64
		var name, role, key string
		var disabled, last sql.NullInt64
		if err := rows.Scan(&id, &name, &role, &key, &created, &disabled, &last); err != nil {
			t.Fatal(err)
		}
		b.WriteString(strings.Join([]string{strconv.FormatInt(id, 10), name, role, key, strconv.FormatInt(created, 10), strconv.FormatInt(disabled.Int64, 10), strconv.FormatInt(last.Int64, 10)}, "|") + "\n")
	}
	return b.String()
}
