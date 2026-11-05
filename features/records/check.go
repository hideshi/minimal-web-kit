package records

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
)

// runRecordsCheck は、確認のコマンド（records-check）の本体です。DBを読むだけの接続で開き、
// この実行ファイルが起動したら何をするか、変換できない行、起動を止める理由、戻し方を出します。DBの表と行は変えません。
// 不変条件: RECORDS-INV-14
func runRecordsCheck(ctx context.Context, dbPath string, defs []RecordDef, retired []RetiredDef, out io.Writer) error {
	if err := validateRecordDefs(defs, retired); err != nil {
		return err
	}
	if _, err := os.Stat(dbPath); errors.Is(err, os.ErrNotExist) {
		fmt.Fprintln(out, "The database does not exist yet; every kind will be created on the first start.")
		fmt.Fprintln(out, "Rollback: the executable only.")
		return nil
	}
	// immutable=1 は使わない（稼働中のDBの WAL の内容を読み落とすため）
	db, err := sql.Open("sqlite", "file:"+(&url.URL{Path: dbPath}).EscapedPath()+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return err
	}
	defer db.Close()
	p, err := planRecordsMigration(ctx, db, defs, retired)
	if err != nil {
		return err
	}
	failures, err := simulateConversions(ctx, db, p, buildRecordStatements(defs, retired))
	if err != nil {
		return err
	}
	_, err = io.WriteString(out, recordsCheckReport(p, failures))
	return err
}

// recordsCheckReport は、確認のコマンドの出力を作ります。種類・項目・行のIDだけを出し、記録の値は出しません。
// 不変条件: RECORDS-INV-13
func recordsCheckReport(p *recordsPlan, failures []convertFailure) string {
	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }
	for _, kp := range p.kinds {
		switch {
		case kp.create:
			w("%s: create (%d step(s))", kp.table, len(kp.pending))
		case len(kp.pending) == 0:
			w("%s: up to date (step %d)", kp.table, kp.from)
		default:
			var steps []string
			for _, s := range kp.pending {
				steps = append(steps, fmt.Sprintf("%d %s", s.Version, stepString(s)))
			}
			w("%s: step %d -> %d: %s", kp.table, kp.from, kp.from+len(kp.pending), strings.Join(steps, ", "))
		}
	}
	for _, f := range failures {
		w("CANNOT CONVERT: %s", strings.TrimPrefix(f.Error(), "records: "))
	}
	for _, s := range p.warnings {
		w("warning: %s", s)
	}
	for _, s := range p.stops {
		w("STOP: %s", s)
	}
	switch {
	case len(p.stops) > 0 || len(failures) > 0:
		w("Result: the server will NOT start with this database. Fix the items above first.")
	case p.needsBackup:
		w("Result: OK. Breaking steps will run; a pre-migration copy is taken on start.")
		w("Rollback: restore the database from that pre-migration copy as well as the executable.")
	default:
		w("Result: OK.")
		w("Rollback: the executable only.")
	}
	return b.String()
}

// simulateConversions は、これから当てる手順を、DBを変えずにメモリの上でたどり、型の変換で変換できない行を見つけます。
func simulateConversions(ctx context.Context, q querier, p *recordsPlan, stmts map[string]*kindStatements) ([]convertFailure, error) {
	var out []convertFailure
	for _, kp := range p.kinds {
		if kp.create {
			continue // 新しい表は空なので、変換できない行は無い
		}
		ks := stmts[kp.def.Name]
		for j, s := range kp.pending {
			if s.ChangeType == nil {
				continue
			}
			// この変換の項目を、当てる前の名前へたどる。途中で足した項目なら、値は全部空
			name, fresh := s.ChangeType.Field, false
			for i := j - 1; i >= 0; i-- {
				prev := kp.pending[i]
				if prev.Rename != nil && prev.Rename.To == name {
					name = prev.Rename.From
				}
				if prev.Add != nil && prev.Add.Field == name {
					fresh = true
					break
				}
			}
			if fresh {
				continue
			}
			sel, ok := ks.selectColumn[name]
			if !ok {
				return nil, fmt.Errorf("records: no statement for %s.%s", kp.table, name)
			}
			rows, err := queryStatement(ctx, q, sel)
			if err != nil {
				return nil, err
			}
			values := map[int64]any{}
			var order []int64
			for rows.Next() {
				var id int64
				var v any
				if err := rows.Scan(&id, &v); err != nil {
					rows.Close()
					return nil, err
				}
				values[id] = v
				order = append(order, id)
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return nil, err
			}
			// 同じ項目への前の変換を順に当ててから、この変換を試す
			chain := []*ChangeType{}
			tracked := name
			for i := 0; i <= j; i++ {
				st := kp.pending[i]
				if st.Rename != nil && st.Rename.From == tracked {
					tracked = st.Rename.To
				}
				if st.ChangeType != nil && st.ChangeType.Field == tracked {
					chain = append(chain, st.ChangeType)
				}
			}
			// 前の変換で既に失敗する行は、その変換の行として出すので、ここでは数えない
			var bad []int64
			for _, id := range order {
				v, ok := values[id], true
				for k, c := range chain {
					if v, ok = convertValue(c.From, c.To, v); !ok {
						if k == len(chain)-1 {
							bad = append(bad, id)
						}
						break
					}
				}
			}
			if len(bad) > 0 {
				out = append(out, convertFailure{Table: kp.table, Field: s.ChangeType.Field, IDs: bad})
			}
		}
	}
	return out, nil
}
