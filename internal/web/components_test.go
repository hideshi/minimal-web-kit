package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"minimal-web-kit/internal/web/form"
	"minimal-web-kit/internal/web/page"
)

func renderComponent(t *testing.T, body string, data any) string {
	t.Helper()
	fsys := fstest.MapFS{"page.html": {Data: []byte(`{{define "page"}}` + body + `{{end}}`)}}
	tmpl, err := ParseWithComponents(fsys, "page.html")
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	if err := tmpl.ExecuteTemplate(&b, "page", data); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func TestComponents_NoticeAndError(t *testing.T) {
	if got := renderComponent(t, `{{template "notice" .}}{{template "error" .}}`, ""); strings.TrimSpace(got) != "" {
		t.Fatalf("empty messages must render nothing: %q", got)
	}
	got := renderComponent(t, `{{template "notice" .}}|{{template "error" .}}`, "<b>保存</b>")
	if !strings.Contains(got, `role="status">&lt;b&gt;保存&lt;/b&gt;</p>`) || !strings.Contains(got, `role="alert">&lt;b&gt;保存&lt;/b&gt;</p>`) {
		t.Fatalf("messages must be escaped and announced: %s", got)
	}
}

func TestComponents_Pagination(t *testing.T) {
	p := page.Page{Number: 2, Prev: 1, Next: 3, Base: "/news", Label: "ページ送り", PrevText: "前へ", NextText: "次へ"}
	got := renderComponent(t, `{{template "pagination" .}}`, p)
	for _, want := range []string{`<nav class="pagination" aria-label="ページ送り">`, `href="/news?page=1" rel="prev">前へ</a>`, `href="/news?page=3" rel="next">次へ</a>`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in %s", want, got)
		}
	}
	if got := renderComponent(t, `{{template "pagination" .}}`, page.Page{Number: 1}); strings.TrimSpace(got) != "" {
		t.Fatalf("a single page must render nothing: %q", got)
	}
}

func TestComponents_Field(t *testing.T) {
	// 説明もエラーも無い1行の欄
	got := renderComponent(t, `{{template "field" .}}`, form.Field{ID: "name", Name: "name", Label: "名前"})
	if !strings.Contains(got, `<label for="name">名前</label>`) || !strings.Contains(got, `<input type="text" id="name" name="name" value="">`) {
		t.Fatalf("plain field: %s", got)
	}

	// 説明・エラー・必須・文字数のある欄。説明とエラーを入力欄に関連付ける
	f := form.Field{ID: "title", Name: "title", Label: "タイトル", Hint: "1〜200文字", Error: "入力してください", Value: `"><script>`, MaxLength: 200, Required: true}
	got = renderComponent(t, `{{template "field" .}}`, f)
	for _, want := range []string{
		`（必須）`,
		`<p class="field-hint" id="title-hint">1〜200文字</p>`,
		`<p class="field-error" id="title-error">入力してください</p>`,
		`maxlength="200" required aria-describedby="title-hint title-error" aria-invalid="true">`,
		`value="&#34;&gt;&lt;script&gt;"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in %s", want, got)
		}
	}

	// 複数行・型の指定
	got = renderComponent(t, `{{template "field" .}}`, form.Field{ID: "body", Name: "body", Label: "本文", Rows: 5, Value: "</textarea>"})
	if !strings.Contains(got, `<textarea id="body" name="body" rows="5">&lt;/textarea&gt;</textarea>`) {
		t.Fatalf("textarea: %s", got)
	}
	got = renderComponent(t, `{{template "field" .}}`, form.Field{ID: "at", Name: "at", Label: "日時", Type: "datetime-local"})
	if !strings.Contains(got, `type="datetime-local"`) {
		t.Fatalf("type: %s", got)
	}
}

func TestComponents_FieldSelectHiddenReadonly(t *testing.T) {
	f := form.Field{ID: "cat", Name: "category", Label: "カテゴリ", Hint: "任意", Options: []form.Option{{Value: "", Label: "未分類"}, {Value: "2", Label: "<b>住宅</b>", Selected: true}}}
	got := renderComponent(t, `{{template "field" .}}`, f)
	for _, want := range []string{`<label for="cat">カテゴリ</label>`, `<select id="cat" name="category" aria-describedby="cat-hint">`, `<option value="">未分類</option>`, `<option value="2" selected>&lt;b&gt;住宅&lt;/b&gt;</option>`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in %s", want, got)
		}
	}
	got = renderComponent(t, `{{template "field" .}}`, form.Field{ID: "label-1", Name: "label", Label: "端末名", LabelHidden: true})
	if !strings.Contains(got, `<label for="label-1" class="visually-hidden">端末名</label>`) {
		t.Fatalf("hidden label: %s", got)
	}
	got = renderComponent(t, `{{template "field" .}}`, form.Field{ID: "url", Name: "url", Label: "登録URL", Value: "https://example.com/#t", Readonly: true})
	if !strings.Contains(got, `value="https://example.com/#t" readonly>`) {
		t.Fatalf("readonly: %s", got)
	}
}

func TestComponents_Choices(t *testing.T) {
	c := form.Choices{ID: "role", Name: "role", Legend: "ロール", Hint: "必要な人に限る", Error: "選んでください", Required: true, Options: []form.Option{{Value: "member", Label: "メンバー"}, {Value: "admin", Label: "<i>管理者</i>", Selected: true}}}
	got := renderComponent(t, `{{template "choices" .}}`, c)
	for _, want := range []string{
		`<fieldset class="form-group" aria-describedby="role-hint role-error">`,
		`<legend>ロール<span class="required-mark">（必須）</span></legend>`,
		`<p class="field-error" id="role-error">選んでください</p>`,
		`<label><input type="radio" name="role" value="member" required> メンバー</label>`,
		`<label><input type="radio" name="role" value="admin" checked required> &lt;i&gt;管理者&lt;/i&gt;</label>`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in %s", want, got)
		}
	}
	c.Multiple, c.Error = true, ""
	if got := renderComponent(t, `{{template "choices" .}}`, c); !strings.Contains(got, `type="checkbox" name="role" value="member">`) {
		t.Fatalf("checkbox group: %s", got)
	}
}

func TestComponents_ConfirmActions(t *testing.T) {
	got := renderComponent(t, `{{template "confirm-actions" .}}`, form.Confirm{Action: "/admin/x/1/delete", Back: "/admin/x/1", CSRFToken: `t"ok`})
	for _, want := range []string{
		`<form method="post" action="/admin/x/1/delete" class="inline-form">`,
		`<input type="hidden" name="csrf_token" value="t&#34;ok">`,
		`<button type="submit" class="btn btn-danger">削除する</button>`,
		`<a href="/admin/x/1" class="btn btn-secondary">削除せずに戻る</a>`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in %s", want, got)
		}
	}
	if got := renderComponent(t, `{{template "confirm-actions" .}}`, form.Confirm{Submit: "停止する"}); !strings.Contains(got, ">停止する</button>") {
		t.Fatalf("submit label: %s", got)
	}
}

func TestComponents_Combobox(t *testing.T) {
	field := form.Combobox{ID: "tags", Name: "tag", Label: "タグ<b>", Hint: "20個まで", Limit: 20, Options: []form.Option{
		{Value: "1", Label: "設計"}, {Value: "2 x", Label: "<script>", Selected: true},
	}}
	got := renderComponent(t, `{{template "combobox" .}}`, field)
	for _, want := range []string{
		`data-combobox data-limit="20"`,
		// JSなし用: 全候補のチェックボックスと選択状態
		`<fieldset data-combobox-fallback aria-describedby="tags-hint">`, `<legend>タグ&lt;b&gt;</legend>`,
		`<input type="checkbox" name="tag" value="1"> 設計`, `<input type="checkbox" name="tag" value="2 x" checked> &lt;script&gt;`,
		// JSあり用: 最初は隠し、ARIAの関連付けを持つ
		`role="group" aria-labelledby="tags-title" data-combobox-ui hidden`,
		`<label for="tags-input">タグ&lt;b&gt;を追加</label>`,
		`id="tags-input" role="combobox" aria-autocomplete="list" aria-controls="tags-list" aria-expanded="false" aria-describedby="tags-input-hint"`,
		`id="tags-list" role="listbox"`, `aria-multiselectable="true" hidden`,
		// 候補のidは値ではなく連番
		`id="tags-option-0" role="option" aria-selected="false" data-label="設計"`, `id="tags-option-1" role="option" aria-selected="false" data-label="&lt;script&gt;"`,
		`role="status" aria-live="polite"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("combobox missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "<script>") || strings.Contains(got, "<b>") {
		t.Fatalf("combobox must escape labels: %s", got)
	}
	empty := renderComponent(t, `{{template "combobox" .}}`, form.Combobox{ID: "x", Name: "tag", Label: "タグ"})
	if !strings.Contains(empty, "候補はまだありません。") || strings.Contains(empty, "aria-describedby=\"x-hint\"") {
		t.Fatalf("empty combobox: %s", empty)
	}
}

func TestAllowScripts(t *testing.T) {
	h := SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { AllowScripts(w) }))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if got := w.Header().Get("Content-Security-Policy"); got != strings.Replace(defaultCSP, "script-src 'none'", "script-src 'self'", 1) || got != scriptCSP {
		t.Fatalf("AllowScripts must change only script-src: %q", got)
	}
	// 機能が独自に設定したCSPは変えない。
	w = httptest.NewRecorder()
	w.Header().Set("Content-Security-Policy", "default-src 'none'")
	AllowScripts(w)
	if got := w.Header().Get("Content-Security-Policy"); got != "default-src 'none'" {
		t.Fatalf("custom CSP changed: %q", got)
	}
}

// TestComboboxScript_NoUnsafeAPIs は、部品のJSがHTMLの文字列化・eval・通信・保存・インラインstyleを使わないことを検査します。
func TestComboboxScript_NoUnsafeAPIs(t *testing.T) {
	body, err := contentFS.ReadFile("static/js/combobox.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{".innerHTML", ".outerHTML", "insertAdjacentHTML", "document.write", "eval(", "new Function", "setTimeout(\"", "fetch(", "XMLHttpRequest", "WebSocket", "localStorage", "sessionStorage", "indexedDB", "document.cookie", ".style", ".submit(", "location"} {
		if strings.Contains(string(body), forbidden) {
			t.Fatalf("combobox.js uses %s", forbidden)
		}
	}
}
