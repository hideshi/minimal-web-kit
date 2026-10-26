package cms

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"minimal-web-kit/features"
	"minimal-web-kit/internal/web"
	"minimal-web-kit/internal/web/form"
	"minimal-web-kit/internal/web/notice"
	"minimal-web-kit/internal/web/page"
)

const adminPageSize = 20

// 管理画面の完了通知。クエリの値は固定の語だけを受け付け、入力値は表示しない。
var adminNotices = notice.Messages{
	"created":     "お知らせを下書きとして保存しました。",
	"saved":       "変更を保存しました。",
	"published":   "お知らせを公開しました。",
	"unpublished": "お知らせを非公開（下書き）に戻しました。",
	"deleted":     "お知らせを削除しました。",
}

// postIDAttr は記事IDをログに記録します。IDはサーバーが採番した値で、利用者の入力を含みません。
func postIDAttr(id int64) slog.Attr { return slog.Int64("post_id", id) }

func setNotice(data map[string]any, r *http.Request) {
	data["Notice"] = adminNotices.From(r)
}

// adminPostID は経路の {id} を解析し、不正なら404/400を返します。
func adminPostID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, status := parsePostID(r.PathValue("id"))
	if status != 0 {
		http.Error(w, http.StatusText(status), status)
		return 0, false
	}
	return id, true
}

// --- お知らせの管理 ---

func (f *Feature) handleAdminPosts(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	pg, ok := page.Parse(r, adminPageSize, maxOffset/adminPageSize)
	if !ok {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	data := f.pageData(s, "お知らせの管理")
	posts, err := f.posts.ListAll(r.Context(), pg.Limit(), pg.Offset())
	if err != nil {
		f.logger.Error("cms list posts failed", f.requestID(w))
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	posts = page.Trim(&pg, posts)
	pg.Base, pg.Label, pg.PrevText, pg.NextText = "/admin/posts", "お知らせの一覧のページ送り", "新しい更新", "過去の更新"
	data["Page"] = pg
	for i := range posts {
		posts[i] = adminLocalTime(posts[i])
	}
	data["Posts"] = posts
	setNotice(data, r)
	f.renderAdmin(w, "admin_posts.html", data)
}

// adminLocalTime は表示用に、日時をサーバーのタイムゾーンへ変換します。
func adminLocalTime(p Post) Post {
	p = localTime(p)
	p.CreatedAt = p.CreatedAt.In(time.Local)
	p.UpdatedAt = p.UpdatedAt.In(time.Local)
	return p
}

func (f *Feature) handleNewPostPage(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	data := f.pageData(s, "お知らせの作成")
	data["Post"] = Post{}
	data["Form"] = postForm{}
	f.renderAdmin(w, "admin_post_form.html", data)
}

// datetimeInputLayout は <input type="datetime-local"> の値の形式です（サーバーのタイムゾーンで解釈する）。
const datetimeInputLayout = "2006-01-02T15:04"

// postForm は記事のフォームの入力です。誤りがあれば、入力をそのまま戻して修正を求めます。
type postForm struct {
	Title, Body, PublishedAt string
}

// Fields は、共通の画面部品 "field" で描く入力欄を返します。
func (pf postForm) Fields() []form.Field {
	return []form.Field{
		{ID: "post-title", Name: "title", Label: "タイトル", Value: pf.Title, MaxLength: maxTitleRunes, Required: true,
			Hint: "1〜200文字。改行は使えません。"},
		// 本文は maxlength を付けない（ブラウザはUTF-16で数えるため、絵文字等でサーバーの文字数と食い違う）
		{ID: "post-body", Name: "body", Label: "本文", Value: pf.Body, Rows: 14,
			Hint: "5万文字以内のプレーンテキスト。空行で段落を分けます。HTMLは文字としてそのまま表示されます。個人情報は書かないでください。"},
		{ID: "post-published-at", Name: "published_at", Label: "公開日時（任意）", Value: pf.PublishedAt, Type: "datetime-local",
			Hint: "公開ページの表示と並び順に使います。空欄のままなら、初めて公開した日時が入ります。未来の日時を指定して「公開を予約する」を押すと、その日時になってから公開ページとAPIに表示されます。時刻はサーバーのタイムゾーンです。"},
	}
}

func readPostForm(r *http.Request) postForm {
	return postForm{Title: r.PostForm.Get("title"), Body: r.PostForm.Get("body"), PublishedAt: r.PostForm.Get("published_at")}
}

// parsePublishedAt は公開日時の入力を解析します。空ならゼロ値（変更しない）を返します。
func (f *Feature) parsePublishedAt(raw string) (time.Time, string) {
	if raw == "" {
		return time.Time{}, ""
	}
	// ブラウザによっては秒まで送るため、両方の形式を受け付ける
	t, err := time.ParseInLocation(datetimeInputLayout, raw, time.Local)
	if err != nil {
		t, err = time.ParseInLocation(datetimeInputLayout+":05", raw, time.Local)
	}
	if err != nil || t.Year() < 1970 {
		return time.Time{}, "公開日時の形式が正しくありません。"
	}
	return t, ""
}

func (f *Feature) handleCreatePost(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	pf := readPostForm(r)
	publishedAt, msg := f.parsePublishedAt(pf.PublishedAt)
	if msg != "" {
		f.renderPostFormError(w, r, s, "お知らせの作成", Post{}, pf, msg)
		return
	}
	id, err := f.posts.CreateWithDate(r.Context(), pf.Title, pf.Body, publishedAt)
	if errors.Is(err, ErrInvalidInput) {
		f.renderPostFormError(w, r, s, "お知らせの作成", Post{}, pf, postInputError)
		return
	}
	if err != nil {
		f.logger.Error("cms create post failed", f.requestID(w))
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	f.logger.Info("cms post created", f.requestID(w), postIDAttr(id))
	notice.Redirect(w, r, "/admin/posts/"+strconv.FormatInt(id, 10), "created")
}

const postInputError = "タイトルは1〜200文字（改行なし）、本文は5万文字以内で入力してください。制御文字は使えません。"

// renderPostFormError は入力をそのまま戻して、修正を求めるフォームを返します。
// p は保存済みの記事（作成時はゼロ値）で、状態の表示に使います。
func (f *Feature) renderPostFormError(w http.ResponseWriter, r *http.Request, s features.AdminSession, title string, p Post, pf postForm, msg string) {
	data := f.pageData(s, title)
	data["Post"] = p
	data["Form"] = pf
	data["Error"] = msg
	f.renderAdminStatus(w, http.StatusBadRequest, "admin_post_form.html", data)
}

// formFromPost は保存済みの記事から、フォームの初期値を作ります。
func formFromPost(p Post) postForm {
	pf := postForm{Title: p.Title, Body: p.Body}
	if !p.PublishedAt.IsZero() {
		pf.PublishedAt = p.PublishedAt.In(time.Local).Format(datetimeInputLayout)
	}
	return pf
}

func (f *Feature) handleEditPostPage(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	id, ok := adminPostID(w, r)
	if !ok {
		return
	}
	data := f.pageData(s, "お知らせの編集")
	p, err := f.posts.Get(r.Context(), id)
	if errors.Is(err, ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		f.logger.Error("cms get post failed", f.requestID(w))
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	data["Post"] = adminLocalTime(p)
	data["Form"] = formFromPost(p)
	setNotice(data, r)
	f.renderAdmin(w, "admin_post_form.html", data)
}

func (f *Feature) handleUpdatePost(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	id, ok := adminPostID(w, r)
	if !ok {
		return
	}
	pf := readPostForm(r)
	publishedAt, msg := f.parsePublishedAt(pf.PublishedAt)
	err := ErrInvalidInput
	if msg == "" {
		err = f.posts.UpdateWithDate(r.Context(), id, pf.Title, pf.Body, publishedAt)
		msg = postInputError
	}
	if errors.Is(err, ErrInvalidInput) {
		p, gerr := f.posts.Get(r.Context(), id)
		if gerr != nil {
			http.NotFound(w, r)
			return
		}
		f.renderPostFormError(w, r, s, "お知らせの編集", adminLocalTime(p), pf, msg)
		return
	}
	f.afterPostChange(w, r, id, err, "cms post updated", "saved")
}

// postAction は公開・非公開の切り替えなど、入力を伴わない操作のハンドラを作ります。
func (f *Feature) postAction(action func(*PostStore, *http.Request, int64) error, event, key string) func(http.ResponseWriter, *http.Request, features.AdminSession) {
	return func(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
		id, ok := adminPostID(w, r)
		if !ok {
			return
		}
		f.afterPostChange(w, r, id, action(f.posts, r, id), event, key)
	}
}

func (f *Feature) afterPostChange(w http.ResponseWriter, r *http.Request, id int64, err error, event, key string) {
	if errors.Is(err, ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		f.logger.Error("cms post change failed", f.requestID(w))
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	f.logger.Info(event, f.requestID(w), postIDAttr(id))
	notice.Redirect(w, r, "/admin/posts/"+strconv.FormatInt(id, 10), key)
}

func (f *Feature) handleDeletePostPage(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	id, ok := adminPostID(w, r)
	if !ok {
		return
	}
	data := f.pageData(s, "お知らせの削除")
	p, err := f.posts.Get(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	data["Post"] = p
	f.renderAdmin(w, "admin_post_delete.html", data)
}

func (f *Feature) handleDeletePost(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	id, ok := adminPostID(w, r)
	if !ok {
		return
	}
	err := f.posts.Delete(r.Context(), id)
	if errors.Is(err, ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		f.logger.Error("cms delete post failed", f.requestID(w))
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	f.logger.Info("cms post deleted", f.requestID(w), postIDAttr(id))
	notice.Redirect(w, r, "/admin/posts", "deleted")
}

func (f *Feature) registerPostAdminRoutes(mux *web.IsolatedMux) {
	mux.HandleFunc("GET /admin/posts", f.admin.Page(f.handleAdminPosts))
	mux.HandleFunc("GET /admin/posts/new", f.admin.Page(f.handleNewPostPage))
	mux.HandleFunc("POST /admin/posts", f.admin.Form(f.handleCreatePost))
	mux.HandleFunc("GET /admin/posts/{id}", f.admin.Page(f.handleEditPostPage))
	mux.HandleFunc("POST /admin/posts/{id}", f.admin.Form(f.handleUpdatePost))
	mux.HandleFunc("POST /admin/posts/{id}/publish", f.admin.Form(f.postAction(func(s *PostStore, r *http.Request, id int64) error {
		return s.Publish(r.Context(), id)
	}, "cms post published", "published")))
	mux.HandleFunc("POST /admin/posts/{id}/unpublish", f.admin.Form(f.postAction(func(s *PostStore, r *http.Request, id int64) error {
		return s.Unpublish(r.Context(), id)
	}, "cms post unpublished", "unpublished")))
	mux.HandleFunc("GET /admin/posts/{id}/delete", f.admin.Page(f.handleDeletePostPage))
	mux.HandleFunc("POST /admin/posts/{id}/delete", f.admin.Form(f.handleDeletePost))
}
