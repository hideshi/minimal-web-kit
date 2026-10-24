package web

import (
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"

	"minimal-web-kit/internal/store"
)

//go:embed views/*.html static/*
var contentFS embed.FS

type Handler struct {
	store     store.Store
	sm        *SessionManager
	templates map[string]*template.Template
	logger    *slog.Logger
}

func NewHandler(s store.Store, sm *SessionManager, logger *slog.Logger) (*Handler, error) {
	tmplMap := make(map[string]*template.Template)

	pages := []string{"index.html", "privacy.html"}
	for _, page := range pages {
		t, err := template.ParseFS(contentFS, "views/layout.html", "views/"+page)
		if err != nil {
			return nil, fmt.Errorf("parse template %q: %w", page, err)
		}
		tmplMap[page] = t
	}

	return &Handler{
		store:     s,
		sm:        sm,
		templates: tmplMap,
		logger:    logger,
	}, nil
}

func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()

	// 静的アセット配信
	staticFS, err := fs.Sub(contentFS, "static")
	if err != nil {
		panic(err)
	}
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))))

	// アプリケーションルート
	mux.HandleFunc("GET /{$}", h.handleIndex)
	mux.HandleFunc("GET /privacy", h.handlePrivacy)
	mux.HandleFunc("POST /account/delete-all", h.handleDeleteAll)

	// ミドルウェアスタックの合成
	handler := CSRFProtect(h.sm)(mux)
	handler = h.sm.ResolveOwner(handler)
	handler = AccessLog(h.logger)(handler)
	handler = MaxBytes(1024 * 1024)(handler) // 1MB制限
	handler = SecurityHeaders(handler)
	handler = Recoverer(h.logger)(handler)
	handler = RequestID(handler)

	return handler
}

func (h *Handler) handleIndex(w http.ResponseWriter, r *http.Request) {
	ownerID := GetOwnerID(r.Context())
	maskedOwnerID := ""
	if len(ownerID) >= 8 {
		maskedOwnerID = string(ownerID[:8]) + "..."
	}

	data := map[string]any{
		"Title":         "minimal-web-kit ホーム",
		"MaskedOwnerID": maskedOwnerID,
		"CSRFToken":     GetCSRFToken(r.Context()),
	}

	if err := h.templates["index.html"].ExecuteTemplate(w, "layout", data); err != nil {
		h.logger.Error("render index failed", slog.Any("error", err))
	}
}

func (h *Handler) handlePrivacy(w http.ResponseWriter, r *http.Request) {
	data := map[string]any{
		"Title":     "このアプリが保存するもの",
		"CSRFToken": GetCSRFToken(r.Context()),
	}

	if err := h.templates["privacy.html"].ExecuteTemplate(w, "layout", data); err != nil {
		h.logger.Error("render privacy failed", slog.Any("error", err))
	}
}

func (h *Handler) handleDeleteAll(w http.ResponseWriter, r *http.Request) {
	ownerID := GetOwnerID(r.Context())
	if err := h.store.DeleteAllData(r.Context(), ownerID); err != nil {
		h.logger.Error("delete all data failed", slog.String("owner_id", string(ownerID)), slog.Any("error", err))
		http.Error(w, "データ削除に失敗しました", http.StatusInternalServerError)
		return
	}

	h.sm.ClearSessionCookie(w)
	h.logger.Info("user data completely purged", slog.String("owner_id", string(ownerID)))
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
