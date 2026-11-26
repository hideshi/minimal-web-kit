package web

import (
	"regexp"
	"strings"
	"testing"
	"time"
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
	idAttr   = regexp.MustCompile(` id="([^"]*)"`)
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

func renderPageRichtext(t *testing.T, body string) (string, bool) {
	t.Helper()
	doc, err := richtext.ParseForPages(body)
	if err != nil {
		return "", false
	}
	if doc.Hero != nil {
		doc.Hero.Title = "題名<b>"
	}
	return renderComponent(t, `{{template "richtext" .}}`, doc), true
}

// TestComponents_RichtextLPParts は、ヒーロー・ボタン・カードの描き方を確かめる（docs/richtext.md 2.2節）。
func TestComponents_RichtextLPParts(t *testing.T) {
	got, _ := renderPageRichtext(t, "ヒーロー: 導入<i>\nボタン: [相談](/contact)\nボタン: [外部](https://example.com)\n\n## 利点\nカード: 少ない | **説明**\nカード: b | c\nカード: d | e\n\nボタン: [もう一度](/contact)")
	for _, want := range []string{
		`<section class="hero"><h1 class="hero-title">題名&lt;b&gt;</h1><p class="hero-lead">導入&lt;i&gt;</p><div class="hero-cta"><a class="btn btn-primary" href="/contact">相談</a><a class="btn btn-secondary" href="https://example.com">外部</a></div></section>`,
		`<div class="grid-3"><article class="card"><h3 class="card-title">少ない</h3><p class="card-content"><strong>説明</strong></p></article>`,
		`<div class="action-bar"><a class="btn btn-primary" href="/contact">もう一度</a></div>`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in %s", want, got)
		}
	}
	if strings.Contains(got, "target=") || strings.Contains(got, "rel=") {
		t.Errorf("buttons must open in the same tab: %s", got)
	}
	if got, _ := renderPageRichtext(t, "カード: a | b"); !strings.Contains(got, `<div class="grid-2"><article class="card"><h2 class="card-title">a</h2>`) {
		t.Errorf("cards without a heading before them use h2: %s", got)
	}
}

// TestComponents_RichtextHomeParts は、トップページ等の部品の描き方を確かめる（docs/richtext.md 2.3節・2.4節）。
// お知らせの一覧とお問い合わせのボタンは、描く側が中身を入れたときだけ描く（PAGES-INV-18・PAGES-INV-20）。
func TestComponents_RichtextHomeParts(t *testing.T) {
	doc, err := richtext.ParseForPages("目次: このページの目次\n## 特長 {#features}\n質問: 必要？\n答え: [いいえ](#features)\n\n## 流れ\n手順: 確かめる | 説明\n手順: 作る | 説明2\n\n帯: 始める | 説明\nボタン: [入手](/get)\n\n引用: 丁寧 | Aさん\n\nお知らせ: 3\n\nお知らせ: イベント | 2\n\nお問い合わせ: 相談する\n\nお問い合わせ: 外へ")
	if err != nil {
		t.Fatal(err)
	}
	var news, contacts []*richtext.Block
	for i := range doc.Blocks {
		switch doc.Blocks[i].Kind {
		case "news":
			news = append(news, &doc.Blocks[i])
		case "contact":
			contacts = append(contacts, &doc.Blocks[i])
		}
	}
	at := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	news[0].NewsReady, news[0].NewsItems, news[0].NewsList = true, []richtext.NewsItem{{Title: "記事<b>", Href: "/news/1", Date: at}}, "/news"
	news[1].NewsReady = true // 0件
	contacts[0].ContactHref = "/contact"
	contacts[1].ContactHref, contacts[1].ContactExternal = "https://forms.example.com/x", true
	got := renderComponent(t, `{{template "richtext" .}}`, doc)
	for _, want := range []string{
		`<nav class="page-contents" aria-label="このページの目次"><a href="#sec-features">特長</a></nav>`,
		`<h2 id="sec-features">特長</h2>`,
		`<div class="faq-list"><details class="faq-item"><summary>必要？</summary><div class="faq-answer"><p><a href="#sec-features">いいえ</a></p></div></details></div>`,
		`<ol class="step-list" role="list"><li class="step-item"><div class="step-number">STEP 1</div><h3 class="step-title">確かめる</h3><p class="step-content">説明</p></li><li class="step-item"><div class="step-number">STEP 2</div>`,
		`<section class="cta-banner"><h3>始める</h3><p>説明</p><div class="cta-actions"><a class="btn btn-primary" href="/get">入手</a></div></section>`,
		`<figure class="quote"><blockquote><p>丁寧</p></blockquote><figcaption>Aさん</figcaption></figure>`,
		`<ul class="news-latest"><li><time datetime="2026-11-02">2026年11月2日</time><a href="/news/1">記事&lt;b&gt;</a></li></ul><p><a href="/news">お知らせの一覧へ</a></p>`,
		`<p>お知らせはまだありません。</p>`,
		`<p class="action-bar"><a class="btn btn-primary" href="/contact">相談する</a></p>`,
		`<a class="btn btn-primary" href="https://forms.example.com/x" target="_blank" rel="noopener noreferrer">外へ（新しいタブで開きます）</a>`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s\nin %s", want, got)
		}
	}
	// 中身を入れなかったお知らせの一覧（機能が無い）とお問い合わせのボタン（行き先が無い）は描かない
	doc2, _ := richtext.ParseForPages("お知らせ: 3\n\nお問い合わせ: 相談する")
	if got := renderComponent(t, `{{template "richtext" .}}`, doc2); got != `<div class="richtext"></div>` {
		t.Errorf("unresolved parts must render nothing: %s", got)
	}
}

var knownPageTag =regexp.MustCompile(`</?(div|section|article|h1|h2|h3|p|br|ul|ol|li|table|caption|thead|tbody|tr|th|td|strong|a|details|summary|figure|blockquote|figcaption|nav)( (class|scope|href|id|role|aria-label)="[^"<>]*")*>`)

// pageHref は、固定ページの本文に描いてよいリンク先です（外部の https://、サイト内の /、同じ本文の飛び先 #sec-名前）。
var pageHref = regexp.MustCompile(`^(https://|/[^/]|/$|#sec-[a-z0-9][a-z0-9-]*[a-z0-9]$)`)

// FuzzRichtextPages は、固定ページの読み方（ParseForPages）でも、止まらず、出力が入力に比例し、リンクとボタンが許可したURLだけで、
// 既知のタグ以外の < が出ないことを確かめる。
func FuzzRichtextPages(f *testing.F) {
	for _, seed := range []string{
		"ヒーロー: 導入\nボタン: [a](/a)\nボタン: [b](https://example.com)\n\n## 利点\nカード: a | b\nカード: [c](/c) | **d**",
		"ボタン: [危険](javascript:alert(1))\nボタン: [x](//evil)\nカード: <script> | <img src=x>",
		"ボタン: " + strings.Repeat("[", 3000), "カード: " + strings.Repeat("|", 3000), strings.Repeat("ボタン: [a](/a)\n", 500), strings.Repeat("カード: a | b\n", 500),
		"ヒーロー: \"><img src=x onerror=alert(1)>\nボタン: [x](/\"onmouseover=alert(1))",
		"目次: <x>\n## 特長 {#features}\n質問: a\n答え: [b](#features)\n帯: c | d\nボタン: [e](/e)\n手順: f | g\n引用: h | i\nお知らせ: j | 3\nお問い合わせ: k",
		"## \"><b> {#a-b}\n目次: \" onclick=\"x\n[x](#a\"b)\n[y](#-)",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, body string) {
		if !utf8.ValidString(body) || len(body) > 20000 {
			return
		}
		out, ok := renderPageRichtext(t, body)
		if !ok {
			return
		}
		if len(out) > 60*len(body)+400 {
			t.Fatalf("output too large: %d for %d", len(out), len(body))
		}
		for _, m := range hrefAttr.FindAllStringSubmatch(out, -1) {
			if !pageHref.MatchString(m[1]) {
				t.Fatalf("unexpected href %q", m[1])
			}
		}
		for _, m := range idAttr.FindAllStringSubmatch(out, -1) {
			if !strings.HasPrefix(m[1], "sec-") {
				t.Fatalf("id without the sec- prefix %q", m[1])
			}
		}
		if rest := knownPageTag.ReplaceAllString(out, ""); strings.Contains(rest, "<") || strings.Contains(rest, ">") {
			t.Fatalf("unexpected markup in %q", rest)
		}
		// ヒーローは1つだけ描く（題名の h1 が2つにならない）
		if doc, _ := richtext.ParseForPages(body); doc.Hero != nil && strings.Count(out, `class="hero"`) != 1 {
			t.Fatalf("hero rendered %d times", strings.Count(out, `class="hero"`))
		}
	})
}
