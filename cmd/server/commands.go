package main

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"

	"minimal-web-kit/features"
	"minimal-web-kit/internal/app"
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

// runCommand は、有効な機能が提供するサブコマンド（例: cms-setup）を実行し、終了コードを返します。
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
	fmt.Fprintf(errOut, "Unknown command. Run without arguments to start the server, or use export-static <dir>.\n")
	if len(commands) == 0 {
		fmt.Fprintln(errOut, "No commands are available. Commands are provided by enabled features (e.g. make add-cms).")
		return 2
	}
	fmt.Fprintln(errOut, "Commands:")
	for _, c := range commands {
		fmt.Fprintf(errOut, "  %-12s %s\n", c.Name, c.Summary)
	}
	return 2
}
