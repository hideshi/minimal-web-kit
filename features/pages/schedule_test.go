package pages

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"minimal-web-kit/features"
	"minimal-web-kit/internal/web/richtext"
)

// schedule は管理画面で公開日時を保存して、公開の操作をします。
func (fx *fixture) schedule(id int64, at string) {
	fx.t.Helper()
	p, err := fx.f.store.Get(context.Background(), id)
	if err != nil {
		fx.t.Fatal(err)
	}
	nav := ""
	if p.NavOrder != nil {
		nav = strconv.Itoa(*p.NavOrder)
	}
	edit := "/admin/pages/" + strconv.FormatInt(id, 10)
	if w := fx.post(edit, url.Values{"slug": {p.Slug}, "title": {p.Title}, "body": {p.Body}, "nav_order": {nav}, "publish_at": {at}}, fx.admin); w.Code != http.StatusSeeOther {
		fx.t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	if w := fx.post(edit+"/publish", nil, fx.admin); w.Code != http.StatusSeeOther || !strings.Contains(w.Header().Get("Location"), "notice=scheduled") {
		fx.t.Fatalf("publish: %d %s", w.Code, w.Header().Get("Location"))
	}
}

// 予約のページは、公開日時の1秒前まで公開ページ・ナビ・sitemap・画像に出ず、公開日時ちょうどから出る。
// ナビは、その間に作り直しの処理（Purge・afterChange）を呼ばなくても載る。
// 不変条件: PAGES-INV-1, PAGES-INV-6, MEDIA-INV-1
// ユースケース: UC-PAGES-4
func TestSchedule_PublicBoundary(t *testing.T) {
	fx := setup(t)
	ctx := context.Background()
	image := strings.Repeat("d", 32) + ".jpg"
	id := fx.create("launch", "募集開始", "本文\n\n"+richtext.ImageLine("募集の写真", image), "5", false)
	fx.schedule(id, "2026-10-30T11:00")
	at := time.Date(2026, 10, 30, 11, 0, 0, 0, time.Local)

	visible := func() map[string]bool {
		got := map[string]bool{}
		got["公開ページ"] = fx.get("/launch", nil).Code == http.StatusOK
		got["ナビ"] = strings.Contains(fx.get("/privacy", nil).Body.String(), `href="/launch"`)
		entries, err := fx.f.SitemapEntries(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			got["sitemap"] = got["sitemap"] || e.Path == "/launch"
		}
		public, err := fx.f.store.publishedUses(ctx, id, image)
		if err != nil {
			t.Fatal(err)
		}
		got["画像"] = public
		return got
	}
	for _, c := range []struct {
		at   time.Time
		want bool
	}{{*fx.clock, false}, {at.Add(-time.Second), false}, {at, true}, {at.Add(time.Hour), true}} {
		*fx.clock = c.at
		for name, shown := range visible() {
			if shown != c.want {
				t.Errorf("%s at %s: shown=%v, want %v", name, c.at.Format("15:04:05"), shown, c.want)
			}
		}
	}
}

// 公開日時の保存の決まりと、MCPの照合に公開日時が入ることを確かめる。
// 不変条件: PAGES-INV-11
func TestSchedule_SaveRules(t *testing.T) {
	fx := setup(t)
	ctx := context.Background()
	s := fx.f.store
	future := fx.clock.Add(24 * time.Hour).Format("2006-01-02T15:04")
	live := fx.create("live-page", "公開中", "本文", "", true)
	w := fx.post("/admin/pages/"+strconv.FormatInt(live, 10), url.Values{"slug": {"live-page"}, "title": {"公開中"}, "body": {"本文"}, "publish_at": {future}}, fx.admin)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "先に「非公開（下書き）に戻す」") {
		t.Errorf("future date on a live page: %d", w.Code)
	}
	if fx.get("/live-page", nil).Code != http.StatusOK {
		t.Error("a refused save must keep the page public")
	}

	id := fx.create("later-page", "予約", "本文", "", false)
	fx.schedule(id, future)
	if _, err := s.Save(ctx, id, Input{Slug: "later-page", Title: "題を直す", Body: "本文"}, nil); err != nil {
		t.Fatal(err)
	}
	p, _ := s.Get(ctx, id)
	if p.PublishAt.IsZero() || !p.Scheduled(*fx.clock) {
		t.Error("blank must keep the date")
	}
	if w := fx.post("/admin/pages/"+strconv.FormatInt(id, 10)+"/unpublish", nil, fx.admin); w.Code != http.StatusSeeOther {
		t.Fatal(w.Code)
	}
	if p, _ := s.Get(ctx, id); p.Published || !p.PublishAt.IsZero() {
		t.Error("unpublish must clear the date")
	}

	// MCPの照合: 読んだ後に日時だけが変わったら、公開も編集もしない
	cur, _ := s.Get(ctx, id)
	if _, err := s.Save(ctx, id, Input{Slug: "later-page", Title: "題を直す", Body: "本文", PublishAt: future}, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPublishedIfUnchanged(ctx, cur, true); !errors.Is(err, features.ErrMCPConflict) {
		t.Errorf("publish after a date change: %v", err)
	}
	if err := s.SaveIfUnchanged(ctx, cur, Input{Slug: "later-page", Title: "別の題", Body: "本文"}, nil); !errors.Is(err, features.ErrMCPConflict) {
		t.Errorf("update after a date change: %v", err)
	}
	cur, _ = s.Get(ctx, id)
	if err := s.SetPublishedIfUnchanged(ctx, cur, true); err != nil {
		t.Fatal(err)
	}
	cur, _ = s.Get(ctx, id)
	if err := s.SetPublishedIfUnchanged(ctx, cur, false); err != nil {
		t.Fatal(err)
	}
	if p, _ := s.Get(ctx, id); !p.PublishAt.IsZero() {
		t.Error("MCP unpublish must clear the date")
	}
}

// ナビの作り直しが重なっても、古い読み取りの結果が新しい結果を上書きしないことを確かめる。
// 定期の作り直し（Purge）がDBを読んだ直後に止まっている間に、ページを非公開にする。
// 直列にしていないと、非公開の後の作り直しが先に終わり、止めていた古い結果（公開中）が最後に残る。
// 不変条件: PAGES-INV-6
func TestSchedule_NavRebuildsAreSerialized(t *testing.T) {
	fx := setup(t)
	ctx := context.Background()
	id := fx.create("toggle", "切り替え", "本文", "1", true)
	read, release := make(chan struct{}), make(chan struct{})
	// 最初の作り直しだけを止める（sync.Once だと、2回目の呼び出しも1回目の終わりを待ってしまう）
	var first atomic.Bool
	fx.f.afterNavRead = func() {
		if first.CompareAndSwap(false, true) {
			close(read)
			<-release
		}
	}
	purged := make(chan error, 1)
	go func() { purged <- fx.f.Purge(ctx) }()
	<-read
	unpublished := make(chan error, 1)
	go func() { unpublished <- fx.f.setPublished(ctx, id, false) }()
	// 直列でなければ、この間に非公開とその後の作り直しが終わる
	select {
	case err := <-unpublished:
		unpublished <- err
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	if err := <-purged; err != nil {
		t.Fatal(err)
	}
	if err := <-unpublished; err != nil {
		t.Fatal(err)
	}
	if links := fx.f.DynamicNavLinks(); len(links) != 0 {
		t.Errorf("a stale rebuild left %v in the nav", links)
	}
}

// 列の無い旧形式の表から起動して列が足され、既存の公開中のページとナビが変わらないことを確かめる。
// 不変条件: PAGES-INV-7
func TestSchedule_Migration(t *testing.T) {
	fx := setup(t)
	db := fx.core.DB()
	if _, err := db.Exec("ALTER TABLE pages DROP COLUMN publish_at"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO pages (slug, title, body, status, nav_order, created_at, updated_at) VALUES ('old-page', '旧のページ', '', 'published', 3, 1, 1)"); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := fx.f.Init(db); err != nil {
			t.Fatal(err)
		}
	}
	if fx.get("/old-page", nil).Code != http.StatusOK || !strings.Contains(fx.get("/privacy", nil).Body.String(), `href="/old-page"`) {
		t.Error("old published page must stay public and in the nav")
	}
}

// 管理画面のボタンの文言と、予約中に過去の日時を保存したときの案内を確かめる。
// 不変条件: PAGES-INV-11
// ユースケース: UC-PAGES-4
func TestSchedule_AdminScreen(t *testing.T) {
	fx := setup(t)
	id := fx.create("event", "催し", "本文", "", false)
	edit := "/admin/pages/" + strconv.FormatInt(id, 10)
	if w := fx.post(edit, url.Values{"slug": {"event"}, "title": {"催し"}, "body": {"本文"}, "publish_at": {"2026-10-31T09:00"}}, fx.admin); w.Code != http.StatusSeeOther {
		t.Fatal(w.Code)
	}
	if body := fx.get(edit, fx.admin).Body.String(); !strings.Contains(body, ">公開を予約する</button>") {
		t.Error("the saved date must show the schedule button")
	}
	if w := fx.post(edit+"/publish", nil, fx.admin); !strings.Contains(w.Header().Get("Location"), "notice=scheduled") {
		t.Fatal(w.Header().Get("Location"))
	}
	body := fx.get(edit, fx.admin).Body.String()
	if !strings.Contains(body, "公開予約（") || !strings.Contains(body, ">予約を取り消す（下書きに戻す）</button>") {
		t.Error("the edit screen must show the scheduled state")
	}
	if !strings.Contains(fx.get("/admin/pages", fx.admin).Body.String(), "公開予約（") {
		t.Error("the list must show the scheduled state")
	}
	w := fx.post(edit, url.Values{"slug": {"event"}, "title": {"催し"}, "body": {"本文"}, "publish_at": {"2026-10-30T09:00"}}, fx.admin)
	if !strings.Contains(w.Header().Get("Location"), "notice=released") || fx.get("/event", nil).Code != http.StatusOK {
		t.Errorf("a past date must publish and say so: %s", w.Header().Get("Location"))
	}
}

// MCPのツールを通して、確認の後に公開日時だけが変わったら、編集も公開もしないことを確かめる。
// 予約中に過去の日時を渡すと、確認の文に警告が出ることも確かめる。
// 不変条件: PAGES-INV-11, MCP-INV-4
func TestSchedule_MCPChecksTheDate(t *testing.T) {
	fx := setup(t)
	ctx := context.Background()
	id := fx.create("mcp-page", "MCPの見本", "本文", "", false)
	fx.schedule(id, "2026-10-31T09:00")
	tools := map[string]features.MCPTool{}
	for _, tool := range fx.f.MCPTools() {
		tools[tool.Name] = tool
	}
	var who features.AgentPrincipal
	moveDate := func(at string) {
		if _, err := fx.f.store.Save(ctx, id, Input{Slug: "mcp-page", Title: "MCPの見本", Body: "本文", PublishAt: at}, nil); err != nil {
			t.Fatal(err)
		}
	}
	args := func(v string) []byte { return []byte(v) }

	plan, err := tools["pages_update"].Prepare(ctx, who, args(`{"id":`+strconv.FormatInt(id, 10)+`,"slug":"mcp-page","title":"新しい題","body":"本文","nav_order":null}`))
	if err != nil {
		t.Fatal(err)
	}
	moveDate("2026-11-01T09:00")
	if _, err := tools["pages_update"].Apply(ctx, who, plan.Canonical, plan.State); !errors.Is(err, features.ErrMCPConflict) {
		t.Errorf("update after a date change: %v", err)
	}

	plan, err = tools["pages_unpublish"].Prepare(ctx, who, args(`{"id":`+strconv.FormatInt(id, 10)+`}`))
	if err != nil {
		t.Fatal(err)
	}
	moveDate("2026-11-02T09:00")
	if _, err := tools["pages_unpublish"].Apply(ctx, who, plan.Canonical, plan.State); !errors.Is(err, features.ErrMCPConflict) {
		t.Errorf("unpublish after a date change: %v", err)
	}

	plan, err = tools["pages_update"].Prepare(ctx, who, args(`{"id":`+strconv.FormatInt(id, 10)+`,"slug":"mcp-page","title":"MCPの見本","body":"本文","nav_order":null,"publish_at":"2026-10-30T09:00"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.Summary, "直ちに公開ページに出ます") {
		t.Errorf("summary must warn: %s", plan.Summary)
	}
}
