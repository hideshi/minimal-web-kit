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
		if strings.HasSuffix(name, "_test.go") || !(strings.HasSuffix(name, ".go") || strings.HasSuffix(name, ".sql") || strings.HasSuffix(name, ".html")) {
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
	source := code.String()

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
			for _, span := range codeSpan.FindAllStringSubmatch(row[2], -1) {
				name := span[1]
				if !identifier.MatchString(name) {
					continue
				}
				checked++
				word := name[strings.LastIndex(name, ".")+1:]
				if strings.HasSuffix(name, ".sql") || strings.HasSuffix(name, ".go") {
					word = name
				}
				if !regexp.MustCompile(`\b` + regexp.QuoteMeta(word) + `\b`).MatchString(source) {
					t.Errorf("%s の %s の守る場所 %s がコードに無い", filepath.Base(doc), row[1], name)
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("no locations were checked")
	}
	t.Logf("checked %d locations", checked)
}
