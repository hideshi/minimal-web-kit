package records

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"minimal-web-kit/internal/store"
)

// testDB は、本番と同じ設定（WAL・secure_delete）のファイルのDBを作ります。複製はこのDBと同じ場所にできます。
func testDB(t *testing.T) (*sql.DB, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := store.NewSQLiteStore(filepath.Join(dir, "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s.DB(), dir
}

// kindDef は、手順だけを変えた試験用の種類を作ります。見せ方は手順を当てた結果から作ります。
func kindDef(t *testing.T, name string, steps ...RecordStep) RecordDef {
	t.Helper()
	for i := range steps {
		steps[i].Version = i + 1
	}
	sc, err := replaySteps(steps)
	if err != nil {
		t.Fatal(err)
	}
	d := RecordDef{Name: name, Title: name, Purpose: "試験", Retention: "消すまで", Access: AllReadWrite, Steps: steps}
	for _, c := range sc.columns {
		f := RecordField{Name: c.name, Label: c.name}
		if c.typ == Select {
			f.Options = []string{"a", "b"}
		}
		d.Fields = append(d.Fields, f)
	}
	return d
}

func add(f string, typ FieldType) RecordStep { return RecordStep{Add: &Add{Field: f, Type: typ}} }
func rename(from, to string) RecordStep      { return RecordStep{Rename: &Rename{From: from, To: to}} }
func remove(f string) RecordStep             { return RecordStep{Remove: &Remove{Field: f}} }
func change(f string, from, to FieldType) RecordStep {
	return RecordStep{ChangeType: &ChangeType{Field: f, From: from, To: to}}
}

func migrate(t *testing.T, db *sql.DB, defs []RecordDef, retired ...RetiredDef) (*recordsPlan, error) {
	t.Helper()
	return migrateRecords(context.Background(), db, defs, retired, nil)
}

func mustMigrate(t *testing.T, db *sql.DB, defs []RecordDef, retired ...RetiredDef) *recordsPlan {
	t.Helper()
	p, err := migrate(t, db, defs, retired...)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// snapshot は、業務記録の表の定義・行と、記録の表の中身を文字にします。何も変わっていないことの比較に使います。
func snapshot(t *testing.T, db *sql.DB) string {
	t.Helper()
	var out []string
	rows, err := db.Query(`SELECT name, sql FROM sqlite_master WHERE name LIKE 'records%' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var n string
		var s sql.NullString
		if err := rows.Scan(&n, &s); err != nil {
			t.Fatal(err)
		}
		out = append(out, n+"="+s.String)
		if s.Valid && strings.HasPrefix(s.String, "CREATE TABLE") {
			tables = append(tables, n)
		}
	}
	rows.Close()
	for _, tb := range tables {
		r, err := db.Query("SELECT * FROM " + quoteIdent(tb))
		if err != nil {
			t.Fatal(err)
		}
		cols, _ := r.Columns()
		for r.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := r.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			var cells []string
			for i, v := range vals {
				cells = append(cells, cols[i]+":"+strings.TrimSpace(strings.ReplaceAll(fmtAny(v), "\n", " ")))
			}
			out = append(out, tb+"|"+strings.Join(cells, ","))
		}
		r.Close()
	}
	sort.Strings(out)
	return strings.Join(out, "\n")
}

func fmtAny(v any) string {
	switch x := v.(type) {
	case nil:
		return "NULL"
	case []byte:
		return "'" + string(x) + "'"
	case string:
		return "'" + x + "'"
	default:
		return fmt.Sprint(x)
	}
}

func insertRow(t *testing.T, db *sql.DB, table string, cols map[string]any) int64 {
	t.Helper()
	names := []string{`"owner_account"`, `"revision"`, `"created_at"`, `"updated_at"`}
	args := []any{1, 1, 1, 1}
	for c, v := range cols {
		names = append(names, quoteIdent(fieldColumn(c)))
		args = append(args, v)
	}
	marks := strings.TrimSuffix(strings.Repeat("?,", len(args)), ",")
	res, err := db.Exec("INSERT INTO "+quoteIdent(table)+" ("+strings.Join(names, ",")+") VALUES ("+marks+")", args...)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

func columnTypes(t *testing.T, db *sql.DB, table string) map[string]string {
	t.Helper()
	cols, err := tableColumns(context.Background(), db, table)
	if err != nil {
		t.Fatal(err)
	}
	return cols
}

func backups(t *testing.T, dir string) []string {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(dir, "pre-migration-records-*.db"))
	return m
}

// TestMigrate_NewDatabaseReplaysSteps は、新しいDBでも手順を1から当てて表を作り、2回起動しても何も変わらないことを確かめます。
// 不変条件: RECORDS-INV-8
func TestMigrate_NewDatabaseReplaysSteps(t *testing.T) {
	db, dir := testDB(t)
	d := kindDef(t, "visit", add("a", Text), add("b", Integer), rename("a", "c"), change("b", Integer, Text), add("d", Date))
	mustMigrate(t, db, []RecordDef{d})
	got := columnTypes(t, db, "records_visit")
	want := map[string]string{"id": "INTEGER", "owner_account": "INTEGER", "revision": "INTEGER", "created_at": "INTEGER", "updated_at": "INTEGER", "f_c": "TEXT", "f_b": "TEXT", "f_d": "TEXT"}
	if len(got) != len(want) {
		t.Fatalf("columns = %v", got)
	}
	for n, typ := range want {
		if got[n] != typ {
			t.Errorf("%s = %q, want %q", n, got[n], typ)
		}
	}
	if b := backups(t, dir); len(b) != 0 {
		t.Errorf("a new table must not take a backup: %v", b)
	}
	before := snapshot(t, db)
	mustMigrate(t, db, []RecordDef{d})
	if after := snapshot(t, db); after != before {
		t.Errorf("second start changed the database:\n%s\n---\n%s", before, after)
	}
}

// TestMigrate_AddOnlyTakesNoBackup は、追加だけの起動では複製を作らず、既存の値を変えないことを確かめます。
// 不変条件: RECORDS-INV-8
func TestMigrate_AddOnlyTakesNoBackup(t *testing.T) {
	db, dir := testDB(t)
	mustMigrate(t, db, []RecordDef{kindDef(t, "k", add("a", Text))})
	id := insertRow(t, db, "records_k", map[string]any{"a": "x"})
	p := mustMigrate(t, db, []RecordDef{kindDef(t, "k", add("a", Text), add("b", Bool))})
	if p.needsBackup || len(backups(t, dir)) != 0 {
		t.Error("add-only start took a backup")
	}
	var a string
	var b sql.NullInt64
	if err := db.QueryRow(`SELECT "f_a", "f_b" FROM "records_k" WHERE id = ?`, id).Scan(&a, &b); err != nil || a != "x" || b.Valid {
		t.Fatalf("row = %q %v %v", a, b, err)
	}
}

// TestMigrate_BreakingStepsKeepValuesAndTakeOneBackup は、改名・削除・型の変換で値を保ち、起動1回につき複製を1つだけ作ることを確かめます。
// 不変条件: RECORDS-INV-8, RECORDS-INV-17
func TestMigrate_BreakingStepsKeepValuesAndTakeOneBackup(t *testing.T) {
	db, dir := testDB(t)
	mustMigrate(t, db, []RecordDef{kindDef(t, "k", add("a", Text), add("n", Text), add("m", Text)), kindDef(t, "j", add("x", Bool))})
	id := insertRow(t, db, "records_k", map[string]any{"a": "keep", "n": "42", "m": "gone"})
	jid := insertRow(t, db, "records_j", map[string]any{"x": 1})
	empty := insertRow(t, db, "records_k", map[string]any{"a": "e", "n": ""})
	mustMigrate(t, db, []RecordDef{
		kindDef(t, "k", add("a", Text), add("n", Text), add("m", Text), rename("a", "b"), change("n", Text, Integer), remove("m")),
		kindDef(t, "j", add("x", Bool), change("x", Bool, Integer), change("x", Integer, Text)),
	})
	if b := backups(t, dir); len(b) != 1 {
		t.Fatalf("backups = %v, want exactly one", b)
	}
	cols := columnTypes(t, db, "records_k")
	if cols["f_b"] != "TEXT" || cols["f_n"] != "INTEGER" || cols["f_a"] != "" || cols["f_m"] != "" || cols["tmp_n"] != "" {
		t.Fatalf("columns = %v", cols)
	}
	var b string
	var n any
	var typ string
	if err := db.QueryRow(`SELECT "f_b", "f_n", typeof("f_n") FROM "records_k" WHERE id = ?`, id).Scan(&b, &n, &typ); err != nil {
		t.Fatal(err)
	}
	if b != "keep" || n != int64(42) || typ != "integer" {
		t.Errorf("row = %q %v %s", b, n, typ)
	}
	var en sql.NullInt64
	if err := db.QueryRow(`SELECT "f_n" FROM "records_k" WHERE id = ?`, empty).Scan(&en); err != nil || en.Valid {
		t.Errorf("empty string must become NULL: %v %v", en, err)
	}
	var x string
	if err := db.QueryRow(`SELECT "f_x" FROM "records_j" WHERE id = ?`, jid).Scan(&x); err != nil || x != "1" {
		t.Errorf("bool → integer → text = %q %v", x, err)
	}
}

// TestMigrate_SkippedVersionsReplayInOrder は、途中の版を飛ばした環境でも、足した項目への手順を含めて1段ずつ当たることを確かめます。
// 不変条件: RECORDS-INV-8
func TestMigrate_SkippedVersionsReplayInOrder(t *testing.T) {
	db, _ := testDB(t)
	mustMigrate(t, db, []RecordDef{kindDef(t, "k", add("a", Text))})
	id := insertRow(t, db, "records_k", map[string]any{"a": "7"})
	// 版2で b を足し、版3で b を c に改名し、版4で a を整数に、版5で c を消す。版2〜4を飛ばして版5を起動する
	mustMigrate(t, db, []RecordDef{kindDef(t, "k", add("a", Text), add("b", Bool), rename("b", "c"), change("a", Text, Integer), remove("c"))})
	var a int64
	if err := db.QueryRow(`SELECT "f_a" FROM "records_k" WHERE id = ?`, id).Scan(&a); err != nil || a != 7 {
		t.Fatalf("a = %d %v", a, err)
	}
	var steps int
	db.QueryRow(`SELECT COUNT(*) FROM records_steps WHERE table_name = 'records_k'`).Scan(&steps)
	if steps != 5 {
		t.Errorf("step log has %d rows", steps)
	}
}

// TestMigrate_ConversionFailureChangesNothing は、変換できない値が1行でもあれば、何も変えずに止まり、エラーに値を出さないことを確かめます。
// 不変条件: RECORDS-INV-17, RECORDS-INV-8, RECORDS-INV-13
func TestMigrate_ConversionFailureChangesNothing(t *testing.T) {
	db, _ := testDB(t)
	mustMigrate(t, db, []RecordDef{kindDef(t, "k", add("a", Text), add("n", Text))})
	insertRow(t, db, "records_k", map[string]any{"a": "x", "n": "2026-01-02"})
	bad := insertRow(t, db, "records_k", map[string]any{"a": "y", "n": "来週火曜-SECRET"})
	before := snapshot(t, db)
	_, err := migrate(t, db, []RecordDef{kindDef(t, "k", add("a", Text), add("n", Text), rename("a", "z"), change("n", Text, Date))})
	var cf convertFailure
	if !errors.As(err, &cf) || len(cf.IDs) != 1 || cf.IDs[0] != bad {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), "来週") {
		t.Errorf("error leaks the value: %v", err)
	}
	if after := snapshot(t, db); after != before {
		t.Errorf("a failed migration changed the database:\n%s\n---\n%s", before, after)
	}
}

// TestConvertValue_Shapes は、型の変換で値の形だけを見ることと、境界を確かめます。
// 不変条件: RECORDS-INV-17
func TestConvertValue_Shapes(t *testing.T) {
	cases := []struct {
		from, to FieldType
		in       any
		want     any
		ok       bool
	}{
		{Text, Date, "2026-11-06", "2026-11-06", true},
		{Text, Date, "2026-02-30", nil, false},
		{Text, Date, "2026/11/06", nil, false},
		{Text, Date, "2026-1-6", nil, false},
		{Text, Integer, "-12", int64(-12), true},
		{Text, Integer, "0", int64(0), true},
		{Text, Integer, "007", nil, false},
		{Text, Integer, "+1", nil, false},
		{Text, Integer, " 1", nil, false},
		{Text, Integer, "-0", nil, false},
		{Text, Integer, "9223372036854775808", nil, false},
		{Text, Integer, "1.5", nil, false},
		{LongText, Text, "a\nb", nil, false},
		{LongText, Text, "a\tb", nil, false},
		{LongText, Text, "ab", "ab", true},
		{Integer, Bool, int64(1), int64(1), true},
		{Integer, Bool, int64(2), nil, false},
		{Integer, Text, int64(-3), "-3", true},
		{Bool, Integer, int64(0), int64(0), true},
		{Text, Select, "どれでも", "どれでも", true},
		{Text, Integer, nil, nil, true},
		{Text, Integer, "", nil, true},
		{Date, Integer, "2026-11-06", nil, false},
	}
	for _, c := range cases {
		got, ok := convertValue(c.from, c.to, c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("%s→%s %#v = %#v %v, want %#v %v", c.from, c.to, c.in, got, ok, c.want, c.ok)
		}
	}
}

// TestMigrate_OlderBinaryStopsAfterBreakingSteps は、壊す手順を当てたDBを前の版が起動すると止まり、
// 追加だけなら警告で続け、値を失わないことを確かめます。
// 不変条件: RECORDS-INV-15
func TestMigrate_OlderBinaryStopsAfterBreakingSteps(t *testing.T) {
	v1 := kindDef(t, "k", add("a", Text))
	v2 := kindDef(t, "k", add("a", Text), add("b", Text))
	v3 := kindDef(t, "k", add("a", Text), add("b", Text), rename("b", "c"))

	db, _ := testDB(t)
	mustMigrate(t, db, []RecordDef{v2})
	id := insertRow(t, db, "records_k", map[string]any{"a": "x", "b": "kept"})
	p := mustMigrate(t, db, []RecordDef{v1}) // 追加だけの版から前の版へ
	if len(p.warnings) == 0 {
		t.Error("an unknown add must warn")
	}
	mustMigrate(t, db, []RecordDef{v2}) // また新しい版へ
	var b string
	if err := db.QueryRow(`SELECT "f_b" FROM "records_k" WHERE id = ?`, id).Scan(&b); err != nil || b != "kept" {
		t.Fatalf("b = %q %v", b, err)
	}

	mustMigrate(t, db, []RecordDef{v3})
	before := snapshot(t, db)
	for name, old := range map[string]RecordDef{"v1": v1, "v2": v2} {
		if _, err := migrate(t, db, []RecordDef{old}); err == nil || !strings.Contains(err.Error(), "does not know") {
			t.Errorf("%s after a rename: err = %v", name, err)
		}
	}
	if after := snapshot(t, db); after != before {
		t.Error("a stopped start changed the database")
	}
}

// TestMigrate_TypeRoundTripMiddleVersionStops は、文字 → 整数 → 文字の往復を当てた後に、中間の版を起動すると止まることを確かめます。
// 不変条件: RECORDS-INV-15
func TestMigrate_TypeRoundTripMiddleVersionStops(t *testing.T) {
	db, _ := testDB(t)
	mustMigrate(t, db, []RecordDef{kindDef(t, "k", add("a", Text), change("a", Text, Integer), change("a", Integer, Text))})
	if _, err := migrate(t, db, []RecordDef{kindDef(t, "k", add("a", Text), change("a", Text, Integer))}); err == nil {
		t.Error("the middle version started")
	}
}

// TestMigrate_DivergedDefinitionsStop は、同じ番号で中身の違う手順（分岐したブランチ）を、最後の項目が同じ名前と型になる組でも止めることを確かめます。
// 不変条件: RECORDS-INV-15
func TestMigrate_DivergedDefinitionsStop(t *testing.T) {
	db, _ := testDB(t)
	branchA := kindDef(t, "k", add("a", Text), add("b", Text), rename("a", "x"), remove("b"))
	branchB := kindDef(t, "k", add("a", Text), add("b", Text), rename("b", "x"), remove("a"))
	mustMigrate(t, db, []RecordDef{branchA})
	if _, err := migrate(t, db, []RecordDef{branchB}); err == nil || !strings.Contains(err.Error(), "diverged") {
		t.Errorf("err = %v", err)
	}
	db2, _ := testDB(t)
	mustMigrate(t, db2, []RecordDef{kindDef(t, "k", add("a", Text), add("b", Text))})
	mustMigrate(t, db2, []RecordDef{branchA})
	more := kindDef(t, "k", add("a", Text), add("b", Text), rename("b", "x"), remove("a"), add("c", Text))
	if _, err := migrate(t, db2, []RecordDef{more}); err == nil {
		t.Error("a diverged definition with more steps started")
	}
}

// TestMigrate_DriftStops は、記録と実際の表・列のずれを、自動で直さずに止めることを確かめます。
// 不変条件: RECORDS-INV-16
func TestMigrate_DriftStops(t *testing.T) {
	d := kindDef(t, "k", add("a", Text), add("b", Integer))
	cases := map[string]string{
		"記録に無い列":      `ALTER TABLE "records_k" ADD COLUMN "f_z" TEXT`,
		"あるはずの列が無い":   `ALTER TABLE "records_k" DROP COLUMN "f_b"`,
		"記録の無い表":      `CREATE TABLE "records_other" (id INTEGER)`,
		"番号の飛び":       `INSERT INTO records_steps (table_name, version, step) VALUES ('records_k', 4, 'add:z:text')`,
		"番号の飛び（列もある）": `INSERT INTO records_steps (table_name, version, step) VALUES ('records_k', 4, 'add:z:text'); ALTER TABLE "records_k" ADD COLUMN "f_z" TEXT`,
		"表の無い記録":      `DROP TABLE "records_k"`,
		"共通の列が無い":     `ALTER TABLE "records_k" DROP COLUMN "revision"`,
		"読めない手順":      `INSERT INTO records_steps (table_name, version, step) VALUES ('records_k', 3, 'split:a')`,
		"知らない種類の記録":   `INSERT INTO records_tables (table_name, state) VALUES ('records_ghost', 'active')`,
	}
	for name, sqlText := range cases {
		db, _ := testDB(t)
		mustMigrate(t, db, []RecordDef{d})
		if _, err := db.Exec(sqlText); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if _, err := migrate(t, db, []RecordDef{d}); err == nil {
			t.Errorf("%s: started", name)
		}
	}
	// 型の宣言の違い: 手で表を作り直したDB
	db, _ := testDB(t)
	mustMigrate(t, db, []RecordDef{d})
	for _, s := range []string{`ALTER TABLE "records_k" DROP COLUMN "f_b"`, `ALTER TABLE "records_k" ADD COLUMN "f_b" TEXT`} {
		if _, err := db.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := migrate(t, db, []RecordDef{d}); err == nil || !strings.Contains(err.Error(), "is TEXT, not INTEGER") {
		t.Errorf("declared type drift: %v", err)
	}
}

// TestMigrate_RetiredAndUnknownKinds は、やめた種類、表を消した種類、定義に無い種類の扱いを確かめます。
// 不変条件: RECORDS-INV-15, RECORDS-INV-16
func TestMigrate_RetiredAndUnknownKinds(t *testing.T) {
	k := kindDef(t, "k", add("a", Text))
	other := kindDef(t, "other", add("x", Text))

	// 新しい版が足した種類は、手順の中身を問わず警告で残す
	db, _ := testDB(t)
	mustMigrate(t, db, []RecordDef{k, kindDef(t, "other", add("x", Text), rename("x", "y"))})
	p := mustMigrate(t, db, []RecordDef{k})
	if !strings.Contains(strings.Join(p.warnings, " "), "records_other") {
		t.Errorf("warnings = %v", p.warnings)
	}

	// やめた種類は表を残す。消した印があるのに表が残っていれば警告する
	p = mustMigrate(t, db, []RecordDef{k}, RetiredDef{Name: "other"})
	if len(p.warnings) != 0 {
		t.Errorf("retired, not dropped: warnings = %v", p.warnings)
	}
	p = mustMigrate(t, db, []RecordDef{k}, RetiredDef{Name: "other", Dropped: true})
	if !strings.Contains(strings.Join(p.warnings, " "), "records-drop") {
		t.Errorf("dropped mark with a table: warnings = %v", p.warnings)
	}

	// 表を消した種類を、その種類を使う版が起動すると止まる
	if _, err := db.Exec(`DROP TABLE "records_other"`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE records_tables SET state = 'dropped' WHERE table_name = 'records_other'`); err != nil {
		t.Fatal(err)
	}
	if _, err := migrate(t, db, []RecordDef{k, other}); err == nil || !strings.Contains(err.Error(), "dropped") {
		t.Errorf("a dropped kind started: %v", err)
	}
	mustMigrate(t, db, []RecordDef{k}, RetiredDef{Name: "other", Dropped: true})

	// やめた後に作った新しいDBには、やめた種類の記録を作らない。前の版はその種類を空で作って動く
	db2, _ := testDB(t)
	mustMigrate(t, db2, []RecordDef{k}, RetiredDef{Name: "other", Dropped: true})
	var n int
	db2.QueryRow(`SELECT COUNT(*) FROM records_tables WHERE table_name = 'records_other'`).Scan(&n)
	if n != 0 {
		t.Error("a new database recorded a retired kind")
	}
	mustMigrate(t, db2, []RecordDef{k, other})
}

// TestBuildRecordStatements_QuotesEveryIdentifier は、組み立てた文の中の表名・列名が、全部引用符で囲まれていることを確かめます。
// 名前は規則で縛ってあるが、文に入れる時も必ず引用符で囲む（二重の守り）。
// 不変条件: RECORDS-INV-7
func TestBuildRecordStatements_QuotesEveryIdentifier(t *testing.T) {
	d := kindDef(t, "k", add("a", Text), rename("a", "b"), change("b", Text, Integer), remove("b"))
	ks := buildRecordStatements([]RecordDef{d}, []RetiredDef{{Name: "old"}})
	var all []recordsStatement
	for _, k := range ks {
		all = append(all, k.create, k.drop)
		for _, s := range k.steps {
			all = append(all, s.apply...)
			all = append(all, s.addTmp, s.selectOld, s.updateTmp, s.dropOld, s.renameTmp)
		}
		for _, sel := range k.selectColumn {
			all = append(all, sel)
		}
	}
	ident := regexp.MustCompile(`"?\b(records_[a-z0-9_]+|f_[a-z0-9_]+|tmp_[a-z0-9_]+|id|owner_account|revision|created_at|updated_at)\b"?`)
	n := 0
	for _, st := range all {
		for _, m := range ident.FindAllString(st.sql, -1) {
			n++
			if !strings.HasPrefix(m, `"`) || !strings.HasSuffix(m, `"`) {
				t.Errorf("unquoted identifier %s in %s", m, st.sql)
			}
		}
	}
	if n < 20 {
		t.Fatalf("checked only %d identifiers", n)
	}
}
