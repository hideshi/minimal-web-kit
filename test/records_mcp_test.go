package test

import (
	"net/http"
	"strings"
	"testing"

	"minimal-web-kit/features/auth/authtest"
)

// enableRecordsMCP は、MCP の設定で業務記録を有効にします。
func enableRecordsMCP(t *testing.T, e *mcpEnv) {
	t.Helper()
	if w := e.adminPost("/admin/mcp/records/enable", "confirmed=1"); w.Code != http.StatusSeeOther {
		t.Fatalf("enable records: %d %s", w.Code, w.Body.String())
	}
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

// TestRecordsMCP_EnableNoticeNamesKinds は、有効にする確認の画面に、使えるようになる種類の名前と、AIのサービスへ送られることが出ることを確かめます。
// 不変条件: RECORDS-INV-11
func TestRecordsMCP_EnableNoticeNamesKinds(t *testing.T) {
	e := newMCPEnv(t)
	req := e.adminGet("/admin/mcp/features/records/enable")
	if req.Code != http.StatusOK || !strings.Contains(req.Body.String(), "「shared」") || !strings.Contains(req.Body.String(), "AIのサービスへ送られます") {
		t.Fatalf("%d %s", req.Code, req.Body.String())
	}
}
