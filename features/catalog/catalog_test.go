package catalog

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"minimal-web-kit/features"
	"minimal-web-kit/features/auth"
	"minimal-web-kit/features/auth/authtest"
	"minimal-web-kit/features/news"
	"minimal-web-kit/features/site"
	"minimal-web-kit/internal/app"
	"minimal-web-kit/internal/store"
	"minimal-web-kit/internal/web"
)

type fixture struct {
	f      *Feature
	core   *store.SQLiteStore
	routes http.Handler
	logs   *bytes.Buffer
}

func setup(t *testing.T, withSite bool) *fixture {
	t.Helper()
	t.Setenv("AUTH_ORIGIN", "")
	t.Setenv("AUTH_RP_ID", "")
	t.Setenv("NEWS_CORS_ORIGINS", "")
	core, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { core.Close() })
	logs := &bytes.Buffer{}
	logger := slog.New(slog.NewTextHandler(logs, nil))
	f := NewFeature(logger)
	reg := features.NewRegistry()
	if withSite {
		reg.Register(site.NewFeature(logger))
	}
	reg.Register(f)
	reg.Register(auth.NewFeature(logger))
	reg.Register(news.NewFeature(logger))
	for _, feature := range reg.All() {
		if err := feature.Init(core.DB()); err != nil {
			t.Fatal(err)
		}
	}
	routes, err := app.BuildRoutes(core, reg, false, logger)
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{f, core, routes, logs}
}
func (fx *fixture) send(method, target string, values url.Values, cookie *http.Cookie, headers map[string]string) *httptest.ResponseRecorder {
	var body io.Reader
	if values != nil {
		body = strings.NewReader(values.Encode())
	}
	r := httptest.NewRequest(method, target, body)
	if values != nil {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	fx.routes.ServeHTTP(w, r)
	return w
}
func mustSave(t *testing.T, s *Store, in Input) int64 {
	t.Helper()
	id, err := s.Save(context.Background(), 0, in)
	if err != nil {
		t.Fatal(err)
	}
	return id
}
func classification(t *testing.T, s *Store, kind, name string) int64 {
	t.Helper()
	id, err := s.SaveClassification(context.Background(), kind, 0, name)
	if err != nil {
		t.Fatal(err)
	}
	return id
}
func mustPublish(t *testing.T, s *Store, id int64) {
	t.Helper()
	if err := s.SetPublished(context.Background(), id, true); err != nil {
		t.Fatal(err)
	}
}

// 不変条件: CATALOG-INV-1, CATALOG-INV-2, CATALOG-INV-7, CATALOG-INV-10, CATALOG-INV-12
func TestPublicRoutesPrivacyAndRepresentations(t *testing.T) {
	for _, withSite := range []bool{false, true} {
		t.Run(map[bool]string{false: "base", true: "site"}[withSite], func(t *testing.T) {
			fx := setup(t, withSite)
			s := fx.f.store
			cat := classification(t, s, "categories", "公開分類")
			privateCat := classification(t, s, "categories", "秘密分類")
			tag := classification(t, s, "tags", "公開タグ")
			privateTag := classification(t, s, "tags", "秘密タグ")
			id := mustSave(t, s, Input{Title: "公開<script>alert(1)</script>", Body: "一行目\n二行目\n\n\n次の段落", CategoryID: cat, TagIDs: []int64{tag}})
			mustPublish(t, s, id)
			draft := mustSave(t, s, Input{Title: "秘密の下書き", CategoryID: privateCat, TagIDs: []int64{privateTag}})
			for _, headers := range []map[string]string{nil, {"HX-Request": "true"}, {"HX-Request": "true", "HX-History-Restore-Request": "true"}} {
				w := fx.send("GET", "/catalog", nil, &http.Cookie{Name: "session", Value: "unknown"}, headers)
				if w.Code != 200 || !strings.Contains(w.Body.String(), "公開&lt;script&gt;") || (strings.Contains(w.Body.String(), "秘密分類") || strings.Contains(w.Body.String(), "秘密タグ") || strings.Contains(w.Body.String(), "秘密の下書き")) || len(w.Result().Cookies()) != 0 {
					t.Fatalf("privacy/escaping: %d %s", w.Code, w.Body.String())
				}
				full := headers == nil || headers["HX-History-Restore-Request"] == "true"
				if strings.Contains(w.Body.String(), "<!DOCTYPE html>") != full {
					t.Fatal("full/fragment mismatch")
				}
				if strings.Contains(w.Header().Get("Content-Security-Policy"), "script-src 'self'") != full {
					t.Fatal("CSP scope mismatch")
				}
				if !strings.Contains(w.Header().Get("Cache-Control"), "no-store") || !strings.Contains(w.Header().Get("Vary"), "HX-History-Restore-Request") {
					t.Fatal("cache headers missing")
				}
			}
			for _, path := range []string{"/catalog/" + idString(draft), "/catalog/999999", "/catalog/not-a-number", "/catalog/0" + idString(id), "/catalog/+" + idString(id), "/catalog/missing/path", "/catalog/static/no.js"} {
				w := fx.send("GET", path, nil, nil, nil)
				if w.Code != 404 || (strings.Contains(w.Body.String(), "秘密分類") || strings.Contains(w.Body.String(), "秘密タグ") || strings.Contains(w.Body.String(), "秘密の下書き")) || len(w.Result().Cookies()) != 0 {
					t.Fatal("draft/404 leak")
				}
			}
			w := fx.send("GET", "/catalog/"+idString(id), nil, nil, nil)
			if w.Code != 200 || strings.Contains(w.Body.String(), "<script") || !strings.Contains(w.Header().Get("Content-Security-Policy"), "script-src 'none'") {
				t.Fatal("detail script scope")
			}
			if !strings.Contains(w.Body.String(), "<p>一行目<br>二行目</p><p>次の段落</p>") {
				t.Fatalf("detail paragraphs: %s", w.Body.String())
			}
			for _, kind := range []string{"category", "tag"} {
				private := privateCat
				if kind == "tag" {
					private = privateTag
				}
				for _, suffix := range []string{"", "&page=2"} {
					a := fx.send("GET", "/catalog?"+kind+"="+idString(private)+suffix, nil, nil, nil)
					b := fx.send("GET", "/catalog?"+kind+"=999999"+suffix, nil, nil, nil)
					if a.Code != 200 || a.Body.String() != b.Body.String() {
						t.Fatalf("unknown/private %s must be indistinguishable", kind)
					}
				}
			}
			for _, q := range []string{"page=5001", "page=x", "category=x", "tag=-1", "q=" + strings.Repeat("a", 201), strings.Repeat("tag=1&", 21), "q=%zz"} {
				if w := fx.send("GET", "/catalog?"+q, nil, nil, nil); w.Code != 400 {
					t.Fatalf("query accepted: %s", q)
				}
			}
			refs, err := fx.f.SitemapEntries(context.Background())
			if err != nil || len(refs) != 2 || refs[1].Path != "/catalog/"+idString(id) {
				t.Fatal("sitemap leak", refs, err)
			}
			var count int
			for _, table := range []string{"owners", "sessions"} {
				if err := fx.core.DB().QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 0 {
					t.Fatal("anonymous persistence", table, count, err)
				}
			}
			// Public static assets do not expose other embedded files.
			for _, path := range []string{"/catalog/static/htmx.min.js", "/catalog/static/catalog.js"} {
				if w := fx.send("GET", path, nil, nil, nil); w.Code != 200 || len(w.Result().Cookies()) != 0 {
					t.Fatal("static route")
				}
			}
			if w := fx.send("GET", "/catalog/static/LICENSE-htmx", nil, nil, nil); w.Code == 200 {
				t.Fatal("unexpected embedded file exposure")
			}
		})
	}
}

// 不変条件: CATALOG-INV-4, CATALOG-INV-5, CATALOG-INV-6, CATALOG-INV-8, CATALOG-INV-11
func TestStoreTransactionsClassificationDeletionAndRetention(t *testing.T) {
	fx := setup(t, false)
	s := fx.f.store
	ctx := context.Background()
	cat := classification(t, s, "categories", "分類")
	tag := classification(t, s, "tags", "タグ")
	id := mustSave(t, s, Input{Title: "元の項目", CategoryID: cat, TagIDs: []int64{tag}})
	before, err := s.Get(ctx, id, false)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Save(ctx, id, Input{Title: "変更", NewTags: []string{"途中作成"}, TagIDs: []int64{9999}})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatal("invalid tag accepted", err)
	}
	after, _ := s.Get(ctx, id, false)
	if after.Title != before.Title || len(after.Tags) != 1 {
		t.Fatal("partial save")
	}
	var n int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM catalog_tags WHERE name='途中作成'").Scan(&n); err != nil || n != 0 {
		t.Fatal("orphan tag after rollback", err)
	}
	// A DB failure after changing the item must roll back the whole save.
	if _, err := s.db.Exec("CREATE TRIGGER fail_relation BEFORE INSERT ON catalog_item_tags BEGIN SELECT RAISE(ABORT,'forced'); END"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(ctx, id, Input{Title: "失敗", TagIDs: []int64{tag}}); err == nil {
		t.Fatal("trigger did not abort")
	}
	after, _ = s.Get(ctx, id, false)
	if after.Title != before.Title || after.CategoryID != cat {
		t.Fatal("DB error partial save")
	}
	_, _ = s.db.Exec("DROP TRIGGER fail_relation")
	s.now = func() time.Time { return before.UpdatedAt.Add(time.Hour) }
	if _, err := s.SaveClassification(ctx, "categories", cat, "変更分類"); err != nil {
		t.Fatal(err)
	}
	after, _ = s.Get(ctx, id, false)
	if after.Category != "変更分類" || !after.UpdatedAt.After(before.UpdatedAt) {
		t.Fatal("classification lastmod")
	}
	if err := s.DeleteClassification(ctx, "categories", cat); err != nil {
		t.Fatal(err)
	}
	after, _ = s.Get(ctx, id, false)
	if after.CategoryID != 0 {
		t.Fatal("category SET NULL")
	}
	if err := s.DeleteClassification(ctx, "tags", tag); err != nil {
		t.Fatal(err)
	}
	after, _ = s.Get(ctx, id, false)
	if len(after.Tags) != 0 {
		t.Fatal("tag CASCADE")
	}
	if _, err := s.SaveClassification(ctx, "tags", 0, "重複"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveClassification(ctx, "tags", 0, " 重複 "); !errors.Is(err, ErrDuplicateName) || !errors.Is(err, ErrInvalidInput) {
		t.Fatal("duplicate classification accepted")
	}
	if err := s.Delete(ctx, id); err != nil {
		t.Fatal(err)
	}
	next := mustSave(t, s, Input{Title: "次の項目", NewTags: []string{"重複"}})
	if next <= id {
		t.Fatal("ID reused")
	}
	if err := Migrate(s.db); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.core.PurgeExpiredSessions(ctx, 30*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, next, false); err != nil {
		t.Fatal("GC/migration deleted shared item", err)
	}
	// Auth account deletion has no FK to catalog data.
	cookie := authtest.NewSession(t, s.db)
	_ = cookie
	if _, err := s.db.Exec("DELETE FROM auth_accounts"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, next, false); err != nil {
		t.Fatal("account deletion removed item")
	}
	rows, err := s.db.Query("PRAGMA foreign_key_check")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("foreign key violation")
	}
}

// 不変条件: CATALOG-INV-4, CATALOG-INV-6
func TestConcurrentTagCreationUsesExistingTag(t *testing.T) {
	fx := setup(t, false)
	s := fx.f.store
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.Save(context.Background(), 0, Input{Title: "項目", NewTags: []string{"共通"}})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM catalog_tags WHERE name='共通'").Scan(&n); err != nil || n != 1 {
		t.Fatal("concurrent duplicate", n, err)
	}
}

// 不変条件: CATALOG-INV-1, CATALOG-INV-10, CATALOG-INV-12
func TestSearchANDLiteralLikeAndPaging(t *testing.T) {
	fx := setup(t, false)
	s := fx.f.store
	ctx := context.Background()
	a := classification(t, s, "tags", "A")
	b := classification(t, s, "tags", "B")
	literal := mustSave(t, s, Input{Title: `100%_\`, TagIDs: []int64{a, b}})
	mustPublish(t, s, literal)
	other := mustSave(t, s, Input{Title: "100xyz", TagIDs: []int64{a}})
	mustPublish(t, s, other)
	for _, q := range []Filter{{Query: `%_\`}, {TagIDs: []int64{a, b}}} {
		items, err := s.List(ctx, true, q, 21, 0)
		if err != nil || len(items) != 1 || items[0].ID != literal {
			t.Fatal("search semantics", items, err)
		}
	}
	for range 21 {
		id := mustSave(t, s, Input{Title: "ページ項目"})
		mustPublish(t, s, id)
	}
	w := fx.send("GET", "/catalog?q="+url.QueryEscape("ページ項目"), nil, nil, nil)
	if w.Code != 200 || strings.Count(w.Body.String(), "<h3>") != 20 || !strings.Contains(w.Body.String(), "次のページがあります") || !strings.Contains(w.Body.String(), "page=2") {
		t.Fatal("pagination first page")
	}
	full := fx.send("GET", "/catalog?q="+url.QueryEscape("ページ項目")+"&page=2", nil, nil, nil)
	part := fx.send("GET", "/catalog?q="+url.QueryEscape("ページ項目")+"&page=2", nil, nil, map[string]string{"HX-Request": "true"})
	if full.Code != 200 || part.Code != 200 || strings.Count(part.Body.String(), "<h3>") != 1 || !strings.Contains(full.Body.String(), part.Body.String()) {
		t.Fatal("HTML/fragment mismatch")
	}
}

// 不変条件: CATALOG-INV-11
func TestInputValidation(t *testing.T) {
	fx := setup(t, false)
	s := fx.f.store
	for _, in := range []Input{{Title: ""}, {Title: strings.Repeat("a", 201)}, {Title: "a\nb"}, {Title: "a\tb"}, {Title: "\xff"}, {Title: "ok", Body: strings.Repeat("a", 50001)}, {Title: "ok", CategoryID: 999}, {Title: "ok", NewTags: []string{"bad\nname"}}, {Title: "ok", TagIDs: make([]int64, 21)}} {
		if _, err := s.Save(context.Background(), 0, in); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("bad input accepted: %v", err)
		}
	}
}

// 不変条件: CATALOG-INV-3, CATALOG-INV-9, CATALOG-INV-10
func TestAdminProtectionWorkflowAndLogCanary(t *testing.T) {
	fx := setup(t, true)
	s := fx.f.store
	ctx := context.Background()
	const canary = "CANARYcatalog7f3a"
	cookie := authtest.NewSessionWithRole(t, fx.core.DB(), "member")
	token := authtest.CSRFToken(t, fx.routes, "/admin/catalog/new", cookie)
	headers := map[string]string{"Origin": "null", "Sec-Fetch-Site": "same-origin", "User-Agent": canary, "X-Request-ID": canary}
	values := url.Values{"csrf_token": {token}, "title": {canary}, "body": {"本文"}, "new_tags": {canary + "タグ"}}
	w := fx.send("POST", "/admin/catalog", values, cookie, headers)
	if w.Code != 303 {
		t.Fatalf("member create: %d %s", w.Code, w.Body.String())
	}
	list, _ := s.List(ctx, false, Filter{}, 20, 0)
	if len(list) != 1 || list[0].Title != canary {
		t.Fatal("canary did not reach save")
	}
	id := list[0].ID
	path := "/admin/catalog/" + idString(id)
	combobox, err := web.AssetURL("js/combobox.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/admin/catalog", "/admin/catalog/new", path, path + "/delete", "/admin/catalog/classifications/tags"} {
		w := fx.send("GET", p, nil, cookie, nil)
		// 項目の作成・編集画面だけ、タグ選択の部品のJSを読み込み、自サイトのJSを許す。
		form := p == "/admin/catalog/new" || p == path
		scripts := strings.Count(w.Body.String(), "<script")
		csp := w.Header().Get("Content-Security-Policy")
		if w.Code != 200 || form && (scripts != 1 || !strings.Contains(w.Body.String(), `<script src="`+combobox+`" defer></script>`) || !strings.Contains(csp, "script-src 'self'")) || !form && (scripts != 0 || !strings.Contains(csp, "script-src 'none'")) || strings.Contains(csp, "unsafe-") {
			t.Fatalf("member page %s: %d scripts=%d csp=%q", p, w.Code, scripts, csp)
		}
		if w := fx.send("GET", p, nil, nil, nil); w.Code != 303 {
			t.Fatalf("unauthenticated GET %s: %d", p, w.Code)
		}
	}
	for _, suffix := range []string{"/publish", "/unpublish", "/delete", ""} {
		for _, bad := range []struct {
			c     *http.Cookie
			h     map[string]string
			token string
		}{{nil, headers, token}, {cookie, headers, "wrong"}, {cookie, map[string]string{"Origin": "https://evil.example", "Sec-Fetch-Site": "cross-site"}, token}, {cookie, map[string]string{"Origin": "null"}, token}} {
			v := url.Values{"csrf_token": {bad.token}, "title": {canary}}
			if w := fx.send("POST", path+suffix, v, bad.c, bad.h); w.Code == 200 || w.Code == 303 && bad.c != nil {
				t.Fatalf("unprotected POST %s: %d", suffix, w.Code)
			}
		}
	}
	if w := fx.send("POST", path+"/publish", url.Values{"csrf_token": {token}}, cookie, headers); w.Code != 303 {
		t.Fatal("publish failed")
	}
	if w := fx.send("GET", "/catalog/"+idString(id), nil, nil, headers); w.Code != 200 || !strings.Contains(w.Body.String(), canary) {
		t.Fatal("publish not visible")
	}
	if w := fx.send("POST", path+"/unpublish", url.Values{"csrf_token": {token}}, cookie, headers); w.Code != 303 {
		t.Fatal("unpublish failed")
	}
	if w := fx.send("GET", "/catalog/"+idString(id), nil, nil, nil); w.Code != 404 {
		t.Fatal("unpublish still visible")
	}
	// Invalid input is intentionally echoed only in the protected form.
	bad := url.Values{"csrf_token": {token}, "title": {canary + "\ninvalid"}}
	if w := fx.send("POST", path, bad, cookie, headers); w.Code != 400 || !strings.Contains(w.Body.String(), canary) || !strings.Contains(w.Header().Get("Content-Security-Policy"), "script-src 'self'") {
		t.Fatal("input error did not preserve form with the tag picker")
	}
	// Stop the account without deleting its session to exercise request-time account checks.
	if _, err := s.db.Exec("UPDATE auth_accounts SET disabled_at=1 WHERE id=(SELECT account_id FROM auth_sessions WHERE token_hash=?)", hash(cookie.Value)); err != nil {
		t.Fatal(err)
	}
	if w := fx.send("POST", path+"/publish", url.Values{"csrf_token": {token}}, cookie, headers); w.Code == 303 && w.Header().Get("Location") == path {
		t.Fatal("stopped session accepted")
	}
	if strings.Contains(fx.logs.String(), canary) || !strings.Contains(fx.logs.String(), "catalog item saved") {
		t.Fatal("log canary leaked or save not logged")
	}
}
func hash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func TestApprovedLibraryAndConfiguration(t *testing.T) {
	body, err := contentFS.ReadFile("static/htmx.min.js")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	if hex.EncodeToString(sum[:]) != "d6fdc75f204e6bdefa99b69bf1e6d4ac69b8a364f77929f45c13476b4000f717" {
		t.Fatal("vendored htmx differs from recorded version")
	}
	template, err := contentFS.ReadFile("views/list.html")
	if err != nil {
		t.Fatal(err)
	}
	match := regexp.MustCompile(`name="htmx-config" content='([^']+)'`).FindSubmatch(template)
	if match == nil {
		t.Fatal("no meta configuration")
	}
	var config map[string]any
	if err := json.Unmarshal(match[1], &config); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"allowEval", "allowScriptTags", "includeIndicatorStyles", "historyRestoreAsHxRequest"} {
		if config[key] != false {
			t.Fatalf("%s must be false", key)
		}
	}
	if config["selfRequestsOnly"] != true || config["historyCacheSize"] != float64(0) || config["refreshOnHistoryMiss"] != true {
		t.Fatal("unsafe requests/history configuration")
	}
}

// 不変条件: CATALOG-INV-3, CATALOG-INV-5, CATALOG-INV-9
func TestClassificationRoutesAndDeletionWorkflow(t *testing.T) {
	fx := setup(t, false)
	cookie := authtest.NewSession(t, fx.core.DB())
	token := authtest.CSRFToken(t, fx.routes, "/admin/catalog/new", cookie)
	headers := map[string]string{"Origin": "null", "Sec-Fetch-Site": "same-origin"}
	for _, kind := range []string{"categories", "tags"} {
		base := "/admin/catalog/classifications/" + kind
		if w := fx.send("POST", base, url.Values{"name": {"分類カナリア"}, "csrf_token": {token}}, cookie, headers); w.Code != 303 {
			t.Fatalf("classification create: %d", w.Code)
		}
		vs, err := fx.f.store.Classifications(context.Background(), kind, false)
		if err != nil || len(vs) != 1 {
			t.Fatal("classification not created", err)
		}
		path := base + "/" + idString(vs[0].ID)
		for _, p := range []string{base, path, path + "/delete"} {
			if w := fx.send("GET", p, nil, cookie, nil); w.Code != 200 {
				t.Fatalf("classification page: %d", w.Code)
			}
			if w := fx.send("GET", p, nil, nil, nil); w.Code != 303 {
				t.Fatal("unprotected classification page")
			}
			if w := fx.send("POST", p, url.Values{"name": {"変化"}}, cookie, headers); w.Code != 403 {
				t.Fatal("missing classification CSRF accepted", w.Code)
			}
		}
		if w := fx.send("POST", path, url.Values{"name": {"改名"}, "csrf_token": {token}}, cookie, headers); w.Code != 303 {
			t.Fatal("rename failed")
		}
		if w := fx.send("GET", path+"/delete", nil, cookie, nil); w.Code != 200 || !strings.Contains(w.Body.String(), "項目自体は削除しません") {
			t.Fatal("missing deletion confirmation")
		}
		if w := fx.send("POST", path+"/delete", url.Values{"csrf_token": {token}}, cookie, headers); w.Code != 303 {
			t.Fatal("delete failed")
		}
		if w := fx.send("GET", path, nil, cookie, nil); w.Code != 404 {
			t.Fatal("deleted classification accessible")
		}
	}
	if strings.Contains(fx.logs.String(), "分類カナリア") {
		t.Fatal("classification leaked to log")
	}
}

// TestClassificationAdminPages は、分類の一覧が使用件数つきの表になり、追加・編集の失敗が理由つきで同じ画面に出ることを確かめる。
func TestClassificationAdminPages(t *testing.T) {
	fx := setup(t, false)
	cookie := authtest.NewSession(t, fx.core.DB())
	token := authtest.CSRFToken(t, fx.routes, "/admin/catalog/new", cookie)
	headers := map[string]string{"Origin": "null", "Sec-Fetch-Site": "same-origin"}
	used := classification(t, fx.f.store, "tags", "使用中")
	unused := classification(t, fx.f.store, "tags", "未使用")
	mustSave(t, fx.f.store, Input{Title: "項目", TagIDs: []int64{used}})
	base := "/admin/catalog/classifications/tags"

	usages, err := fx.f.store.ClassificationUsages(context.Background(), "tags")
	if err != nil || len(usages) != 2 || usages[0].Name != "使用中" || usages[0].Items != 1 || usages[1].Items != 0 {
		t.Fatalf("usages: %+v %v", usages, err)
	}
	if w := fx.send("GET", base, nil, cookie, nil); w.Code != 200 || !strings.Contains(w.Body.String(), "<td>1件</td>") || !strings.Contains(w.Body.String(), "<caption") {
		t.Fatal("classification table without usage count")
	}
	if w := fx.send("GET", "/admin/catalog/classifications/categories", nil, cookie, nil); !strings.Contains(w.Body.String(), "<p>カテゴリはまだありません。</p>") {
		t.Fatal("empty classification list")
	}

	w := fx.send("POST", base, url.Values{"name": {"使用中"}, "csrf_token": {token}}, cookie, headers)
	body := w.Body.String()
	if w.Code != 400 || !strings.Contains(body, "<h1>タグの管理</h1>") || !strings.Contains(body, "同じ名前のタグがあります") || !strings.Contains(body, `aria-invalid="true"`) || !strings.Contains(body, `value="使用中"`) || strings.Contains(body, "タイトルは1〜200文字") {
		t.Fatalf("duplicate add: %d", w.Code)
	}
	if w := fx.send("POST", base, url.Values{"name": {" "}, "csrf_token": {token}}, cookie, headers); w.Code != 400 || !strings.Contains(w.Body.String(), "名前は1〜50文字") {
		t.Fatal("empty name on add")
	}
	w = fx.send("POST", base+"/"+idString(unused), url.Values{"name": {"使用中"}, "csrf_token": {token}}, cookie, headers)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "<h1>タグの編集</h1>") || !strings.Contains(w.Body.String(), "同じ名前のタグがあります") {
		t.Fatal("duplicate rename")
	}

	if w := fx.send("GET", base+"/"+idString(unused)+"/delete", nil, cookie, nil); !strings.Contains(w.Body.String(), "使っている項目はありません。") || strings.Contains(w.Body.String(), "0件") {
		t.Fatal("unused deletion text")
	}
	if w := fx.send("GET", base+"/"+idString(used)+"/delete", nil, cookie, nil); !strings.Contains(w.Body.String(), "現在1件の項目で使っています。") {
		t.Fatal("used deletion text")
	}
	if w := fx.send("GET", base+"/"+idString(used)+"/delete", nil, cookie, nil); !strings.Contains(w.Body.String(), "<title>タグの削除") {
		t.Fatal("deletion page title")
	}
}

func TestMissingAuthAndStaticExportAreRejected(t *testing.T) {
	fx := setup(t, false)
	reg := features.NewRegistry()
	reg.Register(fx.f)
	if _, err := app.BuildRoutes(fx.core, reg, false, slog.New(slog.NewTextHandler(io.Discard, nil))); err == nil || !strings.Contains(err.Error(), "make add-auth") {
		t.Fatal("missing Auth not rejected", err)
	}
	reg = features.NewRegistry()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	reg.Register(site.NewFeature(logger))
	reg.Register(fx.f)
	if err := app.ExportStatic(reg, t.TempDir(), logger); err == nil || !strings.Contains(err.Error(), "feature catalog") {
		t.Fatal("static export must reject Catalog", err)
	}

}

// 不変条件: CATALOG-INV-4, CATALOG-INV-9
func TestBusyWriteDoesNotChangeDataAndCanRetry(t *testing.T) {
	fx := setup(t, false)
	db := fx.core.DB()
	db.SetMaxOpenConns(2)
	ctx := context.Background()
	id := mustSave(t, fx.f.store, Input{Title: "元の内容"})
	cookie := authtest.NewSession(t, db)
	token := authtest.CSRFToken(t, fx.routes, "/admin/catalog/new", cookie)
	held, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	if _, err := held.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	defer held.ExecContext(ctx, "ROLLBACK")
	other, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.ExecContext(ctx, "PRAGMA busy_timeout=1"); err != nil {
		t.Fatal(err)
	}
	other.Close()
	_, err = fx.f.store.Save(ctx, id, Input{Title: "再試行する内容"})
	if !busyError(err) {
		t.Fatal("write conflict not classified", err)
	}
	w := httptest.NewRecorder()
	if fx.f.result(w, httptest.NewRequest("POST", "/admin/catalog", nil), err) || w.Code != 503 || w.Header().Get("Retry-After") == "" {
		t.Fatal("busy failure must invite retry", w.Code)
	}
	values := url.Values{"csrf_token": {token}, "title": {"再試行する内容"}}
	if _, err := held.ExecContext(ctx, "ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	item, err := fx.f.store.Get(ctx, id, false)
	if err != nil || item.Title != "元の内容" {
		t.Fatal("busy request changed data", err)
	}
	w = fx.send("POST", "/admin/catalog/"+idString(id), values, cookie, map[string]string{"Origin": "null", "Sec-Fetch-Site": "same-origin"})
	if w.Code != 303 {
		t.Fatal("retry failed", w.Code)
	}
}
