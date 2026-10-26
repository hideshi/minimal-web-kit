// minimal-web-kit: bundled-feature news
package news

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

// Feature は同梱機能のお知らせ機能（news）です。お知らせの公開ページ・読み取りAPIと、記事の管理画面を提供します。
// 管理者のログインは、同梱機能の管理者認証（features/auth）が担います（make add-news で一緒に有効になる）。
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
	return "news"
}

// Init は設定を検証し、マイグレーションを適用します。設定が不正な場合はエラーを返して起動を止めます。
func (f *Feature) Init(db *sql.DB) error {
	cfg, err := LoadConfig(os.Getenv)
	if err != nil {
		// 設定エラーの文言は設定値を含まないため、原因（どの設定が不正か）を記録する
		f.logger.Error("news config invalid", slog.String("reason", err.Error()))
		return fmt.Errorf("news config: %w", err)
	}
	if err := Migrate(db); err != nil {
		return err
	}
	f.cfg = cfg
	f.db = db
	f.posts = NewPostStore(db)
	return nil
}

// Migrate はお知らせ機能のテーブルを作成します。既存のデータは変更しません。
// 改名前（CMS）の表 cms_posts があれば、記事とIDの採番を残したまま news_posts に名前を変えます。
func Migrate(db *sql.DB) error {
	if err := renameLegacyTable(db); err != nil {
		return fmt.Errorf("news migration: %w", err)
	}
	if _, err := db.Exec(migrationSQL); err != nil {
		return fmt.Errorf("news migration: %w", err)
	}
	return nil
}

// renameLegacyTable は、改名前の表 cms_posts を news_posts に変えます。両方ある場合は判断できないため、起動を止めます。
// SQLiteの ALTER TABLE ... RENAME TO は、AUTOINCREMENTの採番（sqlite_sequence）も引き継ぎます。
func renameLegacyTable(db *sql.DB) error {
	var legacy, current int
	if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'cms_posts'").Scan(&legacy); err != nil {
		return err
	}
	if legacy == 0 {
		return nil
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'news_posts'").Scan(&current); err != nil {
		return err
	}
	if current > 0 {
		return fmt.Errorf("both cms_posts and news_posts exist; keep one of them")
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, q := range []string{
		"ALTER TABLE cms_posts RENAME TO news_posts",
		"DROP INDEX IF EXISTS idx_cms_posts_published",
	} {
		if _, err := tx.Exec(q); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// RegisterRoutes はCoreの経路に何も登録しません（お知らせ機能の経路はすべて独立経路）。
func (f *Feature) RegisterRoutes(mux *http.ServeMux, sm *web.SessionManager) {
}

// NavLinks は、ホーム等の共通のナビゲーションに「お知らせ」を加えます。
func (f *Feature) NavLinks() []web.NavLink {
	return []web.NavLink{{Label: "お知らせ", Href: "/news"}}
}

// RegisterIsolatedRoutes はお知らせの公開ページ・APIと、記事の管理画面を登録します。
// 管理画面は、管理者認証を受け取っている場合だけ登録します。
func (f *Feature) RegisterIsolatedRoutes(mux *web.IsolatedMux) {
	f.registerPublicRoutes(mux)
	if f.admin != nil {
		f.registerPostAdminRoutes(mux)
	}
}
