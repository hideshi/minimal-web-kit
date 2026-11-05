# 配色と書体の差し替え（テーマ） 実装計画

状態: 計画（2026-11-05）。利用者が2節の目的と、承認境界4（CSSのファイル `theme.css` の追加）を承認した。FableとSolの計画のレビューを受け、7節に反映した（**7節は2〜6節に優先する**）。対象の時点は `main` の `082c97b`。関連する正本: [UIの指針](../ui-guidelines.md)、[部品](../components.md)、`internal/web/static/css/style.css`。

## 1. 目的と範囲

キットの見た目は、デジタル庁のサイトを参考にした1通りだけで、配色と書体を変えるには `style.css` を書き換えるしかない。キットから作るサイトが `style.css` を書き換えると、キットの変更を取り込むたびにぶつかる。

お客様ごとに、ブランドの色と書体だけを差し替えられるようにする。差し替えた後も、コントラストの基準（WCAG 2.1 AA）を満たすことをテストで確かめる。

作らないもの:

- 部品の形（カードの影、ボタンの形など）やレイアウトの差し替え。必要になったら、部品ごとに変数を足して、この仕組みに入れる。
- テンプレート（HTML）の差し替え。管理画面の型、アクセシビリティ、ユースケースのテストが前提にしている。
- 暗い配色（`prefers-color-scheme: dark`）。同じ仕組みで後から足せるが、確かめる色の組が倍になるため別に決める。
- 画面からテーマを選ぶ機能。テーマはビルドの時に決まる。
- 外部のWebフォント（Google Fonts等）の読み込み。承認境界3に当たる。

## 2. 決めたこと

| 項目 | 決定 |
|---|---|
| 差し替えの範囲 | 変数（CSSのカスタムプロパティ）の値だけ。色・書体・角丸・余白・本文の最大幅 |
| 既定の値 | すべてキットの `style.css` の `:root` に置く |
| 上書きの置き場所 | サイトの `internal/web/static/css/theme.css`。**キットには置かない** |
| 読み込み | `theme.css` があれば、共通部品 `static-head` が `style.css` の後に読み込む。無ければ何も出さない |
| `theme.css` に書けるもの | `:root { --名前: 値; … }` の1つの規則とコメントだけ。名前は `style.css` の `:root` にあるものだけ |

キットに `theme.css` を置かない理由: キットがこのファイルを持つと、変数を足すたびに変えたくなり、サイトの上書きとぶつかる。キットに無ければ、取り込みでぶつかることは仕組みの上で起きない。新しい変数は `style.css` に既定の値と一緒に足すため、サイトは何もしなくても受け継げる。

ぶつからないのに効かなくなる場合（キットが変数の名前を変えた、消した）は、5節のテストで落として気づかせる。

## 3. 今の作り

- `style.css` の `:root` に、色12・書体1・行の高さ1・余白6・角丸3・表の幅2の変数がある。規則の中で `var(--…)` は196か所。
- 変数を通らない色が18か所と影が1か所ある。

| 行 | 規則 | 値 | 足す変数（案） |
|---|---|---|---|
| 199, 320, 358, 400 | `.hero-lead`・`.card-content`・`.step-content`・`.faq-answer` | `#333333` | `--color-text-sub` |
| 116, 422, 488, 766 | `.site-subtitle`・`.cta-banner p`・`.field-hint`・`.text-meta` | `#4a5568` | `--color-text-muted` |
| 254, 632 | `.section-lead`・`footer` | `#555555` | 6節の論点1 |
| 304, 542, 547 | `.card-badge`・`.btn-primary`・`.btn-primary:hover` | `#ffffff` | `--color-on-primary` |
| 563, 568 | `.btn-danger`・`.btn-danger:hover` | `#ffffff` | `--color-on-danger` |
| 557 | `.btn-secondary:hover` の背景 | `#e9ecef` | `--color-secondary-hover-surface` |
| 585, 586 | `.alert-warning` の枠と背景 | `#e08e00`・`#fff9e6` | `--color-warning-border`・`--color-warning-surface` |
| 1024 | `.public-menu-links` の影 | `rgb(0 0 0 / 12%)` | `--shadow-menu` |
| 49 | `body` の最大幅 | `900px` | `--layout-max-width` |

- CSSの読み込みは共通部品 `static-head`（`internal/web/views/components.html:13`）の1か所で、利用側は定義し直せない（`internal/web/asset.go`）。URLには中身の版（`?v=`）が付く。
- `static/*` は `internal/web/handler.go:16` で丸ごと埋め込まれるため、サイトがファイルを置けば実行ファイルに入る。
- CSPは `style-src 'self'` で、同じ配信元のCSSのファイルは変えずに読める。
- CSSのファイルを1本に限るテスト（`test/template_test.go` の `TestCSS_SingleFileAndNoImportant`）と、`rel="stylesheet"` が1つだけのテスト（`test/layout_test.go:251`）がある。`export-static` は `style.css` を変えずに書き出すことを確かめている（`test/export_static_test.go:55`）。

## 4. 手順

各手順は単独でコミットでき、手順1の後でもキットは今と同じ見た目のまま動く。

1. **直書きの色を変数にする**: 3節の表の19か所を変数に置き換え、既定の値は今の値と同じにする。見た目は変えない。`make test-ui` の画面写真で、前後の差が無いことを確かめる。
2. **読み込み**: `static-head` に、`theme.css` があるときだけの `<link>` を足す。有無は起動の時に埋め込みのファイルから決める（`assetVersions` に入っているか）。`export-static` も、あれば書き出す。
3. **テスト**（5節）: `theme.css` の形の検査、名前の照合、コントラストの検査。キットには `theme.css` が無いため、見本（手順4）を同じ検査に通す。
4. **見本のテーマ**: 違う配色の見本を1つ `test/testdata/themes/` に置く（配信はしない）。コントラストの検査の入力にし、手元で一度 `theme.css` に複製して画面写真を撮り、崩れないことを確かめる。
5. **文書**: `AGENTS.md`・`docs/ui-guidelines.md`・`docs/components.md`・`README.md`・`skills/minimal-web-kit-implementation/SKILL.md` の「CSSは1本」を、「`style.css` と、変数だけを上書きする任意の `theme.css`」に直す。テーマの作り方（変数の一覧、確かめる色の組、書体の指定の注意）を `docs/ui-guidelines.md` に書く。

## 5. テスト

| テスト | 確かめること |
|---|---|
| CSSのファイル | `css/` にあるのは `style.css` と、任意の `theme.css` だけ。どちらも `!important` を使わない |
| `theme.css` の形 | コメントを除くと `:root { … }` の1つの規則だけ。宣言はすべて `--` で始まる。値に `url(`・`@import`・`var(`・`;` の抜けが無い |
| 名前の照合 | `theme.css` の変数は、すべて `style.css` の `:root` にある |
| 読み込み | `theme.css` があるとき、`rel="stylesheet"` は `style.css`・`theme.css` の順に2つで、どちらも版付き。無いときは1つ |
| コントラスト | `style.css` の既定の値に `theme.css` を重ねた値で、下の色の組を計算する。色の変数は `#rgb`・`#rrggbb` だけを許す |

確かめる色の組（案）:

| 前景 | 背景 | 基準 | 理由 |
|---|---|---|---|
| `--color-text`・`--color-text-sub`・`--color-text-muted` | `--color-bg`・`--color-surface` | 4.5:1 | 本文と補足の文字 |
| `--color-primary`・`--color-primary-hover` | `--color-bg`・`--color-surface`・`--color-hover-surface` | 4.5:1 | リンクとナビ |
| `--color-on-primary` | `--color-primary`・`--color-primary-hover` | 4.5:1 | 主ボタンとバッジの文字 |
| `--color-on-danger` | `--color-danger`・`--color-danger-hover` | 4.5:1 | 削除ボタンの文字 |
| `--color-danger` | `--color-bg` | 4.5:1 | エラーの文字 |
| `--color-text` | `--color-secondary-hover-surface`・`--color-warning-surface` | 4.5:1 | 副ボタンと注意書きの文字 |
| `--color-border-input` | `--color-bg` | 3:1 | 入力欄の枠（WCAG 1.4.11） |
| `--color-focus-inner` | `--color-bg` | 3:1 | フォーカスの枠 |
| `--color-focus-outer` | `--color-focus-inner` | 3:1 | 二重の枠が見分けられること |

書体（`--font-base`）は値の形だけを確かめる（引用符と名前の並び、`url(` が無い）。読みやすさは機械では確かめられないため、文書に注意を書く。

## 6. 論点（レビューで見てほしいこと）

1. `#555555`（`.section-lead`・`footer`）を `--color-text-muted`（`#4a5568`）にまとめるか、別の変数にするか。まとめると変数は減るが、手順1で見た目がわずかに変わる。
2. 書体: システムのフォントの指定だけで足りるか。Webフォントのファイルを同梱したい場合、`@font-face` は `theme.css` に書けない（変数だけの規則）。同梱は、ライセンスと実行ファイルの大きさを含めて別に決める案でよいか。
3. 色の組の過不足。とくに、ナビの今いるページの表示、表の縞、選択中の状態、エラーの枠のように、ほかの色の組み合わせで描く場所が漏れていないか。
4. `theme.css` を変数だけに限る検査を、CSSの自前の小さな読み取りで書く。完全なCSSの読み取りではないため、通り抜ける書き方が無いか（コメントの中の `}`、文字列の中の `;` など）。
5. 有無を起動の時に決める作り（`static-head` の分岐）と、既存の予約部品の照合（`staticHeadSource`）との関係。

## 7. レビューの反映（2026-11-05）

Fable（claude-fable-5-1）とSol（gpt-6.1-sol）に計画を見てもらった。2節の「キットに `theme.css` を置かない」は2人とも妥当とした。主な指摘はコードで確かめ、次のとおり直す。

### 7.1 差し替えの範囲を色と書体に絞る（Sol 6）

目的はブランドの色と書体なので、`theme.css` で上書きできる変数を **`--color-*` と `--font-base` だけ**にする。角丸・余白・行の高さ・表の幅・影・最大幅は上書きできない。値の検査と画面の確かめが減る。

- `--shadow-menu` は変数にする（`:root` の外に色を残さないため）が、上書きは許さない。
- `body` の `max-width: 900px` は変数にしない（3節の表から外す）。手順1で変える箇所は19か所になる。

### 7.2 読み込みの作り（Fable 高・Sol 1）

`asset` は実行しない分岐の中でもファイルの実在を確かめる（`internal/web/asset.go:147-160`）。キットには `theme.css` が無いため、`{{if …}}{{asset "css/theme.css"}}{{end}}` は起動でエラーになる。

- 引数の無い関数 `themeStylesheet` を共通の関数に足す。起動の時に `assetVersions` から決め、あれば版付きのURL、無ければ空の文字を返す。
- `static-head` には `{{with themeStylesheet}}<link rel="stylesheet" href="{{.}}">{{end}}` を `style.css` の後に書く。`asset` の実在の検査は変えない。
- `staticHeadSource` は木の文字列の照合なので、ファイルの有無で変わらない。`asset.go:42` の解析にも同じ関数を登録する。
- `docs/components.md` の「`/static/...` を直接書いたレイアウトも動く」に、その場合はテーマが入らないことを書き足す。

### 7.3 黙って効かなくなる場合を狭める（Fable 中・Sol 2）

2節の「ぶつからない」は `theme.css` のファイルについての保証で、変数の意味が保たれることまでは保証しない。次のテストで、気づける範囲を広げる。

| テスト | 防ぐこと |
|---|---|
| `theme.css` が `--color-*` を1つでも上書きするなら、`style.css` の `:root` の `--color-*` を全部上書きする（全部か無しか） | キットが色の変数を足した後、新しい変数だけキットの既定の色のまま混ざる。取り込んだ後のテストで落ちる |
| `style.css` の `:root` の外に、`#…`・`rgb(`・`hsl(` などの色の直書きが無い（`transparent`・`currentColor` は許す） | 規則が直書きに戻って、テーマが効かない箇所が増える |
| `style.css` の `var(--x)` の `x` は、すべて `:root` にある | 名前の誤りで既定の値にも戻らない |

### 7.4 主色の上の文字（Fable 中・Sol 3）

`.card-link:hover::after`（`style.css:745`）と `.combobox-option.is-active`（`style.css:878`）は、主色の上の文字に `--color-bg` を使っている。手順1で `--color-on-primary` にそろえる。既定の値は同じ白で、見た目は変わらない。

### 7.5 `theme.css` の読み取りは字句を狭く決める（Fable 高3件・Sol 5）

入れ子（`:root { @media … { … } }`、`& a { … }`）、文字列の中の `/*`、`\` のエスケープ（`u\72 l(` は `url(`）は、禁止語の検索では通り抜ける。完全なCSSの読み取りは作らず、許す形を狭く決めて、それ以外は全部落とす。

- ファイル全体: BOMは不可、改行は `\n`・`\r\n` を許す。コメント（`/* … */`）と空白を除くと、ちょうど `:root{` ＋ 宣言の並び ＋ `}` になる。`:root` は完全一致（`:ROOT`・`:root, body` は不可）。
- 文字: `\`・`@`・`&`・`!`・`<` と、`:root` の外側以外の `{`・`}` は1文字でもあれば落とす。
- 宣言: `--名前: 値;` の形で、名前は7.1の許可だけ。同じ名前の2回目は不可。最後の宣言の `;` は必須。
- 色の値: `#rgb` か `#rrggbb` に完全一致。
- 書体の値: 引用符で囲んだ名前と総称（`sans-serif`・`serif`・`system-ui`・`monospace`）の、カンマ区切りの並び。名前の中は文字・数字・空白・`-` だけ（`/`・`*`・`;`・引用符を含めない）。これで、コメントを除く処理が文字列の中を壊さない。
- 不正な例（上の通り抜けの例を含む）で落ちることも、テストの表で固定する。

### 7.6 確かめる色の組を直す（Fable・Sol）

5節の表を次に置き換える。基準は文字が4.5:1、文字でない部品の見分け（WCAG 1.4.11）が3:1。この組を満たすことは、WCAG 2.1 AA 全体を満たす保証ではない。

| 前景 | 背景 | 基準 | 実際に描く場所 |
|---|---|---|---|
| `--color-text`・`--color-text-sub`・`--color-text-muted` | `--color-bg`・`--color-surface` | 4.5:1 | 本文、補足の文字 |
| `--color-primary`・`--color-primary-hover` | `--color-bg`・`--color-surface`・`--color-hover-surface`・`--color-warning-surface` | 4.5:1 | リンク、ナビ、注意書きの中のリンク（`features/auth/views/admin_layout.html:4`） |
| `--color-on-primary` | `--color-primary`・`--color-primary-hover` | 4.5:1 | 主ボタン、バッジ、カードの矢印、選択中の候補 |
| `--color-on-primary` を0.95の不透明度で主色に重ねた色 | `--color-primary`・`--color-primary-hover` | 4.5:1 | 主ボタンの補足の文字（`.btn-sub-text`、`style.css:690`） |
| `--color-on-danger` | `--color-danger`・`--color-danger-hover` | 4.5:1 | 削除ボタン |
| `--color-danger-hover` | `--color-bg`・`--color-surface` | 4.5:1 | エラーの文字と、エラーの入力欄の枠（`style.css:814, 822`） |
| `--color-text` | `--color-secondary-hover-surface`・`--color-warning-surface` | 4.5:1 | 副ボタン、注意書き |
| `--color-border-input` | `--color-bg`・`--color-surface` | 3:1 | 入力欄の枠。入力欄の多くは `form.card`（surface）の上にある |
| `--color-focus-inner` | `--color-bg`・`--color-surface` | 3:1 | フォーカスの枠。`.faq-item summary` は内側の枠だけ |
| `--color-focus-outer` | `--color-focus-inner`・`--color-primary` | 3:1 | 二重の枠、選択中の候補の枠 |

入れない色と理由（文書にも書く）: `--color-danger` と背景（塗りだけで文字には使わない）、`--color-border`（区切りの線。今の既定で2.03:1。`.btn-secondary` の輪郭がこの線だけに頼るのは既存の課題として別に扱う）、`--color-warning-border`（飾りの帯。今の既定で2.48:1）。

### 7.7 テーマのある場合を自動で確かめる（Sol 7・Fable 低）

キットには `theme.css` が無いため、見本を検査の関数に渡すだけでは、読み込み・配信・静的の書き出しが確かめられない。アセットの一覧（`assetVersions`）と `themeStylesheet` を、テストから `fstest.MapFS` などで作れる形にし、有る・無いの両方で次を確かめる。

- 起動できる。`rel="stylesheet"` は、無ければ1つ、あれば `style.css`・`theme.css` の順に2つで、どちらも版付き（`test/layout_test.go:251`、`internal/web/asset_test.go:80` の「`<link>` は4つ」を直す）。
- 配信の中身と、キャッシュの扱いが `style.css` と同じ。
- 静的の書き出し（7.8）。

### 7.8 静的の書き出し（Fable 高・Sol 8）

`internal/app/export.go` は書き出すファイルの固定の一覧を持ち、HTMLの `/static/` の参照を一覧と照らす（`export.go:121`）。テーマの `<link>` が出ると「書き出していない」で止まる。

- `staticPages` に `/static/css/theme.css` を、404なら書き出さない扱い（optional）で足す。一覧に常にあるため、`writeExport` の「前回作ったファイル」にも常に入り、テーマを外した再書き出しで古い `theme.css` が消える。
- 有り→無しの再書き出しで、ファイルが消えることをテストする。
- 静的ホストやCDNが `?v=` を無視してキャッシュする場合は、更新が遅れる。これは `style.css` と同じ既存の制約で、テーマを変えた・外した後の確かめを手順書に書く。

### 7.9 6節の論点の決定

1. `#555555`: 手順1では別の変数（`--color-text-quiet`）にして見た目を変えない。その直後の別のコミットで `--color-text-muted` にまとめる（背景との比は7.46と7.5で、差は見えない）。全部か無しかの規則（7.3）で、サイトが書く色の数を減らすため。`#333333` は意図して薄くした段なので `--color-text-sub` のまま残す。
2. 書体: システムのフォントの指定だけで始める。端末にその書体が無ければ次の候補に移ること、`.hero-title` の `font-weight: 800` が書体に無いと合成の太字になることを、文書に書く。Webフォントの同梱は、ライセンスと実行ファイルの大きさを含めて別に決める（CSPは変えずに済むが、`@font-face` は変数だけの `theme.css` に書けないため、別のファイルの例外になる）。
3. 色の組: 7.6のとおり。
4. 読み取り: 7.5のとおり。
5. `static-head`: 7.2のとおり。

### 7.10 手順の置き換え

4節の手順を次に置き換える。

1. 直書きの色19か所を変数にし、7.4の2か所を `--color-on-primary` にする。見た目は変えない（画面写真の前後で確かめる）。7.3の直書きの検査と `var()` の照合を同じコミットで足す。
2. `#555555` を `--color-text-muted` にまとめる（7.9の1）。
3. 読み込み（7.2）と静的の書き出し（7.8）。テスト用のFSで有る・無いの両方を確かめる（7.7）。
4. `theme.css` の読み取りと、形・名前・全部か無しか・コントラストの検査（7.3・7.5・7.6）。見本のテーマを `test/testdata/themes/` に置き、同じ検査と、テーマのある場合の画面写真の入力にする。
5. 文書（4節の手順5の対象に、7.6の「入れない色と理由」、7.9の2の書体の注意、7.8のキャッシュの確かめを足す）。
