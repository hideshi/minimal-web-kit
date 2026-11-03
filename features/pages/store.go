package pages

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"minimal-web-kit/features"
	"minimal-web-kit/internal/store"
	"regexp"
	"slices"
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
	"privacy": true, "pages": true, "feed": true, "search": true, "media": true,
}

// Page は固定ページ1件です。
type Page struct {
	ID                   int64
	Slug, Title, Body    string
	Published            bool
	NavOrder             *int
	CreatedAt, UpdatedAt time.Time
	PublishAt            time.Time // 公開を始める日時。無ければゼロ値
}

// Scheduled は、公開の状態で、公開日時がまだ来ていないかを返します（予約公開）。
func (p Page) Scheduled(now time.Time) bool {
	return p.Published && !p.PublishAt.IsZero() && p.PublishAt.After(now)
}

// Live は、公開ページに出ているかを返します。
func (p Page) Live(now time.Time) bool { return p.Published && !p.Scheduled(now) }

func (Page) LogValue() slog.Value { return slog.StringValue("[page]") }

// Input は管理画面の入力です。NavOrder は空ならナビに出さない。
type Input struct {
	Slug, Title, Body, NavOrder string
	PublishAt                   string // datetime-local の値。空なら今の公開日時を変えない
}

func (Input) LogValue() slog.Value { return slog.StringValue("[page input]") }

// normalized は検査を通した入力です。
type normalized struct {
	slug, title, body string
	navOrder          *int
	images            []string  // 本文が使う画像のファイル名
	publishAt         time.Time // ゼロ値なら変えない
}

// checkSlug は、スラッグの形と、予約語・登録済みの経路に当たらないかを確かめます（PAGES-INV-3）。重複は確かめません。
func checkSlug(slug string, taken func(string) bool) error {
	if !slugPattern.MatchString(slug) {
		return InputError{"slug"}
	}
	if reservedSlugs[slug] || (taken != nil && taken("/"+slug)) {
		return InputError{"slug_reserved"}
	}
	return nil
}

// normalize は入力を検査します。taken は、登録済みの経路に当たるパスかを返す関数（nil なら確かめない）。
func normalize(in Input, taken func(string) bool) (normalized, error) {
	var n normalized
	n.slug = strings.TrimSpace(in.Slug)
	if err := checkSlug(n.slug, taken); err != nil {
		return n, err
	}
	var err error
	if n.title, err = form.Line(in.Title, maxTitleRunes, true); err != nil {
		return n, InputError{"title"}
	}
	if n.body, err = form.Text(in.Body, maxBodyRunes, false); err != nil {
		return n, InputError{"body"}
	}
	doc, err := richtext.ParseWithImages(n.body)
	if err != nil {
		return n, InputError{"body_limit"}
	}
	n.images = doc.Images() // 表示と同じ読み方の結果から取り出す（MEDIA-INV-8）
	if raw := strings.TrimSpace(in.NavOrder); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v < 0 || v > maxNavOrder {
			return n, InputError{"nav_order"}
		}
		n.navOrder = &v
	}
	at, ok := form.ParseLocalDateTime(strings.TrimSpace(in.PublishAt))
	if !ok {
		return n, InputError{"publish_at"}
	}
	n.publishAt = at
	return n, nil
}

// Store は固定ページの読み書きを担当します。
type Store struct {
	db    *sql.DB
	now   func() time.Time
	media features.Media
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db, now: time.Now, media: features.NoMedia{}}
}

// mediaOwner は、画像の参照に記録する機能の名前です。
const mediaOwner = "pages"

// saveRefs は、同じトランザクションの中で本文の画像の参照を置き換えます（MEDIA-INV-5）。
func (s *Store) saveRefs(ctx context.Context, c *sql.Conn, id int64, images []string) error {
	err := s.media.ReplaceRefs(ctx, c, mediaOwner, strconv.FormatInt(id, 10), images)
	if errors.Is(err, features.ErrMediaMissing) {
		return InputError{"body_image"}
	}
	return err
}

const columns = "id, slug, title, body, status, nav_order, created_at, updated_at, COALESCE(publish_at, 0)"

// publicPage は、公開ページに出すページの条件です。予約公開の日時を過ぎたものだけを出します。引数は今の時刻（Unix秒）です。
const publicPage = "status = 'published' AND (publish_at IS NULL OR publish_at <= ?)"

func publishArg(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.Unix()
}

// checkPublishAt は、公開ページに出ているページを未来の日時にする保存を拒みます。保存のトランザクションの中で、今の行と時刻で判定します。
func (s *Store) checkPublishAt(ctx context.Context, c *sql.Conn, id int64, at time.Time) error {
	if at.IsZero() || id == 0 || !at.After(s.now()) {
		return nil
	}
	var status string
	var current sql.NullInt64
	if err := c.QueryRowContext(ctx, "SELECT status, publish_at FROM pages WHERE id = ?", id).Scan(&status, &current); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if status == statusPublished && (!current.Valid || current.Int64 <= s.now().Unix()) {
		return InputError{"publish_at_live"}
	}
	return nil
}

func scan(row interface{ Scan(...any) error }) (Page, error) {
	var p Page
	var status string
	var nav sql.NullInt64
	var created, updated, publishAt int64
	if err := row.Scan(&p.ID, &p.Slug, &p.Title, &p.Body, &status, &nav, &created, &updated, &publishAt); err != nil {
		return p, err
	}
	if publishAt > 0 {
		p.PublishAt = time.Unix(publishAt, 0).In(time.Local)
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
	p, err := scan(s.db.QueryRowContext(ctx, "SELECT "+columns+" FROM pages WHERE slug = ? AND "+publicPage, slug, s.now().Unix()))
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
	err = store.Atomic(ctx, s.db, func(c *sql.Conn) error {
		if err := s.checkPublishAt(ctx, c, id, n.publishAt); err != nil {
			return err
		}
		if id == 0 {
			res, err := c.ExecContext(ctx, "INSERT INTO pages (slug, title, body, nav_order, created_at, updated_at, publish_at) VALUES (?, ?, ?, ?, ?, ?, ?)", n.slug, n.title, n.body, nav, now, now, publishArg(n.publishAt))
			if err != nil {
				return uniqueError(err)
			}
			if id, err = res.LastInsertId(); err != nil {
				return err
			}
		} else {
			res, err := c.ExecContext(ctx, "UPDATE pages SET slug = ?, title = ?, body = ?, nav_order = ?, updated_at = ?, publish_at = COALESCE(?, publish_at) WHERE id = ?", n.slug, n.title, n.body, nav, now, publishArg(n.publishAt), id)
			if err != nil {
				return uniqueError(err)
			}
			if err := store.AffectedOne(res, nil, ErrNotFound); err != nil {
				return err
			}
		}
		return s.saveRefs(ctx, c, id, n.images)
	})
	if err != nil {
		return 0, err
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

// SetPublished は公開状態を変えます。非公開に戻すと公開日時も消します（次に公開するときに古い予約を残さない）。
func (s *Store) SetPublished(ctx context.Context, id int64, published bool) error {
	if published {
		res, err := s.db.ExecContext(ctx, "UPDATE pages SET status = 'published', updated_at = ? WHERE id = ?", s.now().Unix(), id)
		return store.AffectedOne(res, err, ErrNotFound)
	}
	res, err := s.db.ExecContext(ctx, "UPDATE pages SET status = 'draft', publish_at = NULL, updated_at = ? WHERE id = ?", s.now().Unix(), id)
	return store.AffectedOne(res, err, ErrNotFound)
}

// mcpState は、確認に使う保存状態のハッシュです。公開状態・スラッグ・ナビの順を含みます。
func mcpState(p Page) string {
	var nav any
	if p.NavOrder != nil {
		nav = *p.NavOrder
	}
	return features.MCPState("page", p.ID, p.Slug, p.Title, p.Body, p.Published, nav, publishArg(p.PublishAt))
}

func navArg(n *int) any {
	if n == nil {
		return nil
	}
	return *n
}

func statusOf(published bool) string {
	if published {
		return statusPublished
	}
	return statusDraft
}

// SaveIfUnchanged は、読み取った内容のままのときだけ更新します。判定と更新は1つの文です（MCP-INV-4）。
func (s *Store) SaveIfUnchanged(ctx context.Context, cur Page, in Input, taken func(string) bool) error {
	n, err := normalize(in, taken)
	if err != nil {
		return err
	}
	return store.Atomic(ctx, s.db, func(c *sql.Conn) error {
		if err := s.checkPublishAt(ctx, c, cur.ID, n.publishAt); err != nil {
			return err
		}
		res, err := c.ExecContext(ctx,
			`UPDATE pages SET slug = ?, title = ?, body = ?, nav_order = ?, updated_at = ?, publish_at = COALESCE(?, publish_at)
			 WHERE id = ? AND slug = ? AND title = ? AND body = ? AND status = ? AND nav_order IS ? AND publish_at IS ?`,
			n.slug, n.title, n.body, navArg(n.navOrder), s.now().Unix(), publishArg(n.publishAt), cur.ID, cur.Slug, cur.Title, cur.Body, statusOf(cur.Published), navArg(cur.NavOrder), publishArg(cur.PublishAt))
		if err != nil {
			return uniqueError(err)
		}
		if err := store.AffectedOne(res, nil, features.ErrMCPConflict); err != nil {
			return err
		}
		return s.saveRefs(ctx, c, cur.ID, n.images)
	})
}

// SetPublishedIfUnchanged は、読み取った内容のままのときだけ公開状態を変えます。
func (s *Store) SetPublishedIfUnchanged(ctx context.Context, cur Page, published bool) error {
	// 非公開に戻すと公開日時も消す。照合には公開日時も含める
	res, err := s.db.ExecContext(ctx,
		`UPDATE pages SET status = ?, updated_at = ?, publish_at = CASE WHEN ? = 'draft' THEN NULL ELSE publish_at END
		 WHERE id = ? AND slug = ? AND title = ? AND body = ? AND status = ? AND nav_order IS ? AND publish_at IS ?`,
		statusOf(published), s.now().Unix(), statusOf(published), cur.ID, cur.Slug, cur.Title, cur.Body, statusOf(cur.Published), navArg(cur.NavOrder), publishArg(cur.PublishAt))
	return store.AffectedOne(res, err, features.ErrMCPConflict)
}

func (s *Store) Delete(ctx context.Context, id int64) error {
	return store.Atomic(ctx, s.db, func(c *sql.Conn) error {
		res, err := c.ExecContext(ctx, "DELETE FROM pages WHERE id = ?", id)
		if err := store.AffectedOne(res, err, ErrNotFound); err != nil {
			return err
		}
		return s.media.DeleteRefs(ctx, c, mediaOwner, strconv.FormatInt(id, 10))
	})
}

// imageRefs は、全てのページが使う画像を返します。
func (s *Store) imageRefs(ctx context.Context) (map[string][]string, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id, body FROM pages")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	refs := map[string][]string{}
	for rows.Next() {
		var id int64
		var body string
		if err := rows.Scan(&id, &body); err != nil {
			return nil, err
		}
		// 上限を超えた本文は公開できないため、参照も無いものとして扱う
		if doc, err := richtext.ParseWithImages(body); err == nil {
			refs[strconv.FormatInt(id, 10)] = doc.Images()
		}
	}
	return refs, rows.Err()
}

// publishedUses は、ページが公開中で、本文がその画像を使っているかを1回の読み取りで確かめます。
func (s *Store) publishedUses(ctx context.Context, id int64, file string) (bool, error) {
	var body string
	err := s.db.QueryRowContext(ctx, "SELECT body FROM pages WHERE id = ? AND "+publicPage, id, s.now().Unix()).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	doc, err := richtext.ParseWithImages(body)
	if err != nil {
		return false, nil // 上限を超えた本文は公開できない
	}
	return slices.Contains(doc.Images(), file), nil
}
