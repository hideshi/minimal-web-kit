package records

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"minimal-web-kit/features"
)

type fakeDirectory []features.DirectoryAccount

func (d fakeDirectory) Accounts(context.Context) ([]features.DirectoryAccount, error) { return d, nil }

func mcpFeature(t *testing.T, access RecordAccess) (*Feature, RecordDef) {
	t.Helper()
	db, _ := testDB(t)
	d := kindDef(t, "k", add("note", Text))
	d.Access, d.MCP = access, true
	f := NewFeature(slog.New(slog.NewTextHandler(io.Discard, nil)), []RecordDef{d}, nil)
	if err := f.Init(db); err != nil {
		t.Fatal(err)
	}
	f.SetAccountDirectory(fakeDirectory{{ID: 1, Name: "本人"}, {ID: 2, Name: "他の人"}})
	return f, d
}

func toolOf(f *Feature, name string) features.MCPTool {
	for _, tl := range f.MCPTools() {
		if tl.Name == name {
			return tl
		}
	}
	panic(name)
}

// TestMCPApply_RechecksRevisionAndAccess は、確認（Prepare）の後、実行（Apply）の直前に記録が変わった時と、
// 実行の時に編集の権限が無くなった時に、Apply が保存しないことを確かめます。MCPのサーバーの再検査の後の、内側の守りです。
// 不変条件: RECORDS-INV-5, RECORDS-INV-11
func TestMCPApply_RechecksRevisionAndAccess(t *testing.T) {
	f, _ := mcpFeature(t, OwnerWriteAllRead)
	ctx := context.Background()
	owner := features.AgentPrincipal{AccountID: 1, Role: features.RoleUser}
	id, err := insertRecord(ctx, f.db, f.stmts["k"], 1, map[string]any{"note": "元"}, f.now())
	if err != nil {
		t.Fatal(err)
	}
	upd := toolOf(f, "records_k_update")
	args, _ := json.Marshal(map[string]any{"id": id, "values": map[string]any{"note": "MCP"}})
	plan, err := upd.Prepare(ctx, owner, args)
	if err != nil {
		t.Fatal(err)
	}
	// 確認の後、実行の直前に、画面で保存された
	if err := updateRecordRevision(ctx, f.db, f.stmts["k"], id, 1, map[string]any{"note": "画面"}, f.now()); err != nil {
		t.Fatal(err)
	}
	if _, err := upd.Apply(ctx, owner, plan.Canonical, plan.State); !errors.Is(err, features.ErrMCPConflict) {
		t.Errorf("stale apply: %v", err)
	}
	// 実行の時に、別の人として呼ばれた（ロールや持ち主の変化）
	plan, err = upd.Prepare(ctx, owner, args)
	if err != nil {
		t.Fatal(err)
	}
	other := features.AgentPrincipal{AccountID: 2, Role: features.RoleUser}
	if _, err := upd.Apply(ctx, other, plan.Canonical, plan.State); !errors.Is(err, features.ErrMCPConflict) {
		t.Errorf("apply without edit rights: %v", err)
	}
	r, _ := getRecord(ctx, f.db, f.stmts["k"], id)
	if r.Values["note"] != "画面" {
		t.Errorf("note = %v", r.Values["note"])
	}
}

// TestPrivacyNotices_MentionsKeptRetiredKinds は、やめたが表を消していない種類の記録が残っていることを、説明に出すことを確かめます。
func TestPrivacyNotices_MentionsKeptRetiredKinds(t *testing.T) {
	d := kindDef(t, "k", add("note", Text))
	f := NewFeature(nil, []RecordDef{d}, []RetiredDef{{Name: "old_kind"}, {Name: "gone_kind", Dropped: true}})
	n := f.PrivacyNotices()
	if len(n) != 1 || !n[0].Internal || !strings.Contains(n[0].Lead, "old_kind") || strings.Contains(n[0].Lead, "gone_kind") {
		t.Fatalf("%+v", n)
	}
}

// TestMCPCreate_OmittedBoolIsNo は、MCPの作成で省いた「はい・いいえ」の項目が、画面の作成と同じく「いいえ」（0）で保存されることを確かめます。
// 不変条件: RECORDS-INV-6
func TestMCPCreate_OmittedBoolIsNo(t *testing.T) {
	db, _ := testDB(t)
	d := kindDef(t, "k", add("note", Text), add("done", Bool))
	d.MCP = true
	f := NewFeature(slog.New(slog.NewTextHandler(io.Discard, nil)), []RecordDef{d}, nil)
	if err := f.Init(db); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	p := features.AgentPrincipal{AccountID: 1, Role: features.RoleUser}
	args, _ := json.Marshal(map[string]any{"values": map[string]any{"note": "x"}})
	tool := toolOf(f, "records_k_create")
	plan, err := tool.Prepare(ctx, p, args)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tool.Apply(ctx, p, plan.Canonical, plan.State); err != nil {
		t.Fatal(err)
	}
	r, err := getRecord(ctx, f.db, f.stmts["k"], 1)
	if err != nil || r.Values["done"] != int64(0) {
		t.Fatalf("done = %#v %v", r.Values["done"], err)
	}
}
