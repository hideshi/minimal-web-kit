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
		// 作業用のディレクトリだけを名前で読み飛ばす（.worktrees は別の作業ツリー）。
		// static/* の埋め込みは _ や . で始まるディレクトリも含むため、それらは読み飛ばさない
		if d.IsDir() && (d.Name() == ".git" || d.Name() == ".worktrees" || d.Name() == "bin" || d.Name() == "data") {
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
	approved := []string{"../features/auth/static/webauthn.js", "../features/catalog/static/catalog.js", "../features/catalog/static/htmx.min.js", "../internal/web/static/js/combobox.js"}
	if !slices.Equal(found, approved) {
		t.Fatalf("only explicitly approved auth/catalog JavaScript is allowed, found %v", found)
	}
}

// TestTemplate_StaticAssetsUseAsset は、同梱のテンプレートが /static/ を直接書かず、版付きの URL（共通部品 static-head か
// {{asset "..."}}）で読み込むことを検証します。直接書くと、CSS を更新しても古い版がキャッシュに残るためです。
func TestTemplate_StaticAssetsUseAsset(t *testing.T) {
	files, _ := filepath.Glob("../internal/web/views/*.html")
	featureFiles, _ := filepath.Glob("../features/*/views/*.html")
	direct := regexp.MustCompile(`(?:href|src)="/static/`)
	for _, file := range append(files, featureFiles...) {
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if direct.Match(b) {
			t.Errorf("%s loads /static/ directly; use {{template \"static-head\"}} or {{asset \"...\"}}", file)
		}
		if strings.Contains(file, "layout") && !strings.Contains(string(b), `{{template "static-head"}}`) {
			t.Errorf("%s must load CSS and icons with {{template \"static-head\"}}", file)
		}
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
		} else if filepath.ToSlash(file) == "../features/catalog/views/admin_form.html" {
			// 承認済みの例外: 項目の作成・編集画面だけ、Coreの部品 combobox.js を1つ読み込める（版付きの URL）。
			tag := `<script src="{{asset "js/combobox.js"}}" defer></script>`
			if strings.Count(str, "<script") != 1 || strings.Count(str, tag) != 1 {
				t.Errorf("%s may contain only %s", file, tag)
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

// formControlExceptions は、入力欄を部品（field・choices・combobox）を使わずに直接書いてよいテンプレートと理由です。
var formControlExceptions = map[string]string{}

var thPattern = regexp.MustCompile(`<th[\s>]`)

// formControlTagExceptions は、ファイルの中で直接書いてよいタグだけを、理由つきで書きます（ファイル全体は例外にしない）。
var formControlTagExceptions = map[string][]string{
	// お問い合わせのスパム対策の見えない入力欄（ハニーポット）。部品の field は aria-hidden・tabindex を持たない（docs/contact-plan.md 4.1）
	"../features/contact/views/contact.html": {
		`<label for="contact-trap">`,
		`<input type="text" id="contact-trap" name="{{.Trap}}" value="" tabindex="-1" autocomplete="off">`,
	},
}

var formControlPattern = regexp.MustCompile(`<(label|input|select|textarea)\b[^>]*>`)

// TestTemplate_FormControlsUseComponents は、機能のテンプレートが入力欄を直接書かず、共通の部品を使うことを検証します
// （docs/admin-ui-standard-plan.md 6.1）。値を送るだけの hidden の入力は除きます。
func TestTemplate_FormControlsUseComponents(t *testing.T) {
	files, err := filepath.Glob("../features/*/views/*.html")
	if err != nil || len(files) == 0 {
		t.Fatal("no feature templates", err)
	}
	for _, f := range files {
		if _, ok := formControlExceptions[f]; ok {
			continue
		}
		allowed := formControlTagExceptions[f]
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range formControlPattern.FindAllString(string(raw), -1) {
			if strings.HasPrefix(m, "<input") && strings.Contains(m, `type="hidden"`) || slices.Contains(allowed, m) {
				continue
			}
			t.Errorf("%s: %s を直接書いている（field・choices 等の部品を使う。例外は formControlExceptions に理由つきで書く）", f, m)
		}
	}
}

// TestTemplate_TablesHaveCaptionAndScope は、全テンプレートの表に caption と th の scope があることを検証します。
func TestTemplate_TablesHaveCaptionAndScope(t *testing.T) {
	core, _ := filepath.Glob("../internal/web/views/*.html")
	feature, _ := filepath.Glob("../features/*/views/*.html")
	for _, f := range append(core, feature...) {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		src := string(raw)
		if n := strings.Count(src, "<table"); n != strings.Count(src, "<caption") {
			t.Errorf("%s: table %d 個に対して caption が %d 個", f, n, strings.Count(src, "<caption"))
		}
		if n := len(thPattern.FindAllString(src, -1)); n != strings.Count(src, "<th scope=") {
			t.Errorf("%s: scope の無い th がある", f)
		}
	}
}
