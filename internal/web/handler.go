package web

import (
	"bytes"
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

	pages := []string{"index.html", "privacy.html", "confirm_delete.html"}
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
	// アプリケーション用Mux
	appMux := http.NewServeMux()
	appMux.HandleFunc("GET /{$}", h.handleIndex)
	appMux.HandleFunc("GET /privacy", h.handlePrivacy)
	appMux.HandleFunc("GET /account/delete-confirm", h.handleDeleteConfirm)
	appMux.HandleFunc("POST /account/delete-all", h.handleDeleteAll)

	// アプリケーション用ミドルウェア（セッション解決、CSRF検証を含む）
	appHandler := CSRFProtect(h.sm)(appMux)
	appHandler = h.sm.ResolveOwner(appHandler)

	// ルートMux（静的アセットはセッション解決の外側に配置し、無駄なDB書き込みを防止）
	rootMux := http.NewServeMux()

	staticFS, err := fs.Sub(contentFS, "static")
	if err != nil {
		panic(err)
	}
	rootMux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))))
	rootMux.Handle("/", appHandler)

	// 全体に適用する基本ミドルウェア（外側から順に実行）
	handler := AccessLog(h.logger)(rootMux)
	handler = MaxBytes(1024 * 1024)(handler) // 1MB制限
	handler = SecurityHeaders(handler)
	handler = Recoverer(h.logger)(handler)
	handler = RequestID(handler)

	return handler
}

func (h *Handler) render(w http.ResponseWriter, page string, data any) {
	tmpl, ok := h.templates[page]
	if !ok {
		h.logger.Error("template not found", slog.String("page", page))
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	// レンダリングエラー時に壊れたHTMLが出力されないよう、バッファに描画してから書き込む
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "layout", data); err != nil {
		h.logger.Error("template render failed", slog.String("page", page), slog.Any("error", err))
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = buf.WriteTo(w)
}

func (h *Handler) handleIndex(w http.ResponseWriter, r *http.Request) {
	ownerID := GetOwnerID(r.Context())
	maskedOwnerID := ""
	if len(ownerID) >= 8 {
		maskedOwnerID = string(ownerID[:8]) + "..."
	}

	h.render(w, "index.html", map[string]any{
		"Title":         "minimal-web-kit ホーム",
		"MaskedOwnerID": maskedOwnerID,
		"CSRFToken":     GetCSRFToken(r.Context()),
	})
}

func (h *Handler) handlePrivacy(w http.ResponseWriter, r *http.Request) {
	h.render(w, "privacy.html", map[string]any{
		"Title":     "このアプリが保存するもの",
		"CSRFToken": GetCSRFToken(r.Context()),
	})
}

func (h *Handler) handleDeleteConfirm(w http.ResponseWriter, r *http.Request) {
	h.render(w, "confirm_delete.html", map[string]any{
		"Title":     "セッション完全破棄の確認",
		"CSRFToken": GetCSRFToken(r.Context()),
	})
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
