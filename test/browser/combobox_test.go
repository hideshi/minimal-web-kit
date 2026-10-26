package browser

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/chromedp/chromedp"

	"minimal-web-kit/internal/web"
	"minimal-web-kit/internal/web/form"
)

// componentPage は、部品を2つ置いた画面です。送信すると、受け取った値を表示するだけで保存しません。
const componentPage = `{{define "page"}}<!DOCTYPE html><html lang="ja"><head><meta charset="UTF-8"><title>combobox</title>
<link rel="stylesheet" href="/static/css/style.css"><script src="/static/js/combobox.js" defer></script></head>
<body><main><h1 id="heading">部品の確認</h1>
<form method="post" action="/result">{{template "combobox" .A}}{{template "combobox" .B}}<button type="submit" id="submit">送信する</button></form>
</main></body></html>{{end}}`

func sampleOptions(n int) []form.Option {
	names := []string{"設計", "設計レビュー", "詳細設計", "実装", "実装レビュー", "運用", "運用改善", "調査", "テスト", "公開"}
	groups := []string{"設計", "実装", "運用", "調査", "テスト"}
	opts := make([]form.Option, n)
	for i := range opts {
		label := ""
		if i < len(names) {
			label = names[i]
		} else {
			label = fmt.Sprintf("%s %03d", groups[i%len(groups)], i+1)
		}
		opts[i] = form.Option{Value: fmt.Sprint(i + 1), Label: label}
	}
	return opts
}

func newComponentServer(t *testing.T) *httptest.Server {
	t.Helper()
	tmpl, err := web.ParseWithComponents(fstest.MapFS{"page.html": {Data: []byte(componentPage)}}, "page.html")
	if err != nil {
		t.Fatal(err)
	}
	js, err := os.ReadFile("../../internal/web/static/js/combobox.js")
	if err != nil {
		t.Fatal(err)
	}
	css, err := os.ReadFile("../../internal/web/static/css/style.css")
	if err != nil {
		t.Fatal(err)
	}
	data := map[string]any{
		"A": form.Combobox{ID: "a", Name: "a", Label: "タグ", Hint: "20個まで。", Limit: 20, Options: sampleOptions(50)},
		"B": form.Combobox{ID: "b", Name: "b", Label: "担当", Options: []form.Option{{Value: "x", Label: "企画"}, {Value: "y", Label: "開発", Selected: true}, {Value: "z", Label: "運用"}}},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("GET /page", func(w http.ResponseWriter, r *http.Request) {
		var buf bytes.Buffer
		if err := tmpl.ExecuteTemplate(&buf, "page", data); err != nil {
			http.Error(w, "render", 500)
			return
		}
		web.AllowScripts(w)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = buf.WriteTo(w)
	})
	mux.HandleFunc("POST /result", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintf(w, "a=%s b=%s", strings.Join(r.PostForm["a"], ","), strings.Join(r.PostForm["b"], ","))
	})
	mux.HandleFunc("GET /static/js/combobox.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		_, _ = w.Write(js)
	})
	mux.HandleFunc("GET /static/css/style.css", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		_, _ = w.Write(css)
	})
	srv := httptest.NewServer(web.SecurityHeaders(mux))
	t.Cleanup(srv.Close)
	return srv
}

func openComponent(t *testing.T, srv *httptest.Server, b *browser, width int64) {
	t.Helper()
	b.do(t, chromedp.EmulateViewport(width, 900), chromedp.Navigate(srv.URL+"/page"))
	b.waitFor(t, `document.querySelector("#a-input") && !document.querySelector("#a-input").closest("[data-combobox-ui]").hidden`)
}

// TestCombobox_Keyboard は、キーボードでの絞り込み・移動・選択・解除と、読み上げ用の通知を確かめます。
func TestCombobox_Keyboard(t *testing.T) {
	srv := newComponentServer(t)
	b := newBrowser(t)
	openComponent(t, srv, b, 1000)
	if evaluate[bool](t, b, `!document.querySelector("#a-input").closest("[data-combobox]").querySelector("[data-combobox-fallback]").hidden`) {
		t.Fatal("fallback checkboxes must be hidden once the combobox is ready")
	}
	b.do(t, chromedp.Click(chromedp.ID("a-input")))
	// 開くだけで、今いる候補は決めない。
	if !evaluate[bool](t, b, expanded("a")) || evaluate[string](t, b, activeID("a")) != "" {
		t.Fatal("focus must open the list without an active option")
	}
	b.typeText(t, "レビュー")
	if got := evaluate[string](t, b, visibleOptions("a")); got != "設計レビュー,実装レビュー" {
		t.Fatalf("filtered options: %q", got)
	}
	// 候補を指していないEnterは、何も選ばず、送信もしない。
	b.press(t, "Enter")
	time.Sleep(300 * time.Millisecond) // 送信されていれば、この間に画面が移る
	if evaluate[string](t, b, chips("a")) != "" || !strings.HasSuffix(evaluate[string](t, b, "location.pathname"), "/page") {
		t.Fatal("Enter without an active option must do nothing")
	}
	b.press(t, "ArrowDown", "ArrowDown")
	if got := evaluate[string](t, b, activeID("a")); got != "a-option-4" {
		t.Fatalf("active option after two ArrowDown: %q", got)
	}
	// 変換を確定するEnterは選ばない。
	b.composingEnter(t)
	if evaluate[string](t, b, chips("a")) != "" {
		t.Fatal("composing Enter must not select")
	}
	b.press(t, "Enter")
	if evaluate[string](t, b, chips("a")) != "実装レビュー" || evaluate[string](t, b, `document.getElementById("a-input").value`) != "" {
		t.Fatal("Enter must select the active option and clear the query")
	}
	b.waitFor(t, `document.querySelector("#a-input").closest("[data-combobox]").querySelector("[data-status]").textContent.includes("実装レビュー を追加しました。1個を選択中（20個まで）")`)
	if !evaluate[bool](t, b, `document.querySelector('#a-list [role=option][aria-selected="true"]').dataset.label === "実装レビュー"`) {
		t.Fatal("aria-selected not updated")
	}
	if !evaluate[bool](t, b, `document.querySelector('input[name=a][value="5"]').checked`) {
		t.Fatal("fallback checkbox not updated")
	}
	b.press(t, "ArrowUp")
	if got := evaluate[string](t, b, activeID("a")); got != "a-option-3" {
		t.Fatalf("ArrowUp after selection: %q", got)
	}
	b.press(t, "Enter")
	// Escで閉じ、もう一度Escで入力を消す。空欄のBackspaceで最後に選んだものを外す。
	b.press(t, "Escape")
	if evaluate[bool](t, b, expanded("a")) || evaluate[string](t, b, activeID("a")) != "" {
		t.Fatal("Escape must close the list")
	}
	b.press(t, "Backspace")
	if got := evaluate[string](t, b, chips("a")); got != "実装レビュー" {
		t.Fatalf("Backspace must remove the last selection: %q", got)
	}
	b.typeText(t, "zzz")
	if !evaluate[bool](t, b, `!document.querySelector("#a-input").closest("[data-combobox]").querySelector("[data-empty]").hidden`) {
		t.Fatal("no-match message not shown")
	}
	b.press(t, "Escape", "Escape")
	if evaluate[string](t, b, `document.getElementById("a-input").value`) != "" {
		t.Fatal("second Escape must clear the query")
	}
	// Tabで抜けると閉じる。
	b.do(t, chromedp.Click(chromedp.ID("a-input")))
	b.press(t, "Tab")
	if evaluate[bool](t, b, expanded("a")) {
		t.Fatal("Tab must close the list")
	}
	b.noErrors(t)
}

// TestCombobox_Pointer は、候補のクリック、×での解除、外側を押したときの閉じ方を確かめます。
func TestCombobox_Pointer(t *testing.T) {
	srv := newComponentServer(t)
	b := newBrowser(t)
	openComponent(t, srv, b, 1000)
	b.do(t, chromedp.Click(chromedp.ID("a-input")))
	b.typeText(t, "運用改善")
	b.do(t, chromedp.Click(chromedp.ID("a-option-6")))
	if evaluate[string](t, b, chips("a")) != "運用改善" || evaluate[string](t, b, focused) != "a-input" {
		t.Fatal("clicking an option must select it and keep focus in the input")
	}
	b.do(t, chromedp.Click(chromedp.ID("a-option-0")))
	// ×で外すと、入力欄にフォーカスを戻し、候補は開かない。
	b.press(t, "Escape")
	b.do(t, chromedp.Click(chromedp.CSS(`[data-chips] .combobox-chip`)))
	if evaluate[string](t, b, chips("a")) != "設計" || evaluate[string](t, b, focused) != "a-input" || evaluate[bool](t, b, expanded("a")) {
		t.Fatal("removing a chip must focus the input without opening the list")
	}
	// その後に入力欄を押してから外側を押すと閉じる（利用者の報告した不具合の再発防止）。
	b.do(t, chromedp.Click(chromedp.ID("a-input")))
	if !evaluate[bool](t, b, expanded("a")) {
		t.Fatal("clicking the input must open the list")
	}
	b.do(t, chromedp.Click(chromedp.ID("heading")))
	if evaluate[bool](t, b, expanded("a")) || !evaluate[bool](t, b, `document.getElementById("a-list").hidden`) {
		t.Fatal("clicking outside must close the list")
	}
	b.noErrors(t)
}

// TestCombobox_LimitAndInstances は、上限、2つの部品の独立、送信内容を確かめます。
func TestCombobox_LimitAndInstances(t *testing.T) {
	srv := newComponentServer(t)
	b := newBrowser(t)
	openComponent(t, srv, b, 1000)
	b.do(t, chromedp.Click(chromedp.ID("a-input")))
	for range 21 {
		b.press(t, "ArrowDown", "Enter")
	}
	if n := evaluate[int](t, b, `document.querySelectorAll("#a-list [role=option][aria-selected=true]").length`); n != 20 {
		t.Fatalf("selected beyond the limit: %d", n)
	}
	if n := evaluate[int](t, b, `document.querySelectorAll('#a-list [aria-disabled="true"]').length`); n != 30 {
		t.Fatalf("unselected options must be disabled at the limit: %d", n)
	}
	b.waitFor(t, `document.querySelector("#a-input").closest("[data-combobox]").querySelector("[data-status]").textContent.includes("20個に達しています")`)
	// もう1つの部品は上限なしで、初期の選択（開発）を持ち、Aの操作の影響を受けない。
	if got := evaluate[string](t, b, chips("b")); got != "開発" {
		t.Fatalf("second instance initial selection: %q", got)
	}
	// 開いた一覧は下の要素に重なるため、閉じてから次の部品へ移る。
	b.press(t, "Escape")
	b.do(t, chromedp.Click(chromedp.ID("b-input")))
	b.press(t, "ArrowDown", "Enter")
	if evaluate[bool](t, b, expanded("a")) || evaluate[string](t, b, chips("b")) != "開発,企画" {
		t.Fatalf("instances must be independent: a expanded=%v b chips=%q", evaluate[bool](t, b, expanded("a")), evaluate[string](t, b, chips("b")))
	}
	b.press(t, "Escape")
	b.do(t, chromedp.Click(chromedp.ID("submit")))
	b.waitFor(t, `location.pathname === "/result"`)
	got := evaluate[string](t, b, `document.body.innerText`)
	if !strings.HasPrefix(got, "a=1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,17,18,19,20 b=x,y") {
		t.Fatalf("submitted values: %q", got)
	}
	b.noErrors(t)
}

// TestCombobox_NarrowScreen は、幅320pxで横にはみ出さないことを確かめます。
func TestCombobox_NarrowScreen(t *testing.T) {
	srv := newComponentServer(t)
	b := newBrowser(t)
	openComponent(t, srv, b, 320)
	b.do(t, chromedp.Click(chromedp.ID("a-input")))
	b.typeText(t, "テ")
	if w := evaluate[int](t, b, `document.documentElement.scrollWidth`); w > 320 {
		t.Fatalf("horizontal overflow at 320px: %d", w)
	}
	b.noErrors(t)
}

// TestCombobox_WithoutScript は、JSが無効なときと、部品のJSが読めないときに、チェックボックスで送信できることを確かめます。
func TestCombobox_WithoutScript(t *testing.T) {
	srv := newComponentServer(t)
	for name, setup := range map[string]chromedp.Action[chromedp.Void]{
		"disabled": disableScripts(),
		"blocked":  blockURL(srv.URL + "/static/js/combobox.js"),
	} {
		t.Run(name, func(t *testing.T) {
			b := newBrowser(t)
			b.do(t, setup, chromedp.Navigate(srv.URL+"/page"))
			b.waitFor(t, `document.readyState === "complete"`)
			if !evaluate[bool](t, b, `document.querySelector("[data-combobox-ui]").hidden && !document.querySelector("[data-combobox-fallback]").hidden`) {
				t.Fatal("fallback must stay visible without the script")
			}
			b.do(t, chromedp.Click(chromedp.CSS(`input[name=a][value="4"]`)), chromedp.Click(chromedp.ID("submit")))
			b.waitFor(t, `location.pathname === "/result"`)
			if got := evaluate[string](t, b, `document.body.innerText`); got != "a=4 b=y" {
				t.Fatalf("submitted values: %q", got)
			}
		})
	}
}
