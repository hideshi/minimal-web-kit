package notice

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMessages(t *testing.T) {
	m := Messages{"saved": "保存しました。"}
	if got := m.From(httptest.NewRequest("GET", "/x?notice=saved", nil)); got != "保存しました。" {
		t.Fatalf("got %q", got)
	}
	// 表に無い語や、文言そのものを送っても表示しない
	for _, q := range []string{"?notice=unknown", "?notice=%3Cscript%3E", "?notice=保存しました。", ""} {
		if got := m.From(httptest.NewRequest("GET", "/x"+q, nil)); got != "" {
			t.Errorf("%q: got %q", q, got)
		}
	}

	rec := httptest.NewRecorder()
	Redirect(rec, httptest.NewRequest("POST", "/x", nil), "/admin/news", "saved")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/news?notice=saved" {
		t.Fatalf("redirect: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if got := URL("/a", "a b&c"); got != "/a?notice=a+b%26c" {
		t.Fatalf("URL must escape the key: %s", got)
	}
}
