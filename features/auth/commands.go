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
	}
}

func printSetupURL(out io.Writer, origin, token string) {
	fmt.Fprintln(out, "Open this URL within 15 minutes and register a passkey (synced passkeys are rejected):")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "  "+SetupURL(origin, token))
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
	printSetupURL(out, cfg.Origin, token)
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
		printSetupURL(out, cfg.Origin, token)
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
	printSetupURL(out, cfg.Origin, token)
	return nil
}
