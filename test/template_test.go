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
	approved := []string{"../features/auth/static/webauthn.js", "../features/catalog/static/catalog.js", "../features/catalog/static/htmx.min.js", "../internal/web/static/js/analytics.js", "../internal/web/static/js/combobox.js"}
	if !slices.Equal(found, approved) {
		t.Fatalf("only explicitly approved auth/catalog JavaScript is allowed, found %v", found)
	}
}

// TestTemplate_StaticAssetsUseAsset は、同梱のテンプレートが /static/ を直接書かず、版付きの URL（共通部品 static-head か
// {{asset "..."}}）で読み込むことを検証します。直接書くと、CSS を更新しても古い版がキャッシュに残るためです。
func TestTemplate_StaticAssetsUseAsset(t *testing.T) {
	files, _ := filepath.Glob("../internal/web/views/*.html")
	featureFiles, _ := featureViews()
	direct := regexp.MustCompile(`(?:href|src)="/static/`)
	for _, file := range append(files, featureFiles...) {
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if direct.Match(b) {
			t.Errorf("%s loads /static/ directly; use {{template \"static-head\"}} or {{asset \"...\"}}", file)
		}
	}
}

// TestTemplate_NoInlineStylesOrScripts は全HTMLテンプレートにインラインstyle属性、タグ、イベントハンドラがないことを検証します。
func TestTemplate_NoInlineStylesOrScripts(t *testing.T) {
	coreFiles, err := filepath.Glob("../internal/web/views/*.html")
	if err != nil {
		t.Fatalf("failed to find core templates: %v", err)
	}

	featureFiles, _ := featureViews()
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
			for _, tag := range []string{`<script src="{{.HTMXURL}}" defer></script>`, `<script src="{{.CatalogScriptURL}}" defer></script>`} {
				if strings.Count(str, tag) != 1 {
					t.Errorf("missing approved catalog script: %s", tag)
				}
				str = strings.ReplaceAll(str, tag, "")
			}
			if strings.Contains(str, "<script") {
				t.Errorf("unapproved catalog script in %s", file)
			}
		} else if filepath.Base(file) == "components.html" {
			// 承認済みの例外: アクセス解析の部品だけ、自サイトの analytics.js を1つ読み込める
			tag := `<script src="{{asset "js/analytics.js"}}" data-ga4-id="{{.GA4ID}}" defer></script>`
			if strings.Count(str, "<script") != 1 || strings.Count(str, tag) != 1 {
				t.Errorf("%s may contain only %s", file, tag)
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
	// お問い合わせのスパム対策の見えない入力欄（ハニーポット）。部品の field は aria-hidden・tabindex を持たない（docs/plans/contact-plan.md 4.1）
	"../features/contact/views/contact.html": {
		`<label for="contact-trap">`,
		`<input type="text" id="contact-trap" name="{{.Trap}}" value="" tabindex="-1" autocomplete="off">`,
	},
}

var formControlPattern = regexp.MustCompile(`<(label|input|select|textarea)\b[^>]*>`)

// TestTemplate_FormControlsUseComponents は、機能のテンプレートが入力欄を直接書かず、共通の部品を使うことを検証します
// （docs/plans/admin-ui-standard-plan.md 6.1）。値を送るだけの hidden の入力は除きます。
func TestTemplate_FormControlsUseComponents(t *testing.T) {
	files, err := featureViews()
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

// TestForm_OptionalMarkUsesField は、ラベルの文字に「（任意）」を直接書かず、form.Field の Optional を使うことを検証します。
// 直接書くと、部品が描く「（必須）」と見た目がそろいません。
func TestForm_OptionalMarkUsesField(t *testing.T) {
	files, err := filepath.Glob("../features/*/*.go")
	if err != nil || len(files) == 0 {
		t.Fatal("no feature sources", err)
	}
	pattern := regexp.MustCompile(`Label:\s*"[^"]*（任意）`)
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if m := pattern.Find(raw); m != nil {
			t.Errorf("%s: %s を直接書いている（Optional: true を使う）", f, m)
		}
	}
}

// TestTemplate_TablesHaveCaptionAndScope は、全テンプレートの表に caption と th の scope があることを検証します。
func TestTemplate_TablesHaveCaptionAndScope(t *testing.T) {
	core, _ := filepath.Glob("../internal/web/views/*.html")
	feature, _ := featureViews()
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

// featureViews は、機能のテンプレートを返します（features/auth/oidcop のような入れ子の機能も含む）。
func featureViews() ([]string, error) {
	files, err := filepath.Glob("../features/*/views/*.html")
	if err != nil {
		return nil, err
	}
	nested, err := filepath.Glob("../features/*/*/views/*.html")
	return append(files, nested...), err
}

// allViews は、機能とCoreの全部のテンプレートを返します。
func allViews() ([]string, error) {
	files, err := featureViews()
	if err != nil {
		return nil, err
	}
	core, err := filepath.Glob("../internal/web/views/*.html")
	return append(files, core...), err
}

// TestTemplate_LayoutsUseShell は、同梱のレイアウトが自分で文書の骨組みを書かず、枠の部品を使うことを検証します。
// ヘッダー・フッター・スキップリンクを1か所で直せるようにします。
func TestTemplate_LayoutsUseShell(t *testing.T) {
	patterns := []string{"../internal/web/views/*layout.html"}
	for _, b := range collectBundles(t) { // 自作機能のレイアウトは、枠を使わなくてもよい
		patterns = append(patterns, "../features/"+b.dir+"/views/*layout.html")
	}
	var files []string
	for _, pattern := range patterns {
		matched, err := filepath.Glob(pattern)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, matched...)
	}
	if len(files) < 4 {
		t.Fatalf("layouts not found: %v", files)
	}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if problem := layoutProblem(string(raw)); problem != "" {
			t.Errorf("%s: %s", f, problem)
		}
	}
	for _, bad := range []string{`{{define "layout"}}<!DOCTYPE html>{{end}}`, `{{define "layout"}}<header></header>{{end}}`} {
		if layoutProblem(bad) == "" {
			t.Errorf("not detected: %s", bad)
		}
	}

	raw, err := os.ReadFile("../internal/web/views/components.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, shell := range []string{"public-shell", "admin-shell"} {
		_, body, ok := strings.Cut(string(raw), `{{define "`+shell+`"}}`)
		if !ok || !strings.Contains(body, `{{template "static-head"}}`) {
			t.Errorf("%s must load CSS and icons with {{template \"static-head\"}}", shell)
		}
	}
}

func layoutProblem(source string) string {
	if !strings.Contains(source, `{{template "public-shell" .}}`) && !strings.Contains(source, `{{template "admin-shell" .}}`) {
		return "レイアウトは public-shell か admin-shell を呼ぶ"
	}
	if strings.Contains(source, "<html") || strings.Contains(source, "<header") || strings.Contains(source, "<body") {
		return "骨組みは枠の部品に置く"
	}
	return ""
}

// TestTemplate_SingleButtonFormsUseComponent は、ボタン1つのPOSTフォームを直書きせず、部品を使うことを検証します。
// inline-form を書けるのは、部品（post-button・confirm-actions）の定義だけです。
func TestTemplate_SingleButtonFormsUseComponent(t *testing.T) {
	files, err := allViews()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no templates found")
	}
	for _, f := range files {
		if filepath.Base(f) == "components.html" {
			continue // 部品の定義は下で確かめる
		}
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if directInlineForm(string(raw)) {
			t.Errorf("%s: ボタン1つのフォームは post-button を使う", f)
		}
	}
	raw, err := os.ReadFile("../internal/web/views/components.html")
	if err != nil {
		t.Fatal(err)
	}
	_, shells, ok := strings.Cut(string(raw), `{{define "public-shell"}}`)
	if !ok {
		t.Fatal("public-shell not found")
	}
	if directInlineForm(shells) || !strings.Contains(shells, `{{template "post-button" (postButton "/admin/logout"`) {
		t.Error("枠の中のフォームも post-button を使う")
	}
	if !directInlineForm(`<form method="post" action="/x" class="inline-form"></form>`) {
		t.Error("direct form was not detected")
	}
}

func directInlineForm(source string) bool {
	return regexp.MustCompile(`<form\b[^>]*\binline-form\b`).MatchString(source)
}

// TestTemplate_BackLinkWording は、戻るリンクの文言を「〜へ戻る」か「〜ずに戻る」にそろえます。
func TestTemplate_BackLinkWording(t *testing.T) {
	files, err := allViews()
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, text := range backLinkTexts(string(raw)) {
			checked++
			if !backLinkOK(text) {
				t.Errorf("%s: 戻るリンクは「〜へ戻る」にする: %s", f, text)
			}
		}
	}
	if checked < 10 {
		t.Fatalf("back links not found: %d", checked)
	}
	if got := backLinkTexts(`<a href="/x">一覧に戻る</a>`); len(got) != 1 || backLinkOK(got[0]) {
		t.Errorf("not detected: %v", got)
	}
}

func backLinkTexts(source string) []string {
	var texts []string
	for _, m := range regexp.MustCompile(`<a\b[^>]*>([^<]*戻る)</a>`).FindAllStringSubmatch(source, -1) {
		texts = append(texts, m[1])
	}
	return texts
}

func backLinkOK(text string) bool {
	return strings.HasSuffix(text, "へ戻る") || strings.HasSuffix(text, "ずに戻る")
}

// TestTemplate_TableFormsAreInline は、表の中のフォームに inline-form を求めます。無いとフォームの下余白でボタンが上に寄ります。
func TestTemplate_TableFormsAreInline(t *testing.T) {
	files, err := filepath.Glob("../features/*/views/*.html")
	if err != nil {
		t.Fatal(err)
	}
	cell := regexp.MustCompile(`(?s)<td[^>]*>.*?</td>`)
	form := regexp.MustCompile(`<form\b[^>]*>`)
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, td := range cell.FindAllString(string(raw), -1) {
			for _, tag := range form.FindAllString(td, -1) {
				// label-form は入力欄付きの部品で、自分の余白を持つ
				if !strings.Contains(tag, "inline-form") && !strings.Contains(tag, "label-form") {
					t.Errorf("%s: 表の中のフォームに inline-form が無い: %s", f, tag)
				}
			}
		}
	}
}
