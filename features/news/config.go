package news

import (
	"fmt"
	"strings"

	"minimal-web-kit/internal/web/guard"
)

// Config はお知らせ機能の設定です。起動時（Init）に検証し、不正なら起動を止めます。
// 管理者のログインに関する設定（AUTH_ORIGIN 等）は、管理者認証（features/auth）が持ちます。
type Config struct {
	// CORSOrigins は読み取りAPIを読める別オリジンの一覧です。空なら許可しません。
	CORSOrigins []string
}

// LoadConfig は環境変数から設定を読み込み、検証します。
// エラーメッセージには設定値を含めません（ログへの混入を避けるため）。
func LoadConfig(getenv func(string) string) (Config, error) {
	var cfg Config
	// 改名前の設定名が残っていると、CORSの許可が黙って外れるため、起動を止めて知らせる
	if getenv("CMS_CORS_ORIGINS") != "" {
		return Config{}, fmt.Errorf("CMS_CORS_ORIGINS was renamed to NEWS_CORS_ORIGINS")
	}
	for _, raw := range strings.Split(getenv("NEWS_CORS_ORIGINS"), ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		o, _, err := guard.NormalizeOrigin(raw)
		if err != nil {
			return Config{}, fmt.Errorf("NEWS_CORS_ORIGINS: %w", err)
		}
		cfg.CORSOrigins = append(cfg.CORSOrigins, o)
	}
	return cfg, nil
}
