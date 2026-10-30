package main

import (
	"go/format"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const baseRegistration = `package main
import (
 "database/sql"
 "log/slog"
 "minimal-web-kit/features"
 // [FEATURE_IMPORTS]
)
func setupFeatures(db *sql.DB, logger *slog.Logger) *features.Registry {
 r := features.NewRegistry()
 // custom registration remains here
 // [FEATURE_REGISTRATIONS]
 return r
}
// custom code must remain unchanged
func custom() {}
`

func fixture(t *testing.T, registration string) {
	t.Helper()
	t.Chdir(t.TempDir())
	for _, dir := range []string{"cmd/server", "features/site", "features/news", "features/auth", "features/auth/oidcop", "features/federatedauth", "features/custom"} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	write(t, "cmd/server/features.go", registration)
	write(t, "features/site/site.go", bundledSiteMarker+"\npackage site\n")
	write(t, "features/news/news.go", bundledNewsMarker+"\npackage news\n")
	write(t, "features/auth/auth.go", bundledAuthMarker+"\npackage auth\n")
	write(t, "features/auth/oidcop/oidcop.go", bundledOIDCMarker+"\npackage oidcop\n")
	write(t, "features/federatedauth/federatedauth.go", bundledFederatedMarker+"\npackage federatedauth\n")
	write(t, "features/custom/custom.go", "package custom\n// keep me\n")
}
func write(t *testing.T, path, value string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(value), 0644); err != nil {
		t.Fatal(err)
	}
}
func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestToggleSitePreservesFilesAndIsIdempotent(t *testing.T) {
	fixture(t, baseRegistration)
	originalSite := read(t, "features/site/site.go")
	originalCustom := read(t, "features/custom/custom.go")
	if err := addSite(); err != nil {
		t.Fatal(err)
	}
	enabled := read(t, "cmd/server/features.go")
	if strings.Count(enabled, `"minimal-web-kit/features/site"`) != 1 || strings.Count(enabled, "r.Register(site.NewFeature(logger))") != 1 {
		t.Fatal("Site registration missing or duplicated")
	}
	if err := addSite(); err != nil {
		t.Fatal(err)
	}
	if read(t, "cmd/server/features.go") != enabled {
		t.Fatal("second add changed registration")
	}
	if err := removeSite(); err != nil {
		t.Fatal(err)
	}
	if read(t, "cmd/server/features.go") != baseRegistration {
		t.Fatal("custom registration was changed")
	}
	if err := removeSite(); err != nil {
		t.Fatal(err)
	}
	if read(t, "features/site/site.go") != originalSite || read(t, "features/custom/custom.go") != originalCustom {
		t.Fatal("feature files were changed")
	}
}

func TestToggleSiteRejectsConflictsWithoutWriting(t *testing.T) {
	for _, enable := range []bool{true, false} {
		for _, scenario := range []string{"custom_site", "missing_site", "directory_symlink", "file_symlink", "missing_marker", "duplicate_marker", "wrong_marker_location", "malformed_go", "partial_registration", "alias_collision", "aliased_site", "multiline_registration", "registration_in_other_function"} {
			t.Run(scenario+map[bool]string{true: "_add", false: "_remove"}[enable], func(t *testing.T) {
				fixture(t, baseRegistration)
				switch scenario {
				case "custom_site":
					write(t, "features/site/site.go", "package site\n// user's own feature\n")
				case "missing_site":
					if err := os.RemoveAll("features/site"); err != nil {
						t.Fatal(err)
					}
				case "directory_symlink":
					if err := os.Rename("features/site", "features/other"); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink("other", "features/site"); err != nil {
						t.Fatal(err)
					}
				case "file_symlink":
					if err := os.Rename("cmd/server/features.go", "cmd/server/other.go"); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink("other.go", "cmd/server/features.go"); err != nil {
						t.Fatal(err)
					}
				case "missing_marker":
					write(t, "cmd/server/features.go", strings.Replace(baseRegistration, "// [FEATURE_IMPORTS]", "", 1))
				case "duplicate_marker":
					write(t, "cmd/server/features.go", baseRegistration+"\n// [FEATURE_IMPORTS]\n")
				case "wrong_marker_location":
					write(t, "cmd/server/features.go", strings.Replace(baseRegistration, "// [FEATURE_IMPORTS]", "", 1)+"\n// [FEATURE_IMPORTS]\n")
				case "malformed_go":
					write(t, "cmd/server/features.go", "package main\nfunc broken(\n")
				case "partial_registration":
					write(t, "cmd/server/features.go", strings.Replace(baseRegistration, "// [FEATURE_IMPORTS]", `"minimal-web-kit/features/site"`+"\n // [FEATURE_IMPORTS]", 1))
				case "alias_collision":
					write(t, "cmd/server/features.go", strings.Replace(baseRegistration, "// [FEATURE_IMPORTS]", `site "example.invalid/custom"`+"\n // [FEATURE_IMPORTS]", 1))
				case "aliased_site":
					write(t, "cmd/server/features.go", strings.Replace(baseRegistration, "// [FEATURE_IMPORTS]", `lp "minimal-web-kit/features/site"`+"\n // [FEATURE_IMPORTS]", 1))
				case "multiline_registration":
					if err := addSite(); err != nil {
						t.Fatal(err)
					}
					write(t, "cmd/server/features.go", strings.Replace(read(t, "cmd/server/features.go"), "r.Register(site.NewFeature(logger))", "r.Register(\n site.NewFeature(logger),\n)", 1))
				case "registration_in_other_function":
					if err := addSite(); err != nil {
						t.Fatal(err)
					}
					s := read(t, "cmd/server/features.go")
					s = strings.Replace(s, " r.Register(site.NewFeature(logger))\n", "", 1)
					write(t, "cmd/server/features.go", s+"\nfunc elsewhere() {\n r.Register(site.NewFeature(logger))\n}\n")
				}
				before := read(t, "cmd/server/features.go")
				custom := read(t, "features/custom/custom.go")
				if err := toggleSite(enable); err == nil {
					t.Fatal("expected safe refusal")
				}
				if read(t, "cmd/server/features.go") != before || read(t, "features/custom/custom.go") != custom {
					t.Fatal("failed operation changed files")
				}
				leftovers, _ := filepath.Glob("cmd/server/.features-*")
				if len(leftovers) != 0 {
					t.Fatal("temporary file left behind")
				}
			})
		}
	}
}

// Siteとお知らせ機能は独立して切り替えられ、互いの登録と自作コードを変更しないこと。
func TestToggleSiteAndNewsIndependently(t *testing.T) {
	fixture(t, baseRegistration)
	original := read(t, "cmd/server/features.go")
	if err := addSite(); err != nil {
		t.Fatal(err)
	}
	if err := addNews(); err != nil {
		t.Fatal(err)
	}
	if err := addNews(); err != nil {
		t.Fatal(err)
	}
	both := read(t, "cmd/server/features.go")
	// お知らせ機能を有効にすると、お知らせ機能が使う管理者認証も一緒に有効になる
	for _, want := range []string{`"minimal-web-kit/features/site"`, "r.Register(site.NewFeature(logger))", `"minimal-web-kit/features/news"`, "r.Register(news.NewFeature(logger))", `"minimal-web-kit/features/auth"`, "r.Register(auth.NewFeature(logger))"} {
		if strings.Count(both, want) != 1 {
			t.Fatalf("expected exactly one %s", want)
		}
	}
	if !strings.Contains(both, "// custom code must remain unchanged") {
		t.Fatal("custom code removed")
	}
	if err := removeSite(); err != nil {
		t.Fatal(err)
	}
	newsOnly := read(t, "cmd/server/features.go")
	if strings.Contains(newsOnly, "site.") || strings.Count(newsOnly, "r.Register(news.NewFeature(logger))") != 1 {
		t.Fatal("removing Site must keep the news registration")
	}
	// お知らせ機能が有効な間は、管理者認証を外せない
	if err := removeAuth(); err == nil || !strings.Contains(err.Error(), "remove-news") {
		t.Fatalf("removing auth while news is enabled must be refused: %v", err)
	}
	if !strings.Contains(read(t, "cmd/server/features.go"), "r.Register(auth.NewFeature(logger))") {
		t.Fatal("a refused remove-auth must not change the registration")
	}
	if err := removeNews(); err != nil {
		t.Fatal(err)
	}
	if err := removeAuth(); err != nil {
		t.Fatal(err)
	}
	if read(t, "cmd/server/features.go") != original {
		t.Fatal("registration not restored after removing all")
	}
}

// 識別コメントのない同名のnewsディレクトリは、自作機能として扱い変更しないこと。
func TestToggleNewsRejectsCustomDirectory(t *testing.T) {
	fixture(t, baseRegistration)
	write(t, "features/news/news.go", "package news\n// user's own feature\n")
	before := read(t, "cmd/server/features.go")
	if err := addNews(); err == nil {
		t.Fatal("expected refusal for custom news directory")
	}
	if read(t, "cmd/server/features.go") != before {
		t.Fatal("refused operation changed registration")
	}
}

// gofmt済みの登録ファイルは、どの順で有効化・無効化してもgofmt済みのまま保つこと。
func TestToggleKeepsGofmtFormatting(t *testing.T) {
	formatted, err := format.Source([]byte(baseRegistration))
	if err != nil {
		t.Fatal(err)
	}
	fixture(t, string(formatted))
	steps := []func() error{addNews, addSite, removeNews, addNews, removeSite}
	for i, step := range steps {
		if err := step(); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
		content := read(t, "cmd/server/features.go")
		if again, err := format.Source([]byte(content)); err != nil || string(again) != content {
			t.Fatalf("step %d left the file unformatted:\n%s", i, content)
		}
	}
	if !strings.Contains(read(t, "cmd/server/features.go"), "custom code must remain unchanged") {
		t.Fatal("custom code must be kept")
	}
	if err := removeNews(); err != nil {
		t.Fatal(err)
	}
	if err := removeAuth(); err != nil {
		t.Fatal(err)
	}
	if read(t, "cmd/server/features.go") != string(formatted) {
		t.Fatal("disabling everything must restore the original file")
	}
}

// gofmtされていない登録ファイルは、利用者のコードの見た目を変えないこと。
func TestToggleDoesNotReformatUnformattedFile(t *testing.T) {
	fixture(t, baseRegistration)
	if err := addSite(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(read(t, "cmd/server/features.go"), "\n r := features.NewRegistry()") {
		t.Fatal("unformatted user code must not be reformatted")
	}
}

func TestCatalogToggleDependenciesAndPreservation(t *testing.T) {
	fixture(t, baseRegistration)
	if err := os.MkdirAll("features/catalog", 0755); err != nil {
		t.Fatal(err)
	}
	write(t, "features/catalog/catalog.go", bundledCatalogMarker+"\npackage catalog\n")
	original := read(t, "cmd/server/features.go")
	if err := addCatalog(); err != nil {
		t.Fatal(err)
	}
	enabled := read(t, "cmd/server/features.go")
	if !strings.Contains(enabled, "r.Register(catalog.NewFeature(logger))") || !strings.Contains(enabled, "r.Register(auth.NewFeature(logger))") {
		t.Fatal("missing dependency")
	}
	if err := addCatalog(); err != nil || read(t, "cmd/server/features.go") != enabled {
		t.Fatal("not idempotent", err)
	}
	if err := removeAuth(); err == nil || !strings.Contains(err.Error(), "remove-catalog") || read(t, "cmd/server/features.go") != enabled {
		t.Fatal("Auth removal was not refused")
	}
	if err := addNews(); err != nil {
		t.Fatal(err)
	}
	if err := removeCatalog(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(read(t, "cmd/server/features.go"), "r.Register(news.NewFeature(logger))") {
		t.Fatal("removed News")
	}
	if err := removeNews(); err != nil {
		t.Fatal(err)
	}
	if err := removeAuth(); err != nil {
		t.Fatal(err)
	}
	if read(t, "cmd/server/features.go") != original {
		t.Fatal("registration not restored")
	}
	// Refusing a user-owned directory must not first enable Auth.
	write(t, "features/catalog/catalog.go", "package catalog\n")
	if err := addCatalog(); err == nil || read(t, "cmd/server/features.go") != original {
		t.Fatal("custom catalog modified registration")
	}
}

func TestContactToggleDependencies(t *testing.T) {
	fixture(t, baseRegistration)
	if err := os.MkdirAll("features/contact", 0755); err != nil {
		t.Fatal(err)
	}
	write(t, "features/contact/contact.go", bundledContactMarker+"\npackage contact\n")
	original := read(t, "cmd/server/features.go")
	if err := addContact(); err != nil {
		t.Fatal(err)
	}
	enabled := read(t, "cmd/server/features.go")
	if !strings.Contains(enabled, "r.Register(contact.NewFeature(logger))") || !strings.Contains(enabled, "r.Register(auth.NewFeature(logger))") {
		t.Fatal("missing dependency")
	}
	if err := removeAuth(); err == nil || !strings.Contains(err.Error(), "remove-contact") || read(t, "cmd/server/features.go") != enabled {
		t.Fatal("Auth removal was not refused")
	}
	if err := removeContact(); err != nil {
		t.Fatal(err)
	}
	if err := removeAuth(); err != nil {
		t.Fatal(err)
	}
	if read(t, "cmd/server/features.go") != original {
		t.Fatal("registration not restored")
	}
}

func TestPagesToggleDependencies(t *testing.T) {
	fixture(t, baseRegistration)
	if err := os.MkdirAll("features/pages", 0755); err != nil {
		t.Fatal(err)
	}
	write(t, "features/pages/pages.go", bundledPagesMarker+"\npackage pages\n")
	original := read(t, "cmd/server/features.go")
	if err := addPages(); err != nil {
		t.Fatal(err)
	}
	enabled := read(t, "cmd/server/features.go")
	if !strings.Contains(enabled, "r.Register(pages.NewFeature(logger))") || !strings.Contains(enabled, "r.Register(auth.NewFeature(logger))") {
		t.Fatal("missing dependency")
	}
	if err := removeAuth(); err == nil || !strings.Contains(err.Error(), "remove-pages") {
		t.Fatal("Auth removal was not refused")
	}
	if err := removePages(); err != nil {
		t.Fatal(err)
	}
	if err := removeAuth(); err != nil {
		t.Fatal(err)
	}
	if read(t, "cmd/server/features.go") != original {
		t.Fatal("registration not restored")
	}
}

// 管理者認証の提供者は1つだけになるよう入れ替わり、他の機能を足しても通常の管理者認証が戻らないこと。
func TestAdminProviderSwap(t *testing.T) {
	const (
		authImport      = `"minimal-web-kit/features/auth"`
		oidcImport      = `"minimal-web-kit/features/auth/oidcop"`
		federatedImport = `"minimal-web-kit/features/federatedauth"`
	)
	has := func(want string) bool { return strings.Contains(read(t, "cmd/server/features.go"), want) }

	fixture(t, baseRegistration)
	if err := addNews(); err != nil {
		t.Fatal(err)
	}
	if err := addOIDCProvider(); err != nil {
		t.Fatal(err)
	}
	if !has(oidcImport) || !has("oidcop.NewFeature(logger)") || has(authImport) {
		t.Fatalf("provider was not swapped:\n%s", read(t, "cmd/server/features.go"))
	}
	if err := addAuth(); err != nil {
		t.Fatal(err)
	}
	if has(authImport) {
		t.Fatal("auth came back next to the provider")
	}
	if err := addFederatedAuth(); err == nil {
		t.Fatal("a system must not both use and provide login")
	}
	if err := removeOIDCProvider(); err != nil {
		t.Fatal(err)
	}
	if !has(authImport) || has(oidcImport) {
		t.Fatal("removing the provider must keep admin authentication")
	}

	if err := addFederatedAuth(); err != nil {
		t.Fatal(err)
	}
	if !has(federatedImport) || has(authImport) {
		t.Fatalf("consumer was not swapped:\n%s", read(t, "cmd/server/features.go"))
	}
	if err := addOIDCProvider(); err == nil {
		t.Fatal("a system must not both use and provide login")
	}
	if err := removeFederatedAuth(); err == nil || !strings.Contains(err.Error(), "remove-news") {
		t.Fatalf("removing the provider while news is enabled must be refused: %v", err)
	}
	if err := removeNews(); err != nil {
		t.Fatal(err)
	}
	if err := removeFederatedAuth(); err != nil {
		t.Fatal(err)
	}
	if has(federatedImport) {
		t.Fatal("federated login stayed registered")
	}
}
