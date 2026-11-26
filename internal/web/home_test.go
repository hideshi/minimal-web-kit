package web

import (
	"context"
	"io"
	"sync/atomic"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"minimal-web-kit/internal/store"
)

// countingStore は、セッションの照合（GetSessionOwner）の数を数えます。
type countingStore struct {
	store.SessionStore
	lookups atomic.Int64
}

func (s *countingStore) GetSessionOwner(ctx context.Context, tokenHash string) (store.OwnerID, error) {
	s.lookups.Add(1)
	return s.SessionStore.GetSessionOwner(ctx, tokenHash)
}

// TestRoutes_HomeRoot は、トップページの指定があるとき、/ で先に固定ページを試し、描かなければ今のトップ（独自のトップかBase）を返し、
// どちらでも / はセッションを照合も発行もせず、下の処理が公開キャッシュを付けても no-store で返し、/privacy の扱いは変えないことを確かめる。
// 不変条件: PAGES-INV-16, PAGES-INV-15
func TestRoutes_HomeRoot(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, c := range []struct {
		name   string
		custom bool // 今の独自のトップ（Site）があるか
		serve  bool // 固定ページを描くか
		want   string
	}{
		{"page over site", true, true, "固定ページ"},
		{"site fallback", true, false, "独自のトップ"},
		{"page without site", false, true, "固定ページ"},
		{"base fallback", false, false, "セッション未発行"},
	} {
		s := &countingStore{SessionStore: store.NewMemoryStore()}
		sm := NewSessionManager(s, false)
		h, err := NewHandler(s, sm, logger)
		if err != nil {
			t.Fatal(err)
		}
		h.SetHome(func(w http.ResponseWriter, r *http.Request) bool {
			if !c.serve {
				return false
			}
			_, _ = io.WriteString(w, "固定ページ")
			return true
		})
		var root http.HandlerFunc
		if c.custom {
			root = func(w http.ResponseWriter, r *http.Request) {
				PublicCache(w, PublicCacheSeconds)
				_, _ = io.WriteString(w, "独自のトップ")
			}
		}
		routes := h.Routes(root, nil)

		// 既存のセッションCookie（今の名前）を付けても、照合・発行・削除をしない。照合したかはストアの読み取りの数で見る
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			req := httptest.NewRequest(method, "/", nil)
			req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "not-a-session"})
			rec := httptest.NewRecorder()
			routes.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("%s %s: %d", c.name, method, rec.Code)
			}
			if method == http.MethodGet && !strings.Contains(rec.Body.String(), c.want) {
				t.Errorf("%s: want %q in %.200s", c.name, c.want, rec.Body.String())
			}
			if cc := rec.Header().Get("Cache-Control"); !strings.HasPrefix(cc, "no-store") {
				t.Errorf("%s %s: Cache-Control %q", c.name, method, cc)
			}
			if sc := rec.Header().Values("Set-Cookie"); len(sc) != 0 {
				t.Errorf("%s %s: Set-Cookie %v", c.name, method, sc)
			}
		}
		if n := s.lookups.Load(); n != 0 {
			t.Errorf("%s: / looked up the session %d times", c.name, n)
		}

		// /privacy は、指定の有無で変わらない（独自のトップがあるときだけセッションを通さない）
		rec := httptest.NewRecorder()
		routes.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/privacy", nil))
		plain, err := NewHandler(s, sm, logger)
		if err != nil {
			t.Fatal(err)
		}
		want := httptest.NewRecorder()
		plain.Routes(root, nil).ServeHTTP(want, httptest.NewRequest(http.MethodGet, "/privacy", nil))
		if rec.Code != want.Code || rec.Header().Get("Cache-Control") != want.Header().Get("Cache-Control") ||
			len(rec.Header().Values("Set-Cookie")) != len(want.Header().Values("Set-Cookie")) {
			t.Errorf("%s: /privacy changed: %d %q vs %d %q", c.name, rec.Code, rec.Header().Get("Cache-Control"), want.Code, want.Header().Get("Cache-Control"))
		}
	}
}
