package test

import (
	"bytes"
	"database/sql"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"minimal-web-kit/features"
	"minimal-web-kit/features/auth"
	"minimal-web-kit/features/auth/authtest"
	"minimal-web-kit/features/records"
	"minimal-web-kit/internal/app"
	"minimal-web-kit/internal/store"
)

// recordsEnv は、管理者認証と業務記録だけを登録した構成です。経路・認証・CSRF・SQL・DBは全部本物を通ります。
type recordsEnv struct {
	t      *testing.T
	routes http.Handler
	db     *sql.DB
	who    map[string]*http.Cookie
	ids    map[string]int64
	csrf   map[string]string
	logs   *bytes.Buffer
}

// 権限の型ごとに1つの種類を用意する
func recordsTestDefs() []records.RecordDef {
	mk := func(name string, a records.RecordAccess) records.RecordDef {
		return records.RecordDef{
			Name: name, Title: name, Purpose: "試験", Retention: "消すまで", Access: a, MCP: true,
			Steps: []records.RecordStep{
				{Version: 1, Add: &records.Add{Field: "note", Type: records.Text}},
				{Version: 2, Add: &records.Add{Field: "day", Type: records.Date}},
				{Version: 3, Add: &records.Add{Field: "state", Type: records.Select}},
				{Version: 4, Add: &records.Add{Field: "qty", Type: records.Integer}},
			},
			Fields: []records.RecordField{
				{Name: "note", Label: "メモ", Required: true, MaxLen: 20},
				{Name: "day", Label: "日付"},
				{Name: "state", Label: "状態", Options: []string{"未", "済"}},
				{Name: "qty", Label: "数"},
			},
			ListFields: []string{"note", "day"},
			SortBy:     "day",
		}
	}
	return []records.RecordDef{
		mk("only", records.OwnerOnly), mk("adminread", records.OwnerWriteAdminRead),
		mk("allread", records.OwnerWriteAllRead), mk("shared", records.AllReadWrite),
	}
}

func newRecordsEnv(t *testing.T) *recordsEnv {
	t.Helper()
	return newRecordsEnvWith(t, filepath.Join(t.TempDir(), "app.db"), recordsTestDefs())
}

// newRecordsEnvWith は、DBのファイルと定義を指定して構成を作ります（版の行き来の試験に使う）。
func newRecordsEnvWith(t *testing.T, dbPath string, defs []records.RecordDef) *recordsEnv {
	t.Helper()
	t.Setenv("AUTH_ORIGIN", "")
	t.Setenv("AUTH_RP_ID", "")
	core, err := store.NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { core.Close() })
	logs := &bytes.Buffer{}
	logger := slog.New(slog.NewTextHandler(logs, nil))
	reg := features.NewRegistry()
	reg.Register(auth.NewFeature(logger))
	reg.Register(records.NewFeature(logger, defs, nil))
	for _, f := range reg.All() {
		if err := f.Init(core.DB()); err != nil {
			t.Fatal(err)
		}
	}
	routes, err := app.BuildRoutes(core, reg, false, logger)
	if err != nil {
		t.Fatal(err)
	}
	e := &recordsEnv{t: t, routes: routes, db: core.DB(), who: map[string]*http.Cookie{}, ids: map[string]int64{}, csrf: map[string]string{}, logs: logs}
	for name, role := range map[string]string{"owner": "user", "other": "user", "site": "site_admin", "admin": "admin"} {
		c, id := authtest.NewAccountSession(t, e.db, role)
		e.who[name], e.ids[name] = c, id
		e.csrf[name] = authtest.CSRFToken(t, routes, "/admin/passkeys", c)
	}
	return e
}

func (e *recordsEnv) get(who, path string) *httptest.ResponseRecorder {
	e.t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if c := e.who[who]; c != nil {
		req.AddCookie(c)
	}
	w := httptest.NewRecorder()
	e.routes.ServeHTTP(w, req)
	return w
}

func (e *recordsEnv) post(who, path string, form url.Values) *httptest.ResponseRecorder {
	e.t.Helper()
	if form == nil {
		form = url.Values{}
	}
	form.Set("csrf_token", e.csrf[who])
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("Origin", "http://localhost:8080")
	if c := e.who[who]; c != nil {
		req.AddCookie(c)
	}
	w := httptest.NewRecorder()
	e.routes.ServeHTTP(w, req)
	return w
}

// create は、who として記録を作り、その番号を返します。
func (e *recordsEnv) create(who, kind, note string) int64 {
	e.t.Helper()
	w := e.post(who, "/admin/records/"+kind, url.Values{"f_note": {note}, "f_day": {"2026-11-06"}, "f_state": {"未"}, "f_qty": {"3"}})
	if w.Code != http.StatusSeeOther {
		e.t.Fatalf("create %s as %s: %d %s", kind, who, w.Code, w.Body.String())
	}
	loc := w.Header().Get("Location")
	id, err := strconv.ParseInt(strings.Split(strings.TrimPrefix(loc, "/admin/records/"+kind+"/"), "?")[0], 10, 64)
	if err != nil {
		e.t.Fatalf("location %q", loc)
	}
	return id
}

func (e *recordsEnv) revision(kind string, id int64) string {
	var rev int64
	if err := e.db.QueryRow(`SELECT revision FROM "records_`+kind+`" WHERE id = ?`, id).Scan(&rev); err != nil {
		e.t.Fatal(err)
	}
	return strconv.FormatInt(rev, 10)
}

// TestRecords_AccessMatrix は、権限の4つの型ごとに、本人・他の一般ユーザー・サイト管理者・システム管理者が、
// 一覧・詳細・編集・削除でできることが、ドメインモデルの7節の表のとおりであることを確かめます。
// 読めない記録は、無い記録と同じ404になります。
// 不変条件: RECORDS-INV-3, RECORDS-INV-4
// ユースケース: UC-RECORDS-1, UC-RECORDS-2, UC-RECORDS-3, UC-RECORDS-4
func TestRecords_AccessMatrix(t *testing.T) {
	type can struct{ read, edit, del bool }
	want := map[string]map[string]can{
		"only":      {"owner": {true, true, true}, "other": {}, "site": {}, "admin": {}},
		"adminread": {"owner": {true, true, true}, "other": {}, "site": {}, "admin": {true, false, true}},
		"allread":   {"owner": {true, true, true}, "other": {true, false, false}, "site": {true, false, false}, "admin": {true, false, true}},
		"shared":    {"owner": {true, true, true}, "other": {true, true, true}, "site": {true, true, true}, "admin": {true, true, true}},
	}
	e := newRecordsEnv(t)
	for kind, rows := range want {
		for who, c := range rows {
			id := e.create("owner", kind, "秘密のメモ-"+kind)
			path := "/admin/records/" + kind + "/" + strconv.FormatInt(id, 10)
			show := e.get(who, path)
			if c.read != (show.Code == http.StatusOK) {
				t.Errorf("%s/%s read: %d", kind, who, show.Code)
			}
			if !c.read {
				missing := e.get(who, "/admin/records/"+kind+"/999999")
				if show.Code != http.StatusNotFound || show.Body.String() != missing.Body.String() {
					t.Errorf("%s/%s: unreadable (%d) must look like missing (%d)", kind, who, show.Code, missing.Code)
				}
			}
			list := e.get(who, "/admin/records/"+kind)
			if list.Code != http.StatusOK || c.read != strings.Contains(list.Body.String(), "秘密のメモ-"+kind) {
				t.Errorf("%s/%s list: %d, shows=%v", kind, who, list.Code, strings.Contains(list.Body.String(), "秘密のメモ"))
			}
			edit := e.post(who, path+"/edit", url.Values{"revision": {e.revision(kind, id)}, "f_note": {"直した"}, "f_state": {"済"}})
			if c.edit != (edit.Code == http.StatusSeeOther) {
				t.Errorf("%s/%s edit: %d", kind, who, edit.Code)
			}
			del := e.post(who, path+"/delete?revision="+e.revision(kind, id), nil)
			if c.del != (del.Code == http.StatusSeeOther) {
				t.Errorf("%s/%s delete: %d", kind, who, del.Code)
			}
		}
	}
	// ログインしていない人は、どの画面にも入れない
	if w := e.get("", "/admin/records/shared"); w.Code == http.StatusOK {
		t.Error("anonymous reached the list")
	}
}

// TestRecords_RevisionConflict は、同じ秒の中の2回の保存でも、開いた時の版の数が古い保存と削除を止め、何も変えないことを確かめます。
// 不変条件: RECORDS-INV-5
func TestRecords_RevisionConflict(t *testing.T) {
	e := newRecordsEnv(t)
	id := e.create("owner", "shared", "最初")
	path := "/admin/records/shared/" + strconv.FormatInt(id, 10)
	if w := e.post("owner", path+"/edit", url.Values{"revision": {"1"}, "f_note": {"Aの変更"}}); w.Code != http.StatusSeeOther {
		t.Fatalf("first save: %d", w.Code)
	}
	w := e.post("other", path+"/edit", url.Values{"revision": {"1"}, "f_note": {"Bの古い変更"}})
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "開き直して") {
		t.Fatalf("stale save: %d", w.Code)
	}
	if w := e.post("other", path+"/delete?revision=1", nil); w.Code != http.StatusSeeOther || !strings.Contains(w.Header().Get("Location"), "conflict") {
		t.Fatalf("stale delete: %d %s", w.Code, w.Header().Get("Location"))
	}
	var note string
	e.db.QueryRow(`SELECT f_note FROM records_shared WHERE id = ?`, id).Scan(&note)
	if note != "Aの変更" {
		t.Errorf("note = %q", note)
	}
}

// TestRecords_InputValidation は、型ごとの入力の検査と、エラーの時に入力を保ったまま戻すことを確かめます。
// 不変条件: RECORDS-INV-6
func TestRecords_InputValidation(t *testing.T) {
	e := newRecordsEnv(t)
	base := url.Values{"f_note": {"ok"}, "f_day": {"2026-11-06"}, "f_state": {"未"}, "f_qty": {"1"}}
	cases := map[string][2]string{
		"必須が空":     {"f_note", " "},
		"上限ちょうど+1": {"f_note", strings.Repeat("あ", 21)},
		"改行":       {"f_note", "a\nb"},
		"実在しない日付":  {"f_day", "2026-02-30"},
		"日付の形":     {"f_day", "2026/11/06"},
		"選択肢に無い":   {"f_state", "途中"},
		"整数でない":    {"f_qty", "1.5"},
		"先頭の0":     {"f_qty", "007"},
	}
	for name, c := range cases {
		form := url.Values{}
		for k, v := range base {
			form[k] = v
		}
		form.Set(c[0], c[1])
		w := e.post("owner", "/admin/records/shared", form)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", name, w.Code)
		}
	}
	form := url.Values{}
	for k, v := range base {
		form[k] = v
	}
	form.Set("f_note", strings.Repeat("あ", 20))
	if w := e.post("owner", "/admin/records/shared", form); w.Code != http.StatusSeeOther {
		t.Errorf("upper bound exactly: %d", w.Code)
	}
}

// TestRecords_DeletedOwnerPurge は、削除済みのアカウントの記録を、システム管理者だけが、件数だけを見てまとめて消せること、
// 停止中・有効なアカウントの記録と、全員で読み書きする種類は対象にならないことを確かめます。
// 不変条件: RECORDS-INV-10
// ユースケース: UC-RECORDS-5
func TestRecords_DeletedOwnerPurge(t *testing.T) {
	e := newRecordsEnv(t)
	now := int64(1)
	ins := func(kind string, owner int64, note string) {
		if _, err := e.db.Exec(`INSERT INTO "records_`+kind+`" (owner_account, revision, created_at, updated_at, f_note) VALUES (?, 1, ?, ?, ?)`, owner, now, now, note); err != nil {
			t.Fatal(err)
		}
	}
	const gone = 999001
	ins("allread", gone, "辞めた人の本文-SECRET")
	ins("allread", gone, "辞めた人の本文2")
	ins("shared", gone, "共有の台帳")
	ins("allread", e.ids["other"], "有効な人の記録")

	if w := e.get("site", "/admin/records/deleted-owners"); w.Code != http.StatusForbidden {
		t.Errorf("site admin reached the purge list: %d", w.Code)
	}
	w := e.get("admin", "/admin/records/deleted-owners")
	body := w.Body.String()
	if w.Code != http.StatusOK || !strings.Contains(body, "2件") || strings.Contains(body, "SECRET") || strings.Contains(body, "/shared/") {
		t.Fatalf("list: %d\n%s", w.Code, body)
	}
	for _, p := range []string{"/admin/records/deleted-owners/allread/" + strconv.FormatInt(e.ids["other"], 10) + "/delete", "/admin/records/deleted-owners/shared/999001/delete"} {
		if w := e.get("admin", p); w.Code != http.StatusNotFound {
			t.Errorf("%s: %d", p, w.Code)
		}
		if w := e.post("admin", p, nil); w.Code != http.StatusNotFound {
			t.Errorf("POST %s: %d", p, w.Code)
		}
	}
	if w := e.post("admin", "/admin/records/deleted-owners/allread/999001/delete", nil); w.Code != http.StatusSeeOther {
		t.Fatalf("purge: %d", w.Code)
	}
	var left, shared, alive int
	e.db.QueryRow(`SELECT COUNT(*) FROM records_allread WHERE owner_account = ?`, gone).Scan(&left)
	e.db.QueryRow(`SELECT COUNT(*) FROM records_shared`).Scan(&shared)
	e.db.QueryRow(`SELECT COUNT(*) FROM records_allread WHERE owner_account = ?`, e.ids["other"]).Scan(&alive)
	if left != 0 || shared != 1 || alive != 1 {
		t.Errorf("left=%d shared=%d alive=%d", left, shared, alive)
	}
}

// TestRecords_ValuesStayOutOfLogs は、記録の値と絞り込みの語が、成功・失敗のどちらの経路でもログに出ないことを確かめます。
// 不変条件: RECORDS-INV-13
func TestRecords_ValuesStayOutOfLogs(t *testing.T) {
	e := newRecordsEnv(t)
	const canary = "CNRY-7f3a" // 項目の上限（20字）に収め、入力の検査で止まらずに各経路まで届くようにする
	id := e.create("owner", "shared", canary)
	path := "/admin/records/shared/" + strconv.FormatInt(id, 10)
	// 衝突の経路（古い版の数）、入力の誤りの経路（長すぎる）、一覧の絞り込み（語・不正な期間・範囲の外の並び）
	if w := e.post("owner", path+"/edit", url.Values{"revision": {"9"}, "f_note": {canary + "-old"}}); w.Code != http.StatusConflict {
		t.Fatalf("the conflict path was not reached: %d", w.Code)
	}
	if w := e.post("owner", "/admin/records/shared", url.Values{"f_note": {canary + strings.Repeat("x", 30)}}); w.Code != http.StatusBadRequest {
		t.Fatalf("the invalid-input path was not reached: %d", w.Code)
	}
	if w := e.get("owner", "/admin/records/shared?q="+url.QueryEscape(canary)+"&from=bad&sort=99"); !strings.Contains(w.Body.String(), canary) {
		t.Fatal("the search did not reach the record")
	}
	if got := e.get("owner", path); !strings.Contains(got.Body.String(), canary) {
		t.Fatal("the canary did not reach the page")
	}
	if strings.Contains(e.logs.String(), "CNRY") {
		t.Errorf("logs contain a record value:\n%s", e.logs.String())
	}
}

// TestRecords_SearchTreatsWildcardsAsText は、語の % _ \ ' を文字として探し、並びの指定が範囲の外でも既定の並びにすることを確かめます。
// 不変条件: RECORDS-INV-7
func TestRecords_SearchTreatsWildcardsAsText(t *testing.T) {
	e := newRecordsEnv(t)
	for _, n := range []string{"50%引き", "500円引き", "a_b", "axb", `c\d`, "O'Neil"} {
		e.create("owner", "shared", n)
	}
	cases := map[string][]string{"50%": {"50%引き"}, "a_b": {"a_b"}, `c\d`: {`c\d`}, "O'Neil": {"O'Neil"}}
	for q, hits := range cases {
		body := e.get("owner", "/admin/records/shared?sort=-5&q="+url.QueryEscape(q)).Body.String()
		for _, n := range []string{"50%引き", "500円引き", "a_b", "axb", `c\d`, "O&#39;Neil"} {
			want := false
			for _, h := range hits {
				want = want || strings.ReplaceAll(h, "'", "&#39;") == n
			}
			if strings.Contains(body, ">"+n+"<") != want {
				t.Errorf("q=%q: %q shown=%v", q, n, !want)
			}
		}
	}
	if w := e.get("owner", "/admin/records/shared?sort=x';DROP"); w.Code != http.StatusOK {
		t.Errorf("bad sort: %d", w.Code)
	}
	if w := e.get("owner", "/admin/records/nothing"); w.Code != http.StatusNotFound {
		t.Errorf("unknown kind: %d", w.Code)
	}
	if w := e.get("owner", `/admin/records/shared";DROP`); w.Code != http.StatusNotFound {
		t.Errorf("bad kind: %d", w.Code)
	}
}

// TestRecords_OlderVersionKeepsNewerColumns は、項目を足した版から前の版へ実行ファイルだけ戻した時に、
// 前の版の画面には新しい列の値が出ず、前の版で保存・作成しても新しい列の値が残り、新しい版に戻すと見えることを確かめます。
// 不変条件: RECORDS-INV-12, RECORDS-INV-18
// ユースケース: UC-RECORDS-9
func TestRecords_OlderVersionKeepsNewerColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.db")
	older := recordsTestDefs()[3:] // shared
	newer := recordsTestDefs()[3:]
	newer[0].Steps = append(append([]records.RecordStep{}, newer[0].Steps...), records.RecordStep{Version: 5, Add: &records.Add{Field: "extra", Type: records.Text}})
	newer[0].Fields = append(append([]records.RecordField{}, newer[0].Fields...), records.RecordField{Name: "extra", Label: "追加の項目"})

	e := newRecordsEnvWith(t, path, newer)
	if w := e.post("owner", "/admin/records/shared", url.Values{"f_note": {"新しい版"}, "f_extra": {"NEWER-VALUE"}}); w.Code != http.StatusSeeOther {
		t.Fatalf("create on newer: %d", w.Code)
	}
	var id int64
	e.db.QueryRow(`SELECT id FROM records_shared`).Scan(&id)

	old := newRecordsEnvWith(t, path, older) // 実行ファイルだけ前の版へ戻した
	p := "/admin/records/shared/" + strconv.FormatInt(id, 10)
	if strings.Contains(old.get("owner", p).Body.String(), "NEWER-VALUE") || strings.Contains(old.get("owner", "/admin/records/shared").Body.String(), "NEWER-VALUE") {
		t.Error("the older version shows a column it does not know")
	}
	if w := old.post("owner", p+"/edit", url.Values{"revision": {old.revision("shared", id)}, "f_note": {"前の版で直した"}}); w.Code != http.StatusSeeOther {
		t.Fatalf("edit on older: %d", w.Code)
	}
	old.create("owner", "shared", "前の版で作った")

	back := newRecordsEnvWith(t, path, newer)
	var extra string
	var note string
	back.db.QueryRow(`SELECT f_note, f_extra FROM records_shared WHERE id = ?`, id).Scan(&note, &extra)
	if note != "前の版で直した" || extra != "NEWER-VALUE" {
		t.Errorf("note=%q extra=%q", note, extra)
	}
	if !strings.Contains(back.get("owner", p).Body.String(), "NEWER-VALUE") {
		t.Error("the newer version does not show its column again")
	}
}

// TestRecords_NeedsLoginAndAccountDirectory は、ログインしていない要求が画面に入れないことと、
// アカウントの一覧を出す機能が無い構成では起動を止めることを確かめます。
// 不変条件: RECORDS-INV-1
func TestRecords_NeedsLoginAndAccountDirectory(t *testing.T) {
	e := newRecordsEnv(t)
	for _, p := range []string{"/admin/records", "/admin/records/shared", "/admin/records/shared/new", "/admin/records/deleted-owners"} {
		if w := e.get("", p); w.Code == http.StatusOK {
			t.Errorf("%s: anonymous got 200", p)
		}
	}
	core, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer core.Close()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	reg := features.NewRegistry()
	reg.Register(records.NewFeature(logger, recordsTestDefs(), nil))
	for _, f := range reg.All() {
		if err := f.Init(core.DB()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := app.BuildRoutes(core, reg, false, logger); err == nil {
		t.Error("started without an account directory")
	}
}
