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
	"minimal-web-kit/internal/web/form"
)

//go:embed views/*.html static/*
var contentFS embed.FS

type Handler struct {
	store     store.SessionStore
	sm        *SessionManager
	templates map[string]*template.Template
	logger    *slog.Logger
	navLinks  []NavLink
	layout    *Layout
	privacy   []PrivacyNotice
}

// corePages はCoreが描画する画面です。
var corePages = []string{"index.html", "privacy.html", "confirm_delete.html"}

func NewHandler(s store.SessionStore, sm *SessionManager, logger *slog.Logger) (*Handler, error) {
	tmplMap := make(map[string]*template.Template)

	for _, page := range corePages {
		t, err := ParseWithComponents(contentFS, "views/layout.html", "views/"+page)
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

// Patterns は登録された経路のパターン（例: "GET /admin/news/{id}"）を登録順に返します。
// 全画面の検査など、テストで経路を列挙するために使います。
func (m *IsolatedMux) Patterns() []string {
	patterns := make([]string, len(m.routes))
	for i, r := range m.routes {
		patterns[i] = r.pattern
	}
	return patterns
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
	static := http.StripPrefix("/static/", http.FileServer(http.FS(staticFS)))
	rootMux.Handle("GET /static/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		PublicCache(w, staticCacheSeconds)
		static.ServeHTTP(w, r)
	}))
	// /favicon.ico を直接取りに来るブラウザ・クローラ向けに、PNGのアイコンを返す（404のログを増やさない）
	rootMux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		PublicCache(w, staticCacheSeconds)
		http.ServeFileFS(w, r, staticFS, "img/favicon-32.png")
	})
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

// NavLink は、有効な機能が共通のナビゲーションに足すリンクです（例: お知らせ機能の「お知らせ」）。
type NavLink struct {
	Label string
	Href  string
}

// PrivacyNotice は、機能が保存する情報を、プライバシーポリシーの画面で説明するための節です。
type PrivacyNotice struct {
	Title string // 節の見出し（例: "管理画面のアカウントで保存する情報"）
	Lead  string // 節の説明（任意）
	Rows  []PrivacyRow
}

// PrivacyRow は、PrivacyNotice の表の1行です。内容は docs/data-inventory.md と揃えます。
type PrivacyRow struct {
	Kind, Items, Purpose, Retention string
}

// SetPrivacyNotices は、プライバシーポリシーの画面に載せる、機能が保存する情報を設定します。起動時に1回だけ呼びます。
func (h *Handler) SetPrivacyNotices(notices []PrivacyNotice) {
	h.privacy = notices
}

// SetNavLinks は、Coreの画面のナビゲーションに表示する機能のリンクを設定します。起動時に1回だけ呼びます。
func (h *Handler) SetNavLinks(links []NavLink) {
	h.navLinks = links
}

// Layout は、サイト全体のヘッダー・フッターを持つレイアウトです（例: Siteが提供する）。
// 設定すると、Coreの画面や他の機能の公開ページも、このレイアウトの中に本文を差し込んで描画します。
type Layout struct {
	FS   fs.FS
	File string // {{define "layout"}} を含むファイル。本文は {{template "content" .}} で差し込む
	// Data は、レイアウトが必要とする値（ナビのリンク、公開URL、既定の説明文等）を data に加えます。
	// 画面が設定済みの値は上書きしません。
	Data func(data map[string]any)
}

// ParsePage は、共通の画面部品・レイアウト・本文のテンプレートを1つに解析します。
func ParsePage(layout Layout, contentFS fs.FS, contentFiles ...string) (*template.Template, error) {
	t, err := ParseWithComponents(layout.FS, layout.File)
	if err != nil {
		return nil, err
	}
	return t.ParseFS(contentFS, contentFiles...)
}

// SetLayout は、Coreの画面をサイト全体のレイアウトで描画するようにします。起動時に1回だけ呼びます。
func (h *Handler) SetLayout(layout Layout) error {
	for _, page := range corePages {
		t, err := ParsePage(layout, contentFS, "views/"+page)
		if err != nil {
			return fmt.Errorf("parse template %q with layout: %w", page, err)
		}
		h.templates[page] = t
	}
	h.layout = &layout
	return nil
}

// componentFuncs は部品に値を渡すための関数。confirm は削除の確認の操作の並び（confirm-actions）の値を作る:
// {{template "confirm-actions" (confirm "/admin/x/1/delete" "/admin/x/1" .CSRFToken)}}
var componentFuncs = template.FuncMap{
	"confirm": func(action, back, csrfToken string) form.Confirm {
		return form.Confirm{Action: action, Back: back, CSRFToken: csrfToken}
	},
}

// ParseWithComponents は、共通の画面部品（views/components.html の notice・error・pagination・field・choices・
// confirm-actions・combobox）と、機能のテンプレートを1つにまとめて解析します。機能は自分の embed.FS とファイル名を渡します。
func ParseWithComponents(fsys fs.FS, patterns ...string) (*template.Template, error) {
	t, err := template.New("components").Funcs(componentFuncs).ParseFS(contentFS, "views/components.html")
	if err != nil {
		return nil, err
	}
	return t.ParseFS(fsys, patterns...)
}

func (h *Handler) Render(w http.ResponseWriter, page string, data any) {
	if m, ok := data.(map[string]any); ok {
		m["NavLinks"] = h.navLinks
		if h.layout != nil && h.layout.Data != nil {
			h.layout.Data(m)
		}
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
	if siteMode {
		// Siteの /privacy はセッションを照合せず、誰が開いても同じ内容のため、キャッシュしてよい
		PublicCache(w, PublicCacheSeconds)
	}
	h.Render(w, "privacy.html", map[string]any{
		"Title":         "プライバシーポリシー",
		"Description":   "このサイトで保存する情報、アクセスの記録、お問い合わせで提供される情報の扱いと、免責事項について説明します。",
		"CanonicalPath": "/privacy",
		"SiteMode":      siteMode,
		"Notices":       h.privacy,
		"CSRFToken":     GetCSRFToken(r.Context()),
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
