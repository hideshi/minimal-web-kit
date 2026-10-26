package test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"minimal-web-kit/features/cms"
	"minimal-web-kit/features/site"
	"minimal-web-kit/internal/app"
	"minimal-web-kit/internal/store"
	"minimal-web-kit/internal/web"
)

const (
	siteSubtitle = "個人情報の取得と依存関係を最小限に抑える、Webサイト・業務アプリの開発基盤"
	coreSubtitle = "個人情報・外部依存を最小限に抑えたWebアプリのひな形"
)

// Siteが有効なら、/・/privacy・/news が同じヘッダー・フッター（Siteのレイアウト）で描かれること。
func TestLayout_SiteLayoutIsSharedAcrossPages(t *testing.T) {
	t.Setenv("SITE_URL", "https://example.workers.dev")
	t.Setenv("CMS_ORIGIN", "")
	t.Setenv("CMS_RP_ID", "")
	t.Setenv("CMS_CORS_ORIGINS", "")
	logger := discardLogger()
	s, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	st := site.NewFeature(logger)
	cm := cms.NewFeature(logger)
	if err := cm.Init(s.DB()); err != nil {
		t.Fatal(err)
	}
	links := cm.NavLinks()
	st.SetNavLinks(links)
	h, err := web.NewHandler(s, web.NewSessionManager(s, false), logger)
	if err != nil {
		t.Fatal(err)
	}
	h.SetNavLinks(links)
	// サーバーの起動処理と同じ順で、Siteのレイアウトを配る
	if err := h.SetLayout(st.Layout()); err != nil {
		t.Fatal(err)
	}
	if err := cm.SetLayout(st.Layout()); err != nil {
		t.Fatal(err)
	}
	routes, err := h.RoutesWithIsolated(st.HandleRoot, cm.RegisterRoutes, func(mux *web.IsolatedMux) {
		st.RegisterIsolatedRoutes(mux)
		cm.RegisterIsolatedRoutes(mux)
	})
	if err != nil {
		t.Fatal(err)
	}
	posts := cms.NewPostStore(s.DB())
	id, _ := posts.Create(context.Background(), "お知らせ", "本文")
	_ = posts.Publish(context.Background(), id)

	for path, canonical := range map[string]string{
		"/":        "https://example.workers.dev/",
		"/privacy": "https://example.workers.dev/privacy",
		"/news":    "https://example.workers.dev/news",
		"/news/1":  "https://example.workers.dev/news/1",
	} {
		rec := httptest.NewRecorder()
		routes.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		body := rec.Body.String()
		if rec.Code != http.StatusOK {
			t.Errorf("%s: %d", path, rec.Code)
			continue
		}
		for _, want := range []string{
			siteSubtitle,
			`<a href="/#features">特長</a>`,
			`<a href="/news">お知らせ</a>`,
			`<a href="/privacy">プライバシーポリシー・免責事項</a>`,
			`<link rel="canonical" href="` + canonical + `">`,
			`<meta name="description"`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("%s: missing %s", path, want)
			}
		}
		if strings.Contains(body, coreSubtitle) {
			t.Errorf("%s: must not use the Core layout", path)
		}
		if len(rec.Header().Values("Set-Cookie")) != 0 {
			t.Errorf("%s: must not set cookies", path)
		}
	}
}

// Siteが無効（Base）なら、/privacy はCoreのレイアウトのままであること。
func TestLayout_BaseKeepsCoreLayout(t *testing.T) {
	s := store.NewMemoryStore()
	h, err := web.NewHandler(s, web.NewSessionManager(s, false), discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.Routes(nil, nil).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/privacy", nil))
	if !strings.Contains(rec.Body.String(), coreSubtitle) || strings.Contains(rec.Body.String(), siteSubtitle) {
		t.Fatal("Base must keep the Core layout")
	}
}

// 静的に書き出した privacy.html も、Siteのレイアウトで描かれること。
func TestLayout_ExportUsesSiteLayout(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "dist")
	if err := app.ExportStatic(siteRegistry(), dir, discardLogger()); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "privacy.html"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), siteSubtitle) || strings.Contains(string(b), coreSubtitle) {
		t.Fatal("exported privacy.html must use the Site layout")
	}
}
