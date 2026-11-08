package test

import (
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestArchitecture_DependencyAllowedList は go.mod の直接依存が許可リストのみであることを検証します。
func TestArchitecture_DependencyAllowedList(t *testing.T) {
	content, err := os.ReadFile("../go.mod")
	if err != nil {
		t.Fatalf("failed to read go.mod: %v", err)
	}

	allowedDirectDeps := map[string]bool{
		"modernc.org/sqlite": true,
		// 共通認証の提供機能（features/auth/oidcop）と利用機能（features/federatedauth）だけがimportする。
		// Baseと通常の管理者認証のバイナリには組み込まない（TestArchitecture_FederationDependenciesStayOptional）。
		"github.com/zitadel/oidc/v3":    true,
		"github.com/go-jose/go-jose/v4": true,
		"github.com/coreos/go-oidc/v3":  true,
		"golang.org/x/oauth2":           true,
		// WordPressからの移行の道具（cmd/wpimport）だけが、本文のHTMLを読むためにimportする（2026-11-03 承認）。
		// サーバーの実行ファイルには組み込まない（TestArchitecture_LinkedModules・TestArchitecture_HTMLParserOnlyInImporter）。
		"golang.org/x/net": true,
		// OGPの画像を題名から作る部品（internal/web/ogimage）だけがimportする（2026-11-06 承認。docs/plans/lp-plan.md）。
		// 固定ページを登録した構成だけに組み込まれる（TestArchitecture_ImageDependencyStaysOptional）。
		"golang.org/x/image": true,
	}

	lines := strings.Split(string(content), "\n")
	foundDirectDeps := make(map[string]bool)

	inRequireBlock := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "require (") {
			inRequireBlock = true
			continue
		}
		if inRequireBlock && trimmed == ")" {
			inRequireBlock = false
			continue
		}

		if inRequireBlock || strings.HasPrefix(trimmed, "require ") {
			// indirect 行はスキップ
			if strings.Contains(trimmed, "// indirect") {
				continue
			}

			parts := strings.Fields(trimmed)
			if len(parts) >= 2 {
				pkg := parts[0]
				if pkg == "require" {
					pkg = parts[1]
				}
				foundDirectDeps[pkg] = true
			}
		}
	}

	if len(foundDirectDeps) == 0 {
		t.Fatal("No direct dependencies found in go.mod. Verification failed to inspect direct dependencies.")
	}

	for dep := range foundDirectDeps {
		if !allowedDirectDeps[dep] {
			t.Errorf("Disallowed direct dependency found: %s. Direct dependencies must be explicitly approved and added to allowed list.", dep)
		}
	}

	for allowed := range allowedDirectDeps {
		if !foundDirectDeps[allowed] {
			t.Errorf("Expected direct dependency %s is missing from go.mod direct requirements.", allowed)
		}
	}
}

// linkedModules は、サーバーと切り替えCLIの実行ファイルに組み込まれる外部モジュールです（間接依存を含む）。
// SQLiteドライバの更新等で増減したら、追加されたモジュールの用途・ライセンス・保守状況を確認し、
// READMEの一覧と合わせてこのリストを更新します。
var linkedModules = []string{
	"github.com/dustin/go-humanize",
	"github.com/google/uuid",
	"github.com/remyoudompheng/bigfft",
	"golang.org/x/sys",
	"modernc.org/libc",
	"modernc.org/mathutil",
	"modernc.org/memory",
	"modernc.org/sqlite",
}

// pagesModules は、固定ページの機能を登録したときだけ組み込まれるモジュールです（OGPの画像を題名から作るため）。
var pagesModules = []string{"golang.org/x/image", "golang.org/x/text"}

// providerModules は、共通認証の提供機能（make add-oidcop）を登録したときだけ組み込まれるモジュールです。README の依存の表と合わせます。
var providerModules = []string{
	"github.com/bmatcuk/doublestar/v4",
	"github.com/cespare/xxhash/v2",
	"github.com/go-chi/chi/v5",
	"github.com/go-jose/go-jose/v4",
	"github.com/go-logr/logr",
	"github.com/go-logr/stdr",
	"github.com/gorilla/securecookie",
	"github.com/muhlemmer/gu",
	"github.com/muhlemmer/httpforwarded",
	"github.com/rs/cors",
	"github.com/zitadel/oidc/v3",
	"github.com/zitadel/schema",
	"go.opentelemetry.io/auto/sdk",
	"go.opentelemetry.io/otel",
	"go.opentelemetry.io/otel/metric",
	"go.opentelemetry.io/otel/trace",
	"golang.org/x/oauth2",
	"golang.org/x/text",
}

// consumerModules は、共通認証の利用機能（make add-federatedauth）を登録したときだけ組み込まれるモジュールです。
var consumerModules = []string{
	"github.com/coreos/go-oidc/v3",
	"github.com/go-jose/go-jose/v4",
	"golang.org/x/oauth2",
}

const (
	pagesImport    = "minimal-web-kit/features/pages"
	providerImport = "minimal-web-kit/features/auth/oidcop"
	consumerImport = "minimal-web-kit/features/federatedauth"
)

// registers は、cmd/server/features.go が importPath の機能を登録しているかを返します。
// キット自身は固定ページと共通認証を登録しないが、キットから作ったサイト（make add-pages・make add-oidcop 等）は登録する。
func registers(t *testing.T, importPath string) bool {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "cmd", "server", "features.go"))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Contains(string(b), `"`+importPath+`"`)
}

// serverModules は、サーバーと cmd/kit の実行ファイルに組み込まれてよい外部モジュールです。登録した機能で決まります。
func serverModules(t *testing.T) []string {
	t.Helper()
	want := slices.Clone(linkedModules)
	if registers(t, pagesImport) {
		want = append(want, pagesModules...)
	}
	if registers(t, providerImport) {
		want = append(want, providerModules...)
	}
	if registers(t, consumerImport) {
		want = append(want, consumerModules...)
	}
	slices.Sort(want)
	return slices.Compact(want)
}

// TestArchitecture_LinkedModules は、実行ファイルに組み込まれる外部モジュール（間接依存を含む）が一覧と一致することを検証します。
// 本番の対象（Linuxのamd64・arm64）で、CGOを無効にした場合の依存を調べます。
func TestArchitecture_LinkedModules(t *testing.T) {
	goCmd, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("go command not found: %v", err)
	}
	for _, arch := range []string{"amd64", "arm64"} {
		cmd := exec.Command(goCmd, "list", "-deps", "-f", "{{if .Module}}{{.Module.Path}}{{end}}", "./cmd/server", "./cmd/kit")
		cmd.Dir = ".."
		cmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+arch, "CGO_ENABLED=0")
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("go list (%s): %v", arch, err)
		}
		var got []string
		for _, m := range strings.Fields(string(out)) {
			if m != "minimal-web-kit" && !slices.Contains(got, m) {
				got = append(got, m)
			}
		}
		slices.Sort(got)
		want := serverModules(t)
		if !slices.Equal(got, want) {
			t.Errorf("linux/%s: linked modules changed.\n got: %v\nwant: %v\nReview new modules (purpose, license, maintenance), then update linkedModules and README.", arch, got, want)
		}
	}
}

// importerOnlyModules は、移行の道具（cmd/wpimport）だけに組み込まれる外部モジュールです。HTMLを読む x/net と、
// 固定ページの保存の処理を通じて入る x/image（OGPの画像。道具は画像を作らない）。サーバーの既定の構成には入らない。
var importerOnlyModules = []string{"golang.org/x/image", "golang.org/x/net", "golang.org/x/text"}

// TestArchitecture_ImporterModules は、移行の道具（cmd/wpimport）に組み込まれる外部モジュールが、サーバーと同じ一覧に
// importerOnlyModules を足した物だけであることを検証します（計画10.10）。サーバーと道具で期待値を分けて持ちます。
func TestArchitecture_ImporterModules(t *testing.T) {
	want := append(slices.Clone(linkedModules), importerOnlyModules...)
	slices.Sort(want)
	var got []string
	for _, m := range listDeps(t, moduleFormat, "./cmd/wpimport") {
		if m != "" && m != "minimal-web-kit" && !slices.Contains(got, m) {
			got = append(got, m)
		}
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("cmd/wpimport modules changed.\n got: %v\nwant: %v", got, want)
	}
}

// TestArchitecture_HTMLParserOnlyInImporter は、golang.org/x/net を import するのが移行の道具（cmd/wpimport の下）だけであることを検証します。
// キットの機能・Coreのパッケージが使い始めると、サーバーの実行ファイルに入りうるためです。
func TestArchitecture_HTMLParserOnlyInImporter(t *testing.T) {
	importers := map[string]bool{}
	err := filepath.WalkDir("..", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == ".git" || d.Name() == ".worktrees" || d.Name() == "testdata" || path == "../test/browser") {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imp := range f.Imports {
			if strings.HasPrefix(strings.Trim(imp.Path.Value, `"`), "golang.org/x/net") {
				importers[filepath.ToSlash(filepath.Dir(path))] = true
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(importers) == 0 {
		t.Fatal("cmd/wpimport must use golang.org/x/net/html; update this test if it no longer does")
	}
	for dir := range importers {
		if !strings.HasPrefix(dir, "../cmd/wpimport") {
			t.Errorf("%s imports golang.org/x/net; only cmd/wpimport may (the server binary must not link it)", dir)
		}
	}
}

// federationModules は、共通認証の機能を登録したときだけ組み込まれる直接依存です。
var federationModules = []string{
	"github.com/coreos/go-oidc/v3",
	"github.com/go-jose/go-jose/v4",
	"github.com/zitadel/oidc/v3",
	"golang.org/x/oauth2",
}

const (
	moduleFormat = "{{if .Module}}{{.Module.Path}}{{end}}"
	importFormat = "{{.ImportPath}}"
)

func listDeps(t *testing.T, format, pkg string) []string {
	t.Helper()
	cmd := exec.Command("go", "list", "-deps", "-f", format, pkg)
	cmd.Dir = ".."
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	return strings.Fields(string(out))
}

// TestArchitecture_FederationDependenciesStayOptional は、OIDCのライブラリが、それを使う機能を登録したときだけ組み込まれることを検証します。
// go.mod の require 行ではなく、到達するimportで決まります。提供機能と利用機能は互いに依存しません。
func TestArchitecture_FederationDependenciesStayOptional(t *testing.T) {
	pkgs := []string{"./cmd/kit", "./features/auth", "./internal/app", "./internal/federation"}
	// 共通認証を登録したサイトでは、サーバーに組み込まれるのは当然で、その一覧は TestArchitecture_LinkedModules が確かめる。
	if !registers(t, providerImport) && !registers(t, consumerImport) {
		pkgs = append(pkgs, "./cmd/server")
	}
	for _, pkg := range pkgs {
		for _, m := range listDeps(t, moduleFormat, pkg) {
			if slices.Contains(federationModules, m) {
				t.Errorf("%s links %s; only the optional federation features may import it", pkg, m)
			}
		}
	}
	if deps := listDeps(t, importFormat, "./features/auth"); slices.Contains(deps, "minimal-web-kit/features/auth/oidcop") {
		t.Error("features/auth must not import its optional provider")
	}
	if deps := listDeps(t, importFormat, "./features/auth/oidcop"); slices.Contains(deps, "minimal-web-kit/features/federatedauth") {
		t.Error("the provider must not import the consumer")
	}
	consumer := listDeps(t, importFormat, "./features/federatedauth")
	for _, pkg := range []string{"minimal-web-kit/features/auth", "github.com/zitadel/oidc/v3/pkg/op"} {
		if slices.Contains(consumer, pkg) {
			t.Errorf("the consumer must not import %s", pkg)
		}
	}
	if modules := listDeps(t, moduleFormat, "./features/auth/oidcop"); !slices.Contains(modules, "github.com/zitadel/oidc/v3") {
		t.Error("the provider no longer uses the OIDC library; update the dependency allow list")
	}
	// 登録したサイトで TestArchitecture_LinkedModules が使う一覧を、キットでも機能の実際の依存と突き合わせる。
	for pkg, extra := range map[string][]string{"./features/auth/oidcop": providerModules, "./features/federatedauth": consumerModules} {
		allowed := append(slices.Clone(linkedModules), extra...)
		used := map[string]bool{}
		for _, m := range listDeps(t, moduleFormat, pkg) {
			if m == "minimal-web-kit" {
				continue
			}
			used[m] = true
			if !slices.Contains(allowed, m) {
				t.Errorf("%s links %s; review it, then add it to the module list and README", pkg, m)
			}
		}
		for _, m := range extra {
			if !used[m] {
				t.Errorf("%s no longer links %s; remove it from the module list and README", pkg, m)
			}
		}
	}
}

// TestSkills_LinkedForEveryTool は、skills/ の全てのスキルが、各ツール向けのリンクから読めることを確かめます。
// 実体は skills/ だけに置き、リンク先と別のコピーを作りません。
func TestSkills_LinkedForEveryTool(t *testing.T) {
	entries, err := os.ReadDir("../skills")
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"../.claude/skills", "../.agents/skills"} {
		links, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		found := map[string]bool{}
		for _, l := range links {
			found[l.Name()] = true
			path := filepath.Join(dir, l.Name())
			if l.Type()&os.ModeSymlink == 0 {
				t.Errorf("%s はリンクにする（実体は skills/ に置く）", path)
				continue
			}
			if _, err := os.Stat(filepath.Join(path, "SKILL.md")); err != nil {
				t.Errorf("%s のリンク先が読めない", path)
			}
		}
		for _, e := range entries {
			if e.IsDir() && !found[e.Name()] {
				t.Errorf("%s に %s のリンクが無い", dir, e.Name())
			}
		}
	}
}

// usesForbiddenImport は、ファイルが unsafe か C をimportしているかを返します。
func usesForbiddenImport(name string, src any) (bool, error) {
	f, err := parser.ParseFile(token.NewFileSet(), name, src, parser.ImportsOnly)
	if err != nil {
		return false, err
	}
	for _, spec := range f.Imports {
		if path := strings.Trim(spec.Path.Value, `"`); path == "unsafe" || path == "C" {
			return true, nil
		}
	}
	return false, nil
}

// TestArchitecture_KitDoesNotUseUnsafe は、キット自身のコードが unsafe とcgoを使わないことを確かめます。
// メモリ安全でない部分を、SQLiteの層などの依存の中に閉じ込めます。
func TestArchitecture_KitDoesNotUseUnsafe(t *testing.T) {
	for src, want := range map[string]bool{
		"package p\nimport \"fmt\"\n":    false,
		"package p\nimport \"unsafe\"\n": true,
		"package p\nimport \"C\"\n":      true,
		"package p\nimport u \"unsafe\"": true,
	} {
		if got, err := usesForbiddenImport("x.go", src); err != nil || got != want {
			t.Errorf("%q: %v %v", src, got, err)
		}
	}
	seen := 0
	for _, root := range []string{"../features", "../internal", "../cmd"} {
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") {
				return err
			}
			seen++
			bad, err := usesForbiddenImport(path, nil)
			if err != nil {
				return err
			}
			if bad {
				t.Errorf("%s: unsafe と C はキットのコードで使わない", path)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if seen < 100 {
		t.Fatalf("見たファイルが少なすぎる: %d", seen)
	}
}

// TestArchitecture_ModulesHaveNoCgo は、全機能の依存のパッケージのうち、Linuxのamd64・arm64で選ばれるものに、
// cgoのファイルが無いことを確かめます。他のOS用のファイルは見ません。
// CGO_ENABLED=0 ではcgoのファイルが一覧から除かれて検査が空振りするので、有効にして調べます。
func TestArchitecture_ModulesHaveNoCgo(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		cmd := exec.Command("go", "list", "-deps", "-f", `{{if and .Module (not .Standard)}}{{.ImportPath}} {{len .CgoFiles}}{{end}}`, "./cmd/...", "./features/...")
		cmd.Dir = ".."
		cmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+arch, "CGO_ENABLED=1")
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("go list (%s): %v", arch, err)
		}
		seen := 0
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			fields := strings.Fields(line)
			if len(fields) != 2 {
				continue
			}
			seen++
			if fields[1] != "0" {
				t.Errorf("linux/%s %s: cgoのファイルが %s 個ある", arch, fields[0], fields[1])
			}
		}
		if seen < 10 {
			t.Fatalf("見たパッケージが少なすぎる: %d", seen)
		}
	}
}

// TestArchitecture_ImageDependencyStaysOptional は、golang.org/x/image が、OGPの画像を作る部品（internal/web/ogimage）を
// 使う固定ページを登録したときだけ組み込まれ、Base・Site・Core・移行の道具には入らないことを検証します。
func TestArchitecture_ImageDependencyStaysOptional(t *testing.T) {
	// 移行の道具は固定ページの保存の処理を使うので入る（TestArchitecture_ImporterModules）。
	// サーバーは、固定ページを登録した構成（キットから作ったサイト）だけ入る（serverModules）
	pkgs := []string{"./cmd/kit", "./internal/app", "./internal/web", "./features/site", "./features/news", "./features/catalog", "./features/documents"}
	if !registers(t, pagesImport) {
		pkgs = append(pkgs, "./cmd/server")
	}
	for _, pkg := range pkgs {
		if slices.Contains(listDeps(t, moduleFormat, pkg), "golang.org/x/image") {
			t.Errorf("%s links golang.org/x/image; only the OGP image renderer may use it", pkg)
		}
	}
	if !slices.Contains(listDeps(t, moduleFormat, "./features/pages"), "golang.org/x/image") {
		t.Error("features/pages should render OGP images with golang.org/x/image")
	}
	// import するのは ogimage だけ
	err := filepath.WalkDir("..", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == ".git" || d.Name() == ".worktrees" || d.Name() == "testdata" || path == "../test/browser") {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasPrefix(path, "../internal/web/ogimage/") {
			return nil
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imp := range f.Imports {
			if strings.HasPrefix(strings.Trim(imp.Path.Value, `"`), "golang.org/x/image") {
				t.Errorf("%s imports %s; only internal/web/ogimage may", path, imp.Path.Value)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
