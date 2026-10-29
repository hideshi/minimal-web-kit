package news

import (
	"context"
	"database/sql"
	"errors"
	"minimal-web-kit/internal/store"
	"time"

	"minimal-web-kit/internal/web/form"
)

const (
	maxTitleRunes = 200
	maxBodyRunes  = 50000

	statusDraft     = "draft"
	statusPublished = "published"
)

var (
	// ErrNotFound は記事が存在しない（公開側では、公開中でない）ことを示します。
	ErrNotFound = errors.New("news: not found")
	// ErrInvalidInput は入力が制約を満たさないことを示します。
	ErrInvalidInput = errors.New("news: invalid input")
)

// InputError は、どの欄の入力が制約を満たさないかを示します。ErrInvalidInput としても判定できます。
// Field は "title"・"body"・"published_at" のどれかです。
type InputError struct{ Field string }

func (e InputError) Error() string { return "news: invalid " + e.Field }
func (e InputError) Unwrap() error { return ErrInvalidInput }

// Post はお知らせ1件です。PublishedAt は一度も公開していなければゼロ値です。
type Post struct {
	ID          int64
	Title       string
	Body        string
	Published   bool
	PublishedAt time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// ScheduledAt は、予約公開で公開日時前の記事かどうかを返します（管理画面の表示用）。
func (p Post) ScheduledAt(now time.Time) bool {
	return p.Published && p.PublishedAt.After(now)
}

// PostStore はお知らせの読み書きを担当します。HTML表示・JSON API・管理画面で共通に使います。
type PostStore struct {
	db  *sql.DB
	now func() time.Time
}

func NewPostStore(db *sql.DB) *PostStore {
	return &PostStore{db: db, now: time.Now}
}

// normalizePost はタイトル（1行）と本文を検査します。誤りは、欄を示す InputError で返します。
func normalizePost(title, body string) (string, string, error) {
	title, err := form.Line(title, maxTitleRunes, true)
	if err != nil {
		return "", "", InputError{"title"}
	}
	body, err = form.Text(body, maxBodyRunes, false)
	if err != nil {
		return "", "", InputError{"body"}
	}
	return title, body, nil
}

// Create は下書きとして記事を作成し、IDを返します。
func (s *PostStore) Create(ctx context.Context, title, body string) (int64, error) {
	return s.CreateWithDate(ctx, title, body, time.Time{})
}

// CreateWithDate は公開日時を指定して下書きを作成します。publishedAt がゼロ値なら、初めて公開したときに設定します。
func (s *PostStore) CreateWithDate(ctx context.Context, title, body string, publishedAt time.Time) (int64, error) {
	title, body, err := normalizePost(title, body)
	if err != nil {
		return 0, err
	}
	at, err := s.publishedAtArg(publishedAt)
	if err != nil {
		return 0, err
	}
	now := s.now().Unix()
	res, err := s.db.ExecContext(ctx,
		"INSERT INTO news_posts (title, body, status, published_at, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)",
		title, body, statusDraft, at, now, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// Update は記事のタイトルと本文を更新します。公開状態は変えません。
func (s *PostStore) Update(ctx context.Context, id int64, title, body string) error {
	return s.UpdateWithDate(ctx, id, title, body, time.Time{})
}

// UpdateWithDate はタイトル・本文と公開日時を1つの文で更新します。publishedAt がゼロ値なら公開日時は変えません。
func (s *PostStore) UpdateWithDate(ctx context.Context, id int64, title, body string, publishedAt time.Time) error {
	title, body, err := normalizePost(title, body)
	if err != nil {
		return err
	}
	at, err := s.publishedAtArg(publishedAt)
	if err != nil {
		return err
	}
	return s.execOne(ctx,
		"UPDATE news_posts SET title = ?, body = ?, published_at = COALESCE(?, published_at), updated_at = ? WHERE id = ?",
		title, body, at, s.now().Unix(), id)
}

// publishedAtArg は公開日時をSQLの引数に変換します。ゼロ値はNULLです。
// 未来の日時は予約公開になります（公開ページ・APIは、公開日時を過ぎるまで表示しない）。
func (s *PostStore) publishedAtArg(t time.Time) (any, error) {
	if t.IsZero() {
		return nil, nil
	}
	if t.Unix() <= 0 {
		return nil, InputError{"published_at"}
	}
	return t.Unix(), nil
}

// Publish は記事を公開します。初めて公開する場合だけ公開日時を設定し、再公開では維持します。
// 公開日時が未来なら予約公開となり、その日時を過ぎてから公開ページ・APIに表示されます（NEWS-INV-2）。
func (s *PostStore) Publish(ctx context.Context, id int64) error {
	now := s.now().Unix()
	return s.execOne(ctx,
		"UPDATE news_posts SET status = ?, published_at = COALESCE(published_at, ?), updated_at = ? WHERE id = ?",
		statusPublished, now, now, id)
}

// Unpublish は記事を下書きに戻します。公開日時は維持します。
func (s *PostStore) Unpublish(ctx context.Context, id int64) error {
	return s.execOne(ctx,
		"UPDATE news_posts SET status = ?, updated_at = ? WHERE id = ?",
		statusDraft, s.now().Unix(), id)
}

// SetPublishedAt は公開日時を明示的に変更します。
func (s *PostStore) SetPublishedAt(ctx context.Context, id int64, at time.Time) error {
	if at.IsZero() {
		return ErrInvalidInput
	}
	arg, err := s.publishedAtArg(at)
	if err != nil {
		return err
	}
	return s.execOne(ctx,
		"UPDATE news_posts SET published_at = ?, updated_at = ? WHERE id = ?",
		arg, s.now().Unix(), id)
}

// Delete は記事を物理削除します。
func (s *PostStore) Delete(ctx context.Context, id int64) error {
	return s.execOne(ctx, "DELETE FROM news_posts WHERE id = ?", id)
}

// Get は公開状態にかかわらず記事を返します（管理画面用）。
func (s *PostStore) Get(ctx context.Context, id int64) (Post, error) {
	return s.getOne(ctx, "SELECT "+postColumns+" FROM news_posts WHERE id = ?", id)
}

// GetPublished は公開中の記事だけを返します（公開ページ・API用）。予約公開で公開日時前の記事は含みません（NEWS-INV-1）。
func (s *PostStore) GetPublished(ctx context.Context, id int64) (Post, error) {
	return s.getOne(ctx, "SELECT "+postColumns+" FROM news_posts WHERE id = ? AND status = ? AND published_at <= ?", id, statusPublished, s.now().Unix())
}

// ListPublished は公開中の記事を新しい順に返します。予約公開で公開日時前の記事は含みません（NEWS-INV-1）。
func (s *PostStore) ListPublished(ctx context.Context, limit, offset int) ([]Post, error) {
	return s.list(ctx,
		"SELECT "+postColumns+" FROM news_posts WHERE status = ? AND published_at <= ? ORDER BY published_at DESC, id DESC LIMIT ? OFFSET ?",
		statusPublished, s.now().Unix(), limit, offset)
}

// PostRef は、記事のIDと更新日時だけを持つ参照です（sitemap.xml 用）。
type PostRef struct {
	ID        int64
	UpdatedAt time.Time
}

// PublishedRefs は、公開中の記事のIDと更新日時を、新しい順に最大 limit 件返します。本文は読みません（NEWS-INV-1）。
func (s *PostStore) PublishedRefs(ctx context.Context, limit int) ([]PostRef, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT id, updated_at FROM news_posts WHERE status = ? AND published_at <= ? ORDER BY published_at DESC, id DESC LIMIT ?",
		statusPublished, s.now().Unix(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var refs []PostRef
	for rows.Next() {
		var ref PostRef
		var updated int64
		if err := rows.Scan(&ref.ID, &updated); err != nil {
			return nil, err
		}
		ref.UpdatedAt = time.Unix(updated, 0).UTC()
		refs = append(refs, ref)
	}
	return refs, rows.Err()
}

// ListAll は全記事を更新の新しい順に返します（管理画面用）。
func (s *PostStore) ListAll(ctx context.Context, limit, offset int) ([]Post, error) {
	return s.list(ctx,
		"SELECT "+postColumns+" FROM news_posts ORDER BY updated_at DESC, id DESC LIMIT ? OFFSET ?",
		limit, offset)
}

const postColumns = "id, title, body, status, published_at, created_at, updated_at"

type rowScanner interface {
	Scan(dest ...any) error
}

func scanPost(row rowScanner) (Post, error) {
	var p Post
	var status string
	var publishedAt sql.NullInt64
	var createdAt, updatedAt int64
	if err := row.Scan(&p.ID, &p.Title, &p.Body, &status, &publishedAt, &createdAt, &updatedAt); err != nil {
		return Post{}, err
	}
	p.Published = status == statusPublished
	if publishedAt.Valid {
		p.PublishedAt = time.Unix(publishedAt.Int64, 0).UTC()
	}
	p.CreatedAt = time.Unix(createdAt, 0).UTC()
	p.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	return p, nil
}

func (s *PostStore) getOne(ctx context.Context, query string, args ...any) (Post, error) {
	p, err := scanPost(s.db.QueryRowContext(ctx, query, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return Post{}, ErrNotFound
	}
	return p, err
}

func (s *PostStore) list(ctx context.Context, query string, args ...any) ([]Post, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var posts []Post
	for rows.Next() {
		p, err := scanPost(rows)
		if err != nil {
			return nil, err
		}
		posts = append(posts, p)
	}
	return posts, rows.Err()
}

func (s *PostStore) execOne(ctx context.Context, query string, args ...any) error {
	res, err := s.db.ExecContext(ctx, query, args...)
	return store.AffectedOne(res, err, ErrNotFound)
}
