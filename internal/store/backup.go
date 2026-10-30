package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

// TableCount は、複製の中の表の名前と行数です。
type TableCount struct {
	Name string
	Rows int64
}

// Backup は、稼働中のDBの整合した複製を path に作ります。SQLiteの VACUUM INTO を使うため、
// 書き込み中でも WAL の内容を含む1つのファイルになり、sqlite3 コマンドは要りません。
// 複製は個人情報を含むため、所有者だけが読める権限（0600）で作り、既にあるファイルは上書きしません。
func Backup(ctx context.Context, db *sql.DB, path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create backup file: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close backup file: %w", err)
	}
	if _, err := db.ExecContext(ctx, "VACUUM INTO ?", path); err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("write backup: %w", err)
	}
	return nil
}

// VerifyBackup は、複製が壊れていないこと、外部キーの違反が無いこと、このキットのDBであることを確かめ、
// 表ごとの行数を返します。読み取り専用で開き、複製を変えません。中身は返しません。
func VerifyBackup(ctx context.Context, path string) ([]TableCount, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("open backup: %w", err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	dsn := url.URL{Scheme: "file", Path: abs, RawQuery: "mode=ro&immutable=1"}
	db, err := sql.Open("sqlite", dsn.String())
	if err != nil {
		return nil, fmt.Errorf("open backup: %w", err)
	}
	defer db.Close()

	var result string
	if err := db.QueryRowContext(ctx, "PRAGMA integrity_check(1)").Scan(&result); err != nil {
		return nil, fmt.Errorf("check backup: %w", err)
	}
	if result != "ok" {
		return nil, errors.New("backup is corrupted")
	}
	var violation string
	switch err := db.QueryRowContext(ctx, `SELECT "table" FROM pragma_foreign_key_check LIMIT 1`).Scan(&violation); {
	case err == nil:
		return nil, errors.New("backup has foreign key violations")
	case !errors.Is(err, sql.ErrNoRows):
		return nil, fmt.Errorf("check foreign keys: %w", err)
	}

	rows, err := db.QueryContext(ctx, "SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name")
	if err != nil {
		return nil, fmt.Errorf("list tables: %w", err)
	}
	var tables []TableCount
	for rows.Next() {
		var t TableCount
		if err := rows.Scan(&t.Name); err != nil {
			rows.Close()
			return nil, err
		}
		tables = append(tables, t)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	isKit := false
	for i := range tables {
		isKit = isKit || tables[i].Name == "owners"
		// 表の名前は sqlite_master から取った値で、利用者の入力ではない
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM "`+tables[i].Name+`"`).Scan(&tables[i].Rows); err != nil {
			return nil, fmt.Errorf("count rows: %w", err)
		}
	}
	if !isKit {
		return nil, errors.New("not a database of this kit")
	}
	return tables, nil
}

// BackupBeforeMigration は、表を作り直す移行の前に、DBと同じ場所へ複製を作り、そのパスを返します。
// メモリ上のDBでは何もせず、空文字を返します。複製は自動では消えません（docs/deploy.md の9節）。
func BackupBeforeMigration(ctx context.Context, db *sql.DB, label string) (string, error) {
	var seq int
	var name, file string
	if err := db.QueryRowContext(ctx, "PRAGMA database_list").Scan(&seq, &name, &file); err != nil {
		return "", err
	}
	if file == "" {
		return "", nil
	}
	path := filepath.Join(filepath.Dir(file), fmt.Sprintf("pre-migration-%s-%s.db", label, time.Now().Format("20060102-150405")))
	if err := Backup(ctx, db, path); err != nil {
		return "", err
	}
	return path, nil
}
