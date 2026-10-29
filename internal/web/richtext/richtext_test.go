package richtext

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"
)

// dump は構造を、比べやすい1つの文字列にします。
// 文字はそのまま、強調は «S:文字»、リンクは «L:URL:文字»。段落の改行は ⏎、項目・行の区切りは ‖、セルの区切りは ¦。
func dump(doc Doc) string {
	in := func(xs []Inline) string {
		var b strings.Builder
		for _, x := range xs {
			switch x.Kind {
			case "strong":
				b.WriteString("«S:" + x.Text + "»")
			case "link":
				b.WriteString("«L:" + x.Href + ":" + x.Text + "»")
			default:
				b.WriteString(x.Text)
			}
		}
		return b.String()
	}
	join := func(rows [][]Inline, sep string) string {
		var parts []string
		for _, r := range rows {
			parts = append(parts, in(r))
		}
		return strings.Join(parts, sep)
	}
	var out []string
	for _, b := range doc.Blocks {
		switch b.Kind {
		case "h2", "h3":
			out = append(out, b.Kind+"("+in(b.Inlines)+")")
		case "p":
			out = append(out, "p("+join(b.Lines, "⏎")+")")
		case "ul", "ol":
			out = append(out, b.Kind+"("+join(b.Items, "‖")+")")
		case "table":
			var rows []string
			for _, r := range b.Rows {
				rows = append(rows, join(r, "¦"))
			}
			out = append(out, "table["+b.Caption+"]("+join(b.Head, "¦")+")("+strings.Join(rows, "‖")+")")
		}
	}
	return strings.Join(out, "\n")
}

const tbl = "| 項目 | 内容 |\n|---|---|\n| 社名 | 例 |"

// TestParse_SpecExamples は docs/richtext.md 6節の例を、そのまま確かめる。
// 固定ページの「本文をHTMLとして出さない」不変条件の土台（番号は固定ページの機能のテストで付ける）。
func TestParse_SpecExamples(t *testing.T) {
	cases := []struct{ in, want string }{
		// 見出し・段落
		{"## 会社概要", "h2(会社概要)"},
		{"### 小見出し", "h3(小見出し)"},
		{"##会社概要", "p(##会社概要)"},
		{"# 大見出し", "p(# 大見出し)"},
		{"#### x", "p(#### x)"},
		{"##", "p(##)"},
		{"##　x", "p(##　x)"},
		{`\## 見出しではない`, "p(## 見出しではない)"},
		{"一行目\n二行目", "p(一行目⏎二行目)"},
		{"a \nb", "p(a⏎b)"},
		{"a\tb", "p(a\tb)"},
		{"本文\n## 見出し", "p(本文)\nh2(見出し)"},
		{"## [会社](/about)", "h2(«L:/about:会社»)"},
		// リスト
		{"- りんご\n- みかん", "ul(りんご‖みかん)"},
		{"- りんご\n続き", "ul(りんご)\np(続き)"},
		{"- りんご\n1. 一", "ul(りんご)\nol(一)"},
		{"  - 字下げ", "p(  - 字下げ)"},
		{"\t- x", "p(\t- x)"},
		{"　- x", "p(　- x)"},
		{"-\tx", "p(-\tx)"},
		{"- ", "p(-)"},
		{"3. 三\n7. 七", "ol(三‖七)"},
		{"0. x", "ol(x)"},
		{"123456789. x", "ol(x)"},
		{"1234567890. x", "p(1234567890. x)"},
		{"０. x", "p(０. x)"},
		// 表
		{tbl, "table[表1](項目¦内容)(社名¦例)"},
		{"| a\\|b | c |\n|---|---|", "table[表1](a|b¦c)()"},
		{"| a \\\\| b |\n|---|---|", "table[表1](a \\¦b)()"},
		{"||\n|---|", "table[表1]()()"},
		{"| a | b |\n| c | d |", "p(| a | b |⏎| c | d |)"},
		{"| a | b |\n|---|", "p(| a | b |⏎|---|)"},
		{"| a |\n|-|", "p(| a |⏎|-|)"},
		{"| a | b |\n|---|---|\n| x |", "table[表1](a¦b)(x¦)"},
		{"表: 会社の情報\n" + tbl, "table[会社の情報](項目¦内容)(社名¦例)"},
		{"表: a\\|b\n" + tbl, "table[a|b](項目¦内容)(社名¦例)"},
		{"表: x\n本文", "p(表: x⏎本文)"},
		{"表:\n" + tbl, "p(表:)\ntable[表1](項目¦内容)(社名¦例)"},
		{"表：x\n" + tbl, "p(表：x)\ntable[表1](項目¦内容)(社名¦例)"},
		{"## 概要\n" + tbl + "\n\n" + tbl, "h2(概要)\ntable[表1（概要）](項目¦内容)(社名¦例)\ntable[表2（概要）](項目¦内容)(社名¦例)"},
		{"| **重要** | [会社](/about) |\n|---|---|", "table[表1](«S:重要»¦«L:/about:会社»)()"},
		// 文字の並び
		{"**重要**", "p(«S:重要»)"},
		{"**閉じない", "p(**閉じない)"},
		{"****", "p(****)"},
		{"** **", "p(** **)"},
		{"****x**", "p(**«S:x»)"},
		{`**a\**b**`, "p(«S:a**b»)"},
		{"**a[b**](/x)", "p(«S:a[b»](/x))"},
		{"[a**](/x)**", "p(«L:/x:a**»**)"},
		{"[a[b](/x)", "p(«L:/x:a[b»)"},
		{"[**強調**](/about)", "p(«L:/about:**強調**»)"},
		{"**[リンク](/about)**", "p(«S:[リンク](/about)»)"},
		{`\[文字\](/about)`, "p([文字](/about))"},
		{`文字\`, `p(文字\)`},
		{`\\`, `p(\)`},
		{`\- x`, "p(- x)"},
		{`1\. x`, "p(1. x)"},
		{`\1. x`, `p(\1. x)`},
		{"***x***", "p(«S:*x»*)"},
		{"##  x", "h2(x)"},
		{"-  x", "ul(x)"},
		{"| h |\n|\\-\\-\\-|", "p(| h |⏎|---|)"},
		{"| a | b |\n|---|---|\n|---|---|", "table[表1](a¦b)(---¦---)"},
		{"## [会社](/about) **強**\n" + tbl, "h2(«L:/about:会社» «S:強»)\ntable[表1（会社 強）](項目¦内容)(社名¦例)"},
		{"表: **x** [a](/b)\n" + tbl, "table[**x** [a](/b)](項目¦内容)(社名¦例)"},
		{`\a`, `p(\a)`},
		{"<script>", "p(<script>)"},
		// リンクのURL
		{"[会社](/about)", "p(«L:/about:会社»)"},
		{"[a](/x?y=1#z)", "p(«L:/x?y=1#z:a»)"},
		{"[外部](https://example.com)", "p(«L:https://example.com:外部»)"},
		{"[a](https://[::1]/)", "p(«L:https://[::1]/:a»)"},
		{"[a](https://ja.wikipedia.org/wiki/Go_(言語))", "p(«L:https://ja.wikipedia.org/wiki/Go_(言語:a»))"},
		{"[危険](javascript:alert(1))", "p([危険](javascript:alert(1)))"},
		{"[古い](http://example.com)", "p([古い](http://example.com))"},
		{"[a](HTTPS://example.com)", "p([a](HTTPS://example.com))"},
		{"[a](mailto:x@example.com)", "p([a](mailto:x@example.com))"},
		{"[a](https://)", "p([a](https://))"},
		{"[a](https:example.com)", "p([a](https:example.com))"},
		{"[a](https://user@example.com)", "p([a](https://user@example.com))"},
		{"[a](//example.com)", "p([a](//example.com))"},
		{`[a](/\example.com)`, `p([a](/\example.com))`},
		{"[空白](/a b)", "p([空白](/a b))"},
		{"[](/about)", "p([](/about))"},
		{"[ ](/about)", "p([ ](/about))"},
		{`[a](/x\)y)`, "p([a](/x)y))"},
		{"[a](https://example.com%00)", "p([a](https://example.com%00))"},
		{"[a](/x\u202ey)", "p([a](/x\u202ey))"},
	}
	for _, c := range cases {
		doc, err := Parse(c.in)
		if err != nil {
			t.Errorf("%q: %v", c.in, err)
			continue
		}
		if got := dump(doc); got != c.want {
			t.Errorf("%q\n got: %s\nwant: %s", c.in, got, c.want)
		}
	}
}

func TestParse_Limits(t *testing.T) {
	row := func(n int, cell string) string { return "|" + strings.Repeat(" "+cell+" |", n) }
	sep := func(n int) string { return "|" + strings.Repeat("---|", n) }
	if _, err := Parse(row(20, "h") + "\n" + sep(20)); err != nil {
		t.Fatalf("20 columns: %v", err)
	}
	if _, err := Parse(row(21, "h") + "\n" + sep(21)); !errors.Is(err, ErrLimit) {
		t.Fatalf("21 columns: %v", err)
	}
	head := row(2, "h") + "\n" + sep(2)
	if _, err := Parse(head + strings.Repeat("\n"+row(2, "x"), 200)); err != nil {
		t.Fatalf("200 rows: %v", err)
	}
	if _, err := Parse(head + strings.Repeat("\n"+row(2, "x"), 201)); !errors.Is(err, ErrLimit) {
		t.Fatalf("201 rows: %v", err)
	}
	if _, err := Parse(head + "\n" + row(3, "x")); !errors.Is(err, ErrLimit) {
		t.Fatalf("too many cells: %v", err)
	}
}

// TestParse_LinearTime は、閉じる記号が多い・失敗する入力でも、読む時間が長さに比例することを確かめる。
func TestParse_LinearTime(t *testing.T) {
	for name, in := range map[string]string{
		"brackets":     strings.Repeat("[", 50000) + "]",
		"open links":   strings.Repeat("[a](x", 10000),
		"space urls":   strings.Repeat("[a](x ", 10000) + ")",
		"stars":        strings.Repeat("**", 25000),
		"long url":     strings.Repeat("[a](/"+strings.Repeat("x", 3000), 15) + ")",
		"pipes":        strings.Repeat("|", 50000),
		"table rows":   "| a | b |\n|---|---|" + strings.Repeat("\n|", 150),
		"shared paren": strings.Repeat(strings.Repeat("[](", 680)+")", 24),
		"https shared": strings.Repeat("[a](https://x", 4000) + ")",
	} {
		start := time.Now()
		if _, err := Parse(in); err != nil && !errors.Is(err, ErrLimit) {
			t.Fatalf("%s: %v", name, err)
		}
		if d := time.Since(start); d > 500*time.Millisecond {
			t.Errorf("%s took %v", name, d)
		}
	}
}

func TestAllowedURL(t *testing.T) {
	for raw, ok := range map[string]bool{
		"https://example.com/a?b=c": true, "/about": true, "/": true,
		"http://example.com": false, "HTTPS://example.com": false, "https://": false, "https:example.com": false,
		"https://user@example.com": false, "https://exa%41mple.com": false, "//example.com": false, `/\example.com`: false,
		"javascript:alert(1)": false, "data:text/html,x": false, "mailto:a@example.com": false, "about": false,
		"/a b": false, "/a\tb": false, "/a\x00b": false, "/a\u0085b": false, "/a\u200bb": false, "": false,
		"/" + strings.Repeat("a", 2048): false,
	} {
		if got := AllowedURL(raw) != ""; got != ok {
			t.Errorf("AllowedURL(%q) = %v, want %v", raw, got, ok)
		}
	}
}

// checkDoc は、構造が規則どおりかを確かめる（表の大きさ、リンクの許可、空の強調・リンクが無い、決まった種類だけ）。
func checkDoc(t *testing.T, doc Doc) {
	t.Helper()
	inline := func(xs []Inline) {
		for _, x := range xs {
			switch x.Kind {
			case "text":
			case "strong", "link":
				if strings.TrimSpace(x.Text) == "" {
					t.Fatalf("empty %s", x.Kind)
				}
				if x.Kind == "link" && (x.Href == "" || AllowedURL(x.Href) != x.Href) {
					t.Fatalf("link not allowed: %q", x.Href)
				}
			default:
				t.Fatalf("unknown inline %q", x.Kind)
			}
		}
	}
	for _, b := range doc.Blocks {
		if !slices.Contains([]string{"h2", "h3", "p", "ul", "ol", "table"}, b.Kind) {
			t.Fatalf("unknown block %q", b.Kind)
		}
		inline(b.Inlines)
		for _, l := range append(b.Lines, b.Items...) {
			inline(l)
		}
		if b.Kind == "table" {
			if len(b.Head) == 0 || len(b.Head) > MaxColumns || len(b.Rows) > MaxRows || b.Caption == "" {
				t.Fatalf("table size: %d cols, %d rows", len(b.Head), len(b.Rows))
			}
			for _, c := range b.Head {
				inline(c)
			}
			for _, r := range b.Rows {
				if len(r) != len(b.Head) {
					t.Fatalf("row width %d != %d", len(r), len(b.Head))
				}
				for _, c := range r {
					inline(c)
				}
			}
		}
	}
}

// flatten は、構造の文字を入力と同じ順（リンクは文字の後にURL、表は説明・見出し・行の順）でつなぎます。
func flatten(doc Doc) string {
	var b strings.Builder
	in := func(xs []Inline) {
		for _, x := range xs {
			b.WriteString(x.Text + " " + x.Href + " ")
		}
	}
	for _, bl := range doc.Blocks {
		b.WriteString(bl.Caption + " ")
		in(bl.Inlines)
		for _, l := range append(bl.Lines, bl.Items...) {
			in(l)
		}
		for _, c := range bl.Head {
			in(c)
		}
		for _, r := range bl.Rows {
			for _, c := range r {
				in(c)
			}
		}
	}
	return b.String()
}

// letters は、比べる文字（字。記号・数字・空白と、表の説明の接頭辞に使う「表」を除く）です。
func letters(s string) []rune {
	var out []rune
	for _, r := range s {
		if unicode.IsLetter(r) && r != '表' {
			out = append(out, r)
		}
	}
	return out
}

// FuzzParse は、どんな本文でも、構造が規則どおりで、入力の字を落とさない（入力の字が出力の字の部分列になる）ことを確かめる。
func FuzzParse(f *testing.F) {
	for _, seed := range []string{
		"## 見出し\n本文 **強調** [リンク](/about)\n\n- a\n1. b\n\n表: 説明\n| a | b |\n|---|---|\n| [x](https://example.com) | y |",
		strings.Repeat("[", 3000) + "]", strings.Repeat("[](", 500) + ")", strings.Repeat("**", 2000), "\\| \\- 1\\. ***x***",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, body string) {
		if !utf8.ValidString(body) || len(body) > 20000 {
			return
		}
		doc, err := Parse(body)
		if err != nil {
			if !errors.Is(err, ErrLimit) {
				t.Fatalf("unexpected error: %v", err)
			}
			return
		}
		checkDoc(t, doc)
		in, out := letters(body), letters(flatten(doc))
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
