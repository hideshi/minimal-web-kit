package records

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
)

// recordsLock は、定義の記録（records.lock.json）です。最後にレビューを通った定義の、種類ごとの手順の文字列を持ちます。
// コマンドが書き、人は直しません。Markdown は読まず、開発の時の前の定義との比較にはこれだけを使います。
type recordsLock struct {
	Kinds   []lockKind `json:"kinds"`
	Retired []lockKind `json:"retired"`
}

type lockKind struct {
	Name    string   `json:"name"`
	Steps   []string `json:"steps"`
	Dropped bool     `json:"dropped,omitempty"`
}

// 台帳の業務記録の節を囲む印です。印の間は生成し、手で直しません。
const (
	inventoryBegin = "<!-- records:begin（この範囲は make records-inventory が定義から生成します。手で直さないでください） -->"
	inventoryEnd   = "<!-- records:end -->"
)

// buildLock は、今の定義と前の定義の記録から、新しい定義の記録を作ります。やめた種類の手順の文字列は、前の記録から写します。
func buildLock(defs []RecordDef, retired []RetiredDef, prev *recordsLock) recordsLock {
	old := map[string][]string{}
	if prev != nil {
		for _, k := range append(append([]lockKind{}, prev.Kinds...), prev.Retired...) {
			old[k.Name] = k.Steps
		}
	}
	var l recordsLock
	for _, d := range defs {
		k := lockKind{Name: d.Name, Steps: []string{}}
		for _, s := range d.Steps {
			k.Steps = append(k.Steps, stepString(s))
		}
		l.Kinds = append(l.Kinds, k)
	}
	for _, r := range retired {
		steps := old[r.Name]
		if steps == nil {
			steps = []string{}
		}
		l.Retired = append(l.Retired, lockKind{Name: r.Name, Steps: steps, Dropped: r.Dropped})
	}
	sort.Slice(l.Kinds, func(i, j int) bool { return l.Kinds[i].Name < l.Kinds[j].Name })
	sort.Slice(l.Retired, func(i, j int) bool { return l.Retired[i].Name < l.Retired[j].Name })
	if l.Kinds == nil {
		l.Kinds = []lockKind{}
	}
	if l.Retired == nil {
		l.Retired = []lockKind{}
	}
	return l
}

// checkRecordsHistory は、前の定義の記録と今の定義を比べます。適用済みの手順の変更・削除、やめた種類の削除、
// 定義の一覧からもやめた種類からも消えた種類があれば、エラーを返します。
// 不変条件: RECORDS-INV-9
func checkRecordsHistory(prev *recordsLock, defs []RecordDef, retired []RetiredDef) error {
	if prev == nil {
		return nil
	}
	cur := map[string][]string{}
	for _, d := range defs {
		for _, s := range d.Steps {
			cur[d.Name] = append(cur[d.Name], stepString(s))
		}
	}
	ret := map[string]bool{}
	for _, r := range retired {
		ret[r.Name] = true
	}
	var errs []error
	for _, k := range prev.Kinds {
		steps, inDefs := cur[k.Name]
		switch {
		case inDefs:
			if len(k.Steps) > len(steps) {
				errs = append(errs, fmt.Errorf("records %q: %d step(s) were removed from the end; steps that any environment may have applied must stay", k.Name, len(k.Steps)-len(steps)))
				continue
			}
			for i, s := range k.Steps {
				if steps[i] != s {
					errs = append(errs, fmt.Errorf("records %q: step %d changed from %s to %s; add a new step instead (or, if no environment has started it, reset the lock to the deployed version first)", k.Name, i+1, s, steps[i]))
				}
			}
		case ret[k.Name]:
		default:
			errs = append(errs, fmt.Errorf("records %q: removed from the definitions; move it to RetiredDefs", k.Name))
		}
	}
	for _, k := range prev.Retired {
		if !ret[k.Name] {
			errs = append(errs, fmt.Errorf("retired records %q: removed from RetiredDefs; retired kinds must stay listed", k.Name))
		}
	}
	return errors.Join(errs...)
}

// readLock は、定義の記録を読みます。ファイルが無ければ nil を返します。
func readLock(path string) (*recordsLock, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var l recordsLock
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&l); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &l, nil
}

func encodeLock(l recordsLock) []byte {
	b, _ := json.MarshalIndent(l, "", "  ")
	return append(b, '\n')
}

// accessText は、権限の型を台帳の文にします。
func accessText(a RecordAccess) (name, read, del string) {
	switch a {
	case OwnerOnly:
		return "OwnerOnly", "作った本人だけが読む", "本人が消す。削除したアカウントの記録はシステム管理者がまとめて消せる"
	case OwnerWriteAdminRead:
		return "OwnerWriteAdminRead", "作った本人とシステム管理者が読む", "本人かシステム管理者が消す。削除したアカウントの記録はシステム管理者がまとめて消せる"
	case OwnerWriteAllRead:
		return "OwnerWriteAllRead", "ログインした全員が読む", "本人かシステム管理者が消す。削除したアカウントの記録はシステム管理者がまとめて消せる"
	default:
		return "AllReadWrite", "ログインした全員が読み書きする", "ログインした全員が消せる。共有の台帳なので、作った人のアカウントを消しても残る"
	}
}

var typeText = map[FieldType]string{Text: "1行の文字", LongText: "複数行の文字", Integer: "整数", Date: "日付（YYYY-MM-DD）", Select: "選択肢", Bool: "はい・いいえ（0/1）"}

// renderRecordsInventory は、台帳の業務記録の節（印の間）を、定義から作ります。
// 不変条件: RECORDS-INV-9
func renderRecordsInventory(defs []RecordDef, retired []RetiredDef) string {
	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format, args...) }
	row := func(cells ...string) { w("| %s |\n", strings.Join(cells, " | ")) }
	head := func() {
		row("列名", "型", "NULL許可", "保存目的", "保持期間", "備考")
		w("|---|---|---|---|---|---|\n")
	}
	w("%s\n\n", inventoryBegin)
	w("### テーブル: `records_tables`\n\n種類ごとの状態。名前は定義から来て、個人情報は入らない。\n\n")
	head()
	row("`table_name`", "TEXT", "NO", "種類の表の名前", "機能を使う間", "`records_<種類>`")
	row("`state`", "TEXT", "NO", "使っているか、表を消したか", "同上", "`active`・`dropped`")
	w("\n### テーブル: `records_steps`\n\nその環境に当てた手順の記録。名前と型だけで、個人情報は入らない。\n\n")
	head()
	row("`table_name`", "TEXT", "NO", "種類の表の名前", "機能を使う間", "主キーは `version` と組")
	row("`version`", "INTEGER", "NO", "手順の番号", "同上", "1から1ずつ")
	row("`step`", "TEXT", "NO", "手順の中身", "同上", "例: `add:work_date:date`")
	sorted := append([]RecordDef{}, defs...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	for _, d := range sorted {
		sc, err := replaySteps(d.Steps)
		if err != nil {
			continue // 定義の検査で止まる
		}
		labels := map[string]string{}
		for _, f := range d.Fields {
			labels[f.Name] = f.Label
		}
		name, read, del := accessText(d.Access)
		w("\n### テーブル: `%s`\n\n", tableName(d.Name))
		w("%s（`%s`）。目的: %s保持: %s読む人: %s（`%s`）。削除: %s。", d.Title, d.Name, ensurePeriod(d.Purpose), ensurePeriod(d.Retention), read, name, del)
		if d.MCP {
			w("MCPに出す（MCPを有効にすると、接続を許可された人が読んだ記録の内容が、その人の使うAIのサービスへ送られる）。")
		} else {
			w("MCPには出さない。")
		}
		w("移行で消した列の値は、日々のバックアップと移行の前の複製に、それぞれの保持の期間だけ残る。\n\n")
		head()
		row("`id`", "INTEGER", "NO", "記録の識別", "記録を消すまで", "AUTOINCREMENT")
		row("`owner_account`", "INTEGER", "NO", "作った人", "同上", "アカウントID。認証の表と外部キーでつながない")
		row("`revision`", "INTEGER", "NO", "同時の編集の照合", "同上", "保存のたびに1増える")
		row("`created_at`", "INTEGER", "NO", "作成時刻", "同上", "UNIXエポック秒")
		row("`updated_at`", "INTEGER", "NO", "更新時刻", "同上", "UNIXエポック秒")
		for _, c := range sc.columns {
			row("`"+fieldColumn(c.name)+"`", c.typ.sqlType(), "YES", labels[c.name], "同上", typeText[c.typ]+"。ログに記録しない")
		}
	}
	if len(retired) > 0 {
		w("\n#### やめた種類\n\n画面とMCPには出ない。表が残っている間は、上の種類と同じく保持する。\n\n")
		rs := append([]RetiredDef{}, retired...)
		sort.Slice(rs, func(i, j int) bool { return rs[i].Name < rs[j].Name })
		for _, r := range rs {
			if r.Dropped {
				w("- `%s`: 表を消した（`records-drop`）。消す前の複製とバックアップには、それぞれの保持の期間だけ残る\n", tableName(r.Name))
			} else {
				w("- `%s`: 表が残っている。`records-drop` で消せる\n", tableName(r.Name))
			}
		}
	}
	w("\n%s", inventoryEnd)
	return b.String()
}

func ensurePeriod(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasSuffix(s, "。") {
		return s
	}
	return s + "。"
}

// replaceInventorySection は、台帳の文の印の間を置き換えます。印がちょうど1組でなければエラーです。
func replaceInventorySection(doc, section string) (string, error) {
	b, e := strings.Index(doc, inventoryBegin), strings.Index(doc, inventoryEnd)
	if b < 0 || e < b || strings.Count(doc, inventoryBegin) != 1 || strings.Count(doc, inventoryEnd) != 1 {
		return "", errors.New("docs/data-inventory.md must contain the records markers exactly once")
	}
	return doc[:b] + section + doc[e+len(inventoryEnd):], nil
}

// UpdateInventory は、make records-inventory の本体です。定義を検査し、前の定義の記録と比べてから、
// 定義の記録と台帳の節を書き直します。reset は、どの環境にもまだデプロイしていない間だけ、前の記録を無視するために使います。
func UpdateInventory(lockPath, inventoryPath string, defs []RecordDef, retired []RetiredDef, reset bool) error {
	if err := validateRecordDefs(defs, retired); err != nil {
		return err
	}
	prev, err := readLock(lockPath)
	if err != nil {
		return err
	}
	if reset {
		prev = nil
	}
	if err := checkRecordsHistory(prev, defs, retired); err != nil {
		return err
	}
	doc, err := os.ReadFile(inventoryPath)
	if err != nil {
		return err
	}
	updated, err := replaceInventorySection(string(doc), renderRecordsInventory(defs, retired))
	if err != nil {
		return err
	}
	// 両方を一時のファイルに書き終えてから置き換え、片方だけが新しくなることを避ける
	lockTmp, invTmp := lockPath+".tmp", inventoryPath+".tmp"
	if err := os.WriteFile(lockTmp, encodeLock(buildLock(defs, retired, prev)), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(invTmp, []byte(updated), 0o644); err != nil {
		os.Remove(lockTmp)
		return err
	}
	if err := os.Rename(lockTmp, lockPath); err != nil {
		os.Remove(lockTmp)
		os.Remove(invTmp)
		return err
	}
	return os.Rename(invTmp, inventoryPath)
}

// CheckInventory は、定義の記録と台帳の節が、今の定義から作ったものと同じかを確かめます（テストから使う）。
// 違えば、前の定義との比較の結果と、make records-inventory の案内を返します。
func CheckInventory(lockPath, inventoryPath string, defs []RecordDef, retired []RetiredDef) error {
	if err := validateRecordDefs(defs, retired); err != nil {
		return err
	}
	prev, err := readLock(lockPath)
	if err != nil {
		return err
	}
	if prev == nil {
		return fmt.Errorf("%s is missing; run make records-inventory", lockPath)
	}
	if err := checkRecordsHistory(prev, defs, retired); err != nil {
		return err
	}
	if want := encodeLock(buildLock(defs, retired, prev)); !bytes.Equal(want, mustRead(lockPath)) {
		return fmt.Errorf("%s does not match the definitions; run make records-inventory", lockPath)
	}
	doc, err := os.ReadFile(inventoryPath)
	if err != nil {
		return err
	}
	want, err := replaceInventorySection(string(doc), renderRecordsInventory(defs, retired))
	if err != nil {
		return err
	}
	if want != string(doc) {
		return fmt.Errorf("%s: the records section does not match the definitions; run make records-inventory (do not edit between the markers)", inventoryPath)
	}
	return nil
}

func mustRead(path string) []byte {
	b, _ := os.ReadFile(path)
	return b
}
