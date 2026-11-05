package browser

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/chromedp/chromedp"

	"minimal-web-kit/features"
	"minimal-web-kit/features/auth"
	"minimal-web-kit/features/auth/authtest"
	"minimal-web-kit/features/catalog"
	"minimal-web-kit/features/contact"
	"minimal-web-kit/features/documents"
	"minimal-web-kit/features/mcp"
	"minimal-web-kit/features/media"
	"minimal-web-kit/features/news"
	"minimal-web-kit/features/pages"
	"minimal-web-kit/features/records"
	"minimal-web-kit/features/records/defs"
	"minimal-web-kit/internal/app"
	"minimal-web-kit/internal/store"
)

// listScreens は一覧の型の画面です。test/admin_ui_test.go の kindList・kindListAdd の見本と突き合わせます。
var listScreens = []string{
	"/admin/accounts", "/admin/catalog", "/admin/catalog/classifications/categories", "/admin/catalog/classifications/tags",
	"/admin/contact", "/admin/contact/all", "/admin/documents", "/admin/documents/approvals", "/admin/documents/mine",
	"/admin/documents/transfer", "/admin/document-folders", "/admin/mcp", "/admin/media", "/admin/news", "/admin/news-categories", "/admin/pages",
	"/admin/records", "/admin/records/deleted-owners",
}

// listScreenPatterns は、経路に名前の入る一覧の型の画面と、開くURLです（業務記録の種類ごとの一覧）。
var listScreenPatterns = map[string]string{
	"/admin/records/{kind}": "/admin/records/equipment",
}

type screenMetrics struct {
	Path        string   `json:"path"`
	H1Left      float64  `json:"h1Left"`
	H1Top       float64  `json:"h1Top"`
	H1Size      float64  `json:"h1Size"`
	H1Margin    float64  `json:"h1Margin"`
	BarGap      *float64 `json:"barGap"`
	BarLeft     *float64 `json:"barLeft"`
	PrimaryH    *float64 `json:"primaryH"`
	PrimaryLeft *float64 `json:"primaryLeft"`
}

const measureScreen = `(() => {
	const main = document.querySelector("main");
	const h1 = main.querySelector("h1");
	const left = main.getBoundingClientRect().left;
	const m = {
		h1Left: h1.getBoundingClientRect().left - left,
		h1Top: h1.getBoundingClientRect().top,
		h1Size: parseFloat(getComputedStyle(h1).fontSize),
		h1Margin: parseFloat(getComputedStyle(h1).marginBottom),
	};
	const primary = main.querySelector(":scope > .action-bar .btn-primary, :scope > p > .btn-primary"); // フォームの中の送信ボタンは別のもの
	if (primary) {
		const box = primary.getBoundingClientRect();
		m.primaryH = box.height;
		m.primaryLeft = box.left - left;
		if (primary.parentElement.previousElementSibling === h1) {
			m.barGap = box.top - h1.getBoundingClientRect().bottom;
		}
	}
	const bar = main.querySelector(":scope > .action-bar");
	if (bar) {
		m.barLeft = bar.getBoundingClientRect().left - left;
	}
	return JSON.stringify(m);
})()`

// TestLayout_ListScreensMatch は、一覧の画面どうしで、見出しと操作の並びの位置と大きさが同じことを実測で確かめる。
// 画面の型の検査は要素の有無だけを見るので、余白のズレはここで捕まえる。
func TestLayout_ListScreensMatch(t *testing.T) {
	t.Setenv("AUTH_ORIGIN", "")
	t.Setenv("AUTH_RP_ID", "")
	core, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { core.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	reg := features.NewRegistry()
	reg.Register(auth.NewFeature(logger))
	reg.Register(news.NewFeature(logger))
	reg.Register(catalog.NewFeature(logger))
	reg.Register(contact.NewFeature(logger))
	reg.Register(pages.NewFeature(logger))
	reg.Register(documents.NewFeature(logger))
	reg.Register(records.NewFeature(logger, defs.Defs, defs.Retired))
	reg.Register(media.NewFeature(logger))
	reg.Register(mcp.NewFeature(logger))
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
	cookie, _ := authtest.NewAccountSession(t, core.DB(), "admin")
	b := newBrowser(t)
	b.do(t, setCookie(srv.URL, cookie))
	for _, width := range []int64{1280, 390} {
		b.do(t, chromedp.EmulateViewport(width, 900))
		var base *screenMetrics
		firsts := map[string]*float64{}
		paths := append([]string{}, listScreens...)
		for _, u := range listScreenPatterns {
			paths = append(paths, u)
		}
		for _, path := range paths {
			b.do(t, chromedp.Navigate(srv.URL+path))
			b.waitFor(t, `document.readyState === "complete"`)
			var m screenMetrics
			if err := json.Unmarshal([]byte(evaluate[string](t, b, measureScreen)), &m); err != nil {
				t.Fatal(err)
			}
			m.Path = path
			if base == nil {
				c := m
				base = &c
				continue
			}
			for _, c := range []struct {
				name      string
				got, want float64
			}{
				{"見出しの左端", m.H1Left, base.H1Left},
				{"見出しの上端", m.H1Top, base.H1Top},
				{"見出しの大きさ", m.H1Size, base.H1Size},
				{"見出しの下の余白", m.H1Margin, base.H1Margin},
			} {
				if math.Abs(c.got-c.want) > 0.5 {
					t.Errorf("幅%d %s: %s %.1f、%s は %.1f", width, path, c.name, c.got, base.Path, c.want)
				}
			}
			compare(t, fmt.Sprintf("幅%d %s", width, path), "見出しから操作の並びまで", m.BarGap, firsts)
			compare(t, fmt.Sprintf("幅%d %s", width, path), "操作の並びの左端", m.BarLeft, firsts)
			compare(t, fmt.Sprintf("幅%d %s", width, path), "主のボタンの高さ", m.PrimaryH, firsts)
			compare(t, fmt.Sprintf("幅%d %s", width, path), "主のボタンの左端", m.PrimaryLeft, firsts)
		}
	}
	b.noErrors(t)
}

// compare は、最初に見つかった画面の値と比べます。その要素を持たない画面は比べません。
func compare(t *testing.T, where, name string, got *float64, firsts map[string]*float64) {
	t.Helper()
	if got == nil {
		return
	}
	if firsts[name] == nil {
		firsts[name] = got
		return
	}
	if math.Abs(*got-*firsts[name]) > 0.5 {
		t.Errorf("%s: %s %.1f、他の画面は %.1f", where, name, *got, *firsts[name])
	}
}
