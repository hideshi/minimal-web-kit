package store

import "database/sql"

// AffectedOne は、更新・削除が1行以上に効いたかを確かめます。実行のエラーと RowsAffected のエラーはそのまま返し、
// 0行なら ifNone（例: 機能の ErrNotFound）を返します:
//
//	res, err := db.ExecContext(ctx, "DELETE ...")
//	return store.AffectedOne(res, err, ErrNotFound)
func AffectedOne(res sql.Result, err error, ifNone error) error {
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ifNone
	}
	return nil
}
