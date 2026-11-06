package ogimage

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"image/png"
	"strings"
	"testing"

	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
)

func testFace(t *testing.T) font.Face {
	t.Helper()
	if err := load(); err != nil {
		t.Fatal(err)
	}
	f, err := opentype.NewFace(parsed, &opentype.FaceOptions{Size: titleSize, DPI: 72, Hinting: font.HintingNone})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// TestLines_Rules は、幅・英数字のまとまり・禁則・均し・3行での切り詰めを、例で確かめる（計画の4.3節）。
func TestLines_Rules(t *testing.T) {
	face := testFace(t)
	width := func(s string) int { return font.MeasureString(face, s).Round() }
	long := strings.Repeat("あ", 16) // 64pxの全角16文字は1024pxで1行に収まり、17文字は収まらない
	cases := []struct {
		name, in string
		want     []string
	}{
		{"1文字", "あ", []string{"あ"}},
		{"2文字", "あい", []string{"あい"}},
		{"ちょうど1行", long, []string{long}},
		{"行頭の句点は前の行の最後の文字と送る", long + "。続き", []string{strings.Repeat("あ", 15), "あ。続き"}},
		{"句読点の連続", long + "。。。", []string{strings.Repeat("あ", 15), "あ。。。"}},
		{"行末の開き括弧は次の行へ", strings.Repeat("あ", 15) + "「かっこ」", []string{strings.Repeat("あ", 15), "「かっこ」"}},
		{"均しで最後の行を3文字以上にする", long + "い", []string{strings.Repeat("あ", 14), "ああい"}},
		{"英数字のまとまりは切らない", strings.Repeat("あ", 13) + " minimal", []string{strings.Repeat("あ", 13), "minimal"}},
		{"行より長い英数字は幅で切る", strings.Repeat("x", 60), nil},
		{"均しで送った行は空白で始めない", strings.Repeat("あ", 15) + " xy", []string{strings.Repeat("あ", 15), "xy"}},
		{"4行以上は3行目を…で切る", strings.Repeat("あ", 70), []string{long, long, strings.Repeat("あ", 15) + "…"}},
		{"ちょうど3行", strings.Repeat("あ", 48), []string{long, long, long}},
	}
	for _, c := range cases {
		got := Lines(face, c.in)
		for _, line := range got {
			if w := width(line); w > lineWidth {
				t.Errorf("%s: line %q is %dpx wide", c.name, line, w)
			}
		}
		if len(got) > maxLines {
			t.Errorf("%s: %d lines", c.name, len(got))
		}
		if c.want == nil {
			if strings.Join(got, "") != strings.Repeat("x", 60) {
				t.Errorf("%s: lost characters %q", c.name, got)
			}
			continue
		}
		if strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("%s:\n got  %q\n want %q", c.name, got, c.want)
		}
	}
}

// TestDrawable は、フォントに無い文字・結合文字・書式文字・制御文字があれば描かないことを確かめる。
func TestDrawable(t *testing.T) {
	for in, want := range map[string]bool{
		"WordPress から、手間とリスクの少ないホームページへ": true,
		"𠮷野家":      false, // JIS X 0208 に無い
		"絵文字😀":     false,
		"か\u3099":  false, // 結合の濁点
		"右\u202e左": false, // 書式文字
		"タブ\tあり":   false,
		"   ":      false,
		"髙橋と①の㈱":   true,  // CP932 の文字（Windows の日本語入力が出す）
		"〜乗り換え～－":  true,  // 波ダッシュ・全角チルダ・全角ハイフンマイナス
		"約≒":       false, // 上流のフォントに無い
		"高橋 & Co.": true,
	} {
		if got := Drawable(in, "example.com"); got != want {
			t.Errorf("Drawable(%q) = %v", in, got)
		}
	}
	if Drawable("題名", "𠮷.jp") {
		t.Error("the host must be drawable too")
	}
}

// TestVersion は、版が入力の区切りで食い違わず、題名・ホスト名・種類で変わることを確かめる。
func TestVersion(t *testing.T) {
	a := Version("pages", "ab", "c")
	for name, other := range map[string]string{
		"区切り": Version("pages", "a", "bc"),
		"題名":  Version("pages", "ab", "d"),
		"種類":  Version("news", "ab", "c"),
	} {
		if other == a {
			t.Errorf("%s: same version %s", name, a)
		}
	}
	if len(a) != 12 || strings.Trim(a, "0123456789abcdef") != "" || Version("pages", "ab", "c") != a {
		t.Errorf("version %q", a)
	}
}

// goldenPNG は、決まった入力の PNG の SHA-256 です。描き方が変わったら RenderVersion を上げてから、ここを直す。
var goldenPNG = map[int]map[string]string{
	1: {
		"WordPress から、手間とリスクの少ないホームページへ":  "29c2f060463d9aaf3669c31866ffa4f5dcb73763e1f50724ffddda00876e0783",
		"社内の情報から、サイトの更新が続く":               "92a590fb9f68cdcdd67014e606ad80444875357b080f90e2d6e755a15a97994d",
		strings.Repeat("長い題名の例です。", 12):   "0f4994333fb6dcfee6354a355bd53649c31f3fa6a15a1319a7d106573b0136df",
		strings.Repeat("あ", 16) + "。続き":   "a10bd549f9e67ff7cb70d93193c0e006dc4b2dd1223961c81e7e902948e34e86",
		strings.Repeat("あ", 15) + "「かっこ」": "3227b68b91961745fca6aead6480578b2d8ac8ad0e7308385172a8de250a1486",
		strings.Repeat("あ", 16) + "い":     "f5a4e892b057fe2755bdd790646d08f6fdb43fa03458afc21b4adaa0fef4d43a",
	},
}

// TestRender_Golden は、同じ入力で同じバイト列になり、描き方の版と PNG のハッシュの組が変わっていないことを確かめる。
func TestRender_Golden(t *testing.T) {
	want, ok := goldenPNG[RenderVersion]
	if !ok {
		t.Fatalf("no golden hashes for RenderVersion %d", RenderVersion)
	}
	for title, hash := range want {
		a, err := Render(title, "example.com")
		if err != nil {
			t.Fatal(err)
		}
		b, _ := Render(title, "example.com")
		if !bytes.Equal(a, b) {
			t.Fatalf("%q: not deterministic", title)
		}
		img, err := png.Decode(bytes.NewReader(a))
		if err != nil || img.Bounds().Dx() != Width || img.Bounds().Dy() != Height {
			t.Fatalf("%q: bad png %v", title, err)
		}
		sum := sha256.Sum256(a)
		if got := hex.EncodeToString(sum[:]); got != hash {
			t.Errorf("%q: png hash %s; the drawing changed, so raise RenderVersion and update goldenPNG", title, got)
		}
	}
	if _, err := Render("😀", "example.com"); err != ErrNotDrawable {
		t.Errorf("err = %v", err)
	}
}

// TestLicense は、フォントのライセンスの全文と著作権表示を持つことを確かめる。
func TestLicense(t *testing.T) {
	if !strings.Contains(License, "SIL OPEN FONT LICENSE Version 1.1") || !strings.Contains(Copyright, "Adobe") {
		t.Fatal("font license or copyright is missing")
	}
}
