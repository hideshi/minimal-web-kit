// Package catalogtags は架空データだけを使う、明示起動専用のUI比較試作です。
// 通常サーバーやFeatureには登録しません。
package catalogtags

import (
	"bytes"
	"embed"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"minimal-web-kit/internal/web"
	"minimal-web-kit/internal/web/form"
	"minimal-web-kit/internal/web/guard"
)

//go:embed views/*.html static/*
var files embed.FS

type Tag struct {
	ID       int
	Name     string
	Selected bool
}
type Page struct {
	Alpine        bool // Alpineを読み込む版（一覧の絞り込み・コンボボックス）
	Combo         bool
	Mode          string
	Count         int
	Tags          []Tag
	Selected      []Tag
	NewTags       string
	Error, Notice string
}

func (Page) LogValue() slog.Value { return slog.StringValue("[tag comparison input]") }

func sample(count int) []Tag {
	names := []string{"設計", "設計レビュー", "詳細設計", "実装", "実装レビュー", "運用", "運用改善", "調査", "テスト", "公開"}
	categories := []string{"設計", "実装", "運用", "調査", "テスト"}
	tags := make([]Tag, count)
	for i := range tags {
		name := ""
		if i < len(names) {
			name = names[i]
		} else {
			name = fmt.Sprintf("%s %03d", categories[i%len(categories)], i+1)
		}
		tags[i] = Tag{ID: i + 1, Name: name}
	}
	return tags
}
func readPage(mode, rawCount string) (Page, bool) {
	if mode == "" {
		mode = "html"
	}
	if mode != "html" && mode != "alpine" && mode != "combo" {
		return Page{}, false
	}
	if rawCount == "" {
		rawCount = "10"
	}
	count, err := strconv.Atoi(rawCount)
	if err != nil || (count != 10 && count != 50 && count != 200) {
		return Page{}, false
	}
	return Page{Mode: mode, Alpine: mode != "html", Combo: mode == "combo", Count: count, Tags: sample(count)}, true
}

// NewHandler はCSSの内容だけを受け取り、DB・セッション・カタログ本体には触れません。
func NewHandler(css []byte, origin string, logger *slog.Logger) (http.Handler, error) {
	t, err := web.ParseWithComponents(files, "views/compare.html")
	if err != nil {
		return nil, err
	}
	render := func(w http.ResponseWriter, status int, p Page) {
		var buf bytes.Buffer
		if err := t.ExecuteTemplate(&buf, "layout", p); err != nil {
			logger.Error("tag comparison render failed")
			http.Error(w, "Internal Server Error", 500)
			return
		}
		if p.Alpine {
			w.Header().Set("Content-Security-Policy", strings.Replace(w.Header().Get("Content-Security-Policy"), "script-src 'none'", "script-src 'self'", 1))
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(status)
		_, _ = buf.WriteTo(w)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/compare", http.StatusSeeOther) })
	mux.HandleFunc("GET /compare", func(w http.ResponseWriter, r *http.Request) {
		q, err := urlQuery(r)
		if err != nil {
			http.Error(w, "Bad Request", 400)
			return
		}
		p, ok := readPage(q.Get("mode"), q.Get("count"))
		if !ok {
			http.Error(w, "Bad Request", 400)
			return
		}
		render(w, 200, p)
	})
	mux.HandleFunc("POST /compare/result", func(w http.ResponseWriter, r *http.Request) {
		// 保存しない確認処理。同一オリジンだけを許し、セッションやCSRF用Cookieは作らない。
		if !guard.SameOrigin(r, origin) {
			http.Error(w, "Forbidden", 403)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Bad Request", 400)
			return
		}
		p, ok := readPage(r.PostForm.Get("mode"), r.PostForm.Get("count"))
		if !ok {
			http.Error(w, "Bad Request", 400)
			return
		}
		ids := map[int]bool{}
		invalid := len(r.PostForm["tag"]) > 20
		for _, raw := range r.PostForm["tag"] {
			id, err := strconv.Atoi(raw)
			if err != nil || id < 1 || id > p.Count {
				invalid = true
				continue
			}
			ids[id] = true
		}
		for i := range p.Tags {
			if ids[p.Tags[i].ID] {
				p.Tags[i].Selected = true
				p.Selected = append(p.Selected, p.Tags[i])
			}
		}
		p.NewTags = r.PostForm.Get("new_tags")
		text, err := form.Text(p.NewTags, 1020, false)
		if err != nil {
			invalid = true
		} else {
			p.NewTags = text
		}
		n := len(ids)
		for _, name := range strings.Split(text, "\n") {
			if strings.TrimSpace(name) == "" {
				continue
			}
			n++
			if _, err := form.Line(name, 50, true); err != nil {
				invalid = true
			}
		}
		if n > 20 {
			invalid = true
		}
		if invalid {
			p.Error = "タグは新規分を含め20個まで。新規タグ名は1行に1つ、50文字以内で入力してください。"
			render(w, 400, p)
			return
		}
		p.Notice = "選択内容を確認しました。実際のデータには保存していません。"
		render(w, 200, p)
	})
	mux.HandleFunc("GET /static/css/style.css", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		_, _ = w.Write(css)
	})
	for _, name := range []string{"tags.js", "alpine-csp.min.js"} {
		mux.HandleFunc("GET /compare/static/"+name, func(w http.ResponseWriter, r *http.Request) {
			body, err := files.ReadFile("static/" + name)
			if err != nil {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			_, _ = w.Write(body)
		})
	}
	return web.RequestID(web.Recoverer(logger)(web.SecurityHeaders(web.MaxBytes(64 << 10)(mux)))), nil
}

func urlQuery(r *http.Request) (url.Values, error) {
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(q["mode"]) > 1 || len(q["count"]) > 1 {
		return nil, fmt.Errorf("invalid comparison query")
	}
	return q, nil
}
