package auth

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"

	"minimal-web-kit/internal/web/guard"
)

// Config は管理者認証の設定です。起動時（Init）に検証し、不正なら起動を止めます。
type Config struct {
	// RPID はWebAuthnのRP ID（ドメイン名）です。
	RPID string
	// Origin は管理画面のオリジン（scheme://host[:port]）です。
	Origin string
	// SecureCookie はシステム管理者CookieにSecure属性を付けるかです。
	SecureCookie bool
	// AllowSynced は、同期・バックアップできるパスキー（Googleパスワードマネージャー等）を受け入れるかです。
	// 既定は拒否（AUTH-INV-9）。許可すると、署名カウンタによる複製の検出が効かず、同期の提供者のアカウントの守りが前提になります。
	AllowSynced bool
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

	switch getenv("AUTH_ALLOW_SYNCED_PASSKEYS") {
	case "true":
		cfg.AllowSynced = true
	case "false", "":
	default:
		// 綴りの間違いで、気づかないまま既定（拒否）に戻らないよう止める。値は文言に含めない。
		return Config{}, errors.New("AUTH_ALLOW_SYNCED_PASSKEYS must be true or false")
	}

	if strings.HasPrefix(cfg.Origin, "https://") && !cfg.SecureCookie {
		return Config{}, errors.New("SECURE_COOKIE must be true when AUTH_ORIGIN uses https")
	}

	return cfg, nil
}

// signalAllowed は、端末へパスキーの状態を知らせてよい設定かを返します（AUTH-INV-29）。
// RP ID がオリジンのホストと完全に一致し、localhost（*.localhost を含む）でないときだけ許します。
// 上位ドメインや localhost は、別のDBを持つサイトや環境と共有されうるため、
// 「無い」と知らせると、そちらで有効なパスキーを端末から外させます。
// ポートだけ違う環境（本番と検証）は見分けられないので、文書で案内します。
func (c Config) signalAllowed() bool {
	if c.RPID == "localhost" || strings.HasSuffix(c.RPID, ".localhost") {
		return false
	}
	u, err := url.Parse(c.Origin)
	return err == nil && u.Hostname() == c.RPID
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
