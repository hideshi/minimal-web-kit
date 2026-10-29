package catalog

import (
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"minimal-web-kit/features"
	"minimal-web-kit/internal/web"
	"minimal-web-kit/internal/web/form"
	"minimal-web-kit/internal/web/notice"
	"minimal-web-kit/internal/web/page"
)

var messages = notice.Messages{"saved": "変更を保存しました。", "published": "公開しました。", "unpublished": "下書きに戻しました。", "deleted": "削除しました。"}

const inputError = "入力を確認してください。タイトルは1〜200文字、本文は5万文字以内、分類名は1〜50文字、タグは20個までです。分類の重複や存在しない分類は使えません。"

type itemForm struct {
	Title, Body, Category, NewTags string
	Tags                           []int64
}

func (itemForm) LogValue() slog.Value { return slog.StringValue("[catalog form]") }
func (v itemForm) Fields() []form.Field {
	return []form.Field{
		{ID: "item-title", Name: "title", Label: "タイトル", Value: v.Title, Required: true, Hint: "1〜200文字、改行なし。"},
		{ID: "item-body", Name: "body", Label: "本文", Value: v.Body, Rows: 12, Hint: "5万文字以内のプレーンテキスト。HTMLは文字として表示されます。個人情報は書かないでください。"},
	}
}
func readInput(r *http.Request) (itemForm, Input, error) {
	v := itemForm{Title: r.PostForm.Get("title"), Body: r.PostForm.Get("body"), Category: r.PostForm.Get("category"), NewTags: r.PostForm.Get("new_tags")}
	in := Input{Title: v.Title, Body: v.Body}
	if len(r.PostForm["tag"]) > 20 {
		return v, in, ErrInvalidInput
	}
	if v.Category != "" {
		id, ok := parseID(v.Category)
		if !ok {
			return v, in, ErrInvalidInput
		}
		in.CategoryID = id
	}
	for _, raw := range r.PostForm["tag"] {
		id, ok := parseID(raw)
		if !ok {
			return v, in, ErrInvalidInput
		}
		v.Tags = append(v.Tags, id)
	}
	in.TagIDs = v.Tags
	if strings.TrimSpace(v.NewTags) != "" {
		for _, name := range strings.Split(strings.ReplaceAll(v.NewTags, "\r\n", "\n"), "\n") {
			if strings.TrimSpace(name) != "" {
				in.NewTags = append(in.NewTags, name)
			}
		}
	}
	return v, in, nil
}
func (f *Feature) adminData(s features.AdminSession, title string, r *http.Request) map[string]any {
	data := s.PageData(title, "/admin/catalog")
	data["Notice"] = messages.From(r)
	return data
}
func (f *Feature) renderAdmin(w http.ResponseWriter, status int, name string, data map[string]any) {
	f.render(w, status, f.adminPages[name], "layout", data)
}
func (f *Feature) handleAdminList(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	pg, ok := page.Parse(r, 20, 5000)
	if !ok {
		http.Error(w, "Bad Request", 400)
		return
	}
	items, err := f.store.List(r.Context(), false, Filter{}, pg.Limit(), pg.Offset())
	if !f.result(w, r, err) {
		return
	}
	items = page.Trim(&pg, items)
	if pg.Number >= 5000 {
		pg.Next = 0
	}
	pg.Base = "/admin/catalog"
	pg.Label = "カタログ管理のページ送り"
	pg.PrevText = "前のページ"
	pg.NextText = "次のページ"
	data := f.adminData(s, "カタログの管理", r)
	data["Items"] = items
	data["Page"] = pg
	f.renderAdmin(w, 200, "admin_list.html", data)
}
func (f *Feature) itemFormPage(w http.ResponseWriter, r *http.Request, s features.AdminSession, item Item, v itemForm, status int) {
	categories, err := f.store.Classifications(r.Context(), "categories", false)
	if !f.result(w, r, err) {
		return
	}
	tags, err := f.store.Classifications(r.Context(), "tags", false)
	if !f.result(w, r, err) {
		return
	}
	data := f.adminData(s, "カタログ項目の編集", r)
	if item.ID == 0 {
		data["Title"] = "カタログ項目の作成"
	}
	data["Item"] = item
	data["Form"] = v
	data["Categories"] = categories
	data["TagField"] = tagField(tags, v.Tags)
	if status == 400 {
		data["Error"] = inputError
	} else if status == 503 {
		data["Error"] = "一時的に保存できませんでした。時間を置いて再試行してください。"
		w.Header().Set("Retry-After", "1")
	}
	// タグ選択の部品（combobox.js）を読み込むため、この画面だけ自サイトのJSを許す。
	f.renderScripted(w, status, f.adminPages["admin_form.html"], "layout", data)
}

// tagField は、項目のタグ選択の部品に渡す値です。上限は新規タグを含めた合計で、保存時にも検査します。
func tagField(tags []Classification, ids []int64) form.Combobox {
	field := form.Combobox{ID: "item-tags", Name: "tag", Label: "タグ", Hint: "新規タグを含め合計20個まで。", Limit: 20}
	for _, tag := range tags {
		field.Options = append(field.Options, form.Option{Value: strconv.FormatInt(tag.ID, 10), Label: tag.Name, Selected: slices.Contains(ids, tag.ID)})
	}
	return field
}
func (f *Feature) handleNew(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	f.itemFormPage(w, r, s, Item{}, itemForm{}, 200)
}
func (f *Feature) handleEdit(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	id, ok := requestID(w, r)
	if !ok {
		return
	}
	item, err := f.store.Get(r.Context(), id, false)
	if !f.result(w, r, err) {
		return
	}
	v := itemForm{Title: item.Title, Body: item.Body}
	if item.CategoryID > 0 {
		v.Category = idString(item.CategoryID)
	}
	for _, tag := range item.Tags {
		v.Tags = append(v.Tags, tag.ID)
	}
	f.itemFormPage(w, r, s, item, v, 200)
}
func (f *Feature) handleSave(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	var item Item
	if r.PathValue("id") != "" {
		id, ok := requestID(w, r)
		if !ok {
			return
		}
		var err error
		item, err = f.store.Get(r.Context(), id, false)
		if !f.result(w, r, err) {
			return
		}
	}
	v, in, err := readInput(r)
	id := item.ID
	if err == nil {
		id, err = f.store.Save(r.Context(), id, in)
	}
	if busyError(err) {
		f.itemFormPage(w, r, s, item, v, 503)
		return
	}
	if errors.Is(err, ErrInvalidInput) {
		f.itemFormPage(w, r, s, item, v, 400)
		return
	}
	if !f.result(w, r, err) {
		return
	}
	f.logger.Info("catalog item saved", slog.Int64("item_id", id))
	notice.Redirect(w, r, "/admin/catalog/"+idString(id), "saved")
}
func (f *Feature) handlePublish(published bool) func(http.ResponseWriter, *http.Request, features.AdminSession) {
	return func(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
		id, ok := requestID(w, r)
		if !ok {
			return
		}
		if !f.result(w, r, f.store.SetPublished(r.Context(), id, published)) {
			return
		}
		key := "unpublished"
		if published {
			key = "published"
		}
		f.logger.Info("catalog publication changed", slog.Int64("item_id", id))
		notice.Redirect(w, r, "/admin/catalog/"+idString(id), key)
	}
}
func (f *Feature) handleDeletePage(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	id, ok := requestID(w, r)
	if !ok {
		return
	}
	item, err := f.store.Get(r.Context(), id, false)
	if !f.result(w, r, err) {
		return
	}
	data := f.adminData(s, "カタログ項目の削除", r)
	data["Item"] = item
	f.renderAdmin(w, 200, "admin_delete.html", data)
}
func (f *Feature) handleDelete(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	id, ok := requestID(w, r)
	if !ok {
		return
	}
	if !f.result(w, r, f.store.Delete(r.Context(), id)) {
		return
	}
	f.logger.Info("catalog item deleted", slog.Int64("item_id", id))
	notice.Redirect(w, r, "/admin/catalog", "deleted")
}
func kindTitle(kind string) string {
	if kind == "categories" {
		return "カテゴリ"
	}
	return "タグ"
}

// classificationField は分類名の入力欄。追加と編集で同じ説明を使う。
func classificationField(name, fieldError string) form.Field {
	return form.Field{ID: "classification-name", Name: "name", Label: "名前", Value: name, Required: true, MaxLength: 50, Error: fieldError, Hint: "1〜50文字、改行なし。同じ名前は使えません。実名などの個人情報は入力しないでください。"}
}

// classificationError は保存の失敗を、入力欄のエラーか画面全体のエラーに分ける。どちらでもなければ空。
func classificationError(w http.ResponseWriter, kind string, err error) (status int, fieldError, pageError string) {
	switch {
	case busyError(err):
		w.Header().Set("Retry-After", "1")
		return 503, "", "一時的に保存できませんでした。時間を置いて再試行してください。"
	case errors.Is(err, ErrDuplicateName):
		return 400, "同じ名前の" + kindTitle(kind) + "があります。別の名前にしてください。", ""
	case errors.Is(err, ErrInvalidInput):
		return 400, "名前は1〜50文字で、改行を含めずに入力してください。", ""
	}
	return 0, "", ""
}
func (f *Feature) handleClassifications(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	f.classificationsPage(w, r, s, 200, "", "", "")
}

// classificationsPage は一覧と追加のフォームを描画する。追加に失敗したときは、入力した名前とエラーを添える。
func (f *Feature) classificationsPage(w http.ResponseWriter, r *http.Request, s features.AdminSession, status int, name, fieldError, pageError string) {
	kind := r.PathValue("kind")
	values, err := f.store.ClassificationUsages(r.Context(), kind)
	if errors.Is(err, ErrInvalidInput) {
		http.NotFound(w, r)
		return
	}
	if !f.result(w, r, err) {
		return
	}
	data := f.adminData(s, kindTitle(kind)+"の管理", r)
	data["Kind"] = kind
	data["Values"] = values
	data["KindTitle"] = kindTitle(kind)
	data["Field"] = classificationField(name, fieldError)
	data["Error"] = pageError
	f.renderAdmin(w, status, "admin_classifications.html", data)
}
func (f *Feature) classificationPage(w http.ResponseWriter, r *http.Request, s features.AdminSession, v Classification, count, status int, deleting bool, fieldError, pageError string) {
	data := f.adminData(s, kindTitle(r.PathValue("kind"))+"の編集", r)
	data["Value"] = v
	data["Kind"] = r.PathValue("kind")
	data["KindTitle"] = kindTitle(r.PathValue("kind"))
	data["Count"] = count
	data["Deleting"] = deleting
	data["Field"] = classificationField(v.Name, fieldError)
	data["Error"] = pageError
	f.renderAdmin(w, status, "admin_classification.html", data)
}
func (f *Feature) handleClassification(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	id, ok := requestID(w, r)
	if !ok {
		return
	}
	v, n, err := f.store.Classification(r.Context(), r.PathValue("kind"), id)
	if errors.Is(err, ErrInvalidInput) {
		http.NotFound(w, r)
		return
	}
	if !f.result(w, r, err) {
		return
	}
	f.classificationPage(w, r, s, v, n, 200, strings.HasSuffix(r.URL.Path, "/delete"), "", "")
}
func (f *Feature) handleSaveClassification(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	kind := r.PathValue("kind")
	if _, err := classificationTable(kind); err != nil {
		http.NotFound(w, r)
		return
	}
	var id int64
	if r.PathValue("id") != "" {
		var ok bool
		id, ok = requestID(w, r)
		if !ok {
			return
		}
	}
	name := r.PostForm.Get("name")
	_, err := f.store.SaveClassification(r.Context(), kind, id, name)
	if status, fieldError, pageError := classificationError(w, kind, err); status != 0 {
		if id == 0 {
			f.classificationsPage(w, r, s, status, name, fieldError, pageError)
		} else {
			f.classificationPage(w, r, s, Classification{ID: id, Name: name}, 0, status, false, fieldError, pageError)
		}
		return
	}
	if !f.result(w, r, err) {
		return
	}
	f.logger.Info("catalog classification saved")
	notice.Redirect(w, r, "/admin/catalog/classifications/"+kind, "saved")
}
func (f *Feature) handleDeleteClassification(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	id, ok := requestID(w, r)
	if !ok {
		return
	}
	kind := r.PathValue("kind")
	if _, err := classificationTable(kind); err != nil {
		http.NotFound(w, r)
		return
	}
	if !f.result(w, r, f.store.DeleteClassification(r.Context(), kind, id)) {
		return
	}
	f.logger.Info("catalog classification deleted")
	notice.Redirect(w, r, "/admin/catalog/classifications/"+kind, "deleted")
}
func (f *Feature) registerAdmin(mux *web.IsolatedMux) {
	mux.HandleFunc("GET /admin/catalog", f.admin.Page(f.handleAdminList))
	mux.HandleFunc("GET /admin/catalog/new", f.admin.Page(f.handleNew))
	mux.HandleFunc("POST /admin/catalog", f.admin.Form(f.handleSave))
	mux.HandleFunc("GET /admin/catalog/{id}", f.admin.Page(f.handleEdit))
	mux.HandleFunc("POST /admin/catalog/{id}", f.admin.Form(f.handleSave))
	mux.HandleFunc("POST /admin/catalog/{id}/publish", f.admin.Form(f.handlePublish(true)))
	mux.HandleFunc("POST /admin/catalog/{id}/unpublish", f.admin.Form(f.handlePublish(false)))
	mux.HandleFunc("GET /admin/catalog/{id}/delete", f.admin.Page(f.handleDeletePage))
	mux.HandleFunc("POST /admin/catalog/{id}/delete", f.admin.Form(f.handleDelete))
	for _, kind := range []string{"categories", "tags"} {
		wrap := func(next func(http.ResponseWriter, *http.Request, features.AdminSession)) func(http.ResponseWriter, *http.Request, features.AdminSession) {
			return func(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
				r.SetPathValue("kind", kind)
				next(w, r, s)
			}
		}
		base := "/admin/catalog/classifications/" + kind
		mux.HandleFunc("GET "+base, f.admin.Page(wrap(f.handleClassifications)))
		mux.HandleFunc("POST "+base, f.admin.Form(wrap(f.handleSaveClassification)))
		mux.HandleFunc("GET "+base+"/{id}", f.admin.Page(wrap(f.handleClassification)))
		mux.HandleFunc("POST "+base+"/{id}", f.admin.Form(wrap(f.handleSaveClassification)))
		mux.HandleFunc("GET "+base+"/{id}/delete", f.admin.Page(wrap(f.handleClassification)))
		mux.HandleFunc("POST "+base+"/{id}/delete", f.admin.Form(wrap(f.handleDeleteClassification)))
	}
}
