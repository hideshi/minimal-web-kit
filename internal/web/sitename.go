package web

import (
	"errors"
	"os"
	"strings"
	"unicode"
	"unicode/utf8"
)

// DefaultSiteName は、SITE_NAME を設定しないときのサイトの名前です。
const DefaultSiteName = "minimal-web-kit"

// maxSiteNameRunes は、サイトの名前の上限の文字数です。ヘッダーと題名に収まる長さにします。
const maxSiteNameRunes = 40

// SiteName は、管理画面のヘッダー・題名・フッターと、公開ページの既定の表示に使うサイトの名前（SITE_NAME）を返します。
// 未設定か不正なら DefaultSiteName を返します。不正な値は CheckSiteName が起動時に拒否します。
func SiteName() string {
	name, err := siteNameFromEnv()
	if err != nil || name == "" {
		return DefaultSiteName
	}
	return name
}

// CheckSiteName は、SITE_NAME が設定されていれば、使える値かを確かめます。エラーに値を含めません。
func CheckSiteName() error {
	_, err := siteNameFromEnv()
	return err
}

func siteNameFromEnv() (string, error) {
	raw, ok := os.LookupEnv("SITE_NAME")
	if !ok {
		return "", nil
	}
	name := strings.TrimSpace(raw)
	if name == "" {
		return "", errors.New("SITE_NAME must not be blank (unset it to use the default)")
	}
	if !utf8.ValidString(name) || utf8.RuneCountInString(name) > maxSiteNameRunes {
		return "", errors.New("SITE_NAME must be valid UTF-8 and at most 40 characters")
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return "", errors.New("SITE_NAME must not contain control characters")
		}
	}
	return name, nil
}
