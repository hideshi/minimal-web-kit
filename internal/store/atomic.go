package store

import (
	"context"
	"database/sql"
)

// Atomic は、書き込みの権利を先に取ってから、専用の接続で fn を1つのトランザクションとして実行します。
// 判定と更新を同じトランザクションに閉じるときに使います。fn がエラーを返すと全部戻します。
func Atomic(ctx context.Context, db *sql.DB, fn func(*sql.Conn) error) error {
	c, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer c.Close()
	if _, err = c.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = c.ExecContext(context.Background(), "ROLLBACK")
		}
	}()
	if err = fn(c); err != nil {
		return err
	}
	if _, err = c.ExecContext(ctx, "COMMIT"); err != nil {
		return err
	}
	committed = true
	return nil
}
