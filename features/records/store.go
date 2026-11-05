package records

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"minimal-web-kit/internal/store"
)

var (
	// ErrNotFound は、無い・読めない記録です。どちらも同じ404にします（RECORDS-INV-3）。
	ErrNotFound = errors.New("records: not found")
	// ErrConflict は、開いた後に他の人が保存・削除した記録です。
	ErrConflict = errors.New("records: changed since opened")
)

// record は、記録の1行です。Values は項目名ごとの値（NULL は nil、文字は string、整数・はい・いいえは int64）。
type record struct {
	ID        int64
	Owner     int64
	Revision  int64
	CreatedAt time.Time
	UpdatedAt time.Time
	Values    map[string]any
}

// listFilter は、一覧の絞り込みと並びです。値は文の文字列に入らず、プレースホルダで渡します。
type listFilter struct {
	ReadAll  bool   // 読める範囲が全部（false なら viewer の記録だけ）
	Viewer   int64  // 見ている人
	MineOnly bool   // 自分の記録だけ
	Query    string // 語（部分一致）
	Selects  map[string]string
	From, To string // 期間（YYYY-MM-DD）
	Sort     int    // 並びの番号（kindStatements.sorts の添え字）
}

// escapeLike は、部分一致の語の % と _ と \ を、文字として扱うように逃がします。
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

func flag(b bool) int {
	if b {
		return 1
	}
	return 0
}

// listArgs は、一覧の文に渡す値を、文の条件と同じ順に並べます。
func listArgs(k *kindStatements, f listFilter, limit, offset int) []any {
	args := []any{flag(f.ReadAll), f.Viewer, flag(f.MineOnly), f.Viewer}
	if len(k.textColumns) > 0 {
		args = append(args, f.Query)
		pattern := "%" + escapeLike(f.Query) + "%"
		for range k.textColumns {
			args = append(args, pattern)
		}
	}
	for _, n := range k.selectCols {
		v := f.Selects[n]
		args = append(args, v, v)
	}
	if k.dateColumn != "" {
		args = append(args, f.From, f.From, f.To, f.To)
	}
	return append(args, limit, offset)
}

// scanRecord は、取得の文の1行を読みます。
func scanRecord(k *kindStatements, scan func(dest ...any) error) (record, error) {
	var r record
	var created, updated int64
	vals := make([]any, len(k.columns))
	dest := []any{&r.ID, &r.Owner, &r.Revision, &created, &updated}
	for i := range vals {
		dest = append(dest, &vals[i])
	}
	if err := scan(dest...); err != nil {
		return r, err
	}
	r.CreatedAt, r.UpdatedAt = time.Unix(created, 0), time.Unix(updated, 0)
	r.Values = map[string]any{}
	for i, c := range k.columns {
		v := vals[i]
		if b, ok := v.([]byte); ok {
			v = string(b)
		}
		r.Values[c.name] = v
	}
	return r, nil
}

// listRecords は、一覧の1ページを返します。並びの番号が範囲の外なら既定の並び（0）にします。
func listRecords(ctx context.Context, db *sql.DB, k *kindStatements, f listFilter, limit, offset int) ([]record, error) {
	if f.Sort < 0 || f.Sort >= len(k.list) {
		f.Sort = 0
	}
	rows, err := queryStatement(ctx, db, k.list[f.Sort], listArgs(k, f, limit, offset)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []record
	for rows.Next() {
		r, err := scanRecord(k, rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// getRecord は、1件を返します。無ければ ErrNotFound です。
func getRecord(ctx context.Context, q querier, k *kindStatements, id int64) (record, error) {
	r, err := scanRecord(k, queryRowStatement(ctx, q, k.get, id).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	return r, err
}

// valuesArgs は、今の項目の値を、列の順に並べます。
func valuesArgs(k *kindStatements, values map[string]any) []any {
	args := make([]any, 0, len(k.columns))
	for _, c := range k.columns {
		args = append(args, values[c.name])
	}
	return args
}

// insertRecord は、記録を作ります。知っている列だけを名前で書きます。
func insertRecord(ctx context.Context, db *sql.DB, k *kindStatements, owner int64, values map[string]any, now time.Time) (int64, error) {
	args := append([]any{owner, now.Unix(), now.Unix()}, valuesArgs(k, values)...)
	res, err := execStatement(ctx, db, k.insert, args...)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// updateRecordRevision は、開いた時の版の数が今と同じ時だけ保存します。照合と書き込みは1つの文で行います。
// 版の数が違えば ErrConflict、記録が無ければ ErrNotFound を返します。知っている列だけを書きます。
// 不変条件: RECORDS-INV-5, RECORDS-INV-18
func updateRecordRevision(ctx context.Context, db *sql.DB, k *kindStatements, id, revision int64, values map[string]any, now time.Time) error {
	args := append(valuesArgs(k, values), now.Unix(), id, revision)
	res, err := execStatement(ctx, db, k.update, args...)
	return revisionResult(ctx, db, k, id, res, err)
}

// deleteRecordRevision は、開いた時の版の数が今と同じ時だけ消します。
// 不変条件: RECORDS-INV-5
func deleteRecordRevision(ctx context.Context, db *sql.DB, k *kindStatements, id, revision int64) error {
	res, err := execStatement(ctx, db, k.del, id, revision)
	return revisionResult(ctx, db, k, id, res, err)
}

// revisionResult は、版の数で照合した書き込みの結果を、無い（ErrNotFound）と変わった（ErrConflict）に分けます。
func revisionResult(ctx context.Context, db *sql.DB, k *kindStatements, id int64, res sql.Result, err error) error {
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n == 1 {
		return err
	}
	if _, err := getRecord(ctx, db, k, id); err != nil {
		return err
	}
	return ErrConflict
}

// deletedOwnerCounts は、所有者ごとの件数です。呼び出し側が、アカウントの一覧に無い（削除済みの）所有者だけを選びます。
// 不変条件: RECORDS-INV-10
func deletedOwnerCounts(ctx context.Context, db *sql.DB, k *kindStatements) (map[int64]int, error) {
	rows, err := queryStatement(ctx, db, k.ownerCounts)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]int{}
	for rows.Next() {
		var owner int64
		var n int
		if err := rows.Scan(&owner, &n); err != nil {
			return nil, err
		}
		out[owner] = n
	}
	return out, rows.Err()
}

// purgeDeletedOwnerRecords は、1つの種類の、1人の削除済みの所有者の記録を全部消します。
// 呼び出し側は、実行の時にアカウントの一覧を読み直し、所有者が削除済みであることを確かめてから呼びます。
// 不変条件: RECORDS-INV-10
func purgeDeletedOwnerRecords(ctx context.Context, db *sql.DB, k *kindStatements, owner int64) (int64, error) {
	var n int64
	err := store.Atomic(ctx, db, func(c *sql.Conn) error {
		res, err := execStatement(ctx, c, k.purgeOwner, owner)
		if err != nil {
			return err
		}
		n, err = res.RowsAffected()
		return err
	})
	return n, err
}
