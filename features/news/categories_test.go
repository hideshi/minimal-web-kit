package news

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
)

func itoa(id int64) string { return strconv.FormatInt(id, 10) }

// TestCategories_NameRules は、カテゴリの名前の規則と、改名で記事の更新日時を変えないことを確かめる。
// 不変条件: NEWS-INV-14
func TestCategories_NameRules(t *testing.T) {
	_, db := newTestDB(t)
	posts := NewPostStore(db)
	ctx := context.Background()
	id, err := posts.SaveCategory(ctx, 0, "  イベント  ")
	if err != nil {
		t.Fatal(err)
	}
	if c, err := posts.Category(ctx, id); err != nil || c.Name != "イベント" {
		t.Fatalf("name must be trimmed: %+v %v", c, err)
	}
	for _, bad := range []string{"", " ", "a\nb", "a\tb", strings.Repeat("あ", 51)} {
		var invalid InputError
		if _, err := posts.SaveCategory(ctx, 0, bad); !errors.As(err, &invalid) || invalid.Field != "name" {
			t.Errorf("%q must be rejected: %v", bad, err)
		}
	}
	if _, err := posts.SaveCategory(ctx, 0, strings.Repeat("あ", 50)); err != nil {
		t.Errorf("50 runes must be accepted: %v", err)
	}
	other, err := posts.SaveCategory(ctx, 0, "採用")
	if err != nil {
		t.Fatal(err)
	}
	var invalid InputError
	if _, err := posts.SaveCategory(ctx, 0, "イベント"); !errors.As(err, &invalid) || invalid.Field != "name_taken" {
		t.Errorf("duplicate on create: %v", err)
	}
	if _, err := posts.SaveCategory(ctx, other, "イベント"); !errors.As(err, &invalid) || invalid.Field != "name_taken" {
		t.Errorf("duplicate on rename: %v", err)
	}
	if _, err := posts.SaveCategory(ctx, id, "イベント"); err != nil {
		t.Errorf("saving the same name must be allowed: %v", err)
	}
	if _, err := posts.SaveCategory(ctx, 999999, "無い"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing category: %v", err)
	}

	clock := time.Unix(1_800_000_000, 0)
	posts.now = func() time.Time { return clock }
	post, err := posts.CreateWithCover(ctx, "記事", "", time.Time{}, "", id)
	if err != nil {
		t.Fatal(err)
	}
	posts.now = func() time.Time { return clock.Add(time.Hour) }
	if _, err := posts.SaveCategory(ctx, id, "催し"); err != nil {
		t.Fatal(err)
	}
	p, err := posts.Get(ctx, post)
	if err != nil || p.Category != "催し" || !p.UpdatedAt.Equal(clock.UTC()) {
		t.Fatalf("rename must not touch posts: %+v %v", p, err)
	}
}

// TestCategories_PostsKeepOnlyExistingCategories は、無いカテゴリを付けられないことと、カテゴリを消しても記事が残ることを確かめる。
// 不変条件: NEWS-INV-15
func TestCategories_PostsKeepOnlyExistingCategories(t *testing.T) {
	_, db := newTestDB(t)
	posts := NewPostStore(db)
	ctx := context.Background()
	cat, err := posts.SaveCategory(ctx, 0, "イベント")
	if err != nil {
		t.Fatal(err)
	}
	var invalid InputError
	if _, err := posts.CreateWithCover(ctx, "記事", "", time.Time{}, "", 999999); !errors.As(err, &invalid) || invalid.Field != "category" {
		t.Fatalf("missing category on create: %v", err)
	}
	assertRows(t, db, "SELECT COUNT(*) FROM news_posts", 0)
	id, err := posts.CreateWithCover(ctx, "記事", "", time.Time{}, "", cat)
	if err != nil {
		t.Fatal(err)
	}
	for _, update := range []func(int64) error{
		func(c int64) error { return posts.UpdateWithCover(ctx, id, "記事", "", time.Time{}, "", c) },
		func(c int64) error { return posts.UpdateWithCategory(ctx, id, "記事", "", time.Time{}, c) },
	} {
		if err := update(-1); !errors.As(err, &invalid) || invalid.Field != "category" {
			t.Errorf("negative category: %v", err)
		}
		if err := update(999999); !errors.As(err, &invalid) || invalid.Field != "category" {
			t.Errorf("missing category on update: %v", err)
		}
	}
	if p, _ := posts.Get(ctx, id); p.CategoryID != cat {
		t.Fatalf("failed updates must keep the category: %+v", p)
	}

	clock := time.Unix(1_800_000_000, 0)
	mustExec(t, db, "UPDATE news_posts SET updated_at = ? WHERE id = ?", clock.Unix(), id)
	if err := posts.DeleteCategory(ctx, cat); err != nil {
		t.Fatal(err)
	}
	p, err := posts.Get(ctx, id)
	if err != nil || p.CategoryID != 0 || p.Category != "" || !p.UpdatedAt.Equal(clock.UTC()) {
		t.Fatalf("post must remain without category: %+v %v", p, err)
	}
	if err := posts.DeleteCategory(ctx, cat); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
	// 外部キーでも、SQLで直接消したカテゴリは記事から外れる
	other, _ := posts.SaveCategory(ctx, 0, "採用")
	if err := posts.UpdateWithCategory(ctx, id, "記事", "", time.Time{}, other); err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, "DELETE FROM news_categories WHERE id = ?", other)
	if p, _ := posts.Get(ctx, id); p.CategoryID != 0 {
		t.Fatalf("ON DELETE SET NULL: %+v", p)
	}
}

// TestMigrate_AddsCategoryColumn は、カテゴリの列が無い既存のDBに、外部キー付きで列を足すことを確かめる。
// 不変条件: NEWS-INV-10, NEWS-INV-15
func TestMigrate_AddsCategoryColumn(t *testing.T) {
	_, db := newTestDB(t)
	mustExec(t, db, "DROP TABLE news_posts")
	mustExec(t, db, `CREATE TABLE news_posts (
		id INTEGER PRIMARY KEY AUTOINCREMENT, title TEXT NOT NULL, body TEXT NOT NULL,
		status TEXT NOT NULL CHECK (status IN ('draft', 'published')), published_at INTEGER,
		created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, cover_file TEXT)`)
	mustExec(t, db, "INSERT INTO news_posts (title, body, status, created_at, updated_at) VALUES ('残す', '', 'draft', 1, 1)")
	for range 2 {
		if err := Migrate(db); err != nil {
			t.Fatal(err)
		}
	}
	assertRows(t, db, "SELECT COUNT(*) FROM news_posts WHERE title = '残す' AND category_id IS NULL", 1)
	assertRows(t, db, "SELECT COUNT(*) FROM pragma_foreign_key_list('news_posts') WHERE \"table\" = 'news_categories' AND \"from\" = 'category_id' AND on_delete = 'SET NULL'", 1)
}

// TestCategories_MCPConflict は、読んだ後にカテゴリが変わった記事を、MCPの条件付きの保存が変えないことを確かめる。
// 不変条件: NEWS-INV-15
func TestCategories_MCPConflict(t *testing.T) {
	_, db := newTestDB(t)
	posts := NewPostStore(db)
	ctx := context.Background()
	cat, _ := posts.SaveCategory(ctx, 0, "イベント")
	id, _ := posts.CreateWithCover(ctx, "記事", "", time.Time{}, "", 0)
	read, _ := posts.Get(ctx, id)
	before := mcpState(read)
	if err := posts.UpdateWithCategory(ctx, id, "記事", "", time.Time{}, cat); err != nil {
		t.Fatal(err)
	}
	now, _ := posts.Get(ctx, id)
	if mcpState(now) == before {
		t.Error("state hash must include the category")
	}
	if err := posts.UpdateIfUnchanged(ctx, read, "変えた", "", time.Time{}); !errors.Is(err, features.ErrMCPConflict) {
		t.Errorf("update must conflict: %v", err)
	}
	if err := posts.SetPublishedIfUnchanged(ctx, read, true); !errors.Is(err, features.ErrMCPConflict) {
		t.Errorf("publish must conflict: %v", err)
	}
	if err := posts.UpdateIfUnchanged(ctx, now, "変えた", "", time.Time{}); err != nil {
		t.Fatalf("fresh read must succeed: %v", err)
	}
	if p, _ := posts.Get(ctx, id); p.CategoryID != cat {
		t.Fatalf("MCP must keep the category: %+v", p)
	}
}

// TestCategories_PublicFilter は、公開の一覧の絞り込みの候補と結果が、公開の条件に従うことを確かめる。
// 不変条件: NEWS-INV-1, NEWS-INV-16
// ユースケース: UC-NEWS-8
func TestCategories_PublicFilter(t *testing.T) {
	fx := newPublicFixture(t, "")
	ctx := context.Background()
	event, _ := fx.posts.SaveCategory(ctx, 0, "イベント")
	hiring, _ := fx.posts.SaveCategory(ctx, 0, "採用")
	draftOnly, _ := fx.posts.SaveCategory(ctx, 0, "下書きだけ")
	scheduledOnly, _ := fx.posts.SaveCategory(ctx, 0, "予約だけ")
	unused, _ := fx.posts.SaveCategory(ctx, 0, "未使用")

	withCategory := func(title string, cat int64, at time.Time, publish bool) int64 {
		id, err := fx.posts.CreateWithCover(ctx, title, "", at, "", cat)
		if err != nil {
			t.Fatal(err)
		}
		if publish {
			if err := fx.posts.Publish(ctx, id); err != nil {
				t.Fatal(err)
			}
		}
		return id
	}
	for i := 0; i < newsPageSize+1; i++ {
		withCategory("イベントの記事"+strconv.Itoa(i), event, time.Time{}, true)
	}
	hiringPost := withCategory("採用の記事", hiring, time.Time{}, true)
	withCategory("下書きの記事", draftOnly, time.Time{}, false)
	withCategory("予約の記事", scheduledOnly, time.Now().Add(48*time.Hour), true)

	body := fx.do(t, http.MethodGet, "/news", nil).Body.String()
	for _, want := range []string{`<option value="` + itoa(event) + `">イベント</option>`, `<option value="` + itoa(hiring) + `">採用</option>`, "／カテゴリ: イベント"} {
		if !strings.Contains(body, want) {
			t.Errorf("/news must contain %q", want)
		}
	}
	for _, hidden := range []string{"下書きだけ", "予約だけ", "未使用"} {
		if strings.Contains(body, hidden) {
			t.Errorf("/news must not offer %s", hidden)
		}
	}

	rec := fx.do(t, http.MethodGet, "/news?category="+itoa(hiring), nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "採用の記事") || strings.Contains(rec.Body.String(), "イベントの記事") ||
		!strings.Contains(rec.Body.String(), `<option value="`+itoa(hiring)+`" selected>`) {
		t.Fatalf("filter by hiring: %d", rec.Code)
	}
	page1 := fx.do(t, http.MethodGet, "/news?category="+itoa(event), nil).Body.String()
	if strings.Count(page1, `<li class="card card-link">`) != newsPageSize || !strings.Contains(page1, `href="/news?category=`+itoa(event)+`&amp;page=2"`) {
		t.Fatal("pagination must keep the category")
	}
	page2 := fx.do(t, http.MethodGet, "/news?category="+itoa(event)+"&page=2", nil).Body.String()
	if strings.Count(page2, `<li class="card card-link">`) != 1 || strings.Contains(page2, "採用の記事") {
		t.Fatal("page 2 must show the remaining event post only")
	}
	for _, id := range []int64{draftOnly, scheduledOnly, unused, 999999} {
		b := fx.do(t, http.MethodGet, "/news?category="+itoa(id), nil).Body.String()
		if strings.Contains(b, "の記事") || strings.Contains(b, "下書きだけ") || strings.Contains(b, "予約だけ") {
			t.Errorf("category %d must show no posts", id)
		}
	}
	for _, target := range []string{"/news?category=abc", "/news?category=01", "/news?category=-1", "/news?category=" + itoa(event) + "&category=" + itoa(hiring)} {
		if rec := fx.do(t, http.MethodGet, target, nil); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", target, rec.Code)
		}
	}
	show := fx.do(t, http.MethodGet, "/news/"+itoa(hiringPost), nil).Body.String()
	if !strings.Contains(show, `／カテゴリ: <a href="/news?category=`+itoa(hiring)+`">採用</a>`) {
		t.Error("detail must link to the category")
	}
	api := fx.do(t, http.MethodGet, "/api/news/"+itoa(hiringPost), nil).Body.String()
	if strings.Contains(api, "採用\"") || strings.Contains(api, "category") {
		t.Errorf("API must not include the category (NEWS-INV-7): %s", api)
	}
}

// TestAdminCategories_Lifecycle は、管理画面でカテゴリを追加・改名・削除し、記事に付けられることを確かめる。
// 不変条件: NEWS-INV-9, NEWS-INV-12, NEWS-INV-14, NEWS-INV-15
// ユースケース: UC-NEWS-9
func TestAdminCategories_Lifecycle(t *testing.T) {
	fx := newPublicFixture(t, "")
	_, cookie := fx.loggedIn(t)
	ctx := context.Background()

	if rec := fx.do(t, http.MethodGet, "/admin/news-categories", nil); rec.Code == http.StatusOK {
		t.Fatal("categories page must require login")
	}
	if rec := fx.postForm(t, "/admin/news-categories", url.Values{"name": {"x"}}, "same-origin", cookie); rec.Code == http.StatusSeeOther {
		t.Fatal("creating must require CSRF")
	}
	assertRows(t, fx.db, "SELECT COUNT(*) FROM news_categories", 0)

	rec := fx.postForm(t, "/admin/news-categories", withCSRF(fx, cookie, url.Values{"name": {"イベント"}}), "same-origin", cookie)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("create: %d", rec.Code)
	}
	cats, _ := fx.posts.Categories(ctx)
	if len(cats) != 1 {
		t.Fatalf("categories: %+v", cats)
	}
	cat := cats[0].ID
	if rec := fx.postForm(t, "/admin/news-categories", withCSRF(fx, cookie, url.Values{"name": {"イベント"}}), "same-origin", cookie); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "同じ名前のカテゴリがあります") {
		t.Fatalf("duplicate: %d", rec.Code)
	}

	rec = fx.postForm(t, "/admin/news", withCSRF(fx, cookie, url.Values{"title": {"記事"}, "category": {itoa(cat)}}), "same-origin", cookie)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("create post: %d", rec.Code)
	}
	all, _ := fx.posts.ListAll(ctx, 10, 0)
	if len(all) != 1 || all[0].CategoryID != cat {
		t.Fatalf("post category: %+v", all)
	}
	post := all[0].ID
	if b := fx.do(t, http.MethodGet, "/admin/news/"+itoa(post), nil, cookie).Body.String(); !strings.Contains(b, `<option value="`+itoa(cat)+`" selected>イベント</option>`) {
		t.Fatal("edit page must select the category")
	}
	if b := fx.do(t, http.MethodGet, "/admin/news", nil, cookie).Body.String(); !strings.Contains(b, "<td>イベント</td>") || !strings.Contains(b, `href="/admin/news-categories"`) {
		t.Fatal("list must show the category and link to the categories page")
	}
	rec = fx.postForm(t, "/admin/news/"+itoa(post), withCSRF(fx, cookie, url.Values{"title": {"記事"}, "category": {"abc"}}), "same-origin", cookie)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "選んだカテゴリが見つかりません") {
		t.Fatalf("bad category: %d", rec.Code)
	}

	rec = fx.postForm(t, "/admin/news-categories/"+itoa(cat), withCSRF(fx, cookie, url.Values{"name": {"催し"}}), "same-origin", cookie)
	if c, _ := fx.posts.Category(ctx, cat); rec.Code != http.StatusSeeOther || c.Name != "催し" {
		t.Fatalf("rename: %d %+v", rec.Code, c)
	}
	if b := fx.do(t, http.MethodGet, "/admin/news-categories/"+itoa(cat)+"/delete", nil, cookie).Body.String(); !strings.Contains(b, "現在1件の記事で使っています") {
		t.Fatal("delete page must show the usage")
	}
	if rec := fx.do(t, http.MethodGet, "/admin/news-categories/"+itoa(cat)+"/delete", nil, cookie); rec.Code != http.StatusOK {
		t.Fatal("GET must not delete")
	}
	assertRows(t, fx.db, "SELECT COUNT(*) FROM news_categories", 1)
	rec = fx.postForm(t, "/admin/news-categories/"+itoa(cat)+"/delete", withCSRF(fx, cookie, nil), "same-origin", cookie)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("delete: %d", rec.Code)
	}
	assertRows(t, fx.db, "SELECT COUNT(*) FROM news_categories", 0)
	assertRows(t, fx.db, "SELECT COUNT(*) FROM news_posts WHERE category_id IS NULL", 1)
	for _, path := range []string{"/admin/news-categories/" + itoa(cat), "/admin/news-categories/999999/delete", "/admin/news-categories/01"} {
		if rec := fx.do(t, http.MethodGet, path, nil, cookie); rec.Code != http.StatusNotFound {
			t.Errorf("%s: %d, want 404", path, rec.Code)
		}
	}
	if strings.Contains(fx.logs.String(), "催し") || strings.Contains(fx.logs.String(), "イベント") {
		t.Error("logs must not contain category names (NEWS-INV-11)")
	}
}
