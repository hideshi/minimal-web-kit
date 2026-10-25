package main

import (
	"context"
	"database/sql"
	"fmt"
	"io"

	"minimal-web-kit/features"
)

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
	fmt.Fprintf(errOut, "Unknown command. Run without arguments to start the server.\n")
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
