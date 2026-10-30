package web

import "strings"

// NavCurrent は、ナビのリンク href が今の画面に当たるかを、aria-current の値で返します。
// current は描画のデータの CanonicalPath で、404のように値の無い画面では nil や空になり、印を出しません。
//
//	<a href="/news"{{with navCurrent $.CanonicalPath "/news"}} aria-current="{{.}}"{{end}}>
//
// 完全に一致すれば "page"、その配下の画面（/news と /news/3）なら "true"、どちらでもなければ空です。
// "/" は完全に一致するときだけ当たります（配下を認めると、全画面でホームに印が付くため）。
// 対象は単一の "/" で始まるサイト内のパスだけで、外部のURL・相対のURL・"?" や "#" を含むリンクには付けません。
// 末尾の "/" は同じものとして扱いません（/about/ は別の経路で、固定ページでは404のため）。
func NavCurrent(current any, href string) string {
	path, _ := current.(string)
	if path == "" || !strings.HasPrefix(href, "/") || strings.HasPrefix(href, "//") || strings.ContainsAny(href, "?#") {
		return ""
	}
	if path == href {
		return "page"
	}
	if href != "/" && strings.HasPrefix(path, href+"/") {
		return "true"
	}
	return ""
}
