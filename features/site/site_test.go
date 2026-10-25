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
		"直接依存はSQLiteのみ &amp; 約14MB",
		"個人情報の取得最小化 &amp; 漏洩リスクへの対策",
		"アクセシブルなUIデザイン",
		"公的Webアクセシビリティ指針に基づくミニマルUI",
		`href="https://github.com/hideshi/minimal-web-kit" target="_blank" rel="noopener noreferrer" class="btn btn-secondary">GitHubでコードを見る（新しいタブで開きます）</a>`,
		"<details",
		"<summary>",
		"/privacy",
		"id=\"contact\"",
		"お問い合わせ・フィードバック",
		"id=\"use-cases\"",
		"広がるユースケース",
		"SITE TEMPLATE",
		"CMS TEMPLATE",
		"社内ツール &amp; 業務Webアプリ",
		"マイクロSaaS",
		"選べるスターター ＆ 自在な拡張基盤",
		"完全セッションレス",
		"https://forms.google.com",
		"新しいタブで開きます",
		"target=\"_blank\"",
		"rel=\"noopener noreferrer\"",
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
	if !strings.Contains(string(body), customURL) {
		t.Errorf("expected response to contain custom contact URL %q", customURL)
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
}
