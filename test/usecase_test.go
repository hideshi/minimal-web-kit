package test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

var (
	// useCaseRow は、docs/ui/use-cases.md のユースケースの表の行（| UC-CATALOG-1 | ... |）です。
	useCaseRow = regexp.MustCompile(`(?m)^\|\s*(UC-[A-Z]+-\d+)\s*\|`)
	useCaseID  = regexp.MustCompile(`\bUC-[A-Z]+-\d+\b`)
)

// TestUseCases_AreTraceable は、docs/ui/use-cases.md のユースケースのIDが重複せず、
// それぞれ1つ以上のテスト関数のコメントに書かれていること、
// コードに書かれたIDがすべて表にあることを検証します。
// IDの対応を確かめるだけで、目的を達成できることの証明にはなりません。
func TestUseCases_AreTraceable(t *testing.T) {
	doc, err := os.ReadFile("../docs/ui/use-cases.md")
	if err != nil {
		t.Fatal(err)
	}
	defined := map[string]bool{}
	for _, m := range useCaseRow.FindAllStringSubmatch(string(doc), -1) {
		if defined[m[1]] {
			t.Errorf("%s is defined twice", m[1])
		}
		defined[m[1]] = true
	}
	if len(defined) == 0 {
		t.Fatal("no use cases found in docs/ui/use-cases.md")
	}

	tested := map[string]bool{}
	referenced := map[string]string{}
	err = filepath.WalkDir("..", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != ".." && (strings.HasPrefix(d.Name(), ".") || strings.HasPrefix(d.Name(), "_")) {
				return filepath.SkipDir
			}
			switch d.Name() {
			case "bin", "dist", "data", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || path == filepath.Join("..", "test", "usecase_test.go") {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, id := range useCaseID.FindAllString(string(body), -1) {
			referenced[id] = path
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, body, parser.ParseComments)
		if err != nil {
			return err
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || fn.Doc == nil || !strings.HasPrefix(fn.Name.Name, "Test") {
				continue
			}
			for _, id := range useCaseID.FindAllString(fn.Doc.Text(), -1) {
				tested[id] = true
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	var missing []string
	for id := range defined {
		if !tested[id] {
			missing = append(missing, id)
		}
	}
	sort.Strings(missing)
	for _, id := range missing {
		t.Errorf("use case %s is not referenced by any test function comment (// ユースケース: ...)", id)
	}
	for id, path := range referenced {
		if !defined[id] {
			t.Errorf("%s refers to %s, which is not defined in docs/ui/use-cases.md", path, id)
		}
	}
}
