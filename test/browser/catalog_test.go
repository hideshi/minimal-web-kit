package browser

import (
	"bytes"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/chromedp/chromedp"

	"minimal-web-kit/features"
	"minimal-web-kit/features/auth"
	"minimal-web-kit/features/auth/authtest"
	"minimal-web-kit/features/catalog"
	"minimal-web-kit/internal/app"
	"minimal-web-kit/internal/store"
)

// TestCatalogAdminForm_TagCombobox は、カタログの項目の作成画面で、部品からタグを選んで保存できることを確かめます。
// 管理者のセッションは、既存のテスト用ヘルパー（authtest）でDBに直接作ります。
func TestCatalogAdminForm_TagCombobox(t *testing.T) {
	t.Setenv("AUTH_ORIGIN", "")
	t.Setenv("AUTH_RP_ID", "")
	core, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { core.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	reg := features.NewRegistry()
	reg.Register(catalog.NewFeature(logger))
	reg.Register(auth.NewFeature(logger))
	for _, f := range reg.All() {
		if err := f.Init(core.DB()); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"新築", "リフォーム", "木造", "省エネ", "住宅"} {
		if _, err := core.DB().Exec("INSERT INTO catalog_tags (name) VALUES (?)", name); err != nil {
			t.Fatal(err)
		}
	}
	routes, err := app.BuildRoutes(core, reg, false, logger)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(routes)
	t.Cleanup(srv.Close)
	cookie := authtest.NewSession(t, core.DB())

	b := newBrowser(t)
	b.do(t, setCookie(srv.URL, cookie), chromedp.Navigate(srv.URL+"/admin/catalog/new"))
	b.waitFor(t, `document.getElementById("item-tags-input") && !document.querySelector("[data-combobox-ui]").hidden`)
	b.do(t, chromedp.SendKeys(chromedp.ID("item-title"), "試しの項目"), chromedp.Click(chromedp.ID("item-tags-input")))
	b.typeText(t, "リフォ")
	b.press(t, "ArrowDown", "Enter")
	b.typeText(t, "省")
	b.press(t, "ArrowDown", "Enter", "Escape")
	if got := evaluate[string](t, b, chips("item-tags")); got != "リフォーム,省エネ" {
		t.Fatalf("selected tags: %q", got)
	}
	b.do(t, chromedp.Click(chromedp.CSS(`form.card button[type=submit]`)))
	b.waitFor(t, `location.pathname.startsWith("/admin/catalog/") && location.pathname !== "/admin/catalog/new"`)

	var tags bytes.Buffer
	rows, err := core.DB().Query("SELECT t.name FROM catalog_item_tags it JOIN catalog_tags t ON t.id = it.tag_id JOIN catalog_items i ON i.id = it.item_id WHERE i.title = ? ORDER BY t.name", "試しの項目")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		tags.WriteString(name + ",")
	}
	if tags.String() != "リフォーム,省エネ," {
		t.Fatalf("saved tags: %q", tags.String())
	}
	// 保存後の編集画面でも、保存したタグが選択済みとして表示される。
	b.waitFor(t, `document.querySelector("[data-combobox-ui]") && !document.querySelector("[data-combobox-ui]").hidden`)
	if got := evaluate[string](t, b, chips("item-tags")); got != "リフォーム,省エネ" {
		t.Fatalf("tags after saving: %q", got)
	}
	b.noErrors(t)
}
