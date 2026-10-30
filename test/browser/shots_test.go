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

	"github.com/chromedp/chromedp"

	"minimal-web-kit/features"
	"minimal-web-kit/features/auth"
	"minimal-web-kit/features/auth/authtest"
	"minimal-web-kit/features/catalog"
	"minimal-web-kit/features/contact"
	"minimal-web-kit/features/news"
	"minimal-web-kit/features/pages"
	"minimal-web-kit/internal/app"
	"minimal-web-kit/internal/store"
)

// TestAdminScreenshots は、主な管理画面（画面の型ごと・空の状態・入力の失敗）を2つの幅で撮り、
// test/browser/shots/ に書き出します。画像は自動で比べず、人かAIが見て、余白・並び・文の崩れを確かめます
// （docs/admin-ui-standard-plan.md 7節）。構造の検査は test/admin_ui_test.go が受け持ちます。
func TestAdminScreenshots(t *testing.T) {
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
	db := core.DB()
	cookie, self := authtest.NewAccountSession(t, db, "admin")
	authtest.NewCredential(t, db, self, "管理者のPC")
	_, other := authtest.NewAccountSession(t, db, "member")
	authtest.NewCredential(t, db, other, "メンバーのキー")

	dir := "shots"
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	b := newBrowser(t)
	b.do(t, setCookie(srv.URL, cookie))
	shoot := func(name, path string) {
		t.Helper()
		b.do(t, chromedp.Navigate(srv.URL+path))
		for _, width := range []int64{1100, 360} {
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
		}
		// 表の横スクロールのしきい値（docs/components.md の3節）: 幅600px以上は全部の表、
		// 360px以上は4列までの表を、折り返して収める。本文の表は長いURL等があるため対象にしない
		for _, width := range []int64{1100, 768, 600, 390, 360} {
			ctx, cancel := context.WithTimeout(b.ctx, 20*time.Second)
			_, err := chromedp.Run(ctx, chromedp.EmulateViewport(width, 800))
			cancel()
			if err != nil {
				t.Fatal(err)
			}
			maxColumns := 4
			if width >= 600 {
				maxColumns = 99
			}
			got := evaluate[string](t, b, fmt.Sprintf(`Array.from(document.querySelectorAll(".table-container"))
				.filter(c => !c.closest(".richtext") && c.querySelector("table").rows[0].cells.length <= %d && c.scrollWidth > c.clientWidth)
				.map(c => c.querySelector("caption").textContent + ": " + (c.scrollWidth - c.clientWidth) + "px").join(", ")`, maxColumns))
			if got != "" {
				t.Errorf("%s: 幅%dpxで表が横に動く（%s）", name, width, got)
			}
		}
	}

	// 空の状態
	shoot("01-catalog-list-empty", "/admin/catalog")
	shoot("02-categories-empty", "/admin/catalog/classifications/categories")

	// データあり
	ctx := context.Background()
	items := catalog.NewStore(db)
	category, _ := items.SaveClassification(ctx, "categories", 0, "住宅")
	tag, _ := items.SaveClassification(ctx, "tags", 0, "新築")
	item, _ := items.Save(ctx, 0, catalog.Input{Title: "見本の項目", CategoryID: category, TagIDs: []int64{tag}})
	posts := news.NewPostStore(db)
	post, _ := posts.Create(ctx, "見本のお知らせ", "本文")
	shoot("03-catalog-list", "/admin/catalog")
	shoot("04-categories", "/admin/catalog/classifications/categories")
	shoot("05-catalog-form", fmt.Sprintf("/admin/catalog/%d", item))
	shoot("06-catalog-delete", fmt.Sprintf("/admin/catalog/%d/delete", item))
	shoot("07-news-list", "/admin/news")
	shoot("08-news-form", fmt.Sprintf("/admin/news/%d", post))
	shoot("09-account-detail", fmt.Sprintf("/admin/accounts/%d", other))
	shoot("10-passkeys", "/admin/passkeys")
	inbox := contact.NewStore(db)
	msg, _ := inbox.Save(ctx, contact.Input{Name: "見本の人", ReplyTo: "reply@example.com", Body: "見本のお問い合わせです。\n\n2段落目です。"}, contact.Limits{Hourly: 10, Daily: 10, Unread: 10})
	_, _ = inbox.Save(ctx, contact.Input{Body: "無記名のお問い合わせ"}, contact.Limits{Hourly: 10, Daily: 10, Unread: 10})
	shoot("12-contact-form", "/contact")
	shoot("13-contact-inbox", "/admin/contact")
	shoot("14-contact-message", fmt.Sprintf("/admin/contact/%d", msg))
	pageStore := pages.NewStore(db)
	page, _ := pageStore.Save(ctx, 0, pages.Input{Slug: "about", Title: "会社概要", NavOrder: "1", Body: "## 概要\n小さなチームで、個人情報を集めないWebサイトを作っています。\n詳しくは[お知らせ](/news)をご覧ください。\n\n表: 会社の情報\n| 項目 | 内容 |\n|---|---|\n| 社名 | 例の会社 |\n| 所在地 | 東京都 |\n| 有効にするコマンド | make add-catalog |\n| 資料 | https://github.com/hideshi/minimal-web-kit/blob/main/docs/data-inventory.md |\n\n表: 機能と設定\n| 機能 | 有効にするコマンド | 設定 | 説明 |\n|---|---|---|---|\n| カタログ | make add-catalog | なし | 項目の一覧と詳細 |\n| お問い合わせ | make add-contact | CONTACT_RETENTION_DAYS | 受信箱。internationalization のような長い単語も入れる |\n\n### 大切にしていること\n- 取得するデータを減らす\n- **依存**を減らす\n1. 計画\n2. 実装"}, nil)
	_ = pageStore.SetPublished(ctx, page, true)
	shoot("15-page-public", "/about")
	// 公開一覧の絞り込み（タグ20個。狭い画面での折り返しを見る）
	for i, name := range []string{"新築", "リフォーム", "木造", "鉄骨", "省エネ", "断熱", "耐震", "バリアフリー", "店舗", "オフィス", "住宅", "倉庫", "外構", "内装", "水回り", "屋根", "外壁", "太陽光", "蓄電池", "地域材"} {
		tagID, _ := items.SaveClassification(ctx, "tags", 0, name)
		if i < 3 {
			pub, _ := items.Save(ctx, 0, catalog.Input{Title: "公開の項目 " + name, CategoryID: category, TagIDs: []int64{tagID}})
			_ = items.SetPublished(ctx, pub, true)
		} else {
			pub, _ := items.Save(ctx, 0, catalog.Input{Title: "項目 " + name, TagIDs: []int64{tagID}})
			_ = items.SetPublished(ctx, pub, true)
		}
	}
	shoot("18-catalog-public", "/catalog?tag="+fmt.Sprint(tag))
	shoot("16-pages-admin", "/admin/pages")
	shoot("17-page-form", fmt.Sprintf("/admin/pages/%d", page))

	// 入力の失敗（同じ名前のカテゴリを追加する）。応答は400のため、ブラウザのログのエラーは確かめない
	b.do(t, chromedp.Navigate(srv.URL+"/admin/catalog/classifications/categories"),
		chromedp.SendKeys(chromedp.ID("classification-name"), "住宅"),
		chromedp.Click(chromedp.CSS(`form.card button[type=submit]`)))
	b.waitFor(t, `document.querySelector('[aria-invalid="true"]') !== null`)
	for _, width := range []int64{1100, 360} {
		ctx, cancel := context.WithTimeout(b.ctx, 20*time.Second)
		_, _ = chromedp.Run(ctx, chromedp.EmulateViewport(width, 800))
		buf, err := chromedp.Run(ctx, chromedp.FullScreenshot(85))
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		_ = os.WriteFile(filepath.Join(dir, fmt.Sprintf("11-category-duplicate-%d.jpg", width)), buf, 0o600)
	}
	t.Logf("撮影した画面: %s", dir)
}
