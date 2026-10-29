package app

import (
	"errors"
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"minimal-web-kit/features"
	"minimal-web-kit/internal/store"
)

// staticPages は書き出す経路と、書き出し先のファイル名です。
// 静的ホスティング（Cloudflare Workers・Pages等）は privacy.html を /privacy で配信します。
// optional の経路は、404なら書き出さない（例: SITE_URL 未設定時の sitemap.xml）。
var staticPages = []struct {
	path, file string
	optional   bool
}{
	{"/", "index.html", false},
	{"/privacy", "privacy.html", false},
	{"/static/css/style.css", "static/css/style.css", false},
	{"/static/img/ogp.png", "static/img/ogp.png", true},
	{"/static/img/favicon.svg", "static/img/favicon.svg", true},
	{"/static/img/favicon-32.png", "static/img/favicon-32.png", true},
	{"/static/img/apple-touch-icon.png", "static/img/apple-touch-icon.png", true},
	{"/favicon.ico", "favicon.ico", true},
	{"/robots.txt", "robots.txt", true},
	{"/sitemap.xml", "sitemap.xml", true},
}

// notFoundFile は、静的ホスティングが存在しないパスに返すページです。
const notFoundFile = "404.html"

// staticHeaders は、Goのサーバーと同じ値で静的ホスティングにも付けるセキュリティヘッダーです。
// Cache-Control（no-store）はセッションを扱う画面のためのもので、公開ページには付けません。
var staticHeaders = []string{"Content-Security-Policy", "X-Content-Type-Options", "X-Frame-Options", "Referrer-Policy"}

const headersFile = "_headers"

// ExportStatic は、Cookieも保存データも使わない公開ページ（Siteの / と /privacy）とCSSを、
// 静的ファイルとして dir に書き出します。セキュリティヘッダーは _headers に書き出します。
// セッションや管理画面を使う機能（お知らせ機能等）が有効な場合は、静的にできないため拒否します。
func ExportStatic(reg *features.Registry, dir string, logger *slog.Logger) error {
	var root features.CustomRootFeature
	for _, f := range reg.All() {
		if cr, ok := f.(features.CustomRootFeature); ok && root == nil {
			root = cr
			continue
		}
		return fmt.Errorf("feature %s has pages that cannot be exported as static files; enable only Site (e.g. make remove-news)", f.Name())
	}
	if root == nil {
		return errors.New("no static homepage to export; enable Site first (make add-site)")
	}

	// DBを使わない。公開ページは保存データに触れないため、メモリ上のストアで足りる。
	// 経路は起動時と同じつなぎ方（BuildRoutes）で作り、Goのサーバーと中身がずれないようにする。
	routes, err := BuildRoutes(store.NewMemoryStore(), reg, false, logger)
	if err != nil {
		return err
	}

	files := map[string][]byte{}
	var headers http.Header
	for _, p := range staticPages {
		rec := httptest.NewRecorder()
		routes.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p.path, nil))
		if p.optional && rec.Code == http.StatusNotFound {
			continue
		}
		if rec.Code != http.StatusOK {
			return fmt.Errorf("GET %s returned %d", p.path, rec.Code)
		}
		if len(rec.Header().Values("Set-Cookie")) != 0 {
			return fmt.Errorf("GET %s sets cookies and cannot be exported as a static file", p.path)
		}
		if strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") {
			// 静的ファイルでは、全ページに同じヘッダーを付ける。ページごとに違えば書き出せない。
			if headers == nil {
				headers = rec.Header()
			}
			for _, name := range staticHeaders {
				if rec.Header().Get(name) != headers.Get(name) {
					return fmt.Errorf("pages use different %s headers; cannot export one _headers file", name)
				}
			}
		}
		files[p.file] = rec.Body.Bytes()
	}

	if nf, ok := root.(features.NotFoundFeature); ok {
		rec := httptest.NewRecorder()
		nf.HandleNotFound(rec, httptest.NewRequest(http.MethodGet, "/404", nil))
		if rec.Code != http.StatusNotFound {
			return fmt.Errorf("not found page returned %d", rec.Code)
		}
		files[notFoundFile] = rec.Body.Bytes()
	}

	if err := checkStaticRefs(files); err != nil {
		return err
	}

	var b strings.Builder
	b.WriteString("/*\n")
	for _, name := range staticHeaders {
		if v := headers.Get(name); v != "" {
			fmt.Fprintf(&b, "  %s: %s\n", name, v)
		}
	}
	files[headersFile] = []byte(b.String())

	return writeExport(dir, files)
}

// staticRefPattern は、HTML の href・src にある /static/ への参照です（外部のURL・ページへのリンク・canonical は対象外）。
var staticRefPattern = regexp.MustCompile(`(?:href|src)="(/static/[^"]*)"`)

// checkStaticRefs は、書き出す HTML が参照する /static/ のファイルが、すべて書き出しの対象に入っているかを確かめます。
// 書き出すファイルは staticPages の固定の一覧なので、レイアウトが一覧に無いファイルを参照すると、静的ホスティングで欠けるためです。
// 版の ?v= とフラグメントは除いて照合します（静的ホスティングがクエリを無視してファイルを返すかは、使うホストで確かめる）。
func checkStaticRefs(files map[string][]byte) error {
	for name, body := range files {
		if !strings.HasSuffix(name, ".html") {
			continue
		}
		for _, m := range staticRefPattern.FindAllStringSubmatch(string(body), -1) {
			u, err := url.Parse(html.UnescapeString(m[1]))
			if err != nil {
				return fmt.Errorf("%s refers to an invalid URL %q", name, m[1])
			}
			if _, ok := files[strings.TrimPrefix(u.Path, "/")]; !ok {
				return fmt.Errorf("%s refers to %s, which is not exported", name, u.Path)
			}
		}
	}
	return nil
}

// writeExport は書き出し先を確認してからファイルを書きます。
// 書き出し先に、この処理が作るもの以外のファイルがあれば、上書き・削除せずに拒否します。
func writeExport(dir string, files map[string][]byte) error {
	// 前回の書き出しで作った可能性のあるファイル。今回作らないものは削除する（古いsitemap等を残さない）。
	known := []string{filepath.FromSlash(headersFile), notFoundFile}
	for _, p := range staticPages {
		known = append(known, filepath.FromSlash(p.file))
	}
	if info, err := os.Lstat(dir); err == nil {
		if !info.IsDir() {
			return fmt.Errorf("%s is not a directory", dir)
		}
		err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(dir, path)
			if err != nil {
				return err
			}
			if d.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("%s contains a symbolic link (%s); refusing to write", dir, rel)
			}
			if d.IsDir() || slices.Contains(known, rel) {
				return nil
			}
			return fmt.Errorf("%s contains a file not created by export-static (%s); use an empty directory", dir, rel)
		})
		if err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, name := range known {
		if _, ok := files[filepath.ToSlash(name)]; !ok {
			if err := os.Remove(filepath.Join(dir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	for name, body := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, body, 0o644); err != nil {
			return err
		}
	}
	return nil
}
