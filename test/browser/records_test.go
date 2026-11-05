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
	"minimal-web-kit/features/records"
	"minimal-web-kit/features/records/defs"
	"minimal-web-kit/internal/app"
	"minimal-web-kit/internal/store"
)

// TestRecords_Screens は、業務記録の画面（見本の定義）を、狭い画面と広い画面で撮り、ページが横にはみ出さないことを確かめます。
// 撮った画面は shots/ に置き、余白・並び・文の自然さを人が見ます。
func TestRecords_Screens(t *testing.T) {
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
	reg.Register(records.NewFeature(logger, defs.Defs, defs.Retired))
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
	cookie, me := authtest.NewAccountSession(t, db, "admin")
	now := time.Now().Unix()
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO records_daily_report (owner_account, revision, created_at, updated_at, f_work_date, f_body, f_next_plan) VALUES (?, 1, ?, ?, '2026-11-06', ?, ?)`,
			[]any{me, now, now, "午前は見積もりの作成。午後はお客様先で打ち合わせ（次回の日程を調整）。\n\n移動の時間に、来週の資料の構成を考えた。長い単語の例: https://example.com/a/very/long/path/that/should/wrap/somewhere", "資料の作成と、打ち合わせの議事録の共有"}},
		{`INSERT INTO records_daily_report (owner_account, revision, created_at, updated_at, f_work_date, f_body) VALUES (987654, 1, ?, ?, '2026-01-05', '辞めた人の日報')`, []any{now, now}},
		{`INSERT INTO records_equipment (owner_account, revision, created_at, updated_at, f_item_name, f_asset_number, f_status, f_user_name) VALUES (?, 1, ?, ?, 'ノートPC（14インチ）', 'PC-2026-0001', '貸出中', '山田')`, []any{me, now, now}},
		{`INSERT INTO records_equipment (owner_account, revision, created_at, updated_at, f_item_name, f_asset_number, f_status) VALUES (?, 1, ?, ?, 'プロジェクター', 'PJ-0003', '在庫')`, []any{me, now, now}},
	} {
		if _, err := db.Exec(q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
	b := newBrowser(t)
	b.do(t, setCookie(srv.URL, cookie))
	if err := os.MkdirAll("shots", 0o700); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ name, path string }{
		{"80-records-kinds", "/admin/records"},
		{"81-records-daily-list", "/admin/records/daily_report"},
		{"82-records-equipment-list", "/admin/records/equipment"},
		{"83-records-daily-detail", "/admin/records/daily_report/1"},
		{"84-records-daily-new", "/admin/records/daily_report/new"},
		{"85-records-equipment-edit", "/admin/records/equipment/1/edit"},
		{"86-records-delete", "/admin/records/daily_report/1/delete"},
		{"87-records-deleted-owners", "/admin/records/deleted-owners"},
	} {
		b.do(t, chromedp.Navigate(srv.URL+c.path))
		for _, width := range []int64{1100, 360} {
			ctx, cancel := context.WithTimeout(b.ctx, 20*time.Second)
			if _, err := chromedp.Run(ctx, chromedp.EmulateViewport(width, 800)); err != nil {
				cancel()
				t.Fatal(err)
			}
			cancel()
			if !evaluate[bool](t, b, `document.documentElement.scrollWidth <= document.documentElement.clientWidth`) {
				t.Errorf("%s: %dpxでページが横にはみ出す", c.path, width)
			}
			ctx, cancel = context.WithTimeout(b.ctx, 20*time.Second)
			buf, err := chromedp.Run(ctx, chromedp.FullScreenshot(85))
			cancel()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join("shots", fmt.Sprintf("%s-%d.jpg", c.name, width)), buf, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
}
