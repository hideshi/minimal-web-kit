package records

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// inventoryFiles は、試験用の定義の記録と台帳を一時の場所に作ります。
func inventoryFiles(t *testing.T) (lock, inv string) {
	t.Helper()
	dir := t.TempDir()
	lock, inv = filepath.Join(dir, "records.lock.json"), filepath.Join(dir, "inventory.md")
	if err := os.WriteFile(inv, []byte("# 台帳\n\n手書きの部分\n\n"+inventoryBegin+"\n"+inventoryEnd+"\n\n後ろの手書き\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return lock, inv
}

// TestInventory_HistoryCheck は、前の定義の記録と比べて、適用済みの手順の変更・削除、種類の消失、やめた種類の削除を止めることを確かめます。
// 不変条件: RECORDS-INV-9
// ユースケース: UC-RECORDS-7
func TestInventory_HistoryCheck(t *testing.T) {
	v1 := kindDef(t, "k", add("a", Text), add("b", Text))
	lock, inv := inventoryFiles(t)
	if err := UpdateInventory(lock, inv, []RecordDef{v1}, nil, false); err != nil {
		t.Fatal(err)
	}
	cases := map[string]struct {
		defs    []RecordDef
		retired []RetiredDef
	}{
		"手順を書き換える":       {[]RecordDef{kindDef(t, "k", add("a", Text), add("b", Integer))}, nil},
		"末尾の手順を消す":       {[]RecordDef{kindDef(t, "k", add("a", Text))}, nil},
		"途中に手順を挟む":       {[]RecordDef{kindDef(t, "k", add("a", Text), add("x", Text), add("b", Text))}, nil},
		"種類を黙って消す":       {nil, nil},
		"改名を足して型を黙って変える": {[]RecordDef{kindDef(t, "k", add("a", Text), add("c", Integer))}, nil},
	}
	for name, c := range cases {
		if err := UpdateInventory(lock, inv, c.defs, c.retired, false); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// 正しい続き: 手順を積む
	v2 := kindDef(t, "k", add("a", Text), add("b", Text), rename("b", "c"), change("c", Text, Integer))
	if err := UpdateInventory(lock, inv, []RecordDef{v2}, nil, false); err != nil {
		t.Fatalf("appending steps: %v", err)
	}
	// やめた種類に移すと、手順の文字列は定義の記録に残る
	if err := UpdateInventory(lock, inv, nil, []RetiredDef{{Name: "k"}}, false); err != nil {
		t.Fatalf("retiring: %v", err)
	}
	l, err := readLock(lock)
	if err != nil || len(l.Retired) != 1 || len(l.Retired[0].Steps) != 4 {
		t.Fatalf("retired steps not carried: %+v %v", l, err)
	}
	// やめた種類を一覧から消すと止まる
	if err := UpdateInventory(lock, inv, nil, nil, false); err == nil {
		t.Error("removing a retired kind: accepted")
	}
	// どの環境にもまだデプロイしていない間は、記録を作り直してよい
	if err := UpdateInventory(lock, inv, []RecordDef{kindDef(t, "k", add("z", Date))}, nil, true); err != nil {
		t.Fatalf("reset: %v", err)
	}
}

// TestInventory_CheckDetectsDrift は、定義の記録と台帳の節が今の定義とずれた時と、印の間を手で直した時に、照合が落ちることを確かめます。
// 不変条件: RECORDS-INV-9
func TestInventory_CheckDetectsDrift(t *testing.T) {
	d := kindDef(t, "k", add("a", Text))
	lock, inv := inventoryFiles(t)
	if err := UpdateInventory(lock, inv, []RecordDef{d}, nil, false); err != nil {
		t.Fatal(err)
	}
	if err := CheckInventory(lock, inv, []RecordDef{d}, nil); err != nil {
		t.Fatalf("fresh files: %v", err)
	}
	doc, _ := os.ReadFile(inv)
	if !strings.Contains(string(doc), "手書きの部分") || !strings.Contains(string(doc), "後ろの手書き") || !strings.Contains(string(doc), "### テーブル: `records_k`") {
		t.Fatalf("section was not placed between the markers:\n%s", doc)
	}

	// 定義だけ足して作り直さない
	d2 := kindDef(t, "k", add("a", Text), add("b", Text))
	if err := CheckInventory(lock, inv, []RecordDef{d2}, nil); err == nil {
		t.Error("definition changed without regenerating: accepted")
	}
	// 印の間を手で直す
	edited := strings.Replace(string(doc), "1行の文字", "何でも", 1)
	os.WriteFile(inv, []byte(edited), 0o644)
	if err := CheckInventory(lock, inv, []RecordDef{d}, nil); err == nil {
		t.Error("hand edit between the markers: accepted")
	}
	// 印が2組
	os.WriteFile(inv, append(doc, []byte("\n"+inventoryBegin+"\n"+inventoryEnd+"\n")...), 0o644)
	if err := CheckInventory(lock, inv, []RecordDef{d}, nil); err == nil {
		t.Error("two sets of markers: accepted")
	}
	// 定義の記録だけが古い（台帳の節は合っている）
	os.WriteFile(inv, doc, 0o644)
	os.WriteFile(lock, []byte(`{"kinds":[],"retired":[]}`+"\n"), 0o644)
	if err := CheckInventory(lock, inv, []RecordDef{d}, nil); err == nil {
		t.Error("stale lock: accepted")
	}
	// 定義の記録を手で直す
	os.WriteFile(lock, []byte(`{"kinds":[{"name":"k","steps":["add:a:text"]}],"retired":[],"extra":1}`), 0o644)
	if err := CheckInventory(lock, inv, []RecordDef{d}, nil); err == nil {
		t.Error("unknown field in the lock: accepted")
	}
}
