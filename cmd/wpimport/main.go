// Command wpimport は、WordPressの書き出し（WXR）とアップロードのフォルダから、キットのお知らせ・固定ページ・画像へ移行します。
// サーバーとは別の実行ファイルで、外部へ通信しません。稼働中のDBは読むだけで、別の作業DBに取り込みます。
// 手順は docs/deploy.md の「WordPressからの移行」、設計は docs/plans/wordpress-import-plan.md。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"minimal-web-kit/cmd/wpimport/wxr"
	"minimal-web-kit/features"
	"minimal-web-kit/features/media"
	"minimal-web-kit/features/news"
	"minimal-web-kit/features/pages"
	"minimal-web-kit/internal/store"
)

type options struct {
	wxr, uploads, db, out, report, redirects, mapFile, newOrigin string
	oldOrigins, uploadsURLs                                      []string
	dryRun                                                       bool
}

type multi []string

func (m *multi) String() string     { return strings.Join(*m, ",") }
func (m *multi) Set(v string) error { *m = append(*m, v); return nil }

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	var o options
	var olds, uploadsURLs multi
	fs := flag.NewFlagSet("wpimport", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&o.wxr, "wxr", "", "WordPressの書き出し（WXR）のファイル（必須）")
	fs.StringVar(&o.uploads, "uploads", "", "手元に取ったアップロードのフォルダ（wp-content/uploads）。無ければ画像を移さない")
	fs.StringVar(&o.db, "db", "", "稼働中のDB（読むだけ。サーバーを止めてから指定する。必須）")
	fs.StringVar(&o.out, "out", "", "取り込み先の作業DB（まだ無いパス。-dry-run では使わない）")
	fs.StringVar(&o.report, "report", "", "報告のMarkdown（まだ無いパス。必須）")
	fs.StringVar(&o.redirects, "redirects", "", "Cloudflareの一括転送のCSV（まだ無いパス。任意）")
	fs.StringVar(&o.mapFile, "map", "", "対応表のCSV（まだ無いパス。-dry-run では使わない。二重の取り込みを防ぐ印にもなる）")
	fs.StringVar(&o.newOrigin, "new-origin", "", "移行先のオリジン（例: https://example.com。-redirects に必須）")
	fs.Var(&olds, "old-origin", "古いサイトのオリジン（複数可。既定は書き出しの base_blog_url）")
	fs.Var(&uploadsURLs, "uploads-url", "アップロードのURLの接頭辞（複数可。既定は古いオリジンごとに /wp-content/uploads/）")
	fs.BoolVar(&o.dryRun, "dry-run", false, "試しの実行（一時の作業DBに取り込んで報告を書き、作業DBを消す）")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	o.oldOrigins, o.uploadsURLs = olds, uploadsURLs
	if err := o.check(); err != nil {
		fmt.Fprintln(stderr, "wpimport:", err)
		return 2
	}
	summary, err := execute(context.Background(), o)
	if err != nil {
		fmt.Fprintln(stderr, "wpimport:", err)
		return 1
	}
	fmt.Fprintln(stdout, summary)
	return 0
}

func (o options) check() error {
	switch {
	case o.wxr == "" || o.db == "" || o.report == "":
		return errors.New("-wxr, -db, -report are required")
	case !o.dryRun && (o.out == "" || o.mapFile == ""):
		return errors.New("-out and -map are required (or use -dry-run)")
	case o.dryRun && (o.out != "" || o.mapFile != "" || o.redirects != ""):
		return errors.New("-dry-run writes only the report; do not pass -out, -map or -redirects")
	case o.redirects != "" && o.newOrigin == "":
		return errors.New("-redirects needs -new-origin")
	}
	if o.newOrigin != "" {
		if u, err := url.Parse(o.newOrigin); err != nil || u.Scheme != "https" || u.Host == "" || (u.Path != "" && u.Path != "/") {
			return errors.New("-new-origin must be like https://example.com")
		}
	}
	for _, p := range []string{o.out, o.report, o.redirects, o.mapFile} {
		if p == "" {
			continue
		}
		if _, err := os.Stat(p); err == nil {
			// 既にある出力は上書きしない。対応表があれば、取り込みは済んでいる（二重の取り込みを防ぐ。計画10.1）
			return fmt.Errorf("%s already exists; remove it only if you are sure the import has not been done", filepath.Base(p))
		}
	}
	return nil
}

// execute は、作業DBを作り、2段で取り込み、出力を書きます。失敗したら作業DBを消します。
func execute(ctx context.Context, o options) (string, error) {
	f, err := os.Open(o.wxr)
	if err != nil {
		return "", errors.New("cannot open the WXR file")
	}
	exp, err := wxr.Read(f)
	f.Close()
	if err != nil {
		return "", err
	}
	if len(o.oldOrigins) == 0 && exp.BaseBlogURL != "" {
		o.oldOrigins = []string{exp.BaseBlogURL}
	}
	if len(o.uploadsURLs) == 0 {
		for _, origin := range o.oldOrigins {
			o.uploadsURLs = append(o.uploadsURLs, strings.TrimSuffix(origin, "/")+"/wp-content/uploads/")
		}
	}

	work := o.out
	if o.dryRun {
		dir, err := os.MkdirTemp("", "wpimport-dry-run-")
		if err != nil {
			return "", err
		}
		defer os.RemoveAll(dir)
		work = filepath.Join(dir, "work.db")
	}
	if err := copyDB(ctx, o.db, work); err != nil {
		return "", err
	}
	p, err := importInto(ctx, work, exp, o)
	if err != nil || o.dryRun {
		removeDB(work)
	}
	if err != nil {
		return "", err
	}
	if err := writeOutputs(p, o); err != nil {
		if !o.dryRun {
			removeDB(work)
		}
		return "", err
	}
	imported := 0
	for _, e := range p.entries {
		if e.imported {
			imported++
		}
	}
	if o.dryRun {
		return fmt.Sprintf("dry run: %d entries would be imported; see the report", imported), nil
	}
	return fmt.Sprintf("imported %d entries as drafts into %s; see the report", imported, filepath.Base(work)), nil
}

// copyDB は、稼働中のDBを読むだけで開き、整合した複製を作業DBとして作ります（稼働中のDBには書かない）。
func copyDB(ctx context.Context, live, work string) error {
	abs, err := filepath.Abs(live)
	if err != nil {
		return err
	}
	if _, err := os.Stat(abs); err != nil {
		return errors.New("cannot open the database")
	}
	dsn := url.URL{Scheme: "file", Path: abs, RawQuery: "mode=ro"}
	db, err := sqlOpen(dsn.String())
	if err != nil {
		return errors.New("cannot open the database")
	}
	defer db.Close()
	if err := store.Backup(ctx, db, work); err != nil {
		return errors.New("cannot copy the database (is the server stopped?)")
	}
	return nil
}

func removeDB(path string) {
	for _, s := range []string{"", "-wal", "-shm"} {
		_ = os.Remove(path + s)
	}
}

// importInto は、作業DBを開いて機能を初期化し、2段で取り込みます。
func importInto(ctx context.Context, work string, exp *wxr.Export, o options) (*planner, error) {
	en, err := enabledFeatures(ctx, work)
	if err != nil {
		return nil, err
	}
	if !en.news && !en.pages {
		return nil, errors.New("neither news nor pages is enabled in this database; run make add-news or make add-pages first")
	}
	s, err := store.NewSQLiteStore(work)
	if err != nil {
		return nil, errors.New("cannot open the work database")
	}
	defer s.Close()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil)) // 機能のログは出さない（本文や題名を出さないため）
	var k kit
	var owners []features.MediaOwner
	if en.media {
		k.media = media.NewFeature(logger)
		if err := k.media.Init(s.DB()); err != nil {
			return nil, errors.New("cannot initialize media")
		}
	}
	existing := map[string]bool{}
	checkSlug := func(string) error { return nil }
	if en.news {
		k.news = news.NewFeature(logger)
		if err := k.news.Init(s.DB()); err != nil {
			return nil, errors.New("cannot initialize news")
		}
		owners = append(owners, k.news)
	}
	if en.pages {
		k.pages = pages.NewFeature(logger)
		if err := k.pages.Init(s.DB()); err != nil {
			return nil, errors.New("cannot initialize pages")
		}
		owners = append(owners, k.pages)
		if existing, err = k.pages.Slugs(ctx); err != nil {
			return nil, err
		}
		checkSlug = k.pages.CheckSlug
	}
	if k.media != nil {
		// サーバーと同じ配線: 各機能は画像の口で参照を記録し、画像機能は各機能に公開の判定を問い合わせる
		if k.news != nil {
			k.news.SetMedia(k.media.Media())
		}
		if k.pages != nil {
			k.pages.SetMedia(k.media.Media())
		}
		k.media.SetMediaOwners(owners)
	}

	p := newPlanner(exp, o.oldOrigins, o.uploadsURLs, en, existing, checkSlug)
	p.run()
	p.importImages(ctx, k, o.uploads)
	p.countUnlisted(o.uploads)
	categories := map[string]int64{}
	if k.news != nil {
		if categories, err = p.importCategories(ctx, k.news.Posts()); err != nil {
			return nil, err
		}
		if err := p.createNews(ctx, k.news.Posts(), categories); err != nil {
			return nil, err
		}
	}
	if err := p.saveBodies(ctx, k, categories); err != nil {
		return nil, err
	}
	if k.media != nil {
		if err := k.media.Startup(ctx); err != nil { // 参照を作り直す（MEDIA-INV-7）
			return nil, err
		}
	}
	var check string
	if err := s.DB().QueryRowContext(ctx, "PRAGMA integrity_check(1)").Scan(&check); err != nil || check != "ok" {
		return nil, errors.New("integrity check of the work database failed")
	}
	return p, nil
}

// enabledFeatures は、作業DBにお知らせ・固定ページ・画像の表があるかを返します。無い機能の表は作りません
// （サーバーが知らない表をDBに足さないため）。
func enabledFeatures(ctx context.Context, path string) (enabled, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return enabled{}, err
	}
	dsn := url.URL{Scheme: "file", Path: abs, RawQuery: "mode=ro"}
	db, err := sqlOpen(dsn.String())
	if err != nil {
		return enabled{}, err
	}
	defer db.Close()
	has := func(table string) (bool, error) {
		var n int
		err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?", table).Scan(&n)
		return n > 0, err
	}
	var en enabled
	if en.news, err = has("news_posts"); err != nil {
		return en, err
	}
	if en.pages, err = has("pages"); err != nil {
		return en, err
	}
	en.media, err = has("media_items")
	return en, err
}

// writeOutputs は、報告・転送のCSV・対応表を書きます。どれも、まだ無いファイルとして作ります。
func writeOutputs(p *planner, o options) error {
	files := []struct {
		path  string
		write func(io.Writer) error
	}{
		{o.report, func(w io.Writer) error { p.writeReport(w, o.dryRun, o.newOrigin); return nil }},
	}
	if !o.dryRun && o.redirects != "" {
		files = append(files, struct {
			path  string
			write func(io.Writer) error
		}{o.redirects, func(w io.Writer) error { return writeCSV(w, p.redirectRows(o.oldOrigins, o.newOrigin)) }})
	}
	if !o.dryRun {
		files = append(files, struct {
			path  string
			write func(io.Writer) error
		}{o.mapFile, func(w io.Writer) error { return writeCSV(w, p.mapRows()) }})
	}
	for _, f := range files {
		out, err := os.OpenFile(f.path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return fmt.Errorf("cannot create %s", filepath.Base(f.path))
		}
		werr := f.write(out)
		cerr := out.Close()
		if werr != nil || cerr != nil {
			return fmt.Errorf("cannot write %s", filepath.Base(f.path))
		}
	}
	return nil
}
