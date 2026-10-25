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
	navLinks  []NavLink
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

// IsolatedMux は、CoreのCSRF検査とセッション解決を通さない独立経路を登録するためのものです。
// 独立経路の認証・CSRF・Origin検査は、登録した機能自身が実装します。
type IsolatedMux struct {
	routes []isolatedRoute
}

type isolatedRoute struct {
	pattern string
	handler http.Handler
}

func (m *IsolatedMux) Handle(pattern string, handler http.Handler) {
	m.routes = append(m.routes, isolatedRoute{pattern: pattern, handler: handler})
}

func (m *IsolatedMux) HandleFunc(pattern string, handler http.HandlerFunc) {
	m.Handle(pattern, handler)
}

// Routes はCoreのルーティングを構築し、登録されたFeatureのルートを合成します。
func (h *Handler) Routes(rootHandler http.HandlerFunc, registerFeatures func(mux *http.ServeMux, sm *SessionManager)) http.Handler {
	handler, err := h.RoutesWithIsolated(rootHandler, registerFeatures, nil)
	if err != nil {
		panic(err)
	}
	return handler
}

// RoutesWithIsolated は Routes に加えて、独立経路を合成します。
// 独立経路はCoreの経路より優先されます。CoreやFeatureの経路と同じパターンを登録した場合はエラーを返します。
func (h *Handler) RoutesWithIsolated(rootHandler http.HandlerFunc, registerFeatures func(mux *http.ServeMux, sm *SessionManager), registerIsolated func(mux *IsolatedMux)) (handler http.Handler, err error) {
	defer func() {
		// ServeMuxはパターンの衝突をpanicで通知するため、起動時エラーに変換する
		if rvr := recover(); rvr != nil {
			handler, err = nil, fmt.Errorf("register routes: %v", rvr)
		}
	}()

	appMux := http.NewServeMux()

	// ルートパス（GET /）：Featureが独自トップ画面を提供する場合はそれを使い、なければBaseデフォルト画面
	if rootHandler != nil {
		appMux.HandleFunc("GET /{$}", rootHandler)
	} else {
		appMux.HandleFunc("GET /{$}", h.handleIndex)
	}

	// Coreルート
	appMux.HandleFunc("GET /privacy", func(w http.ResponseWriter, r *http.Request) {
		h.handlePrivacy(w, r, rootHandler != nil)
	})
	appMux.HandleFunc("GET /account/delete-confirm", h.handleDeleteConfirm)
	appMux.HandleFunc("POST /account/delete-all", h.handleDeleteAll)

	// 各Feature（自作機能や追加モジュール）のルートを合成
	if registerFeatures != nil {
		registerFeatures(appMux, h.sm)
	}

	appHandler := CSRFProtect(h.sm)(appMux)
	sessionHandler := h.sm.ResolveOwner(appHandler)
	appHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Siteの公開ページは既存Cookieも照合せず、アクセス時刻も更新しない。
		if rootHandler != nil && (r.Method == http.MethodGet || r.Method == http.MethodHead) &&
			(r.URL.Path == "/" || r.URL.Path == "/privacy") {
			appMux.ServeHTTP(w, r)
			return
		}
		sessionHandler.ServeHTTP(w, r)
	})

	rootMux := http.NewServeMux()

	staticFS, err := fs.Sub(contentFS, "static")
	if err != nil {
		panic(err)
	}
	rootMux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))))
	rootMux.Handle("/", appHandler)

	if registerIsolated != nil {
		isolated := &IsolatedMux{}
		registerIsolated(isolated)
		for _, route := range isolated.routes {
			// Core・Featureの経路との衝突を検出するため、到達しない形でappMuxにも登録する
			appMux.Handle(route.pattern, http.NotFoundHandler())
			rootMux.Handle(route.pattern, route.handler)
		}
	}

	handler = AccessLog(h.logger)(rootMux)
	handler = MaxBytes(1024 * 1024)(handler)
	handler = SecurityHeaders(handler)
	handler = Recoverer(h.logger)(handler)
	handler = RequestID(handler)

	return handler, nil
}

// NavLink は、有効な機能が共通のナビゲーションに足すリンクです（例: CMSの「お知らせ」）。
type NavLink struct {
	Label string
	Href  string
}

// SetNavLinks は、Coreの画面のナビゲーションに表示する機能のリンクを設定します。起動時に1回だけ呼びます。
func (h *Handler) SetNavLinks(links []NavLink) {
	h.navLinks = links
}

func (h *Handler) Render(w http.ResponseWriter, page string, data any) {
	if m, ok := data.(map[string]any); ok {
		m["NavLinks"] = h.navLinks
	}
	tmpl, ok := h.templates[page]
	if !ok {
		h.logger.Error("template not found", slog.String("page", page))
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "layout", data); err != nil {
		h.logger.Error("template render failed", slog.String("page", page))
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = buf.WriteTo(w)
}

func (h *Handler) handleIndex(w http.ResponseWriter, r *http.Request) {
	ownerID := GetOwnerID(r.Context())
	maskedOwnerID := "（セッション未発行・Cookie不要閲覧中）"
	if len(ownerID) >= 8 {
		maskedOwnerID = string(ownerID[:8]) + "..."
	}

	h.Render(w, "index.html", map[string]any{
		"Title":         "minimal-web-kit ホーム",
		"MaskedOwnerID": maskedOwnerID,
		"CSRFToken":     GetCSRFToken(r.Context()),
	})
}

func (h *Handler) handlePrivacy(w http.ResponseWriter, r *http.Request, siteMode bool) {
	h.Render(w, "privacy.html", map[string]any{
		"Title":     "プライバシーポリシー",
		"SiteMode":  siteMode,
		"CSRFToken": GetCSRFToken(r.Context()),
	})
}

func (h *Handler) handleDeleteConfirm(w http.ResponseWriter, r *http.Request) {
	ownerID := GetOwnerID(r.Context())
	hasSession := ownerID != ""

	h.Render(w, "confirm_delete.html", map[string]any{
		"Title":      "セッション破棄の確認",
		"HasSession": hasSession,
		"CSRFToken":  GetCSRFToken(r.Context()),
	})
}

func (h *Handler) handleDeleteAll(w http.ResponseWriter, r *http.Request) {
	ownerID := GetOwnerID(r.Context())
	if ownerID != "" {
		if err := h.store.DeleteAllData(r.Context(), ownerID); err != nil {
			h.logger.Error("delete user data failed")
			http.Error(w, "データ削除に失敗しました", http.StatusInternalServerError)
			return
		}
	}

	h.sm.ClearSessionCookie(w)
	// ログ契約: 削除ログにも owner_id を残さない
	h.logger.Info("user data purged from active database")
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
