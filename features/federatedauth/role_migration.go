package federatedauth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"minimal-web-kit/internal/store"
)

const (
	oldRoleColumn = "DEFAULT 'member' CHECK (role IN ('admin', 'member'))"
	newRoleColumn = "DEFAULT 'site_admin' CHECK (role IN ('admin', 'site_admin'))"
)

// migrate は表を今の形式にします。旧称の member は site_admin に写します。
// 不変条件: AUTH-INV-27
func migrate(ctx context.Context, db *sql.DB) error {
	err := store.RebuildTable(ctx, db, store.Rebuild{
		Table: "federated_accounts",
		Label: "federated-roles",
		Current: func(definition string) bool {
			return strings.Contains(definition, newRoleColumn)
		},
		Define: func(definition string) (string, error) {
			if strings.Count(definition, oldRoleColumn) != 1 {
				return "", errors.New("federatedauth migration: unexpected definition of federated_accounts")
			}
			return strings.Replace(definition, oldRoleColumn, newRoleColumn, 1), nil
		},
		Columns: map[string]string{"role": "CASE role WHEN 'member' THEN 'site_admin' ELSE role END"},
		After:   migrationSQL,
	})
	if err != nil {
		return fmt.Errorf("federatedauth role migration: %w", err)
	}
	_, err = db.ExecContext(ctx, migrationSQL)
	return err
}
