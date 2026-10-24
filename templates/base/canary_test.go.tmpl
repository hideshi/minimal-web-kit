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

// TestCanary_LogProtection はセッショントークン等の機密がログに出力されないことを検証します。
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

	// 1. 静的アセットアクセスでセッションが作成されないことを検証
	staticReq := httptest.NewRequest(http.MethodGet, "/static/css/style.css", nil)
	staticRec := httptest.NewRecorder()
	routes.ServeHTTP(staticRec, staticReq)
	if staticRec.Code != http.StatusOK {
		t.Fatalf("expected 200 for static asset, got %d", staticRec.Code)
	}
	if len(staticRec.Result().Cookies()) > 0 {
		t.Errorf("static asset request should not issue session cookies")
	}

	// 2. セッション固定化攻撃の防止検証: クライアントが任意に指定したCookieは拒否され、新トークンが発行されること
	attackerCookie := &http.Cookie{Name: "session", Value: "attacker-fixed-token-12345678"}
	reqFixed := httptest.NewRequest(http.MethodGet, "/", nil)
	reqFixed.AddCookie(attackerCookie)
	recFixed := httptest.NewRecorder()
	routes.ServeHTTP(recFixed, reqFixed)

	if recFixed.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", recFixed.Code)
	}
	newCookies := recFixed.Result().Cookies()
	if len(newCookies) == 0 {
		t.Fatal("expected new cookie to be issued when attacker-chosen cookie is rejected")
	}
	if newCookies[0].Value == "attacker-fixed-token-12345678" {
		t.Fatalf("CRITICAL SECURITY FAILURE: Attacker chosen session token was accepted without regeneration!")
	}

	// 3. 正常アクセスでセッショントークンを取得
	req1 := httptest.NewRequest(http.MethodGet, "/", nil)
	rec1 := httptest.NewRecorder()
	routes.ServeHTTP(rec1, req1)

	cookie := rec1.Result().Cookies()
	if len(cookie) == 0 {
		t.Fatal("expected session cookie, but got none")
	}

	rawSessionToken := cookie[0].Value
	if rawSessionToken == "" {
		t.Fatal("session token must not be empty")
	}

	// 4. ログバッファに生のセッショントークンが出現していないことを検証（ログ契約の検証）
	logOutput := logBuf.String()
	if strings.Contains(logOutput, rawSessionToken) {
		t.Fatalf("CRITICAL SECURITY FAILURE: Raw session token was leaked into logs!\nLogs:\n%s", logOutput)
	}

	// 5. CSRFトークンを導出して全データ即時削除を実行
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

	// 6. 最終確認: ログに一度もセッショントークンが現れていないこと
	finalLog := logBuf.String()
	if strings.Contains(finalLog, rawSessionToken) {
		t.Fatalf("CRITICAL SECURITY FAILURE: Raw session token was leaked into logs during test execution!")
	}
}
