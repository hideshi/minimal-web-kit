// minimal-web-kit: bundled-feature auth
package auth

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

	"minimal-web-kit/features"
	"minimal-web-kit/internal/web"
	"minimal-web-kit/internal/web/guard"
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

// Feature は同梱機能の管理者認証です。パスキー（WebAuthn）だけでログインする管理者1人を前提とし、
// ログイン・パスキーの登録と管理の画面と、他の機能が管理画面を守るための口（features.Admin）を提供します。
// 画面はすべて、CoreのCSRF検査と匿名セッションの照合を通さない独立経路で提供します。
type Feature struct {
	logger     *slog.Logger
	cfg        Config
	db         *sql.DB
	store      *adminStore
	challenges *challenges
	csrf       *guard.CSRF
	pages      map[string]*template.Template
	navLinks   []web.NavLink
	now        func() time.Time
}

func NewFeature(logger *slog.Logger) *Feature {
	return &Feature{
		logger:     logger,
		challenges: newChallenges(time.Now),
		csrf:       guard.NewCSRF(),
		pages:      parseTemplates(),
		now:        time.Now,
	}
}

func (f *Feature) Name() string {
	return "auth"
}

// Init は設定を検証し、マイグレーションを適用します。設定が不正な場合はエラーを返して起動を止めます。
func (f *Feature) Init(db *sql.DB) error {
	cfg, err := LoadConfig(os.Getenv)
	if err != nil {
		// 設定エラーの文言は設定値を含まないため、原因（どの設定が不正か）を記録する
		f.logger.Error("auth config invalid", slog.String("reason", err.Error()))
		return fmt.Errorf("auth config: %w", err)
	}
	if err := Migrate(db); err != nil {
		return err
	}
	f.cfg = cfg
	f.db = db
	f.store = newAdminStore(db, f.now)
	return nil
}

// Migrate は管理者認証のテーブルを作成し、WebAuthnの user.id を用意します。既存のデータは変更しません。
func Migrate(db *sql.DB) error {
	if _, err := db.Exec(migrationSQL); err != nil {
		return fmt.Errorf("auth migration: %w", err)
	}
	userID := make([]byte, 16)
	if _, err := rand.Read(userID); err != nil {
		return fmt.Errorf("auth webauthn user id: %w", err)
	}
	if _, err := db.Exec(
		"INSERT OR IGNORE INTO auth_settings (id, webauthn_user_id, created_at) VALUES (1, ?, ?)",
		userID, time.Now().Unix(),
	); err != nil {
		return fmt.Errorf("auth settings: %w", err)
	}
	return nil
}

// RegisterRoutes はCoreの経路に何も登録しません（管理者認証の経路はすべて独立経路）。
func (f *Feature) RegisterRoutes(mux *http.ServeMux, sm *web.SessionManager) {
}

// RegisterIsolatedRoutes は、ログイン・パスキーの登録と管理の画面、WebAuthnのAPIを登録します。
func (f *Feature) RegisterIsolatedRoutes(mux *web.IsolatedMux) {
	f.registerRoutes(mux)
}

// Admin は、管理画面を持つ他の機能（例: CMS）に、ログインの確認とCSRFの守りを提供します。
func (f *Feature) Admin() features.Admin {
	return adminAPI{f}
}

// SetAdminNavLinks は、他の機能の管理画面へのリンク（例: お知らせの管理）を、管理画面のナビに加えます。
func (f *Feature) SetAdminNavLinks(links []web.NavLink) {
	f.navLinks = links
}

// Purge は期限切れの管理者セッションと、使用済み・期限切れの登録トークンを削除します。パスキーは削除しません。
func (f *Feature) Purge(ctx context.Context) error {
	if f.db == nil {
		return nil
	}
	now := f.now()
	if _, err := f.db.ExecContext(ctx,
		"DELETE FROM auth_sessions WHERE expires_at <= ? OR last_seen_at <= ?",
		now.Unix(), now.Add(-adminSessionIdle).Unix(),
	); err != nil {
		return err
	}
	_, err := f.db.ExecContext(ctx,
		"DELETE FROM auth_setup_tokens WHERE expires_at <= ? OR used_at IS NOT NULL",
		now.Unix(),
	)
	return err
}
