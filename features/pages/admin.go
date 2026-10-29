package pages

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"minimal-web-kit/features"
	"minimal-web-kit/internal/web"
	"minimal-web-kit/internal/web/form"
	"minimal-web-kit/internal/web/notice"
	"minimal-web-kit/internal/web/richtext"
)

var adminPageNames = []string{"admin_pages.html", "admin_page_form.html", "admin_page_delete.html"}

var messages = notice.Messages{"created": "作成しました。", "saved": "変更を保存しました。", "published": "公開しました。", "unpublished": "下書きに戻しました。", "deleted": "削除しました。"}

var fieldErrors = form.FieldErrors{
	"slug":          "URLは、英小文字・数字・ハイフンで2〜40文字にしてください（ハイフンで始めたり終えたりはできません）。",
	"slug_reserved": "このURLは、サイトの他の機能が使っているため使えません。別のURLにしてください。",
	"slug_taken":    "このURLは、他のページが使っています。別のURLにしてください。",
	"title":         "タイトルは1〜200文字で、改行を含めずに入力してください。",
	"body":          "本文は5万文字以内で入力してください。",
	"body_limit":    "表が大きすぎるか、見出しより列の多い行があります。表は20列・200行まで、行の列は見出しの列以下にしてください。",
	"nav_order":     "ナビに出す順は、0〜999の数字で入力してください（ナビに出さない場合は空にします）。",
}

// SetAdmin は管理者認証を受け取り、管理画面のテンプレートを解析します。
func (f *Feature) SetAdmin(admin features.Admin) error {
	pages, err := web.ParsePages(admin.Layout(), viewsFS, adminPageNames...)
	if err != nil {
		return err
	}
	f.admin, f.adminPages = admin, pages
	return nil
}

// AdminNavLinks は管理画面のナビに「固定ページの管理」を加えます（管理者とメンバー）。
func (f *Feature) AdminNavLinks() []web.NavLink {
	return []web.NavLink{{Label: "固定ページの管理", Href: "/admin/pages"}}
}

// registerAdmin は管理画面の経路を登録します（PAGES-INV-4）。経路は文字列のリテラルで書く。
func (f *Feature) registerAdmin(mux *web.IsolatedMux) {
	mux.HandleFunc("GET /admin/pages", f.admin.Page(f.handleList))
	mux.HandleFunc("GET /admin/pages/new", f.admin.Page(f.handleNew))
	mux.HandleFunc("POST /admin/pages", f.admin.Form(f.handleSave))
	mux.HandleFunc("POST /admin/pages/preview", f.admin.Form(f.handlePreview))
	mux.HandleFunc("GET /admin/pages/{id}", f.admin.Page(f.handleEdit))
	mux.HandleFunc("POST /admin/pages/{id}", f.admin.Form(f.handleSave))
	mux.HandleFunc("POST /admin/pages/{id}/publish", f.admin.Form(f.handlePublish(true)))
	mux.HandleFunc("POST /admin/pages/{id}/unpublish", f.admin.Form(f.handlePublish(false)))
	mux.HandleFunc("GET /admin/pages/{id}/delete", f.admin.Page(f.handleDeletePage))
	mux.HandleFunc("POST /admin/pages/{id}/delete", f.admin.Form(f.handleDelete))
}

func (f *Feature) renderAdmin(w http.ResponseWriter, status int, name string, data map[string]any) {
	var buf bytes.Buffer
	if err := f.adminPages[name].ExecuteTemplate(&buf, "layout", data); err != nil {
		f.logger.Error("pages render admin failed")
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}

func (f *Feature) failed(w http.ResponseWriter, r *http.Request, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, ErrNotFound):
		http.NotFound(w, r)
	default:
		f.logger.Error("pages admin failed", web.RequestIDAttr(w))
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
	return true
}

func pageID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		http.NotFound(w, r)
		return 0, false
	}
	return id, true
}

func (f *Feature) handleList(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	list, err := f.store.List(r.Context())
	if f.failed(w, r, err) {
		return
	}
	data := s.PageData("固定ページの管理", "/admin/pages")
	data["Pages"] = list
	data["Notice"] = messages.From(r)
	f.renderAdmin(w, http.StatusOK, "admin_pages.html", data)
}

// pageForm は作成・編集の画面の入力と、欄のエラーです。
type pageForm struct {
	Input
	Invalid string
}

func (v pageForm) err(names ...string) string { return fieldErrors.For(v.Invalid, names...) }

func (v pageForm) Fields() []form.Field {
	return []form.Field{
		{ID: "page-title", Name: "title", Label: "タイトル", Value: v.Title, Required: true, MaxLength: maxTitleRunes, Error: v.err("title"), Hint: "1〜200文字、改行なし。ページの見出しになります。"},
		{ID: "page-slug", Name: "slug", Label: "URL", Value: v.Slug, Required: true, MaxLength: 40, Error: v.err("slug", "slug_reserved", "slug_taken"),
			Hint: "/ の後ろの部分（例: about → /about）。英小文字・数字・ハイフンで2〜40文字。公開中に変えると、前のURLは見られなくなります。"},
		{ID: "page-body", Name: "body", Label: "本文", Value: v.Body, Rows: 16, Error: v.err("body", "body_limit"),
			Hint: "5万文字以内。書き方は下の「本文の書き方」を参照。公開する文章なので、個人情報は書かないでください。"},
		{ID: "page-nav-order", Name: "nav_order", Label: "ナビに出す順（任意）", Value: v.NavOrder, MaxLength: 3, Error: v.err("nav_order"),
			Hint: "0〜999。小さい順にサイトのナビに並びます。空ならナビに出しません。公開中のページだけが出ます。"},
	}
}

func readInput(r *http.Request) Input {
	return Input{Slug: r.PostForm.Get("slug"), Title: r.PostForm.Get("title"), Body: r.PostForm.Get("body"), NavOrder: r.PostForm.Get("nav_order")}
}

// formPage は作成・編集の画面を描きます。preview があれば、フォームの上にプレビューを出します。
func (f *Feature) formPage(w http.ResponseWriter, r *http.Request, s features.AdminSession, p Page, v pageForm, status int, preview *richtext.Doc) {
	title := "固定ページの編集"
	if p.ID == 0 {
		title = "固定ページの作成"
	}
	data := s.PageData(title, "/admin/pages")
	data["Page"] = p
	data["Form"] = v
	data["Preview"] = preview
	data["Notice"] = messages.From(r)
	if status == http.StatusBadRequest && v.Invalid == "" {
		data["Error"] = "入力を確認してください。"
	}
	f.renderAdmin(w, status, "admin_page_form.html", data)
}

func (f *Feature) handleNew(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	f.formPage(w, r, s, Page{}, pageForm{}, http.StatusOK, nil)
}

func (f *Feature) handleEdit(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	id, ok := pageID(w, r)
	if !ok {
		return
	}
	p, err := f.store.Get(r.Context(), id)
	if f.failed(w, r, err) {
		return
	}
	in := Input{Slug: p.Slug, Title: p.Title, Body: p.Body}
	if p.NavOrder != nil {
		in.NavOrder = strconv.Itoa(*p.NavOrder)
	}
	f.formPage(w, r, s, p, pageForm{Input: in}, http.StatusOK, nil)
}

// handleSave は作成・更新します。プレビューのボタン（formaction）は handlePreview に送られます。
func (f *Feature) handleSave(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	var p Page
	if r.PathValue("id") != "" {
		id, ok := pageID(w, r)
		if !ok {
			return
		}
		var err error
		if p, err = f.store.Get(r.Context(), id); f.failed(w, r, err) {
			return
		}
	}
	in := readInput(r)
	id, err := f.store.Save(r.Context(), p.ID, in, f.taken)
	var invalid InputError
	if errors.As(err, &invalid) {
		f.formPage(w, r, s, p, pageForm{Input: in, Invalid: invalid.Field}, http.StatusBadRequest, nil)
		return
	}
	if f.failed(w, r, err) {
		return
	}
	f.afterChange(r)
	f.logger.Info("pages page saved", slog.Int64("page_id", id))
	key := "saved"
	if p.ID == 0 {
		key = "created"
	}
	notice.Redirect(w, r, "/admin/pages/"+strconv.FormatInt(id, 10), key)
}

// handlePreview は、保存せずに本文を描いて、入力を残したフォームと一緒に返します（PAGES-INV-9）。
func (f *Feature) handlePreview(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	in := readInput(r)
	var p Page
	if raw := r.PostForm.Get("id"); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 {
			http.Error(w, "Bad Request", http.StatusBadRequest)
			return
		}
		if p, err = f.store.Get(r.Context(), id); f.failed(w, r, err) {
			return
		}
	}
	n, err := normalize(in, f.taken)
	if err != nil {
		var invalid InputError
		errors.As(err, &invalid)
		f.formPage(w, r, s, p, pageForm{Input: in, Invalid: invalid.Field}, http.StatusBadRequest, nil)
		return
	}
	doc, _ := richtext.Parse(n.body)
	f.formPage(w, r, s, p, pageForm{Input: in}, http.StatusOK, &doc)
}

func (f *Feature) handlePublish(published bool) func(http.ResponseWriter, *http.Request, features.AdminSession) {
	return func(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
		id, ok := pageID(w, r)
		if !ok {
			return
		}
		if f.failed(w, r, f.store.SetPublished(r.Context(), id, published)) {
			return
		}
		f.afterChange(r)
		key := "unpublished"
		if published {
			key = "published"
		}
		f.logger.Info("pages publication changed", slog.Int64("page_id", id))
		notice.Redirect(w, r, "/admin/pages/"+strconv.FormatInt(id, 10), key)
	}
}

func (f *Feature) handleDeletePage(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	id, ok := pageID(w, r)
	if !ok {
		return
	}
	p, err := f.store.Get(r.Context(), id)
	if f.failed(w, r, err) {
		return
	}
	data := s.PageData("固定ページの削除", "/admin/pages")
	data["Page"] = p
	f.renderAdmin(w, http.StatusOK, "admin_page_delete.html", data)
}

func (f *Feature) handleDelete(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	id, ok := pageID(w, r)
	if !ok {
		return
	}
	if f.failed(w, r, f.store.Delete(r.Context(), id)) {
		return
	}
	f.afterChange(r)
	f.logger.Info("pages page deleted", slog.Int64("page_id", id))
	notice.Redirect(w, r, "/admin/pages", "deleted")
}

// afterChange は、ナビのスナップショットを作り直します。失敗は記録し、次の変更か再起動で直る。
func (f *Feature) afterChange(r *http.Request) {
	if err := f.refreshNav(r.Context()); err != nil {
		f.logger.Error("pages nav refresh failed")
	}
}
