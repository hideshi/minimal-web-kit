package contact

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"minimal-web-kit/features"
	"minimal-web-kit/internal/store"
	"minimal-web-kit/internal/web"
	"minimal-web-kit/internal/web/form"
	"minimal-web-kit/internal/web/notice"
	"minimal-web-kit/internal/web/page"
)

var adminPageNames = []string{"admin_messages.html", "admin_message.html", "admin_message_delete.html", "admin_spam_delete.html"}

var adminNotices = notice.Messages{
	"active":       "対応中にしました。",
	"done":         "完了にしました。",
	"unread":       "未読に戻しました。",
	"spam":         "スパムにしました。",
	"note":         "メモを保存しました。",
	"deleted":      "削除しました。",
	"spam_deleted": "スパムを削除しました。",
	"no_spam":      "スパムはありません。",
}

// SetAdmin は管理者認証を受け取り、受信箱のテンプレートを管理画面のレイアウトで解析します。
func (f *Feature) SetAdmin(admin features.Admin) error {
	pages, err := web.ParsePages(admin.Layout(), viewsFS, adminPageNames...)
	if err != nil {
		return err
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
	mux.HandleFunc("GET /admin/contact/all", f.admin.AdminPage(f.handleListAll))
	mux.HandleFunc("GET /admin/contact/spam/delete", f.admin.AdminPage(f.handleSpamDeletePage))
	mux.HandleFunc("POST /admin/contact/spam/delete", f.admin.AdminForm(f.handleSpamDelete))
	mux.HandleFunc("GET /admin/contact/{id}", f.admin.AdminPage(f.handleShow))
	mux.HandleFunc("POST /admin/contact/{id}/status", f.admin.AdminForm(f.handleStatus))
	mux.HandleFunc("POST /admin/contact/{id}/note", f.admin.AdminForm(f.handleNote))
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
	case store.Busy(err):
		web.WriteBusy(w)
	default:
		f.logger.Error("contact admin failed", web.RequestIDAttr(w))
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
	return true
}

// messageRow は一覧の1行です。DeleteSoon は、削除が近いときの「あと3日」のような表示です。
type messageRow struct {
	Message
	DeleteSoon string
}

// deleteSoon は、削除まで7日以内なら残りの日数を返します。1日に満たない分は切り上げます。
func deleteSoon(left time.Duration) string {
	const day = 24 * time.Hour
	if left > 7*day {
		return ""
	}
	if left < 0 {
		left = 0
	}
	return fmt.Sprintf("あと%d日", max(int((left+day-1)/day), 1))
}

// handleList は、未読と対応中だけの一覧です。
func (f *Feature) handleList(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	f.renderList(w, r, s, true, "/admin/contact")
}

// handleListAll は、全件の一覧です。
func (f *Feature) handleListAll(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	f.renderList(w, r, s, false, "/admin/contact/all")
}

func (f *Feature) renderList(w http.ResponseWriter, r *http.Request, s features.AdminSession, openOnly bool, base string) {
	pg, ok := page.Parse(r, 20, 5000)
	if !ok {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	list, err := f.store.List(r.Context(), openOnly, pg.Limit(), pg.Offset())
	if f.failed(w, r, err) {
		return
	}
	unread, active, spam, err := f.store.Counts(r.Context())
	if f.failed(w, r, err) {
		return
	}
	list = page.Trim(&pg, list)
	pg.Base, pg.Label, pg.PrevText, pg.NextText = base, "お問い合わせの一覧のページ送り", "新しい受付", "過去の受付"
	now := f.now()
	rows := make([]messageRow, 0, len(list))
	for _, m := range list {
		rows = append(rows, messageRow{Message: m, DeleteSoon: deleteSoon(f.cfg.Retention.DeleteAt(m).Sub(now))})
	}
	data := s.PageData("お問い合わせの管理", "/admin/contact")
	data["Messages"] = rows
	data["OpenOnly"] = openOnly
	data["Unread"] = unread
	data["Active"] = active
	data["Spam"] = spam
	data["RetentionDays"] = days(f.cfg.Retention.Base)
	data["ActiveMaxDays"] = days(f.cfg.Retention.ActiveMax)
	data["Page"] = pg
	data["Notice"] = adminNotices.From(r)
	f.renderAdmin(w, http.StatusOK, "admin_messages.html", data)
}

// noteField は、詳細の画面のメモの入力欄です。
func noteField(value, fieldError string) form.Field {
	return form.Field{ID: "contact-note", Name: "note", Label: "メモ", Optional: true, Value: value, Rows: 5, MaxLength: maxNoteRunes, Error: fieldError,
		Hint: "2,000文字以内。対応の引き継ぎに使います。送った人の返信先以外の個人情報や、病歴など配慮が必要な情報は書かないでください。"}
}

// renderMessage は詳細の画面を描きます。入力の失敗では、書いたメモとエラーを残します。
func (f *Feature) renderMessage(w http.ResponseWriter, r *http.Request, s features.AdminSession, status int, m Message, field form.Field) {
	data := s.PageData("お問い合わせ: "+m.DisplayName(), "/admin/contact")
	data["Message"] = m
	data["Paragraphs"] = form.Paragraphs(m.Body)
	data["NoteField"] = field
	data["DeleteAt"] = f.cfg.Retention.DeleteAt(m)
	// 対応中以外にすると、受付からの保持期間で削除の時期を判定し直す
	data["DeleteOnChange"] = m.Status == statusActive && !m.CreatedAt.Add(f.cfg.Retention.Base).After(f.now())
	data["Notice"] = adminNotices.From(r)
	f.renderAdmin(w, status, "admin_message.html", data)
}

// handleShow は1件を表示します。開いただけでは状態も最終更新も変えない（CONTACT-INV-7）。
func (f *Feature) handleShow(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	id, ok := web.PathID(w, r, "id")
	if !ok {
		return
	}
	m, err := f.store.Get(r.Context(), id)
	if f.failed(w, r, err) {
		return
	}
	f.renderMessage(w, r, s, http.StatusOK, m, noteField(m.Note, ""))
}

// handleNote はメモを保存します。ログには番号だけを出す（CONTACT-INV-6）。
func (f *Feature) handleNote(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	id, ok := web.PathID(w, r, "id")
	if !ok {
		return
	}
	note := r.PostForm.Get("note")
	err := f.store.SetNote(r.Context(), id, note)
	var invalid InputError
	if errors.As(err, &invalid) {
		m, err := f.store.Get(r.Context(), id)
		if f.failed(w, r, err) {
			return
		}
		f.renderMessage(w, r, s, http.StatusBadRequest, m, noteField(note, "メモを保存できません。2,000文字以内にして、使えない文字を取り除いてください。"))
		return
	}
	if f.failed(w, r, err) {
		return
	}
	f.logger.Info("contact note saved", slog.Int64("message_id", id))
	notice.Redirect(w, r, "/admin/contact/"+strconv.FormatInt(id, 10), "note")
}

func (f *Feature) handleStatus(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	id, ok := web.PathID(w, r, "id")
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
	id, ok := web.PathID(w, r, "id")
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
	id, ok := web.PathID(w, r, "id")
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
	_, _, spam, err := f.store.Counts(r.Context())
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
