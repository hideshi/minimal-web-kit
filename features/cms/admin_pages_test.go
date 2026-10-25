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
)

// loggedIn は登録トークンでパスキーを登録してログインし、認証器と管理者Cookieを返します。
func (fx *publicFixture) loggedIn(t *testing.T) (*fakeAuthenticator, *http.Cookie) {
	t.Helper()
	ctx := context.Background()
	token, err := Setup(ctx, fx.db, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	a := newFakeAuthenticator(t, algES256)
	if rec := fx.registerViaHTTP(t, a, token); rec.Code != http.StatusOK {
		t.Fatalf("register: %d %s", rec.Code, rec.Body.String())
	}
	userID, _ := fx.feature.admin.webAuthnUserID(ctx)
	return a, adminCookie(t, fx.loginViaHTTP(t, a, userID))
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
	form.Set("csrf_token", fx.feature.csrfToken(cookie.Value))
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
	cred := b64(mustFirstCredential(t, fx).ID)

	for _, path := range []string{"/admin/posts", "/admin/posts/new", post, post + "/delete", "/admin/passkeys", "/admin/passkeys/" + cred + "/delete"} {
		if rec := fx.do(t, http.MethodGet, path, nil); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/login" {
			t.Errorf("GET %s without session: %d", path, rec.Code)
		}
	}
	posts := []string{"/admin/posts", post, post + "/publish", post + "/unpublish", post + "/delete", "/admin/passkeys/" + cred + "/label", "/admin/passkeys/" + cred + "/delete"}
	for _, path := range posts {
		form := url.Values{"title": {"改ざん"}, "body": {""}, "label": {"改ざん"}}
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
	if creds, _ := fx.feature.admin.listCredentials(context.Background()); len(creds) != 1 || creds[0].Label == "改ざん" {
		t.Fatal("rejected requests must not change passkeys")
	}

	// 不正なID
	for path, want := range map[string]int{
		"/admin/posts/abc":            http.StatusBadRequest,
		"/admin/posts/999":            http.StatusNotFound,
		"/admin/passkeys/***/delete":  http.StatusNotFound,
		"/admin/passkeys/AAAA/delete": http.StatusNotFound,
	} {
		if rec := fx.do(t, http.MethodGet, path, nil, cookie); rec.Code != want {
			t.Errorf("GET %s: got %d, want %d", path, rec.Code, want)
		}
	}
}

func mustFirstCredential(t *testing.T, fx *publicFixture) credentialInfo {
	t.Helper()
	creds, err := fx.feature.admin.listCredentials(context.Background())
	if err != nil || len(creds) == 0 {
		t.Fatalf("no credentials: %v", err)
	}
	return creds[0]
}

func TestAdminPasskeys_AddRenameDelete(t *testing.T) {
	fx := newPublicFixture(t, "")
	ctx := context.Background()
	first, cookie := fx.loggedIn(t)
	userID, _ := fx.feature.admin.webAuthnUserID(ctx)
	// 別の端末でもログインしておく
	other := adminCookie(t, fx.loginViaHTTP(t, first, userID))

	// パスキー管理画面は、追加にWebAuthnを使うため認証画面と同じCSPで返る
	rec := fx.do(t, http.MethodGet, "/admin/passkeys", nil, cookie)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Security-Policy") != authPageCSP {
		t.Fatalf("passkeys page: %d %s", rec.Code, rec.Header().Get("Content-Security-Policy"))
	}
	if !strings.Contains(rec.Body.String(), "有効なパスキーが1つだけです") {
		t.Fatal("a single active passkey must be warned about")
	}

	// 最後の有効なパスキーは削除できない
	firstParam := b64(first.credID)
	if rec := fx.postForm(t, "/admin/passkeys/"+firstParam+"/delete", withCSRF(fx, cookie, nil), "same-origin", cookie); rec.Code != http.StatusConflict {
		t.Fatalf("deleting the last passkey: %d", rec.Code)
	}

	// ログイン中の追加は、セッションとCSRFトークン（ヘッダー）が必要
	second := newFakeAuthenticator(t, algEdDSA)
	headers := map[string]string{"Sec-Fetch-Site": "same-origin", "Content-Type": "application/json"}
	if rec := fx.postJSON(t, "/admin/webauthn/register/begin", map[string]string{}, headers, cookie); rec.Code != http.StatusForbidden {
		t.Fatalf("adding without CSRF header: %d", rec.Code)
	}
	headers["X-CSRF-Token"] = fx.feature.csrfToken(cookie.Value)
	rec = fx.postJSON(t, "/admin/webauthn/register/begin", map[string]string{}, headers, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("add begin: %d %s", rec.Code, rec.Body.String())
	}
	opts := decode(t, rec)
	cd := clientDataJSON("webauthn.create", opts["challenge"].(string), fx.feature.cfg.Origin, false)
	rec = fx.postJSON(t, "/admin/webauthn/register/finish", map[string]any{
		"label":              "<i>予備のキー</i>",
		"rawId":              b64(second.credID),
		"clientDataJSON":     b64(cd),
		"authenticatorData":  b64(authData(fx.feature.cfg.RPID, second.flags|flagAT, second.count, second.credID)),
		"publicKey":          b64(second.spki()),
		"publicKeyAlgorithm": second.alg,
	}, headers, cookie)
	if rec.Code != http.StatusOK || decode(t, rec)["next"] != "/admin/passkeys?notice=added" {
		t.Fatalf("add finish: %d %s", rec.Code, rec.Body.String())
	}
	rec = fx.do(t, http.MethodGet, "/admin/passkeys", nil, cookie)
	if !strings.Contains(rec.Body.String(), "&lt;i&gt;予備のキー&lt;/i&gt;") || strings.Contains(rec.Body.String(), "<i>予備") {
		t.Fatal("labels must be escaped")
	}

	// 名前の変更
	if rec := fx.postForm(t, "/admin/passkeys/"+firstParam+"/label", withCSRF(fx, cookie, url.Values{"label": {""}}), "same-origin", cookie); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty label: %d", rec.Code)
	}
	if rec := fx.postForm(t, "/admin/passkeys/"+firstParam+"/label", withCSRF(fx, cookie, url.Values{"label": {"仕事用PC"}}), "same-origin", cookie); rec.Code != http.StatusSeeOther {
		t.Fatalf("rename: %d", rec.Code)
	}
	if c, _ := fx.feature.admin.getCredentialInfo(ctx, first.credID); c.Label != "仕事用PC" {
		t.Fatalf("label not updated: %q", c.Label)
	}

	// 削除すると、現在のセッション以外のログイン状態を解除する
	if rec := fx.do(t, http.MethodGet, "/admin/passkeys/"+firstParam+"/delete", nil, cookie); rec.Code != http.StatusOK {
		t.Fatalf("delete confirm: %d", rec.Code)
	}
	rec = fx.postForm(t, "/admin/passkeys/"+firstParam+"/delete", withCSRF(fx, cookie, nil), "same-origin", cookie)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/passkeys?notice=removed" {
		t.Fatalf("delete: %d", rec.Code)
	}
	if rec := fx.do(t, http.MethodGet, "/admin/posts", nil, cookie); rec.Code != http.StatusOK {
		t.Fatal("the current session must be kept")
	}
	if rec := fx.do(t, http.MethodGet, "/admin/posts", nil, other); rec.Code != http.StatusSeeOther {
		t.Fatal("other sessions must be revoked")
	}
	if rec := fx.loginViaHTTP(t, first, userID); rec.Code != http.StatusUnauthorized {
		t.Fatal("a deleted passkey must not log in")
	}
	if strings.Contains(fx.logs.String(), "予備のキー") || strings.Contains(fx.logs.String(), "仕事用PC") {
		t.Fatal("labels must not be logged")
	}
}

func TestAdminPasskeys_DisabledPasskeyIsShownAndDeletable(t *testing.T) {
	fx := newPublicFixture(t, "")
	ctx := context.Background()
	first, _ := fx.loggedIn(t)
	userID, _ := fx.feature.admin.webAuthnUserID(ctx)

	second := newFakeAuthenticator(t, algES256)
	if err := fx.feature.admin.addCredential(ctx, newCredential{ID: second.credID, PublicKey: second.spki(), Alg: second.alg}, "予備"); err != nil {
		t.Fatal(err)
	}
	if err := fx.feature.admin.disableCredential(ctx, first.credID); err != nil {
		t.Fatal(err)
	}
	cookie := adminCookie(t, fx.loginViaHTTP(t, second, userID))

	// 全画面の上部に警告を出し、管理画面に無効化の表示を出す
	rec := fx.do(t, http.MethodGet, "/admin/posts", nil, cookie)
	if !strings.Contains(rec.Body.String(), "複製の疑いで無効化されたパスキーがあります") {
		t.Fatal("the warning must be shown on admin pages")
	}
	rec = fx.do(t, http.MethodGet, "/admin/passkeys", nil, cookie)
	if !strings.Contains(rec.Body.String(), "無効（複製の疑い）") {
		t.Fatal("the disabled passkey must be marked")
	}
	// 無効化済みのパスキーは、有効なパスキーが1つでも削除できる
	rec = fx.postForm(t, "/admin/passkeys/"+b64(first.credID)+"/delete", withCSRF(fx, cookie, nil), "same-origin", cookie)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("delete disabled passkey: %d", rec.Code)
	}
	rec = fx.do(t, http.MethodGet, "/admin/posts", nil, cookie)
	if strings.Contains(rec.Body.String(), "複製の疑いで無効化されたパスキーがあります") {
		t.Fatal("the warning must disappear after deletion")
	}
}

func TestAdminStore_DeleteCredentialKeepsOneActive(t *testing.T) {
	s, _, _ := newAdminFixture(t)
	ctx := context.Background()
	for _, id := range []string{"a", "b"} {
		if err := s.addCredential(ctx, testCredential(id), "キー"+id); err != nil {
			t.Fatal(err)
		}
	}
	// 並行して両方を削除しても、有効なパスキーは1つ残る
	errs := make(chan error, 2)
	for _, id := range []string{"a", "b"} {
		go func(id string) { errs <- s.deleteCredential(ctx, []byte(id), "") }(id)
	}
	ok := 0
	for range 2 {
		if err := <-errs; err == nil {
			ok++
		} else if err != ErrLastCredential {
			t.Fatal(err)
		}
	}
	creds, _ := s.listCredentials(ctx)
	if ok != 1 || len(creds) != 1 {
		t.Fatalf("deleted %d, remaining %d", ok, len(creds))
	}
	if err := s.deleteCredential(ctx, []byte("missing"), ""); err != ErrNotFound {
		t.Fatalf("missing credential: %v", err)
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
