// minimal-web-kit: bundled-feature catalog
package catalog

import (
	"bytes"
	"context"
	"database/sql"
	"embed"
	"errors"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"time"

	"minimal-web-kit/features"
	"minimal-web-kit/internal/store"
	"minimal-web-kit/internal/web"
	"minimal-web-kit/internal/web/form"
	"minimal-web-kit/internal/web/page"
)

//go:embed migration.sql views/*.html static/*
var contentFS embed.FS

type Feature struct {
	web.PublicNavigation
	logger                  *slog.Logger
	store                   *Store
	admin                   features.Admin
	publicPages, adminPages map[string]*template.Template
	layout                  *web.Layout
}

func NewFeature(logger *slog.Logger) *Feature {
	f := &Feature{logger: logger}
	if err := f.SetLayout(web.Layout{FS: contentFS, File: "views/layout.html"}); err != nil {
		panic(err)
	}
	return f
}
func (*Feature) Name() string { return "catalog" }
func Migrate(db *sql.DB) error {
	body, err := contentFS.ReadFile("migration.sql")
	if err != nil {
		return err
	}
	_, err = db.Exec(string(body))
	return err
}
func (f *Feature) Init(db *sql.DB) error {
	if err := Migrate(db); err != nil {
		return err
	}
	f.store = NewStore(db)
	return nil
}
func (*Feature) RegisterRoutes(*http.ServeMux, *web.SessionManager) {}
func (*Feature) NavLinks() []web.NavLink {
	return []web.NavLink{{Label: "カタログ", Href: "/catalog"}}
}
func (*Feature) AdminNavLinks() []web.NavLink {
	return []web.NavLink{{Label: "カタログの管理", Href: "/admin/catalog"}}
}
func (f *Feature) SetLayout(layout web.Layout) error {
	pages, err := web.ParsePages(layout, contentFS, "list.html", "show.html")
	if err != nil {
		return err
	}
	f.publicPages = pages
	f.layout = &layout
	return nil
}
func (f *Feature) SetAdmin(admin features.Admin) error {
	pages, err := web.ParsePages(admin.Layout(), contentFS, "admin_list.html", "admin_form.html", "admin_delete.html", "admin_classifications.html", "admin_classification.html")
	if err != nil {
		return err
	}
	f.adminPages = pages
	f.admin = admin
	return nil
}
func (f *Feature) render(w http.ResponseWriter, status int, t *template.Template, name string, data map[string]any) {
	f.write(w, status, t, name, data, false)
}

// renderScripted は render と同じく描画し、成功したときだけ自サイトのJSを許すCSPにします（失敗した500は既定のまま）。
func (f *Feature) renderScripted(w http.ResponseWriter, status int, t *template.Template, name string, data map[string]any) {
	f.write(w, status, t, name, data, true)
}

func (f *Feature) write(w http.ResponseWriter, status int, t *template.Template, name string, data map[string]any, scripts bool) {
	var b bytes.Buffer
	if err := t.ExecuteTemplate(&b, name, data); err != nil {
		f.fail(w)
		return
	}
	if scripts {
		web.AllowScripts(w)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = b.WriteTo(w)
}
func (f *Feature) fail(w http.ResponseWriter) {
	f.logger.Error("catalog operation failed", web.RequestIDAttr(w))
	http.Error(w, "Internal Server Error", 500)
}
func (f *Feature) result(w http.ResponseWriter, r *http.Request, err error) bool {
	if err == nil {
		return true
	}
	if store.Busy(err) {
		web.WriteBusy(w)
	} else if errors.Is(err, ErrNotFound) {
		http.NotFound(w, r)
	} else {
		f.fail(w)
	}
	return false
}

func parseFilter(r *http.Request) (Filter, page.Page, error) {
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return Filter{}, page.Page{}, ErrInvalidInput
	}
	pg, ok := page.Parse(r, 20, 5000)
	if !ok || len(q["q"]) > 1 || len(q["category"]) > 1 || len(q["page"]) > 1 || len(q["tag"]) > 20 {
		return Filter{}, pg, ErrInvalidInput
	}
	query, err := line(q.Get("q"), 200, false)
	if err != nil {
		return Filter{}, pg, err
	}
	f := Filter{Query: query}
	if raw := q.Get("category"); raw != "" {
		id, ok := form.ID(raw)
		if !ok {
			return f, pg, ErrInvalidInput
		}
		f.CategoryID = id
	}
	for _, raw := range q["tag"] {
		id, ok := form.ID(raw)
		if !ok {
			return f, pg, ErrInvalidInput
		}
		f.TagIDs = append(f.TagIDs, id)
	}
	slices.Sort(f.TagIDs)
	f.TagIDs = slices.Compact(f.TagIDs)
	return f, pg, nil
}
func filterURL(filter Filter, number int) string {
	q := url.Values{}
	if filter.Query != "" {
		q.Set("q", filter.Query)
	}
	if filter.CategoryID > 0 {
		q.Set("category", idString(filter.CategoryID))
	}
	for _, id := range filter.TagIDs {
		q.Add("tag", idString(id))
	}
	if number > 1 {
		q.Set("page", strconv.Itoa(number))
	}
	if len(q) == 0 {
		return "/catalog"
	}
	return "/catalog?" + q.Encode()
}
func (f *Feature) handleList(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Vary", "HX-Request, HX-History-Restore-Request")
	filter, pg, err := parseFilter(r)
	if err != nil {
		http.Error(w, "Bad Request", 400)
		return
	}
	items, err := f.store.List(r.Context(), true, filter, pg.Limit(), pg.Offset())
	if !f.result(w, r, err) {
		return
	}
	items = page.Trim(&pg, items)
	if pg.Number >= 5000 {
		pg.Next = 0
	}
	categories, err := f.store.Classifications(r.Context(), "categories", true)
	if !f.result(w, r, err) {
		return
	}
	tags, err := f.store.Classifications(r.Context(), "tags", true)
	if !f.result(w, r, err) {
		return
	}
	invalidFilter := filter.CategoryID > 0 && !slices.ContainsFunc(categories, func(c Classification) bool { return c.ID == filter.CategoryID })
	for _, id := range filter.TagIDs {
		if !slices.ContainsFunc(tags, func(t Classification) bool { return t.ID == id }) {
			invalidFilter = true
		}
	}
	if invalidFilter {
		items = []Item{}
		pg.Prev = 0
		pg.Next = 0
	}
	data := map[string]any{"HTMXURL": htmxURL, "CatalogScriptURL": catalogScriptURL, "Title": "カタログ", "Description": "公開中のカタログです。", "CanonicalPath": "/catalog", "Items": items, "Filter": filter, "Page": pg}
	shown := filter
	if invalidFilter {
		// 公開候補に無い分類の指定は、存在しないものと同じ扱い（CATALOG-INV-1）。選択の状態は出さない
		shown.CategoryID, shown.TagIDs = 0, nil
	}
	data["QueryField"] = form.Field{ID: "catalog-q", Name: "q", Label: "キーワード", Type: "search", Value: filter.Query, MaxLength: 200}
	data["CategoryField"] = categoryFilterField(categories, shown.CategoryID)
	data["TagChoices"] = tagFilterChoices(tags, shown.TagIDs)
	if pg.Prev > 0 {
		data["PrevURL"] = filterURL(filter, pg.Prev)
	}
	if pg.Next > 0 {
		data["NextURL"] = filterURL(filter, pg.Next)
	}
	fragment := r.Header.Get("HX-Request") == "true" && r.Header.Get("HX-History-Restore-Request") != "true"
	if fragment {
		f.render(w, 200, f.publicPages["list.html"], "results", data)
		return
	}
	f.AddNavigation(data)
	if f.layout.Data != nil {
		f.layout.Data(data)
	}
	// 全ページの応答だけ、htmxとcatalog.jsを読み込むため自サイトのJSを許す。
	f.renderScripted(w, 200, f.publicPages["list.html"], "layout", data)
}

// categoryFilterField は、公開一覧のカテゴリの選択欄です。
func categoryFilterField(categories []Classification, selected int64) form.Field {
	field := form.Field{ID: "catalog-category", Name: "category", Label: "カテゴリ", Options: []form.Option{{Value: "", Label: "すべて"}}}
	for _, c := range categories {
		field.Options = append(field.Options, form.Option{Value: idString(c.ID), Label: c.Name, Selected: c.ID == selected})
	}
	return field
}

// tagFilterChoices は、公開一覧のタグの選択肢です（横に並べて折り返す）。
func tagFilterChoices(tags []Classification, selected []int64) form.Choices {
	choices := form.Choices{ID: "catalog-tags", Name: "tag", Legend: "タグ（すべてに一致・20個まで）", Multiple: true, Inline: true, Empty: "公開中のタグはありません。"}
	for _, tag := range tags {
		choices.Options = append(choices.Options, form.Option{Value: idString(tag.ID), Label: tag.Name, Selected: slices.Contains(selected, tag.ID)})
	}
	return choices
}
func (f *Feature) handleShow(w http.ResponseWriter, r *http.Request) {
	id, ok := web.PathID(w, r, "id")
	if !ok {
		return
	}
	item, err := f.store.Get(r.Context(), id, true)
	if !f.result(w, r, err) {
		return
	}
	data := map[string]any{"Title": item.Title, "CanonicalPath": "/catalog/" + idString(id), "Item": item, "Paragraphs": form.Paragraphs(item.Body)}
	f.AddNavigation(data)
	if f.layout.Data != nil {
		f.layout.Data(data)
	}
	f.render(w, 200, f.publicPages["show.html"], "layout", data)
}
func (f *Feature) SitemapEntries(ctx context.Context) ([]web.SitemapEntry, error) {
	// 本文の大きさに関係なく、公開IDと更新日時だけを取得する。
	rows, err := f.store.db.QueryContext(ctx, "SELECT id,updated_at FROM catalog_items WHERE status='published' ORDER BY id DESC LIMIT 49000")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	entries := []web.SitemapEntry{{Path: "/catalog"}}
	for rows.Next() {
		var id, at int64
		if err := rows.Scan(&id, &at); err != nil {
			return nil, err
		}
		updated := time.Unix(at, 0)
		entries = append(entries, web.SitemapEntry{Path: "/catalog/" + idString(id), LastMod: updated})
		if updated.After(entries[0].LastMod) {
			entries[0].LastMod = updated
		}
	}
	return entries, rows.Err()
}

// scriptURL は、公開一覧が読み込むJSの版付きのURLを返します。ファイルは埋め込みなので、起動の後は変わりません。
func scriptURL(name string) string {
	body, err := contentFS.ReadFile("static/" + name)
	if err != nil {
		panic(err)
	}
	return "/catalog/static/" + name + "?v=" + web.ContentVersion(body)
}

var htmxURL, catalogScriptURL = scriptURL("htmx.min.js"), scriptURL("catalog.js")

func (f *Feature) RegisterIsolatedRoutes(mux *web.IsolatedMux) {
	mux.HandleFunc("GET /catalog", f.handleList)
	mux.HandleFunc("GET /catalog/{id}", f.handleShow)
	mux.Handle("GET /catalog/{rest...}", http.NotFoundHandler())
	for _, name := range []string{"htmx.min.js", "catalog.js"} {
		mux.HandleFunc("GET /catalog/static/"+name, func(w http.ResponseWriter, r *http.Request) {
			body, err := contentFS.ReadFile("static/" + name)
			if err != nil {
				f.fail(w)
				return
			}
			web.VersionedCache(w, r, web.ContentVersion(body))
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			_, _ = w.Write(body)
		})
	}
	if f.admin != nil {
		f.registerAdmin(mux)
	}
}
