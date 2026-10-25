package test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"minimal-web-kit/features"
	"minimal-web-kit/features/cms"
	"minimal-web-kit/features/site"
	"minimal-web-kit/internal/app"
	"minimal-web-kit/internal/web"
)

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func siteRegistry() *features.Registry {
	r := features.NewRegistry()
	r.Register(site.NewFeature(discardLogger()))
	return r
}

// TestExportStatic_Site は、Siteの公開ページがGoのサーバーと同じ内容・同じセキュリティヘッダーで書き出されることを検証します。
func TestExportStatic_Site(t *testing.T) {
	t.Setenv("CONTACT_FORM_URL", "https://forms.example.com/contact")
	dir := filepath.Join(t.TempDir(), "dist")
	if err := app.ExportStatic(siteRegistry(), dir, discardLogger()); err != nil {
		t.Fatal(err)
	}
	read := func(name string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	index := read("index.html")
	if !strings.Contains(index, "https://forms.example.com/contact") || !strings.Contains(index, `<link rel="stylesheet" href="/static/css/style.css">`) {
		t.Fatal("index.html must contain the contact URL and the stylesheet")
	}
	if !strings.Contains(read("privacy.html"), "プライバシーポリシー") {
		t.Fatal("privacy.html must be exported")
	}
	css, _ := os.ReadFile("../internal/web/static/css/style.css")
	if read("static/css/style.css") != string(css) {
		t.Fatal("style.css must be exported unchanged")
	}

	// _headers はGoのサーバーと同じセキュリティヘッダーを全経路に付ける
	rec := httptest.NewRecorder()
	web.SecurityHeaders(http.NotFoundHandler()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	headers := read("_headers")
	if !strings.HasPrefix(headers, "/*\n") {
		t.Fatalf("_headers must apply to all paths:\n%s", headers)
	}
	for _, name := range []string{"Content-Security-Policy", "X-Content-Type-Options", "X-Frame-Options", "Referrer-Policy"} {
		if !strings.Contains(headers, "  "+name+": "+rec.Header().Get(name)+"\n") {
			t.Errorf("_headers must contain %s with the server's value", name)
		}
	}
	if strings.Contains(headers, "script-src 'self'") || strings.Contains(headers, "Cache-Control") {
		t.Fatal("_headers must not relax the CSP or add no-store")
	}

	// SEO用のファイルと404ページも書き出す
	if !strings.Contains(read("robots.txt"), "User-agent: *") || !strings.Contains(read("404.html"), "ページが見つかりません") {
		t.Fatal("robots.txt and 404.html must be exported")
	}
	if png, _ := os.ReadFile("../internal/web/static/img/ogp.png"); read("static/img/ogp.png") != string(png) {
		t.Fatal("the OGP image must be exported unchanged")
	}
	if _, err := os.Stat(filepath.Join(dir, "sitemap.xml")); !os.IsNotExist(err) {
		t.Fatal("sitemap.xml needs SITE_URL")
	}
	t.Setenv("SITE_URL", "https://example.workers.dev")
	if err := app.ExportStatic(siteRegistry(), dir, discardLogger()); err != nil {
		t.Fatalf("re-export: %v", err)
	}
	if !strings.Contains(read("sitemap.xml"), "<loc>https://example.workers.dev/</loc>") || !strings.Contains(read("index.html"), "og:image") {
		t.Fatal("SITE_URL must enable sitemap.xml and OGP")
	}

	// 今回作らないファイル（SITE_URLを外した後のsitemap.xml）は残さない
	t.Setenv("SITE_URL", "")
	if err := app.ExportStatic(siteRegistry(), dir, discardLogger()); err != nil {
		t.Fatalf("re-export: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "sitemap.xml")); !os.IsNotExist(err) {
		t.Fatal("a stale sitemap.xml must be removed")
	}
}

func TestExportStatic_RefusesUnsafeTargetsAndFeatures(t *testing.T) {
	// Siteが無い（Base版）
	if err := app.ExportStatic(features.NewRegistry(), t.TempDir(), discardLogger()); err == nil || !strings.Contains(err.Error(), "add-site") {
		t.Fatalf("Base must be refused: %v", err)
	}
	// CMS等の動的な機能が有効
	withCMS := siteRegistry()
	withCMS.Register(cms.NewFeature(discardLogger()))
	if err := app.ExportStatic(withCMS, t.TempDir(), discardLogger()); err == nil || !strings.Contains(err.Error(), "cms") {
		t.Fatalf("CMS must be refused: %v", err)
	}

	// 書き出し先に別のファイルがあれば、消さずに拒否する
	dir := t.TempDir()
	other := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(other, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := app.ExportStatic(siteRegistry(), dir, discardLogger()); err == nil {
		t.Fatal("a directory with other files must be refused")
	}
	if b, _ := os.ReadFile(other); string(b) != "keep" {
		t.Fatal("other files must be kept")
	}
	if _, err := os.Stat(filepath.Join(dir, "index.html")); !os.IsNotExist(err) {
		t.Fatal("nothing must be written when refused")
	}

	// 書き出し先がファイル・シンボリックリンクを含む場合も拒否する
	if err := app.ExportStatic(siteRegistry(), other, discardLogger()); err == nil {
		t.Fatal("a file target must be refused")
	}
	linkDir := t.TempDir()
	if err := os.Symlink(t.TempDir(), filepath.Join(linkDir, "static")); err != nil {
		t.Skip("symlinks not supported")
	}
	if err := app.ExportStatic(siteRegistry(), linkDir, discardLogger()); err == nil {
		t.Fatal("a symbolic link inside the target must be refused")
	}
}
