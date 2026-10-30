package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"minimal-web-kit/internal/store"
)

// backup は複製を作って検査し、verify-backup は表の行数だけを表示すること。
// ユースケース: UC-OPS-1, UC-OPS-2
func TestBackupCommands(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "app.db")
	s, err := store.NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateSession(context.Background(), "カナリアのトークン", store.OwnerID("カナリアの持ち主")); err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	backup := filepath.Join(dir, "backup.db")
	var out, errOut strings.Builder
	if code := runBackup([]string{backup}, dbPath, &out, &errOut); code != 0 {
		t.Fatalf("backup: %d %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "verified") {
		t.Fatalf("output: %s", out.String())
	}
	// 既にあるファイルには書かない
	if code := runBackup([]string{backup}, dbPath, &out, &errOut); code != 1 {
		t.Fatalf("overwrite: %d", code)
	}
	out.Reset()
	if code := runVerifyBackup([]string{backup}, &out, &errOut); code != 0 {
		t.Fatalf("verify: %d", code)
	}
	if !strings.Contains(out.String(), "owners") || !strings.HasSuffix(out.String(), "OK\n") || strings.Contains(out.String(), "カナリア") {
		t.Fatalf("output: %s", out.String())
	}
}

// 使い方の誤りは2、DBや複製が無ければ1で失敗し、DBを新しく作らないこと。
func TestBackupCommands_Failures(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "none.db")
	var out, errOut strings.Builder
	if code := runBackup(nil, missing, &out, &errOut); code != 2 {
		t.Fatalf("usage: %d", code)
	}
	if code := runBackup([]string{filepath.Join(dir, "backup.db")}, missing, &out, &errOut); code != 1 {
		t.Fatalf("missing database: %d", code)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("backup must not create the database")
	}
	if code := runVerifyBackup(nil, &out, &errOut); code != 2 {
		t.Fatalf("usage: %d", code)
	}
	if code := runVerifyBackup([]string{missing}, &out, &errOut); code != 1 {
		t.Fatalf("missing backup: %d", code)
	}
}
