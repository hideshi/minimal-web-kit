package main

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"

	"minimal-web-kit/features"
	"minimal-web-kit/internal/app"
	"minimal-web-kit/internal/store"
)

// runExportStatic は、Siteの公開ページを静的ファイルとして書き出します（例: minimal-web-kit export-static dist）。
func runExportStatic(args []string, registry *features.Registry, logger *slog.Logger, out, errOut io.Writer) int {
	if len(args) != 1 || args[0] == "" {
		fmt.Fprintln(errOut, "Usage: minimal-web-kit export-static <output-directory>")
		return 2
	}
	if err := app.ExportStatic(registry, args[0], logger); err != nil {
		fmt.Fprintf(errOut, "Error: %v\n", err)
		return 1
	}
	fmt.Fprintf(out, "Exported static files to %s\n", args[0])
	return 0
}

// runBackup は、DB_PATH のDBの整合した複製を作り、検査します（例: minimal-web-kit backup /var/backups/app.db）。
// 機能の初期化より前に動かし、バックアップのためにDBへ書き込みません。DBが無ければ新しく作らずに失敗します。
func runBackup(args []string, dbPath string, out, errOut io.Writer) int {
	if len(args) != 1 || args[0] == "" {
		fmt.Fprintln(errOut, "Usage: minimal-web-kit backup <file>")
		return 2
	}
	if _, err := os.Stat(dbPath); err != nil {
		fmt.Fprintln(errOut, "Error: the database file was not found (check DB_PATH)")
		return 1
	}
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: dbPath, RawQuery: "_pragma=busy_timeout(5000)"}).String())
	if err != nil {
		fmt.Fprintf(errOut, "Error: %v\n", err)
		return 1
	}
	defer db.Close()
	ctx := context.Background()
	if err := store.Backup(ctx, db, args[0]); err != nil {
		fmt.Fprintf(errOut, "Error: %v\n", err)
		return 1
	}
	tables, err := store.VerifyBackup(ctx, args[0])
	if err != nil {
		fmt.Fprintf(errOut, "Error: the backup failed verification: %v\n", err)
		return 1
	}
	fmt.Fprintf(out, "Backup written to %s (%d tables, verified)\n", args[0], len(tables))
	return 0
}

// runVerifyBackup は、複製が壊れていないことを確かめ、表ごとの行数を表示します。中身は表示しません。
func runVerifyBackup(args []string, out, errOut io.Writer) int {
	if len(args) != 1 || args[0] == "" {
		fmt.Fprintln(errOut, "Usage: minimal-web-kit verify-backup <file>")
		return 2
	}
	tables, err := store.VerifyBackup(context.Background(), args[0])
	if err != nil {
		fmt.Fprintf(errOut, "Error: %v\n", err)
		return 1
	}
	for _, t := range tables {
		fmt.Fprintf(out, "%-32s %d\n", t.Name, t.Rows)
	}
	fmt.Fprintln(out, "OK")
	return 0
}

// runCommand は、有効な機能が提供するサブコマンド（例: auth-setup）を実行し、終了コードを返します。
// 本番サーバーにGoを入れずに、サーバーと同じ実行ファイル・同じ DB_PATH で運用操作を行うためのものです。
func runCommand(args []string, registry *features.Registry, db *sql.DB, in io.Reader, out, errOut io.Writer) int {
	var commands []features.Command
	for _, f := range registry.All() {
		if cf, ok := f.(features.CommandFeature); ok {
			commands = append(commands, cf.Commands()...)
		}
	}
	for _, c := range commands {
		if c.Name == args[0] {
			if err := c.Run(context.Background(), db, args[1:], in, out); err != nil {
				fmt.Fprintf(errOut, "Error: %v\n", err)
				return 1
			}
			return 0
		}
	}
	fmt.Fprintf(errOut, "Unknown command. Run without arguments to start the server, or use export-static <dir>, backup <file>, verify-backup <file>.\n")
	if len(commands) == 0 {
		fmt.Fprintln(errOut, "No commands are available. Commands are provided by enabled features (e.g. make add-auth).")
		return 2
	}
	fmt.Fprintln(errOut, "Commands:")
	for _, c := range commands {
		fmt.Fprintf(errOut, "  %-12s %s\n", c.Name, c.Summary)
	}
	return 2
}
