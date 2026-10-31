package auth

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"minimal-web-kit/features"
)

// Commands は、サーバーの実行ファイルから実行できる管理者認証の運用コマンドを返します。
//
//	minimal-web-kit auth-setup    有効なパスキーが無いときだけ、最初の管理者の登録URLを表示する
//	minimal-web-kit auth-recover  パスキーとセッションを削除し、管理者の登録URLを表示する
//	    -account <ID>   登録し直す管理者アカウント（全体の復旧で、管理者がいる場合は必須）
//	    -only <ID>      そのアカウントだけを初期化する（他のアカウントは変えない）
//	    -reset-schema   古い形式の表を作り直す（管理者認証の表だけを削除する）
//	    -yes            確認の入力を省く
func (f *Feature) Commands() []features.Command {
	return []features.Command{
		{Name: "auth-setup", Summary: "Print a one-time URL to register the first admin passkey (only while no active passkey exists)", Run: runSetup},
		{Name: "auth-recover", Summary: "Delete passkeys and sessions, then print a registration URL (-account <ID>, -only <ID>, -reset-schema, -yes)", Run: runRecover},
		{Name: "agent-client", Summary: "Manage AI agent clients (-create -label -redirect, or -client <ID> with -grant -account <ID>, -revoke -account <ID>, -disable, -enable)", Run: runAgentClient},
	}
}

func printSetupURL(out io.Writer, cfg Config, token string) {
	if cfg.AllowSynced {
		fmt.Fprintln(out, "Open this URL within 15 minutes and register a passkey (synced passkeys are allowed by AUTH_ALLOW_SYNCED_PASSKEYS):")
	} else {
		fmt.Fprintln(out, "Open this URL within 15 minutes and register a passkey (synced passkeys are rejected):")
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, "  "+SetupURL(cfg.Origin, token))
	fmt.Fprintln(out)
	fmt.Fprintln(out, "The URL can be used only once. Do not share it, paste it into chats, or leave it on a shared screen.")
}

func oldSchemaError(err error) error {
	if errors.Is(err, ErrOldSchema) {
		return fmt.Errorf("the admin authentication tables use an old format; run auth-recover -reset-schema to recreate them (passkeys must be registered again)")
	}
	return err
}

// runSetup は、有効なパスキーが無い場合だけ、最初の管理者の登録URLを表示します。
func runSetup(ctx context.Context, db *sql.DB, args []string, in io.Reader, out io.Writer) error {
	cfg, err := LoadConfig(os.Getenv)
	if err != nil {
		return err
	}
	token, err := Setup(ctx, db, time.Now())
	if errors.Is(err, ErrAlreadySetUp) {
		return fmt.Errorf("passkeys are already registered; add accounts from the admin screen, or run auth-recover if every admin lost their passkeys")
	} else if err != nil {
		return oldSchemaError(err)
	}
	printSetupURL(out, cfg, token)
	return nil
}

func confirm(in io.Reader, out io.Writer, message string) error {
	fmt.Fprint(out, message+"\nType 'recover' to continue: ")
	line, _ := bufio.NewReader(in).ReadString('\n')
	if strings.TrimSpace(line) != "recover" {
		return fmt.Errorf("aborted; nothing was changed")
	}
	return nil
}

// runRecover は確認のうえで、パスキー・セッション・登録トークンを削除し、管理者の登録URLを表示します。
// アカウント（呼び名・ロール）と、他の機能のデータ（お知らせの記事等）は残します。
func runRecover(ctx context.Context, db *sql.DB, args []string, in io.Reader, out io.Writer) error {
	cfg, err := LoadConfig(os.Getenv)
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("auth-recover", flag.ContinueOnError)
	fs.SetOutput(out)
	accountID := fs.Int64("account", 0, "admin account to register again")
	only := fs.Int64("only", 0, "reset only this account")
	resetSchema := fs.Bool("reset-schema", false, "recreate the admin authentication tables")
	yes := fs.Bool("yes", false, "skip the confirmation")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *resetSchema {
		if !*yes {
			if err := confirm(in, out, "This DROPS the admin authentication tables (accounts, passkeys, sessions). Other features' data is kept."); err != nil {
				return err
			}
		}
		if err := ResetSchema(ctx, db); err != nil {
			return err
		}
		token, err := Setup(ctx, db, time.Now())
		if err != nil {
			return err
		}
		fmt.Fprintln(out, "The admin authentication tables were recreated.")
		printSetupURL(out, cfg, token)
		return nil
	}

	opt := RecoverOptions{AccountID: *accountID}
	message := "This deletes ALL passkeys, admin sessions and registration URLs of every account. Accounts and other features' data are kept."
	if *only != 0 {
		opt = RecoverOptions{AccountID: *only, Only: true}
		message = fmt.Sprintf("This deletes the passkeys, sessions and registration URLs of account %d only.", *only)
	}
	if opt.AccountID == 0 {
		// 管理者がいれば、登録し直す管理者を選んでもらう（呼び名は端末に表示するだけ）
		list, err := ListAccounts(ctx, db)
		if err != nil {
			return oldSchemaError(err)
		}
		var admins []AccountSummary
		for _, a := range list {
			if a.Role == roleAdmin {
				admins = append(admins, a)
			}
		}
		if len(admins) > 0 {
			fmt.Fprintln(out, "Choose the admin account to register again with -account <ID>:")
			for _, a := range admins {
				state := ""
				if a.Disabled {
					state = " (disabled; it will be re-enabled)"
				}
				fmt.Fprintf(out, "  %d  %s%s\n", a.ID, a.Name, state)
			}
			return ErrChooseAccount
		}
	}
	if !*yes {
		if err := confirm(in, out, message); err != nil {
			return err
		}
	}
	token, err := Recover(ctx, db, time.Now(), opt)
	if err != nil {
		return oldSchemaError(err)
	}
	fmt.Fprintln(out, "Passkeys and sessions were deleted.")
	printSetupURL(out, cfg, token)
	return nil
}

// runAgentClient は、AIエージェントの公開クライアントと利用許可を管理します。秘密値はありません。
func runAgentClient(ctx context.Context, db *sql.DB, args []string, _ io.Reader, out io.Writer) error {
	fs := flag.NewFlagSet("agent-client", flag.ContinueOnError)
	fs.SetOutput(out)
	create := fs.Bool("create", false, "register an AI agent client")
	label := fs.String("label", "", "name shown on the consent screen")
	redirect := fs.String("redirect", "", "loopback callback URL without a port (http://localhost/callback)")
	client := fs.String("client", "", "client ID")
	grant := fs.Bool("grant", false, "allow -account to use the client")
	revoke := fs.Bool("revoke", false, "take the permission away from -account")
	account := fs.Int64("account", 0, "account ID")
	disable := fs.Bool("disable", false, "disable the client and end its tokens")
	enable := fs.Bool("enable", false, "enable the client again")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := Migrate(db); err != nil {
		return err
	}
	s := NewFederationStore(db)
	switch {
	case *create:
		c, err := s.CreateAgentClient(ctx, *label, *redirect)
		if err != nil {
			return err
		}
		fmt.Fprintln(out, "client_id:", c.ID)
		return nil
	case *client == "":
		return errors.New("choose -create, or -client <ID>")
	case *grant && *account != 0:
		_, err := s.Grant(ctx, *client, *account)
		return err
	case *revoke && *account != 0:
		return s.Revoke(ctx, *client, *account)
	case *disable:
		return s.SetClientEnabled(ctx, *client, false)
	case *enable:
		return s.SetClientEnabled(ctx, *client, true)
	}
	return errors.New("with -client, choose -grant, -revoke, -disable or -enable")
}
