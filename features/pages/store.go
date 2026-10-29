package pages

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"

	"minimal-web-kit/internal/web/form"
	"minimal-web-kit/internal/web/richtext"
)

const (
	maxTitleRunes = 200
	maxBodyRunes  = 50000
	maxNavOrder   = 999

	statusDraft     = "draft"
	statusPublished = "published"
)

var ErrNotFound = errors.New("pages: not found")

// InputError は、どの欄の入力が誤りかを示します。Field は "slug"・"slug_taken"・"slug_reserved"・"title"・"body"・"body_limit"・"nav_order" のどれかです。
type InputError struct{ Field string }

func (e InputError) Error() string { return "pages: invalid " + e.Field }

// slugPattern は、英小文字・数字・ハイフンで2〜40文字、ハイフンで始まり・終わらない形です（PAGES-INV-3）。
var slugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,38}[a-z0-9]$`)

// reservedSlugs は、受け皿に落ちても使わせない語です。他の機能・Coreの経路の接頭辞と、将来使う見込みのある語。
// 登録済みの経路と同じスラッグは、別に経路に当てて確かめます（RouteCheckConsumer）。
var reservedSlugs = map[string]bool{
	"admin": true, "api": true, "static": true, "account": true, "news": true, "catalog": true, "contact": true,
	"privacy": true, "pages": true, "feed": true, "search": true,
}

// Page は固定ページ1件です。
type Page struct {
	ID                   int64
	Slug, Title, Body    string
	Published            bool
	NavOrder             *int
	CreatedAt, UpdatedAt time.Time
}

func (Page) LogValue() slog.Value { return slog.StringValue("[page]") }

// Input は管理画面の入力です。NavOrder は空ならナビに出さない。
type Input struct {
	Slug, Title, Body, NavOrder string
}

func (Input) LogValue() slog.Value { return slog.StringValue("[page input]") }

// normalized は検査を通した入力です。
type normalized struct {
	slug, title, body string
	navOrder          *int
}

// normalize は入力を検査します。taken は、登録済みの経路に当たるパスかを返す関数（nil なら確かめない）。
func normalize(in Input, taken func(string) bool) (normalized, error) {
	var n normalized
	n.slug = strings.TrimSpace(in.Slug)
	if !slugPattern.MatchString(n.slug) {
		return n, InputError{"slug"}
	}
	if reservedSlugs[n.slug] || (taken != nil && taken("/"+n.slug)) {
		return n, InputError{"slug_reserved"}
	}
	var err error
	if n.title, err = form.Line(in.Title, maxTitleRunes, true); err != nil {
		return n, InputError{"title"}
	}
	if n.body, err = form.Text(in.Body, maxBodyRunes, false); err != nil {
		return n, InputError{"body"}
	}
	if _, err := richtext.Parse(n.body); err != nil {
		return n, InputError{"body_limit"}
	}
	if raw := strings.TrimSpace(in.NavOrder); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v < 0 || v > maxNavOrder {
			return n, InputError{"nav_order"}
		}
		n.navOrder = &v
	}
	return n, nil
}

// Store は固定ページの読み書きを担当します。
type Store struct {
	db  *sql.DB
	now func() time.Time
}

func NewStore(db *sql.DB) *Store { return &Store{db: db, now: time.Now} }

const columns = "id, slug, title, body, status, nav_order, created_at, updated_at"

func scan(row interface{ Scan(...any) error }) (Page, error) {
	var p Page
	var status string
	var nav sql.NullInt64
	var created, updated int64
	if err := row.Scan(&p.ID, &p.Slug, &p.Title, &p.Body, &status, &nav, &created, &updated); err != nil {
		return p, err
	}
	p.Published = status == statusPublished
	if nav.Valid {
		v := int(nav.Int64)
		p.NavOrder = &v
	}
	p.CreatedAt, p.UpdatedAt = time.Unix(created, 0), time.Unix(updated, 0)
	return p, nil
}

func (s *Store) List(ctx context.Context) ([]Page, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+columns+" FROM pages ORDER BY nav_order IS NULL, nav_order, slug")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Page
	for rows.Next() {
		p, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) Get(ctx context.Context, id int64) (Page, error) {
	p, err := scan(s.db.QueryRowContext(ctx, "SELECT "+columns+" FROM pages WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	return p, err
}

// Published は、公開中のページをスラッグで返します。下書き・存在しないページは ErrNotFound（PAGES-INV-1）。
func (s *Store) Published(ctx context.Context, slug string) (Page, error) {
	p, err := scan(s.db.QueryRowContext(ctx, "SELECT "+columns+" FROM pages WHERE slug = ? AND status = 'published'", slug))
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	return p, err
}

// Save は作成（id が0）か更新をします。スラッグの重複は InputError{"slug_taken"}。公開状態は変えない。
func (s *Store) Save(ctx context.Context, id int64, in Input, taken func(string) bool) (int64, error) {
	n, err := normalize(in, taken)
	if err != nil {
		return 0, err
	}
	var nav any
	if n.navOrder != nil {
		nav = *n.navOrder
	}
	now := s.now().Unix()
	if id == 0 {
		res, err := s.db.ExecContext(ctx, "INSERT INTO pages (slug, title, body, nav_order, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)", n.slug, n.title, n.body, nav, now, now)
		if err != nil {
			return 0, uniqueError(err)
		}
		return res.LastInsertId()
	}
	res, err := s.db.ExecContext(ctx, "UPDATE pages SET slug = ?, title = ?, body = ?, nav_order = ?, updated_at = ? WHERE id = ?", n.slug, n.title, n.body, nav, now, id)
	if err != nil {
		return 0, uniqueError(err)
	}
	if c, err := res.RowsAffected(); err != nil {
		return 0, err
	} else if c == 0 {
		return 0, ErrNotFound
	}
	return id, nil
}

// uniqueError は、スラッグの重複（UNIQUE違反）を欄のエラーに変えます。
func uniqueError(err error) error {
	if strings.Contains(err.Error(), "UNIQUE") {
		return InputError{"slug_taken"}
	}
	return err
}

func (s *Store) SetPublished(ctx context.Context, id int64, published bool) error {
	status := statusDraft
	if published {
		status = statusPublished
	}
	res, err := s.db.ExecContext(ctx, "UPDATE pages SET status = ?, updated_at = ? WHERE id = ?", status, s.now().Unix(), id)
	if err != nil {
		return err
	}
	if c, _ := res.RowsAffected(); c == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) Delete(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, "DELETE FROM pages WHERE id = ?", id)
	if err != nil {
		return err
	}
	if c, _ := res.RowsAffected(); c == 0 {
		return ErrNotFound
	}
	return nil
}
