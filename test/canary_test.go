package test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"minimal-web-kit/internal/store"
	"minimal-web-kit/internal/web"
)

// TestCanary_LogProtection はメモ本文の機密データがログバッファやエラー応答に漏洩しないことを検証します。
func TestCanary_LogProtection(t *testing.T) {
	// ログ出力を捕捉するバッファ
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

	// CSRFトークンを導出
	csrfToken := sm.GenerateCSRFToken(cookie[0].Value)

	// 2. カナリア文字列を本文に含めてメモを作成
	canarySecret := "CANARY_SECRET_DO_NOT_LEAK_XYZ_123456789"
	form := url.Values{}
	form.Set("csrf_token", csrfToken)
	form.Set("title", "機密テストメモ")
	form.Set("content", canarySecret)

	req2 := httptest.NewRequest(http.MethodPost, "/memos", strings.NewReader(form.Encode()))
	req2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req2.AddCookie(cookie[0])
	rec2 := httptest.NewRecorder()
	routes.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 See Other, got %d", rec2.Code)
	}

	// 3. ログバッファにカナリア文字列が出現していないことを検証（ログ契約の検証）
	logOutput := logBuf.String()
	if strings.Contains(logOutput, canarySecret) {
		t.Fatalf("CRITICAL SECURITY FAILURE: Canary secret was leaked into logs!\nLogs:\n%s", logOutput)
	}

	// 4. メモ一覧を再取得してメモが保存されていることを確認
	req3 := httptest.NewRequest(http.MethodGet, "/", nil)
	req3.AddCookie(cookie[0])
	rec3 := httptest.NewRecorder()
	routes.ServeHTTP(rec3, req3)

	if rec3.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", rec3.Code)
	}
	if !strings.Contains(rec3.Body.String(), "機密テストメモ") {
		t.Errorf("expected memo title to appear in HTML, but not found")
	}

	// 5. エクスポートエンドポイントからJSONを取得
	req4 := httptest.NewRequest(http.MethodGet, "/export", nil)
	req4.AddCookie(cookie[0])
	rec4 := httptest.NewRecorder()
	routes.ServeHTTP(rec4, req4)

	if rec4.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", rec4.Code)
	}

	var exportData struct {
		Count int `json:"count"`
		Memos []struct {
			ID      string `json:"id"`
			Title   string `json:"title"`
			Content string `json:"content"`
		} `json:"memos"`
	}
	body, _ := io.ReadAll(rec4.Body)
	if err := json.Unmarshal(body, &exportData); err != nil {
		t.Fatalf("failed to parse export JSON: %v", err)
	}
	if exportData.Count != 1 || exportData.Memos[0].Content != canarySecret {
		t.Fatalf("expected exported memo to contain canary secret, got count=%d", exportData.Count)
	}

	// 6. 全データ即時削除を実行
	formDeleteAll := url.Values{}
	formDeleteAll.Set("csrf_token", csrfToken)
	req5 := httptest.NewRequest(http.MethodPost, "/account/delete-all", strings.NewReader(formDeleteAll.Encode()))
	req5.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req5.AddCookie(cookie[0])
	rec5 := httptest.NewRecorder()
	routes.ServeHTTP(rec5, req5)

	if rec5.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 See Other, got %d", rec5.Code)
	}

	// 削除後の確認
	memosAfter, _ := memStore.ListMemos(req5.Context(), "test-owner")
	if len(memosAfter) != 0 {
		t.Errorf("expected 0 memos after delete all, got %d", len(memosAfter))
	}

	// 最終確認: ログに一度もカナリア文字列が現れていないこと
	finalLog := logBuf.String()
	if strings.Contains(finalLog, canarySecret) {
		t.Fatalf("CRITICAL SECURITY FAILURE: Canary secret was leaked into logs during test execution!")
	}
}
