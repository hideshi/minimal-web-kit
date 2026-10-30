package contact

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"minimal-web-kit/features"
	"minimal-web-kit/features/auth"
	"minimal-web-kit/features/auth/authtest"
	"minimal-web-kit/internal/app"
	"minimal-web-kit/internal/store"
)

func setup(t *testing.T) (*Feature, http.Handler, *store.SQLiteStore, *bytes.Buffer, *time.Time) {
	t.Helper()
	t.Setenv("AUTH_ORIGIN", "")
	t.Setenv("AUTH_RP_ID", "")
	core, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { core.Close() })
	logs := &bytes.Buffer{}
	logger := slog.New(slog.NewTextHandler(logs, nil))
	f := NewFeature(logger)
	clock := time.Date(2026, 10, 29, 12, 0, 0, 0, time.Local)
	f.now = func() time.Time { return clock }
	reg := features.NewRegistry()
	reg.Register(auth.NewFeature(logger))
	reg.Register(f)
	for _, feature := range reg.All() {
		if err := feature.Init(core.DB()); err != nil {
			t.Fatal(err)
		}
	}
	routes, err := app.BuildRoutes(core, reg, false, logger)
	if err != nil {
		t.Fatal(err)
	}
	return f, routes, core, logs, &clock
}

var tokenPattern = regexp.MustCompile(`name="token" value="([A-Za-z0-9_-]+)"`)

func getForm(t *testing.T, routes http.Handler) (*httptest.ResponseRecorder, string) {
	t.Helper()
	w := httptest.NewRecorder()
	routes.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/contact", nil))
	m := tokenPattern.FindStringSubmatch(w.Body.String())
	if w.Code != http.StatusOK || m == nil {
		t.Fatalf("form: %d", w.Code)
	}
	return w, m[1]
}

func submit(routes http.Handler, values url.Values, site string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/contact", strings.NewReader(values.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Sec-Fetch-Site", site)
	req.Header.Set("Origin", "null")
	w := httptest.NewRecorder()
	routes.ServeHTTP(w, req)
	return w
}

func count(t *testing.T, core *store.SQLiteStore) int {
	t.Helper()
	var n int
	if err := core.DB().QueryRow("SELECT COUNT(*) FROM contact_messages").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func valid(token, body string) url.Values {
	return url.Values{"token": {token}, "body": {body}, "name": {"お名前カナリア"}, "reply_to": {"返信先カナリア"}}
}

// TestPublicForm_NoCookieNoStoreAndSaves は、公開のフォームがCookieを使わず、キャッシュさせず、IP等を保存しないことと、
// 送信が保存されて完了の画面へ移り、ログに中身が出ないことを確かめる。
// 不変条件: CONTACT-INV-1, CONTACT-INV-6, CONTACT-INV-9
// ユースケース: UC-CONTACT-1
func TestPublicForm_NoCookieNoStoreAndSaves(t *testing.T) {
	_, routes, core, logs, clock := setup(t)
	w, token := getForm(t, routes)
	if w.Header().Get("Set-Cookie") != "" || !strings.Contains(w.Header().Get("Cache-Control"), "no-store") {
		t.Fatalf("form must not set cookies and must not be cached: %v", w.Header())
	}
	for _, want := range []string{"お問い合わせへの回答のためだけに使い", "受付から90日で削除します", `href="/privacy"`, "特に配慮が必要な情報は書かないでください"} {
		if !strings.Contains(w.Body.String(), want) {
			t.Fatalf("%s must be shown before sending", want)
		}
	}
	*clock = clock.Add(5 * time.Second)
	res := submit(routes, valid(token, "内容カナリア"), "same-origin")
	if res.Code != http.StatusSeeOther || res.Header().Get("Location") != "/contact?notice=sent" || res.Header().Get("Set-Cookie") != "" {
		t.Fatalf("submit: %d %v", res.Code, res.Header())
	}
	if count(t, core) != 1 {
		t.Fatal("not saved")
	}
	rows, err := core.DB().Query("SELECT name FROM pragma_table_info('contact_messages')")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		_ = rows.Scan(&name)
		if strings.Contains(name, "ip") || strings.Contains(name, "agent") || strings.Contains(name, "owner") {
			t.Errorf("column %s must not exist", name)
		}
	}
	for _, canary := range []string{"内容カナリア", "お名前カナリア", "返信先カナリア"} {
		if strings.Contains(logs.String(), canary) {
			t.Fatalf("log leaked %s", canary)
		}
	}
	if !strings.Contains(logs.String(), "result=saved") {
		t.Fatal("result must be logged as a fixed class")
	}
	done := httptest.NewRecorder()
	routes.ServeHTTP(done, httptest.NewRequest(http.MethodGet, "/contact?notice=sent", nil))
	if !strings.Contains(done.Body.String(), "お問い合わせを受け付けました") {
		t.Fatal("missing completion notice")
	}
}

// TestSubmit_SpamVerdicts は、各判定で保存しないことと、受け付けたふり・400・403の出し分け、
// 入力の誤りの出し直しが元のフォームの値を使うこと（すぐ直して送っても捨てない）を確かめる。
// 不変条件: CONTACT-INV-4, CONTACT-INV-8
func TestSubmit_SpamVerdicts(t *testing.T) {
	f, routes, core, logs, clock := setup(t)
	_, token := getForm(t, routes)
	issued := *clock

	// 他のサイトからの送信は拒否する
	*clock = issued.Add(5 * time.Second)
	if w := submit(routes, valid(token, "本文"), "cross-site"); w.Code != http.StatusForbidden {
		t.Fatalf("cross-site: %d", w.Code)
	}
	// 見えない入力欄に値がある → 受け付けたふり
	v := valid(token, "本文")
	v.Set(trapField, "bot")
	if w := submit(routes, v, "same-origin"); w.Code != http.StatusSeeOther {
		t.Fatalf("honeypot: %d", w.Code)
	}
	// 3秒未満 → 受け付けたふり
	*clock = issued.Add(time.Second)
	if w := submit(routes, valid(token, "本文"), "same-origin"); w.Code != http.StatusSeeOther {
		t.Fatalf("too fast: %d", w.Code)
	}
	// 署名の改ざん → 400、入力を残す
	*clock = issued.Add(5 * time.Second)
	forged := []byte(token)
	forged[len(forged)-2] ^= 1
	if w := submit(routes, valid(string(forged), "残す本文"), "same-origin"); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "残す本文") || !strings.Contains(w.Body.String(), `role="alert"`) {
		t.Fatalf("bad token: %d", w.Code)
	}
	// URLが3つ → 内容の欄のエラー。元の値のまま出し直し、直してすぐ（0秒後）送っても保存される
	w := submit(routes, valid(token, "https://a.example https://b.example www.c.example"), "same-origin")
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "URLは2つまで") || !strings.Contains(w.Body.String(), `aria-invalid="true"`) {
		t.Fatalf("links: %d", w.Code)
	}
	if m := tokenPattern.FindStringSubmatch(w.Body.String()); m == nil || m[1] != token {
		t.Fatal("an input error must keep the original form value")
	}
	if len(f.tokens.used) != 0 {
		t.Fatal("a rejected submission must not mark the value as used")
	}
	if count(t, core) != 0 {
		t.Fatal("rejected submissions must not be saved")
	}
	if w := submit(routes, valid(token, "直した本文"), "same-origin"); w.Code != http.StatusSeeOther || count(t, core) != 1 {
		t.Fatalf("fixed submission must be saved: %d", w.Code)
	}
	// 使用済みの値 → 400、入力を残す（戻るボタンで古いフォームから送った人）
	if w := submit(routes, valid(token, "二回目の本文"), "same-origin"); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "二回目の本文") || count(t, core) != 1 {
		t.Fatalf("replay: %d", w.Code)
	}
	// 期限切れ → 400、入力を残す
	*clock = issued.Add(2 * time.Hour)
	if w := submit(routes, valid(token, "古い本文"), "same-origin"); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "時間が経った") || !strings.Contains(w.Body.String(), "古い本文") {
		t.Fatalf("expired: %d", w.Code)
	}
	// 再起動（鍵の作り直し）の後 → 400、入力を残す
	_, fresh := getForm(t, routes)
	f.tokens = newTokens()
	*clock = clock.Add(5 * time.Second)
	if w := submit(routes, valid(fresh, "再起動の本文"), "same-origin"); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "再起動の本文") {
		t.Fatalf("after restart: %d", w.Code)
	}
	if count(t, core) != 1 {
		t.Fatal("only the fixed submission must be saved")
	}
	for _, class := range []string{"honeypot", "too_fast", "bad_token", "invalid_links", "replayed", "expired"} {
		if !strings.Contains(logs.String(), "result="+class) {
			t.Errorf("missing log class %s", class)
		}
	}
}

// TestSubmit_LimitsCountSavedOnly は、全体の受付上限を超えたら保存せず503で入力を返すことと、捨てた送信を数えないことを確かめる。
// 不変条件: CONTACT-INV-5, CONTACT-INV-8
func TestSubmit_LimitsCountSavedOnly(t *testing.T) {
	f, routes, core, _, clock := setup(t)
	f.cfg.Limits = Limits{Hourly: 2, Daily: 100, Unread: 100}
	send := func(body string, trap bool) *httptest.ResponseRecorder {
		_, token := getForm(t, routes)
		*clock = clock.Add(5 * time.Second)
		v := valid(token, body)
		if trap {
			v.Set(trapField, "x")
		}
		return submit(routes, v, "same-origin")
	}
	for i := 0; i < 5; i++ {
		send("ボット", true)
	}
	for i := 0; i < 2; i++ {
		if w := send("人の本文", false); w.Code != http.StatusSeeOther {
			t.Fatalf("send %d: %d", i, w.Code)
		}
	}
	w := send("上限の本文", false)
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "上限の本文") || !strings.Contains(w.Body.String(), "一時停止") || count(t, core) != 2 {
		t.Fatalf("over limit: %d", w.Code)
	}
	if len(f.tokens.used) != 2 {
		t.Fatalf("used records must equal saved submissions: %d", len(f.tokens.used))
	}
	*clock = clock.Add(time.Hour)
	if w := send("1時間後", false); w.Code != http.StatusSeeOther {
		t.Fatalf("after an hour: %d", w.Code)
	}
	f.cfg.Limits = Limits{Hourly: 100, Daily: 100, Unread: 3}
	if w := send("未読の上限", false); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("unread limit: %d", w.Code)
	}
}

// 送られたお問い合わせは受信箱に未読で届き、管理者が読んで既読やスパムに変えられること。
//
// ユースケース: UC-CONTACT-2, UC-CONTACT-3
func TestAdmin_ProcessSubmittedMessage(t *testing.T) {
	f, routes, core, _, clock := setup(t)
	admin := authtest.NewSessionWithRole(t, core.DB(), "admin")
	_, token := getForm(t, routes)
	*clock = clock.Add(5 * time.Second)
	if w := submit(routes, valid(token, "届いた内容"), "same-origin"); w.Code != http.StatusSeeOther {
		t.Fatalf("submit: %d", w.Code)
	}
	var id int64
	if err := core.DB().QueryRow("SELECT id FROM contact_messages").Scan(&id); err != nil {
		t.Fatal(err)
	}
	path := "/admin/contact/" + itoa(id)
	inbox := adminRequest(routes, http.MethodGet, "/admin/contact", admin, nil).Body.String()
	if !strings.Contains(inbox, "届いた内容") || !strings.Contains(inbox, "未読 1件") {
		t.Fatalf("the message must reach the inbox as unread: %s", inbox)
	}
	if body := adminRequest(routes, http.MethodGet, path, admin, nil).Body.String(); !strings.Contains(body, "届いた内容") || !strings.Contains(body, "返信先カナリア") {
		t.Fatal("the detail must show the message and the reply address")
	}
	csrf := authtest.CSRFToken(t, routes, path, admin)
	for _, status := range []string{statusRead, statusSpam, statusUnread} {
		if w := adminRequest(routes, http.MethodPost, path+"/status", admin, url.Values{"status": {status}, "csrf_token": {csrf}}); w.Code != http.StatusSeeOther {
			t.Fatalf("status %s: %d", status, w.Code)
		}
		m, err := f.store.Get(context.Background(), id)
		if err != nil || m.Status != status {
			t.Fatalf("status %s was not saved: %q %v", status, m.Status, err)
		}
		inbox := adminRequest(routes, http.MethodGet, "/admin/contact", admin, nil).Body.String()
		if strings.Contains(inbox, "未読 1件") != (status == statusUnread) || strings.Contains(inbox, "スパムをまとめて削除する") != (status == statusSpam) {
			t.Fatalf("the inbox does not show %s", status)
		}
	}
}

// 1日の上限は直近24時間の件数で数えること。
// 不変条件: CONTACT-INV-5
func TestSave_DailyLimitIsARollingWindow(t *testing.T) {
	f, _, _, _, clock := setup(t)
	limits := Limits{Hourly: 100, Daily: 2, Unread: 100}
	save := func() error {
		_, err := f.store.Save(context.Background(), Input{Body: "本文"}, limits)
		return err
	}
	for i := range 2 {
		if err := save(); err != nil {
			t.Fatalf("save %d: %v", i, err)
		}
	}
	*clock = clock.Add(23 * time.Hour)
	if err := save(); !errors.Is(err, ErrOverLimit) {
		t.Fatalf("within 24 hours: %v", err)
	}
	*clock = clock.Add(time.Hour + time.Second)
	if err := save(); err != nil {
		t.Fatalf("after 24 hours: %v", err)
	}
}

// 同時に送られても、上限を超えて保存しないこと。
// 不変条件: CONTACT-INV-5
func TestSave_ConcurrentSubmissionsKeepTheLimit(t *testing.T) {
	f, _, core, _, _ := setup(t)
	limits := Limits{Hourly: 3, Daily: 100, Unread: 100}
	var wg sync.WaitGroup
	errs := make([]error, 12)
	for i := range errs {
		wg.Go(func() {
			_, errs[i] = f.store.Save(context.Background(), Input{Body: "本文"}, limits)
		})
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil && !errors.Is(err, ErrOverLimit) {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if got := count(t, core); got != 3 {
		t.Fatalf("saved %d, want 3", got)
	}
}

// TestPurge_RemovesExpiredRegardlessOfStatus は、保持期間を過ぎたものだけが、状態に関係なく削除されることを確かめる。
// 不変条件: CONTACT-INV-2
func TestPurge_RemovesExpiredRegardlessOfStatus(t *testing.T) {
	f, _, core, _, clock := setup(t)
	old := clock.Add(-91 * 24 * time.Hour).Unix()
	recent := clock.Add(-89 * 24 * time.Hour).Unix()
	for _, row := range []struct {
		status string
		at     int64
	}{{"unread", old}, {"read", old}, {"spam", old}, {"unread", recent}, {"spam", recent}} {
		if _, err := core.DB().Exec("INSERT INTO contact_messages (body, status, created_at) VALUES ('x', ?, ?)", row.status, row.at); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Purge(context.Background()); err != nil {
		t.Fatal(err)
	}
	var left, stale int
	_ = core.DB().QueryRow("SELECT COUNT(*), COUNT(CASE WHEN created_at = ? THEN 1 END) FROM contact_messages", old).Scan(&left, &stale)
	if left != 2 || stale != 0 {
		t.Fatalf("left=%d stale=%d", left, stale)
	}
}

func adminRequest(routes http.Handler, method, path string, cookie *http.Cookie, values url.Values) *httptest.ResponseRecorder {
	var body *strings.Reader
	if values != nil {
		body = strings.NewReader(values.Encode())
	} else {
		body = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, body)
	if values != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		req.Header.Set("Origin", "null")
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	routes.ServeHTTP(w, req)
	return w
}

// TestAdmin_InboxIsAdminOnlyAndGetDoesNotChangeState は、受信箱が管理者だけに見え、GETで状態が変わらず、
// 状態の変更・削除・スパムのまとめての削除ができることを確かめる。
// 不変条件: CONTACT-INV-3, CONTACT-INV-7
// ユースケース: UC-CONTACT-4, UC-CONTACT-5
func TestAdmin_InboxIsAdminOnlyAndGetDoesNotChangeState(t *testing.T) {
	f, routes, core, _, _ := setup(t)
	id, err := f.store.Save(context.Background(), Input{Body: "<b>本文</b>", ReplyTo: "mailto:x@example.com"}, Limits{Hourly: 10, Daily: 10, Unread: 10})
	if err != nil {
		t.Fatal(err)
	}
	admin := authtest.NewSession(t, core.DB())
	member := authtest.NewSessionWithRole(t, core.DB(), "member")
	path := "/admin/contact/" + itoa(id)

	for _, p := range []string{"/admin/contact", path, path + "/delete", "/admin/contact/spam/delete"} {
		if w := adminRequest(routes, http.MethodGet, p, member, nil); w.Code != http.StatusForbidden {
			t.Errorf("member %s: %d", p, w.Code)
		}
		if w := adminRequest(routes, http.MethodGet, p, nil, nil); w.Code != http.StatusSeeOther {
			t.Errorf("anonymous %s: %d", p, w.Code)
		}
	}
	if w := adminRequest(routes, http.MethodGet, "/admin/passkeys", member, nil); strings.Contains(w.Body.String(), "お問い合わせの管理") {
		t.Fatal("members must not see the inbox link")
	}
	if w := adminRequest(routes, http.MethodGet, "/admin/passkeys", admin, nil); !strings.Contains(w.Body.String(), "お問い合わせの管理") {
		t.Fatal("admins must see the inbox link")
	}

	w := adminRequest(routes, http.MethodGet, path, admin, nil)
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "<b>本文") || strings.Contains(w.Body.String(), `href="mailto:`) {
		t.Fatalf("show must escape and must not link the reply address: %d", w.Code)
	}
	if m, _ := f.store.Get(context.Background(), id); m.Status != statusUnread {
		t.Fatal("GET must not mark as read")
	}
	token := authtest.CSRFToken(t, routes, path, admin)
	for _, status := range []string{"read", "spam"} {
		if w := adminRequest(routes, http.MethodPost, path+"/status", admin, url.Values{"status": {status}, "csrf_token": {token}}); w.Code != http.StatusSeeOther {
			t.Fatalf("status %s: %d", status, w.Code)
		}
	}
	if w := adminRequest(routes, http.MethodPost, path+"/status", admin, url.Values{"status": {"other"}, "csrf_token": {token}}); w.Code != http.StatusBadRequest {
		t.Fatalf("unknown status: %d", w.Code)
	}
	if w := adminRequest(routes, http.MethodPost, path+"/status", member, url.Values{"status": {"read"}, "csrf_token": {token}}); w.Code == http.StatusSeeOther {
		t.Fatal("member must not change status")
	}
	if w := adminRequest(routes, http.MethodGet, "/admin/contact/spam/delete", admin, nil); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "1件") {
		t.Fatalf("spam delete page: %d", w.Code)
	}
	if w := adminRequest(routes, http.MethodPost, "/admin/contact/spam/delete", admin, url.Values{"csrf_token": {token}}); w.Code != http.StatusSeeOther || count(t, core) != 0 {
		t.Fatalf("spam delete: %d", w.Code)
	}
	if w := adminRequest(routes, http.MethodGet, "/admin/contact/spam/delete", admin, nil); w.Code != http.StatusSeeOther || !strings.Contains(w.Header().Get("Location"), "no_spam") {
		t.Fatalf("no spam: %d", w.Code)
	}
	id2, _ := f.store.Save(context.Background(), Input{Body: "消す"}, Limits{Hourly: 10, Daily: 10, Unread: 10})
	if w := adminRequest(routes, http.MethodPost, "/admin/contact/"+itoa(id2)+"/delete", admin, url.Values{"csrf_token": {token}}); w.Code != http.StatusSeeOther || count(t, core) != 0 {
		t.Fatalf("delete: %d", w.Code)
	}
	if w := adminRequest(routes, http.MethodGet, "/admin/contact/"+itoa(id2), admin, nil); w.Code != http.StatusNotFound {
		t.Fatalf("deleted message: %d", w.Code)
	}
}

func itoa(id int64) string { return strconv.FormatInt(id, 10) }

func TestLoadConfig(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	cfg, err := LoadConfig(env(nil))
	if err != nil || cfg.Retention != 90*24*time.Hour || cfg.Limits != (Limits{Hourly: 20, Daily: 100, Unread: 500}) || cfg.Origin != defaultOrigin {
		t.Fatalf("defaults: %+v %v", cfg, err)
	}
	for _, bad := range []map[string]string{
		{"CONTACT_RETENTION_DAYS": "0"}, {"CONTACT_RETENTION_DAYS": "366"}, {"CONTACT_RETENTION_DAYS": "abc"},
		{"CONTACT_HOURLY_LIMIT": "0"}, {"CONTACT_DAILY_LIMIT": "x"}, {"CONTACT_UNREAD_LIMIT": "-1"}, {"AUTH_ORIGIN": "ftp://x"},
	} {
		if _, err := LoadConfig(env(bad)); err == nil {
			t.Errorf("%v must be rejected", bad)
		} else {
			for _, v := range bad {
				if strings.Contains(err.Error(), v) && v != "0" {
					t.Errorf("error must not contain the value: %v", err)
				}
			}
		}
	}
	if cfg, err := LoadConfig(env(map[string]string{"CONTACT_RETENTION_DAYS": "30"})); err != nil || cfg.Retention != 30*24*time.Hour {
		t.Fatal("custom retention")
	}
}

func TestCountLinks(t *testing.T) {
	for body, want := range map[string]int{"なし": 0, "https://a.example": 1, "http://a https://www.b.example": 2, "www.a.example と WWW.b.example": 2} {
		if got := countLinks(body); got != want {
			t.Errorf("%q: %d want %d", body, got, want)
		}
	}
}
