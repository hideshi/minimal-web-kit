package news

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"minimal-web-kit/features/auth"
	"minimal-web-kit/internal/domain"
	"minimal-web-kit/internal/store"
	"minimal-web-kit/internal/web"
)

// lookupStore はCoreの匿名セッションの照合・作成を数えます。
type lookupStore struct {
	store.SessionStore
	lookups, creates int
}

func (s *lookupStore) GetSessionOwner(ctx context.Context, hash string) (domain.OwnerID, error) {
	s.lookups++
	return s.SessionStore.GetSessionOwner(ctx, hash)
}

func (s *lookupStore) CreateSession(ctx context.Context, hash string, owner domain.OwnerID) error {
	s.creates++
	return s.SessionStore.CreateSession(ctx, hash, owner)
}

type publicFixture struct {
	t       *testing.T
	feature *Feature
	db      *sql.DB
	routes  http.Handler
	posts   *PostStore
	core    *lookupStore
	sm      *web.SessionManager
	logs    *strings.Builder
}

// newPublicFixture はCoreのルーティングにお知らせ機能を組み込んだ状態を作ります。
func newPublicFixture(t *testing.T, corsOrigins string) *publicFixture {
	t.Helper()
	t.Setenv("NEWS_CORS_ORIGINS", corsOrigins)
	sqlite, db := newTestDB(t)
	logs := &strings.Builder{}
	logger := slog.New(slog.NewTextHandler(logs, nil))
	f := NewFeature(logger)
	if err := f.Init(db); err != nil {
		t.Fatal(err)
	}
	// 管理画面のために、起動処理と同じ形で管理者認証を組み込む
	t.Setenv("AUTH_ORIGIN", "")
	t.Setenv("AUTH_RP_ID", "")
	admin := auth.NewFeature(logger)
	if err := admin.Init(db); err != nil {
		t.Fatal(err)
	}
	admin.SetAdminNavLinks(f.AdminNavLinks())
	if err := f.SetAdmin(admin.Admin()); err != nil {
		t.Fatal(err)
	}
	core := &lookupStore{SessionStore: sqlite}
	sm := web.NewSessionManager(core, false)
	h, err := web.NewHandler(core, sm, logger)
	if err != nil {
		t.Fatal(err)
	}
	routes, err := h.RoutesWithIsolated(nil, f.RegisterRoutes, func(mux *web.IsolatedMux) {
		admin.RegisterIsolatedRoutes(mux)
		f.RegisterIsolatedRoutes(mux)
	})
	if err != nil {
		t.Fatal(err)
	}
	return &publicFixture{t: t, feature: f, db: db, routes: routes, posts: f.posts, core: core, sm: sm, logs: logs}
}

func (fx *publicFixture) do(t *testing.T, method, target string, headers map[string]string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	fx.routes.ServeHTTP(rec, req)
	return rec
}

func (fx *publicFixture) publish(t *testing.T, title, body string) int64 {
	t.Helper()
	id, err := fx.posts.Create(context.Background(), title, body)
	if err != nil {
		t.Fatal(err)
	}
	if err := fx.posts.Publish(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	return id
}

// 不変条件: NEWS-INV-1, NEWS-INV-5
func TestNewsPages_ListShowAndEscape(t *testing.T) {
	fx := newPublicFixture(t, "")
	id := fx.publish(t, "<script>alert(1)</script>見出し", "一行目\n二行目\n\n<b>段落</b>")
	draft, err := fx.posts.Create(context.Background(), "下書きの記事", "")
	if err != nil {
		t.Fatal(err)
	}

	rec := fx.do(t, http.MethodGet, "/news", nil)
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, "/news/"+strconv.FormatInt(id, 10)) {
		t.Fatalf("list: %d", rec.Code)
	}
	if strings.Contains(body, "<script>alert") || !strings.Contains(body, "&lt;script&gt;") {
		t.Fatal("title must be escaped")
	}
	if strings.Contains(body, "下書きの記事") {
		t.Fatal("drafts must not be listed")
	}
	if !strings.Contains(rec.Header().Get("Content-Security-Policy"), "script-src 'none'") {
		t.Fatal("public pages must keep script-src 'none'")
	}

	rec = fx.do(t, http.MethodGet, "/news/"+strconv.FormatInt(id, 10), nil)
	body = rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, "一行目<br>二行目") || !strings.Contains(body, "&lt;b&gt;段落&lt;/b&gt;") {
		t.Fatalf("show must render escaped paragraphs and line breaks: %d", rec.Code)
	}

	for target, want := range map[string]int{
		"/news/" + strconv.FormatInt(draft, 10): http.StatusNotFound,
		"/news/999999":                          http.StatusNotFound,
		"/news/0":                               http.StatusNotFound,
		"/news/99999999999999999999":            http.StatusNotFound,
		"/news/abc":                             http.StatusBadRequest,
		"/news/-1":                              http.StatusBadRequest,
		"/news?page=0":                          http.StatusBadRequest,
		"/news?page=x":                          http.StatusBadRequest,
		"/news?page=999999":                     http.StatusBadRequest,
	} {
		if rec := fx.do(t, http.MethodGet, target, nil); rec.Code != want {
			t.Errorf("%s: status %d, want %d", target, rec.Code, want)
		}
	}
}

func TestNewsPages_Pagination(t *testing.T) {
	fx := newPublicFixture(t, "")
	for i := 0; i < newsPageSize+1; i++ {
		fx.publish(t, "post-"+strconv.Itoa(i), "")
	}
	page1 := fx.do(t, http.MethodGet, "/news", nil).Body.String()
	if strings.Count(page1, `<li class="card">`) != newsPageSize || !strings.Contains(page1, `href="/news?page=2"`) || strings.Contains(page1, `rel="prev"`) {
		t.Fatal("page 1 must show 20 posts and a link to page 2 only")
	}
	page2 := fx.do(t, http.MethodGet, "/news?page=2", nil).Body.String()
	if strings.Count(page2, `<li class="card">`) != 1 || !strings.Contains(page2, `href="/news?page=1"`) || strings.Contains(page2, `rel="next"`) {
		t.Fatal("page 2 must show the remaining post and a link back")
	}
}

// 公開ページ・APIは、Coreの匿名セッションCookieを照合・発行・消去しないこと。
// 不変条件: NEWS-INV-6
func TestPublicRoutes_IgnoreCoreSessions(t *testing.T) {
	fx := newPublicFixture(t, "")
	id := fx.publish(t, "t", "b")
	seed := httptest.NewRecorder()
	if _, _, err := fx.sm.EnsureSession(seed, httptest.NewRequest(http.MethodGet, "/", nil)); err != nil {
		t.Fatal(err)
	}
	valid := seed.Result().Cookies()[0]
	fx.core.lookups, fx.core.creates = 0, 0
	for _, path := range []string{"/news", "/news/" + strconv.FormatInt(id, 10), "/api/news", "/api/news/" + strconv.FormatInt(id, 10)} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			for _, c := range []*http.Cookie{nil, valid, {Name: "session", Value: "unknown"}} {
				var cookies []*http.Cookie
				if c != nil {
					cookies = append(cookies, c)
				}
				rec := fx.do(t, method, path, nil, cookies...)
				if rec.Code != http.StatusOK {
					t.Fatalf("%s %s: %d", method, path, rec.Code)
				}
				if len(rec.Header().Values("Set-Cookie")) != 0 {
					t.Fatalf("%s %s modified cookies", method, path)
				}
			}
		}
	}
	if fx.core.lookups != 0 || fx.core.creates != 0 {
		t.Fatalf("public news routes touched Core sessions: lookups=%d creates=%d", fx.core.lookups, fx.core.creates)
	}
}

// 不変条件: NEWS-INV-1, NEWS-INV-7
func TestAPI_ListAndShow(t *testing.T) {
	fx := newPublicFixture(t, "")
	first := fx.publish(t, "first", "body-1")
	second := fx.publish(t, "second", "body-2")
	if _, err := fx.posts.Create(context.Background(), "draft", ""); err != nil {
		t.Fatal(err)
	}

	rec := fx.do(t, http.MethodGet, "/api/news?limit=1", nil)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatalf("list: %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
	var list struct {
		Items      []map[string]any `json:"items"`
		Limit      int              `json:"limit"`
		Offset     int              `json:"offset"`
		NextOffset *int             `json:"next_offset"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 || list.Items[0]["id"] != float64(second) || list.NextOffset == nil || *list.NextOffset != 1 {
		t.Fatalf("unexpected list: %s", rec.Body.String())
	}
	var keys []string
	for k := range list.Items[0] {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	if !slices.Equal(keys, []string{"body", "id", "published_at", "title", "updated_at"}) {
		t.Fatalf("API must expose only public fields, got %v", keys)
	}
	if _, err := time.Parse(time.RFC3339, list.Items[0]["published_at"].(string)); err != nil {
		t.Fatal("published_at must be RFC 3339")
	}

	rec = fx.do(t, http.MethodGet, "/api/news?offset=1&limit=50", nil)
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list.Items) != 1 || list.NextOffset != nil {
		t.Fatalf("drafts must not be listed and the last page has no next_offset: %s", rec.Body.String())
	}
	if !strings.Contains(fx.do(t, http.MethodGet, "/api/news?offset=10", nil).Body.String(), `"items":[]`) {
		t.Fatal("empty pages must return an empty array, not null")
	}

	rec = fx.do(t, http.MethodGet, "/api/news/"+strconv.FormatInt(first, 10), nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"body":"body-1"`) {
		t.Fatalf("show: %d %s", rec.Code, rec.Body.String())
	}
}

// 不変条件: NEWS-INV-11
func TestAPI_ErrorsDoNotReflectInput(t *testing.T) {
	fx := newPublicFixture(t, "")
	draft, err := fx.posts.Create(context.Background(), "draft", "")
	if err != nil {
		t.Fatal(err)
	}
	const canary = "canary-x9"
	for target, want := range map[string]struct {
		status int
		body   string
	}{
		"/api/news?limit=0":                         {400, `{"error":"bad_request"}`},
		"/api/news?limit=51":                        {400, `{"error":"bad_request"}`},
		"/api/news?offset=-1":                       {400, `{"error":"bad_request"}`},
		"/api/news?limit=" + canary:                 {400, `{"error":"bad_request"}`},
		"/api/news/" + canary:                       {400, `{"error":"bad_request"}`},
		"/api/news/" + strconv.FormatInt(draft, 10): {404, `{"error":"not_found"}`},
		"/api/news/999999":                          {404, `{"error":"not_found"}`},
	} {
		rec := fx.do(t, http.MethodGet, target, nil)
		if rec.Code != want.status || rec.Body.String() != want.body {
			t.Errorf("%s: %d %q", target, rec.Code, rec.Body.String())
		}
	}
	if strings.Contains(fx.logs.String(), canary) {
		t.Fatal("request values leaked into logs")
	}
}

// 不変条件: NEWS-INV-8
func TestAPI_CORS(t *testing.T) {
	const allowed = "https://www.example.com"
	fx := newPublicFixture(t, allowed+", http://localhost:3000")
	id := fx.publish(t, "t", "b")
	show := "/api/news/" + strconv.FormatInt(id, 10)

	// 許可元: ACAOを返し、資格情報は許可しない
	for _, target := range []string{"/api/news", show, "/api/news/999999"} {
		rec := fx.do(t, http.MethodGet, target, map[string]string{"Origin": allowed})
		if rec.Header().Get("Access-Control-Allow-Origin") != allowed || rec.Header().Get("Access-Control-Allow-Credentials") != "" {
			t.Fatalf("%s: allowed origin not granted correctly", target)
		}
		if !slices.Contains(rec.Header().Values("Vary"), "Origin") {
			t.Fatalf("%s: Vary: Origin missing", target)
		}
	}
	// 許可リスト外・null・Originなし: ACAOを返さない
	for _, origin := range []string{"https://evil.example", "null", "https://www.example.com.evil.example", ""} {
		rec := fx.do(t, http.MethodGet, "/api/news", map[string]string{"Origin": origin})
		if rec.Code != http.StatusOK || rec.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Fatalf("origin %q must not be granted", origin)
		}
		if !slices.Contains(rec.Header().Values("Vary"), "Origin") {
			t.Fatal("Vary: Origin must be set for every API response")
		}
	}
	// プリフライト
	rec := fx.do(t, http.MethodOptions, show, map[string]string{"Origin": allowed, "Access-Control-Request-Method": "GET"})
	if rec.Code != http.StatusNoContent || rec.Header().Get("Access-Control-Allow-Methods") != "GET, HEAD" || rec.Header().Get("Access-Control-Allow-Origin") != allowed {
		t.Fatalf("preflight from allowed origin: %d", rec.Code)
	}
	rec = fx.do(t, http.MethodOptions, "/api/news", map[string]string{"Origin": "https://evil.example"})
	if rec.Code != http.StatusForbidden || rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("preflight from other origin: %d", rec.Code)
	}
	// HTMLの公開ページにはCORSヘッダーを出さない
	rec = fx.do(t, http.MethodGet, "/news", map[string]string{"Origin": allowed})
	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("HTML pages must not send CORS headers")
	}
}

// 不変条件: NEWS-INV-8
func TestAPI_NoCORSByDefault(t *testing.T) {
	fx := newPublicFixture(t, "")
	rec := fx.do(t, http.MethodGet, "/api/news", map[string]string{"Origin": "http://localhost:3000"})
	if rec.Code != http.StatusOK || rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("no origin may be allowed by default")
	}
}
