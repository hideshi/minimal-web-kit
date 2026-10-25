// minimal-web-kit: bundled-cms-template
package cms

import (
	"context"
	"crypto/rand"
	"database/sql"
	_ "embed"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"os"
	"time"

	"minimal-web-kit/internal/web"
)

//go:embed migration.sql
var migrationSQL string

const (
	// 管理者セッションは、無操作2時間または発行から12時間で失効する。
	adminSessionIdle = 2 * time.Hour
	adminSessionMax  = 12 * time.Hour
	// 初回登録・復旧用のトークンは発行から15分で失効する。
	setupTokenTTL = 15 * time.Minute
)

// Feature はお知らせを管理するCMSテンプレートです。
// 公開ページ・APIと管理画面は、CoreのCSRF検査と匿名セッションの照合を通さない独立経路で提供します。
type Feature struct {
	logger      *slog.Logger
	cfg         Config
	db          *sql.DB
	posts       *PostStore
	publicPages map[string]*template.Template
	now         func() time.Time
}

func NewFeature(logger *slog.Logger) *Feature {
	return &Feature{logger: logger, publicPages: parsePublicTemplates(), now: time.Now}
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

// Migrate はCMSのテーブルを作成し、WebAuthnの user.id を用意します。既存のデータは変更しません。
func Migrate(db *sql.DB) error {
	if _, err := db.Exec(migrationSQL); err != nil {
		return fmt.Errorf("cms migration: %w", err)
	}
	userID := make([]byte, 16)
	if _, err := rand.Read(userID); err != nil {
		return fmt.Errorf("cms webauthn user id: %w", err)
	}
	if _, err := db.Exec(
		"INSERT OR IGNORE INTO cms_settings (id, webauthn_user_id, created_at) VALUES (1, ?, ?)",
		userID, time.Now().Unix(),
	); err != nil {
		return fmt.Errorf("cms settings: %w", err)
	}
	return nil
}

// RegisterRoutes はCoreの経路に何も登録しません（CMSの経路はすべて独立経路）。
func (f *Feature) RegisterRoutes(mux *http.ServeMux, sm *web.SessionManager) {
}

// RegisterIsolatedRoutes はCMSの公開ページ・API・管理画面を登録します。
func (f *Feature) RegisterIsolatedRoutes(mux *web.IsolatedMux) {
	f.registerPublicRoutes(mux)
}

// Purge は期限切れの管理者セッションと、使用済み・期限切れの登録トークンを削除します。
// 記事とパスキーは削除しません。
func (f *Feature) Purge(ctx context.Context) error {
	if f.db == nil {
		return nil
	}
	now := f.now()
	if _, err := f.db.ExecContext(ctx,
		"DELETE FROM cms_admin_sessions WHERE expires_at <= ? OR last_seen_at <= ?",
		now.Unix(), now.Add(-adminSessionIdle).Unix(),
	); err != nil {
		return err
	}
	_, err := f.db.ExecContext(ctx,
		"DELETE FROM cms_setup_tokens WHERE expires_at <= ? OR used_at IS NOT NULL",
		now.Unix(),
	)
	return err
}
