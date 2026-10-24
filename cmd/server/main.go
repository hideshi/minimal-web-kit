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
	slog.SetDefault(logger)

	cfg := app.LoadConfig()

	s, err := store.NewSQLiteStore(cfg.DBPath)
	if err != nil {
		logger.Error("failed to initialize sqlite store")
		os.Exit(1)
	}
	defer s.Close()

	// 有効化された機能モジュール（Feature）の登録
	featuresRegistry := setupFeatures(s.DB(), logger)

	srv := app.NewServer(cfg, s, featuresRegistry, logger)
	if err := srv.Run(); err != nil {
		logger.Error("server exited with error")
		os.Exit(1)
	}
}
