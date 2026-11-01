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
	"strconv"
	"strings"
	"testing"
)

var (
	bundleMarker = regexp.MustCompile(`(?m)^// minimal-web-kit: bundled-feature ([a-z]+)$`)
	makeTarget   = regexp.MustCompile(`(?m)^([a-z][a-z-]*):`)
)

// readmeOnlyInCode は、コードで読むが README の表に載せない環境変数です。
var readmeOnlyInCode = map[string]string{
	"CMS_CORS_ORIGINS": "改名前の設定名。残っていたら起動を止めるために読むだけ。改名は news の節に書いてある",
}

// readmeBundle は同梱機能の1つです。dir は features/ からの相対パスです。
type readmeBundle struct{ name, dir string }

// readmeFacts は、README に載っているべき、コードから集めた事実です。
type readmeFacts struct {
	bundles []readmeBundle
	envs    []string
	targets []string
	dirs    []string
}

// TestREADME_ListsWhatExists は、同梱機能・環境変数・make のコマンド・主なディレクトリが
// README に載っていることを検証します。載っているかを見るだけで、書いてある内容が
// 今も正しいかは見ません（内容は実装スキルの手順と監査で確かめます）。
// キットから作ったアプリが README を書き換えたら、先頭の見出しを変えると検査は止まります。
func TestREADME_ListsWhatExists(t *testing.T) {
	readme := readmeText(t)
	if !strings.HasPrefix(readme, readmeTitle) {
		t.Skip("README is not the kit's own")
	}
	for _, gap := range readmeGaps(readme, collectReadmeFacts(t)) {
		t.Errorf("README: %s", gap)
	}
}

// TestREADME_CheckerFindsGaps は、検査が欠けを見つけることを確かめます。
func TestREADME_CheckerFindsGaps(t *testing.T) {
	readme := readmeText(t)
	if !strings.HasPrefix(readme, readmeTitle) {
		t.Skip("README is not the kit's own")
	}
	facts := collectReadmeFacts(t)
	if gaps := readmeGaps(readme, facts); len(gaps) != 0 {
		t.Fatalf("baseline has gaps: %v", gaps)
	}
	cases := []struct {
		name, remove string
		want         string
	}{
		{"コマンド表の行", "| `make add-documents` / `make remove-documents` |", "command table"},
		{"機能の節", "`make add-media` で有効化します", "section"},
		{"仕様へのリンク", "[domain/analytics.md](docs/domain/analytics.md)", "docs/domain/analytics.md"},
		{"予約ディレクトリ", "`documents`・", "reserved"},
		{"構成の行", "│   ├── media/ ", "project structure"},
		{"環境変数の行", "| `ANALYTICS_GA4_ID` |", "environment table"},
		{"OIDCの行", "| `OIDC_` で始まる変数 |", "environment table"},
		{"makeのコマンド", "| `make hooks` |", "make hooks"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if !strings.Contains(readme, c.remove) {
				t.Fatalf("sample text not found: %q", c.remove)
			}
			broken := strings.ReplaceAll(readme, c.remove, "")
			if !strings.Contains(strings.Join(readmeGaps(broken, facts), "\n"), c.want) {
				t.Errorf("removing %q was not reported as %q", c.remove, c.want)
			}
		})
	}
}

const readmeTitle = "# minimal-web-kit\n"

func readmeText(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../README.md")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// readmeGaps は、facts のうち README に載っていないものを返します。
func readmeGaps(readme string, f readmeFacts) []string {
	var gaps []string
	missing := func(format string, args ...string) {
		for _, a := range args {
			format = strings.Replace(format, "%s", a, 1)
		}
		gaps = append(gaps, format)
	}
	setup := between(readme, "## 起動方法\n", "## コマンドと設定\n")
	reserved := between(readme, "### 切り替えコマンドの保護範囲\n", "## コマンドと設定\n")
	commands := between(readme, "| コマンド | 動作 |\n", "\n\n")
	envs := between(readme, "| 環境変数 | 既定値・用途 |\n", "\n\n")
	tree := between(readme, "```text\n", "```\n")

	for _, b := range f.bundles {
		cmd := "`make add-" + b.name + "`"
		if !strings.Contains(commands, cmd) {
			missing("%s is not in the command table", b.name)
		}
		if !strings.Contains(setup, "make add-"+b.name) {
			missing("%s has no section under 起動方法 that shows make add-%s", b.name, b.name)
		}
		if _, err := os.Stat("../docs/domain/" + b.name + ".md"); err == nil && !strings.Contains(readme, "docs/domain/"+b.name+".md") {
			missing("%s does not link docs/domain/%s.md", b.name, b.name)
		}
		if !strings.Contains(b.dir, "/") && !strings.Contains(reserved, "`"+b.dir+"`") {
			missing("%s is not in the reserved directories", b.dir)
		}
		if !strings.Contains(tree, "── "+filepath.Base(b.dir)+"/") {
			missing("%s is not in the project structure", b.dir)
		}
	}
	for _, name := range f.envs {
		if strings.HasPrefix(name, "OIDC_") {
			// 共通認証の変数は1行にまとめ、値は共通認証の節に書く
			if !strings.Contains(envs, "`OIDC_`") || !strings.Contains(readme, name) {
				missing("%s is not explained from the environment table", name)
			}
			continue
		}
		if !strings.Contains(envs, "`"+name+"`") {
			missing("%s is not in the environment table", name)
		}
	}
	for _, target := range f.targets {
		if !strings.Contains(commands, "`make "+target+"`") && !strings.Contains(commands, "`make "+target+" ") {
			missing("make %s is not in the command table", target)
		}
	}
	for _, dir := range f.dirs {
		if !strings.Contains(tree, "── "+dir+"/") {
			missing("%s/ is not in the project structure", dir)
		}
	}
	return gaps
}

// between は、start の直後から、次の end までを返します。無ければ空です。
func between(s, start, end string) string {
	_, after, ok := strings.Cut(s, start)
	if !ok {
		return ""
	}
	body, _, _ := strings.Cut(after, end)
	return body
}

func collectReadmeFacts(t *testing.T) readmeFacts {
	t.Helper()
	f := readmeFacts{
		bundles: collectBundles(t),
		envs:    collectEnvs(t),
		targets: collectMakeTargets(t),
		dirs:    collectDirs(t),
	}
	// 集める側が壊れて検査が空振りしないよう、必ずあるものを確かめる
	if !containsBundle(f.bundles, "site") || !containsBundle(f.bundles, "oidcop") {
		t.Fatalf("bundles not collected: %v", f.bundles)
	}
	for _, c := range []struct {
		got  []string
		want string
	}{{f.envs, "DB_PATH"}, {f.envs, "SECURE_COOKIE"}, {f.envs, "CONTACT_DAILY_LIMIT"}, {f.targets, "test-ui"}, {f.dirs, "domain"}, {f.dirs, "richtext"}} {
		if i := sort.SearchStrings(c.got, c.want); i == len(c.got) || c.got[i] != c.want {
			t.Fatalf("%s not collected: %v", c.want, c.got)
		}
	}
	return f
}

func containsBundle(bundles []readmeBundle, name string) bool {
	for _, b := range bundles {
		if b.name == name {
			return true
		}
	}
	return false
}

// collectBundles は、切り替えコマンドが見る識別コメントから同梱機能を集めます。
func collectBundles(t *testing.T) []readmeBundle {
	t.Helper()
	var out []readmeBundle
	err := filepath.WalkDir("../features", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if m := bundleMarker.FindSubmatch(b); m != nil {
			rel, err := filepath.Rel("../features", filepath.Dir(path))
			if err != nil {
				return err
			}
			out = append(out, readmeBundle{name: string(m[1]), dir: filepath.ToSlash(rel)})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// collectEnvs は、os.Getenv・os.LookupEnv と、LoadConfig の引数 getenv で読む変数名を、テスト以外のコードから集めます。
func collectEnvs(t *testing.T) []string {
	t.Helper()
	found := map[string]bool{}
	fset := token.NewFileSet()
	for _, root := range []string{"../cmd", "../internal", "../features"} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			file, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				return err
			}
			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				if arg := envName(call); arg != nil {
					if name, err := strconv.Unquote(arg.Value); err == nil {
						found[name] = true
					}
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	var out []string
	for name := range found {
		if _, skip := readmeOnlyInCode[name]; !skip {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// envName は、環境変数を読む呼び出しの変数名を返します。getenv を部品へ渡す形（intSetting(getenv, "X", ...)）も読みます。
func envName(call *ast.CallExpr) *ast.BasicLit {
	args := call.Args
	switch {
	case len(args) == 1 && readsEnv(call.Fun):
	case len(args) >= 2 && isGetenv(args[0]):
		args = args[1:]
	default:
		return nil
	}
	if lit, ok := args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
		return lit
	}
	return nil
}

func isGetenv(e ast.Expr) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == "getenv"
}

func readsEnv(fun ast.Expr) bool {
	switch f := fun.(type) {
	case *ast.Ident:
		return isGetenv(f)
	case *ast.SelectorExpr:
		pkg, ok := f.X.(*ast.Ident)
		return ok && pkg.Name == "os" && (f.Sel.Name == "Getenv" || f.Sel.Name == "LookupEnv")
	}
	return false
}

func collectMakeTargets(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile("../Makefile")
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, m := range makeTarget.FindAllStringSubmatch(string(b), -1) {
		out = append(out, m[1])
	}
	sort.Strings(out)
	return out
}

// collectDirs は、構成の図に載せるディレクトリを集めます。features/ は同梱機能だけを見ます
// （キットから作ったアプリの自作機能では落とさない）。
func collectDirs(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, root := range []string{"../docs", "../internal", "../internal/web"} {
		entries, err := os.ReadDir(root)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if e.IsDir() {
				out = append(out, e.Name())
			}
		}
	}
	sort.Strings(out)
	return out
}
