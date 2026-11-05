package test

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode"
)

// テーマ（internal/web/static/css/theme.css）は、サイトが任意で置き、style.css の :root の色と書体の変数だけを上書きする。
// 完全なCSSの読み取りは作らず、許す形を狭く決めて、それ以外は全部落とす（docs/plans/theme-plan.md 7.5）。

const themePath = "../internal/web/static/css/theme.css"

var (
	themeDeclPattern  = regexp.MustCompile(`^(--[a-z0-9-]+)\s*:\s*(.+)$`)
	themeColorPattern = regexp.MustCompile(`^#(?:[0-9a-fA-F]{3}|[0-9a-fA-F]{6})$`)
	themeIdentPattern = regexp.MustCompile(`^-?[A-Za-z][A-Za-z0-9-]*$`)
)

// themeFontVar は、色のほかに上書きを許す唯一の変数です。
const themeFontVar = "--font-base"

// styleRootVars は、style.css の先頭の :root の変数と値です。
func styleRootVars(t *testing.T) map[string]string {
	t.Helper()
	b, err := os.ReadFile("../internal/web/static/css/style.css")
	if err != nil {
		t.Fatal(err)
	}
	css := cssCommentPattern.ReplaceAllString(string(b), "")
	m := cssRootPattern.FindStringSubmatch(css)
	if m == nil {
		t.Fatal("style.css must start with a :root block")
	}
	vars := map[string]string{}
	for _, d := range strings.Split(m[1], ";") {
		if d = strings.TrimSpace(d); d == "" {
			continue
		}
		name, value, ok := strings.Cut(d, ":")
		if !ok {
			t.Fatalf("style.css :root: bad declaration %q", d)
		}
		vars[strings.TrimSpace(name)] = strings.TrimSpace(value)
	}
	return vars
}

// stripThemeComments は、文字列の外のコメントを除きます。文字列の中に許すのは文字・数字・空白・「-」だけなので、
// 文字列の中の「/*」でコメントの範囲がずれることはありません。閉じていないコメントと文字列はエラーにします。
func stripThemeComments(src string) (string, error) {
	var out strings.Builder
	for i := 0; i < len(src); {
		switch {
		case strings.HasPrefix(src[i:], "/*"):
			end := strings.Index(src[i+2:], "*/")
			if end < 0 {
				return "", errors.New("unclosed comment")
			}
			i += 2 + end + 2
			out.WriteByte(' ')
		case src[i] == '"' || src[i] == '\'':
			q := src[i]
			end := strings.IndexByte(src[i+1:], q)
			if end < 0 {
				return "", errors.New("unclosed string")
			}
			s := src[i+1 : i+1+end]
			for _, r := range s {
				if !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == ' ' || r == '-') {
					return "", fmt.Errorf("a string may contain only letters, digits, spaces and '-': %q", s)
				}
			}
			out.WriteString(src[i : i+1+end+1])
			i += 1 + end + 1
		default:
			out.WriteByte(src[i])
			i++
		}
	}
	return out.String(), nil
}

// parseTheme は、テーマを読み、上書きする変数と値を返します。allowed は style.css の :root の変数です。
func parseTheme(src string, allowed map[string]string) (map[string]string, error) {
	if strings.HasPrefix(src, "\xef\xbb\xbf") {
		return nil, errors.New("BOM is not allowed")
	}
	src = strings.ReplaceAll(src, "\r\n", "\n")
	for _, c := range []string{"\r", "\\", "@", "&", "!", "<"} {
		if strings.Contains(src, c) {
			return nil, fmt.Errorf("%q is not allowed", c)
		}
	}
	css, err := stripThemeComments(src)
	if err != nil {
		return nil, err
	}
	css = strings.TrimSpace(css)
	body, ok := strings.CutPrefix(css, ":root")
	if !ok {
		return nil, errors.New("the theme must be a single :root { ... } rule")
	}
	body = strings.TrimSpace(body)
	if !strings.HasPrefix(body, "{") || !strings.HasSuffix(body, "}") {
		return nil, errors.New("the theme must be a single :root { ... } rule")
	}
	body = strings.TrimSpace(body[1 : len(body)-1])
	if strings.ContainsAny(body, "{}") {
		return nil, errors.New("nested blocks and other rules are not allowed")
	}
	if body != "" && !strings.HasSuffix(body, ";") {
		return nil, errors.New("every declaration must end with ';'")
	}
	vars := map[string]string{}
	for _, d := range strings.Split(strings.TrimSuffix(body, ";"), ";") {
		d = strings.TrimSpace(d)
		m := themeDeclPattern.FindStringSubmatch(d)
		if m == nil {
			return nil, fmt.Errorf("only custom properties are allowed: %q", d)
		}
		name, value := m[1], strings.TrimSpace(m[2])
		if _, ok := allowed[name]; !ok || !(strings.HasPrefix(name, "--color-") || name == themeFontVar) {
			return nil, fmt.Errorf("%s cannot be overridden; only the --color-* and %s variables in style.css", name, themeFontVar)
		}
		if _, dup := vars[name]; dup {
			return nil, fmt.Errorf("%s is declared twice", name)
		}
		if err := checkThemeValue(name, value); err != nil {
			return nil, err
		}
		vars[name] = value
	}
	if len(vars) == 0 {
		return nil, errors.New("the theme overrides nothing")
	}
	return vars, nil
}

func checkThemeValue(name, value string) error {
	if name != themeFontVar {
		if !themeColorPattern.MatchString(value) {
			return fmt.Errorf("%s must be #rgb or #rrggbb: %q", name, value)
		}
		return nil
	}
	for _, f := range strings.Split(value, ",") {
		f = strings.TrimSpace(f)
		quoted := len(f) >= 3 && (f[0] == '"' || f[0] == '\'') && f[len(f)-1] == f[0] && strings.TrimSpace(f[1:len(f)-1]) != ""
		if !quoted && !themeIdentPattern.MatchString(f) {
			return fmt.Errorf("%s must be a comma-separated list of font names: %q", name, f)
		}
	}
	return nil
}

// checkThemeColors は、色を1つでも上書きするなら全部を上書きしていること（全部か無しか）を確かめます。
// キットが色の変数を足したとき、新しい変数だけ既定の色のまま混ざるのを、取り込んだ後のテストで気づくためです。
func checkThemeColors(theme, style map[string]string) error {
	var colors, missing []string
	for name := range style {
		if strings.HasPrefix(name, "--color-") {
			colors = append(colors, name)
		}
	}
	slices.Sort(colors)
	overrides := 0
	for _, name := range colors {
		if _, ok := theme[name]; ok {
			overrides++
		} else {
			missing = append(missing, name)
		}
	}
	if overrides > 0 && len(missing) > 0 {
		return fmt.Errorf("a theme that changes colors must set every color variable; missing: %s", strings.Join(missing, ", "))
	}
	return nil
}

type rgb struct{ r, g, b float64 }

func parseHex(s string) (rgb, error) {
	s = strings.TrimPrefix(s, "#")
	if len(s) == 3 {
		s = string([]byte{s[0], s[0], s[1], s[1], s[2], s[2]})
	}
	v, err := strconv.ParseUint(s, 16, 32)
	if err != nil || len(s) != 6 {
		return rgb{}, fmt.Errorf("not a hex color: %q", s)
	}
	return rgb{float64(v >> 16 & 0xff), float64(v >> 8 & 0xff), float64(v & 0xff)}, nil
}

func (c rgb) luminance() float64 {
	ch := func(v float64) float64 {
		v /= 255
		if v <= 0.04045 {
			return v / 12.92
		}
		return math.Pow((v+0.055)/1.055, 2.4)
	}
	return 0.2126*ch(c.r) + 0.7152*ch(c.g) + 0.0722*ch(c.b)
}

func contrast(a, b rgb) float64 {
	la, lb := a.luminance(), b.luminance()
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

// blend は、前景 fg を不透明度 alpha で背景 bg に重ねた色です。
func blend(fg, bg rgb, alpha float64) rgb {
	return rgb{fg.r*alpha + bg.r*(1-alpha), fg.g*alpha + bg.g*(1-alpha), fg.b*alpha + bg.b*(1-alpha)}
}

// contrastPair は、実際に重ねて描く色の組です（docs/plans/theme-plan.md 7.6）。alpha は前景の不透明度（0なら1）。
type contrastPair struct {
	fg, bg []string
	min    float64
	alpha  float64
	where  string
}

var contrastPairs = []contrastPair{
	{fg: []string{"--color-text", "--color-text-sub", "--color-text-muted"}, bg: []string{"--color-bg", "--color-surface"}, min: 4.5, where: "本文と補足の文字"},
	{fg: []string{"--color-primary", "--color-primary-hover"}, bg: []string{"--color-bg", "--color-surface", "--color-hover-surface", "--color-warning-surface"}, min: 4.5, where: "リンク・ナビ・注意書きの中のリンク"},
	{fg: []string{"--color-on-primary"}, bg: []string{"--color-primary", "--color-primary-hover"}, min: 4.5, where: "主ボタン・バッジ・カードの矢印・選択中の候補"},
	{fg: []string{"--color-on-primary"}, bg: []string{"--color-primary", "--color-primary-hover"}, min: 4.5, alpha: 0.95, where: "主ボタンの補足の文字（.btn-sub-text）"},
	{fg: []string{"--color-on-danger"}, bg: []string{"--color-danger", "--color-danger-hover"}, min: 4.5, where: "削除ボタン"},
	{fg: []string{"--color-danger-hover"}, bg: []string{"--color-bg", "--color-surface"}, min: 4.5, where: "エラーの文字と入力欄の枠"},
	{fg: []string{"--color-text"}, bg: []string{"--color-secondary-hover-surface", "--color-warning-surface"}, min: 4.5, where: "副ボタン・注意書き"},
	{fg: []string{"--color-border-input"}, bg: []string{"--color-bg", "--color-surface"}, min: 3, where: "入力欄の枠"},
	{fg: []string{"--color-focus-inner"}, bg: []string{"--color-bg", "--color-surface"}, min: 3, where: "フォーカスの枠"},
	{fg: []string{"--color-focus-outer"}, bg: []string{"--color-focus-inner", "--color-primary"}, min: 3, where: "二重のフォーカスの枠・選択中の候補の枠"},
}

// checkContrast は、style.css の既定の値に theme を重ねた色で、contrastPairs を確かめます。
func checkContrast(style, theme map[string]string) []error {
	color := func(name string) (rgb, error) {
		v, ok := theme[name]
		if !ok {
			v = style[name]
		}
		return parseHex(v)
	}
	var errs []error
	for _, p := range contrastPairs {
		for _, f := range p.fg {
			for _, b := range p.bg {
				fg, err1 := color(f)
				bg, err2 := color(b)
				if err := errors.Join(err1, err2); err != nil {
					errs = append(errs, fmt.Errorf("%s on %s: %w", f, b, err))
					continue
				}
				if p.alpha > 0 {
					fg = blend(fg, bg, p.alpha)
				}
				if r := contrast(fg, bg); r < p.min {
					errs = append(errs, fmt.Errorf("%s on %s is %.2f:1, needs %.1f:1 (%s)", f, b, r, p.min, p.where))
				}
			}
		}
	}
	return errs
}

func checkThemeSource(t *testing.T, name, src string, style map[string]string) {
	t.Helper()
	theme, err := parseTheme(src, style)
	if err != nil {
		t.Errorf("%s: %v", name, err)
		return
	}
	if err := checkThemeColors(theme, style); err != nil {
		t.Errorf("%s: %v", name, err)
	}
	for _, err := range checkContrast(style, theme) {
		t.Errorf("%s: %v", name, err)
	}
}

// TestTheme_Contrast は、既定の配色と、見本のテーマと、サイトが置いたテーマが、色の組の基準を満たすことを検証します。
// 基準は文字が4.5:1、文字でない部品の見分けが3:1（WCAG 2.1 の 1.4.3・1.4.11）。この組を満たしても、AA全体の保証ではありません。
func TestTheme_Contrast(t *testing.T) {
	style := styleRootVars(t)
	for name, v := range style {
		if strings.HasPrefix(name, "--color-") && !themeColorPattern.MatchString(v) {
			t.Errorf("style.css: %s must be #rgb or #rrggbb so that the contrast can be checked: %q", name, v)
		}
	}
	for _, err := range checkContrast(style, nil) {
		t.Errorf("style.css: %v", err)
	}
	samples, _ := filepath.Glob("testdata/themes/*.css")
	if len(samples) == 0 {
		t.Fatal("testdata/themes must have a sample theme")
	}
	if b, err := os.ReadFile(themePath); err == nil {
		checkThemeSource(t, "theme.css", string(b), style)
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
	for _, f := range samples {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		checkThemeSource(t, f, string(b), style)
	}
}

// TestTheme_RejectsUnsafeForms は、テーマの読み取りが、変数の上書き以外の書き方を通さないことを検証します。
func TestTheme_RejectsUnsafeForms(t *testing.T) {
	style := styleRootVars(t)
	ok := ":root {\n  /* ブランドの色 */\n  --color-primary: #7a1f3d;\n  --font-base: \"BIZ UDPGothic\", 'Noto Sans JP', sans-serif;\n}\n"
	if _, err := parseTheme(ok, style); err != nil {
		t.Fatalf("a valid theme must pass: %v", err)
	}
	if _, err := parseTheme(strings.ReplaceAll(ok, "\n", "\r\n"), style); err != nil {
		t.Fatalf("CRLF must pass: %v", err)
	}
	for name, src := range map[string]string{
		"BOM":                      "\xef\xbb\xbf" + ok,
		"other rule":               ":root { --color-primary: #7a1f3d; }\nbody { color: #000; }",
		"selector list":            ":root, body { --color-primary: #7a1f3d; }",
		"upper-case root":          ":ROOT { --color-primary: #7a1f3d; }",
		"nested at-rule":           ":root { @media (min-width: 1px) { --color-primary: #7a1f3d; } }",
		"nested selector":          ":root { & a { color: #7a1f3d; } }",
		"ordinary property":        ":root { color: #7a1f3d; }",
		"spacing variable":         ":root { --space-md: 2rem; }",
		"unknown variable":         ":root { --color-brand: #7a1f3d; }",
		"duplicate":                ":root { --color-primary: #7a1f3d; --color-primary: #000; }",
		"missing semicolon":        ":root { --color-primary: #7a1f3d }",
		"important":                ":root { --color-primary: #7a1f3d !important; }",
		"escape":                   ":root { --font-base: u\\72 l(x); }",
		"url":                      ":root { --font-base: url(/x.woff2); }",
		"var":                      ":root { --color-primary: var(--color-text); }",
		"named color":              ":root { --color-primary: red; }",
		"rgb":                      ":root { --color-primary: rgb(1 2 3); }",
		"8-digit hex":              ":root { --color-primary: #7a1f3dff; }",
		"comment in string":        ":root { --font-base: \"a/*\"; } body { background: #000 } /*\"; }",
		"string with semicolon":    ":root { --font-base: \"a;b\"; }",
		"slash in string":          ":root { --font-base: \"a/*b\"; }",
		"quote in string":          ":root { --font-base: \"a'b\"; }",
		"unclosed comment":         ":root { --color-primary: #7a1f3d; } /*",
		"unclosed string":          ":root { --font-base: \"abc; }",
		"empty":                    ":root { }",
		"braces in value":          ":root { --font-base: { a }; }",
		"html":                     ":root { --font-base: a; } </style>",
		"font with slash":          ":root { --font-base: a/b; }",
		"font with empty name":     ":root { --font-base: a, , b; }",
		"font with function":       ":root { --font-base: local(a); }",
		"lone carriage return":     ":root {\r--color-primary: #7a1f3d;\r}",
		"text after the rule":      ":root { --color-primary: #7a1f3d; } x",
		"bare declarations":        "--color-primary: #7a1f3d;",
		"colors without the block": ":root --color-primary: #7a1f3d;",
	} {
		if _, err := parseTheme(src, style); err == nil {
			t.Errorf("%s: must be rejected: %q", name, src)
		}
	}
}

// TestTheme_ColorsAllOrNothing は、色を一部だけ上書きしたテーマと、基準を下回るテーマが落ちることを検証します。
func TestTheme_ColorsAllOrNothing(t *testing.T) {
	style := styleRootVars(t)
	if err := checkThemeColors(map[string]string{themeFontVar: "serif"}, style); err != nil {
		t.Fatalf("a font-only theme must pass: %v", err)
	}
	if err := checkThemeColors(map[string]string{"--color-primary": "#7a1f3d"}, style); err == nil {
		t.Fatal("a theme that changes only some colors must fail")
	}
	all := map[string]string{}
	for name, v := range style {
		if strings.HasPrefix(name, "--color-") {
			all[name] = v
		}
	}
	if err := checkThemeColors(all, style); err != nil {
		t.Fatal(err)
	}
	all["--color-primary"] = "#7f9fd0" // 白の上で約2.6:1
	if errs := checkContrast(style, all); len(errs) == 0 {
		t.Fatal("a light primary color must fail the contrast check")
	}
	// 主色の上で4.5:1をわずかに超える文字は、0.95の不透明度で重ねた補足の文字で下回る
	if r := contrast(blend(rgb{255, 255, 255}, rgb{0x2b, 0x6c, 0xb0}, 0.95), rgb{0x2b, 0x6c, 0xb0}); r >= contrast(rgb{255, 255, 255}, rgb{0x2b, 0x6c, 0xb0}) {
		t.Fatalf("blending must lower the contrast: %.2f", r)
	}
}
