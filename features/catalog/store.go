package catalog

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"minimal-web-kit/internal/web/form"
)

var (
	ErrNotFound     = errors.New("catalog: not found")
	ErrInvalidInput = errors.New("catalog: invalid input")
)

type Item struct {
	ID                   int64
	Title, Body          string
	Published            bool
	CategoryID           int64
	Category             string
	Tags                 []Classification
	CreatedAt, UpdatedAt time.Time
}

func (Item) LogValue() slog.Value { return slog.StringValue("[catalog item]") }

type Classification struct {
	ID   int64
	Name string
}

func (Classification) LogValue() slog.Value { return slog.StringValue("[catalog classification]") }

type Input struct {
	Title, Body string
	CategoryID  int64
	TagIDs      []int64
	NewTags     []string
}

func (Input) LogValue() slog.Value { return slog.StringValue("[catalog input]") }

type Filter struct {
	Query      string
	CategoryID int64
	TagIDs     []int64
}

func (Filter) LogValue() slog.Value { return slog.StringValue("[catalog filter]") }

type Store struct {
	db  *sql.DB
	now func() time.Time
}

func NewStore(db *sql.DB) *Store { return &Store{db: db, now: time.Now} }

func line(raw string, max int, required bool) (string, error) {
	v, err := form.Line(raw, max, required)
	if err != nil {
		return "", ErrInvalidInput
	}
	for _, r := range v {
		if unicode.IsControl(r) {
			return "", ErrInvalidInput
		}
	}
	return v, nil
}
func normalize(in Input) (Input, error) {
	var err error
	if in.Title, err = line(in.Title, 200, true); err != nil {
		return in, err
	}
	if in.Body, err = form.Text(in.Body, 50000, false); err != nil {
		return in, ErrInvalidInput
	}
	if in.CategoryID < 0 || len(in.TagIDs)+len(in.NewTags) > 20 {
		return in, ErrInvalidInput
	}
	in.TagIDs = slices.Clone(in.TagIDs)
	slices.Sort(in.TagIDs)
	in.TagIDs = slices.Compact(in.TagIDs)
	for _, id := range in.TagIDs {
		if id <= 0 {
			return in, ErrInvalidInput
		}
	}
	names := make([]string, 0, len(in.NewTags))
	for _, name := range in.NewTags {
		v, err := line(name, 50, true)
		if err != nil {
			return in, err
		}
		names = append(names, v)
	}
	slices.Sort(names)
	in.NewTags = slices.Compact(names)
	return in, nil
}

// write は判定と関連更新を同一接続・トランザクションに閉じる（CATALOG-INV-4, CATALOG-INV-5）。
func (s *Store) write(ctx context.Context, fn func(*sql.Conn) error) error {
	c, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer c.Close()
	if _, err = c.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	defer func() { _, _ = c.ExecContext(context.Background(), "ROLLBACK") }()
	if err = fn(c); err != nil {
		return err
	}
	_, err = c.ExecContext(ctx, "COMMIT")
	return err
}
func changed(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// Save は新規を下書きで作り、既存項目の公開状態は変えない。
// 不変条件: CATALOG-INV-4
func (s *Store) Save(ctx context.Context, id int64, in Input) (int64, error) {
	in, err := normalize(in)
	if err != nil {
		return 0, err
	}
	err = s.write(ctx, func(c *sql.Conn) error {
		var category any
		if in.CategoryID != 0 {
			var found int
			if err := c.QueryRowContext(ctx, "SELECT 1 FROM catalog_categories WHERE id=?", in.CategoryID).Scan(&found); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return ErrInvalidInput
				}
				return err
			}
			category = in.CategoryID
		}
		now := s.now().Unix()
		if id == 0 {
			res, err := c.ExecContext(ctx, "INSERT INTO catalog_items(title,body,status,category_id,created_at,updated_at) VALUES(?,?,'draft',?,?,?)", in.Title, in.Body, category, now, now)
			if err != nil {
				return err
			}
			id, err = res.LastInsertId()
			if err != nil {
				return err
			}
		} else if err := changed(c.ExecContext(ctx, "UPDATE catalog_items SET title=?,body=?,category_id=?,updated_at=? WHERE id=?", in.Title, in.Body, category, now, id)); err != nil {
			return err
		}
		ids := slices.Clone(in.TagIDs)
		for _, name := range in.NewTags {
			if _, err := c.ExecContext(ctx, "INSERT INTO catalog_tags(name) VALUES(?) ON CONFLICT(name) DO NOTHING", name); err != nil {
				return err
			}
			var tagID int64
			if err := c.QueryRowContext(ctx, "SELECT id FROM catalog_tags WHERE name=?", name).Scan(&tagID); err != nil {
				return err
			}
			ids = append(ids, tagID)
		}
		slices.Sort(ids)
		ids = slices.Compact(ids)
		if _, err := c.ExecContext(ctx, "DELETE FROM catalog_item_tags WHERE item_id=?", id); err != nil {
			return err
		}
		for _, tagID := range ids {
			var exists int
			if err := c.QueryRowContext(ctx, "SELECT 1 FROM catalog_tags WHERE id=?", tagID).Scan(&exists); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return ErrInvalidInput
				}
				return err
			}
			if _, err := c.ExecContext(ctx, "INSERT INTO catalog_item_tags(item_id,tag_id) VALUES(?,?)", id, tagID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return id, nil
}
func (s *Store) SetPublished(ctx context.Context, id int64, published bool) error {
	status := "draft"
	if published {
		status = "published"
	}
	return changed(s.db.ExecContext(ctx, "UPDATE catalog_items SET status=?,updated_at=? WHERE id=?", status, s.now().Unix(), id))
}
func (s *Store) Delete(ctx context.Context, id int64) error {
	return changed(s.db.ExecContext(ctx, "DELETE FROM catalog_items WHERE id=?", id))
}

const itemColumns = "i.id,i.title,i.body,i.status,i.category_id,COALESCE(c.name,''),i.created_at,i.updated_at"
const itemFrom = " FROM catalog_items i LEFT JOIN catalog_categories c ON c.id=i.category_id"

func scanItem(row interface{ Scan(...any) error }) (Item, error) {
	var item Item
	var status string
	var cat sql.NullInt64
	var created, updated int64
	err := row.Scan(&item.ID, &item.Title, &item.Body, &status, &cat, &item.Category, &created, &updated)
	item.Published = status == "published"
	item.CategoryID = cat.Int64
	item.CreatedAt = time.Unix(created, 0).In(time.Local)
	item.UpdatedAt = time.Unix(updated, 0).In(time.Local)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	return item, err
}
func (s *Store) Get(ctx context.Context, id int64, public bool) (Item, error) {
	where := " WHERE i.id=?"
	if public {
		where += " AND i.status='published'"
	}
	item, err := scanItem(s.db.QueryRowContext(ctx, "SELECT "+itemColumns+itemFrom+where, id))
	if err != nil {
		return item, err
	}
	q := "SELECT t.id,t.name FROM catalog_tags t JOIN catalog_item_tags it ON it.tag_id=t.id JOIN catalog_items i ON i.id=it.item_id WHERE i.id=?"
	if public {
		q += " AND i.status='published'"
	}
	q += " ORDER BY t.name,t.id"
	item.Tags, err = s.classifications(ctx, q, id)
	return item, err
}

func filterSQL(f Filter) (string, []any) {
	where := " WHERE i.status='published'"
	args := []any{}
	if f.Query != "" {
		escaped := strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(f.Query)
		where += ` AND (i.title LIKE ? ESCAPE '\' OR i.body LIKE ? ESCAPE '\')`
		args = append(args, "%"+escaped+"%", "%"+escaped+"%")
	}
	if f.CategoryID > 0 {
		where += " AND i.category_id=?"
		args = append(args, f.CategoryID)
	}
	if len(f.TagIDs) > 0 {
		marks := strings.TrimSuffix(strings.Repeat("?,", len(f.TagIDs)), ",")
		where += " AND i.id IN (SELECT item_id FROM catalog_item_tags WHERE tag_id IN (" + marks + ") GROUP BY item_id HAVING COUNT(DISTINCT tag_id)=?)"
		for _, id := range f.TagIDs {
			args = append(args, id)
		}
		args = append(args, len(f.TagIDs))
	}
	return where, args
}
func (s *Store) List(ctx context.Context, public bool, f Filter, limit, offset int) ([]Item, error) {
	where := ""
	var args []any
	if public {
		where, args = filterSQL(f)
	}
	args = append(args, limit, offset)
	rows, err := s.db.QueryContext(ctx, "SELECT "+itemColumns+itemFrom+where+" ORDER BY i.id DESC LIMIT ? OFFSET ?", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Item{}
	for rows.Next() {
		item, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func classificationTable(kind string) (string, error) {
	switch kind {
	case "categories":
		return "catalog_categories", nil
	case "tags":
		return "catalog_tags", nil
	}
	return "", ErrInvalidInput
}
func (s *Store) classifications(ctx context.Context, q string, args ...any) ([]Classification, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Classification{}
	for rows.Next() {
		var v Classification
		if err := rows.Scan(&v.ID, &v.Name); err != nil {
			return nil, err
		}
		result = append(result, v)
	}
	return result, rows.Err()
}
func (s *Store) Classifications(ctx context.Context, kind string, public bool) ([]Classification, error) {
	table, err := classificationTable(kind)
	if err != nil {
		return nil, err
	}
	q := "SELECT c.id,c.name FROM " + table + " c"
	if public {
		if kind == "categories" {
			q += " WHERE EXISTS(SELECT 1 FROM catalog_items i WHERE i.category_id=c.id AND i.status='published')"
		} else {
			q += " WHERE EXISTS(SELECT 1 FROM catalog_item_tags it JOIN catalog_items i ON i.id=it.item_id WHERE it.tag_id=c.id AND i.status='published')"
		}
	}
	return s.classifications(ctx, q+" ORDER BY c.name,c.id")
}
func (s *Store) Classification(ctx context.Context, kind string, id int64) (Classification, int, error) {
	table, err := classificationTable(kind)
	if err != nil {
		return Classification{}, 0, err
	}
	var v Classification
	v.ID = id
	err = s.db.QueryRowContext(ctx, "SELECT name FROM "+table+" WHERE id=?", id).Scan(&v.Name)
	if errors.Is(err, sql.ErrNoRows) {
		return v, 0, ErrNotFound
	}
	if err != nil {
		return v, 0, err
	}
	var n int
	q := "SELECT COUNT(*) FROM catalog_items WHERE category_id=?"
	if kind == "tags" {
		q = "SELECT COUNT(*) FROM catalog_item_tags WHERE tag_id=?"
	}
	err = s.db.QueryRowContext(ctx, q, id).Scan(&n)
	return v, n, err
}
func (s *Store) SaveClassification(ctx context.Context, kind string, id int64, name string) (int64, error) {
	table, err := classificationTable(kind)
	if err != nil {
		return 0, err
	}
	name, err = line(name, 50, true)
	if err != nil {
		return 0, err
	}
	err = s.write(ctx, func(c *sql.Conn) error {
		var existing int64
		err := c.QueryRowContext(ctx, "SELECT id FROM "+table+" WHERE name=?", name).Scan(&existing)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if existing != 0 && existing != id {
			return ErrInvalidInput
		}
		if id == 0 {
			res, err := c.ExecContext(ctx, "INSERT INTO "+table+"(name) VALUES(?)", name)
			if err != nil {
				return err
			}
			id, err = res.LastInsertId()
			return err
		}
		if err := s.touchClassification(ctx, c, kind, id); err != nil {
			return err
		}
		return changed(c.ExecContext(ctx, "UPDATE "+table+" SET name=? WHERE id=?", name, id))
	})
	return id, err
}
func (s *Store) touchClassification(ctx context.Context, c *sql.Conn, kind string, id int64) error {
	q := "UPDATE catalog_items SET updated_at=? WHERE category_id=?"
	if kind == "tags" {
		q = "UPDATE catalog_items SET updated_at=? WHERE id IN (SELECT item_id FROM catalog_item_tags WHERE tag_id=?)"
	}
	_, err := c.ExecContext(ctx, q, s.now().Unix(), id)
	return err
}
func (s *Store) DeleteClassification(ctx context.Context, kind string, id int64) error {
	table, err := classificationTable(kind)
	if err != nil {
		return err
	}
	return s.write(ctx, func(c *sql.Conn) error {
		if err := s.touchClassification(ctx, c, kind, id); err != nil {
			return err
		}
		return changed(c.ExecContext(ctx, "DELETE FROM "+table+" WHERE id=?", id))
	})
}
func idString(id int64) string { return strconv.FormatInt(id, 10) }

// busyError はSQLiteの一時的な書き込み競合だけを分類する。エラー文字列は表示・記録しない。
func busyError(err error) bool {
	var coded interface{ Code() int }
	return errors.As(err, &coded) && (coded.Code()&255 == 5 || coded.Code()&255 == 6)
}
