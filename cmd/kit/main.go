package main

import (
	"fmt"
	"os"
	"strings"
)

const memoDataMdSection = `
### テーブル: ` + "`" + `memos` + "`" + `
利用者が作成したプライベートメモ。

| 列名 | 型 | NULL許可 | 保存目的 | 保持期間 | 備考 |
|---|---|---|---|---|---|
| ` + "`" + `id` + "`" + ` | TEXT | NO | メモの一意識別 | 削除時またはセッションGC時 | 128ビット以上のランダムID |
| ` + "`" + `owner_id` + "`" + ` | TEXT | NO | 所有者への紐付け | 同上 | ` + "`" + `owners.id` + "`" + ` への外部キー（ON DELETE CASCADE） |
| ` + "`" + `title` + "`" + ` | TEXT | NO | メモの件名表示 | 同上 | 最大100文字 |
| ` + "`" + `content` + "`" + ` | TEXT | NO | メモの本文表示 | 同上 | 最大10,000文字。ログへの出力禁止 |
| ` + "`" + `created_at` + "`" + ` | INTEGER | NO | 作成日時の表示 | 同上 | UNIXエポック秒 |
| ` + "`" + `updated_at` + "`" + ` | INTEGER | NO | 更新日時の表示 | 同上 | UNIXエポック秒 |
`

func main() {
	if len(os.Args) < 2 {
		printUsage()
		return
	}

	cmd := os.Args[1]
	switch cmd {
	case "add-memo":
		if err := addMemo(); err != nil {
			fmt.Fprintf(os.Stderr, "Error enabling memo feature: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Successfully enabled memo feature! Run 'go test ./...' or 'go run ./cmd/server' to try it.")
	case "remove-memo":
		if err := removeMemo(); err != nil {
			fmt.Fprintf(os.Stderr, "Error disabling memo feature: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Successfully disabled memo feature! The app is now back to clean base without affecting any custom features.")
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
	fmt.Println("  add-memo     Enable private memo feature module in cmd/server/features.go and DATA.md")
	fmt.Println("  remove-memo  Disable private memo feature module without touching custom features")
}

func addMemo() error {
	// 1. cmd/server/features.go に登録行を追記
	featuresGoBytes, err := os.ReadFile("cmd/server/features.go")
	if err != nil {
		return fmt.Errorf("read features.go: %w", err)
	}
	content := string(featuresGoBytes)

	if !strings.Contains(content, `"minimal-web-kit/features/memo"`) {
		content = strings.Replace(content,
			"// [FEATURE_IMPORTS]",
			"\"minimal-web-kit/features/memo\"\n\t// [FEATURE_IMPORTS]",
			1)
	}
	if !strings.Contains(content, "memo.NewFeature") {
		content = strings.Replace(content,
			"// [FEATURE_REGISTRATIONS]",
			"r.Register(memo.NewFeature(logger))\n\t// [FEATURE_REGISTRATIONS]",
			1)
	}
	if err := os.WriteFile("cmd/server/features.go", []byte(content), 0644); err != nil {
		return fmt.Errorf("write features.go: %w", err)
	}

	// 2. DATA.md に memos セクションを追記
	dataMdBytes, err := os.ReadFile("DATA.md")
	if err != nil {
		return fmt.Errorf("read DATA.md: %w", err)
	}
	dataMd := string(dataMdBytes)

	if !strings.Contains(dataMd, "### テーブル: `memos`") {
		dataMd = strings.TrimRight(dataMd, "\n") + "\n" + memoDataMdSection
		if err := os.WriteFile("DATA.md", []byte(dataMd), 0644); err != nil {
			return fmt.Errorf("write DATA.md: %w", err)
		}
	}

	return nil
}

func removeMemo() error {
	// 1. cmd/server/features.go から memo の登録を解除
	featuresGoBytes, err := os.ReadFile("cmd/server/features.go")
	if err != nil {
		return fmt.Errorf("read features.go: %w", err)
	}
	content := string(featuresGoBytes)

	content = strings.Replace(content, "\t\"minimal-web-kit/features/memo\"\n", "", 1)
	content = strings.Replace(content, "\tr.Register(memo.NewFeature(logger))\n", "", 1)

	if err := os.WriteFile("cmd/server/features.go", []byte(content), 0644); err != nil {
		return fmt.Errorf("write features.go: %w", err)
	}

	// 2. DATA.md から memos セクションを削除
	dataMdBytes, err := os.ReadFile("DATA.md")
	if err != nil {
		return fmt.Errorf("read DATA.md: %w", err)
	}
	dataMd := string(dataMdBytes)

	if strings.Contains(dataMd, "### テーブル: `memos`") {
		idx := strings.Index(dataMd, "### テーブル: `memos`")
		// そのテーブル定義ブロックのみを除去
		nextSection := strings.Index(dataMd[idx:], "\n## ")
		if nextSection != -1 {
			dataMd = dataMd[:idx] + dataMd[idx+nextSection:]
		} else {
			dataMd = dataMd[:idx]
		}
		if err := os.WriteFile("DATA.md", []byte(strings.TrimRight(dataMd, "\n")+"\n"), 0644); err != nil {
			return fmt.Errorf("write DATA.md: %w", err)
		}
	}

	return nil
}
