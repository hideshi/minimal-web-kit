package main

import (
	"database/sql"
	"log/slog"

	"minimal-web-kit/features"
	// [FEATURE_IMPORTS]
)

// setupFeatures はアプリケーションに組み込む機能モジュール（Feature）を登録します。
// 自作機能を追加する場合は、ここに r.Register(自作機能) を追記します。
func setupFeatures(db *sql.DB, logger *slog.Logger) *features.Registry {
	r := features.NewRegistry()

	// [FEATURE_REGISTRATIONS]

	return r
}
