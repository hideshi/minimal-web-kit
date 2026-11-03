package convert

import (
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"minimal-web-kit/internal/web/richtext"
)

// 表の上限（richtext と同じ）。超える表は文字の段落にして報告する。
const (
	maxColumns = richtext.MaxColumns
	maxRows    = richtext.MaxRows
)

// allowedHref は、URLをキットのリンクに使える形に整えます（計画13.2の順: 書き換えの後に、解析・小文字化・符号化・許可の判定）。
func allowedHref(href string) (string, bool) {
	href = strings.TrimSpace(href)
	if href == "" {
		return "", false
	}
	u, err := url.Parse(href)
	if err != nil {
		return "", false
	}
	switch {
	case strings.EqualFold(u.Scheme, "https") && u.Host != "":
		u.Scheme = "https"
		u.Host = strings.ToLower(u.Host)
	case u.Scheme == "" && u.Host == "" && strings.HasPrefix(u.Path, "/"):
	default:
		return "", false
	}
	out := u.String()
	// URLの中の ( ) | は、記法の区切りと表の列にならないよう符号化する
	out = strings.NewReplacer("(", "%28", ")", "%29", "|", "%7C").Replace(out)
	if richtext.AllowedURL(out) != out {
		return "", false
	}
	return out, true
}

// render はブロックの並びを、出力先の記法の本文にします。
func render(blocks []block, target Target, r *Result) string {
	var parts []string
	for _, b := range blocks {
		if target == News {
			parts = append(parts, renderNews(b, r)...)
		} else {
			parts = append(parts, renderPage(b, r))
		}
	}
	var out []string
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, "\n\n")
}

// ---- 固定ページ（richtext） ----

func renderPage(b block, r *Result) string {
	switch b.kind {
	case "h2", "h3":
		marker := "## "
		if b.kind == "h3" {
			marker = "### "
		}
		var line []inline
		for _, l := range b.lines {
			if len(line) > 0 {
				line = append(line, inline{kind: "text", text: " "})
			}
			line = append(line, l...)
		}
		return marker + pageInlines(line, false)
	case "ul", "ol":
		var lines []string
		for i, item := range b.items {
			marker := "- "
			if b.kind == "ol" {
				marker = strconv.Itoa(i+1) + ". "
			}
			lines = append(lines, marker+pageInlines(item, false))
		}
		return strings.Join(lines, "\n")
	case "table":
		return renderTable(b, r)
	case "image":
		return richtext.ImageLine(imageAlt(b.image), b.image.File)
	}
	var lines []string
	for _, l := range b.lines {
		lines = append(lines, escapeLineStart(pageInlines(l, false)))
	}
	return strings.Join(lines, "\n")
}

// imageAlt は、画像の説明です。Alt は Resolver が返した説明（WXRの代替テキスト等）か本文の alt、無ければキャプション、仮の文の順。
func imageAlt(img Image) string {
	alt := img.Alt
	if alt == "" {
		alt = img.Caption
	}
	if alt == "" {
		alt = PlaceholderAlt
	}
	if r := []rune(alt); len(r) > richtext.MaxAltRunes {
		alt = string(r[:richtext.MaxAltRunes])
	}
	return alt
}

// PlaceholderAlt は、説明が見つからない画像の仮の説明です。報告に一覧し、人が書き直します。
const PlaceholderAlt = "画像の説明を書いてください"

var (
	reHeadingStart = regexp.MustCompile(`^#`)
	reListStart    = regexp.MustCompile(`^(-|[0-9]{1,9}\.) `)
	reNumberStart  = regexp.MustCompile(`^([0-9]{1,9})\. `)
)

// escapeLineStart は、段落の行の頭が、見出し・リスト・表・表の説明・画像の行に読まれないよう逃がします（意味を持つ場所だけ。計画13.5）。
func escapeLineStart(line string) string {
	switch {
	case reHeadingStart.MatchString(line):
		return `\` + line
	case strings.HasPrefix(line, "- "):
		return `\` + line
	case reNumberStart.MatchString(line):
		m := reNumberStart.FindStringSubmatch(line)
		return m[1] + `\.` + line[len(m[1])+1:]
	case strings.HasPrefix(line, "|"):
		return `\` + line
	case strings.HasPrefix(line, "表: "):
		return "表\\:" + line[len("表:"):]
	}
	return line
}

// pageInlines は、文字の並びを richtext の書き方にします。cell なら | も逃がします。
func pageInlines(in []inline, cell bool) string {
	var b strings.Builder
	// 強調のある並びでは、文字の * を全部逃がす（強調の印とつながって *** にならないように）
	strong := false
	for _, x := range in {
		strong = strong || x.kind == "strong"
	}
	for _, x := range in {
		switch x.kind {
		case "link":
			b.WriteString("[" + escapePageText(x.text, cell, true) + "](" + x.href + ")")
		case "strong":
			t := strings.TrimSpace(x.text)
			if t == "" {
				b.WriteString(x.text)
				continue
			}
			lead := x.text[:strings.Index(x.text, t)]
			b.WriteString(escapePageText(lead, cell, true) + "**" + escapePageText(t, cell, true) + "**" + escapePageText(x.text[len(lead)+len(t):], cell, true))
		default:
			b.WriteString(escapePageText(x.text, cell, strong))
		}
	}
	return b.String()
}

// escapePageText は、文字の中の記法の記号を逃がします。`\` と `[`、`**`（強調の印になる並び）、`]`（リンク・強調の中）、表のセルの `|`。
func escapePageText(s string, cell, inner bool) string {
	var b strings.Builder
	rs := []rune(s)
	for i, r := range rs {
		switch {
		case r == '\\' || r == '[':
			b.WriteRune('\\')
		case r == ']' && inner:
			b.WriteRune('\\')
		case r == '*' && ((i+1 < len(rs) && rs[i+1] == '*') || (i > 0 && rs[i-1] == '*') || inner):
			b.WriteRune('\\')
		case r == '|' && cell:
			b.WriteRune('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

func renderTable(b block, r *Result) string {
	cols := 0
	for _, row := range b.rows {
		cols = max(cols, len(row))
	}
	if cols > maxColumns || len(b.rows)-1 > maxRows {
		r.note("table_too_large")
		var lines []string
		for _, row := range b.rows {
			var cells []string
			for _, c := range row {
				cells = append(cells, pageInlines(c, false))
			}
			lines = append(lines, escapeLineStart(strings.Join(cells, " / ")))
		}
		return strings.Join(lines, "\n")
	}
	var lines []string
	if caption := strings.TrimSpace(b.caption); caption != "" {
		lines = append(lines, "表: "+escapePageText(caption, false, false))
	}
	for i, row := range b.rows {
		cells := make([]string, cols)
		for j := range cells {
			if j < len(row) {
				cells[j] = pageInlines(row[j], true)
			}
		}
		lines = append(lines, "| "+strings.Join(cells, " | ")+" |")
		if i == 0 {
			seps := make([]string, cols)
			for j := range seps {
				seps[j] = "---"
			}
			lines = append(lines, "|"+strings.Join(seps, "|")+"|")
		}
	}
	if len(b.rows) == 1 {
		r.note("table_header_only")
	}
	return strings.Join(lines, "\n")
}

// ---- お知らせ（行の中のリンクだけ） ----

func renderNews(b block, r *Result) []string {
	switch b.kind {
	case "ul", "ol":
		var lines []string
		for i, item := range b.items {
			marker := "・"
			if b.kind == "ol" {
				marker = strconv.Itoa(i+1) + ". "
			}
			lines = append(lines, marker+newsInlines(item))
		}
		return []string{strings.Join(lines, "\n")}
	case "table":
		var lines []string
		for _, row := range b.rows {
			var cells []string
			for _, c := range row {
				cells = append(cells, newsInlines(c))
			}
			lines = append(lines, strings.Join(cells, " / "))
		}
		if b.caption != "" {
			lines = append([]string{escapeNewsText(b.caption, false)}, lines...)
		}
		r.note("table_as_text")
		return []string{strings.Join(lines, "\n")}
	case "image":
		return nil
	}
	var lines []string
	for _, l := range b.lines {
		lines = append(lines, newsInlines(l))
	}
	return []string{strings.Join(lines, "\n")}
}

func newsInlines(in []inline) string {
	var b strings.Builder
	for _, x := range in {
		if x.kind == "link" {
			b.WriteString("[" + escapeNewsText(x.text, true) + "](" + x.href + ")")
			continue
		}
		b.WriteString(escapeNewsText(x.text, false))
	}
	return b.String()
}

// escapeNewsText は、お知らせの本文で読まれる記号だけを逃がします（`\` と `[`、リンクの文字の中の `]`。計画13.5）。
func escapeNewsText(s string, inner bool) string {
	var b strings.Builder
	for _, r := range s {
		if r == '\\' || r == '[' || (r == ']' && inner) {
			b.WriteRune('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}
