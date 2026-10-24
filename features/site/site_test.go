package site_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"minimal-web-kit/features/site"
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
		"個人情報非取得 &amp; 漏洩防止",
		"アクセシブルなUIデザイン",
		"公的Webアクセシビリティ指針に基づくミニマルUI",
		"<details",
		"<summary>",
		"/privacy",
		"id=\"contact\"",
		"お問い合わせ・フィードバック",
		"id=\"templates\"",
		"SITE TEMPLATE",
		"CMS TEMPLATE",
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
