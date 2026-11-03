package richtext

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// lineDump は1行の構造を比べやすい文字列にします。文字はそのまま、リンクは «L:URL:文字»。
func lineDump(xs []Inline) string {
	var b strings.Builder
	for _, x := range xs {
		switch x.Kind {
		case "link":
			b.WriteString("«L:" + x.Href + ":" + x.Text + "»")
		case "text":
			b.WriteString(x.Text)
		default:
			b.WriteString("«" + x.Kind + "?»") // 強調等は出てはならない
		}
	}
	return b.String()
}

// TestLinkLine_SpecExamples は docs/richtext.md 7節の例を、そのまま確かめる。
// 不変条件: NEWS-INV-5, CATALOG-INV-2
func TestLinkLine_SpecExamples(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"詳しくは[こちら](/about)をご覧ください", "詳しくは«L:/about:こちら»をご覧ください"},
		{"[外部](https://example.com/a?b=1)", "«L:https://example.com/a?b=1:外部»"},
		{"[a](https://ja.wikipedia.org/wiki/Go_(言語))", "«L:https://ja.wikipedia.org/wiki/Go_(言語:a»)"},
		{"[古い](http://example.com)", "[古い](http://example.com)"},
		{"[危険](javascript:alert(1))", "[危険](javascript:alert(1))"},
		{"[a](//example.com)", "[a](//example.com)"},
		{"[](/about)", "[](/about)"},
		{"[ ](/about)", "[ ](/about)"},
		{"[空白](/a b)", "[空白](/a b)"},
		{`\[文字\](/about)`, "[文字](/about)"},
		{`\\`, `\`}, {`\(`, "("}, {`\)`, ")"},
		{`C:\Users`, `C:\Users`}, {`\a`, `\a`},
		{"[**強調**](/about)", "«L:/about:**強調**»"},
		{"[a|b](/x)", "«L:/x:a|b»"},
		{`<a href="/x">x</a>`, `<a href="/x">x</a>`},
		// 固定ページとの違い（7節の表）
		{"**重要**", "**重要**"},
		{`\*\#\-\.\:\|`, `\*\#\-\.\:\|`},
		{"- 項目", "- 項目"}, {"## 見出し", "## 見出し"}, {"1. 項目", "1. 項目"},
		{"![a](/media/0123456789abcdef0123456789abcdef.jpg)", "!«L:/media/0123456789abcdef0123456789abcdef.jpg:a»"},
		{"|a|b|", "|a|b|"},
	} {
		if got := lineDump(LinkLine(c.in)); got != c.want {
			t.Errorf("LinkLine(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestLinkParagraphs(t *testing.T) {
	got := LinkParagraphs([][]string{{"a", "[b](/x)"}, {"c"}})
	if len(got) != 2 || len(got[0]) != 2 || len(got[1]) != 1 {
		t.Fatalf("shape: %#v", got)
	}
	if lineDump(got[0][1]) != "«L:/x:b»" || lineDump(got[1][0]) != "c" {
		t.Fatalf("lines: %#v", got)
	}
}

func TestLinkPlain(t *testing.T) {
	for in, want := range map[string]string{
		"詳しくは[こちら](/about)\n次の行":   "詳しくはこちら\n次の行",
		"[古い](http://example.com)": "[古い](http://example.com)",
		`\[a\]`:                    "[a]",
		"**a** \\*":                "**a** \\*",
	} {
		if got := LinkPlain(in); got != want {
			t.Errorf("LinkPlain(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestLinkLine_LinearTime は、TestParse_LinearTime と同じ意地悪な入力で、読む時間が長さに比例することを確かめる。
func TestLinkLine_LinearTime(t *testing.T) {
	for name, in := range map[string]string{
		"brackets":     strings.Repeat("[", 50000) + "]",
		"open links":   strings.Repeat("[a](x", 10000),
		"space urls":   strings.Repeat("[a](x ", 10000) + ")",
		"long url":     strings.Repeat("[a](/"+strings.Repeat("x", 3000), 15) + ")",
		"shared paren": strings.Repeat(strings.Repeat("[](", 680)+")", 24),
		"https shared": strings.Repeat("[a](https://x", 4000) + ")",
		"escapes":      strings.Repeat(`\[`, 25000),
	} {
		start := time.Now()
		LinkLine(in)
		if d := time.Since(start); d > 500*time.Millisecond {
			t.Errorf("%s took %v", name, d)
		}
	}
}

// FuzzLinkLine は、どんな行でも、出るのは文字と許可したリンクだけで、入力の字を落とさないことを確かめる。
func FuzzLinkLine(f *testing.F) {
	for _, seed := range []string{
		"詳しくは[こちら](/about)と[外部](https://example.com)", strings.Repeat("[", 3000) + "]", strings.Repeat("[](", 500) + ")",
		`\[a\](/x) \\ \* **b**`,
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, line string) {
		if !utf8.ValidString(line) || len(line) > 20000 || strings.Contains(line, "\n") {
			return
		}
		var b strings.Builder
		for _, x := range LinkLine(line) {
			switch x.Kind {
			case "text":
			case "link":
				if strings.TrimSpace(x.Text) == "" || x.Href == "" || AllowedURL(x.Href) != x.Href {
					t.Fatalf("bad link: %#v", x)
				}
			default:
				t.Fatalf("unexpected kind %q", x.Kind)
			}
			b.WriteString(x.Text + " " + x.Href + " ")
		}
		in, out := letters(line), letters(b.String())
		k := 0
		for _, r := range out {
			if k < len(in) && in[k] == r {
				k++
			}
		}
		if k != len(in) {
			t.Fatalf("letters dropped: %q", string(in[k:]))
		}
	})
}
