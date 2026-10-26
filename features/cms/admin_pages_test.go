package cms

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"minimal-web-kit/features/auth/authtest"
)

// loggedIn は管理者セッションを作り、そのCookieを返します（パスキーの操作は管理者認証のテストで確かめる）。
func (fx *publicFixture) loggedIn(t *testing.T) (any, *http.Cookie) {
	t.Helper()
	return nil, authtest.NewSession(t, fx.db)
}

// postForm は管理画面のフォーム送信を再現します。csrf が空なら csrf_token を送りません。
func (fx *publicFixture) postForm(t *testing.T, path string, form url.Values, site string, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if site != "" {
		req.Header.Set("Sec-Fetch-Site", site)
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	fx.routes.ServeHTTP(rec, req)
	return rec
}

func withCSRF(fx *publicFixture, cookie *http.Cookie, form url.Values) url.Values {
	if form == nil {
		form = url.Values{}
	}
	form.Set("csrf_token", authtest.CSRFToken(fx.t, fx.routes, "/admin/posts", cookie))
	return form
}

func TestAdminPosts_CreateEditPublishDelete(t *testing.T) {
	fx := newPublicFixture(t, "")
	_, cookie := fx.loggedIn(t)

	rec := fx.do(t, http.MethodGet, "/admin/posts/new", nil, cookie)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "<script") || !strings.Contains(rec.Header().Get("Content-Security-Policy"), "script-src 'none'") {
		t.Fatalf("new post page: %d", rec.Code)
	}

	// 作成すると下書きになり、編集画面へ移る
	title, body := "<b>秘密の見出し</b>", "本文の一行目\n\n段落"
	rec = fx.postForm(t, "/admin/posts", withCSRF(fx, cookie, url.Values{"title": {title}, "body": {body}}), "same-origin", cookie)
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/admin/posts/") {
		t.Fatalf("create: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	loc, _ := url.Parse(rec.Header().Get("Location"))
	id, _ := strconv.ParseInt(strings.TrimPrefix(loc.Path, "/admin/posts/"), 10, 64)
	path := "/admin/posts/" + strconv.FormatInt(id, 10)
	if rec := fx.do(t, http.MethodGet, "/news/"+strconv.FormatInt(id, 10), nil); rec.Code != http.StatusNotFound {
		t.Fatal("a new post must be a draft")
	}
	rec = fx.do(t, http.MethodGet, loc.String(), nil, cookie)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), adminNotices["created"]) {
		t.Fatalf("edit page: %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "<b>秘密") || !strings.Contains(rec.Body.String(), "&lt;b&gt;秘密") {
		t.Fatal("title must be escaped in the edit form")
	}

	// 一覧に下書きとして出る
	rec = fx.do(t, http.MethodGet, "/admin/posts?notice=bogus", nil, cookie)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "下書き") || strings.Contains(rec.Body.String(), "bogus") {
		t.Fatalf("list: %d", rec.Code)
	}

	// 不正な入力は400で、入力を残したフォームを返す
	rec = fx.postForm(t, path, withCSRF(fx, cookie, url.Values{"title": {""}, "body": {"書きかけの本文"}}), "same-origin", cookie)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "書きかけの本文") {
		t.Fatalf("invalid update: %d", rec.Code)
	}
	if p, _ := fx.posts.Get(context.Background(), id); p.Title != title {
		t.Fatal("invalid input must not be saved")
	}

	rec = fx.postForm(t, path, withCSRF(fx, cookie, url.Values{"title": {"新しい見出し"}, "body": {"新しい本文"}}), "same-origin", cookie)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != path+"?notice=saved" {
		t.Fatalf("update: %d %s", rec.Code, rec.Header().Get("Location"))
	}

	// 公開・非公開
	if rec := fx.postForm(t, path+"/publish", withCSRF(fx, cookie, nil), "same-origin", cookie); rec.Code != http.StatusSeeOther {
		t.Fatalf("publish: %d", rec.Code)
	}
	if rec := fx.do(t, http.MethodGet, "/news/"+strconv.FormatInt(id, 10), nil); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "新しい見出し") {
		t.Fatalf("published post must be public: %d", rec.Code)
	}
	if rec := fx.postForm(t, path+"/unpublish", withCSRF(fx, cookie, nil), "same-origin", cookie); rec.Code != http.StatusSeeOther {
		t.Fatalf("unpublish: %d", rec.Code)
	}
	if rec := fx.do(t, http.MethodGet, "/news/"+strconv.FormatInt(id, 10), nil); rec.Code != http.StatusNotFound {
		t.Fatal("unpublished post must not be public")
	}

	// 削除は確認画面を経てPOSTで行う
	if rec := fx.do(t, http.MethodGet, path+"/delete", nil, cookie); rec.Code != http.StatusOK {
		t.Fatalf("delete confirm: %d", rec.Code)
	}
	if _, err := fx.posts.Get(context.Background(), id); err != nil {
		t.Fatal("the confirmation page must not delete")
	}
	rec = fx.postForm(t, path+"/delete", withCSRF(fx, cookie, nil), "same-origin", cookie)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/posts?notice=deleted" {
		t.Fatalf("delete: %d", rec.Code)
	}
	if rec := fx.do(t, http.MethodGet, path, nil, cookie); rec.Code != http.StatusNotFound {
		t.Fatal("deleted post must be gone")
	}

	// 記事のタイトル・本文はログに出さない
	logs := fx.logs.String()
	for _, secret := range []string{"秘密の見出し", "新しい見出し", "書きかけの本文", "新しい本文"} {
		if strings.Contains(logs, secret) {
			t.Fatalf("post content leaked into logs: %q", secret)
		}
	}
}

func TestAdminPages_RequireSessionCSRFAndSameOrigin(t *testing.T) {
	fx := newPublicFixture(t, "")
	_, cookie := fx.loggedIn(t)
	id, _ := fx.posts.Create(context.Background(), "見出し", "")
	post := "/admin/posts/" + strconv.FormatInt(id, 10)

	// 記事の管理画面は、管理者認証の口（Page・Form）で守られていること
	for _, path := range []string{"/admin/posts", "/admin/posts/new", post, post + "/delete"} {
		if rec := fx.do(t, http.MethodGet, path, nil); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/login" {
			t.Errorf("GET %s without session: %d", path, rec.Code)
		}
	}
	posts := []string{"/admin/posts", post, post + "/publish", post + "/unpublish", post + "/delete"}
	for _, path := range posts {
		form := url.Values{"title": {"改ざん"}, "body": {""}}
		if rec := fx.postForm(t, path, form, "same-origin", cookie); rec.Code != http.StatusForbidden {
			t.Errorf("POST %s without CSRF token: %d", path, rec.Code)
		}
		if rec := fx.postForm(t, path, withCSRF(fx, cookie, form), "cross-site", cookie); rec.Code != http.StatusForbidden {
			t.Errorf("POST %s cross-site: %d", path, rec.Code)
		}
		if rec := fx.postForm(t, path, withCSRF(fx, cookie, form), "", cookie); rec.Code != http.StatusForbidden {
			t.Errorf("POST %s without Sec-Fetch-Site: %d", path, rec.Code)
		}
		if rec := fx.postForm(t, path, form, "same-origin", nil); rec.Code != http.StatusSeeOther {
			t.Errorf("POST %s without session: %d", path, rec.Code)
		}
	}
	if p, err := fx.posts.Get(context.Background(), id); err != nil || p.Title != "見出し" || p.Published {
		t.Fatal("rejected requests must not change the post")
	}

	// 不正なID
	for path, want := range map[string]int{
		"/admin/posts/abc": http.StatusBadRequest,
		"/admin/posts/999": http.StatusNotFound,
	} {
		if rec := fx.do(t, http.MethodGet, path, nil, cookie); rec.Code != want {
			t.Errorf("GET %s: got %d, want %d", path, rec.Code, want)
		}
	}
}

func TestAdminPosts_PublishedAt(t *testing.T) {
	fx := newPublicFixture(t, "")
	_, cookie := fx.loggedIn(t)
	ctx := context.Background()

	// 作成時に過去の公開日時を指定すると、公開してもその日時のまま
	past := time.Date(2024, 4, 1, 9, 30, 0, 0, time.Local)
	rec := fx.postForm(t, "/admin/posts", withCSRF(fx, cookie, url.Values{"title": {"過去のお知らせ"}, "published_at": {past.Format("2006-01-02T15:04")}}), "same-origin", cookie)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("create with date: %d %s", rec.Code, rec.Body.String())
	}
	loc, _ := url.Parse(rec.Header().Get("Location"))
	path := loc.Path
	id, _ := strconv.ParseInt(strings.TrimPrefix(path, "/admin/posts/"), 10, 64)
	if rec := fx.postForm(t, path+"/publish", withCSRF(fx, cookie, nil), "same-origin", cookie); rec.Code != http.StatusSeeOther {
		t.Fatalf("publish: %d", rec.Code)
	}
	if p, _ := fx.posts.Get(ctx, id); !p.PublishedAt.Equal(past) {
		t.Fatalf("published_at = %v, want %v", p.PublishedAt, past)
	}
	// 編集画面に現在の公開日時が入る
	if rec := fx.do(t, http.MethodGet, path, nil, cookie); !strings.Contains(rec.Body.String(), `value="2024-04-01T09:30"`) {
		t.Fatal("the edit form must show the current published_at")
	}

	// 秒つきの形式も受け付ける。空欄なら変更しない
	newer := time.Date(2025, 5, 6, 7, 8, 9, 0, time.Local)
	if rec := fx.postForm(t, path, withCSRF(fx, cookie, url.Values{"title": {"過去のお知らせ"}, "published_at": {newer.Format("2006-01-02T15:04:05")}}), "same-origin", cookie); rec.Code != http.StatusSeeOther {
		t.Fatalf("update date: %d", rec.Code)
	}
	if rec := fx.postForm(t, path, withCSRF(fx, cookie, url.Values{"title": {"過去のお知らせ"}}), "same-origin", cookie); rec.Code != http.StatusSeeOther {
		t.Fatalf("update without date: %d", rec.Code)
	}
	if p, _ := fx.posts.Get(ctx, id); !p.PublishedAt.Equal(newer) {
		t.Fatalf("published_at = %v, want %v", p.PublishedAt, newer)
	}

	// 不正な形式は400で、入力を残したフォームを返し、保存しない
	for value, msg := range map[string]string{"2025/05/06 07:08": "形式が正しくありません", "<script>": "形式が正しくありません", "1960-01-01T00:00": "形式が正しくありません"} {
		rec := fx.postForm(t, path, withCSRF(fx, cookie, url.Values{"title": {"書き換え後"}, "published_at": {value}}), "same-origin", cookie)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), msg) || !strings.Contains(rec.Body.String(), "書き換え後") {
			t.Errorf("published_at %q: %d", value, rec.Code)
		}
		if strings.Contains(rec.Body.String(), "<script>") {
			t.Error("the rejected value must be escaped")
		}
	}
	if p, _ := fx.posts.Get(ctx, id); p.Title != "過去のお知らせ" || !p.PublishedAt.Equal(newer) {
		t.Fatal("rejected input must not be saved")
	}
}

func TestAdminPosts_ScheduledPublishing(t *testing.T) {
	fx := newPublicFixture(t, "")
	_, cookie := fx.loggedIn(t)
	ctx := context.Background()
	clock := time.Now()
	fx.posts.now = func() time.Time { return clock }
	fx.feature.now = func() time.Time { return clock }

	future := clock.Add(48 * time.Hour).Truncate(time.Minute)
	rec := fx.postForm(t, "/admin/posts", withCSRF(fx, cookie, url.Values{"title": {"予約のお知らせ"}, "published_at": {future.Format("2006-01-02T15:04")}}), "same-origin", cookie)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("create scheduled: %d %s", rec.Code, rec.Body.String())
	}
	path := rec.Header().Get("Location")
	path = path[:strings.Index(path, "?")]
	id, _ := strconv.ParseInt(strings.TrimPrefix(path, "/admin/posts/"), 10, 64)
	public := "/news/" + strconv.FormatInt(id, 10)

	if rec := fx.do(t, http.MethodGet, path, nil, cookie); !strings.Contains(rec.Body.String(), "公開を予約する") {
		t.Fatal("the publish button must say it schedules")
	}
	if rec := fx.postForm(t, path+"/publish", withCSRF(fx, cookie, nil), "same-origin", cookie); rec.Code != http.StatusSeeOther {
		t.Fatalf("schedule: %d", rec.Code)
	}
	if p, _ := fx.posts.Get(ctx, id); !p.Published || !p.PublishedAt.Equal(future) {
		t.Fatalf("publishing must keep the scheduled time: %+v", p)
	}

	// 公開日時までは、公開ページ・一覧・APIのどれにも出ない
	for _, target := range []string{public, "/api/news/" + strconv.FormatInt(id, 10)} {
		if rec := fx.do(t, http.MethodGet, target, nil); rec.Code != http.StatusNotFound {
			t.Errorf("%s before schedule: %d", target, rec.Code)
		}
	}
	for _, target := range []string{"/news", "/api/news"} {
		if rec := fx.do(t, http.MethodGet, target, nil); strings.Contains(rec.Body.String(), "予約のお知らせ") {
			t.Errorf("%s must not list the scheduled post", target)
		}
	}
	if rec := fx.do(t, http.MethodGet, "/admin/posts", nil, cookie); !strings.Contains(rec.Body.String(), "公開予約") {
		t.Fatal("the admin list must show the scheduled state")
	}

	// 公開日時を過ぎると表示される
	clock = future.Add(time.Second)
	if rec := fx.do(t, http.MethodGet, public, nil); rec.Code != http.StatusOK {
		t.Fatalf("after schedule: %d", rec.Code)
	}
	for _, target := range []string{"/news", "/api/news"} {
		if rec := fx.do(t, http.MethodGet, target, nil); !strings.Contains(rec.Body.String(), "予約のお知らせ") {
			t.Errorf("%s must list the post after the schedule", target)
		}
	}
	if rec := fx.do(t, http.MethodGet, "/admin/posts", nil, cookie); strings.Contains(rec.Body.String(), "公開予約") || !strings.Contains(rec.Body.String(), "公開中") {
		t.Fatal("the admin list must show the post as published")
	}
}
