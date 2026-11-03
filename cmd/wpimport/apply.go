package main

import (
	"context"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"minimal-web-kit/cmd/wpimport/convert"
	"minimal-web-kit/features/media"
	"minimal-web-kit/features/news"
	"minimal-web-kit/features/pages"
	"minimal-web-kit/internal/web/form"
)

// 2段目: 画像 → カテゴリ → お知らせ（本文なし）→ 全部の本文を変換して保存、の順に取り込みます（計画10.1・10.8）。
// お知らせのURLは保存して初めて決まるため、本文の内部リンクは全部の移行先が決まってから書き換えます。

type kit struct {
	news  *news.Feature
	pages *pages.Feature
	media *media.Feature
}

// maxReadBytes は、画像のファイルを読む上限です（画像機能の上限より1バイト多く読み、超えたことを分かるようにする）。
const maxReadBytes = 8_000_001

func (p *planner) importImages(ctx context.Context, k kit, uploads string) {
	ids := make([]int64, 0, len(p.attachments))
	for id := range p.attachments {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		a := p.attachments[id]
		switch {
		case !a.wanted():
			a.failure = "news_body_only"
			continue
		case k.media == nil:
			a.failure = "media_disabled"
			continue
		case uploads == "":
			a.failure = "no_uploads_dir"
			continue
		}
		full, ok := safePath(uploads, a.rel)
		if !ok {
			a.failure = "file_missing_or_outside"
			continue
		}
		data, err := readLimited(full)
		if err != nil {
			a.failure = "file_unreadable"
			continue
		}
		file, err := k.media.Import(ctx, data, a.alt)
		switch {
		case errors.Is(err, media.ErrImportSize):
			a.failure = "too_large"
		case errors.Is(err, media.ErrImportFormat):
			a.failure = "not_jpeg_png"
		case errors.Is(err, media.ErrImportPixels):
			a.failure = "too_many_pixels"
		case errors.Is(err, media.ErrFull):
			a.failure = "total_limit"
		case errors.Is(err, media.ErrImportAlt):
			a.failure = "bad_alt"
		case err != nil:
			a.failure = "save_failed"
		default:
			a.file = file
		}
	}
}

// countUnlisted は、アップロードのフォルダにあってWXRの添付に無いJPEG・PNGの数を数えます（計画10.6。件数だけ報告する）。
// WordPressが作る小さい版（-<幅>x<高さ>）は数えません。
func (p *planner) countUnlisted(uploads string) {
	if uploads == "" {
		return
	}
	known := map[string]bool{}
	for _, a := range p.attachments {
		known[filepath.ToSlash(filepath.Clean(a.rel))] = true
	}
	_ = filepath.WalkDir(uploads, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(uploads, path)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(rel)), ".")
		if imageExt[ext] && !known[rel] && !reSized.MatchString(rel) {
			p.unlisted++
		}
		return nil
	})
}

func readLimited(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, maxReadBytes))
}

// importCategories は、お知らせのカテゴリを作り、名前からIDを引く表を返します。同じ名前の既存のカテゴリはそれを使います。
func (p *planner) importCategories(ctx context.Context, posts *news.PostStore) (map[string]int64, error) {
	ids := map[string]int64{}
	existing, err := posts.Categories(ctx)
	if err != nil {
		return nil, err
	}
	for _, c := range existing {
		ids[c.Name] = c.ID
	}
	for _, e := range p.entries {
		if e.category == "" || ids[e.category] != 0 {
			continue
		}
		id, err := posts.SaveCategory(ctx, 0, e.category)
		if err != nil {
			var invalid news.InputError
			if errors.As(err, &invalid) {
				e.notes["category_invalid"]++
				e.category = ""
				continue
			}
			return nil, err
		}
		ids[e.category] = id
	}
	return ids, nil
}

// createNews は、お知らせを本文なしの下書きで作り、移行先のIDとURLを決めます。
func (p *planner) createNews(ctx context.Context, posts *news.PostStore, categories map[string]int64) error {
	for _, e := range p.entries {
		if e.target != convert.News {
			continue
		}
		if e.cover != 0 && p.coverFile(e) == "" {
			e.notes["cover_not_imported"]++ // 代表画像に決めた画像を移せなかった（理由は「移せなかった画像」）
		}
		id, err := posts.CreateWithCover(ctx, e.title, "", e.publishedAt, p.coverFile(e), categories[e.category])
		if err != nil {
			var invalid news.InputError
			if errors.As(err, &invalid) {
				e.failure = "invalid_" + invalid.Field
				continue
			}
			return err
		}
		e.newID, e.newPath = id, "/news/"+strconv.FormatInt(id, 10)
	}
	return nil
}

func (p *planner) coverFile(e *entry) string {
	if a := p.attachments[e.cover]; a != nil {
		return a.file
	}
	return ""
}

// saveBodies は、全部の移行先が決まった後で本文を変換し、お知らせを更新して固定ページを作ります。
func (p *planner) saveBodies(ctx context.Context, k kit, categories map[string]int64) error {
	res := resolver{p}
	for _, e := range p.entries {
		if e.failure != "" {
			continue
		}
		out := convert.Convert(e.item.Content, e.item.ID, e.target, res)
		for key, n := range out.Notes {
			e.notes[key] += n
		}
		if e.target == convert.News {
			body := out.Body
			err := k.news.Posts().UpdateWithCover(ctx, e.newID, e.title, body, e.publishedAt, p.coverFile(e), categories[e.category])
			var invalid news.InputError
			if errors.As(err, &invalid) && invalid.Field == "body" {
				e.notes["body_not_imported"]++
				err = k.news.Posts().UpdateWithCover(ctx, e.newID, e.title, "", e.publishedAt, p.coverFile(e), categories[e.category])
			}
			if err != nil {
				return err
			}
			e.imported = true
			continue
		}
		in := pages.Input{Slug: e.slug, Title: e.title, Body: out.Body}
		if !e.publishedAt.IsZero() {
			in.PublishAt = form.FormatLocalDateTime(e.publishedAt)
		}
		id, err := k.pages.Import(ctx, in)
		var invalid pages.InputError
		if errors.As(err, &invalid) && (invalid.Field == "body" || invalid.Field == "body_limit" || invalid.Field == "body_image") {
			e.notes["body_not_imported"]++
			in.Body = ""
			id, err = k.pages.Import(ctx, in)
		}
		if errors.As(err, &invalid) {
			e.failure = "invalid_" + invalid.Field
			continue
		}
		if err != nil {
			return err
		}
		e.newID, e.imported = id, true
	}
	return nil
}

// resolver は、2段目の対応です。画像は取り込んだファイルに、古いサイトの中のリンクは移行先に結びます。
type resolver struct{ p *planner }

func (r resolver) Image(img convert.Image) (string, string, bool) {
	a := r.p.attachments[r.p.attachmentFor(img)]
	if a == nil || a.file == "" {
		return "", "", false
	}
	if a.placehold {
		return a.file, "", true // 仮の説明は返さない（本文の alt・キャプションがあればそれを使う）
	}
	return a.file, a.alt, true
}

func (r resolver) Attachments(id int64) []int64     { return r.p.children[id] }
func (r resolver) Pattern(ref int64) (string, bool) { s, ok := r.p.patterns[ref]; return s, ok }

// Link は、古いサイトの中へのリンクを移行先のパスにします（計画10.8）。?p=・?page_id= は記事の番号で引き、
// パスは記事のパーマリンクで、画像のURLは取り込んだ画像で引きます。フラグメントは残します。
func (r resolver) Link(href string) (string, bool, bool) {
	u, err := url.Parse(strings.TrimSpace(href))
	if err != nil {
		return href, false, false
	}
	internal := (u.Host == "" && strings.HasPrefix(u.Path, "/")) || (u.Host != "" && r.p.oldHosts[hostKey(u.Hostname())])
	if !internal {
		return href, false, false
	}
	frag := ""
	if u.Fragment != "" {
		frag = "#" + u.EscapedFragment()
	}
	q := u.Query()
	for _, key := range []string{"p", "page_id"} {
		if id, err := strconv.ParseInt(q.Get(key), 10, 64); err == nil {
			if e := r.p.byWPID[id]; e != nil && e.newPath != "" && e.failure == "" {
				return e.newPath + frag, true, true
			}
		}
	}
	if e := r.p.byLink[pathKey(u.Path)]; e != nil && e.newPath != "" && e.failure == "" {
		return e.newPath + frag, true, true
	}
	if a := r.p.attachments[r.p.attachmentFor(convert.Image{Src: href})]; a != nil && a.file != "" {
		return "/media/" + a.file, true, true
	}
	if u.Host == "" {
		return href, false, false // サイト内のパスは、古いサイトのリンクか分からないため、そのまま残す
	}
	return href, true, false
}
