package browser

import (
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chromedp/chromedp"

	"minimal-web-kit/features"
	"minimal-web-kit/features/site"
	"minimal-web-kit/internal/app"
	"minimal-web-kit/internal/store"
)

// トップの主のボタンと補助のボタンの、左右の端がそろっていることを確かめる。
// キットの紹介ページの部品を見るので、トップを書き換えて印 kit-landing を消したサイトでは検査しない（test/landing_test.go と同じ）。
func TestLanding_HeroButtonsAlign(t *testing.T) {
	page, err := os.ReadFile("../../features/site/views/index.html")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(page), "<!-- kit-landing:") {
		t.Skip("the top page is not the kit's landing page")
	}
	core, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { core.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	reg := features.NewRegistry()
	reg.Register(site.NewFeature(logger))
	routes, err := app.BuildRoutes(core, reg, false, logger)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(routes)
	t.Cleanup(srv.Close)
	b := newBrowser(t)
	for _, width := range []int64{1280, 360} {
		b.do(t, chromedp.EmulateViewport(width, 800), chromedp.Navigate(srv.URL+"/"))
		b.waitFor(t, `document.readyState === "complete"`)
		gap := evaluate[float64](t, b, `(() => {
			const main = document.querySelector(".hero-cta .btn-primary").getBoundingClientRect();
			const links = [...document.querySelectorAll(".hero-links .btn")].map(e => e.getBoundingClientRect());
			return Math.max(Math.abs(main.left - Math.min(...links.map(r => r.left))), Math.abs(main.right - Math.max(...links.map(r => r.right))));
		})()`)
		if gap > 1 {
			t.Errorf("width %d: the buttons differ by %.1fpx", width, gap)
		}
		// ボタンの文字が1行に収まること
		if evaluate[bool](t, b, `[...document.querySelectorAll(".hero-links .btn, .hero-cta .btn-main-text")].some(e => e.getClientRects().length > 1 || e.getBoundingClientRect().height > 60)`) {
			t.Errorf("width %d: a button label wraps", width)
		}
		if evaluate[bool](t, b, `document.documentElement.scrollWidth > document.documentElement.clientWidth`) {
			t.Errorf("width %d: the page overflows", width)
		}
	}
}
