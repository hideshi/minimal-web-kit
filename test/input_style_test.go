package test

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// inputTypeUse は、入力欄の型を決める書き方です。form.Field の値（同じ行の Label: より後の Type:、変数の .Type への代入）と、
// テンプレートの <input> の type 属性を拾う。
var inputTypeUse = regexp.MustCompile(`(?:Label:[^\n]*\bType:\s*|\.Type\s*=\s*|<input[^>]*\btype=)"([a-z-]+)"`)

// 枠と余白をCSSで付けない型。チェック・ラジオは choice、ボタンは btn で見た目を決め、hidden は見えない。
// file はブラウザの部品をそのまま使う。
var unstyledInputTypes = []string{"checkbox", "radio", "hidden", "submit", "button", "file"}

// TestInputs_EveryTypeIsStyled は、コードとテンプレートが使う入力欄の型が、全部 style.css の入力欄の規則に載っていることを確かめます。
// 載っていない型（例: 業務記録の日付と整数）は、ブラウザの既定の見た目になり、ほかの入力欄と揃わない。
func TestInputs_EveryTypeIsStyled(t *testing.T) {
	css, err := os.ReadFile(filepath.Join("..", "internal", "web", "static", "css", "style.css"))
	if err != nil {
		t.Fatal(err)
	}
	used := map[string]string{}
	for _, dir := range []string{"features", "internal"} {
		err := filepath.WalkDir(filepath.Join("..", dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || strings.HasSuffix(path, "_test.go") || !(strings.HasSuffix(path, ".go") || strings.HasSuffix(path, ".html")) {
				return err
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, m := range inputTypeUse.FindAllStringSubmatch(string(b), -1) {
				if _, seen := used[m[1]]; !seen {
					used[m[1]] = path
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, want := range []string{"text", "date", "number", "datetime-local"} {
		if _, ok := used[want]; !ok {
			t.Errorf("the scan missed %q; the pattern no longer matches how inputs are written", want)
		}
	}
	for typ, path := range used {
		if slices.Contains(unstyledInputTypes, typ) {
			continue
		}
		if !strings.Contains(string(css), `input[type="`+typ+`"]`) {
			t.Errorf(`%s uses <input type=%q>, but style.css has no input[type=%q] rule`, path, typ, typ)
		}
	}
}
