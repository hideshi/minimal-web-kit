// minimal-web-kit: bundled-feature contact
package contact

import (
	"context"
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

// Feature は同梱機能のお問い合わせ機能（contact）です。公開のフォーム（/contact）と、管理者だけが見られる受信箱を提供します。
// メールは送らず、IPアドレス・User-Agent・Cookieを使いません。スパム対策は docs/plans/contact-plan.md の4節。
type Feature struct {
	logger      *slog.Logger
	cfg         Config
	store       *Store
	tokens      *tokens
	admin       features.Admin
	publicPages map[string]*template.Template
	adminPages  map[string]*template.Template
	layout      *web.Layout
	now         func() time.Time
}

func NewFeature(logger *slog.Logger) *Feature {
	return &Feature{logger: logger, publicPages: parsePublicTemplates(), tokens: newTokens(), now: time.Now}
}

func (f *Feature) Name() string { return "contact" }

// Init は設定を検証し、マイグレーションを適用します。設定が不正な場合は起動を止めます。
func (f *Feature) Init(db *sql.DB) error {
	cfg, err := LoadConfig(os.Getenv)
	if err != nil {
		f.logger.Error("contact config invalid", slog.String("reason", err.Error()))
		return fmt.Errorf("contact config: %w", err)
	}
	if err := migrate(db); err != nil {
		return fmt.Errorf("contact migration: %w", err)
	}
	f.cfg = cfg
	f.store = NewStore(db)
	f.store.now = func() time.Time { return f.now() }
	return nil
}

// RegisterRoutes はCoreの経路に何も登録しません（経路はすべて独立経路）。
func (f *Feature) RegisterRoutes(mux *http.ServeMux, sm *web.SessionManager) {}

// RegisterIsolatedRoutes は公開のフォームと、受信箱を登録します。
func (f *Feature) RegisterIsolatedRoutes(mux *web.IsolatedMux) {
	mux.HandleFunc("GET /contact", f.handleForm)
	mux.HandleFunc("POST /contact", f.handleSubmit)
	if f.admin != nil {
		f.registerAdmin(mux)
	}
}

// ContactLink は、お問い合わせの行き先として /contact を申告します。
func (f *Feature) ContactLink() (web.ContactLink, bool) {
	return web.ContactLink{Href: "/contact"}, true
}

// Purge は保持期間を過ぎたお問い合わせを削除します（起動時と定期のGCから呼ばれる）。
func (f *Feature) Purge(ctx context.Context) error {
	if f.store == nil {
		return nil
	}
	return f.store.Purge(ctx, f.cfg.Retention)
}

// days は期間を日数で返します。
func days(d time.Duration) int { return int(d / (24 * time.Hour)) }

// PrivacyNotices は、プライバシーポリシーの画面に、お問い合わせで保存する情報を載せます。
func (f *Feature) PrivacyNotices() []web.PrivacyNotice {
	return []web.PrivacyNotice{{
		Title: "お問い合わせで保存する情報",
		Lead:  "お問い合わせのフォームから送られた次の情報を保存します。お名前と返信先は、返信が必要な場合だけ任意で書いていただくものです。IPアドレス・ブラウザの種類・Cookieは保存せず、メールも送りません。",
		Rows: []web.PrivacyRow{{
			Kind:      "お問い合わせ",
			Items:     "お問い合わせ内容、お名前（任意）、返信先（任意）、受付日時、対応の状態、対応の記録（運営者のメモ）",
			Purpose:   "お問い合わせへの回答と対応",
			Retention: fmt.Sprintf("受付から%d日で自動で削除。対応を続けている間は、最後の対応から%d日、受付から最長%d日まで保存します。管理者が先に削除することもあります。削除前のバックアップの複製には、その保持期間のあいだ残ります", days(f.cfg.Retention.Base), days(f.cfg.Retention.Base), days(f.cfg.Retention.ActiveMax)),
		}},
	}}
}
