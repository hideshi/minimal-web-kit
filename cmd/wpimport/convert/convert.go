// Package convert は、WordPressの本文のHTMLを、キットの本文の記法に変えます。
// 固定ページは docs/richtext.md の書き方（見出し・段落・リスト・表・リンク・強調・画像の行）、
// お知らせは同じ文書の7節（段落・行・リンクだけ）に変えます。規則は docs/plans/wordpress-import-plan.md の5.3・10.7・13.2。
// HTMLの読み取りに golang.org/x/net/html を使います（この道具だけの依存）。
package convert

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// Target は変換の出力先です。
type Target int

const (
	Page Target = iota // 固定ページ（richtext）
	News               // お知らせ（行の中のリンクだけ）
)

// Image は本文に出た画像です。
type Image struct {
	AttachmentID int64  // class="wp-image-<ID>" の ID。無ければ0
	Src          string // src 属性
	Alt          string // alt 属性（空のこともある）
	Caption      string // [caption]・<figcaption> の文字（あれば）
	File         string // Resolver が返したキットのファイル名。結べなければ空
}

// Resolver は、移行先の対応を引きます。1段目（全件の移行先を決める前）は、何も結ばない resolver で呼べます。
type Resolver interface {
	// Image は、本文の画像をキットの画像のファイル名（<id>.<ext>）に結びます。
	Image(img Image) (file string, ok bool)
	// Link は、古いサイトの中へのリンクを新しいURLに書き換えます。古いサイトの外なら、そのまま返します。
	// internal は古いサイトの中だったか、ok は移行先が見つかったかです。
	Link(href string) (out string, internal, ok bool)
	// Attachments は、記事に付いた添付（post_parent が postID）の画像のIDを、WordPressの既定の順（menu_order、ID）で返します。
	Attachments(postID int64) []int64
	// Pattern は、同期パターン（wp:block の ref）の本文のHTMLを返します。
	Pattern(ref int64) (string, bool)
}

// Result は変換の結果です。
type Result struct {
	Body   string
	Images []Image        // 本文に出た画像（出た順）。お知らせでは本文に入れず、代表画像の候補と報告に使う
	Notes  map[string]int // 報告に出す、落とした物・変えた物の種類と数（固定の語。本文の文字は含めない）
}

func (r *Result) note(key string) { r.Notes[key]++ }

// Convert は本文のHTMLを変換します。postID は [gallery] の既定の添付に使います。
func Convert(src string, postID int64, target Target, res Resolver) Result {
	r := Result{Notes: map[string]int{}}
	src = expandPatterns(src, res, &r, 0)
	classic := !strings.Contains(src, "<!-- wp:")
	countComments(src, &r)
	src = shortcodes(src, postID, res, &r)
	nodes, err := html.ParseFragment(strings.NewReader(src), &html.Node{Type: html.ElementNode, Data: "body", DataAtom: atom.Body})
	if err != nil {
		r.note("unreadable_html")
		return r
	}
	c := &converter{res: res, r: &r, target: target, classic: classic}
	for _, n := range nodes {
		c.walk(n)
	}
	c.closeBlock()
	r.Body = render(c.blocks, target, &r)
	return r
}

// ---- 前処理: 同期パターン、コメントの数え、ショートコード ----

var (
	rePattern   = regexp.MustCompile(`<!-- wp:block \{"ref":(\d+)\}\s*/-->`)
	reSelfBlock = regexp.MustCompile(`<!-- wp:([a-z0-9/-]+)(?:\s+\{[^>]*?\})?\s*/-->`)
)

func expandPatterns(src string, res Resolver, r *Result, depth int) string {
	return rePattern.ReplaceAllStringFunc(src, func(m string) string {
		ref, _ := strconv.ParseInt(rePattern.FindStringSubmatch(m)[1], 10, 64)
		body, ok := res.Pattern(ref)
		if !ok || depth > 2 {
			r.note("pattern_missing")
			return ""
		}
		return expandPatterns(body, res, r, depth+1)
	})
}

// countComments は、表示の時に中身を作るブロック（HTMLを持たない wp:xxx /-->）と、ページ分割の印を数えます。
func countComments(src string, r *Result) {
	for _, m := range reSelfBlock.FindAllStringSubmatch(src, -1) {
		r.note("dynamic_block:" + m[1])
	}
	if n := strings.Count(src, "<!--more-->"); n > 0 {
		r.Notes["more_tag"] += n
	}
	if n := strings.Count(src, "<!--nextpage-->"); n > 0 {
		r.Notes["nextpage_tag"] += n
	}
}

var (
	reShortcode = regexp.MustCompile(`\[(/?)(caption|wp_caption|gallery|embed|audio|video|playlist)((?:\s[^\]]*)?)\]`)
	reAttr      = regexp.MustCompile(`([a-z_]+)\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s"']+))`)
)

// shortcodes は既知のショートコードだけを扱います（角括弧の普通の文字は触らない）。
// [caption] は figure に、[gallery] は画像の並びに変えます。他は囲みを外して中身を残し、中身の無い物は数えます。
func shortcodes(src string, postID int64, res Resolver, r *Result) string {
	return reShortcode.ReplaceAllStringFunc(src, func(m string) string {
		g := reShortcode.FindStringSubmatch(m)
		closing, name, attrs := g[1] == "/", g[2], parseAttrs(g[3])
		switch name {
		case "caption", "wp_caption":
			if closing {
				return "</figure>"
			}
			return `<figure data-shortcode="caption">`
		case "gallery":
			if closing {
				return ""
			}
			var b strings.Builder
			for _, id := range galleryIDs(attrs, postID, res) {
				b.WriteString(`<img class="wp-image-` + strconv.FormatInt(id, 10) + `" alt="">`)
			}
			if b.Len() == 0 {
				r.note("gallery_empty")
			}
			return `<div data-shortcode="gallery">` + b.String() + `</div>`
		default:
			if !closing {
				r.note("shortcode:" + name)
			}
			return ""
		}
	})
}

func parseAttrs(s string) map[string]string {
	out := map[string]string{}
	for _, m := range reAttr.FindAllStringSubmatch(s, -1) {
		out[strings.ToLower(m[1])] = m[2] + m[3] + m[4]
	}
	return out
}

func idList(s string) []int64 {
	var out []int64
	for _, part := range strings.Split(s, ",") {
		if id, err := strconv.ParseInt(strings.TrimSpace(part), 10, 64); err == nil && id > 0 {
			out = append(out, id)
		}
	}
	return out
}

// galleryIDs は、[gallery] の画像のIDを、WordPressの既定と同じ規則で決めます（計画13.3）。
// ids・include は書かれた順。無ければ記事（id= があればその記事）に付いた添付を menu_order・IDの順。
// exclude は除き、order="DESC" は逆順。orderby="rand" は元の順にする。
func galleryIDs(attrs map[string]string, postID int64, res Resolver) []int64 {
	var ids []int64
	switch {
	case attrs["ids"] != "":
		ids = idList(attrs["ids"])
	case attrs["include"] != "":
		ids = idList(attrs["include"])
	default:
		parent := postID
		if v := idList(attrs["id"]); len(v) == 1 {
			parent = v[0]
		}
		ids = append(ids, res.Attachments(parent)...)
		if exclude := idList(attrs["exclude"]); len(exclude) > 0 {
			kept := ids[:0]
			for _, id := range ids {
				if !contains(exclude, id) {
					kept = append(kept, id)
				}
			}
			ids = kept
		}
		if strings.EqualFold(attrs["order"], "desc") {
			for i, j := 0, len(ids)-1; i < j; i, j = i+1, j-1 {
				ids[i], ids[j] = ids[j], ids[i]
			}
		}
	}
	return ids
}

func contains(s []int64, v int64) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// ---- 木を歩いて、ブロックの並びを作る ----

type inline struct {
	kind string // text・strong・link
	text string
	href string
}

type block struct {
	kind    string // p・h2・h3・ul・ol・table・image
	lines   [][]inline
	items   [][]inline
	caption string
	rows    [][][]inline // 表の行（最初の行が見出し）
	image   Image
}

type converter struct {
	res     Resolver
	r       *Result
	target  Target
	classic bool

	blocks []block
	cur    *block   // 作りかけの段落・見出し・リスト
	line   []inline // 作りかけの行
	strong int      // strong の深さ
	link   *inline  // 作りかけのリンク
	pre    int
	figure []string // 開いている figure の中の文字（キャプションの候補）
	list   []string // 開いているリストの種類（ul・ol）

	table *tableState
}

type tableState struct {
	rows    [][][]inline
	row     [][]inline
	cell    *[]inline
	caption string
}

// dropped は、中身ごと捨てる要素です。
var dropped = map[atom.Atom]bool{
	atom.Script: true, atom.Style: true, atom.Iframe: true, atom.Form: true, atom.Embed: true, atom.Object: true,
	atom.Video: true, atom.Audio: true, atom.Input: true, atom.Select: true, atom.Textarea: true, atom.Button: true,
	atom.Noscript: true, atom.Svg: true, atom.Canvas: true, atom.Template: true, atom.Math: true,
}

// blockLevel は、段落を区切る要素です（中の文字は残す）。
var blockLevel = map[atom.Atom]bool{
	atom.P: true, atom.Div: true, atom.Section: true, atom.Article: true, atom.Header: true, atom.Footer: true,
	atom.Main: true, atom.Aside: true, atom.Nav: true, atom.Blockquote: true, atom.Pre: true, atom.Figure: true,
	atom.Figcaption: true, atom.Details: true, atom.Summary: true, atom.Dl: true, atom.Dt: true, atom.Dd: true,
	atom.Hr: true, atom.Address: true, atom.Center: true,
}

func (c *converter) walk(n *html.Node) {
	switch n.Type {
	case html.TextNode:
		c.text(n.Data)
		return
	case html.CommentNode, html.DoctypeNode:
		return
	case html.ElementNode:
	default:
		for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
			c.walk(ch)
		}
		return
	}
	a := n.DataAtom
	switch {
	case dropped[a]:
		c.r.note("dropped:" + n.Data)
		return
	case a == atom.Img:
		c.image(n)
		return
	case a == atom.Br:
		c.lineBreak()
		return
	case a == atom.H1 || a == atom.H2 || a == atom.H3 || a == atom.H4 || a == atom.H5 || a == atom.H6:
		c.closeBlock()
		kind := "h3"
		if a == atom.H1 || a == atom.H2 {
			kind = "h2"
		}
		c.cur = &block{kind: kind}
		c.children(n)
		c.closeBlock()
		return
	case a == atom.Ul || a == atom.Ol:
		if c.table != nil {
			c.children(n)
			return
		}
		if len(c.list) == 0 {
			c.closeBlock()
		} else {
			c.closeItem()
			c.r.note("nested_list")
		}
		kind := "ul"
		if a == atom.Ol {
			kind = "ol"
		}
		c.list = append(c.list, kind)
		if c.cur == nil {
			c.cur = &block{kind: kind}
		}
		c.children(n)
		c.closeItem()
		c.list = c.list[:len(c.list)-1]
		if len(c.list) == 0 {
			c.closeBlock()
		}
		return
	case a == atom.Li:
		if len(c.list) == 0 || c.table != nil {
			c.boundary(n)
			return
		}
		c.closeItem()
		if c.cur == nil {
			c.cur = &block{kind: c.list[len(c.list)-1]}
		}
		c.children(n)
		c.closeItem()
		return
	case a == atom.Table:
		c.tableElement(n)
		return
	case a == atom.Caption && c.table != nil:
		c.table.caption = strings.TrimSpace(textOf(n))
		return
	case a == atom.Tr && c.table != nil:
		c.table.row = nil
		c.children(n)
		if len(c.table.row) > 0 {
			c.table.rows = append(c.table.rows, c.table.row)
		}
		c.table.row = nil
		return
	case (a == atom.Td || a == atom.Th) && c.table != nil:
		cell := []inline{}
		c.table.cell = &cell
		c.children(n)
		c.flushLink()
		c.table.row = append(c.table.row, trimInlines(*c.table.cell))
		c.table.cell = nil
		return
	case a == atom.A:
		c.anchor(n)
		return
	case a == atom.Strong || a == atom.B:
		c.strong++
		c.children(n)
		c.strong--
		return
	case a == atom.Figure:
		c.closeBlock()
		c.figure = append(c.figure, "")
		c.children(n)
		c.figure = c.figure[:len(c.figure)-1]
		c.closeBlock()
		return
	case a == atom.Pre:
		c.closeBlock()
		c.pre++
		c.children(n)
		c.pre--
		c.closeBlock()
		return
	case blockLevel[a]:
		c.boundary(n)
		return
	}
	c.children(n) // span・em・code 等は中の文字だけを残す
}

func (c *converter) children(n *html.Node) {
	for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
		c.walk(ch)
	}
}

// boundary は、段落の区切りにして中の文字を残します。リストの項目の中では、項目を区切りません。
func (c *converter) boundary(n *html.Node) {
	if len(c.list) > 0 || c.table != nil {
		c.children(n)
		return
	}
	c.closeBlock()
	c.children(n)
	c.closeBlock()
}

func textOf(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		if n.Type == html.ElementNode && dropped[n.DataAtom] {
			return
		}
		for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
			walk(ch)
		}
	}
	walk(n)
	return strings.Join(strings.Fields(b.String()), " ")
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

var reWPImage = regexp.MustCompile(`(?:^|\s)wp-image-(\d+)(?:\s|$)`)

func (c *converter) image(n *html.Node) {
	img := Image{Src: attr(n, "src"), Alt: strings.TrimSpace(strings.Join(strings.Fields(attr(n, "alt")), " "))}
	if m := reWPImage.FindStringSubmatch(attr(n, "class")); m != nil {
		img.AttachmentID, _ = strconv.ParseInt(m[1], 10, 64)
	}
	if len(c.figure) > 0 {
		img.Caption = figureCaption(n)
	}
	if file, ok := c.res.Image(img); ok {
		img.File = file
	}
	c.r.Images = append(c.r.Images, img)
	if c.target == News {
		return // お知らせの本文には画像を入れない（代表画像の候補と報告に使う）
	}
	if c.table != nil {
		c.r.note("image_in_table")
		if text := altText(img); text != "" {
			c.add(inline{kind: "text", text: text})
		}
		return
	}
	// 画像は常にそれだけの行にする（計画13.2）。リストの中なら項目とリストを閉じる
	listKinds := c.list
	c.closeBlock()
	if img.File == "" {
		c.r.note("image_unresolved")
		if text := altText(img); text != "" {
			c.blocks = append(c.blocks, block{kind: "p", lines: [][]inline{{{kind: "text", text: text}}}})
		}
	} else {
		c.blocks = append(c.blocks, block{kind: "image", image: img})
	}
	if len(listKinds) > 0 {
		c.cur = &block{kind: listKinds[len(listKinds)-1]}
	}
}

// figureCaption は、画像を含む figure の中の、画像以外の文字です（[caption] の文字、figcaption）。
func figureCaption(img *html.Node) string {
	for p := img.Parent; p != nil; p = p.Parent {
		if p.DataAtom == atom.Figure {
			return strings.TrimSpace(textOf(p))
		}
	}
	return ""
}

// altText は、画像を置けない所に代わりに書く文字です。
func altText(img Image) string {
	if img.Alt != "" {
		return img.Alt
	}
	return img.Caption
}

func (c *converter) anchor(n *html.Node) {
	if hasImage(n) && strings.TrimSpace(textOf(n)) == "" {
		c.children(n) // <a><img></a> は a を外す
		return
	}
	href := strings.TrimSpace(attr(n, "href"))
	c.flushLink()
	c.link = &inline{kind: "link", href: href}
	c.children(n)
	c.flushLink()
}

func hasImage(n *html.Node) bool {
	for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
		if ch.DataAtom == atom.Img || hasImage(ch) {
			return true
		}
	}
	return false
}

func (c *converter) tableElement(n *html.Node) {
	if c.table != nil {
		c.r.note("nested_table")
		c.children(n)
		return
	}
	c.closeBlock()
	c.table = &tableState{}
	c.children(n)
	t := c.table
	c.table = nil
	if len(t.rows) == 0 {
		return
	}
	c.blocks = append(c.blocks, block{kind: "table", rows: t.rows, caption: t.caption})
}

// ---- 文字と行 ----

func (c *converter) text(s string) {
	if strings.IndexFunc(s, func(r rune) bool { return unicode.IsControl(r) && r != '\n' && r != '\t' && r != '\r' }) >= 0 {
		c.r.note("control_char")
		s = strings.Map(func(r rune) rune {
			if unicode.IsControl(r) && r != '\n' && r != '\t' && r != '\r' {
				return -1
			}
			return r
		}, s)
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	switch {
	case c.pre > 0:
		for i, part := range strings.Split(s, "\n") {
			if i > 0 {
				c.lineBreak()
			}
			c.add(inline{kind: "text", text: part})
		}
	case c.classic && c.table == nil && c.link == nil:
		// 古いエディタの本文は <p> を持たない。空行で段落、改行で行にする（wpautop の最小の相当）
		paragraphs := strings.Split(s, "\n\n")
		for i, para := range paragraphs {
			if i > 0 && len(c.list) == 0 {
				c.closeBlock()
			}
			for j, part := range strings.Split(para, "\n") {
				if j > 0 && strings.TrimSpace(part) != "" && len(c.list) == 0 {
					c.lineBreak()
				}
				c.add(inline{kind: "text", text: collapse(part)})
			}
		}
	default:
		c.add(inline{kind: "text", text: collapse(s)})
	}
}

// collapse は、HTMLと同じく空白の並びを1つにします。
func collapse(s string) string {
	var b strings.Builder
	space := false
	for _, r := range s {
		if r == ' ' || r == '\n' || r == '\t' || r == '\r' || r == '\f' {
			space = true
			continue
		}
		if space && b.Len() > 0 {
			b.WriteByte(' ')
		}
		space = false
		b.WriteRune(r)
	}
	if space && b.Len() > 0 {
		b.WriteByte(' ')
	}
	if b.Len() == 0 && s != "" && strings.TrimSpace(s) == "" {
		return " "
	}
	return b.String()
}

// add は文字を、今のリンク・セル・行に足します。強調の中なら強調にします。
func (c *converter) add(in inline) {
	if in.text == "" {
		return
	}
	if c.link != nil {
		c.link.text += in.text
		return
	}
	if c.strong > 0 && in.kind == "text" && strings.TrimSpace(in.text) != "" { // 強調とリンクが重なればリンクを優先する
		in.kind = "strong"
	}
	if c.table != nil {
		if c.table.cell != nil {
			*c.table.cell = appendInline(*c.table.cell, in)
		}
		return
	}
	if c.cur == nil {
		if strings.TrimSpace(in.text) == "" {
			return
		}
		kind := "p"
		if len(c.list) > 0 {
			kind = c.list[len(c.list)-1]
		}
		c.cur = &block{kind: kind}
	}
	c.line = appendInline(c.line, in)
}

func appendInline(line []inline, in inline) []inline {
	if n := len(line); n > 0 && line[n-1].kind == in.kind && in.kind != "link" {
		line[n-1].text += in.text
		return line
	}
	return append(line, in)
}

func (c *converter) flushLink() {
	if c.link == nil {
		return
	}
	l := *c.link
	c.link = nil
	l.text = strings.Join(strings.Fields(l.text), " ") // リンクの文字の中の改行は空白にする
	c.add(c.linkInline(l))
}

// linkInline は、リンクを移行先のURLにし、キットで読めない物は「文字（URL）」の文字にします（計画13.2）。
func (c *converter) linkInline(l inline) inline {
	if l.text == "" {
		c.r.note("link_empty_text")
		if l.href == "" {
			return inline{kind: "text"}
		}
		return inline{kind: "text", text: l.href}
	}
	href, internal, ok := c.res.Link(l.href)
	if internal && !ok {
		c.r.note("link_internal_unresolved")
	}
	if url, allowed := allowedHref(href); allowed {
		return inline{kind: "link", text: l.text, href: url}
	}
	if href == "" || strings.HasPrefix(href, "#") {
		c.r.note("link_fragment_or_empty")
		return inline{kind: "text", text: l.text}
	}
	c.r.note("link_as_text")
	return inline{kind: "text", text: l.text + "（" + href + "）"}
}

func (c *converter) lineBreak() {
	if c.link != nil {
		c.link.text += " "
		return
	}
	if c.table != nil {
		if c.table.cell != nil {
			*c.table.cell = appendInline(*c.table.cell, inline{kind: "text", text: " "})
		}
		return
	}
	if len(c.list) > 0 {
		c.line = appendInline(c.line, inline{kind: "text", text: " "}) // 項目は1行（計画13.2）
		return
	}
	if c.cur == nil {
		return
	}
	c.cur.lines = append(c.cur.lines, trimInlines(c.line))
	c.line = nil
}

func (c *converter) closeItem() {
	c.flushLink()
	if c.cur != nil && (c.cur.kind == "ul" || c.cur.kind == "ol") {
		if line := trimInlines(c.line); len(line) > 0 {
			c.cur.items = append(c.cur.items, line)
		}
	}
	c.line = nil
}

func (c *converter) closeBlock() {
	c.flushLink()
	if c.cur == nil {
		c.line = nil
		return
	}
	b := c.cur
	c.cur = nil
	switch b.kind {
	case "ul", "ol":
		if line := trimInlines(c.line); len(line) > 0 {
			b.items = append(b.items, line)
		}
		if len(b.items) > 0 {
			c.blocks = append(c.blocks, *b)
		}
	default:
		b.lines = append(b.lines, trimInlines(c.line))
		var lines [][]inline
		for _, l := range b.lines {
			if len(l) > 0 {
				lines = append(lines, l)
			}
		}
		if len(lines) > 0 {
			b.lines = lines
			c.blocks = append(c.blocks, *b)
		} else if b.kind != "p" {
			c.r.note("empty_heading")
		}
	}
	c.line = nil
}

// trimInlines は、並びの前後の空白を除き、空の要素を外します。
func trimInlines(in []inline) []inline {
	var out []inline
	for _, x := range in {
		if x.text != "" {
			out = append(out, x)
		}
	}
	for len(out) > 0 && out[0].kind == "text" {
		out[0].text = strings.TrimLeftFunc(out[0].text, unicode.IsSpace)
		if out[0].text != "" {
			break
		}
		out = out[1:]
	}
	for len(out) > 0 && out[len(out)-1].kind == "text" {
		last := &out[len(out)-1]
		last.text = strings.TrimRightFunc(last.text, unicode.IsSpace)
		if last.text != "" {
			break
		}
		out = out[:len(out)-1]
	}
	return out
}
