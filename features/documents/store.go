package documents

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"strings"
	"time"

	"minimal-web-kit/features"
	"minimal-web-kit/internal/web/form"
	"minimal-web-kit/internal/web/page"
	"minimal-web-kit/internal/web/richtext"
)

const (
	maxTitleRunes  = 200
	maxBodyRunes   = 50000
	maxFolderRunes = 50
	maxReasonRunes = 200

	audiencePrivate  = "private"
	audienceEveryone = "everyone"
	audienceSelected = "selected"

	stateDraft     = "draft"
	statePending   = "pending"
	stateRejected  = "rejected"
	stateWithdrawn = "withdrawn"
	statePublished = "published"
)

var (
	// ErrNotFound は、文書が無いか、読む権限が無いことを表します。どちらかは区別しません。
	ErrNotFound = errors.New("documents: not found")
	// ErrConflict は、画面を開いた後に状態が変わっていて、操作が当たらないことを表します。何も変えていません。
	ErrConflict = errors.New("documents: state changed")
)

// InputError は、どの欄の入力が誤りかを示します。
type InputError struct{ Field string }

func (e InputError) Error() string { return "documents: invalid " + e.Field }

// Input は文書の作成・編集の入力です。
type Input struct {
	Title, Body string
	FolderID    int64
}

func (Input) LogValue() slog.Value { return slog.StringValue("[document input]") }

func normalize(in Input) (Input, error) {
	title, err := form.Line(in.Title, maxTitleRunes, true)
	if err != nil {
		return in, InputError{"title"}
	}
	body, err := form.Text(in.Body, maxBodyRunes, false)
	if err != nil {
		return in, InputError{"body"}
	}
	if _, err := richtext.Parse(body); err != nil {
		return in, InputError{"body_limit"}
	}
	in.Title, in.Body = title, body
	return in, nil
}

// Document は文書の本体です。
type Document struct {
	ID        int64
	OwnerID   int64
	FolderID  int64 // 0ならフォルダなし
	Audience  string
	CurrentID int64 // 0なら未公開
}

// Version は文書の版です。
type Version struct {
	ID, DocumentID int64
	Number         int
	Title, Body    string
	State          string
	CreatedAt      time.Time
	RequestedAt    time.Time
	PublishedAt    time.Time
}

func (Version) LogValue() slog.Value { return slog.StringValue("[document version]") }

// StateLabel は画面に出す版の状態です。
func (v Version) StateLabel() string {
	switch v.State {
	case stateDraft:
		return "下書き"
	case statePending:
		return "承認待ち"
	case stateRejected:
		return "差し戻し"
	case stateWithdrawn:
		return "取り下げ"
	}
	return "公開"
}

// Item は一覧の1行です。
type Item struct {
	ID          int64
	OwnerID     int64
	Title       string
	FolderName  string
	State       string
	Unread      bool
	Published   bool
	UpdatedAt   time.Time
	VersionID   int64
	RequestedAt time.Time
}

func (Item) LogValue() slog.Value { return slog.StringValue("[document item]") }

// StateLabel は一覧に出す状態です。
func (i Item) StateLabel() string { return Version{State: i.State}.StateLabel() }

// Store は文書の保存先です。読む関数は、すべて閲覧者のアカウントIDを受け取ります。
type Store struct {
	db  *sql.DB
	now func() time.Time
}

func NewStore(db *sql.DB, now func() time.Time) *Store { return &Store{db: db, now: now} }

// querier は、接続とDBのどちらからでも読むための口です。
type querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// atomic は、書き込みの権利を先に取ってから fn を実行します（BEGIN IMMEDIATE）。
// 読んでから書く処理が、途中で別の書き込みに割り込まれないようにします。
func (s *Store) atomic(ctx context.Context, fn func(c *sql.Conn) error) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_, _ = conn.ExecContext(cleanup, "ROLLBACK")
		}
	}()
	if err := fn(conn); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return err
	}
	committed = true
	return nil
}

// snapshot は、1つの読み取りの取引の中で fn を実行します。権限の判定と本文の取得の間に、
// 移管や共有の解除、下書きの更新が割り込まないようにします（判定の後の内容を返さない）。
func (s *Store) snapshot(ctx context.Context, fn func(q querier) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	return fn(tx)
}

// readsCurrent は、閲覧者（?）が今の版を読める条件です。文書の別名は d。一覧・検索・閲覧の全てがこの条件を使います。
// 不変条件: DOC-INV-2、DOC-INV-4
const readsCurrent = `d.current_version_id IS NOT NULL AND (d.owner_account_id = ? OR d.audience = 'everyone'
	OR (d.audience = 'selected' AND EXISTS (SELECT 1 FROM document_shares s WHERE s.document_id = d.id AND s.account_id = ?)))`

const documentColumns = "d.id, d.owner_account_id, COALESCE(d.folder_id, 0), d.audience, COALESCE(d.current_version_id, 0)"

func scanDocument(row interface{ Scan(...any) error }) (Document, error) {
	var d Document
	err := row.Scan(&d.ID, &d.OwnerID, &d.FolderID, &d.Audience, &d.CurrentID)
	if errors.Is(err, sql.ErrNoRows) {
		return d, ErrNotFound
	}
	return d, err
}

const versionColumns = "v.id, v.document_id, v.number, v.title, v.body, v.state, v.created_at, COALESCE(v.requested_at, 0), COALESCE(v.published_at, 0)"

func scanVersion(row interface{ Scan(...any) error }) (Version, error) {
	var v Version
	var created, requested, published int64
	err := row.Scan(&v.ID, &v.DocumentID, &v.Number, &v.Title, &v.Body, &v.State, &created, &requested, &published)
	if errors.Is(err, sql.ErrNoRows) {
		return v, ErrNotFound
	}
	v.CreatedAt, v.RequestedAt, v.PublishedAt = timeOf(created), timeOf(requested), timeOf(published)
	return v, err
}

func timeOf(unix int64) time.Time {
	if unix == 0 {
		return time.Time{}
	}
	return time.Unix(unix, 0).In(time.Local)
}

// owned は、所有者の文書を返します。他の人の文書は ErrNotFound です。
func (s *Store) owned(ctx context.Context, q querier, owner, id int64) (Document, error) {
	return scanDocument(q.QueryRowContext(ctx, "SELECT "+documentColumns+" FROM documents d WHERE d.id = ? AND d.owner_account_id = ?", id, owner))
}

// Owned は、所有者の文書を返します。不変条件: DOC-INV-3
func (s *Store) Owned(ctx context.Context, owner, id int64) (Document, error) {
	return s.owned(ctx, s.db, owner, id)
}

// Current は、閲覧者が読める今の版を返します。読めなければ ErrNotFound です。
// 不変条件: DOC-INV-2、DOC-INV-4
func (s *Store) Current(ctx context.Context, viewer, id int64) (d Document, v Version, err error) {
	err = s.snapshot(ctx, func(q querier) error {
		d, v, err = current(ctx, q, viewer, id)
		return err
	})
	return d, v, err
}

func current(ctx context.Context, q querier, viewer, id int64) (Document, Version, error) {
	d, err := scanDocument(q.QueryRowContext(ctx, "SELECT "+documentColumns+" FROM documents d WHERE d.id = ? AND "+readsCurrent, id, viewer, viewer))
	if err != nil {
		return d, Version{}, err
	}
	v, err := scanVersion(q.QueryRowContext(ctx, "SELECT "+versionColumns+" FROM document_versions v WHERE v.id = ?", d.CurrentID))
	return d, v, err
}

// VersionFor は、閲覧者が読める版を返します。判定は版ごとに行います。
// 所有者は全ての版、共有先は今の版、承認者は承認待ちか判断を記録した版を読めます。
// 不変条件: DOC-INV-2
func (s *Store) VersionFor(ctx context.Context, viewer, id, versionID int64) (d Document, v Version, err error) {
	err = s.snapshot(ctx, func(q querier) error {
		d, v, err = versionFor(ctx, q, viewer, id, versionID)
		return err
	})
	return d, v, err
}

func versionFor(ctx context.Context, q querier, viewer, id, versionID int64) (Document, Version, error) {
	d, err := scanDocument(q.QueryRowContext(ctx, "SELECT "+documentColumns+" FROM documents d WHERE d.id = ?", id))
	if err != nil {
		return d, Version{}, err
	}
	v, err := scanVersion(q.QueryRowContext(ctx, "SELECT "+versionColumns+" FROM document_versions v WHERE v.id = ? AND v.document_id = ?", versionID, id))
	if err != nil {
		return d, v, err
	}
	if d.OwnerID == viewer {
		return d, v, nil
	}
	var ok bool
	err = q.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM document_approvers a WHERE a.version_id = ?1 AND a.account_id = ?2
		AND (a.decision IS NOT NULL OR ?3 = 'pending'))`, versionID, viewer, v.State).Scan(&ok)
	if err != nil {
		return d, v, err
	}
	if ok {
		return d, v, nil
	}
	if d.CurrentID == versionID {
		if _, cur, err := current(ctx, q, viewer, id); err == nil {
			return d, cur, nil
		} else if !errors.Is(err, ErrNotFound) {
			return d, v, err
		}
	}
	return d, Version{}, ErrNotFound
}

// Draft は、所有者の下書きを返します。無ければ ErrNotFound です。
func (s *Store) Draft(ctx context.Context, owner, id int64) (d Document, v Version, err error) {
	err = s.snapshot(ctx, func(q querier) error {
		if d, err = s.owned(ctx, q, owner, id); err != nil {
			return err
		}
		v, err = scanVersion(q.QueryRowContext(ctx, "SELECT "+versionColumns+" FROM document_versions v WHERE v.document_id = ? AND v.state = 'draft'", id))
		return err
	})
	return d, v, err
}

// mcpState は、確認の時点の文書の状態のハッシュです。所有者、下書き、フォルダ、共有の範囲を含みます。
func mcpState(ctx context.Context, q querier, d Document) (string, error) {
	v, err := scanVersion(q.QueryRowContext(ctx, "SELECT "+versionColumns+" FROM document_versions v WHERE v.document_id = ? AND v.state IN ('draft', 'pending')", d.ID))
	if err != nil && !errors.Is(err, ErrNotFound) {
		return "", err
	}
	rows, err := q.QueryContext(ctx, "SELECT account_id FROM document_shares WHERE document_id = ? ORDER BY account_id", d.ID)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	shares := []int64{}
	for rows.Next() {
		var a int64
		if err := rows.Scan(&a); err != nil {
			return "", err
		}
		shares = append(shares, a)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	return features.MCPState(d.OwnerID, d.FolderID, d.Audience, d.CurrentID, shares, v.ID, v.Title, v.Body, v.State), nil
}

// MCPTarget は、所有者の文書の、確認に使う情報と状態のハッシュを返します。
func (s *Store) MCPTarget(ctx context.Context, owner, id int64) (d Document, draft Version, shares int, state string, err error) {
	err = s.snapshot(ctx, func(q querier) error {
		if d, err = s.owned(ctx, q, owner, id); err != nil {
			return err
		}
		draft, err = scanVersion(q.QueryRowContext(ctx, "SELECT "+versionColumns+" FROM document_versions v WHERE v.document_id = ? AND v.state = 'draft'", id))
		if err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		if err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM document_shares WHERE document_id = ?", id).Scan(&shares); err != nil {
			return err
		}
		state, err = mcpState(ctx, q, d)
		return err
	})
	return d, draft, shares, state, err
}

// checkState は、保存と同じ取引の中で、確認の時点から文書が変わっていないことを確かめます。expect が空なら確かめません。
// 不変条件: MCP-INV-4
func (s *Store) checkState(ctx context.Context, c *sql.Conn, owner, id int64, expect string) error {
	if expect == "" {
		return nil
	}
	d, err := s.owned(ctx, c, owner, id)
	if errors.Is(err, ErrNotFound) {
		return features.ErrMCPConflict
	} else if err != nil {
		return err
	}
	state, err := mcpState(ctx, c, d)
	if err != nil {
		return err
	}
	if state != expect {
		return features.ErrMCPConflict
	}
	return nil
}

// Open は、所有者の下書きか承認待ちの版を返します。無ければゼロ値です。
func (s *Store) Open(ctx context.Context, owner, id int64) (Version, error) {
	if _, err := s.Owned(ctx, owner, id); err != nil {
		return Version{}, err
	}
	v, err := scanVersion(s.db.QueryRowContext(ctx, "SELECT "+versionColumns+" FROM document_versions v WHERE v.document_id = ? AND v.state IN ('draft', 'pending')", id))
	if errors.Is(err, ErrNotFound) {
		return Version{}, nil
	}
	return v, err
}

// Versions は、所有者が見る版の一覧を、新しい順に返します。
func (s *Store) Versions(ctx context.Context, owner, id int64) ([]Version, error) {
	if _, err := s.Owned(ctx, owner, id); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, "SELECT "+versionColumns+" FROM document_versions v WHERE v.document_id = ? ORDER BY v.number DESC", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []Version
	for rows.Next() {
		v, err := scanVersion(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, v)
	}
	return list, rows.Err()
}

// Filter は読める文書の一覧の絞り込みです。
type Filter struct {
	FolderID int64
	Query    string
}

// likeEscape は LIKE の特殊な文字を文字として扱います。
var likeEscape = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// List は、閲覧者が読める文書の今の版を、新しい順に返します。検索もこの関数を通ります。
// 不変条件: DOC-INV-2
func (s *Store) List(ctx context.Context, viewer int64, f Filter, p page.Page) ([]Item, error) {
	q := `SELECT d.id, d.owner_account_id, v.title, COALESCE(fo.name, ''), v.state, COALESCE(v.published_at, 0), v.id,
		d.owner_account_id != ? AND NOT EXISTS (SELECT 1 FROM document_reads r WHERE r.version_id = v.id AND r.account_id = ?)
		FROM documents d JOIN document_versions v ON v.id = d.current_version_id
		LEFT JOIN document_folders fo ON fo.id = d.folder_id
		WHERE ` + readsCurrent
	args := []any{viewer, viewer, viewer, viewer}
	if f.FolderID > 0 {
		q += " AND d.folder_id = ?"
		args = append(args, f.FolderID)
	}
	if f.Query != "" {
		like := "%" + likeEscape.Replace(f.Query) + "%"
		q += ` AND (v.title LIKE ? ESCAPE '\' OR v.body LIKE ? ESCAPE '\')`
		args = append(args, like, like)
	}
	q += " ORDER BY v.published_at DESC, d.id DESC LIMIT ? OFFSET ?"
	args = append(args, p.Limit(), p.Offset())
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []Item
	for rows.Next() {
		var it Item
		var at int64
		if err := rows.Scan(&it.ID, &it.OwnerID, &it.Title, &it.FolderName, &it.State, &at, &it.VersionID, &it.Unread); err != nil {
			return nil, err
		}
		it.UpdatedAt, it.Published = timeOf(at), true
		list = append(list, it)
	}
	return list, rows.Err()
}

// Mine は、自分の文書を、最新の版のタイトルと状態で返します。
func (s *Store) Mine(ctx context.Context, owner int64, p page.Page) ([]Item, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT d.id, v.title, COALESCE(fo.name, ''), v.state, d.updated_at, v.id, d.current_version_id IS NOT NULL
		FROM documents d
		JOIN document_versions v ON v.id = (SELECT MAX(x.id) FROM document_versions x WHERE x.document_id = d.id)
		LEFT JOIN document_folders fo ON fo.id = d.folder_id
		WHERE d.owner_account_id = ? ORDER BY d.updated_at DESC, d.id DESC LIMIT ? OFFSET ?`, owner, p.Limit(), p.Offset())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []Item
	for rows.Next() {
		var it Item
		var at int64
		if err := rows.Scan(&it.ID, &it.Title, &it.FolderName, &it.State, &at, &it.VersionID, &it.Published); err != nil {
			return nil, err
		}
		it.OwnerID, it.UpdatedAt = owner, timeOf(at)
		list = append(list, it)
	}
	return list, rows.Err()
}

// Approvals は、閲覧者の判断を待っている版を返します。
func (s *Store) Approvals(ctx context.Context, viewer int64) ([]Item, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT d.id, d.owner_account_id, v.title, v.id, COALESCE(v.requested_at, 0)
		FROM document_approvers a JOIN document_versions v ON v.id = a.version_id JOIN documents d ON d.id = v.document_id
		WHERE a.account_id = ? AND a.decision IS NULL AND v.state = 'pending' ORDER BY v.requested_at, v.id`, viewer)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []Item
	for rows.Next() {
		var it Item
		var at int64
		if err := rows.Scan(&it.ID, &it.OwnerID, &it.Title, &it.VersionID, &at); err != nil {
			return nil, err
		}
		it.RequestedAt = timeOf(at)
		list = append(list, it)
	}
	return list, rows.Err()
}

// Approver は版の承認者1人の状態です。
type Approver struct {
	AccountID int64
	Decision  string
	Reason    string
	DecidedAt time.Time
}

func (Approver) LogValue() slog.Value { return slog.StringValue("[approver]") }

// Approvers は版の承認者を返します。呼び出す側が、その版を読めることを確かめます。
func (s *Store) Approvers(ctx context.Context, versionID int64) ([]Approver, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT account_id, COALESCE(decision, ''), reason, COALESCE(decided_at, 0) FROM document_approvers WHERE version_id = ? ORDER BY account_id", versionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []Approver
	for rows.Next() {
		var a Approver
		var at int64
		if err := rows.Scan(&a.AccountID, &a.Decision, &a.Reason, &at); err != nil {
			return nil, err
		}
		a.DecidedAt = timeOf(at)
		list = append(list, a)
	}
	return list, rows.Err()
}

// Shares は指定の共有先を返します。不変条件: DOC-INV-3
func (s *Store) Shares(ctx context.Context, owner, id int64) ([]int64, error) {
	if _, err := s.Owned(ctx, owner, id); err != nil {
		return nil, err
	}
	return s.int64s(ctx, "SELECT account_id FROM document_shares WHERE document_id = ? ORDER BY account_id", id)
}

// Reads は、今の版の既読をアカウントごとに返します。不変条件: DOC-INV-3
func (s *Store) Reads(ctx context.Context, owner, id int64) (map[int64]time.Time, error) {
	d, err := s.Owned(ctx, owner, id)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, "SELECT account_id, first_read_at FROM document_reads WHERE version_id = ?", d.CurrentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]time.Time{}
	for rows.Next() {
		var id, at int64
		if err := rows.Scan(&id, &at); err != nil {
			return nil, err
		}
		out[id] = timeOf(at)
	}
	return out, rows.Err()
}

func (s *Store) int64s(ctx context.Context, q string, args ...any) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var v int64
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// RecordRead は、今の版を閲覧者が開いたことを記録します。所有者は記録しません。2回目以降は最初の日時を残します。
// 不変条件: DOC-INV-8
func (s *Store) RecordRead(ctx context.Context, d Document, viewer int64) error {
	if d.OwnerID == viewer || d.CurrentID == 0 {
		return nil
	}
	_, err := s.db.ExecContext(ctx, "INSERT OR IGNORE INTO document_reads (version_id, account_id, first_read_at) VALUES (?, ?, ?)", d.CurrentID, viewer, s.now().Unix())
	return err
}

// Folder はフォルダです。Used は入っている文書があるかです。
type Folder struct {
	ID        int64
	Name      string
	CreatedBy int64
	Used      bool
}

func (Folder) LogValue() slog.Value { return slog.StringValue("[document folder]") }

// Folders はフォルダを名前の順に返します。
func (s *Store) Folders(ctx context.Context) ([]Folder, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT f.id, f.name, f.created_by, EXISTS (SELECT 1 FROM documents d WHERE d.folder_id = f.id)
		FROM document_folders f ORDER BY f.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []Folder
	for rows.Next() {
		var f Folder
		if err := rows.Scan(&f.ID, &f.Name, &f.CreatedBy, &f.Used); err != nil {
			return nil, err
		}
		list = append(list, f)
	}
	return list, rows.Err()
}

// Folder は1件を返します。
func (s *Store) Folder(ctx context.Context, id int64) (Folder, error) {
	var f Folder
	err := s.db.QueryRowContext(ctx, `SELECT f.id, f.name, f.created_by, EXISTS (SELECT 1 FROM documents d WHERE d.folder_id = f.id)
		FROM document_folders f WHERE f.id = ?`, id).Scan(&f.ID, &f.Name, &f.CreatedBy, &f.Used)
	if errors.Is(err, sql.ErrNoRows) {
		return f, ErrNotFound
	}
	return f, err
}

// OwnerCount は、所有者ごとの文書の件数です。移管の画面に出す情報は、これだけです。
type OwnerCount struct {
	OwnerID int64
	Count   int
}

// OwnerCounts は、所有者ごとの件数を返します。タイトルと本文は読みません。
// 不変条件: DOC-INV-9
func (s *Store) OwnerCounts(ctx context.Context) ([]OwnerCount, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT owner_account_id, COUNT(*) FROM documents GROUP BY owner_account_id ORDER BY owner_account_id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []OwnerCount
	for rows.Next() {
		var o OwnerCount
		if err := rows.Scan(&o.OwnerID, &o.Count); err != nil {
			return nil, err
		}
		list = append(list, o)
	}
	return list, rows.Err()
}

// Transfer は移管の記録の1行です。
type Transfer struct {
	From, To, By int64
	Count        int
	At           time.Time
}

// Transfers は移管の記録を新しい順に返します。
func (s *Store) Transfers(ctx context.Context) ([]Transfer, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT from_account_id, to_account_id, by_account_id, count, transferred_at FROM document_transfers ORDER BY id DESC LIMIT 100")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []Transfer
	for rows.Next() {
		var t Transfer
		var at int64
		if err := rows.Scan(&t.From, &t.To, &t.By, &t.Count, &at); err != nil {
			return nil, err
		}
		t.At = timeOf(at)
		list = append(list, t)
	}
	return list, rows.Err()
}
