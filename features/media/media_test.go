package media_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"minimal-web-kit/features"
	"minimal-web-kit/features/auth"
	"minimal-web-kit/features/auth/authtest"
	"minimal-web-kit/features/catalog"
	"minimal-web-kit/features/media"
	"minimal-web-kit/features/news"
	"minimal-web-kit/features/pages"
	"minimal-web-kit/internal/app"
	"minimal-web-kit/internal/store"
)

type fixture struct {
	t      *testing.T
	core   *store.SQLiteStore
	db     *sql.DB
	routes http.Handler
	reg    *features.Registry
	admin  *http.Cookie
	token  string
	logs   *bytes.Buffer
}

// build は、機能を組み立てて経路を作ります。同じDBで機能の組み合わせを変えて試せます。
func build(t *testing.T, core *store.SQLiteStore, logs io.Writer, names ...string) (*features.Registry, http.Handler) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(logs, nil))
	reg := features.NewRegistry()
	reg.Register(auth.NewFeature(logger))
	for _, name := range names {
		switch name {
		case "pages":
			reg.Register(pages.NewFeature(logger))
		case "news":
			reg.Register(news.NewFeature(logger))
		case "catalog":
			reg.Register(catalog.NewFeature(logger))
		case "media":
			reg.Register(media.NewFeature(logger))
		}
	}
	for _, f := range reg.All() {
		if err := f.Init(core.DB()); err != nil {
			t.Fatal(err)
		}
	}
	routes, err := app.BuildRoutes(core, reg, false, logger)
	if err != nil {
		t.Fatal(err)
	}
	return reg, routes
}

func setup(t *testing.T, names ...string) *fixture {
	t.Helper()
	t.Setenv("AUTH_ORIGIN", "")
	t.Setenv("AUTH_RP_ID", "")
	core, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { core.Close() })
	if len(names) == 0 {
		names = []string{"media", "pages", "news", "catalog"}
	}
	logs := &bytes.Buffer{}
	reg, routes := build(t, core, logs, names...)
	fx := &fixture{t: t, core: core, db: core.DB(), routes: routes, reg: reg, logs: logs}
	fx.admin = authtest.NewSession(t, core.DB())
	fx.token = authtest.CSRFToken(t, routes, "/admin/pages/new", fx.admin)
	return fx
}

func (fx *fixture) get(path string, cookie *http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	fx.routes.ServeHTTP(w, req)
	return w
}

func (fx *fixture) post(path string, values url.Values) *httptest.ResponseRecorder {
	if values == nil {
		values = url.Values{}
	}
	values.Set("csrf_token", fx.token)
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(values.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.AddCookie(fx.admin)
	w := httptest.NewRecorder()
	fx.routes.ServeHTTP(w, req)
	return w
}

type part struct{ name, file, value string }

func multipartBody(t *testing.T, parts ...part) (*bytes.Buffer, string) {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for _, p := range parts {
		var w io.Writer
		var err error
		if p.file != "" {
			w, err = mw.CreateFormFile(p.name, p.file)
		} else {
			w, err = mw.CreateFormField(p.name)
		}
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(w, p.value); err != nil {
			t.Fatal(err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	return &body, mw.FormDataContentType()
}

func (fx *fixture) upload(cookie *http.Cookie, parts ...part) *httptest.ResponseRecorder {
	fx.t.Helper()
	body, contentType := multipartBody(fx.t, parts...)
	req := httptest.NewRequest(http.MethodPost, "/admin/media", body)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	fx.routes.ServeHTTP(w, req)
	return w
}

func photo(t *testing.T, seed uint8) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 64, 48))
	for y := range 48 {
		for x := range 64 {
			img.SetRGBA(x, y, color.RGBA{uint8(x) + seed, uint8(y), seed, 255})
		}
	}
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, nil); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// add は画像を追加して、公開のファイル名を返します。
func (fx *fixture) add(alt string, seed uint8) string {
	fx.t.Helper()
	w := fx.upload(fx.admin, part{name: "csrf_token", value: fx.token}, part{name: "alt", value: alt},
		part{name: "file", file: "photo.jpg", value: string(photo(fx.t, seed))})
	if w.Code != http.StatusSeeOther {
		fx.t.Fatalf("upload: %d %s", w.Code, w.Body.String())
	}
	id := strings.TrimPrefix(strings.Split(w.Header().Get("Location"), "?")[0], "/admin/media/")
	var format string
	if err := fx.db.QueryRow("SELECT format FROM media_items WHERE id = ?", id).Scan(&format); err != nil {
		fx.t.Fatal(err)
	}
	return id + "." + format
}

func createdID(t *testing.T, w *httptest.ResponseRecorder, prefix string) int64 {
	t.Helper()
	if w.Code != http.StatusSeeOther {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(strings.Split(w.Header().Get("Location"), "?")[0], prefix), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func (fx *fixture) page(slug, body string) int64 {
	fx.t.Helper()
	w := fx.post("/admin/pages", url.Values{"slug": {slug}, "title": {"題"}, "body": {body}})
	return createdID(fx.t, w, "/admin/pages/")
}

func (fx *fixture) status(file string) int {
	return fx.get("/media/"+file, nil).Code
}

func refCount(t *testing.T, db *sql.DB, file string) int {
	t.Helper()
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM media_refs WHERE media_id = ?", strings.Split(file, ".")[0]).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// 公開の経路で見えるのは、公開中の記事から使われている画像だけ。記事の状態から毎回判定する。
//
// 不変条件: MEDIA-INV-1
// ユースケース: UC-MEDIA-2, UC-MEDIA-3
func TestPublic_FollowsArticleState(t *testing.T) {
	fx := setup(t)
	file := fx.add("外観", 1)
	if got := fx.status(file); got != http.StatusNotFound {
		t.Fatalf("unused: %d", got)
	}
	line := "![外観](/media/" + file + ")"
	id := fx.page("about-us", line)
	if got := fx.status(file); got != http.StatusNotFound {
		t.Fatalf("draft only: %d", got)
	}
	if w := fx.post(fmt.Sprintf("/admin/pages/%d/publish", id), nil); w.Code != http.StatusSeeOther {
		t.Fatalf("publish: %d", w.Code)
	}
	if got := fx.status(file); got != http.StatusOK {
		t.Fatalf("published: %d", got)
	}
	// 2つの記事で使えば、片方が公開中の間は見える
	second := fx.page("second", line)
	if w := fx.post(fmt.Sprintf("/admin/pages/%d/publish", second), nil); w.Code != http.StatusSeeOther {
		t.Fatalf("publish: %d", w.Code)
	}
	if w := fx.post(fmt.Sprintf("/admin/pages/%d/unpublish", id), nil); w.Code != http.StatusSeeOther {
		t.Fatalf("unpublish: %d", w.Code)
	}
	if got := fx.status(file); got != http.StatusOK {
		t.Fatalf("still used by another: %d", got)
	}
	// 本文から外すと見えなくなる
	if w := fx.post(fmt.Sprintf("/admin/pages/%d", second), url.Values{"slug": {"second"}, "title": {"題"}, "body": {"文字"}}); w.Code != http.StatusSeeOther {
		t.Fatalf("save: %d", w.Code)
	}
	if got := fx.status(file); got != http.StatusNotFound {
		t.Fatalf("reference removed: %d", got)
	}
	// 記事を消すと参照も消える
	if w := fx.post(fmt.Sprintf("/admin/pages/%d/delete", id), nil); w.Code != http.StatusSeeOther {
		t.Fatalf("delete: %d", w.Code)
	}
	if n := refCount(t, fx.db, file); n != 0 {
		t.Fatalf("refs after delete: %d", n)
	}
}

// お知らせの予約公開は、公開日時を過ぎて初めて画像も見える。カタログの代表画像も同じ。
//
// 不変条件: MEDIA-INV-1
// ユースケース: UC-MEDIA-4
func TestPublic_NewsSchedule(t *testing.T) {
	fx := setup(t)
	file := fx.add("発表の写真", 2)
	id := createdID(t, fx.post("/admin/news", url.Values{"title": {"題"}, "body": {"本文"}, "cover": {file}}), "/admin/news/")
	future := time.Now().Add(time.Hour).Unix()
	if _, err := fx.db.Exec("UPDATE news_posts SET published_at = ? WHERE id = ?", future, id); err != nil {
		t.Fatal(err)
	}
	if w := fx.post(fmt.Sprintf("/admin/news/%d/publish", id), nil); w.Code != http.StatusSeeOther {
		t.Fatalf("publish: %d", w.Code)
	}
	if got := fx.status(file); got != http.StatusNotFound {
		t.Fatalf("scheduled: %d", got)
	}
	past := time.Now().Add(-time.Minute).Unix()
	if _, err := fx.db.Exec("UPDATE news_posts SET published_at = ? WHERE id = ?", past, id); err != nil {
		t.Fatal(err)
	}
	if got := fx.status(file); got != http.StatusOK {
		t.Fatalf("after the date: %d", got)
	}
	show := fx.get(fmt.Sprintf("/news/%d", id), nil).Body.String()
	if !strings.Contains(show, `src="/media/`+file+`"`) || !strings.Contains(show, `alt="発表の写真"`) ||
		!strings.Contains(show, `width="64"`) || !strings.Contains(show, `height="48"`) {
		t.Fatalf("cover not shown: %s", show)
	}
	future = time.Now().Add(time.Hour).Unix()
	if _, err := fx.db.Exec("UPDATE news_posts SET published_at = ? WHERE id = ?", future, id); err != nil {
		t.Fatal(err)
	}
	if got := fx.status(file); got != http.StatusNotFound {
		t.Fatalf("moved to the future: %d", got)
	}

	other := fx.add("商品の写真", 3)
	item := createdID(t, fx.post("/admin/catalog", url.Values{"title": {"商品"}, "body": {"説明"}, "cover": {other}}), "/admin/catalog/")
	if got := fx.status(other); got != http.StatusNotFound {
		t.Fatalf("catalog draft: %d", got)
	}
	if w := fx.post(fmt.Sprintf("/admin/catalog/%d/publish", item), nil); w.Code != http.StatusSeeOther {
		t.Fatalf("publish: %d", w.Code)
	}
	if got := fx.status(other); got != http.StatusOK {
		t.Fatalf("catalog published: %d", got)
	}
	for _, path := range []string{"/catalog", fmt.Sprintf("/catalog/%d", item)} {
		if body := fx.get(path, nil).Body.String(); !strings.Contains(body, `src="/media/`+other+`"`) {
			t.Fatalf("%s: no cover", path)
		}
	}
}

// 公開の応答は、固定の形式でキャッシュでき、Cookieを発行しない。404はキャッシュさせない。
//
// 不変条件: MEDIA-INV-2
func TestPublic_Response(t *testing.T) {
	fx := setup(t)
	file := fx.add("外観", 1)
	missing := fx.get("/media/"+file, nil)
	if missing.Code != http.StatusNotFound || !strings.Contains(missing.Header().Get("Cache-Control"), "no-store") {
		t.Fatalf("404 must not be cached: %d %q", missing.Code, missing.Header().Get("Cache-Control"))
	}
	id := fx.page("about-us", "![外観](/media/"+file+")")
	if w := fx.post(fmt.Sprintf("/admin/pages/%d/publish", id), nil); w.Code != http.StatusSeeOther {
		t.Fatalf("publish: %d", w.Code)
	}
	w := fx.get("/media/"+file, nil)
	if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "public, max-age=86400" ||
		w.Header().Get("Content-Type") != "image/jpeg" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("response: %d %v", w.Code, w.Header())
	}
	if len(w.Result().Cookies()) != 0 {
		t.Fatal("public route must not set cookies")
	}
	if _, err := jpeg.Decode(bytes.NewReader(w.Body.Bytes())); err != nil {
		t.Fatal(err)
	}
	// 正規の形だけを認める
	id2, _, _ := strings.Cut(file, ".")
	for _, path := range []string{"/media/" + id2 + ".png", "/media/" + id2 + ".JPG", "/media/" + id2, "/media/" + strings.ToUpper(id2) + ".jpg", "/media/x.jpg"} {
		if got := fx.get(path, nil).Code; got != http.StatusNotFound {
			t.Errorf("%s: %d", path, got)
		}
	}
	// HEADは本体を返さない
	req := httptest.NewRequest(http.MethodHead, "/media/"+file, nil)
	head := httptest.NewRecorder()
	fx.routes.ServeHTTP(head, req)
	if head.Code != http.StatusOK || head.Body.Len() != 0 || head.Header().Get("Content-Length") == "" {
		t.Fatalf("head: %d %d %v", head.Code, head.Body.Len(), head.Header())
	}
}

// 追加はサイトを管理するロールだけで、ファイルを読む前にCSRFを確かめる。本文の上限はこの経路だけが広い。
//
// 不変条件: MEDIA-INV-4
// ユースケース: UC-MEDIA-1
func TestUpload_Guards(t *testing.T) {
	fx := setup(t)
	token := part{name: "csrf_token", value: fx.token}
	alt := part{name: "alt", value: "説明"}
	file := part{name: "file", file: "a.jpg", value: string(photo(t, 1))}
	count := func() int {
		var n int
		if err := fx.db.QueryRow("SELECT COUNT(*) FROM media_items").Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	for name, c := range map[string]struct {
		parts []part
		want  int
	}{
		"CSRFなし":   {[]part{alt, file}, http.StatusBadRequest},
		"CSRFが後ろ":  {[]part{alt, file, token}, http.StatusBadRequest},
		"CSRFが不一致": {[]part{{name: "csrf_token", value: "wrong"}, alt, file}, http.StatusForbidden},
		"説明が無い":    {[]part{token, file}, http.StatusBadRequest},
		"順が違う":     {[]part{token, file, alt}, http.StatusBadRequest},
		"余分な部品":    {[]part{token, alt, file, {name: "extra", value: "x"}}, http.StatusBadRequest},
		"ファイルが無い":  {[]part{token, alt}, http.StatusBadRequest},
	} {
		if w := fx.upload(fx.admin, c.parts...); w.Code != c.want {
			t.Errorf("%s: %d", name, w.Code)
		}
	}
	if n := count(); n != 0 {
		t.Fatalf("saved %d", n)
	}
	if w := fx.upload(nil, token, alt, file); w.Code != http.StatusSeeOther || !strings.Contains(w.Header().Get("Location"), "/admin/login") {
		t.Fatalf("no session: %d", w.Code)
	}
	userCookie := authtest.NewSessionWithRole(t, fx.db, "user")
	if w := fx.upload(userCookie, part{name: "csrf_token", value: "x"}, alt, file); w.Code != http.StatusForbidden {
		t.Fatalf("user role: %d", w.Code)
	}
	if w := fx.get("/admin/media", userCookie); w.Code != http.StatusForbidden {
		t.Fatalf("user list: %d", w.Code)
	}
	body, contentType := multipartBody(t, token, alt, file)
	req := httptest.NewRequest(http.MethodPost, "/admin/media", body)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	req.AddCookie(fx.admin)
	cross := httptest.NewRecorder()
	fx.routes.ServeHTTP(cross, req)
	if cross.Code != http.StatusForbidden {
		t.Fatalf("cross site: %d", cross.Code)
	}
	if n := count(); n != 0 {
		t.Fatalf("saved %d", n)
	}
	// サイト管理者は追加できる
	member := authtest.NewSessionWithRole(t, fx.db, "site_admin")
	memberToken := authtest.CSRFToken(t, fx.routes, "/admin/pages/new", member)
	if w := fx.upload(member, part{name: "csrf_token", value: memberToken}, alt, file); w.Code != http.StatusSeeOther {
		t.Fatalf("site admin: %d %s", w.Code, w.Body.String())
	}
	// 管理者の経路は未ログインに見せない
	var id string
	if err := fx.db.QueryRow("SELECT id FROM media_items").Scan(&id); err != nil {
		t.Fatal(err)
	}
	if w := fx.get("/admin/media/"+id+"/image", nil); w.Code != http.StatusSeeOther {
		t.Fatalf("admin image without session: %d", w.Code)
	}
	w := fx.get("/admin/media/"+id+"/image", fx.admin)
	if w.Code != http.StatusOK || !strings.Contains(w.Header().Get("Cache-Control"), "no-store") {
		t.Fatalf("admin image: %d %q", w.Code, w.Header().Get("Cache-Control"))
	}
}

// 上限を超える送信は、本文を読まずに理由を示して断る。ほかの経路の上限は1MiBのまま。
//
// 不変条件: MEDIA-INV-4
func TestUpload_BodyLimits(t *testing.T) {
	fx := setup(t)
	big := strings.Repeat("x", 9_000_000)
	w := fx.upload(fx.admin, part{name: "csrf_token", value: fx.token}, part{name: "alt", value: "説明"}, part{name: "file", file: "a.jpg", value: big})
	if w.Code != http.StatusRequestEntityTooLarge || !strings.Contains(w.Body.String(), "8MB") {
		t.Fatalf("over limit: %d", w.Code)
	}
	// 本文の上限の内側で、ファイルが上限を超える
	edge := strings.Repeat("x", 8_000_001)
	w = fx.upload(fx.admin, part{name: "csrf_token", value: fx.token}, part{name: "alt", value: "説明"}, part{name: "file", file: "a.jpg", value: edge})
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("file limit: %d", w.Code)
	}
	huge := strings.Repeat("x", 2<<20)
	if w := fx.post("/admin/media/"+strings.Repeat("a", 32), url.Values{"alt": {huge}}); w.Code < 400 {
		t.Fatalf("other route: %d", w.Code)
	}
}

// 中身が画像でないものは保存せず、理由を示す。
//
// 不変条件: MEDIA-INV-3
func TestUpload_RejectsContent(t *testing.T) {
	fx := setup(t)
	send := func(alt, value string) *httptest.ResponseRecorder {
		return fx.upload(fx.admin, part{name: "csrf_token", value: fx.token}, part{name: "alt", value: alt}, part{name: "file", file: "a.jpg", value: value})
	}
	for name, c := range map[string]struct {
		alt, value, want string
	}{
		"偽装":    {"説明", "<svg></svg>", "JPEGかPNG"},
		"空":     {"説明", "", "画像のファイルを選んで"},
		"説明なし":  {"  ", string(photo(t, 1)), "説明は1〜200文字"},
		"説明が長い": {strings.Repeat("あ", 201), string(photo(t, 1)), "説明は1〜200文字"},
		"改行":    {"a\nb", string(photo(t, 1)), "説明は1〜200文字"},
	} {
		w := send(c.alt, c.value)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), c.want) {
			t.Errorf("%s: %d", name, w.Code)
		}
	}
	var n int
	if err := fx.db.QueryRow("SELECT COUNT(*) FROM media_items").Scan(&n); err != nil || n != 0 {
		t.Fatalf("saved %d %v", n, err)
	}
}

// 無い画像を指す本文や代表画像は保存せず、記事も参照も変えない。
//
// 不変条件: MEDIA-INV-5
func TestSave_MissingImageRollsBack(t *testing.T) {
	fx := setup(t)
	missing := strings.Repeat("a", 32) + ".jpg"
	w := fx.post("/admin/pages", url.Values{"slug": {"about-us"}, "title": {"題"}, "body": {"![a](/media/" + missing + ")"}})
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "本文の画像が見つかりません") {
		t.Fatalf("page: %d", w.Code)
	}
	w = fx.post("/admin/news", url.Values{"title": {"題"}, "body": {"本文"}, "cover": {missing}})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("news: %d", w.Code)
	}
	w = fx.post("/admin/catalog", url.Values{"title": {"題"}, "body": {"本文"}, "cover": {missing}})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("catalog: %d", w.Code)
	}
	var count int
	if err := fx.db.QueryRow("SELECT (SELECT COUNT(*) FROM pages) + (SELECT COUNT(*) FROM news_posts) + (SELECT COUNT(*) FROM catalog_items)").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("articles left: %d", count)
	}
	// 既存の記事は、失敗した保存で変わらない
	file := fx.add("外観", 1)
	id := fx.page("about-us", "![外観](/media/"+file+")")
	w = fx.post(fmt.Sprintf("/admin/pages/%d", id), url.Values{"slug": {"about-us"}, "title": {"変更"}, "body": {"![a](/media/" + missing + ")"}})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("update: %d", w.Code)
	}
	var title string
	if err := fx.db.QueryRow("SELECT title FROM pages WHERE id = ?", id).Scan(&title); err != nil {
		t.Fatal(err)
	}
	if title != "題" || refCount(t, fx.db, file) != 1 {
		t.Fatalf("rolled back: %q refs=%d", title, refCount(t, fx.db, file))
	}
}

// 使っている画像は、公開か下書きかによらず消せず、使用先を示す。
//
// 不変条件: MEDIA-INV-5
// ユースケース: UC-MEDIA-5
func TestDelete_RefusedWhileUsed(t *testing.T) {
	fx := setup(t)
	file := fx.add("外観", 1)
	id := strings.Split(file, ".")[0]
	page := fx.page("about-us", "![外観](/media/"+file+")")
	w := fx.post("/admin/media/"+id+"/delete", nil)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "固定ページ") {
		t.Fatalf("delete used: %d", w.Code)
	}
	if body := fx.get("/admin/media", fx.admin).Body.String(); !strings.Contains(body, "1件") {
		t.Fatal("list must show usage")
	}
	if w := fx.post(fmt.Sprintf("/admin/pages/%d/delete", page), nil); w.Code != http.StatusSeeOther {
		t.Fatalf("delete page: %d", w.Code)
	}
	if body := fx.get("/admin/media", fx.admin).Body.String(); !strings.Contains(body, "未使用") {
		t.Fatal("list must show unused")
	}
	if w := fx.post("/admin/media/"+id+"/delete", nil); w.Code != http.StatusSeeOther {
		t.Fatalf("delete: %d", w.Code)
	}
}

// 起動時に参照を作り直すため、機能を外している間に変わった記事があってもずれない。
// 外した機能の参照は残り、公開中として数えず、削除は止める。
//
// 不変条件: MEDIA-INV-7
func TestStartup_RebuildsReferences(t *testing.T) {
	fx := setup(t)
	kept, dropped := fx.add("残す", 1), fx.add("外す", 2)
	id := fx.page("about-us", "![残す](/media/"+kept+")\n\n![外す](/media/"+dropped+")")
	if w := fx.post(fmt.Sprintf("/admin/pages/%d/publish", id), nil); w.Code != http.StatusSeeOther {
		t.Fatalf("publish: %d", w.Code)
	}

	// 画像機能を外している間に本文が変わった
	if _, err := fx.db.Exec("UPDATE pages SET body = ? WHERE id = ?", "![残す](/media/"+kept+")", id); err != nil {
		t.Fatal(err)
	}
	reg, routes := build(t, fx.core, io.Discard, "media", "pages")
	if err := app.RunStartup(context.Background(), reg, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
	fx.routes = routes
	if n := refCount(t, fx.db, dropped); n != 0 {
		t.Fatalf("stale ref: %d", n)
	}
	if got := fx.status(dropped); got != http.StatusNotFound {
		t.Fatalf("dropped image: %d", got)
	}
	if got := fx.status(kept); got != http.StatusOK {
		t.Fatalf("kept image: %d", got)
	}

	// 固定ページを外すと、参照は残るが公開中として数えず、削除も止める
	reg, routes = build(t, fx.core, io.Discard, "media")
	if err := app.RunStartup(context.Background(), reg, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
	fx.routes = routes
	fx.token = authtest.CSRFToken(t, routes, "/admin/media", fx.admin) // 組み直すと鍵が変わる
	if got := fx.status(kept); got != http.StatusNotFound {
		t.Fatalf("owner removed: %d", got)
	}
	if n := refCount(t, fx.db, kept); n != 1 {
		t.Fatalf("ref must stay: %d", n)
	}
	w := fx.post("/admin/media/"+strings.Split(kept, ".")[0]+"/delete", nil)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "無効な機能") {
		t.Fatalf("delete: %d", w.Code)
	}
}

// 本文の参照は、表示と同じ読み方の結果だけを数える。
//
// 不変条件: MEDIA-INV-8
func TestReferences_MatchRendering(t *testing.T) {
	fx := setup(t)
	used, escaped := fx.add("使う", 1), fx.add("使わない", 2)
	id := fx.page("about-us", "![使う](/media/"+used+")\n\n!\\[a](/media/"+escaped+")\n\n前 ![a](/media/"+escaped+")\n\n| ![a](/media/"+escaped+") |\n|---|\n")
	if refCount(t, fx.db, used) != 1 || refCount(t, fx.db, escaped) != 0 {
		t.Fatalf("refs: %d %d", refCount(t, fx.db, used), refCount(t, fx.db, escaped))
	}
	if w := fx.post(fmt.Sprintf("/admin/pages/%d/publish", id), nil); w.Code != http.StatusSeeOther {
		t.Fatalf("publish: %d", w.Code)
	}
	body := fx.get("/about-us", nil).Body.String()
	if !strings.Contains(body, `<img class="content-image" src="/media/`+used+`" alt="使う" width="64" height="48"`) {
		t.Fatalf("image not rendered: %s", body)
	}
	if strings.Contains(body, `<img class="content-image" src="/media/`+escaped) {
		t.Fatal("not an image line")
	}
}

// プレビューは管理者の経路で描き、挿入のボタンは保存せずに本文へ足す。
//
// ユースケース: UC-MEDIA-3
func TestPages_InsertAndPreview(t *testing.T) {
	fx := setup(t)
	file := fx.add(`説明]と\記号`, 1)
	if body := fx.get("/admin/pages/new", fx.admin).Body.String(); !strings.Contains(body, "画像を挿入") || !strings.Contains(body, `value="`+file+`"`) {
		t.Fatal("picker missing")
	}
	w := fx.post("/admin/pages/preview", url.Values{"slug": {"about-us"}, "title": {"題"}, "body": {"本文"}, "insert_media": {file}})
	if w.Code != http.StatusOK {
		t.Fatalf("insert: %d", w.Code)
	}
	id := strings.Split(file, ".")[0]
	if !strings.Contains(w.Body.String(), `src="/admin/media/`+id+`/image"`) || !strings.Contains(w.Body.String(), `alt="説明]と\記号"`) {
		t.Fatalf("preview: %s", w.Body.String())
	}
	var n int
	if err := fx.db.QueryRow("SELECT COUNT(*) FROM pages").Scan(&n); err != nil || n != 0 {
		t.Fatalf("saved: %d %v", n, err)
	}
	w = fx.post("/admin/pages/preview", url.Values{"slug": {"about-us"}, "title": {"題"}, "body": {"本文"}, "insert_media": {strings.Repeat("a", 32) + ".jpg"}})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("missing image: %d", w.Code)
	}
}

// 選択の一覧の範囲の外の代表画像も画面に出し、本文だけの保存で消えない。ページ送りは保存しない。
//
// 不変条件: MEDIA-INV-9
func TestCover_KeptOutsideThePage(t *testing.T) {
	fx := setup(t)
	oldest := fx.add("最初の画像", 0)
	for i := 1; i <= 13; i++ {
		fx.add(fmt.Sprintf("画像%d", i), uint8(i))
	}
	if _, err := fx.db.Exec("UPDATE media_items SET created_at = created_at - 1000 WHERE id = ?", strings.Split(oldest, ".")[0]); err != nil {
		t.Fatal(err)
	}
	id := createdID(t, fx.post("/admin/news", url.Values{"title": {"題"}, "body": {"本文"}, "cover": {oldest}}), "/admin/news/")
	edit := fx.get(fmt.Sprintf("/admin/news/%d", id), fx.admin).Body.String()
	if !regexp.MustCompile(`value="` + oldest + `" checked`).MatchString(edit) {
		t.Fatal("selected image must stay in the form")
	}
	if !strings.Contains(edit, `name="media_page" value="2"`) {
		t.Fatal("pager missing")
	}
	// ページ送りは保存しない
	w := fx.post(fmt.Sprintf("/admin/news/%d", id), url.Values{"title": {"変更"}, "body": {"本文"}, "cover": {oldest}, "media_page": {"2"}})
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `value="変更"`) {
		t.Fatalf("page: %d", w.Code)
	}
	var title string
	if err := fx.db.QueryRow("SELECT title FROM news_posts WHERE id = ?", id).Scan(&title); err != nil || title != "題" {
		t.Fatalf("saved by pager: %q %v", title, err)
	}
	if w := fx.post(fmt.Sprintf("/admin/news/%d", id), url.Values{"title": {"本文だけ"}, "body": {"変更"}, "cover": {oldest}}); w.Code != http.StatusSeeOther {
		t.Fatalf("save: %d", w.Code)
	}
	var cover string
	if err := fx.db.QueryRow("SELECT cover_file FROM news_posts WHERE id = ?", id).Scan(&cover); err != nil || cover != oldest {
		t.Fatalf("cover: %q %v", cover, err)
	}
	// 「なし」に戻すと参照も消える
	if w := fx.post(fmt.Sprintf("/admin/news/%d", id), url.Values{"title": {"題"}, "body": {"本文"}, "cover": {""}}); w.Code != http.StatusSeeOther {
		t.Fatalf("clear: %d", w.Code)
	}
	if n := refCount(t, fx.db, oldest); n != 0 {
		t.Fatalf("refs: %d", n)
	}
}

// ファイル名・説明・画像のIDをログに出さない。
//
// 不変条件: MEDIA-INV-10
func TestLogs_DoNotCarryContent(t *testing.T) {
	fx := setup(t)
	w := fx.upload(fx.admin, part{name: "csrf_token", value: fx.token}, part{name: "alt", value: "ログ確認の説明"},
		part{name: "file", file: "secret-name.jpg", value: string(photo(t, 1))})
	if w.Code != http.StatusSeeOther {
		t.Fatalf("upload: %d", w.Code)
	}
	id := strings.TrimPrefix(strings.Split(w.Header().Get("Location"), "?")[0], "/admin/media/")
	fx.get("/admin/media/"+id, fx.admin)
	fx.get("/media/"+id+".jpg", nil)
	logs := fx.logs.String()
	for _, secret := range []string{"secret-name", "ログ確認の説明", id} {
		if strings.Contains(logs, secret) {
			t.Fatalf("log contains %q", secret)
		}
	}
}

// 画像機能が無い構成でも、本文の画像の行は説明の文字になり、記事を保存できる。代表画像の値は変えない。
//
// 不変条件: MEDIA-INV-11
func TestWithoutMedia_ArticlesKeepWorking(t *testing.T) {
	fx := setup(t, "pages", "news", "catalog")
	missing := strings.Repeat("a", 32) + ".jpg"
	id := fx.page("about-us", "本文\n\n![本社の外観](/media/"+missing+")")
	if w := fx.post(fmt.Sprintf("/admin/pages/%d/publish", id), nil); w.Code != http.StatusSeeOther {
		t.Fatalf("publish: %d", w.Code)
	}
	body := fx.get("/about-us", nil).Body.String()
	if !strings.Contains(body, "<p>本社の外観</p>") || strings.Contains(body, "<img class=\"content-image\"") {
		t.Fatalf("alt text expected: %s", body)
	}
	if strings.Contains(fx.get("/admin/pages/new", fx.admin).Body.String(), "画像を挿入") {
		t.Fatal("picker must be hidden")
	}
	post := createdID(t, fx.post("/admin/news", url.Values{"title": {"題"}, "body": {"本文"}}), "/admin/news/")
	if _, err := fx.db.Exec("UPDATE news_posts SET cover_file = ? WHERE id = ?", missing, post); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(fx.get(fmt.Sprintf("/admin/news/%d", post), fx.admin).Body.String(), "代表画像") {
		t.Fatal("cover must be hidden")
	}
	if w := fx.post(fmt.Sprintf("/admin/news/%d", post), url.Values{"title": {"変更"}, "body": {"本文"}}); w.Code != http.StatusSeeOther {
		t.Fatalf("save: %d", w.Code)
	}
	var cover string
	if err := fx.db.QueryRow("SELECT cover_file FROM news_posts WHERE id = ?", post).Scan(&cover); err != nil || cover != missing {
		t.Fatalf("cover changed: %q %v", cover, err)
	}
	item := createdID(t, fx.post("/admin/catalog", url.Values{"title": {"題"}, "body": {"本文"}}), "/admin/catalog/")
	if _, err := fx.db.Exec("UPDATE catalog_items SET cover_file = ? WHERE id = ?", missing, item); err != nil {
		t.Fatal(err)
	}
	if w := fx.post(fmt.Sprintf("/admin/catalog/%d", item), url.Values{"title": {"変更"}, "body": {"本文"}}); w.Code != http.StatusSeeOther {
		t.Fatalf("save: %d", w.Code)
	}
	if err := fx.db.QueryRow("SELECT cover_file FROM catalog_items WHERE id = ?", item).Scan(&cover); err != nil || cover != missing {
		t.Fatalf("catalog cover changed: %q %v", cover, err)
	}
}

// 確認の後に代表画像が変わったら、MCPの保存は通らない。MCPの更新は代表画像を変えない。
//
// 不変条件: MEDIA-INV-9
func TestMCP_CoverConflict(t *testing.T) {
	fx := setup(t)
	first, second := fx.add("一", 1), fx.add("二", 2)
	id := createdID(t, fx.post("/admin/news", url.Values{"title": {"題"}, "body": {"本文"}, "cover": {first}}), "/admin/news/")
	posts := news.NewPostStore(fx.db)
	ctx := context.Background()
	cur, err := posts.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := posts.UpdateIfUnchanged(ctx, cur, "MCP", "本文", time.Time{}); err != nil {
		t.Fatal(err)
	}
	if cur, err = posts.Get(ctx, id); err != nil || cur.CoverFile != first {
		t.Fatalf("MCP must keep the cover: %q %v", cur.CoverFile, err)
	}
	if w := fx.post(fmt.Sprintf("/admin/news/%d", id), url.Values{"title": {"MCP"}, "body": {"本文"}, "cover": {second}}); w.Code != http.StatusSeeOther {
		t.Fatalf("change: %d", w.Code)
	}
	if err := posts.UpdateIfUnchanged(ctx, cur, "古い", "本文", time.Time{}); !errors.Is(err, features.ErrMCPConflict) {
		t.Fatalf("update: %v", err)
	}
	if err := posts.SetPublishedIfUnchanged(ctx, cur, true); !errors.Is(err, features.ErrMCPConflict) {
		t.Fatalf("publish: %v", err)
	}
}

// 容量の上限の設定が不正なら、起動を止める。
func TestConfig_TotalLimit(t *testing.T) {
	core, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer core.Close()
	t.Setenv("MEDIA_TOTAL_LIMIT_MB", "abc")
	if err := media.NewFeature(slog.New(slog.NewTextHandler(io.Discard, nil))).Init(core.DB()); err == nil {
		t.Fatal("invalid limit must stop the start")
	}
}
