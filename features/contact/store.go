package contact

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"minimal-web-kit/internal/store"
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
	maxNoteRunes    = 2000

	statusUnread = "unread"
	statusActive = "active"
	statusDone   = "done"
	statusSpam   = "spam"
)

var (
	ErrNotFound = errors.New("contact: not found")
	// ErrOverLimit は、全体の受付上限を超えたことを示します。
	ErrOverLimit = errors.New("contact: over limit")
)

// InputError は、どの欄の入力が誤りかを示します。Field は "body"・"links"・"name"・"reply_to"・"note"・"status" のどれかです。
type InputError struct{ Field string }

func (e InputError) Error() string { return "contact: invalid " + e.Field }

// Message はお問い合わせ1件です。
type Message struct {
	ID                  int64
	Name, ReplyTo, Body string
	Status              string
	Note                string // 運営者のメモ
	CreatedAt           time.Time
	LastActivityAt      time.Time // 対応中の削除の時期の起点
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
	case statusActive:
		return "対応中"
	case statusDone:
		return "完了"
	case statusSpam:
		return "スパム"
	}
	return "未読"
}

// Retention は削除の時期の決まりです。Base は保持期間、ActiveMax は対応中のものを残す上限（どちらも受付からの長さ）です。
type Retention struct {
	Base, ActiveMax time.Duration
}

// DeleteAt は、そのお問い合わせが削除される時刻を返します。Purge の条件と同じ計算です。
// 不変条件: CONTACT-INV-10
func (p Retention) DeleteAt(m Message) time.Time {
	if m.Status != statusActive {
		return m.CreatedAt.Add(p.Base)
	}
	at := m.LastActivityAt.Add(p.Base)
	if limit := m.CreatedAt.Add(p.ActiveMax); limit.Before(at) {
		return limit
	}
	return at
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
	res, err := c.ExecContext(ctx, "INSERT INTO contact_messages (name, reply_to, body, last_activity_at, created_at) VALUES (?, ?, ?, ?, ?)", in.Name, in.ReplyTo, in.Body, now.Unix(), now.Unix())
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

const messageColumns = "id, name, reply_to, body, status, note, last_activity_at, created_at"

func scanMessage(row interface{ Scan(...any) error }) (Message, error) {
	var m Message
	var activity, created int64
	if err := row.Scan(&m.ID, &m.Name, &m.ReplyTo, &m.Body, &m.Status, &m.Note, &activity, &created); err != nil {
		return m, err
	}
	m.LastActivityAt = time.Unix(activity, 0)
	m.CreatedAt = time.Unix(created, 0)
	return m, nil
}

// List は新しい順に返します。openOnly なら、未読と対応中だけを返します。
func (s *Store) List(ctx context.Context, openOnly bool, limit, offset int) ([]Message, error) {
	where := ""
	if openOnly {
		where = " WHERE status IN ('unread', 'active')"
	}
	rows, err := s.db.QueryContext(ctx, "SELECT "+messageColumns+" FROM contact_messages"+where+" ORDER BY id DESC LIMIT ? OFFSET ?", limit, offset)
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
	m, err := scanMessage(s.db.QueryRowContext(ctx, "SELECT "+messageColumns+" FROM contact_messages WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return m, ErrNotFound
	}
	return m, err
}

// Counts は未読・対応中・スパムの件数を返します。
func (s *Store) Counts(ctx context.Context) (unread, active, spam int, err error) {
	err = s.db.QueryRowContext(ctx, "SELECT COUNT(CASE WHEN status = 'unread' THEN 1 END), COUNT(CASE WHEN status = 'active' THEN 1 END), COUNT(CASE WHEN status = 'spam' THEN 1 END) FROM contact_messages").Scan(&unread, &active, &spam)
	return unread, active, spam, err
}

func changed(res sql.Result, err error) error {
	return store.AffectedOne(res, err, ErrNotFound)
}

// SetStatus は状態を変えます。対応中へ変えたときだけ、最終更新を今にします。
// 不変条件: CONTACT-INV-11
func (s *Store) SetStatus(ctx context.Context, id int64, status string) error {
	switch status {
	case statusUnread, statusActive, statusDone, statusSpam:
	default:
		return InputError{"status"}
	}
	return changed(s.db.ExecContext(ctx,
		"UPDATE contact_messages SET last_activity_at = CASE WHEN ? = 'active' AND status <> 'active' THEN ? ELSE last_activity_at END, status = ? WHERE id = ?",
		status, s.now().Unix(), status, id))
}

// SetNote はメモを保存します。対応中のものだけ、最終更新を今にします。
// 不変条件: CONTACT-INV-11
func (s *Store) SetNote(ctx context.Context, id int64, note string) error {
	note, err := form.Text(note, maxNoteRunes, false)
	if err != nil {
		return InputError{"note"}
	}
	return changed(s.db.ExecContext(ctx,
		"UPDATE contact_messages SET note = ?, last_activity_at = CASE WHEN status = 'active' THEN ? ELSE last_activity_at END WHERE id = ?",
		note, s.now().Unix(), id))
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

// Purge は削除の時期を過ぎたものを削除します。条件は Retention.DeleteAt と同じです。
// 不変条件: CONTACT-INV-10
func (s *Store) Purge(ctx context.Context, p Retention) error {
	now := s.now()
	base, limit := now.Add(-p.Base).Unix(), now.Add(-p.ActiveMax).Unix()
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM contact_messages
		 WHERE (status <> 'active' AND created_at <= ?)
		    OR (status = 'active' AND (last_activity_at <= ? OR created_at <= ?))`,
		base, base, limit)
	return err
}
