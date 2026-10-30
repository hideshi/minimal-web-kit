package browser

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/chromedp"

	"minimal-web-kit/features"
	"minimal-web-kit/features/auth"
	"minimal-web-kit/features/catalog"
	"minimal-web-kit/features/contact"
	"minimal-web-kit/features/news"
	"minimal-web-kit/features/pages"
	"minimal-web-kit/features/site"
	"minimal-web-kit/internal/app"
	"minimal-web-kit/internal/store"
)

// newPublicServer は、Siteを含む全機能を有効にした経路を作り、公開の詳細ページの見本を入れます。
// 返すのはサーバーと、タグ付きのカタログの項目のパスです。
func newPublicServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	t.Setenv("AUTH_ORIGIN", "")
	t.Setenv("AUTH_RP_ID", "")
	core, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { core.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	reg := features.NewRegistry()
	reg.Register(site.NewFeature(logger))
	reg.Register(auth.NewFeature(logger))
	reg.Register(news.NewFeature(logger))
	reg.Register(catalog.NewFeature(logger))
	reg.Register(contact.NewFeature(logger))
	reg.Register(pages.NewFeature(logger))
	for _, f := range reg.All() {
		if err := f.Init(core.DB()); err != nil {
			t.Fatal(err)
		}
	}
	routes, err := app.BuildRoutes(core, reg, false, logger)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(routes)
	t.Cleanup(srv.Close)

	ctx := context.Background()
	posts := news.NewPostStore(core.DB())
	post, _ := posts.Create(ctx, "見本のお知らせ", "1段落目の本文です。\n\n2段落目の本文です。")
	_ = posts.Publish(ctx, post)
	items := catalog.NewStore(core.DB())
	category, _ := items.SaveClassification(ctx, "categories", 0, "同梱機能")
	first, _ := items.SaveClassification(ctx, "tags", 0, "公開ページ")
	second, _ := items.SaveClassification(ctx, "tags", 0, "管理画面")
	item, _ := items.Save(ctx, 0, catalog.Input{Title: "見本の項目", Body: "1段落目の本文です。\n\n2段落目の本文です。", CategoryID: category, TagIDs: []int64{first, second}})
	_ = items.SetPublished(ctx, item, true)
	return srv, fmt.Sprintf("/catalog/%d", item)
}

// TestPublicScreens は、Siteを有効にした公開ページとログイン画面を撮り、狭い幅でページ全体が横にはみ出さないことを確かめます。
func TestPublicScreens(t *testing.T) {
	srv, itemPath := newPublicServer(t)
	b := newBrowser(t)
	dir := "shots"
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{
		"19-site-top":       "/",
		"20-site-news-show": "/news/1",
		"21-site-catalog":   "/catalog",
		"22-site-item":      itemPath,
		"23-site-contact":   "/contact",
		"24-site-not-found": "/no-such-page",
		"25-admin-login":    "/admin/login",
		"26-site-privacy":   "/privacy",
	} {
		b.do(t, chromedp.Navigate(srv.URL+path))
		for _, width := range []int64{1100, 360, 320} {
			ctx, cancel := context.WithTimeout(b.ctx, 20*time.Second)
			if _, err := chromedp.Run(ctx, chromedp.EmulateViewport(width, 800)); err != nil {
				cancel()
				t.Fatal(err)
			}
			buf, err := chromedp.Run(ctx, chromedp.FullScreenshot(85))
			cancel()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%s-%d.jpg", name, width)), buf, 0o600); err != nil {
				t.Fatal(err)
			}
			if !evaluate[bool](t, b, `document.documentElement.scrollWidth <= document.documentElement.clientWidth`) {
				t.Errorf("%s: %dpxでページが横にはみ出す", path, width)
			}
		}
	}
}

// TestNav_States は、ナビの3つの状態（今いるページ・マウスを乗せる・フォーカス）が見分けられ、
// マウスを乗せてもリンクの大きさが変わらないことを確かめます。
func TestNav_States(t *testing.T) {
	srv, _ := newPublicServer(t)
	b := newBrowser(t)
	b.do(t, chromedp.Navigate(srv.URL+"/news"))
	ctx, cancel := context.WithTimeout(b.ctx, 20*time.Second)
	defer cancel()
	if _, err := chromedp.Run(ctx, chromedp.EmulateViewport(1100, 800)); err != nil {
		t.Fatal(err)
	}
	const current = `document.querySelector('header nav a[aria-current="page"]')`
	if got := evaluate[string](t, b, `getComputedStyle(`+current+`).fontWeight`); got != "700" {
		t.Errorf("今いるページの太さ: %s", got)
	}
	if got := evaluate[string](t, b, `getComputedStyle(`+current+`).borderTopColor`); got == "rgba(0, 0, 0, 0)" {
		t.Error("今いるページの枠線が透明")
	}

	const other = `document.querySelector('header nav a[href="/privacy"]')`
	if got := evaluate[string](t, b, `getComputedStyle(`+other+`).textDecorationLine`); got != "none" {
		t.Errorf("ふだんの下線: %s", got)
	}
	size := `(() => { const r = ` + other + `.getBoundingClientRect(); return r.width + "x" + r.height })()`
	before := evaluate[string](t, b, size)
	moveMouse(t, b, other)
	b.waitFor(t, other+`.matches(":hover")`)
	if got := evaluate[string](t, b, `getComputedStyle(`+other+`).backgroundColor`); got != "rgb(242, 242, 242)" {
		t.Errorf("マウスを乗せたときの背景: %s", got)
	}
	if got := evaluate[string](t, b, `getComputedStyle(`+other+`).textDecorationLine`); got != "underline" {
		t.Errorf("マウスを乗せたときの下線: %s", got)
	}
	if after := evaluate[string](t, b, size); after != before {
		t.Errorf("マウスを乗せると大きさが変わる: %s → %s", before, after)
	}

	// 今いるページに、マウスとキーボードのフォーカスを重ねた状態
	moveMouse(t, b, current)
	b.press(t, "Tab")
	evaluate[any](t, b, current+`.focus()`)
	b.waitFor(t, current+`.matches(":focus-visible")`)
	if got := evaluate[string](t, b, `getComputedStyle(`+current+`).outlineStyle`); got != "solid" {
		t.Errorf("フォーカスの枠: %s", got)
	}
	buf, err := chromedp.Run(ctx, chromedp.CaptureScreenshot())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join("shots", "27-nav-current-hover-focus-1100.png"), buf, 0o600); err != nil {
		t.Fatal(err)
	}
	b.noErrors(t)
}

// moveMouse は、式で指定した要素の中心へマウスを動かします。
func moveMouse(t *testing.T, b *browser, element string) {
	t.Helper()
	x := evaluate[float64](t, b, `(() => { const r = `+element+`.getBoundingClientRect(); return r.left + r.width / 2 })()`)
	y := evaluate[float64](t, b, `(() => { const r = `+element+`.getBoundingClientRect(); return r.top + r.height / 2 })()`)
	b.do(t, chromedp.Func(func(ctx context.Context, target *chromedp.Target) error {
		_, err := cdp.Call(ctx, target, input.DispatchMouseEvent, input.DispatchMouseEventParams{Type: input.DispatchMouseEventTypeMouseMoved, X: x, Y: y})
		return err
	}))
}
