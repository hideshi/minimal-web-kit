package cms

import (
	"bytes"
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
)

//go:embed views/*.html
var viewsFS embed.FS

const (
	newsPageSize    = 20
	apiDefaultLimit = 20
	apiMaxLimit     = 50
	// 極端に大きいoffsetによる重い走査を避けるための上限
	maxOffset = 100000
)

func parsePublicTemplates() map[string]*template.Template {
	pages := map[string]*template.Template{}
	for _, page := range []string{"news_list.html", "news_show.html"} {
		pages[page] = template.Must(template.ParseFS(viewsFS, "views/layout.html", "views/"+page))
	}
	return pages
}

// render はテンプレートをバッファに描画してから送信します（途中失敗で壊れたHTMLを返さないため）。
func (f *Feature) render(w http.ResponseWriter, page string, data map[string]any) {
	var buf bytes.Buffer
	if err := f.publicPages[page].ExecuteTemplate(&buf, "layout", data); err != nil {
		f.logger.Error("cms render failed")
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = buf.WriteTo(w)
}

// parseIntParam は数値パラメータを解析します。未指定なら def を返し、不正・範囲外なら ok=false を返します。
func parseIntParam(raw string, def, min, max int) (int, bool) {
	if raw == "" {
		return def, true
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < min || n > max {
		return 0, false
	}
	return n, true
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

// paragraphs は本文を空行で段落に、段落内を改行で行に分けます。表示時はテンプレートがエスケープします。
func paragraphs(body string) [][]string {
	var result [][]string
	for _, block := range strings.Split(body, "\n\n") {
		block = strings.Trim(block, "\n")
		if strings.TrimSpace(block) == "" {
			continue
		}
		result = append(result, strings.Split(block, "\n"))
	}
	return result
}

// localTime は表示用に、公開日時をサーバーのタイムゾーンへ変換します。
func localTime(p Post) Post {
	p.PublishedAt = p.PublishedAt.In(time.Local)
	return p
}

func (f *Feature) handleNewsList(w http.ResponseWriter, r *http.Request) {
	page, ok := parseIntParam(r.URL.Query().Get("page"), 1, 1, maxOffset/newsPageSize)
	if !ok {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	// 次のページの有無を知るため1件多く取得する
	posts, err := f.posts.ListPublished(r.Context(), newsPageSize+1, (page-1)*newsPageSize)
	if err != nil {
		f.logger.Error("cms list published posts failed")
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	data := map[string]any{"Title": "お知らせ"}
	if len(posts) > newsPageSize {
		posts = posts[:newsPageSize]
		data["NextPage"] = page + 1
	}
	if page > 1 {
		data["PrevPage"] = page - 1
	}
	for i := range posts {
		posts[i] = localTime(posts[i])
	}
	data["Posts"] = posts
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
		f.logger.Error("cms get published post failed")
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	f.render(w, "news_show.html", map[string]any{
		"Title":      post.Title,
		"Post":       localTime(post),
		"Paragraphs": paragraphs(post.Body),
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
		f.logger.Error("cms encode json failed")
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
	limit, okLimit := parseIntParam(q.Get("limit"), apiDefaultLimit, 1, apiMaxLimit)
	offset, okOffset := parseIntParam(q.Get("offset"), 0, 0, maxOffset)
	if !okLimit || !okOffset {
		f.writeJSON(w, http.StatusBadRequest, apiError("bad_request"))
		return
	}
	posts, err := f.posts.ListPublished(r.Context(), limit+1, offset)
	if err != nil {
		f.logger.Error("cms api list published posts failed")
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
		f.logger.Error("cms api get published post failed")
		f.writeJSON(w, http.StatusInternalServerError, apiError("internal_error"))
		return
	}
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
