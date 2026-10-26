package auth

import (
	"context"
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
	if err := runRecover(ctx, db, nil, strings.NewReader("no\n"), &out); err == nil || !strings.Contains(err.Error(), "aborted") {
		t.Fatalf("recover must abort without confirmation, got %v", err)
	}
	if strings.Contains(out.String(), "#token=") {
		t.Fatal("aborted recover must not print a URL")
	}

	out.Reset()
	if err := runRecover(ctx, db, nil, strings.NewReader("recover\n"), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "/admin/setup#token=") {
		t.Fatal("recover must print a new URL")
	}
	out.Reset()
	if err := runRecover(ctx, db, []string{"-yes"}, strings.NewReader(""), &out); err != nil {
		t.Fatal("-yes must skip the confirmation")
	}
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
	if err := runRecover(ctx, db, []string{"-yes"}, strings.NewReader(""), &strings.Builder{}); err == nil {
		t.Fatal("recover must validate the config before deleting anything")
	}
	if creds, _ := s.listCredentials(ctx); len(creds) != 1 {
		t.Fatal("a rejected recover must not delete passkeys")
	}
}
