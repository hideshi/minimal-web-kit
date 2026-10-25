package test

import (
	"os"
	"os/exec"
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
