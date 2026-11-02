package catalog

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"minimal-web-kit/features"
	"minimal-web-kit/features/auth/authtest"
)

// 予約の項目は、公開日時の1秒前まで公開の全部の経路に出ず、公開日時ちょうどから出ることを確かめる。
// 分類と画像は、予約の項目だけが使うものにする（他の公開項目が使うと、判定が無くても出てしまう）。
// 不変条件: CATALOG-INV-1, MEDIA-INV-1
// ユースケース: UC-CATALOG-4
func TestSchedule_PublicBoundary(t *testing.T) {
	fx := setup(t, true)
	s := fx.f.store
	clock := time.Date(2026, 11, 2, 9, 0, 0, 0, time.Local)
	s.now = func() time.Time { return clock }
	ctx := context.Background()
	cat := classification(t, s, "categories", "予約だけの分類")
	tag := classification(t, s, "tags", "予約だけのタグ")
	at := clock.Add(time.Hour)
	id := mustSave(t, s, Input{Title: "予約の項目", Body: "本文", CategoryID: cat, TagIDs: []int64{tag}, PublishAt: at})
	mustPublish(t, s, id)
	cover := strings.Repeat("c", 32) + ".jpg"
	if _, err := s.db.Exec("UPDATE catalog_items SET cover_file=? WHERE id=?", cover, id); err != nil {
		t.Fatal(err)
	}
	// 比べるために、公開日時の無い公開項目と、下書きを置く
	now := mustSave(t, s, Input{Title: "すぐ公開の項目"})
	mustPublish(t, s, now)
	mustSave(t, s, Input{Title: "下書きの項目"})

	path := "/catalog/" + strconv.FormatInt(id, 10)
	visible := func() map[string]bool {
		got := map[string]bool{}
		got["詳細"] = fx.send(http.MethodGet, path, nil, nil, nil).Code == http.StatusOK
		got["一覧"] = strings.Contains(fx.send(http.MethodGet, "/catalog", nil, nil, nil).Body.String(), "予約の項目")
		got["断片"] = strings.Contains(fx.send(http.MethodGet, "/catalog", nil, nil, map[string]string{"HX-Request": "true", "HX-Target": "catalog-results"}).Body.String(), "予約の項目")
		got["履歴復元"] = strings.Contains(fx.send(http.MethodGet, "/catalog", nil, nil, map[string]string{"HX-Request": "true", "HX-History-Restore-Request": "true"}).Body.String(), "予約の項目")
		for _, kind := range []string{"categories", "tags"} {
			list, err := s.Classifications(ctx, kind, true)
			if err != nil {
				t.Fatal(err)
			}
			for _, c := range list {
				got[kind] = got[kind] || c.ID == cat || c.ID == tag
			}
		}
		entries, err := fx.f.SitemapEntries(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			got["sitemap"] = got["sitemap"] || e.Path == path
		}
		public, err := fx.f.MediaIsPublic(ctx, strconv.FormatInt(id, 10), cover)
		if err != nil {
			t.Fatal(err)
		}
		got["画像"] = public
		return got
	}
	for _, c := range []struct {
		at   time.Time
		want bool
	}{{clock, false}, {at.Add(-time.Second), false}, {at, true}, {at.Add(time.Hour), true}} {
		clock = c.at
		for name, shown := range visible() {
			if shown != c.want {
				t.Errorf("%s at %s: shown=%v, want %v", name, c.at.Format("15:04:05"), shown, c.want)
			}
		}
	}
	if !strings.Contains(fx.send(http.MethodGet, "/catalog", nil, nil, nil).Body.String(), "すぐ公開の項目") ||
		strings.Contains(fx.send(http.MethodGet, "/catalog", nil, nil, nil).Body.String(), "下書きの項目") {
		t.Error("items without a date must follow the status")
	}
}

// 公開日時の保存の決まり（空欄で変えない、公開中を未来にしない、非公開で消す）を確かめる。
// 不変条件: CATALOG-INV-13
func TestSchedule_SaveRules(t *testing.T) {
	fx := setup(t, false)
	s := fx.f.store
	clock := time.Date(2026, 11, 2, 9, 0, 0, 0, time.Local)
	s.now = func() time.Time { return clock }
	ctx := context.Background()
	future := clock.Add(24 * time.Hour)
	get := func(id int64) Item {
		item, err := s.Get(ctx, id, false)
		if err != nil {
			t.Fatal(err)
		}
		return item
	}

	live := mustSave(t, s, Input{Title: "公開中"})
	mustPublish(t, s, live)
	if _, err := s.Save(ctx, live, Input{Title: "公開中", PublishAt: future}); !errors.Is(err, InputError{"publish_at_live"}) {
		t.Errorf("future date on a live item: %v", err)
	}
	if !get(live).PublishAt.IsZero() || !get(live).Live(clock) {
		t.Error("a refused save must not change the item")
	}
	if _, err := s.Save(ctx, live, Input{Title: "公開中", PublishAt: clock.Add(-time.Hour)}); err != nil {
		t.Errorf("past date on a live item: %v", err)
	}

	scheduled := mustSave(t, s, Input{Title: "予約", PublishAt: future})
	mustPublish(t, s, scheduled)
	if _, err := s.Save(ctx, scheduled, Input{Title: "予約の題を直す"}); err != nil || !get(scheduled).PublishAt.Equal(future) {
		t.Errorf("blank must keep the date: %v %v", err, get(scheduled).PublishAt)
	}
	later := future.Add(time.Hour)
	if _, err := s.Save(ctx, scheduled, Input{Title: "予約", PublishAt: later}); err != nil || !get(scheduled).PublishAt.Equal(later) {
		t.Errorf("a scheduled item can move its date: %v", err)
	}
	// 公開日時を過ぎると公開中になり、未来へ戻せない（判定は保存の時刻）
	clock = later
	if _, err := s.Save(ctx, scheduled, Input{Title: "予約", PublishAt: later.Add(time.Hour)}); !errors.Is(err, InputError{"publish_at_live"}) {
		t.Errorf("after the date the item is live: %v", err)
	}
	if err := s.SetPublished(ctx, scheduled, false); err != nil {
		t.Fatal(err)
	}
	if item := get(scheduled); item.Published || !item.PublishAt.IsZero() {
		t.Errorf("unpublish must clear the date: %+v", item.PublishAt)
	}

	// MCPの照合: 確認の後に日時だけが変わったら、公開も編集もしない
	draft := mustSave(t, s, Input{Title: "MCPの見本", PublishAt: clock.Add(time.Hour)})
	state := mcpState(get(draft))
	if _, err := s.Save(ctx, draft, Input{Title: "MCPの見本", PublishAt: clock.Add(2 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPublishedIfUnchanged(ctx, draft, true, state); !errors.Is(err, features.ErrMCPConflict) {
		t.Errorf("publish after a date change: %v", err)
	}
	if _, err := s.SaveIfUnchanged(ctx, draft, Input{Title: "MCPの見本"}, state); !errors.Is(err, features.ErrMCPConflict) {
		t.Errorf("update after a date change: %v", err)
	}
}

// 管理画面: 日時を保存してから公開すると「予約」の案内とボタンになり、取り消すと日時が消える。
// 不変条件: CATALOG-INV-13
// ユースケース: UC-CATALOG-4
func TestSchedule_AdminScreen(t *testing.T) {
	fx := setup(t, false)
	s := fx.f.store
	clock := time.Date(2026, 11, 2, 9, 0, 0, 0, time.Local)
	s.now = func() time.Time { return clock }
	cookie := authtest.NewSession(t, fx.core.DB())
	token := authtest.CSRFToken(t, fx.routes, "/admin/catalog/new", cookie)
	post := func(path string, values url.Values) *http.Response {
		values.Set("csrf_token", token)
		w := fx.send(http.MethodPost, path, values, cookie, map[string]string{"Origin": "null", "Sec-Fetch-Site": "same-origin"})
		return w.Result()
	}
	id := mustSave(t, s, Input{Title: "予約する項目"})
	edit := "/admin/catalog/" + strconv.FormatInt(id, 10)
	if res := post(edit, url.Values{"title": {"予約する項目"}, "publish_at": {"2026-11-03T10:30"}}); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("save: %d", res.StatusCode)
	}
	body := fx.send(http.MethodGet, edit, nil, cookie, nil).Body.String()
	if !strings.Contains(body, ">公開を予約する</button>") || !strings.Contains(body, `value="2026-11-03T10:30"`) {
		t.Error("the saved date must show the schedule button")
	}
	res := post(edit+"/publish", url.Values{})
	if loc := res.Header.Get("Location"); !strings.Contains(loc, "notice=scheduled") {
		t.Errorf("publish notice: %s", loc)
	}
	body = fx.send(http.MethodGet, edit, nil, cookie, nil).Body.String()
	if !strings.Contains(body, "公開予約（") || !strings.Contains(body, ">予約を取り消す（下書きに戻す）</button>") {
		t.Error("the edit screen must show the scheduled state")
	}
	if list := fx.send(http.MethodGet, "/admin/catalog", nil, cookie, nil).Body.String(); !strings.Contains(list, "公開予約（") {
		t.Error("the list must show the scheduled state")
	}
	if res := post(edit, url.Values{"title": {"予約する項目"}, "publish_at": {"2026-11-03"}}); res.StatusCode != http.StatusBadRequest {
		t.Errorf("bad date: %d", res.StatusCode)
	}
	// 予約中に過去の日時を保存すると、その保存で公開になり、案内で伝える
	res = post(edit, url.Values{"title": {"予約する項目"}, "publish_at": {"2026-11-02T08:00"}})
	if loc := res.Header.Get("Location"); !strings.Contains(loc, "notice=released") {
		t.Errorf("release notice: %s", loc)
	}
	if fx.send(http.MethodGet, "/catalog/"+strconv.FormatInt(id, 10), nil, nil, nil).Code != http.StatusOK {
		t.Error("a past date must publish the scheduled item")
	}
}

// 列の無い旧形式の表から起動して列が足され、既存の公開中の項目が公開のままであることを確かめる。
// 不変条件: CATALOG-INV-8
func TestSchedule_Migration(t *testing.T) {
	fx := setup(t, false)
	db := fx.core.DB()
	if _, err := db.Exec("ALTER TABLE catalog_items DROP COLUMN publish_at"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO catalog_items(title,body,status,created_at,updated_at) VALUES('旧の公開項目','', 'published',1,1)"); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := Migrate(db); err != nil {
			t.Fatal(err)
		}
	}
	items, err := fx.f.store.List(context.Background(), true, Filter{}, 10, 0)
	if err != nil || len(items) != 1 || items[0].Title != "旧の公開項目" {
		t.Fatalf("old published item must stay public: %v %v", items, err)
	}
}
