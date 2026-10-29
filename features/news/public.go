package news

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"html/template"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"minimal-web-kit/internal/web"
	"minimal-web-kit/internal/web/form"
	"minimal-web-kit/internal/web/page"
)

//go:embed views/*.html
var viewsFS embed.FS

const (
	newsPageSize    = 20
	apiDefaultLimit = 20
	apiMaxLimit     = 50
	// 極端に大きいoffsetによる重い走査を避けるための上限
	maxOffset = 100000
	// sitemap.xml に載せる記事の上限（1ファイル5万URLの仕様の範囲に収める）
	maxSitemapPosts = 49000
)

var publicPageNames = []string{"news_list.html", "news_show.html"}

func parsePublicTemplates() map[string]*template.Template {
	pages, err := web.ParsePages(web.Layout{FS: viewsFS, File: "views/layout.html"}, viewsFS, publicPageNames...)
	if err != nil {
		panic(err)
	}
	return pages
}

// SetLayout は、公開ページ（/news）をサイト全体のレイアウト（例: Siteのヘッダー・フッター）で描画するようにします。
// 管理画面は、引き続き管理画面のレイアウトで描画します。
func (f *Feature) SetLayout(layout web.Layout) error {
	pages, err := web.ParsePages(layout, viewsFS, publicPageNames...)
	if err != nil {
		return err
	}
	f.publicPages = pages
	f.layout = &layout
	return nil
}

// render はテンプレートをバッファに描画してから送信します（途中失敗で壊れたHTMLを返さないため）。
func (f *Feature) render(w http.ResponseWriter, page string, data map[string]any) {
	if f.layout != nil && f.layout.Data != nil {
		f.layout.Data(data)
	}
	var buf bytes.Buffer
	if err := f.publicPages[page].ExecuteTemplate(&buf, "layout", data); err != nil {
		f.logger.Error("news render failed")
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = buf.WriteTo(w)
}

// parsePostID は経路の {id} を解析します。数値でなければ400、範囲外なら404とします。
func parsePostID(raw string) (id int64, status int) {
	if raw == "" || strings.TrimLeft(raw, "0123456789") != "" {
		return 0, http.StatusBadRequest
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, http.StatusNotFound
	}
	return id, 0
}

// excerptRunes は、記事の説明文（検索結果・SNSのカード）に使う本文の文字数です。
const excerptRunes = 120

// excerpt は、本文の冒頭から説明文を作ります。改行や連続する空白は1つの空白にまとめます。本文が空ならタイトルを使います。
func excerpt(body, fallback string) string {
	s := strings.Join(strings.Fields(body), " ")
	if s == "" {
		return fallback
	}
	if r := []rune(s); len(r) > excerptRunes {
		return string(r[:excerptRunes]) + "…"
	}
	return s
}

// SitemapEntries は、公開中の記事と一覧を sitemap.xml に載せます（Siteが有効な場合に使われる）。
func (f *Feature) SitemapEntries(ctx context.Context) ([]web.SitemapEntry, error) {
	refs, err := f.posts.PublishedRefs(ctx, maxSitemapPosts)
	if err != nil {
		return nil, err
	}
	entries := []web.SitemapEntry{{Path: "/news"}}
	for _, ref := range refs {
		if ref.UpdatedAt.After(entries[0].LastMod) {
			entries[0].LastMod = ref.UpdatedAt
		}
		entries = append(entries, web.SitemapEntry{Path: "/news/" + strconv.FormatInt(ref.ID, 10), LastMod: ref.UpdatedAt})
	}
	return entries, nil
}

// localTime は表示用に、公開日時をサーバーのタイムゾーンへ変換します。
func localTime(p Post) Post {
	p.PublishedAt = p.PublishedAt.In(time.Local)
	return p
}

func (f *Feature) handleNewsList(w http.ResponseWriter, r *http.Request) {
	pg, ok := page.Parse(r, newsPageSize, maxOffset/newsPageSize)
	if !ok {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	posts, err := f.posts.ListPublished(r.Context(), pg.Limit(), pg.Offset())
	if err != nil {
		f.logger.Error("news list published posts failed")
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	posts = page.Trim(&pg, posts)
	pg.Base, pg.Label, pg.PrevText, pg.NextText = "/news", "お知らせのページ送り", "新しいお知らせ", "過去のお知らせ"
	data := map[string]any{"Title": "お知らせ", "Page": pg, "CanonicalPath": "/news", "Description": "公開中のお知らせの一覧です。"}
	for i := range posts {
		posts[i] = localTime(posts[i])
	}
	data["Posts"] = posts
	web.PublicCache(w, web.PublicCacheSeconds)
	f.render(w, "news_list.html", data)
}

func (f *Feature) handleNewsShow(w http.ResponseWriter, r *http.Request) {
	id, status := parsePostID(r.PathValue("id"))
	if status != 0 {
		http.Error(w, http.StatusText(status), status)
		return
	}
	post, err := f.posts.GetPublished(r.Context(), id)
	if errors.Is(err, ErrNotFound) {
		http.NotFound(w, r)
		return
	} else if err != nil {
		f.logger.Error("news get published post failed")
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	web.PublicCache(w, web.PublicCacheSeconds)
	f.render(w, "news_show.html", map[string]any{
		"Title":         post.Title,
		"Description":   excerpt(post.Body, post.Title),
		"OGType":        "article",
		"PublishedTime": post.PublishedAt.UTC().Format(time.RFC3339),
		"CanonicalPath": "/news/" + strconv.FormatInt(post.ID, 10),
		"Post":          localTime(post),
		"Paragraphs":    form.Paragraphs(post.Body),
	})
}

// apiPost は読み取りAPIで返す項目です。下書きや内部の列は含めません。
type apiPost struct {
	ID          int64  `json:"id"`
	Title       string `json:"title"`
	Body        string `json:"body"`
	PublishedAt string `json:"published_at"`
	UpdatedAt   string `json:"updated_at"`
}

func toAPIPost(p Post) apiPost {
	return apiPost{
		ID:          p.ID,
		Title:       p.Title,
		Body:        p.Body,
		PublishedAt: p.PublishedAt.UTC().Format(time.RFC3339),
		UpdatedAt:   p.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

// writeJSON はJSONを返します。エラー時の本文は固定の識別子のみとし、入力値を反射しません。
func (f *Feature) writeJSON(w http.ResponseWriter, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		f.logger.Error("news encode json failed")
		status, body = http.StatusInternalServerError, []byte(`{"error":"internal_error"}`)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func apiError(code string) map[string]string {
	return map[string]string{"error": code}
}

func (f *Feature) handleAPIList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, okLimit := form.Int(q.Get("limit"), apiDefaultLimit, 1, apiMaxLimit)
	offset, okOffset := form.Int(q.Get("offset"), 0, 0, maxOffset)
	if !okLimit || !okOffset {
		f.writeJSON(w, http.StatusBadRequest, apiError("bad_request"))
		return
	}
	posts, err := f.posts.ListPublished(r.Context(), limit+1, offset)
	if err != nil {
		f.logger.Error("news api list published posts failed")
		f.writeJSON(w, http.StatusInternalServerError, apiError("internal_error"))
		return
	}
	resp := struct {
		Items      []apiPost `json:"items"`
		Limit      int       `json:"limit"`
		Offset     int       `json:"offset"`
		NextOffset *int      `json:"next_offset"`
	}{Items: []apiPost{}, Limit: limit, Offset: offset}
	if len(posts) > limit {
		posts = posts[:limit]
		next := offset + limit
		resp.NextOffset = &next
	}
	for _, p := range posts {
		resp.Items = append(resp.Items, toAPIPost(p))
	}
	web.PublicCache(w, web.PublicCacheSeconds)
	f.writeJSON(w, http.StatusOK, resp)
}

func (f *Feature) handleAPIShow(w http.ResponseWriter, r *http.Request) {
	id, status := parsePostID(r.PathValue("id"))
	if status == http.StatusBadRequest {
		f.writeJSON(w, status, apiError("bad_request"))
		return
	} else if status != 0 {
		f.writeJSON(w, status, apiError("not_found"))
		return
	}
	post, err := f.posts.GetPublished(r.Context(), id)
	if errors.Is(err, ErrNotFound) {
		f.writeJSON(w, http.StatusNotFound, apiError("not_found"))
		return
	} else if err != nil {
		f.logger.Error("news api get published post failed")
		f.writeJSON(w, http.StatusInternalServerError, apiError("internal_error"))
		return
	}
	web.PublicCache(w, web.PublicCacheSeconds)
	f.writeJSON(w, http.StatusOK, toAPIPost(post))
}

// cors は、Originが許可リストと一致する場合だけ、読み取りAPIへのアクセスを許可します。
// 資格情報（Cookie）は許可しません。許可リストが空なら、どのOriginも許可しません。
func (f *Feature) cors(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Vary", "Origin")
		origin := r.Header.Get("Origin")
		allowed := origin != "" && slices.Contains(f.cfg.CORSOrigins, origin)
		if allowed {
			w.Header().Set("Access-Control-Allow-Origin", origin)
		}
		if r.Method == http.MethodOptions {
			if !allowed {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD")
			w.Header().Set("Access-Control-Max-Age", "600")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next(w, r)
	}
}

func (f *Feature) registerPublicRoutes(mux *web.IsolatedMux) {
	mux.HandleFunc("GET /news", f.handleNewsList)
	mux.HandleFunc("GET /news/{id}", f.handleNewsShow)
	mux.HandleFunc("GET /api/news", f.cors(f.handleAPIList))
	mux.HandleFunc("GET /api/news/{id}", f.cors(f.handleAPIShow))
	mux.HandleFunc("OPTIONS /api/news", f.cors(nil))
	mux.HandleFunc("OPTIONS /api/news/{id}", f.cors(nil))
}
