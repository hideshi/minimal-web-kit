// Package richtext は、書き方を絞ったMarkdownを構造（ブロックと文字の並び）に読みます。
// 規則の正本は docs/richtext.md です。描くのは components.html の部品 "richtext" で、HTMLの文字列は組み立てません。
package richtext

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	MaxColumns = 20
	MaxRows    = 200
	// MaxImages は、1つの本文に書ける画像の数です（ParseWithImages）。
	MaxImages = 100
	// MaxAltRunes は、画像の説明の文字数の上限です。
	MaxAltRunes = 200
	// MaxButtons と MaxCards は、1つの並びのボタン・カードの数です（ParseForPages）。超えた行は新しい並びを始めます。
	MaxButtons = 3
	MaxCards   = 12
	maxURL     = 2048
	maxDigits  = 9
	// imagePath は、画像の記法に書けるURLの接頭辞です。
	imagePath = "/media/"
)

// imageFile は、画像のファイル名の正規の形です（IDは32文字の小文字の16進数）。
var imageFile = regexp.MustCompile(`^[a-f0-9]{32}\.(jpg|png)$`)

// ImageFile は、画像のファイル名が正規の形かを返します。公開の経路も同じ形だけを認めます。
func ImageFile(name string) bool { return imageFile.MatchString(name) }

// LimitError は、表の上限を超えたことを示します（書き方の誤りはエラーにせず、文字として出します）。
type LimitError struct{ Reason string }

func (e LimitError) Error() string { return "richtext: " + e.Reason }

// ErrLimit は LimitError を errors.Is で判定するための値です。
var ErrLimit = errors.New("richtext: over limit")

func (e LimitError) Is(target error) bool { return target == ErrLimit }

// Inline は文字の並びの1つです。Kind は "text"・"strong"・"link" のどれかです。
type Inline struct {
	Kind string
	Text string
	Href string // Kind が "link" のときだけ。docs/richtext.md 5節の許可を通ったもの
}

// Block はブロック1つです。Kind は "h2"・"h3"・"p"・"ul"・"ol"・"table"・"image"・"buttons"・"cards" のどれかです。
// ヒーロー（"hero"）は Blocks に入れず、Doc.Hero に置きます。
type Block struct {
	Kind    string
	Inlines []Inline   // 見出し
	Lines   [][]Inline // 段落（行ごと。間は改行）
	Items   [][]Inline // 箇条書き・番号付き
	Caption string     // 表
	Head    [][]Inline // 表の見出しの行のセル
	Rows    [][][]Inline
	Alt     string // 画像の説明
	File    string // 画像のファイル名（<id>.<ext>）。URLは描く側が決める
	Src     string // 描くURL。空なら説明の文字として描く（ResolveImages）
	Width   int
	Height  int
	Buttons []Inline // ボタンの並びとヒーローのボタン（どれもリンク）
	Cards   []Card   // カードの並び
	Heading string   // カードの題名の見出し（"h2" か "h3"）
	Grid    string   // カードの並びのクラス（"grid-2" か "grid-3"）
	Title   string   // ヒーローの見出しに描くページの題名。読む処理は入れず、描く側が入れる
}

// Card はカード1枚です。
type Card struct {
	Title []Inline
	Body  []Inline
}

// Doc は読んだ本文です。
type Doc struct {
	Blocks []Block
	Hero   *Block // ヒーロー（ParseForPages で、本文の最初の空でない行が当たった時だけ）。導入は Inlines、ボタンは Buttons
}

// Images は、本文の画像のファイル名を、重ねずに出てくる順で返します。
func (d Doc) Images() []string {
	seen := map[string]bool{}
	var out []string
	for _, b := range d.Blocks {
		if b.Kind == "image" && !seen[b.File] {
			seen[b.File] = true
			out = append(out, b.File)
		}
	}
	return out
}

// ResolveImages は、画像のURLと大きさを埋めます。resolve が ok=false を返した画像は説明の文字として描きます。
func (d Doc) ResolveImages(resolve func(file string) (src string, width, height int, ok bool)) {
	for i := range d.Blocks {
		b := &d.Blocks[i]
		if b.Kind != "image" {
			continue
		}
		if src, w, h, ok := resolve(b.File); ok {
			b.Src, b.Width, b.Height = src, w, h
		}
	}
}

// Parse は本文を構造に読みます。画像の行は読みません。エラーは上限を超えたときだけです。
func Parse(body string) (Doc, error) { return parse(body, false, false) }

// ParseWithImages は Parse に加えて、画像の行を読みます（docs/richtext.md 2節の順5.5）。
func ParseWithImages(body string) (Doc, error) { return parse(body, true, false) }

// ParseForPages は ParseWithImages に加えて、LPの部品の行（ヒーロー・ボタン・カード）を読みます（docs/richtext.md 2.2節）。
// 固定ページの保存の検査・公開の表示・プレビューが使います。
func ParseForPages(body string) (Doc, error) { return parse(body, true, true) }

func parse(body string, images, lp bool) (Doc, error) {
	raw := strings.Split(body, "\n")
	lines := make([]string, len(raw))
	for i, l := range raw {
		lines[i] = strings.TrimRightFunc(l, unicode.IsSpace)
	}
	var doc Doc
	var cur *Block
	closeBlock := func() {
		if cur == nil {
			return
		}
		switch cur.Kind {
		case "hero":
			hero := *cur
			doc.Hero = &hero
		case "cards":
			cur.Grid = "grid-2"
			if n := len(cur.Cards); n == 3 || n == 6 {
				cur.Grid = "grid-3"
			}
			doc.Blocks = append(doc.Blocks, *cur)
		default:
			doc.Blocks = append(doc.Blocks, *cur)
		}
		cur = nil
	}
	lastHeading := ""
	tables, imageCount := 0, 0
	first := true  // まだ空でない行を読んでいない（ヒーローは最初の空でない行だけ）
	sawH2 := false // 本文の ## の見出しが既にある（カードの題名の見出しの階層）
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		isFirst := first && line != ""
		if line != "" {
			first = false
		}
		kind := headingLevel(line)
		ul, ol := listItem(line, "ul"), listItem(line, "ol")
		alt, file, isImage := "", "", false
		if images {
			alt, file, isImage = imageLine(line)
		}
		var heroLead []Inline
		var button Inline
		var card Card
		isHero, isButton, isCard := false, false, false
		if lp {
			if isFirst {
				heroLead, isHero = heroLine(line)
			}
			button, isButton = buttonLine(line)
			card, isCard = cardLine(line)
		}
		switch {
		case line == "":
			closeBlock()
		case kind != "":
			closeBlock()
			inl := inlines(trimTokens(lex(line[len(kind)+1:]))) // "## " は3バイト、"### " は4バイト
			doc.Blocks = append(doc.Blocks, Block{Kind: kind, Inlines: inl})
			lastHeading = Plain(inl)
			sawH2 = sawH2 || kind == "h2"
		case strings.HasPrefix(line, "表: ") && len(line) > len("表: ") && i+2 < len(lines) && isTableStart(lines[i+1], lines[i+2]):
			closeBlock()
			caption := joinTokens(lex(strings.TrimSpace(strings.TrimPrefix(line, "表: "))))
			tables++
			b, next, err := parseTable(lines, i+1, caption, tables, lastHeading)
			if err != nil {
				return Doc{}, err
			}
			doc.Blocks = append(doc.Blocks, b)
			i = next - 1
		case i+1 < len(lines) && isTableStart(line, lines[i+1]):
			closeBlock()
			tables++
			b, next, err := parseTable(lines, i, "", tables, lastHeading)
			if err != nil {
				return Doc{}, err
			}
			doc.Blocks = append(doc.Blocks, b)
			i = next - 1
		case isImage:
			closeBlock()
			if imageCount++; imageCount > MaxImages {
				return Doc{}, LimitError{fmt.Sprintf("more than %d images", MaxImages)}
			}
			doc.Blocks = append(doc.Blocks, Block{Kind: "image", Alt: alt, File: file})
		case isHero:
			closeBlock()
			cur = &Block{Kind: "hero", Inlines: heroLead}
		case isButton:
			// ヒーローの直後か、ボタンの並びの途中なら続ける。上限を超えたら新しい並びを始める（入力を捨てない）
			if cur == nil || (cur.Kind != "hero" && cur.Kind != "buttons") || len(cur.Buttons) >= MaxButtons {
				closeBlock()
				cur = &Block{Kind: "buttons"}
			}
			cur.Buttons = append(cur.Buttons, button)
		case isCard:
			if cur == nil || cur.Kind != "cards" || len(cur.Cards) >= MaxCards {
				closeBlock()
				cur = &Block{Kind: "cards", Heading: "h2"}
				if sawH2 {
					cur.Heading = "h3"
				}
			}
			cur.Cards = append(cur.Cards, card)
		case ul != "":
			if cur == nil || cur.Kind != "ul" {
				closeBlock()
				cur = &Block{Kind: "ul"}
			}
			cur.Items = append(cur.Items, inlines(trimTokens(lex(ul))))
		case ol != "":
			if cur == nil || cur.Kind != "ol" {
				closeBlock()
				cur = &Block{Kind: "ol"}
			}
			cur.Items = append(cur.Items, inlines(trimTokens(lex(ol))))
		default:
			if cur == nil || cur.Kind != "p" {
				closeBlock()
				cur = &Block{Kind: "p"}
			}
			cur.Lines = append(cur.Lines, inlines(lex(line)))
		}
	}
	closeBlock()
	return doc, nil
}

// imageLine は、行全体が画像の記法なら説明とファイル名を返します（docs/richtext.md 2.1節）。
// 当たらない行は段落の行に任せます。読む時間は行の長さに比例します。
func imageLine(line string) (alt, file string, ok bool) {
	if !strings.HasPrefix(line, "![") {
		return "", "", false
	}
	ts := lex(line)
	if len(ts) < 2 || !ts[1].sym {
		return "", "", false
	}
	closeAlt := -1
	for i := 2; i < len(ts); i++ {
		if ts[i].sym && ts[i].r == ']' {
			closeAlt = i
			break
		}
	}
	if closeAlt < 0 || closeAlt+2 >= len(ts) || !ts[closeAlt+1].sym || ts[closeAlt+1].r != '(' {
		return "", "", false
	}
	last := len(ts) - 1
	if !ts[last].sym || ts[last].r != ')' {
		return "", "", false
	}
	url := ts[closeAlt+2 : last]
	for _, t := range url {
		if t.sym || t.escaped {
			return "", "", false
		}
	}
	path := joinTokens(url)
	if !strings.HasPrefix(path, imagePath) || !ImageFile(path[len(imagePath):]) {
		return "", "", false
	}
	alt = strings.TrimSpace(joinTokens(ts[2:closeAlt]))
	if alt == "" || utf8.RuneCountInString(alt) > MaxAltRunes {
		return "", "", false
	}
	return alt, path[len(imagePath):], true
}

// ImageLine は、説明とファイル名から画像の行を作ります。読みを変える `\` と `]` だけを逃がします。
func ImageLine(alt, file string) string {
	return "![" + strings.NewReplacer(`\`, `\\`, `]`, `\]`).Replace(alt) + "](" + imagePath + file + ")"
}

// ---- LPの部品の行（docs/richtext.md 2.2節。ParseForPages だけ） ----

const (
	heroPrefix   = "ヒーロー: "
	buttonPrefix = "ボタン: "
	cardPrefix   = "カード: "
)

// LPLine は、行が LPの部品の前置き（ヒーロー・ボタン・カード）で始まるかを返します。形の検査はしません。
func LPLine(line string) bool {
	return strings.HasPrefix(line, heroPrefix) || strings.HasPrefix(line, buttonPrefix) || strings.HasPrefix(line, cardPrefix)
}

// heroLine は、ヒーローの行の形なら導入を返します。最初の空でない行かどうかは呼ぶ側が見ます。
func heroLine(line string) ([]Inline, bool) {
	if !strings.HasPrefix(line, heroPrefix) {
		return nil, false
	}
	ts := trimTokens(lex(line[len(heroPrefix):]))
	if len(ts) == 0 {
		return nil, false
	}
	return inlines(ts), true
}

// buttonLine は、前置きの後ろが、前後の空白を除いてリンクちょうど1つなら、そのリンクを返します。
func buttonLine(line string) (Inline, bool) {
	if !strings.HasPrefix(line, buttonPrefix) {
		return Inline{}, false
	}
	inl := inlines(trimTokens(lex(line[len(buttonPrefix):])))
	if len(inl) != 1 || inl[0].Kind != "link" {
		return Inline{}, false
	}
	return inl[0], true
}

// cardLine は、前置きの後ろに記号の | がちょうど1つあり、題名と説明がどちらも空でなければ、カードを返します。
// 区切りは走査の記号の | で数え、強調・リンクを読む前に分けます（リンクの文字やURLの中の | も数える）。
func cardLine(line string) (Card, bool) {
	if !strings.HasPrefix(line, cardPrefix) {
		return Card{}, false
	}
	ts := lex(line[len(cardPrefix):])
	at := -1
	for i, t := range ts {
		if t.sym && t.r == '|' {
			if at >= 0 {
				return Card{}, false
			}
			at = i
		}
	}
	if at < 0 {
		return Card{}, false
	}
	title, body := trimTokens(ts[:at]), trimTokens(ts[at+1:])
	if len(title) == 0 || len(body) == 0 {
		return Card{}, false
	}
	return Card{Title: inlines(title), Body: inlines(body)}, true
}

// headingLevel は、見出しの行なら "h2"・"h3" を返します（docs/richtext.md 2節の順2）。
func headingLevel(line string) string {
	switch {
	case strings.HasPrefix(line, "### ") && len(line) > 4:
		return "h3"
	case strings.HasPrefix(line, "## ") && len(line) > 3:
		return "h2"
	}
	return ""
}

var orderedPrefix = regexp.MustCompile(`^[0-9]{1,9}\. `)

// listItem は、項目の行なら、記号の後ろの文字を返します（順6・7）。
func listItem(line, kind string) string {
	if kind == "ul" {
		if strings.HasPrefix(line, "- ") && len(line) > 2 {
			return line[2:]
		}
		return ""
	}
	if m := orderedPrefix.FindString(line); m != "" && len(line) > len(m) {
		return line[len(m):]
	}
	return ""
}

// ---- 記号の並び（docs/richtext.md 3.1） ----

type token struct {
	r       rune
	sym     bool // 記号の | * [ ] ( )
	escaped bool // \ で作った文字
}

const escapable = "\\*[]()|#-.:"
const symbols = "|*[]()"

// リンクだけを読む本文（docs/richtext.md 7節）の、逃がせる文字と記号。強調と表の記号を持たない。
const linkEscapable = "\\[]()"
const linkSymbols = "[]()"

// lex は、\ を解決しながら、文字と記号の並びにします。
func lex(s string) []token { return lexWith(s, escapable, symbols) }

func lexWith(s, esc, sym string) []token {
	rs := []rune(s)
	out := make([]token, 0, len(rs))
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		if r == '\\' && i+1 < len(rs) && strings.ContainsRune(esc, rs[i+1]) {
			out = append(out, token{r: rs[i+1], escaped: true})
			i++
			continue
		}
		out = append(out, token{r: r, sym: strings.ContainsRune(sym, r)})
	}
	return out
}

// LinkLine は、1行の中のリンクの記法 `[文字](URL)` だけを読みます（docs/richtext.md 7節）。
// 強調・見出し・リスト・表・画像は読みません。お知らせ・カタログの本文の行に使います。読む時間は行の長さに比例します。
func LinkLine(line string) []Inline { return inlines(lexWith(line, linkEscapable, linkSymbols)) }

// LinkParagraphs は、段落と行に分けた本文（form.Paragraphs の結果）の各行を LinkLine で読みます。
func LinkParagraphs(paragraphs [][]string) [][][]Inline {
	out := make([][][]Inline, len(paragraphs))
	for i, p := range paragraphs {
		out[i] = make([][]Inline, len(p))
		for j, line := range p {
			out[i][j] = LinkLine(line)
		}
	}
	return out
}

// LinkPlain は、本文をリンクの記法を読んだ後の文字にします（リンクは文字だけを残す）。説明文に使います。
func LinkPlain(body string) string {
	lines := strings.Split(body, "\n")
	for i, l := range lines {
		lines[i] = Plain(LinkLine(l))
	}
	return strings.Join(lines, "\n")
}

func joinTokens(ts []token) string {
	var b strings.Builder
	for _, t := range ts {
		b.WriteRune(t.r)
	}
	return b.String()
}

func trimTokens(ts []token) []token {
	for len(ts) > 0 && !ts[0].sym && unicode.IsSpace(ts[0].r) {
		ts = ts[1:]
	}
	for len(ts) > 0 && !ts[len(ts)-1].sym && unicode.IsSpace(ts[len(ts)-1].r) {
		ts = ts[:len(ts)-1]
	}
	return ts
}

// ---- 表（docs/richtext.md 3節） ----

// cells は、表の行を記号の | で分けたセル（前後の空白を除く）です。
func cells(line string) [][]token {
	ts := lex(line)
	if len(ts) > 0 && ts[0].sym && ts[0].r == '|' {
		ts = ts[1:]
	}
	if len(ts) > 0 && ts[len(ts)-1].sym && ts[len(ts)-1].r == '|' {
		ts = ts[:len(ts)-1]
	}
	var out [][]token
	start := 0
	for i, t := range ts {
		if t.sym && t.r == '|' {
			out = append(out, trimTokens(ts[start:i]))
			start = i + 1
		}
	}
	return append(out, trimTokens(ts[start:]))
}

var separatorCell = regexp.MustCompile(`^:?-{3,}:?$`)

// isTableStart は、line が表の見出しの行で、next がその区切り行かを返します（順4）。
func isTableStart(line, next string) bool {
	if !strings.HasPrefix(line, "|") || !strings.HasPrefix(next, "|") {
		return false
	}
	sep := cells(next)
	if len(sep) != len(cells(line)) {
		return false
	}
	for _, c := range sep {
		for _, t := range c {
			if t.escaped {
				return false // \ で作った文字は区切りの記号にならない
			}
		}
		if !separatorCell.MatchString(joinTokens(c)) {
			return false
		}
	}
	return true
}

// parseTable は、lines[start] から始まる表を読み、表の次の行の位置を返します。
func parseTable(lines []string, start int, caption string, n int, heading string) (Block, int, error) {
	head := cells(lines[start])
	if len(head) > MaxColumns {
		return Block{}, 0, LimitError{fmt.Sprintf("table %d has more than %d columns", n, MaxColumns)}
	}
	if caption == "" {
		caption = fmt.Sprintf("表%d", n)
		if heading != "" {
			caption += "（" + heading + "）"
		}
	}
	b := Block{Kind: "table", Caption: caption}
	for _, c := range head {
		b.Head = append(b.Head, inlines(c))
	}
	i := start + 2
	for ; i < len(lines) && strings.HasPrefix(lines[i], "|"); i++ {
		row := cells(lines[i])
		if len(row) > len(head) {
			return Block{}, 0, LimitError{fmt.Sprintf("table %d has a row with more cells than its header", n)}
		}
		if len(b.Rows) == MaxRows {
			return Block{}, 0, LimitError{fmt.Sprintf("table %d has more than %d rows", n, MaxRows)}
		}
		cellsRow := make([][]Inline, len(head))
		for j, c := range row {
			cellsRow[j] = inlines(c)
		}
		b.Rows = append(b.Rows, cellsRow)
	}
	return b, i, nil
}

// ---- 文字の並び（docs/richtext.md 4節） ----

// inlines は記号の並びを、文字・強調・リンクに読みます。探索は事前に作る表を引くだけで、時間は長さに比例します。
func inlines(ts []token) []Inline {
	n := len(ts)
	nextClose := func(r rune) []int {
		next := make([]int, n+1)
		next[n] = -1
		for i := n - 1; i >= 0; i-- {
			if ts[i].sym && ts[i].r == r {
				next[i] = i
			} else {
				next[i] = next[i+1]
			}
		}
		return next
	}
	nextBracket, nextParen := nextClose(']'), nextClose(')')
	// nextBad は、URLに使えない文字（\ で作った文字・空白・制御文字・書式文字・\）の次の位置。URLの文字種の検査を表引きにする
	nextBad := make([]int, n+1)
	nextBad[n] = n
	for i := n - 1; i >= 0; i-- {
		if r := ts[i].r; ts[i].escaped || badURLRune(r) {
			nextBad[i] = i
		} else {
			nextBad[i] = nextBad[i+1]
		}
	}
	nextStars := make([]int, n+1)
	nextStars[n] = -1
	for i := n - 1; i >= 0; i-- {
		if i+1 < n && ts[i].sym && ts[i].r == '*' && ts[i+1].sym && ts[i+1].r == '*' {
			nextStars[i] = i
		} else {
			nextStars[i] = nextStars[i+1]
		}
	}
	urlOK := map[int]string{} // 記号の ] の位置ごとのURLの判定（同じ ] を共有する [ で繰り返さない）
	var out []Inline
	var text strings.Builder
	flush := func() {
		if text.Len() > 0 {
			out = append(out, Inline{Kind: "text", Text: text.String()})
			text.Reset()
		}
	}
	for i := 0; i < n; {
		t := ts[i]
		if t.sym && t.r == '*' && i+1 < n && ts[i+1].sym && ts[i+1].r == '*' {
			if j := nextStars[min(i+2, n)]; j >= 0 && !blank(ts[i+2:j]) {
				flush()
				out = append(out, Inline{Kind: "strong", Text: joinTokens(ts[i+2 : j])})
				i = j + 2
				continue
			}
			text.WriteString("**")
			i += 2
			continue
		}
		if t.sym && t.r == '[' {
			if j := nextBracket[i+1]; j >= 0 && j+1 < n && ts[j+1].sym && ts[j+1].r == '(' {
				// 文字が空でないか（次の記号か字で止まるので安い）を先に見て、URLの検査を減らす
				if k := nextParen[j+2]; k >= 0 && k-(j+2) <= maxURL && nextBad[j+2] >= k && !blank(ts[i+1:j]) {
					href, done := urlOK[j]
					if !done {
						href = tokenURL(ts[j+2 : k])
						urlOK[j] = href
					}
					if href != "" {
						flush()
						out = append(out, Inline{Kind: "link", Text: joinTokens(ts[i+1 : j]), Href: href})
						i = k + 1
						continue
					}
				}
			}
		}
		text.WriteRune(t.r)
		i++
	}
	flush()
	return out
}

func blank(ts []token) bool {
	for _, t := range ts {
		if t.sym || !unicode.IsSpace(t.r) {
			return false
		}
	}
	return true
}

// Plain は、強調・リンクの記号を除いた文字です（表の説明に使う直前の見出し、説明文）。
func Plain(in []Inline) string {
	var b strings.Builder
	for _, x := range in {
		b.WriteString(x.Text)
	}
	return b.String()
}

// ---- リンクのURL（docs/richtext.md 5節） ----

// tokenURL は、URLの文字種の検査（nextBad）を通った並びを検査します。サイト内のパスは先頭の2文字だけで決まるため、
// 組み立てるのは許可するときと、https:// の外部のときだけにする（多数の [ が同じ ) を共有しても速い）。
func tokenURL(ts []token) string {
	switch {
	case len(ts) == 0:
		return ""
	case ts[0].r == '/' && (len(ts) == 1 || ts[1].r != '/'):
		return joinTokens(ts)
	case len(ts) > len("https://") && joinTokens(ts[:len("https://")]) == "https://":
		return AllowedURL(joinTokens(ts))
	}
	return ""
}

// badURLRune は、URLに使えない文字（空白・\・制御文字・書式文字）かを返します。
func badURLRune(r rune) bool {
	return unicode.IsSpace(r) || r == '\\' || unicode.IsControl(r) || unicode.Is(unicode.Cf, r)
}

// AllowedURL は、リンクに使ってよいURL（小文字の https:// の外部か、/ で始まるサイト内のパス）ならそのまま、そうでなければ空を返します。
func AllowedURL(raw string) string {
	if raw == "" || utf8.RuneCountInString(raw) > maxURL {
		return ""
	}
	for _, r := range raw {
		if badURLRune(r) {
			return ""
		}
	}
	switch {
	case strings.HasPrefix(raw, "https://"):
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "https" || u.User != nil || u.Hostname() == "" || strings.Contains(u.Host, "%") {
			return ""
		}
		return raw
	case strings.HasPrefix(raw, "/") && !strings.HasPrefix(raw, "//"):
		return raw
	}
	return ""
}
