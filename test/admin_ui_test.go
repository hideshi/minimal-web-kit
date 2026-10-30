package test

// 管理画面のUI標準の自動検査（docs/admin-ui-standard-plan.md 6.2）。
// 全機能を登録した構成で、管理画面の経路を全部集め、見本のデータで描画して、HTMLの構造を検査する。
// 画面を足したら、adminSamples（開くURL）と、必要なら adminPostSamples・adminFailures に見本を足す。

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"minimal-web-kit/features"
	"minimal-web-kit/features/auth"
	"minimal-web-kit/features/auth/authtest"
	"minimal-web-kit/features/catalog"
	"minimal-web-kit/features/contact"
	"minimal-web-kit/features/news"
	"minimal-web-kit/features/pages"
	"minimal-web-kit/features/site"
	"minimal-web-kit/internal/app"
	"minimal-web-kit/internal/store"
	"minimal-web-kit/internal/web"
)

// ---- 構成と見本のデータ ----

type adminUI struct {
	t        *testing.T
	routes   http.Handler
	patterns []string
	cookies  map[string]*http.Cookie // "admin"・"member"
	csrf     map[string]string
	ids      map[string]string // 見本のデータの識別子（URLに使う）
}

func newAdminUIRegistry(logger *slog.Logger) *features.Registry {
	reg := features.NewRegistry()
	reg.Register(site.NewFeature(logger))
	reg.Register(auth.NewFeature(logger))
	reg.Register(news.NewFeature(logger))
	reg.Register(catalog.NewFeature(logger))
	reg.Register(contact.NewFeature(logger))
	reg.Register(pages.NewFeature(logger))
	return reg
}

// setupAdminUI は全機能を登録した構成を作る。full なら、各機能の見本のデータ（状態の違うものを含む）を作る。
func setupAdminUI(t *testing.T, full bool) *adminUI {
	t.Helper()
	t.Setenv("AUTH_ORIGIN", "")
	t.Setenv("AUTH_RP_ID", "")
	core, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { core.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	reg := newAdminUIRegistry(logger)
	for _, f := range reg.All() {
		if err := f.Init(core.DB()); err != nil {
			t.Fatal(err)
		}
	}
	routes, err := app.BuildRoutes(core, reg, false, logger)
	if err != nil {
		t.Fatal(err)
	}
	// 登録された独立経路を集める（BuildRoutes で管理者認証を配った後に、もう一度登録させて読む）
	mux := &web.IsolatedMux{}
	for _, f := range reg.All() {
		if iso, ok := f.(features.IsolatedRoutesFeature); ok {
			iso.RegisterIsolatedRoutes(mux)
		}
	}
	ui := &adminUI{t: t, routes: routes, patterns: mux.Patterns(), cookies: map[string]*http.Cookie{}, csrf: map[string]string{}, ids: map[string]string{}}
	db := core.DB()
	adminCookie, adminID := authtest.NewAccountSession(t, db, "admin")
	ui.cookies["admin"] = adminCookie
	ui.ids["self"] = strconv.FormatInt(adminID, 10)
	ui.csrf["admin"] = authtest.CSRFToken(t, routes, "/admin/news/new", adminCookie)
	if !full {
		return ui
	}
	memberCookie, _ := authtest.NewAccountSession(t, db, "member")
	ui.cookies["member"] = memberCookie
	ui.csrf["member"] = authtest.CSRFToken(t, routes, "/admin/news/new", memberCookie)
	ui.ids["self_key"] = authtest.NewCredential(t, db, adminID, "管理者のPC")
	_, otherID := authtest.NewAccountSession(t, db, "member")
	ui.ids["other"] = strconv.FormatInt(otherID, 10)
	ui.ids["other_key"] = authtest.NewCredential(t, db, otherID, "メンバーのキー")
	_, stoppedID := authtest.NewAccountSession(t, db, "member")
	ui.ids["stopped"] = strconv.FormatInt(stoppedID, 10)
	if w := ui.post("admin", "/admin/accounts/"+ui.ids["stopped"]+"/disable", nil); w.Code != http.StatusSeeOther {
		t.Fatalf("disable sample account: %d", w.Code)
	}

	ctx := context.Background()
	posts := news.NewPostStore(db)
	draft, _ := posts.Create(ctx, "下書きのお知らせ", "本文")
	published, _ := posts.Create(ctx, "公開中のお知らせ", "本文")
	scheduled, _ := posts.CreateWithDate(ctx, "予約のお知らせ", "本文", time.Now().Add(48*time.Hour))
	for _, id := range []int64{published, scheduled} {
		if err := posts.Publish(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	ui.ids["post_draft"], ui.ids["post_published"], ui.ids["post_scheduled"] = itoa(draft), itoa(published), itoa(scheduled)

	items := catalog.NewStore(db)
	category, err := items.SaveClassification(ctx, "categories", 0, "住宅")
	if err != nil {
		t.Fatal(err)
	}
	unusedCategory, _ := items.SaveClassification(ctx, "categories", 0, "店舗")
	tag, _ := items.SaveClassification(ctx, "tags", 0, "新築")
	unusedTag, _ := items.SaveClassification(ctx, "tags", 0, "木造")
	itemDraft, err := items.Save(ctx, 0, catalog.Input{Title: "下書きの項目", CategoryID: category, TagIDs: []int64{tag}})
	if err != nil {
		t.Fatal(err)
	}
	itemPublished, _ := items.Save(ctx, 0, catalog.Input{Title: "公開中の項目"})
	if err := items.SetPublished(ctx, itemPublished, true); err != nil {
		t.Fatal(err)
	}
	ui.ids["item_draft"], ui.ids["item_published"] = itoa(itemDraft), itoa(itemPublished)
	ui.ids["category"], ui.ids["category_unused"] = itoa(category), itoa(unusedCategory)
	ui.ids["tag"], ui.ids["tag_unused"] = itoa(tag), itoa(unusedTag)

	pageStore := pages.NewStore(db)
	draftPage, err := pageStore.Save(ctx, 0, pages.Input{Slug: "draft-page", Title: "下書きのページ", Body: "## 見出し\n本文"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	publishedPage, _ := pageStore.Save(ctx, 0, pages.Input{Slug: "about", Title: "会社概要", Body: "| 項目 | 内容 |\n|---|---|\n| 社名 | 例 |", NavOrder: "1"}, nil)
	if err := pageStore.SetPublished(ctx, publishedPage, true); err != nil {
		t.Fatal(err)
	}
	ui.ids["page_draft"], ui.ids["page_published"] = itoa(draftPage), itoa(publishedPage)

	inbox := contact.NewStore(db)
	limits := contact.Limits{Hourly: 100, Daily: 100, Unread: 100}
	for key, in := range map[string]contact.Input{
		"msg_unread": {Name: "未読の人", ReplyTo: "reply@example.com", Body: "未読の本文"},
		"msg_read":   {Name: "既読の人", Body: "既読の本文"},
		"msg_spam":   {Body: "スパムの本文"},
		"msg_anon":   {Body: "無記名の本文"},
	} {
		id, err := inbox.Save(ctx, in, limits)
		if err != nil {
			t.Fatal(err)
		}
		ui.ids[key] = itoa(id)
	}
	for key, status := range map[string]string{"msg_read": "read", "msg_spam": "spam"} {
		id, _ := strconv.ParseInt(ui.ids[key], 10, 64)
		if err := inbox.SetStatus(ctx, id, status); err != nil {
			t.Fatal(err)
		}
	}
	return ui
}

func itoa(id int64) string { return strconv.FormatInt(id, 10) }

func (ui *adminUI) get(who, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if c := ui.cookies[who]; c != nil {
		req.AddCookie(c)
	}
	w := httptest.NewRecorder()
	ui.routes.ServeHTTP(w, req)
	return w
}

func (ui *adminUI) post(who, path string, values url.Values) *httptest.ResponseRecorder {
	if values == nil {
		values = url.Values{}
	}
	values.Set("csrf_token", ui.csrf[who])
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(values.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "null")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.AddCookie(ui.cookies[who])
	w := httptest.NewRecorder()
	ui.routes.ServeHTTP(w, req)
	return w
}

// expand は見本のURLの {名前} を、見本のデータの識別子に置き換える。
func (ui *adminUI) expand(path string) string {
	return regexp.MustCompile(`\{([a-z_]+)\}`).ReplaceAllStringFunc(path, func(m string) string {
		v, ok := ui.ids[m[1:len(m)-1]]
		if !ok {
			ui.t.Fatalf("no sample id for %s in %s", m, path)
		}
		return v
	})
}

// ---- 見本の表 ----

// adminSample は、GETの経路の型ごとの開くURL。URLの {名前} は見本のデータに置き換える。
// Who は "admin"・"member"・""（セッションなし）。Kind は画面の型（docs/components.md の「画面の型」）。
type adminSample struct {
	Pattern, URL, Who, Template, Kind string
}

// 画面の型。kindOther は型の外（認証の画面等）で、全画面の共通の検査だけを当てる。
const (
	kindList    = "一覧"
	kindListAdd = "一覧＋追加"
	kindForm    = "作成・編集"
	kindDetail  = "詳細"
	kindConfirm = "削除の確認"
	kindOther   = "型の外"
)

var adminSamples = []adminSample{
	{"GET /admin", "/admin", "admin", "", kindOther}, // 転送だけ
	{"GET /admin/login", "/admin/login", "", "admin_login.html", kindOther},
	{"GET /admin/setup", "/admin/setup", "", "admin_setup.html", kindOther},
	{"GET /admin/passkeys", "/admin/passkeys", "admin", "admin_passkeys.html", kindOther},
	{"GET /admin/passkeys/{id}/delete", "/admin/passkeys/{self_key}/delete", "admin", "admin_passkey_delete.html", kindConfirm},
	{"GET /admin/accounts", "/admin/accounts", "admin", "admin_accounts.html", kindList},
	{"GET /admin/accounts/new", "/admin/accounts/new", "admin", "admin_account_form.html", kindForm},
	{"GET /admin/accounts/{id}", "/admin/accounts/{self}", "admin", "admin_account.html", kindDetail},
	{"GET /admin/accounts/{id}", "/admin/accounts/{other}", "admin", "admin_account.html", kindDetail},
	{"GET /admin/accounts/{id}", "/admin/accounts/{stopped}", "admin", "admin_account.html", kindDetail},
	{"GET /admin/accounts/{id}/delete", "/admin/accounts/{other}/delete", "admin", "admin_account_delete.html", kindConfirm},
	{"GET /admin/accounts/{id}/passkeys/{cid}/delete", "/admin/accounts/{other}/passkeys/{other_key}/delete", "admin", "admin_account_passkey_delete.html", kindConfirm},
	{"GET /admin/news", "/admin/news", "admin", "admin_posts.html", kindList},
	{"GET /admin/news/new", "/admin/news/new", "admin", "admin_post_form.html", kindForm},
	{"GET /admin/news/{id}", "/admin/news/{post_draft}", "admin", "admin_post_form.html", kindForm},
	{"GET /admin/news/{id}", "/admin/news/{post_published}", "member", "admin_post_form.html", kindForm},
	{"GET /admin/news/{id}", "/admin/news/{post_scheduled}", "admin", "admin_post_form.html", kindForm},
	{"GET /admin/news/{id}/delete", "/admin/news/{post_draft}/delete", "admin", "admin_post_delete.html", kindConfirm},
	{"GET /admin/catalog", "/admin/catalog", "admin", "admin_list.html", kindList},
	{"GET /admin/catalog/new", "/admin/catalog/new", "member", "admin_form.html", kindForm},
	{"GET /admin/catalog/{id}", "/admin/catalog/{item_draft}", "admin", "admin_form.html", kindForm},
	{"GET /admin/catalog/{id}", "/admin/catalog/{item_published}", "admin", "admin_form.html", kindForm},
	{"GET /admin/catalog/{id}/delete", "/admin/catalog/{item_draft}/delete", "admin", "admin_delete.html", kindConfirm},
	{"GET /admin/catalog/classifications/categories", "/admin/catalog/classifications/categories", "admin", "admin_classifications.html", kindListAdd},
	{"GET /admin/catalog/classifications/categories/{id}", "/admin/catalog/classifications/categories/{category}", "admin", "admin_classification.html", kindForm},
	{"GET /admin/catalog/classifications/categories/{id}/delete", "/admin/catalog/classifications/categories/{category}/delete", "admin", "admin_classification.html", kindConfirm},
	{"GET /admin/catalog/classifications/categories/{id}/delete", "/admin/catalog/classifications/categories/{category_unused}/delete", "admin", "admin_classification.html", kindConfirm},
	{"GET /admin/catalog/classifications/tags", "/admin/catalog/classifications/tags", "member", "admin_classifications.html", kindListAdd},
	{"GET /admin/catalog/classifications/tags/{id}", "/admin/catalog/classifications/tags/{tag}", "admin", "admin_classification.html", kindForm},
	{"GET /admin/catalog/classifications/tags/{id}/delete", "/admin/catalog/classifications/tags/{tag_unused}/delete", "admin", "admin_classification.html", kindConfirm},
	{"GET /admin/contact", "/admin/contact", "admin", "admin_messages.html", kindList},
	{"GET /admin/contact/{id}", "/admin/contact/{msg_unread}", "admin", "admin_message.html", kindDetail},
	{"GET /admin/contact/{id}", "/admin/contact/{msg_read}", "admin", "admin_message.html", kindDetail},
	{"GET /admin/contact/{id}", "/admin/contact/{msg_spam}", "admin", "admin_message.html", kindDetail},
	{"GET /admin/contact/{id}", "/admin/contact/{msg_anon}", "admin", "admin_message.html", kindDetail},
	{"GET /admin/contact/{id}/delete", "/admin/contact/{msg_read}/delete", "admin", "admin_message_delete.html", kindConfirm},
	{"GET /admin/contact/spam/delete", "/admin/contact/spam/delete", "admin", "admin_spam_delete.html", kindConfirm},
	{"GET /admin/pages", "/admin/pages", "member", "admin_pages.html", kindList},
	{"GET /admin/pages/new", "/admin/pages/new", "admin", "admin_page_form.html", kindForm},
	{"GET /admin/pages/{id}", "/admin/pages/{page_draft}", "admin", "admin_page_form.html", kindForm},
	{"GET /admin/pages/{id}", "/admin/pages/{page_published}", "member", "admin_page_form.html", kindForm},
	{"GET /admin/pages/{id}/delete", "/admin/pages/{page_draft}/delete", "admin", "admin_page_delete.html", kindConfirm},
}

// adminPostSamples は、成功したときに画面を返す送信（GETの経路が無い画面）。
var adminPostSamples = []struct {
	URL      string
	Values   url.Values
	Template string
}{
	{"/admin/accounts", url.Values{"name": {"見本の追加"}, "role": {"member"}}, "admin_account_created.html"},
	{"/admin/accounts/{other}/setup-url", url.Values{}, "admin_account_created.html"},
	{"/admin/pages/preview", url.Values{"slug": {"new-page"}, "title": {"題"}, "body": {"## 見出し\n\n| a | b |\n|---|---|\n| [x](/about) | **y** |"}}, "admin_page_form.html"},
}

// adminFailure は、わざと失敗する送信の見本。Field はエラーを出す入力欄の id（空なら画面全体のエラー）、
// Kept は送った値のうち、応答の入力欄に残っているべきもの。
type adminFailure struct {
	URL    string
	Values url.Values
	Status int
	H1     string
	Field  string
	Kept   string
}

var adminFailures = []adminFailure{
	{"/admin/catalog/classifications/categories", url.Values{"name": {"住宅"}}, 400, "カテゴリの管理", "classification-name", "住宅"},
	{"/admin/catalog/classifications/tags", url.Values{"name": {" "}}, 400, "タグの管理", "classification-name", ""},
	{"/admin/catalog/classifications/tags/{tag_unused}", url.Values{"name": {"新築"}}, 400, "タグの編集", "classification-name", "新築"},
	{"/admin/catalog", url.Values{"title": {""}, "body": {"残す本文"}}, 400, "カタログ項目の作成", "item-title", "残す本文"},
	{"/admin/catalog/{item_draft}", url.Values{"title": {"残す題名"}, "new_tags": {strings.Repeat("長", 51)}}, 400, "カタログ項目の編集", "item-new-tags", "残す題名"},
	{"/admin/news", url.Values{"title": {""}, "body": {"残す本文"}}, 400, "お知らせの作成", "post-title", "残す本文"},
	{"/admin/news/{post_draft}", url.Values{"title": {"残す題名"}, "published_at": {"2025/05/06"}}, 400, "お知らせの編集", "post-published-at", "残す題名"},
	{"/admin/accounts", url.Values{"name": {""}, "role": {"member"}}, 400, "アカウントの追加", "account-name", ""},
	{"/admin/accounts/{self}/disable", url.Values{}, 409, "アカウント: ", "", ""},
	{"/admin/accounts/{other}/passkeys/{other_key}/delete", url.Values{}, 409, "アカウント: ", "", ""},
	{"/admin/pages", url.Values{"slug": {"news"}, "title": {"題"}, "body": {"残す本文"}}, 400, "固定ページの作成", "page-slug", "残す本文"},
	{"/admin/pages/{page_draft}", url.Values{"slug": {"about"}, "title": {"題"}, "body": {"残す本文"}}, 400, "固定ページの編集", "page-slug", "残す本文"},
	{"/admin/pages/preview", url.Values{"slug": {"new-page"}, "title": {""}, "body": {"残す本文"}}, 400, "固定ページの作成", "page-title", "残す本文"},
	{"/admin/pages", url.Values{"slug": {"wide"}, "title": {"題"}, "body": {"| a |\n|---|\n| x | y |"}}, 400, "固定ページの作成", "page-body", ""},
}

// ---- HTMLの解析 ----

type node struct {
	name     string
	attr     map[string]string
	children []*node
	parent   *node
	text     strings.Builder
}

func (n *node) has(class string) bool { return slices.Contains(strings.Fields(n.attr["class"]), class) }

// walk は自分と子孫を、文書の順にたどる。
func (n *node) walk(fn func(*node)) {
	fn(n)
	for _, c := range n.children {
		c.walk(fn)
	}
}

func (n *node) find(name string) []*node {
	var found []*node
	n.walk(func(m *node) {
		if m.name == name {
			found = append(found, m)
		}
	})
	return found
}

func (n *node) inside(name string) *node {
	for p := n.parent; p != nil; p = p.parent {
		if p.name == name {
			return p
		}
	}
	return nil
}

// textContent は子孫の文字をつなげる。
func (n *node) textContent() string {
	var b strings.Builder
	n.walk(func(m *node) { b.WriteString(m.text.String()) })
	return strings.Join(strings.Fields(b.String()), " ")
}

// parseHTML は標準の encoding/xml を、HTML向けの緩い設定で使って木を作る（依存を足さないため）。
func parseHTML(t *testing.T, body string) *node {
	t.Helper()
	d := xml.NewDecoder(strings.NewReader(body))
	d.Strict = false
	d.AutoClose = xml.HTMLAutoClose
	d.Entity = xml.HTMLEntity
	root := &node{name: "#root", attr: map[string]string{}}
	cur := root
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("parse html: %v", err)
		}
		switch v := tok.(type) {
		case xml.StartElement:
			n := &node{name: strings.ToLower(v.Name.Local), attr: map[string]string{}, parent: cur}
			for _, a := range v.Attr {
				n.attr[strings.ToLower(a.Name.Local)] = a.Value
			}
			cur.children = append(cur.children, n)
			cur = n
		case xml.EndElement:
			for p := cur; p != nil; p = p.parent {
				if p.name == strings.ToLower(v.Name.Local) {
					cur = p.parent
					break
				}
			}
			if cur == nil {
				cur = root
			}
		case xml.CharData:
			cur.text.Write(v)
		}
	}
	return root
}

// ---- 検査 ----

var hiddenInputTypes = []string{"hidden", "submit", "button"}

// checkAdminPage は全画面に共通の検査をして、問題を返す。
func checkAdminPage(doc *node, kind string) []string {
	var problems []string
	add := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}
	h1s := doc.find("h1")
	if len(h1s) != 1 {
		add("h1 が %d 個（1個にする）", len(h1s))
	} else if titles := doc.find("title"); len(titles) == 1 {
		body, _, _ := strings.Cut(titles[0].textContent(), " - ")
		if h1 := h1s[0].textContent(); !strings.HasPrefix(h1, body) {
			add("<title>「%s」と h1「%s」が合わない（h1 は title の本体で始める）", body, h1)
		}
	}
	ids := map[string]bool{}
	doc.walk(func(n *node) {
		if id := n.attr["id"]; id != "" {
			if ids[id] {
				add("id「%s」が重複", id)
			}
			ids[id] = true
		}
	})
	labels := map[string]bool{}
	for _, l := range doc.find("label") {
		labels[l.attr["for"]] = true
	}
	doc.walk(func(n *node) {
		for _, a := range []string{"aria-describedby", "aria-controls", "aria-labelledby"} {
			for _, ref := range strings.Fields(n.attr[a]) {
				if !ids[ref] {
					add("%s の参照先「%s」が無い", a, ref)
				}
			}
		}
		if f := n.attr["for"]; n.name == "label" && f != "" && !ids[f] {
			add("label for の参照先「%s」が無い", f)
		}
	})
	groups := map[string]int{}
	for _, name := range []string{"input", "select", "textarea"} {
		for _, n := range doc.find(name) {
			if slices.Contains(hiddenInputTypes, n.attr["type"]) {
				continue
			}
			if !labels[n.attr["id"]] && n.inside("label") == nil && n.attr["aria-label"] == "" {
				add("%s（name=%s）にラベルが無い", name, n.attr["name"])
			}
			if t := n.attr["type"]; t == "radio" || t == "checkbox" {
				groups[n.attr["name"]]++
			}
		}
	}
	for _, n := range doc.find("input") {
		if t := n.attr["type"]; (t == "radio" || t == "checkbox") && groups[n.attr["name"]] > 1 {
			if fs := n.inside("fieldset"); fs == nil || len(fs.find("legend")) == 0 {
				add("同じ名前（%s）の選択肢が fieldset・legend の中に無い", n.attr["name"])
			}
		}
	}
	for _, f := range doc.find("form") {
		if strings.EqualFold(f.attr["method"], "post") {
			found := false
			for _, in := range f.find("input") {
				found = found || in.attr["name"] == "csrf_token"
			}
			if !found {
				add("POST のフォーム（%s）に csrf_token が無い", f.attr["action"])
			}
		}
	}
	for _, m := range doc.find("main") {
		for _, name := range []string{"ul", "ol"} {
			for _, l := range m.find(name) {
				if l.attr["class"] == "" && l.attr["role"] == "" {
					add("main の中の素の %s（クラスを付けるか、表・段落にする）", name)
				}
			}
		}
	}
	for _, tbl := range doc.find("table") {
		if len(tbl.find("caption")) == 0 {
			add("table に caption が無い")
		}
		for _, th := range tbl.find("th") {
			if th.attr["scope"] == "" {
				add("th に scope が無い")
			}
		}
		for _, td := range tbl.find("td") {
			td.walk(func(n *node) {
				if n.has("btn-danger") {
					add("表の中に btn-danger（表の削除は確認画面へのリンクにする）")
				}
			})
		}
	}
	main := doc.find("main")
	switch kind {
	case kindConfirm:
		ok := false
		for _, bar := range doc.find("div") {
			if !bar.has("action-bar") {
				continue
			}
			var danger, back int
			bar.walk(func(n *node) {
				if n.name == "button" && n.has("btn-danger") {
					danger++
				}
				if n.name == "a" && n.has("btn-secondary") && n.textContent() == "削除せずに戻る" {
					back++
				}
			})
			ok = ok || (danger == 1 && back == 1)
		}
		if !ok {
			add("削除の確認に、btn-danger の送信ボタンと「削除せずに戻る」（btn-secondary）を並べた action-bar が無い")
		}
	case kindList, kindListAdd:
		hasEmpty := false
		if len(main) == 1 {
			for _, c := range main[0].children {
				hasEmpty = hasEmpty || (c.name == "p" && strings.HasSuffix(c.textContent(), "はまだありません。"))
			}
		}
		if len(doc.find("table")) == 0 && !hasEmpty {
			add("一覧に、表も、空のときの1文（「〜はまだありません。」）も無い")
		}
	}
	return problems
}

// TestAdminUI_AllPagesFollowStandard は、全管理画面を見本のデータで開き、UI標準の構造を満たすかを確かめる。
// 不変条件ではなく、docs/components.md の「画面の型」の検査。
func TestAdminUI_AllPagesFollowStandard(t *testing.T) {
	full := setupAdminUI(t, true)
	empty := setupAdminUI(t, false)

	// 登録された GET /admin の経路に、全部見本があること
	covered := map[string]bool{}
	for _, s := range adminSamples {
		covered[s.Pattern] = true
	}
	for _, p := range full.patterns {
		if !strings.HasPrefix(p, "GET /admin") || strings.HasPrefix(p, "GET /admin/static/") {
			continue
		}
		if !covered[p] {
			t.Errorf("経路 %s の見本が adminSamples に無い（開くURLと画面の型を書く）", p)
		}
	}

	check := func(label string, w *httptest.ResponseRecorder, kind string) {
		t.Helper()
		if w.Code >= 300 && w.Code < 400 || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/html") {
			return
		}
		// 管理画面は前段（Cloudflareのメールの難読化等）に書き換えさせない
		if cc := w.Header().Get("Cache-Control"); !strings.Contains(cc, "no-transform") || !strings.Contains(cc, "no-store") {
			t.Errorf("%s: Cache-Control %q（no-store と no-transform が要る）", label, cc)
		}
		for _, p := range checkAdminPage(parseHTML(t, w.Body.String()), kind) {
			t.Errorf("%s: %s", label, p)
		}
	}
	for _, s := range adminSamples {
		w := full.get(s.Who, full.expand(s.URL))
		if s.Template == "" {
			if w.Code != http.StatusSeeOther {
				t.Errorf("%s: 転送のはずが %d", s.URL, w.Code)
			}
			continue
		}
		if w.Code != http.StatusOK {
			t.Errorf("%s（%s）: %d", s.URL, s.Who, w.Code)
			continue
		}
		check(s.URL+"（データあり）", w, s.Kind)
		if !strings.Contains(s.URL, "{") {
			who := s.Who
			if who == "member" {
				who = "admin"
			}
			if w := empty.get(who, s.URL); w.Code == http.StatusOK {
				check(s.URL+"（空）", w, s.Kind)
			}
		}
		// 管理者だけの画面をメンバーで開くと拒否される（権限の検査は各機能のテストが受け持つ）
		if s.Who == "admin" {
			if w := full.get("member", full.expand(s.URL)); w.Code == http.StatusOK {
				check(s.URL+"（メンバー）", w, s.Kind)
			}
		}
	}
	for _, s := range adminPostSamples {
		w := full.post("admin", full.expand(s.URL), s.Values)
		if w.Code != http.StatusOK {
			t.Errorf("POST %s: %d", s.URL, w.Code)
			continue
		}
		check("POST "+s.URL, w, kindOther)
	}
	for _, f := range adminFailures {
		values := url.Values{}
		for k, v := range f.Values {
			values[k] = slices.Clone(v)
		}
		w := full.post("admin", full.expand(f.URL), values)
		label := "失敗 POST " + f.URL
		if w.Code != f.Status {
			t.Errorf("%s: 状態コード %d（期待 %d）", label, w.Code, f.Status)
			continue
		}
		doc := parseHTML(t, w.Body.String())
		check(label, w, kindOther)
		if h1s := doc.find("h1"); len(h1s) != 1 || !strings.HasPrefix(h1s[0].textContent(), f.H1) {
			t.Errorf("%s: 見出しが送る前の画面と違う", label)
		}
		if f.Field == "" {
			alerts := 0
			doc.walk(func(n *node) {
				if n.attr["role"] == "alert" && n.textContent() != "" {
					alerts++
				}
			})
			if alerts == 0 {
				t.Errorf("%s: 画面全体のエラー（role=alert）が無い", label)
			}
		} else {
			var field *node
			doc.walk(func(n *node) {
				if n.attr["id"] == f.Field {
					field = n
				}
			})
			if field == nil || field.attr["aria-invalid"] != "true" || !strings.Contains(field.attr["aria-describedby"], f.Field+"-error") {
				t.Errorf("%s: 入力欄 %s にエラー（aria-invalid と -error の文）が無い", label, f.Field)
			}
			doc.walk(func(n *node) {
				if n.attr["role"] == "alert" && n.textContent() != "" {
					t.Errorf("%s: 欄のエラーなのに、画面全体のエラーも出ている", label)
				}
			})
		}
		if f.Kept != "" {
			kept := false
			doc.walk(func(n *node) {
				kept = kept || (n.name == "input" && n.attr["value"] == f.Kept) || (n.name == "textarea" && strings.TrimSpace(n.text.String()) == f.Kept)
			})
			if !kept {
				t.Errorf("%s: 送った値「%s」が入力欄に残っていない", label, f.Kept)
			}
		}
	}
}

// TestAdminUI_TemplatesAndRoutesHaveSamples は、見本の表が漏れていないことを、ソースと突き合わせて確かめる。
// 管理画面のテンプレートを足したのに見本を書かなかった場合、管理画面の経路を IsolatedMux 以外に登録した場合に落ちる。
func TestAdminUI_TemplatesAndRoutesHaveSamples(t *testing.T) {
	sampled := map[string]bool{}
	for _, s := range adminSamples {
		sampled[s.Template] = true
	}
	for _, s := range adminPostSamples {
		sampled[s.Template] = true
	}
	files, err := filepath.Glob("../features/*/views/admin_*.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		name := filepath.Base(f)
		if strings.HasSuffix(name, "_layout.html") {
			continue
		}
		if !sampled[name] {
			t.Errorf("テンプレート %s を描く見本が adminSamples・adminPostSamples に無い", f)
		}
	}

	registered := map[string]bool{}
	for _, p := range setupAdminUI(t, false).patterns {
		registered[p] = true
	}
	literal := regexp.MustCompile(`"(GET /admin[A-Za-z0-9_/{}.-]*)"`)
	err = filepath.WalkDir("../features", func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		// 管理者認証の提供者を置き換える機能の経路は、この見本の構成に入らない（federated_auth_test.go で確かめる）
		if strings.Contains(filepath.ToSlash(path), "features/auth/oidcop/") || strings.Contains(filepath.ToSlash(path), "features/federatedauth/") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range literal.FindAllStringSubmatch(string(src), -1) {
			if !registered[m[1]] {
				t.Errorf("%s の経路 %s が IsolatedMux に登録されていない（管理画面は独立経路に登録する）", path, m[1])
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// 公開ページは書き換えを止めない（Cloudflareのメールの難読化に任せる）。管理画面だけが no-transform。
// ナビの印は、一覧そのものなら page、配下の画面なら true になること。カタログの公開ページも同じ。
func TestAdminUI_NavCurrent(t *testing.T) {
	ui := setupAdminUI(t, true)
	for _, c := range []struct{ who, path, want string }{
		{"admin", "/admin/catalog", `<a href="/admin/catalog" aria-current="page">`},
		{"admin", "/admin/catalog/{item_draft}", `<a href="/admin/catalog" aria-current="true">`},
		{"admin", "/admin/catalog/classifications/categories", `<a href="/admin/catalog" aria-current="true">`},
		{"", "/catalog", `<a href="/catalog" aria-current="page">`},
		{"", "/catalog?q=x", `<a href="/catalog" aria-current="page">`},
		{"", "/catalog/{item_published}", `<a href="/catalog" aria-current="true">`},
	} {
		body := ui.get(c.who, ui.expand(c.path)).Body.String()
		if !strings.Contains(body, c.want) {
			t.Errorf("%s: missing %s", c.path, c.want)
		}
		if got := strings.Count(body, "aria-current"); got != 1 {
			t.Errorf("%s: aria-current が %d 個", c.path, got)
		}
	}
}

func TestAdminUI_NoTransformOnlyOnAdmin(t *testing.T) {
	ui := setupAdminUI(t, false)
	for _, path := range []string{"/", "/privacy", "/news", "/catalog", "/contact"} {
		if cc := ui.get("", path).Header().Get("Cache-Control"); strings.Contains(cc, "no-transform") {
			t.Errorf("%s must not set no-transform: %q", path, cc)
		}
	}
	if w := ui.post("admin", "/admin/catalog/classifications/tags", url.Values{"name": {""}}); !strings.Contains(w.Header().Get("Cache-Control"), "no-transform") {
		t.Errorf("admin form responses must set no-transform")
	}
}
