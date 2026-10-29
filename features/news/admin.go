package news

import (
	"bytes"
	"log/slog"
	"net/http"

	"minimal-web-kit/features"
	"minimal-web-kit/internal/web"
)

// adminPageNames は、管理画面（管理者認証のレイアウトで描く）のテンプレートです。
var adminPageNames = []string{"admin_posts.html", "admin_post_form.html", "admin_post_delete.html"}

// SetAdmin は、管理者認証（features/auth）を受け取り、管理画面のテンプレートをその管理画面のレイアウトで解析します。
// ログイン・セッション・CSRFの確認は、管理者認証が行います。
func (f *Feature) SetAdmin(admin features.Admin) error {
	pages, err := web.ParsePages(admin.Layout(), viewsFS, adminPageNames...)
	if err != nil {
		return err
	}
	f.admin = admin
	f.adminPages = pages
	return nil
}

// AdminNavLinks は、管理画面のナビに「お知らせの管理」を加えます。
func (f *Feature) AdminNavLinks() []web.NavLink {
	return []web.NavLink{{Label: "お知らせの管理", Href: "/admin/news"}}
}

// pageData は、管理画面のレイアウトに渡す値に、予約公開の判定に使う現在時刻を加えます。
func (f *Feature) pageData(s features.AdminSession, title string) map[string]any {
	data := s.PageData(title, "/admin/news")
	data["Now"] = f.now()
	return data
}

func (f *Feature) requestID(w http.ResponseWriter) slog.Attr {
	return slog.String("request_id", w.Header().Get("X-Request-ID"))
}

func (f *Feature) renderAdmin(w http.ResponseWriter, page string, data map[string]any) {
	f.renderAdminStatus(w, http.StatusOK, page, data)
}

func (f *Feature) renderAdminStatus(w http.ResponseWriter, status int, page string, data map[string]any) {
	var buf bytes.Buffer
	if err := f.adminPages[page].ExecuteTemplate(&buf, "layout", data); err != nil {
		f.logger.Error("news render admin failed", f.requestID(w))
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}
