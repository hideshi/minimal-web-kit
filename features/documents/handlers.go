package documents

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"minimal-web-kit/features"
	"minimal-web-kit/internal/store"
	"minimal-web-kit/internal/web"
	"minimal-web-kit/internal/web/form"
	"minimal-web-kit/internal/web/notice"
	"minimal-web-kit/internal/web/page"
	"minimal-web-kit/internal/web/richtext"
)

var pageNames = []string{
	"documents.html", "documents_mine.html", "documents_approvals.html", "document.html", "document_form.html",
	"document_share.html", "document_approval.html", "document_reads.html", "document_versions.html", "document_delete.html",
	"document_folders.html", "document_folder.html", "document_folder_delete.html", "document_transfer.html",
}

const (
	pageSize = 30
	maxPage  = 1000
	root     = "/admin/documents"
)

var messages = notice.Messages{
	"created": "下書きを作成しました。", "saved": "変更を保存しました。", "published": "公開しました。", "revised": "改定の下書きを作りました。",
	"shared": "共有先を変更しました。", "requested": "承認を依頼しました。", "withdrawn": "依頼を取り下げました。下書きに戻しました。",
	"approved": "承認しました。", "rejected": "差し戻しました。", "deleted": "削除しました。", "folder_added": "フォルダを追加しました。",
	"folder_saved": "フォルダの名前を変更しました。", "folder_deleted": "フォルダを削除しました。", "transferred": "所有者を変更しました。",
}

var fieldErrors = form.FieldErrors{
	"title":      "タイトルは1〜200文字で、改行を含めずに入力してください。",
	"body":       "本文は5万文字以内で入力してください。",
	"body_limit": "表が大きすぎるか、見出しより列の多い行があります。表は20列・200行まで、行の列は見出しの列以下にしてください。",
	"folder":     "フォルダを選び直してください。",
	"audience":   "共有の範囲を選んでください。",
	"accounts":   "共有先を1人以上選んでください。",
	"approvers":  "承認者を1人以上選んでください。自分は選べません。",
	"reason":     "差し戻しの理由を200文字以内で入力してください。",
	"name":       "フォルダの名前は1〜50文字で、改行を含めずに入力してください。",
	"name_taken": "同じ名前のフォルダがあります。別の名前にしてください。",
	"to":         "移す先を選び直してください。自分と、元の所有者は選べません。",
}

const conflictMessage = "画面を開いた後に状態が変わりました。開き直してから、もう一度操作してください。"

// registerRoutes は経路を登録します。経路は文字列のリテラルで書きます。
// 全ての経路にログインが要り、変更はPOSTだけで行います。
// 不変条件: DOC-INV-1、DOC-INV-12
func (f *Feature) RegisterIsolatedRoutes(mux *web.IsolatedMux) {
	if f.admin == nil {
		return
	}
	page, submit := f.admin.AccountPage, f.admin.AccountForm
	mux.HandleFunc("GET /admin/documents", page(f.handleList))
	mux.HandleFunc("GET /admin/documents/mine", page(f.handleMine))
	mux.HandleFunc("GET /admin/documents/approvals", page(f.handleApprovals))
	mux.HandleFunc("GET /admin/documents/new", page(f.handleNew))
	mux.HandleFunc("POST /admin/documents", submit(f.handleCreate))
	mux.HandleFunc("GET /admin/document-folders", page(f.handleFolders))
	mux.HandleFunc("POST /admin/document-folders", submit(f.handleAddFolder))
	mux.HandleFunc("GET /admin/document-folders/{id}", page(f.handleFolder))
	mux.HandleFunc("POST /admin/document-folders/{id}", submit(f.handleRenameFolder))
	mux.HandleFunc("GET /admin/document-folders/{id}/delete", page(f.handleFolderDeletePage))
	mux.HandleFunc("POST /admin/document-folders/{id}/delete", submit(f.handleFolderDelete))
	mux.HandleFunc("GET /admin/documents/transfer", f.admin.AdminPage(f.handleTransferPage))
	mux.HandleFunc("POST /admin/documents/transfer", f.admin.AdminForm(f.handleTransfer))
	mux.HandleFunc("GET /admin/documents/{id}", page(f.handleShow))
	mux.HandleFunc("GET /admin/documents/{id}/edit", page(f.handleEdit))
	mux.HandleFunc("POST /admin/documents/{id}/edit", submit(f.handleSave))
	mux.HandleFunc("POST /admin/documents/{id}/publish", submit(f.handlePublish))
	mux.HandleFunc("POST /admin/documents/{id}/revise", submit(f.handleRevise))
	mux.HandleFunc("POST /admin/documents/{id}/withdraw", submit(f.handleWithdraw))
	mux.HandleFunc("GET /admin/documents/{id}/share", page(f.handleSharePage))
	mux.HandleFunc("POST /admin/documents/{id}/share", submit(f.handleShare))
	mux.HandleFunc("GET /admin/documents/{id}/approval", page(f.handleApprovalPage))
	mux.HandleFunc("POST /admin/documents/{id}/approval", submit(f.handleRequest))
	mux.HandleFunc("GET /admin/documents/{id}/reads", page(f.handleReads))
	mux.HandleFunc("GET /admin/documents/{id}/versions", page(f.handleVersions))
	mux.HandleFunc("GET /admin/documents/{id}/versions/{version}", page(f.handleVersion))
	mux.HandleFunc("POST /admin/documents/{id}/versions/{version}/approve", submit(f.handleApprove))
	mux.HandleFunc("POST /admin/documents/{id}/versions/{version}/reject", submit(f.handleReject))
	mux.HandleFunc("GET /admin/documents/{id}/delete", page(f.handleDeletePage))
	mux.HandleFunc("POST /admin/documents/{id}/delete", submit(f.handleDelete))
}

func (f *Feature) render(w http.ResponseWriter, status int, name string, data map[string]any) {
	var buf bytes.Buffer
	if err := f.pages[name].ExecuteTemplate(&buf, "layout", data); err != nil {
		f.logger.Error("documents render failed", web.RequestIDAttr(w))
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}

// failed はエラーの応答を書きます。本文やタイトルはログに出しません。不変条件: DOC-INV-10
func (f *Feature) failed(w http.ResponseWriter, r *http.Request, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, ErrNotFound):
		http.NotFound(w, r)
	case errors.Is(err, ErrConflict):
		http.Error(w, conflictMessage, http.StatusConflict)
	case store.Busy(err):
		web.WriteBusy(w)
	default:
		f.logger.Error("documents failed", web.RequestIDAttr(w))
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
	return true
}

func docID(w http.ResponseWriter, r *http.Request, name string) (int64, bool) {
	return web.PathID(w, r, name)
}

func docPath(id int64) string { return root + "/" + strconv.FormatInt(id, 10) }

// people はアカウントの呼び名と状態の引き当てです。
type people struct {
	all  []features.DirectoryAccount
	byID map[int64]features.DirectoryAccount
}

func (f *Feature) people(ctx context.Context) (people, error) {
	all, err := f.directory.Accounts(ctx)
	if err != nil {
		return people{}, err
	}
	p := people{all: all, byID: map[int64]features.DirectoryAccount{}}
	for _, a := range all {
		p.byID[a.ID] = a
	}
	return p, nil
}

// Name は表示する呼び名です。削除済みのIDは呼び名が分かりません。
func (p people) Name(id int64) string {
	a, ok := p.byID[id]
	switch {
	case !ok:
		return "削除済みのアカウント"
	case a.Disabled:
		return a.Name + "（停止中）"
	}
	return a.Name
}

func (p people) active(id int64) bool {
	a, ok := p.byID[id]
	return ok && !a.Disabled
}

// options は、有効なアカウントの選択肢を返します。except は除きます。
func (p people) options(except int64, selected []int64) []form.Option {
	var out []form.Option
	for _, a := range p.all {
		if a.Disabled || a.ID == except {
			continue
		}
		out = append(out, form.Option{Value: strconv.FormatInt(a.ID, 10), Label: a.Name, Selected: slices.Contains(selected, a.ID)})
	}
	return out
}

func parseIDs(values []string) []int64 {
	var out []int64
	for _, v := range values {
		if id, ok := form.ID(v); ok {
			out = append(out, id)
		}
	}
	return out
}

func (f *Feature) folderField(ctx context.Context, selected int64, msg string) (form.Field, error) {
	folders, err := f.store.Folders(ctx)
	if err != nil {
		return form.Field{}, err
	}
	options := []form.Option{{Value: "0", Label: "フォルダなし", Selected: selected == 0}}
	for _, fo := range folders {
		options = append(options, form.Option{Value: strconv.FormatInt(fo.ID, 10), Label: fo.Name, Selected: fo.ID == selected})
	}
	return form.Field{ID: "document-folder", Name: "folder", Label: "フォルダ", Optional: true, Options: options, Error: msg}, nil
}

func (f *Feature) handleList(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	p, ok := page.Parse(r, pageSize, maxPage)
	if !ok {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	filter := Filter{Query: strings.TrimSpace(r.URL.Query().Get("q"))}
	if raw := r.URL.Query().Get("folder"); raw != "" && raw != "0" {
		id, ok := form.ID(raw)
		if !ok {
			http.Error(w, "Bad Request", http.StatusBadRequest)
			return
		}
		filter.FolderID = id
	}
	if len([]rune(filter.Query)) > 100 {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	items, err := f.store.List(r.Context(), s.AccountID(), filter, p)
	if f.failed(w, r, err) {
		return
	}
	items = page.Trim(&p, items)
	folder, err := f.folderField(r.Context(), filter.FolderID, "")
	if f.failed(w, r, err) {
		return
	}
	folder.Options[0].Label = "全てのフォルダ"
	data := s.PageData("文書", root)
	data["Items"] = items
	data["Page"] = p
	data["Notice"] = messages.From(r)
	data["Search"] = []form.Field{
		{ID: "document-q", Name: "q", Label: "検索", Optional: true, Value: filter.Query, MaxLength: 100, Hint: "読める文書のタイトルと本文から探します。"},
		folder,
	}
	data["Filtered"] = filter.Query != "" || filter.FolderID != 0
	f.render(w, http.StatusOK, "documents.html", data)
}

func (f *Feature) handleMine(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	p, ok := page.Parse(r, pageSize, maxPage)
	if !ok {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	items, err := f.store.Mine(r.Context(), s.AccountID(), p)
	if f.failed(w, r, err) {
		return
	}
	items = page.Trim(&p, items)
	data := s.PageData("自分の文書", root)
	data["Items"] = items
	data["Page"] = p
	data["Notice"] = messages.From(r)
	f.render(w, http.StatusOK, "documents_mine.html", data)
}

func (f *Feature) handleApprovals(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	items, err := f.store.Approvals(r.Context(), s.AccountID())
	if f.failed(w, r, err) {
		return
	}
	who, err := f.people(r.Context())
	if f.failed(w, r, err) {
		return
	}
	data := s.PageData("承認の依頼", root)
	data["Items"] = items
	data["People"] = who
	data["Notice"] = messages.From(r)
	f.render(w, http.StatusOK, "documents_approvals.html", data)
}

// documentForm は作成・編集の画面の値です。
type documentForm struct {
	Input
	Invalid string
}

func (v documentForm) err(names ...string) string { return fieldErrors.For(v.Invalid, names...) }

func (f *Feature) renderForm(w http.ResponseWriter, r *http.Request, s features.AdminSession, id int64, version Version, v documentForm, status int) {
	title := "文書の作成"
	if id != 0 {
		title = "文書の編集"
	}
	folder, err := f.folderField(r.Context(), v.FolderID, v.err("folder"))
	if f.failed(w, r, err) {
		return
	}
	data := s.PageData(title, root)
	data["ID"] = id
	data["Version"] = version
	data["Fields"] = []form.Field{
		{ID: "document-title", Name: "title", Label: "タイトル", Value: v.Title, Required: true, MaxLength: maxTitleRunes, Error: v.err("title"), Hint: "1〜200文字、改行なし。"},
		folder,
		{ID: "document-body", Name: "body", Label: "本文", Value: v.Body, Rows: 16, Error: v.err("body", "body_limit"), Hint: "5万文字以内。書き方は下の「本文の書き方」を参照。"},
	}
	data["Notice"] = messages.From(r)
	if status == http.StatusBadRequest && v.Invalid == "" {
		data["Error"] = "入力を確認してください。"
	}
	if id != 0 {
		asked, err := f.store.RequiresApproval(r.Context(), id)
		if f.failed(w, r, err) {
			return
		}
		data["NeedsApproval"] = asked
		data["Rejection"] = f.lastRejection(r.Context(), s.AccountID(), id)
	}
	f.render(w, status, "document_form.html", data)
}

// lastRejection は、下書きの1つ前が差し戻しなら、その理由を返します。
func (f *Feature) lastRejection(ctx context.Context, owner, id int64) string {
	versions, err := f.store.Versions(ctx, owner, id)
	if err != nil || len(versions) < 2 || versions[1].State != stateRejected {
		return ""
	}
	approvers, err := f.store.Approvers(ctx, versions[1].ID)
	if err != nil {
		return ""
	}
	for _, a := range approvers {
		if a.Decision == "rejected" {
			return a.Reason
		}
	}
	return ""
}

func readInput(r *http.Request) Input {
	folder, _ := form.Int(r.PostForm.Get("folder"), 0, 0, 1<<62)
	return Input{Title: r.PostForm.Get("title"), Body: r.PostForm.Get("body"), FolderID: int64(folder)}
}

func (f *Feature) handleNew(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	f.renderForm(w, r, s, 0, Version{}, documentForm{}, http.StatusOK)
}

// inputFailed は入力の誤りなら同じ画面を400で返します。
func (f *Feature) inputFailed(w http.ResponseWriter, r *http.Request, s features.AdminSession, id int64, version Version, in Input, err error) bool {
	var input InputError
	if errors.As(err, &input) {
		f.renderForm(w, r, s, id, version, documentForm{Input: in, Invalid: input.Field}, http.StatusBadRequest)
		return true
	}
	return f.failed(w, r, err)
}

func (f *Feature) handleCreate(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	in := readInput(r)
	id, err := f.store.Create(r.Context(), s.AccountID(), in)
	if f.inputFailed(w, r, s, 0, Version{}, in, err) {
		return
	}
	notice.Redirect(w, r, docPath(id)+"/edit", "created")
}

func (f *Feature) handleEdit(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	id, ok := docID(w, r, "id")
	if !ok {
		return
	}
	d, v, err := f.store.Draft(r.Context(), s.AccountID(), id)
	if f.failed(w, r, err) {
		return
	}
	f.renderForm(w, r, s, id, v, documentForm{Input: Input{Title: v.Title, Body: v.Body, FolderID: d.FolderID}}, http.StatusOK)
}

func (f *Feature) handleSave(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	id, ok := docID(w, r, "id")
	if !ok {
		return
	}
	versionID, ok := form.ID(r.PostForm.Get("version"))
	if !ok {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	in := readInput(r)
	err := f.store.SaveDraft(r.Context(), s.AccountID(), id, versionID, in)
	if f.inputFailed(w, r, s, id, Version{ID: versionID}, in, err) {
		return
	}
	notice.Redirect(w, r, docPath(id)+"/edit", "saved")
}

func (f *Feature) versionFromForm(w http.ResponseWriter, r *http.Request) (int64, int64, bool) {
	id, ok := docID(w, r, "id")
	if !ok {
		return 0, 0, false
	}
	versionID, ok := form.ID(r.PostForm.Get("version"))
	if !ok {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return 0, 0, false
	}
	return id, versionID, true
}

func (f *Feature) handlePublish(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	id, versionID, ok := f.versionFromForm(w, r)
	if !ok || f.failed(w, r, f.store.Publish(r.Context(), s.AccountID(), id, versionID)) {
		return
	}
	notice.Redirect(w, r, docPath(id), "published")
}

func (f *Feature) handleRevise(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	id, ok := docID(w, r, "id")
	if !ok || f.failed(w, r, f.store.Revise(r.Context(), s.AccountID(), id)) {
		return
	}
	notice.Redirect(w, r, docPath(id)+"/edit", "revised")
}

func (f *Feature) handleWithdraw(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	id, versionID, ok := f.versionFromForm(w, r)
	if !ok || f.failed(w, r, f.store.Withdraw(r.Context(), s.AccountID(), id, versionID)) {
		return
	}
	notice.Redirect(w, r, docPath(id)+"/edit", "withdrawn")
}

// viewData は閲覧の画面の共通の値です。
func (f *Feature) viewData(w http.ResponseWriter, r *http.Request, s features.AdminSession, d Document, v Version) (map[string]any, bool) {
	doc, err := richtext.Parse(v.Body)
	if f.failed(w, r, err) {
		return nil, false
	}
	approvers, err := f.store.Approvers(r.Context(), v.ID)
	if f.failed(w, r, err) {
		return nil, false
	}
	who, err := f.people(r.Context())
	if f.failed(w, r, err) {
		return nil, false
	}
	data := s.PageData(v.Title, root)
	data["Doc"] = doc
	data["Document"] = d
	data["Version"] = v
	data["Approvers"] = approvers
	data["People"] = who
	data["Mine"] = d.OwnerID == s.AccountID()
	data["IsCurrent"] = d.CurrentID == v.ID
	data["Notice"] = messages.From(r)
	return data, true
}

// handleShow は今の版を描きます。共有先でない承認者には404で、公開中の別の版を見せません。
// 所有者は、未公開なら版の履歴へ移ります。
func (f *Feature) handleShow(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	id, ok := docID(w, r, "id")
	if !ok {
		return
	}
	d, v, err := f.store.Current(r.Context(), s.AccountID(), id)
	if errors.Is(err, ErrNotFound) {
		if _, err := f.store.Owned(r.Context(), s.AccountID(), id); err == nil {
			http.Redirect(w, r, docPath(id)+"/versions", http.StatusSeeOther)
			return
		}
	}
	if f.failed(w, r, err) {
		return
	}
	data, ok := f.viewData(w, r, s, d, v)
	if !ok {
		return
	}
	if data["Mine"] == true {
		open, err := f.store.Open(r.Context(), s.AccountID(), id)
		if f.failed(w, r, err) {
			return
		}
		data["Open"] = open
	}
	if r.Method == http.MethodGet && !prefetch(r) {
		if err := f.store.RecordRead(r.Context(), d, s.AccountID()); err != nil {
			f.logger.Error("documents read record failed", web.RequestIDAttr(w)) // 記録の失敗で本文の表示を止めない
		}
	}
	f.render(w, http.StatusOK, "document.html", data)
}

// prefetch は、ブラウザが先読みした要求かを返します。人が開いていないので既読にしません。
func prefetch(r *http.Request) bool {
	for _, name := range []string{"Sec-Purpose", "Purpose"} {
		if strings.Contains(r.Header.Get(name), "prefetch") {
			return true
		}
	}
	return false
}

func (f *Feature) handleVersion(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	id, ok := docID(w, r, "id")
	if !ok {
		return
	}
	versionID, ok := docID(w, r, "version")
	if !ok {
		return
	}
	d, v, err := f.store.VersionFor(r.Context(), s.AccountID(), id, versionID)
	if f.failed(w, r, err) {
		return
	}
	data, ok := f.viewData(w, r, s, d, v)
	if !ok {
		return
	}
	pending := false
	if v.State == statePending {
		for _, a := range data["Approvers"].([]Approver) {
			pending = pending || (a.AccountID == s.AccountID() && a.Decision == "")
		}
	}
	data["CanDecide"] = pending
	data["ReasonField"] = reasonField("")
	f.render(w, http.StatusOK, "document.html", data)
}

func reasonField(msg string) form.Field {
	return form.Field{ID: "document-reason", Name: "reason", Label: "差し戻しの理由", MaxLength: maxReasonRunes, Required: true, Error: msg, Hint: "200文字以内。所有者に伝えます。"}
}

func (f *Feature) handleApprove(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	id, ok := docID(w, r, "id")
	if !ok {
		return
	}
	versionID, ok := docID(w, r, "version")
	if !ok || f.failed(w, r, f.store.Approve(r.Context(), s.AccountID(), id, versionID)) {
		return
	}
	notice.Redirect(w, r, root+"/approvals", "approved")
}

func (f *Feature) handleReject(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	id, ok := docID(w, r, "id")
	if !ok {
		return
	}
	versionID, ok := docID(w, r, "version")
	if !ok {
		return
	}
	err := f.store.Reject(r.Context(), s.AccountID(), id, versionID, r.PostForm.Get("reason"))
	var input InputError
	if errors.As(err, &input) {
		d, v, err := f.store.VersionFor(r.Context(), s.AccountID(), id, versionID)
		if f.failed(w, r, err) {
			return
		}
		data, ok := f.viewData(w, r, s, d, v)
		if !ok {
			return
		}
		data["CanDecide"] = true
		data["ReasonField"] = reasonField(fieldErrors.For(input.Field, "reason"))
		f.render(w, http.StatusBadRequest, "document.html", data)
		return
	}
	if f.failed(w, r, err) {
		return
	}
	notice.Redirect(w, r, root+"/approvals", "rejected")
}

func audienceChoices(selected, msg string) form.Choices {
	return form.Choices{ID: "document-audience", Name: "audience", Legend: "共有の範囲", Required: true, Error: msg, Options: []form.Option{
		{Value: audiencePrivate, Label: "自分だけ", Selected: selected == audiencePrivate},
		{Value: audienceEveryone, Label: "全員", Selected: selected == audienceEveryone},
		{Value: audienceSelected, Label: "指定した人", Selected: selected == audienceSelected},
	}}
}

func (f *Feature) renderShare(w http.ResponseWriter, r *http.Request, s features.AdminSession, id int64, audience string, selected []int64, invalid string, status int) {
	who, err := f.people(r.Context())
	if f.failed(w, r, err) {
		return
	}
	data := s.PageData("共有先の設定", root)
	data["ID"] = id
	data["Audience"] = audienceChoices(audience, fieldErrors.For(invalid, "audience"))
	data["Accounts"] = form.Choices{ID: "document-accounts", Name: "accounts", Legend: "指定した人", Multiple: true, Hint: "「指定した人」を選んだときに使います。", Empty: "選べるアカウントはありません。",
		Error: fieldErrors.For(invalid, "accounts"), Options: who.options(s.AccountID(), selected)}
	f.render(w, status, "document_share.html", data)
}

func (f *Feature) handleSharePage(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	id, ok := docID(w, r, "id")
	if !ok {
		return
	}
	d, err := f.store.Owned(r.Context(), s.AccountID(), id)
	if f.failed(w, r, err) {
		return
	}
	shares, err := f.store.Shares(r.Context(), s.AccountID(), id)
	if f.failed(w, r, err) {
		return
	}
	f.renderShare(w, r, s, id, d.Audience, shares, "", http.StatusOK)
}

func (f *Feature) handleShare(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	id, ok := docID(w, r, "id")
	if !ok {
		return
	}
	who, err := f.people(r.Context())
	if f.failed(w, r, err) {
		return
	}
	audience, accounts := r.PostForm.Get("audience"), parseIDs(r.PostForm["accounts"])
	err = f.store.SetSharing(r.Context(), s.AccountID(), id, audience, accounts, who.active)
	var input InputError
	if errors.As(err, &input) {
		if _, err := f.store.Owned(r.Context(), s.AccountID(), id); f.failed(w, r, err) {
			return
		}
		f.renderShare(w, r, s, id, audience, accounts, input.Field, http.StatusBadRequest)
		return
	}
	if f.failed(w, r, err) {
		return
	}
	notice.Redirect(w, r, root+"/mine", "shared")
}

func (f *Feature) renderApproval(w http.ResponseWriter, r *http.Request, s features.AdminSession, id int64, v Version, selected []int64, invalid string, status int) {
	who, err := f.people(r.Context())
	if f.failed(w, r, err) {
		return
	}
	data := s.PageData("承認の依頼の作成", root)
	data["ID"] = id
	data["Version"] = v
	data["Approvers"] = form.Choices{ID: "document-approvers", Name: "approvers", Legend: "承認者", Required: true, Multiple: true, Empty: "選べるアカウントはありません。",
		Hint: "選んだ全員の承認で公開します。順番はありません。自分は選べません。", Error: fieldErrors.For(invalid, "approvers"), Options: who.options(s.AccountID(), selected)}
	f.render(w, status, "document_approval.html", data)
}

func (f *Feature) handleApprovalPage(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	id, ok := docID(w, r, "id")
	if !ok {
		return
	}
	_, v, err := f.store.Draft(r.Context(), s.AccountID(), id)
	if f.failed(w, r, err) {
		return
	}
	f.renderApproval(w, r, s, id, v, nil, "", http.StatusOK)
}

func (f *Feature) handleRequest(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	id, versionID, ok := f.versionFromForm(w, r)
	if !ok {
		return
	}
	who, err := f.people(r.Context())
	if f.failed(w, r, err) {
		return
	}
	approvers := parseIDs(r.PostForm["approvers"])
	err = f.store.RequestApproval(r.Context(), s.AccountID(), id, versionID, approvers, who.active)
	var input InputError
	if errors.As(err, &input) {
		_, v, err := f.store.Draft(r.Context(), s.AccountID(), id)
		if f.failed(w, r, err) {
			return
		}
		f.renderApproval(w, r, s, id, v, approvers, input.Field, http.StatusBadRequest)
		return
	}
	if f.failed(w, r, err) {
		return
	}
	notice.Redirect(w, r, docPath(id)+"/versions", "requested")
}

// readRow は既読の状況の1行です。
type readRow struct {
	Name string
	At   time.Time
}

func (f *Feature) handleReads(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	id, ok := docID(w, r, "id")
	if !ok {
		return
	}
	d, err := f.store.Owned(r.Context(), s.AccountID(), id)
	if f.failed(w, r, err) {
		return
	}
	reads, err := f.store.Reads(r.Context(), s.AccountID(), id)
	if f.failed(w, r, err) {
		return
	}
	shares, err := f.store.Shares(r.Context(), s.AccountID(), id)
	if f.failed(w, r, err) {
		return
	}
	who, err := f.people(r.Context())
	if f.failed(w, r, err) {
		return
	}
	targets := shares
	if d.Audience == audienceEveryone {
		targets = nil
		for _, a := range who.all {
			if !a.Disabled && a.ID != d.OwnerID {
				targets = append(targets, a.ID)
			}
		}
	} else if d.Audience == audiencePrivate {
		targets = nil
	}
	var rows []readRow
	done := 0
	for _, t := range targets {
		at := reads[t]
		if !at.IsZero() {
			done++
		}
		rows = append(rows, readRow{Name: who.Name(t), At: at})
	}
	data := s.PageData("既読の状況", root)
	data["ID"] = id
	data["Published"] = d.CurrentID != 0
	data["Rows"] = rows
	data["Done"] = done
	f.render(w, http.StatusOK, "document_reads.html", data)
}

func (f *Feature) handleVersions(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	id, ok := docID(w, r, "id")
	if !ok {
		return
	}
	d, err := f.store.Owned(r.Context(), s.AccountID(), id)
	if f.failed(w, r, err) {
		return
	}
	versions, err := f.store.Versions(r.Context(), s.AccountID(), id)
	if f.failed(w, r, err) {
		return
	}
	asked, err := f.store.RequiresApproval(r.Context(), id)
	if f.failed(w, r, err) {
		return
	}
	data := s.PageData("版の履歴", root)
	data["ID"] = id
	data["Document"] = d
	data["Versions"] = versions
	data["NeedsApproval"] = asked
	data["Notice"] = messages.From(r)
	f.render(w, http.StatusOK, "document_versions.html", data)
}

func (f *Feature) handleDeletePage(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	id, ok := docID(w, r, "id")
	if !ok {
		return
	}
	versions, err := f.store.Versions(r.Context(), s.AccountID(), id)
	if f.failed(w, r, err) {
		return
	}
	data := s.PageData("文書の削除", root)
	data["ID"] = id
	data["DocumentTitle"] = versions[0].Title
	f.render(w, http.StatusOK, "document_delete.html", data)
}

func (f *Feature) handleDelete(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	id, ok := docID(w, r, "id")
	if !ok || f.failed(w, r, f.store.Delete(r.Context(), s.AccountID(), id)) {
		return
	}
	notice.Redirect(w, r, root+"/mine", "deleted")
}

func (f *Feature) renderFolders(w http.ResponseWriter, r *http.Request, s features.AdminSession, name, invalid string, status int) {
	folders, err := f.store.Folders(r.Context())
	if f.failed(w, r, err) {
		return
	}
	data := s.PageData("フォルダの管理", root)
	data["Folders"] = folders
	data["Notice"] = messages.From(r)
	data["Field"] = folderName(name, fieldErrors.For(invalid, "name", "name_taken"))
	data["IsOwner"] = func(fo Folder) bool { return fo.CreatedBy == s.AccountID() || s.IsAdmin() }
	f.render(w, status, "document_folders.html", data)
}

func folderName(value, msg string) form.Field {
	return form.Field{ID: "folder-name", Name: "name", Label: "フォルダの名前", Value: value, Required: true, MaxLength: maxFolderRunes, Error: msg,
		Hint: "1〜50文字。名前はログインしている全員に見えます。"}
}

func (f *Feature) handleFolders(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	f.renderFolders(w, r, s, "", "", http.StatusOK)
}

func (f *Feature) handleAddFolder(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	name := r.PostForm.Get("name")
	err := f.store.AddFolder(r.Context(), s.AccountID(), name)
	var input InputError
	if errors.As(err, &input) {
		f.renderFolders(w, r, s, name, input.Field, http.StatusBadRequest)
		return
	}
	if f.failed(w, r, err) {
		return
	}
	notice.Redirect(w, r, "/admin/document-folders", "folder_added")
}

// editableFolder は、操作できるフォルダを返します。作った人とシステム管理者のほかは404です。
func (f *Feature) editableFolder(w http.ResponseWriter, r *http.Request, s features.AdminSession) (Folder, bool) {
	id, ok := docID(w, r, "id")
	if !ok {
		return Folder{}, false
	}
	fo, err := f.store.Folder(r.Context(), id)
	if err == nil && fo.CreatedBy != s.AccountID() && !s.IsAdmin() {
		err = ErrNotFound
	}
	if f.failed(w, r, err) {
		return Folder{}, false
	}
	return fo, true
}

func (f *Feature) renderFolder(w http.ResponseWriter, r *http.Request, s features.AdminSession, fo Folder, name, invalid string, status int) {
	data := s.PageData("フォルダの編集", root)
	data["Folder"] = fo
	data["Field"] = folderName(name, fieldErrors.For(invalid, "name", "name_taken"))
	f.render(w, status, "document_folder.html", data)
}

func (f *Feature) handleFolder(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	if fo, ok := f.editableFolder(w, r, s); ok {
		f.renderFolder(w, r, s, fo, fo.Name, "", http.StatusOK)
	}
}

func (f *Feature) handleRenameFolder(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	fo, ok := f.editableFolder(w, r, s)
	if !ok {
		return
	}
	name := r.PostForm.Get("name")
	err := f.store.RenameFolder(r.Context(), s.AccountID(), s.IsAdmin(), fo.ID, name)
	var input InputError
	if errors.As(err, &input) {
		f.renderFolder(w, r, s, fo, name, input.Field, http.StatusBadRequest)
		return
	}
	if f.failed(w, r, err) {
		return
	}
	notice.Redirect(w, r, "/admin/document-folders", "folder_saved")
}

func (f *Feature) handleFolderDeletePage(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	fo, ok := f.editableFolder(w, r, s)
	if !ok {
		return
	}
	data := s.PageData("フォルダの削除", root)
	data["Folder"] = fo
	f.render(w, http.StatusOK, "document_folder_delete.html", data)
}

func (f *Feature) handleFolderDelete(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	fo, ok := f.editableFolder(w, r, s)
	if !ok {
		return
	}
	err := f.store.DeleteFolder(r.Context(), s.AccountID(), s.IsAdmin(), fo.ID)
	if errors.Is(err, ErrConflict) {
		data := s.PageData("フォルダの削除", root)
		data["Folder"] = fo
		data["Error"] = "文書が入っているので削除できません。読めない文書が入っていることもあります。"
		f.render(w, http.StatusConflict, "document_folder_delete.html", data)
		return
	}
	if f.failed(w, r, err) {
		return
	}
	notice.Redirect(w, r, "/admin/document-folders", "folder_deleted")
}

// ownerRow は移管の画面の1行です。件数だけを出し、タイトルは読みません。
type ownerRow struct {
	ID    int64
	Name  string
	Count int
}

// transferRow は移管の記録の表示です。
type transferRow struct {
	From, To, By string
	Count        int
	At           time.Time
}

func (f *Feature) renderTransfer(w http.ResponseWriter, r *http.Request, s features.AdminSession, from, to, invalid, errMsg string, status int) {
	counts, err := f.store.OwnerCounts(r.Context())
	if f.failed(w, r, err) {
		return
	}
	who, err := f.people(r.Context())
	if f.failed(w, r, err) {
		return
	}
	var rows []ownerRow
	var fromOptions []form.Option
	for _, c := range counts {
		if who.active(c.OwnerID) {
			continue
		}
		rows = append(rows, ownerRow{ID: c.OwnerID, Name: who.Name(c.OwnerID), Count: c.Count})
		value := strconv.FormatInt(c.OwnerID, 10)
		fromOptions = append(fromOptions, form.Option{Value: value, Label: who.Name(c.OwnerID) + "（ID " + value + "）", Selected: value == from})
	}
	var toOptions []form.Option
	for _, o := range who.options(s.AccountID(), nil) {
		o.Selected = o.Value == to
		toOptions = append(toOptions, o)
	}
	history, err := f.store.Transfers(r.Context())
	if f.failed(w, r, err) {
		return
	}
	var log []transferRow
	for _, t := range history {
		log = append(log, transferRow{From: who.Name(t.From), To: who.Name(t.To), By: who.Name(t.By), Count: t.Count, At: t.At})
	}
	data := s.PageData("文書の所有者の変更", root)
	data["Owners"] = rows
	data["History"] = log
	data["Notice"] = messages.From(r)
	data["Error"] = errMsg
	data["Fields"] = []form.Field{
		{ID: "transfer-from", Name: "from", Label: "元の所有者", Required: true, Options: fromOptions},
		{ID: "transfer-to", Name: "to", Label: "移す先", Required: true, Options: toOptions, Error: fieldErrors.For(invalid, "to"), Hint: "自分は選べません。移す先の人は、文書の内容を全て読めるようになります。"},
	}
	f.render(w, status, "document_transfer.html", data)
}

func (f *Feature) handleTransferPage(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	f.renderTransfer(w, r, s, "", "", "", "", http.StatusOK)
}

// handleTransfer は、有効でない所有者の文書を移します。判定は書き込みの権利を得てから行います。
func (f *Feature) handleTransfer(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	rawFrom, rawTo := r.PostForm.Get("from"), r.PostForm.Get("to")
	from, okFrom := form.ID(rawFrom)
	to, okTo := form.ID(rawTo)
	if !okFrom || !okTo {
		f.renderTransfer(w, r, s, rawFrom, rawTo, "to", "", http.StatusBadRequest)
		return
	}
	_, err := f.store.TransferOwner(r.Context(), s.AccountID(), from, to, func(from, to int64) (bool, error) {
		who, err := f.people(r.Context())
		if err != nil {
			return false, err
		}
		return !who.active(from) && who.active(to), nil
	})
	var input InputError
	switch {
	case errors.As(err, &input):
		f.renderTransfer(w, r, s, rawFrom, rawTo, input.Field, "", http.StatusBadRequest)
		return
	case errors.Is(err, ErrConflict):
		f.renderTransfer(w, r, s, rawFrom, rawTo, "", conflictMessage, http.StatusConflict)
		return
	}
	if f.failed(w, r, err) {
		return
	}
	f.logger.Info("documents owner transferred", web.RequestIDAttr(w))
	notice.Redirect(w, r, root+"/transfer", "transferred")
}
