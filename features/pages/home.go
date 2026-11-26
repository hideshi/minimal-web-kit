package pages

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"slices"

	"minimal-web-kit/features"
	"minimal-web-kit/internal/web"
	"minimal-web-kit/internal/web/richtext"
)

// ---- トップページ（docs/domain/pages.md の PAGES-INV-15〜17） ----

// HomeSlug は、トップページに使うページのスラッグ（環境変数 HOME_PAGE_SLUG）です。空なら指定は無い。
func (f *Feature) HomeSlug() string { return f.homeSlug }

func (f *Feature) isHome(slug string) bool { return f.homeSlug != "" && slug == f.homeSlug }

// ServeHomePage は、トップページのページが公開中なら / に描いて真を返します。無い・下書き・予約中と、描けない失敗のときは、
// 何も書かずに偽を返し、Coreが今のトップを返します（500にしない）。失敗はページのIDと固定の分類だけを記録します。
// Cookieは照合も発行もしない（Coreが / をセッションに通さない。PAGES-INV-16）。
func (f *Feature) ServeHomePage(w http.ResponseWriter, r *http.Request) bool {
	if f.homeSlug == "" || f.store == nil {
		return false
	}
	p, err := f.store.Published(r.Context(), f.homeSlug)
	if errors.Is(err, ErrNotFound) {
		return false
	} else if err != nil {
		f.logger.Error("pages home load failed", web.RequestIDAttr(w))
		return false
	}
	buf, err := f.render(r.Context(), w, p, "/")
	if err != nil {
		f.logger.Error("pages home render failed", slog.Int64("page_id", p.ID), slog.String("stage", err.Error()), web.RequestIDAttr(w))
		return false
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = buf.WriteTo(w)
	return true
}

// homePath は、ページの公開のパスです。トップページなら / です。
func (f *Feature) homePath(slug string) string {
	if f.isHome(slug) {
		return "/"
	}
	return "/" + slug
}

// ---- お知らせの一覧とお問い合わせのボタン（PAGES-INV-18・PAGES-INV-20） ----

// SetNewsList は、公開の記事の新着を読む口を受け取ります（お知らせの機能があるときだけ）。
func (f *Feature) SetNewsList(list func(ctx context.Context, category string, n int) (features.NewsList, error)) {
	f.newsList = list
}

// resolveParts は、本文のお知らせの一覧とお問い合わせのボタンに中身を入れ、書く人への警告を返します（公開ページには出さない）。
// 公開ページでお知らせを読めなければ、そのブロックだけを省きます（ページは描く）。
func (f *Feature) resolveParts(ctx context.Context, doc *richtext.Doc, pageID int64, preview bool) []string {
	var warnings []string
	contact := f.ContactTarget()
	for i := range doc.Blocks {
		b := &doc.Blocks[i]
		switch b.Kind {
		case "news":
			if f.newsList == nil {
				warnings = append(warnings, "お知らせの機能が無いため、「お知らせ: 」の行は表示されません。")
				continue
			}
			list, err := f.newsBlock(ctx, b)
			if err != nil {
				if !preview {
					f.logger.Error("pages news list failed", slog.Int64("page_id", pageID))
				}
				warnings = append(warnings, "お知らせを読めなかったため、「お知らせ: 」の行は表示されません。")
				continue
			}
			if !list.Found {
				warnings = append(warnings, "カテゴリ「"+b.NewsCategory+"」がありません。名前を確かめてください（公開ページでは「お知らせはまだありません。」と表示されます）。")
			}
		case "contact":
			if contact == nil {
				warnings = append(warnings, "お問い合わせの行き先が設定されていないため、「お問い合わせ: 」の行は表示されません。")
				continue
			}
			b.ContactHref, b.ContactExternal = contact.Href, contact.External
		}
	}
	for _, name := range doc.DuplicateAnchors {
		warnings = append(warnings, "見出しの飛び先「{#"+name+"}」が2つ以上あります。2つ目からは飛び先になりません。")
	}
	return slices.Compact(warnings)
}

// newsBlock は、お知らせの一覧に記事を入れます。公開の記事の無いカテゴリと無い名前のカテゴリは、同じ表示（0件）になります。
func (f *Feature) newsBlock(ctx context.Context, b *richtext.Block) (features.NewsList, error) {
	list, err := f.newsList(ctx, b.NewsCategory, b.NewsCount)
	if err != nil {
		return list, err
	}
	b.NewsReady, b.NewsItems, b.NewsList = true, list.Items, ""
	if len(list.Items) > 0 {
		b.NewsList = list.ListPath
	}
	return list, nil
}

// mcpNotes は、MCPの確認の文に足す、トップページであることと本文の部品の警告です。
func (f *Feature) mcpNotes(ctx context.Context, slug, body string) string {
	s := ""
	if f.isHome(slug) {
		s += "【トップページとして / に出ます（HOME_PAGE_SLUG）】"
	}
	for _, w := range f.bodyWarnings(ctx, body) {
		s += "【注意: " + w + "】"
	}
	return s
}

// bodyWarnings は、保存済みか入力中の本文の、書く人への警告です（編集の画面とMCPの確認の文）。
func (f *Feature) bodyWarnings(ctx context.Context, body string) []string {
	doc, err := richtext.ParseForPages(body)
	if err != nil {
		return nil
	}
	return f.resolveParts(ctx, &doc, 0, true)
}
