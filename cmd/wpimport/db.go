package main

import (
	"database/sql"

	_ "modernc.org/sqlite" // internal/store と同じドライバ
)

// sqlOpen は、読むだけの接続を開きます（稼働中のDBと作業DBの表の有無の確かめ）。
func sqlOpen(dsn string) (*sql.DB, error) { return sql.Open("sqlite", dsn) }
