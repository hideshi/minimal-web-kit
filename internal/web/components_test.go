package web

import (
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
