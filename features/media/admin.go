package media

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"regexp"

	"minimal-web-kit/features"
	"minimal-web-kit/internal/store"
	"minimal-web-kit/internal/web"
	"minimal-web-kit/internal/web/form"
	"minimal-web-kit/internal/web/notice"
	"minimal-web-kit/internal/web/page"
	"minimal-web-kit/internal/web/richtext"
)

// maxUploadBody は、ファイルに説明と区切りの分を足した、本文全体の上限です。
const maxUploadBody = maxFileBytes + 64*1024

// maxAltBytes は、説明の部品を読む上限です。文字数の検査は form.Line で行います。
const maxAltBytes = richtext.MaxAltRunes*4 + 64

var adminPageNames = []string{"admin_media.html", "admin_media_item.html", "admin_media_delete.html"}

var messages = notice.Messages{"created": "画像を追加しました。", "saved": "説明を保存しました。", "deleted": "画像を削除しました。"}

var fieldErrors = form.FieldErrors{
	"alt":    "説明は1〜200文字で、改行を含めずに入力してください。",
	"file":   "画像のファイルを選んでください。",
	"format": "JPEGかPNGの画像を選んでください。それ以外の形式や、壊れたファイルは使えません。",
	"pixels": "画像の縦横が大きすぎます。小さくしてから追加してください。",
	"size":   "ファイルが大きすぎます。8MBまでの画像を選んでください。",
	"full":   "画像の保存容量の上限に達しました。使っていない画像を削除してください。",
}

var idPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)

// SetAdmin は管理者認証を受け取り、管理画面のテンプレートを解析します。
func (f *Feature) SetAdmin(admin features.Admin) error {
	pages, err := web.ParsePages(admin.Layout(), viewsFS, adminPageNames...)
	if err != nil {
		return err
	}
	f.admin, f.adminPages = admin, pages
	return nil
}

// registerAdmin は管理画面の経路を登録します（MEDIA-INV-4）。経路は文字列のリテラルで書く。
func (f *Feature) registerAdmin(mux *web.IsolatedMux) {
	mux.HandleFunc("GET /admin/media", f.admin.Page(f.handleList))
	mux.HandleUpload("POST /admin/media", maxUploadBody, f.admin.Upload(maxUploadBody, f.handleUpload))
	mux.HandleFunc("GET /admin/media/{id}", f.admin.Page(f.handleItem))
	mux.HandleFunc("POST /admin/media/{id}", f.admin.Form(f.handleSave))
	mux.HandleFunc("GET /admin/media/{id}/image", f.admin.Page(f.handleAdminImage))
	mux.HandleFunc("GET /admin/media/{id}/delete", f.admin.Page(f.handleDeletePage))
	mux.HandleFunc("POST /admin/media/{id}/delete", f.admin.Form(f.handleDelete))
}

func (f *Feature) render(w http.ResponseWriter, status int, name string, data map[string]any) {
	var buf bytes.Buffer
	if err := f.adminPages[name].ExecuteTemplate(&buf, "layout", data); err != nil {
		f.logger.Error("media render admin failed", web.RequestIDAttr(w))
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
	case store.Busy(err):
		web.WriteBusy(w)
	default:
		f.logger.Error("media admin failed", web.RequestIDAttr(w))
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
	return true
}

// pathID は経路の画像のIDを読みます。読めなければ404を返します。
func pathID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := r.PathValue("id")
	if !idPattern.MatchString(id) {
		http.NotFound(w, r)
		return "", false
	}
	return id, true
}

// uploadForm は追加のフォームの入力と、欄のエラーです。
type uploadForm struct {
	Alt, Invalid string
}

func (v uploadForm) Fields() []form.Field {
	return []form.Field{
		{ID: "media-alt", Name: "alt", Label: "説明", Value: v.Alt, Required: true, MaxLength: richtext.MaxAltRunes, Error: fieldErrors.For(v.Invalid, "alt"),
			Hint: "画像の内容を1〜200文字で書きます。画像が見えない人への説明になります。"},
		{ID: "media-file", Name: "file", Label: "画像のファイル", Type: "file", Required: true, Accept: "image/jpeg,image/png",
			Error: fieldErrors.For(v.Invalid, "file", "format", "pixels", "size", "full"),
			Hint:  "JPEGかPNG、8MBまで。長辺1,600pxまで縮小して保存します。撮影位置等の情報は保存しません。人の顔や名札が写っていないか、公開の前に確かめてください。"},
	}
}

func (f *Feature) listPage(w http.ResponseWriter, r *http.Request, s features.AdminSession, v uploadForm, status int) {
	pg, ok := page.Parse(r, pageSize, maxPage)
	if !ok {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	items, err := f.store.List(r.Context(), pg.Limit(), pg.Offset())
	if f.failed(w, r, err) {
		return
	}
	total, err := f.store.Total(r.Context())
	if f.failed(w, r, err) {
		return
	}
	items = page.Trim(&pg, items)
	pg.Base, pg.Label, pg.PrevText, pg.NextText = "/admin/media", "画像の一覧のページ送り", "新しい画像", "古い画像"
	data := s.PageData("画像の管理", "/admin/media")
	data["Items"] = items
	data["Page"] = pg
	data["Form"] = v
	data["Used"] = megabytes(total)
	data["Limit"] = megabytes(f.store.limit)
	data["Notice"] = messages.From(r)
	f.render(w, status, "admin_media.html", data)
}

func megabytes(n int64) string { return fmt.Sprintf("%.1f", float64(n)/1_000_000) }

func (f *Feature) handleList(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	f.listPage(w, r, s, uploadForm{}, http.StatusOK)
}

// readUpload は、説明とファイルの部品を決まった順で読みます。余分な部品があれば断ります。
func readUpload(parts *multipart.Reader) (alt string, data []byte, err error) {
	part, err := parts.NextRawPart()
	if err != nil || part.FormName() != "alt" || part.FileName() != "" {
		return "", nil, errUploadShape
	}
	raw, err := io.ReadAll(io.LimitReader(part, maxAltBytes+1))
	if err != nil {
		return "", nil, err
	}
	alt = string(raw)
	if len(raw) > maxAltBytes {
		return alt[:0], nil, errBadAlt
	}
	part, err = parts.NextRawPart()
	if err != nil || part.FormName() != "file" {
		return alt, nil, errUploadShape
	}
	data, err = io.ReadAll(io.LimitReader(part, maxFileBytes+1))
	if err != nil {
		return alt, nil, err
	}
	if len(data) > maxFileBytes {
		return alt, nil, errFileSize
	}
	if _, err := parts.NextRawPart(); !errors.Is(err, io.EOF) {
		return alt, nil, errUploadShape
	}
	return alt, data, nil
}

var (
	errUploadShape = errors.New("media: invalid upload form")
	errBadAlt      = errors.New("media: invalid alt")
	errFileSize    = errors.New("media: file too large")
)

// handleUpload は画像を追加します。本文を読む前に同時処理の枠を取り、読み終えて加工するまで保持します。
// 不変条件: MEDIA-INV-3, MEDIA-INV-4
func (f *Feature) handleUpload(w http.ResponseWriter, r *http.Request, s features.AdminSession, parts *multipart.Reader) {
	if parts == nil {
		f.listPage(w, r, s, uploadForm{Invalid: "size"}, http.StatusRequestEntityTooLarge)
		return
	}
	select {
	case f.slots <- struct{}{}:
	default:
		web.WriteBusy(w)
		return
	}
	alt, result, invalid, status := func() (string, processed, string, int) {
		defer func() { <-f.slots }() // 加工の途中のpanicでも枠を返す
		return f.receive(parts)
	}()
	if invalid != "" {
		f.listPage(w, r, s, uploadForm{Alt: alt, Invalid: invalid}, status)
		return
	}
	if status != 0 {
		http.Error(w, http.StatusText(status), status)
		return
	}
	it, err := f.store.Add(r.Context(), result, alt)
	if errors.Is(err, ErrFull) {
		f.listPage(w, r, s, uploadForm{Alt: alt, Invalid: "full"}, http.StatusBadRequest)
		return
	}
	if f.failed(w, r, err) {
		return
	}
	f.logger.Info("media added", web.RequestIDAttr(w))
	notice.Redirect(w, r, "/admin/media/"+it.ID, "created")
}

// receive は部品を読んで加工します。invalid は欄のエラー、status は応答の状態です。
func (f *Feature) receive(parts *multipart.Reader) (alt string, result processed, invalid string, status int) {
	alt, data, err := readUpload(parts)
	var tooLarge *http.MaxBytesError
	switch {
	case errors.Is(err, errFileSize), errors.As(err, &tooLarge):
		return alt, result, "size", http.StatusRequestEntityTooLarge
	case errors.Is(err, errBadAlt):
		return "", result, "alt", http.StatusBadRequest
	case err != nil:
		return alt, result, "", http.StatusBadRequest
	}
	clean, err := form.Line(alt, richtext.MaxAltRunes, true)
	if err != nil {
		return "", result, "alt", http.StatusBadRequest
	}
	if len(data) == 0 {
		return clean, result, "file", http.StatusBadRequest
	}
	result, err = process(data)
	switch {
	case errors.Is(err, errPixels):
		return clean, result, "pixels", http.StatusBadRequest
	case err != nil:
		return clean, result, "format", http.StatusBadRequest
	}
	return clean, result, "", 0
}

func (f *Feature) itemPage(w http.ResponseWriter, r *http.Request, s features.AdminSession, id, alt, invalid string, status int) {
	it, err := f.store.Get(r.Context(), id)
	if f.failed(w, r, err) {
		return
	}
	refs, err := f.store.Refs(r.Context(), id)
	if f.failed(w, r, err) {
		return
	}
	if invalid == "" {
		alt = it.Alt
	}
	data := s.PageData("画像の編集", "/admin/media")
	data["Item"] = it
	data["Refs"] = f.refViews(refs)
	data["AltField"] = form.Field{ID: "media-alt", Name: "alt", Label: "説明", Value: alt, Required: true, MaxLength: richtext.MaxAltRunes, Error: fieldErrors.For(invalid, "alt"),
		Hint: "画像が見えない人への説明です。固定ページの本文の説明は、本文に書いた文字を使います。"}
	data["Notice"] = messages.From(r)
	f.render(w, status, "admin_media_item.html", data)
}

// refView は、使用先の表示です。外した機能の参照は Disabled になります。
type refView struct {
	Label    string
	Disabled bool
}

var ownerLabels = map[string]string{"pages": "固定ページ", "news": "お知らせ", "catalog": "カタログ"}

func (f *Feature) refViews(refs []Ref) []refView {
	out := make([]refView, 0, len(refs))
	for _, r := range refs {
		label := ownerLabels[r.Owner]
		if label == "" {
			label = r.Owner
		}
		_, enabled := f.owners[r.Owner]
		out = append(out, refView{Label: label + " " + r.Key, Disabled: !enabled})
	}
	return out
}

func (f *Feature) handleItem(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	id, ok := pathID(w, r)
	if ok {
		f.itemPage(w, r, s, id, "", "", http.StatusOK)
	}
}

func (f *Feature) handleSave(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	raw := r.PostForm.Get("alt")
	alt, err := form.Line(raw, richtext.MaxAltRunes, true)
	if err != nil {
		f.itemPage(w, r, s, id, raw, "alt", http.StatusBadRequest)
		return
	}
	if f.failed(w, r, f.store.UpdateAlt(r.Context(), id, alt)) {
		return
	}
	f.logger.Info("media saved", web.RequestIDAttr(w))
	notice.Redirect(w, r, "/admin/media/"+id, "saved")
}

// handleAdminImage は、管理画面とプレビューの画像です。下書きの画像も見えます。キャッシュさせません。
// 不変条件: MEDIA-INV-4
func (f *Feature) handleAdminImage(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	it, err := f.store.Get(r.Context(), id)
	if f.failed(w, r, err) {
		return
	}
	f.serve(w, r, it)
}

func (f *Feature) deletePage(w http.ResponseWriter, r *http.Request, s features.AdminSession, id string, status int) {
	it, err := f.store.Get(r.Context(), id)
	if f.failed(w, r, err) {
		return
	}
	refs, err := f.store.Refs(r.Context(), id)
	if f.failed(w, r, err) {
		return
	}
	data := s.PageData("画像の削除", "/admin/media")
	data["Item"] = it
	data["Refs"] = f.refViews(refs)
	f.render(w, status, "admin_media_delete.html", data)
}

func (f *Feature) handleDeletePage(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	if id, ok := pathID(w, r); ok {
		f.deletePage(w, r, s, id, http.StatusOK)
	}
}

func (f *Feature) handleDelete(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	err := f.store.Delete(r.Context(), id)
	if errors.Is(err, ErrInUse) {
		f.deletePage(w, r, s, id, http.StatusConflict)
		return
	}
	if f.failed(w, r, err) {
		return
	}
	f.logger.Info("media deleted", web.RequestIDAttr(w))
	notice.Redirect(w, r, "/admin/media", "deleted")
}
