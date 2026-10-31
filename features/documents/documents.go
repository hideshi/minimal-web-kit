// minimal-web-kit: bundled-feature documents
package documents

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"time"

	"minimal-web-kit/features"
	"minimal-web-kit/internal/web"
)

//go:embed migration.sql
var migrationSQL string

//go:embed views/*.html
var viewsFS embed.FS

// Feature は同梱機能の社内文書（documents）です。回覧や規定を作り、全員か特定の人へ共有します。
// 全ての画面にログインが要ります。管理者認証のアカウントの一覧を使うので、federatedauth とは組み合わせられません。
type Feature struct {
	logger    *slog.Logger
	store     *Store
	admin     features.Admin
	directory features.AccountDirectory
	pages     map[string]*template.Template
	now       func() time.Time
}

func NewFeature(logger *slog.Logger) *Feature {
	return &Feature{logger: logger, now: time.Now}
}

func (f *Feature) Name() string { return "documents" }

// Init はマイグレーションを適用します。
func (f *Feature) Init(db *sql.DB) error {
	if _, err := db.Exec(migrationSQL); err != nil {
		return fmt.Errorf("documents migration: %w", err)
	}
	f.store = NewStore(db, f.now)
	return nil
}

// RegisterRoutes はCoreの経路に何も登録しません。
func (f *Feature) RegisterRoutes(mux *http.ServeMux, sm *web.SessionManager) {}

// SetAdmin は管理者認証を受け取り、画面のテンプレートを解析します。
func (f *Feature) SetAdmin(admin features.Admin) error {
	pages, err := web.ParsePages(admin.Layout(), viewsFS, pageNames...)
	if err != nil {
		return err
	}
	f.admin, f.pages = admin, pages
	return nil
}

// SetAccountDirectory はアカウントの一覧を受け取ります。
func (f *Feature) SetAccountDirectory(d features.AccountDirectory) { f.directory = d }

// AdminNavLinks は、全てのロールのナビに「文書」を加えます。
func (f *Feature) AdminNavLinks() []web.NavLink {
	return []web.NavLink{{Label: "文書", Href: "/admin/documents", AllRoles: true}}
}

// Purge は保持期間を過ぎた移管の記録を消します。
func (f *Feature) Purge(ctx context.Context) error { return f.store.Purge(ctx) }

// PrivacyNotices は、文書の機能で保存する情報を、ログイン後の画面に載せます。社内の人の情報なので公開のポリシーには載せません。
// docs/data-inventory.md の11節と揃えます。
func (f *Feature) PrivacyNotices() []web.PrivacyNotice {
	return []web.PrivacyNotice{{
		Title:    "社内文書で保存する情報",
		Internal: true,
		Lead:     "ログインして文書の機能を使う人について、次の情報を保存します。呼び名は、共有先や承認者を選ぶ画面で、ログインしている全員に見えます。AIエージェントの接続で文書の機能を有効にしたサイトでは、接続を許可された人が読み取った本文・タイトル・呼び名が、その人の使うAIのサービスへ送られます。",
		Rows: []web.PrivacyRow{
			{Kind: "文書", Items: "タイトル、本文、所有者のアカウントID、共有先、作成・更新の日時", Purpose: "文書の作成と共有", Retention: "所有者が削除するまで"},
			{Kind: "既読", Items: "文書の版、開いた人のアカウントID、最初に開いた日時", Purpose: "所有者が読んだ人を確かめる", Retention: "文書を削除するまで"},
			{Kind: "承認", Items: "文書の版、承認者のアカウントID、判断、差し戻しの理由、判断の日時", Purpose: "社内の確認の記録", Retention: "文書を削除するまで"},
			{Kind: "所有者の移管", Items: "移管の元・先・操作者のアカウントID、件数、日時", Purpose: "移管の記録", Retention: "3年"},
		},
	}}
}
