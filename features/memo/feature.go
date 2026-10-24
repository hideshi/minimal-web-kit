package memo

import (
	"database/sql"
	_ "embed"
	"fmt"
	"log/slog"
	"net/http"

	"minimal-web-kit/internal/web"
)

//go:embed migration.sql
var migrationSQL string

// Feature はメモ機能のモジュール実装です。
type Feature struct {
	store   *Store
	handler *Handler
	logger  *slog.Logger
}

func NewFeature(logger *slog.Logger) *Feature {
	return &Feature{
		logger: logger,
	}
}

func (f *Feature) Name() string {
	return "memo"
}

func (f *Feature) Init(db *sql.DB) error {
	// 外部キーCASCADEを有効化してマイグレーションを実行
	if _, err := db.Exec(migrationSQL); err != nil {
		return fmt.Errorf("run memo migration: %w", err)
	}
	f.store = NewStore(db)
	h, err := NewHandler(f.store, f.logger)
	if err != nil {
		return fmt.Errorf("init memo handler: %w", err)
	}
	f.handler = h
	return nil
}

func (f *Feature) RegisterRoutes(mux *http.ServeMux, sm *web.SessionManager) {
	if f.handler == nil {
		panic("memo feature not initialized")
	}

	mux.HandleFunc("GET /memos", f.handler.HandleList)
	mux.HandleFunc("POST /memos", f.handler.HandleCreate)
	mux.HandleFunc("GET /memos/{id}", f.handler.HandleDetail)
	mux.HandleFunc("POST /memos/{id}", f.handler.HandleUpdate)
	mux.HandleFunc("GET /memos/{id}/delete-confirm", f.handler.HandleDeleteConfirm)
	mux.HandleFunc("POST /memos/{id}/delete", f.handler.HandleDelete)
	mux.HandleFunc("GET /memos/export", f.handler.HandleExport)
}
