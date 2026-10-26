package catalogtags

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

func handler(t *testing.T) http.Handler {
	t.Helper()
	h, err := NewHandler([]byte("body {}"), "http://127.0.0.1:9999", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return h
}
func send(h http.Handler, method, target string, values url.Values, same bool) *httptest.ResponseRecorder {
	var body io.Reader
	if values != nil {
		body = strings.NewReader(values.Encode())
	}
	r := httptest.NewRequest(method, target, body)
	r.AddCookie(&http.Cookie{Name: "session", Value: "unknown"})
	r.AddCookie(&http.Cookie{Name: "admin_session", Value: "unknown"})
	if values != nil {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if same {
		r.Header.Set("Origin", "null")
		r.Header.Set("Sec-Fetch-Site", "same-origin")
	} else {
		r.Header.Set("Origin", "https://other.example")
		r.Header.Set("Sec-Fetch-Site", "cross-site")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func TestVariantsUseIdenticalDataAndScopedScripts(t *testing.T) {
	h := handler(t)
	for _, mode := range []string{"html", "alpine", "combo"} {
		for _, count := range []string{"10", "50", "200"} {
			w := send(h, "GET", "/compare?mode="+mode+"&count="+count, nil, false)
			if w.Code != 200 || len(w.Result().Cookies()) != 0 || !strings.Contains(w.Header().Get("Cache-Control"), "no-store") {
				t.Fatal("page/cookie/cache", w.Code)
			}
			expected := 0
			if mode != "html" {
				expected = 2
			}
			if strings.Count(w.Body.String(), "<script") != expected || strings.Contains(w.Header().Get("Content-Security-Policy"), "script-src 'self'") != (mode != "html") {
				t.Fatal("script/CSP scope")
			}
			if !strings.Contains(w.Body.String(), "設計レビュー") || !strings.Contains(w.Body.String(), `id="tag-`+count+`"`) {
				t.Fatal("sample tags differ")
			}
			// コンボボックス版も、JSが動かない場合に送信できる全件のチェックボックスを残す。
			if (mode == "combo") != strings.Contains(w.Body.String(), `role="combobox"`) || strings.Count(w.Body.String(), `type="checkbox" name="tag"`) != countOf(count) {
				t.Fatal("combobox markup or fallback checkboxes", mode, count)
			}
			if strings.Contains(w.Header().Get("Content-Security-Policy"), "unsafe-") || strings.Contains(w.Body.String(), "x-show") || strings.Contains(w.Body.String(), "x-cloak") {
				t.Fatal("unsafe styles/policy")
			}
		}
	}
	for _, path := range []string{"/compare?mode=bad", "/compare?count=11", "/compare?count=200&count=10", "/compare?mode=%zz"} {
		if w := send(h, "GET", path, nil, false); w.Code != 400 {
			t.Fatal("invalid page parameters accepted", path, w.Code)
		}
	}
	for _, path := range []string{"/compare/static/tags.js", "/compare/static/alpine-csp.min.js", "/static/css/style.css"} {
		if w := send(h, "GET", path, nil, false); w.Code != 200 || len(w.Result().Cookies()) != 0 {
			t.Fatal("asset response", path, w.Code)
		}
	}
	for _, path := range []string{"/admin", "/news", "/catalog", "/compare/static/LICENSE-Alpine"} {
		if w := send(h, "GET", path, nil, false); w.Code != 404 || len(w.Result().Cookies()) != 0 {
			t.Fatal("unexpected routes", path, w.Code)
		}
	}
}
func TestConfirmationIsValidatedAndNotPersisted(t *testing.T) {
	h := handler(t)
	for _, mode := range []string{"html", "alpine", "combo"} {
		values := url.Values{"mode": {mode}, "count": {"50"}, "tag": {"2", "4", "7"}, "new_tags": {"<script>alert(1)</script>"}}
		w := send(h, "POST", "/compare/result", values, true)
		if w.Code != 200 || len(w.Result().Cookies()) != 0 || !strings.Contains(w.Body.String(), "&lt;script&gt;") || !strings.Contains(w.Body.String(), "確認しました") {
			t.Fatal("confirmation/escape", w.Code)
		}
		for _, id := range []string{"2", "4", "7"} {
			if !strings.Contains(w.Body.String(), `value="`+id+`" checked`) {
				t.Fatal("selection lost", id)
			}
		}
		if w := send(h, "POST", "/compare/result", values, false); w.Code != 403 {
			t.Fatal("cross-origin accepted")
		}
		fresh := send(h, "GET", "/compare?mode="+mode+"&count=50", nil, false)
		if strings.Contains(fresh.Body.String(), " checked") || strings.Contains(fresh.Body.String(), "alert(1)") {
			t.Fatal("input persisted")
		}
		values.Set("tag", "51")
		if w := send(h, "POST", "/compare/result", values, true); w.Code != 400 {
			t.Fatal("unknown tag accepted")
		}
		values.Del("tag")
		for i := range 21 {
			values.Add("tag", fmt.Sprint(i+1))
		}
		if w := send(h, "POST", "/compare/result", values, true); w.Code != 400 {
			t.Fatal("selection limit missing")
		}
		values.Set("tag", "1")
		values.Set("new_tags", strings.Repeat("a", 51))
		if w := send(h, "POST", "/compare/result", values, true); w.Code != 400 {
			t.Fatal("name limit missing")
		}
	}
}
func TestFixedAlpineBuildAndNoStorageCode(t *testing.T) {
	body, err := files.ReadFile("static/alpine-csp.min.js")
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(body)
	if hex.EncodeToString(hash[:]) != "48c170c92032e527cbae71b8ce75cf1f96ca06fa869a6120ef0fe438d8e92c36" {
		t.Fatal("Alpine build differs from pinned upstream")
	}
	body, err = files.ReadFile("static/tags.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"localStorage", "sessionStorage", "document.cookie", "fetch(", "XMLHttpRequest", ".innerHTML", ".style"} {
		if strings.Contains(string(body), forbidden) {
			t.Fatal("comparison script added persistence/network/HTML/style", forbidden)
		}
	}
}

func countOf(raw string) int {
	n, _ := strconv.Atoi(raw)
	return n
}
