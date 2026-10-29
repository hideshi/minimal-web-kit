package web

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"minimal-web-kit/internal/store"
)

// 版は、そのファイルの中身の SHA-256 の先頭12桁で、存在しないファイルはエラーになること。
func TestAsset_URLUsesContentHash(t *testing.T) {
	b, err := fs.ReadFile(contentFS, "static/css/style.css")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	got, err := AssetURL("css/style.css")
	if err != nil || got != "/static/css/style.css?v="+hex.EncodeToString(sum[:])[:12] {
		t.Fatalf("AssetURL = %q, %v", got, err)
	}
	for _, bad := range []string{"css/missing.css", "/css/style.css", "../views/layout.html", ""} {
		if _, err := AssetURL(bad); err == nil {
			t.Errorf("AssetURL(%q) must fail", bad)
		}
	}
	icon, _ := AssetURL("img/favicon.svg")
	if strings.TrimPrefix(icon, "/static/img/favicon.svg?v=") == strings.TrimPrefix(got, "/static/css/style.css?v=") {
		t.Fatal("each file must have its own version")
	}
}

// 版が一致すれば1年・immutable、版なし・古い版は短いキャッシュで返すこと。
func TestAsset_StaticCacheByVersion(t *testing.T) {
	s := store.NewMemoryStore()
	h, err := NewHandler(s, NewSessionManager(s, false), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	routes := h.Routes(nil, nil)
	current, _ := AssetURL("css/style.css")
	for path, want := range map[string]string{
		current:                                "public, max-age=31536000, immutable",
		"/static/css/style.css":                "public, max-age=600",
		"/static/css/style.css?v=000000000000": "public, max-age=600",
		"/static/img/favicon.svg?v=" + current[len("/static/css/style.css?v="):]: "public, max-age=600",
	} {
		rec := httptest.NewRecorder()
		routes.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != want {
			t.Errorf("%s: %d Cache-Control = %q, want %q", path, rec.Code, rec.Header().Get("Cache-Control"), want)
		}
	}
}

// static-head は、版付きの CSS 1本とアイコン3つを出すこと。
func TestComponents_StaticHead(t *testing.T) {
	got := renderComponent(t, `{{template "static-head"}}`, nil)
	for name, tag := range map[string]string{
		"img/favicon.svg":          `<link rel="icon" href="%s" type="image/svg+xml">`,
		"img/favicon-32.png":       `<link rel="icon" href="%s" sizes="32x32" type="image/png">`,
		"img/apple-touch-icon.png": `<link rel="apple-touch-icon" href="%s">`,
		"css/style.css":            `<link rel="stylesheet" href="%s">`,
	} {
		u, err := AssetURL(name)
		if err != nil {
			t.Fatal(err)
		}
		if want := strings.Replace(tag, "%s", u, 1); strings.Count(got, want) != 1 {
			t.Errorf("static-head must contain %s once:\n%s", want, got)
		}
	}
	if strings.Count(got, "<link") != 4 {
		t.Fatalf("static-head must contain exactly 4 links:\n%s", got)
	}
}

// asset の引数の誤りと static-head の定義し直しは、実行しない分岐の中でも解析の時点でエラーになること。
// ParsePage で後から足す本文の定義も対象にすること。
func TestAsset_ParseRejectsBadReferences(t *testing.T) {
	layout := `{{define "layout"}}<head>{{template "static-head"}}{{block "script" .}}{{end}}</head>{{template "content" .}}{{end}}`
	for name, body := range map[string]string{
		"missing file in untaken branch": `{{define "content"}}{{if false}}<img src="{{asset "img/missing.png"}}">{{end}}{{end}}`,
		"missing file in else":           `{{define "content"}}{{with .X}}{{else}}{{range .Y}}{{asset "nope.css"}}{{end}}{{end}}{{end}}`,
		"variable argument":              `{{define "content"}}{{asset .Name}}{{end}}`,
		"piped argument":                 `{{define "content"}}{{"css/style.css" | asset}}{{end}}`,
		"no argument":                    `{{define "content"}}{{asset}}{{end}}`,
		"two arguments":                  `{{define "content"}}{{asset "css/style.css" "x"}}{{end}}`,
		"nested in another call":         `{{define "content"}}{{printf "%s" (asset "x.css")}}{{end}}`,
		"passed as a value":              `{{define "content"}}{{printf "%v" asset}}{{end}}`,
		"inside template argument":       `{{define "content"}}{{template "notice" (asset "x.css")}}{{end}}`,
		"redefined static-head":          `{{define "content"}}{{end}}{{define "static-head"}}<link rel="stylesheet" href="/other.css">{{end}}`,
		"bad reference in script block":  `{{define "content"}}{{end}}{{define "script"}}{{if .X}}<script src="{{asset "js/missing.js"}}"></script>{{end}}{{end}}`,
	} {
		fsys := fstest.MapFS{"layout.html": {Data: []byte(layout)}, "content.html": {Data: []byte(body)}}
		// レイアウトと本文を一緒に解析する経路（ParseWithComponents）
		if _, err := ParseWithComponents(fsys, "layout.html", "content.html"); err == nil {
			t.Errorf("%s: ParseWithComponents must fail", name)
		}
		// レイアウトの後で本文を足す経路（ParsePage）
		if _, err := ParsePage(Layout{FS: fsys, File: "layout.html"}, fsys, "content.html"); err == nil {
			t.Errorf("%s: ParsePage must fail", name)
		}
	}

	ok := fstest.MapFS{"layout.html": {Data: []byte(layout)}, "content.html": {Data: []byte(
		`{{define "content"}}{{if false}}<img src="{{asset "img/ogp.png"}}">{{end}}{{end}}{{define "script"}}<script src="{{asset "js/combobox.js"}}"></script>{{end}}`)}}
	if _, err := ParsePage(Layout{FS: ok, File: "layout.html"}, ok, "content.html"); err != nil {
		t.Fatalf("valid references must parse: %v", err)
	}
}

// static-head を使わず /static/ を直接書いた自作のレイアウトも、今までどおり解析・描画できること（その場合は版なし）。
func TestAsset_DirectStaticLinksStillWork(t *testing.T) {
	fsys := fstest.MapFS{"layout.html": {Data: []byte(`{{define "layout"}}<link rel="stylesheet" href="/static/css/style.css">{{end}}`)}}
	tmpl, err := ParseWithComponents(fsys, "layout.html")
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	if err := tmpl.ExecuteTemplate(&b, "layout", nil); err != nil || !strings.Contains(b.String(), `href="/static/css/style.css"`) {
		t.Fatalf("direct link: %q %v", b.String(), err)
	}
}
