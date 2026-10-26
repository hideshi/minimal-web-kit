package cms

import (
	"fmt"
	"strings"

	"minimal-web-kit/internal/web/guard"
)

// Config はCMS（お知らせ機能）の設定です。起動時（Init）に検証し、不正なら起動を止めます。
// 管理者のログインに関する設定（AUTH_ORIGIN 等）は、管理者認証（features/auth）が持ちます。
type Config struct {
	// CORSOrigins は読み取りAPIを読める別オリジンの一覧です。空なら許可しません。
	CORSOrigins []string
}

// LoadConfig は環境変数から設定を読み込み、検証します。
// エラーメッセージには設定値を含めません（ログへの混入を避けるため）。
func LoadConfig(getenv func(string) string) (Config, error) {
	var cfg Config
	for _, raw := range strings.Split(getenv("CMS_CORS_ORIGINS"), ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		o, _, err := guard.NormalizeOrigin(raw)
		if err != nil {
			return Config{}, fmt.Errorf("CMS_CORS_ORIGINS: %w", err)
		}
		cfg.CORSOrigins = append(cfg.CORSOrigins, o)
	}
	return cfg, nil
}
