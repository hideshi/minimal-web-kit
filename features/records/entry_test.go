package records

import (
	"context"
	"database/sql"
	"net/url"
	"sync"
	"testing"
)

func listEntryDef() RecordDef {
	return RecordDef{
		Name: "k", Title: "k", Purpose: "試験", Retention: "消すまで", Access: OwnerWriteAllRead,
		Steps: []RecordStep{
			{Version: 1, Add: &Add{Field: "note", Type: Text}},
			{Version: 2, Add: &Add{Field: "day", Type: Date}},
			{Version: 3, Add: &Add{Field: "state", Type: Select}},
		},
		Fields: []RecordField{
			{Name: "note", Label: "メモ"}, {Name: "day", Label: "日"}, {Name: "state", Label: "状態", Options: []string{"未", "済"}},
		},
		ListFields: []string{"note", "day", "state"}, SortBy: "day",
	}
}

// entryRunner は、一覧の入口に値を渡し、実行された文が起動の時に作った文のどれかかを確かめる関数を返します。
func entryRunner(t testing.TB, db *sql.DB) func(qv url.Values) {
	d := listEntryDef()
	k := buildRecordStatements([]RecordDef{d}, nil)[d.Name]
	allowed := map[string]bool{}
	for _, st := range k.list {
		allowed[st.sql] = true
	}
	var mu sync.Mutex
	var seen []string
	statementHook = func(st recordsStatement) {
		mu.Lock()
		seen = append(seen, st.sql)
		mu.Unlock()
	}
	t.Cleanup(func() { statementHook = nil })
	return func(qv url.Values) {
		mu.Lock()
		seen = nil
		mu.Unlock()
		f, ok := parseListFilter(d, k, viewer{ID: 1}, qv)
		if !ok {
			return // 語が長すぎるなど、400 で止まる
		}
		if _, err := listRecords(context.Background(), db, k, f, 31, 0); err != nil {
			t.Fatalf("list %v: %v", qv, err)
		}
		mu.Lock()
		defer mu.Unlock()
		if len(seen) != 1 {
			t.Fatalf("list %v ran %d statements", qv, len(seen))
		}
		if !allowed[seen[0]] {
			t.Fatalf("list %v ran a statement that was not built on start:\n%s", qv, seen[0])
		}
	}
}

// TestListEntry_OnlyPrebuiltStatements は、一覧の入口に不正な値（SQLの断片、記号、範囲の外の並び）を渡しても、
// 実行される文が起動の時に作った文のどれかで、文の文字列が要求の値で変わらないことを確かめます。
// 不変条件: RECORDS-INV-7
func TestListEntry_OnlyPrebuiltStatements(t *testing.T) {
	db, _ := testDB(t)
	mustMigrate(t, db, []RecordDef{listEntryDef()})
	run := entryRunner(t, db)
	for _, bad := range []string{`'; DROP TABLE records_k; --`, `" OR 1=1 --`, `%`, `_`, `\`, `f_note`, `-1`, `99999999999999999999`, `1e3`, `0x10`, "\x00", `未' OR '1'='1`} {
		run(url.Values{"q": {bad}, "sort": {bad}, "s_state": {bad}, "from": {bad}, "to": {bad}, "mine": {bad}, "s_note": {bad}})
	}
}

// FuzzListEntry は、一覧の入口に任意の値を与え、実行される文が起動の時に作った文のどれかであることを確かめます。
// 不変条件: RECORDS-INV-7
func FuzzListEntry(f *testing.F) {
	for _, s := range []string{"", "0", "3", "-1", "未", "2026-11-06", `'; DROP TABLE x; --`, "%_\\"} {
		f.Add(s, s, s, s)
	}
	f.Fuzz(func(t *testing.T, q, sort, state, day string) {
		db, _ := testDB(t)
		mustMigrate(t, db, []RecordDef{listEntryDef()})
		entryRunner(t, db)(url.Values{"q": {q}, "sort": {sort}, "s_state": {state}, "from": {day}, "to": {day}, "mine": {"1"}})
	})
}
