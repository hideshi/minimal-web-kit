package guard

import (
	"net/http/httptest"
	"testing"
)

func TestSameOrigin(t *testing.T) {
	const origin = "https://example.com"
	for _, tc := range []struct {
		name, site, origin string
		want               bool
	}{
		{"同一オリジン・Originなし", "same-origin", "", true},
		{"同一オリジン・Origin null（no-referrer）", "same-origin", "null", true},
		{"同一オリジン・Origin一致", "same-origin", origin, true},
		{"Origin不一致", "same-origin", "https://evil.example", false},
		{"別サイト", "cross-site", origin, false},
		{"同一サイトの別オリジン", "same-site", origin, false},
		{"直接入力", "none", "", false},
		{"Sec-Fetch-Siteなし", "", origin, false},
	} {
		r := httptest.NewRequest("POST", "/", nil)
		if tc.site != "" {
			r.Header.Set("Sec-Fetch-Site", tc.site)
		}
		if tc.origin != "" {
			r.Header.Set("Origin", tc.origin)
		}
		if got := SameOrigin(r, origin); got != tc.want {
			t.Errorf("%s: got %v", tc.name, got)
		}
	}
}

func TestCSRF(t *testing.T) {
	c := NewCSRF()
	token := c.Token("session-a")
	if !c.Valid("session-a", token) {
		t.Fatal("a valid token must be accepted")
	}
	last := byte('0')
	if token[len(token)-1] == '0' {
		last = '1'
	}
	tampered := token[:len(token)-1] + string(last)
	for name, ok := range map[string]bool{
		"別のセッション":     c.Valid("session-b", token),
		"空のセッション":     c.Valid("", token),
		"空のトークン":      c.Valid("session-a", ""),
		"改ざん":         c.Valid("session-a", tampered),
		"別の鍵で作ったトークン": NewCSRF().Valid("session-a", token),
	} {
		if ok {
			t.Errorf("%s must be rejected", name)
		}
	}
}
