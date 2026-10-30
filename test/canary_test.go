package test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"minimal-web-kit/internal/store"
	"minimal-web-kit/internal/web"
)

// TestCanary_LogAndPrivacyProtection はセッショントークン等の機密がログに出力されず、公開ページでCookieが発行されないことを検証します。
func TestCanary_LogAndPrivacyProtection(t *testing.T) {
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

	routes := handler.Routes(nil, nil)

	// 1. 公開ページ（GET /, GET /privacy）および静的アセットへのアクセスでCookieが一切発行されないことを検証
	publicPaths := []string{"/", "/privacy", "/static/css/style.css"}
	for _, path := range publicPaths {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		routes.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 for %s, got %d", path, rec.Code)
		}
		for _, c := range rec.Result().Cookies() {
			if c.Name == "session" && c.MaxAge > 0 {
				t.Errorf("public path %s must not issue session cookie, got cookie: %v", path, c)
			}
		}
	}

	// 2. 404アクセスでもセッションCookieが発行されないことを検証
	req404 := httptest.NewRequest(http.MethodGet, "/non-existent-page", nil)
	rec404 := httptest.NewRecorder()
	routes.ServeHTTP(rec404, req404)
	for _, c := range rec404.Result().Cookies() {
		if c.Name == "session" && c.MaxAge > 0 {
			t.Errorf("404 request must not issue session cookie, got: %v", c)
		}
	}

	// 3. セッション固定化攻撃の防止検証: クライアントが任意に指定したCookieは拒否され、クリアされること
	attackerCookie := &http.Cookie{Name: "session", Value: "attacker-fixed-token-12345678"}
	reqFixed := httptest.NewRequest(http.MethodGet, "/account/delete-confirm", nil)
	reqFixed.AddCookie(attackerCookie)
	recFixed := httptest.NewRecorder()
	routes.ServeHTTP(recFixed, reqFixed)

	clearedCookieFound := false
	for _, c := range recFixed.Result().Cookies() {
		if c.Name == "session" && (c.MaxAge == -1 || c.Value == "") {
			clearedCookieFound = true
		}
		if c.Value == "attacker-fixed-token-12345678" && c.MaxAge > 0 {
			t.Fatalf("CRITICAL SECURITY FAILURE: Attacker chosen session token was accepted!")
		}
	}
	if !clearedCookieFound {
		t.Errorf("expected attacker cookie to be cleared with MaxAge=-1")
	}

	// 4. 正当なセッションの発行と、X-Request-IDヘッダーによるログ注入防止の検証
	dummyRec := httptest.NewRecorder()
	dummyReq := httptest.NewRequest(http.MethodGet, "/", nil)
	ownerID, csrfToken, err := sm.EnsureSession(dummyRec, dummyReq)
	if err != nil {
		t.Fatalf("failed to ensure session: %v", err)
	}
	sessionCookie := dummyRec.Result().Cookies()[0]
	rawSessionToken := sessionCookie.Value

	// クライアントがセッショントークンを X-Request-ID ヘッダーに注入してリクエスト
	reqInject := httptest.NewRequest(http.MethodGet, "/", nil)
	reqInject.AddCookie(sessionCookie)
	reqInject.Header.Set("X-Request-ID", rawSessionToken)
	recInject := httptest.NewRecorder()
	routes.ServeHTTP(recInject, reqInject)

	// X-Request-ID はサーバー生成の安全なIDに上書きされ、生セッショントークンがログに出現しないこと
	logOutput := logBuf.String()
	if strings.Contains(logOutput, rawSessionToken) {
		t.Fatalf("CRITICAL SECURITY FAILURE: Raw session token was leaked into logs via X-Request-ID injection!\nLogs:\n%s", logOutput)
	}

	// 確認画面は部品 confirm-actions で、そのセッションのCSRFトークンを送る
	reqConfirm := httptest.NewRequest(http.MethodGet, "/account/delete-confirm", nil)
	reqConfirm.AddCookie(sessionCookie)
	recConfirm := httptest.NewRecorder()
	routes.ServeHTTP(recConfirm, reqConfirm)
	for _, want := range []string{`action="/account/delete-all"`, `name="csrf_token" value="` + csrfToken + `"`, "はい、破棄します", `<a href="/" class="btn btn-secondary">削除せずに戻る</a>`} {
		if !strings.Contains(recConfirm.Body.String(), want) {
			t.Fatalf("delete confirm missing %q", want)
		}
	}

	// 5. CSRFトークンを検証して全データ即時削除を実行
	formDeleteAll := url.Values{}
	formDeleteAll.Set("csrf_token", csrfToken)
	reqDelete := httptest.NewRequest(http.MethodPost, "/account/delete-all", strings.NewReader(formDeleteAll.Encode()))
	reqDelete.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	reqDelete.AddCookie(sessionCookie)
	recDelete := httptest.NewRecorder()
	routes.ServeHTTP(recDelete, reqDelete)

	if recDelete.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 See Other, got %d", recDelete.Code)
	}

	// 6. 削除成功ログにも owner_id やトークンが含まれていないことを検証
	finalLog := logBuf.String()
	if strings.Contains(finalLog, string(ownerID)) {
		t.Fatalf("SECURITY FAILURE: OwnerID was leaked into logs during delete-all!\nLogs:\n%s", finalLog)
	}
	if strings.Contains(finalLog, rawSessionToken) {
		t.Fatalf("CRITICAL SECURITY FAILURE: Raw session token was leaked into logs during test execution!")
	}
}

// TestSession_ExpirationRejection は失効セッションが確実に拒否されることを検証します。
func TestSession_ExpirationRejection(t *testing.T) {
	memStore := store.NewMemoryStore()
	sm := web.NewSessionManager(memStore, false)

	dummyRec := httptest.NewRecorder()
	dummyReq := httptest.NewRequest(http.MethodGet, "/", nil)
	_, _, err := sm.EnsureSession(dummyRec, dummyReq)
	if err != nil {
		t.Fatalf("failed to ensure session: %v", err)
	}
	cookie := dummyRec.Result().Cookies()[0]

	// 10ミリ秒待機して、1ミリ秒前を閾値としてGCを実行
	time.Sleep(10 * time.Millisecond)
	count, err := memStore.PurgeExpiredSessions(context.Background(), 1*time.Millisecond)
	if err != nil {
		t.Fatalf("purge failed: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 expired session purged, got %d", count)
	}

	// 失効したCookieでアクセスした際、エラーなく拒否されてCookieがクリアされること
	handler, err := web.NewHandler(memStore, sm, slog.Default())
	if err != nil {
		t.Fatalf("failed to create handler: %v", err)
	}
	routes := handler.Routes(nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/account/delete-confirm", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	routes.ServeHTTP(rec, req)

	// Cookieがクリアされていること
	cleared := false
	for _, c := range rec.Result().Cookies() {
		if c.Name == "session" && (c.MaxAge == -1 || c.Value == "") {
			cleared = true
		}
	}
	if !cleared {
		t.Errorf("expired session cookie must be cleared on access")
	}
}

func TestCanary_RequestAndPanicLogProtection(t *testing.T) {
	const secret = "private-canary@example.invalid"
	for _, tc := range []struct {
		name, method, path string
		panicValue         any
	}{
		{"path", "GET", "/resource/" + secret, nil},
		{"not_found", "GET", "/missing/" + secret, nil},
		{"method", "private-canary", "/", nil},
		{"panic_string", "GET", "/panic/" + secret, secret},
		{"panic_error", "GET", "/panic/" + secret, errors.New(secret)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&logs, nil))
			s := store.NewMemoryStore()
			sm := web.NewSessionManager(s, false)
			h, err := web.NewHandler(s, sm, logger)
			if err != nil {
				t.Fatal(err)
			}
			routes := h.Routes(nil, func(mux *http.ServeMux, _ *web.SessionManager) {
				mux.HandleFunc("GET /resource/{value}", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
				mux.HandleFunc("GET /panic/{value}", func(w http.ResponseWriter, r *http.Request) { panic(tc.panicValue) })
			})
			req := httptest.NewRequest(tc.method, tc.path+"?secret="+url.QueryEscape(secret), strings.NewReader(secret))
			req.Header.Set("X-Request-ID", secret)
			req.Header.Set("User-Agent", secret)
			req.Header.Set("Authorization", secret)
			req.Header.Set("Cookie", "private="+secret)
			req.RemoteAddr = "192.0.2.123:12345"
			rec := httptest.NewRecorder()
			routes.ServeHTTP(rec, req)
			if logs.Len() == 0 {
				t.Fatal("logging was disabled")
			}
			for _, value := range []string{secret, url.QueryEscape(secret), "192.0.2.123", "private-canary"} {
				if strings.Contains(logs.String(), value) {
					t.Fatalf("sensitive value leaked to log: %q", value)
				}
				if strings.Contains(rec.Body.String(), value) {
					t.Fatalf("sensitive value leaked to response: %q", value)
				}
			}
			var event map[string]any
			if err := json.Unmarshal(bytes.TrimSpace(logs.Bytes()), &event); err != nil {
				t.Fatal(err)
			}
			if event["request_id"] != rec.Header().Get("X-Request-ID") || event["request_id"] == "" {
				t.Fatal("server request ID missing")
			}
			if tc.panicValue != nil {
				if rec.Code != http.StatusInternalServerError || event["msg"] != "panic recovered" {
					t.Fatal("panic was not safely recovered")
				}
			} else {
				if event["msg"] != "http request" || event["status"] != float64(rec.Code) {
					t.Fatal("access log lost response status")
				}
				if tc.name == "method" && event["method"] != "OTHER" {
					t.Fatal("unknown method not classified")
				}
				for key := range event {
					switch key {
					case "time", "level", "msg", "request_id", "method", "status", "duration":
					default:
						t.Fatalf("unexpected access log field: %s", key)
					}
				}
			}
		})
	}
}

type failingDeleteStore struct{ store.SessionStore }

func (s failingDeleteStore) DeleteAllData(context.Context, store.OwnerID) error {
	return errors.New("db-error-private@example.invalid")
}

func TestCanary_StoreErrorLogProtection(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	s := failingDeleteStore{SessionStore: store.NewMemoryStore()}
	sm := web.NewSessionManager(s, false)
	seed := httptest.NewRecorder()
	_, csrf, err := sm.EnsureSession(seed, httptest.NewRequest("GET", "/", nil))
	if err != nil {
		t.Fatal(err)
	}
	h, err := web.NewHandler(s, sm, logger)
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{"csrf_token": {csrf}}
	req := httptest.NewRequest("POST", "/account/delete-all", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(seed.Result().Cookies()[0])
	rec := httptest.NewRecorder()
	h.Routes(nil, nil).ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError || !strings.Contains(logs.String(), "delete user data failed") {
		t.Fatal("expected deletion failure was not recorded")
	}
	if strings.Contains(logs.String()+rec.Body.String(), "db-error-private@example.invalid") {
		t.Fatal("raw database error leaked")
	}
}
