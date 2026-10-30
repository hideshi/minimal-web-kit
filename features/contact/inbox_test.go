package contact

import (
	"context"
	"database/sql"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"minimal-web-kit/features/auth/authtest"
	"minimal-web-kit/internal/store"
)

const day = 24 * time.Hour

func addRow(t *testing.T, db *sql.DB, status string, created, activity time.Time) int64 {
	t.Helper()
	res, err := db.Exec("INSERT INTO contact_messages (body, status, last_activity_at, created_at) VALUES ('x', ?, ?, ?)", status, activity.Unix(), created.Unix())
	if err != nil {
		t.Fatal(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// Purge は状態ごとの削除の時期で消し、画面に出す DeleteAt と同じ時刻で判定すること。消える1秒前は残り、ちょうどで消える。
// 不変条件: CONTACT-INV-10
func TestPurge_FollowsDeleteAt(t *testing.T) {
	f, _, core, _, clock := setup(t)
	now := *clock
	base, limit := 90*day, 365*day
	second := time.Second
	cases := []struct {
		name             string
		status           string
		created, updated time.Duration // 今からどれだけ前か
		deleted          bool
	}{
		{"未読のちょうど", "unread", base, base, true},
		{"未読の1秒前", "unread", base - second, base - second, false},
		{"未読はメモの更新で延びない", "unread", base, 0, true},
		{"完了のちょうど", "done", base, base, true},
		{"完了の1秒前", "done", base - second, 0, false},
		{"完了は更新で延びない", "done", base, 0, true},
		{"スパムのちょうど", "spam", base, base, true},
		{"対応中は更新から数える", "active", base + day, day, false},
		{"対応中の更新からちょうど", "active", base + day, base, true},
		{"対応中の更新から1秒前", "active", base + day, base - second, false},
		{"対応中の上限のちょうど", "active", limit, 0, true},
		{"対応中の上限の1秒前", "active", limit - second, 0, false},
	}
	ids := make([]int64, len(cases))
	for i, c := range cases {
		ids[i] = addRow(t, core.DB(), c.status, now.Add(-c.created), now.Add(-c.updated))
	}
	for i, c := range cases {
		m, err := f.store.Get(context.Background(), ids[i])
		if err != nil {
			t.Fatal(err)
		}
		if got := !f.cfg.Retention.DeleteAt(m).After(now); got != c.deleted {
			t.Errorf("%s: DeleteAt: 消える=%v", c.name, got)
		}
	}
	if err := f.Purge(context.Background()); err != nil {
		t.Fatal(err)
	}
	for i, c := range cases {
		_, err := f.store.Get(context.Background(), ids[i])
		if deleted := err == ErrNotFound; deleted != c.deleted {
			t.Errorf("%s: Purge: 消えた=%v (%v)", c.name, deleted, err)
		}
	}
}

// 受付から保持期間を過ぎた対応中のものは、完了・未読・スパムのどれに変えても、次の Purge で消えること。
// 対応中のままなら残ること。
// 不変条件: CONTACT-INV-10
func TestPurge_LeavingActiveAfterRetention(t *testing.T) {
	for _, status := range []string{statusDone, statusUnread, statusSpam} {
		f, _, core, _, clock := setup(t)
		id := addRow(t, core.DB(), statusActive, clock.Add(-100*day), clock.Add(-day))
		if err := f.Purge(context.Background()); err != nil {
			t.Fatal(err)
		}
		if _, err := f.store.Get(context.Background(), id); err != nil {
			t.Fatalf("対応中は残る: %v", err)
		}
		if err := f.store.SetStatus(context.Background(), id, status); err != nil {
			t.Fatal(err)
		}
		if err := f.Purge(context.Background()); err != nil {
			t.Fatal(err)
		}
		if _, err := f.store.Get(context.Background(), id); err != ErrNotFound {
			t.Fatalf("%s: 消えていない: %v", status, err)
		}
	}
}

// 最終更新が進むのは、対応中のもののメモの保存と、対応中への変更だけであること。
// 不変条件: CONTACT-INV-11
func TestActivity_OnlyActiveExtends(t *testing.T) {
	f, _, core, _, clock := setup(t)
	ctx := context.Background()
	created := clock.Add(-10 * day)
	activity := func(id int64) time.Time {
		t.Helper()
		m, err := f.store.Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return m.LastActivityAt
	}
	for _, status := range []string{statusUnread, statusDone, statusSpam} {
		id := addRow(t, core.DB(), status, created, created)
		if err := f.store.SetNote(ctx, id, "メモ"); err != nil {
			t.Fatal(err)
		}
		if !activity(id).Equal(created) {
			t.Fatalf("%s: メモで延びた", status)
		}
		// 対応中以外への変更でも進まない
		if err := f.store.SetStatus(ctx, id, statusDone); err != nil {
			t.Fatal(err)
		}
		if !activity(id).Equal(created) {
			t.Fatalf("%s: 状態の変更で延びた", status)
		}
		if err := f.store.SetStatus(ctx, id, statusActive); err != nil {
			t.Fatal(err)
		}
		if activity(id).Unix() != clock.Unix() {
			t.Fatalf("%s: 対応中への変更で進まない", status)
		}
	}
	id := addRow(t, core.DB(), statusActive, created, created)
	// 対応中のものへ同じ状態を送っても進まない
	if err := f.store.SetStatus(ctx, id, statusActive); err != nil {
		t.Fatal(err)
	}
	if !activity(id).Equal(created) {
		t.Fatal("同じ状態の再設定で延びた")
	}
	if err := f.store.SetNote(ctx, id, "電話した"); err != nil {
		t.Fatal(err)
	}
	if activity(id).Unix() != clock.Unix() {
		t.Fatal("対応中のメモの保存で進まない")
	}
}

func TestDeleteSoon(t *testing.T) {
	for left, want := range map[time.Duration]string{
		8 * day:             "",
		7*day + time.Second: "",
		7 * day:             "あと7日",
		3*day - time.Hour:   "あと3日",
		time.Hour:           "あと1日",
		0:                   "あと1日",
		-time.Hour:          "あと1日",
	} {
		if got := deleteSoon(left); got != want {
			t.Errorf("%v: %q, want %q", left, got, want)
		}
	}
}

// legacySchema は、状態に既読があり、メモと最終更新の列が無い、旧形式の表です。
const legacySchema = `CREATE TABLE contact_messages (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL DEFAULT '',
    reply_to TEXT NOT NULL DEFAULT '',
    body TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'unread' CHECK (status IN ('unread', 'read', 'spam')),
    created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_contact_messages_created ON contact_messages(created_at);`

func openDB(t *testing.T) *sql.DB {
	t.Helper()
	s, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s.DB()
}

func columns(t *testing.T, db *sql.DB) string {
	t.Helper()
	rows, err := db.Query("SELECT name, type, \"notnull\", COALESCE(dflt_value, '') FROM pragma_table_info('contact_messages') ORDER BY name")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var name, kind, def string
		var notNull int
		if err := rows.Scan(&name, &kind, &notNull, &def); err != nil {
			t.Fatal(err)
		}
		b.WriteString(name + " " + kind + " " + def + " " + string(rune('0'+notNull)) + "\n")
	}
	return b.String()
}

// 移行で、既読は完了になり、最終更新は受付と同じで削除の時期が延びず、採番も巻き戻らず、列は新規のDBと同じになること。
// 2回起動しても変わらないこと。
// 不変条件: CONTACT-INV-12
func TestMigrate_FromLegacy(t *testing.T) {
	db := openDB(t)
	if _, err := db.Exec(legacySchema); err != nil {
		t.Fatal(err)
	}
	created := time.Now().Add(-10 * day).Unix()
	for _, status := range []string{"unread", "read", "spam", "read"} {
		if _, err := db.Exec("INSERT INTO contact_messages (name, reply_to, body, status, created_at) VALUES ('名', '先', '本文', ?, ?)", status, created); err != nil {
			t.Fatal(err)
		}
	}
	// 最新の行を消す。採番は 4 のまま、行の最大の id は 3 になる
	if _, err := db.Exec("DELETE FROM contact_messages WHERE id = 4"); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := migrate(db); err != nil {
			t.Fatal(err)
		}
	}

	fresh := openDB(t)
	if err := migrate(fresh); err != nil {
		t.Fatal(err)
	}
	if got, want := columns(t, db), columns(t, fresh); got != want {
		t.Fatalf("列が新規のDBと違う:\n%s\nwant:\n%s", got, want)
	}
	s := NewStore(db)
	want := map[int64]string{1: statusUnread, 2: statusDone, 3: statusSpam}
	for id, status := range want {
		m, err := s.Get(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if m.Status != status || m.Note != "" || m.Body != "本文" || m.Name != "名" || m.ReplyTo != "先" {
			t.Fatalf("id %d: %+v", id, m)
		}
		if m.LastActivityAt.Unix() != created || m.CreatedAt.Unix() != created {
			t.Fatalf("id %d: 移行で時刻が変わった", id)
		}
	}
	id, err := s.Save(context.Background(), Input{Body: "移行の後"}, Limits{Hourly: 10, Daily: 10, Unread: 10})
	if err != nil {
		t.Fatal(err)
	}
	if id != 5 {
		t.Fatalf("採番が巻き戻った: %d", id)
	}
	var legacy, index int
	_ = db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE name = ?", legacyTable).Scan(&legacy)
	_ = db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_contact_messages_created' AND tbl_name = 'contact_messages'").Scan(&index)
	if legacy != 0 || index != 1 {
		t.Fatalf("legacy=%d index=%d", legacy, index)
	}
}

// 1件も受けていない旧形式の表でも移行できること。
// 不変条件: CONTACT-INV-12
func TestMigrate_EmptyLegacy(t *testing.T) {
	db := openDB(t)
	if _, err := db.Exec(legacySchema); err != nil {
		t.Fatal(err)
	}
	if err := migrate(db); err != nil {
		t.Fatal(err)
	}
	id, err := NewStore(db).Save(context.Background(), Input{Body: "最初"}, Limits{Hourly: 10, Daily: 10, Unread: 10})
	if err != nil || id != 1 {
		t.Fatalf("id=%d err=%v", id, err)
	}
}

// 途中で失敗したら元のままで、移行を起動の失敗として返すこと。
// 不変条件: CONTACT-INV-12
func TestMigrate_FailureLeavesTheTable(t *testing.T) {
	db := openDB(t)
	// CHECK 制約の無い旧形式の表に、新しい表へ写せない値を置く
	if _, err := db.Exec(strings.Replace(legacySchema, "CHECK (status IN ('unread', 'read', 'spam'))", "", 1)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO contact_messages (body, status, created_at) VALUES ('残す', 'broken', 1)"); err != nil {
		t.Fatal(err)
	}
	if err := migrate(db); err == nil {
		t.Fatal("must fail")
	}
	var body string
	if err := db.QueryRow("SELECT body FROM contact_messages").Scan(&body); err != nil || body != "残す" {
		t.Fatalf("元に戻っていない: %q %v", body, err)
	}
	var legacy, column int
	_ = db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE name = ?", legacyTable).Scan(&legacy)
	_ = db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('contact_messages') WHERE name = 'last_activity_at'").Scan(&column)
	if legacy != 0 || column != 0 {
		t.Fatalf("legacy=%d column=%d", legacy, column)
	}
}

// 作業用の表が残っていたら、触らずに起動を止めること。
// 不変条件: CONTACT-INV-12
func TestMigrate_StopsOnLeftover(t *testing.T) {
	db := openDB(t)
	if _, err := db.Exec("CREATE TABLE " + legacyTable + " (id INTEGER)"); err != nil {
		t.Fatal(err)
	}
	if err := migrate(db); err == nil || strings.Contains(err.Error(), "SELECT") {
		t.Fatalf("must stop: %v", err)
	}
}

// 既定の一覧は未読と対応中だけで、全件の画面はすべて出し、どちらも管理者だけが見られること。
// 空の文と、全件の画面のページ送りも確かめる。
// 不変条件: CONTACT-INV-3
// ユースケース: UC-CONTACT-3
func TestAdmin_ListViews(t *testing.T) {
	f, routes, core, _, clock := setup(t)
	admin := authtest.NewSessionWithRole(t, core.DB(), "admin")
	member := authtest.NewSessionWithRole(t, core.DB(), "member")
	if body := adminRequest(routes, http.MethodGet, "/admin/contact", admin, nil).Body.String(); !strings.Contains(body, "未読と対応中のお問い合わせはまだありません。") {
		t.Fatal("empty open list")
	}
	limits := Limits{Hourly: 100, Daily: 100, Unread: 100}
	for body, status := range map[string]string{"本文未読": statusUnread, "本文対応": statusActive, "本文完了": statusDone, "本文迷惑": statusSpam} {
		id, err := f.store.Save(context.Background(), Input{Body: body}, limits)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.store.SetStatus(context.Background(), id, status); err != nil {
			t.Fatal(err)
		}
	}
	open := adminRequest(routes, http.MethodGet, "/admin/contact", admin, nil).Body.String()
	if !strings.Contains(open, "本文未読") || !strings.Contains(open, "本文対応") || strings.Contains(open, "本文完了") || strings.Contains(open, "本文迷惑") {
		t.Fatalf("open list: %s", open)
	}
	all := adminRequest(routes, http.MethodGet, "/admin/contact/all", admin, nil)
	for _, want := range []string{"本文未読", "本文対応", "本文完了", "本文迷惑"} {
		if all.Code != http.StatusOK || !strings.Contains(all.Body.String(), want) {
			t.Fatalf("all list must show %s: %d", want, all.Code)
		}
	}
	if w := adminRequest(routes, http.MethodGet, "/admin/contact/all", member, nil); w.Code == http.StatusOK {
		t.Fatal("member must not see the list")
	}
	if w := adminRequest(routes, http.MethodGet, "/admin/contact/all?page=abc", admin, nil); w.Code != http.StatusBadRequest {
		t.Fatalf("bad page: %d", w.Code)
	}
	for range 21 {
		if _, err := f.store.Save(context.Background(), Input{Body: "x"}, Limits{Hourly: 100, Daily: 100, Unread: 100}); err != nil {
			t.Fatal(err)
		}
	}
	if body := adminRequest(routes, http.MethodGet, "/admin/contact/all", admin, nil).Body.String(); !strings.Contains(body, `href="/admin/contact/all?page=2"`) {
		t.Fatal("the all list must keep its path in the pagination")
	}
	// 削除が近いものは、状態の列に残りの日数を添える
	id := addRow(t, core.DB(), statusActive, clock.Add(-100*day), clock.Add(-87*day))
	if body := adminRequest(routes, http.MethodGet, "/admin/contact", admin, nil).Body.String(); !strings.Contains(body, "対応中（あと3日）") {
		t.Fatalf("delete soon (id %d)", id)
	}
}

// メモは管理者だけが保存でき、開いただけでは状態もメモも最終更新も変わらず、中身がログに出ないこと。
// 長すぎるメモは、書いた内容を残して出し直すこと。
// 不変条件: CONTACT-INV-3, CONTACT-INV-6, CONTACT-INV-7
// ユースケース: UC-CONTACT-3
func TestAdmin_Note(t *testing.T) {
	f, routes, core, logs, clock := setup(t)
	admin := authtest.NewSessionWithRole(t, core.DB(), "admin")
	created := clock.Add(-100 * day)
	id := addRow(t, core.DB(), statusActive, created, clock.Add(-day))
	path := "/admin/contact/" + itoa(id)
	before, _ := f.store.Get(context.Background(), id)
	page := adminRequest(routes, http.MethodGet, path, admin, nil).Body.String()
	if !strings.Contains(page, "対応中以外にすると、1時間以内に削除されます") {
		t.Fatal("the warning is missing")
	}
	if after, _ := f.store.Get(context.Background(), id); after != before {
		t.Fatal("GET must not change the message")
	}
	token := authtest.CSRFToken(t, routes, path, admin)
	const canary = "メモカナリア"
	if w := adminRequest(routes, http.MethodPost, path+"/note", admin, url.Values{"note": {canary}}); w.Code == http.StatusSeeOther {
		t.Fatal("the note requires a CSRF token")
	}
	if w := adminRequest(routes, http.MethodPost, path+"/note", admin, url.Values{"note": {canary}, "csrf_token": {token}}); w.Code != http.StatusSeeOther {
		t.Fatalf("save: %d", w.Code)
	}
	if m, _ := f.store.Get(context.Background(), id); m.Note != canary {
		t.Fatalf("note: %q", m.Note)
	}
	if body := adminRequest(routes, http.MethodGet, path, admin, nil).Body.String(); !strings.Contains(body, canary) {
		t.Fatal("the note must be shown")
	}
	tooLong := canary + strings.Repeat("あ", maxNoteRunes)
	w := adminRequest(routes, http.MethodPost, path+"/note", admin, url.Values{"note": {tooLong}, "csrf_token": {token}})
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), tooLong) || !strings.Contains(w.Body.String(), "2,000文字以内にして") {
		t.Fatalf("too long: %d", w.Code)
	}
	if m, _ := f.store.Get(context.Background(), id); m.Note != canary {
		t.Fatal("a rejected note must not be saved")
	}
	// 値がログを出す処理まで届いたうえで、中身が出ていないこと
	if !strings.Contains(logs.String(), "contact note saved") {
		t.Fatal("the save must reach the log")
	}
	if strings.Contains(logs.String(), canary) {
		t.Fatal("the note leaked into the log")
	}
	// 空にして保存すると、メモを消せる
	if w := adminRequest(routes, http.MethodPost, path+"/note", admin, url.Values{"note": {""}, "csrf_token": {token}}); w.Code != http.StatusSeeOther {
		t.Fatalf("clear: %d", w.Code)
	}
	if m, _ := f.store.Get(context.Background(), id); m.Note != "" {
		t.Fatal("the note must be cleared")
	}
}

// メンバーは、自分の有効なCSRFトークンを送っても、受信箱の書き込みの経路を使えないこと。
// トークンは管理者のものを使い回さない（CSRFの検査で先に止まり、権限の検査を確かめられないため）。
// 不変条件: CONTACT-INV-3
func TestAdmin_MemberCannotWrite(t *testing.T) {
	f, routes, core, _, clock := setup(t)
	member := authtest.NewSessionWithRole(t, core.DB(), "member")
	id := addRow(t, core.DB(), statusSpam, *clock, *clock)
	path := "/admin/contact/" + itoa(id)
	token := authtest.CSRFToken(t, routes, "/admin/passkeys", member)
	for _, c := range []struct {
		path   string
		values url.Values
	}{
		{path + "/note", url.Values{"note": {"書けない"}}},
		{path + "/status", url.Values{"status": {statusDone}}},
		{path + "/delete", url.Values{}},
		{"/admin/contact/spam/delete", url.Values{}},
	} {
		c.values.Set("csrf_token", token)
		if w := adminRequest(routes, http.MethodPost, c.path, member, c.values); w.Code != http.StatusForbidden {
			t.Errorf("%s: %d", c.path, w.Code)
		}
	}
	m, err := f.store.Get(context.Background(), id)
	if err != nil || m.Note != "" || m.Status != statusSpam {
		t.Fatalf("the message changed: %+v %v", m, err)
	}
}

// 公開フォームとプライバシーポリシーの説明に、対応中の最長の期間とメモが載ること。
// 不変条件: CONTACT-INV-9
func TestRetention_ExplainedToSenders(t *testing.T) {
	f, routes, _, _, _ := setup(t)
	w, _ := getForm(t, routes)
	if !strings.Contains(w.Body.String(), "受付から最長365日まで残すことがあります") {
		t.Fatal("the form must show the longest period")
	}
	notices := f.PrivacyNotices()
	if len(notices) != 1 || len(notices[0].Rows) != 1 {
		t.Fatalf("notices: %+v", notices)
	}
	row := notices[0].Rows[0]
	if !strings.Contains(row.Retention, "受付から最長365日") || !strings.Contains(row.Items, "対応の記録") {
		t.Fatalf("privacy: %+v", row)
	}
}
