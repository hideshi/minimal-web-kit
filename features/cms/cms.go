// minimal-web-kit: bundled-feature cms
package cms

import (
	"database/sql"
	_ "embed"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"os"
	"time"

	"minimal-web-kit/features"
	"minimal-web-kit/internal/web"
)

//go:embed migration.sql
var migrationSQL string

// Feature は同梱機能のCMS（お知らせ機能）です。お知らせの公開ページ・読み取りAPIと、記事の管理画面を提供します。
// 管理者のログインは、同梱機能の管理者認証（features/auth）が担います（make add-cms で一緒に有効になる）。
// 公開ページ・APIと管理画面は、CoreのCSRF検査と匿名セッションの照合を通さない独立経路で提供します。
type Feature struct {
	logger      *slog.Logger
	cfg         Config
	db          *sql.DB
	posts       *PostStore
	admin       features.Admin
	publicPages map[string]*template.Template
	adminPages  map[string]*template.Template
	layout      *web.Layout
	now         func() time.Time
}

func NewFeature(logger *slog.Logger) *Feature {
	return &Feature{
		logger:      logger,
		publicPages: parsePublicTemplates(),
		now:         time.Now,
	}
}

func (f *Feature) Name() string {
	return "cms"
}

// Init は設定を検証し、マイグレーションを適用します。設定が不正な場合はエラーを返して起動を止めます。
func (f *Feature) Init(db *sql.DB) error {
	cfg, err := LoadConfig(os.Getenv)
	if err != nil {
		// 設定エラーの文言は設定値を含まないため、原因（どの設定が不正か）を記録する
		f.logger.Error("cms config invalid", slog.String("reason", err.Error()))
		return fmt.Errorf("cms config: %w", err)
	}
	if err := Migrate(db); err != nil {
		return err
	}
	f.cfg = cfg
	f.db = db
	f.posts = NewPostStore(db)
	return nil
}

// Migrate はCMSのテーブルを作成します。既存のデータは変更しません。
func Migrate(db *sql.DB) error {
	if _, err := db.Exec(migrationSQL); err != nil {
		return fmt.Errorf("cms migration: %w", err)
	}
	return nil
}

// RegisterRoutes はCoreの経路に何も登録しません（CMSの経路はすべて独立経路）。
func (f *Feature) RegisterRoutes(mux *http.ServeMux, sm *web.SessionManager) {
}

// NavLinks は、ホーム等の共通のナビゲーションに「お知らせ」を加えます。
func (f *Feature) NavLinks() []web.NavLink {
	return []web.NavLink{{Label: "お知らせ", Href: "/news"}}
}

// RegisterIsolatedRoutes はCMSの公開ページ・APIと、記事の管理画面を登録します。
// 管理画面は、管理者認証を受け取っている場合だけ登録します。
func (f *Feature) RegisterIsolatedRoutes(mux *web.IsolatedMux) {
	f.registerPublicRoutes(mux)
	if f.admin != nil {
		f.registerPostAdminRoutes(mux)
	}
}
