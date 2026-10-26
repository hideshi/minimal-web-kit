package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestCommands_SetupAndRecover(t *testing.T) {
	_, db := newTestDB(t)
	ctx := context.Background()
	t.Setenv("AUTH_ORIGIN", "")
	t.Setenv("AUTH_RP_ID", "")

	var out strings.Builder
	if err := runSetup(ctx, db, nil, strings.NewReader(""), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "http://localhost:8080/admin/setup#token=") || strings.Contains(out.String(), "?token=") {
		t.Fatalf("setup must print a URL with the token in the fragment:\n%s", out.String())
	}

	// 中止した場合は何も変えない
	out.Reset()
	if err := runRecover(ctx, db, []string{"-account", "1"}, strings.NewReader("no\n"), &out); err == nil || !strings.Contains(err.Error(), "aborted") {
		t.Fatalf("recover must abort without confirmation, got %v", err)
	}
	if strings.Contains(out.String(), "#token=") {
		t.Fatal("aborted recover must not print a URL")
	}

	// 管理者がいれば、登録し直す管理者を選ばせる
	out.Reset()
	if err := runRecover(ctx, db, nil, strings.NewReader("recover\n"), &out); !errors.Is(err, ErrChooseAccount) {
		t.Fatalf("recover must ask which admin, got %v", err)
	}
	if !strings.Contains(out.String(), "1  管理者") || strings.Contains(out.String(), "#token=") {
		t.Fatalf("recover must list the admins without a URL:\n%s", out.String())
	}
	out.Reset()
	if err := runRecover(ctx, db, []string{"-account", "1"}, strings.NewReader("recover\n"), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "/admin/setup#token=") {
		t.Fatal("recover must print a new URL")
	}
	out.Reset()
	if err := runRecover(ctx, db, []string{"-only", "1", "-yes"}, strings.NewReader(""), &out); err != nil {
		t.Fatal("-yes must skip the confirmation")
	}
	if !strings.Contains(out.String(), "/admin/setup#token=") {
		t.Fatal("recover -only must print a new URL")
	}
}

func TestCommands_ResetSchema(t *testing.T) {
	_, db := newTestDB(t)
	ctx := context.Background()
	t.Setenv("AUTH_ORIGIN", "")
	t.Setenv("AUTH_RP_ID", "")
	mustExec(t, db, "DROP TABLE auth_setup_tokens; DROP TABLE auth_sessions; DROP TABLE auth_credentials; DROP TABLE auth_accounts; CREATE TABLE auth_settings (id INTEGER PRIMARY KEY)")
	if err := runSetup(ctx, db, nil, strings.NewReader(""), &strings.Builder{}); err == nil || !strings.Contains(err.Error(), "-reset-schema") {
		t.Fatalf("setup must point to -reset-schema on the old schema, got %v", err)
	}
	if err := runRecover(ctx, db, []string{"-reset-schema"}, strings.NewReader("no\n"), &strings.Builder{}); err == nil {
		t.Fatal("reset-schema must ask for confirmation")
	}
	var out strings.Builder
	if err := runRecover(ctx, db, []string{"-reset-schema"}, strings.NewReader("recover\n"), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "/admin/setup#token=") {
		t.Fatal("reset-schema must print the first admin's URL")
	}
	assertRows(t, db, "SELECT COUNT(*) FROM sqlite_master WHERE name = 'auth_settings'", 0)
}

func TestCommands_SetupRefusesWhenRegisteredAndInvalidConfig(t *testing.T) {
	_, db := newTestDB(t)
	ctx := context.Background()
	t.Setenv("AUTH_ORIGIN", "")
	t.Setenv("AUTH_RP_ID", "")
	token, err := Setup(ctx, db, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	s := newAdminStore(db, time.Now)
	if err := s.registerWithSetupToken(ctx, token, testCredential("a"), "キー"); err != nil {
		t.Fatal(err)
	}
	if err := runSetup(ctx, db, nil, strings.NewReader(""), &strings.Builder{}); err == nil || !strings.Contains(err.Error(), "already registered") {
		t.Fatalf("setup must refuse when passkeys exist, got %v", err)
	}

	t.Setenv("AUTH_ORIGIN", "http://example.com")
	if err := runSetup(ctx, db, nil, strings.NewReader(""), &strings.Builder{}); err == nil {
		t.Fatal("invalid AUTH_ORIGIN must be rejected")
	}
	if err := runRecover(ctx, db, []string{"-account", "1", "-yes"}, strings.NewReader(""), &strings.Builder{}); err == nil {
		t.Fatal("recover must validate the config before deleting anything")
	}
	if creds, _ := s.listCredentials(ctx, 1); len(creds) != 1 {
		t.Fatal("a rejected recover must not delete passkeys")
	}
}
