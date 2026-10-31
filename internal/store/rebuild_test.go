package store

import (
	"context"
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
