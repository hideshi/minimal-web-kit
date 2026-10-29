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
	// invariantRow は、docs/domain/*.md の不変条件の表の行（| AUTH-INV-1 | ... |）です。
	invariantRow = regexp.MustCompile(`(?m)^\|\s*([A-Z]+-INV-\d+)\s*\|`)
	invariantID  = regexp.MustCompile(`\b[A-Z]+-INV-\d+\b`)
)

// TestDomain_InvariantsAreTested は、docs/domain/*.md に定義した不変条件の番号が、
// それぞれ1つ以上のテスト関数のコメントに書かれていること、
// コードに書かれた番号がすべて docs/domain/*.md に定義されていることを検証します。
func TestDomain_InvariantsAreTested(t *testing.T) {
	docs, err := filepath.Glob("../docs/domain/*.md")
	if err != nil || len(docs) == 0 {
		t.Fatalf("no domain model documents in docs/domain: %v", err)
	}
	defined := map[string]string{}
	for _, doc := range docs {
		body, err := os.ReadFile(doc)
		if err != nil {
			t.Fatal(err)
		}
		rows := invariantRow.FindAllStringSubmatch(string(body), -1)
		if len(rows) == 0 {
			t.Errorf("%s defines no invariants", doc)
		}
		for _, m := range rows {
			if prev, ok := defined[m[1]]; ok {
				t.Errorf("%s is defined twice (%s, %s)", m[1], prev, doc)
			}
			defined[m[1]] = doc
		}
	}

	tested := map[string]bool{}
	referenced := map[string]string{}
	err = filepath.WalkDir("..", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// . と _ で始まるディレクトリ（.git、.worktrees の別の作業ツリー等）は、Goのツールと同じく読み飛ばす
			if path != ".." && (strings.HasPrefix(d.Name(), ".") || strings.HasPrefix(d.Name(), "_")) {
				return filepath.SkipDir
			}
			switch d.Name() {
			case "bin", "dist", "data", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || path == filepath.Join("..", "test", "domain_test.go") {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, id := range invariantID.FindAllString(string(body), -1) {
			referenced[id] = path
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		// テスト関数（TestXxx）の説明コメントに書かれた番号だけを「テストした」と数える
		file, err := parser.ParseFile(token.NewFileSet(), path, body, parser.ParseComments)
		if err != nil {
			return err
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || fn.Doc == nil || !strings.HasPrefix(fn.Name.Name, "Test") {
				continue
			}
			for _, id := range invariantID.FindAllString(fn.Doc.Text(), -1) {
				tested[id] = true
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	var missing []string
	for id, doc := range defined {
		if !tested[id] {
			missing = append(missing, id+" ("+doc+")")
		}
	}
	sort.Strings(missing)
	for _, m := range missing {
		t.Errorf("invariant %s is not referenced by any test function comment (// 不変条件: ...)", m)
	}
	for id, path := range referenced {
		if _, ok := defined[id]; !ok {
			t.Errorf("%s refers to %s, which is not defined in docs/domain/*.md", path, id)
		}
	}
}
