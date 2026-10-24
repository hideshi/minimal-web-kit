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
	store     store.SessionStore
	sm        *SessionManager
	templates map[string]*template.Template
	logger    *slog.Logger
}

func NewHandler(s store.SessionStore, sm *SessionManager, logger *slog.Logger) (*Handler, error) {
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

// Routes はCoreのルーティングを構築し、登録されたFeatureのルートを合成します。
func (h *Handler) Routes(rootHandler http.HandlerFunc, registerFeatures func(mux *http.ServeMux, sm *SessionManager)) http.Handler {
	appMux := http.NewServeMux()

	// ルートパス（GET /）：Featureが独自トップ画面を提供する場合はそれを使い、なければBaseデフォルト画面
	if rootHandler != nil {
		appMux.HandleFunc("GET /{$}", rootHandler)
	} else {
		appMux.HandleFunc("GET /{$}", h.handleIndex)
	}

	// Coreルート
	appMux.HandleFunc("GET /privacy", h.handlePrivacy)
	appMux.HandleFunc("GET /account/delete-confirm", h.handleDeleteConfirm)
	appMux.HandleFunc("POST /account/delete-all", h.handleDeleteAll)

	// 各Feature（自作機能や追加モジュール）のルートを合成
	if registerFeatures != nil {
		registerFeatures(appMux, h.sm)
	}

	appHandler := CSRFProtect(h.sm)(appMux)
	appHandler = h.sm.ResolveOwner(appHandler)

	rootMux := http.NewServeMux()

	staticFS, err := fs.Sub(contentFS, "static")
	if err != nil {
		panic(err)
	}
	rootMux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))))
	rootMux.Handle("/", appHandler)

	handler := AccessLog(h.logger)(rootMux)
	handler = MaxBytes(1024 * 1024)(handler)
	handler = SecurityHeaders(handler)
	handler = Recoverer(h.logger)(handler)
	handler = RequestID(handler)

	return handler
}

func (h *Handler) Render(w http.ResponseWriter, page string, data any) {
	tmpl, ok := h.templates[page]
	if !ok {
		h.logger.Error("template not found", slog.String("page", page))
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

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

	h.Render(w, "index.html", map[string]any{
		"Title":         "minimal-web-kit ホーム",
		"MaskedOwnerID": maskedOwnerID,
		"CSRFToken":     GetCSRFToken(r.Context()),
	})
}

func (h *Handler) handlePrivacy(w http.ResponseWriter, r *http.Request) {
	h.Render(w, "privacy.html", map[string]any{
		"Title":     "このアプリが保存するもの",
		"CSRFToken": GetCSRFToken(r.Context()),
	})
}

func (h *Handler) handleDeleteConfirm(w http.ResponseWriter, r *http.Request) {
	h.Render(w, "confirm_delete.html", map[string]any{
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
