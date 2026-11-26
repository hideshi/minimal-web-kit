package pages

import (
	"context"
	"errors"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"minimal-web-kit/features"
	"minimal-web-kit/features/auth"
	"minimal-web-kit/features/site"
	"minimal-web-kit/internal/app"
	"minimal-web-kit/internal/store"
	"minimal-web-kit/internal/web/richtext"
)

const siteTopMark = `class="hero-links"` // Siteのトップだけにある部品

func itoa(id int64) string { return strconv.FormatInt(id, 10) }

func (fx *fixture) head(path string, cookie *http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodHead, path, nil)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	fx.routes.ServeHTTP(w, req)
	return w
}

// TestHome_ServedOnlyWhenPublished は、HOME_PAGE_SLUG のページが公開中のときだけ / に出し、下書き・予約中・描けない失敗では
// 今のトップ（Site）を返し、どちらの場合も / はCookieを照合も発行もせず no-store で返し、/privacy の扱いは変えないことを確かめる。
// 不変条件: PAGES-INV-15, PAGES-INV-16, PAGES-INV-5
// ユースケース: UC-PAGES-9, UC-SITE-1
func TestHome_ServedOnlyWhenPublished(t *testing.T) {
	t.Setenv("HOME_PAGE_SLUG", "home")
	fx := setup(t)
	checkRoot := func(wantPage bool) {
		t.Helper()
		for _, w := range []*httptest.ResponseRecorder{fx.get("/", nil), fx.get("/", fx.member), fx.head("/", fx.member)} {
			if w.Code != http.StatusOK {
				t.Fatalf("/: %d", w.Code)
			}
			if cc := w.Header().Get("Cache-Control"); !strings.HasPrefix(cc, "no-store") {
				t.Errorf("/ must not be cached while HOME_PAGE_SLUG is set: %q", cc)
			}
			if sc := w.Header().Values("Set-Cookie"); len(sc) != 0 {
				t.Errorf("/ must not set cookies: %v", sc)
			}
		}
		body := fx.get("/", nil).Body.String()
		if got := strings.Contains(body, "トップの本文"); got != wantPage {
			t.Errorf("page shown = %v, want %v: %.300s", got, wantPage, body)
		}
		if got := strings.Contains(body, siteTopMark); got == wantPage {
			t.Errorf("site top shown = %v, want %v", got, !wantPage)
		}
	}
	checkRoot(false) // ページが無い
	id := fx.create("home", "ようこそ", "ヒーロー: トップの本文", "", false)
	checkRoot(false) // 下書き
	if _, err := fx.core.DB().Exec("UPDATE pages SET status = 'published', publish_at = ? WHERE id = ?", fx.clock.Add(time.Hour).Unix(), id); err != nil {
		t.Fatal(err)
	}
	checkRoot(false) // 予約中
	*fx.clock = fx.clock.Add(2 * time.Hour)
	checkRoot(true) // 予約の時刻を過ぎた最初の要求から出る
	if body := fx.get("/", nil).Body.String(); !strings.Contains(body, `<link rel="canonical" href="https://example.com/">`) {
		t.Errorf("canonical must be /: %.500s", body)
	}

	// 描けない失敗は、今のトップに戻して500にしない。ログはページのIDと分類だけ
	saved := fx.f.publicPage
	fx.f.publicPage = template.Must(template.New("x").Parse(`{{define "layout"}}{{template "missing"}}{{end}}`))
	fx.logs.Reset()
	checkRoot(false)
	if logs := fx.logs.String(); !strings.Contains(logs, "pages home render failed") || strings.Contains(logs, "トップの本文") {
		t.Errorf("render failure log: %s", logs)
	}
	fx.f.publicPage = saved

	// /privacy の表示とキャッシュは、指定で変わらない（Siteの公開キャッシュのまま、Cookieを発行しない）
	if w := fx.get("/privacy", nil); w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "public, max-age=60" || len(w.Header().Values("Set-Cookie")) != 0 {
		t.Errorf("/privacy changed: %d %q %v", w.Code, w.Header().Get("Cache-Control"), w.Header().Values("Set-Cookie"))
	}
}

// TestHome_SlugRedirectsAndSitemap は、トップページの /{スラッグ} が、公開中なら / へ302で no-store のまま転送し、公開でなければ404で、
// sitemap.xml とナビでは / の1件にまとまることを確かめる。
// 不変条件: PAGES-INV-17
func TestHome_SlugRedirectsAndSitemap(t *testing.T) {
	t.Setenv("HOME_PAGE_SLUG", "home")
	fx := setup(t)
	id := fx.create("home", "ようこそ", "本文", "1", false)
	if w := fx.get("/home", nil); w.Code != http.StatusNotFound {
		t.Errorf("draft home page: %d", w.Code)
	}
	if w := fx.post("/admin/pages/"+itoa(id)+"/publish", nil, fx.admin); w.Code != http.StatusSeeOther {
		t.Fatalf("publish: %d", w.Code)
	}
	w := fx.get("/home", nil)
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/" || !strings.HasPrefix(w.Header().Get("Cache-Control"), "no-store") {
		t.Errorf("redirect: %d %q %q", w.Code, w.Header().Get("Location"), w.Header().Get("Cache-Control"))
	}
	sitemap := fx.get("/sitemap.xml", nil).Body.String()
	if strings.Count(sitemap, "<loc>https://example.com/</loc>") != 1 || strings.Contains(sitemap, "/home<") {
		t.Errorf("sitemap: %s", sitemap)
	}
	if nav := fx.f.DynamicNavLinks(); len(nav) != 1 || nav[0].Href != "/" {
		t.Errorf("nav: %+v", nav)
	}
	// 編集の画面は、トップページであることを伝える
	if body := fx.get("/admin/pages/"+itoa(id), fx.admin).Body.String(); !strings.Contains(body, "トップページとして / に出ます") {
		t.Errorf("edit page must say it is the top page")
	}
}

// buildWith は、固定ページの有無とトップページの指定を変えて、経路を作ります（起動の検査を確かめる）。
func buildWith(t *testing.T, withPages bool) error {
	t.Helper()
	core, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { core.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	reg := features.NewRegistry()
	reg.Register(site.NewFeature(logger))
	reg.Register(auth.NewFeature(logger))
	if withPages {
		p := NewFeature(logger)
		reg.Register(p)
	}
	for _, f := range reg.All() {
		if err := f.Init(core.DB()); err != nil {
			return err
		}
	}
	_, err = app.BuildRoutes(core, reg, false, logger)
	return err
}

// TestHome_StartupChecks は、HOME_PAGE_SLUG の形が違う・固定ページの機能が無い・既存の経路に当たるときは起動を止め、
// 正しい指定なら起動することを確かめる。
// 不変条件: PAGES-INV-15
func TestHome_StartupChecks(t *testing.T) {
	t.Setenv("SITE_URL", "")
	t.Setenv("CONTACT_FORM_URL", "")
	t.Setenv("AUTH_ORIGIN", "")
	t.Setenv("AUTH_RP_ID", "")
	for _, c := range []struct {
		slug      string
		withPages bool
		ok        bool
	}{
		{"home", true, true},
		{"", false, true},
		{"Home", true, false},
		{"a", true, false},
		{"home", false, false},
		{"privacy", true, false},
	} {
		t.Setenv("HOME_PAGE_SLUG", c.slug)
		if err := buildWith(t, c.withPages); (err == nil) != c.ok {
			t.Errorf("%q pages=%v: %v", c.slug, c.withPages, err)
		}
	}
}

// TestHome_NewsAndContactParts は、本文のお知らせの一覧とお問い合わせのボタンに中身が入り、公開の記事が無いカテゴリと無い名前の
// カテゴリが公開ページで同じ表示（一覧へのリンク無し）になり、読めなければブロックだけを省き、書く人には警告を出すことを確かめる。
// 不変条件: PAGES-INV-18, PAGES-INV-20
// ユースケース: UC-PAGES-9
func TestHome_NewsAndContactParts(t *testing.T) {
	fx := setup(t)
	at := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	var fail error
	fx.f.SetNewsList(func(_ context.Context, category string, n int) (features.NewsList, error) {
		switch {
		case fail != nil:
			return features.NewsList{}, fail
		case category == "":
			return features.NewsList{Found: true, Items: []richtext.NewsItem{{Title: "新着記事", Href: "/news/1", Date: at}}, ListPath: "/news"}, nil
		case category == "空":
			return features.NewsList{Found: true}, nil
		}
		return features.NewsList{}, nil
	})
	fx.create("top", "トップ", "お知らせ: 3\n\nお知らせ: 空 | 2\n\nお知らせ: 無い | 2\n\nお問い合わせ: 相談する", "", true)
	body := fx.get("/top", nil).Body.String()
	for _, want := range []string{
		`<a href="/news/1">新着記事</a>`, `<a href="/news">お知らせの一覧へ</a>`,
		`<a class="btn btn-primary" href="/contact">相談する</a>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %s", want)
		}
	}
	if strings.Count(body, "お知らせはまだありません。") != 2 || strings.Contains(body, "category=") {
		t.Errorf("empty and missing categories must look the same: %s", body)
	}
	if strings.Contains(body, "カテゴリ「") {
		t.Errorf("warnings must not appear on the public page")
	}
	fail = errors.New("db down")
	fx.logs.Reset()
	w := fx.get("/top", nil)
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "news-latest") || strings.Contains(w.Body.String(), "まだありません") {
		t.Errorf("a failed news read must omit only the block: %d", w.Code)
	}
	if !strings.Contains(fx.logs.String(), "pages news list failed") {
		t.Errorf("news failure must be logged: %s", fx.logs.String())
	}
	fail = nil
	// 書く人への警告（プレビュー・編集の画面・MCPの確認の文）
	warnings := fx.f.bodyWarnings(context.Background(), "お知らせ: 無い | 2\n## a {#x-1}\n## b {#x-1}")
	if len(warnings) != 2 || !strings.Contains(warnings[0], "カテゴリ「無い」がありません") || !strings.Contains(warnings[1], "{#x-1}") {
		t.Errorf("warnings: %q", warnings)
	}
	fx.f.newsList = nil
	if w := fx.f.bodyWarnings(context.Background(), "お知らせ: 3"); len(w) != 1 || !strings.Contains(w[0], "お知らせの機能が無い") {
		t.Errorf("no news feature: %q", w)
	}
	if notes := fx.f.mcpNotes(context.Background(), "top", "お知らせ: 3"); !strings.Contains(notes, "【注意: お知らせの機能が無い") {
		t.Errorf("MCP notes: %q", notes)
	}
}

// TestHome_ContactMissing は、お問い合わせの行き先が無いサイトでは、ボタンを描かず、書く人に警告することを確かめる。
// 不変条件: PAGES-INV-20
func TestHome_ContactMissing(t *testing.T) {
	f := NewFeature(slog.New(slog.NewTextHandler(io.Discard, nil)))
	doc, err := richtext.ParseForPages("お問い合わせ: 相談する")
	if err != nil {
		t.Fatal(err)
	}
	warnings := f.resolveParts(context.Background(), &doc, 0, true)
	if doc.Blocks[0].ContactHref != "" || len(warnings) != 1 || !strings.Contains(warnings[0], "お問い合わせの行き先") {
		t.Errorf("contact: %+v %q", doc.Blocks[0], warnings)
	}
}
