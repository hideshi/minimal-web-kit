package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
)

// Rebuild は、CHECK 等を変えるために表を作り直す指定です。
type Rebuild struct {
	Table string // 作り直す表。他の表から参照されていてよい
	Label string // 複製のファイル名に入れる語
	// Current は、表の定義が今の形式かを返します。true なら何もしません。
	Current func(definition string) bool
	// Define は、今の定義を新しい定義へ直します。形が分からなければエラーを返します。
	Define func(definition string) (string, error)
	// Columns は、値を変える列の式です（例: "role": "CASE role WHEN 'a' THEN 'b' ELSE role END"）。無い列はそのまま写します。
	Columns map[string]string
	// After は、作り直しの後に同じトランザクションで流すSQLです。表と一緒に消えたトリガーをここで作り直します。
	After string
}

// RebuildTable は、複製を作ってから、表を1つのトランザクションで作り直します。表が無いか今の形式なら何もしません。
//
// 他の表が ON DELETE CASCADE で参照している表は、外部キーが有効なまま落とすと、参照する行が消えます。
// SQLiteの公式の手順に従い、接続を1本に固定して、トランザクションの外で外部キーを止めます。
// 表を落とすとトリガーも消えるので、After で作り直します。採番は、削除済みのIDを再利用しないよう引き継ぎます。
func RebuildTable(ctx context.Context, db *sql.DB, r Rebuild) error {
	current, err := tableSQL(ctx, db, r.Table)
	if err != nil {
		return err
	}
	if current == "" || r.Current(current) {
		return nil
	}
	path, err := BackupBeforeMigration(ctx, db, r.Label)
	if err != nil {
		return fmt.Errorf("backup before migration: %w", err)
	}
	if path != "" {
		slog.Info("migration: backup written", slog.String("file", filepath.Base(path)))
	}

	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := setForeignKeys(ctx, conn, false); err != nil {
		return err
	}
	defer func() {
		if err := setForeignKeys(context.Background(), conn, true); err != nil {
			// 外部キーの止まった接続をプールへ返さない
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
	}()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()
	if err := rebuild(ctx, conn, r); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return err
	}
	committed = true
	return nil
}

type rowQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func tableSQL(ctx context.Context, q rowQuerier, table string) (string, error) {
	var text string
	err := q.QueryRowContext(ctx, "SELECT sql FROM sqlite_master WHERE type = 'table' AND name = ?", table).Scan(&text)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return text, err
}

// setForeignKeys は接続の外部キーの設定を変え、読み返して確かめます。トランザクションの中では効きません。
func setForeignKeys(ctx context.Context, conn *sql.Conn, on bool) error {
	want := 0
	if on {
		want = 1
	}
	if _, err := conn.ExecContext(ctx, fmt.Sprintf("PRAGMA foreign_keys = %d", want)); err != nil {
		return err
	}
	var got int
	if err := conn.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&got); err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("foreign_keys is %d, want %d", got, want)
	}
	return nil
}

func rebuild(ctx context.Context, conn *sql.Conn, r Rebuild) error {
	current, err := tableSQL(ctx, conn, r.Table) // 別のプロセスが先に移行した場合に備えて、書き込みの権利を得てから読み直す
	if err != nil {
		return err
	}
	if current == "" || r.Current(current) {
		return nil
	}
	definition, err := r.Define(current)
	if err != nil {
		return err
	}
	work := r.Table + "_new"
	// 改名した表の定義は、SQLiteが名前を引用符で囲んで保存する
	body, ok := strings.CutPrefix(definition, "CREATE TABLE "+r.Table)
	if !ok {
		body, ok = strings.CutPrefix(definition, `CREATE TABLE "`+r.Table+`"`)
	}
	if !ok {
		return fmt.Errorf("migration: unexpected definition of %s", r.Table)
	}
	create := "CREATE TABLE " + work + body

	names, values, err := copyColumns(ctx, conn, r)
	if err != nil {
		return err
	}
	var seq sql.NullInt64
	err = conn.QueryRowContext(ctx, "SELECT seq FROM sqlite_sequence WHERE name = ?", r.Table).Scan(&seq)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	for _, q := range []string{
		create,
		"INSERT INTO " + work + " (" + names + ") SELECT " + values + " FROM " + r.Table,
		"DROP TABLE " + r.Table,
		"ALTER TABLE " + work + " RENAME TO " + r.Table,
		"DELETE FROM sqlite_sequence WHERE name = '" + r.Table + "'",
	} {
		if _, err := conn.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	if seq.Valid {
		if _, err := conn.ExecContext(ctx, "INSERT INTO sqlite_sequence (name, seq) VALUES (?, ?)", r.Table, seq.Int64); err != nil {
			return err
		}
	}
	if _, err := conn.ExecContext(ctx, r.After); err != nil {
		return err
	}
	var broken int
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM pragma_foreign_key_check WHERE parent = ?", r.Table).Scan(&broken); err != nil {
		return err
	}
	if broken > 0 {
		return fmt.Errorf("migration: %d rows lost their reference to %s", broken, r.Table)
	}
	return nil
}

// copyColumns は、写す先の列の並びと、写す元の式の並びを返します。
func copyColumns(ctx context.Context, conn *sql.Conn, r Rebuild) (string, string, error) {
	rows, err := conn.QueryContext(ctx, "SELECT name FROM pragma_table_info(?)", r.Table)
	if err != nil {
		return "", "", err
	}
	defer rows.Close()
	var names, values []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return "", "", err
		}
		names = append(names, name)
		if expr, ok := r.Columns[name]; ok {
			name = expr
		}
		values = append(values, name)
	}
	return strings.Join(names, ", "), strings.Join(values, ", "), rows.Err()
}
