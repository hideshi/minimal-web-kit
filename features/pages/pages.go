// minimal-web-kit: bundled-feature pages
package pages

import (
	"bytes"
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"minimal-web-kit/features"
	"minimal-web-kit/internal/web"
	"minimal-web-kit/internal/web/richtext"
)

//go:embed migration.sql
var migrationSQL string

//go:embed views/*.html
var viewsFS embed.FS

// Feature は同梱機能の固定ページ（pages）です。管理画面で作った /{slug} のページを公開します。
// 公開ページは、どの経路にも当たらなかったGET・HEADを受けるCoreの受け皿（FallbackFeature）で返します（docs/pages-plan.md 2.1）。
type Feature struct {
	logger     *slog.Logger
	store      *Store
	admin      features.Admin
	notFound   http.HandlerFunc
	taken      func(string) bool
	layout     *web.Layout
	publicPage *template.Template
	adminPages map[string]*template.Template
	now        func() time.Time

	mu  sync.RWMutex
	nav []web.NavLink // 公開中でナビの順のあるページ（スナップショット。PAGES-INV-6）
}

func NewFeature(logger *slog.Logger) *Feature {
	return &Feature{
		logger:     logger,
		publicPage: template.Must(web.ParseWithComponents(viewsFS, "views/layout.html", "views/page.html")),
		notFound:   http.NotFound,
		now:        time.Now,
	}
}

func (f *Feature) Name() string { return "pages" }

// Init はマイグレーションを適用し、ナビのスナップショットを作ります。
func (f *Feature) Init(db *sql.DB) error {
	if _, err := db.Exec(migrationSQL); err != nil {
		return fmt.Errorf("pages migration: %w", err)
	}
	f.store = NewStore(db)
	f.store.now = func() time.Time { return f.now() }
	return f.refreshNav(context.Background())
}

func (f *Feature) RegisterRoutes(mux *http.ServeMux, sm *web.SessionManager) {}

// RegisterIsolatedRoutes は管理画面を登録します（公開ページは受け皿で返す）。
func (f *Feature) RegisterIsolatedRoutes(mux *web.IsolatedMux) {
	if f.admin != nil {
		f.registerAdmin(mux)
	}
}

func (f *Feature) SetNotFound(h http.HandlerFunc)        { f.notFound = h }
func (f *Feature) SetRouteTaken(taken func(string) bool) { f.taken = taken }

// SetLayout は、公開ページをサイト全体のレイアウトで描くようにします。
func (f *Feature) SetLayout(layout web.Layout) error {
	t, err := web.ParsePage(layout, viewsFS, "views/page.html")
	if err != nil {
		return err
	}
	f.publicPage, f.layout = t, &layout
	return nil
}

// refreshNav は、公開中でナビの順のあるページのリンクを作り直します。保存・公開・非公開・削除の後に呼びます。
func (f *Feature) refreshNav(ctx context.Context) error {
	rows, err := f.store.db.QueryContext(ctx, "SELECT slug, title, nav_order FROM pages WHERE status = 'published' AND nav_order IS NOT NULL")
	if err != nil {
		return err
	}
	defer rows.Close()
	type entry struct {
		link  web.NavLink
		order int
	}
	var entries []entry
	for rows.Next() {
		var e entry
		if err := rows.Scan(&e.link.Href, &e.link.Label, &e.order); err != nil {
			return err
		}
		e.link.Href = "/" + e.link.Href
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].order != entries[j].order {
			return entries[i].order < entries[j].order
		}
		return entries[i].link.Href < entries[j].link.Href
	})
	nav := make([]web.NavLink, len(entries))
	for i, e := range entries {
		nav[i] = e.link
	}
	f.mu.Lock()
	f.nav = nav
	f.mu.Unlock()
	return nil
}

// DynamicNavLinks は、公開ナビに載せるページのリンクを返します（描画のたびに呼ばれる。DBは読まない）。
func (f *Feature) DynamicNavLinks() []web.NavLink {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return append([]web.NavLink(nil), f.nav...)
}

// SitemapEntries は、公開中のページを sitemap.xml に載せます。
func (f *Feature) SitemapEntries(ctx context.Context) ([]web.SitemapEntry, error) {
	rows, err := f.store.db.QueryContext(ctx, "SELECT slug, updated_at FROM pages WHERE status = 'published' ORDER BY slug")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []web.SitemapEntry
	for rows.Next() {
		var slug string
		var at int64
		if err := rows.Scan(&slug, &at); err != nil {
			return nil, err
		}
		out = append(out, web.SitemapEntry{Path: "/" + slug, LastMod: time.Unix(at, 0)})
	}
	return out, rows.Err()
}

// HandleFallback は、どの経路にも当たらなかったGET・HEADを受け、1階層で公開中のページなら描き、それ以外は404にします。
// Cookieは発行・照合しません（PAGES-INV-5）。
func (f *Feature) HandleFallback(w http.ResponseWriter, r *http.Request) {
	slug := strings.TrimPrefix(r.URL.Path, "/")
	if !slugPattern.MatchString(slug) {
		f.notFound(w, r)
		return
	}
	p, err := f.store.Published(r.Context(), slug)
	if errors.Is(err, ErrNotFound) {
		f.notFound(w, r)
		return
	} else if err != nil {
		// 取得の失敗を404にすると、障害の間に公開中のページが「無い」と扱われる（検索エンジン等）ため、500にする
		f.logger.Error("pages load failed", web.RequestIDAttr(w))
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	doc, err := richtext.Parse(p.Body)
	if err != nil {
		// 保存のときに検査しているため、ここには来ない想定
		f.logger.Error("pages body over limit", slog.Int64("page_id", p.ID))
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	data := map[string]any{
		"Title":         p.Title,
		"CanonicalPath": "/" + p.Slug,
		"Description":   description(doc, p.Title),
		"Page":          p,
		"Doc":           doc,
	}
	if f.layout != nil && f.layout.Data != nil {
		f.layout.Data(data)
	}
	var buf bytes.Buffer
	if err := f.publicPage.ExecuteTemplate(&buf, "layout", data); err != nil {
		f.logger.Error("pages render failed")
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	// 公開・非公開をすぐ反映するため、公開キャッシュは使わない（Coreの既定の no-store のまま）
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = buf.WriteTo(w)
}

// description は、検索結果等に出す説明文を、本文の最初の段落から作ります（120文字まで）。
func description(doc richtext.Doc, fallback string) string {
	for _, b := range doc.Blocks {
		if b.Kind != "p" {
			continue
		}
		var s strings.Builder
		for _, line := range b.Lines {
			for _, x := range line {
				s.WriteString(x.Text)
			}
			s.WriteString(" ")
		}
		text := []rune(strings.Join(strings.Fields(s.String()), " "))
		if len(text) > 120 {
			return string(text[:120]) + "…"
		}
		return string(text)
	}
	return fallback
}
