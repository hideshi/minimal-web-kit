package news

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"minimal-web-kit/features"
)

// mcpFixture は、お知らせのツールを、MCPサーバーを通さずに直接呼びます。確認は Prepare の結果をそのまま Apply に渡します。
type mcpFixture struct {
	t     *testing.T
	f     *Feature
	posts *PostStore
	tools map[string]features.MCPTool
}

func newMCPFixture(t *testing.T) *mcpFixture {
	t.Helper()
	_, db := newTestDB(t)
	f := NewFeature(slog.New(slog.NewTextHandler(io.Discard, nil)))
	f.posts = NewPostStore(db)
	tools := map[string]features.MCPTool{}
	for _, tool := range f.MCPTools() {
		tools[tool.Name] = tool
	}
	return &mcpFixture{t: t, f: f, posts: f.posts, tools: tools}
}

func (fx *mcpFixture) args(v map[string]any) json.RawMessage {
	fx.t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		fx.t.Fatal(err)
	}
	return b
}

func (fx *mcpFixture) read(name string, v map[string]any) string {
	fx.t.Helper()
	out, err := fx.tools[name].Read(context.Background(), features.AgentPrincipal{}, fx.args(v))
	if err != nil {
		fx.t.Fatalf("%s: %v", name, err)
	}
	b, _ := json.Marshal(out)
	return string(b)
}

func (fx *mcpFixture) prepare(name string, v map[string]any) (features.MCPPlan, error) {
	return fx.tools[name].Prepare(context.Background(), features.AgentPrincipal{}, fx.args(v))
}

func (fx *mcpFixture) apply(name string, plan features.MCPPlan) (any, error) {
	return fx.tools[name].Apply(context.Background(), features.AgentPrincipal{}, plan.Canonical, plan.State)
}

// run は、確認の文を作り、そのまま実行します。
func (fx *mcpFixture) run(name string, v map[string]any) (string, any) {
	fx.t.Helper()
	plan, err := fx.prepare(name, v)
	if err != nil {
		fx.t.Fatalf("%s prepare: %v", name, err)
	}
	out, err := fx.apply(name, plan)
	if err != nil {
		fx.t.Fatalf("%s apply: %v", name, err)
	}
	return plan.Summary, out
}

func (fx *mcpFixture) category(name string) int64 {
	fx.t.Helper()
	id, err := fx.posts.SaveCategory(context.Background(), 0, name)
	if err != nil {
		fx.t.Fatal(err)
	}
	return id
}

func (fx *mcpFixture) get(id int64) Post {
	fx.t.Helper()
	p, err := fx.posts.Get(context.Background(), id)
	if err != nil {
		fx.t.Fatal(err)
	}
	return p
}

func createdPostID(t *testing.T, out any) int64 {
	t.Helper()
	m, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("result: %#v", out)
	}
	id, ok := m["id"].(int64)
	if !ok {
		t.Fatalf("id: %#v", m["id"])
	}
	return id
}

// カテゴリの一覧のツールは、付けられるカテゴリの番号と名前を返し、読むだけで作らない。
// 記事の読み取りは、カテゴリの名前と番号を返す。
func TestMCP_CategoriesListAndRead(t *testing.T) {
	fx := newMCPFixture(t)
	if got := fx.read("news_categories", map[string]any{}); got != "[]" {
		t.Fatalf("empty list must be []: %s", got)
	}
	event, recruit := fx.category("イベント"), fx.category("採用")
	got := fx.read("news_categories", map[string]any{})
	want, _ := json.Marshal([]mcpCategory{{event, "イベント"}, {recruit, "採用"}})
	if got != string(want) {
		t.Fatalf("categories = %s, want %s", got, want)
	}
	if _, err := fx.tools["news_categories"].Read(context.Background(), features.AgentPrincipal{}, fx.args(map[string]any{"name": "新しい"})); err == nil {
		t.Fatal("news_categories must not accept arguments")
	}
	if fx.tools["news_categories"].Effect != features.MCPRead {
		t.Fatal("news_categories must be a read tool")
	}
	if cs, _ := fx.posts.Categories(context.Background()); len(cs) != 2 {
		t.Fatalf("reading must not create categories: %+v", cs)
	}

	_, out := fx.run("news_create", map[string]any{"title": "記事", "body": "", "category_id": event})
	id := createdPostID(t, out)
	for _, got := range []string{
		fx.read("news_get", map[string]any{"id": id}),
		fx.read("news_list", map[string]any{}),
	} {
		if !strings.Contains(got, `"category_id":`+itoa(event)) || !strings.Contains(got, `"category":"イベント"`) {
			t.Errorf("read must return the category id and name: %s", got)
		}
	}
	_, out = fx.run("news_create", map[string]any{"title": "無し", "body": ""})
	if got := fx.read("news_get", map[string]any{"id": createdPostID(t, out)}); strings.Contains(got, "category") {
		t.Errorf("a post without a category must omit it: %s", got)
	}
}

// 作成と編集でカテゴリを付けられ、0で外れる。確認の文には、作成ではカテゴリの名前、編集では変わるときだけ前と後を出す。
// 不変条件: NEWS-INV-15
func TestMCP_CategorySetAndClear(t *testing.T) {
	fx := newMCPFixture(t)
	event, recruit := fx.category("イベント"), fx.category("採用")

	summary, out := fx.run("news_create", map[string]any{"title": "記事", "body": "本文", "category_id": event})
	id := createdPostID(t, out)
	if p := fx.get(id); p.CategoryID != event || p.Published {
		t.Fatalf("create must attach the category to a draft: %+v", p)
	}
	if lines := strings.Split(summary, "\n"); !strings.Contains(lines[1], "カテゴリ: イベント") {
		t.Errorf("create summary must name the category in the target line: %q", summary)
	}
	summary, out = fx.run("news_create", map[string]any{"title": "零", "body": "", "category_id": 0})
	if p := fx.get(createdPostID(t, out)); p.CategoryID != 0 || !strings.Contains(summary, "カテゴリ: なし") {
		t.Fatalf("0 must mean no category: %+v %q", p, summary)
	}

	summary, _ = fx.run("news_update", map[string]any{"id": id, "title": "記事", "body": "本文", "category_id": recruit})
	if p := fx.get(id); p.CategoryID != recruit {
		t.Fatalf("update must change the category: %+v", p)
	}
	if lines := strings.Split(summary, "\n"); lines[2] != "変更: カテゴリ" || !strings.Contains(summary, "カテゴリ: イベント → 採用") {
		t.Errorf("update summary must show before and after: %q", summary)
	}
	summary, _ = fx.run("news_update", map[string]any{"id": id, "title": "題を変えた", "body": "本文", "category_id": recruit})
	if strings.Contains(summary, "カテゴリ") {
		t.Errorf("an unchanged category must not appear in the summary: %q", summary)
	}
	summary, _ = fx.run("news_update", map[string]any{"id": id, "title": "題を変えた", "body": "本文", "category_id": 0})
	if p := fx.get(id); p.CategoryID != 0 || p.Category != "" {
		t.Fatalf("0 must clear the category: %+v", p)
	}
	if !strings.Contains(summary, "カテゴリ: 採用 → なし") {
		t.Errorf("clearing must show the change: %q", summary)
	}
}

// 編集で category_id を省いたら、付いているカテゴリを変えない。前の版のクライアントが本文だけを直しても消えない。
// 不変条件: NEWS-INV-17
func TestMCP_UpdateWithoutCategoryKeepsIt(t *testing.T) {
	fx := newMCPFixture(t)
	event := fx.category("イベント")
	_, out := fx.run("news_create", map[string]any{"title": "記事", "body": "", "category_id": event})
	id := createdPostID(t, out)
	summary, _ := fx.run("news_update", map[string]any{"id": id, "title": "直した", "body": "本文を足した"})
	p := fx.get(id)
	if p.CategoryID != event || p.Title != "直した" {
		t.Fatalf("omitting category_id must keep the category: %+v", p)
	}
	if strings.Contains(summary, "カテゴリ") {
		t.Errorf("an omitted category must not appear in the summary: %q", summary)
	}
}

// 無い番号・負の番号のカテゴリは、確認の前に断り、何も保存しない。確認の後にカテゴリが消されても保存しない。
// 不変条件: NEWS-INV-15
func TestMCP_UnknownCategoryRejected(t *testing.T) {
	fx := newMCPFixture(t)
	ctx := context.Background()
	_, out := fx.run("news_create", map[string]any{"title": "記事", "body": ""})
	id := createdPostID(t, out)
	for _, c := range []struct {
		tool string
		args map[string]any
	}{
		{"news_create", map[string]any{"title": "新", "body": "", "category_id": 999999}},
		{"news_create", map[string]any{"title": "新", "body": "", "category_id": -1}},
		{"news_update", map[string]any{"id": id, "title": "記事", "body": "", "category_id": 999999}},
		{"news_update", map[string]any{"id": id, "title": "記事", "body": "", "category_id": -1}},
	} {
		var invalid features.MCPInputError
		if _, err := fx.prepare(c.tool, c.args); !errors.As(err, &invalid) {
			t.Errorf("%s %v must be rejected before the confirmation: %v", c.tool, c.args, err)
		}
	}

	// 確認の後に、付けるカテゴリが消された
	gone := fx.category("消える")
	create, err := fx.prepare("news_create", map[string]any{"title": "新", "body": "", "category_id": gone})
	if err != nil {
		t.Fatal(err)
	}
	update, err := fx.prepare("news_update", map[string]any{"id": id, "title": "記事", "body": "", "category_id": gone})
	if err != nil {
		t.Fatal(err)
	}
	if err := fx.posts.DeleteCategory(ctx, gone); err != nil {
		t.Fatal(err)
	}
	var invalid features.MCPInputError
	if _, err := fx.apply("news_create", create); !errors.As(err, &invalid) {
		t.Errorf("create with a deleted category must fail: %v", err)
	}
	if _, err := fx.apply("news_update", update); !errors.As(err, &invalid) {
		t.Errorf("update with a deleted category must fail: %v", err)
	}
	if list, _ := fx.posts.ListAll(ctx, 50, 0); len(list) != 1 || list[0].CategoryID != 0 {
		t.Fatalf("nothing must be saved: %+v", list)
	}
}

// 確認の後に記事のカテゴリが変わったら、編集・公開は保存しない。省いた編集も同じ。
// 不変条件: MCP-INV-4, NEWS-INV-17
func TestMCP_CategoryChangedAfterConfirmation(t *testing.T) {
	fx := newMCPFixture(t)
	ctx := context.Background()
	event, recruit := fx.category("イベント"), fx.category("採用")
	_, out := fx.run("news_create", map[string]any{"title": "記事", "body": "", "category_id": event})
	id := createdPostID(t, out)
	plans := map[string]features.MCPPlan{}
	for name, args := range map[string]map[string]any{
		"news_update":  {"id": id, "title": "MCP", "body": "", "category_id": event},
		"news_publish": {"id": id},
	} {
		plan, err := fx.prepare(name, args)
		if err != nil {
			t.Fatal(err)
		}
		plans[name] = plan
	}
	omitted, err := fx.prepare("news_update", map[string]any{"id": id, "title": "MCP", "body": ""})
	if err != nil {
		t.Fatal(err)
	}
	// 管理画面でカテゴリだけを変えた
	if err := fx.posts.UpdateWithCategory(ctx, id, "記事", "", time.Time{}, recruit); err != nil {
		t.Fatal(err)
	}
	for name, plan := range plans {
		if _, err := fx.apply(name, plan); !errors.Is(err, features.ErrMCPConflict) {
			t.Errorf("%s must conflict: %v", name, err)
		}
	}
	if _, err := fx.apply("news_update", omitted); !errors.Is(err, features.ErrMCPConflict) {
		t.Errorf("update without category_id must conflict: %v", err)
	}
	if p := fx.get(id); p.Title != "記事" || p.CategoryID != recruit || p.Published {
		t.Fatalf("the admin change must remain: %+v", p)
	}
	// 付いているカテゴリが消された（記事はカテゴリなしになる）場合も、省いた編集は保存しない
	fresh, err := fx.prepare("news_update", map[string]any{"id": id, "title": "MCP", "body": ""})
	if err != nil {
		t.Fatal(err)
	}
	if err := fx.posts.DeleteCategory(ctx, recruit); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.apply("news_update", fresh); !errors.Is(err, features.ErrMCPConflict) {
		t.Errorf("update after the category was deleted must conflict: %v", err)
	}
}
