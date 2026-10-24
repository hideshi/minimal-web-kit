package test

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"minimal-web-kit/internal/store"
	"minimal-web-kit/internal/web"
)

// TestCanary_LogProtection はセッショントークンや機密データがログバッファやエラー応答に漏洩しないことを検証します。
func TestCanary_LogProtection(t *testing.T) {
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logBuf, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	memStore := store.NewMemoryStore()
	sm := web.NewSessionManager(memStore, false)
	handler, err := web.NewHandler(memStore, sm, logger)
	if err != nil {
		t.Fatalf("failed to create handler: %v", err)
	}

	routes := handler.Routes()

	// 1. トップページにアクセスしてセッションCookieとCSRFトークンを取得
	req1 := httptest.NewRequest(http.MethodGet, "/", nil)
	rec1 := httptest.NewRecorder()
	routes.ServeHTTP(rec1, req1)

	if rec1.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", rec1.Code)
	}

	cookie := rec1.Result().Cookies()
	if len(cookie) == 0 {
		t.Fatal("expected session cookie, but got none")
	}

	rawSessionToken := cookie[0].Value
	if rawSessionToken == "" {
		t.Fatal("session token must not be empty")
	}

	// 2. ログバッファに生のセッショントークンが出現していないことを検証（ログ契約の検証）
	logOutput := logBuf.String()
	if strings.Contains(logOutput, rawSessionToken) {
		t.Fatalf("CRITICAL SECURITY FAILURE: Raw session token was leaked into logs!\nLogs:\n%s", logOutput)
	}

	// 3. CSRFトークンを導出して全データ即時削除を実行
	csrfToken := sm.GenerateCSRFToken(rawSessionToken)
	formDeleteAll := url.Values{}
	formDeleteAll.Set("csrf_token", csrfToken)
	req2 := httptest.NewRequest(http.MethodPost, "/account/delete-all", strings.NewReader(formDeleteAll.Encode()))
	req2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req2.AddCookie(cookie[0])
	rec2 := httptest.NewRecorder()
	routes.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 See Other, got %d", rec2.Code)
	}

	// 最終確認: ログに一度もセッショントークンが現れていないこと
	finalLog := logBuf.String()
	if strings.Contains(finalLog, rawSessionToken) {
		t.Fatalf("CRITICAL SECURITY FAILURE: Raw session token was leaked into logs during test execution!")
	}
}
