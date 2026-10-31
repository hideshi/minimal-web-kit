package test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sqlMethods は、SQLの文を受け取るメソッドと、文の引数の位置です。
var sqlMethods = map[string]int{
	"Exec": 0, "Query": 0, "QueryRow": 0,
	"ExecContext": 1, "QueryContext": 1, "QueryRowContext": 1, "PrepareContext": 1,
}

// sqlExceptions は、機械で固定と言えない文の許可リストです。値は、そのファイルで許す箇所の数です。
// 足すときは、外部の入力が文に入らないことを確かめ、理由をここに書きます。
var sqlExceptions = map[string]int{
	// 表の名前は呼び出し側の定数。Rebuild の値はコードの中だけで決まる
	"internal/store/rebuild.go": 2,
	// バックアップのファイルから読んだ表の名前を、quoteIdentifier で引用して数える
	"internal/store/backup.go": 1,
}

// sqlChecker は、1つのパッケージのSQLの呼び出しを調べます。
type sqlChecker struct {
	fset    *token.FileSet
	static  map[string]bool // 定数と、go:embed の変数
	funcs   map[string]bool // 最初の戻り値が必ず固定の文字列の関数
	sinks   map[string]int  // 文を引数で受け取り、SQLへ渡す関数と、その引数の位置
	bad     []token.Pos
	checked int
}

func newSQLChecker(fset *token.FileSet, files []*ast.File) *sqlChecker {
	c := &sqlChecker{fset: fset, static: map[string]bool{}, funcs: map[string]bool{}, sinks: map[string]int{}}
	for changed := true; changed; {
		changed = false
		for _, f := range files {
			for _, d := range f.Decls {
				gen, ok := d.(*ast.GenDecl)
				if !ok {
					continue
				}
				embedded := gen.Tok == token.VAR && gen.Doc != nil && strings.Contains(commentText(gen.Doc), "go:embed")
				for _, spec := range gen.Specs {
					vs, ok := spec.(*ast.ValueSpec)
					if !ok {
						continue
					}
					for i, id := range vs.Names {
						if !c.static[id.Name] && (embedded || (gen.Tok == token.CONST && i < len(vs.Values) && c.isStatic(vs.Values[i], nil))) {
							c.static[id.Name] = true
							changed = true
						}
					}
				}
			}
		}
		for _, f := range files {
			for _, d := range f.Decls {
				fn, ok := d.(*ast.FuncDecl)
				if ok && fn.Body != nil && !c.funcs[fn.Name.Name] && c.returnsStatic(fn) {
					c.funcs[fn.Name.Name] = true
					changed = true
				}
			}
		}
	}
	return c
}

func commentText(g *ast.CommentGroup) string {
	var b strings.Builder
	for _, c := range g.List {
		b.WriteString(c.Text)
	}
	return b.String()
}

// isStatic は、式が固定の文字列だけでできているかを返します。
func (c *sqlChecker) isStatic(e ast.Expr, local map[string]bool) bool {
	switch v := e.(type) {
	case *ast.BasicLit:
		return v.Kind == token.STRING
	case *ast.ParenExpr:
		return c.isStatic(v.X, local)
	case *ast.BinaryExpr:
		return v.Op == token.ADD && c.isStatic(v.X, local) && c.isStatic(v.Y, local)
	case *ast.Ident:
		if static, ok := local[v.Name]; ok {
			return static
		}
		return c.static[v.Name]
	case *ast.CallExpr:
		if id, ok := v.Fun.(*ast.Ident); ok {
			return c.funcs[id.Name]
		}
		// 「?,?,?」のように、固定の文字列を繰り返すだけの呼び出し
		if sel, ok := v.Fun.(*ast.SelectorExpr); ok && len(v.Args) > 0 {
			if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "strings" && (sel.Sel.Name == "Repeat" || sel.Sel.Name == "TrimSuffix") {
				return c.isStatic(v.Args[0], local)
			}
		}
	}
	return false
}

func paramNames(fn *ast.FuncDecl) []string {
	var names []string
	for _, field := range fn.Type.Params.List {
		for _, name := range field.Names {
			names = append(names, name.Name)
		}
	}
	return names
}

// locals は、関数の中の変数のうち、代入が全て固定の文字列のものを返します。
// 引数と、それ以外の値を1度でも代入した変数は false です。
func (c *sqlChecker) locals(fn *ast.FuncDecl) map[string]bool {
	local := map[string]bool{}
	for _, name := range paramNames(fn) {
		local[name] = false
	}
	mark := func(name string, value bool) bool {
		old, seen := local[name]
		if seen && (!old || value) {
			return false
		}
		local[name] = value
		return true
	}
	for changed := true; changed; {
		changed = false
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			switch s := n.(type) {
			case *ast.AssignStmt:
				for i, lhs := range s.Lhs {
					id, ok := lhs.(*ast.Ident)
					if !ok || id.Name == "_" {
						continue
					}
					value := false
					if len(s.Rhs) == len(s.Lhs) {
						value = c.isStatic(s.Rhs[i], local)
					} else if i == 0 {
						value = c.isStatic(s.Rhs[0], local) // 例: table, err := classificationTable(kind)
					}
					changed = mark(id.Name, value) || changed
				}
			case *ast.RangeStmt:
				if id, ok := s.Value.(*ast.Ident); ok {
					changed = mark(id.Name, c.staticRange(s, local)) || changed
				}
				if id, ok := s.Key.(*ast.Ident); ok && id.Name != "_" {
					changed = mark(id.Name, false) || changed
				}
			case *ast.ValueSpec:
				for i, id := range s.Names {
					changed = mark(id.Name, i < len(s.Values) && c.isStatic(s.Values[i], local)) || changed
				}
			}
			return true
		})
	}
	return local
}

// staticRange は、固定の文字列の配列を回す値かを返します（例: for _, q := range []string{"..."}）。
func (c *sqlChecker) staticRange(s *ast.RangeStmt, local map[string]bool) bool {
	lit, ok := s.X.(*ast.CompositeLit)
	if !ok {
		return false
	}
	for _, e := range lit.Elts {
		if !c.isStatic(e, local) {
			return false
		}
	}
	return true
}

func (c *sqlChecker) returnsStatic(fn *ast.FuncDecl) bool {
	if fn.Type.Results == nil || len(fn.Type.Results.List) == 0 {
		return false
	}
	if id, ok := fn.Type.Results.List[0].Type.(*ast.Ident); !ok || id.Name != "string" {
		return false
	}
	local := c.locals(fn)
	result, found := true, false
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch s := n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.ReturnStmt:
			found = true
			if len(s.Results) == 0 || !c.isStatic(s.Results[0], local) {
				result = false
			}
		}
		return true
	})
	return found && result
}

func calleeName(call *ast.CallExpr) (name string, method bool) {
	switch f := call.Fun.(type) {
	case *ast.Ident:
		return f.Name, false
	case *ast.SelectorExpr:
		return f.Sel.Name, true
	}
	return "", false
}

// run は全ての呼び出しを調べます。文を引数のまま受け渡す関数は、その呼び出し元を調べます。
func (c *sqlChecker) run(files []*ast.File) {
	for changed := true; changed; {
		changed = false
		c.bad, c.checked = nil, 0
		for _, f := range files {
			for _, d := range f.Decls {
				fn, ok := d.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				local := c.locals(fn)
				params := paramNames(fn)
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					name, method := calleeName(call)
					at, known := c.sinks[name]
					if m, isSQL := sqlMethods[name]; isSQL && method {
						at, known = m, true
					}
					if !known || at >= len(call.Args) {
						return true
					}
					c.checked++
					arg := call.Args[at]
					if c.isStatic(arg, local) {
						return true
					}
					if id, ok := arg.(*ast.Ident); ok {
						for i, p := range params {
							if p == id.Name {
								if _, seen := c.sinks[fn.Name.Name]; !seen {
									c.sinks[fn.Name.Name] = i
									changed = true
								}
								return true
							}
						}
					}
					c.bad = append(c.bad, call.Pos())
					return true
				})
			}
		}
	}
}

// TestSQL_StatementsAreStatic は、SQLの文が固定の文字列だけでできていることを確かめる。
// 利用者の値は ? で渡す。文の中に値をつなぐと、このテストが落ちる。
// 定数、go:embed の文、固定の文字列だけを代入した変数と関数は使ってよい。
// 表の名前のように ? で渡せないものは、固定の文字列を返す関数で選ぶ。
func TestSQL_StatementsAreStatic(t *testing.T) {
	var dirs []string
	for _, root := range []string{"../features", "../internal", "../cmd"} {
		if err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err == nil && d.IsDir() {
				dirs = append(dirs, path)
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	checked := 0
	found := map[string]int{}
	for _, dir := range dirs {
		fset := token.NewFileSet()
		paths, _ := filepath.Glob(filepath.Join(dir, "*.go"))
		var files []*ast.File
		for _, path := range paths {
			if strings.HasSuffix(path, "_test.go") {
				continue
			}
			f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
			if err != nil {
				t.Fatal(err)
			}
			files = append(files, f)
		}
		c := newSQLChecker(fset, files)
		c.run(files)
		checked += c.checked
		for _, pos := range c.bad {
			p := fset.Position(pos)
			name := strings.TrimPrefix(filepath.ToSlash(p.Filename), "../")
			found[name]++
			if _, ok := sqlExceptions[name]; !ok {
				t.Errorf("%s:%d: SQLの文に固定でない値がある（値は ? で渡す）", name, p.Line)
			}
		}
	}
	for name, want := range sqlExceptions {
		if found[name] != want {
			t.Errorf("%s: 許可リストは %d 箇所、実際は %d 箇所（理由を確かめて数を直す）", name, want, found[name])
		}
	}
	if checked < 200 {
		t.Fatalf("見たSQLの呼び出しが少なすぎる: %d", checked)
	}
}

// TestSQL_CheckerFindsUnsafeStatements は、検査自体が危ない書き方を見つけることを確かめる。
func TestSQL_CheckerFindsUnsafeStatements(t *testing.T) {
	const head = "package p\nconst safe = \"SELECT 1\"\nfunc table(k string) (string, error) { if k == \"a\" { return \"t_a\", nil }; return \"\", nil }\n"
	for name, c := range map[string]struct {
		body string
		bad  int
	}{
		"literal":       {`func f(db D) { db.Query("SELECT 1") }`, 0},
		"const":         {`func f(db D) { db.Query(safe + " WHERE a=?") }`, 0},
		"local":         {`func f(db D) { q := "SELECT 1"; q += " LIMIT 1"; db.Query(q) }`, 0},
		"table func":    {`func f(db D, k string) { t, _ := table(k); db.Query("SELECT 1 FROM " + t) }`, 0},
		"marks":         {`func f(db D, n int) { m := strings.Repeat("?,", n); db.Query("SELECT 1 IN (" + m + ")") }`, 0},
		"range":         {`func f(db D) { for _, q := range []string{"A", "B"} { db.Exec(q) } }`, 0},
		"helper static": {`func g(db D, q string) { db.Query(q) }; func f(db D) { g(db, "SELECT 1") }`, 0},
		"param":         {`func f(db D, name string) { db.Query("SELECT " + name) }`, 1},
		"reassigned":    {`func f(db D, name string) { q := "SELECT 1"; q = name; db.Query(q) }`, 1},
		"appended":      {`func f(db D, name string) { q := "SELECT 1"; q += name; db.Query(q) }`, 1},
		"sprintf":       {`func f(db D, name string) { db.Query(fmt.Sprintf("SELECT %s", name)) }`, 1},
		"helper value":  {`func g(db D, q string) { db.Query(q) }; func f(db D, name string) { g(db, "SELECT " + name) }`, 1},
		"range value":   {`func f(db D, name string) { for _, q := range []string{"A", name} { db.Exec(q) } }`, 1},
		"func value":    {`func v(k string) (string, error) { return k, nil }; func f(db D, k string) { t, _ := v(k); db.Query(t) }`, 1},
		"context":       {`func f(db D, ctx C, name string) { db.QueryContext(ctx, name+"x") }`, 1},
	} {
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, "x.go", head+c.body, 0)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		checker := newSQLChecker(fset, []*ast.File{file})
		checker.run([]*ast.File{file})
		if len(checker.bad) != c.bad {
			t.Errorf("%s: 見つけた数 %d、期待 %d", name, len(checker.bad), c.bad)
		}
	}
}
