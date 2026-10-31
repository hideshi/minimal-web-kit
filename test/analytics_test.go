package test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"minimal-web-kit/features"
	"minimal-web-kit/features/analytics"
	"minimal-web-kit/features/auth"
	"minimal-web-kit/features/catalog"
	"minimal-web-kit/features/contact"
	"minimal-web-kit/features/news"
	"minimal-web-kit/features/pages"
	"minimal-web-kit/features/site"
	"minimal-web-kit/internal/app"
	"minimal-web-kit/internal/store"
)

const testGA4ID = "G-TEST12345"

// analyticsEnv は、解析の試験に使う経路と、見本の記事のパスです。
type analyticsEnv struct {
	routes                   http.Handler
	newsPath, itemPath, page string
}

func analyticsRoutes(t *testing.T, id string, withSite bool) analyticsEnv {
	t.Helper()
	t.Setenv("AUTH_ORIGIN", "")
	t.Setenv("AUTH_RP_ID", "")
	t.Setenv("ANALYTICS_GA4_ID", id)
	core, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { core.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	reg := features.NewRegistry()
	if withSite {
		reg.Register(site.NewFeature(logger))
	}
	reg.Register(auth.NewFeature(logger))
	reg.Register(news.NewFeature(logger))
	reg.Register(catalog.NewFeature(logger))
	reg.Register(contact.NewFeature(logger))
	reg.Register(pages.NewFeature(logger))
	if id != "" {
		reg.Register(analytics.NewFeature(logger))
	}
	for _, f := range reg.All() {
		if err := f.Init(core.DB()); err != nil {
			t.Fatal(err)
		}
	}
	routes, err := app.BuildRoutes(core, reg, false, logger)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	posts := news.NewPostStore(core.DB())
	post, err := posts.Create(ctx, "見本の記事", "本文")
	if err != nil {
		t.Fatal(err)
	}
	if err := posts.Publish(ctx, post); err != nil {
		t.Fatal(err)
	}
	items := catalog.NewStore(core.DB())
	item, err := items.Save(ctx, 0, catalog.Input{Title: "見本の項目", Body: "本文"})
	if err != nil {
		t.Fatal(err)
	}
	if err := items.SetPublished(ctx, item, true); err != nil {
		t.Fatal(err)
	}
	store2 := pages.NewStore(core.DB())
	page, err := store2.Save(ctx, 0, pages.Input{Slug: "about", Title: "概要", Body: "本文"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store2.SetPublished(ctx, page, true); err != nil {
		t.Fatal(err)
	}
	return analyticsEnv{routes, "/news/" + strconv.FormatInt(post, 10), "/catalog/" + strconv.FormatInt(item, 10), "/about"}
}

func getPage(routes http.Handler, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	routes.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w
}

const analyticsMark = "data-ga4-id"

// 計測する画面にだけ、部品とCSPが同時に付く。Siteのレイアウトを使うお問い合わせにも付かない。
// 不変条件: ANALYTICS-INV-3, ANALYTICS-INV-7
func TestAnalytics_OnlyOnMeasuredPages(t *testing.T) {
	for _, withSite := range []bool{true, false} {
		e := analyticsRoutes(t, testGA4ID, withSite)
		for _, path := range []string{"/", "/privacy", "/news", e.newsPath, "/catalog", e.itemPath} {
			w := getPage(e.routes, path)
			if w.Code != http.StatusOK {
				t.Fatalf("%s: %d", path, w.Code)
			}
			if !strings.Contains(w.Body.String(), analyticsMark+`="`+testGA4ID+`"`) || !strings.Contains(w.Header().Get("Content-Security-Policy"), "googletagmanager") {
				t.Errorf("site=%v %s: analytics must be on", withSite, path)
			}
			if len(w.Header().Values("Set-Cookie")) != 0 {
				t.Errorf("%s: the server must not set cookies", path)
			}
		}
		// 固定ページの受け皿は、Siteの有無で描き方が同じ。公開ページとして確かめる
		if w := getPage(e.routes, e.page); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), analyticsMark) || !strings.Contains(w.Header().Get("Content-Security-Policy"), "googletagmanager") {
			t.Errorf("site=%v page: %d", withSite, w.Code)
		}
		for path, code := range map[string]int{"/contact": 200, "/admin/login": 200, "/admin": 303, "/no-such-page": 404, "/news/999999": 404, "/admin/no-such-page": 404} {
			w := getPage(e.routes, path)
			if w.Code != code {
				t.Errorf("site=%v %s: %d, want %d", withSite, path, w.Code, code)
			}
			if strings.Contains(w.Body.String(), analyticsMark) || strings.Contains(w.Header().Get("Content-Security-Policy"), "googletagmanager") {
				t.Errorf("site=%v %s: analytics must be off", withSite, path)
			}
		}
		// htmxの断片は既定のCSPのまま
		req := httptest.NewRequest(http.MethodGet, "/catalog", nil)
		req.Header.Set("HX-Request", "true")
		w := httptest.NewRecorder()
		e.routes.ServeHTTP(w, req)
		if strings.Contains(w.Body.String(), analyticsMark) || strings.Contains(w.Header().Get("Content-Security-Policy"), "googletagmanager") {
			t.Errorf("site=%v fragment: analytics must be off", withSite)
		}
	}
}

// お問い合わせは PublicNavigation を埋め込むので AddAnalytics を持つが、呼ばない。
// 不変条件: ANALYTICS-INV-3
func TestAnalytics_ContactNeverCallsIt(t *testing.T) {
	files, err := filepath.Glob("../features/contact/*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("%v %v", files, err)
	}
	for _, file := range files {
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "AddAnalytics") {
			t.Errorf("%s must not call AddAnalytics", file)
		}
	}
}

// 同意の処理のJSが、ブラウザのテストを待たずに守れる形であることを確かめる。CIではブラウザのテストが走らないため。
// 不変条件: ANALYTICS-INV-2
func TestAnalytics_ScriptSendsNothingByItself(t *testing.T) {
	b, err := os.ReadFile("../internal/web/static/js/analytics.js")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	for _, word := range []string{"fetch(", "sendBeacon", "XMLHttpRequest", "new Image", "innerHTML", "eval("} {
		if strings.Contains(src, word) {
			t.Errorf("analytics.js must not use %s", word)
		}
	}
	if strings.Count(src, "googletagmanager") != 1 || len(regexp.MustCompile(`\bload\(\);`).FindAllString(src, -1)) != 2 {
		t.Errorf("gtag.js must be loaded only from load(), called from the consent and from sync")
	}
}

// 同意前のHTMLに、Googleへの通信を起こす要素が無い。
// 不変条件: ANALYTICS-INV-2
func TestAnalytics_NoExternalRequestInHTML(t *testing.T) {
	e := analyticsRoutes(t, testGA4ID, true)
	body := getPage(e.routes, "/").Body.String()
	for _, word := range []string{"googletagmanager", "google-analytics", "preconnect", "dns-prefetch"} {
		if strings.Contains(body, word) {
			t.Errorf("the page must not mention %s before consent", word)
		}
	}
}

// 機能を登録しなければ何も変わらない。
// 不変条件: ANALYTICS-INV-1
func TestAnalytics_OffByDefault(t *testing.T) {
	routes := analyticsRoutes(t, "", true).routes
	for _, path := range []string{"/", "/privacy", "/news", "/catalog"} {
		w := getPage(routes, path)
		if strings.Contains(w.Body.String(), analyticsMark) || strings.Contains(w.Header().Get("Content-Security-Policy"), "google") {
			t.Errorf("%s: analytics must be absent", path)
		}
	}
	if w := getPage(routes, "/"); !strings.Contains(w.Header().Get("Content-Security-Policy"), "script-src 'none'") {
		t.Fatalf("csp = %s", w.Header().Get("Content-Security-Policy"))
	}
}

// 1つのプロセスの中でも、設定は構成ごとに独立している。
// 不変条件: ANALYTICS-INV-1
func TestAnalytics_ConfigIsPerInstance(t *testing.T) {
	on := analyticsRoutes(t, testGA4ID, true).routes
	off := analyticsRoutes(t, "", true).routes
	if !strings.Contains(getPage(on, "/").Body.String(), testGA4ID) || strings.Contains(getPage(off, "/").Body.String(), testGA4ID) {
		t.Fatal("analytics leaked between instances")
	}
}

// 不変条件: ANALYTICS-INV-6
func TestAnalytics_RejectsBadID(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, id := range []string{"", "UA-1234", "G-", `G-X"><script>`, "g-abc12345"} {
		t.Setenv("ANALYTICS_GA4_ID", id)
		core, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "app.db"))
		if err != nil {
			t.Fatal(err)
		}
		reg := features.NewRegistry()
		reg.Register(analytics.NewFeature(logger))
		_, err = app.BuildRoutes(core, reg, false, logger)
		core.Close()
		if err == nil {
			t.Errorf("%q must be refused", id)
		}
	}
}

// 書き出しでも検査が走り、JSと許可先が入る。
// 不変条件: ANALYTICS-INV-6
func TestAnalytics_StaticExport(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	dir := filepath.Join(t.TempDir(), "out")
	t.Setenv("ANALYTICS_GA4_ID", testGA4ID)
	reg := features.NewRegistry()
	reg.Register(site.NewFeature(logger))
	reg.Register(analytics.NewFeature(logger))
	if err := app.ExportStatic(reg, dir, logger); err != nil {
		t.Fatal(err)
	}
	headers, err := os.ReadFile(filepath.Join(dir, "_headers"))
	if err != nil || !strings.Contains(string(headers), "googletagmanager") {
		t.Fatalf("headers: %s %v", headers, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "static/js/analytics.js")); err != nil {
		t.Fatal(err)
	}
	// 2回目の書き出しも通り、機能を外すとJSも消える
	again := features.NewRegistry()
	again.Register(site.NewFeature(logger))
	again.Register(analytics.NewFeature(logger))
	if err := app.ExportStatic(again, dir, logger); err != nil {
		t.Fatalf("re-export: %v", err)
	}
	plain := features.NewRegistry()
	plain.Register(site.NewFeature(logger))
	if err := app.ExportStatic(plain, dir, logger); err != nil {
		t.Fatalf("export without analytics: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "static/js/analytics.js")); !os.IsNotExist(err) {
		t.Fatalf("the script must be removed: %v", err)
	}
	t.Setenv("ANALYTICS_GA4_ID", "bad")
	bad := features.NewRegistry()
	bad.Register(site.NewFeature(logger))
	bad.Register(analytics.NewFeature(logger))
	if err := app.ExportStatic(bad, filepath.Join(t.TempDir(), "out"), logger); err == nil {
		t.Fatal("a bad id must stop the export")
	}
}
