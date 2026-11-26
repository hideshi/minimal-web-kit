package news

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"

	"minimal-web-kit/features"
	"minimal-web-kit/internal/web/richtext"
)

// LatestPublished は、固定ページの本文のお知らせの一覧に、公開の記事の新着を n 件返します（docs/domain/news.md 6節）。
// category は名前の完全一致で解決し、無ければ Found を偽にします。記事は公開の一覧と同じ条件と並びで選びます（NEWS-INV-16）。
// 記事が0件なら ListPath を空にし、公開の記事の無いカテゴリの番号を公開ページに出させません（PAGES-INV-18）。
func (f *Feature) LatestPublished(ctx context.Context, category string, n int) (features.NewsList, error) {
	return f.posts.LatestPublished(ctx, category, n)
}

// LatestPublished は、カテゴリの解決と記事の読み取りを、1つの読み取りのトランザクションで行います。
func (s *PostStore) LatestPublished(ctx context.Context, category string, n int) (features.NewsList, error) {
	if n < 1 || n > richtext.MaxNews {
		return features.NewsList{}, errors.New("news: latest count out of range")
	}
	// 読むだけの遅延のトランザクション（SQLiteは最初の読み取りから終わりまで同じ版を見る）
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return features.NewsList{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var categoryID int64
	if category != "" {
		err := tx.QueryRowContext(ctx, "SELECT id FROM news_categories WHERE name = ?", category).Scan(&categoryID)
		if errors.Is(err, sql.ErrNoRows) {
			return features.NewsList{}, nil
		} else if err != nil {
			return features.NewsList{}, err
		}
	}
	rows, err := tx.QueryContext(ctx,
		"SELECT id, title, published_at FROM news_posts WHERE status = ? AND published_at <= ? AND (? = 0 OR category_id = ?) ORDER BY published_at DESC, id DESC LIMIT ?",
		statusPublished, s.now().Unix(), categoryID, categoryID, n)
	if err != nil {
		return features.NewsList{}, err
	}
	defer rows.Close()
	out := features.NewsList{Found: true}
	for rows.Next() {
		var id, published int64
		var title string
		if err := rows.Scan(&id, &title, &published); err != nil {
			return features.NewsList{}, err
		}
		out.Items = append(out.Items, richtext.NewsItem{Title: title, Href: "/news/" + strconv.FormatInt(id, 10), Date: time.Unix(published, 0).UTC()})
	}
	if err := rows.Err(); err != nil {
		return features.NewsList{}, err
	}
	if len(out.Items) > 0 {
		out.ListPath = "/news"
		if categoryID != 0 {
			out.ListPath += "?category=" + strconv.FormatInt(categoryID, 10)
		}
	}
	return out, nil
}
