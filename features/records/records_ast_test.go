package records

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// TestStatements_OnlyBuiltFromDefinitions は、recordsStatement の値を作る・中身を書き換える箇所が、
// buildRecordStatements の中だけであることを、ソースの構文木で確かめます。ほかの場所で文を作ると落ちます。
// 不変条件: RECORDS-INV-7
func TestStatements_OnlyBuiltFromDefinitions(t *testing.T) {
	paths, _ := filepath.Glob("*.go")
	fset := token.NewFileSet()
	found := 0
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range f.Decls {
			fn, _ := decl.(*ast.FuncDecl)
			allowed := fn != nil && fn.Name.Name == "buildRecordStatements"
			ast.Inspect(decl, func(n ast.Node) bool {
				bad := ""
				switch x := n.(type) {
				case *ast.CompositeLit:
					if id, ok := x.Type.(*ast.Ident); ok && id.Name == "recordsStatement" {
						found++
						bad = "builds a recordsStatement"
					}
				case *ast.CallExpr:
					if id, ok := x.Fun.(*ast.Ident); ok && id.Name == "recordsStatement" {
						bad = "converts to recordsStatement"
					}
				case *ast.AssignStmt:
					for _, lhs := range x.Lhs {
						if sel, ok := lhs.(*ast.SelectorExpr); ok && sel.Sel.Name == "sql" {
							bad = "assigns to .sql"
						}
					}
				}
				if bad != "" && !allowed {
					t.Errorf("%s: %s outside buildRecordStatements", fset.Position(n.Pos()), bad)
				}
				return true
			})
		}
	}
	if found != 1 {
		t.Errorf("expected exactly one recordsStatement literal (in buildRecordStatements), found %d", found)
	}
}
