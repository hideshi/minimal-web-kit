package test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"minimal-web-kit/features/auth"
	"minimal-web-kit/features/auth/authtest"
	"minimal-web-kit/features/documents"
)

// mcpAccount は、接続を許可したアカウントです。
type mcpAccount struct {
	id     int64
	cookie *http.Cookie
	token  string
}

func (e *mcpEnv) account(role string) mcpAccount {
	e.t.Helper()
	ctx := context.Background()
	db := e.core.DB()
	cookie, id := authtest.NewAccountSession(e.t, db, role)
	fed := auth.NewFederationStore(db)
	if _, err := fed.Grant(ctx, e.client, id); err != nil {
		e.t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(mcpVerifier))
	resource := "http://localhost:8080/mcp"
	flow := fmt.Sprintf("flow-%d", id)
	req := auth.AgentRequest{ID: "request-" + flow, Client: e.client, Redirect: "http://localhost:18767/callback", State: "state-state", Challenge: base64.RawURLEncoding.EncodeToString(sum[:]), Resource: resource, Scope: "mcp:read mcp:write"}
	if err := fed.NewAgentRequest(ctx, flow, req); err != nil {
		e.t.Fatal(err)
	}
	code, err := fed.ApproveAgent(ctx, req.ID, cookie.Value)
	if err != nil {
		e.t.Fatal(err)
	}
	grant, err := fed.ExchangeAgentCode(ctx, e.client, code, req.Redirect, mcpVerifier, resource)
	if err != nil {
		e.t.Fatal(err)
	}
	return mcpAccount{id, cookie, grant.Token}
}

func (e *mcpEnv) as(a mcpAccount) *mcpEnv {
	c := *e
	c.token = a.token
	return &c
}

func (e *mcpEnv) docs() *documents.Store { return documents.NewStore(e.core.DB(), time.Now) }

func (e *mcpEnv) enableDocuments() {
	e.t.Helper()
	if w := e.adminPost("/admin/mcp/documents/enable", "confirmed=1"); w.Code != http.StatusSeeOther {
		e.t.Fatalf("enable documents: %d", w.Code)
	}
}

func (e *mcpEnv) toolNames() []string {
	e.t.Helper()
	list, _ := e.call("tools/list", "", nil)["tools"].([]any)
	names := []string{}
	for _, item := range list {
		names = append(names, item.(map[string]any)["name"].(string))
	}
	sort.Strings(names)
	return names
}

// 確認の前に落ちる呼び出しの結果を読みます。
func (e *mcpEnv) raw(method, name string, params map[string]any) (int, string) {
	e.t.Helper()
	// 確認に対応したクライアントとして送る。対応しないと別の理由で確認に進まず、検査の空振りになる
	meta := map[string]any{"io.modelcontextprotocol/protocolVersion": "2026-07-28", "io.modelcontextprotocol/clientCapabilities": mcpCaps["io.modelcontextprotocol/clientCapabilities"]}
	if params == nil {
		params = map[string]any{}
	}
	params["_meta"] = meta
	raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
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
	return w.Code, w.Body.String()
}

// 不変条件: DOC-INV-13
func TestDocumentsMCP_ToolsAreFixed(t *testing.T) {
	e := newMCPEnv(t)
	e.enableDocuments()
	var got []string
	for _, name := range e.as(e.account("user")).toolNames() {
		got = append(got, name)
	}
	want := []string{"documents_accounts", "documents_approvals", "documents_create", "documents_folders", "documents_list", "documents_mine", "documents_publish",
		"documents_read", "documents_request_approval", "documents_revise", "documents_update", "documents_version"}
	if !slices.Equal(got, want) {
		t.Fatalf("tools = %v", got)
	}
}

// 一般ユーザーは文書のツールだけを使える。他の機能のツールは、無いツールと同じ応答になる。
// 不変条件: MCP-INV-11, AUTH-INV-26
func TestDocumentsMCP_UserRoleSeesOnlyDocuments(t *testing.T) {
	e := newMCPEnv(t)
	e.enableDocuments()
	user := e.as(e.account("user"))
	for _, name := range user.toolNames() {
		if !strings.HasPrefix(name, "documents_") {
			t.Errorf("a general user sees %s", name)
		}
	}
	for _, tool := range []string{"pages_list", "news_list", "catalog_list", "no_such_tool"} {
		_, body := user.raw("tools/call", tool, map[string]any{"name": tool, "arguments": map[string]any{}})
		if !strings.Contains(body, "Unknown tool") {
			t.Errorf("%s: %s", tool, body)
		}
	}
	// サイト管理者は両方を使える
	site := e.as(e.account("site_admin")).toolNames()
	if !slices.Contains(site, "pages_list") || !slices.Contains(site, "documents_list") {
		t.Fatalf("site admin tools = %v", site)
	}
	// 同意は一般ユーザーにも開く。管理の画面は開かない
	u := e.account("user")
	if w := e.routesGet(u.cookie, "/admin/mcp"); w.Code != http.StatusForbidden {
		t.Fatalf("/admin/mcp as user: %d", w.Code)
	}
	if w := e.routesGet(u.cookie, "/admin/agent/continue"); w.Code == http.StatusForbidden {
		t.Fatalf("consent as user: %d", w.Code)
	}
}

func (e *mcpEnv) routesGet(cookie *http.Cookie, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	e.routes.ServeHTTP(w, req)
	return w
}

// 確認の途中でロールを下げると、他の機能の書き込みは実行されない。文書のツールは続けて使える。
// 不変条件: MCP-INV-11
func TestDocumentsMCP_LoweredRoleStopsOtherTools(t *testing.T) {
	e := newMCPEnv(t)
	e.enableDocuments()
	a := e.account("site_admin")
	site := e.as(a)
	args := map[string]any{"title": "題", "body": "本文", "slug": "lowered", "nav_order": nil}
	state, _ := site.ask("pages_create", args)
	if _, err := e.core.DB().Exec("UPDATE auth_accounts SET role = 'user' WHERE id = ?", a.id); err != nil {
		t.Fatal(err)
	}
	_, body := site.raw("tools/call", "pages_create", map[string]any{"name": "pages_create", "arguments": args, "requestState": state, "inputResponses": map[string]any{"confirm": map[string]any{"action": "accept", "content": map[string]any{}}}})
	if !strings.Contains(body, "Unknown tool") {
		t.Fatalf("resend after lowering: %s", body)
	}
	if list := e.as(a).toolNames(); !slices.Contains(list, "documents_list") || slices.Contains(list, "pages_list") {
		t.Fatalf("tools after lowering = %v", list)
	}
	var count int
	if err := e.core.DB().QueryRow("SELECT COUNT(*) FROM pages WHERE slug = 'lowered'").Scan(&count); err != nil || count != 0 {
		t.Fatalf("pages = %d %v", count, err)
	}
}

// 読める範囲は本人と同じ。システム管理者のエージェントも、共有されていない文書を読めない。既読にしない。
// 不変条件: DOC-INV-2, DOC-INV-8, DOC-INV-13
func TestDocumentsMCP_ReadsFollowTheAccount(t *testing.T) {
	ctx := context.Background()
	e := newMCPEnv(t)
	e.enableDocuments()
	owner, reader := e.account("user"), e.account("user")
	docs := e.docs()
	id, err := docs.Create(ctx, owner.id, documents.Input{Title: "秘密の回覧", Body: "秘密の本文"})
	if err != nil {
		t.Fatal(err)
	}
	_, v, _ := docs.Draft(ctx, owner.id, id)
	raw := func(env *mcpEnv, tool string, args map[string]any) string {
		_, body := env.raw("tools/call", tool, map[string]any{"name": tool, "arguments": args})
		return body
	}
	for _, c := range []struct {
		name string
		tool string
		args map[string]any
	}{
		{"read", "documents_read", map[string]any{"id": id}},
		{"version", "documents_version", map[string]any{"id": id, "version_id": v.ID}},
		{"list", "documents_list", map[string]any{"query": "秘密"}},
	} {
		if body := raw(e, c.tool, c.args); strings.Contains(body, "秘密の") {
			t.Errorf("system admin %s: %s", c.name, body)
		}
		if body := raw(e.as(reader), c.tool, c.args); strings.Contains(body, "秘密の") {
			t.Errorf("reader %s: %s", c.name, body)
		}
	}
	if body := raw(e.as(owner), "documents_version", map[string]any{"id": id, "version_id": v.ID}); !strings.Contains(body, "秘密の本文") {
		t.Fatalf("owner must read the draft: %s", body)
	}
	if err := docs.Publish(ctx, owner.id, id, v.ID); err != nil {
		t.Fatal(err)
	}
	if err := docs.SetSharing(ctx, owner.id, id, "selected", []int64{reader.id}, func(int64) bool { return true }); err != nil {
		t.Fatal(err)
	}
	if body := raw(e.as(reader), "documents_read", map[string]any{"id": id}); !strings.Contains(body, "秘密の本文") {
		t.Fatalf("a sharer must read: %s", body)
	}
	reads, err := docs.Reads(ctx, owner.id, id)
	if err != nil || len(reads) != 0 {
		t.Fatalf("reading through MCP must not mark as read: %v %v", reads, err)
	}
}

// 書き込みは確認を経て、本人の文書だけに行える。確認の後に共有の範囲が変われば公開しない。
// 不変条件: MCP-INV-2, MCP-INV-4, DOC-INV-3, DOC-INV-6
// ユースケース: UC-DOCUMENTS-9
func TestDocumentsMCP_WritesAreConfirmedAndGuarded(t *testing.T) {
	ctx := context.Background()
	e := newMCPEnv(t)
	e.enableDocuments()
	ownerAccount, other := e.account("user"), e.account("user")
	owner := e.as(ownerAccount)
	docs := e.docs()

	res := owner.run("documents_create", map[string]any{"title": "回覧", "body": "本文"})
	id := resultID(t, res)
	if _, _, err := docs.Current(ctx, other.id, id); err == nil {
		t.Fatal("a new document must stay private")
	}
	// 他人の文書は確認の前に「対象がありません」
	_, body := e.as(other).raw("tools/call", "documents_publish", map[string]any{"name": "documents_publish", "arguments": map[string]any{"id": id}, "_meta": nil})
	if strings.Contains(body, "input_required") {
		t.Fatalf("others must not reach the confirmation: %s", body)
	}
	owner.run("documents_update", map[string]any{"id": id, "title": "回覧の改訂", "body": "新しい本文"})
	_, d, err := docs.Draft(ctx, ownerAccount.id, id)
	if err != nil || d.Title != "回覧の改訂" {
		t.Fatalf("draft = %+v %v", d, err)
	}

	// 確認の文に共有の範囲が出て、確認の後に広げられたら公開しない
	state, message := owner.ask("documents_publish", map[string]any{"id": id})
	if !strings.Contains(strings.SplitN(message, "\n", 3)[1], "共有の範囲: 自分だけ") {
		t.Fatalf("message = %s", message)
	}
	if err := docs.SetSharing(ctx, ownerAccount.id, id, "everyone", nil, func(int64) bool { return true }); err != nil {
		t.Fatal(err)
	}
	result := owner.accept("documents_publish", map[string]any{"id": id}, state)
	if result["isError"] != true {
		t.Fatalf("published after the audience changed: %v", result)
	}
	if d, _, _ := docs.Current(ctx, ownerAccount.id, id); d.CurrentID != 0 {
		t.Fatal("must not be published")
	}

	// 承認の依頼: 確認の文に承認者が出る。依頼の後は公開も改定もできない
	stateA, messageA := owner.ask("documents_request_approval", map[string]any{"id": id, "approvers": []int64{other.id, other.id}})
	if !strings.Contains(messageA, "承認者はこの版を読めるようになります") || !strings.Contains(messageA, "> ") {
		t.Fatalf("message = %s", messageA)
	}
	owner.accept("documents_request_approval", map[string]any{"id": id, "approvers": []int64{other.id, other.id}}, stateA)
	_, body = e.as(ownerAccount).raw("tools/call", "documents_publish", map[string]any{"name": "documents_publish", "arguments": map[string]any{"id": id}})
	if strings.Contains(body, "input_required") {
		t.Fatalf("no draft to publish: %s", body)
	}
	if list, _ := docs.Approvals(ctx, other.id); len(list) != 1 {
		t.Fatalf("approvals = %v", list)
	}
	// 依頼を取り下げた文書は、承認なしで公開できない
	_, pending, err := docs.VersionFor(ctx, ownerAccount.id, id, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := docs.Withdraw(ctx, ownerAccount.id, id, pending.ID); err != nil {
		t.Fatal(err)
	}
	_, body = owner.raw("tools/call", "documents_publish", map[string]any{"name": "documents_publish", "arguments": map[string]any{"id": id}})
	if strings.Contains(body, "input_required") || !strings.Contains(body, "承認を経て公開") {
		t.Fatalf("publish after asking: %s", body)
	}
	// 自分を承認者に選べない
	id2 := resultID(t, owner.run("documents_create", map[string]any{"title": "別", "body": "本文"}))
	_, body = owner.raw("tools/call", "documents_request_approval", map[string]any{"name": "documents_request_approval", "arguments": map[string]any{"id": id2, "approvers": []int64{ownerAccount.id}}})
	if strings.Contains(body, "input_required") {
		t.Fatalf("the owner must not approve: %s", body)
	}
}

// 注意を持つ機能は確認の画面を経る。無効にするときと、注意の無い機能は今のまま。
// 不変条件: MCP-INV-12
func TestDocumentsMCP_EnableNeedsTheNotice(t *testing.T) {
	e := newMCPEnv(t)
	w := e.adminPost("/admin/mcp/documents/enable", "")
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/admin/mcp/features/documents/enable" {
		t.Fatalf("enable without the notice: %d %s", w.Code, w.Header().Get("Location"))
	}
	if slices.Contains(e.toolNames(), "documents_list") {
		t.Fatal("must stay disabled")
	}
	page := e.adminGet("/admin/mcp/features/documents/enable")
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "AIのサービスへ送られます") {
		t.Fatalf("notice page: %d", page.Code)
	}
	if !strings.Contains(e.adminGet("/admin/mcp").Body.String(), `href="/admin/mcp/features/documents/enable"`) {
		t.Fatal("the button must go to the notice")
	}
	e.enableDocuments()
	if !slices.Contains(e.toolNames(), "documents_list") {
		t.Fatal("must be enabled")
	}
	if w := e.adminGet("/admin/mcp/features/pages/enable"); w.Code != http.StatusNotFound {
		t.Fatalf("no notice page for pages: %d", w.Code)
	}
	site, _ := authtest.NewAccountSession(t, e.core.DB(), "site_admin")
	if w := e.routesGet(site, "/admin/mcp/features/documents/enable"); w.Code != http.StatusForbidden {
		t.Fatalf("notice page as site admin: %d", w.Code)
	}
}
