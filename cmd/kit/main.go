package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"minimal-web-kit/features/cms"
	"minimal-web-kit/internal/store"
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
	case "add-cms":
		if err := addCMS(); err != nil {
			fmt.Fprintf(os.Stderr, "Error enabling cms feature: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Successfully enabled CMS template! Run 'go test ./...' or 'go run ./cmd/server' to try it.")
	case "remove-cms":
		if err := removeCMS(); err != nil {
			fmt.Fprintf(os.Stderr, "Error disabling cms feature: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Successfully disabled CMS template! CMS registration is disabled; feature directories and stored data were not modified.")
	case "cms-setup":
		if err := cmsSetup(os.Stdout); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	case "cms-recover":
		if err := cmsRecover(os.Stdin, os.Stdout, slices.Contains(os.Args[2:], "-yes")); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
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
	fmt.Println("  add-cms      Enable CMS template module in cmd/server/features.go")
	fmt.Println("  remove-cms   Disable CMS template registration (stored data is kept)")
	fmt.Println("  cms-setup    Print a one-time URL to register the first CMS passkey")
	fmt.Println("  cms-recover  Delete all CMS passkeys and admin sessions, then print a new registration URL")
}

// openCMSDB はサーバーと同じ DB_PATH（既定は data/app.db）のSQLiteを開きます。
func openCMSDB() (*store.SQLiteStore, cms.Config, error) {
	cfg, err := cms.LoadConfig(os.Getenv)
	if err != nil {
		return nil, cms.Config{}, err
	}
	dbPath := os.Getenv("DB_PATH")
	if dbPath == "" {
		dbPath = "data/app.db"
	}
	s, err := store.NewSQLiteStore(dbPath)
	if err != nil {
		return nil, cms.Config{}, err
	}
	return s, cfg, nil
}

func printSetupURL(out io.Writer, origin, token string) {
	fmt.Fprintln(out, "Open this URL within 15 minutes and register a passkey (synced passkeys are rejected):")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "  "+cms.SetupURL(origin, token))
	fmt.Fprintln(out)
	fmt.Fprintln(out, "The URL can be used only once. Do not share it, paste it into chats, or leave it on a shared screen.")
}

// cmsSetup は、パスキーが未登録の場合だけ、初回登録用のURLを表示します。
func cmsSetup(out io.Writer) error {
	s, cfg, err := openCMSDB()
	if err != nil {
		return err
	}
	defer s.Close()
	token, err := cms.Setup(context.Background(), s.DB(), time.Now())
	if errors.Is(err, cms.ErrAlreadySetUp) {
		return fmt.Errorf("passkeys are already registered; add passkeys from the admin screen, or run cms-recover if you lost them")
	} else if err != nil {
		return err
	}
	printSetupURL(out, cfg.Origin, token)
	return nil
}

// cmsRecover は確認のうえで、パスキー・管理者セッション・登録トークンを削除し、新しい登録URLを表示します。
func cmsRecover(in io.Reader, out io.Writer, yes bool) error {
	if !yes {
		fmt.Fprint(out, "This deletes ALL registered CMS passkeys and admin sessions. Posts are kept.\nType 'recover' to continue: ")
		line, _ := bufio.NewReader(in).ReadString('\n')
		if strings.TrimSpace(line) != "recover" {
			return fmt.Errorf("aborted; nothing was changed")
		}
	}
	s, cfg, err := openCMSDB()
	if err != nil {
		return err
	}
	defer s.Close()
	token, err := cms.Recover(context.Background(), s.DB(), time.Now())
	if err != nil {
		return err
	}
	fmt.Fprintln(out, "All passkeys and admin sessions were deleted.")
	printSetupURL(out, cfg.Origin, token)
	return nil
}

// bundle は登録を切り替えられる同梱テンプレートです。
// ディレクトリ features/<name>/、ソース <name>.go、識別コメント marker を持ちます。
type bundle struct {
	name   string
	label  string
	marker string
}

const (
	bundledSiteMarker = "// minimal-web-kit: bundled-site-template"
	bundledCMSMarker  = "// minimal-web-kit: bundled-cms-template"
)

var (
	siteBundle = bundle{name: "site", label: "Site", marker: bundledSiteMarker}
	cmsBundle  = bundle{name: "cms", label: "CMS", marker: bundledCMSMarker}
)

func addSite() error               { return toggleSite(true) }
func removeSite() error            { return toggleSite(false) }
func toggleSite(enable bool) error { return toggleBundle(siteBundle, enable) }
func addCMS() error                { return toggleBundle(cmsBundle, true) }
func removeCMS() error             { return toggleBundle(cmsBundle, false) }

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

func toggleBundle(b bundle, enable bool) error {
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
		return fmt.Errorf("%s is not identified as the bundled %s template; refusing to modify registration", dir, b.label)
	}
	sourceFile, err := parser.ParseFile(token.NewFileSet(), sourcePath, source, 0)
	if err != nil || sourceFile.Name.Name != b.name {
		return fmt.Errorf("bundled %s source is invalid", b.label)
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
