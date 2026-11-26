package news

import (
	"context"
	"testing"
	"time"
)

// TestLatestPublished_SameRulesAsPublicList は、固定ページの本文のお知らせの一覧に渡す記事が、公開の一覧と同じ条件と並びで、
// 公開の記事の無いカテゴリと無い名前のカテゴリでは一覧のパス（カテゴリの番号）を返さないことを確かめる。
// 不変条件: PAGES-INV-18, NEWS-INV-16
func TestLatestPublished_SameRulesAsPublicList(t *testing.T) {
	_, db := newTestDB(t)
	posts := NewPostStore(db)
	ctx := context.Background()
	clock := time.Unix(1_800_000_000, 0)
	posts.now = func() time.Time { return clock }
	event, err := posts.SaveCategory(ctx, 0, "イベント")
	if err != nil {
		t.Fatal(err)
	}
	hidden, err := posts.SaveCategory(ctx, 0, "下書きだけ")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := posts.SaveCategory(ctx, 0, "空"); err != nil {
		t.Fatal(err)
	}
	publish := func(title string, at time.Time, category int64) int64 {
		t.Helper()
		id, err := posts.CreateWithCover(ctx, title, "", at, "", category)
		if err != nil {
			t.Fatal(err)
		}
		if err := posts.Publish(ctx, id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	old := publish("古い", clock.Add(-2*time.Hour), event)
	newer := publish("新しい", clock.Add(-time.Hour), event)
	publish("ほか", clock.Add(-30*time.Minute), 0)
	publish("予約", clock.Add(time.Hour), event)
	if _, err := posts.CreateWithCover(ctx, "下書き", "", time.Time{}, "", hidden); err != nil {
		t.Fatal(err)
	}

	got, err := posts.LatestPublished(ctx, "イベント", 5)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Found || len(got.Items) != 2 || got.Items[0].Href != "/news/"+itoa(newer) || got.Items[1].Href != "/news/"+itoa(old) {
		t.Fatalf("category: %+v", got)
	}
	if got.ListPath != "/news?category="+itoa(event) {
		t.Errorf("list path: %q", got.ListPath)
	}
	if all, err := posts.LatestPublished(ctx, "", 2); err != nil || len(all.Items) != 2 || all.Items[0].Title != "ほか" || all.ListPath != "/news" {
		t.Errorf("all: %+v %v", all, err)
	}
	for _, name := range []string{"下書きだけ", "空"} {
		if got, err := posts.LatestPublished(ctx, name, 3); err != nil || !got.Found || len(got.Items) != 0 || got.ListPath != "" {
			t.Errorf("%s: %+v %v", name, got, err)
		}
	}
	if got, err := posts.LatestPublished(ctx, "無い", 3); err != nil || got.Found || got.ListPath != "" {
		t.Errorf("missing: %+v %v", got, err)
	}
	if got, err := posts.LatestPublished(ctx, "イベント ", 3); err != nil || got.Found {
		t.Errorf("names must match exactly: %+v %v", got, err)
	}
	for _, n := range []int{0, 11} {
		if _, err := posts.LatestPublished(ctx, "", n); err == nil {
			t.Errorf("count %d must be rejected", n)
		}
	}
}
