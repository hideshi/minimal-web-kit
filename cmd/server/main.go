package main

import (
	"log/slog"
	"os"

	"minimal-web-kit/internal/app"
	"minimal-web-kit/internal/notify"
	"minimal-web-kit/internal/store"
	"minimal-web-kit/internal/web"
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

	// 複製の検査は、DB_PATH のDBを開かずに行う
	if len(os.Args) > 1 && os.Args[1] == "verify-backup" {
		os.Exit(runVerifyBackup(os.Args[2:], os.Stdout, os.Stderr))
	}

	cfg := app.LoadConfig()

	// バックアップは、機能の初期化（表の作成や移行）を走らせずに行う
	if len(os.Args) > 1 && os.Args[1] == "backup" {
		os.Exit(runBackup(os.Args[2:], cfg.DBPath, os.Stdout, os.Stderr))
	}

	// DBを読むだけのコマンド（例: records-check）は、Store の初期化（ファイルの作成、WAL の設定、Core の表の作成）の前に行う
	if len(os.Args) > 1 {
		if code, ok := runReadOnlyCommand(os.Args[1:], setupFeatures(nil, logger), cfg.DBPath, os.Stdout, os.Stderr); ok {
			os.Exit(code)
		}
	}

	s, err := store.NewSQLiteStore(cfg.DBPath)
	if err != nil {
		logger.Error("failed to initialize sqlite store")
		os.Exit(1)
	}
	defer s.Close()

	// サーバーとして起動するときだけ、サイトの名前を確かめ、運営者への通知を用意する。
	// 通知はログから出来事を数えるので、機能が logger を受け取る前に包む（docs/domain/notify.md）。
	var notifier *notify.Notifier
	if len(os.Args) == 1 {
		// サイトの名前（SITE_NAME）が不正なら、値をログに出さずに起動を止める
		if err := web.CheckSiteName(); err != nil {
			logger.Error("site name invalid", "reason", err.Error())
			os.Exit(1)
		}
		// エラーの文は固定で、URL もファイルのパスも含まない（NOTIFY-INV-3）
		notifier, err = notify.New(cfg.NotifyWebhookURLFile, web.ConfiguredSiteName())
		if err != nil {
			logger.Error("notify setup failed", "reason", err.Error())
			os.Exit(1)
		}
		if notifier != nil {
			logger = slog.New(notifier.Handler(logger.Handler()))
			slog.SetDefault(logger)
		}
	}

	// 有効化された機能モジュール（Feature）の登録
	featuresRegistry := setupFeatures(s.DB(), logger)

	// 引数があれば、機能が提供するサブコマンド（例: auth-setup）を実行して終了する
	if len(os.Args) > 1 {
		code := runCommand(os.Args[1:], featuresRegistry, s.DB(), os.Stdin, os.Stdout, os.Stderr)
		_ = s.Close() // 終了の直前で、コマンドの結果は code で返す
		os.Exit(code)
	}

	srv := app.NewServer(cfg, s, featuresRegistry, logger)
	if notifier != nil {
		notifier.Start(logger)
	}
	err = srv.Run()
	if notifier != nil {
		notifier.Stop() // まとめ待ちの件数を送ってから止める。失敗で戻ったときも送る
	}
	if err != nil {
		logger.Error("server exited with error")
		os.Exit(1)
	}
}
