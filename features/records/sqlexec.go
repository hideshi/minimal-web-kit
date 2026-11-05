package records

import (
	"context"
	"database/sql"
)

// recordsStatement は、検査済みの定義から組み立てたSQLの文です。値を作れるのは buildRecordStatements だけで、
// 要求の値で文の文字列は変わりません（test/sql_static_test.go の例外と、records_ast_test.go の検査で守る）。
// 不変条件: RECORDS-INV-7
type recordsStatement struct {
	sql string
}

// querier は、*sql.DB・*sql.Conn・*sql.Tx のどれでも受けるための口です。
type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// statementHook は、テストの時だけ、実行した文を記録するための口です。本番では nil です。
var statementHook func(recordsStatement)

// 業務記録の、定義から組み立てた文を実行するのは、このファイルの3つの関数だけです。

func execStatement(ctx context.Context, q querier, st recordsStatement, args ...any) (sql.Result, error) {
	if statementHook != nil {
		statementHook(st)
	}
	return q.ExecContext(ctx, st.sql, args...)
}

func queryStatement(ctx context.Context, q querier, st recordsStatement, args ...any) (*sql.Rows, error) {
	if statementHook != nil {
		statementHook(st)
	}
	return q.QueryContext(ctx, st.sql, args...)
}

func queryRowStatement(ctx context.Context, q querier, st recordsStatement, args ...any) *sql.Row {
	if statementHook != nil {
		statementHook(st)
	}
	return q.QueryRowContext(ctx, st.sql, args...)
}
