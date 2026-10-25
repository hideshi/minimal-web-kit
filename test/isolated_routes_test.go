package test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"minimal-web-kit/internal/domain"
	"minimal-web-kit/internal/store"
	"minimal-web-kit/internal/web"
)

// lookupCountingStore はセッション照合の回数を数えます。
type lookupCountingStore struct {
	store.SessionStore
	lookups int
}

func (s *lookupCountingStore) GetSessionOwner(ctx context.Context, hash string) (domain.OwnerID, error) {
	s.lookups++
	return s.SessionStore.GetSessionOwner(ctx, hash)
}

func newIsolatedTestHandler(t *testing.T, s store.SessionStore) (*web.Handler, *web.SessionManager) {
	t.Helper()
	sm := web.NewSessionManager(s, false)
	h, err := web.NewHandler(s, sm, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return h, sm
}

// 独立経路は、CoreのCSRF検査とセッション解決を通らず、共通のセキュリティヘッダーは付くこと。
func TestIsolatedRoutes_BypassCoreSessionAndCSRF(t *testing.T) {
	s := &lookupCountingStore{SessionStore: store.NewMemoryStore()}
	h, sm := newIsolatedTestHandler(t, s)
	routes, err := h.RoutesWithIsolated(nil, nil, func(mux *web.IsolatedMux) {
		mux.HandleFunc("GET /iso", func(w http.ResponseWriter, r *http.Request) {
			if web.GetOwnerID(r.Context()) != "" {
				t.Error("isolated route must not resolve the Core owner")
			}
			_, _ = io.WriteString(w, "iso-get")
		})
		mux.HandleFunc("POST /iso", func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, "iso-post")
		})
	})
	if err != nil {
		t.Fatalf("routes: %v", err)
	}

	seed := httptest.NewRecorder()
	if _, _, err := sm.EnsureSession(seed, httptest.NewRequest(http.MethodGet, "/", nil)); err != nil {
		t.Fatal(err)
	}
	validCookie := seed.Result().Cookies()[0]

	for _, cookie := range []*http.Cookie{nil, validCookie, {Name: "session", Value: "unknown"}} {
		req := httptest.NewRequest(http.MethodGet, "/iso", nil)
		if cookie != nil {
			req.AddCookie(cookie)
		}
		rec := httptest.NewRecorder()
		routes.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || rec.Body.String() != "iso-get" {
			t.Fatalf("GET /iso: status %d body %q", rec.Code, rec.Body.String())
		}
		if len(rec.Header().Values("Set-Cookie")) != 0 {
			t.Fatal("isolated route must not modify Core cookies")
		}
		if !strings.Contains(rec.Header().Get("Content-Security-Policy"), "script-src 'none'") || rec.Header().Get("X-Request-ID") == "" {
			t.Fatal("isolated route must keep common security headers and request ID")
		}
	}
	if s.lookups != 0 {
		t.Fatalf("isolated route looked up Core sessions %d times", s.lookups)
	}

	// Coreセッションが無くてもPOSTがCoreのCSRF検査で拒否されないこと（検査は機能の責任）
	rec := httptest.NewRecorder()
	routes.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/iso", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "iso-post" {
		t.Fatalf("POST /iso: status %d", rec.Code)
	}

	// Coreの経路は従来どおりCSRF検査を受けること
	rec = httptest.NewRecorder()
	routes.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/account/delete-all", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("Core POST without session must be rejected, got %d", rec.Code)
	}
}

// Core・Feature・他の独立経路と同じパターンは、起動時エラーになること。
func TestIsolatedRoutes_ConflictIsError(t *testing.T) {
	cases := map[string]struct {
		features func(mux *http.ServeMux, sm *web.SessionManager)
		isolated func(mux *web.IsolatedMux)
	}{
		"core route": {isolated: func(mux *web.IsolatedMux) {
			mux.Handle("GET /privacy", http.NotFoundHandler())
		}},
		"static": {isolated: func(mux *web.IsolatedMux) {
			mux.Handle("GET /static/", http.NotFoundHandler())
		}},
		"feature route": {
			features: func(mux *http.ServeMux, _ *web.SessionManager) {
				mux.Handle("GET /news", http.NotFoundHandler())
			},
			isolated: func(mux *web.IsolatedMux) {
				mux.Handle("GET /news", http.NotFoundHandler())
			},
		},
		"duplicate isolated": {isolated: func(mux *web.IsolatedMux) {
			mux.Handle("GET /news", http.NotFoundHandler())
			mux.Handle("GET /news", http.NotFoundHandler())
		}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			h, _ := newIsolatedTestHandler(t, store.NewMemoryStore())
			if _, err := h.RoutesWithIsolated(nil, c.features, c.isolated); err == nil {
				t.Fatal("expected route conflict error")
			}
		})
	}
}
