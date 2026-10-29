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
)

const (
	MaxColumns = 20
	MaxRows    = 200
	maxURL     = 2048
	maxDigits  = 9
)

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

// Block はブロック1つです。Kind は "h2"・"h3"・"p"・"ul"・"ol"・"table" のどれかです。
type Block struct {
	Kind    string
	Inlines []Inline   // 見出し
	Lines   [][]Inline // 段落（行ごと。間は改行）
	Items   [][]Inline // 箇条書き・番号付き
	Caption string     // 表
	Head    [][]Inline // 表の見出しの行のセル
	Rows    [][][]Inline
}

// Doc は読んだ本文です。
type Doc struct {
	Blocks []Block
}

// Parse は本文を構造に読みます。エラーは表の上限を超えたときだけです。
func Parse(body string) (Doc, error) {
	raw := strings.Split(body, "\n")
	lines := make([]string, len(raw))
	for i, l := range raw {
		lines[i] = strings.TrimRightFunc(l, unicode.IsSpace)
	}
	var doc Doc
	var cur *Block
	closeBlock := func() {
		if cur != nil {
			doc.Blocks = append(doc.Blocks, *cur)
			cur = nil
		}
	}
	lastHeading := ""
	tables := 0
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		switch {
		case line == "":
			closeBlock()
		case headingLevel(line) != "":
			closeBlock()
			kind := headingLevel(line)
			prefix := map[string]int{"h2": len("## "), "h3": len("### ")}[kind]
			inl := inlines(lex(line[prefix:]))
			doc.Blocks = append(doc.Blocks, Block{Kind: kind, Inlines: inl})
			lastHeading = plain(inl)
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
		case listItem(line, "ul") != "":
			if cur == nil || cur.Kind != "ul" {
				closeBlock()
				cur = &Block{Kind: "ul"}
			}
			cur.Items = append(cur.Items, inlines(lex(listItem(line, "ul"))))
		case listItem(line, "ol") != "":
			if cur == nil || cur.Kind != "ol" {
				closeBlock()
				cur = &Block{Kind: "ol"}
			}
			cur.Items = append(cur.Items, inlines(lex(listItem(line, "ol"))))
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

// lex は、\ を解決しながら、文字と記号の並びにします。
func lex(s string) []token {
	rs := []rune(s)
	out := make([]token, 0, len(rs))
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		if r == '\\' && i+1 < len(rs) && strings.ContainsRune(escapable, rs[i+1]) {
			out = append(out, token{r: rs[i+1], escaped: true})
			i++
			continue
		}
		out = append(out, token{r: r, sym: strings.ContainsRune(symbols, r)})
	}
	return out
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
	nextStars := make([]int, n+2)
	nextStars[n], nextStars[n+1] = -1, -1
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
			if j := nextStars[i+2]; j >= 0 && !blank(ts[i+2:j]) {
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
				if k := nextParen[j+2]; k >= 0 && k-(j+2) <= maxURL {
					href, done := urlOK[j]
					if !done {
						href = linkURL(ts[j+2 : k])
						urlOK[j] = href
					}
					if href != "" && !blank(ts[i+1:j]) {
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

// plain は、強調・リンクの記号を除いた文字です（表の説明に使う直前の見出し）。
func plain(in []Inline) string {
	var b strings.Builder
	for _, x := range in {
		b.WriteString(x.Text)
	}
	return b.String()
}

// ---- リンクのURL（docs/richtext.md 5節） ----

// linkURL は、許可するURLならその文字を、許可しないなら空を返します。
func linkURL(ts []token) string {
	for _, t := range ts {
		if t.escaped {
			return ""
		}
	}
	return AllowedURL(joinTokens(ts))
}

// AllowedURL は、リンクに使ってよいURL（小文字の https:// の外部か、/ で始まるサイト内のパス）ならそのまま、そうでなければ空を返します。
func AllowedURL(raw string) string {
	if raw == "" || len([]rune(raw)) > maxURL {
		return ""
	}
	for _, r := range raw {
		if unicode.IsSpace(r) || r == '\\' || unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
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
