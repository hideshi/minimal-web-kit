package contact

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"minimal-web-kit/features/auth/authtest"
)

const testKindOptions = "history=歴史|region=地域・防災|public-business=行政・ビジネス（自治体, 企業）|eq=A=B"

func setKind(t *testing.T, options, label, required string) {
	t.Helper()
	t.Setenv("CONTACT_KIND_OPTIONS", options)
	t.Setenv("CONTACT_KIND_LABEL", label)
	t.Setenv("CONTACT_KIND_REQUIRED", required)
}

func kindOf(t *testing.T, f *Feature, body string) string {
	t.Helper()
	var kind string
	if err := f.store.db.QueryRow("SELECT kind FROM contact_messages WHERE body = ?", body).Scan(&kind); err != nil {
		t.Fatal(err)
	}
	return kind
}

// 設定の書き方: 区切りは「|」と最初の「=」だけで、表示名の「、」「,」「（）」「=」は壊れない。誤りは値を出さずに拒否する。
// 不変条件: CONTACT-INV-14
func TestLoadKindConfig(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	k, err := loadKindConfig(env(nil))
	if err != nil || k.Enabled() {
		t.Fatalf("unset must be disabled: %+v %v", k, err)
	}
	k, err = loadKindConfig(env(map[string]string{"CONTACT_KIND_OPTIONS": " " + testKindOptions + " "}))
	if err != nil {
		t.Fatal(err)
	}
	want := []KindOption{{"history", "歴史"}, {"region", "地域・防災"}, {"public-business", "行政・ビジネス（自治体, 企業）"}, {"eq", "A=B"}}
	if len(k.Options) != len(want) || k.Label != defaultKindLabel || k.Required {
		t.Fatalf("parsed: %+v", k)
	}
	for i := range want {
		if k.Options[i] != want[i] {
			t.Fatalf("option %d: %+v", i, k.Options[i])
		}
	}
	k, err = loadKindConfig(env(map[string]string{"CONTACT_KIND_OPTIONS": "a=あ", "CONTACT_KIND_LABEL": "関心のある分野", "CONTACT_KIND_REQUIRED": "true"}))
	if err != nil || k.Label != "関心のある分野" || !k.Required {
		t.Fatalf("label and required: %+v %v", k, err)
	}
	var many []string
	for i := range 21 {
		many = append(many, "o"+strconv.Itoa(i)+"=選択肢")
	}
	for _, bad := range []map[string]string{
		{"CONTACT_KIND_LABEL": "カナリア見出し"},
		{"CONTACT_KIND_REQUIRED": "true"},
		{"CONTACT_KIND_OPTIONS": "カナリア"},
		{"CONTACT_KIND_OPTIONS": "History=歴史"},
		{"CONTACT_KIND_OPTIONS": "a b=歴史"},
		{"CONTACT_KIND_OPTIONS": "a=歴史|a=文化"},
		{"CONTACT_KIND_OPTIONS": "a=歴史||b=文化"},
		{"CONTACT_KIND_OPTIONS": "a="},
		{"CONTACT_KIND_OPTIONS": "a=" + strings.Repeat("あ", 41)},
		{"CONTACT_KIND_OPTIONS": "a=歴\t史"},
		{"CONTACT_KIND_OPTIONS": strings.Repeat("a", 33) + "=歴史"},
		{"CONTACT_KIND_OPTIONS": strings.Join(many, "|")},
		{"CONTACT_KIND_OPTIONS": "a=歴史", "CONTACT_KIND_REQUIRED": "yes"},
		{"CONTACT_KIND_OPTIONS": "a=歴史", "CONTACT_KIND_LABEL": strings.Repeat("あ", 41)},
	} {
		_, err := loadKindConfig(env(bad))
		if err == nil {
			t.Errorf("%v must be rejected", bad)
			continue
		}
		if strings.Contains(err.Error(), "カナリア") || strings.Contains(err.Error(), "歴史") {
			t.Errorf("error must not contain the value: %v", err)
		}
	}
	if _, err := LoadConfig(env(map[string]string{"CONTACT_KIND_REQUIRED": "true"})); err == nil {
		t.Error("LoadConfig must reject the kind settings without options")
	}
}

// 無効（既定）なら、フォーム・プライバシーの表示・受信箱に出さず、送られても読まずに空で保存する。
// 不変条件: CONTACT-INV-14
// ユースケース: UC-CONTACT-1
func TestKind_DisabledByDefault(t *testing.T) {
	setKind(t, "", "", "")
	f, routes, core, _, clock := setup(t)
	w, token := getForm(t, routes)
	if strings.Contains(w.Body.String(), `name="kind"`) {
		t.Fatal("the form must not show the kind when disabled")
	}
	*clock = clock.Add(5 * time.Second)
	values := valid(token, "無効の本文")
	values.Set("kind", "history")
	if res := submit(routes, values, "same-origin"); res.Code != http.StatusSeeOther {
		t.Fatalf("submit: %d", res.Code)
	}
	if got := kindOf(t, f, "無効の本文"); got != "" {
		t.Fatalf("kind must not be stored when disabled: %q", got)
	}
	for _, n := range f.PrivacyNotices() {
		for _, row := range n.Rows {
			if strings.Contains(row.Items, "種別") || strings.Contains(row.Items, "選択式") {
				t.Fatalf("privacy must not mention the kind: %s", row.Items)
			}
		}
	}
	admin := authtest.NewSession(t, core.DB())
	list := adminRequest(routes, http.MethodGet, "/admin/contact", admin, nil).Body.String()
	if strings.Contains(list, "お問い合わせの種別") || strings.Contains(list, "?kind=") {
		t.Fatal("the inbox must not show the kind when disabled")
	}
}

// 有効なら、任意のときは先頭に「選ばない」を置き、選んだ識別子を保存する。選択肢に無い値は保存せず、400で入力を返す。
// 不変条件: CONTACT-INV-8, CONTACT-INV-14
// ユースケース: UC-CONTACT-1
func TestKind_SubmitOptional(t *testing.T) {
	setKind(t, testKindOptions, "関心のある分野", "")
	f, routes, core, _, clock := setup(t)
	w, token := getForm(t, routes)
	body := w.Body.String()
	first := strings.Index(body, `value="" checked> 選ばない`)
	if first < 0 || first > strings.Index(body, `value="history"`) || !strings.Contains(body, "<legend>関心のある分野</legend>") ||
		!strings.Contains(body, "行政・ビジネス（自治体, 企業）") || strings.Contains(body, `name="kind" value="history" required`) {
		t.Fatalf("form must offer 選ばない first and the configured options:\n%s", body)
	}
	*clock = clock.Add(5 * time.Second)

	values := valid(token, "選ばない本文")
	if res := submit(routes, values, "same-origin"); res.Code != http.StatusSeeOther {
		t.Fatalf("no choice: %d", res.Code)
	}
	if got := kindOf(t, f, "選ばない本文"); got != "" {
		t.Fatalf("no choice must be empty: %q", got)
	}

	_, token = getForm(t, routes)
	*clock = clock.Add(5 * time.Second)
	values = valid(token, "地域の本文")
	values.Set("kind", "region")
	if res := submit(routes, values, "same-origin"); res.Code != http.StatusSeeOther {
		t.Fatalf("region: %d", res.Code)
	}
	if got := kindOf(t, f, "地域の本文"); got != "region" {
		t.Fatalf("the id must be stored: %q", got)
	}

	before := count(t, core)
	_, token = getForm(t, routes)
	*clock = clock.Add(5 * time.Second)
	for _, bad := range []string{"地域・防災", "unknown", "History"} {
		values = valid(token, "不正の本文カナリア")
		values.Set("kind", bad)
		res := submit(routes, values, "same-origin")
		if res.Code != http.StatusBadRequest || !strings.Contains(res.Body.String(), "関心のある分野は、選択肢から選んでください。") ||
			!strings.Contains(res.Body.String(), "不正の本文カナリア") {
			t.Fatalf("%q must be rejected with the input kept: %d", bad, res.Code)
		}
	}
	if count(t, core) != before {
		t.Fatal("an invalid kind must not be saved")
	}

	notices := f.PrivacyNotices()
	if !strings.HasSuffix(notices[0].Rows[0].Items, "、関心のある分野（選択式・任意）") {
		t.Fatalf("privacy must name the kind with its label: %s", notices[0].Rows[0].Items)
	}
}

// 必須なら「選ばない」を置かず、未選択は400で入力を返す。
// 不変条件: CONTACT-INV-14
func TestKind_SubmitRequired(t *testing.T) {
	setKind(t, testKindOptions, "", "true")
	f, routes, core, _, clock := setup(t)
	w, token := getForm(t, routes)
	if strings.Contains(w.Body.String(), "選ばない") || !strings.Contains(w.Body.String(), `name="kind" value="history" required`) ||
		!strings.Contains(w.Body.String(), "<legend>お問い合わせの種別<span class=\"field-mark\">（必須）</span></legend>") {
		t.Fatalf("required form:\n%s", w.Body.String())
	}
	*clock = clock.Add(5 * time.Second)
	res := submit(routes, valid(token, "未選択の本文"), "same-origin")
	if res.Code != http.StatusBadRequest || count(t, core) != 0 {
		t.Fatalf("missing required kind: %d", res.Code)
	}
	values := valid(token, "必須の本文")
	values.Set("kind", "history")
	if res := submit(routes, values, "same-origin"); res.Code != http.StatusSeeOther {
		t.Fatalf("submit: %d", res.Code)
	}
	if kindOf(t, f, "必須の本文") != "history" {
		t.Fatal("not saved")
	}
	if !strings.HasSuffix(f.PrivacyNotices()[0].Rows[0].Items, "、お問い合わせの種別（選択式・必須）") {
		t.Fatal("privacy must say the kind is required")
	}
}

// 受信箱の一覧と詳細に、今の設定の表示名を出し、種別で絞り込める。設定から外した識別子は、消さずにそのまま示す。
// 表示名を直しても、保存した識別子のまま新しい表示名で出る。
// 不変条件: CONTACT-INV-3, CONTACT-INV-14
// ユースケース: UC-CONTACT-4
func TestKind_Inbox(t *testing.T) {
	setKind(t, testKindOptions, "関心のある分野", "")
	f, routes, core, _, _ := setup(t)
	lim := Limits{Hourly: 10, Daily: 10, Unread: 10}
	ids := map[string]int64{}
	for kind, body := range map[string]string{"history": "歴史の本文", "region": "地域の本文", "": "選ばない本文", "retired": "外した本文"} {
		id, err := f.store.Save(context.Background(), Input{Body: body, Kind: kind}, lim)
		if err != nil {
			t.Fatal(err)
		}
		ids[kind] = id
	}
	admin := authtest.NewSession(t, core.DB())
	list := adminRequest(routes, http.MethodGet, "/admin/contact", admin, nil).Body.String()
	for _, want := range []string{`<th scope="col">関心のある分野</th>`, "<td>歴史</td>", "<td>地域・防災</td>", "<td>（選ばない）</td>", "<td>設定に無い値（retired）</td>",
		`<a href="/admin/contact?kind=region">地域・防災</a>`, `<a href="/admin/contact" aria-current="page">すべて</a>`} {
		if !strings.Contains(list, want) {
			t.Errorf("list lacks %q", want)
		}
	}
	filtered := adminRequest(routes, http.MethodGet, "/admin/contact?kind=region", admin, nil).Body.String()
	if !strings.Contains(filtered, "地域の本文") || strings.Contains(filtered, "歴史の本文") || strings.Contains(filtered, "選ばない本文") ||
		!strings.Contains(filtered, `<a href="/admin/contact?kind=region" aria-current="page">`) || !strings.Contains(filtered, `href="/admin/contact/all?kind=region"`) {
		t.Fatal("the inbox must filter by kind and keep the filter")
	}
	all := adminRequest(routes, http.MethodGet, "/admin/contact/all?kind=history", admin, nil).Body.String()
	if !strings.Contains(all, "歴史の本文") || strings.Contains(all, "地域の本文") {
		t.Fatal("the full list must filter by kind")
	}
	// 選択肢に無い値では絞り込まない
	unknown := adminRequest(routes, http.MethodGet, "/admin/contact?kind=retired", admin, nil).Body.String()
	if !strings.Contains(unknown, "歴史の本文") || !strings.Contains(unknown, "地域の本文") {
		t.Fatal("an unknown filter must show everything")
	}
	detail := adminRequest(routes, http.MethodGet, "/admin/contact/"+itoa(ids["region"]), admin, nil).Body.String()
	if !strings.Contains(detail, `<tr><th scope="row">関心のある分野</th><td>地域・防災</td></tr>`) {
		t.Fatal("the detail must show the kind label")
	}
	// 表示名を直しても、過去の記録は識別子で引いた新しい表示名で出る
	f.cfg.Kind.Options[1].Label = "地域と防災"
	detail = adminRequest(routes, http.MethodGet, "/admin/contact/"+itoa(ids["region"]), admin, nil).Body.String()
	if !strings.Contains(detail, "<td>地域と防災</td>") {
		t.Fatal("a renamed label must apply to past messages")
	}
	if w := adminRequest(routes, http.MethodGet, "/admin/contact?kind=region", authtest.NewSessionWithRole(t, core.DB(), "site_admin"), nil); w.Code != http.StatusForbidden {
		t.Fatalf("members must not filter the inbox: %d", w.Code)
	}
}

// 種別の値はログに出さない（固定の分類だけ）。
// 不変条件: CONTACT-INV-6
func TestKind_NotLogged(t *testing.T) {
	setKind(t, testKindOptions, "", "")
	_, routes, _, logs, clock := setup(t)
	_, token := getForm(t, routes)
	*clock = clock.Add(5 * time.Second)
	values := valid(token, "本文")
	values.Set("kind", "kindcanary")
	submit(routes, values, "same-origin")
	if strings.Contains(logs.String(), "kindcanary") || !strings.Contains(logs.String(), "result=invalid_kind") {
		t.Fatalf("logs: %s", logs.String())
	}
}

// previousSchema は、種別の列を足す前の版（0e41c85）の migration.sql の表です。
const previousSchema = `CREATE TABLE IF NOT EXISTS contact_messages (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL DEFAULT '',
    reply_to TEXT NOT NULL DEFAULT '',
    body TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'unread' CHECK (status IN ('unread', 'active', 'done', 'spam')),
    note TEXT NOT NULL DEFAULT '',
    last_activity_at INTEGER NOT NULL,
    created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_contact_messages_created ON contact_messages(created_at);`

// 前の版のDBに種別の列を空文字の既定で足し、既存の行は空のまま残す。表は作り直さず（移行前の複製も作らない）、
// 2回起動しても変わらず、新規のDBと同じ列になる。前の版の実行ファイルに戻しても、その書き込み・読み出し・起動の処理が動く。
// 不変条件: CONTACT-INV-15
func TestMigrate_AddsKindColumn(t *testing.T) {
	db := openDB(t)
	if _, err := db.Exec(previousSchema); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO contact_messages (name, reply_to, body, last_activity_at, created_at) VALUES ('', '', '前の版の本文', 1, 1)"); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := migrate(db, slog.Default()); err != nil {
			t.Fatal(err)
		}
	}
	fresh := openDB(t)
	if err := migrate(fresh, slog.Default()); err != nil {
		t.Fatal(err)
	}
	if got, want := columns(t, db), columns(t, fresh); got != want || !strings.Contains(got, "kind TEXT '' 1") {
		t.Fatalf("columns differ:\n%s\nwant:\n%s", got, want)
	}
	if files := preMigrationFiles(t, db); len(files) != 0 {
		t.Fatalf("adding a column must not rebuild the table: %v", files)
	}
	var kind string
	if err := db.QueryRow("SELECT kind FROM contact_messages WHERE body = '前の版の本文'").Scan(&kind); err != nil || kind != "" {
		t.Fatalf("existing rows must keep an empty kind: %q %v", kind, err)
	}

	// 前の版の実行ファイルの処理（起動時の表の作成、保存、一覧の読み出し）
	if _, err := db.Exec(previousSchema); err != nil {
		t.Fatalf("the previous schema must be a no-op: %v", err)
	}
	if _, err := db.Exec("INSERT INTO contact_messages (name, reply_to, body, last_activity_at, created_at) VALUES (?, ?, ?, ?, ?)", "", "", "戻した版の本文", 2, 2); err != nil {
		t.Fatalf("the previous insert must work: %v", err)
	}
	rows, err := db.Query("SELECT id, name, reply_to, body, status, note, last_activity_at, created_at FROM contact_messages ORDER BY id DESC")
	if err != nil {
		t.Fatalf("the previous select must work: %v", err)
	}
	rows.Close()
	if err := db.QueryRow("SELECT kind FROM contact_messages WHERE body = '戻した版の本文'").Scan(&kind); err != nil || kind != "" {
		t.Fatalf("rows from the previous version must have an empty kind: %q %v", kind, err)
	}
	// 戻した後に、もう一度新しい版で起動しても変わらない
	if err := migrate(db, slog.Default()); err != nil {
		t.Fatal(err)
	}
}
