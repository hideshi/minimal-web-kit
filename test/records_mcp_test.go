package test

import (
	"net/http"
	"strings"
	"testing"

	"minimal-web-kit/features/auth/authtest"
	"minimal-web-kit/features/records"
)

// enableRecordsMCP は、MCP の設定で業務記録の種類を有効にします。省けば試験の4つの種類の全部です。
func enableRecordsMCP(t *testing.T, e *mcpEnv, kinds ...string) {
	t.Helper()
	if len(kinds) == 0 {
		kinds = []string{"only", "adminread", "allread", "shared"}
	}
	for _, kind := range kinds {
		if w := e.adminPost("/admin/mcp/records."+kind+"/enable", "confirmed=1"); w.Code != http.StatusSeeOther {
			t.Fatalf("enable records.%s: %d %s", kind, w.Code, w.Body.String())
		}
	}
}

// recordsNoMCPDef は、定義で MCP を有効にしていない種類です。
func recordsNoMCPDef() records.RecordDef {
	d := recordsTestDefs()[0]
	d.Name, d.Title, d.MCP = "nomcp", "nomcp", false
	return d
}

// recordsToolNames は、今の一覧に出る業務記録のツールの名前です。
func recordsToolNames(e *mcpEnv) string {
	var out []string
	for _, tool := range e.call("tools/list", "", map[string]any{})["tools"].([]any) {
		if name := tool.(map[string]any)["name"].(string); strings.HasPrefix(name, "records_") {
			out = append(out, name)
		}
	}
	return strings.Join(out, " ")
}

// TestRecordsMCP_CreateUpdateAndSummary は、作成と編集が、確認の文の最初の3行に操作・対象・変わる項目を置き、
// 値を引用で全文出し、編集では変える項目だけを変えることを確かめます。
// 不変条件: RECORDS-INV-11
// ユースケース: UC-RECORDS-6
func TestRecordsMCP_CreateUpdateAndSummary(t *testing.T) {
	e := newMCPEnv(t)
	enableRecordsMCP(t, e)
	create := map[string]any{"values": map[string]any{"note": "MCPのメモ", "day": "2026-11-06", "state": "未", "qty": 3}}
	state, message := e.ask("records_shared_create", create)
	lines := strings.Split(message, "\n")
	if !strings.Contains(lines[0], "作成します") || !strings.Contains(lines[1], "対象: shared") || lines[2] != "変更: メモ、日付、状態、数" || !strings.Contains(message, "> MCPのメモ") {
		t.Fatal(message)
	}
	id := resultID(t, e.accept("records_shared_create", create, state))

	update := map[string]any{"id": id, "values": map[string]any{"qty": 7, "day": nil}}
	state, message = e.ask("records_shared_update", update)
	lines = strings.Split(message, "\n")
	if !strings.Contains(lines[0], "編集します") || !strings.Contains(lines[1], "#") || lines[2] != "変更: 日付、数" || strings.Contains(message, "MCPのメモ") {
		t.Fatal(message)
	}
	resultID(t, e.accept("records_shared_update", update, state))
	var note, stateValue string
	var qty int64
	var day *string
	e.core.DB().QueryRow(`SELECT f_note, f_state, f_qty, f_day FROM records_shared WHERE id = ?`, id).Scan(&note, &stateValue, &qty, &day)
	if note != "MCPのメモ" || stateValue != "未" || qty != 7 || day != nil {
		t.Errorf("note=%q state=%q qty=%d day=%v", note, stateValue, qty, day)
	}

	r := e.call("tools/call", "records_shared_get", map[string]any{"name": "records_shared_get", "arguments": map[string]any{"id": id}})
	if text := resultText(t, r); !strings.Contains(text, `"qty":7`) || !strings.Contains(text, `"mine":true`) {
		t.Fatal(text)
	}
	if r := e.call("tools/call", "records_shared_list", map[string]any{"name": "records_shared_list", "arguments": map[string]any{"query": "MCP"}}); !strings.Contains(resultText(t, r), "MCPのメモ") {
		t.Fatalf("%+v", r)
	}
}

// TestRecordsMCP_ConflictAfterConfirmation は、確認の後に管理画面等で記録が変わったら、実行しないことを確かめます。
// 不変条件: RECORDS-INV-5, RECORDS-INV-11
func TestRecordsMCP_ConflictAfterConfirmation(t *testing.T) {
	e := newMCPEnv(t)
	enableRecordsMCP(t, e)
	id := resultID(t, e.run("records_shared_create", map[string]any{"values": map[string]any{"note": "元"}}))
	update := map[string]any{"id": id, "values": map[string]any{"note": "MCP"}}
	state, _ := e.ask("records_shared_update", update)
	if _, err := e.core.DB().Exec(`UPDATE records_shared SET f_note = '画面', revision = revision + 1 WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	if r := e.accept("records_shared_update", update, state); r["isError"] != true {
		t.Fatalf("overwrote the change: %+v", r)
	}
	var note string
	e.core.DB().QueryRow(`SELECT f_note FROM records_shared WHERE id = ?`, id).Scan(&note)
	if note != "画面" {
		t.Errorf("note = %q", note)
	}
}

// TestRecordsMCP_PermissionsAndInput は、権限が本人のもので、読めない記録は無い記録と同じ答えになること、
// 型の違う値・知らない項目・選択肢に無い値を拒否すること、削除のツールが無いことを確かめます。
// 不変条件: RECORDS-INV-3, RECORDS-INV-4, RECORDS-INV-11
func TestRecordsMCP_PermissionsAndInput(t *testing.T) {
	e := newMCPEnv(t)
	enableRecordsMCP(t, e)
	_, other := authtest.NewAccountSession(t, e.core.DB(), "user")
	insert := func(kind string) int64 {
		res, err := e.core.DB().Exec(`INSERT INTO "records_`+kind+`" (owner_account, revision, created_at, updated_at, f_note) VALUES (?, 1, 1, 1, 'SECRET-OTHER')`, other)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		return id
	}
	onlyID, allreadID := insert("only"), insert("allread")
	missing := e.call("tools/call", "records_only_get", map[string]any{"name": "records_only_get", "arguments": map[string]any{"id": 999999}})
	hidden := e.call("tools/call", "records_only_get", map[string]any{"name": "records_only_get", "arguments": map[string]any{"id": onlyID}})
	if hidden["isError"] != true || resultText(t, hidden) != resultText(t, missing) {
		t.Errorf("unreadable must look like missing: %+v / %+v", hidden, missing)
	}
	if r := e.call("tools/call", "records_only_list", map[string]any{"name": "records_only_list"}); strings.Contains(resultText(t, r), "SECRET-OTHER") {
		t.Error("list shows another person's OwnerOnly record")
	}
	// システム管理者は allread を読めるが、他人の記録は編集できない
	if r := e.call("tools/call", "records_allread_get", map[string]any{"name": "records_allread_get", "arguments": map[string]any{"id": allreadID}}); r["isError"] != false {
		t.Errorf("admin cannot read allread: %+v", r)
	}
	r := e.call("tools/call", "records_allread_update", map[string]any{"name": "records_allread_update", "_meta": mcpCaps, "arguments": map[string]any{"id": allreadID, "values": map[string]any{"note": "x"}}})
	if r["isError"] != true {
		t.Errorf("admin edited another person's allread record: %+v", r)
	}
	for name, values := range map[string]map[string]any{
		"整数に文字":  {"note": "a", "qty": "3"},
		"整数に小数":  {"note": "a", "qty": 1.5},
		"知らない項目": {"note": "a", "other": "x"},
		"選択肢に無い": {"note": "a", "state": "途中"},
		"必須が無い":  {"day": "2026-11-06"},
		"日付の形":   {"note": "a", "day": "2026/11/06"},
		"文字に真偽値": {"note": true},
		"上限を超える": {"note": strings.Repeat("あ", 21)},
	} {
		r := e.call("tools/call", "records_shared_create", map[string]any{"name": "records_shared_create", "_meta": mcpCaps, "arguments": map[string]any{"values": values}})
		if r["isError"] != true {
			t.Errorf("%s: accepted %+v", name, r)
		}
	}
	list := e.call("tools/list", "", map[string]any{})
	raw := ""
	for _, tool := range list["tools"].([]any) {
		raw += tool.(map[string]any)["name"].(string) + " "
	}
	if !strings.Contains(raw, "records_shared_update") || strings.Contains(raw, "records_shared_delete") {
		t.Errorf("tools: %s", raw)
	}
}

// TestRecordsMCP_EnableNoticeNamesKinds は、有効にする確認の画面に、その種類の名前と、AIのサービスへ送られることが出ることを確かめます。
// 不変条件: RECORDS-INV-11, RECORDS-INV-19
func TestRecordsMCP_EnableNoticeNamesKinds(t *testing.T) {
	e := newMCPEnv(t)
	req := e.adminGet("/admin/mcp/features/records.shared/enable")
	body := req.Body.String()
	if req.Code != http.StatusOK || !strings.Contains(body, "「shared」") || strings.Contains(body, "「only」") || !strings.Contains(body, "AIのサービスへ送られます") {
		t.Fatalf("%d %s", req.Code, body)
	}
	if w := e.adminPost("/admin/mcp/records.shared/enable", ""); w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/admin/mcp/features/records.shared/enable" {
		t.Fatalf("enabled without the notice: %d %s", w.Code, w.Header().Get("Location"))
	}
}

// TestRecordsMCP_KindsAreEnabledSeparately は、MCPの有効・無効が種類ごとで、有効にした種類のツールだけが見えて動くこと、
// 機能全体の設定（records）は使わないこと、定義で MCP を有効にしていない種類は設定に出ないことを確かめます。
// 不変条件: RECORDS-INV-19
// ユースケース: UC-RECORDS-6
func TestRecordsMCP_KindsAreEnabledSeparately(t *testing.T) {
	e := newMCPEnv(t)
	if w := e.adminPost("/admin/mcp/records/enable", "confirmed=1"); w.Code != http.StatusNotFound {
		t.Fatalf("the whole feature can still be enabled: %d", w.Code)
	}
	if _, err := e.core.DB().Exec(`INSERT INTO mcp_feature_settings (feature, enabled, updated_at) VALUES ('records', 1, 0)`); err != nil {
		t.Fatal(err)
	}
	if got := recordsToolNames(e); got != "" {
		t.Fatalf("the old whole-feature row opened tools: %s", got)
	}
	enableRecordsMCP(t, e, "shared")
	if got := recordsToolNames(e); got != "records_shared_list records_shared_get records_shared_create records_shared_update" {
		t.Fatalf("tools = %s", got)
	}
	if r := e.call("tools/call", "records_only_list", map[string]any{"name": "records_only_list", "arguments": map[string]any{}}); r != nil {
		t.Fatalf("a disabled kind ran: %+v", r)
	}
	resultID(t, e.run("records_shared_create", map[string]any{"values": map[string]any{"note": "共有"}}))
	page := e.adminGet("/admin/mcp").Body.String()
	for _, label := range []string{"業務記録: shared", "業務記録: only"} {
		if !strings.Contains(page, label) {
			t.Errorf("settings page lacks %s", label)
		}
	}
	if strings.Contains(page, "業務記録: nomcp") || e.adminGet("/admin/mcp/features/records.nomcp/enable").Code != http.StatusNotFound {
		t.Error("a kind without MCP in its definition is listed")
	}
	if w := e.adminPost("/admin/mcp/records.shared/disable", ""); w.Code != http.StatusSeeOther {
		t.Fatalf("disable: %d", w.Code)
	}
	if got := recordsToolNames(e); got != "" {
		t.Fatalf("tools after disabling = %s", got)
	}
}

// TestRecordsMCP_PartialEditChecksWholeRecord は、MCPの部分の編集でも、変えない項目が今の定義に合わなければ（必須なのに空、
// 選択肢に無い）保存させず、直せば保存できること、作成で省いた「はい・いいえ」が画面と同じく「いいえ」になることを確かめます。
// 不変条件: RECORDS-INV-6, RECORDS-INV-11
func TestRecordsMCP_PartialEditChecksWholeRecord(t *testing.T) {
	e := newMCPEnv(t)
	enableRecordsMCP(t, e)
	id := resultID(t, e.run("records_shared_create", map[string]any{"values": map[string]any{"note": "元"}}))
	// 定義が変わって合わなくなった既存の値（必須のメモが空、選択肢に無い状態）
	if _, err := e.core.DB().Exec(`UPDATE records_shared SET f_note = NULL, f_state = '廃止した選択肢' WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	r := e.call("tools/call", "records_shared_update", map[string]any{"name": "records_shared_update", "_meta": mcpCaps, "arguments": map[string]any{"id": id, "values": map[string]any{"qty": 1}}})
	if r["isError"] != true || !strings.Contains(resultText(t, r), "note") {
		t.Fatalf("saved over values that no longer fit: %+v", r)
	}
	resultID(t, e.run("records_shared_update", map[string]any{"id": id, "values": map[string]any{"qty": 1, "note": "直した", "state": "済"}}))
}
