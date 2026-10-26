// Package page は、一覧のページ送りを行う部品です。
// 次のページの有無は、1件多く取得して判断します（全件数を数える重い問い合わせをしない）。
package page

import (
	"net/http"

	"minimal-web-kit/internal/web/form"
)

// Page はページ送りの状態です。共通の画面部品 "pagination" にそのまま渡せます。
type Page struct {
	Number int // 現在のページ（1から）
	Size   int // 1ページの件数
	Prev   int // 前のページ。無ければ0
	Next   int // 次のページ。無ければ0

	// 以下は "pagination" の表示用です。
	Base     string // リンク先のパス（例: "/news"）。?page= を付けて使う
	Label    string // ページ送りのナビの名前（aria-label）
	PrevText string // 前のページへのリンクの文言
	NextText string // 次のページへのリンクの文言
}

// Parse は ?page= を解析します。未指定なら1ページ目です。数値でない・maxPage を超える場合は ok=false を返すので、400にします。
func Parse(r *http.Request, size, maxPage int) (Page, bool) {
	n, ok := form.Int(r.URL.Query().Get("page"), 1, 1, maxPage)
	if !ok {
		return Page{}, false
	}
	p := Page{Number: n, Size: size}
	if n > 1 {
		p.Prev = n - 1
	}
	return p, true
}

// Limit はDBから取得する件数です（次のページの有無を知るため、1件多い）。
func (p Page) Limit() int { return p.Size + 1 }

// Offset はDBの読み飛ばし件数です。
func (p Page) Offset() int { return (p.Number - 1) * p.Size }

// Trim は、Limit 件で取得した結果から次のページの有無を決め、表示する分だけを返します。
func Trim[T any](p *Page, items []T) []T {
	if len(items) > p.Size {
		p.Next = p.Number + 1
		return items[:p.Size]
	}
	p.Next = 0
	return items
}
