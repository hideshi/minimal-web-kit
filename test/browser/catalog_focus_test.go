package browser

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chromedp/chromedp"

	"minimal-web-kit/features"
	"minimal-web-kit/features/auth"
	"minimal-web-kit/features/catalog"
	"minimal-web-kit/internal/app"
	"minimal-web-kit/internal/store"
)

// カタログの公開一覧で、検索の結果を差し替えてもフォーカスを動かさず件数の案内を読み上げ、
// ページ送りでは結果の見出しへフォーカスを移すこと。
func TestCatalogList_FocusAndStatus(t *testing.T) {
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
	reg.Register(catalog.NewFeature(logger))
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
	items := catalog.NewStore(core.DB())
	for i := range 25 {
		id, err := items.Save(context.Background(), 0, catalog.Input{Title: fmt.Sprintf("項目%d", i)})
		if err != nil {
			t.Fatal(err)
		}
		if err := items.SetPublished(context.Background(), id, true); err != nil {
			t.Fatal(err)
		}
	}

	b := newBrowser(t)
	b.do(t, chromedp.Navigate(srv.URL+"/catalog"))
	b.waitFor(t, `window.htmx && document.getElementById("catalog-results-heading")`)

	// 検索: 入力欄にフォーカスを残したまま送り、結果だけが差し替わる
	b.do(t, chromedp.Focus("#catalog-filter input[name=q]"))
	b.typeText(t, "項目24")
	b.press(t, "Enter")
	b.waitFor(t, `document.querySelectorAll("#catalog-results .card").length === 1`)
	b.waitFor(t, `document.getElementById("catalog-status").textContent.includes("1件")`)
	if got := evaluate[string](t, b, `document.activeElement.name || document.activeElement.id`); got != "q" {
		t.Fatalf("search moved the focus to %q", got)
	}
	if !strings.Contains(evaluate[string](t, b, "location.search"), "q=") {
		t.Fatal("search must be in the URL")
	}

	// ページ送り: 結果の見出しへフォーカスを移し、案内を読み上げる
	b.do(t, chromedp.Navigate(srv.URL+"/catalog"))
	b.waitFor(t, `window.htmx && document.querySelector('#catalog-results a[rel="next"]')`)
	b.do(t, chromedp.Click(`#catalog-results a[rel="next"]`))
	b.waitFor(t, `document.activeElement.id === "catalog-results-heading"`)
	b.waitFor(t, `document.getElementById("catalog-status").textContent.includes("2ページ")`)
	b.noErrors(t)
}
