package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"minimal-web-kit/internal/store"
)

// CoreのCSRF検査の入力と結果を固定します。独立経路の guard と違い、Coreは cross-site だけを見出しで拒否し、
// 同じサイトの別オリジン（same-site）とヘッダーの無いブラウザは、トークンの検証で守ります。
func TestCSRFProtect_Table(t *testing.T) {
	sm := NewSessionManager(store.NewMemoryStore(), false)
	const session = "session-token"
	valid := sm.GenerateCSRFToken(session)
	other := sm.GenerateCSRFToken("another-session")
	cookie := &http.Cookie{Name: sessionCookieName, Value: session}

	cases := []struct {
		name     string
		method   string
		target   string
		cookie   *http.Cookie
		site     string
		body     url.Values
		rawBody  string
		header   string
		want     int
		wantNext bool
	}{
		{name: "正しいトークン（本文）", cookie: cookie, site: "same-origin", body: url.Values{"csrf_token": {valid}}, want: 200, wantNext: true},
		{name: "正しいトークン（ヘッダー）", cookie: cookie, site: "same-origin", body: url.Values{}, header: valid, want: 200, wantNext: true},
		{name: "same-site はトークンで判定する", cookie: cookie, site: "same-site", body: url.Values{"csrf_token": {valid}}, want: 200, wantNext: true},
		{name: "Sec-Fetch-Site が無くてもトークンで判定する", cookie: cookie, body: url.Values{"csrf_token": {valid}}, want: 200, wantNext: true},
		{name: "cross-site は正しいトークンでも拒否", cookie: cookie, site: "cross-site", body: url.Values{"csrf_token": {valid}}, want: 403},
		{name: "Cookieなし", site: "same-origin", body: url.Values{"csrf_token": {valid}}, want: 403},
		{name: "空のCookie", cookie: &http.Cookie{Name: sessionCookieName, Value: ""}, site: "same-origin", body: url.Values{"csrf_token": {valid}}, want: 403},
		{name: "トークンなし", cookie: cookie, site: "same-origin", body: url.Values{}, want: 403},
		{name: "違うトークン", cookie: cookie, site: "same-origin", body: url.Values{"csrf_token": {"wrong"}}, want: 403},
		{name: "別のセッションのトークン", cookie: cookie, site: "same-origin", body: url.Values{"csrf_token": {other}}, want: 403},
		{name: "クエリだけのトークン", cookie: cookie, site: "same-origin", target: "/?csrf_token=" + valid, body: url.Values{}, want: 403},
		{name: "本文が違えば、ヘッダーが正しくても拒否", cookie: cookie, site: "same-origin", body: url.Values{"csrf_token": {"wrong"}}, header: valid, want: 403},
		{name: "壊れた本文", cookie: cookie, site: "same-origin", rawBody: "%zz=1", want: 400},
		{name: "DELETEも検査する", method: http.MethodDelete, cookie: cookie, site: "same-origin", body: url.Values{}, want: 403},
		{name: "GETは検査しない", method: http.MethodGet, site: "cross-site", want: 200, wantNext: true},
		{name: "HEADは検査しない", method: http.MethodHead, site: "cross-site", want: 200, wantNext: true},
		{name: "OPTIONSは検査しない", method: http.MethodOptions, site: "cross-site", want: 200, wantNext: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			method, target := c.method, c.target
			if method == "" {
				method = http.MethodPost
			}
			if target == "" {
				target = "/"
			}
			body := c.rawBody
			if c.body != nil {
				body = c.body.Encode()
			}
			r := httptest.NewRequest(method, target, strings.NewReader(body))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			if c.site != "" {
				r.Header.Set("Sec-Fetch-Site", c.site)
			}
			if c.header != "" {
				r.Header.Set("X-CSRF-Token", c.header)
			}
			if c.cookie != nil {
				r.AddCookie(c.cookie)
			}
			reached := false
			next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true })
			w := httptest.NewRecorder()
			CSRFProtect(sm)(next).ServeHTTP(w, r)
			if w.Code != c.want || reached != c.wantNext {
				t.Fatalf("status %d, reached %v; want %d, %v", w.Code, reached, c.want, c.wantNext)
			}
		})
	}
}
