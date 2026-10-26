package auth

import (
	"errors"
	"fmt"
	"net"
	"strings"

	"minimal-web-kit/internal/web/guard"
)

// Config は管理者認証の設定です。起動時（Init）に検証し、不正なら起動を止めます。
type Config struct {
	// RPID はWebAuthnのRP ID（ドメイン名）です。
	RPID string
	// Origin は管理画面のオリジン（scheme://host[:port]）です。
	Origin string
	// SecureCookie は管理者CookieにSecure属性を付けるかです。
	SecureCookie bool
}

const (
	defaultRPID   = "localhost"
	defaultOrigin = "http://localhost:8080"
)

// LoadConfig は環境変数から設定を読み込み、検証します。
// エラーメッセージには設定値を含めません（ログへの混入を避けるため）。
func LoadConfig(getenv func(string) string) (Config, error) {
	cfg := Config{SecureCookie: getenv("SECURE_COOKIE") == "true"}

	rawOrigin := getenv("AUTH_ORIGIN")
	if rawOrigin == "" {
		rawOrigin = defaultOrigin
	}
	origin, host, err := guard.NormalizeOrigin(rawOrigin)
	if err != nil {
		return Config{}, fmt.Errorf("AUTH_ORIGIN: %w", err)
	}
	cfg.Origin = origin

	rpID := strings.ToLower(getenv("AUTH_RP_ID"))
	if rpID == "" {
		rpID = defaultRPID
	}
	if err := validateRPID(rpID, host); err != nil {
		return Config{}, fmt.Errorf("AUTH_RP_ID: %w", err)
	}
	cfg.RPID = rpID

	if strings.HasPrefix(cfg.Origin, "https://") && !cfg.SecureCookie {
		return Config{}, errors.New("SECURE_COOKIE must be true when AUTH_ORIGIN uses https")
	}

	return cfg, nil
}

// validateRPID は、RP IDがドメイン名で、オリジンのホストと同一かその上位ドメインであることを確認します。
// 公開サフィックス（例: co.jp）の判定は行わず、ブラウザ側の検査に委ねます。
func validateRPID(rpID, originHost string) error {
	if net.ParseIP(strings.Trim(rpID, "[]")) != nil {
		return errors.New("RP ID must be a domain name, not an IP address")
	}
	if rpID != "localhost" && !strings.Contains(rpID, ".") {
		return errors.New("RP ID must be localhost or a registrable domain")
	}
	if strings.HasPrefix(rpID, ".") || strings.HasSuffix(rpID, ".") || strings.Contains(rpID, "..") {
		return errors.New("RP ID is malformed")
	}
	if originHost != rpID && !strings.HasSuffix(originHost, "."+rpID) {
		return errors.New("RP ID must equal the AUTH_ORIGIN host or one of its parent domains")
	}
	return nil
}
