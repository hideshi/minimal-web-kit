# 部品カタログ (components.md)

テンプレートを使わずに機能をイチから作るときに、再利用する部品の一覧です。新しく書く前に、ここにある部品で作れないかを確かめてください。動きのあるUIの作り方は [ui-guidelines.md](ui-guidelines.md) を参照してください。

部品を変えると、使っている全部の画面に影響します。変更したら全テストを実行し、使っている画面を確認してください。

## 1. Goの部品

| パッケージ | 部品 | 用途 |
|---|---|---|
| `internal/web/form` | `form.Text(s, max, required)` | 複数行の文章の検査（UTF-8、改行・タブ以外の制御文字、文字数、必須）。改行をLFに揃える |
| | `form.Line(s, max, required)` | 1行の値（タイトル、名前等）の検査。前後の空白を除き、改行・タブを拒否する |
| | `form.Int(raw, def, min, max)` | クエリ等の整数の解析。未指定なら `def`、不正・範囲外なら `ok=false`（400にする） |
| | `form.ID(raw)` | データの番号（1以上）の解析。半角の数字だけで、先頭のゼロ（`01`）・符号（`+1`）・範囲外は `ok=false`。1つの番号に書き方を1通りにする |
| | `form.Paragraphs(body)` | プレーンテキストの本文を段落と行に分ける（画面部品 `paragraphs` に渡す） |
| | `form.FieldErrors` と `For(invalid, names...)` | 欄の名前から、その欄に出すエラーの文言を引く表 |
| | `form.Field` | 画面部品 `field` に渡す入力欄の値 |
| `internal/web/guard` | `guard.SameOrigin(r, origin)` | 独立経路（`IsolatedRoutesFeature`）で、別サイトからのPOSTを拒否する |
| | `guard.NewCSRF()`・`Token`・`Valid` | セッションに結びつけたCSRFトークンの発行・検証 |
| `internal/web/page` | `page.Parse(r, size, maxPage)`・`Limit`・`Offset`・`page.Trim` | 一覧のページ送り。1件多く取得して次のページの有無を決める |
| `internal/web/notice` | `notice.Messages`・`From`・`Redirect`・`URL` | 操作の完了メッセージの受け渡し（`?notice=` に固定の語だけを載せる） |
| `internal/store` | `store.AffectedOne(res, err, ifNone)` | 更新・削除が1行に当たったかを確かめる。0行なら `ifNone`（例: `ErrNotFound`） |
| | `store.Busy(err)` | SQLiteの一時的な書き込みの競合（BUSY・LOCKED）かを判定する。503と `Retry-After` で返すかは画面ごとに決める |
| | `form.Combobox`・`form.Option` | 画面部品 `combobox` に渡す値（候補、上限、要素idの接頭辞） |
| `internal/web` | `web.ParseWithComponents(fsys, files...)` | 共通の画面部品と、機能のテンプレートを一緒に解析する |
| | `web.ParsePages(layout, fsys, names...)` | レイアウトと `views/` の各ファイルを解析し、ファイル名をキーにした表を返す |
| | `web.PathID(w, r, "id")` | 経路の `{id}` を `form.ID` の規則で読む。読めなければ404を返す |
| | `web.RequestIDAttr(w)` | ログに添えるリクエストID（サーバーが作った値） |
| | `web.WriteJSON(w, logger, event, status, v)` | JSONを返す。変換に失敗したら500と `{"error":"internal_error"}` |
| | `web.AllowScripts(w)` | その応答だけ、CSPの `script-src` を `'self'` にする（部品のJSを読み込む画面で、描画に成功した後に呼ぶ）。既定のCSPのときだけ変える |

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
| `field` | `form.Field` | ラベル・説明・エラーを入力欄に関連付ける（`for`、`aria-describedby`、`aria-invalid`）。`Required` なら「（必須）」、`Optional` なら「（任意）」を同じ見た目で表示する（ラベルの文字に直接書かない）。`Rows` が1以上なら複数行、`Options` があれば選択（`select`）、`Type: "checkbox"` なら単独のチェックボックス（`Checked`）。`LabelHidden` は表の中の欄のようにラベルを読み上げ専用にし、`Readonly` は登録URLのような表示用の値に使う |
| `choices` | `form.Choices` | 同じ名前のラジオ（`Multiple` ならチェックボックス）の組。`fieldset`・`legend` で囲み、説明・エラーを組に関連付ける。選択肢が1つなら `field` の `checkbox` を使う |
| `paragraphs` | `form.Paragraphs` の結果 | プレーンテキストの本文。空行で段落（`p`）、段落の中の改行は `br`。`text-body` で囲み、長い英数字も折り返す |
| `datetime`・`date` | `time.Time` | 日時（「2026年1月2日 15:04」）と日付（「2026年1月2日」）を `<time datetime>` で出す。時刻帯は渡す側で決める（例: `time.Local`）。予定の短い表示（登録URLの期限「1月2日 15:04」）だけは画面で書く |
| `richtext` | `richtext.Doc`（`internal/web/richtext` の `Parse` で読んだもの） | 書き方を絞ったMarkdownの本文（見出し2段、段落、箇条書き、番号付き、表、リンク、強調）。規則の正本は [richtext.md](richtext.md)。利用者の入力はHTMLとして出さず、リンクは小文字の `https://` とサイト内のパスだけ。保存の前に `Parse` を通して、表の上限を欄のエラーにする |
| `confirm-actions` | `form.Confirm`（テンプレートでは `confirm` 関数） | 削除の確認画面の操作の並び。`btn-danger` の「削除する」（`Submit` で変更可）と、`btn-secondary` の「削除せずに戻る」。`{{template "confirm-actions" (confirm 送信先 戻り先 .CSRFToken)}}` |
| `combobox` | `form.Combobox` | 入力で候補を絞り込み、複数を選ぶ（WAI-ARIAのcombobox＋listbox）。JSなし用のチェックボックスの一覧も描き、`/static/js/combobox.js` が動いたときだけ入れ替える。送信は `Name` の繰り返しで、チェックボックスと同じ。1画面に複数置くときは `ID` を変える |
| `static-head` | なし | CSSとアイコンの `<link>`（CSS 1本、アイコン3つ）。URLは中身の版付き（`?v=`）で、CSSを変えるとすぐ新しい版が読まれる。レイアウトの `<head>` で `{{template "static-head"}}` と書く。予約名で、利用側で同じ名前を定義すると解析でエラーになる |

**CSSとアイコンの読み込み**: 同梱の7つのレイアウト（Base・Site・お知らせ・カタログ・お問い合わせ・固定ページ・管理画面）は、`static-head` の1か所から読み込みます。共通部品は登録されるだけでレイアウトに自動では入らないため、自作のレイアウトでも `<head>` に `{{template "static-head"}}` を書きます。`/static/...` を直接書いたレイアウトも動きますが、版が付かず、CSSの更新が届くまでキャッシュの期間（10分。前段のCDNが引き上げることがある）だけ遅れます。`/static/` の他のファイルは、`{{asset "js/combobox.js"}}` で版付きのURLにします。`asset` の引数は文字列だけで、`/static/` に無いファイルや文字列以外の引数は、実行しない分岐の中でも解析（`web.ParseWithComponents`・`web.ParsePage`）でエラーになります。版はファイルごとの中身のSHA-256の先頭12桁で、CSSを変えてもアイコンのURLは変わりません。

**ナビの今いるページ**: ヘッダーのナビのリンクには、`<a href="/news"{{with navCurrent $.CanonicalPath "/news"}} aria-current="{{.}}"{{end}}>お知らせ</a>` の形で印を付けます（`{{range}}` の中では `.Href` を渡す）。`navCurrent` は、描画のデータの `CanonicalPath` とリンクを比べ、完全に一致すれば `page`、配下の画面（`/news` と `/news/3`）なら `true`、どちらでもなければ空を返します。`CanonicalPath` の無い画面（404等）では空になり、印は出ません。対象は `/` で始まるサイト内のパスだけで、`/` は完全一致のときだけ当たります。ページ内リンク（`/#faq`）・クエリ付きのリンク・外部のURLには付きません。`navCurrent` は部品が使う関数名なので、利用側で別の意味に上書きしないでください。管理画面のレイアウトは、機能が渡す `Current` と要求のパスで同じ値を出します。

`combobox` を使う画面は、レイアウトの `script` ブロック等で `<script src="{{asset "js/combobox.js"}}" defer></script>` を読み込み、その応答で `web.AllowScripts` を呼びます。テンプレートの検査（`test/template_test.go`）の許可リストにその画面を足す必要があります（承認境界4・5）。上限（`Limit`）は画面上の制限で、送信された値の検査はサーバー側で別に行います。

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
| カード | `card`、`card-title`、`card-content`、`card-badge` | 囲みの枠。`<form class="card">` のようにフォームにも使える。直下の `btn` は文字の幅になる |
| タグの並び | `tag-list` | 印の無い横並びの `<ul>`。`role="list"` を付ける |
| 並べる | `grid-2`、`grid-3` | 狭い画面では1列になる |
| ボタンの並び | `action-bar`（中央寄せは `action-bar-center`） | 送信と取り消しのボタン等。フォームを並べるときは `inline-form` |
| 表 | `table-container` で `<table>` を囲む | 狭い画面では表だけが横に動く。`<th scope="col">` と `<caption>` を付ける。列には最小の幅があり、`<td>` の中の `<time>` は折り返さない |
| 入力 | `form-group`、`field-hint`、`field-error` | 直接書くより、部品の `field` を使う |
| ページ送り | `pagination` | 部品の `pagination` を使う |
| 区切り | `section`、`section-header`、`section-lead` | LP等の大きな区切り |
| 読み上げ専用 | `visually-hidden` | 画面には出さず、読み上げソフトにだけ伝える文字（表の中の入力欄のラベル等） |
| 開閉 | `faq-list`、`faq-item`、`faq-answer` | `<details>` と組み合わせる |

インラインの `style` 属性と、新しいCSSファイルは使えません。必要な見た目は `style.css` にクラスを追加します（新しいファイルの追加は承認境界4）。

## 4. 管理画面の画面の型

管理画面（`/admin` 以下）は、次のどれかの型で作ります。型ごとの構成と、6節の検査がそろえる点です。

| 型 | 構成（上から） | 見本 |
|---|---|---|
| 一覧 | `h1`（「〜の管理」）→ `notice` → 操作の並び（`action-bar`。主操作は `btn-primary` 1つ、他はリンク）→ 表（`table-container`、`caption`、`th scope`、最後の列が「操作」）か、空のときの `<p>`（「〜はまだありません。」）→ `pagination` | `features/news/views/admin_posts.html` |
| 一覧＋追加 | 一覧の後に、`h2`（「〜の追加」）→ `error` → フォーム（`card`）。追加の失敗はこの画面のまま、入力欄にエラーを出す | `features/catalog/views/admin_classifications.html` |
| 作成・編集 | `h1`（「〜の作成」「〜の編集」）→ `notice`・`error` →（プレビューを持つ場合は、フォームの上にプレビューの節）→ フォーム（`card`、入力欄は部品、送信は `btn-primary`。プレビューのボタンは保存の後に置き `formaction` で送る）→ 公開などの別操作（`action-bar`、フォームは `inline-form`）→ 削除（`btn-danger` のリンク、編集のときだけ）→ 一覧へ戻るリンク | `features/news/views/admin_post_form.html`、プレビューは `features/pages/views/admin_page_form.html` |
| 詳細（状態と操作） | `h1`（「〜: 名前」）→ `notice`・`error` → 状態の表 → 操作ごとの `h2` とフォーム → 削除（`btn-danger` のリンク）→ 一覧へ戻るリンク | `features/auth/views/admin_account.html` |
| 削除の確認 | `h1`（「〜の削除」）→ 対象と影響の説明（何が消え、何が残り、元に戻せないか）→ `confirm-actions` | `features/news/views/admin_post_delete.html` |

- `<title>` は `h1` と同じか、`h1` がそれで始まる（例: `h1`「アカウント: 名前（自分）」、`<title>`「アカウント: 名前」）。
- 表の中に削除のボタンを置かない。操作の列は、確認画面へのリンク「編集」「削除」にし、対象の名前を `visually-hidden` で添える（`削除<span class="visually-hidden">（名前）</span>`）。
- 入力の失敗は、送る前と同じ画面・同じ `h1` で、入力した値を残して返す。原因が入力欄にあれば、その欄の `Error`（`aria-invalid`）に、ないとき（一時的な失敗・状態の衝突）は `error`（`role="alert"`）に出す。状態コードは、入力の誤り400、状態の衝突（最後の管理者を消す等）409、一時的な失敗503。
- ログイン・初回登録・登録URLの表示・パスキーの追加（JSで開くフォーム）は型の外で、6節の共通の検査だけを当てる。

## 5. 用語とメッセージ

- 操作: 「〜を作成する」（新しく作る主操作）、「保存する」「変更を保存する」、「追加する」（一覧の画面で足す）、「削除する」、「削除せずに戻る」、「〜の一覧へ戻る」、「公開する」「非公開（下書き）に戻す」。表の操作列は「編集」「削除」。
- 件数: 「○件」。0件のときは「〜はありません。」のように言い換える（「0件の項目で使用中」と書かない）。
- エラー: 何が問題か＋どうすればよいか（例:「同じ名前のタグがあります。別の名前にしてください。」）。その画面に関係しない条件を並べた汎用の文を使わない。
- 完了: `notice` に機能の `notice.Messages` の文（「変更を保存しました。」「削除しました。」等）を出す。

## 6. 検査（`go test ./...`）

- `test/admin_ui_test.go`: 登録された管理画面の経路を全部集め、見本のデータ（状態の違うものを含む）と空の状態で開いて、`h1` と `<title>`、`id` の重複と参照先、ラベル、選択肢の組、CSRFトークン、素の箇条書き、表の `caption`・`scope`、表の中の削除ボタン、削除の確認の操作の並び、一覧の空の1文を確かめる。わざと失敗する送信では、状態コード・`h1`・エラーの場所・入力の保持を確かめる。
- `test/template_test.go`: 機能のテンプレートで入力欄を直接書かないこと（例外は理由つきの許可リスト）、全部の表の `caption`・`scope`。
- **管理画面を足したら**、`adminSamples`（開くURLと画面の型）に見本を足す。成功で画面を返す送信は `adminPostSamples`、入力の失敗は `adminFailures` に足す。見本の無い経路・テンプレートがあるとテストが落ちる。経路は文字列のリテラルで登録する（ソースの経路との突き合わせに使う）。
- 見た目（余白・並び・狭い画面）は検査できないため、`make test-ui` が `test/browser/shots/` に撮る主な画面を見て確かめる。

## 7. 部品を増やすとき

- 2つ以上の機能で同じものを書いたら、部品にすることを検討します。使う予定の無い部品は作りません。
- 部品には単体テストを付けます。画面の部品は、エスケープとアクセシビリティの属性（ラベルの関連付け、`role`、`aria-*`）を検査します。
- この文書の一覧に追加します。
