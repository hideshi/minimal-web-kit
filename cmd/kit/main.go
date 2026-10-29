package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		return
	}

	cmd := os.Args[1]
	switch cmd {
	case "add-site":
		if err := addSite(); err != nil {
			fmt.Fprintf(os.Stderr, "Error enabling site feature: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Successfully enabled Site (LP template) feature! Run 'go test ./...' or 'go run ./cmd/server' to try it.")
	case "remove-site":
		if err := removeSite(); err != nil {
			fmt.Fprintf(os.Stderr, "Error disabling site feature: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Successfully disabled Site (LP template) feature! Site registration is disabled; feature directories were not modified.")
	case "add-auth":
		if err := addAuth(); err != nil {
			fmt.Fprintf(os.Stderr, "Error enabling auth feature: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Successfully enabled admin authentication (passkey) feature! Run 'make auth-setup' to register the first passkey.")
	case "remove-auth":
		if err := removeAuth(); err != nil {
			fmt.Fprintf(os.Stderr, "Error disabling auth feature: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Successfully disabled admin authentication feature! Stored passkeys were not modified.")
	case "add-catalog":
		if err := addCatalog(); err != nil {
			fmt.Fprintf(os.Stderr, "Error enabling catalog feature: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Successfully enabled Catalog with admin authentication.")
	case "remove-catalog":
		if err := removeCatalog(); err != nil {
			fmt.Fprintf(os.Stderr, "Error disabling catalog feature: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Catalog registration disabled; stored data is kept.")
	case "add-contact":
		if err := addContact(); err != nil {
			fmt.Fprintf(os.Stderr, "Error enabling contact feature: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Successfully enabled the contact form with admin authentication.")
	case "remove-contact":
		if err := removeContact(); err != nil {
			fmt.Fprintf(os.Stderr, "Error disabling contact feature: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Contact registration disabled; stored messages are kept until removed from the database.")
	case "add-news":
		if err := addNews(); err != nil {
			fmt.Fprintf(os.Stderr, "Error enabling news feature: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Successfully enabled the news feature together with admin authentication! Run 'go test ./...' or 'go run ./cmd/server' to try it.")
	case "remove-news":
		if err := removeNews(); err != nil {
			fmt.Fprintf(os.Stderr, "Error disabling news feature: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Successfully disabled the news feature! News registration is disabled; feature directories and stored data were not modified.")
	default:
		printUsage()
	}
}

func printUsage() {
	fmt.Println("minimal-web-kit CLI tool")
	fmt.Println()
	fmt.Println("Usage:")
	fmt.Println("  go run ./cmd/kit <command>")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  add-site     Enable the Site (LP template) feature in cmd/server/features.go")
	fmt.Println("  remove-site  Disable the Site (LP template) feature registration")
	fmt.Println("  add-auth     Enable the admin authentication (passkey) feature")
	fmt.Println("  remove-auth  Disable admin authentication (refused while news, catalog or contact is enabled; stored passkeys are kept)")
	fmt.Println("  add-catalog  Enable Catalog with admin authentication")
	fmt.Println("  remove-catalog Disable Catalog registration (stored data is kept)")
	fmt.Println("  add-contact  Enable the contact form and inbox, together with admin authentication")
	fmt.Println("  remove-contact Disable the contact registration (stored messages are kept)")
	fmt.Println("  add-news     Enable the news feature, together with admin authentication")
	fmt.Println("  remove-news  Disable the news feature registration (stored data is kept)")
	fmt.Println()
	fmt.Println("Passkey setup and recovery are subcommands of the server binary:")
	fmt.Println("  go run ./cmd/server auth-setup   (or: ./bin/minimal-web-kit auth-setup)")
}

// bundle は登録を切り替えられる同梱機能です。
// ディレクトリ features/<name>/、ソース <name>.go、識別コメント marker を持ちます。
type bundle struct {
	name   string
	label  string
	marker string
}

const (
	bundledSiteMarker    = "// minimal-web-kit: bundled-feature site"
	bundledNewsMarker    = "// minimal-web-kit: bundled-feature news"
	bundledCatalogMarker = "// minimal-web-kit: bundled-feature catalog"
	bundledAuthMarker    = "// minimal-web-kit: bundled-feature auth"
	bundledContactMarker = "// minimal-web-kit: bundled-feature contact"
)

var (
	siteBundle    = bundle{name: "site", label: "Site", marker: bundledSiteMarker}
	newsBundle    = bundle{name: "news", label: "News", marker: bundledNewsMarker}
	catalogBundle = bundle{name: "catalog", label: "Catalog", marker: bundledCatalogMarker}
	authBundle    = bundle{name: "auth", label: "Auth", marker: bundledAuthMarker}
	contactBundle = bundle{name: "contact", label: "Contact", marker: bundledContactMarker}
)

func addSite() error               { return toggleSite(true) }
func removeSite() error            { return toggleSite(false) }
func toggleSite(enable bool) error { return toggleBundle(siteBundle, enable) }
func addAuth() error               { return toggleBundle(authBundle, true) }

// addNews は、お知らせ機能が使う管理者認証も一緒に有効にします。お知らせ機能のソースを先に確認し、拒否する場合は何も変えません。
func addNews() error {
	if err := verifyBundle(newsBundle); err != nil {
		return err
	}
	if err := toggleBundle(authBundle, true); err != nil {
		return err
	}
	return toggleBundle(newsBundle, true)
}

// addCatalog は、カタログと管理者認証の登録を有効にします。
func addCatalog() error {
	if err := verifyBundle(catalogBundle); err != nil {
		return err
	}
	if err := toggleBundle(authBundle, true); err != nil {
		return err
	}
	return toggleBundle(catalogBundle, true)
}
func removeCatalog() error { return toggleBundle(catalogBundle, false) }

// addContact は、お問い合わせ機能と管理者認証の登録を有効にします。
func addContact() error {
	if err := verifyBundle(contactBundle); err != nil {
		return err
	}
	if err := toggleBundle(authBundle, true); err != nil {
		return err
	}
	return toggleBundle(contactBundle, true)
}
func removeContact() error { return toggleBundle(contactBundle, false) }

// removeAuth は、News・Catalog・Contactのどれかが有効な間は拒否します。
func removeAuth() error {
	registration, err := os.ReadFile("cmd/server/features.go")
	if err != nil {
		return err
	}
	if strings.Contains(string(registration), strconv.Quote("minimal-web-kit/features/"+newsBundle.name)) {
		return fmt.Errorf("news uses admin authentication; run remove-news first")
	}
	if strings.Contains(string(registration), strconv.Quote("minimal-web-kit/features/"+catalogBundle.name)) {
		return fmt.Errorf("catalog uses admin authentication; run remove-catalog first")
	}
	if strings.Contains(string(registration), strconv.Quote("minimal-web-kit/features/"+contactBundle.name)) {
		return fmt.Errorf("contact uses admin authentication; run remove-contact first")
	}
	return toggleBundle(authBundle, false)
}
func removeNews() error { return toggleBundle(newsBundle, false) }

// 実体を確認できない同名ディレクトリと、シンボリックリンクは操作しない。
func regularPath(path string, directory bool) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect %s: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || (directory && !info.IsDir()) || (!directory && !info.Mode().IsRegular()) {
		return fmt.Errorf("%s must be a regular %s, not a symlink", path, map[bool]string{true: "directory", false: "file"}[directory])
	}
	return nil
}

// verifyBundle は、features/<name>/ が同梱機能の元のソースとして識別できるかを確認します。
func verifyBundle(b bundle) error {
	dir := "features/" + b.name
	sourcePath := dir + "/" + b.name + ".go"
	for _, d := range []string{"features", dir, "cmd", "cmd/server"} {
		if err := regularPath(d, true); err != nil {
			return err
		}
	}
	if err := regularPath(sourcePath, false); err != nil {
		return err
	}
	source, err := os.ReadFile(sourcePath)
	if err != nil {
		return err
	}
	if !bytes.HasPrefix(source, []byte(b.marker+"\n")) {
		return fmt.Errorf("%s is not identified as the bundled %s feature; refusing to modify registration", dir, b.label)
	}
	sourceFile, err := parser.ParseFile(token.NewFileSet(), sourcePath, source, 0)
	if err != nil || sourceFile.Name.Name != b.name {
		return fmt.Errorf("bundled %s source is invalid", b.label)
	}

	return nil
}

func toggleBundle(b bundle, enable bool) error {
	if err := verifyBundle(b); err != nil {
		return err
	}
	const path = "cmd/server/features.go"
	if err := regularPath(path, false); err != nil {
		return err
	}
	original, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	content := string(original)
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, original, parser.ParseComments)
	if err != nil {
		return fmt.Errorf("parse registration: %w", err)
	}

	// マーカーはコメントとして一意で、importブロックとsetupFeatures内にあること。
	markers := map[string]token.Pos{}
	for _, group := range file.Comments {
		for _, comment := range group.List {
			if comment.Text == "// [FEATURE_IMPORTS]" || comment.Text == "// [FEATURE_REGISTRATIONS]" {
				if markers[comment.Text] != token.NoPos {
					return fmt.Errorf("duplicate registration marker")
				}
				markers[comment.Text] = comment.Pos()
			}
		}
	}
	importMarker := markers["// [FEATURE_IMPORTS]"]
	registrationMarker := markers["// [FEATURE_REGISTRATIONS]"]
	validImport, validRegistration := false, false
	var setup *ast.FuncDecl
	for _, decl := range file.Decls {
		if d, ok := decl.(*ast.GenDecl); ok && d.Tok == token.IMPORT && d.Lparen < importMarker && importMarker < d.Rparen {
			validImport = true
		}
		if d, ok := decl.(*ast.FuncDecl); ok && d.Name.Name == "setupFeatures" && d.Body != nil && d.Body.Lbrace < registrationMarker && registrationMarker < d.Body.Rbrace {
			if setup != nil {
				return fmt.Errorf("multiple setupFeatures declarations")
			}
			setup = d
			validRegistration = true
		}
	}
	if !validImport || !validRegistration {
		return fmt.Errorf("missing or misplaced registration markers")
	}

	importPath := "minimal-web-kit/features/" + b.name
	registration := "r.Register(" + b.name + ".NewFeature(logger))"
	imports := 0
	for _, imp := range file.Imports {
		value, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			return err
		}
		if value == importPath {
			if imp.Name != nil {
				return fmt.Errorf("aliased %s import is not supported; edit registration manually", b.label)
			}
			imports++
		} else if imp.Name != nil && imp.Name.Name == b.name {
			return fmt.Errorf("import name %s is already used by another feature", b.name)
		}
		if value != importPath && imp.Name == nil && filepath.Base(value) == b.name {
			return fmt.Errorf("import name %s conflicts with another feature", b.name)
		}
	}
	importLine := regexp.MustCompile(`(?m)^[\t ]*` + regexp.QuoteMeta(strconv.Quote(importPath)) + `\r?\n`)
	registrationLine := regexp.MustCompile(`(?m)^[\t ]*` + regexp.QuoteMeta(registration) + `\r?\n`)
	registrations := len(registrationLine.FindAllStringIndex(content, -1))
	selectors := 0
	ast.Inspect(file, func(node ast.Node) bool {
		if selector, ok := node.(*ast.SelectorExpr); ok {
			if id, ok := selector.X.(*ast.Ident); ok && id.Name == b.name {
				selectors++
			}
		}
		return true
	})
	setupRegistrations := 0
	for _, stmt := range setup.Body.List {
		if strings.TrimSpace(content[fset.Position(stmt.Pos()).Offset:fset.Position(stmt.End()).Offset]) == registration {
			setupRegistrations++
		}
	}
	if setupRegistrations != registrations || imports > 1 || registrations > 1 || imports != registrations || selectors != registrations || len(importLine.FindAllStringIndex(content, -1)) != imports {
		return fmt.Errorf("unexpected %s registration; refusing to rewrite custom code", b.label)
	}
	if (enable && imports == 1) || (!enable && imports == 0) {
		return nil
	}
	if enable {
		// 置換はコメント単独行に限定する。コード中の文字列は置換しない。
		for marker, replacement := range map[string]string{
			"FEATURE_IMPORTS":       strconv.Quote(importPath),
			"FEATURE_REGISTRATIONS": registration,
		} {
			pattern := regexp.MustCompile(`(?m)^[\t ]*// \[` + marker + `\]\r?$`)
			if len(pattern.FindAllStringIndex(content, -1)) != 1 {
				return fmt.Errorf("registration marker must occupy its own line")
			}
			content = pattern.ReplaceAllStringFunc(content, func(line string) string {
				indent := line[:strings.Index(line, "//")]
				return indent + replacement + "\n" + line
			})
		}
	} else {
		content = importLine.ReplaceAllString(content, "")
		content = registrationLine.ReplaceAllString(content, "")
	}
	if _, err := parser.ParseFile(token.NewFileSet(), path, content, 0); err != nil {
		return fmt.Errorf("invalid updated registration: %w", err)
	}
	updated := []byte(content)
	// 元のファイルがgofmt済みなら、追加したimportの並び順も含めてgofmt済みに保つ。
	// 整形されていないファイルは、利用者のコードの見た目を変えないよう、そのままにする。
	if formatted, err := format.Source(original); err == nil && bytes.Equal(formatted, original) {
		if updated, err = format.Source(updated); err != nil {
			return fmt.Errorf("format updated registration: %w", err)
		}
	}
	return replaceRegistration(path, original, updated)
}

// 完成した内容を同じディレクトリの一時ファイルから置換する。
func replaceRegistration(path string, original, updated []byte) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".features-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if err := tmp.Chmod(info.Mode().Perm()); err != nil {
		return err
	}
	if _, err := tmp.Write(updated); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := regularPath(path, false); err != nil {
		return err
	}
	current, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if !bytes.Equal(current, original) {
		return fmt.Errorf("registration changed during operation; retry after reviewing changes")
	}
	return os.Rename(tmp.Name(), path)
}
