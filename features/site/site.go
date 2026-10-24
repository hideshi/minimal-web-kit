package site

import (
	"bytes"
	"database/sql"
	"embed"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"os"

	"minimal-web-kit/internal/web"
)

//go:embed views/*.html
var viewsFS embed.FS

// Feature はLP・ホームページテンプレートのモジュール実装です。
// DBテーブルを必要とせず、完全な静的配信・SSRで動作します。
type Feature struct {
	tmpl           *template.Template
	logger         *slog.Logger
	contactFormURL string
}

func NewFeature(logger *slog.Logger) *Feature {
	t, err := template.ParseFS(viewsFS, "views/layout.html", "views/index.html")
	if err != nil {
		panic(fmt.Sprintf("parse site views: %v", err))
	}
	contactURL := os.Getenv("CONTACT_FORM_URL")
	if contactURL == "" {
		contactURL = "https://forms.google.com"
	}
	return &Feature{
		tmpl:           t,
		logger:         logger,
		contactFormURL: contactURL,
	}
}

func (f *Feature) Name() string {
	return "site"
}

// Init はモジュールの初期化を行います。LPはDB保存データが不要なため何もしません。
func (f *Feature) Init(db *sql.DB) error {
	return nil
}

// RegisterRoutes はトップページ以外の追加ルート（もしあれば）を登録します。
func (f *Feature) RegisterRoutes(mux *http.ServeMux, sm *web.SessionManager) {
}

// HandleRoot はBaseのデフォルトトップ（GET /）をこのLP画面で置き換えます。
func (f *Feature) HandleRoot(w http.ResponseWriter, r *http.Request) {
	var buf bytes.Buffer
	data := map[string]any{
		"Title":          "超軽量・安全なWebひな形",
		"ContactFormURL": f.contactFormURL,
	}

	if err := f.tmpl.ExecuteTemplate(&buf, "layout", data); err != nil {
		f.logger.Error("render site template failed")
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = buf.WriteTo(w)
}
