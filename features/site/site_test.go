package site_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"minimal-web-kit/features/site"
	"minimal-web-kit/internal/domain"
	"minimal-web-kit/internal/store"
	"minimal-web-kit/internal/web"
)

func TestSiteFeature_HandleRoot(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	feat := site.NewFeature(logger)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()

	feat.HandleRoot(rec, req)

	resp := rec.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read response body: %v", err)
	}
	bodyStr := string(body)

	expectedSnippets := []string{
		"ムダを削ぎ落とし、最短で公開する。",
		"直接依存はSQLiteのみ &amp; 単一バイナリ",
		"<title>個人情報と依存を最小限に抑えるWeb開発基盤 - minimal-web-kit</title>",
		"個人情報の取得最小化 &amp; 漏洩リスクへの対策",
		"アクセシブルなUIデザイン",
		"公的Webアクセシビリティ指針に基づくミニマルUI",
		`<div class="hero-cta">
    <a href="https://github.com/hideshi/minimal-web-kit" target="_blank" rel="noopener noreferrer" class="btn btn-primary btn-cta">
      <span class="btn-main-text">GitHubでキットを入手する</span>
      <span class="btn-sub-text">新しいタブで開きます</span>`,
		`<div class="hero-links">`,
		`<span class="btn-main-text">GitHubでキットを入手する</span>`,
		`rel="noopener noreferrer">GitHub（新しいタブ）</a>`,
		"<details",
		"<summary>",
		"/privacy",
		"id=\"contact\"",
		"お問い合わせ・フィードバック",
		"id=\"use-cases\"",
		"広がるユースケース",
		"SITE（LP TEMPLATE）",
		"CMS（NEWS）",
		"社内ツール &amp; 業務Webアプリ",
		"マイクロSaaS",
		"選べるスターター ＆ 自在な拡張基盤",
		"完全セッションレス",
		"STANDARD LIBRARY FIRST",
		"プライバシーポリシー・免責事項",
		"公開ページはCookieなしで閲覧できます。",
		"お問い合わせ窓口は現在準備中です。",
		"新しいタブで開きます",
		"target=\"_blank\"",
		"rel=\"noopener noreferrer\"",
	}

	// GitHubへのリンクは、すべて新しいタブで開き、opener・referrerを渡さないこと
	if got := strings.Count(bodyStr, `href="https://github.com/hideshi/minimal-web-kit"`); got != 3 {
		t.Errorf("expected 3 GitHub CTAs (hero, how-it-works, nav), got %d", got)
	}
	if got := strings.Count(bodyStr, `href="https://github.com/hideshi/minimal-web-kit" target="_blank" rel="noopener noreferrer"`); got != 3 {
		t.Errorf("all GitHub CTAs must open in a new tab with noopener noreferrer, got %d", got)
	}

	for _, snippet := range expectedSnippets {
		if !strings.Contains(bodyStr, snippet) {
			t.Errorf("response body does not contain expected snippet: %q", snippet)
		}
	}
}

func TestSiteFeature_CustomContactFormURL(t *testing.T) {
	customURL := "https://example.com/inquiry"
	t.Setenv("CONTACT_FORM_URL", customURL)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	feat := site.NewFeature(logger)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()

	feat.HandleRoot(rec, req)

	body, err := io.ReadAll(rec.Result().Body)
	if err != nil {
		t.Fatalf("failed to read response: %v", err)
	}
	if !strings.Contains(string(body), `href="`+customURL+`"`) {
		t.Errorf("expected response to contain custom contact URL %q", customURL)
	}
	if strings.Contains(string(body), "準備中") {
		t.Errorf("configured contact URL must not show the placeholder notice")
	}
}

// 未設定・https以外のURLでは、汎用ページや危険なURLへのボタンを出さず準備中と表示すること。
func TestSiteFeature_ContactFormURLUnsetOrInvalid(t *testing.T) {
	for _, v := range []string{"", "http://example.com/inquiry", "javascript:alert(1)", "https://", "not a url"} {
		t.Run(v, func(t *testing.T) {
			t.Setenv("CONTACT_FORM_URL", v)
			var logBuf strings.Builder
			feat := site.NewFeature(slog.New(slog.NewTextHandler(&logBuf, nil)))

			rec := httptest.NewRecorder()
			feat.HandleRoot(rec, httptest.NewRequest(http.MethodGet, "/", nil))
			body, err := io.ReadAll(rec.Result().Body)
			if err != nil {
				t.Fatalf("failed to read response: %v", err)
			}
			bodyStr := string(body)
			if strings.Contains(bodyStr, "お問い合わせフォームを開く") {
				t.Errorf("contact button must be hidden for %q", v)
			}
			if !strings.Contains(bodyStr, "お問い合わせ窓口は現在準備中です。") {
				t.Errorf("placeholder notice must be shown for %q", v)
			}
			if !strings.Contains(logBuf.String(), "contact form url is not configured") {
				t.Errorf("startup warning must be logged for %q", v)
			}
			if v != "" && strings.Contains(logBuf.String(), v) {
				t.Errorf("configured value must not be logged: %q", v)
			}
		})
	}
}

// 観測対象は保存・照合。既存CookieがあってもSite公開ページでは呼ばれないこと。
type observedStore struct {
	store.SessionStore
	lookups int
	creates int
}

func (s *observedStore) GetSessionOwner(ctx context.Context, hash string) (domain.OwnerID, error) {
	s.lookups++
	return s.SessionStore.GetSessionOwner(ctx, hash)
}

func (s *observedStore) CreateSession(ctx context.Context, hash string, owner domain.OwnerID) error {
	s.creates++
	return s.SessionStore.CreateSession(ctx, hash, owner)
}

func TestSitePublicRoutes_SessionlessAndPrivacy(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := &observedStore{SessionStore: store.NewMemoryStore()}
	sm := web.NewSessionManager(s, false)
	h, err := web.NewHandler(s, sm, logger)
	if err != nil {
		t.Fatal(err)
	}
	feat := site.NewFeature(logger)
	routes := h.Routes(feat.HandleRoot, nil)
	seed := httptest.NewRecorder()
	if _, _, err := sm.EnsureSession(seed, httptest.NewRequest("GET", "/", nil)); err != nil {
		t.Fatal(err)
	}
	validCookie := seed.Result().Cookies()[0]
	s.creates = 0
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		for _, path := range []string{"/", "/privacy", "/static/css/style.css"} {
			for _, cookie := range []*http.Cookie{nil, validCookie, {Name: "session", Value: "unknown"}} {
				req := httptest.NewRequest(method, path, nil)
				if cookie != nil {
					req.AddCookie(cookie)
				}
				rec := httptest.NewRecorder()
				routes.ServeHTTP(rec, req)
				if rec.Code != http.StatusOK {
					t.Fatalf("%s %s: status %d", method, path, rec.Code)
				}
				if len(rec.Header().Values("Set-Cookie")) != 0 {
					t.Fatalf("%s %s modified cookies", method, path)
				}
				if path == "/privacy" && method == http.MethodGet {
					body := rec.Body.String()
					if strings.Contains(body, "<table>") || strings.Contains(body, "/account/delete-confirm") || strings.Contains(body, "アクセス識別が切断") {
						t.Fatal("Site privacy exposes session controls or table")
					}
					if !strings.Contains(body, "お問い合わせ") || !strings.Contains(body, "アクセスログ") {
						t.Fatal("Site privacy omits contact or logging information")
					}
					if !strings.Contains(body, `<h2 id="disclaimer">免責事項</h2>`) || !strings.Contains(body, "MIT License") {
						t.Fatal("Site privacy omits the disclaimer")
					}
				}
			}
		}
	}
	if s.lookups != 0 || s.creates != 0 {
		t.Fatalf("public pages accessed sessions: lookups=%d creates=%d", s.lookups, s.creates)
	}

	// Baseには保存項目と削除動線を残す。
	rec := httptest.NewRecorder()
	h.Routes(nil, nil).ServeHTTP(rec, httptest.NewRequest("GET", "/privacy", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "<table>") || !strings.Contains(rec.Body.String(), "/account/delete-confirm") {
		t.Fatal("Base privacy lost session disclosure")
	}
	// 免責事項はSite（このキットを紹介するサイト）の例で、Baseには出さない
	if strings.Contains(rec.Body.String(), "免責事項") {
		t.Fatal("Base privacy must not include the site disclaimer")
	}
}

func TestSiteAndCore_ShowFeatureNavLinks(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := store.NewMemoryStore()
	sm := web.NewSessionManager(s, false)
	h, err := web.NewHandler(s, sm, logger)
	if err != nil {
		t.Fatal(err)
	}
	feat := site.NewFeature(logger)
	routes := h.Routes(feat.HandleRoot, nil)
	get := func(path string) string {
		rec := httptest.NewRecorder()
		routes.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec.Body.String()
	}

	// 機能のリンクが無ければ、お知らせへのリンクは出さない（404になるリンクを出さない）
	for _, path := range []string{"/", "/privacy"} {
		if body := get(path); strings.Contains(body, `href="/news"`) {
			t.Fatalf("%s must not link to /news without the CMS", path)
		}
	}

	links := []web.NavLink{{Label: "お知らせ", Href: "/news"}}
	feat.SetNavLinks(links)
	h.SetNavLinks(links)
	body := get("/")
	news, contact := strings.Index(body, `<a href="/news">お知らせ</a>`), strings.Index(body, `<a href="/#contact">お問い合わせ</a>`)
	if news < 0 || contact < 0 || news > contact {
		t.Fatal("the site nav must show the feature link before the contact link")
	}
	if !strings.Contains(body, `<a href="/privacy">プライバシーポリシー</a>`) {
		t.Fatal("the site nav must use the label プライバシーポリシー")
	}
	if !strings.Contains(get("/privacy"), `<a href="/news">お知らせ</a>`) {
		t.Fatal("core pages must show the feature link")
	}
}

func TestSiteFeature_SEOAndOGP(t *testing.T) {
	t.Setenv("SITE_URL", "https://example.workers.dev/")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := store.NewMemoryStore()
	sm := web.NewSessionManager(s, false)
	h, err := web.NewHandler(s, sm, logger)
	if err != nil {
		t.Fatal(err)
	}
	feat := site.NewFeature(logger)
	routes, err := h.RoutesWithIsolated(feat.HandleRoot, nil, feat.RegisterIsolatedRoutes)
	if err != nil {
		t.Fatal(err)
	}
	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		routes.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec
	}

	body := get("/").Body.String()
	for _, want := range []string{
		`<meta name="description" content="個人情報の取得と依存関係を最小限に抑える`,
		`<link rel="canonical" href="https://example.workers.dev/">`,
		`<meta property="og:url" content="https://example.workers.dev/">`,
		`<meta property="og:image" content="https://example.workers.dev/static/img/ogp.png">`,
		`<meta name="twitter:card" content="summary_large_image">`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %s", want)
		}
	}

	if rec := get("/static/img/ogp.png"); rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("ogp image: %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
	robots := get("/robots.txt")
	if robots.Code != http.StatusOK || !strings.Contains(robots.Body.String(), "Sitemap: https://example.workers.dev/sitemap.xml") {
		t.Fatalf("robots.txt: %d %s", robots.Code, robots.Body.String())
	}
	sitemap := get("/sitemap.xml")
	if sitemap.Code != http.StatusOK || !strings.Contains(sitemap.Body.String(), "<loc>https://example.workers.dev/privacy</loc>") {
		t.Fatalf("sitemap.xml: %d %s", sitemap.Code, sitemap.Body.String())
	}
	for _, path := range []string{"/robots.txt", "/sitemap.xml"} {
		if rec := get(path); len(rec.Header().Values("Set-Cookie")) != 0 || rec.Header().Get("Content-Security-Policy") == "" {
			t.Errorf("%s must not set cookies and must carry security headers", path)
		}
	}

	rec := httptest.NewRecorder()
	feat.HandleNotFound(rec, httptest.NewRequest(http.MethodGet, "/missing", nil))
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "ページが見つかりません") || !strings.Contains(rec.Body.String(), `<meta name="robots" content="noindex">`) || strings.Contains(rec.Body.String(), "canonical") {
		t.Fatalf("not found page: %d", rec.Code)
	}
}

func TestSiteFeature_SEOWithoutSiteURL(t *testing.T) {
	for _, value := range []string{"", "ftp://example.com", "https://example.com/path", "https://example.com/?q=1", "not a url"} {
		t.Setenv("SITE_URL", value)
		logger := slog.New(slog.NewTextHandler(io.Discard, nil))
		s := store.NewMemoryStore()
		h, _ := web.NewHandler(s, web.NewSessionManager(s, false), logger)
		feat := site.NewFeature(logger)
		routes, err := h.RoutesWithIsolated(feat.HandleRoot, nil, feat.RegisterIsolatedRoutes)
		if err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		routes.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		body := rec.Body.String()
		// 絶対URLが要るタグは出さず、説明文は出す
		if strings.Contains(body, "og:image") || strings.Contains(body, "canonical") || !strings.Contains(body, `<meta name="description"`) {
			t.Errorf("SITE_URL=%q: unexpected head", value)
		}
		rec = httptest.NewRecorder()
		routes.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/sitemap.xml", nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("SITE_URL=%q: sitemap must be 404, got %d", value, rec.Code)
		}
	}
}
