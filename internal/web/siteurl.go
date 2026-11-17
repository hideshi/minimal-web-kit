package web

import (
	"net/url"
	"os"
	"strings"
)

// SiteURLFromEnv は SITE_URL（公開するサイトのURL）を検証し、末尾の / を除いて返します。
// canonical・OGP・sitemap.xml と、外へ送る記事の URL（LINEでの一斉配信）の絶対URLに使います。
// 未設定なら空文字、不正なら ok=false を返します。
func SiteURLFromEnv() (string, bool) {
	raw := os.Getenv("SITE_URL")
	if raw == "" {
		return "", true
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil ||
		u.RawQuery != "" || u.Fragment != "" || strings.TrimSuffix(u.Path, "/") != "" {
		return "", false
	}
	return u.Scheme + "://" + u.Host, true
}
