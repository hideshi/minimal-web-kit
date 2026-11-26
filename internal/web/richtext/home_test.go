package richtext

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

// homeDump は、トップページ等の部品（docs/richtext.md 2.3節・2.4節）を含む本文の構造を、1ブロック1行の文字にします。
// よくある質問は "faq(問=答;…)"、お知らせは "news(カテゴリ|件数)"、お問い合わせは "contact(文)"、引用は "quote(本文|出典)"、
// 帯は "cta[見出し](題=説明;ボタン…)"、手順は "steps[見出し](題=説明;…)"、目次は "toc(ラベル;id=文字,…)"、
// 飛び先つきの見出しは "h2#id(文字)"。
func homeDump(doc Doc) string {
	in := func(xs []Inline) string {
		return strings.TrimSuffix(dump(Doc{Blocks: []Block{{Kind: "p", Lines: [][]Inline{xs}}}})[2:], ")")
	}
	links := func(xs []Inline) string {
		var parts []string
		for _, x := range xs {
			parts = append(parts, x.Href+":"+x.Text)
		}
		return strings.Join(parts, ",")
	}
	var out []string
	for _, b := range doc.Blocks {
		switch b.Kind {
		case "h2", "h3":
			if b.ID != "" {
				out = append(out, b.Kind+"#"+b.ID+"("+in(b.Inlines)+")")
				continue
			}
			out = append(out, dump(Doc{Blocks: []Block{b}}))
		case "faq":
			var qs []string
			for _, q := range b.Cards {
				qs = append(qs, in(q.Title)+"="+in(q.Body))
			}
			out = append(out, "faq("+strings.Join(qs, ";")+")")
		case "news":
			out = append(out, "news("+b.NewsCategory+"|"+strconv.Itoa(b.NewsCount)+")")
		case "contact":
			out = append(out, "contact("+b.Label+")")
		case "quote":
			out = append(out, "quote("+in(b.Inlines)+"|"+in(b.Source)+")")
		case "cta":
			out = append(out, "cta["+b.Heading+"]("+in(b.Cards[0].Title)+"="+in(b.Cards[0].Body)+";"+links(b.Buttons)+")")
		case "steps":
			var ss []string
			for _, s := range b.Cards {
				ss = append(ss, in(s.Title)+"="+in(s.Body))
			}
			out = append(out, "steps["+b.Heading+"]("+strings.Join(ss, ";")+")")
		case "toc":
			var es []string
			for _, e := range b.Toc {
				es = append(es, e.ID+"="+e.Text)
			}
			out = append(out, "toc("+b.Label+";"+strings.Join(es, ",")+")")
		case "buttons":
			out = append(out, "btn("+links(b.Buttons)+")")
		default:
			out = append(out, dump(Doc{Blocks: []Block{b}}))
		}
	}
	return strings.Join(out, "\n")
}

// TestParseForPages_HomeParts は docs/richtext.md 6節の「トップページ等の部品」の例を、そのまま確かめる。
func TestParseForPages_HomeParts(t *testing.T) {
	qa := "質問: a\n答え: b"
	steps := func(n int) string {
		return "steps[h2](" + strings.TrimSuffix(strings.Repeat("a=b;", n), ";") + ")"
	}
	cases := []struct{ in, want string }{
		{"質問: 必要？\n答え: 不要です。", "faq(必要？=不要です。)"},
		{"質問: a\n答え: b\n質問: c\n答え: d", "faq(a=b;c=d)"},
		{"質問: a\n本文", "p(質問: a⏎本文)"},
		{"質問: a\n\n答え: b", "p(質問: a)\np(答え: b)"},
		{"答え: b", "p(答え: b)"},
		{"質問: a\n答え: b\n答え: c", "faq(a=b)\np(答え: c)"},
		{repeatLines(qa, 13), "faq(" + strings.TrimSuffix(strings.Repeat("a=b;", 12), ";") + ")\nfaq(a=b)"},
		{"質問:\n答え: b", "p(質問:⏎答え: b)"},
		{"質問: 　\n答え: b", "p(質問:⏎答え: b)"},
		{"質問\\: a\n答え: b", "p(質問: a⏎答え: b)"},
		{" 質問: a\n答え: b", "p( 質問: a⏎答え: b)"},
		{"質問: **重要**\n答え: [詳細](/about)", "faq(«S:重要»=«L:/about:詳細»)"},
		{"お知らせ: 3", "news(|3)"},
		{"お知らせ: イベント | 5", "news(イベント|5)"},
		{"お知らせ: a\\|b | 2", "news(a|b|2)"},
		{"お知らせ: 10", "news(|10)"},
		{"お知らせ: 0", "p(お知らせ: 0)"},
		{"お知らせ: 11", "p(お知らせ: 11)"},
		{"お知らせ: 03", "p(お知らせ: 03)"},
		{"お知らせ: ３", "p(お知らせ: ３)"},
		{"お知らせ: x", "p(お知らせ: x)"},
		{"お知らせ: a | b | 3", "p(お知らせ: a | b | 3)"},
		{"お知らせ:  | 3", "p(お知らせ:  | 3)"},
		{"お知らせ: **a** | 3", "news(**a**|3)"},
		{"お問い合わせ: フォームを開く", "contact(フォームを開く)"},
		{"お問い合わせ: [x](/x)", "contact([x](/x))"},
		{"お問い合わせ: ", "p(お問い合わせ:)"},
		{"お問い合わせ\\: x", "p(お問い合わせ: x)"},
		{"引用: 丁寧でした。", "quote(丁寧でした。|)"},
		{"引用: 丁寧でした。 | Aさん", "quote(丁寧でした。|Aさん)"},
		{"引用: a | ", "p(引用: a |)"},
		{"引用:  | b", "p(引用:  | b)"},
		{"引用: a | b | c", "p(引用: a | b | c)"},
		{"引用: a \\| b", "quote(a | b|)"},
		{"引用: **a** | [b](/b)", "quote(«S:a»|«L:/b:b»)"},
		{"帯: 始める | 説明\nボタン: [入手](/get)", "cta[h2](始める=説明;/get:入手)"},
		{"帯: a | b\nボタン: [1](/1)\nボタン: [2](/2)\nボタン: [3](/3)\nボタン: [4](/4)", "cta[h2](a=b;/1:1,/2:2,/3:3)\nbtn(/4:4)"},
		{"帯: a | b\n\nボタン: [x](/x)", "cta[h2](a=b;)\nbtn(/x:x)"},
		{"帯: a | b\nボタン: x[y](/y)\nボタン: [z](/z)", "cta[h2](a=b;)\np(ボタン: x«L:/y:y»)\nbtn(/z:z)"},
		{"帯: ab", "p(帯: ab)"},
		{"帯: a | b | c", "p(帯: a | b | c)"},
		{"帯:  | b", "p(帯:  | b)"},
		{"## 利点\n帯: a | b", "h2(利点)\ncta[h3](a=b;)"},
		{"帯: [a](/a) | b", "cta[h2]([a](/a)=b;)"},
		{"手順: 確かめる | 説明\n手順: 作る | 説明", "steps[h2](確かめる=説明;作る=説明)"},
		{repeatLines("手順: a | b", 13), steps(12) + "\n" + steps(1)},
		{"手順: ab", "p(手順: ab)"},
		{"手順: a | b | c", "p(手順: a | b | c)"},
		{"## 流れ\n手順: a | b", "h2(流れ)\nsteps[h3](a=b)"},
		{"目次: このページの目次", "toc(このページの目次;)"},
		{"目次: x\n目次: y", "toc(x;)\np(目次: y)"},
		{"目次:", "p(目次:)"},
		{"目次\\: x", "p(目次: x)"},
		{"## 特長 {#features}", "h2#sec-features(特長)"},
		{"### 小 {#a-1}", "h3#sec-a-1(小)"},
		{"## 特長 {#features}\n## 別 {#features}", "h2#sec-features(特長)\nh2(別 {#features})"},
		{"## 特長 {\\#features}", "h2(特長 {#features})"},
		{"## 特長 {#Features}", "h2(特長 {#Features})"},
		{"## 特長 {#a}", "h2(特長 {#a})"},
		{"## 特長 {#-a}", "h2(特長 {#-a})"},
		{"## 特長{#features}", "h2(特長{#features})"},
		{"## 特長 {#features} x", "h2(特長 {#features} x)"},
		{"## {#features}", "h2({#features})"},
		{"## 特長 {#features}\n目次: 目次", "h2#sec-features(特長)\ntoc(目次;sec-features=特長)"},
		{"目次: 目次\n## [特長](/a) {#features}\n### 小 {#small}\n## 無し", "toc(目次;sec-features=特長)\nh2#sec-features(«L:/a:特長»)\nh3#sec-small(小)\nh2(無し)"},
		{"[特長](#features)", "p(«L:#sec-features:特長»)"},
		{"[特長](#sec-features)", "p(«L:#sec-sec-features:特長»)"},
		{"[特長](#Features)", "p([特長](#Features))"},
		{"[特長](#a b)", "p([特長](#a b))"},
		{"[特長](#)", "p([特長](#))"},
		{"[特長](/about#sec-features)", "p(«L:/about#sec-features:特長»)"},
	}
	for _, c := range cases {
		doc, err := ParseForPages(c.in)
		if err != nil {
			t.Fatalf("%q: %v", c.in, err)
		}
		if got := homeDump(doc); got != c.want {
			t.Errorf("%q\n got: %s\nwant: %s", c.in, got, c.want)
		}
	}
}

// TestParseForPages_DuplicateAnchors は、重なった飛び先の名前を、警告のために返すことを確かめる（PAGES-INV-19）。
func TestParseForPages_DuplicateAnchors(t *testing.T) {
	doc, err := ParseForPages("## a {#x-1}\n## b {#x-1}\n## c {#x-1}\n## d {#y-2}")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(doc.DuplicateAnchors, ","); got != "x-1" {
		t.Errorf("duplicates: %q", got)
	}
}

// TestParse_HomePartsAreText は、社内文書（Parse）と ParseWithImages では、トップページ等の部品・飛び先・#名前 を読まないことを確かめる。
func TestParse_HomePartsAreText(t *testing.T) {
	cases := []struct{ in, want string }{
		{"[a](#features)", "p([a](#features))"},
		{"質問: a\n答え: b", "p(質問: a⏎答え: b)"},
		{"## a {#b-c}", "h2(a {#b-c})"},
		{"お知らせ: 3", "p(お知らせ: 3)"},
		{"目次: x", "p(目次: x)"},
	}
	for _, parse := range []func(string) (Doc, error){Parse, ParseWithImages} {
		for _, c := range cases {
			doc, err := parse(c.in)
			if err != nil {
				t.Fatal(err)
			}
			if got := homeDump(doc); got != c.want {
				t.Errorf("%q: got %s, want %s", c.in, got, c.want)
			}
		}
	}
}

// TestParseForPages_HomeLinearTime は、部品・飛び先・目次の多い長い本文を、決まった時間（1秒）の中で読めることを確かめる
// （処理の時間の回帰の検査。比例することそのものは BenchmarkParseForPages_Home の大きさ別の結果で見る）。
func TestParseForPages_HomeLinearTime(t *testing.T) {
	for name, body := range map[string]string{
		"faq":         repeatLines("質問: a\n答え: b", 10000),
		"anchors":     repeatLines("## a {#b-c}", 10000),
		"tocs":        repeatLines("目次: x", 10000),
		"anchors+toc": "目次: x\n" + repeatLines("## a {#b-c}\n## d {#e-f}", 5000),
		"questions":   repeatLines("質問: a", 20000),
		"steps":       repeatLines("手順: a | b", 10000),
		"cta pipes":   "帯: " + strings.Repeat("|", 50000),
		"heading":     "## " + strings.Repeat("{#", 25000),
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

// BenchmarkParseForPages_Home は、部品・飛び先・目次の多い本文を、大きさを変えて読みます。
// 大きさが4倍になったとき、1回あたりの時間がおよそ4倍（二乗なら16倍）になることを、結果を並べて見る。
// 例: go test -run XXX -bench ParseForPages_Home ./internal/web/richtext/
func BenchmarkParseForPages_Home(b *testing.B) {
	unit := "目次: x\n## a {#b-c}\n質問: a\n答え: [b](#b-c)\n手順: a | b\n帯: a | b\nボタン: [x](/x)\n引用: a | b\nお知らせ: c | 3\n"
	for _, n := range []int{250, 1000, 4000} {
		body := strings.Repeat(unit, n)
		b.Run(strconv.Itoa(n), func(b *testing.B) {
			b.SetBytes(int64(len(body)))
			for b.Loop() {
				if _, err := ParseForPages(body); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
