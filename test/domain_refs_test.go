package test

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// invariantLocation は、不変条件の表の最後の列（守る場所）です。
var (
	invariantLocation = regexp.MustCompile(`(?m)^\|\s*([A-Z]+-INV-\d+)\s*\|.*\|([^|]*)\|\s*$`)
	codeSpan          = regexp.MustCompile("`([^`]+)`")
	identifier        = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.]*$`)
)

// TestDomain_LocationsExist は、docs/domain/*.md の「守る場所」の列に書いた識別子が、今のコードにあることを検証します。
// 関数の改名や削除で、文書だけが古くなることを防ぎます。条件の内容が実装と合っているかは確かめません。
func TestDomain_LocationsExist(t *testing.T) {
	source := codeSource(t)
	docs, err := filepath.Glob("../docs/domain/*.md")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, doc := range docs {
		body, err := os.ReadFile(doc)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range invariantLocation.FindAllStringSubmatch(string(body), -1) {
			found := 0
			for _, name := range codeNames(row[2]) {
				found++
				checked++
				if !inCode(source, name) {
					t.Errorf("%s の %s の守る場所 %s がコードに無い", filepath.Base(doc), row[1], name)
				}
			}
			if found == 0 {
				t.Errorf("%s の %s の守る場所に、コードの識別子が無い", filepath.Base(doc), row[1])
			}
		}
	}
	if checked == 0 {
		t.Fatal("no locations were checked")
	}
	t.Logf("checked %d locations", checked)
}

// codeNames は、文の中の `…` のうち、コードの識別子の形のものを返します。
func codeNames(text string) []string {
	var names []string
	for _, span := range codeSpan.FindAllStringSubmatch(text, -1) {
		if identifier.MatchString(span[1]) {
			names = append(names, span[1])
		}
	}
	return names
}

// inCode は、識別子かファイル名がコードにあるかを返します。a.B は B を探します。
func inCode(source, name string) bool {
	word := name[strings.LastIndex(name, ".")+1:]
	for _, suffix := range []string{".sql", ".go", ".js", ".html"} {
		if strings.HasSuffix(name, suffix) {
			word = name
		}
	}
	return regexp.MustCompile(`\b` + regexp.QuoteMeta(word) + `\b`).MatchString(source)
}

// codeSource は、テスト以外のコードとファイル名をつないで返します。
func codeSource(t *testing.T) string {
	t.Helper()
	var code strings.Builder
	err := filepath.WalkDir("..", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != ".." && (strings.HasPrefix(d.Name(), ".") || strings.HasPrefix(d.Name(), "_")) {
				return filepath.SkipDir
			}
			switch d.Name() {
			case "bin", "dist", "data", "node_modules", "docs":
				return filepath.SkipDir
			}
			return nil
		}
		name := d.Name()
		if strings.HasSuffix(name, "_test.go") || !(strings.HasSuffix(name, ".go") || strings.HasSuffix(name, ".sql") || strings.HasSuffix(name, ".html") || strings.HasSuffix(name, ".js")) {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		code.WriteString(string(body))
		code.WriteString("\n" + filepath.Base(path) + "\n")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return code.String()
}
