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
	if code := runCommand([]string{"cms-setup"}, features.NewRegistry(), nil, strings.NewReader(""), &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "No commands") {
		t.Fatalf("no features: code=%d err=%q", code, errOut.String())
	}
}
