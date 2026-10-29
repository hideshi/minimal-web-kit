package test

import (
	"database/sql"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"minimal-web-kit/features"
	"minimal-web-kit/features/auth"
	"minimal-web-kit/features/news"
	"minimal-web-kit/features/site"
	"minimal-web-kit/internal/app"
	"minimal-web-kit/internal/store"
	"minimal-web-kit/internal/web"
)

// fallbackProbe は、受け皿に来た要求を記録する見本の機能です（固定ページの代わり）。
type fallbackProbe struct {
	hits     []string
	notFound http.HandlerFunc
	taken    func(string) bool
}

func (p *fallbackProbe) Name() string                                              { return "probe" }
func (p *fallbackProbe) Init(db *sql.DB) error                                     { return nil }
func (p *fallbackProbe) RegisterRoutes(mux *http.ServeMux, sm *web.SessionManager) {}
func (p *fallbackProbe) SetNotFound(h http.HandlerFunc)                            { p.notFound = h }
func (p *fallbackProbe) SetRouteTaken(taken func(string) bool)                     { p.taken = taken }
func (p *fallbackProbe) HandleFallback(w http.ResponseWriter, r *http.Request) {
	p.hits = append(p.hits, r.Method+" "+r.URL.Path)
	if r.URL.Path == "/about" {
		_, _ = io.WriteString(w, "probe-page")
		return
	}
	p.notFound(w, r)
}

func fallbackRoutes(t *testing.T, extra ...features.Feature) (http.Handler, error) {
	t.Helper()
	t.Setenv("AUTH_ORIGIN", "")
	t.Setenv("AUTH_RP_ID", "")
	core, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { core.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	reg := features.NewRegistry()
	reg.Register(site.NewFeature(logger))
	reg.Register(auth.NewFeature(logger))
	reg.Register(news.NewFeature(logger))
	for _, f := range extra {
		reg.Register(f)
	}
	for _, f := range reg.All() {
		if err := f.Init(core.DB()); err != nil {
			t.Fatal(err)
		}
	}
	return app.BuildRoutes(core, reg, false, logger)
}

// 受け皿は、どの経路にも当たらないGET・HEADだけを受け、Coreと機能の経路を取らない。404はSiteの画面。
func TestFallback_OnlyUnmatchedGetAndKeepsCoreRoutes(t *testing.T) {
	probe := &fallbackProbe{}
	routes, err := fallbackRoutes(t, probe)
	if err != nil {
		t.Fatal(err)
	}
	do := func(method, path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		routes.ServeHTTP(w, httptest.NewRequest(method, path, nil))
		return w
	}
	for _, path := range []string{"/", "/privacy", "/news", "/admin/login", "/static/css/style.css", "/favicon.ico", "/robots.txt"} {
		if w := do(http.MethodGet, path); w.Code != http.StatusOK {
			t.Errorf("%s: %d", path, w.Code)
		}
	}
	if len(probe.hits) != 0 {
		t.Fatalf("core and feature routes must not reach the fallback: %v", probe.hits)
	}
	if w := do(http.MethodGet, "/about"); w.Code != http.StatusOK || w.Body.String() != "probe-page" || w.Header().Get("Set-Cookie") != "" {
		t.Fatalf("fallback page: %d", w.Code)
	}
	for _, path := range []string{"/missing", "/a/b", "/about/"} {
		w := do(http.MethodGet, path)
		if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "ページが見つかりません") {
			t.Errorf("%s must be the Site 404: %d", path, w.Code)
		}
	}
	if w := do(http.MethodHead, "/about"); w.Code != http.StatusOK {
		t.Fatalf("HEAD: %d", w.Code)
	}
	before := len(probe.hits)
	do(http.MethodPost, "/about")
	do(http.MethodPost, "/privacy")
	if len(probe.hits) != before {
		t.Fatal("only GET and HEAD may reach the fallback")
	}

	for path, want := range map[string]bool{"/privacy": true, "/news": true, "/admin": true, "/static": true, "/favicon.ico": true, "/about": false, "/company": false} {
		if got := probe.taken(path); got != want {
			t.Errorf("RouteTaken(%s) = %v, want %v", path, got, want)
		}
	}
}

func TestFallback_OnlyOne(t *testing.T) {
	if _, err := fallbackRoutes(t, &fallbackProbe{}, &namedProbe{}); err == nil || !strings.Contains(err.Error(), "handle unmatched paths") {
		t.Fatalf("two fallbacks must stop the start: %v", err)
	}
}

type namedProbe struct{ fallbackProbe }

func (p *namedProbe) Name() string { return "probe2" }
