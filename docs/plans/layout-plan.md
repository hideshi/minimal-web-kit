# レイアウトとボタンの部品の共通化の計画

状態: 完了（2026-11-01）。計画のレビューはSol。対象の時点は `83483c3`。今の仕様は `docs/components.md` にある。

## 目的

レイアウトが8本あり、ヘッダー・フッター・スキップリンクを直すたびに8本を触る。画面どうしのズレが起きたのと同じ構造を無くす。あわせて、ボタン1つのPOSTフォームの直書き18か所を部品にする。

見た目と応答は変えない。CSS・JSの追加、保存データ、外部通信、CSPの変更は無く、承認境界には当たらない。Coreのテンプレートの解析に関わるので、計画の段階でレビューを受ける。

## 今の状態

| ファイル | 違い |
|---|---|
| `features/news`・`pages` の `views/layout.html` | 完全に同じ |
| `features/catalog/views/layout.html` | 上に `page_head` のブロックを足しただけ |
| `features/contact/views/layout.html` | 上から `analytics` の部品を除いただけ |
| `internal/web/views/layout.html`（Base） | サブタイトルと、フッターの本文（`SiteMode` で分岐）が違う |
| `features/site/views/layout.html` | SEOの `<meta>`、`page_head`、サブタイトル、フッターの本文が違う |
| `features/auth/views/admin_layout.html` | `federatedauth` のものに、複製の疑いの警告を足しただけ |
| `features/federatedauth/views/admin_layout.html` | 上の共通部分だけ |

`news`・`pages`・`catalog`・`contact` のレイアウトは、Siteが無いときの予備で、Siteがあれば `LayoutConsumer` で置き換わる。紹介サイトはこの7ファイルを書き換えていない（`upstream/main` との差分なしを確認済み）。

## 設計

### 1. 枠の部品

`internal/web/views/components.html` に、2つの枠を足す。

- `public-shell`: `<!DOCTYPE>` から `</html>` まで。可変の箇所はブロックにする。
  - `page_head`（既存の名前。`</head>` の直前）
  - `site-meta`（`<title>` の後。既定は空。SiteのSEOの `<meta>` を置く）
  - `page-title`（`<title>` の中身。既定は `{{.Title}} - minimal-web-kit`）
  - `site-brand`（ヘッダーの左。既定はサイト名だけ。BaseとSiteはサブタイトルまで書く）
  - `site-footer`（既定は `&copy; 2026 minimal-web-kit` と `/privacy` のリンク）
- `admin-shell`: 同じく全体。ブロックは `script`（既存）と `admin-alerts`（`<main>` の先頭。既定は空）。

`analytics` は `public-shell` が常に呼ぶ。部品は値が無ければ何も出さず、`contact` の画面は `AddAnalytics` を呼ばないので、出力は変わらない。Site有効時の `contact` は今も `analytics` を呼ぶレイアウトで描かれていて、同じ状態で動いている。

### 2. 公開のレイアウト

- `web.PublicLayout()` が `web.Layout{FS: contentFS, File: "views/public_layout.html"}` を返す。中身は `{{define "layout"}}{{template "public-shell" .}}{{end}}` だけ。
- `news`・`pages`・`catalog`・`contact` の `views/layout.html` を消し、予備のレイアウトに `web.PublicLayout()` を使う。`pages` は `ParseWithComponents` から `ParsePage` へ替える。
- Base の `layout.html` と Site の `layout.html` は、`layout` が `public-shell` を呼び、差分のブロックだけを定義する形にする。

利用者がヘッダーの名前・サブタイトル・フッター・SEOの情報を書き換える場所は `features/site/views/layout.html` のままにする（READMEの案内を保つ）。そのために `page-title` と `site-brand` を差し替え口にする。ナビは今も `public-menu` の部品で、変わらない。

### 3. 管理画面のレイアウト

`features/auth/views/admin_layout.html` と `federatedauth` のものは、`layout` が `admin-shell` を呼ぶだけにする。`auth` は `admin-alerts` に複製の疑いの警告を定義する。1本に消さないのは、`auth` の固有の警告を残すため、各機能の `AdminLayout()` の形を変えないためである。

### 4. ボタン1つのPOSTフォーム

部品 `post-button` と関数 `postButton` を足す。

```html
{{template "post-button" (postButton "/admin/news/1/publish" .CSRFToken "公開する" primaryButton)}}
{{template "post-button" (postButton $url $.CSRFToken "取り消す" (rowName .Name))}}
{{template "post-button" (postButton $url .CSRFToken "公開する" (hiddenField "version" .ID))}}
```

`form.PostButton` の値を作る。任意の指定は、`primaryButton`（既定は `btn-secondary`）、`rowName`（表の行のボタンの `visually-hidden` の補足）、`hiddenField`（追加の隠し欄）の3つ。対象は `inline-form` のフォームのうち、入力欄を持たないものだけで、`confirm-actions` と入力欄のあるフォームは変えない。ラベルが条件で変わる箇所は、呼ぶ前に `$label` に入れる。

### 5. 戻るリンク

部品にしない。`docs/components.md` に「`〜へ戻る` と書く」規則を足し、テンプレートの検査で文言だけを確かめる。

## テスト

| テスト | 内容 |
|---|---|
| 新規 `TestTemplate_LayoutsUseShell` | `views/` の `layout.html`・`*_layout.html`・`public_layout.html` が `public-shell` か `admin-shell` を呼ぶ。自分で `<html>` を書かない。落ちるべき例を持つ |
| 新規 `TestTemplate_SingleButtonFormsUseComponent` | 機能のテンプレートに `class="inline-form"` の直書きが無い。ボタンの直書きを見つける検査が落ちる例を持つ |
| 変更 `TestTemplate_StaticAssetsUseAsset` | レイアウトのソースに `static-head` を直接求める検査を、枠が `static-head` を呼ぶ検査に置き換える。`/static/` の直書きの禁止は保つ。描画の結果は `TestLayout_StaticHeadOnEveryLayout` が見る |
| 変更 `TestTemplate_TableFormsAreInline` | ソースの検査は部品の導入で空振りするので、`checkAdminPage` に、描画した各 `td` のフォームが `inline-form` か `label-form` であることの検査を足す。クラスの無いHTMLで落ちる例を持つ |
| 新規 `post-button` の単体テスト | エスケープ、CSRF、任意の指定、既定のスタイル、`primaryButton` と `hiddenField` の併用、数値の隠し欄。Coreの `admin-shell` のログアウトも部品を使うこと |
| 既存 | `TestLayout_StaticHeadOnEveryLayout`、`TestAdminUI_*`、`ANALYTICS-INV-3`、`test/browser/layout_consistency_test.go` |

### 変わっていないことの確かめ方

実装の前後で、同じ見本のデータの応答を比べる。一時のテストで書き出し、コミットしない。

- 比べるもの: 状態コード、`Content-Type`・CSP・`Cache-Control`・`Location`・`Vary`、発行するCookieの名前、本文。
- 構成: Siteあり／なし × アクセス解析あり／なしの全機能、`federatedauth` のログイン・ホーム・保存する情報・利用不能、`export-static` のSiteのみ／Site＋解析（`404.html`・`_headers` を含む）。
- 画面: `adminSamples` の全画面を、本来のロール・未ログイン・サイト管理者で開く。公開ページ、404、カタログの通常・断片・履歴復元、複製の疑いの警告あり。
- 揺れる値は、項目ごとに置き換える（CSRF、トークン、乱数のID、時刻、ポート）。アカウント名と問い合わせの登録順は、見本の側で固定する。行頭の空白とタグの間の改行だけを無視する。
- 同じ版で2回書き出して差分が0件であることを確かめてから比べる。差分があれば止める。

WebAuthnの `script` は描画の比較とCSPで確かめる。ブラウザの実測は一覧どうしを比べるものなので、ヘッダー・フッターの位置は前後の撮影を見て確かめる。

## 手順

1. 枠の部品と `web.PublicLayout()` を足し、公開のレイアウト5本を置き換える。
2. 管理画面の2本を置き換える。
3. `post-button` へ置き換える。
4. 戻るリンクの規則と、検査を足す。
5. `docs/components.md`、`docs/README.md` の該当があれば、README、実装スキルの9番を更新する。

1〜4は別のコミットにする。

## 危険と対策

- **ブロックの定義の順**: `text/template` は後の定義で置き換わる。部品 → レイアウト → 本文の順に解析するので、Baseや機能の定義が既定に勝つ。空白だけの定義は、中身のある定義を置き換えないので、既定を空にしたいブロックは作らない（`site-footer` を消す用途は想定しない）。
- **`html/template` の文脈**: 枠の中の `{{block}}` が別ファイルで定義されても、エスケープは実行の直前に全体で行われる。`site-meta` は `<head>` の中、`site-footer` は `<div>` の中で、今と文脈は変わらない。
- **自作のレイアウト**: `{{define "layout"}}` を丸ごと書いた機能は、そのまま動く。枠は強制せず、検査はキットの同梱のファイルだけを見る。キットから作ったアプリが、消す4ファイルを書き換えていた場合は、取り込みのときに衝突する。自分のレイアウトを残すには `web.Layout` を渡す形に変える必要がある。
- **`static-head` の定義し直し検査**: `checkTemplates` はそのまま動く。

## 採らなかった案

- **`web.Layout` に複数のファイルを持たせ、枠を別ファイルにする**: `ParsePage` の変更が増える。部品の中に置けば、`ParseWithComponents` の変更が要らない。
- **文言を全てデータで渡す**: `site-footer` にHTMLが入るので、`template.HTML` が要る。ブロックならエスケープの範囲を外さない。
- **表の共通化、削除の確認の画面の共通化**: 列と説明文が画面ごとに違い、短くならない。
