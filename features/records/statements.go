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

	// 以下は定義の一覧にある種類だけ。読み書きの文は、今の定義が知っている列だけを名前で書く（RECORDS-INV-18）
	columns     []column           // 今の項目の列（手順を当てた結果の順）
	insert      recordsStatement   // owner, now, now, 項目の値… → id
	get         recordsStatement   // id
	update      recordsStatement   // 項目の値…, now, id, revision
	del         recordsStatement   // id, revision
	list        []recordsStatement // 並びの番号ごと。条件の値は listArgs の順
	sorts       []sortOption       // 並びの選択肢。番号は list と同じ
	textColumns []string           // 語で探す項目
	selectCols  []string           // 値で絞る項目（Select）
	dateColumn  string             // 期間で絞る項目（無ければ空）
	ownerCounts recordsStatement   // 所有者ごとの件数
	purgeOwner  recordsStatement   // owner
}

// sortOption は、一覧の並びの選択肢です。
type sortOption struct {
	Field string // 項目名。空なら更新日時
	Desc  bool
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
		buildCRUD(k, d, st)
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

// buildCRUD は、定義の一覧にある種類の、読み書きと一覧の文を組み立てます。buildRecordStatements からだけ呼びます。
// 一覧は、絞り込みの全部の条件を1つの文に入れ、使わない条件はプレースホルダの値で無効にします。並びは選択肢ごとに文を作ります。
// 不変条件: RECORDS-INV-7, RECORDS-INV-18
func buildCRUD(k *kindStatements, d RecordDef, st func(parts ...string) recordsStatement) {
	sc, err := replaySteps(d.Steps)
	if err != nil {
		return // 定義の検査で止まる
	}
	q := quoteIdent
	t := q(k.table)
	k.columns = sc.columns
	var names, sets []string
	for _, c := range sc.columns {
		col := q(fieldColumn(c.name))
		names = append(names, col)
		sets = append(sets, col+" = ?")
		switch c.typ {
		case Text, LongText:
			k.textColumns = append(k.textColumns, c.name)
		case Select:
			k.selectCols = append(k.selectCols, c.name)
		}
	}
	for _, c := range sc.columns {
		if c.typ == Date && (k.dateColumn == "" || c.name == d.SortBy) {
			k.dateColumn = c.name
		}
	}
	head := []string{q("id"), q("owner_account"), q("revision"), q("created_at"), q("updated_at")}
	selectList := strings.Join(append(head, names...), ", ")
	k.insert = st("INSERT INTO ", t, " (", strings.Join(append([]string{q("owner_account"), q("revision"), q("created_at"), q("updated_at")}, names...), ", "),
		") VALUES (?, 1, ?, ?", strings.Repeat(", ?", len(names)), ")")
	k.get = st("SELECT ", selectList, " FROM ", t, " WHERE ", q("id"), " = ?")
	k.update = st("UPDATE ", t, " SET ", strings.Join(append(sets, q("revision")+" = "+q("revision")+" + 1", q("updated_at")+" = ?"), ", "),
		" WHERE ", q("id"), " = ? AND ", q("revision"), " = ?")
	k.del = st("DELETE FROM ", t, " WHERE ", q("id"), " = ? AND ", q("revision"), " = ?")
	k.ownerCounts = st("SELECT ", q("owner_account"), ", COUNT(*) FROM ", t, " GROUP BY ", q("owner_account"), " ORDER BY ", q("owner_account"))
	k.purgeOwner = st("DELETE FROM ", t, " WHERE ", q("owner_account"), " = ?")

	// 条件の並び: 読める範囲、自分の記録だけ、語、選択肢ごと、期間（listArgs と同じ順）
	conds := []string{
		"(? = 1 OR " + q("owner_account") + " = ?)",
		"(? = 0 OR " + q("owner_account") + " = ?)",
	}
	var like []string
	for _, n := range k.textColumns {
		like = append(like, q(fieldColumn(n))+` LIKE ? ESCAPE '\'`)
	}
	if len(like) > 0 {
		conds = append(conds, "(? = '' OR "+strings.Join(like, " OR ")+")")
	}
	for _, n := range k.selectCols {
		conds = append(conds, "(? = '' OR "+q(fieldColumn(n))+" = ?)")
	}
	if k.dateColumn != "" {
		dc := q(fieldColumn(k.dateColumn))
		conds = append(conds, "(? = '' OR "+dc+" >= ?)", "(? = '' OR "+dc+" <= ?)")
	}
	where := strings.Join(conds, " AND ")
	listed := map[string]bool{}
	for _, n := range d.ListFields {
		listed[n] = true
	}
	if d.SortBy != "" {
		listed[d.SortBy] = true
	}
	// 既定の並び（番号0）: SortBy の項目で、日付・整数は新しい順・大きい順、文字は順。無ければ更新日時の新しい順
	if d.SortBy != "" {
		desc := false
		for _, c := range sc.columns {
			if c.name == d.SortBy && (c.typ == Date || c.typ == Integer || c.typ == Bool) {
				desc = true
			}
		}
		k.sorts = append(k.sorts, sortOption{Field: d.SortBy, Desc: desc}, sortOption{Field: d.SortBy, Desc: !desc})
	}
	k.sorts = append(k.sorts, sortOption{Desc: true}, sortOption{})
	for _, c := range sc.columns {
		if listed[c.name] && c.name != d.SortBy {
			k.sorts = append(k.sorts, sortOption{Field: c.name, Desc: true}, sortOption{Field: c.name})
		}
	}
	for _, o := range k.sorts {
		col := q("updated_at")
		if o.Field != "" {
			col = q(fieldColumn(o.Field))
		}
		dir := " ASC"
		if o.Desc {
			dir = " DESC"
		}
		k.list = append(k.list, st("SELECT ", selectList, " FROM ", t, " WHERE ", where, " ORDER BY ", col, dir, ", ", q("id"), " DESC LIMIT ? OFFSET ?"))
	}
}
