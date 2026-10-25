package cms

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// Config はCMSの設定です。起動時（Init）に検証し、不正なら起動を止めます。
type Config struct {
	// RPID はWebAuthnのRP ID（ドメイン名）です。
	RPID string
	// Origin は管理画面のオリジン（scheme://host[:port]）です。
	Origin string
	// CORSOrigins は読み取りAPIを読める別オリジンの一覧です。空なら許可しません。
	CORSOrigins []string
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

	rawOrigin := getenv("CMS_ORIGIN")
	if rawOrigin == "" {
		rawOrigin = defaultOrigin
	}
	origin, host, err := normalizeOrigin(rawOrigin)
	if err != nil {
		return Config{}, fmt.Errorf("CMS_ORIGIN: %w", err)
	}
	cfg.Origin = origin

	rpID := strings.ToLower(getenv("CMS_RP_ID"))
	if rpID == "" {
		rpID = defaultRPID
	}
	if err := validateRPID(rpID, host); err != nil {
		return Config{}, fmt.Errorf("CMS_RP_ID: %w", err)
	}
	cfg.RPID = rpID

	if strings.HasPrefix(cfg.Origin, "https://") && !cfg.SecureCookie {
		return Config{}, errors.New("SECURE_COOKIE must be true when CMS_ORIGIN uses https")
	}

	for _, raw := range strings.Split(getenv("CMS_CORS_ORIGINS"), ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		o, _, err := normalizeOrigin(raw)
		if err != nil {
			return Config{}, fmt.Errorf("CMS_CORS_ORIGINS: %w", err)
		}
		cfg.CORSOrigins = append(cfg.CORSOrigins, o)
	}
	return cfg, nil
}

// normalizeOrigin は scheme://host[:port] の形式だけを受け付け、正規化して返します。
// http は localhost のみ許可します。既定のポートは省略します（ブラウザが送るOriginと揃えるため）。
func normalizeOrigin(raw string) (origin, host string, err error) {
	if raw == "*" || raw == "null" {
		return "", "", errors.New("wildcard and opaque origins are not allowed")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", "", errors.New("invalid origin")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", "", errors.New("origin must use http or https")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || u.Opaque != "" {
		return "", "", errors.New("origin must not contain user info, path, query or fragment")
	}
	host = strings.ToLower(u.Hostname())
	if host == "" {
		return "", "", errors.New("origin must have a host")
	}
	if u.Scheme == "http" && host != "localhost" {
		return "", "", errors.New("http is allowed only for localhost")
	}
	port := u.Port()
	if (u.Scheme == "https" && port == "443") || (u.Scheme == "http" && port == "80") {
		port = ""
	}
	hostport := host
	if strings.Contains(host, ":") {
		hostport = "[" + host + "]"
	}
	if port != "" {
		hostport += ":" + port
	}
	return u.Scheme + "://" + hostport, host, nil
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
		return errors.New("RP ID must equal the CMS_ORIGIN host or one of its parent domains")
	}
	return nil
}
