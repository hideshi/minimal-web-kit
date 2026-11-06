// Package ogimage は、SNS等で共有した時のカードの画像（OGPの画像）を、題名から作ります。
// 保存はしません。同じ入力からは同じバイト列を作るので、URLに版（Version）を入れて長くキャッシュできます。
// 仕様は docs/plans/lp-plan.md の4節。フォントの記録は font/FONT.md。
package ogimage

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
)

//go:embed font/NotoSansJP-Bold-JIS.otf
var fontData []byte

// License は、組み込んだフォントのライセンス（SIL Open Font License 1.1）の全文です。
//
//go:embed font/LICENSE
var License string

// Copyright は、組み込んだフォントの著作権表示です。
const Copyright = "Noto Sans JP Bold (subset): © 2014-2021 Adobe (http://www.adobe.com/)."

const (
	Width  = 1200
	Height = 630
	// RenderVersion は描き方の版です。描く結果のPNGが変わる変更（色・配置・折り返し・依存の更新）では1つ上げます。
	// 試験が決まった入力のPNGのハッシュを持つので、上げ忘れると落ちます。
	RenderVersion = 1

	maxLines   = 3
	lineWidth  = 1040
	titleSize  = 64
	lineHeight = 92
	hostSize   = 32
	marginX    = 80
)

var (
	colorBackground = color.RGBA{0xf8, 0xf9, 0xfa, 0xff} // style.css の --color-surface
	colorBar        = color.RGBA{0x00, 0x40, 0x98, 0xff} // --color-primary
	colorTitle      = color.RGBA{0x1a, 0x1a, 0x1a, 0xff} // --color-text
	colorHost       = color.RGBA{0x4a, 0x55, 0x68, 0xff} // --color-text-muted
)

var (
	loadOnce sync.Once
	parsed   *opentype.Font
	loadErr  error
	fontHash string
)

func load() error {
	loadOnce.Do(func() {
		sum := sha256.Sum256(fontData)
		fontHash = hex.EncodeToString(sum[:])
		parsed, loadErr = opentype.Parse(fontData)
	})
	return loadErr
}

// ErrNotDrawable は、フォントに無い文字があって描けないことを示します。
var ErrNotDrawable = errors.New("ogimage: text has characters outside the font")

// Drawable は、題名とホスト名の文字が全部フォントにあり、結合文字・書式文字・制御文字を含まないかを返します。
// 描けない時は画像を作らず、呼ぶ側はサイト共通の画像を使います（豆腐を出さない）。
func Drawable(title, host string) bool {
	if load() != nil || strings.TrimSpace(title) == "" {
		return false
	}
	var buf sfnt.Buffer
	for _, r := range title + host + "…" {
		if unicode.In(r, unicode.Mn, unicode.Me, unicode.Cf, unicode.Cc) {
			return false
		}
		if i, err := parsed.GlyphIndex(&buf, r); err != nil || i == 0 {
			return false
		}
	}
	return true
}

// Version は、画像のURLに入れる版です。印・描き方の版・フォントのハッシュ・題名・ホスト名を、
// それぞれ長さ（バイト数）を前に付けてつないだものの SHA-256 の、小文字16進の先頭12文字です。
func Version(kind, title, host string) string {
	_ = load()
	h := sha256.New()
	for _, f := range []string{kind, strconv.Itoa(RenderVersion), fontHash, title, host} {
		fmt.Fprintf(h, "%d:%s", len(f), f)
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
}

// Render は、題名とホスト名から PNG を作ります。描けない文字があれば ErrNotDrawable を返します。
func Render(title, host string) ([]byte, error) {
	if !Drawable(title, host) {
		return nil, ErrNotDrawable
	}
	titleFace, err := opentype.NewFace(parsed, &opentype.FaceOptions{Size: titleSize, DPI: 72, Hinting: font.HintingNone})
	if err != nil {
		return nil, err
	}
	defer titleFace.Close()
	hostFace, err := opentype.NewFace(parsed, &opentype.FaceOptions{Size: hostSize, DPI: 72, Hinting: font.HintingNone})
	if err != nil {
		return nil, err
	}
	defer hostFace.Close()

	img := image.NewRGBA(image.Rect(0, 0, Width, Height))
	draw.Draw(img, img.Bounds(), &image.Uniform{colorBackground}, image.Point{}, draw.Src)
	draw.Draw(img, image.Rect(0, 0, Width, 16), &image.Uniform{colorBar}, image.Point{}, draw.Src)

	lines := Lines(titleFace, title)
	d := &font.Drawer{Dst: img, Src: image.NewUniform(colorTitle), Face: titleFace}
	// 題名の行のまとまりを、帯とホスト名の間の中ほどに置く
	baseline := 280 - (len(lines)-1)*lineHeight/2 + titleSize*35/100
	for i, line := range lines {
		d.Dot = fixed.P(marginX, baseline+i*lineHeight)
		d.DrawString(line)
	}
	h := &font.Drawer{Dst: img, Src: image.NewUniform(colorHost), Face: hostFace, Dot: fixed.P(marginX, Height-70)}
	h.DrawString(host)

	var out bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.BestSpeed}).Encode(&out, img); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// ---- 折り返し（docs/plans/lp-plan.md 4.3節） ----

// 行頭に来てはいけない文字と、行末に来てはいけない文字。
const (
	headNG = "、。，．,.）)」』】〕］]｝}〉》！!？?：:；;ー・ぁぃぅぇぉっゃゅょゎァィゥェォッャュョヮヵヶ々〜～…‥"
	tailNG = "（(「『【〔［[｛{〈《"
)

// isWord は、途中で切らない英数字のまとまりの文字です。
func isWord(r rune) bool {
	return r < 0x80 && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.')
}

// Lines は、題名を幅 lineWidth で折り返した行です。優先の順は、幅（必ず守る）、英数字のまとまり、禁則、均し、3行での切り詰め。
func Lines(face font.Face, title string) []string {
	rs := []rune(strings.Join(strings.Fields(title), " "))
	width := func(a, b int) fixed.Int26_6 { return font.MeasureString(face, string(rs[a:b])) }
	max := fixed.I(lineWidth)
	breakable := func(i int) bool { return i <= 0 || i >= len(rs) || !(isWord(rs[i-1]) && isWord(rs[i])) }
	kinsokuOK := func(i int) bool {
		return i <= 0 || i >= len(rs) || (!strings.ContainsRune(headNG, rs[i]) && !strings.ContainsRune(tailNG, rs[i-1]))
	}

	var starts, ends []int
	for s := 0; s < len(rs); {
		for s < len(rs) && rs[s] == ' ' {
			s++
		}
		if s >= len(rs) {
			break
		}
		// 幅に収まる一番遠い位置
		fit := s + 1
		for fit < len(rs) && width(s, fit+1) <= max {
			fit++
		}
		e := fit
		if e < len(rs) {
			// 英数字のまとまりの途中なら、手前の切れ目へ（行より長いまとまりは、幅で切る）
			for e > s+1 && !breakable(e) {
				e--
			}
			if !breakable(e) {
				e = fit
			}
			// 禁則: 守れる位置まで手前へ。行が空になるなら、禁則を守らない（幅を優先）
			if !kinsokuOK(e) {
				k := e - 1
				for k > s && !(kinsokuOK(k) && breakable(k)) {
					k--
				}
				if k > s {
					e = k
				}
			}
		}
		starts, ends = append(starts, s), append(ends, e)
		s = e
	}
	if len(starts) > maxLines {
		// 3行で止め、3行目の末尾を … が幅に収まるまで削る
		s := starts[maxLines-1]
		e := ends[maxLines-1]
		for e > s && font.MeasureString(face, string(rs[s:e])+"…") > max {
			e--
		}
		lines := make([]string, 0, maxLines)
		for i := 0; i < maxLines-1; i++ {
			lines = append(lines, strings.TrimRight(string(rs[starts[i]:ends[i]]), " "))
		}
		return append(lines, strings.TrimRight(string(rs[s:e]), " ")+"…")
	}
	// 均し: 最後の行が2文字以下なら、前の行の末尾から送って3文字以上にする（幅・まとまり・禁則を破るなら均さない）
	if n := len(starts); n >= 2 && ends[n-1]-starts[n-1] <= 2 {
		for k := starts[n-1] - 1; k > starts[n-2]; k-- {
			if ends[n-1]-k < 3 {
				continue
			}
			if breakable(k) && kinsokuOK(k) && width(k, ends[n-1]) <= max {
				ends[n-2], starts[n-1] = k, k
				break
			}
		}
	}
	lines := make([]string, len(starts))
	for i := range starts {
		lines[i] = strings.TrimRight(string(rs[starts[i]:ends[i]]), " ")
	}
	return lines
}
