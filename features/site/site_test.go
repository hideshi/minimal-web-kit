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
		"外部依存ゼロ &amp; 単一バイナリ",
		"個人情報非取得 &amp; 漏洩防止",
		"アクセシブルなUIデザイン",
		"<details",
		"<summary>",
		"/privacy",
	}

	for _, snippet := range expectedSnippets {
		if !strings.Contains(bodyStr, snippet) {
			t.Errorf("response body does not contain expected snippet: %q", snippet)
		}
	}
}
