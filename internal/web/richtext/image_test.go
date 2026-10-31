package richtext

import (
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"minimal-web-kit/internal/web/form"
)

const testID = "0123456789abcdef0123456789abcdef"

func imageDump(doc Doc) string {
	var out []string
	for _, b := range doc.Blocks {
		if b.Kind == "image" {
			out = append(out, "img("+b.Alt+":"+b.File+")")
			continue
		}
		out = append(out, dump(Doc{Blocks: []Block{b}}))
	}
	return strings.Join(out, "\n")
}

// TestParseWithImages_SpecExamples は docs/richtext.md 6節の画像の例を、そのまま確かめる。
func TestParseWithImages_SpecExamples(t *testing.T) {
	jpg := "/media/" + testID + ".jpg"
	link := "«L:" + jpg + ":a»"
	cases := []struct{ in, want string }{
		{"![本社](" + jpg + ")", "img(本社:" + testID + ".jpg)"},
		{"![a\\]b](/media/" + testID + ".png)", "img(a]b:" + testID + ".png)"},
		{"![a*b(c)](" + jpg + ")", "img(a*b(c):" + testID + ".jpg)"},
		{"![a|[b](" + jpg + ")", "img(a|[b:" + testID + ".jpg)"},
		{"![ a ](" + jpg + ")", "img(a:" + testID + ".jpg)"},
		{"![a](" + jpg + ") ", "img(a:" + testID + ".jpg)"},
		{" ![a](" + jpg + ")", "p( !" + link + ")"},
		{"!\\[a](" + jpg + ")", "p(![a](" + jpg + "))"},
		{"![](" + jpg + ")", "p(![](" + jpg + "))"},
		{"![ ](" + jpg + ")", "p(![ ](" + jpg + "))"},
		{"![a](/media/" + testID + ".gif)", "p(!«L:/media/" + testID + ".gif:a»)"},
		{"![a](" + jpg + "?x=1)", "p(!«L:" + jpg + "?x=1:a»)"},
		{"![a](/media/ABC.jpg)", "p(!«L:/media/ABC.jpg:a»)"},
		{"![a](/media/" + testID + ".JPG)", "p(!«L:/media/" + testID + ".JPG:a»)"},
		{"![a](/media/" + testID + ".jpeg)", "p(!«L:/media/" + testID + ".jpeg:a»)"},
		{"![a](/media/" + strings.ToUpper(testID) + ".jpg)", "p(!«L:/media/" + strings.ToUpper(testID) + ".jpg:a»)"},
		{"![a](/media/" + testID + "\\.jpg)", "p(![a](" + jpg + "))"},
		{"![[a]](" + jpg + ")", "p(![[a]](" + jpg + "))"},
		{"![a]", "p(![a])"},
		{"![a](" + jpg, "p(![a](" + jpg + ")"},
		{"\\![a](" + jpg + ")", "p(\\!" + link + ")"},
		{"![" + strings.Repeat("あ", MaxAltRunes+1) + "](" + jpg + ")", "p(!«L:" + jpg + ":" + strings.Repeat("あ", MaxAltRunes+1) + "»)"},
		{"![a](https://example.com/x.jpg)", "p(!«L:https://example.com/x.jpg:a»)"},
		{"![a](/about)", "p(!«L:/about:a»)"},
		{"![a](" + jpg + ") 続き", "p(!" + link + " 続き)"},
		{"前 ![a](" + jpg + ")", "p(前 !" + link + ")"},
		{"- ![a](" + jpg + ")", "ul(!" + link + ")"},
		{"| ![a](" + jpg + ") |\n|---|", "table[表1](!" + link + ")()"},
		{"本文\n![a](" + jpg + ")\n続き", "p(本文)\nimg(a:" + testID + ".jpg)\np(続き)"},
		{"| h |\n|---|\n| x |\n![a](" + jpg + ")", "table[表1](h)(x)\nimg(a:" + testID + ".jpg)"},
	}
	for _, c := range cases {
		doc, err := ParseWithImages(c.in)
		if err != nil {
			t.Errorf("%q: %v", c.in, err)
			continue
		}
		if got := imageDump(doc); got != c.want {
			t.Errorf("%q\n got: %s\nwant: %s", c.in, got, c.want)
		}
	}
}

// 社内文書が使う Parse は、同じ行を画像にしない。
func TestParse_DoesNotReadImages(t *testing.T) {
	doc, err := Parse("![a](/media/" + testID + ".jpg)")
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Blocks) != 1 || doc.Blocks[0].Kind != "p" {
		t.Fatalf("got %+v", doc.Blocks)
	}
}

func TestParseWithImages_Limit(t *testing.T) {
	line := "![a](/media/" + testID + ".jpg)\n"
	doc, err := ParseWithImages(strings.Repeat(line, MaxImages))
	if err != nil || len(doc.Blocks) != MaxImages {
		t.Fatalf("%d images: %d, %v", MaxImages, len(doc.Blocks), err)
	}
	if _, err := ParseWithImages(strings.Repeat(line, MaxImages+1)); !errors.Is(err, ErrLimit) {
		t.Fatalf("over limit: %v", err)
	}
	// 上限は画像を読む入口だけのもの
	if _, err := Parse(strings.Repeat(line, MaxImages+1)); err != nil {
		t.Fatalf("Parse must ignore the image limit: %v", err)
	}
}

// 形に当たらない長い行でも、読む時間は長さに比例する。
func TestParseWithImages_LinearTime(t *testing.T) {
	const size = 50000
	start := time.Now()
	for _, body := range []string{
		"![" + strings.Repeat("[", size),
		"![" + strings.Repeat("]", size),
		"![a](" + strings.Repeat("(", size),
		strings.Repeat("![a](x)", size/7),
		strings.Repeat("![\n", size/3),
	} {
		if _, err := ParseWithImages(body); err != nil {
			t.Fatal(err)
		}
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("too slow: %v", d)
	}
}

func TestDoc_ImagesAndResolve(t *testing.T) {
	a, b := testID+".jpg", strings.Repeat("a", 32)+".png"
	doc, err := ParseWithImages(ImageLine("x", a) + "\n" + ImageLine("y", b) + "\n" + ImageLine("z", a))
	if err != nil {
		t.Fatal(err)
	}
	if got := doc.Images(); len(got) != 2 || got[0] != a || got[1] != b {
		t.Fatalf("images: %v", got)
	}
	doc.ResolveImages(func(file string) (string, int, int, bool) { return "/x/" + file, 4, 3, file == a })
	if doc.Blocks[0].Src != "/x/"+a || doc.Blocks[0].Width != 4 || doc.Blocks[1].Src != "" {
		t.Fatalf("resolve: %+v", doc.Blocks)
	}
}

// FuzzImageLine は、挿入のボタンが作る行を本文に足すと、画像がちょうど1つ増え、同じ説明とファイル名に戻ることを確かめる。
func FuzzImageLine(f *testing.F) {
	for _, seed := range []string{"本社", `a\`, `a]b`, `\]`, "[x](y)", "*a*", "a|b", "!["} {
		f.Add("本文", seed)
	}
	f.Fuzz(func(t *testing.T, body, raw string) {
		if !utf8.ValidString(body) || len(body) > 5000 {
			return
		}
		alt, err := form.Line(raw, MaxAltRunes, true)
		if err != nil {
			return
		}
		before, err := ParseWithImages(body)
		if err != nil {
			return
		}
		count := func(d Doc) (n int) {
			for _, b := range d.Blocks {
				if b.Kind == "image" {
					n++
				}
			}
			return
		}
		file := testID + ".jpg"
		after, err := ParseWithImages(body + "\n\n" + ImageLine(alt, file) + "\n")
		if err != nil {
			return // 画像の上限
		}
		if count(after) != count(before)+1 {
			t.Fatalf("images: %d to %d", count(before), count(after))
		}
		last := after.Blocks[len(after.Blocks)-1]
		if last.Kind != "image" || last.Alt != alt || last.File != file {
			t.Fatalf("round trip: %+v", last)
		}
	})
}
