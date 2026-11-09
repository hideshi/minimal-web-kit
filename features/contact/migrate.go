package contact

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"path/filepath"

	"minimal-web-kit/internal/store"
)

// legacyTable は、表を作り直す間だけ、旧形式の表に付ける名前です。
const legacyTable = "contact_messages_legacy"

// migrate は表を今の形式にします。新規のDBなら migration.sql を流すだけです。
//
// 旧形式（既読の状態 read、メモと最終更新の列が無い）のDBは、CHECK 制約を変えるために表を作り直します。
// 表の定義を二重に持たないよう、旧表を改名してから migration.sql を流し、行を写します。全体を1つの
// トランザクションで行うため、途中で失敗すれば元のままで、起動が止まります。表を作り直す前に、
// DBと同じ場所へ複製を作ります（複製に失敗したら移行しません）。
//
// 既読は完了に写します（既定の一覧に過去の既読を並べないため）。last_activity_at は created_at にして、
// 移行で削除の時期を延ばしません。id の採番は、旧表の sqlite_sequence の行をそのまま引き継ぎます
// （最新の行を削除した後は、写した行の最大の id より旧表の値の方が大きいため）。
// 不変条件: CONTACT-INV-12
func migrate(db *sql.DB, logger *slog.Logger) error {
	var working int
	if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?", legacyTable).Scan(&working); err != nil {
		return err
	}
	if working > 0 {
		return fmt.Errorf("%s exists; restore the database from a backup", legacyTable)
	}
	var table, column int
	if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'contact_messages'").Scan(&table); err != nil {
		return err
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('contact_messages') WHERE name = 'last_activity_at'").Scan(&column); err != nil {
		return err
	}
	if table == 0 || column > 0 {
		if _, err := db.Exec(migrationSQL); err != nil {
			return err
		}
		return addKindColumn(db)
	}

	path, err := store.BackupBeforeMigration(context.Background(), db, "contact")
	if err != nil {
		return fmt.Errorf("backup before migration: %w", err)
	}
	logger.Info("contact migration: backup written", slog.String("file", filepath.Base(path)))

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, q := range []string{
		"DROP INDEX IF EXISTS idx_contact_messages_created",
		"ALTER TABLE contact_messages RENAME TO " + legacyTable,
		migrationSQL,
		`INSERT INTO contact_messages (id, name, reply_to, body, status, note, last_activity_at, created_at)
		 SELECT id, name, reply_to, body, CASE status WHEN 'read' THEN 'done' ELSE status END, '', created_at, created_at
		 FROM ` + legacyTable,
		// 改名で旧表の行に付いて行った採番を、新しい表へ戻す。行を写したときに新しい表の行ができている
		"DELETE FROM sqlite_sequence WHERE name = 'contact_messages'",
		"UPDATE sqlite_sequence SET name = 'contact_messages' WHERE name = '" + legacyTable + "'",
		"DROP TABLE " + legacyTable,
	} {
		if _, err := tx.Exec(q); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// addKindColumn は、種別の列が無い表に、空文字を既定にした列を足します。表は作り直さないため、行数によらず
// すぐ終わります。古い実行ファイルは列名を指定して書き込むため、戻しても既定の空文字で動き続けます。
// 不変条件: CONTACT-INV-15
func addKindColumn(db *sql.DB) error {
	var kind int
	if err := db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('contact_messages') WHERE name = 'kind'").Scan(&kind); err != nil {
		return err
	}
	if kind > 0 {
		return nil
	}
	_, err := db.Exec("ALTER TABLE contact_messages ADD COLUMN kind TEXT NOT NULL DEFAULT ''")
	return err
}
