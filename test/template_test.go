package test

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

var inlineEventHandlerPattern = regexp.MustCompile(`(?i)\bon[a-z]+\s*=`)

// approvedScriptTag は、利用者が承認したWebAuthn用JavaScriptの読み込み方です（AGENTS.mdの承認境界#4）。
const approvedScriptTag = `<script src="/admin/static/webauthn.js" defer></script>`

// allowedScriptTemplates は approvedScriptTag を含められる、管理者認証の画面のテンプレートです。
var allowedScriptTemplates = map[string]bool{
	"admin_setup.html":    true,
	"admin_login.html":    true,
	"admin_passkeys.html": true,
}

// TestJavaScript_OnlyApprovedFile は、リポジトリ内のJavaScriptが承認済みのファイルだけであることを検証します。
func TestJavaScript_OnlyApprovedFile(t *testing.T) {
	var found []string
	err := filepath.WalkDir("..", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == ".git" || d.Name() == "bin" || d.Name() == "data") {
			return filepath.SkipDir
		}
		if !d.IsDir() && (strings.HasSuffix(path, ".js") || strings.HasSuffix(path, ".mjs")) {
			found = append(found, filepath.ToSlash(path))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(found)
	approved := []string{"../features/auth/static/webauthn.js", "../features/catalog/static/catalog.js", "../features/catalog/static/htmx.min.js"}
	if !slices.Equal(found, approved) {
		t.Fatalf("only explicitly approved auth/catalog JavaScript is allowed, found %v", found)
	}
}

// TestTemplate_NoInlineStylesOrScripts は全HTMLテンプレートにインラインstyle属性、タグ、イベントハンドラがないことを検証します。
func TestTemplate_NoInlineStylesOrScripts(t *testing.T) {
	coreFiles, err := filepath.Glob("../internal/web/views/*.html")
	if err != nil {
		t.Fatalf("failed to find core templates: %v", err)
	}

	featureFiles, _ := filepath.Glob("../features/*/views/*.html")
	files := append(coreFiles, featureFiles...)

	if len(files) == 0 {
		t.Fatal("no template files found")
	}

	for _, file := range files {
		content, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("failed to read %s: %v", file, err)
		}
		str := string(content)

		if strings.Contains(str, "style=") {
			t.Errorf("Inline style attribute found in %s. Styles must be defined in style.css.", file)
		}
		if strings.Contains(str, "<style") {
			t.Errorf("<style> tag found in %s. Styles must be defined in style.css.", file)
		}
		// 承認済みの例外: 管理者認証の画面だけ、自サイトの webauthn.js を外部ファイルとして1つ読み込める。
		if allowedScriptTemplates[filepath.Base(file)] && strings.Contains(file, filepath.Join("features", "auth", "views")) {
			if strings.Count(str, "<script") != 1 || strings.Count(str, approvedScriptTag) != 1 {
				t.Errorf("%s may contain only %s", file, approvedScriptTag)
			}
		} else if filepath.ToSlash(file) == "../features/catalog/views/list.html" {
			for _, tag := range []string{`<script src="/catalog/static/htmx.min.js" defer></script>`, `<script src="/catalog/static/catalog.js" defer></script>`} {
				if strings.Count(str, tag) != 1 {
					t.Errorf("missing approved catalog script: %s", tag)
				}
				str = strings.ReplaceAll(str, tag, "")
			}
			if strings.Contains(str, "<script") {
				t.Errorf("unapproved catalog script in %s", file)
			}
		} else if strings.Contains(str, "<script") {
			t.Errorf("<script> tag found in %s. Inline scripts are prohibited for security and simplicity.", file)
		}
		if inlineEventHandlerPattern.MatchString(str) {
			t.Errorf("Inline event handler (on* attribute) found in %s. Inline event handlers violate CSP script-src 'self'. Use server-side links or pages instead.", file)
		}
		if strings.Contains(strings.ToLower(str), "javascript:") {
			t.Errorf("javascript: pseudo-protocol found in %s. Prohibited by CSP.", file)
		}
	}
}

// TestCSS_SingleFileAndNoImportant はCSSファイルが1本のみであり、!importantが使われていないことを検証します。
func TestCSS_SingleFileAndNoImportant(t *testing.T) {
	files, err := filepath.Glob("../internal/web/static/css/*.css")
	if err != nil {
		t.Fatalf("failed to find css files: %v", err)
	}

	if len(files) != 1 {
		t.Errorf("Expected exactly 1 CSS file, but found %d: %v", len(files), files)
	}

	content, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatalf("failed to read css file: %v", err)
	}

	if strings.Contains(string(content), "!important") {
		t.Errorf("!important found in %s. Avoid !important to keep CSS maintainable.", files[0])
	}
}
