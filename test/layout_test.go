package test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"minimal-web-kit/features"
	"minimal-web-kit/features/auth"
	"minimal-web-kit/features/catalog"
	"minimal-web-kit/features/contact"
	"minimal-web-kit/features/news"
	"minimal-web-kit/features/pages"
	"minimal-web-kit/features/site"
	"minimal-web-kit/internal/app"
	"minimal-web-kit/internal/store"
	"minimal-web-kit/internal/web"
)

const (
	siteSubtitle = "個人情報の取得と依存関係を最小限に抑える、Webサイト・業務アプリの開発基盤"
	coreSubtitle = "個人情報・外部依存を最小限に抑えたWebアプリのひな形"
)

// newSiteNewsRoutes は、サーバーの起動処理と同じ順で、Site＋お知らせ機能を組み込んだ経路を作ります。
func newSiteNewsRoutes(t *testing.T) (http.Handler, *news.PostStore) {
	t.Helper()
	t.Setenv("SITE_URL", "https://example.workers.dev")
	t.Setenv("AUTH_ORIGIN", "")
	t.Setenv("AUTH_RP_ID", "")
	t.Setenv("NEWS_CORS_ORIGINS", "")
	logger := discardLogger()
	s, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	// 起動処理と同じ登録・初期化・つなぎ方（app.BuildRoutes）で組み立てる
	reg := features.NewRegistry()
	reg.Register(site.NewFeature(logger))
	reg.Register(news.NewFeature(logger))
	reg.Register(auth.NewFeature(logger))
	for _, f := range reg.All() {
		if err := f.Init(s.DB()); err != nil {
			t.Fatal(err)
		}
	}
	routes, err := app.BuildRoutes(s, reg, false, logger)
	if err != nil {
		t.Fatal(err)
	}
	posts := news.NewPostStore(s.DB())
	id, _ := posts.Create(context.Background(), "お知らせ", "本文")
	_ = posts.Publish(context.Background(), id)
	return routes, posts
}

// Siteが有効なら、/・/privacy・/news が同じヘッダー・フッター（Siteのレイアウト）で描かれること。
func TestLayout_SiteLayoutIsSharedAcrossPages(t *testing.T) {
	routes, _ := newSiteNewsRoutes(t)

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
			`<details class="public-menu">`,
			`<a href="/news"`,
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

// 管理者認証が有効なら、/privacy に管理画面のアカウントで保存する情報が載り、無効なら載らないこと。
func TestPrivacy_AdminAuthNotice(t *testing.T) {
	routes, _ := newSiteNewsRoutes(t)
	rec := httptest.NewRecorder()
	routes.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/privacy", nil))
	for _, want := range []string{"<h2>管理画面のアカウントで保存する情報</h2>", "<td>パスキー</td>", "<td>無操作2時間または発行から12時間。ログアウト時等にも削除</td>"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("privacy page with auth: missing %s", want)
		}
	}
	s := store.NewMemoryStore()
	h, err := web.NewHandler(s, web.NewSessionManager(s, false), discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	h.Routes(nil, nil).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/privacy", nil))
	if strings.Contains(rec.Body.String(), "管理画面のアカウント") {
		t.Fatal("privacy page without auth must not describe admin accounts")
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

// SEO: ページごとの説明文、記事の og:type、sitemap.xml の記事、公開ページのキャッシュ、favicon。
// 不変条件: NEWS-INV-1
func TestSEO_PerPageMetaSitemapCacheAndFavicon(t *testing.T) {
	routes, posts := newSiteNewsRoutes(t)
	ctx := context.Background()
	long := strings.Repeat("あ", 130)
	articleID, _ := posts.Create(ctx, "長い記事", "一行目\n\n"+long)
	_ = posts.Publish(ctx, articleID)
	draftID, _ := posts.Create(ctx, "下書き", "")
	get := func(path string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		for _, c := range cookies {
			req.AddCookie(c)
		}
		rec := httptest.NewRecorder()
		routes.ServeHTTP(rec, req)
		return rec
	}
	article := "/news/" + strconv.FormatInt(articleID, 10)

	body := get(article).Body.String()
	for _, want := range []string{
		`<meta name="description" content="一行目 ` + strings.Repeat("あ", 116) + `…">`,
		`<meta property="og:type" content="article">`,
		`<meta property="article:published_time" content="`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("article: missing %s", want)
		}
	}
	if !strings.Contains(get("/privacy").Body.String(), `content="このサイトで保存する情報`) {
		t.Error("privacy must have its own description")
	}
	if strings.Contains(get("/").Body.String(), `og:type" content="article"`) {
		t.Error("the top page must stay og:type website")
	}

	sitemap := get("/sitemap.xml").Body.String()
	if !strings.Contains(sitemap, "<loc>https://example.workers.dev"+article+"</loc><lastmod>") || !strings.Contains(sitemap, "<loc>https://example.workers.dev/news</loc><lastmod>") {
		t.Errorf("sitemap must list published posts with lastmod:\n%s", sitemap)
	}
	if strings.Contains(sitemap, "/news/"+strconv.FormatInt(draftID, 10)+"<") || strings.Count(sitemap, "<loc>https://example.workers.dev/news</loc>") != 1 {
		t.Errorf("sitemap must not list drafts or duplicate /news:\n%s", sitemap)
	}

	// Cookieも利用者ごとの内容も持たない公開ページだけをキャッシュさせる
	for _, path := range []string{"/", "/privacy", "/news", article, "/api/news", "/robots.txt", "/sitemap.xml", "/static/css/style.css", "/favicon.ico"} {
		if got := get(path).Header().Get("Cache-Control"); !strings.HasPrefix(got, "public, max-age=") {
			t.Errorf("%s: Cache-Control = %q", path, got)
		}
	}
	// 管理画面は、前段に書き換えさせない指定（no-transform）も付ける
	for path, want := range map[string]string{"/admin/login": "no-store, max-age=0, no-transform", "/account/delete-confirm": "no-store, max-age=0"} {
		if got := get(path).Header().Get("Cache-Control"); got != want {
			t.Errorf("%s: Cache-Control = %q", path, got)
		}
	}

	if rec := get("/favicon.ico"); rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/png" {
		t.Errorf("favicon.ico: %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
	favicon, err := web.AssetURL("img/favicon.svg")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/", "/privacy", "/news", "/admin/login"} {
		if !strings.Contains(get(path).Body.String(), `<link rel="icon" href="`+favicon+`" type="image/svg+xml">`) {
			t.Errorf("%s: missing favicon link", path)
		}
	}
}

// CSS とアイコンは共通部品 static-head の1か所から、版付きの URL で読み込むこと。
// Site の有効・無効（各機能の予備のレイアウト・Base）と管理画面で、CSS が1本、アイコンが各1つだけ出ることを確かめる。
func TestLayout_StaticHeadOnEveryLayout(t *testing.T) {
	var links []string
	for _, name := range []string{"css/style.css", "img/favicon.svg", "img/favicon-32.png", "img/apple-touch-icon.png"} {
		u, err := web.AssetURL(name)
		if err != nil {
			t.Fatal(err)
		}
		links = append(links, `href="`+u+`"`)
	}
	check := func(label string, routes http.Handler, paths ...string) {
		t.Helper()
		for _, path := range paths {
			rec := httptest.NewRecorder()
			routes.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
			body := rec.Body.String()
			if rec.Code != http.StatusOK {
				t.Errorf("%s %s: %d", label, path, rec.Code)
				continue
			}
			for _, link := range links {
				if strings.Count(body, link) != 1 {
					t.Errorf("%s %s: %s must appear once", label, path, link)
				}
			}
			if strings.Count(body, `rel="stylesheet"`) != 1 || strings.Contains(body, `"/static/css/style.css"`) {
				t.Errorf("%s %s: must load only the versioned stylesheet", label, path)
			}
		}
	}

	withSite, _ := newSiteNewsRoutes(t)
	check("site", withSite, "/", "/privacy", "/news", "/news/1", "/admin/login")

	// Site が無効なら、お知らせは自分の予備のレイアウトで、/ と /privacy は Base のレイアウトで描く
	t.Setenv("AUTH_ORIGIN", "")
	t.Setenv("AUTH_RP_ID", "")
	t.Setenv("NEWS_CORS_ORIGINS", "")
	s, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	reg := features.NewRegistry()
	reg.Register(news.NewFeature(discardLogger()))
	reg.Register(auth.NewFeature(discardLogger()))
	for _, f := range reg.All() {
		if err := f.Init(s.DB()); err != nil {
			t.Fatal(err)
		}
	}
	noSite, err := app.BuildRoutes(s, reg, false, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	check("no site", noSite, "/", "/privacy", "/news", "/admin/login")
}

// Baseの /privacy はセッションを扱うため、キャッシュさせないこと。
func TestSEO_BasePrivacyIsNotCached(t *testing.T) {
	s := store.NewMemoryStore()
	h, _ := web.NewHandler(s, web.NewSessionManager(s, false), discardLogger())
	rec := httptest.NewRecorder()
	h.Routes(nil, nil).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/privacy", nil))
	if got := rec.Header().Get("Cache-Control"); got != "no-store, max-age=0" {
		t.Fatalf("Base privacy: Cache-Control = %q", got)
	}
}

// お知らせ機能は管理者認証を前提にするため、管理者認証が無ければ起動時に止まること（add-auth を案内する）。
// 不変条件: NEWS-INV-9
func TestBuildRoutes_NewsRequiresAuth(t *testing.T) {
	t.Setenv("NEWS_CORS_ORIGINS", "")
	s, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	reg := features.NewRegistry()
	cm := news.NewFeature(discardLogger())
	if err := cm.Init(s.DB()); err != nil {
		t.Fatal(err)
	}
	reg.Register(cm)
	if _, err := app.BuildRoutes(s, reg, false, discardLogger()); err == nil || !strings.Contains(err.Error(), "add-auth") {
		t.Fatalf("news without auth must fail with guidance: %v", err)
	}
}

// ナビの今いるページの印は、一致するリンクにだけ付き、値の無い画面やページ内リンクには付かないこと。
func TestLayout_NavCurrent(t *testing.T) {
	routes, _ := newSiteNewsRoutes(t)
	for _, c := range []struct {
		path string
		code int
		want string // 空なら、印が1つも無いこと
	}{
		{"/", http.StatusOK, `<a href="/" aria-current="page">`},
		{"/privacy", http.StatusOK, `<a href="/privacy" aria-current="page">`},
		{"/news", http.StatusOK, `<a href="/news" aria-current="page">`},
		{"/news?page=2", http.StatusOK, `<a href="/news" aria-current="page">`},
		{"/news/1", http.StatusOK, `<a href="/news" aria-current="true">`},
		{"/no-such-page", http.StatusNotFound, ""},
	} {
		rec := httptest.NewRecorder()
		routes.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, c.path, nil))
		body := rec.Body.String()
		if rec.Code != c.code {
			t.Errorf("%s: %d", c.path, rec.Code)
			continue
		}
		wantCount := 0
		if c.want != "" {
			wantCount = 1
			if !strings.Contains(body, c.want) {
				t.Errorf("%s: missing %s", c.path, c.want)
			}
		}
		if got := strings.Count(body, "aria-current"); got != wantCount {
			t.Errorf("%s: aria-current が %d 個", c.path, got)
		}
	}

	// Siteが無いときは、Baseのレイアウトでホームに印が付く
	s := store.NewMemoryStore()
	h, err := web.NewHandler(s, web.NewSessionManager(s, false), discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.Routes(nil, nil).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if !strings.Contains(rec.Body.String(), `<a href="/" aria-current="page">`) {
		t.Fatal("Base: ホームに印が無い")
	}
}

// TestLayout_PublicMenu は、Siteの有無にかかわらず全公開ページが同じリンクを1組だけ描くことを確かめます。
func TestLayout_PublicMenu(t *testing.T) {
	for _, withSite := range []bool{false, true} {
		t.Run(strconv.FormatBool(withSite), func(t *testing.T) {
			t.Setenv("CONTACT_FORM_URL", "")
			t.Setenv("AUTH_ORIGIN", "")
			t.Setenv("AUTH_RP_ID", "")
			core, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "app.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer core.Close()
			reg := features.NewRegistry()
			if withSite {
				reg.Register(site.NewFeature(discardLogger()))
			}
			reg.Register(auth.NewFeature(discardLogger()))
			reg.Register(news.NewFeature(discardLogger()))
			reg.Register(catalog.NewFeature(discardLogger()))
			reg.Register(contact.NewFeature(discardLogger()))
			pf := pages.NewFeature(discardLogger())
			reg.Register(pf)
			for _, f := range reg.All() {
				if err := f.Init(core.DB()); err != nil {
					t.Fatal(err)
				}
			}
			ctx := context.Background()
			ps := pages.NewStore(core.DB())
			id, err := ps.Save(ctx, 0, pages.Input{Slug: "about", Title: "紹介", Body: "本文", NavOrder: "1"}, func(string) bool { return false })
			if err != nil {
				t.Fatal(err)
			}
			if err := ps.SetPublished(ctx, id, true); err != nil {
				t.Fatal(err)
			}
			if err := pf.Purge(ctx); err != nil {
				t.Fatal(err)
			}
			routes, err := app.BuildRoutes(core, reg, false, discardLogger())
			if err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{"/", "/privacy", "/news", "/catalog", "/contact", "/about"} {
				rec := httptest.NewRecorder()
				routes.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
				if rec.Code != 200 {
					t.Fatalf("%s: %d", path, rec.Code)
				}
				body := rec.Body.String()
				start, end := strings.Index(body, "<header>"), strings.Index(body, "</header>")
				if start < 0 || end < start {
					t.Fatalf("%s: header missing", path)
				}
				header := body[start:end]
				if strings.Count(header, `class="public-menu"`) != 1 {
					t.Errorf("%s: public-menu must occur once", path)
				}
				for _, href := range []string{"/news", "/catalog", "/contact", "/about", "/privacy"} {
					if strings.Count(header, `href="`+href+`"`) != 1 {
						t.Errorf("%s: link %s must occur once", path, href)
					}
				}
				for _, unexpected := range []string{"#features", "#quality", "#faq", "github.com"} {
					if strings.Contains(header, unexpected) {
						t.Errorf("%s: Site-specific item %s", path, unexpected)
					}
				}
				if len(rec.Header().Values("Set-Cookie")) != 0 {
					t.Errorf("%s: unexpected cookie", path)
				}
			}
			// 固定ページのナビ更新は、描画のたびに同じ共通部品へ届く。
			if err := ps.SetPublished(ctx, id, false); err != nil {
				t.Fatal(err)
			}
			if err := pf.Purge(ctx); err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			routes.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/news", nil))
			if strings.Contains(rec.Body.String(), `href="/about"`) {
				t.Fatal("unpublished link remains")
			}
		})
	}
}
