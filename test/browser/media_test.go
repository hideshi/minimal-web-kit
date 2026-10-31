package browser

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"

	"minimal-web-kit/features"
	"minimal-web-kit/features/auth"
	"minimal-web-kit/features/auth/authtest"
	"minimal-web-kit/features/catalog"
	"minimal-web-kit/features/media"
	"minimal-web-kit/features/news"
	"minimal-web-kit/features/pages"
	"minimal-web-kit/features/site"
	"minimal-web-kit/internal/app"
	"minimal-web-kit/internal/store"
)

func addSampleImage(t *testing.T, db *sql.DB, id string, w, h int) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.SetRGBA(x, y, color.RGBA{uint8(x * 255 / w), uint8(y * 255 / h), 120, 255})
		}
	}
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO media_items (id, format, width, height, bytes, alt, created_at) VALUES (?, 'jpg', ?, ?, ?, '見本の画像', ?)", id, w, h, b.Len(), time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO media_blobs (id, data) VALUES (?, ?)", id, b.Bytes()); err != nil {
		t.Fatal(err)
	}
	return id + ".jpg"
}

// TestMedia_ImagesFitTheLayout は、公開ページの画像が読み込まれ、本文の幅に収まり、小さい画像を引き伸ばさないことを確かめます。
// 管理画面の画像の一覧と代表画像の選択も、狭い幅で崩れないことを確かめます。
//
// 不変条件: MEDIA-INV-1
func TestMedia_ImagesFitTheLayout(t *testing.T) {
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
	reg.Register(pages.NewFeature(logger))
	reg.Register(media.NewFeature(logger))
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
	wide := addSampleImage(t, db, strings.Repeat("a", 32), 1600, 600)
	small := addSampleImage(t, db, strings.Repeat("b", 32), 200, 100)

	// 公開の判定まで通すため、保存の経路を使う。参照は画面の保存と同じ口で作る
	if err := app.RunStartup(context.Background(), reg, logger); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		"INSERT INTO pages (slug, title, body, status, created_at, updated_at) VALUES ('about-us', '会社概要', ?, 'published', 1, 1)",
		"本文の段落です。\n\n![横長の写真](/media/"+wide+")\n\n![小さな図](/media/"+small+")\n\n続きの段落です。"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO news_posts (title, body, status, published_at, created_at, updated_at, cover_file) VALUES ('お知らせ', '本文', 'published', 1, 1, 1, ?)", wide); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO catalog_items (title, body, status, created_at, updated_at, cover_file) VALUES ('項目', '本文', 'published', 1, 1, ?)", small); err != nil {
		t.Fatal(err)
	}
	if err := app.RunStartup(context.Background(), reg, logger); err != nil {
		t.Fatal(err)
	}

	cookie, _ := authtest.NewAccountSession(t, db, "admin")
	b := newBrowser(t)
	b.do(t, setCookie(srv.URL, cookie))
	dir := "shots"
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, path string
		images     int
	}{
		{"60-media-page", "/about-us", 2},
		{"61-media-news", "/news/1", 1},
		{"62-media-catalog", "/catalog/1", 1},
		{"63-media-catalog-list", "/catalog", 1},
		{"64-media-admin", "/admin/media", 2},
		{"65-media-news-form", "/admin/news/1", 2},
		{"66-media-page-form", "/admin/pages/1", 2},
	} {
		b.do(t, chromedp.Navigate(srv.URL+c.path))
		for _, width := range []int64{1100, 360, 320} {
			ctx, cancel := context.WithTimeout(b.ctx, 20*time.Second)
			if _, err := chromedp.Run(ctx, chromedp.EmulateViewport(width, 800)); err != nil {
				cancel()
				t.Fatal(err)
			}
			cancel()
			b.waitFor(t, fmt.Sprintf(`(() => { document.querySelectorAll('img').forEach(i => i.loading = 'eager'); return [...document.images].filter(i => i.complete && i.naturalWidth > 0).length >= %d })()`, c.images))
			if !evaluate[bool](t, b, `document.documentElement.scrollWidth <= document.documentElement.clientWidth`) {
				t.Errorf("%s: %dpxでページが横にはみ出す", c.path, width)
			}
			ctx, cancel = context.WithTimeout(b.ctx, 20*time.Second)
			buf, err := chromedp.Run(ctx, chromedp.FullScreenshot(85))
			cancel()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%s-%d.jpg", c.name, width)), buf, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}

	// 代表画像の選択の、選んでいる画像が読み込まれている
	b.do(t, chromedp.Navigate(srv.URL+"/admin/news/1"))
	b.waitFor(t, `(() => { document.querySelectorAll('img').forEach(i => i.loading = 'eager'); return document.querySelector('input[name="cover"]:checked').parentElement.querySelector('img').naturalWidth === 1600 })()`)

	b.do(t, chromedp.Navigate(srv.URL+"/about-us"))
	ctx, cancel := context.WithTimeout(b.ctx, 20*time.Second)
	_, err = chromedp.Run(ctx, chromedp.EmulateViewport(1100, 800))
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	b.waitFor(t, `(() => { document.querySelectorAll('img').forEach(i => i.loading = 'eager'); return [...document.images].every(i => i.complete && i.naturalWidth > 0) })()`)
	if got := evaluate[int](t, b, `document.querySelector('img[alt="小さな図"]').clientWidth`); got != 200 {
		t.Errorf("small image stretched: %d", got)
	}
	if !evaluate[bool](t, b, `(() => { const i = document.querySelector('img[alt="横長の写真"]'); return i.clientWidth <= i.parentElement.clientWidth && i.clientWidth < 1600 })()`) {
		t.Error("wide image must fit the column")
	}
	b.noErrors(t)
}
