package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/csv"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"minimal-web-kit/features/media"
	"minimal-web-kit/features/news"
	"minimal-web-kit/features/pages"
	"minimal-web-kit/internal/store"
)

// canaries は、見本の WXR に入れた、どの出力にも出てはならない値です（計画10.9）。
var canaries = []string{
	"CANARY_AUTHOR", "canary-author@", "CANARY_COMMENT", "canary-comment@", "203.0.113.77", "CANARY_PASSWORD",
	"CANARY_POSTMETA", "CANARY_ATTACHMENT_META", "CANARY_EXCERPT", "CANARY_TAG", "CANARY_SITE_DESCRIPTION",
	"CANARY_PRIVATE", "CANARY_PLUGIN",
}

type fixture struct {
	dir, live, uploads string
}

// newFixture は、お知らせ・固定ページ・画像を有効にした稼働DBと、アップロードのフォルダを作ります。
func newFixture(t *testing.T, withMedia bool) fixture {
	t.Helper()
	dir := t.TempDir()
	fx := fixture{dir: dir, live: filepath.Join(dir, "app.db"), uploads: filepath.Join(dir, "uploads")}
	s, err := store.NewSQLiteStore(fx.live)
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := news.NewFeature(logger).Init(s.DB()); err != nil {
		t.Fatal(err)
	}
	pf := pages.NewFeature(logger)
	if err := pf.Init(s.DB()); err != nil {
		t.Fatal(err)
	}
	if _, err := pf.Import(context.Background(), pages.Input{Slug: "company", Title: "既にある会社概要"}); err != nil {
		t.Fatal(err)
	}
	if withMedia {
		if err := media.NewFeature(logger).Init(s.DB()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.DB().Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatal(err)
	}
	s.Close()

	write := func(rel string, data []byte) {
		path := filepath.Join(fx.uploads, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	img := image.NewRGBA(image.Rect(0, 0, 40, 30))
	for x := range 40 {
		for y := range 30 {
			img.Set(x, y, color.RGBA{uint8(x * 6), uint8(y * 8), 90, 255})
		}
	}
	var j, p bytes.Buffer
	_ = jpeg.Encode(&j, img, nil)
	_ = png.Encode(&p, img)
	write("2024/05/shop-scaled.jpg", j.Bytes())
	write("2024/05/inside.png", p.Bytes())
	write("2024/05/extra-unlisted.jpg", j.Bytes())
	if err := os.WriteFile(filepath.Join(dir, "secret.txt"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	return fx
}

func (fx fixture) path(name string) string { return filepath.Join(fx.dir, name) }

func fileHash(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// ユースケース: UC-OPS-3
func TestRun_ImportsSample(t *testing.T) {
	fx := newFixture(t, true)
	before := fileHash(t, fx.live)
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"-wxr", "testdata/sample.xml", "-uploads", fx.uploads, "-db", fx.live, "-out", fx.path("work.db"),
		"-report", fx.path("report.md"), "-redirects", fx.path("redirects.csv"), "-map", fx.path("map.csv"),
		"-new-origin", "https://new.example.com", "-old-origin", "https://old.example.com", "-old-origin", "http://www.old.example.com",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if !bytes.Equal(before, fileHash(t, fx.live)) {
		t.Fatal("the live database must not change")
	}

	db, err := sql.Open("sqlite", "file:"+fx.path("work.db")+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	q := func(query string, args ...any) string {
		t.Helper()
		var s sql.NullString
		if err := db.QueryRow(query, args...).Scan(&s); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		return s.String
	}
	// お知らせ: 公開・下書き・保護・予約の4件（非公開とプラグインの投稿は取り込まない）。全部下書き
	if got := q("SELECT COUNT(*) FROM news_posts WHERE status = 'draft'"); got != "4" {
		t.Errorf("news drafts: %s", got)
	}
	if got := q("SELECT COUNT(*) FROM news_posts WHERE status <> 'draft'"); got != "0" {
		t.Errorf("all news must be drafts: %s", got)
	}
	open := q("SELECT id FROM news_posts WHERE title = '開店のお知らせ'")
	if body := q("SELECT body FROM news_posts WHERE id = ?", open); body != "5月に新しい店を開きました。\n\n営業時間は次のとおりです。\n平日は10時から。" {
		t.Errorf("news body: %q", body)
	}
	if got := q("SELECT c.name FROM news_posts p JOIN news_categories c ON c.id = p.category_id WHERE p.id = ?", open); got != "店舗" {
		t.Errorf("category must follow the Yoast primary category: %q", got)
	}
	cover := q("SELECT cover_file FROM news_posts WHERE id = ?", open)
	if cover == "" || q("SELECT alt FROM media_items WHERE id || '.' || format = ?", cover) != "店の外観" {
		t.Errorf("cover: %q", cover)
	}
	if got := q("SELECT COUNT(*) FROM news_posts WHERE title = '書きかけの記事' AND published_at IS NULL"); got != "1" {
		t.Error("draft with 0000-00-00 must have no published_at")
	}
	if got := q("SELECT published_at FROM news_posts WHERE title = '予約の記事'"); got != "1893456000" {
		t.Errorf("future post keeps its date: %s", got)
	}
	// 固定ページ: スラッグが既存と重なるので page-10 にする。本文の内部リンクはお知らせの移行先へ書き換える
	page := q("SELECT body FROM pages WHERE slug = 'page-10'")
	for _, want := range []string{"## 私たちについて", "[開店のお知らせ](/news/" + open + ")", "[外部](https://example.org/)", "![店の外観](/media/" + cover + ")"} {
		if !strings.Contains(page, want) {
			t.Errorf("page body must contain %q:\n%s", want, page)
		}
	}
	if got := q("SELECT status FROM pages WHERE slug = 'page-10'"); got != "draft" {
		t.Errorf("page status: %s", got)
	}
	// 画像: shop と inside の2枚だけ（inside はお知らせの本文にしか出ないので取り込まない、logo は WebP で外を指す）
	if got := q("SELECT COUNT(*) FROM media_items"); got != "1" {
		t.Errorf("media count: %s", got)
	}
	if got := q("SELECT COUNT(*) FROM media_refs"); got == "0" {
		t.Error("media references must be recorded")
	}

	report := string(fileHash(t, fx.path("report.md")))
	for _, want := range []string{"2枚中1枚を移した", "パスワード保護の記事", "スラッグを page-<ID> に変えた", "表示の時に中身を作るブロック（latest-posts ×1）",
		"お知らせの本文の途中にだけ出る画像", "https://old.example.com/wp-content/uploads/2024/05/inside.png（店内）", "type:flamingo_inbound | 1", "status:post/private | 1", "| webp | 1 |", "フォルダにあって書き出しに無い画像 | 1枚"} {
		if !strings.Contains(report, want) {
			t.Errorf("report must contain %q", want)
		}
	}
	rows, err := csv.NewReader(bytes.NewReader(fileHash(t, fx.path("redirects.csv")))).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, r := range rows {
		if len(r) != 7 || r[2] != "301" {
			t.Fatalf("row: %v", r)
		}
		got[r[0]] = r[1]
	}
	for source, target := range map[string]string{
		"old.example.com/news/2024/05/open":    "https://new.example.com/news/" + open,
		"old.example.com/news/2024/05/open/":   "https://new.example.com/news/" + open,
		"www.old.example.com/company/":         "https://new.example.com/page-10",
		"old.example.com/news/2030/01/future/": "",
		// 取り込んだ画像の旧URL（添付のURLと、本文に出た小さい版）
		"old.example.com/wp-content/uploads/2024/05/shop-scaled.jpg":  "https://new.example.com/media/" + cover,
		"old.example.com/wp-content/uploads/2024/05/shop-300x200.jpg": "https://new.example.com/media/" + cover,
	} {
		if target != "" && got[source] != target {
			t.Errorf("redirect %s = %q, want %q", source, got[source], target)
		}
	}
	for source := range got {
		if strings.Contains(source, "members") || strings.Contains(source, "?p=") || strings.Contains(source, "private") || strings.Contains(source, "inside") {
			t.Errorf("protected, draft and private posts must not be redirected: %s", source)
		}
	}
	if !strings.Contains(string(fileHash(t, fx.path("map.csv"))), "news,20,"+open+",/news/"+open) {
		t.Error("map must record the WordPress ID and the new URL")
	}

	for _, name := range []string{"report.md", "redirects.csv", "map.csv"} {
		checkCanaries(t, name, string(fileHash(t, fx.path(name))))
	}
	checkCanaries(t, "stdout", stdout.String()+stderr.String())
	checkCanaries(t, "work.db", string(fileHash(t, fx.path("work.db"))))
}

func checkCanaries(t *testing.T, where, s string) {
	t.Helper()
	for _, c := range canaries {
		if strings.Contains(s, c) {
			t.Errorf("%s must not contain %s", where, c)
		}
	}
}

func TestRun_DryRunChangesNothing(t *testing.T) {
	fx := newFixture(t, true)
	before := fileHash(t, fx.live)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-wxr", "testdata/sample.xml", "-uploads", fx.uploads, "-db", fx.live, "-report", fx.path("report.md"), "-dry-run"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if !bytes.Equal(before, fileHash(t, fx.live)) {
		t.Fatal("dry run must not change the live database")
	}
	entries, _ := os.ReadDir(fx.dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".db") && e.Name() != "app.db" || strings.HasSuffix(e.Name(), ".csv") {
			t.Errorf("dry run must not leave %s", e.Name())
		}
	}
	report := string(fileHash(t, fx.path("report.md")))
	if !strings.Contains(report, "試しの実行です") || !strings.Contains(report, "2枚中1枚を移した") {
		t.Errorf("dry-run report:\n%s", report)
	}
	checkCanaries(t, "report.md", report)
}

func TestRun_RefusesToOverwriteAndChecksFlags(t *testing.T) {
	fx := newFixture(t, false)
	if err := os.WriteFile(fx.path("map.csv"), []byte("done"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"-wxr", "testdata/sample.xml", "-db", fx.live, "-out", fx.path("work.db"), "-report", fx.path("r.md"), "-map", fx.path("map.csv")},
		{"-wxr", "testdata/sample.xml", "-db", fx.live, "-report", fx.path("r.md")},
		{"-wxr", "testdata/sample.xml", "-db", fx.live, "-report", fx.path("r.md"), "-dry-run", "-redirects", fx.path("x.csv")},
		{"-wxr", "testdata/sample.xml", "-db", fx.live, "-report", fx.path("r.md"), "-dry-run", "-out", fx.path("work.db")},
		{"-wxr", "testdata/sample.xml", "-db", fx.live, "-report", fx.path("r.md"), "-dry-run", "-new-origin", "http://new.example.com"},
	} {
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr); code != 2 {
			t.Errorf("%v: exit %d", args, code)
		}
	}
	if _, err := os.Stat(fx.path("work.db")); err == nil {
		t.Error("refused runs must not create the work database")
	}
}

// TestRun_FailuresDoNotLeak は、壊れた WXR や読めない入力の失敗で、作業DBを残さず、中身を出さないことを確かめる。
func TestRun_FailuresDoNotLeak(t *testing.T) {
	fx := newFixture(t, true)
	broken := fx.path("broken.xml")
	if err := os.WriteFile(broken, []byte("<rss><channel><item><title>CANARY_BROKEN</title><wp:comment>canary-comment@example.invalid"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := run([]string{"-wxr", broken, "-uploads", fx.uploads, "-db", fx.live, "-out", fx.path("work.db"), "-report", fx.path("r.md"), "-map", fx.path("m.csv")}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit %d", code)
	}
	out := stdout.String() + stderr.String()
	if strings.Contains(out, "CANARY") || strings.Contains(out, "canary") {
		t.Errorf("errors must not echo content: %s", out)
	}
	for _, name := range []string{"work.db", "r.md", "m.csv"} {
		if _, err := os.Stat(fx.path(name)); err == nil {
			t.Errorf("%s must not be left", name)
		}
	}
	// DBが開けない
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"-wxr", "testdata/sample.xml", "-db", fx.path("missing.db"), "-report", fx.path("r.md"), "-dry-run"}, &stdout, &stderr); code != 1 {
		t.Fatalf("missing db: exit %d", code)
	}
	checkCanaries(t, "stderr", stderr.String())
}

// TestRun_BrokenImage は、画像のファイルが壊れていても止まらず、移せなかった画像と、代表画像を移せなかった記事を報告することを確かめる。
func TestRun_BrokenImage(t *testing.T) {
	fx := newFixture(t, true)
	if err := os.WriteFile(filepath.Join(fx.uploads, "2024", "05", "shop-scaled.jpg"), []byte("CANARY_NOT_AN_IMAGE"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-wxr", "testdata/sample.xml", "-uploads", fx.uploads, "-db", fx.live, "-report", fx.path("r.md"), "-dry-run"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	report := string(fileHash(t, fx.path("r.md")))
	for _, want := range []string{"2枚中0枚を移した", "中身がJPEG・PNGでない | 1", "代表画像を移せなかった"} {
		if !strings.Contains(report, want) {
			t.Errorf("report must contain %q", want)
		}
	}
	checkCanaries(t, "report", report+stdout.String()+stderr.String())
}

// TestRun_WithoutMedia は、画像機能の無いDBでも記事を取り込み、画像の表を作らないことを確かめる。
func TestRun_WithoutMedia(t *testing.T) {
	fx := newFixture(t, false)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-wxr", "testdata/sample.xml", "-uploads", fx.uploads, "-db", fx.live, "-out", fx.path("work.db"), "-report", fx.path("r.md"), "-map", fx.path("m.csv")}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	db, _ := sql.Open("sqlite", "file:"+fx.path("work.db")+"?mode=ro")
	defer db.Close()
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE name = 'media_items'").Scan(&n); err != nil || n != 0 {
		t.Fatalf("media table must not be created: %d %v", n, err)
	}
	if !strings.Contains(string(fileHash(t, fx.path("r.md"))), "画像機能が無効") {
		t.Error("report must say media is disabled")
	}
}

func TestSafePath(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "up", "a"), 0o700); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(root, "up", "a", "x.jpg"), []byte("x"), 0o600)
	_ = os.WriteFile(filepath.Join(root, "secret"), []byte("x"), 0o600)
	_ = os.Symlink(filepath.Join(root, "secret"), filepath.Join(root, "up", "link.jpg"))
	up := filepath.Join(root, "up")
	for rel, ok := range map[string]bool{"a/x.jpg": true, "../secret": false, "/etc/passwd": false, "link.jpg": false, "a/../../secret": false, "missing.jpg": false, "": false} {
		if _, got := safePath(up, rel); got != ok {
			t.Errorf("safePath(%q) = %v, want %v", rel, got, ok)
		}
	}
}
