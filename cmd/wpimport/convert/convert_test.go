package convert

import (
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"minimal-web-kit/internal/web/richtext"
)

const fileA = "0123456789abcdef0123456789abcdef.jpg"
const fileB = "fedcba9876543210fedcba9876543210.png"

// fake は、添付30・31を画像に、古いサイトの /old/ と ?p=20 を新しいURLに結ぶ見本の対応です。
type fake struct{}

func (fake) Image(img Image) (string, string, bool) {
	switch {
	case img.AttachmentID == 30 || strings.Contains(img.Src, "/shop"):
		return fileA, "", true
	case img.AttachmentID == 31:
		return fileB, "", true
	case img.AttachmentID == 32:
		return fileA, "添付の説明", true // WXRの代替テキストのある添付
	}
	return "", "", false
}

func (fake) Link(href string) (string, bool, bool) {
	switch {
	case strings.HasPrefix(href, "https://old.example.com/old/"), href == "https://old.example.com/?p=20":
		return "/news/7", true, true
	case strings.HasPrefix(href, "https://old.example.com/"):
		return href, true, false
	}
	return href, false, false
}

func (fake) Attachments(postID int64) []int64 {
	if postID == 5 {
		return []int64{31, 30}
	}
	return nil
}

func (fake) Pattern(ref int64) (string, bool) {
	if ref == 9 {
		return "<p>パターンの中身</p>", true
	}
	return "", false
}

func TestConvert_Page(t *testing.T) {
	for _, c := range []struct{ name, in, want string }{
		{"heading and paragraph", "<h1>題</h1><h2>大</h2><h4>小</h4><p>本文</p>", "## 題\n\n## 大\n\n### 小\n\n本文"},
		{"classic blank lines", "一行目\n二行目\n\n次の段落", "一行目\n二行目\n\n次の段落"},
		{"block comments", "<!-- wp:paragraph -->\n<p>a</p>\n<!-- /wp:paragraph -->\n\n<!-- wp:paragraph -->\n<p>b</p>\n<!-- /wp:paragraph -->", "a\n\nb"},
		{"br", "<p>a<br>b</p>", "a\nb"},
		{"lists", "<ul><li>a</li><li>b<ul><li>c</li></ul></li></ul><ol><li>x</li><li>y</li></ol>", "- a\n- b\n- c\n\n1. x\n2. y"},
		{"li br", "<ul><li>a<br>b</li></ul>", "- a b"},
		{"strong and link", `<p><strong>強</strong>と<a href="https://example.com/x">外部</a>と<a href="/about">内部</a></p>`, "**強**と[外部](https://example.com/x)と[内部](/about)"},
		{"strong around link", `<p><strong>前<a href="/a">リンク</a>後</strong></p>`, "**前**[リンク](/a)**後**"},
		{"link rewrite", `<p><a href="https://old.example.com/old/x/">前の記事</a> <a href="https://old.example.com/?p=20">番号</a></p>`, "[前の記事](/news/7) [番号](/news/7)"},
		{"link as text", `<p><a href="http://example.com/">古い</a> <a href="mailto:a@example.invalid">メール</a></p>`, "古い（http://example.com/） メール（mailto:a@example.invalid）"},
		{"fragment and empty", `<p><a href="#top">上へ</a><a href="/x"></a></p>`, "上へ/x"},
		{"paren in url", `<p><a href="https://ja.wikipedia.org/wiki/Go_(言語)">Go</a></p>`, "[Go](https://ja.wikipedia.org/wiki/Go_%28%E8%A8%80%E8%AA%9E%29)"},
		{"uppercase scheme", `<p><a href="HTTPS://Example.COM/a">a</a></p>`, "[a](https://example.com/a)"},
		{"escape", `<p>[注] C:\temp と **星** と a*b</p>`, `\[注] C:\\temp と \*\*星\*\* と a*b`},
		{"line start", "<p># 番号</p><p>- 項目</p><p>1. 一</p><p>| 縦</p><p>表: 説明</p>", "\\# 番号\n\n\\- 項目\n\n1\\. 一\n\n\\| 縦\n\n表\\: 説明"},
		{"lp line start", `<p>ヒーロー: 導入</p><p>ボタン: <a href="/a">押す</a></p><p>カード: 題 | 説明</p>`, "ヒーロー\\: 導入\n\nボタン\\: [押す](/a)\n\nカード\\: 題 | 説明"},
		{"image", `<p>前</p><figure><img src="x" alt="店の外観" class="wp-image-30"></figure><p>後</p>`, "前\n\n![店の外観](/media/" + fileA + ")\n\n後"},
		{"image in li", `<ul><li>a</li><li><img class="wp-image-31" alt="図"></li><li>b</li></ul>`, "- a\n\n![図](/media/" + fileB + ")\n\n- b"},
		{"image in text", `<p>前<img class="wp-image-30" alt="絵">後</p>`, "前\n\n![絵](/media/" + fileA + ")\n\n後"},
		{"linked image", `<p><a href="/full.jpg"><img class="wp-image-30" alt="絵"></a></p>`, "![絵](/media/" + fileA + ")"},
		{"caption shortcode", `[caption id="attachment_30" width="300"]<img src="x" alt="" class="wp-image-30"> 店の外観[/caption]`, "![店の外観](/media/" + fileA + ")\n\n店の外観"},
		{"attachment alt first", `<p><img class="wp-image-32" alt="本文の説明"></p><p><img class="wp-image-32" alt=""></p>`, "![添付の説明](/media/" + fileA + ")\n\n![添付の説明](/media/" + fileA + ")"},
		{"spaces around inline", `<p>Look at <a href="/a">caption</a> love and <strong>bold</strong> text.</p>`, "Look at [caption](/a) love and **bold** text."},
		{"spaces around inline classic", `getting some <a href="/a">caption</a> love.` + "\n\n" + `next <em>em</em> word`, "getting some [caption](/a) love.\n\nnext em word"},
		{"spaces around inline news", `<p>see <a href="/a">this</a> now</p>`, "see [this](/a) now"},
		{"star before strong", `<p>*<strong>a</strong>*</p>`, `\***a**\*`},
		{"unresolved image", `<p><img src="https://elsewhere.example/a.gif" alt="他所の絵"></p>`, "他所の絵"},
		{"gallery default", `[gallery columns="2"]`, "![" + PlaceholderAlt + "](/media/" + fileB + ")\n\n![" + PlaceholderAlt + "](/media/" + fileA + ")"},
		{"gallery ids", `[gallery ids="30,31"]`, "![" + PlaceholderAlt + "](/media/" + fileA + ")\n\n![" + PlaceholderAlt + "](/media/" + fileB + ")"},
		{"gallery exclude desc", `[gallery exclude="31" order="DESC"]`, "![" + PlaceholderAlt + "](/media/" + fileA + ")"},
		{"table", "<table><caption>料金</caption><thead><tr><th>品</th><th>値</th></tr></thead><tbody><tr><td>a|b</td></tr><tr><td>c</td><td>d</td><td>e</td></tr></tbody></table>",
			"表: 料金\n| 品 | 値 |  |\n|---|---|---|\n| a\\|b |  |  |\n| c | d | e |"},
		{"dropped", `<p>a</p><script>alert(1)</script><iframe src="x"></iframe><p>b</p>`, "a\n\nb"},
		{"pattern", `<!-- wp:block {"ref":9} /-->`, "パターンの中身"},
		{"pre", "<pre>line1\n  line2</pre>", "line1\nline2"}, // 字下げは残らない（計画13.2）
		{"empty heading", "<h2> </h2><p>a</p>", "a"},
		{"control char", "<p>a\x01b</p>", "ab"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := Convert(c.in, 5, Page, fake{})
			if r.Body != c.want {
				t.Errorf("Convert(%q)\n got: %q\nwant: %q", c.in, r.Body, c.want)
			}
			doc, err := richtext.ParseForPages(r.Body)
			if err != nil {
				t.Errorf("output must parse: %v", err)
			}
			// 移行した本文は、LPの部品にならない（元の段落のまま読む）
			if doc.Hero != nil {
				t.Errorf("output became a hero: %q", r.Body)
			}
			for _, b := range doc.Blocks {
				if b.Kind == "buttons" || b.Kind == "cards" {
					t.Errorf("output became %s: %q", b.Kind, r.Body)
				}
			}
		})
	}
}

func TestConvert_News(t *testing.T) {
	for _, c := range []struct{ name, in, want string }{
		{"paragraphs and links", `<h2>見出し</h2><p><strong>強</strong>と<a href="/about">内部</a></p>`, "見出し\n\n強と[内部](/about)"},
		{"classic", "一行目\n二行目\n\n次", "一行目\n二行目\n\n次"},
		{"lists", "<ul><li>a</li><li>b</li></ul><ol><li>x</li></ol>", "・a\n・b\n\n1. x"},
		{"table", "<table><tr><th>品</th><th>値</th></tr><tr><td>a</td><td>1</td></tr></table>", "品 / 値\na / 1"},
		{"escape", `<p>[注] \ と *星* と <a href="/x">a]b</a></p>`, `\[注] \\ と *星* と [a\]b](/x)`},
		{"images out", `<p>前</p><img class="wp-image-30" alt="絵"><p>後</p>`, "前\n\n後"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := Convert(c.in, 0, News, fake{})
			if r.Body != c.want {
				t.Errorf("Convert(%q)\n got: %q\nwant: %q", c.in, r.Body, c.want)
			}
		})
	}
	r := Convert(`<p>前</p><img class="wp-image-30" alt="絵"><img class="wp-image-31" alt="">`, 0, News, fake{})
	if len(r.Images) != 2 || r.Images[0].File != fileA || r.Images[0].Alt != "絵" || r.Images[1].File != fileB {
		t.Fatalf("news images: %+v", r.Images)
	}
}

func TestConvert_Notes(t *testing.T) {
	src := `<!-- wp:latest-posts /--><!-- wp:search {"label":"a > b"} /--><p>a<!--more-->b</p><script>x</script>[audio src="a.mp3"]` +
		`<p><a href="https://old.example.com/missing/">消えた</a></p><img src="https://elsewhere.example/x.gif" alt="">` +
		`<!-- wp:block {"ref":99} /-->`
	r := Convert(src, 0, Page, fake{})
	for key, want := range map[string]int{
		"dynamic_block:latest-posts": 1, "dynamic_block:search": 1, "more_tag": 1, "dropped:script": 1, "shortcode:audio": 1,
		"link_internal_unresolved": 1, "image_unresolved": 1, "pattern_missing": 1,
	} {
		if r.Notes[key] != want {
			t.Errorf("note %s = %d, want %d (%v)", key, r.Notes[key], want, r.Notes)
		}
	}
}

// letters は比べる字です（記号・空白・数字を除く。仮の説明の文字は除く）。
func letters(s string) []rune {
	s = strings.ReplaceAll(s, PlaceholderAlt, "")
	var out []rune
	for _, r := range s {
		if unicode.IsLetter(r) {
			out = append(out, r)
		}
	}
	return out
}

// FuzzConvert は、どんな本文でも、出力が出力先の読み方で読めて、捨てると決めた要素の外の字を落とさないことを確かめる。
func FuzzConvert(f *testing.F) {
	for _, seed := range []string{
		"<h2>a</h2><p><strong>b</strong><a href='/c'>d</a></p><ul><li>e<img class='wp-image-30' alt='f'></li></ul>",
		"<table><tr><td>a|b</td><td>[c]</td></tr></table>", "plain [x](/y) **z** \\ # - 1. |",
		"[caption]<img class='wp-image-30'> cap[/caption][gallery]", "<pre>a\n\nb</pre>",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, src string) {
		if !utf8.ValidString(src) || len(src) > 4000 {
			return
		}
		// 見張り: 終わらない入力を、その入力ごと落とす（ファズが黙って止まらないように）
		var page, news Result
		done := make(chan struct{})
		go func() { page, news = Convert(src, 5, Page, fake{}), Convert(src, 5, News, fake{}); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("too slow: %q", src)
		}
		doc, err := richtext.ParseWithImages(page.Body)
		if err != nil {
			t.Fatalf("page output must parse: %v\n%q", err, page.Body)
		}
		if !strings.ContainsAny(src, "<&[") { // HTMLもショートコードも無い文字だけの本文
			in, out := letters(src), letters(plainOf(doc))
			if string(in) != string(out) {
				t.Fatalf("letters changed:\n in: %q\nout: %q\nbody: %q", string(in), string(out), page.Body)
			}
		}
		for _, line := range strings.Split(news.Body, "\n") {
			for _, x := range richtext.LinkLine(line) {
				if x.Kind == "link" && richtext.AllowedURL(x.Href) != x.Href {
					t.Fatalf("news link not allowed: %q", x.Href)
				}
			}
		}
	})
}

// plainOf は、読んだ本文の文字（画像は説明、リンクは文字）を並べます。
func plainOf(doc richtext.Doc) string {
	var b strings.Builder
	in := func(xs []richtext.Inline) {
		b.WriteString(richtext.Plain(xs))
		b.WriteString(" ")
	}
	for _, bl := range doc.Blocks {
		b.WriteString(bl.Alt + " " + bl.Caption + " ")
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
