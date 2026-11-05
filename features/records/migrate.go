package records

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"minimal-web-kit/internal/store"
)

//go:embed migration.sql
var migrationSQL string

// metaTables は、業務記録の記録の表です。種類の表の照合から外します。
var metaTables = map[string]bool{"records_tables": true, "records_steps": true}

// dbState は、DBから読んだ業務記録の今の姿です。
type dbState struct {
	states  map[string]string            // 表の名前 → active・dropped
	steps   map[string][]string          // 表の名前 → 手順の文字列（番号の順。1から1ずつ）
	tables  map[string]bool              // 実際にある records_ の表（記録の表を除く）
	columns map[string]map[string]string // 表の名前 → 列の名前 → 宣言の型
}

// readRecordsState は、記録の表と実際の表・列を読みます。記録の表がまだ無いDB（確認のコマンドで開く新しいDB等）は、空として扱います。
func readRecordsState(ctx context.Context, q querier) (*dbState, error) {
	st := &dbState{states: map[string]string{}, steps: map[string][]string{}, tables: map[string]bool{}, columns: map[string]map[string]string{}}
	rows, err := q.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'table' AND name LIKE 'records\_%' ESCAPE '\'`)
	if err != nil {
		return nil, err
	}
	var hasMeta int
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return nil, err
		}
		if metaTables[name] {
			hasMeta++
			continue
		}
		st.tables[name] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for t := range st.tables {
		cols, err := tableColumns(ctx, q, t)
		if err != nil {
			return nil, err
		}
		st.columns[t] = cols
	}
	if hasMeta < len(metaTables) {
		return st, nil
	}
	rows, err = q.QueryContext(ctx, "SELECT table_name, state FROM records_tables")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var t, s string
		if err := rows.Scan(&t, &s); err != nil {
			rows.Close()
			return nil, err
		}
		st.states[t] = s
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = q.QueryContext(ctx, "SELECT table_name, version, step FROM records_steps ORDER BY table_name, version")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var t, s string
		var v int
		if err := rows.Scan(&t, &v, &s); err != nil {
			return nil, err
		}
		// 番号が1から1ずつ増えていなければ、手順の記録が壊れている（RECORDS-INV-16）
		if v != len(st.steps[t])+1 {
			return nil, fmt.Errorf("records: the step log of %s is not numbered 1, 2, 3, ... (found %d); restore the database from a backup", t, v)
		}
		st.steps[t] = append(st.steps[t], s)
	}
	return st, rows.Err()
}

// tableColumns は、表の列の名前と宣言の型を読みます。
func tableColumns(ctx context.Context, q querier, table string) (map[string]string, error) {
	rows, err := q.QueryContext(ctx, "SELECT name, type FROM pragma_table_info(?)", table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols := map[string]string{}
	for rows.Next() {
		var n, t string
		if err := rows.Scan(&n, &t); err != nil {
			return nil, err
		}
		cols[n] = strings.ToUpper(t)
	}
	return cols, rows.Err()
}

// kindPlan は、1つの種類について、起動の時にすることです。
type kindPlan struct {
	def     RecordDef
	table   string
	create  bool         // 表を作る（その起動で作った表への手順は、複製と戻し方の判定では追加と同じ）
	from    int          // 当て終わっている手順の数
	pending []RecordStep // これから当てる手順（番号の順）
}

// recordsPlan は、起動の時にすることの全体です。確認のコマンドも同じものを出します。
type recordsPlan struct {
	kinds    []kindPlan
	stops    []string // 起動を止める理由
	warnings []string // 起動は続けるが知らせること
	// needsBackup は、既にあった表に壊す手順を1つでも当てるかです。true なら、移行の前に複製を作り、戻すときはDBも戻す。
	needsBackup bool
}

// err は、起動を止める理由があればエラーにします。
func (p *recordsPlan) err() error {
	if len(p.stops) == 0 {
		return nil
	}
	return errors.New("records: " + strings.Join(p.stops, "; "))
}

// planRecordsMigration は、定義とDBの記録と実際の表・列を突き合わせて、起動の時にすることを決めます。DBは変えません。
// 不変条件: RECORDS-INV-8
func planRecordsMigration(ctx context.Context, q querier, defs []RecordDef, retired []RetiredDef) (*recordsPlan, error) {
	st, err := readRecordsState(ctx, q)
	if err != nil {
		return nil, err
	}
	p := &recordsPlan{}
	known := map[string]bool{}
	for _, d := range defs {
		t := tableName(d.Name)
		known[t] = true
		kp, stops, warns := checkRecordsVersion(d, st)
		p.stops = append(p.stops, stops...)
		p.warnings = append(p.warnings, warns...)
		if kp == nil {
			continue
		}
		for _, s := range kp.pending {
			if s.breaking() && !kp.create {
				p.needsBackup = true
			}
		}
		p.kinds = append(p.kinds, *kp)
	}
	for _, r := range retired {
		t := tableName(r.Name)
		known[t] = true
		state, hasState := st.states[t]
		switch {
		case st.tables[t] && !hasState:
			p.stops = append(p.stops, fmt.Sprintf("%s: the table has no entry in records_tables", t))
		case !st.tables[t] && hasState && state == "active":
			p.stops = append(p.stops, fmt.Sprintf("%s: records_tables says active but the table is missing", t))
		case st.tables[t]:
			// 残っている表も、手順の記録と列を突き合わせる（RECORDS-INV-16）
			if state == "active" {
				if err := reconcileRecordsSchema(t, st.steps[t], st); err != nil {
					p.stops = append(p.stops, err.Error())
				}
			}
			if r.Dropped {
				p.warnings = append(p.warnings, fmt.Sprintf("%s: the table of a dropped kind is back (a backup from before records-drop?); run records-drop %s again", t, r.Name))
			}
		}
	}
	// 定義にもやめた種類にも無い表と記録（新しい版が足した種類）は、警告して残す。記録の無い表は止める。
	for t := range st.tables {
		if known[t] {
			continue
		}
		if _, ok := st.states[t]; !ok {
			p.stops = append(p.stops, fmt.Sprintf("%s: the table has no entry in records_tables", t))
			continue
		}
		p.warnings = append(p.warnings, fmt.Sprintf("%s: unknown kind (added by a newer version?); kept and not shown", t))
	}
	for t, state := range st.states {
		if !known[t] && !st.tables[t] && state == "active" {
			p.stops = append(p.stops, fmt.Sprintf("%s: records_tables says active but the table is missing", t))
		}
	}
	return p, nil
}

// checkRecordsVersion は、定義の一覧にある1つの種類について、DBの手順の記録と定義の手順を比べます。
// 起動を止める理由があれば kindPlan は nil です。
// 不変条件: RECORDS-INV-15
func checkRecordsVersion(d RecordDef, st *dbState) (kp *kindPlan, stops, warns []string) {
	t := tableName(d.Name)
	state, hasState := st.states[t]
	applied := st.steps[t]
	switch {
	case hasState && state == "dropped":
		return nil, []string{fmt.Sprintf("%s: the table was dropped by records-drop, but this version still uses it; start the version that retired it, or restore the database", t)}, nil
	case !hasState && !st.tables[t] && len(applied) == 0:
		return &kindPlan{def: d, table: t, create: true, pending: d.Steps}, nil, nil
	case !hasState:
		return nil, []string{fmt.Sprintf("%s: the table or step log exists without an entry in records_tables", t)}, nil
	}
	for i, text := range applied {
		if i < len(d.Steps) {
			if stepString(d.Steps[i]) != text {
				stops = append(stops, fmt.Sprintf("%s: step %d in the database (%s) differs from this version (%s); the definitions have diverged", t, i+1, text, stepString(d.Steps[i])))
			}
			continue
		}
		s, ok := parseStep(text)
		if !ok || s.breaking() {
			stops = append(stops, fmt.Sprintf("%s: the database has step %d (%s) that this version does not know; start the newer version, or restore the database from the backup taken before it", t, i+1, text))
		}
	}
	if len(stops) > 0 {
		return nil, stops, nil
	}
	if len(applied) > len(d.Steps) {
		warns = append(warns, fmt.Sprintf("%s: %d field(s) added by a newer version are kept and not shown", t, len(applied)-len(d.Steps)))
	}
	if err := reconcileRecordsSchema(t, applied, st); err != nil {
		return nil, []string{err.Error()}, warns
	}
	kp = &kindPlan{def: d, table: t, from: len(applied)}
	if len(applied) < len(d.Steps) {
		kp.pending = d.Steps[len(applied):]
	}
	return kp, nil, warns
}

// reconcileRecordsSchema は、手順の記録の全部の手順（定義に無い番号の追加を含む）を当てた結果の列と、実際の列を比べます。
// 名前と宣言の型が過不足なく一致しなければ、自動で直さずにエラーを返します。
// 不変条件: RECORDS-INV-16
func reconcileRecordsSchema(table string, applied []string, st *dbState) error {
	if !st.tables[table] {
		return fmt.Errorf("%s: records_tables says active but the table is missing", table)
	}
	sc := newSchema()
	for i, text := range applied {
		s, ok := parseStep(text)
		if !ok {
			return fmt.Errorf("%s: step %d (%s) cannot be read", table, i+1, text)
		}
		s.Version = i + 1
		if err := sc.apply(s); err != nil {
			return fmt.Errorf("%s: the step log does not replay: %v", table, err)
		}
	}
	want := map[string]string{}
	for _, c := range commonColumns {
		want[c.name] = c.typ.sqlType()
	}
	for _, c := range sc.columns {
		want[fieldColumn(c.name)] = c.typ.sqlType()
	}
	got := map[string]string{}
	for n, typ := range st.columns[table] {
		if strings.HasPrefix(n, "tmp_") {
			return fmt.Errorf("%s: a work column %s from an interrupted conversion is left; restore the database from a backup", table, n)
		}
		got[n] = typ
	}
	var diffs []string
	for n, typ := range want {
		g, ok := got[n]
		switch {
		case !ok:
			diffs = append(diffs, "missing column "+n)
		case g != typ:
			diffs = append(diffs, fmt.Sprintf("column %s is %s, not %s", n, g, typ))
		}
	}
	for n := range got {
		if _, ok := want[n]; !ok {
			diffs = append(diffs, "unexpected column "+n)
		}
	}
	if len(diffs) > 0 {
		return fmt.Errorf("%s: the columns do not match the step log (%s); the database was changed by hand, restore it from a backup", table, strings.Join(diffs, ", "))
	}
	return nil
}

// applyRecordsMigration は、計画のとおりに手順を当てます。全部を1つのトランザクションで行い、途中で失敗したら何も変えません。
// 既にあった表に壊す手順を当てるときは、その前に複製を作り、複製に失敗したら当てません。
// 不変条件: RECORDS-INV-8
func applyRecordsMigration(ctx context.Context, db *sql.DB, p *recordsPlan, stmts map[string]*kindStatements, logger *slog.Logger) error {
	if err := p.err(); err != nil {
		return err
	}
	work := false
	for _, kp := range p.kinds {
		if kp.create || len(kp.pending) > 0 {
			work = true
		}
	}
	if !work {
		return nil
	}
	backup := ""
	if p.needsBackup {
		path, err := store.BackupBeforeMigration(ctx, db, "records")
		if err != nil {
			return fmt.Errorf("records: backup before migration: %w", err)
		}
		backup = filepath.Base(path)
	}
	err := store.Atomic(ctx, db, func(c *sql.Conn) error {
		for _, kp := range p.kinds {
			ks := stmts[kp.def.Name]
			if kp.create {
				if _, err := execStatement(ctx, c, ks.create); err != nil {
					return err
				}
				if _, err := c.ExecContext(ctx, "INSERT INTO records_tables (table_name, state) VALUES (?, 'active')", kp.table); err != nil {
					return err
				}
			}
			for _, s := range kp.pending {
				ss := ks.steps[s.Version-1]
				if s.ChangeType != nil {
					if err := convertRecordColumn(ctx, c, kp.table, ss); err != nil {
						return err
					}
				} else {
					for _, a := range ss.apply {
						if _, err := execStatement(ctx, c, a); err != nil {
							return err
						}
					}
				}
				if _, err := c.ExecContext(ctx, "INSERT INTO records_steps (table_name, version, step) VALUES (?, ?, ?)", kp.table, s.Version, stepString(s)); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	logRecordsMigration(logger, p, backup)
	return nil
}

// logRecordsMigration は、移行で当てた手順と複製のファイル名をログに出します。記録の値は出しません。
// 不変条件: RECORDS-INV-13
func logRecordsMigration(logger *slog.Logger, p *recordsPlan, backup string) {
	if logger == nil {
		return
	}
	for _, kp := range p.kinds {
		if !kp.create && len(kp.pending) == 0 {
			continue
		}
		var steps []string
		for _, s := range kp.pending {
			steps = append(steps, stepString(s))
		}
		logger.Info("records migration applied", slog.String("table", kp.table), slog.Bool("created", kp.create),
			slog.Int("from", kp.from), slog.Int("to", kp.from+len(kp.pending)), slog.String("steps", strings.Join(steps, " ")),
			slog.String("backup", backup))
	}
}

// convertFailure は、型の変換で変換できなかった行です。値は持ちません。
type convertFailure struct {
	Table, Field string
	IDs          []int64
}

func (f convertFailure) Error() string {
	ids := make([]string, 0, len(f.IDs))
	for _, id := range f.IDs {
		ids = append(ids, strconv.FormatInt(id, 10))
	}
	return fmt.Sprintf("records: %s.%s: %d row(s) cannot be converted (id %s); fix them with the current version first", f.Table, f.Field, len(f.IDs), strings.Join(ids, ", "))
}

// convertRecordColumn は、型の変換の手順を1つ当てます。全部の行が新しい型の形に合う時だけ写し、1行でも合わなければエラーを返します
// （呼び出し側のトランザクションで全部戻る）。作業用の列に写し、古い列を消して名前を戻すので、列の宣言の型も新しい型になります。
// 不変条件: RECORDS-INV-17
func convertRecordColumn(ctx context.Context, q querier, table string, ss stepStatements) error {
	c := ss.step.ChangeType
	rows, err := queryStatement(ctx, q, ss.selectOld)
	if err != nil {
		return err
	}
	type row struct {
		id int64
		v  any
	}
	var all []row
	var bad []int64
	for rows.Next() {
		var id int64
		var v any
		if err := rows.Scan(&id, &v); err != nil {
			rows.Close()
			return err
		}
		nv, ok := convertValue(c.From, c.To, v)
		if !ok {
			bad = append(bad, id)
			continue
		}
		all = append(all, row{id, nv})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(bad) > 0 {
		return convertFailure{Table: table, Field: c.Field, IDs: bad}
	}
	if _, err := execStatement(ctx, q, ss.addTmp); err != nil {
		return err
	}
	for _, r := range all {
		if _, err := execStatement(ctx, q, ss.updateTmp, r.v, r.id); err != nil {
			return err
		}
	}
	if _, err := execStatement(ctx, q, ss.dropOld); err != nil {
		return err
	}
	_, err = execStatement(ctx, q, ss.renameTmp)
	return err
}

// integerForm は、整数として受け付ける文字列の形です。符号は - だけ、先頭に余分な 0 や空白、-0 を許しません。
var integerForm = regexp.MustCompile(`^(0|-?[1-9][0-9]*)$`)

// convertValue は、1つの値を新しい型の形に変換します。NULL と空の文字列は NULL にします。上限・選択肢・必須は見ません。
func convertValue(from, to FieldType, v any) (any, bool) {
	if b, ok := v.([]byte); ok {
		v = string(b)
	}
	if v == nil {
		return nil, true
	}
	if s, ok := v.(string); ok && s == "" {
		return nil, true
	}
	if conversionOf(from, to) == convNotAllowed {
		return nil, false
	}
	switch to {
	case Text, Select:
		s, ok := asText(v)
		if !ok || strings.ContainsAny(s, "\r\n\t") {
			return nil, false
		}
		return s, true
	case LongText:
		return asText(v)
	case Date:
		s, ok := v.(string)
		if !ok {
			return nil, false
		}
		d, err := time.Parse("2006-01-02", s)
		if err != nil || d.Format("2006-01-02") != s {
			return nil, false
		}
		return s, true
	case Integer:
		switch x := v.(type) {
		case int64:
			return x, true
		case string:
			if !integerForm.MatchString(x) {
				return nil, false
			}
			n, err := strconv.ParseInt(x, 10, 64)
			if err != nil {
				return nil, false
			}
			return n, true
		}
		return nil, false
	case Bool:
		if x, ok := v.(int64); ok && (x == 0 || x == 1) {
			return x, true
		}
		return nil, false
	}
	return nil, false
}

// asText は、文字か整数の値を文字にします。
func asText(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case int64:
		return strconv.FormatInt(x, 10), true
	}
	return "", false
}

// migrateRecords は、起動の時の移行の全体です。記録の表を作り、定義を検査し、計画を立てて当てます。
// 警告はログに出し、起動を止める理由があればエラーを返します。
func migrateRecords(ctx context.Context, db *sql.DB, defs []RecordDef, retired []RetiredDef, logger *slog.Logger) (*recordsPlan, error) {
	if err := validateRecordDefs(defs, retired); err != nil {
		return nil, err
	}
	if _, err := db.ExecContext(ctx, migrationSQL); err != nil {
		return nil, fmt.Errorf("records migration: %w", err)
	}
	p, err := planRecordsMigration(ctx, db, defs, retired)
	if err != nil {
		return nil, err
	}
	if logger != nil {
		for _, w := range p.warnings {
			logger.Warn("records migration warning", slog.String("detail", w))
		}
	}
	if err := applyRecordsMigration(ctx, db, p, buildRecordStatements(defs, retired), logger); err != nil {
		return p, err
	}
	return p, nil
}

// Migrate は、定義のとおりに業務記録の表を作り・移します。機能の Init と、台帳の照合のテスト（test/data_schema_test.go）が使います。
func Migrate(ctx context.Context, db *sql.DB, defs []RecordDef, retired []RetiredDef, logger *slog.Logger) error {
	_, err := migrateRecords(ctx, db, defs, retired, logger)
	return err
}
