package contact

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"minimal-web-kit/internal/web/form"
)

const (
	maxBodyRunes    = 2000
	maxNameRunes    = 50
	maxReplyToRunes = 200
	maxLinks        = 2

	statusUnread = "unread"
	statusRead   = "read"
	statusSpam   = "spam"
)

var (
	ErrNotFound = errors.New("contact: not found")
	// ErrOverLimit は、全体の受付上限を超えたことを示します。
	ErrOverLimit = errors.New("contact: over limit")
)

// InputError は、どの欄の入力が誤りかを示します。Field は "body"・"links"・"name"・"reply_to" のどれかです。
type InputError struct{ Field string }

func (e InputError) Error() string { return "contact: invalid " + e.Field }

// Message はお問い合わせ1件です。
type Message struct {
	ID                  int64
	Name, ReplyTo, Body string
	Status              string
	CreatedAt           time.Time
}

func (Message) LogValue() slog.Value { return slog.StringValue("[contact message]") }

// DisplayName は、お名前が無ければ「（無記名）」を返します。
func (m Message) DisplayName() string {
	if m.Name == "" {
		return "（無記名）"
	}
	return m.Name
}

// Excerpt は内容の冒頭30文字です（一覧の表示用）。
func (m Message) Excerpt() string {
	s := strings.Join(strings.Fields(m.Body), " ")
	if utf8.RuneCountInString(s) <= 30 {
		return s
	}
	return string([]rune(s)[:30]) + "…"
}

// StatusLabel は状態の表示名です。
func (m Message) StatusLabel() string {
	switch m.Status {
	case statusRead:
		return "既読"
	case statusSpam:
		return "スパム"
	}
	return "未読"
}

// Input は送られた入力です。
type Input struct {
	Name, ReplyTo, Body string
}

func (Input) LogValue() slog.Value { return slog.StringValue("[contact input]") }

// countLinks は本文のURL（http://・https://・www.）の数を数えます。
func countLinks(body string) int {
	lower := strings.ToLower(body)
	n := strings.Count(lower, "http://") + strings.Count(lower, "https://")
	// "https://www." を二重に数えない
	n += strings.Count(lower, "www.") - strings.Count(lower, "://www.")
	return n
}

// normalize は入力を検査します。誤りは、欄を示す InputError で返します。
func normalize(in Input) (Input, error) {
	var err error
	if in.Body, err = form.Text(in.Body, maxBodyRunes, true); err != nil {
		return in, InputError{"body"}
	}
	if countLinks(in.Body) > maxLinks {
		return in, InputError{"links"}
	}
	if in.Name, err = form.Line(in.Name, maxNameRunes, false); err != nil {
		return in, InputError{"name"}
	}
	if in.ReplyTo, err = form.Line(in.ReplyTo, maxReplyToRunes, false); err != nil {
		return in, InputError{"reply_to"}
	}
	return in, nil
}

// Store はお問い合わせの読み書きを担当します。
type Store struct {
	db  *sql.DB
	now func() time.Time
}

func NewStore(db *sql.DB) *Store { return &Store{db: db, now: time.Now} }

// Save は入力を検査し、全体の受付上限を確かめてから保存します。判定と保存は1つのトランザクションで行います。
// 不変条件: CONTACT-INV-5
func (s *Store) Save(ctx context.Context, in Input, lim Limits) (int64, error) {
	in, err := normalize(in)
	if err != nil {
		return 0, err
	}
	c, err := s.db.Conn(ctx)
	if err != nil {
		return 0, err
	}
	defer c.Close()
	if _, err := c.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return 0, err
	}
	defer func() { _, _ = c.ExecContext(context.Background(), "ROLLBACK") }()
	now := s.now()
	var hourly, daily, unread int
	if err := c.QueryRowContext(ctx,
		"SELECT COUNT(CASE WHEN created_at > ? THEN 1 END), COUNT(CASE WHEN created_at > ? THEN 1 END), COUNT(CASE WHEN status = 'unread' THEN 1 END) FROM contact_messages",
		now.Add(-time.Hour).Unix(), now.Add(-24*time.Hour).Unix(),
	).Scan(&hourly, &daily, &unread); err != nil {
		return 0, err
	}
	if hourly >= lim.Hourly || daily >= lim.Daily || unread >= lim.Unread {
		return 0, ErrOverLimit
	}
	res, err := c.ExecContext(ctx, "INSERT INTO contact_messages (name, reply_to, body, created_at) VALUES (?, ?, ?, ?)", in.Name, in.ReplyTo, in.Body, now.Unix())
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if _, err := c.ExecContext(ctx, "COMMIT"); err != nil {
		return 0, err
	}
	return id, nil
}

func scanMessage(row interface{ Scan(...any) error }) (Message, error) {
	var m Message
	var created int64
	if err := row.Scan(&m.ID, &m.Name, &m.ReplyTo, &m.Body, &m.Status, &created); err != nil {
		return m, err
	}
	m.CreatedAt = time.Unix(created, 0)
	return m, nil
}

// List は新しい順に返します。
func (s *Store) List(ctx context.Context, limit, offset int) ([]Message, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id, name, reply_to, body, status, created_at FROM contact_messages ORDER BY id DESC LIMIT ? OFFSET ?", limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, m)
	}
	return result, rows.Err()
}

func (s *Store) Get(ctx context.Context, id int64) (Message, error) {
	m, err := scanMessage(s.db.QueryRowContext(ctx, "SELECT id, name, reply_to, body, status, created_at FROM contact_messages WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return m, ErrNotFound
	}
	return m, err
}

// Counts は未読とスパムの件数を返します。
func (s *Store) Counts(ctx context.Context) (unread, spam int, err error) {
	err = s.db.QueryRowContext(ctx, "SELECT COUNT(CASE WHEN status = 'unread' THEN 1 END), COUNT(CASE WHEN status = 'spam' THEN 1 END) FROM contact_messages").Scan(&unread, &spam)
	return unread, spam, err
}

func changed(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetStatus は状態を変えます。
func (s *Store) SetStatus(ctx context.Context, id int64, status string) error {
	switch status {
	case statusUnread, statusRead, statusSpam:
	default:
		return InputError{"status"}
	}
	return changed(s.db.ExecContext(ctx, "UPDATE contact_messages SET status = ? WHERE id = ?", status, id))
}

func (s *Store) Delete(ctx context.Context, id int64) error {
	return changed(s.db.ExecContext(ctx, "DELETE FROM contact_messages WHERE id = ?", id))
}

// DeleteSpam はスパムの印を付けたものをまとめて削除し、件数を返します。
func (s *Store) DeleteSpam(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, "DELETE FROM contact_messages WHERE status = 'spam'")
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Purge は保持期間を過ぎたものを、状態に関係なく削除します。
// 不変条件: CONTACT-INV-2
func (s *Store) Purge(ctx context.Context, retention time.Duration) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM contact_messages WHERE created_at <= ?", s.now().Add(-retention).Unix())
	return err
}
