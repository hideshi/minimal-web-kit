package auth

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"minimal-web-kit/features"
)

// Commands は、サーバーの実行ファイルから実行できるCMSの運用コマンドを返します。
//
//	minimal-web-kit auth-setup          パスキーが未登録のときだけ、初回登録用のURLを表示する
//	minimal-web-kit auth-recover [-yes] パスキー・管理者セッションを全削除し、登録用のURLを再発行する
func (f *Feature) Commands() []features.Command {
	return []features.Command{
		{Name: "auth-setup", Summary: "Print a one-time URL to register the first CMS passkey", Run: runSetup},
		{Name: "auth-recover", Summary: "Delete all CMS passkeys and admin sessions, then print a new registration URL (-yes skips the confirmation)", Run: runRecover},
	}
}

func printSetupURL(out io.Writer, origin, token string) {
	fmt.Fprintln(out, "Open this URL within 15 minutes and register a passkey (synced passkeys are rejected):")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "  "+SetupURL(origin, token))
	fmt.Fprintln(out)
	fmt.Fprintln(out, "The URL can be used only once. Do not share it, paste it into chats, or leave it on a shared screen.")
}

// runSetup は、パスキーが未登録の場合だけ、初回登録用のURLを表示します。
func runSetup(ctx context.Context, db *sql.DB, args []string, in io.Reader, out io.Writer) error {
	cfg, err := LoadConfig(os.Getenv)
	if err != nil {
		return err
	}
	token, err := Setup(ctx, db, time.Now())
	if errors.Is(err, ErrAlreadySetUp) {
		return fmt.Errorf("passkeys are already registered; add passkeys from the admin screen, or run auth-recover if you lost them")
	} else if err != nil {
		return err
	}
	printSetupURL(out, cfg.Origin, token)
	return nil
}

// runRecover は確認のうえで、パスキー・管理者セッション・登録トークンを削除し、新しい登録URLを表示します。記事は残します。
func runRecover(ctx context.Context, db *sql.DB, args []string, in io.Reader, out io.Writer) error {
	cfg, err := LoadConfig(os.Getenv)
	if err != nil {
		return err
	}
	if !slices.Contains(args, "-yes") {
		fmt.Fprint(out, "This deletes ALL registered CMS passkeys and admin sessions. Posts are kept.\nType 'recover' to continue: ")
		line, _ := bufio.NewReader(in).ReadString('\n')
		if strings.TrimSpace(line) != "recover" {
			return fmt.Errorf("aborted; nothing was changed")
		}
	}
	token, err := Recover(ctx, db, time.Now())
	if err != nil {
		return err
	}
	fmt.Fprintln(out, "All passkeys and admin sessions were deleted.")
	printSetupURL(out, cfg.Origin, token)
	return nil
}
