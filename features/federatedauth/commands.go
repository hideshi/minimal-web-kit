package federatedauth

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"minimal-web-kit/features"
)

// Commands は、利用者のロールと停止を管理するコマンドを返します。
// 認証基盤に通信しません。付与しても、実際の要求では認証基盤の有効性を必ず確かめます。
//
//	minimal-web-kit federated-account -sub <識別子> -role admin|site_admin
//	minimal-web-kit federated-account -sub <識別子> -disable | -enable | -delete
func (f *Feature) Commands() []features.Command {
	return []features.Command{{
		Name:    "federated-account",
		Summary: "Set the role of a user who has logged in once (-sub <ID> with -role, -disable, -enable or -delete)",
		Run:     runAccount,
	}}
}

func runAccount(ctx context.Context, db *sql.DB, args []string, _ io.Reader, out io.Writer) error {
	fs := flag.NewFlagSet("federated-account", flag.ContinueOnError)
	fs.SetOutput(out)
	subject := fs.String("sub", "", "user identifier issued by the identity provider")
	role := fs.String("role", "", "admin or site_admin")
	disable := fs.Bool("disable", false, "disable the user here and end their sessions")
	enable := fs.Bool("enable", false, "enable the user again")
	remove := fs.Bool("delete", false, "delete the local user")
	if err := fs.Parse(args); err != nil {
		return err
	}
	issuer := os.Getenv("OIDC_ISSUER")
	if *subject == "" || issuer == "" {
		return fmt.Errorf("-sub and the OIDC_ISSUER setting are required")
	}
	if err := migrate(ctx, db); err != nil {
		return err
	}
	s := &sessionStore{db: db, issuer: issuer, now: time.Now}
	switch {
	case *role != "":
		return s.setRole(ctx, *subject, *role)
	case *disable:
		return s.setDisabled(ctx, *subject, true)
	case *enable:
		return s.setDisabled(ctx, *subject, false)
	case *remove:
		return s.deleteAccount(ctx, *subject)
	}
	return fmt.Errorf("choose -role, -disable, -enable or -delete")
}
