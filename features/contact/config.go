package contact

import (
	"fmt"
	"strconv"
	"time"

	"minimal-web-kit/internal/web/guard"
)

// defaultOrigin は、AUTH_ORIGIN が未設定のとき（手元の開発）に比べるオリジンです。管理者認証の既定と同じです。
const defaultOrigin = "http://localhost:8080"

// Config はお問い合わせ機能の設定です。起動時（Init）に検証し、不正なら起動を止めます。
type Config struct {
	// Origin は、送信を受け付けるサイトのオリジンです。管理者認証と同じ AUTH_ORIGIN を使います。
	Origin    string
	Retention time.Duration
	Limits    Limits
}

// Limits は全体の受付上限です。保存した件数で数えます（CONTACT-INV-5）。
type Limits struct {
	Hourly, Daily, Unread int
}

// LoadConfig は環境変数から設定を読み込み、検証します。エラーの文言に設定値を含めません。
func LoadConfig(getenv func(string) string) (Config, error) {
	raw := getenv("AUTH_ORIGIN")
	if raw == "" {
		raw = defaultOrigin
	}
	origin, _, err := guard.NormalizeOrigin(raw)
	if err != nil {
		return Config{}, fmt.Errorf("AUTH_ORIGIN: %w", err)
	}
	cfg := Config{Origin: origin}
	days, err := intSetting(getenv, "CONTACT_RETENTION_DAYS", 90, 1, 365)
	if err != nil {
		return Config{}, err
	}
	cfg.Retention = time.Duration(days) * 24 * time.Hour
	if cfg.Limits.Hourly, err = intSetting(getenv, "CONTACT_HOURLY_LIMIT", 20, 1, 10000); err != nil {
		return Config{}, err
	}
	if cfg.Limits.Daily, err = intSetting(getenv, "CONTACT_DAILY_LIMIT", 100, 1, 100000); err != nil {
		return Config{}, err
	}
	if cfg.Limits.Unread, err = intSetting(getenv, "CONTACT_UNREAD_LIMIT", 500, 1, 100000); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func intSetting(getenv func(string) string, name string, def, min, max int) (int, error) {
	raw := getenv(name)
	if raw == "" {
		return def, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < min || n > max {
		return 0, fmt.Errorf("%s must be a number from %d to %d", name, min, max)
	}
	return n, nil
}
