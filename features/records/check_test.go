package records

import (
	"bytes"
	"context"
	"crypto/sha256"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fileHash(t *testing.T, path string) [32]byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(b)
}

func check(t *testing.T, dbPath string, defs []RecordDef, retired ...RetiredDef) string {
	t.Helper()
	var out bytes.Buffer
	if err := runRecordsCheck(context.Background(), dbPath, defs, retired, &out); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

// TestRecordsCheck_ReportsPlanWithoutChangingTheDatabase は、確認のコマンドが、当てる手順・変換できない行・戻し方を出し、
// 書き込む接続が無い状態で、DBの本体のファイルを1バイトも変えないことを確かめます。値は出しません。
// 不変条件: RECORDS-INV-14, RECORDS-INV-13
func TestRecordsCheck_ReportsPlanWithoutChangingTheDatabase(t *testing.T) {
	db, dir := testDB(t)
	path := filepath.Join(dir, "app.db")
	mustMigrate(t, db, []RecordDef{kindDef(t, "k", add("a", Text), add("n", Text))})
	insertRow(t, db, "records_k", map[string]any{"a": "x", "n": "2026-01-02"})
	bad := insertRow(t, db, "records_k", map[string]any{"a": "y", "n": "来週-SECRET"})
	if _, err := db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	before := fileHash(t, path)

	next := kindDef(t, "k", add("a", Text), add("n", Text), rename("n", "m"), change("m", Text, Date), add("z", Bool))
	out := check(t, path, []RecordDef{next})
	for _, want := range []string{"records_k: step 2 -> 5", "rename:n:m", "CANNOT CONVERT", "records_k.m", "id " + itoaID(bad), "will NOT start"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "SECRET") || strings.Contains(out, "来週") {
		t.Errorf("output leaks a value:\n%s", out)
	}
	if fileHash(t, path) != before {
		t.Error("records-check changed the database file")
	}

	// 追加だけなら、実行ファイルだけ戻せばよい
	out = check(t, path, []RecordDef{kindDef(t, "k", add("a", Text), add("n", Text), add("z", Bool))})
	if !strings.Contains(out, "Rollback: the executable only.") {
		t.Errorf("add-only rollback:\n%s", out)
	}
	// 変換できる壊す手順なら、DBも戻す
	out = check(t, path, []RecordDef{kindDef(t, "k", add("a", Text), add("n", Text), rename("a", "b"))})
	if !strings.Contains(out, "restore the database") || strings.Contains(out, "NOT start") {
		t.Errorf("breaking rollback:\n%s", out)
	}
	// 分岐した定義は止まると出す
	out = check(t, path, []RecordDef{kindDef(t, "k", add("a", Text), add("q", Text))})
	if !strings.Contains(out, "STOP:") || !strings.Contains(out, "diverged") {
		t.Errorf("diverged:\n%s", out)
	}
	if fileHash(t, path) != before {
		t.Error("records-check changed the database file")
	}
}

// TestRecordsCheck_FollowsRenamesAndChains は、改名の後の変換や、同じ項目への2回の変換を、名前をたどって試すことを確かめます。
// 不変条件: RECORDS-INV-14
func TestRecordsCheck_FollowsRenamesAndChains(t *testing.T) {
	db, dir := testDB(t)
	path := filepath.Join(dir, "app.db")
	mustMigrate(t, db, []RecordDef{kindDef(t, "k", add("a", Text))})
	insertRow(t, db, "records_k", map[string]any{"a": "1"})
	two := insertRow(t, db, "records_k", map[string]any{"a": "2"})
	// a を b に改名し、整数にし、はい・いいえにする。1 はできて、2 ははい・いいえにできない
	out := check(t, path, []RecordDef{kindDef(t, "k", add("a", Text), rename("a", "b"), change("b", Text, Integer), change("b", Integer, Bool))})
	if !strings.Contains(out, "records_k.b") || !strings.Contains(out, "(id "+itoaID(two)) {
		t.Errorf("chain:\n%s", out)
	}
	if strings.Count(out, "CANNOT CONVERT") != 2-1 {
		t.Errorf("each row must be reported once, at the step that fails:\n%s", out)
	}
}

// TestRecordsCheck_MissingDatabase は、DBのファイルが無い時に、全部を作ると出し、ファイルを作らないことを確かめます。
// 不変条件: RECORDS-INV-14
func TestRecordsCheck_MissingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "none.db")
	out := check(t, path, []RecordDef{kindDef(t, "k", add("a", Text))})
	if !strings.Contains(out, "does not exist") {
		t.Errorf("output:\n%s", out)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("records-check created the database file")
	}
}

// TestRecordsCheck_WhileAnotherConnectionWrites は、別の接続が書き込み中でも読めて、表と行を変えないことを確かめます。
// 不変条件: RECORDS-INV-14
func TestRecordsCheck_WhileAnotherConnectionWrites(t *testing.T) {
	db, dir := testDB(t)
	path := filepath.Join(dir, "app.db")
	d := kindDef(t, "k", add("a", Text))
	mustMigrate(t, db, []RecordDef{d})
	insertRow(t, db, "records_k", map[string]any{"a": "x"})
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO "records_k" ("owner_account","revision","created_at","updated_at","f_a") VALUES (1,1,1,1,'pending')`); err != nil {
		t.Fatal(err)
	}
	before := snapshot(t, db)
	out := check(t, path, []RecordDef{d})
	if !strings.Contains(out, "up to date") {
		t.Errorf("output:\n%s", out)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if after := snapshot(t, db); after != before {
		t.Error("records-check changed tables or rows")
	}
}

// TestRecordsDrop_DropsOnlyRetiredKinds は、表を消すコマンドが、やめた種類だけを、複製を作ってから消し、2回目は何もしないことを確かめます。
// 不変条件: RECORDS-INV-8
func TestRecordsDrop_DropsOnlyRetiredKinds(t *testing.T) {
	db, dir := testDB(t)
	k, old := kindDef(t, "k", add("a", Text)), kindDef(t, "old", add("x", Text))
	mustMigrate(t, db, []RecordDef{k, old})
	var out bytes.Buffer
	if err := dropRecordsTable(context.Background(), db, "k", []RecordDef{k}, []RetiredDef{{Name: "old"}}, &out); err == nil {
		t.Error("dropped a kind that is not retired")
	}
	if err := dropRecordsTable(context.Background(), db, "old", []RecordDef{k}, []RetiredDef{{Name: "old"}}, &out); err != nil {
		t.Fatal(err)
	}
	m, _ := filepath.Glob(filepath.Join(dir, "pre-migration-records-drop-*.db"))
	if len(m) != 1 || !strings.Contains(out.String(), filepath.Base(m[0])) {
		t.Errorf("backup %v, output %q", m, out.String())
	}
	var state string
	db.QueryRow(`SELECT state FROM records_tables WHERE table_name = 'records_old'`).Scan(&state)
	if state != "dropped" || columnTypes(t, db, "records_old")["id"] != "" {
		t.Errorf("state = %q", state)
	}
	out.Reset()
	if err := dropRecordsTable(context.Background(), db, "old", []RecordDef{k}, []RetiredDef{{Name: "old"}}, &out); err != nil || !strings.Contains(out.String(), "nothing to do") {
		t.Errorf("second drop: %v %q", err, out.String())
	}
	// 表を消した後、その種類を使う版は起動を止める
	if _, err := migrate(t, db, []RecordDef{k, old}); err == nil {
		t.Error("a version using the dropped kind started")
	}
}

func itoaID(n int64) string { return strings.TrimSpace(strings.TrimPrefix(fmtAny(n), "'")) }
