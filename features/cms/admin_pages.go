package cms

import (
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"minimal-web-kit/internal/web"
)

const adminPageSize = 20

// 管理画面の完了通知。クエリの値は固定の語だけを受け付け、入力値は表示しない。
var adminNotices = map[string]string{
	"created":     "お知らせを下書きとして保存しました。",
	"saved":       "変更を保存しました。",
	"published":   "お知らせを公開しました。",
	"unpublished": "お知らせを非公開（下書き）に戻しました。",
	"deleted":     "お知らせを削除しました。",
	"added":       "パスキーを追加しました。",
	"renamed":     "端末名を変更しました。",
	"removed":     "パスキーを削除しました。ほかの端末でのログイン状態も解除しました。",
}

// postIDAttr は記事IDをログに記録します。IDはサーバーが採番した値で、利用者の入力を含みません。
func postIDAttr(id int64) slog.Attr { return slog.Int64("post_id", id) }

func setNotice(data map[string]any, r *http.Request) {
	if msg, ok := adminNotices[r.URL.Query().Get("notice")]; ok {
		data["Notice"] = msg
	}
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

func (f *Feature) handleAdminPosts(w http.ResponseWriter, r *http.Request) {
	page, ok := parseIntParam(r.URL.Query().Get("page"), 1, 1, maxOffset/adminPageSize)
	if !ok {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	data, _, ok := f.adminPage(w, r, "お知らせの管理", "posts")
	if !ok {
		return
	}
	posts, err := f.posts.ListAll(r.Context(), adminPageSize+1, (page-1)*adminPageSize)
	if err != nil {
		f.logger.Error("cms list posts failed", f.requestID(w))
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	if len(posts) > adminPageSize {
		posts = posts[:adminPageSize]
		data["NextPage"] = page + 1
	}
	if page > 1 {
		data["PrevPage"] = page - 1
	}
	for i := range posts {
		posts[i] = adminLocalTime(posts[i])
	}
	data["Posts"] = posts
	setNotice(data, r)
	f.renderAdmin(w, "admin_posts.html", data, false)
}

// adminLocalTime は表示用に、日時をサーバーのタイムゾーンへ変換します。
func adminLocalTime(p Post) Post {
	p = localTime(p)
	p.CreatedAt = p.CreatedAt.In(time.Local)
	p.UpdatedAt = p.UpdatedAt.In(time.Local)
	return p
}

func (f *Feature) handleNewPostPage(w http.ResponseWriter, r *http.Request) {
	data, _, ok := f.adminPage(w, r, "お知らせの作成", "posts")
	if !ok {
		return
	}
	f.renderAdmin(w, "admin_post_form.html", data, false)
}

func (f *Feature) handleCreatePost(w http.ResponseWriter, r *http.Request, session string) {
	title, body := r.PostForm.Get("title"), r.PostForm.Get("body")
	id, err := f.posts.Create(r.Context(), title, body)
	if errors.Is(err, ErrInvalidInput) {
		f.renderPostFormError(w, r, "お知らせの作成", Post{Title: title, Body: body})
		return
	}
	if err != nil {
		f.logger.Error("cms create post failed", f.requestID(w))
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	f.logger.Info("cms post created", f.requestID(w), postIDAttr(id))
	http.Redirect(w, r, "/admin/posts/"+strconv.FormatInt(id, 10)+"?notice=created", http.StatusSeeOther)
}

// renderPostFormError は入力をそのまま戻して、修正を求めるフォームを返します。
func (f *Feature) renderPostFormError(w http.ResponseWriter, r *http.Request, title string, p Post) {
	data, _, ok := f.adminPage(w, r, title, "posts")
	if !ok {
		return
	}
	data["Post"] = p
	data["Error"] = "タイトルは1〜200文字（改行なし）、本文は5万文字以内で入力してください。制御文字は使えません。"
	f.renderAdminStatus(w, http.StatusBadRequest, "admin_post_form.html", data, false)
}

func (f *Feature) handleEditPostPage(w http.ResponseWriter, r *http.Request) {
	id, ok := adminPostID(w, r)
	if !ok {
		return
	}
	data, _, ok := f.adminPage(w, r, "お知らせの編集", "posts")
	if !ok {
		return
	}
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
	setNotice(data, r)
	f.renderAdmin(w, "admin_post_form.html", data, false)
}

func (f *Feature) handleUpdatePost(w http.ResponseWriter, r *http.Request, session string) {
	id, ok := adminPostID(w, r)
	if !ok {
		return
	}
	title, body := r.PostForm.Get("title"), r.PostForm.Get("body")
	err := f.posts.Update(r.Context(), id, title, body)
	if errors.Is(err, ErrInvalidInput) {
		p, gerr := f.posts.Get(r.Context(), id)
		if gerr != nil {
			http.NotFound(w, r)
			return
		}
		p.Title, p.Body = title, body
		f.renderPostFormError(w, r, "お知らせの編集", adminLocalTime(p))
		return
	}
	f.afterPostChange(w, r, id, err, "cms post updated", "saved")
}

// postAction は公開・非公開の切り替えなど、入力を伴わない操作のハンドラを作ります。
func (f *Feature) postAction(action func(*PostStore, *http.Request, int64) error, event, notice string) func(http.ResponseWriter, *http.Request, string) {
	return func(w http.ResponseWriter, r *http.Request, session string) {
		id, ok := adminPostID(w, r)
		if !ok {
			return
		}
		f.afterPostChange(w, r, id, action(f.posts, r, id), event, notice)
	}
}

func (f *Feature) afterPostChange(w http.ResponseWriter, r *http.Request, id int64, err error, event, notice string) {
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
	http.Redirect(w, r, "/admin/posts/"+strconv.FormatInt(id, 10)+"?notice="+notice, http.StatusSeeOther)
}

func (f *Feature) handleDeletePostPage(w http.ResponseWriter, r *http.Request) {
	id, ok := adminPostID(w, r)
	if !ok {
		return
	}
	data, _, ok := f.adminPage(w, r, "お知らせの削除", "posts")
	if !ok {
		return
	}
	p, err := f.posts.Get(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	data["Post"] = p
	f.renderAdmin(w, "admin_post_delete.html", data, false)
}

func (f *Feature) handleDeletePost(w http.ResponseWriter, r *http.Request, session string) {
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
	http.Redirect(w, r, "/admin/posts?notice=deleted", http.StatusSeeOther)
}

func (f *Feature) registerPostAdminRoutes(mux *web.IsolatedMux) {
	mux.HandleFunc("GET /admin/posts", f.handleAdminPosts)
	mux.HandleFunc("GET /admin/posts/new", f.handleNewPostPage)
	mux.HandleFunc("POST /admin/posts", f.adminForm(f.handleCreatePost))
	mux.HandleFunc("GET /admin/posts/{id}", f.handleEditPostPage)
	mux.HandleFunc("POST /admin/posts/{id}", f.adminForm(f.handleUpdatePost))
	mux.HandleFunc("POST /admin/posts/{id}/publish", f.adminForm(f.postAction(func(s *PostStore, r *http.Request, id int64) error {
		return s.Publish(r.Context(), id)
	}, "cms post published", "published")))
	mux.HandleFunc("POST /admin/posts/{id}/unpublish", f.adminForm(f.postAction(func(s *PostStore, r *http.Request, id int64) error {
		return s.Unpublish(r.Context(), id)
	}, "cms post unpublished", "unpublished")))
	mux.HandleFunc("GET /admin/posts/{id}/delete", f.handleDeletePostPage)
	mux.HandleFunc("POST /admin/posts/{id}/delete", f.adminForm(f.handleDeletePost))
}

// --- パスキーの管理 ---

// credentialParam は経路の {id}（credential IDのbase64url）を解析します。
func credentialParam(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	id, err := base64.RawURLEncoding.Strict().DecodeString(r.PathValue("id"))
	if err != nil || len(id) == 0 || len(id) > maxCredentialIDLen {
		http.NotFound(w, r)
		return nil, false
	}
	return id, true
}

// renderPasskeys はパスキー管理画面を返します。パスキーの追加にWebAuthnを使うため、認証画面と同じCSPで返します。
func (f *Feature) renderPasskeys(w http.ResponseWriter, r *http.Request, status int, errMsg string) {
	data, _, ok := f.adminPage(w, r, "パスキーの管理", "passkeys")
	if !ok {
		return
	}
	creds, err := f.admin.listCredentials(r.Context())
	if err != nil {
		f.logger.Error("cms list passkeys failed", f.requestID(w))
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	active := 0
	for _, c := range creds {
		if !c.Disabled() {
			active++
		}
	}
	data["Credentials"] = creds
	data["ActiveCount"] = active
	data["Error"] = errMsg
	setNotice(data, r)
	f.renderAdminStatus(w, status, "admin_passkeys.html", data, true)
}

func (f *Feature) handlePasskeysPage(w http.ResponseWriter, r *http.Request) {
	f.renderPasskeys(w, r, http.StatusOK, "")
}

func (f *Feature) handleRenamePasskey(w http.ResponseWriter, r *http.Request, session string) {
	id, ok := credentialParam(w, r)
	if !ok {
		return
	}
	err := f.admin.renameCredential(r.Context(), id, r.PostForm.Get("label"))
	switch {
	case errors.Is(err, ErrInvalidInput):
		f.renderPasskeys(w, r, http.StatusBadRequest, errorMessages["label_invalid"])
	case errors.Is(err, ErrNotFound):
		http.NotFound(w, r)
	case err != nil:
		f.logger.Error("cms rename passkey failed", f.requestID(w))
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	default:
		f.logger.Info("cms passkey renamed", f.requestID(w))
		http.Redirect(w, r, "/admin/passkeys?notice=renamed", http.StatusSeeOther)
	}
}

func (f *Feature) handleDeletePasskeyPage(w http.ResponseWriter, r *http.Request) {
	id, ok := credentialParam(w, r)
	if !ok {
		return
	}
	data, _, ok := f.adminPage(w, r, "パスキーの削除", "passkeys")
	if !ok {
		return
	}
	c, err := f.admin.getCredentialInfo(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	data["Credential"] = c
	f.renderAdmin(w, "admin_passkey_delete.html", data, false)
}

func (f *Feature) handleDeletePasskey(w http.ResponseWriter, r *http.Request, session string) {
	id, ok := credentialParam(w, r)
	if !ok {
		return
	}
	err := f.admin.deleteCredential(r.Context(), id, session)
	switch {
	case errors.Is(err, ErrLastCredential):
		f.renderPasskeys(w, r, http.StatusConflict, "最後の有効なパスキーは削除できません。先に別のパスキーを追加してください。")
	case errors.Is(err, ErrNotFound):
		http.NotFound(w, r)
	case err != nil:
		f.logger.Error("cms delete passkey failed", f.requestID(w))
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	default:
		f.logger.Info("cms passkey deleted", f.requestID(w))
		http.Redirect(w, r, "/admin/passkeys?notice=removed", http.StatusSeeOther)
	}
}

func (f *Feature) registerPasskeyAdminRoutes(mux *web.IsolatedMux) {
	mux.HandleFunc("GET /admin/passkeys", f.handlePasskeysPage)
	mux.HandleFunc("POST /admin/passkeys/{id}/label", f.adminForm(f.handleRenamePasskey))
	mux.HandleFunc("GET /admin/passkeys/{id}/delete", f.handleDeletePasskeyPage)
	mux.HandleFunc("POST /admin/passkeys/{id}/delete", f.adminForm(f.handleDeletePasskey))
}
