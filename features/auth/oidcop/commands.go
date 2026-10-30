package oidcop

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"io"

	"minimal-web-kit/features"
	"minimal-web-kit/features/auth"
)

// Commands は、管理者認証のコマンドに、連携先と利用許可を管理するコマンドを足します。
// 秘密値は生成時に標準出力へ1度だけ表示します。ログには出しません。
//
//	minimal-web-kit oidc-client -create -label <名前> -redirect <URL>
//	minimal-web-kit oidc-client -client <ID> -disable | -enable | -rotate [-compromised]
//	minimal-web-kit oidc-grant  -client <ID> -account <ID> [-revoke]
//	minimal-web-kit oidc-revoke-all -yes
func (f *Feature) Commands() []features.Command {
	return append(f.Feature.Commands(),
		features.Command{Name: "oidc-client", Summary: "Manage connected systems (-create -label -redirect, or -client <ID> with -disable, -enable, -rotate, -compromised)", Run: runClient},
		features.Command{Name: "oidc-grant", Summary: "Allow an account to use a connected system (-client <ID> -account <ID> [-revoke])", Run: runGrant},
		features.Command{Name: "oidc-revoke-all", Summary: "End every login at every connected system (after a signing key leak or a restore; -yes)", Run: runRevokeAll},
	)
}

func newStore(db *sql.DB) (*auth.FederationStore, error) {
	if err := auth.Migrate(db); err != nil {
		return nil, err
	}
	return auth.NewFederationStore(db), nil
}

func runClient(ctx context.Context, db *sql.DB, args []string, _ io.Reader, out io.Writer) error {
	fs := flag.NewFlagSet("oidc-client", flag.ContinueOnError)
	fs.SetOutput(out)
	create := fs.Bool("create", false, "register a connected system")
	label := fs.String("label", "", "name shown on the login screen")
	redirect := fs.String("redirect", "", "exact HTTPS callback URL")
	client := fs.String("client", "", "client ID")
	disable := fs.Bool("disable", false, "disable the client and end its logins")
	enable := fs.Bool("enable", false, "enable the client again")
	rotate := fs.Bool("rotate", false, "issue a new secret (the old one stays valid for 24 hours)")
	compromised := fs.Bool("compromised", false, "with -rotate: invalidate the old secret and end logins now")
	if err := fs.Parse(args); err != nil {
		return err
	}
	s, err := newStore(db)
	if err != nil {
		return err
	}
	switch {
	case *create:
		c, secret, err := s.CreateClient(ctx, *label, *redirect)
		if err != nil {
			return err
		}
		fmt.Fprintln(out, "client_id:", c.ID)
		fmt.Fprintln(out, "client_secret:", secret)
		fmt.Fprintln(out, "The secret is shown only this once. Save it in a file only its owner can read.")
		return nil
	case *disable && *client != "":
		return s.SetClientEnabled(ctx, *client, false)
	case *enable && *client != "":
		return s.SetClientEnabled(ctx, *client, true)
	case *rotate && *client != "":
		secret, err := s.RotateSecret(ctx, *client, *compromised)
		if err != nil {
			return err
		}
		fmt.Fprintln(out, "client_secret:", secret)
		return nil
	}
	return fmt.Errorf("choose -create, or -client <ID> with -disable, -enable or -rotate")
}

func runGrant(ctx context.Context, db *sql.DB, args []string, _ io.Reader, out io.Writer) error {
	fs := flag.NewFlagSet("oidc-grant", flag.ContinueOnError)
	fs.SetOutput(out)
	client := fs.String("client", "", "client ID")
	account := fs.Int64("account", 0, "account ID")
	revoke := fs.Bool("revoke", false, "take the permission away")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *client == "" || *account == 0 {
		return fmt.Errorf("-client and -account are required")
	}
	s, err := newStore(db)
	if err != nil {
		return err
	}
	if *revoke {
		return s.Revoke(ctx, *client, *account)
	}
	subject, err := s.Grant(ctx, *client, *account)
	if err != nil {
		return err
	}
	fmt.Fprintln(out, "sub:", subject)
	return nil
}

func runRevokeAll(ctx context.Context, db *sql.DB, args []string, _ io.Reader, out io.Writer) error {
	fs := flag.NewFlagSet("oidc-revoke-all", flag.ContinueOnError)
	fs.SetOutput(out)
	yes := fs.Bool("yes", false, "confirm")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !*yes {
		return fmt.Errorf("this ends every login at every connected system; run again with -yes")
	}
	s, err := newStore(db)
	if err != nil {
		return err
	}
	return s.RevokeAll(ctx)
}
