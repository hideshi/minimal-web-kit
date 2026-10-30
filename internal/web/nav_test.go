package web

import (
	"strings"
	"testing"
)

func TestNavCurrent(t *testing.T) {
	for _, c := range []struct {
		name    string
		current any
		href    string
		want    string
	}{
		{"完全に一致", "/news", "/news", "page"},
		{"配下の画面", "/news/3", "/news", "true"},
		{"深い配下の画面", "/catalog/3/x", "/catalog", "true"},
		{"似た名前は当たらない", "/catalog-x", "/catalog", ""},
		{"親の画面で子のリンクは当たらない", "/news", "/news/3", ""},
		{"ホームは完全に一致するときだけ", "/", "/", "page"},
		{"ホームに配下は無い", "/news", "/", ""},
		{"ページ内リンク", "/", "/#faq", ""},
		{"ページ内リンクは配下でも当たらない", "/news/3", "/news#x", ""},
		{"クエリ付きのリンク", "/catalog", "/catalog?tag=1", ""},
		{"外部のURL", "/news", "https://example.com/news", ""},
		{"プロトコル相対のURL", "//example.com/news", "//example.com/news", ""},
		{"相対のURL", "news", "news", ""},
		{"末尾のスラッシュは別の経路", "/about", "/about/", ""},
		{"値が無い", nil, "/news", ""},
		{"値が空", "", "/news", ""},
		{"文字列でない値", 3, "/news", ""},
	} {
		if got := NavCurrent(c.current, c.href); got != c.want {
			t.Errorf("%s: NavCurrent(%v, %q) = %q, want %q", c.name, c.current, c.href, got, c.want)
		}
	}
}

// データに CanonicalPath が無くても描画でき、属性も出ないこと。値があれば属性が1つだけ出ること。
func TestNavCurrent_RendersAttribute(t *testing.T) {
	const link = `<a href="/news"{{with navCurrent $.CanonicalPath "/news"}} aria-current="{{.}}"{{end}}>お知らせ</a>`
	if got := renderComponent(t, link, map[string]any{}); got != `<a href="/news">お知らせ</a>` {
		t.Fatalf("値が無いとき: %s", got)
	}
	got := renderComponent(t, link, map[string]any{"CanonicalPath": "/news/3"})
	if want := `<a href="/news" aria-current="true">お知らせ</a>`; !strings.Contains(got, want) {
		t.Fatalf("配下の画面: %s", got)
	}
}
