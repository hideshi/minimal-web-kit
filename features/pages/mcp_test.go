package pages

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"minimal-web-kit/features"
)

// topPageSummaryLimit は、MCPの確認の文の上限（features/mcp の maxSummaryRunes）と同じ値です。
const topPageSummaryLimit = 12000

// TestMCP_TopPageSizedBodyFitsConfirmation は、トップページ程度の本文（約6,000字）の固定ページを、MCPで作成・編集するときの
// 確認の文が、全文を引用したまま上限に収まり、操作・対象・変更する欄が最初の3行にあることを確かめる
// （docs/plans/mcp-plan.md の残る論点1。紹介サイトのトップを書き直した本文は5,384字だった）。
// 不変条件: MCP-INV-9
func TestMCP_TopPageSizedBodyFitsConfirmation(t *testing.T) {
	fx := setup(t)
	body := "ヒーロー: 導入の文\n\n" + strings.Repeat("## 見出し {#sec-a}\nカード: 題名 | 説明の文が続きます。説明の文が続きます。\n質問: 問いの文ですか？\n答え: 答えの文です。答えの文です。\n\n", 75)
	if n := utf8.RuneCountInString(body); n < 5500 || n > 6500 {
		t.Fatalf("sample body is %d runes", n)
	}
	tools := map[string]features.MCPTool{}
	for _, tool := range fx.f.MCPTools() {
		tools[tool.Name] = tool
	}
	p := features.AgentPrincipal{AccountID: 1, Role: features.RoleSiteAdmin}
	args, _ := json.Marshal(map[string]any{"slug": "home", "title": "トップ", "body": body})
	plan, err := tools["pages_create"].Prepare(context.Background(), p, args)
	if err != nil {
		t.Fatal(err)
	}
	check := func(name, summary string) {
		t.Helper()
		if n := utf8.RuneCountInString(summary); n > topPageSummaryLimit {
			t.Errorf("%s: the confirmation is %d runes, over the limit", name, n)
		}
		if !strings.Contains(summary, "> カード: 題名 | 説明の文が続きます。") {
			t.Errorf("%s: the body must be quoted in full", name)
		}
		if lines := strings.SplitN(summary, "\n", 4); len(lines) < 3 || !strings.Contains(lines[2], "本文") {
			t.Errorf("%s: the changed fields must be in the first 3 lines: %q", name, lines)
		}
	}
	check("create", plan.Summary)

	id := fx.create("home", "トップ", "短い本文", "", false)
	args, _ = json.Marshal(map[string]any{"id": id, "slug": "home", "title": "トップ", "body": body, "nav_order": nil})
	plan, err = tools["pages_update"].Prepare(context.Background(), p, args)
	if err != nil {
		t.Fatal(err)
	}
	check("update", plan.Summary)
}
