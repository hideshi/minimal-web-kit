package catalog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"minimal-web-kit/features"
	"minimal-web-kit/internal/store"
	"slices"
	"strconv"
	"strings"
	"time"

	"minimal-web-kit/internal/web/form"
	"minimal-web-kit/internal/web/richtext"
)

var (
	ErrNotFound     = errors.New("catalog: not found")
	ErrInvalidInput = errors.New("catalog: invalid input")
	// ErrDuplicateName は同種の分類名の重複。ErrInvalidInput としても判定できる。
	ErrDuplicateName = fmt.Errorf("catalog: duplicate name: %w", ErrInvalidInput)
)

// InputError は、項目の入力のどの欄が誤りかを示す。ErrInvalidInput としても判定できる。
// Field は "title"・"body"・"category"・"tags"・"new_tags"・"cover" のどれか。
type InputError struct{ Field string }

func (e InputError) Error() string { return "catalog: invalid " + e.Field }
func (e InputError) Unwrap() error { return ErrInvalidInput }

type Item struct {
	ID                   int64
	Title, Body          string
	Published            bool
	CategoryID           int64
	Category             string
	Tags                 []Classification
	CreatedAt, UpdatedAt time.Time
	CoverFile            string    // 代表画像のファイル名。無ければ空
	PublishAt            time.Time // 公開を始める日時。無ければゼロ値
}

// Scheduled は、公開の状態で、公開日時がまだ来ていないかを返します（予約公開）。
func (i Item) Scheduled(now time.Time) bool {
	return i.Published && !i.PublishAt.IsZero() && i.PublishAt.After(now)
}

// Live は、公開ページに出ているかを返します。
func (i Item) Live(now time.Time) bool { return i.Published && !i.Scheduled(now) }

func (Item) LogValue() slog.Value { return slog.StringValue("[catalog item]") }

type Classification struct {
	ID   int64
	Name string
}

// ClassificationUsage は管理画面の一覧用に、分類と、それを使う項目の数（公開・下書きを含む）を持つ。
type ClassificationUsage struct {
	Classification
	Items int
}

func (Classification) LogValue() slog.Value { return slog.StringValue("[catalog classification]") }

type Input struct {
	Title, Body string
	CategoryID  int64
	TagIDs      []int64
	NewTags     []string
	// SetCover が true のときだけ代表画像を変えます（MCPと画像機能の無い構成は値を変えない）。
	SetCover bool
	Cover    string
	// PublishAt は公開を始める日時です。ゼロ値なら今の値を変えません。
	PublishAt time.Time
}

func (Input) LogValue() slog.Value { return slog.StringValue("[catalog input]") }

type Filter struct {
	Query      string
	CategoryID int64
	TagIDs     []int64
}

func (Filter) LogValue() slog.Value { return slog.StringValue("[catalog filter]") }

type Store struct {
	db    *sql.DB
	now   func() time.Time
	media features.Media
}

// publicItem は、公開ページに出す項目の条件です。予約公開の日時を過ぎたものだけを出します。引数は今の時刻（Unix秒）です。
const publicItem = "i.status='published' AND (i.publish_at IS NULL OR i.publish_at<=?)"

func NewStore(db *sql.DB) *Store {
	return &Store{db: db, now: time.Now, media: features.NoMedia{}}
}

// mediaOwner は、画像の参照に記録する機能の名前です。
const mediaOwner = "catalog"

// saveCover は、同じトランザクションの中で代表画像と参照を更新します（MEDIA-INV-5）。
func (s *Store) saveCover(ctx context.Context, c *sql.Conn, id int64, cover string) error {
	var arg any
	var files []string
	if cover != "" {
		arg, files = cover, []string{cover}
	}
	if _, err := c.ExecContext(ctx, "UPDATE catalog_items SET cover_file=? WHERE id=?", arg, id); err != nil {
		return err
	}
	err := s.media.ReplaceRefs(ctx, c, mediaOwner, strconv.FormatInt(id, 10), files)
	if errors.Is(err, features.ErrMediaMissing) {
		return InputError{"cover"}
	}
	return err
}

// publishedCover は、項目が公開中で、代表画像がその画像かを1回の読み取りで確かめます。
func (s *Store) publishedCover(ctx context.Context, id int64, file string) (bool, error) {
	var found int
	err := s.db.QueryRowContext(ctx, "SELECT 1 FROM catalog_items i WHERE i.id=? AND "+publicItem+" AND i.cover_file=?", id, s.now().Unix(), file).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// coverRefs は、代表画像を持つ項目を返します。
func (s *Store) coverRefs(ctx context.Context) (map[string][]string, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id,cover_file FROM catalog_items WHERE cover_file IS NOT NULL")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	refs := map[string][]string{}
	for rows.Next() {
		var id int64
		var cover string
		if err := rows.Scan(&id, &cover); err != nil {
			return nil, err
		}
		refs[strconv.FormatInt(id, 10)] = []string{cover}
	}
	return refs, rows.Err()
}

func line(raw string, max int, required bool) (string, error) {
	v, err := form.Line(raw, max, required)
	if err != nil {
		return "", ErrInvalidInput
	}
	return v, nil
}
func normalize(in Input) (Input, error) {
	var err error
	if in.Title, err = line(in.Title, 200, true); err != nil {
		return in, InputError{"title"}
	}
	if in.Body, err = form.Text(in.Body, 50000, false); err != nil {
		return in, InputError{"body"}
	}
	if in.CategoryID < 0 {
		return in, InputError{"category"}
	}
	if in.SetCover && in.Cover != "" && !richtext.ImageFile(in.Cover) {
		return in, InputError{"cover"}
	}
	if len(in.TagIDs)+len(in.NewTags) > 20 {
		return in, InputError{"tags"}
	}
	in.TagIDs = slices.Clone(in.TagIDs)
	slices.Sort(in.TagIDs)
	in.TagIDs = slices.Compact(in.TagIDs)
	for _, id := range in.TagIDs {
		if id <= 0 {
			return in, InputError{"tags"}
		}
	}
	names := make([]string, 0, len(in.NewTags))
	for _, name := range in.NewTags {
		v, err := line(name, 50, true)
		if err != nil {
			return in, InputError{"new_tags"}
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
	return store.AffectedOne(res, err, ErrNotFound)
}

// Save は新規を下書きで作り、既存項目の公開状態は変えない。
// 不変条件: CATALOG-INV-4
func (s *Store) Save(ctx context.Context, id int64, in Input) (int64, error) {
	return s.save(ctx, id, in, func(*sql.Conn) error { return nil })
}

// SaveIfUnchanged は、確認の時点の状態のままのときだけ更新します。照合と更新は同じトランザクションです（MCP-INV-4）。
func (s *Store) SaveIfUnchanged(ctx context.Context, id int64, in Input, state string) (int64, error) {
	return s.save(ctx, id, in, func(c *sql.Conn) error { return s.expectState(ctx, c, id, state) })
}

// mcpState は、確認に使う保存状態のハッシュです。公開状態と分類を含みます。
func mcpState(item Item) string {
	tags := make([]int64, 0, len(item.Tags))
	for _, t := range item.Tags {
		tags = append(tags, t.ID)
	}
	slices.Sort(tags)
	return features.MCPState("item", item.ID, item.Title, item.Body, item.Published, item.CategoryID, tags, item.CoverFile, unixOrZero(item.PublishAt))
}

func unixOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

// publishArg は、公開日時の列に渡す値です。ゼロ値なら NULL です。
func publishArg(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.Unix()
}

// checkPublishAt は、公開ページに出ている項目を未来の日時にする保存を拒みます。保存のトランザクションの中で、今の行と時刻で判定します。
func (s *Store) checkPublishAt(ctx context.Context, c *sql.Conn, id int64, at time.Time) error {
	if at.IsZero() || id == 0 || !at.After(s.now()) {
		return nil
	}
	var status string
	var current sql.NullInt64
	if err := c.QueryRowContext(ctx, "SELECT status,publish_at FROM catalog_items WHERE id=?", id).Scan(&status, &current); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if status == "published" && (!current.Valid || current.Int64 <= s.now().Unix()) {
		return InputError{"publish_at_live"}
	}
	return nil
}

// expectState は、トランザクションの中で今の状態を読んで比べます。
func (s *Store) expectState(ctx context.Context, c *sql.Conn, id int64, state string) error {
	item, err := scanItem(c.QueryRowContext(ctx, "SELECT "+itemColumns+itemFrom+" WHERE i.id=?", id))
	if errors.Is(err, ErrNotFound) {
		return features.ErrMCPConflict
	} else if err != nil {
		return err
	}
	rows, err := c.QueryContext(ctx, "SELECT tag_id FROM catalog_item_tags WHERE item_id=?", id)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var tag Classification
		if err := rows.Scan(&tag.ID); err != nil {
			return err
		}
		item.Tags = append(item.Tags, tag)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if mcpState(item) != state {
		return features.ErrMCPConflict
	}
	return nil
}

// SetPublishedIfUnchanged は、確認の時点の状態のままのときだけ公開状態を変えます。
func (s *Store) SetPublishedIfUnchanged(ctx context.Context, id int64, published bool, state string) error {
	return s.write(ctx, func(c *sql.Conn) error {
		if err := s.expectState(ctx, c, id, state); err != nil {
			return err
		}
		return s.setPublished(ctx, c, id, published)
	})
}

// setPublished は公開状態を変えます。非公開に戻すと公開日時も消します（次に公開するときに古い予約を残さない）。
func (s *Store) setPublished(ctx context.Context, c *sql.Conn, id int64, published bool) error {
	if published {
		return changed(c.ExecContext(ctx, "UPDATE catalog_items SET status='published',updated_at=? WHERE id=?", s.now().Unix(), id))
	}
	return changed(c.ExecContext(ctx, "UPDATE catalog_items SET status='draft',publish_at=NULL,updated_at=? WHERE id=?", s.now().Unix(), id))
}

func (s *Store) save(ctx context.Context, id int64, in Input, check func(*sql.Conn) error) (int64, error) {
	in, err := normalize(in)
	if err != nil {
		return 0, err
	}
	err = s.write(ctx, func(c *sql.Conn) error {
		if err := check(c); err != nil {
			return err
		}
		if err := s.checkPublishAt(ctx, c, id, in.PublishAt); err != nil {
			return err
		}
		var category any
		if in.CategoryID != 0 {
			var found int
			if err := c.QueryRowContext(ctx, "SELECT 1 FROM catalog_categories WHERE id=?", in.CategoryID).Scan(&found); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return InputError{"category"}
				}
				return err
			}
			category = in.CategoryID
		}
		now := s.now().Unix()
		if id == 0 {
			res, err := c.ExecContext(ctx, "INSERT INTO catalog_items(title,body,status,category_id,created_at,updated_at,publish_at) VALUES(?,?,'draft',?,?,?,?)", in.Title, in.Body, category, now, now, publishArg(in.PublishAt))
			if err != nil {
				return err
			}
			id, err = res.LastInsertId()
			if err != nil {
				return err
			}
		} else if err := changed(c.ExecContext(ctx, "UPDATE catalog_items SET title=?,body=?,category_id=?,updated_at=?,publish_at=COALESCE(?,publish_at) WHERE id=?", in.Title, in.Body, category, now, publishArg(in.PublishAt), id)); err != nil {
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
					return InputError{"tags"}
				}
				return err
			}
			if _, err := c.ExecContext(ctx, "INSERT INTO catalog_item_tags(item_id,tag_id) VALUES(?,?)", id, tagID); err != nil {
				return err
			}
		}
		if in.SetCover {
			return s.saveCover(ctx, c, id, in.Cover)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return id, nil
}
func (s *Store) SetPublished(ctx context.Context, id int64, published bool) error {
	return s.write(ctx, func(c *sql.Conn) error { return s.setPublished(ctx, c, id, published) })
}
func (s *Store) Delete(ctx context.Context, id int64) error {
	return s.write(ctx, func(c *sql.Conn) error {
		if err := changed(c.ExecContext(ctx, "DELETE FROM catalog_items WHERE id=?", id)); err != nil {
			return err
		}
		return s.media.DeleteRefs(ctx, c, mediaOwner, strconv.FormatInt(id, 10))
	})
}

const itemColumns = "i.id,i.title,i.body,i.status,i.category_id,COALESCE(c.name,''),i.created_at,i.updated_at,COALESCE(i.cover_file,''),COALESCE(i.publish_at,0)"
const itemFrom = " FROM catalog_items i LEFT JOIN catalog_categories c ON c.id=i.category_id"

func scanItem(row interface{ Scan(...any) error }) (Item, error) {
	var item Item
	var status string
	var cat sql.NullInt64
	var created, updated, publishAt int64
	err := row.Scan(&item.ID, &item.Title, &item.Body, &status, &cat, &item.Category, &created, &updated, &item.CoverFile, &publishAt)
	if publishAt > 0 {
		item.PublishAt = time.Unix(publishAt, 0).In(time.Local)
	}
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
	args := []any{id}
	if public {
		where += " AND " + publicItem
		args = append(args, s.now().Unix())
	}
	item, err := scanItem(s.db.QueryRowContext(ctx, "SELECT "+itemColumns+itemFrom+where, args...))
	if err != nil {
		return item, err
	}
	q := "SELECT t.id,t.name FROM catalog_tags t JOIN catalog_item_tags it ON it.tag_id=t.id JOIN catalog_items i ON i.id=it.item_id WHERE i.id=?"
	if public {
		q += " AND " + publicItem
	}
	q += " ORDER BY t.name,t.id"
	item.Tags, err = s.classifications(ctx, q, args...)
	return item, err
}

func filterSQL(f Filter, now time.Time) (string, []any) {
	where := " WHERE " + publicItem
	args := []any{now.Unix()}
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
		where, args = filterSQL(f, s.now())
	}
	args = append(args, limit, offset)
	rows, err := s.db.QueryContext(ctx, "SELECT "+itemColumns+itemFrom+where+" ORDER BY i.id DESC LIMIT ? OFFSET ?", args...) // #nosec G701 -- 連結するのは固定の部品だけで、値は ? で渡す（test/sql_static_test.go）
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
	var args []any
	if public {
		if kind == "categories" {
			q += " WHERE EXISTS(SELECT 1 FROM catalog_items i WHERE i.category_id=c.id AND " + publicItem + ")"
		} else {
			q += " WHERE EXISTS(SELECT 1 FROM catalog_item_tags it JOIN catalog_items i ON i.id=it.item_id WHERE it.tag_id=c.id AND " + publicItem + ")"
		}
		args = append(args, s.now().Unix())
	}
	return s.classifications(ctx, q+" ORDER BY c.name,c.id", args...)
}

// ClassificationUsages は管理画面の一覧用に、全分類と使っている項目の数を名前順で返す。
func (s *Store) ClassificationUsages(ctx context.Context, kind string) ([]ClassificationUsage, error) {
	table, err := classificationTable(kind)
	if err != nil {
		return nil, err
	}
	count := "SELECT COUNT(*) FROM catalog_items i WHERE i.category_id=c.id"
	if kind == "tags" {
		count = "SELECT COUNT(*) FROM catalog_item_tags it WHERE it.tag_id=c.id"
	}
	rows, err := s.db.QueryContext(ctx, "SELECT c.id,c.name,("+count+") FROM "+table+" c ORDER BY c.name,c.id") // #nosec G202 -- 表の名前は classificationTable が固定の文字列から選ぶ（test/sql_static_test.go）
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []ClassificationUsage{}
	for rows.Next() {
		var v ClassificationUsage
		if err := rows.Scan(&v.ID, &v.Name, &v.Items); err != nil {
			return nil, err
		}
		result = append(result, v)
	}
	return result, rows.Err()
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
			return ErrDuplicateName
		}
		if id == 0 {
			res, err := c.ExecContext(ctx, "INSERT INTO "+table+"(name) VALUES(?)", name) // #nosec G202 -- 表の名前は classificationTable が固定の文字列から選び、値は ? で渡す（test/sql_static_test.go）
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
