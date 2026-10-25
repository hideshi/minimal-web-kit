package main

import (
	"bytes"
	"fmt"
	"go/ast"
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
		fmt.Println("Successfully enabled LP/site template! Run 'go test ./...' or 'go run ./cmd/server' to try it.")
	case "remove-site":
		if err := removeSite(); err != nil {
			fmt.Fprintf(os.Stderr, "Error disabling site feature: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Successfully disabled LP/site template! Site registration is disabled; feature directories were not modified.")
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
	fmt.Println("  add-site     Enable LP / homepage template module in cmd/server/features.go")
	fmt.Println("  remove-site  Disable LP / homepage template registration")
}

const bundledSiteMarker = "// minimal-web-kit: bundled-site-template"

func addSite() error    { return toggleSite(true) }
func removeSite() error { return toggleSite(false) }

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

func toggleSite(enable bool) error {
	for _, dir := range []string{"features", "features/site", "cmd", "cmd/server"} {
		if err := regularPath(dir, true); err != nil {
			return err
		}
	}
	if err := regularPath("features/site/site.go", false); err != nil {
		return err
	}
	siteSource, err := os.ReadFile("features/site/site.go")
	if err != nil {
		return err
	}
	if !bytes.HasPrefix(siteSource, []byte(bundledSiteMarker+"\n")) {
		return fmt.Errorf("features/site is not identified as the bundled Site template; refusing to modify registration")
	}
	siteFile, err := parser.ParseFile(token.NewFileSet(), "features/site/site.go", siteSource, 0)
	if err != nil || siteFile.Name.Name != "site" {
		return fmt.Errorf("bundled Site source is invalid")
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

	const siteImport = "minimal-web-kit/features/site"
	imports := 0
	for _, imp := range file.Imports {
		value, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			return err
		}
		if value == siteImport {
			if imp.Name != nil {
				return fmt.Errorf("aliased Site import is not supported; edit registration manually")
			}
			imports++
		} else if imp.Name != nil && imp.Name.Name == "site" {
			return fmt.Errorf("import name site is already used by another feature")
		}
		if value != siteImport && imp.Name == nil && filepath.Base(value) == "site" {
			return fmt.Errorf("import name site conflicts with another feature")
		}
	}
	importLine := regexp.MustCompile(`(?m)^[\t ]*"minimal-web-kit/features/site"\r?\n`)
	registrationLine := regexp.MustCompile(`(?m)^[\t ]*r\.Register\(site\.NewFeature\(logger\)\)\r?\n`)
	registrations := len(registrationLine.FindAllStringIndex(content, -1))
	selectors := 0
	ast.Inspect(file, func(node ast.Node) bool {
		if selector, ok := node.(*ast.SelectorExpr); ok {
			if id, ok := selector.X.(*ast.Ident); ok && id.Name == "site" {
				selectors++
			}
		}
		return true
	})
	setupRegistrations := 0
	for _, stmt := range setup.Body.List {
		if strings.TrimSpace(content[fset.Position(stmt.Pos()).Offset:fset.Position(stmt.End()).Offset]) == "r.Register(site.NewFeature(logger))" {
			setupRegistrations++
		}
	}
	if setupRegistrations != registrations || imports > 1 || registrations > 1 || imports != registrations || selectors != registrations || len(importLine.FindAllStringIndex(content, -1)) != imports {
		return fmt.Errorf("unexpected Site registration; refusing to rewrite custom code")
	}
	if (enable && imports == 1) || (!enable && imports == 0) {
		return nil
	}
	if enable {
		// 置換はコメント単独行に限定する。コード中の文字列は置換しない。
		for marker, replacement := range map[string]string{
			"FEATURE_IMPORTS":       `"minimal-web-kit/features/site"`,
			"FEATURE_REGISTRATIONS": "r.Register(site.NewFeature(logger))",
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
	return replaceRegistration(path, original, []byte(content))
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
