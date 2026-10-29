package web

import (
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

	"minimal-web-kit/internal/web/richtext"
)

func renderRichtext(t *testing.T, body string) (string, bool) {
	t.Helper()
	doc, err := richtext.Parse(body)
	if err != nil {
		return "", false
	}
	return renderComponent(t, `{{template "richtext" .}}`, doc), true
}

func TestComponents_Richtext(t *testing.T) {
	got, _ := renderRichtext(t, "## 会社<b>\n\n表: 情報\n| 項目 | 内容 |\n|---|---|\n| **社名** | [例](/about) |\n\n- a\n1. b\n\n一行目\n二行目 <script>")
	for _, want := range []string{
		`<h2>会社&lt;b&gt;</h2>`,
		`<div class="table-container"><table><caption>情報</caption><thead><tr><th scope="col">項目</th><th scope="col">内容</th></tr></thead><tbody><tr><td><strong>社名</strong></td><td><a href="/about">例</a></td></tr></tbody></table></div>`,
		`<ul class="richtext-list"><li>a</li></ul><ol class="richtext-list"><li>b</li></ol>`,
		`<p>一行目<br>二行目 &lt;script&gt;</p>`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in %s", want, got)
		}
	}
}

var (
	knownTag = regexp.MustCompile(`</?(div|h2|h3|p|br|ul|ol|li|table|caption|thead|tbody|tr|th|td|strong|a)( (class|scope|href)="[^"<>]*")*>`)
	hrefAttr = regexp.MustCompile(`href="([^"]*)"`)
)

// FuzzRichtext は、どんな本文でも、止まらず、出力が入力に比例し、リンクが許可したURLだけで、既知のタグ以外の < が出ないことを確かめる。
// 固定ページの「本文をHTMLとして出さない」不変条件の土台（番号は固定ページの機能のテストで付ける）。
func FuzzRichtext(f *testing.F) {
	for _, seed := range []string{
		"## 見出し\n本文 **強調** [リンク](/about)\n\n- a\n- b\n\n" + "| a | b |\n|---|---|\n| [x](https://example.com) | y |",
		"[a](javascript:alert(1)) [b](//evil) [c](/\\evil) [d](HTTPS://x) <script>alert(1)</script>",
		strings.Repeat("[", 5000) + "]", strings.Repeat("[a](x", 2000), strings.Repeat("**", 3000), strings.Repeat("|", 5000),
		"\\\\| \\[a\\](/x) **a\\**b** 表: x\n| a |\n|---|",
		"\"><img src=x onerror=alert(1)> [x](/\"onmouseover=alert(1))",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, body string) {
		if !utf8.ValidString(body) || len(body) > 20000 {
			return
		}
		out, ok := renderRichtext(t, body)
		if !ok {
			return
		}
		if len(out) > 40*len(body)+200 {
			t.Fatalf("output too large: %d for %d", len(out), len(body))
		}
		for _, m := range hrefAttr.FindAllStringSubmatch(out, -1) {
			if !strings.HasPrefix(m[1], "https://") && !strings.HasPrefix(m[1], "/") {
				t.Fatalf("unexpected href %q", m[1])
			}
			if strings.HasPrefix(m[1], "//") {
				t.Fatalf("scheme-relative href %q", m[1])
			}
		}
		if rest := knownTag.ReplaceAllString(out, ""); strings.Contains(rest, "<") || strings.Contains(rest, ">") {
			t.Fatalf("unexpected markup in %q", rest)
		}
	})
}
