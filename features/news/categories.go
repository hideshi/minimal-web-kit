package news

import (
	"context"
	"database/sql"
	"errors"

	"minimal-web-kit/internal/store"
	"minimal-web-kit/internal/web/form"
)

// maxCategoryRunes は、カテゴリの名前の文字数の上限です（カタログの分類と同じ）。
const maxCategoryRunes = 50

// Category はお知らせのカテゴリ1つです。Posts は、そのカテゴリの記事の数（管理画面は全部、公開の候補は公開中だけ）です。
type Category struct {
	ID    int64
	Name  string
	Posts int
}

// Categories は、全部のカテゴリを名前順に、記事の数と一緒に返します（管理画面用）。
func (s *PostStore) Categories(ctx context.Context) ([]Category, error) {
	return s.categories(ctx,
		`SELECT c.id, c.name, COUNT(p.id) FROM news_categories c LEFT JOIN news_posts p ON p.category_id = c.id
		 GROUP BY c.id ORDER BY c.name, c.id`)
}

// PublicCategories は、公開中の記事を持つカテゴリだけを名前順に返します。公開の判定は公開ページと同じで、
// 予約公開で公開日時前の記事だけのカテゴリは含めません（NEWS-INV-16）。
func (s *PostStore) PublicCategories(ctx context.Context) ([]Category, error) {
	return s.categories(ctx,
		`SELECT c.id, c.name, COUNT(p.id) FROM news_categories c JOIN news_posts p ON p.category_id = c.id
		 WHERE p.status = ? AND p.published_at <= ? GROUP BY c.id ORDER BY c.name, c.id`,
		statusPublished, s.now().Unix())
}

func (s *PostStore) categories(ctx context.Context, query string, args ...any) ([]Category, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Category
	for rows.Next() {
		var c Category
		if err := rows.Scan(&c.ID, &c.Name, &c.Posts); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Category は、カテゴリ1つを、記事の数と一緒に返します。
func (s *PostStore) Category(ctx context.Context, id int64) (Category, error) {
	var c Category
	err := s.db.QueryRowContext(ctx,
		"SELECT c.id, c.name, (SELECT COUNT(*) FROM news_posts p WHERE p.category_id = c.id) FROM news_categories c WHERE c.id = ?", id).
		Scan(&c.ID, &c.Name, &c.Posts)
	if errors.Is(err, sql.ErrNoRows) {
		return Category{}, ErrNotFound
	}
	return c, err
}

// SaveCategory は、id が0ならカテゴリを作り、そうでなければ名前を変えます。名前は1行の1〜50文字で、
// 同じ名前のカテゴリは作れません（NEWS-INV-14）。記事の更新日時は変えません。
func (s *PostStore) SaveCategory(ctx context.Context, id int64, name string) (int64, error) {
	name, err := form.Line(name, maxCategoryRunes, true)
	if err != nil {
		return 0, InputError{"name"}
	}
	err = store.Atomic(ctx, s.db, func(c *sql.Conn) error {
		var existing int64
		err := c.QueryRowContext(ctx, "SELECT id FROM news_categories WHERE name = ?", name).Scan(&existing)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if existing != 0 && existing != id {
			return InputError{"name_taken"}
		}
		if id == 0 {
			res, err := c.ExecContext(ctx, "INSERT INTO news_categories (name, created_at) VALUES (?, ?)", name, s.now().Unix())
			if err != nil {
				return err
			}
			id, err = res.LastInsertId()
			return err
		}
		res, err := c.ExecContext(ctx, "UPDATE news_categories SET name = ? WHERE id = ?", name, id)
		return store.AffectedOne(res, err, ErrNotFound)
	})
	if err != nil {
		return 0, err
	}
	return id, nil
}

// DeleteCategory はカテゴリを消します。記事は消さず、カテゴリを無しに戻します（NEWS-INV-15）。記事の更新日時は変えません。
// 外部キーの ON DELETE SET NULL に加え、同じトランザクションで明示的に外します（外部キーが無効な接続でも残さない）。
func (s *PostStore) DeleteCategory(ctx context.Context, id int64) error {
	return store.Atomic(ctx, s.db, func(c *sql.Conn) error {
		if _, err := c.ExecContext(ctx, "UPDATE news_posts SET category_id = NULL WHERE category_id = ?", id); err != nil {
			return err
		}
		res, err := c.ExecContext(ctx, "DELETE FROM news_categories WHERE id = ?", id)
		return store.AffectedOne(res, err, ErrNotFound)
	})
}
