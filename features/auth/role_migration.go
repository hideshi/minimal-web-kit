package auth

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"minimal-web-kit/internal/store"
)

const newRoleList = "('admin', 'site_admin', 'user')"

// oldRoleLists は、過去の版のロールの CHECK です。
var oldRoleLists = []string{"('admin', 'member', 'user')", "('admin', 'member')"}

// migrateRoles は、ロールの CHECK を今の形式にします。旧称の member は site_admin に写します。
// 新規のDBと移行済みのDBでは何もしません。
// 不変条件: AUTH-INV-27
func migrateRoles(ctx context.Context, db *sql.DB) error {
	return store.RebuildTable(ctx, db, store.Rebuild{
		Table: "auth_accounts",
		Label: "auth-roles",
		Current: func(definition string) bool {
			return strings.Contains(definition, newRoleList)
		},
		Define: func(definition string) (string, error) {
			for _, old := range oldRoleLists {
				if strings.Count(definition, old) == 1 {
					return strings.Replace(definition, old, newRoleList, 1), nil
				}
			}
			return "", errors.New("auth migration: unexpected definition of auth_accounts")
		},
		Columns: map[string]string{"role": "CASE role WHEN 'member' THEN 'site_admin' ELSE role END"},
		After:   migrationSQL,
	})
}
