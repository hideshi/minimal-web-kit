package main

import (
	"log/slog"
	"os"

	"minimal-web-kit/internal/app"
	"minimal-web-kit/internal/store"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	if len(os.Args) > 1 {
		// サブコマンドの実行時は、表示する登録URL等にログが混ざらないよう、エラーだけを標準エラーに出す
		logger = slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	}
	slog.SetDefault(logger)

	// 静的ファイルへの書き出しは、DBを開かずに行う
	if len(os.Args) > 1 && os.Args[1] == "export-static" {
		os.Exit(runExportStatic(os.Args[2:], setupFeatures(nil, logger), logger, os.Stdout, os.Stderr))
	}

	cfg := app.LoadConfig()

	s, err := store.NewSQLiteStore(cfg.DBPath)
	if err != nil {
		logger.Error("failed to initialize sqlite store")
		os.Exit(1)
	}
	defer s.Close()

	// 有効化された機能モジュール（Feature）の登録
	featuresRegistry := setupFeatures(s.DB(), logger)

	// 引数があれば、機能が提供するサブコマンド（例: auth-setup）を実行して終了する
	if len(os.Args) > 1 {
		code := runCommand(os.Args[1:], featuresRegistry, s.DB(), os.Stdin, os.Stdout, os.Stderr)
		s.Close()
		os.Exit(code)
	}

	srv := app.NewServer(cfg, s, featuresRegistry, logger)
	if err := srv.Run(); err != nil {
		logger.Error("server exited with error")
		os.Exit(1)
	}
}
