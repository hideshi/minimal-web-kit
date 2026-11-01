package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 作り直した表をもう一度作り直せることを確かめる（改名の後は、定義の名前に引用符が付く）。
// 不変条件: AUTH-INV-27
func TestRebuildTable_Twice(t *testing.T) {
	ctx := context.Background()
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "rebuild.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	db := s.DB()
	if _, err := db.Exec("CREATE TABLE sample (id INTEGER PRIMARY KEY AUTOINCREMENT, kind TEXT NOT NULL CHECK (kind IN ('a'))); INSERT INTO sample (kind) VALUES ('a')"); err != nil {
		t.Fatal(err)
	}
	step := func(from, to string) Rebuild {
		return Rebuild{
			Table: "sample", Label: "sample",
			Current: func(d string) bool { return strings.Contains(d, to) },
			Define:  func(d string) (string, error) { return strings.Replace(d, from, to, 1), nil },
			Columns: map[string]string{"kind": "CASE kind WHEN 'a' THEN 'b' ELSE kind END"},
			After:   "SELECT 1",
		}
	}
	if err := RebuildTable(ctx, db, step("IN ('a')", "IN ('b')")); err != nil {
		t.Fatal(err)
	}
	second := step("IN ('b')", "IN ('c')")
	second.Label = "sample-second" // 複製の名前は秒までなので、同じ秒に同じ語で作れない
	second.Columns = map[string]string{"kind": "CASE kind WHEN 'b' THEN 'c' ELSE kind END"}
	if err := RebuildTable(ctx, db, second); err != nil {
		t.Fatal(err)
	}
	var kind string
	if err := db.QueryRow("SELECT kind FROM sample").Scan(&kind); err != nil || kind != "c" {
		t.Fatalf("kind = %q, %v", kind, err)
	}
}

const rebuildSchema = `
CREATE TABLE parent (id INTEGER PRIMARY KEY AUTOINCREMENT, kind TEXT NOT NULL CHECK (kind IN ('a', 'b')));
CREATE TABLE child (id INTEGER PRIMARY KEY, parent_id INTEGER NOT NULL REFERENCES parent(id) ON DELETE CASCADE);
CREATE TABLE audit (note TEXT);
CREATE TRIGGER parent_audit AFTER UPDATE ON parent BEGIN INSERT INTO audit (note) VALUES ('updated'); END;
INSERT INTO parent (kind) VALUES ('a'), ('b');
INSERT INTO child (parent_id) VALUES (1), (2);
`

const rebuildTrigger = `CREATE TRIGGER parent_audit AFTER UPDATE ON parent BEGIN INSERT INTO audit (note) VALUES ('updated'); END`

// rebuildDB は、1本の接続だけを持つDBを返します。外部キーの設定が接続に残っていないかを同じ接続で読むためです。
func rebuildDB(t *testing.T, schema string) *sql.DB {
	t.Helper()
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "rebuild.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	db := s.DB()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		t.Fatal(err)
	}
	return db
}

// widen は、CHECK に 'c' を足す作り直しです。
func widen() Rebuild {
	return Rebuild{
		Table: "parent", Label: "parent",
		Current: func(d string) bool { return strings.Contains(d, "'c'") },
		Define: func(d string) (string, error) {
			return strings.Replace(d, "IN ('a', 'b')", "IN ('a', 'b', 'c')", 1), nil
		},
		After: rebuildTrigger,
	}
}

func queryInt(t *testing.T, db *sql.DB, q string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(q).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

// assertUntouched は、失敗の後に表が元の定義と行のままで、作業用の表が残らず、外部キーが有効であることを確かめます。
func assertUntouched(t *testing.T, db *sql.DB) {
	t.Helper()
	definition, err := tableSQL(context.Background(), db, "parent")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(definition, "'c'") {
		t.Errorf("definition changed: %s", definition)
	}
	if n := queryInt(t, db, "SELECT COUNT(*) FROM parent"); n != 2 {
		t.Errorf("parent rows = %d, want 2", n)
	}
	if n := queryInt(t, db, "SELECT COUNT(*) FROM child"); n != 2 {
		t.Errorf("child rows = %d, want 2", n)
	}
	if n := queryInt(t, db, "SELECT COUNT(*) FROM sqlite_master WHERE name = 'parent_new'"); n != 0 {
		t.Error("work table left behind")
	}
	if n := queryInt(t, db, "SELECT COUNT(*) FROM sqlite_master WHERE type = 'trigger' AND name = 'parent_audit'"); n != 1 {
		t.Error("trigger lost")
	}
	assertForeignKeysOn(t, db)
}

func assertForeignKeysOn(t *testing.T, db *sql.DB) {
	t.Helper()
	if n := queryInt(t, db, "PRAGMA foreign_keys"); n != 1 {
		t.Errorf("foreign_keys = %d, want 1", n)
	}
	if n := queryInt(t, db, "SELECT COUNT(*) FROM pragma_foreign_key_check"); n != 0 {
		t.Errorf("%d rows broke foreign keys", n)
	}
}

// 途中で失敗したら、表は元のまま残り、外部キーの止まった接続も残らないことを確かめる。
// 不変条件: AUTH-INV-27
func TestRebuildTable_FailureLeavesTableUntouched(t *testing.T) {
	cases := []struct {
		name   string
		change func(*Rebuild)
		want   string
	}{
		{"定義を直せない", func(r *Rebuild) {
			r.Define = func(string) (string, error) { return "", errors.New("unknown shape") }
		}, "unknown shape"},
		{"定義の形が想定と違う", func(r *Rebuild) {
			r.Define = func(string) (string, error) { return "CREATE TEMP TABLE parent (id INTEGER)", nil }
		}, "unexpected definition"},
		{"既存の行が新しいCHECKに合わない", func(r *Rebuild) {
			r.Define = func(d string) (string, error) { return strings.Replace(d, "IN ('a', 'b')", "IN ('c')", 1), nil }
		}, "CHECK"},
		{"値を変える式が誤り", func(r *Rebuild) {
			r.Columns = map[string]string{"kind": "no_such_column"}
		}, "no_such_column"},
		{"トリガーを作り直せない", func(r *Rebuild) {
			r.After = "CREATE TRIGGER broken"
		}, "incomplete input"},
		{"参照する行が親を失う", func(r *Rebuild) {
			r.Columns = map[string]string{"id": "id + 100"}
		}, "lost their reference"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			db := rebuildDB(t, rebuildSchema)
			r := widen()
			c.change(&r)
			err := RebuildTable(context.Background(), db, r)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
			assertUntouched(t, db)
		})
	}
}

// 複製を作れなければ、表に触らないことを確かめる。
func TestRebuildTable_NoBackupNoChange(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	db := rebuildDB(t, rebuildSchema)
	var seq int
	var name, file string
	if err := db.QueryRow("PRAGMA database_list").Scan(&seq, &name, &file); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(file)
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })
	err := RebuildTable(context.Background(), db, widen())
	if err == nil || !strings.Contains(err.Error(), "backup before migration") {
		t.Fatalf("err = %v", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	assertUntouched(t, db)
}

// 他の表から CASCADE で参照されている表でも、参照する行とトリガーが残り、採番が巻き戻らないことを確かめる。
// 不変条件: AUTH-INV-27
func TestRebuildTable_KeepsReferencesTriggerAndSequence(t *testing.T) {
	db := rebuildDB(t, rebuildSchema+"DELETE FROM child WHERE parent_id = 2; DELETE FROM parent WHERE id = 2;")
	if err := RebuildTable(context.Background(), db, widen()); err != nil {
		t.Fatal(err)
	}
	if n := queryInt(t, db, "SELECT COUNT(*) FROM child WHERE parent_id = 1"); n != 1 {
		t.Errorf("child rows = %d, want 1", n)
	}
	assertForeignKeysOn(t, db)
	if _, err := db.Exec("INSERT INTO parent (kind) VALUES ('c')"); err != nil {
		t.Fatalf("new CHECK: %v", err)
	}
	if n := queryInt(t, db, "SELECT MAX(id) FROM parent"); n != 3 {
		t.Errorf("new id = %d, want 3 (deleted id reused?)", n)
	}
	if _, err := db.Exec("UPDATE parent SET kind = 'b' WHERE id = 1"); err != nil {
		t.Fatal(err)
	}
	if n := queryInt(t, db, "SELECT COUNT(*) FROM audit"); n != 1 {
		t.Errorf("trigger did not fire: %d", n)
	}
	// 外部キーが戻っていれば、親を消すと参照する行も消える
	if _, err := db.Exec("DELETE FROM parent WHERE id = 1"); err != nil {
		t.Fatal(err)
	}
	if n := queryInt(t, db, "SELECT COUNT(*) FROM child"); n != 0 {
		t.Errorf("cascade did not run: %d", n)
	}
}

// 採番の記録が無い表（AUTOINCREMENT でない表）も作り直せることを確かめる。
func TestRebuildTable_WithoutAutoincrement(t *testing.T) {
	db := rebuildDB(t, "CREATE TABLE plain (id INTEGER PRIMARY KEY, kind TEXT CHECK (kind IN ('a'))); INSERT INTO plain (kind) VALUES ('a');")
	err := RebuildTable(context.Background(), db, Rebuild{
		Table: "plain", Label: "plain",
		Current: func(d string) bool { return strings.Contains(d, "'b'") },
		Define:  func(d string) (string, error) { return strings.Replace(d, "IN ('a')", "IN ('a', 'b')", 1), nil },
		After:   "SELECT 1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if n := queryInt(t, db, "SELECT COUNT(*) FROM plain"); n != 1 {
		t.Errorf("rows = %d, want 1", n)
	}
	if n := queryInt(t, db, "SELECT COUNT(*) FROM sqlite_master WHERE sql LIKE '%''b''%' AND name = 'plain'"); n != 1 {
		t.Error("definition not changed")
	}
}

// 作り直しが要らないときは、複製を作らず、何も変えないことを確かめる。
func TestRebuildTable_NothingToDo(t *testing.T) {
	cases := []struct {
		name   string
		change func(*Rebuild)
	}{
		{"表が無い", func(r *Rebuild) { r.Table = "no_such_table" }},
		{"今の形式", func(r *Rebuild) { r.Current = func(string) bool { return true } }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			db := rebuildDB(t, rebuildSchema)
			r := widen()
			r.Define = func(string) (string, error) { t.Error("Define must not run"); return "", nil }
			c.change(&r)
			if err := RebuildTable(context.Background(), db, r); err != nil {
				t.Fatal(err)
			}
			var seq int
			var name, file string
			if err := db.QueryRow("PRAGMA database_list").Scan(&seq, &name, &file); err != nil {
				t.Fatal(err)
			}
			backups, err := filepath.Glob(filepath.Join(filepath.Dir(file), "pre-migration-*"))
			if err != nil || len(backups) != 0 {
				t.Errorf("backups = %v, %v", backups, err)
			}
			assertUntouched(t, db)
		})
	}
}

// 書き込みの権利を得るまでに別のプロセスが移行を済ませていたら、何もしないことを確かめる。
func TestRebuildTable_AnotherProcessFinishedFirst(t *testing.T) {
	db := rebuildDB(t, rebuildSchema)
	reads := 0
	r := widen()
	r.Current = func(string) bool { reads++; return reads > 1 }
	r.Define = func(string) (string, error) { t.Error("Define must not run"); return "", nil }
	if err := RebuildTable(context.Background(), db, r); err != nil {
		t.Fatal(err)
	}
	if reads != 2 {
		t.Errorf("Current ran %d times, want 2", reads)
	}
	assertUntouched(t, db)
}
