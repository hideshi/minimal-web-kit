package test

import (
	"bytes"
	"context"
	"encoding/base64"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"minimal-web-kit/features"
	"minimal-web-kit/features/auth"
	"minimal-web-kit/features/auth/authtest"
	"minimal-web-kit/features/cms"
	"minimal-web-kit/internal/app"
	"minimal-web-kit/internal/store"
)

// TestCanary_CMSRoutesDoNotLeakInput は、CMSの公開ページ・API・管理画面に送った値（パス・クエリ・ヘッダー・本文）が
// ログに出力されず、公開ページとAPIがCookieを発行しないことを検証します。
// 不変条件: AUTH-INV-13, CMS-INV-11
func TestCanary_CMSRoutesDoNotLeakInput(t *testing.T) {
	const canary = "CANARYcms7f3a9"
	t.Setenv("AUTH_ORIGIN", "")
	t.Setenv("AUTH_RP_ID", "")
	t.Setenv("CMS_CORS_ORIGINS", "https://allowed.example")

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	s, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	// 起動処理と同じ登録・初期化・つなぎ方（app.BuildRoutes）で、CMSと管理者認証を組み込む
	reg := features.NewRegistry()
	reg.Register(cms.NewFeature(logger))
	reg.Register(auth.NewFeature(logger))
	for _, f := range reg.All() {
		if err := f.Init(s.DB()); err != nil {
			t.Fatal(err)
		}
	}
	posts := cms.NewPostStore(s.DB())
	id, err := posts.Create(context.Background(), "お知らせ", "本文")
	if err != nil {
		t.Fatal(err)
	}
	if err := posts.Publish(context.Background(), id); err != nil {
		t.Fatal(err)
	}

	routes, err := app.BuildRoutes(s, reg, false, logger)
	if err != nil {
		t.Fatal(err)
	}

	send := func(method, target, contentType, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, target, strings.NewReader(body))
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		req.Header.Set("User-Agent", canary)
		req.Header.Set("Referer", "https://"+canary+".example/")
		if strings.HasPrefix(target, "/admin") {
			// 管理画面は同一オリジンの送信だけを受け付けるため、Originは null（Referrer-Policy: no-referrer の場合）にする
			req.Header.Set("Origin", "null")
		} else {
			req.Header.Set("Origin", "https://"+canary+".example")
		}
		req.Header.Set("X-Request-ID", canary)
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		req.AddCookie(&http.Cookie{Name: "session", Value: canary})
		req.AddCookie(&http.Cookie{Name: "admin_session", Value: canary})
		rec := httptest.NewRecorder()
		routes.ServeHTTP(rec, req)
		return rec
	}

	// 公開ページ・APIはCookieを発行・削除しない
	for _, target := range []string{
		"/news?page=1&q=" + canary,
		"/news/1?q=" + canary,
		"/news/" + canary,
		"/api/news?limit=" + canary,
		"/api/news/1?q=" + canary,
		"/api/news/" + canary,
	} {
		rec := send(http.MethodGet, target, "", "")
		if len(rec.Header().Values("Set-Cookie")) != 0 {
			t.Errorf("GET %s must not touch cookies", target)
		}
		if strings.Contains(rec.Body.String(), canary) {
			t.Errorf("GET %s must not reflect the input", target)
		}
	}

	// 管理画面・WebAuthnのAPIに送った値もログに出さない
	// base64urlの項目は、形式の検査で止まらずに検証処理まで届くよう、正しい形式で送る
	b64 := base64.RawURLEncoding.EncodeToString([]byte(canary))
	jsonBody := `{"token":"` + canary + `","label":"` + canary + `","rawId":"` + b64 + `","clientDataJSON":"` + b64 +
		`","authenticatorData":"` + b64 + `","signature":"` + b64 + `","userHandle":"` + b64 + `"}`
	for _, target := range []string{
		"/admin/webauthn/register/begin",
		"/admin/webauthn/register/finish",
		"/admin/webauthn/login/begin",
		"/admin/webauthn/login/finish",
	} {
		send(http.MethodPost, target, "application/json", jsonBody)
	}
	form := url.Values{"csrf_token": {canary}, "title": {canary}, "body": {canary}, "label": {canary}}.Encode()
	for _, target := range []string{"/admin/posts", "/admin/posts/1", "/admin/posts/1/delete", "/admin/logout", "/admin/passkeys/" + canary + "/delete"} {
		send(http.MethodPost, target, "application/x-www-form-urlencoded", form)
	}
	send(http.MethodGet, "/admin/posts?notice="+canary, "", "")
	send(http.MethodGet, "/admin/setup?token="+canary, "", "")
	for _, target := range []string{"/admin/accounts", "/admin/accounts/" + canary + "/role", "/admin/accounts/1/setup-url"} {
		send(http.MethodPost, target, "application/x-www-form-urlencoded", url.Values{"csrf_token": {canary}, "name": {canary}, "role": {canary}}.Encode())
	}

	// ログイン中の管理者がアカウントを作っても、呼び名と登録URLのトークンはログに出ない
	admin := authtest.NewSession(t, s.DB())
	createForm := url.Values{"csrf_token": {authtest.CSRFToken(t, routes, "/admin/accounts", admin)}, "name": {canary}, "role": {"member"}}.Encode()
	req := httptest.NewRequest(http.MethodPost, "/admin/accounts", strings.NewReader(createForm))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.AddCookie(admin)
	rec := httptest.NewRecorder()
	routes.ServeHTTP(rec, req)
	token := regexp.MustCompile(`#token=([A-Za-z0-9_-]+)`).FindStringSubmatch(rec.Body.String())
	if rec.Code != http.StatusOK || token == nil || !strings.Contains(logBuf.String(), "auth account created") {
		t.Fatalf("the account creation must succeed (otherwise the canary check is meaningless): %d", rec.Code)
	}
	if strings.Contains(logBuf.String(), token[1]) {
		t.Fatal("the registration token leaked into logs")
	}

	if logBuf.Len() == 0 {
		t.Fatal("no logs were captured")
	}
	if !strings.Contains(logBuf.String(), "auth login rejected") {
		t.Fatal("the login request must reach the verification (otherwise the canary check is meaningless)")
	}
	if strings.Contains(logBuf.String(), canary) || strings.Contains(logBuf.String(), b64) {
		t.Fatalf("request input leaked into logs:\n%s", logBuf.String())
	}
}
