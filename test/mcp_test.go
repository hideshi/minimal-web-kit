package test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"minimal-web-kit/features"
	"minimal-web-kit/features/auth"
	"minimal-web-kit/features/auth/authtest"
	"minimal-web-kit/features/catalog"
	"minimal-web-kit/features/mcp"
	"minimal-web-kit/features/news"
	"minimal-web-kit/features/pages"
	"minimal-web-kit/internal/app"
	"minimal-web-kit/internal/store"
)

const mcpVerifier = "verifier-verifier-verifier-verifier-verifier-0001"

type mcpEnv struct {
	t      *testing.T
	routes http.Handler
	token  string
	admin  *http.Cookie
	csrf   string
	core   *store.SQLiteStore
}

// newMCPEnv は、起動処理と同じ配線で、3機能とMCPサーバーを組み込み、トークンを発行します。
func newMCPEnv(t *testing.T) *mcpEnv {
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
	reg.Register(auth.NewFeature(logger))
	reg.Register(news.NewFeature(logger))
	reg.Register(catalog.NewFeature(logger))
	reg.Register(pages.NewFeature(logger))
	reg.Register(mcp.NewFeature(logger))
	for _, f := range reg.All() {
		if err := f.Init(core.DB()); err != nil {
			t.Fatal(err)
		}
	}
	routes, err := app.BuildRoutes(core, reg, false, logger)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	db := core.DB()
	cookie, account := authtest.NewAccountSession(t, db, "admin")
	fed := auth.NewFederationStore(db)
	client, err := fed.CreateAgentClient(ctx, "テスト", "http://localhost/callback")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fed.Grant(ctx, client.ID, account); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(mcpVerifier))
	resource := "http://localhost:8080/mcp"
	req := auth.AgentRequest{ID: "request-id", Client: client.ID, Redirect: "http://localhost:18767/callback", State: "state-state", Challenge: base64.RawURLEncoding.EncodeToString(sum[:]), Resource: resource, Scope: "mcp:read mcp:write"}
	if err := fed.NewAgentRequest(ctx, "flow", req); err != nil {
		t.Fatal(err)
	}
	code, err := fed.ApproveAgent(ctx, req.ID, cookie.Value)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := fed.ExchangeAgentCode(ctx, client.ID, code, req.Redirect, mcpVerifier, resource)
	if err != nil {
		t.Fatal(err)
	}
	e := &mcpEnv{t: t, routes: routes, token: grant.Token, admin: cookie, core: core}
	e.csrf = authtest.CSRFToken(t, routes, "/admin/mcp", cookie)
	for _, name := range []string{"news", "catalog", "pages"} {
		if w := e.adminPost("/admin/mcp/"+name+"/enable", ""); w.Code != http.StatusSeeOther {
			t.Fatalf("enable %s: %d %s", name, w.Code, w.Body.String())
		}
	}
	return e
}

func (e *mcpEnv) adminPost(path, form string) *httptest.ResponseRecorder {
	e.t.Helper()
	body := "csrf_token=" + e.csrf
	if form != "" {
		body += "&" + form
	}
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("Origin", "http://localhost:8080")
	req.AddCookie(e.admin)
	w := httptest.NewRecorder()
	e.routes.ServeHTTP(w, req)
	return w
}

func (e *mcpEnv) call(method, name string, params map[string]any) map[string]any {
	e.t.Helper()
	raw, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	if err != nil {
		e.t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(raw))
	req.Host = "localhost:8080"
	req.Header.Set("Authorization", "Bearer "+e.token)
	req.Header.Set("MCP-Protocol-Version", "2026-07-28")
	req.Header.Set("Mcp-Method", method)
	if name != "" {
		req.Header.Set("Mcp-Name", name)
	}
	w := httptest.NewRecorder()
	e.routes.ServeHTTP(w, req)
	var out struct {
		Result map[string]any `json:"result"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil {
		e.t.Fatalf("%s %s: %d %s", method, name, w.Code, w.Body.String())
	}
	return out.Result
}

var mcpCaps = map[string]any{"io.modelcontextprotocol/clientCapabilities": map[string]any{"elicitation": map[string]any{"form": map[string]any{}}}}

func (e *mcpEnv) ask(tool string, args map[string]any) (string, string) {
	e.t.Helper()
	r := e.call("tools/call", tool, map[string]any{"name": tool, "arguments": args, "_meta": mcpCaps})
	if r["resultType"] != "input_required" {
		e.t.Fatalf("%s: %+v", tool, r)
	}
	message := r["inputRequests"].(map[string]any)["confirm"].(map[string]any)["params"].(map[string]any)["message"].(string)
	return r["requestState"].(string), message
}

func (e *mcpEnv) accept(tool string, args map[string]any, state string) map[string]any {
	e.t.Helper()
	return e.call("tools/call", tool, map[string]any{"name": tool, "arguments": args, "_meta": mcpCaps, "requestState": state,
		"inputResponses": map[string]any{"confirm": map[string]any{"action": "accept"}}})
}

func (e *mcpEnv) run(tool string, args map[string]any) map[string]any {
	e.t.Helper()
	state, _ := e.ask(tool, args)
	return e.accept(tool, args, state)
}

func resultText(t *testing.T, r map[string]any) string {
	t.Helper()
	return r["content"].([]any)[0].(map[string]any)["text"].(string)
}

func resultID(t *testing.T, r map[string]any) int64 {
	t.Helper()
	var v struct {
		ID int64 `json:"id"`
	}
	if r["isError"] != false || json.Unmarshal([]byte(resultText(t, r)), &v) != nil {
		t.Fatalf("%+v", r)
	}
	return v.ID
}

func (e *mcpEnv) get(path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	w := httptest.NewRecorder()
	e.routes.ServeHTTP(w, req)
	return w
}

// TestMCP_PagesFollowTheAdminPath は、作成から公開までを通し、ナビの更新と検査が管理画面と同じことを確かめます。
// ユースケース: UC-MCP-3, UC-MCP-4
// 不変条件: MCP-INV-7, PAGES-INV-3, PAGES-INV-6
func TestMCP_PagesFollowTheAdminPath(t *testing.T) {
	e := newMCPEnv(t)
	create := map[string]any{"slug": "about-us", "title": "会社概要", "body": "本文", "nav_order": 1}
	state, message := e.ask("pages_create", create)
	if !strings.Contains(message, "公開はしません") || !strings.Contains(message, "> 会社概要") {
		t.Fatal(message)
	}
	id := resultID(t, e.accept("pages_create", create, state))
	if w := e.get("/about-us"); w.Code != 404 {
		t.Fatal("create published the page", w.Code)
	}
	publish := map[string]any{"id": id}
	state, message = e.ask("pages_publish", publish)
	if !strings.Contains(message, "だれでも見られる") {
		t.Fatal(message)
	}
	resultID(t, e.accept("pages_publish", publish, state))
	w := e.get("/about-us")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `href="/about-us"`) {
		t.Fatal("nav not refreshed", w.Code)
	}
	// 予約語は管理画面と同じく拒否する
	r := e.call("tools/call", "pages_create", map[string]any{"name": "pages_create", "_meta": mcpCaps, "arguments": map[string]any{"slug": "privacy", "title": "x", "body": ""}})
	if r["isError"] != true {
		t.Fatalf("%+v", r)
	}
	update := map[string]any{"id": id, "slug": "company", "title": "会社概要", "body": "本文2", "nav_order": nil}
	state, message = e.ask("pages_update", update)
	if !strings.Contains(message, "公開中の内容が直ちに変わります") || !strings.Contains(message, "URLが変わります") {
		t.Fatal(message)
	}
	resultID(t, e.accept("pages_update", update, state))
	if w := e.get("/company"); w.Code != 200 || strings.Contains(w.Body.String(), `href="/company"`) {
		t.Fatal(w.Code)
	}
	resultID(t, e.run("pages_unpublish", publish))
	if w := e.get("/company"); w.Code != 404 {
		t.Fatal(w.Code)
	}
}

// TestMCP_ConflictWithAdminEdit は、確認の後の管理画面の編集を、MCPの保存が上書きしないことを確かめます。
// 不変条件: MCP-INV-4
func TestMCP_ConflictWithAdminEdit(t *testing.T) {
	e := newMCPEnv(t)
	ctx := context.Background()
	pageID := resultID(t, e.run("pages_create", map[string]any{"slug": "about-us", "title": "元", "body": ""}))
	postID := resultID(t, e.run("news_create", map[string]any{"title": "元", "body": ""}))
	itemID := resultID(t, e.run("catalog_create", map[string]any{"title": "元", "body": ""}))
	for _, c := range []struct {
		tool string
		args map[string]any
		edit func() error
	}{
		{"pages_update", map[string]any{"id": pageID, "slug": "about-us", "title": "MCP", "body": "", "nav_order": nil}, func() error {
			_, err := pages.NewStore(e.core.DB()).Save(ctx, pageID, pages.Input{Slug: "about-us", Title: "管理画面", Body: ""}, nil)
			return err
		}},
		{"pages_publish", map[string]any{"id": pageID}, func() error {
			_, err := pages.NewStore(e.core.DB()).Save(ctx, pageID, pages.Input{Slug: "about-us", Title: "管理画面2", Body: ""}, nil)
			return err
		}},
		{"news_update", map[string]any{"id": postID, "title": "MCP", "body": ""}, func() error {
			return news.NewPostStore(e.core.DB()).Update(ctx, postID, "管理画面", "")
		}},
		{"news_publish", map[string]any{"id": postID}, func() error {
			return news.NewPostStore(e.core.DB()).Update(ctx, postID, "管理画面2", "")
		}},
		{"catalog_update", map[string]any{"id": itemID, "title": "MCP", "body": "", "category_id": 0, "tag_ids": []int64{}}, func() error {
			_, err := catalog.NewStore(e.core.DB()).Save(ctx, itemID, catalog.Input{Title: "管理画面"})
			return err
		}},
		{"catalog_publish", map[string]any{"id": itemID}, func() error {
			_, err := catalog.NewStore(e.core.DB()).Save(ctx, itemID, catalog.Input{Title: "管理画面2"})
			return err
		}},
	} {
		state, _ := e.ask(c.tool, c.args)
		if err := c.edit(); err != nil {
			t.Fatal(err)
		}
		if r := e.accept(c.tool, c.args, state); r["isError"] != true {
			t.Errorf("%s overwrote the change: %+v", c.tool, r)
		}
	}
	if p, err := pages.NewStore(e.core.DB()).Get(ctx, pageID); err != nil || p.Title != "管理画面2" || p.Published {
		t.Fatalf("%+v %v", p, err)
	}
	if p, err := news.NewPostStore(e.core.DB()).Get(ctx, postID); err != nil || p.Title != "管理画面2" || p.Published {
		t.Fatalf("%+v %v", p, err)
	}
	if i, err := catalog.NewStore(e.core.DB()).Get(ctx, itemID, false); err != nil || i.Title != "管理画面2" || i.Published {
		t.Fatalf("%+v %v", i, err)
	}
}

// TestMCP_NewsAndCatalog は、読み取りが下書きを返し、タグの新規作成を受け付けないことを確かめます。
// ユースケース: UC-MCP-3
// 不変条件: MCP-INV-7
func TestMCP_NewsAndCatalog(t *testing.T) {
	e := newMCPEnv(t)
	postID := resultID(t, e.run("news_create", map[string]any{"title": "下書き", "body": "本文"}))
	r := e.call("tools/call", "news_get", map[string]any{"name": "news_get", "arguments": map[string]any{"id": postID}})
	if !strings.Contains(resultText(t, r), "下書き") {
		t.Fatalf("%+v", r)
	}
	if r := e.call("tools/call", "news_list", map[string]any{"name": "news_list"}); !strings.Contains(resultText(t, r), "下書き") {
		t.Fatalf("%+v", r)
	}
	resultID(t, e.run("news_publish", map[string]any{"id": postID}))
	if w := e.get("/news/" + strconv.FormatInt(postID, 10)); w.Code != 200 {
		t.Fatal(w.Code)
	}
	r = e.call("tools/call", "catalog_create", map[string]any{"name": "catalog_create", "_meta": mcpCaps, "arguments": map[string]any{"title": "x", "body": "", "new_tags": []string{"タグ"}}})
	if r["isError"] != true {
		t.Fatalf("new_tags accepted: %+v", r)
	}
	if r := e.call("tools/call", "catalog_classifications", map[string]any{"name": "catalog_classifications"}); r["isError"] != false {
		t.Fatalf("%+v", r)
	}
}

// TestMCP_AdminControlsFeatures は、管理者だけが機能を切り替えられ、一覧に反映されることを確かめます。
// ユースケース: UC-MCP-1, UC-MCP-2
// 不変条件: MCP-INV-5
func TestMCP_AdminControlsFeatures(t *testing.T) {
	e := newMCPEnv(t)
	names := func() string {
		b, _ := json.Marshal(e.call("tools/list", "", nil))
		return string(b)
	}
	if !strings.Contains(names(), "pages_update") {
		t.Fatal("enabled tool missing")
	}
	if w := e.adminPost("/admin/mcp/pages/disable", ""); w.Code != http.StatusSeeOther {
		t.Fatal(w.Code)
	}
	if list := names(); strings.Contains(list, "pages_") || !strings.Contains(list, "news_") {
		t.Fatal(list)
	}
	member, _ := authtest.NewAccountSession(t, e.core.DB(), "member")
	csrf := authtest.CSRFToken(t, e.routes, "/admin/mcp", member)
	req := httptest.NewRequest(http.MethodPost, "/admin/mcp/pages/enable", strings.NewReader("csrf_token="+csrf))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("Origin", "http://localhost:8080")
	req.AddCookie(member)
	w := httptest.NewRecorder()
	e.routes.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatal(w.Code)
	}
	if w := e.adminPost("/admin/mcp/unknown/enable", ""); w.Code != http.StatusNotFound {
		t.Fatal(w.Code)
	}
}

// TestMCP_HTTPRoutes は、公開する経路の形を確かめます。
func TestMCP_HTTPRoutes(t *testing.T) {
	e := newMCPEnv(t)
	if w := e.get("/mcp"); w.Code != http.StatusMethodNotAllowed {
		t.Fatal(w.Code)
	}
	w := e.get("/.well-known/oauth-protected-resource")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"resource":"http://localhost:8080/mcp"`) || w.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := e.get("/.well-known/oauth-authorization-server"); w.Code != 200 {
		t.Fatal(w.Code)
	}
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader("{}"))
	req.Host = "localhost:8080"
	rec := httptest.NewRecorder()
	e.routes.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized || rec.Header().Get("Set-Cookie") != "" {
		t.Fatal(rec.Code)
	}
}

// TestMCP_ConditionalWritesRefuseStaleReads は、読み取りと保存の間に変わった内容を、更新の文そのものが拒否することを確かめます。
// 不変条件: MCP-INV-4
func TestMCP_ConditionalWritesRefuseStaleReads(t *testing.T) {
	e := newMCPEnv(t)
	ctx := context.Background()
	ps := pages.NewStore(e.core.DB())
	id, err := ps.Save(ctx, 0, pages.Input{Slug: "about-us", Title: "元"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	stale, err := ps.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ps.Save(ctx, id, pages.Input{Slug: "about-us", Title: "管理画面"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := ps.SaveIfUnchanged(ctx, stale, pages.Input{Slug: "about-us", Title: "MCP"}, nil); !errors.Is(err, features.ErrMCPConflict) {
		t.Fatal(err)
	}
	if err := ps.SetPublishedIfUnchanged(ctx, stale, true); !errors.Is(err, features.ErrMCPConflict) {
		t.Fatal(err)
	}
	if p, _ := ps.Get(ctx, id); p.Title != "管理画面" || p.Published {
		t.Fatalf("%+v", p)
	}

	posts := news.NewPostStore(e.core.DB())
	postID, err := posts.Create(ctx, "元", "")
	if err != nil {
		t.Fatal(err)
	}
	old, err := posts.Get(ctx, postID)
	if err != nil {
		t.Fatal(err)
	}
	if err := posts.Update(ctx, postID, "管理画面", ""); err != nil {
		t.Fatal(err)
	}
	if err := posts.UpdateIfUnchanged(ctx, old, "MCP", "", old.PublishedAt); !errors.Is(err, features.ErrMCPConflict) {
		t.Fatal(err)
	}
	if err := posts.SetPublishedIfUnchanged(ctx, old, true); !errors.Is(err, features.ErrMCPConflict) {
		t.Fatal(err)
	}
	if p, _ := posts.Get(ctx, postID); p.Title != "管理画面" || p.Published {
		t.Fatalf("%+v", p)
	}
}
