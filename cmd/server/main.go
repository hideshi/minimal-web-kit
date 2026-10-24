package main

import (
	"log/slog"
	"os"

	"minimal-web-kit/internal/app"
	"minimal-web-kit/internal/store"
)

func main() {
	// JSON構造化ロガー（slog）の初期化
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	cfg := app.LoadConfig()

	// SQLite永続化ストアの初期化
	s, err := store.NewSQLiteStore(cfg.DBPath)
	if err != nil {
		logger.Error("failed to initialize sqlite store", slog.Any("error", err))
		os.Exit(1)
	}
	defer s.Close()

	srv := app.NewServer(cfg, s, logger)
	if err := srv.Run(); err != nil {
		logger.Error("server exited with error", slog.Any("error", err))
		os.Exit(1)
	}
}
