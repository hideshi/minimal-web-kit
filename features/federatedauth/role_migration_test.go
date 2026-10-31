package federatedauth

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"minimal-web-kit/internal/store"
)

// 不変条件: AUTH-INV-27
func TestMigrate_ReplacesMemberAndKeepsSessions(t *testing.T) {
	ctx := context.Background()
	s, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "old.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	db := s.DB()
	if _, err := db.Exec(strings.ReplaceAll(migrationSQL, newRoleColumn, oldRoleColumn)); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		"INSERT INTO federated_accounts (id, issuer, sub, role, created_at) VALUES (1, 'i', 'a', 'member', 0), (2, 'i', 'b', 'admin', 0), (3, 'i', 'c', 'member', 0)",
		"INSERT INTO federated_sessions (token_hash, account_id, account_epoch, client_epoch, grant_epoch, auth_time, created_at, last_seen_at, expires_at) VALUES ('h', 1, '1', '1', '1', 0, 0, 0, 0)",
		"DELETE FROM federated_accounts WHERE id = 3",
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 { // 2回目は何もしない
		if err := migrate(ctx, db); err != nil {
			t.Fatal(err)
		}
	}
	count := func(q string) (n int) {
		t.Helper()
		if err := db.QueryRow(q).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if count("SELECT COUNT(*) FROM federated_accounts WHERE role = 'site_admin' AND id = 1") != 1 || count("SELECT COUNT(*) FROM federated_accounts WHERE role = 'admin'") != 1 {
		t.Fatal("roles were not carried over")
	}
	if count("SELECT COUNT(*) FROM federated_sessions") != 1 {
		t.Fatal("the session was lost")
	}
	if _, err := db.Exec("INSERT INTO federated_accounts (issuer, sub, created_at) VALUES ('i', 'd', 0)"); err != nil {
		t.Fatal(err)
	}
	if count("SELECT id FROM federated_accounts WHERE sub = 'd'") != 4 || count("SELECT COUNT(*) FROM federated_accounts WHERE sub = 'd' AND role = 'site_admin'") != 1 {
		t.Fatal("the id must not be reused and the default must be site_admin")
	}
}
