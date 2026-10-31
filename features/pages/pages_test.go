package pages

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
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
	"minimal-web-kit/features/site"
	"minimal-web-kit/internal/app"
	"minimal-web-kit/internal/store"
)

type fixture struct {
	t      *testing.T
	f      *Feature
	routes http.Handler
	core   *store.SQLiteStore
	logs   *bytes.Buffer
	clock  *time.Time
	admin  *http.Cookie
	member *http.Cookie
	token  string
	mtoken string // メンバーのCSRFトークン
}

func setup(t *testing.T) *fixture {
	t.Helper()
	t.Setenv("AUTH_ORIGIN", "")
	t.Setenv("AUTH_RP_ID", "")
	t.Setenv("SITE_URL", "https://example.com")
	t.Setenv("CONTACT_FORM_URL", "")
	core, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { core.Close() })
	logs := &bytes.Buffer{}
	logger := slog.New(slog.NewTextHandler(logs, nil))
	f := NewFeature(logger)
	clock := time.Date(2026, 10, 30, 10, 0, 0, 0, time.Local)
	f.now = func() time.Time { return clock }
	reg := features.NewRegistry()
	reg.Register(site.NewFeature(logger))
	reg.Register(auth.NewFeature(logger))
	reg.Register(news.NewFeature(logger))
	reg.Register(catalog.NewFeature(logger))
	reg.Register(contact.NewFeature(slog.New(slog.NewTextHandler(io.Discard, nil))))
	reg.Register(f)
	for _, feature := range reg.All() {
		if err := feature.Init(core.DB()); err != nil {
			t.Fatal(err)
		}
	}
	routes, err := app.BuildRoutes(core, reg, false, logger)
	if err != nil {
		t.Fatal(err)
	}
	fx := &fixture{t: t, f: f, routes: routes, core: core, logs: logs, clock: &clock}
	fx.admin = authtest.NewSession(t, core.DB())
	fx.member = authtest.NewSessionWithRole(t, core.DB(), "member")
	fx.token = authtest.CSRFToken(t, routes, "/admin/pages/new", fx.admin)
	fx.mtoken = authtest.CSRFToken(t, routes, "/admin/pages/new", fx.member)
	return fx
}

func (fx *fixture) get(path string, cookie *http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	fx.routes.ServeHTTP(w, req)
	return w
}

func (fx *fixture) post(path string, values url.Values, cookie *http.Cookie) *httptest.ResponseRecorder {
	if values == nil {
		values = url.Values{}
	}
	if values.Get("csrf_token") == "" {
		values.Set("csrf_token", fx.token)
		if cookie == fx.member {
			values.Set("csrf_token", fx.mtoken)
		}
	}
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(values.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("Origin", "null")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	fx.routes.ServeHTTP(w, req)
	return w
}

// create は、管理画面から作って、IDを返します。
func (fx *fixture) create(slug, title, body, nav string, publish bool) int64 {
	fx.t.Helper()
	w := fx.post("/admin/pages", url.Values{"slug": {slug}, "title": {title}, "body": {body}, "nav_order": {nav}}, fx.admin)
	if w.Code != http.StatusSeeOther {
		fx.t.Fatalf("create %s: %d %s", slug, w.Code, w.Body.String())
	}
	loc := w.Header().Get("Location")
	id, err := strconv.ParseInt(strings.TrimPrefix(strings.Split(loc, "?")[0], "/admin/pages/"), 10, 64)
	if err != nil {
		fx.t.Fatalf("location %s", loc)
	}
	if publish {
		if w := fx.post("/admin/pages/"+strconv.FormatInt(id, 10)+"/publish", nil, fx.admin); w.Code != http.StatusSeeOther {
			fx.t.Fatalf("publish: %d", w.Code)
		}
	}
	return id
}

// TestPublic_PublishedOnlyAndCoreRoutesKept は、公開中のページだけが /{slug} で見え、それ以外は同じ404で、
// Cookieを使わず、Coreと機能の経路が取られないことを確かめる。
// 不変条件: PAGES-INV-1, PAGES-INV-5
// ユースケース: UC-PAGES-1
func TestPublic_PublishedOnlyAndCoreRoutesKept(t *testing.T) {
	fx := setup(t)
	fx.create("about", "会社概要", "## 概要\n本文です。", "", true)
	fx.create("draft-page", "下書き", "見えない", "", false)

	w := fx.get("/about", nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "<h1>会社概要</h1>") || !strings.Contains(w.Body.String(), "<h2>概要</h2>") {
		t.Fatalf("published page: %d", w.Code)
	}
	if w.Header().Get("Set-Cookie") != "" || !strings.Contains(w.Header().Get("Cache-Control"), "no-store") {
		t.Fatalf("public page must not use cookies or the public cache: %v", w.Header())
	}
	if w := fx.get("/%61bout", nil); w.Code != http.StatusOK {
		t.Fatalf("escaped path: %d", w.Code)
	}
	notFound := fx.get("/missing", nil).Body.String()
	for _, path := range []string{"/draft-page", "/missing", "/about/", "/a/b", "/ABOUT", "/-x", "/a"} {
		w := fx.get(path, nil)
		if w.Code != http.StatusNotFound || w.Body.String() != notFound {
			t.Errorf("%s must be the same 404: %d", path, w.Code)
		}
	}
	req := httptest.NewRequest(http.MethodHead, "/about", nil)
	head := httptest.NewRecorder()
	fx.routes.ServeHTTP(head, req)
	if head.Code != http.StatusOK {
		t.Fatalf("HEAD: %d", head.Code)
	}
	for _, path := range []string{"/", "/privacy", "/news", "/catalog", "/contact", "/admin/login", "/static/css/style.css", "/robots.txt", "/sitemap.xml"} {
		if w := fx.get(path, nil); w.Code != http.StatusOK || strings.Contains(w.Body.String(), "<h1>会社概要</h1>") {
			t.Errorf("%s must keep its own route: %d", path, w.Code)
		}
	}
}

// TestPublic_LoadFailureIsNot404 は、取得の失敗を「ページが無い」（404）と区別して500にすることを確かめる。
//
// 不変条件: PAGES-INV-10
func TestPublic_LoadFailureIsNot404(t *testing.T) {
	fx := setup(t)
	fx.create("about", "会社概要", "本文", "", true)
	if _, err := fx.core.DB().Exec("ALTER TABLE pages RENAME TO pages_gone"); err != nil {
		t.Fatal(err)
	}
	if w := fx.get("/about", nil); w.Code != http.StatusInternalServerError || strings.Contains(w.Body.String(), "会社概要") {
		t.Fatalf("load failure: %d", w.Code)
	}
	if !strings.Contains(fx.logs.String(), "pages load failed") {
		t.Fatal("load failure must be logged")
	}
}

// TestPublic_BodyIsRenderedAsText は、本文の利用者の入力がHTMLとして出ず、許可したリンクだけが出ることを確かめる。
// 不変条件: PAGES-INV-2
// ユースケース: UC-PAGES-1
func TestPublic_BodyIsRenderedAsText(t *testing.T) {
	fx := setup(t)
	fx.create("about", "<b>題</b>", "<script>alert(1)</script>\n[会社](/about) [危険](javascript:alert(1)) [外部](https://example.com)\n\n| 項目 | 内容 |\n|---|---|\n| 社名 | <i>例</i> |", "", true)
	body := fx.get("/about", nil).Body.String()
	for _, bad := range []string{"<script>", "<b>題", "<i>例", `href="javascript`} {
		if strings.Contains(body, bad) {
			t.Errorf("must not contain %s", bad)
		}
	}
	for _, want := range []string{"&lt;script&gt;", `<a href="/about">会社</a>`, `<a href="https://example.com">外部</a>`, "[危険](javascript:alert(1))", "<caption>表1</caption>"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %s", want)
		}
	}
}

// TestSlugRules は、スラッグの書式・予約語・登録済みの経路・重複を拒否することを確かめる。
// 不変条件: PAGES-INV-3
func TestSlugRules(t *testing.T) {
	fx := setup(t)
	fx.create("about", "会社概要", "", "", false)
	for slug, field := range map[string]string{
		"A": "slug", "a": "slug", "-ab": "slug", "ab-": "slug", "a_b": "slug", "a/b": "slug", strings.Repeat("a", 41): "slug",
		"admin": "slug_reserved", "news": "slug_reserved", "privacy": "slug_reserved", "contact": "slug_reserved", "static": "slug_reserved",
		"about": "slug_taken",
	} {
		w := fx.post("/admin/pages", url.Values{"slug": {slug}, "title": {"題"}}, fx.admin)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), fieldErrors[field]) || !strings.Contains(w.Body.String(), `aria-invalid="true"`) {
			t.Errorf("slug %q: %d", slug, w.Code)
		}
	}
	// 登録済みの経路に当たるスラッグは、固定の一覧に無くても拒否する
	if _, err := normalize(Input{Slug: "orders", Title: "題"}, func(p string) bool { return p == "/orders" }); err != (InputError{"slug_reserved"}) {
		t.Fatalf("taken route: %v", err)
	}
	if !fx.f.taken("/privacy") || fx.f.taken("/company") {
		t.Fatal("route check must come from the real routes")
	}
}

// TestAdmin_ProtectedPreviewAndLifecycle は、管理画面の守り、プレビュー（保存しない）、公開・非公開・削除を確かめる。
// 不変条件: PAGES-INV-4, PAGES-INV-9
// ユースケース: UC-PAGES-2, UC-PAGES-3, UC-PAGES-4, UC-PAGES-5, UC-PAGES-6
func TestAdmin_ProtectedPreviewAndLifecycle(t *testing.T) {
	fx := setup(t)
	if w := fx.get("/admin/pages", nil); w.Code != http.StatusSeeOther {
		t.Fatalf("anonymous: %d", w.Code)
	}
	if w := fx.get("/admin/pages", fx.member); w.Code != http.StatusOK {
		t.Fatalf("member: %d", w.Code)
	}
	if w := fx.post("/admin/pages", url.Values{"slug": {"x1"}, "title": {"t"}, "csrf_token": {"bad"}}, fx.admin); w.Code != http.StatusForbidden {
		t.Fatalf("bad csrf: %d", w.Code)
	}
	id := fx.create("about", "会社概要", "古い本文", "", false)
	path := "/admin/pages/" + strconv.FormatInt(id, 10)
	before, _ := fx.f.store.Get(context.Background(), id)

	*fx.clock = fx.clock.Add(time.Hour)
	w := fx.post("/admin/pages/preview", url.Values{"id": {strconv.FormatInt(id, 10)}, "slug": {"about"}, "title": {"会社概要"}, "body": {"## 新しい見出し\n新しい本文"}}, fx.member)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "プレビュー（保存していません）") || !strings.Contains(w.Body.String(), "<h2>新しい見出し</h2>") || !strings.Contains(w.Body.String(), "新しい本文</textarea>") {
		t.Fatalf("preview: %d", w.Code)
	}
	after, _ := fx.f.store.Get(context.Background(), id)
	if after.Body != "古い本文" || !after.UpdatedAt.Equal(before.UpdatedAt) || after.Published {
		t.Fatal("preview must not save")
	}
	if w := fx.post("/admin/pages/preview", url.Values{"slug": {"new-page"}, "title": {""}, "body": {"## x"}}, fx.admin); w.Code != http.StatusBadRequest || strings.Contains(w.Body.String(), "プレビュー（保存していません）") {
		t.Fatalf("invalid preview must not render: %d", w.Code)
	}
	if w := fx.post(path, url.Values{"slug": {"about"}, "title": {"会社概要"}, "body": {"新しい本文"}}, fx.member); w.Code != http.StatusSeeOther {
		t.Fatalf("update: %d", w.Code)
	}
	if fx.get("/about", nil).Code != http.StatusNotFound {
		t.Fatal("saving must not publish")
	}
	fx.post(path+"/publish", nil, fx.admin)
	if !strings.Contains(fx.get("/about", nil).Body.String(), "新しい本文") {
		t.Fatal("publish")
	}
	fx.post(path+"/unpublish", nil, fx.admin)
	if fx.get("/about", nil).Code != http.StatusNotFound {
		t.Fatal("unpublish")
	}
	if w := fx.get(path+"/delete", fx.admin); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "削除せずに戻る") {
		t.Fatalf("delete page: %d", w.Code)
	}
	if w := fx.post(path+"/delete", nil, fx.admin); w.Code != http.StatusSeeOther {
		t.Fatalf("delete: %d", w.Code)
	}
	if w := fx.get(path, fx.admin); w.Code != http.StatusNotFound {
		t.Fatalf("deleted: %d", w.Code)
	}
}

// TestNavAndSitemap_ReflectWithoutRestart は、公開中で順のあるページだけがナビとsitemapに出て、変更が再起動なしで反映されることを確かめる。
// 不変条件: PAGES-INV-6
// ユースケース: UC-PAGES-7
func TestNavAndSitemap_ReflectWithoutRestart(t *testing.T) {
	fx := setup(t)
	about := fx.create("about", "会社概要", "", "2", true)
	fx.create("recruit", "採用情報", "", "1", true)
	fx.create("hidden", "ナビに出さない", "", "", true)
	fx.create("draft", "下書き", "", "0", false)

	home := fx.get("/privacy", nil).Body.String() // Coreの画面もSiteのナビを使う
	recruit, aboutAt := strings.Index(home, `<a href="/recruit">採用情報</a>`), strings.Index(home, `<a href="/about">会社概要</a>`)
	if recruit < 0 || aboutAt < 0 || recruit > aboutAt || strings.Contains(home, `href="/hidden"`) || strings.Contains(home, `href="/draft"`) {
		t.Fatal("nav must list published pages with an order, sorted by the order")
	}
	sitemap := fx.get("/sitemap.xml", nil).Body.String()
	if !strings.Contains(sitemap, "https://example.com/hidden") || strings.Contains(sitemap, "https://example.com/draft<") {
		t.Fatal("sitemap must list published pages only")
	}
	fx.post("/admin/pages/"+strconv.FormatInt(about, 10)+"/unpublish", nil, fx.admin)
	if strings.Contains(fx.get("/privacy", nil).Body.String(), `href="/about"`) || strings.Contains(fx.get("/sitemap.xml", nil).Body.String(), "https://example.com/about<") {
		t.Fatal("unpublishing must update the nav and sitemap without a restart")
	}
}

// ナビの作り直しに失敗しても、非公開にしたページのリンクを公開ナビに残さないこと。利用者が接続を切っても反映すること。
// 不変条件: PAGES-INV-6
func TestNav_RefreshFailureDoesNotKeepOldLinks(t *testing.T) {
	fx := setup(t)
	fx.create("about", "会社概要", "本文", "10", true)
	nav := func() string { return fx.get("/privacy", nil).Body.String() }
	if !strings.Contains(nav(), `href="/about"`) {
		t.Fatal("the published page must be in the nav")
	}

	// 取得に失敗したら、古いリンクを残さない
	mustExec := func(q string) {
		t.Helper()
		if _, err := fx.core.DB().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	mustExec("ALTER TABLE pages RENAME TO pages_gone")
	fx.f.afterChange(context.Background())
	if strings.Contains(nav(), `href="/about"`) {
		t.Fatal("a failed refresh must not keep the old link")
	}
	if !strings.Contains(fx.logs.String(), "pages nav refresh failed") {
		t.Fatal("the failure must be logged")
	}

	// 定期のGCで戻る
	mustExec("ALTER TABLE pages_gone RENAME TO pages")
	if err := fx.f.Purge(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(nav(), `href="/about"`) {
		t.Fatal("the purge must restore the nav")
	}

	// 接続が切れた要求でも、コミット済みの変更をナビに反映する
	fx.create("recruit", "採用情報", "本文", "20", true)
	mustExec("UPDATE pages SET status = 'draft' WHERE slug = 'about'")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	fx.f.afterChange(ctx)
	if got := nav(); strings.Contains(got, `href="/about"`) || !strings.Contains(got, `href="/recruit"`) {
		t.Fatal("a cancelled request must still rebuild the nav from the database")
	}
}

// TestPersistence_SurvivesReinit は、ページがマイグレーションの再実行とCoreのセッションの削除で消えないことを確かめる。
// 不変条件: PAGES-INV-7
func TestPersistence_SurvivesReinit(t *testing.T) {
	fx := setup(t)
	fx.create("about", "会社概要", "本文", "", true)
	if _, err := fx.core.DB().Exec("DELETE FROM sessions; DELETE FROM owners"); err != nil {
		t.Fatal(err)
	}
	if err := fx.f.Init(fx.core.DB()); err != nil {
		t.Fatal(err)
	}
	if fx.get("/about", nil).Code != http.StatusOK {
		t.Fatal("pages must survive")
	}
}

// TestLogs_DoNotLeakContent は、タイトル・本文・スラッグがログに出ないことを確かめる。
// 不変条件: PAGES-INV-8
func TestLogs_DoNotLeakContent(t *testing.T) {
	fx := setup(t)
	id := fx.create("canary-slug", "題名カナリア", "本文カナリア", "1", true)
	fx.post("/admin/pages/preview", url.Values{"slug": {"canary-slug"}, "title": {"題名カナリア"}, "body": {"本文カナリア"}}, fx.admin)
	fx.post("/admin/pages/"+strconv.FormatInt(id, 10), url.Values{"slug": {"admin"}, "title": {"題名カナリア"}, "body": {"本文カナリア"}}, fx.admin)
	fx.get("/canary-slug", nil)
	for _, canary := range []string{"canary-slug", "題名カナリア", "本文カナリア"} {
		if strings.Contains(fx.logs.String(), canary) {
			t.Fatalf("log leaked %s", canary)
		}
	}
}

// busyError は、SQLiteの書き込みの競合のエラーの見本です（SQLITE_BUSY）。
type busyError struct{}

func (busyError) Error() string { return "database is locked" }
func (busyError) Code() int     { return 5 }

// 書き込みの競合は503と Retry-After で返し、他の失敗は500のままにすること。
func TestAdmin_BusyInvitesRetry(t *testing.T) {
	fx := setup(t)
	r := httptest.NewRequest(http.MethodPost, "/admin/pages", nil)
	w := httptest.NewRecorder()
	if !fx.f.failed(w, r, fmt.Errorf("save: %w", busyError{})) || w.Code != http.StatusServiceUnavailable || w.Header().Get("Retry-After") == "" {
		t.Fatalf("busy: %d %v", w.Code, w.Header())
	}
	w = httptest.NewRecorder()
	if !fx.f.failed(w, r, errors.New("broken")) || w.Code != http.StatusInternalServerError {
		t.Fatalf("other failure: %d", w.Code)
	}
}
