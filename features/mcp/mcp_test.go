package mcp

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"minimal-web-kit/features"
	"minimal-web-kit/internal/store"
	"minimal-web-kit/internal/web"
)

const testOrigin = "http://localhost:8080"

// fakeAuth は、トークンの検証を差し替えます。トークンの文字列をそのまま持ち主に対応させます。
type fakeAuth struct {
	tokens map[string]features.AgentPrincipal
	broken bool
}

func (a *fakeAuth) RegisterAgentRoutes(*web.IsolatedMux) {}
func (a *fakeAuth) AuthorizationServers() []string       { return []string{testOrigin} }
func (a *fakeAuth) Resource() string                     { return testOrigin + "/mcp" }
func (a *fakeAuth) Origin() string                       { return testOrigin }
func (a *fakeAuth) AgentClients(context.Context) ([]features.AgentClient, error) {
	return nil, nil
}
func (a *fakeAuth) AgentClient(context.Context, string) (features.AgentClient, error) {
	return features.AgentClient{}, features.ErrAgentNotFound
}
func (a *fakeAuth) AddAgentClient(context.Context, string, string, int64) (features.AgentClient, error) {
	return features.AgentClient{}, nil
}
func (a *fakeAuth) SetAgentClientEnabled(context.Context, string, bool) error { return nil }
func (a *fakeAuth) SetAgentGrant(context.Context, string, int64, bool) error  { return nil }
func (a *fakeAuth) VerifyBearer(_ context.Context, token, resource string) (features.AgentPrincipal, error) {
	if a.broken {
		return features.AgentPrincipal{}, errors.New("database unavailable")
	}
	p, ok := a.tokens[token]
	if !ok || resource != a.Resource() {
		return p, features.ErrBearerInvalid
	}
	return p, nil
}

// demoFeature は、ツールの見本です。Apply の回数を数えます。
type demoFeature struct {
	applied atomic.Int64
	state   atomic.Value
	summary string
}

func (d *demoFeature) Name() string                                              { return "demo" }
func (d *demoFeature) Init(*sql.DB) error                                        { return nil }
func (d *demoFeature) RegisterRoutes(mux *http.ServeMux, sm *web.SessionManager) {}
func (d *demoFeature) MCPLabel() string                                          { return "見本" }

type demoArgs struct {
	Note string `json:"note"`
}

func (d *demoFeature) MCPTools() []features.MCPTool {
	schema := json.RawMessage(`{"type":"object"}`)
	prepare := func(_ context.Context, _ features.AgentPrincipal, args json.RawMessage) (features.MCPPlan, error) {
		var a demoArgs
		if err := features.MCPDecode(args, &a); err != nil {
			return features.MCPPlan{}, err
		}
		canonical, err := json.Marshal(a)
		if err != nil {
			return features.MCPPlan{}, err
		}
		summary := d.summary
		if summary == "" {
			summary = "見本を変更します。\n" + features.MCPQuote(a.Note)
		}
		return features.MCPPlan{Summary: summary, Canonical: canonical, State: d.state.Load().(string)}, nil
	}
	apply := func(_ context.Context, _ features.AgentPrincipal, _ json.RawMessage, state string) (any, error) {
		if state != d.state.Load().(string) {
			return nil, features.ErrMCPConflict
		}
		d.applied.Add(1)
		return map[string]any{"ok": true}, nil
	}
	return []features.MCPTool{
		{Name: "demo_read", Title: "読む", InputSchema: schema, Effect: features.MCPRead, Read: func(context.Context, features.AgentPrincipal, json.RawMessage) (any, error) {
			return map[string]any{"applied": d.applied.Load()}, nil
		}},
		{Name: "demo_write", Title: "書く", InputSchema: schema, Prepare: prepare, Apply: apply}, // Effect を省いても書き込み
		{Name: "demo_other", Title: "別の書き込み", InputSchema: schema, Effect: features.MCPWrite, Prepare: prepare, Apply: apply},
	}
}

type env struct {
	f    *Feature
	demo *demoFeature
	auth *fakeAuth
	logs *bytes.Buffer
}

func newEnv(t *testing.T) *env {
	t.Helper()
	s, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "mcp.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	logs := &bytes.Buffer{}
	f := NewFeature(slog.New(slog.NewTextHandler(logs, nil)))
	if err := f.Init(s.DB()); err != nil {
		t.Fatal(err)
	}
	full := []string{scopeRead, scopeWrite}
	a := &fakeAuth{tokens: map[string]features.AgentPrincipal{
		"good":  {AccountID: 1, Client: "c1", Role: features.RoleMember, Scopes: full, Generation: "1.1.1"},
		"other": {AccountID: 2, Client: "c1", Role: features.RoleMember, Scopes: full, Generation: "1.1.1"},
		"read":  {AccountID: 1, Client: "c1", Role: features.RoleMember, Scopes: []string{scopeRead}, Generation: "1.1.1"},
	}}
	if err := f.SetAgentAuth(a); err != nil {
		t.Fatal(err)
	}
	demo := &demoFeature{}
	demo.state.Store("s1")
	if err := f.SetMCPFeatures([]features.MCPFeature{demo}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.set(context.Background(), "demo", true); err != nil {
		t.Fatal(err)
	}
	return &env{f: f, demo: demo, auth: a, logs: logs}
}

type response struct {
	Status int
	Header http.Header
	Error  *rpcError
	Result map[string]any
}

func (r response) text() string {
	content, _ := r.Result["content"].([]any)
	if len(content) == 0 {
		return ""
	}
	return content[0].(map[string]any)["text"].(string)
}

func (e *env) raw(t *testing.T, token, method, name string, body []byte, change func(*http.Request)) response {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
	req.Host = "localhost:8080"
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("MCP-Protocol-Version", protocolVersion)
	req.Header.Set("Mcp-Method", method)
	if name != "" {
		req.Header.Set("Mcp-Name", name)
	}
	if change != nil {
		change(req)
	}
	rec := httptest.NewRecorder()
	e.f.handle(rec, req)
	out := response{Status: rec.Code, Header: rec.Header()}
	var decoded struct {
		Error  *rpcError      `json:"error"`
		Result map[string]any `json:"result"`
	}
	if rec.Body.Len() > 0 && json.Unmarshal(rec.Body.Bytes(), &decoded) == nil {
		out.Error, out.Result = decoded.Error, decoded.Result
	}
	return out
}

func (e *env) rpc(t *testing.T, token, method, name string, params map[string]any) response {
	t.Helper()
	if params == nil {
		params = map[string]any{}
	}
	if _, ok := params["_meta"]; !ok {
		params["_meta"] = map[string]any{metaCapability: map[string]any{}}
	}
	meta := map[string]any{metaVersion: protocolVersion}
	for k, v := range params["_meta"].(map[string]any) {
		meta[k] = v
	}
	params["_meta"] = meta
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	if err != nil {
		t.Fatal(err)
	}
	return e.raw(t, token, method, name, body, nil)
}

var formCaps = map[string]any{metaCapability: map[string]any{"elicitation": map[string]any{"form": map[string]any{}}}}

func call(tool string, args map[string]any, extra map[string]any) map[string]any {
	p := map[string]any{"name": tool, "arguments": args, "_meta": formCaps}
	for k, v := range extra {
		p[k] = v
	}
	return p
}

// ask は確認を求めて、requestState を返します。
func (e *env) ask(t *testing.T, token, tool string, args map[string]any) string {
	t.Helper()
	r := e.rpc(t, token, "tools/call", tool, call(tool, args, nil))
	if r.Result["resultType"] != "input_required" {
		t.Fatalf("no confirmation: %+v", r)
	}
	return r.Result["requestState"].(string)
}

func answer(state, action string) map[string]any {
	return map[string]any{"requestState": state, "inputResponses": map[string]any{confirmKey: map[string]any{"action": action}}}
}

// TestTools_MustDeclareTheirKind は、種類と関数の組み合わせの誤りが起動を止めることを確かめます。
// 不変条件: MCP-INV-1
func TestTools_MustDeclareTheirKind(t *testing.T) {
	read := func(context.Context, features.AgentPrincipal, json.RawMessage) (any, error) { return nil, nil }
	prepare := func(context.Context, features.AgentPrincipal, json.RawMessage) (features.MCPPlan, error) {
		return features.MCPPlan{}, nil
	}
	apply := func(context.Context, features.AgentPrincipal, json.RawMessage, string) (any, error) { return nil, nil }
	schema := json.RawMessage(`{}`)
	for name, tool := range map[string]features.MCPTool{
		"write without apply":   {Name: "demo_x", InputSchema: schema, Prepare: prepare},
		"write without prepare": {Name: "demo_x", InputSchema: schema, Apply: apply},
		"undeclared with read":  {Name: "demo_x", InputSchema: schema, Read: read},
		"write with read":       {Name: "demo_x", InputSchema: schema, Read: read, Prepare: prepare, Apply: apply},
		"read with apply":       {Name: "demo_x", InputSchema: schema, Effect: features.MCPRead, Read: read, Apply: apply},
		"unknown effect":        {Name: "demo_x", InputSchema: schema, Effect: features.MCPEffect(9), Read: read},
		"other prefix":          {Name: "news_x", InputSchema: schema, Prepare: prepare, Apply: apply},
		"no schema":             {Name: "demo_x", Prepare: prepare, Apply: apply},
	} {
		if err := validTool("demo", tool); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

// TestUndeclaredEffect_IsAWrite は、種類を省いたツールが確認を求めることを確かめます。
// 不変条件: MCP-INV-1
func TestUndeclaredEffect_IsAWrite(t *testing.T) {
	e := newEnv(t)
	if r := e.rpc(t, "good", "tools/call", "demo_write", call("demo_write", nil, nil)); r.Result["resultType"] != "input_required" || e.demo.applied.Load() != 0 {
		t.Fatalf("%+v", r)
	}
}

// TestRead_RunsWithoutConfirmation は読み取りが確認なしで動くことを確かめます。
func TestRead_RunsWithoutConfirmation(t *testing.T) {
	e := newEnv(t)
	r := e.rpc(t, "read", "tools/call", "demo_read", map[string]any{"name": "demo_read"})
	if r.Result["isError"] != false || !strings.Contains(r.text(), "applied") {
		t.Fatalf("%+v", r)
	}
}

// TestWrite_RunsOnlyAfterAccept は、承諾のときだけ実行されることを確かめます。
// 不変条件: MCP-INV-2
func TestWrite_RunsOnlyAfterAccept(t *testing.T) {
	e := newEnv(t)
	args := map[string]any{"note": "x"}
	state := e.ask(t, "good", "demo_write", args)
	if e.demo.applied.Load() != 0 {
		t.Fatal("ran before confirmation")
	}
	r := e.rpc(t, "good", "tools/call", "demo_write", call("demo_write", args, answer(state, "accept")))
	if r.Result["isError"] != false || e.demo.applied.Load() != 1 {
		t.Fatalf("%+v", r)
	}
	for _, action := range []string{"decline", "cancel", "", "ACCEPT", "approve"} {
		state := e.ask(t, "good", "demo_write", args)
		e.rpc(t, "good", "tools/call", "demo_write", call("demo_write", args, answer(state, action)))
		if e.demo.applied.Load() != 1 {
			t.Fatalf("action %q ran the tool", action)
		}
	}
}

// TestWrite_RefusesWithoutConfirmationSupport は、確認に対応しないクライアントでは変更しないことを確かめます。
// 不変条件: MCP-INV-2
func TestWrite_RefusesWithoutConfirmationSupport(t *testing.T) {
	e := newEnv(t)
	for name, meta := range map[string]map[string]any{
		"none":     {metaCapability: map[string]any{}},
		"no form":  {metaCapability: map[string]any{"roots": map[string]any{}}},
		"url only": {metaCapability: map[string]any{"elicitation": map[string]any{"url": map[string]any{}}}},
	} {
		r := e.rpc(t, "good", "tools/call", "demo_write", map[string]any{"name": "demo_write", "_meta": meta})
		if r.Status != 400 || r.Error == nil || r.Error.Code != codeMissingCap || e.demo.applied.Load() != 0 {
			t.Errorf("%s: %+v", name, r)
		}
	}
	if r := e.rpc(t, "good", "tools/call", "demo_write", map[string]any{"name": "demo_write", "_meta": map[string]any{metaCapability: map[string]any{"elicitation": map[string]any{}}}}); r.Result["resultType"] != "input_required" {
		t.Fatalf("empty elicitation means form: %+v", r)
	}
}

// TestWrite_AnswerNeedsAValidState は、応答だけを送っても実行されないことを確かめます。
// 不変条件: MCP-INV-2
func TestWrite_AnswerNeedsAValidState(t *testing.T) {
	e := newEnv(t)
	state := e.ask(t, "good", "demo_write", nil)
	for name, extra := range map[string]map[string]any{
		"no state":      {"inputResponses": map[string]any{confirmKey: map[string]any{"action": "accept"}}},
		"forged":        answer("e30.AAAA", "accept"),
		"tampered":      answer(strings.Replace(state, "e", "f", 1), "accept"),
		"state only":    {"requestState": state},
		"unknown key":   {"requestState": state, "inputResponses": map[string]any{"other": map[string]any{"action": "accept"}}},
		"two responses": {"requestState": state, "inputResponses": map[string]any{confirmKey: map[string]any{"action": "accept"}, "x": map[string]any{"action": "accept"}}},
	} {
		e.rpc(t, "good", "tools/call", "demo_write", call("demo_write", nil, extra))
		if e.demo.applied.Load() != 0 {
			t.Fatalf("%s ran the tool", name)
		}
	}
}

// TestConfirmation_IsBound は、別の持ち主・別のツール・別の引数・別の世代に流用できないことを確かめます。
// 不変条件: MCP-INV-3
func TestConfirmation_IsBound(t *testing.T) {
	cases := map[string]func(e *env, state string) response{
		"other account": func(e *env, s string) response {
			return e.rpc(t, "other", "tools/call", "demo_write", call("demo_write", map[string]any{"note": "x"}, answer(s, "accept")))
		},
		"other tool": func(e *env, s string) response {
			return e.rpc(t, "good", "tools/call", "demo_other", call("demo_other", map[string]any{"note": "x"}, answer(s, "accept")))
		},
		"other args": func(e *env, s string) response {
			return e.rpc(t, "good", "tools/call", "demo_write", call("demo_write", map[string]any{"note": "y"}, answer(s, "accept")))
		},
		"other generation": func(e *env, s string) response {
			p := e.auth.tokens["good"]
			p.Generation = "1.1.2"
			e.auth.tokens["good"] = p
			return e.rpc(t, "good", "tools/call", "demo_write", call("demo_write", map[string]any{"note": "x"}, answer(s, "accept")))
		},
	}
	for name, try := range cases {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			state := e.ask(t, "good", "demo_write", map[string]any{"note": "x"})
			if r := try(e, state); r.Result["isError"] != true {
				t.Fatalf("%+v", r)
			}
			if e.demo.applied.Load() != 0 {
				t.Fatal("ran")
			}
		})
	}
}

// TestConfirmation_SingleUse は、承諾の再送、却下の後の承諾、同時の送信で、実行が1回までなことを確かめます。
// 不変条件: MCP-INV-3
func TestConfirmation_SingleUse(t *testing.T) {
	e := newEnv(t)
	state := e.ask(t, "good", "demo_write", nil)
	e.rpc(t, "good", "tools/call", "demo_write", call("demo_write", nil, answer(state, "accept")))
	if r := e.rpc(t, "good", "tools/call", "demo_write", call("demo_write", nil, answer(state, "accept"))); r.Result["isError"] != true || e.demo.applied.Load() != 1 {
		t.Fatalf("replay: %+v", r)
	}
	state = e.ask(t, "good", "demo_write", nil)
	e.rpc(t, "good", "tools/call", "demo_write", call("demo_write", nil, answer(state, "decline")))
	e.rpc(t, "good", "tools/call", "demo_write", call("demo_write", nil, answer(state, "accept")))
	if e.demo.applied.Load() != 1 {
		t.Fatal("accept after decline ran")
	}
	state = e.ask(t, "good", "demo_write", nil)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e.rpc(t, "good", "tools/call", "demo_write", call("demo_write", nil, answer(state, "accept")))
		}()
	}
	wg.Wait()
	if got := e.demo.applied.Load(); got != 2 {
		t.Fatalf("concurrent accepts ran %d times", got-1)
	}
}

// TestConfirmation_Expires は期限切れを確かめます。
// 不変条件: MCP-INV-3
func TestConfirmation_Expires(t *testing.T) {
	e := newEnv(t)
	state := e.ask(t, "good", "demo_write", nil)
	base := e.f.now
	e.f.now = func() time.Time { return base().Add(confirmTTL + time.Second) }
	e.rpc(t, "good", "tools/call", "demo_write", call("demo_write", nil, answer(state, "accept")))
	if e.demo.applied.Load() != 0 {
		t.Fatal("expired confirmation ran")
	}
}

// TestConflict_NothingSaved は、確認の後に対象が変わると実行しないことを確かめます。
// 不変条件: MCP-INV-4
func TestConflict_NothingSaved(t *testing.T) {
	e := newEnv(t)
	state := e.ask(t, "good", "demo_write", nil)
	e.demo.state.Store("s2")
	r := e.rpc(t, "good", "tools/call", "demo_write", call("demo_write", nil, answer(state, "accept")))
	if r.Result["isError"] != true || e.demo.applied.Load() != 0 {
		t.Fatalf("%+v", r)
	}
}

// TestDisabled_HiddenAndRejected は、無効の機能が見えず、確認の途中でも実行されず、有効に戻しても古い確認が使えないことを確かめます。
// 不変条件: MCP-INV-5
func TestDisabled_HiddenAndRejected(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	state := e.ask(t, "good", "demo_write", nil)
	if err := e.f.store.set(ctx, "demo", false); err != nil {
		t.Fatal(err)
	}
	if r := e.rpc(t, "good", "tools/list", "", nil); len(r.Result["tools"].([]any)) != 0 {
		t.Fatalf("%+v", r)
	}
	for _, tool := range []string{"demo_read", "demo_write"} {
		if r := e.rpc(t, "good", "tools/call", tool, call(tool, nil, nil)); r.Error == nil {
			t.Fatalf("%s: %+v", tool, r)
		}
	}
	e.rpc(t, "good", "tools/call", "demo_write", call("demo_write", nil, answer(state, "accept")))
	if err := e.f.store.set(ctx, "demo", true); err != nil {
		t.Fatal(err)
	}
	e.rpc(t, "good", "tools/call", "demo_write", call("demo_write", nil, answer(state, "accept")))
	if e.demo.applied.Load() != 0 {
		t.Fatal("old confirmation survived re-enabling")
	}
}

func TestDefault_FeaturesAreDisabled(t *testing.T) {
	e := newEnv(t)
	if _, err := e.f.store.db.Exec("DELETE FROM mcp_feature_settings"); err != nil {
		t.Fatal(err)
	}
	if r := e.rpc(t, "good", "tools/list", "", nil); len(r.Result["tools"].([]any)) != 0 {
		t.Fatalf("%+v", r)
	}
}

// TestAuth_Rejections は、無効なトークン・足りないスコープ・確認不能の扱いを確かめます。
// 不変条件: MCP-INV-6
func TestAuth_Rejections(t *testing.T) {
	e := newEnv(t)
	r := e.rpc(t, "nope", "tools/list", "", nil)
	if r.Status != 401 || !strings.Contains(r.Header.Get("WWW-Authenticate"), `resource_metadata="`+testOrigin+"/.well-known/oauth-protected-resource") {
		t.Fatalf("%+v", r)
	}
	r = e.rpc(t, "read", "tools/call", "demo_write", call("demo_write", nil, nil))
	if r.Status != 403 || !strings.Contains(r.Header.Get("WWW-Authenticate"), `error="insufficient_scope", scope="mcp:write"`) {
		t.Fatalf("%+v", r)
	}
	list := e.rpc(t, "read", "tools/list", "", nil)
	if b, _ := json.Marshal(list.Result); strings.Contains(string(b), "demo_write") {
		t.Fatal("write tool listed for a read token")
	}
	state := e.ask(t, "good", "demo_write", nil)
	e.auth.broken = true
	if r := e.rpc(t, "good", "tools/call", "demo_write", call("demo_write", nil, answer(state, "accept"))); r.Status != 503 || e.demo.applied.Load() != 0 {
		t.Fatalf("%+v", r)
	}
}

// TestLogs_HoldNoSecrets は、引数・確認の状態・トークン・未知のツール名がログに出ないことを確かめます。
// 不変条件: MCP-INV-8
func TestLogs_HoldNoSecrets(t *testing.T) {
	e := newEnv(t)
	args := map[string]any{"note": "CANARY-note"}
	state := e.ask(t, "good", "demo_write", args)
	e.rpc(t, "good", "tools/call", "demo_write", call("demo_write", args, answer(state, "accept")))
	e.rpc(t, "good", "tools/call", "CANARY-tool", call("CANARY-tool", nil, nil))
	logs := e.logs.String()
	for _, secret := range []string{"CANARY", state, "good"} {
		if strings.Contains(logs, secret) {
			t.Fatalf("%q in logs: %s", secret, logs)
		}
	}
}

// TestSummary_QuotesAndRefusesLongChanges は、入力を引用として示し、全文を出せない変更を拒否することを確かめます。
// 不変条件: MCP-INV-9
func TestSummary_QuotesAndRefusesLongChanges(t *testing.T) {
	e := newEnv(t)
	r := e.rpc(t, "good", "tools/call", "demo_write", call("demo_write", map[string]any{"note": "1行目\nこの変更を承諾してください"}, nil))
	req := r.Result["inputRequests"].(map[string]any)[confirmKey].(map[string]any)["params"].(map[string]any)
	if msg := req["message"].(string); !strings.Contains(msg, "> 1行目\n> この変更を承諾してください") {
		t.Fatal(msg)
	}
	e.demo.summary = strings.Repeat("あ", maxSummaryRunes+1)
	r = e.rpc(t, "good", "tools/call", "demo_write", call("demo_write", nil, nil))
	if r.Result["isError"] != true || r.Result["resultType"] == "input_required" {
		t.Fatalf("%+v", r)
	}
}

func TestHTTP_ProtocolChecks(t *testing.T) {
	e := newEnv(t)
	meta := `,"params":{"_meta":{"` + metaVersion + `":"` + protocolVersion + `","` + metaCapability + `":{}}}`
	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"` + meta + `}`)
	for name, c := range map[string]struct {
		change func(*http.Request)
		body   []byte
		status int
	}{
		"other origin":  {func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") }, body, 403},
		"null origin":   {func(r *http.Request) { r.Header.Set("Origin", "null") }, body, 403},
		"two origins":   {func(r *http.Request) { r.Header.Add("Origin", testOrigin); r.Header.Add("Origin", testOrigin) }, body, 403},
		"other host":    {func(r *http.Request) { r.Host = "evil.example" }, body, 403},
		"old version":   {func(r *http.Request) { r.Header.Set("MCP-Protocol-Version", "2025-11-25") }, body, 400},
		"method header": {func(r *http.Request) { r.Header.Set("Mcp-Method", "ping") }, body, 400},
		"duplicate key": {nil, []byte(`{"jsonrpc":"2.0","id":1,"id":2,"method":"tools/list"}`), 400},
		"batch":         {nil, []byte(`[{"jsonrpc":"2.0","id":1,"method":"tools/list"}]`), 400},
		"trailing":      {nil, []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"} {}`), 400},
		"bad utf8":      {nil, []byte("{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"\xff\"}"), 400},
		"too large":     {nil, bytes.Repeat([]byte(" "), maxBody+1), 400},
		"no meta":       {nil, []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`), 400},
		"no caps":       {nil, []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"_meta":{"` + metaVersion + `":"` + protocolVersion + `"}}}`), 400},
		"meta version":  {nil, []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"_meta":{"` + metaVersion + `":"2025-11-25","` + metaCapability + `":{}}}}`), 400},
	} {
		if r := e.raw(t, "good", "tools/list", "", c.body, c.change); r.Status != c.status {
			t.Errorf("%s: %d", name, r.Status)
		}
	}
	if r := e.raw(t, "good", "tools/list", "", body, func(r *http.Request) { r.Header.Set("Origin", testOrigin) }); r.Status != 200 {
		t.Fatal(r.Status)
	}
	// 本文とヘッダーでツールの名前が違えば、読み取りに見せた書き込みも通らない
	raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "demo_write", "_meta": map[string]any{metaVersion: protocolVersion, metaCapability: map[string]any{}}}})
	if r := e.raw(t, "good", "tools/call", "demo_read", raw, nil); r.Status != 400 {
		t.Fatal(r.Status)
	}
}

func TestHTTP_SpecShapes(t *testing.T) {
	e := newEnv(t)
	r := e.rpc(t, "good", "server/discover", "", nil)
	if r.Result["resultType"] != "complete" || r.Result["serverInfo"] != nil {
		t.Fatalf("%+v", r)
	}
	if _, ok := r.Result["_meta"].(map[string]any)["io.modelcontextprotocol/serverInfo"]; !ok {
		t.Fatalf("%+v", r)
	}
	if r := e.rpc(t, "good", "resources/list", "", nil); r.Status != 404 || r.Error == nil || r.Error.Code != codeMethodNotFound {
		t.Fatalf("%+v", r)
	}
	for _, method := range []string{"tools/list", "server/discover"} {
		r := e.rpc(t, "good", method, "", nil)
		if r.Result["resultType"] != "complete" || r.Result["ttlMs"] != float64(0) || r.Result["cacheScope"] != "private" {
			t.Fatalf("%s: %+v", method, r)
		}
	}
	if r := e.rpc(t, "read", "tools/call", "demo_read", map[string]any{"name": "demo_read"}); r.Result["resultType"] != "complete" {
		t.Fatalf("%+v", r)
	}
}

func TestLimiter_PerClient(t *testing.T) {
	e := newEnv(t)
	limited := 0
	for range limitBurst + 20 {
		if r := e.rpc(t, "good", "ping", "", nil); r.Status == 429 {
			limited++
		}
	}
	if limited == 0 {
		t.Fatal("no limit")
	}
	if r := e.rpc(t, "other", "ping", "", nil); r.Status != 200 {
		t.Fatal("limit shared between accounts")
	}
}
