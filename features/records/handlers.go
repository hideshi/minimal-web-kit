package records

import (
	"bytes"
	"context"
	"embed"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"minimal-web-kit/features"
	"minimal-web-kit/internal/store"
	"minimal-web-kit/internal/web"
	"minimal-web-kit/internal/web/form"
	"minimal-web-kit/internal/web/notice"
	"minimal-web-kit/internal/web/page"
)

//go:embed views/*.html
var viewsFS embed.FS

var pageNames = []string{
	"records_kinds.html", "records_list.html", "record.html", "record_form.html", "record_delete.html",
	"records_deleted_owners.html", "records_deleted_owner_delete.html",
}

const (
	root     = "/admin/records"
	pageSize = 30
	maxPage  = 1000
	// maxQueryRunes は、一覧の語の上限です。
	maxQueryRunes = 100
)

var messages = notice.Messages{
	"created": "記録を作成しました。", "saved": "変更を保存しました。", "deleted": "記録を削除しました。",
	"conflict": conflictMessage, "purged": "削除済みのアカウントの記録を削除しました。",
}

const conflictMessage = "画面を開いた後に、ほかの人がこの記録を変更・削除しました。開き直してから、もう一度操作してください。"

// SetAdmin は管理者認証を受け取り、画面のテンプレートを解析します。
func (f *Feature) SetAdmin(admin features.Admin) error {
	pages, err := web.ParsePages(admin.Layout(), viewsFS, pageNames...)
	if err != nil {
		return err
	}
	f.admin, f.pages = admin, pages
	return nil
}

// SetAccountDirectory はアカウントの一覧を受け取ります。
func (f *Feature) SetAccountDirectory(d features.AccountDirectory) { f.directory = d }

// AdminNavLinks は、全てのロールのナビに「業務記録」を加えます。
func (f *Feature) AdminNavLinks() []web.NavLink {
	return []web.NavLink{{Label: "業務記録", Href: root, AllRoles: true}}
}

// PrivacyNotices は、業務記録で保存する情報を、ログイン後の画面に載せます（公開のポリシーには載せない）。
// 内容は docs/data-inventory.md の14節（定義から生成）と揃えます。
func (f *Feature) PrivacyNotices() []web.PrivacyNotice {
	var rows []web.PrivacyRow
	for _, d := range f.defs {
		var labels []string
		for _, fl := range d.Fields {
			labels = append(labels, fl.Label)
		}
		_, read, _ := accessText(d.Access)
		rows = append(rows, web.PrivacyRow{
			Kind:      d.Title,
			Items:     strings.Join(labels, "、") + "、作った人のアカウントID、作成・更新の日時（" + read + "）",
			Purpose:   d.Purpose,
			Retention: d.Retention,
		})
	}
	var kept []string
	for _, r := range f.retired {
		if !r.Dropped {
			kept = append(kept, r.Name)
		}
	}
	if len(rows) == 0 && len(kept) == 0 {
		return nil
	}
	lead := "ログインして業務記録を使う人について、種類ごとに次の情報を保存します。括弧の中は、記録を読める人です。消した記録も、バックアップには保持の期間だけ残ります。AIエージェントの接続で業務記録の種類を有効にしたサイトでは、接続を許可された人が読み取ったその種類の記録の内容が、その人の使うAIのサービスへ送られます。"
	if len(kept) > 0 {
		lead += "使うのをやめた種類（" + strings.Join(kept, "、") + "）の記録は、画面には出ませんが、システム管理者が消すまで残っています。"
	}
	return []web.PrivacyNotice{{Title: "業務記録で保存する情報", Internal: true, Lead: lead, Rows: rows}}
}

// RegisterIsolatedRoutes は経路を登録します。経路は文字列のリテラルで書きます。全ての経路にログインが要り、変更はPOSTだけで行います。
// 削除済みのアカウントの記録の画面は、システム管理者だけが使えます。
// 不変条件: RECORDS-INV-1
func (f *Feature) RegisterIsolatedRoutes(mux *web.IsolatedMux) {
	if f.admin == nil {
		return
	}
	f.recordsRoutes(mux)
}

func (f *Feature) recordsRoutes(mux *web.IsolatedMux) {
	view, submit := f.admin.AccountPage, f.admin.AccountForm
	mux.HandleFunc("GET /admin/records", view(f.handleKinds))
	mux.HandleFunc("GET /admin/records/deleted-owners", f.admin.AdminPage(f.handleDeletedOwners))
	mux.HandleFunc("GET /admin/records/deleted-owners/{kind}/{owner}/delete", f.admin.AdminPage(f.handleDeletedOwnerPage))
	mux.HandleFunc("POST /admin/records/deleted-owners/{kind}/{owner}/delete", f.admin.AdminForm(f.handleDeletedOwnerPurge))
	mux.HandleFunc("GET /admin/records/{kind}", view(f.handleList))
	mux.HandleFunc("GET /admin/records/{kind}/new", view(f.handleNew))
	mux.HandleFunc("POST /admin/records/{kind}", submit(f.handleCreate))
	mux.HandleFunc("GET /admin/records/{kind}/{id}", view(f.handleShow))
	mux.HandleFunc("GET /admin/records/{kind}/{id}/edit", view(f.handleEdit))
	mux.HandleFunc("POST /admin/records/{kind}/{id}/edit", submit(f.handleSave))
	mux.HandleFunc("GET /admin/records/{kind}/{id}/delete", view(f.handleDeletePage))
	mux.HandleFunc("POST /admin/records/{kind}/{id}/delete", submit(f.handleDelete))
}

// requireRecordsDirectory は、アカウントの一覧を返します。一覧の無い構成は起動の時に止まるので、ここで nil なら内部の誤りです。
// 不変条件: RECORDS-INV-1
func (f *Feature) requireRecordsDirectory() (features.AccountDirectory, error) {
	if f.directory == nil {
		return nil, errors.New("records: no account directory")
	}
	return f.directory, nil
}

func (f *Feature) render(w http.ResponseWriter, status int, name string, data map[string]any) {
	var buf bytes.Buffer
	if err := f.pages[name].ExecuteTemplate(&buf, "layout", data); err != nil {
		f.logger.Error("records render failed", web.RequestIDAttr(w))
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}

// failed はエラーの応答を書きます。記録の値はログに出しません（RECORDS-INV-13）。
func (f *Feature) failed(w http.ResponseWriter, r *http.Request, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, ErrNotFound):
		http.NotFound(w, r)
	case errors.Is(err, ErrConflict):
		http.Error(w, conflictMessage, http.StatusConflict)
	case store.Busy(err):
		web.WriteBusy(w)
	default:
		f.logger.Error("records failed", web.RequestIDAttr(w))
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
	return true
}

// kind は、経路の種類の名前から、定義と文を引きます。無い種類は404です（読めない記録と同じ応答）。
func (f *Feature) kind(w http.ResponseWriter, r *http.Request) (RecordDef, *kindStatements, bool) {
	name := r.PathValue("kind")
	for _, d := range f.defs {
		if d.Name == name {
			return d, f.stmts[name], true
		}
	}
	http.NotFound(w, r)
	return RecordDef{}, nil, false
}

func viewerOf(s features.AdminSession) viewer { return viewer{ID: s.AccountID(), Admin: s.IsAdmin()} }

func kindPath(d RecordDef) string { return root + "/" + d.Name }

func recordPath(d RecordDef, id int64) string { return kindPath(d) + "/" + strconv.FormatInt(id, 10) }

// people は、アカウントの呼び名と状態の引き当てです。一覧の取得に失敗したら、削除済みと扱わずにエラーにします。
type people map[int64]features.DirectoryAccount

func (f *Feature) people(ctx context.Context) (people, error) {
	dir, err := f.requireRecordsDirectory()
	if err != nil {
		return nil, err
	}
	all, err := dir.Accounts(ctx)
	if err != nil {
		return nil, err
	}
	p := people{}
	for _, a := range all {
		p[a.ID] = a
	}
	return p, nil
}

// Name は表示する呼び名です。
func (p people) Name(id int64) string {
	a, ok := p[id]
	switch {
	case !ok:
		return "削除済みのアカウント"
	case a.Disabled:
		return a.Name + "（停止中）"
	}
	return a.Name
}

// cell は、一覧と詳細に出す1つの値です。
type cell struct {
	Label string
	Text  string     // 1行で見せる値
	Lines [][]string // 複数行の文字（詳細の画面）
	Date  time.Time  // 日付の項目の値（ゼロなら Text を使う）
}

// cellOf は、1つの値を画面に出す形にします。日付は共通の部品 "date" で出します。
func cellOf(label string, t FieldType, v any, short int) cell {
	c := cell{Label: label, Text: displayText(t, v)}
	if s, ok := v.(string); ok && t == Date {
		if d, err := time.Parse("2006-01-02", s); err == nil {
			c.Date = d
		}
	}
	if short > 0 {
		c.Text = shorten(c.Text, short)
	} else if t == LongText && v != nil {
		c.Lines = form.Paragraphs(c.Text)
	}
	return c
}

func fieldDef(d RecordDef, name string) RecordField {
	for _, f := range d.Fields {
		if f.Name == name {
			return f
		}
	}
	return RecordField{Name: name, Label: name}
}

// visibleRecordFields は、画面とMCPに出す項目です。今の定義の項目だけで、定義に無い列（新しい版が足した列）は出しません。
// 不変条件: RECORDS-INV-12
func visibleRecordFields(d RecordDef, k *kindStatements) []column {
	var out []column
	for _, c := range k.columns {
		for _, f := range d.Fields {
			if f.Name == c.name {
				out = append(out, c)
			}
		}
	}
	return out
}

// displayText は、値を1行の文字にします。
func displayText(t FieldType, v any) string {
	switch x := v.(type) {
	case nil:
		return "なし"
	case int64:
		if t == Bool {
			if x == 1 {
				return "はい"
			}
			return "いいえ"
		}
		return strconv.FormatInt(x, 10)
	case string:
		return x
	}
	return fmt.Sprint(v)
}

func shorten(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

// ---- 種類の一覧 ----

func (f *Feature) handleKinds(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	data := s.PageData("業務記録", root)
	data["Kinds"] = f.defs
	data["IsAdmin"] = s.IsAdmin()
	f.render(w, http.StatusOK, "records_kinds.html", data)
}

// ---- 記録の一覧 ----

func (f *Feature) handleList(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	d, k, ok := f.kind(w, r)
	if !ok {
		return
	}
	p, ok := page.Parse(r, pageSize, maxPage)
	if !ok {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	filter, ok := parseListFilter(d, k, viewerOf(s), r.URL.Query())
	if !ok {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	items, err := listRecords(r.Context(), f.db, k, filter, p.Limit(), p.Offset())
	if f.failed(w, r, err) {
		return
	}
	who, err := f.people(r.Context())
	if f.failed(w, r, err) {
		return
	}
	items = page.Trim(&p, items)
	p.Base, p.Label, p.PrevText, p.NextText = kindPath(d), d.Title+"の一覧のページ送り", "前のページ", "次のページ"
	for _, pr := range []struct{ name, value string }{{"q", filter.Query}, {"from", filter.From}, {"to", filter.To}} {
		if pr.value != "" {
			p.Params = append(p.Params, page.Param{Name: pr.name, Value: pr.value})
		}
	}
	if filter.MineOnly {
		p.Params = append(p.Params, page.Param{Name: "mine", Value: "1"})
	}
	for _, n := range k.selectCols {
		if filter.Selects[n] != "" {
			p.Params = append(p.Params, page.Param{Name: "s_" + n, Value: filter.Selects[n]})
		}
	}
	if filter.Sort != 0 {
		p.Params = append(p.Params, page.Param{Name: "sort", Value: strconv.Itoa(filter.Sort)})
	}

	types := map[string]FieldType{}
	for _, c := range k.columns {
		types[c.name] = c.typ
	}
	var heads []string
	for _, n := range d.ListFields {
		heads = append(heads, fieldDef(d, n).Label)
	}
	type row struct {
		ID    int64
		Cells []cell
		Owner string
	}
	var rows []row
	for _, it := range items {
		var cells []cell
		for _, n := range d.ListFields {
			cells = append(cells, cellOf("", types[n], it.Values[n], 40))
		}
		rows = append(rows, row{ID: it.ID, Cells: cells, Owner: who.Name(it.Owner)})
	}
	data := s.PageData(d.Title, root)
	data["Def"] = d
	data["Path"] = kindPath(d)
	data["Heads"] = heads
	data["Rows"] = rows
	data["Page"] = p
	data["Notice"] = messages.From(r)
	data["Search"] = f.searchFields(d, k, filter)
	data["Filtered"] = filter.Query != "" || filter.MineOnly || filter.From != "" || filter.To != "" || anySet(filter.Selects)
	f.render(w, http.StatusOK, "records_list.html", data)
}

// parseListFilter は、一覧の要求の値から絞り込みを作ります。値は文の文字列に入らず、並びは番号に変え、
// 選択肢に無い値・形の違う日付・範囲の外の並びは、絞り込まない・既定の並びにします。語が長すぎれば false です。
// 不変条件: RECORDS-INV-7
func parseListFilter(d RecordDef, k *kindStatements, v viewer, qv url.Values) (listFilter, bool) {
	filter := listFilter{ReadAll: readableCondition(d.Access, v), Viewer: v.ID, Query: strings.TrimSpace(qv.Get("q")), Selects: map[string]string{}}
	if utf8.RuneCountInString(filter.Query) > maxQueryRunes || !utf8.ValidString(filter.Query) {
		return filter, false
	}
	filter.MineOnly = filter.ReadAll && qv.Get("mine") == "1"
	for _, n := range k.selectCols {
		val := qv.Get("s_" + n)
		if val != "" && !contains(fieldDef(d, n).Options, val) {
			val = "" // 選択肢に無い値は、絞り込まない
		}
		filter.Selects[n] = val
	}
	if k.dateColumn != "" {
		filter.From, filter.To = dateParam(qv.Get("from")), dateParam(qv.Get("to"))
	}
	filter.Sort, _ = strconv.Atoi(qv.Get("sort"))
	if filter.Sort < 0 || filter.Sort >= len(k.sorts) {
		filter.Sort = 0
	}
	return filter, true
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func anySet(m map[string]string) bool {
	for _, v := range m {
		if v != "" {
			return true
		}
	}
	return false
}

// dateParam は、期間の入力を YYYY-MM-DD の時だけ受け取ります。それ以外は絞り込みません。
func dateParam(raw string) string {
	if _, msg := validateFieldValue(RecordField{Label: "日付"}, Date, raw); msg != "" || raw == "" {
		return ""
	}
	return raw
}

// searchFields は、一覧の絞り込みの欄です。
func (f *Feature) searchFields(d RecordDef, k *kindStatements, filter listFilter) []form.Field {
	var out []form.Field
	if len(k.textColumns) > 0 {
		out = append(out, form.Field{ID: "records-q", Name: "q", Label: "語で探す", Optional: true, Value: filter.Query, MaxLength: maxQueryRunes, Hint: "文字の項目から、語を含む記録を探します。"})
	}
	for _, n := range k.selectCols {
		fd := fieldDef(d, n)
		opts := []form.Option{{Value: "", Label: "全て", Selected: filter.Selects[n] == ""}}
		for _, o := range fd.Options {
			opts = append(opts, form.Option{Value: o, Label: o, Selected: filter.Selects[n] == o})
		}
		out = append(out, form.Field{ID: "records-s-" + n, Name: "s_" + n, Label: fd.Label, Optional: true, Options: opts})
	}
	if k.dateColumn != "" {
		label := fieldDef(d, k.dateColumn).Label
		out = append(out,
			form.Field{ID: "records-from", Name: "from", Label: label + "（から）", Optional: true, Type: "date", Value: filter.From},
			form.Field{ID: "records-to", Name: "to", Label: label + "（まで）", Optional: true, Type: "date", Value: filter.To})
	}
	types := map[string]FieldType{}
	for _, c := range k.columns {
		types[c.name] = c.typ
	}
	var sorts []form.Option
	for i, o := range k.sorts {
		label, typ := "更新日時", Date
		if o.Field != "" {
			label, typ = fieldDef(d, o.Field).Label, types[o.Field]
		}
		switch {
		case typ == Date && o.Desc:
			label += "の新しい順"
		case typ == Date:
			label += "の古い順"
		case (typ == Integer || typ == Bool) && o.Desc:
			label += "の大きい順"
		case typ == Integer || typ == Bool:
			label += "の小さい順"
		case o.Desc:
			label += "の逆順"
		default:
			label += "の順"
		}
		sorts = append(sorts, form.Option{Value: strconv.Itoa(i), Label: label, Selected: i == filter.Sort})
	}
	out = append(out, form.Field{ID: "records-sort", Name: "sort", Label: "並び", Optional: true, Options: sorts})
	if filter.ReadAll {
		out = append(out, form.Field{ID: "records-mine", Name: "mine", Label: "自分の記録だけ", Type: "checkbox", Checked: filter.MineOnly})
	}
	return out
}

// ---- 詳細 ----

// loadReadable は、読める記録を返します。読めない記録は無い記録と同じ404にします。
func (f *Feature) loadReadable(w http.ResponseWriter, r *http.Request, s features.AdminSession, d RecordDef, k *kindStatements, act action) (record, bool) {
	id, ok := web.PathID(w, r, "id")
	if !ok {
		return record{}, false
	}
	rec, err := getRecord(r.Context(), f.db, k, id)
	if f.failed(w, r, err) {
		return record{}, false
	}
	v := viewerOf(s)
	if !checkRecordAccess(d.Access, v, rec.Owner, actRead) {
		http.NotFound(w, r)
		return record{}, false
	}
	if act != actRead && !checkRecordAccess(d.Access, v, rec.Owner, act) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return record{}, false
	}
	return rec, true
}

func (f *Feature) handleShow(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	d, k, ok := f.kind(w, r)
	if !ok {
		return
	}
	rec, ok := f.loadReadable(w, r, s, d, k, actRead)
	if !ok {
		return
	}
	who, err := f.people(r.Context())
	if f.failed(w, r, err) {
		return
	}
	var cells []cell
	for _, c := range visibleRecordFields(d, k) {
		cells = append(cells, cellOf(fieldDef(d, c.name).Label, c.typ, rec.Values[c.name], 0))
	}
	v := viewerOf(s)
	data := s.PageData(d.Title+" #"+strconv.FormatInt(rec.ID, 10), root)
	data["Def"] = d
	data["Path"] = kindPath(d)
	data["Record"] = rec
	data["Cells"] = cells
	data["Owner"] = who.Name(rec.Owner)
	data["CanEdit"] = checkRecordAccess(d.Access, v, rec.Owner, actEdit)
	data["CanDelete"] = checkRecordAccess(d.Access, v, rec.Owner, actDelete)
	data["Notice"] = messages.From(r)
	f.render(w, http.StatusOK, "record.html", data)
}

// ---- 作成・編集 ----

// formState は、作成・編集の画面の値です。Raw は送られた入力、Errors は項目ごとのエラーの文です。
type formState struct {
	Raw      map[string]string
	Errors   map[string]string
	Revision int64
	Conflict bool
}

// formFields は、項目ごとの入力欄を作ります。選択肢に無い既存の値は、今の値として説明に示し、選び直させます。
func formFields(d RecordDef, k *kindStatements, st formState) []form.Field {
	var out []form.Field
	for _, c := range visibleRecordFields(d, k) {
		fd := fieldDef(d, c.name)
		id := "record-" + c.name
		raw := st.Raw[c.name]
		fl := form.Field{ID: id, Name: "f_" + c.name, Label: fd.Label, Value: raw, Required: fd.Required && c.typ != Bool, Optional: !fd.Required, Error: st.Errors[c.name]}
		switch c.typ {
		case Text:
			fl.MaxLength = maxLen(fd, c.typ)
			fl.Hint = fmt.Sprintf("%d文字以内、改行なし。", fl.MaxLength)
		case LongText:
			fl.Rows = 8
			fl.Hint = fmt.Sprintf("%d文字以内。", maxLen(fd, c.typ))
		case Integer:
			fl.Type = "number"
			fl.Hint = "整数。"
			if r := rangeText(fd); r != "" {
				fl.Hint = strings.TrimSuffix(r, "の") + "の整数。"
			}
		case Date:
			fl.Type = "date"
		case Select:
			opts := []form.Option{{Value: "", Label: "選んでください", Selected: raw == ""}}
			for _, o := range fd.Options {
				opts = append(opts, form.Option{Value: o, Label: o, Selected: raw == o})
			}
			fl.Options = opts
			if raw != "" && !contains(fd.Options, raw) {
				fl.Hint = "今の値「" + raw + "」は選択肢にありません。選び直してください。"
			}
		case Bool:
			fl.Type = "checkbox"
			fl.Checked = raw == "1"
			fl.Value = ""
			fl.Optional = false
		}
		out = append(out, fl)
	}
	return out
}

// rawOf は、保存してある値を入力欄の文字にします。
func rawOf(k *kindStatements, values map[string]any) map[string]string {
	out := map[string]string{}
	for _, c := range k.columns {
		switch x := values[c.name].(type) {
		case nil:
		case int64:
			out[c.name] = strconv.FormatInt(x, 10)
		case string:
			out[c.name] = x
		}
	}
	return out
}

func (f *Feature) renderForm(w http.ResponseWriter, s features.AdminSession, d RecordDef, k *kindStatements, id int64, st formState, status int) {
	title := d.Title + "の作成"
	if id != 0 {
		title = d.Title + " #" + strconv.FormatInt(id, 10) + "の編集"
	}
	data := s.PageData(title, root)
	data["Def"] = d
	data["Path"] = kindPath(d)
	data["ID"] = id
	data["Revision"] = st.Revision
	data["Fields"] = formFields(d, k, st)
	switch {
	case st.Conflict:
		data["Error"] = conflictMessage
	case len(st.Errors) > 0:
		data["Error"] = "入力を確認してください。"
	}
	f.render(w, status, "record_form.html", data)
}

// readForm は、送られた入力を検査し、保存する値にします。
func readForm(r *http.Request, d RecordDef, k *kindStatements) (map[string]any, formState) {
	st := formState{Raw: map[string]string{}, Errors: map[string]string{}}
	values := map[string]any{}
	for _, c := range visibleRecordFields(d, k) {
		raw := r.PostFormValue("f_" + c.name)
		st.Raw[c.name] = raw
		v, msg := validateFieldValue(fieldDef(d, c.name), c.typ, raw)
		if msg != "" {
			st.Errors[c.name] = msg
			continue
		}
		values[c.name] = v
	}
	return values, st
}

func (f *Feature) handleNew(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	d, k, ok := f.kind(w, r)
	if !ok {
		return
	}
	f.renderForm(w, s, d, k, 0, formState{Raw: map[string]string{}}, http.StatusOK)
}

func (f *Feature) handleCreate(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	d, k, ok := f.kind(w, r)
	if !ok {
		return
	}
	values, st := readForm(r, d, k)
	if len(st.Errors) > 0 {
		f.renderForm(w, s, d, k, 0, st, http.StatusBadRequest)
		return
	}
	id, err := insertRecord(r.Context(), f.db, k, s.AccountID(), values, f.now())
	if f.failed(w, r, err) {
		return
	}
	notice.Redirect(w, r, recordPath(d, id), "created")
}

func (f *Feature) handleEdit(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	d, k, ok := f.kind(w, r)
	if !ok {
		return
	}
	rec, ok := f.loadReadable(w, r, s, d, k, actEdit)
	if !ok {
		return
	}
	f.renderForm(w, s, d, k, rec.ID, formState{Raw: rawOf(k, rec.Values), Revision: rec.Revision}, http.StatusOK)
}

func (f *Feature) handleSave(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	d, k, ok := f.kind(w, r)
	if !ok {
		return
	}
	rec, ok := f.loadReadable(w, r, s, d, k, actEdit)
	if !ok {
		return
	}
	revision, err := strconv.ParseInt(r.PostFormValue("revision"), 10, 64)
	if err != nil || revision < 1 {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	values, st := readForm(r, d, k)
	st.Revision = revision
	if len(st.Errors) > 0 {
		f.renderForm(w, s, d, k, rec.ID, st, http.StatusBadRequest)
		return
	}
	err = updateRecordRevision(r.Context(), f.db, k, rec.ID, revision, values, f.now())
	if errors.Is(err, ErrConflict) {
		st.Conflict = true
		f.renderForm(w, s, d, k, rec.ID, st, http.StatusConflict)
		return
	}
	if f.failed(w, r, err) {
		return
	}
	notice.Redirect(w, r, recordPath(d, rec.ID), "saved")
}

// ---- 削除 ----

func (f *Feature) handleDeletePage(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	d, k, ok := f.kind(w, r)
	if !ok {
		return
	}
	rec, ok := f.loadReadable(w, r, s, d, k, actDelete)
	if !ok {
		return
	}
	data := s.PageData(d.Title+"の削除", root)
	data["Def"] = d
	data["Record"] = rec
	// 開いた時の版の数を送り先に付け、消す時に照合する（RECORDS-INV-5）
	data["Confirm"] = form.Confirm{Action: recordPath(d, rec.ID) + "/delete?revision=" + strconv.FormatInt(rec.Revision, 10), Back: recordPath(d, rec.ID), CSRFToken: s.CSRFToken()}
	f.render(w, http.StatusOK, "record_delete.html", data)
}

func (f *Feature) handleDelete(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	d, k, ok := f.kind(w, r)
	if !ok {
		return
	}
	rec, ok := f.loadReadable(w, r, s, d, k, actDelete)
	if !ok {
		return
	}
	revision, err := strconv.ParseInt(r.URL.Query().Get("revision"), 10, 64)
	if err != nil || revision < 1 {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	err = deleteRecordRevision(r.Context(), f.db, k, rec.ID, revision)
	if errors.Is(err, ErrConflict) {
		notice.Redirect(w, r, recordPath(d, rec.ID), "conflict")
		return
	}
	if f.failed(w, r, err) {
		return
	}
	notice.Redirect(w, r, kindPath(d), "deleted")
}

// ---- 削除済みのアカウントの記録（システム管理者） ----

// deletedOwnerRow は、種類と、削除済みの所有者ごとの件数です。本文は持ちません。
type deletedOwnerRow struct {
	Kind  RecordDef
	Owner int64
	Count int
}

// purgeable は、まとめて消せる種類です（AllReadWrite は共有の台帳なので対象にしない）。
func purgeable(d RecordDef) bool { return d.Access != AllReadWrite }

func (f *Feature) deletedOwnerRows(ctx context.Context) ([]deletedOwnerRow, error) {
	who, err := f.people(ctx)
	if err != nil {
		return nil, err
	}
	var out []deletedOwnerRow
	for _, d := range f.defs {
		if !purgeable(d) {
			continue
		}
		counts, err := deletedOwnerCounts(ctx, f.db, f.stmts[d.Name])
		if err != nil {
			return nil, err
		}
		for owner, n := range counts {
			if _, alive := who[owner]; !alive {
				out = append(out, deletedOwnerRow{Kind: d, Owner: owner, Count: n})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind.Name != out[j].Kind.Name {
			return out[i].Kind.Name < out[j].Kind.Name
		}
		return out[i].Owner < out[j].Owner
	})
	return out, nil
}

func (f *Feature) handleDeletedOwners(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	rows, err := f.deletedOwnerRows(r.Context())
	if f.failed(w, r, err) {
		return
	}
	data := s.PageData("削除済みのアカウントの記録", root)
	data["Rows"] = rows
	data["Notice"] = messages.From(r)
	f.render(w, http.StatusOK, "records_deleted_owners.html", data)
}

// deletedOwnerTarget は、経路の種類と所有者を確かめ、今も削除済みならその件数を返します。
// アカウントの一覧を読み直し、一覧にあれば（停止中を含む）404にします。
func (f *Feature) deletedOwnerTarget(w http.ResponseWriter, r *http.Request) (RecordDef, *kindStatements, int64, int, bool) {
	d, k, ok := f.kind(w, r)
	if !ok {
		return d, nil, 0, 0, false
	}
	owner, ok := web.PathID(w, r, "owner")
	if !ok {
		return d, nil, 0, 0, false
	}
	if !purgeable(d) {
		http.NotFound(w, r)
		return d, nil, 0, 0, false
	}
	who, err := f.people(r.Context())
	if f.failed(w, r, err) {
		return d, nil, 0, 0, false
	}
	if _, alive := who[owner]; alive {
		http.NotFound(w, r)
		return d, nil, 0, 0, false
	}
	counts, err := deletedOwnerCounts(r.Context(), f.db, k)
	if f.failed(w, r, err) {
		return d, nil, 0, 0, false
	}
	if counts[owner] == 0 {
		http.NotFound(w, r)
		return d, nil, 0, 0, false
	}
	return d, k, owner, counts[owner], true
}

func (f *Feature) handleDeletedOwnerPage(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	d, _, owner, n, ok := f.deletedOwnerTarget(w, r)
	if !ok {
		return
	}
	data := s.PageData("削除済みのアカウントの記録の削除", root)
	data["Def"] = d
	data["Owner"] = owner
	data["Count"] = n
	data["Confirm"] = form.Confirm{Action: fmt.Sprintf("%s/deleted-owners/%s/%d/delete", root, d.Name, owner), Back: root + "/deleted-owners", CSRFToken: s.CSRFToken()}
	f.render(w, http.StatusOK, "records_deleted_owner_delete.html", data)
}

func (f *Feature) handleDeletedOwnerPurge(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	_, k, owner, _, ok := f.deletedOwnerTarget(w, r)
	if !ok {
		return
	}
	if _, err := purgeDeletedOwnerRecords(r.Context(), f.db, k, owner); f.failed(w, r, err) {
		return
	}
	notice.Redirect(w, r, root+"/deleted-owners", "purged")
}
