package test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// mcpTimeForbidden は、MCPのツールのコードで使わない日時の読み書きです。
// 日時は features.MCPTime（読み取り）・MCPParseTime（書き込み）・MCPLocalTime（確認の文）だけを通します。
var mcpTimeForbidden = map[string]bool{
	"time.Parse": true, "time.ParseInLocation": true,
	"time.RFC3339": true, "time.RFC3339Nano": true, "time.RFC1123": true, "time.RFC1123Z": true,
	"time.RFC822": true, "time.RFC822Z": true, "time.RFC850": true, "time.ANSIC": true,
	"time.UnixDate": true, "time.RubyDate": true, "time.Kitchen": true, "time.Stamp": true,
	"time.DateTime": true, "time.DateOnly": true, "time.TimeOnly": true, "time.Layout": true,
	"form.ParseLocalDateTime": true, "form.DateTimeLayout": true,
}

// mcpTimeProblems は、1つのファイルのMCPの日時の扱いの違反を返します。
// MCPのツールを定義するファイル（名前が mcp で始まるか、features.MCPTool・features.MCPPlan を使うもの）だけを調べます。
func mcpTimeProblems(fset *token.FileSet, name string, file *ast.File) []string {
	defines := strings.HasPrefix(filepath.Base(name), "mcp")
	ast.Inspect(file, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok {
			if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "features" && (sel.Sel.Name == "MCPTool" || sel.Sel.Name == "MCPPlan") {
				defines = true
			}
		}
		return !defines
	})
	if !defines {
		return nil
	}
	var problems []string
	add := func(pos token.Pos, what string) {
		problems = append(problems, fset.Position(pos).String()+": "+what)
	}
	ast.Inspect(file, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.BasicLit:
			if v.Kind != token.STRING {
				return true
			}
			s, err := strconv.Unquote(v.Value)
			if err != nil {
				s = v.Value
			}
			for _, layout := range []string{"2006", "15:04", "Z07"} {
				if strings.Contains(s, layout) {
					add(v.Pos(), "日時の書式 "+strconv.Quote(layout)+" を直接書いている")
					break
				}
			}
		case *ast.SelectorExpr:
			if pkg, ok := v.X.(*ast.Ident); ok && mcpTimeForbidden[pkg.Name+"."+v.Sel.Name] {
				add(v.Pos(), pkg.Name+"."+v.Sel.Name+" を使っている")
			}
		case *ast.CallExpr:
			if sel, ok := v.Fun.(*ast.SelectorExpr); ok && (sel.Sel.Name == "Format" || sel.Sel.Name == "AppendFormat") {
				add(v.Pos(), sel.Sel.Name+" で日時を書いている")
			}
		}
		return true
	})
	return problems
}

// TestMCP_TimesGoThroughTheHelpers は、MCPのツールのコードが、日時を共通の関数だけで読み書きすることを確かめます。
// 新しい機能の mcp.go も、置いただけで対象になります。
// 不変条件: MCP-INV-14
func TestMCP_TimesGoThroughTheHelpers(t *testing.T) {
	fset := token.NewFileSet()
	files := 0
	err := filepath.WalkDir("../features", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// features 直下は共通の関数の置き場所で、検査の対象の外です。
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || filepath.Dir(path) == "../features" {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		problems := mcpTimeProblems(fset, path, file)
		if strings.HasPrefix(filepath.Base(path), "mcp") || len(problems) > 0 {
			files++
		}
		for _, p := range problems {
			t.Errorf("%s。features.MCPTime・MCPParseTime・MCPLocalTime を使う", p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if files < 5 {
		t.Fatalf("調べたMCPのファイルが %d 本しかない。検査の対象の決め方が壊れている", files)
	}
}

// TestMCP_TimeCheckerFindsDirectLayouts は、検査自体が直接の書式を見つけることを確かめます。
func TestMCP_TimeCheckerFindsDirectLayouts(t *testing.T) {
	src := `package x
import ("time"; "minimal-web-kit/features")
var _ features.MCPPlan
func a(t time.Time) string { return t.Format("2006-01-02") }
func b(t time.Time) string { return t.UTC().Format(time.RFC3339) }
func c(s string) (time.Time, error) { return time.Parse("2006-01-02T15:04Z07:00", s) }
`
	ok := `package x
import ("time"; "minimal-web-kit/features")
var _ features.MCPPlan
func a(t time.Time) string { return features.MCPTime(t) }
`
	plain := `package x
import "time"
func a(t time.Time) string { return t.Format("2006-01-02") }
`
	for _, c := range []struct {
		name, src string
		want      int
	}{{"store.go", src, 6}, {"store.go", ok, 0}, {"mcp_extra.go", plain, 2}, {"store.go", plain, 0}} {
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, c.name, c.src, 0)
		if err != nil {
			t.Fatal(err)
		}
		if got := mcpTimeProblems(fset, c.name, file); len(got) != c.want {
			t.Errorf("%s: %d 件 %v、%d 件のはず", c.name, len(got), got, c.want)
		}
	}
}
