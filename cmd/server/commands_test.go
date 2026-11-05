package main

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"minimal-web-kit/features"
	"minimal-web-kit/internal/web"
)

type commandFeature struct{ ran []string }

func (f *commandFeature) Name() string                                              { return "test" }
func (f *commandFeature) Init(db *sql.DB) error                                     { return nil }
func (f *commandFeature) RegisterRoutes(mux *http.ServeMux, sm *web.SessionManager) {}
func (f *commandFeature) Commands() []features.Command {
	return []features.Command{
		{Name: "ok", Summary: "succeeds", Run: func(ctx context.Context, db *sql.DB, args []string, in io.Reader, out io.Writer) error {
			f.ran = append(f.ran, strings.Join(args, ","))
			_, err := io.WriteString(out, "done")
			return err
		}},
		{Name: "fail", Summary: "fails", Run: func(ctx context.Context, db *sql.DB, args []string, in io.Reader, out io.Writer) error {
			return errors.New("boom")
		}},
	}
}

func TestRunCommand(t *testing.T) {
	f := &commandFeature{}
	r := features.NewRegistry()
	r.Register(f)

	var out, errOut strings.Builder
	if code := runCommand([]string{"ok", "-yes"}, r, nil, strings.NewReader(""), &out, &errOut); code != 0 || out.String() != "done" || f.ran[0] != "-yes" {
		t.Fatalf("ok: code=%d out=%q ran=%v", code, out.String(), f.ran)
	}
	if code := runCommand([]string{"fail"}, r, nil, strings.NewReader(""), &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "boom") {
		t.Fatalf("fail: code=%d err=%q", code, errOut.String())
	}
	errOut.Reset()
	if code := runCommand([]string{"unknown"}, r, nil, strings.NewReader(""), &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "ok") || !strings.Contains(errOut.String(), "fail") {
		t.Fatalf("unknown: code=%d err=%q", code, errOut.String())
	}
	errOut.Reset()
	if code := runCommand([]string{"auth-setup"}, features.NewRegistry(), nil, strings.NewReader(""), &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "No commands") {
		t.Fatalf("no features: code=%d err=%q", code, errOut.String())
	}
}

type readOnlyFeature struct{ commandFeature }

func (f *readOnlyFeature) ReadOnlyCommands() []features.ReadOnlyCommand {
	return []features.ReadOnlyCommand{
		{Name: "peek", Summary: "reads", Run: func(ctx context.Context, dbPath string, args []string, out io.Writer) error {
			f.ran = append(f.ran, dbPath+"|"+strings.Join(args, ","))
			return nil
		}},
		{Name: "peek-fail", Summary: "fails", Run: func(ctx context.Context, dbPath string, args []string, out io.Writer) error {
			return errors.New("boom")
		}},
	}
}

// TestRunReadOnlyCommand は、DBを読むだけのコマンドが、DBのパスと引数を受け取って動き、
// 該当しない名前では ok=false を返して、通常のコマンドへ回ることを確かめます。
func TestRunReadOnlyCommand(t *testing.T) {
	f := &readOnlyFeature{}
	r := features.NewRegistry()
	r.Register(f)
	var out, errOut strings.Builder
	if code, ok := runReadOnlyCommand([]string{"peek", "a"}, r, "/data/app.db", &out, &errOut); !ok || code != 0 || len(f.ran) != 1 || f.ran[0] != "/data/app.db|a" {
		t.Fatalf("peek: code=%d ok=%v ran=%v", code, ok, f.ran)
	}
	if code, ok := runReadOnlyCommand([]string{"peek-fail"}, r, "/data/app.db", &out, &errOut); !ok || code != 1 || !strings.Contains(errOut.String(), "boom") {
		t.Fatalf("peek-fail: code=%d ok=%v err=%q", code, ok, errOut.String())
	}
	if _, ok := runReadOnlyCommand([]string{"ok"}, r, "/data/app.db", &out, &errOut); ok {
		t.Fatal("a normal command was taken as read-only")
	}
}
