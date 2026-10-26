# 部品カタログ (components.md)

テンプレートを使わずに機能をイチから作るときに、再利用する部品の一覧です。新しく書く前に、ここにある部品で作れないかを確かめてください。動きのあるUIの作り方は [ui-guidelines.md](ui-guidelines.md) を参照してください。

部品を変えると、使っている全部の画面に影響します。変更したら全テストを実行し、使っている画面を確認してください。

## 1. Goの部品

| パッケージ | 部品 | 用途 |
|---|---|---|
| `internal/web/form` | `form.Text(s, max, required)` | 複数行の文章の検査（UTF-8、改行・タブ以外の制御文字、文字数、必須）。改行をLFに揃える |
| | `form.Line(s, max, required)` | 1行の値（タイトル、名前等）の検査。前後の空白を除き、改行を拒否する |
| | `form.Int(raw, def, min, max)` | クエリ等の整数の解析。未指定なら `def`、不正・範囲外なら `ok=false`（400にする） |
| | `form.Field` | 画面部品 `field` に渡す入力欄の値 |
| `internal/web/guard` | `guard.SameOrigin(r, origin)` | 独立経路（`IsolatedRoutesFeature`）で、別サイトからのPOSTを拒否する |
| | `guard.NewCSRF()`・`Token`・`Valid` | セッションに結びつけたCSRFトークンの発行・検証 |
| `internal/web/page` | `page.Parse(r, size, maxPage)`・`Limit`・`Offset`・`page.Trim` | 一覧のページ送り。1件多く取得して次のページの有無を決める |
| `internal/web/notice` | `notice.Messages`・`From`・`Redirect`・`URL` | 操作の完了メッセージの受け渡し（`?notice=` に固定の語だけを載せる） |
| `internal/web` | `web.ParseWithComponents(fsys, files...)` | 共通の画面部品と、機能のテンプレートを一緒に解析する |

使い方の例（一覧のページ送りと完了メッセージ）:

```go
var messages = notice.Messages{"saved": "保存しました。"}

func (f *Feature) handleList(w http.ResponseWriter, r *http.Request) {
	pg, ok := page.Parse(r, 20, 1000)
	if !ok {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	items, err := f.store.List(r.Context(), pg.Limit(), pg.Offset())
	// …エラー処理…
	items = page.Trim(&pg, items)
	pg.Base, pg.Label, pg.PrevText, pg.NextText = "/items", "一覧のページ送り", "前のページ", "次のページ"
	f.render(w, "list.html", map[string]any{"Items": items, "Page": pg, "Notice": messages.From(r)})
}

func (f *Feature) handleSave(w http.ResponseWriter, r *http.Request) {
	name, err := form.Line(r.PostForm.Get("name"), 50, true)
	// …エラーなら入力を残して400で画面を返す。保存したら:
	notice.Redirect(w, r, "/items", "saved")
}
```

## 2. 画面の部品（`internal/web/views/components.html`）

`web.ParseWithComponents` で解析したテンプレートから、`{{template "名前" 値}}` で使います。

| 部品 | 渡す値 | 出力と配慮 |
|---|---|---|
| `notice` | 文字列 | 完了メッセージ。`role="status"` で読み上げソフトに穏やかに伝える。空なら何も出さない |
| `error` | 文字列 | 画面全体のエラー。`role="alert"` ですぐに伝える。空なら何も出さない |
| `pagination` | `page.Page` | 前後のページへのリンクを `<nav aria-label>` で出す。`rel="prev"`・`rel="next"` 付き。1ページだけなら何も出さない |
| `field` | `form.Field` | ラベル・説明・エラーを入力欄に関連付ける（`for`、`aria-describedby`、`aria-invalid`）。必須なら「（必須）」を表示。`Rows` が1以上なら複数行 |

```html
{{template "notice" .Notice}}
{{template "error" .Error}}
<form method="post" action="/items">
  <input type="hidden" name="csrf_token" value="{{.CSRFToken}}">
  {{template "field" .NameField}}
  <button type="submit" class="btn btn-primary">保存する</button>
</form>
{{template "pagination" .Page}}
```

## 3. CSSのクラス（`internal/web/static/css/style.css`）

| 用途 | クラス | 例・注意 |
|---|---|---|
| ボタン | `btn` ＋ `btn-primary`・`btn-secondary`・`btn-danger` | `<button>` にも `<a>` にも使える（高さは揃う）。削除等の取り消せない操作は `btn-danger` |
| お知らせ | `alert` ＋ `alert-info`・`alert-warning` | 直接書くより、部品の `notice`・`error` を使う |
| カード | `card`、`card-title`、`card-content`、`card-badge` | 囲みの枠。`<form class="card">` のようにフォームにも使える |
| 並べる | `grid-2`、`grid-3` | 狭い画面では1列になる |
| ボタンの並び | `action-bar`（中央寄せは `action-bar-center`） | 送信と取り消しのボタン等。フォームを並べるときは `inline-form` |
| 表 | `table-container` で `<table>` を囲む | 狭い画面では表だけが横に動く。`<th scope="col">` と `<caption>` を付ける |
| 入力 | `form-group`、`field-hint`、`field-error` | 直接書くより、部品の `field` を使う |
| ページ送り | `pagination` | 部品の `pagination` を使う |
| 区切り | `section`、`section-header`、`section-lead` | LP等の大きな区切り |
| 読み上げ専用 | `visually-hidden` | 画面には出さず、読み上げソフトにだけ伝える文字（表の中の入力欄のラベル等） |
| 開閉 | `faq-list`、`faq-item`、`faq-answer` | `<details>` と組み合わせる |

インラインの `style` 属性と、新しいCSSファイルは使えません。必要な見た目は `style.css` にクラスを追加します（新しいファイルの追加は承認境界4）。

## 4. 部品を増やすとき

- 2つ以上の機能で同じものを書いたら、部品にすることを検討します。使う予定の無い部品は作りません。
- 部品には単体テストを付けます。画面の部品は、エスケープとアクセシビリティの属性（ラベルの関連付け、`role`、`aria-*`）を検査します。
- この文書の一覧に追加します。
