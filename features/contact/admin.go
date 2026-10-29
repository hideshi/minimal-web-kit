package contact

import (
	"bytes"
	"errors"
	"html/template"
	"log/slog"
	"net/http"
	"strconv"

	"minimal-web-kit/features"
	"minimal-web-kit/internal/web"
	"minimal-web-kit/internal/web/form"
	"minimal-web-kit/internal/web/notice"
	"minimal-web-kit/internal/web/page"
)

var adminPageNames = []string{"admin_messages.html", "admin_message.html", "admin_message_delete.html", "admin_spam_delete.html"}

var adminNotices = notice.Messages{
	"read":         "既読にしました。",
	"unread":       "未読に戻しました。",
	"spam":         "スパムにしました。",
	"deleted":      "削除しました。",
	"spam_deleted": "スパムを削除しました。",
	"no_spam":      "スパムはありません。",
}

// SetAdmin は管理者認証を受け取り、受信箱のテンプレートを管理画面のレイアウトで解析します。
func (f *Feature) SetAdmin(admin features.Admin) error {
	pages := map[string]*template.Template{}
	for _, name := range adminPageNames {
		t, err := web.ParsePage(admin.Layout(), viewsFS, "views/"+name)
		if err != nil {
			return err
		}
		pages[name] = t
	}
	f.admin = admin
	f.adminPages = pages
	return nil
}

// AdminNavLinks は、管理画面のナビに「お問い合わせの管理」を、管理者にだけ出します（CONTACT-INV-3）。
func (f *Feature) AdminNavLinks() []web.NavLink {
	return []web.NavLink{{Label: "お問い合わせの管理", Href: "/admin/contact", AdminOnly: true}}
}

// registerAdmin は受信箱の経路を、管理者だけに許す AdminPage・AdminForm で登録します。
// 経路は文字列のリテラルで書く（管理画面の検査が、ソースに書いた経路と登録された経路を突き合わせる）。
func (f *Feature) registerAdmin(mux *web.IsolatedMux) {
	mux.HandleFunc("GET /admin/contact", f.admin.AdminPage(f.handleList))
	mux.HandleFunc("GET /admin/contact/spam/delete", f.admin.AdminPage(f.handleSpamDeletePage))
	mux.HandleFunc("POST /admin/contact/spam/delete", f.admin.AdminForm(f.handleSpamDelete))
	mux.HandleFunc("GET /admin/contact/{id}", f.admin.AdminPage(f.handleShow))
	mux.HandleFunc("POST /admin/contact/{id}/status", f.admin.AdminForm(f.handleStatus))
	mux.HandleFunc("GET /admin/contact/{id}/delete", f.admin.AdminPage(f.handleDeletePage))
	mux.HandleFunc("POST /admin/contact/{id}/delete", f.admin.AdminForm(f.handleDelete))
}

func (f *Feature) renderAdmin(w http.ResponseWriter, status int, name string, data map[string]any) {
	var buf bytes.Buffer
	if err := f.adminPages[name].ExecuteTemplate(&buf, "layout", data); err != nil {
		f.logger.Error("contact render admin failed")
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
		f.logger.Error("contact admin failed", slog.String("request_id", w.Header().Get("X-Request-ID")))
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
	return true
}

func messageID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		http.NotFound(w, r)
		return 0, false
	}
	return id, true
}

func (f *Feature) handleList(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	pg, ok := page.Parse(r, 20, 5000)
	if !ok {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	list, err := f.store.List(r.Context(), pg.Limit(), pg.Offset())
	if f.failed(w, r, err) {
		return
	}
	unread, spam, err := f.store.Counts(r.Context())
	if f.failed(w, r, err) {
		return
	}
	list = page.Trim(&pg, list)
	pg.Base, pg.Label, pg.PrevText, pg.NextText = "/admin/contact", "お問い合わせの一覧のページ送り", "新しい受付", "過去の受付"
	data := s.PageData("お問い合わせの管理", "/admin/contact")
	data["Messages"] = list
	data["Unread"] = unread
	data["Spam"] = spam
	data["RetentionDays"] = int(f.cfg.Retention.Hours() / 24)
	data["Page"] = pg
	data["Notice"] = adminNotices.From(r)
	f.renderAdmin(w, http.StatusOK, "admin_messages.html", data)
}

// handleShow は1件を表示します。開いただけでは既読にしない（CONTACT-INV-7）。
func (f *Feature) handleShow(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	id, ok := messageID(w, r)
	if !ok {
		return
	}
	m, err := f.store.Get(r.Context(), id)
	if f.failed(w, r, err) {
		return
	}
	data := s.PageData("お問い合わせ: "+m.DisplayName(), "/admin/contact")
	data["Message"] = m
	data["Paragraphs"] = form.Paragraphs(m.Body)
	data["Notice"] = adminNotices.From(r)
	f.renderAdmin(w, http.StatusOK, "admin_message.html", data)
}

func (f *Feature) handleStatus(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	id, ok := messageID(w, r)
	if !ok {
		return
	}
	status := r.PostForm.Get("status")
	err := f.store.SetStatus(r.Context(), id, status)
	var invalid InputError
	if errors.As(err, &invalid) {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	if f.failed(w, r, err) {
		return
	}
	f.logger.Info("contact status changed", slog.Int64("message_id", id), slog.String("status", status))
	notice.Redirect(w, r, "/admin/contact/"+strconv.FormatInt(id, 10), status)
}

func (f *Feature) handleDeletePage(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	id, ok := messageID(w, r)
	if !ok {
		return
	}
	m, err := f.store.Get(r.Context(), id)
	if f.failed(w, r, err) {
		return
	}
	data := s.PageData("お問い合わせの削除", "/admin/contact")
	data["Message"] = m
	f.renderAdmin(w, http.StatusOK, "admin_message_delete.html", data)
}

func (f *Feature) handleDelete(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	id, ok := messageID(w, r)
	if !ok {
		return
	}
	if f.failed(w, r, f.store.Delete(r.Context(), id)) {
		return
	}
	f.logger.Info("contact message deleted", slog.Int64("message_id", id))
	notice.Redirect(w, r, "/admin/contact", "deleted")
}

// handleSpamDeletePage は、スパムをまとめて削除する前の確認画面です。0件なら一覧へ移します。
func (f *Feature) handleSpamDeletePage(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	_, spam, err := f.store.Counts(r.Context())
	if f.failed(w, r, err) {
		return
	}
	if spam == 0 {
		notice.Redirect(w, r, "/admin/contact", "no_spam")
		return
	}
	data := s.PageData("スパムの削除", "/admin/contact")
	data["Spam"] = spam
	f.renderAdmin(w, http.StatusOK, "admin_spam_delete.html", data)
}

func (f *Feature) handleSpamDelete(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	n, err := f.store.DeleteSpam(r.Context())
	if f.failed(w, r, err) {
		return
	}
	f.logger.Info("contact spam deleted", slog.Int64("count", n))
	notice.Redirect(w, r, "/admin/contact", "spam_deleted")
}
