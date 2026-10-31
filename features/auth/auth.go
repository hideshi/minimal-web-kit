// minimal-web-kit: bundled-feature auth
package auth

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
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

// Feature は同梱機能の管理者認証です。ロール（システム管理者・サイト管理者）を持つ複数のアカウントが、パスキー（WebAuthn）だけでログインします。
// ログイン・パスキーの登録と管理・アカウントの管理の画面と、他の機能が管理画面を守るための口（features.Admin）を提供します。
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
	loginNext  func(*http.Request) string
	now        func() time.Time
	// agentEnabled は、エージェントの認証を使う機能が登録されているかです。
	agentEnabled bool
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

// PrivacyNotices は、管理画面を使うアカウントについて保存する情報を、プライバシーポリシーの画面に載せます。
// 内容は docs/data-inventory.md の4節と揃えます。
func (f *Feature) PrivacyNotices() []web.PrivacyNotice {
	notices := []web.PrivacyNotice{{
		Title: "管理画面のアカウントで保存する情報",
		Lead:  "管理画面にログインする人（システム管理者・サイト管理者・一般ユーザー）について、次の情報を保存します。サイトの閲覧者の情報ではありません。氏名・メールアドレス・パスワード・生体情報は保存しません。",
		Rows: []web.PrivacyRow{
			{Kind: "アカウント", Items: "呼び名（実名やメールアドレスを入れない運用のラベル）、ロール、認証器に登録するランダムな識別子、作成・停止・最終ログインの日時", Purpose: "管理画面へのログインと権限の判定", Retention: "システム管理者が削除するまで"},
			{Kind: "パスキー", Items: "公開鍵、認証器の識別子、端末名、署名カウンタ、登録・最終使用の日時（秘密鍵と生体情報は認証器の外に出ません）", Purpose: "本人の確認と、複製の疑いの検出", Retention: "本人かシステム管理者が削除するまで"},
			{Kind: "ログイン状態", Items: "Cookie（admin_session）のランダムなトークンと、そのSHA-256ハッシュ、発行・最終操作の日時", Purpose: "ログイン状態の維持", Retention: "無操作2時間または発行から12時間。ログアウト時等にも削除"},
			{Kind: "パスキーの登録用URL", Items: "登録トークンのSHA-256ハッシュ、有効期限、使用の日時", Purpose: "パスキーの登録（1回限り）", Retention: "発行から15分または24時間。使用済み・期限切れは自動で削除"},
		},
	}}
	if f.agentEnabled {
		notices = append(notices, web.PrivacyNotice{
			Title: "AIエージェントの接続で保存する情報",
			Lead:  "管理画面のアカウントでAIエージェントを接続する人について、次の情報を保存します。書き込みの内容は、確認のために保存しません。",
			Rows: []web.PrivacyRow{
				{Kind: "接続の手続き", Items: "手続きのCookie（agent_flow）のSHA-256ハッシュ、クライアント、戻り先、クライアントが発行した一時的な値、接続先、権限", Purpose: "接続の完了", Retention: "5分。完了したものは、コードの再利用の検出のため8時間"},
				{Kind: "アクセストークン", Items: "トークンのSHA-256ハッシュ、クライアント、連携用の識別子、接続先、権限、失効の世代、発行日時", Purpose: "AIエージェントの要求の本人確認", Retention: "発行から8時間"},
			},
		})
	}
	return notices
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
		if errors.Is(err, ErrOldSchema) {
			f.logger.Error("auth old schema found; run auth-recover -reset-schema")
		}
		return err
	}
	f.cfg = cfg
	f.db = db
	f.store = newAdminStore(db, f.now)
	return nil
}

// Migrate は管理者認証のテーブルを作成します。既存のデータは変更しません。
// アカウント導入前の形式の表が残っている場合は、ErrOldSchema を返します（auth-recover -reset-schema で作り直す）。
func Migrate(db *sql.DB) error {
	old, err := detectOldSchema(context.Background(), db)
	if err != nil {
		return fmt.Errorf("auth schema check: %w", err)
	}
	if old {
		return ErrOldSchema
	}
	if err := migrateRoles(context.Background(), db); err != nil {
		return fmt.Errorf("auth role migration: %w", err)
	}
	if _, err := db.Exec(migrationSQL); err != nil {
		return fmt.Errorf("auth migration: %w", err)
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

// Admin は、管理画面を持つ他の機能（例: お知らせ機能）に、ログインの確認とCSRFの守りを提供します。
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
	if err != nil {
		return err
	}
	if err := purgeAgent(ctx, f.db, now); err != nil {
		return err
	}
	return (&FederationStore{db: f.db, now: f.now}).Purge(ctx)
}
