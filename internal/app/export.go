package app

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"minimal-web-kit/features"
	"minimal-web-kit/internal/store"
	"minimal-web-kit/internal/web"
)

// staticPages は書き出す経路と、書き出し先のファイル名です。
// 静的ホスティング（Cloudflare Workers・Pages等）は privacy.html を /privacy で配信します。
var staticPages = []struct{ path, file string }{
	{"/", "index.html"},
	{"/privacy", "privacy.html"},
	{"/static/css/style.css", "static/css/style.css"},
}

// staticHeaders は、Goのサーバーと同じ値で静的ホスティングにも付けるセキュリティヘッダーです。
// Cache-Control（no-store）はセッションを扱う画面のためのもので、公開ページには付けません。
var staticHeaders = []string{"Content-Security-Policy", "X-Content-Type-Options", "X-Frame-Options", "Referrer-Policy"}

const headersFile = "_headers"

// ExportStatic は、Cookieも保存データも使わない公開ページ（Siteの / と /privacy）とCSSを、
// 静的ファイルとして dir に書き出します。セキュリティヘッダーは _headers に書き出します。
// セッションや管理画面を使う機能（CMS等）が有効な場合は、静的にできないため拒否します。
func ExportStatic(reg *features.Registry, dir string, logger *slog.Logger) error {
	var root features.CustomRootFeature
	for _, f := range reg.All() {
		if cr, ok := f.(features.CustomRootFeature); ok && root == nil {
			root = cr
			continue
		}
		return fmt.Errorf("feature %s has pages that cannot be exported as static files; enable only Site (e.g. make remove-cms)", f.Name())
	}
	if root == nil {
		return errors.New("no static homepage to export; enable Site first (make add-site)")
	}
	if _, ok := root.(features.IsolatedRoutesFeature); ok {
		return fmt.Errorf("feature %s has isolated routes that cannot be exported", root.Name())
	}

	// DBを使わない。公開ページは保存データに触れないため、メモリ上のストアで足りる。
	mem := store.NewMemoryStore()
	sm := web.NewSessionManager(mem, false)
	h, err := web.NewHandler(mem, sm, logger)
	if err != nil {
		return err
	}
	routes := h.Routes(root.HandleRoot, func(mux *http.ServeMux, sm *web.SessionManager) {
		root.RegisterRoutes(mux, sm)
	})

	files := map[string][]byte{}
	var headers http.Header
	for _, p := range staticPages {
		rec := httptest.NewRecorder()
		routes.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p.path, nil))
		if rec.Code != http.StatusOK {
			return fmt.Errorf("GET %s returned %d", p.path, rec.Code)
		}
		if len(rec.Header().Values("Set-Cookie")) != 0 {
			return fmt.Errorf("GET %s sets cookies and cannot be exported as a static file", p.path)
		}
		if p.file != "static/css/style.css" {
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

// writeExport は書き出し先を確認してからファイルを書きます。
// 書き出し先に、この処理が作るもの以外のファイルがあれば、上書き・削除せずに拒否します。
func writeExport(dir string, files map[string][]byte) error {
	known := []string{}
	for name := range files {
		known = append(known, filepath.FromSlash(name))
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
