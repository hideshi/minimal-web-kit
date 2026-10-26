// Package notice は、操作の完了メッセージを、リダイレクト先に受け渡す部品です（Post/Redirect/Get）。
// クエリには固定の語（例: ?notice=saved）だけを載せ、表示する文言はサーバー側の対応表から引きます。
// 利用者が書き換えたクエリの値を、そのまま画面に表示しないためです。Cookieやセッションは使いません。
package notice

import (
	"net/http"
	"net/url"
)

// Messages は、固定の語と表示する文言の対応表です。
type Messages map[string]string

// From は、要求の ?notice= に対応する文言を返します。表に無い語なら空文字です。
func (m Messages) From(r *http.Request) string {
	return m[r.URL.Query().Get("notice")]
}

// URL は、path に ?notice=key を付けたURLを返します。
func URL(path, key string) string {
	return path + "?notice=" + url.QueryEscape(key)
}

// Redirect は、完了メッセージを付けて path へ303で移動します。
func Redirect(w http.ResponseWriter, r *http.Request, path, key string) {
	http.Redirect(w, r, URL(path, key), http.StatusSeeOther)
}
