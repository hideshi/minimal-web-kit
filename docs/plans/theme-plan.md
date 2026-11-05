# 配色と書体の差し替え（テーマ） 実装計画

状態: 計画（2026-11-05）。利用者が2節の目的と、承認境界4（CSSのファイル `theme.css` の追加）を承認した。実装の前に、外部のモデルのレビューを受ける。対象の時点は `main` の `082c97b`。関連する正本: [UIの指針](../ui-guidelines.md)、[部品](../components.md)、`internal/web/static/css/style.css`。

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
