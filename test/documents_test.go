package test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"minimal-web-kit/features"
	"minimal-web-kit/features/auth"
	"minimal-web-kit/features/auth/authtest"
	"minimal-web-kit/features/documents"
	"minimal-web-kit/features/federatedauth"
	"minimal-web-kit/internal/app"
	"minimal-web-kit/internal/store"
)

type docsEnv struct {
	t      *testing.T
	routes http.Handler
	store  *documents.Store
	core   *store.SQLiteStore
}

func newDocsEnv(t *testing.T, logger *slog.Logger) *docsEnv {
	t.Helper()
	t.Setenv("AUTH_ORIGIN", "")
	t.Setenv("AUTH_RP_ID", "")
	core, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { core.Close() })
	reg := features.NewRegistry()
	reg.Register(auth.NewFeature(logger))
	reg.Register(documents.NewFeature(logger))
	for _, f := range reg.All() {
		if err := f.Init(core.DB()); err != nil {
			t.Fatal(err)
		}
	}
	routes, err := app.BuildRoutes(core, reg, false, logger)
	if err != nil {
		t.Fatal(err)
	}
	return &docsEnv{t: t, routes: routes, store: documents.NewStore(core.DB(), time.Now), core: core}
}

type docsUser struct {
	cookie *http.Cookie
	id     int64
	csrf   string
}

func (e *docsEnv) user(role string) docsUser {
	e.t.Helper()
	cookie, id := authtest.NewAccountSession(e.t, e.core.DB(), role)
	return docsUser{cookie, id, authtest.CSRFToken(e.t, e.routes, "/admin/passkeys", cookie)}
}

func (e *docsEnv) get(u docsUser, path string, header ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if u.cookie != nil {
		req.AddCookie(u.cookie)
	}
	if len(header) == 2 {
		req.Header.Set(header[0], header[1])
	}
	w := httptest.NewRecorder()
	e.routes.ServeHTTP(w, req)
	return w
}

func (e *docsEnv) post(u docsUser, path string, values url.Values) *httptest.ResponseRecorder {
	if values == nil {
		values = url.Values{}
	}
	values.Set("csrf_token", u.csrf)
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(values.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "null")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	if u.cookie != nil {
		req.AddCookie(u.cookie)
	}
	w := httptest.NewRecorder()
	e.routes.ServeHTTP(w, req)
	return w
}

// 不変条件: DOC-INV-1、DOC-INV-12
func TestDocuments_LoginRequired(t *testing.T) {
	e := newDocsEnv(t, slog.New(slog.NewTextHandler(io.Discard, nil)))
	owner := e.user("user")
	id, err := e.store.Create(context.Background(), owner.id, documents.Input{Title: "秘密の回覧", Body: "本文"})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/admin/documents", "/admin/documents/" + strconv.FormatInt(id, 10), "/admin/documents/mine", "/admin/document-folders"} {
		w := e.get(docsUser{}, path)
		if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/admin/login" || strings.Contains(w.Body.String(), "秘密") {
			t.Errorf("%s: %d %s", path, w.Code, w.Header().Get("Location"))
		}
	}
	// 変更はGETでは経路が無く、CSRFトークンの無いPOSTも通らない
	if w := e.get(owner, "/admin/documents/"+strconv.FormatInt(id, 10)+"/publish"); w.Code != http.StatusNotFound && w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET publish: %d", w.Code)
	}
	bad := owner
	bad.csrf = "wrong"
	if w := e.post(bad, "/admin/documents/"+strconv.FormatInt(id, 10)+"/delete", nil); w.Code != http.StatusForbidden {
		t.Errorf("POST without CSRF: %d", w.Code)
	}
}

// 読めない文書は、システム管理者にも404で返す。
// 不変条件: DOC-INV-2、DOC-INV-9
// ユースケース: UC-DOCUMENTS-2, UC-DOCUMENTS-8
func TestDocuments_AdminCannotReadUnshared(t *testing.T) {
	ctx := context.Background()
	e := newDocsEnv(t, slog.New(slog.NewTextHandler(io.Discard, nil)))
	owner, admin := e.user("user"), e.user("admin")
	id, err := e.store.Create(ctx, owner.id, documents.Input{Title: "人事の秘密タイトル", Body: "秘密の本文"})
	if err != nil {
		t.Fatal(err)
	}
	path := "/admin/documents/" + strconv.FormatInt(id, 10)
	for _, p := range []string{path, path + "/edit", path + "/versions", path + "/reads", path + "/share", path + "/delete"} {
		if w := e.get(admin, p); w.Code != http.StatusNotFound {
			t.Errorf("%s: %d", p, w.Code)
		}
	}
	if _, err := e.core.DB().Exec("UPDATE auth_accounts SET disabled_at = 1 WHERE id = ?", owner.id); err != nil {
		t.Fatal(err)
	}
	w := e.get(admin, "/admin/documents/transfer")
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "秘密") {
		t.Fatalf("transfer page must show counts only: %d", w.Code)
	}
	member := e.user("site_admin")
	if w := e.get(member, "/admin/documents/transfer"); w.Code != http.StatusForbidden {
		t.Fatalf("transfer is for system admins: %d", w.Code)
	}
}

// 開くと既読になる。先読みとHEADでは記録しない。
// 不変条件: DOC-INV-8、DOC-INV-12
// ユースケース: UC-DOCUMENTS-1, UC-DOCUMENTS-2, UC-DOCUMENTS-3
func TestDocuments_ShareReadAndConfirm(t *testing.T) {
	ctx := context.Background()
	e := newDocsEnv(t, slog.New(slog.NewTextHandler(io.Discard, nil)))
	owner, reader, outsider := e.user("user"), e.user("user"), e.user("site_admin")
	w := e.post(owner, "/admin/documents", url.Values{"title": {"全社回覧"}, "body": {"## 件名\n本文"}, "folder": {"0"}})
	if w.Code != http.StatusSeeOther {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	id, err := strconv.ParseInt(strings.Split(w.Header().Get("Location"), "/")[3], 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	path := "/admin/documents/" + strconv.FormatInt(id, 10)
	_, v, err := e.store.Draft(ctx, owner.id, id)
	if err != nil {
		t.Fatal(err)
	}
	if w := e.get(reader, path); w.Code != http.StatusNotFound {
		t.Fatalf("draft visible: %d", w.Code)
	}
	if w := e.post(owner, path+"/publish", url.Values{"version": {strconv.FormatInt(v.ID, 10)}}); w.Code != http.StatusSeeOther {
		t.Fatalf("publish: %d", w.Code)
	}
	if w := e.post(owner, path+"/share", url.Values{"audience": {"selected"}, "accounts": {strconv.FormatInt(reader.id, 10)}}); w.Code != http.StatusSeeOther {
		t.Fatalf("share: %d %s", w.Code, w.Body.String())
	}
	if w := e.get(outsider, path); w.Code != http.StatusNotFound {
		t.Fatalf("outsider: %d", w.Code)
	}
	if w := e.get(reader, path, "Sec-Purpose", "prefetch"); w.Code != http.StatusOK {
		t.Fatalf("prefetch: %d", w.Code)
	}
	req := httptest.NewRequest(http.MethodHead, path, nil)
	req.AddCookie(reader.cookie)
	e.routes.ServeHTTP(httptest.NewRecorder(), req)
	if w := e.get(owner, path+"/reads"); !strings.Contains(w.Body.String(), "1人のうち0人") {
		t.Fatalf("not read yet: %s", w.Body.String())
	}
	if w := e.get(reader, path); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "件名") {
		t.Fatalf("read: %d", w.Code)
	}
	if w := e.get(owner, path+"/reads"); !strings.Contains(w.Body.String(), "1人のうち1人") {
		t.Fatalf("read not counted: %s", w.Body.String())
	}
	if w := e.get(reader, path); strings.Contains(w.Body.String(), "/share") {
		t.Fatal("a reader must not see the owner's actions")
	}
}

// ユースケース: UC-DOCUMENTS-4, UC-DOCUMENTS-5
func TestDocuments_ApprovalScreens(t *testing.T) {
	ctx := context.Background()
	e := newDocsEnv(t, slog.New(slog.NewTextHandler(io.Discard, nil)))
	owner, approver := e.user("user"), e.user("user")
	id, _ := e.store.Create(ctx, owner.id, documents.Input{Title: "就業規則", Body: "本文"})
	_, v, _ := e.store.Draft(ctx, owner.id, id)
	path := "/admin/documents/" + strconv.FormatInt(id, 10)
	version := strconv.FormatInt(v.ID, 10)
	if w := e.post(owner, path+"/approval", url.Values{"version": {version}}); w.Code != http.StatusBadRequest {
		t.Fatalf("no approvers: %d", w.Code)
	}
	if w := e.post(owner, path+"/approval", url.Values{"version": {version}, "approvers": {strconv.FormatInt(approver.id, 10)}}); w.Code != http.StatusSeeOther {
		t.Fatalf("request: %d %s", w.Code, w.Body.String())
	}
	if w := e.get(approver, "/admin/documents/approvals"); !strings.Contains(w.Body.String(), "就業規則") {
		t.Fatal("the request must be listed")
	}
	if w := e.get(approver, path+"/versions/"+version); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "承認する") {
		t.Fatalf("decide page: %d", w.Code)
	}
	if w := e.post(approver, path+"/versions/"+version+"/reject", nil); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "document-reason") {
		t.Fatalf("reject without reason: %d", w.Code)
	}
	if w := e.post(approver, path+"/versions/"+version+"/reject", url.Values{"reason": {"第3条を直す"}}); w.Code != http.StatusSeeOther {
		t.Fatalf("reject: %d", w.Code)
	}
	if w := e.get(owner, path+"/edit"); !strings.Contains(w.Body.String(), "第3条を直す") || strings.Contains(w.Body.String(), "公開する") {
		t.Fatalf("the owner must see the reason and no direct publish: %s", w.Body.String())
	}
}

// 承認者でない人の送信は、版の有無が分からないように、無い版と同じ404にする。
// 不変条件: DOC-INV-2
func TestDocuments_DecisionDoesNotRevealVersions(t *testing.T) {
	ctx := context.Background()
	e := newDocsEnv(t, slog.New(slog.NewTextHandler(io.Discard, nil)))
	owner, approver, outsider := e.user("user"), e.user("user"), e.user("user")
	id, _ := e.store.Create(ctx, owner.id, documents.Input{Title: "規定", Body: "本文"})
	_, v, _ := e.store.Draft(ctx, owner.id, id)
	if err := e.store.RequestApproval(ctx, owner.id, id, v.ID, []int64{approver.id}, func(int64) bool { return true }); err != nil {
		t.Fatal(err)
	}
	path := "/admin/documents/" + strconv.FormatInt(id, 10) + "/versions/"
	for _, version := range []string{strconv.FormatInt(v.ID, 10), "999999"} {
		for _, action := range []string{"/approve", "/reject"} {
			if w := e.post(outsider, path+version+action, url.Values{"reason": {"x"}}); w.Code != http.StatusNotFound {
				t.Errorf("%s%s: %d", version, action, w.Code)
			}
		}
	}
	// 判断の済んだ人の再送は、案内を添えて依頼の一覧へ戻す
	version := strconv.FormatInt(v.ID, 10)
	if w := e.post(approver, path+version+"/approve", nil); w.Code != http.StatusSeeOther {
		t.Fatalf("approve: %d", w.Code)
	}
	if w := e.post(approver, path+version+"/approve", nil); w.Code != http.StatusSeeOther || !strings.Contains(w.Header().Get("Location"), "notice=conflict") {
		t.Fatalf("resend: %d %s", w.Code, w.Header().Get("Location"))
	}
}

// ユースケース: UC-DOCUMENTS-6
func TestDocuments_OwnerScreens(t *testing.T) {
	ctx := context.Background()
	e := newDocsEnv(t, slog.New(slog.NewTextHandler(io.Discard, nil)))
	owner, approver := e.user("user"), e.user("user")
	id, _ := e.store.Create(ctx, owner.id, documents.Input{Title: "回覧", Body: "本文"})
	_, v, _ := e.store.Draft(ctx, owner.id, id)
	path := "/admin/documents/" + strconv.FormatInt(id, 10)
	version := strconv.FormatInt(v.ID, 10)
	// フォルダの値の形が誤りなら、黙って外さない
	if w := e.post(owner, path+"/edit", url.Values{"version": {version}, "title": {"回覧"}, "folder": {"abc"}}); w.Code != http.StatusBadRequest {
		t.Fatalf("bad folder: %d", w.Code)
	}
	// 所有者は今の版を版の画面でも開ける
	if err := e.store.Publish(ctx, owner.id, id, v.ID); err != nil {
		t.Fatal(err)
	}
	if w := e.get(owner, path+"/versions/"+version); w.Code != http.StatusOK {
		t.Fatalf("current version page: %d %s", w.Code, w.Body.String())
	}
	if err := e.store.Revise(ctx, owner.id, id); err != nil {
		t.Fatal(err)
	}
	_, d, _ := e.store.Draft(ctx, owner.id, id)
	draft := strconv.FormatInt(d.ID, 10)
	if err := e.store.RequestApproval(ctx, owner.id, id, d.ID, []int64{approver.id}, func(int64) bool { return true }); err != nil {
		t.Fatal(err)
	}
	// 依頼の後に古い画面から保存しても、書いた本文を失わない
	w := e.post(owner, path+"/edit", url.Values{"version": {draft}, "title": {"回覧"}, "body": {"長い本文を残す"}})
	if w.Code != http.StatusConflict || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/html") || !strings.Contains(w.Body.String(), "長い本文を残す") {
		t.Fatalf("stale save: %d %s", w.Code, w.Header().Get("Content-Type"))
	}
	if w := e.get(owner, path+"/versions/"+draft); !strings.Contains(w.Body.String(), "依頼を取り下げる") {
		t.Fatal("the owner must be able to withdraw from the version page")
	}
	if w := e.post(owner, path+"/withdraw", url.Values{"version": {draft}}); w.Code != http.StatusSeeOther {
		t.Fatalf("withdraw: %d", w.Code)
	}
}

// ページ送りは検索の条件を残し、リンクに文言を持つ。
// ユースケース: UC-DOCUMENTS-2
func TestDocuments_PaginationKeepsTheSearch(t *testing.T) {
	ctx := context.Background()
	e := newDocsEnv(t, slog.New(slog.NewTextHandler(io.Discard, nil)))
	owner := e.user("user")
	for range 35 {
		id, err := e.store.Create(ctx, owner.id, documents.Input{Title: "回覧 & 通知", Body: "本文"})
		if err != nil {
			t.Fatal(err)
		}
		_, v, _ := e.store.Draft(ctx, owner.id, id)
		if err := e.store.Publish(ctx, owner.id, id, v.ID); err != nil {
			t.Fatal(err)
		}
	}
	body := e.get(owner, "/admin/documents?q="+url.QueryEscape("回覧 &")).Body.String()
	if !strings.Contains(body, `href="/admin/documents?q=%e5%9b%9e%e8%a6%a7%20%26&amp;page=2"`) || !strings.Contains(body, "過去の文書") {
		t.Fatalf("pagination lost the search:\n%s", body)
	}
}

// 社内の人の情報は公開のプライバシーポリシーに載せず、ログイン後の画面に出す。
// 不変条件: AUTH-INV-28, DOC-INV-14
func TestNotices_InternalStayBehindLogin(t *testing.T) {
	e := newDocsEnv(t, slog.New(slog.NewTextHandler(io.Discard, nil)))
	public := e.get(docsUser{}, "/privacy").Body.String()
	for _, title := range []string{"社内文書で保存する情報", "管理画面のアカウントで保存する情報"} {
		if strings.Contains(public, title) {
			t.Errorf("/privacy must not show %s", title)
		}
	}
	if w := e.get(docsUser{}, "/admin/data-notice"); w.Code != http.StatusSeeOther {
		t.Fatalf("notice page without login: %d", w.Code)
	}
	body := e.get(e.user("user"), "/admin/data-notice").Body.String()
	for _, title := range []string{"社内文書で保存する情報", "管理画面のアカウントで保存する情報"} {
		if !strings.Contains(body, title) {
			t.Errorf("the notice page must show %s", title)
		}
	}
}

// 呼び名を持たない認証と組み合わせると、起動を止める。
// 不変条件: DOC-INV-1
func TestDocuments_RequiresAccountDirectory(t *testing.T) {
	t.Setenv("AUTH_ORIGIN", "https://app.example")
	t.Setenv("OIDC_ISSUER", "https://id.example")
	t.Setenv("OIDC_CLIENT_ID", "client")
	secret := filepath.Join(t.TempDir(), "client_secret")
	if err := os.WriteFile(secret, []byte(strings.Repeat("s", 40)), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OIDC_CLIENT_SECRET_FILE", secret)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	core, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { core.Close() })
	reg := features.NewRegistry()
	reg.Register(federatedauth.NewFeature(logger))
	reg.Register(documents.NewFeature(logger))
	for _, f := range reg.All() {
		if err := f.Init(core.DB()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := app.BuildRoutes(core, reg, false, logger); err == nil || !strings.Contains(err.Error(), "account names") {
		t.Fatalf("documents must not start with federatedauth: %v", err)
	}
}

// 本文、タイトル、理由、検索語をログに出さない。
// 不変条件: DOC-INV-10
func TestCanary_DocumentsDoNotLeakInput(t *testing.T) {
	const canary = "CANARYdocs5e1b"
	var logBuf bytes.Buffer
	e := newDocsEnv(t, slog.New(slog.NewJSONHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	owner, approver := e.user("user"), e.user("user")
	w := e.post(owner, "/admin/documents", url.Values{"title": {canary}, "body": {canary}, "folder": {"0"}})
	id := strings.Split(w.Header().Get("Location"), "/")[3]
	num, _ := strconv.ParseInt(id, 10, 64)
	_, v, err := e.store.Draft(context.Background(), owner.id, num)
	if err != nil {
		t.Fatal(err)
	}
	version := strconv.FormatInt(v.ID, 10)
	e.post(owner, "/admin/documents/"+id+"/approval", url.Values{"version": {version}, "approvers": {strconv.FormatInt(approver.id, 10)}})
	e.post(approver, "/admin/documents/"+id+"/versions/"+version+"/reject", url.Values{"reason": {canary}})
	e.get(owner, "/admin/documents?q="+canary)
	e.get(owner, "/admin/documents/"+id+"/versions")
	e.post(owner, "/admin/document-folders", url.Values{"name": {canary}})
	if strings.Contains(logBuf.String(), canary) {
		t.Fatalf("canary found in logs:\n%s", logBuf.String())
	}
}
