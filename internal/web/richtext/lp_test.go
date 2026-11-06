package richtext

import (
	"strings"
	"testing"
	"time"
)

// lpDump は、LPの部品を含む本文の構造を、1ブロック1行の文字にします。
// ヒーローは "hero(導入;ボタン…)"、ボタンの並びは "btn(…)"、カードの並びは "cards[grid,見出し](題名=説明;…)"。
func lpDump(doc Doc) string {
	in := func(xs []Inline) string { return dump(Doc{Blocks: []Block{{Kind: "p", Lines: [][]Inline{xs}}}})[2:] }
	strip := func(s string) string { return strings.TrimSuffix(s, ")") }
	links := func(xs []Inline) string {
		var parts []string
		for _, x := range xs {
			parts = append(parts, x.Href+":"+x.Text)
		}
		return strings.Join(parts, ",")
	}
	var out []string
	if h := doc.Hero; h != nil {
		out = append(out, "hero("+strip(in(h.Inlines))+";"+links(h.Buttons)+")")
	}
	for _, b := range doc.Blocks {
		switch b.Kind {
		case "buttons":
			out = append(out, "btn("+links(b.Buttons)+")")
		case "cards":
			var cs []string
			for _, c := range b.Cards {
				cs = append(cs, strip(in(c.Title))+"="+strip(in(c.Body)))
			}
			out = append(out, "cards["+b.Grid+","+b.Heading+"]("+strings.Join(cs, ";")+")")
		case "image":
			out = append(out, "img("+b.Alt+")")
		default:
			out = append(out, dump(Doc{Blocks: []Block{b}}))
		}
	}
	return strings.Join(out, "\n")
}

func repeatLines(line string, n int) string {
	return strings.TrimSuffix(strings.Repeat(line+"\n", n), "\n")
}

// TestParseForPages_SpecExamples は docs/richtext.md 6節の「LPの部品」の例を、そのまま確かめる。
func TestParseForPages_SpecExamples(t *testing.T) {
	card := "カード: a | b"
	cards := func(grid, h string, n int) string {
		return "cards[" + grid + "," + h + "](" + strings.TrimSuffix(strings.Repeat("a=b;", n), ";") + ")"
	}
	cases := []struct{ in, want string }{
		{"ヒーロー: 必要な機能だけで作る\nボタン: [相談する](/contact)", "hero(必要な機能だけで作る;/contact:相談する)"},
		{"\n\nヒーロー: x", "hero(x;)"},
		{"本文\nヒーロー: x", "p(本文⏎ヒーロー: x)"},
		{"ヒーロー: x\n\nヒーロー: y", "hero(x;)\np(ヒーロー: y)"},
		{"ヒーロー:", "p(ヒーロー:)"},
		{"ヒーロー: ", "p(ヒーロー:)"},
		{"ヒーロー:x", "p(ヒーロー:x)"},
		{"ヒーロー：x", "p(ヒーロー：x)"},
		{"ヒーロー: 　", "p(ヒーロー:)"},
		{"ヒーロー: \\", "hero(\\;)"},
		{"ヒーロー: **重要**な話", "hero(«S:重要»な話;)"},
		{"ヒーロー: x\nボタン: [a](/a)\nボタン: [b](/b)\nボタン: [c](/c)\nボタン: [d](/d)", "hero(x;/a:a,/b:b,/c:c)\nbtn(/d:d)"},
		{"ヒーロー: x\n\nボタン: [a](/a)", "hero(x;)\nbtn(/a:a)"},
		{"ボタン: [相談する](/contact)", "btn(/contact:相談する)"},
		{"ボタン:  [進む](/next)", "btn(/next:進む)"},
		{"ボタン: [進む](/next)　", "btn(/next:進む)"},
		{"ボタン: x[進む](/next)", "p(ボタン: x«L:/next:進む»)"},
		{"ボタン: [進む](/next) x", "p(ボタン: «L:/next:進む» x)"},
		{"ボタン: **[進む](/next)**", "p(ボタン: «S:[進む](/next)»)"},
		{"ボタン: [a](/a) [b](/b)", "p(ボタン: «L:/a:a» «L:/b:b»)"},
		{"ボタン: \\[進む](/next)", "p(ボタン: [進む](/next))"},
		{"ボタン: [進む](http://example.com)", "p(ボタン: [進む](http://example.com))"},
		{"ボタン: [外部](https://example.com)", "btn(https://example.com:外部)"},
		{"ボタン: [a](/a)\n\nボタン: [b](/b)", "btn(/a:a)\nbtn(/b:b)"},
		{"ボタン\\: [a](/a)", "p(ボタン: «L:/a:a»)"},
		{"カード: 少ない | 預かる情報が少ない", "cards[grid-2,h2](少ない=預かる情報が少ない)"},
		{"カード: a \\| b | c", "cards[grid-2,h2](a | b=c)"},
		{"カード: a | b | c", "p(カード: a | b | c)"},
		{"カード: ab", "p(カード: ab)"},
		{"カード:  | b", "p(カード:  | b)"},
		{"カード: a | ", "p(カード: a |)"},
		{"カード: 　| b", "p(カード: 　| b)"},
		{"カード: [x](/a|b) | c", "p(カード: «L:/a|b:x» | c)"},
		{"カード: [会社](/about) | **強調**の説明", "cards[grid-2,h2](«L:/about:会社»=«S:強調»の説明)"},
		{"カード: **a|b** | c", "p(カード: «S:a|b» | c)"},
		{"カード: [a|b](/x) | c", "p(カード: «L:/x:a|b» | c)"},
		{"カード: **a\\|b** | c", "cards[grid-2,h2](«S:a|b»=c)"},
		{repeatLines(card, 3), cards("grid-3", "h2", 3)},
		{repeatLines(card, 4), cards("grid-2", "h2", 4)},
		{repeatLines(card, 6), cards("grid-3", "h2", 6)},
		{repeatLines(card, 13), cards("grid-2", "h2", 12) + "\n" + cards("grid-2", "h2", 1)},
		{"カード: a | b\n\nカード: c | d", "cards[grid-2,h2](a=b)\ncards[grid-2,h2](c=d)"},
		{"## 利点\nカード: a | b", "h2(利点)\ncards[grid-2,h3](a=b)"},
		{"### 小見出し\nカード: a | b", "h3(小見出し)\ncards[grid-2,h2](a=b)"},
		{"カード\\: a | b", "p(カード: a | b)"},
		{"![a](/media/" + testID + ".jpg)\nボタン: [b](/b)", "img(a)\nbtn(/b:b)"},
		{"- 項目\nカード: a | b", "ul(項目)\ncards[grid-2,h2](a=b)"},
		{"ヒーロー: x\nボタン: x[a](/a)\nボタン: [b](/b)", "hero(x;)\np(ボタン: x«L:/a:a»)\nbtn(/b:b)"},
		{"| ボタン: [a](/a) |\n|---|", "table[表1](ボタン: «L:/a:a»)()"},
		{"本文\nボタン: [a](/a)\n続き", "p(本文)\nbtn(/a:a)\np(続き)"},
	}
	for _, c := range cases {
		doc, err := ParseForPages(c.in)
		if err != nil {
			t.Fatalf("%q: %v", c.in, err)
		}
		if got := lpDump(doc); got != c.want {
			t.Errorf("%q:\n got  %s\n want %s", c.in, got, c.want)
		}
	}
}

// TestParse_LPLinesStayParagraphs は、社内文書（Parse）と画像だけの読み方（ParseWithImages）では、
// LPの部品の行が今と同じ段落の行になることを確かめる。
func TestParse_LPLinesStayParagraphs(t *testing.T) {
	body := "ヒーロー: x\nボタン: [a](/a)\nカード: a | b"
	for name, parse := range map[string]func(string) (Doc, error){"Parse": Parse, "ParseWithImages": ParseWithImages} {
		doc, err := parse(body)
		if err != nil {
			t.Fatal(err)
		}
		if doc.Hero != nil || lpDump(doc) != "p(ヒーロー: x⏎ボタン: «L:/a:a»⏎カード: a | b)" {
			t.Errorf("%s: %s", name, lpDump(doc))
		}
	}
}

// TestParseForPages_LinearTime は、LPの部品の行の、長い1行と多数の短い行で、読む時間が入力の長さに比例することを確かめる。
func TestParseForPages_LinearTime(t *testing.T) {
	for name, body := range map[string]string{
		"button brackets": "ボタン: " + strings.Repeat("[", 50000),
		"card pipes":      "カード: " + strings.Repeat("|", 50000),
		"buttons":         repeatLines("ボタン: [a](/a)", 5000),
		"cards":           repeatLines("カード: a | b", 5000),
		"card links":      repeatLines("カード: [a](/a | b", 5000),
	} {
		start := time.Now()
		if _, err := ParseForPages(body); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if d := time.Since(start); d > time.Second {
			t.Errorf("%s took %v", name, d)
		}
	}
}
