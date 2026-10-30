package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newBackupStore(t *testing.T) (*SQLiteStore, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := NewSQLiteStore(filepath.Join(dir, "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, dir
}

func rowsOf(tables []TableCount, name string) int64 {
	for _, t := range tables {
		if t.Name == name {
			return t.Rows
		}
	}
	return -1
}

// 複製は所有者だけが読め、WALに残っている書き込みも含み、検査を通り、開いて使えること。
// ユースケース: UC-OPS-1, UC-OPS-2
func TestBackup_CopiesLiveDatabase(t *testing.T) {
	ctx := context.Background()
	s, dir := newBackupStore(t)
	for _, token := range []string{"a", "b"} {
		if err := s.CreateSession(ctx, token, OwnerID("owner-"+token)); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(dir, "backup.db")
	if err := Backup(ctx, s.DB(), path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("permissions: %v", info.Mode().Perm())
	}
	tables, err := VerifyBackup(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if rowsOf(tables, "owners") != 2 || rowsOf(tables, "sessions") != 2 {
		t.Fatalf("counts: %+v", tables)
	}
	// 検査は複製を変えない
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyBackup(ctx, path); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("verify must not change the backup")
	}
	// 戻す: 複製をDBとして開ける
	restored, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if owner, err := restored.GetSessionOwner(ctx, "a"); err != nil || owner != "owner-a" {
		t.Fatalf("restored: %q %v", owner, err)
	}
}

// 既にあるファイルは上書きしないこと。
func TestBackup_RefusesExistingFile(t *testing.T) {
	s, dir := newBackupStore(t)
	path := filepath.Join(dir, "backup.db")
	if err := os.WriteFile(path, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Backup(context.Background(), s.DB(), path); err == nil {
		t.Fatal("must not overwrite")
	}
	if got, _ := os.ReadFile(path); string(got) != "keep" {
		t.Fatal("the file was changed")
	}
}

// 書き込みに失敗したら、空のファイルを残さないこと。
func TestBackup_RemovesFileOnFailure(t *testing.T) {
	s, dir := newBackupStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	path := filepath.Join(dir, "backup.db")
	if err := Backup(ctx, s.DB(), path); err == nil {
		t.Fatal("must fail")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a failed backup must be removed: %v", err)
	}
}

// 壊れたファイル、無いファイル、別のDB、外部キーの違反は、検査で失敗すること。
func TestVerifyBackup_RejectsBadFiles(t *testing.T) {
	ctx := context.Background()
	s, dir := newBackupStore(t)
	if err := s.CreateSession(ctx, "a", OwnerID("owner-a")); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyBackup(ctx, filepath.Join(dir, "none.db")); err == nil {
		t.Fatal("missing file must fail")
	}
	garbage := filepath.Join(dir, "garbage.db")
	if err := os.WriteFile(garbage, []byte(strings.Repeat("x", 8192)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyBackup(ctx, garbage); err == nil {
		t.Fatal("garbage must fail")
	}

	// 中身の一部を壊した複製
	path := filepath.Join(dir, "backup.db")
	if err := Backup(ctx, s.DB(), path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := 4096; i < len(data); i++ {
		data[i] = 0xff
	}
	broken := filepath.Join(dir, "broken.db")
	if err := os.WriteFile(broken, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyBackup(ctx, broken); err == nil {
		t.Fatal("a corrupted backup must fail")
	}

	// ファイルは読めるが、整合性の検査が問題を報告する複製
	invalid := filepath.Join(dir, "invalid.db")
	if err := Backup(ctx, s.DB(), invalid); err != nil {
		t.Fatal(err)
	}
	checked, err := sql.Open("sqlite", invalid)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := checked.Exec("CREATE TABLE t (x INTEGER CHECK (x > 0)); PRAGMA ignore_check_constraints = ON; INSERT INTO t VALUES (-1)"); err != nil {
		t.Fatal(err)
	}
	checked.Close()
	if _, err := VerifyBackup(ctx, invalid); err == nil || !strings.Contains(err.Error(), "corrupted") {
		t.Fatalf("an integrity problem must fail: %v", err)
	}

	other := filepath.Join(dir, "other.db")
	db, err := sql.Open("sqlite", other)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE x (id INTEGER)"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err := VerifyBackup(ctx, other); err == nil || !strings.Contains(err.Error(), "not a database") {
		t.Fatalf("another database: %v", err)
	}

	bad := filepath.Join(dir, "fk.db")
	if err := Backup(ctx, s.DB(), bad); err != nil {
		t.Fatal(err)
	}
	fk, err := sql.Open("sqlite", bad)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fk.Exec("PRAGMA foreign_keys = OFF; DELETE FROM owners"); err != nil {
		t.Fatal(err)
	}
	fk.Close()
	if _, err := VerifyBackup(ctx, bad); err == nil || !strings.Contains(err.Error(), "foreign key") {
		t.Fatalf("foreign keys: %v", err)
	}
}

// 移行の前の複製は、DBと同じ場所に作られること。メモリ上のDBでは何もしないこと。
func TestBackupBeforeMigration(t *testing.T) {
	ctx := context.Background()
	s, dir := newBackupStore(t)
	path, err := BackupBeforeMigration(ctx, s.DB(), "contact")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(path) != dir || !strings.Contains(filepath.Base(path), "pre-migration-contact-") {
		t.Fatalf("path: %s", path)
	}
	if _, err := VerifyBackup(ctx, path); err != nil {
		t.Fatal(err)
	}
	mem, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer mem.Close()
	if path, err := BackupBeforeMigration(ctx, mem.DB(), "contact"); err != nil || path != "" {
		t.Fatalf("memory: %q %v", path, err)
	}
}
