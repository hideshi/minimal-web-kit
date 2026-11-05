package records

import (
	"strings"
)

// commonColumns は、どの種類の表にもある列と、その宣言の型です。
var commonColumns = []column{
	{name: "id", typ: Integer},
	{name: "owner_account", typ: Integer},
	{name: "revision", typ: Integer},
	{name: "created_at", typ: Integer},
	{name: "updated_at", typ: Integer},
}

// tableName は、種類の表の名前です。名前は検査済み（namePattern）であることが前提です。
func tableName(kind string) string { return "records_" + kind }

// fieldColumn は、項目の列の名前です。
func fieldColumn(field string) string { return "f_" + field }

// tmpColumn は、型の変換の作業用の列の名前です。`f_` の外に置き、項目の列とぶつからないようにします。
func tmpColumn(field string) string { return "tmp_" + field }

// stepStatements は、1つの手順を当てる文です。
type stepStatements struct {
	step  RecordStep
	apply []recordsStatement // 型の変換以外は、順に実行すれば当たる
	// 型の変換だけで使う。作業用の列を足し、値を読み、1行ずつ写し、古い列を消し、作業用の列の名前を戻す
	addTmp, selectOld, updateTmp, dropOld, renameTmp recordsStatement
}

// kindStatements は、1つの種類（やめた種類を含む）の文です。
type kindStatements struct {
	table  string
	create recordsStatement
	drop   recordsStatement
	steps  []stepStatements // 番号の順。steps[i] は番号 i+1
	// selectColumn は、一度でも使った項目名ごとの、値を読む文です（確認のコマンドが型の変換を試すのに使う）
	selectColumn map[string]recordsStatement
}

// buildRecordStatements は、検査済みの定義とやめた種類から、業務記録の文を全部組み立てます。
// 文の文字列を作るのはこの関数だけです。名前は検査済みですが、二重の守りとして全部を引用符で囲みます。
// 不変条件: RECORDS-INV-7
func buildRecordStatements(defs []RecordDef, retired []RetiredDef) map[string]*kindStatements {
	st := func(parts ...string) recordsStatement { return recordsStatement{sql: strings.Join(parts, "")} }
	q := quoteIdent
	out := map[string]*kindStatements{}
	kind := func(name string) *kindStatements {
		t := q(tableName(name))
		var cols []string
		for _, c := range commonColumns {
			def := q(c.name) + " INTEGER NOT NULL"
			if c.name == "id" {
				def = q(c.name) + " INTEGER PRIMARY KEY AUTOINCREMENT"
			}
			cols = append(cols, def)
		}
		return &kindStatements{
			table:  tableName(name),
			create: st("CREATE TABLE ", t, " (", strings.Join(cols, ", "), ")"),
			drop:   st("DROP TABLE ", t),
		}
	}
	for _, d := range defs {
		k := kind(d.Name)
		t := q(k.table)
		k.selectColumn = map[string]recordsStatement{}
		if sc, err := replaySteps(d.Steps); err == nil {
			for name := range sc.used {
				k.selectColumn[name] = st("SELECT ", q("id"), ", ", q(fieldColumn(name)), " FROM ", t, " ORDER BY ", q("id"))
			}
		}
		for _, s := range d.Steps {
			ss := stepStatements{step: s}
			switch {
			case s.Add != nil:
				ss.apply = []recordsStatement{st("ALTER TABLE ", t, " ADD COLUMN ", q(fieldColumn(s.Add.Field)), " ", s.Add.Type.sqlType())}
			case s.Rename != nil:
				ss.apply = []recordsStatement{st("ALTER TABLE ", t, " RENAME COLUMN ", q(fieldColumn(s.Rename.From)), " TO ", q(fieldColumn(s.Rename.To)))}
			case s.Remove != nil:
				ss.apply = []recordsStatement{st("ALTER TABLE ", t, " DROP COLUMN ", q(fieldColumn(s.Remove.Field)))}
			case s.ChangeType != nil:
				c := s.ChangeType
				old, tmp := q(fieldColumn(c.Field)), q(tmpColumn(c.Field))
				ss.addTmp = st("ALTER TABLE ", t, " ADD COLUMN ", tmp, " ", c.To.sqlType())
				ss.selectOld = st("SELECT ", q("id"), ", ", old, " FROM ", t, " ORDER BY ", q("id"))
				ss.updateTmp = st("UPDATE ", t, " SET ", tmp, " = ? WHERE ", q("id"), " = ?")
				ss.dropOld = st("ALTER TABLE ", t, " DROP COLUMN ", old)
				ss.renameTmp = st("ALTER TABLE ", t, " RENAME COLUMN ", tmp, " TO ", old)
			}
			k.steps = append(k.steps, ss)
		}
		out[d.Name] = k
	}
	for _, r := range retired {
		out[r.Name] = kind(r.Name)
	}
	return out
}

// quoteIdent は、名前をSQLの識別子として引用します。
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
