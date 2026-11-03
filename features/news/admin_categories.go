package news

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"minimal-web-kit/features"
	"minimal-web-kit/internal/store"
	"minimal-web-kit/internal/web"
	"minimal-web-kit/internal/web/form"
	"minimal-web-kit/internal/web/notice"
)

// categoryNameField は、カテゴリの名前の入力欄です。追加と編集で同じ説明を使います。
func categoryNameField(name, fieldError string) form.Field {
	return form.Field{ID: "category-name", Name: "name", Label: "名前", Value: name, Required: true, MaxLength: maxCategoryRunes, Error: fieldError,
		Hint: "1〜50文字、改行なし。同じ名前は使えません。実名などの個人情報は入力しないでください。"}
}

// categoryError は、カテゴリの保存の失敗を、入力欄のエラーか画面全体のエラーに分けます。どちらでもなければ status は0です。
func categoryError(w http.ResponseWriter, err error) (status int, fieldError, pageError string) {
	var invalid InputError
	switch {
	case store.Busy(err):
		w.Header().Set("Retry-After", "1")
		return http.StatusServiceUnavailable, "", "一時的に保存できませんでした。時間を置いて再試行してください。"
	case errors.As(err, &invalid) && invalid.Field == "name_taken":
		return http.StatusBadRequest, "同じ名前のカテゴリがあります。別の名前にしてください。", ""
	case errors.As(err, &invalid):
		return http.StatusBadRequest, "名前は1〜50文字で、改行を含めずに入力してください。", ""
	}
	return 0, "", ""
}

func (f *Feature) handleAdminCategories(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	f.categoriesPage(w, r, s, http.StatusOK, "", "", "")
}

// categoriesPage は、カテゴリの一覧と追加のフォームを描きます。追加に失敗したときは、入力した名前とエラーを添えます。
func (f *Feature) categoriesPage(w http.ResponseWriter, r *http.Request, s features.AdminSession, status int, name, fieldError, pageError string) {
	categories, err := f.posts.Categories(r.Context())
	if err != nil {
		f.logger.Error("news list categories failed", f.requestID(w))
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	data := f.pageData(s, "お知らせのカテゴリの管理")
	data["Categories"] = categories
	data["Field"] = categoryNameField(name, fieldError)
	data["Error"] = pageError
	setNotice(data, r)
	f.renderAdminStatus(w, status, "admin_categories.html", data)
}

// categoryPage は、カテゴリの編集か削除の確認を描きます。
func (f *Feature) categoryPage(w http.ResponseWriter, s features.AdminSession, c Category, status int, deleting bool, fieldError, pageError string) {
	title := "お知らせのカテゴリの編集"
	if deleting {
		title = "お知らせのカテゴリの削除"
	}
	data := f.pageData(s, title)
	data["Category"] = c
	data["Deleting"] = deleting
	data["Field"] = categoryNameField(c.Name, fieldError)
	data["Error"] = pageError
	f.renderAdminStatus(w, status, "admin_category.html", data)
}

func (f *Feature) handleAdminCategory(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	id, ok := web.PathID(w, r, "id")
	if !ok {
		return
	}
	c, err := f.posts.Category(r.Context(), id)
	if errors.Is(err, ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		f.logger.Error("news get category failed", f.requestID(w))
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	f.categoryPage(w, s, c, http.StatusOK, strings.HasSuffix(r.URL.Path, "/delete"), "", "")
}

func (f *Feature) handleSaveCategory(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	var id int64
	if r.PathValue("id") != "" {
		var ok bool
		if id, ok = web.PathID(w, r, "id"); !ok {
			return
		}
	}
	name := r.PostForm.Get("name")
	_, err := f.posts.SaveCategory(r.Context(), id, name)
	if errors.Is(err, ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if status, fieldError, pageError := categoryError(w, err); status != 0 {
		if id == 0 {
			f.categoriesPage(w, r, s, status, name, fieldError, pageError)
		} else {
			f.categoryPage(w, s, Category{ID: id, Name: name}, status, false, fieldError, pageError)
		}
		return
	}
	if err != nil {
		f.writeFailed(w, "news save category failed", err)
		return
	}
	f.logger.Info("news category saved", f.requestID(w))
	notice.Redirect(w, r, "/admin/news-categories", "category_saved")
}

func (f *Feature) handleDeleteCategory(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	id, ok := web.PathID(w, r, "id")
	if !ok {
		return
	}
	err := f.posts.DeleteCategory(r.Context(), id)
	if errors.Is(err, ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		f.writeFailed(w, "news delete category failed", err)
		return
	}
	f.logger.Info("news category deleted", f.requestID(w), categoryIDAttr(id))
	notice.Redirect(w, r, "/admin/news-categories", "category_deleted")
}

func (f *Feature) registerCategoryAdminRoutes(mux *web.IsolatedMux) {
	// 経路は文字列のリテラルで書く（管理画面の検査が、ソースに書いた経路と登録された経路を突き合わせる）
	mux.HandleFunc("GET /admin/news-categories", f.admin.Page(f.handleAdminCategories))
	mux.HandleFunc("POST /admin/news-categories", f.admin.Form(f.handleSaveCategory))
	mux.HandleFunc("GET /admin/news-categories/{id}", f.admin.Page(f.handleAdminCategory))
	mux.HandleFunc("POST /admin/news-categories/{id}", f.admin.Form(f.handleSaveCategory))
	mux.HandleFunc("GET /admin/news-categories/{id}/delete", f.admin.Page(f.handleAdminCategory))
	mux.HandleFunc("POST /admin/news-categories/{id}/delete", f.admin.Form(f.handleDeleteCategory))
}

// categoryFieldFor は、記事のフォームのカテゴリの選択欄です。選択肢はカテゴリの名前順で、先頭は「なし」です。
func categoryFieldFor(categories []Category, selected, fieldError string) form.Field {
	field := form.Field{ID: "post-category", Name: "category", Label: "カテゴリ", Error: fieldError,
		Hint:    "カテゴリは「お知らせのカテゴリの管理」で作ります。公開ページの一覧で、カテゴリごとに絞り込めます。",
		Options: []form.Option{{Value: "", Label: "なし", Selected: selected == ""}}}
	for _, c := range categories {
		id := strconv.FormatInt(c.ID, 10)
		field.Options = append(field.Options, form.Option{Value: id, Label: c.Name, Selected: id == selected})
	}
	return field
}

// parseCategory は、フォームのカテゴリの値を読みます。空は0（なし）です。
func parseCategory(raw string) (int64, error) {
	if raw == "" {
		return 0, nil
	}
	id, ok := form.ID(raw)
	if !ok {
		return 0, InputError{"category"}
	}
	return id, nil
}
