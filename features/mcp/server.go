package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"minimal-web-kit/features"
	"minimal-web-kit/internal/federation"
	"minimal-web-kit/internal/web"
)

const (
	protocolVersion = "2026-07-28"
	maxBody         = 64 << 10
	maxSummaryRunes = 4000
	confirmKey      = "confirm"
	metaVersion     = "io.modelcontextprotocol/protocolVersion"
	metaCapability  = "io.modelcontextprotocol/clientCapabilities"

	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternal       = -32603
	codeHeaderMismatch = -32020
	codeMissingCap     = -32021
	codeUnsupported    = -32022
)

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type callParams struct {
	Name           string                     `json:"name"`
	Arguments      json.RawMessage            `json:"arguments"`
	InputResponses map[string]json.RawMessage `json:"inputResponses"`
	RequestState   string                     `json:"requestState"`
	Meta           map[string]json.RawMessage `json:"_meta"`
}

// reply は結果を返します。resultType は result が持ちます。
func reply(w http.ResponseWriter, id json.RawMessage, result map[string]any) {
	if _, ok := result["resultType"]; !ok {
		result["resultType"] = "complete"
	}
	federation.JSON(w, http.StatusOK, map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func replyError(w http.ResponseWriter, status int, id json.RawMessage, code int, message string, data any) {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	federation.JSON(w, status, map[string]any{"jsonrpc": "2.0", "id": id, "error": rpcError{code, message, data}})
}

func text(message string, isError bool) map[string]any {
	return map[string]any{"content": []any{map[string]any{"type": "text", "text": message}}, "isError": isError}
}

// validOrigin は、Originが無いか、設定した公開オリジンと完全に一致することを確かめます。
// Sec-Fetch-Site を要求しないCLIも通すため、guard.SameOrigin は使いません。
func (f *Feature) validOrigin(r *http.Request) bool {
	values := r.Header.Values("Origin")
	if len(values) > 1 {
		return false
	}
	return len(values) == 0 || values[0] == f.auth.Origin()
}

// validHost は、要求のHostが設定のオリジンと同じことを確かめます。DNSリバインディングへの備えです。
func (f *Feature) validHost(r *http.Request) bool {
	u, err := url.Parse(f.auth.Origin())
	return err == nil && r.Host == u.Host
}

func (f *Feature) unauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+f.metadataURL()+`", scope="`+scopeRead+" "+scopeWrite+`"`)
	w.WriteHeader(http.StatusUnauthorized)
}

func (f *Feature) forbidden(w http.ResponseWriter, scope string) {
	w.Header().Set("WWW-Authenticate", `Bearer error="insufficient_scope", scope="`+scope+`", resource_metadata="`+f.metadataURL()+`"`)
	w.WriteHeader(http.StatusForbidden)
}

// handle はMCPの要求を1件処理します。応答はJSONだけで、セッションを持ちません。
func (f *Feature) handle(w http.ResponseWriter, r *http.Request) {
	if !f.validOrigin(r) || !f.validHost(r) {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	select {
	case f.slots <- struct{}{}:
		defer func() { <-f.slots }()
	default:
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusTooManyRequests)
		return
	}
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || len(r.Header.Values("Authorization")) != 1 {
		f.unauthorized(w)
		return
	}
	principal, err := f.auth.VerifyBearer(r.Context(), token, f.auth.Resource())
	if errors.Is(err, features.ErrBearerInvalid) {
		f.unauthorized(w)
		return
	} else if err != nil {
		f.logger.Error("mcp token check failed", web.RequestIDAttr(w))
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	if !principal.Role.CanManageSite() { // ロールを下げた後の、発行済みのトークンも止める
		w.WriteHeader(http.StatusForbidden)
		return
	}
	if !f.limits.allow(principal.Client + "|" + strconv.FormatInt(principal.AccountID, 10)) {
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusTooManyRequests)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil || !utf8.Valid(body) {
		replyError(w, http.StatusBadRequest, nil, codeInvalidRequest, "Invalid request", nil)
		return
	}
	var q rpcRequest
	if err := federation.Decode(body, &q); err != nil || q.JSONRPC != "2.0" || q.Method == "" {
		replyError(w, http.StatusBadRequest, nil, codeInvalidRequest, "Invalid request", nil)
		return
	}
	if len(q.ID) == 0 {
		w.WriteHeader(http.StatusAccepted) // 通知に応答はない
		return
	}
	if !validID(q.ID) {
		replyError(w, http.StatusBadRequest, nil, codeInvalidRequest, "Invalid request", nil)
		return
	}
	var params callParams
	if len(q.Params) > 0 && json.Unmarshal(q.Params, &params) != nil {
		replyError(w, http.StatusBadRequest, q.ID, codeInvalidParams, "Invalid params", nil)
		return
	}
	if !f.checkHeaders(w, r, q, params) {
		return
	}
	switch q.Method {
	case "server/discover":
		reply(w, q.ID, map[string]any{
			"supportedVersions": []string{protocolVersion},
			"capabilities":      map[string]any{"tools": map[string]any{}},
			"ttlMs":             0,
			"cacheScope":        "private",
			"_meta":             map[string]any{"io.modelcontextprotocol/serverInfo": map[string]any{"name": "minimal-web-kit", "version": "1"}},
		})
	case "ping":
		reply(w, q.ID, map[string]any{})
	case "tools/list":
		f.listTools(w, r, q, principal)
	case "tools/call":
		f.callTool(w, r, q, params, principal)
	default:
		replyError(w, http.StatusNotFound, q.ID, codeMethodNotFound, "Method not found", nil)
	}
}

// validID は、文字列か整数のIDだけを受け付けます。
func validID(id json.RawMessage) bool {
	var v any
	if json.Unmarshal(id, &v) != nil {
		return false
	}
	switch n := v.(type) {
	case string:
		return len(n) <= 128
	case float64:
		return n == float64(int64(n))
	}
	return false
}

// checkHeaders は、版とHTTPヘッダーを本文と照合します。層によって解釈が変わらないようにします。
func (f *Feature) checkHeaders(w http.ResponseWriter, r *http.Request, q rpcRequest, p callParams) bool {
	version := r.Header.Get("MCP-Protocol-Version")
	if version != protocolVersion {
		replyError(w, http.StatusBadRequest, q.ID, codeUnsupported, "Unsupported protocol version", map[string]any{"supported": []string{protocolVersion}})
		return false
	}
	var meta string
	var caps map[string]json.RawMessage
	if json.Unmarshal(p.Meta[metaVersion], &meta) != nil || meta == "" || json.Unmarshal(p.Meta[metaCapability], &caps) != nil || caps == nil {
		replyError(w, http.StatusBadRequest, q.ID, codeInvalidParams, "Invalid params", nil)
		return false
	}
	mismatch := r.Header.Get("Mcp-Method") != q.Method || meta != version
	if q.Method == "tools/call" && r.Header.Get("Mcp-Name") != p.Name {
		mismatch = true
	}
	if mismatch {
		replyError(w, http.StatusBadRequest, q.ID, codeHeaderMismatch, "Header mismatch", nil)
		return false
	}
	return true
}

func (f *Feature) listTools(w http.ResponseWriter, r *http.Request, q rpcRequest, p features.AgentPrincipal) {
	if !p.HasScope(scopeRead) && !p.HasScope(scopeWrite) {
		f.forbidden(w, scopeRead)
		return
	}
	settings, err := f.store.all(r.Context())
	if err != nil {
		f.logger.Error("mcp settings read failed", web.RequestIDAttr(w))
		replyError(w, http.StatusOK, q.ID, codeInternal, "Internal error", nil)
		return
	}
	tools := []any{}
	for _, name := range f.order {
		e := f.tools[name]
		if !settings[e.feature].Enabled {
			continue
		}
		read := e.tool.Effect == features.MCPRead
		if (read && !p.HasScope(scopeRead)) || (!read && !p.HasScope(scopeWrite)) {
			continue
		}
		item := map[string]any{
			"name": e.tool.Name, "title": e.tool.Title, "description": e.tool.Description, "inputSchema": e.tool.InputSchema,
			"annotations": map[string]any{"readOnlyHint": read, "destructiveHint": !read, "openWorldHint": false},
		}
		if !read {
			item["_meta"] = map[string]any{"anthropic/requiresUserInteraction": true}
		}
		tools = append(tools, item)
	}
	// 機能の有効・無効は再起動なしで変わり、変更を送る経路を持たないので、キャッシュさせない。一覧はトークンの権限で変わる
	reply(w, q.ID, map[string]any{"tools": tools, "ttlMs": 0, "cacheScope": "private"})
}

func (f *Feature) callTool(w http.ResponseWriter, r *http.Request, q rpcRequest, params callParams, p features.AgentPrincipal) {
	e, known := f.tools[params.Name]
	if !known {
		replyError(w, http.StatusOK, q.ID, codeInvalidParams, "Unknown tool", nil)
		return
	}
	need := scopeWrite
	if e.tool.Effect == features.MCPRead {
		need = scopeRead
	}
	if !p.HasScope(need) {
		f.forbidden(w, need)
		return
	}
	generation, err := f.featureGeneration(r.Context(), e.feature)
	if errors.Is(err, errFeatureDisabled) {
		replyError(w, http.StatusOK, q.ID, codeInvalidParams, "Unknown tool", nil)
		return
	} else if err != nil {
		f.logger.Error("mcp settings read failed", web.RequestIDAttr(w))
		replyError(w, http.StatusOK, q.ID, codeInternal, "Internal error", nil)
		return
	}
	if e.tool.Effect == features.MCPRead {
		value, err := e.tool.Read(r.Context(), p, params.Arguments)
		f.finish(w, r, q, e, value, err)
		return
	}
	f.write(w, r, q, e, params, p, generation)
}

// write は、書き込みの共通の処理です。書き込みのツールは、ここからだけ呼びます（MCP-INV-2）。
func (f *Feature) write(w http.ResponseWriter, r *http.Request, q rpcRequest, e entry, params callParams, p features.AgentPrincipal, generation int64) {
	if params.RequestState == "" && len(params.InputResponses) == 0 {
		f.ask(w, r, q, e, params, p, generation)
		return
	}
	state, err := f.confirm.open(params.RequestState)
	if err != nil || state.Account != p.AccountID || state.Client != p.Client || state.Generation != p.Generation ||
		state.Tool != e.tool.Name || state.Settings != generation {
		reply(w, q.ID, text("確認が無効です。実行していません。もう一度、操作からやり直してください。", true))
		return
	}
	if !f.confirm.consume(state) { // 結果を見る前に消費する。却下でも再利用できない
		reply(w, q.ID, text("この確認は使用済みです。実行していません。もう一度、操作からやり直してください。", true))
		return
	}
	if len(params.InputResponses) != 1 {
		reply(w, q.ID, text("確認の応答が正しくありません。実行していません。", true))
		return
	}
	var answer struct {
		Action  string          `json:"action"`
		Content json.RawMessage `json:"content"`
	}
	raw, ok := params.InputResponses[confirmKey]
	if !ok || json.Unmarshal(raw, &answer) != nil {
		reply(w, q.ID, text("確認の応答が正しくありません。実行していません。", true))
		return
	}
	switch answer.Action {
	case "accept":
	case "decline", "cancel":
		reply(w, q.ID, text("利用者が実行しませんでした。何も変更していません。", false))
		return
	default:
		reply(w, q.ID, text("確認の応答が正しくありません。実行していません。", true))
		return
	}
	plan, err := e.tool.Prepare(r.Context(), p, params.Arguments)
	if err != nil {
		f.finish(w, r, q, e, nil, err)
		return
	}
	if features.MCPState(string(plan.Canonical)) != state.Args {
		reply(w, q.ID, text("確認の後に引数が変わりました。実行していません。もう一度、操作からやり直してください。", true))
		return
	}
	// 接続が切れても、始めた保存は最後まで行う
	value, err := e.tool.Apply(context.WithoutCancel(r.Context()), p, plan.Canonical, state.Target)
	f.finish(w, r, q, e, value, err)
}

// ask は、検査をして、クライアントに利用者の確認を求めます。何も変更しません。
func (f *Feature) ask(w http.ResponseWriter, r *http.Request, q rpcRequest, e entry, params callParams, p features.AgentPrincipal, generation int64) {
	if !supportsForm(params.Meta[metaCapability]) {
		replyError(w, http.StatusBadRequest, q.ID, codeMissingCap, "Server requires the elicitation capability for this request",
			map[string]any{"requiredCapabilities": map[string]any{"elicitation": map[string]any{"form": map[string]any{}}}})
		return
	}
	plan, err := e.tool.Prepare(r.Context(), p, params.Arguments)
	if err != nil {
		f.finish(w, r, q, e, nil, err)
		return
	}
	if utf8.RuneCountInString(plan.Summary) > maxSummaryRunes {
		reply(w, q.ID, text("変更が大きく、確認の画面に全文を出せません。何も変更していません。管理画面から操作してください。", true))
		return
	}
	state, err := f.confirm.issue(confirmState{
		Account: p.AccountID, Client: p.Client, Generation: p.Generation, Tool: e.tool.Name,
		Args: features.MCPState(string(plan.Canonical)), Target: plan.State, Settings: generation,
	})
	if err != nil {
		f.logger.Error("mcp confirmation failed", web.RequestIDAttr(w))
		replyError(w, http.StatusOK, q.ID, codeInternal, "Internal error", nil)
		return
	}
	reply(w, q.ID, map[string]any{
		"resultType":   "input_required",
		"requestState": state,
		"inputRequests": map[string]any{confirmKey: map[string]any{"method": "elicitation/create", "params": map[string]any{
			"mode": "form", "message": plan.Summary, "requestedSchema": map[string]any{"type": "object", "properties": map[string]any{}},
		}}},
	})
}

// supportsForm は、クライアントがフォームの確認を宣言しているかを返します。空の elicitation はフォームを表します。
func supportsForm(raw json.RawMessage) bool {
	var caps struct {
		Elicitation *struct {
			Form json.RawMessage `json:"form"`
			URL  json.RawMessage `json:"url"`
		} `json:"elicitation"`
	}
	if json.Unmarshal(raw, &caps) != nil || caps.Elicitation == nil {
		return false
	}
	return len(caps.Elicitation.Form) > 0 || len(caps.Elicitation.URL) == 0
}

// finish は、ツールの結果を返します。ログには登録済みのツール名と固定の分類だけを出します（MCP-INV-8）。
func (f *Feature) finish(w http.ResponseWriter, r *http.Request, q rpcRequest, e entry, value any, err error) {
	var input features.MCPInputError
	switch {
	case err == nil:
		f.logger.Info("mcp tool executed", web.RequestIDAttr(w), slog.String("tool", e.tool.Name))
		raw, merr := json.Marshal(value)
		if merr != nil {
			replyError(w, http.StatusOK, q.ID, codeInternal, "Internal error", nil)
			return
		}
		reply(w, q.ID, text(string(raw), false))
	case errors.Is(err, features.ErrMCPConflict):
		reply(w, q.ID, text("確認の後に対象が変わりました。何も変更していません。内容を読み直してください。", true))
	case errors.As(err, &input):
		reply(w, q.ID, text(input.Message, true))
	default:
		f.logger.Error("mcp tool failed", web.RequestIDAttr(w), slog.String("tool", e.tool.Name))
		replyError(w, http.StatusOK, q.ID, codeInternal, "Internal error", nil)
	}
}
