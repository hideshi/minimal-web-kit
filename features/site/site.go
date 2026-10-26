// minimal-web-kit: bundled-site-template
package site

import (
	"bytes"
	"database/sql"
	"embed"
	"encoding/xml"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"

	"minimal-web-kit/internal/web"
)

//go:embed views/*.html
var viewsFS embed.FS

// Feature はLP・ホームページテンプレートのモジュール実装です。
// DBテーブルを必要とせず、完全な静的配信・SSRで動作します。
type Feature struct {
	pages          map[string]*template.Template
	logger         *slog.Logger
	contactFormURL string
	siteURL        string
	navLinks       []web.NavLink
}

const (
	// description は検索結果とSNSのカードに表示する説明文です。
	description = "個人情報の取得と依存関係を最小限に抑える、Go製のWebサイト・業務アプリの開発基盤。直接依存はSQLiteドライバのみ、Node.js/npm不要。LP・お知らせCMSのテンプレートとAI開発スキルを同梱。"
	// ogImagePath はSNSのカード用画像（1200×630px）の配信パスです。
	ogImagePath = "/static/img/ogp.png"
	ogImageAlt  = "minimal-web-kit：個人情報と依存を最小限に抑えるWeb開発基盤"
)

func NewFeature(logger *slog.Logger) *Feature {
	pages := map[string]*template.Template{}
	for _, page := range []string{"index.html", "notfound.html"} {
		t, err := template.ParseFS(viewsFS, "views/layout.html", "views/"+page)
		if err != nil {
			panic(fmt.Sprintf("parse site views: %v", err))
		}
		pages[page] = t
	}
	contactURL := contactFormURLFromEnv()
	if contactURL == "" {
		// 設定値は記録せず、未設定・不正の区別のみを固定メッセージで通知する
		logger.Warn("contact form url is not configured; contact button is hidden")
	}
	siteURL, ok := siteURLFromEnv()
	if !ok {
		logger.Warn("site url is invalid; canonical, OGP and sitemap are disabled")
	}
	return &Feature{
		pages:          pages,
		logger:         logger,
		contactFormURL: contactURL,
		siteURL:        siteURL,
	}
}

// siteURLFromEnv は SITE_URL（公開するサイトのURL）を検証し、末尾の / を除いて返します。
// canonical・OGP・sitemap.xml の絶対URLに使います。未設定なら空文字、不正なら ok=false を返します。
func siteURLFromEnv() (string, bool) {
	raw := os.Getenv("SITE_URL")
	if raw == "" {
		return "", true
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil ||
		u.RawQuery != "" || u.Fragment != "" || strings.TrimSuffix(u.Path, "/") != "" {
		return "", false
	}
	return u.Scheme + "://" + u.Host, true
}

// contactFormURLFromEnv は CONTACT_FORM_URL を検証して返します。
// 未設定、またはホスト付きのhttps URLでない場合は空文字を返し、ボタンを表示しません。
func contactFormURLFromEnv() string {
	raw := os.Getenv("CONTACT_FORM_URL")
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return ""
	}
	return u.String()
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

// SetNavLinks は、有効な他の機能（例: CMSの「お知らせ」）のリンクをナビゲーションに加えます。
func (f *Feature) SetNavLinks(links []web.NavLink) {
	f.navLinks = links
}

// HandleRoot はBaseのデフォルトトップ（GET /）をこのLP画面で置き換えます。
func (f *Feature) HandleRoot(w http.ResponseWriter, r *http.Request) {
	f.render(w, http.StatusOK, "index.html", map[string]any{
		"Title":          "個人情報と依存を最小限に抑えるWeb開発基盤",
		"CanonicalPath":  "/",
		"ContactFormURL": f.contactFormURL,
	})
}

// HandleNotFound は、静的ホスティング用の 404.html を描画します（export-static で書き出す）。
func (f *Feature) HandleNotFound(w http.ResponseWriter, r *http.Request) {
	f.render(w, http.StatusNotFound, "notfound.html", map[string]any{
		"Title":   "ページが見つかりません",
		"NoIndex": true,
	})
}

// Layout は、Siteのヘッダー・フッターを、Coreの画面（/privacy 等）やCMSの公開ページにも使わせます。
func (f *Feature) Layout() web.Layout {
	return web.Layout{FS: viewsFS, File: "views/layout.html", Data: f.layoutData}
}

// layoutData は、レイアウトが必要とする値を加えます。画面が設定済みの値は上書きしません。
func (f *Feature) layoutData(data map[string]any) {
	data["NavLinks"] = f.navLinks
	data["SiteURL"] = f.siteURL
	data["OGImagePath"] = ogImagePath
	data["OGImageAlt"] = ogImageAlt
	if _, ok := data["Description"]; !ok {
		data["Description"] = description
	}
}

func (f *Feature) render(w http.ResponseWriter, status int, page string, data map[string]any) {
	f.layoutData(data)
	var buf bytes.Buffer
	if err := f.pages[page].ExecuteTemplate(&buf, "layout", data); err != nil {
		f.logger.Error("render site template failed")
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}

// RegisterIsolatedRoutes は robots.txt と sitemap.xml を登録します。
// 公開ページと同じく、匿名セッションのCookieを照合しない独立経路で返します。
func (f *Feature) RegisterIsolatedRoutes(mux *web.IsolatedMux) {
	mux.HandleFunc("GET /robots.txt", f.handleRobots)
	mux.HandleFunc("GET /sitemap.xml", f.handleSitemap)
}

func (f *Feature) handleRobots(w http.ResponseWriter, r *http.Request) {
	var b strings.Builder
	b.WriteString("User-agent: *\nAllow: /\n")
	if f.siteURL != "" {
		b.WriteString("\nSitemap: " + f.siteURL + "/sitemap.xml\n")
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, b.String())
}

// handleSitemap は公開ページの一覧を返します。絶対URLが必要なため、SITE_URL が無ければ404にします。
func (f *Feature) handleSitemap(w http.ResponseWriter, r *http.Request) {
	if f.siteURL == "" {
		http.NotFound(w, r)
		return
	}
	paths := []string{"/", "/privacy"}
	for _, l := range f.navLinks {
		paths = append(paths, l.Href)
	}
	var b strings.Builder
	b.WriteString(xml.Header + `<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">` + "\n")
	for _, p := range paths {
		b.WriteString("  <url><loc>")
		_ = xml.EscapeText(&b, []byte(f.siteURL+p))
		b.WriteString("</loc></url>\n")
	}
	b.WriteString("</urlset>\n")
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	_, _ = io.WriteString(w, b.String())
}
