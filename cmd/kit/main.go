package main

import (
	"fmt"
	"io"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		return
	}

	cmd := os.Args[1]
	switch cmd {
	case "add-memo":
		if err := applyMemo(); err != nil {
			fmt.Fprintf(os.Stderr, "Error applying memo template: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Successfully applied memo template! Run 'go test ./...' or 'go run ./cmd/server' to try it.")
	case "clean-base":
		if err := applyBase(); err != nil {
			fmt.Fprintf(os.Stderr, "Error restoring clean base: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Successfully restored clean base! The codebase is now completely free of sample features.")
	default:
		printUsage()
	}
}

func printUsage() {
	fmt.Println("minimal-web-kit CLI tool")
	fmt.Println()
	fmt.Println("Usage:")
	fmt.Println("  go run ./cmd/kit <command>")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  add-memo    Apply private memo CRUD template as a working reference")
	fmt.Println("  clean-base  Restore clean base (remove all sample CRUD features)")
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

func applyMemo() error {
	files := map[string]string{
		"templates/memo/memo.go.tmpl":        "internal/domain/memo.go",
		"templates/memo/001_init.sql.tmpl":   "internal/store/migrations/001_init.sql",
		"templates/memo/store.go.tmpl":       "internal/store/store.go",
		"templates/memo/memory.go.tmpl":      "internal/store/memory.go",
		"templates/memo/sqlite.go.tmpl":      "internal/store/sqlite.go",
		"templates/memo/handler.go.tmpl":     "internal/web/handler.go",
		"templates/memo/index.html.tmpl":     "internal/web/views/index.html",
		"templates/memo/detail.html.tmpl":    "internal/web/views/detail.html",
		"templates/memo/DATA.md.tmpl":        "DATA.md",
		"templates/memo/canary_test.go.tmpl": "test/canary_test.go",
	}

	for src, dst := range files {
		if err := copyFile(src, dst); err != nil {
			return fmt.Errorf("copy %s -> %s: %w", src, dst, err)
		}
	}
	return nil
}

func applyBase() error {
	files := map[string]string{
		"templates/base/DATA.md.tmpl":        "DATA.md",
		"templates/base/001_init.sql.tmpl":   "internal/store/migrations/001_init.sql",
		"templates/base/store.go.tmpl":       "internal/store/store.go",
		"templates/base/memory.go.tmpl":      "internal/store/memory.go",
		"templates/base/sqlite.go.tmpl":      "internal/store/sqlite.go",
		"templates/base/handler.go.tmpl":     "internal/web/handler.go",
		"templates/base/index.html.tmpl":     "internal/web/views/index.html",
		"templates/base/privacy.html.tmpl":   "internal/web/views/privacy.html",
		"templates/base/canary_test.go.tmpl": "test/canary_test.go",
	}

	for src, dst := range files {
		if err := copyFile(src, dst); err != nil {
			return fmt.Errorf("copy %s -> %s: %w", src, dst, err)
		}
	}

	// 不要になったサンプル固有ファイルを削除
	_ = os.Remove("internal/domain/memo.go")
	_ = os.Remove("internal/web/views/detail.html")

	return nil
}
