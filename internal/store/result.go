package store

import (
	"database/sql"
	"errors"
)

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

// Busy は、SQLiteの一時的な書き込みの競合（SQLITE_BUSY・SQLITE_LOCKED。拡張コードを含む）かを判定します。
// 時間を置けば通る失敗として、503と Retry-After で返すかを画面ごとに決めるのに使います。エラーの文字列は表示・記録しない。
func Busy(err error) bool {
	var coded interface{ Code() int }
	return errors.As(err, &coded) && (coded.Code()&255 == 5 || coded.Code()&255 == 6)
}
