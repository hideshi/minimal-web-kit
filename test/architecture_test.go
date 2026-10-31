package test

import (
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
		if !slices.Equal(got, linkedModules) {
			t.Errorf("linux/%s: linked modules changed.\n got: %v\nwant: %v\nReview new modules (purpose, license, maintenance), then update linkedModules and README.", arch, got, linkedModules)
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
	for _, pkg := range []string{"./cmd/server", "./cmd/kit", "./features/auth", "./internal/app", "./internal/federation"} {
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
