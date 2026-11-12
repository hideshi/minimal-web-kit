package test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// crossSitePrefixes は、機械（ほかのサーバーやAIエージェントのクライアント）や、ほかのサイトの利用者が
// 使う経路の名前空間です。管理画面へのアクセス元を前段で絞るとき、ここに当たる経路は止めてはいけないか、
// 止めると連携が壊れるため、docs/deploy.md の表に全部載っている必要があります。
// 新しい名前空間で機械向けの経路を作ったら、ここに足します。
var crossSitePrefixes = []string{"/oidc/", "/agent/", "/mcp", "/.well-known/", "/api/", "/admin/oidc/", "/admin/agent/", "/admin/sso/"}

// unresolvedRoutesOK は、経路を機械で読めない登録の許可リストです。値は、そのファイルで許す数です。
// 足すときは、機械やほかのサイトが使う経路でないことを確かめ、理由をここに書きます。
var unresolvedRoutesOK = map[string]int{
	// IsolatedMux.HandleFunc と、独立経路をCoreの mux へ写す処理。経路は呼び出し元の登録で読む
	"../internal/web/handler.go": 3,
	// カタログの公開一覧で読み込む同梱のJS（/catalog/static/ の2つ）を、名前の一覧から登録する
	"../features/catalog/catalog.go": 1,
}

// routePatterns は、Goのファイルの中の Handle・HandleFunc・HandleUpload の最初の引数（経路）を集めます。
// 文字列と、同じパッケージの文字列の定数をつないだ形を読みます。読めない引数は unresolved に入れます。
func routePatterns(files map[string]string) (patterns []string, unresolved []string, err error) {
	fset := token.NewFileSet()
	consts := map[string]string{}
	var parsed []*ast.File
	for name, src := range files {
		f, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			return nil, nil, err
		}
		parsed = append(parsed, f)
		for _, d := range f.Decls {
			g, ok := d.(*ast.GenDecl)
			if !ok || g.Tok != token.CONST {
				continue
			}
			for _, s := range g.Specs {
				v := s.(*ast.ValueSpec)
				for i, n := range v.Names {
					if i < len(v.Values) {
						if lit, ok := v.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
							consts[n.Name], _ = strconv.Unquote(lit.Value)
						}
					}
				}
			}
		}
	}
	var eval func(e ast.Expr) (string, bool)
	eval = func(e ast.Expr) (string, bool) {
		switch x := e.(type) {
		case *ast.BasicLit:
			if x.Kind != token.STRING {
				return "", false
			}
			s, err := strconv.Unquote(x.Value)
			return s, err == nil
		case *ast.Ident:
			s, ok := consts[x.Name]
			return s, ok
		case *ast.BinaryExpr:
			if x.Op != token.ADD {
				return "", false
			}
			l, ok1 := eval(x.X)
			r, ok2 := eval(x.Y)
			return l + r, ok1 && ok2
		case *ast.ParenExpr:
			return eval(x.X)
		}
		return "", false
	}
	for _, f := range parsed {
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || !slices.Contains([]string{"Handle", "HandleFunc", "HandleUpload"}, sel.Sel.Name) {
				return true
			}
			p, ok := eval(call.Args[0])
			if !ok {
				unresolved = append(unresolved, fset.Position(call.Pos()).String())
				return true
			}
			if method, path, found := strings.Cut(p, " "); found && method == strings.ToUpper(method) {
				p = path
			}
			patterns = append(patterns, p)
			return true
		})
	}
	return patterns, unresolved, nil
}

// TestDeploy_CrossSiteRoutesAreListed は、機械やほかのサイトの利用者が使う経路が、
// docs/deploy.md の「管理画面へのアクセス元を絞る」の表に全部載っていることを確かめる。
// 表から漏れると、前段で絞ったときに連携が止まる経路に気づけない。
func TestDeploy_CrossSiteRoutesAreListed(t *testing.T) {
	doc, err := os.ReadFile("../docs/deploy.md")
	if err != nil {
		t.Fatal(err)
	}
	_, section, ok := strings.Cut(string(doc), "### 管理画面へのアクセス元を絞る")
	if !ok {
		t.Fatal("docs/deploy.md: 管理画面へのアクセス元を絞る節がありません")
	}
	section, _, _ = strings.Cut(section, "\n#")
	pkgs := map[string]map[string]string{}
	for _, root := range []string{"../features", "../internal"} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			dir := filepath.Dir(path)
			if pkgs[dir] == nil {
				pkgs[dir] = map[string]string{}
			}
			pkgs[dir][path] = string(b)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	listed := 0
	unresolvedByFile := map[string]int{}
	for _, files := range pkgs {
		patterns, unresolved, err := routePatterns(files)
		if err != nil {
			t.Fatal(err)
		}
		for _, u := range unresolved {
			file, _, _ := strings.Cut(u, ".go:")
			unresolvedByFile[file+".go"]++
		}
		for _, p := range patterns {
			if !slices.ContainsFunc(crossSitePrefixes, func(prefix string) bool { return strings.HasPrefix(p, prefix) }) {
				continue
			}
			listed++
			if !strings.Contains(section, "`"+p+"`") {
				t.Errorf("docs/deploy.md の管理画面へのアクセス元を絞る表に `%s` がありません", p)
			}
		}
	}
	for file, n := range unresolvedByFile {
		if n > unresolvedRoutesOK[file] {
			t.Errorf("%s: 経路を機械で読めない登録が %d 件あります（許可は %d 件）。経路を文字列か定数で書くか、機械向けでないことを確かめて unresolvedRoutesOK に理由と一緒に足してください", file, n, unresolvedRoutesOK[file])
		}
	}
	if listed < 15 {
		t.Fatalf("found only %d cross-site routes; the scan may be broken", listed)
	}
}

// TestDeploy_RoutePatternsReadsConstants は、経路の読み取りが定数をつないだ形も読むことを確かめる。
func TestDeploy_RoutePatternsReadsConstants(t *testing.T) {
	src := `package p
const endpoint = "/mcp"
func f(mux M, g func() string) {
	mux.HandleFunc("POST "+endpoint, nil)
	mux.HandleFunc("GET /.well-known/x"+endpoint, nil)
	mux.Handle("/plain", nil)
	mux.HandleFunc(g(), nil)
	other.Get("/not-a-route")
}`
	got, unresolved, err := routePatterns(map[string]string{"p.go": src})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"/mcp", "/.well-known/x/mcp", "/plain"}; !slices.Equal(got, want) {
		t.Fatalf("patterns = %v, want %v", got, want)
	}
	if len(unresolved) != 1 {
		t.Fatalf("unresolved = %v", unresolved)
	}
}
