# コンボボックス部品 実装計画

状態: 2026-10-27、利用者が次を承認。素のJSの `combobox.js` をCoreに置く（承認境界4）、部品を使う画面だけCSPの `script-src` を `'self'` にする共通の関数とカタログ管理画面の項目編集での利用（承認境界5）、ブラウザテスト用の別モジュール `test/browser/`（chromedp、承認境界1）。外部レビュー（Fable）の指摘を9節に反映し、実装した。結果は10節。
経緯は [catalog-ui-comparison-plan.md](catalog-ui-comparison-plan.md)。試作は `experiment/catalog-tags` ブランチの `experiments/catalog-tags/static/combobox.js`。

## 1. 目的と範囲

`docs/ui-guidelines.md` の3段目（標準の要素で作れない部品）の見本として、入力で候補を絞り込み、複数を選べるコンボボックスを、外部依存のない素のJSでCoreの部品にする。最初の利用先は、カタログ管理画面の項目編集のタグ選択。

- 複数選択だけを作る。1つだけ選ぶ形（`<select>` の置き換え）は、必要になってから別に設計する。
- 候補はサーバーが描いたものだけ。入力のたびにサーバーへ問い合わせる形（htmxでの候補取得）、新しい候補をこの部品で作る機能は作らない。新規タグは今の「新規タグ名」の欄のまま。
- 保存データ・フォームの項目・送信内容は変えない（送信は今と同じ `tag` の繰り返し）。承認境界2・3には当たらない。
- キットの線引き（`ui-guidelines.md` の「このキットで作らないUI」）に従い、部品を増やす前提の仕組み（汎用の部品登録、状態管理）は作らない。

## 2. 部品の構成

| 置き場所 | 内容 |
|---|---|
| `internal/web/static/js/combobox.js` | 振る舞いだけ。`/static/js/combobox.js` で配信する |
| `internal/web/views/components.html` の `combobox` | 画面の部品。JSなし用のチェックボックスの `<fieldset>` と、隠したコンボボックス（入力欄・候補の一覧・選択済みの一覧・通知の領域）を両方描く |
| `internal/web/form`（または `internal/web`）の `form.Combobox` | 部品に渡す値。名前（`name`）、見出し、説明、上限、候補（値・表示名・選択済みか）、要素IDの接頭辞 |

- JSはサーバーが描いた候補に、属性・`textContent`・`<template>` の複製だけで手を加える。`innerHTML`・`eval`・通信・ストレージは使わない。
- 1画面に複数置けるよう、要素のIDは接頭辞で分け、JSは `[data-combobox]` ごとに独立して動かす。`htmx:load` でも付け直す（htmxで差し替えた部分に置かれた場合）。
- 文言（「N件の候補」「〜を追加しました」等）は日本語で固定する。上限は `data-limit` で渡す。
- JSが無効・読込失敗の場合は、コンボボックスを `hidden` のまま出さず、チェックボックスの一覧で同じ値を送信する。

試作から引き継ぐ振る舞い: フォーカス・クリック・入力・↓↑で開く、↑↓で移動（`aria-activedescendant`）、Enterで選択・解除（フォームは送信しない）、Escで閉じる・入力を消す、空欄のBackspaceで最後を外す、日本語入力の変換中のキーは無視、上限で未選択の候補を `aria-disabled` にして通知、×で外したら入力欄へフォーカスを戻し候補は開かない、外側を押すかフォーカスが外れたら閉じる、選んだら検索語を空にして候補の位置を保つ。

## 3. CSP

- Coreに `web.AllowScripts(w http.ResponseWriter)` を足す。応答の `Content-Security-Policy` の `script-src 'none'` だけを `'self'` に置き換え、他の指示は変えない。`unsafe-inline`・`unsafe-eval` は足さない。
- 部品を使う全ページ応答（項目の作成・編集画面の200・400・503）でだけ呼ぶ。カタログ管理の他の画面、公開側の詳細、エラー応答は `'none'` のまま。
- カタログ公開一覧の今の置き換え処理（`features/catalog/catalog.go`）も、この関数に揃える。認証画面は `connect-src` も持つ独自のCSPのまま変えない。
- 静的書き出し（`export-static`）は `combobox.js` を書き出さない（Siteは使わない）。

## 4. カタログでの利用

- `features/catalog/views/admin_form.html` のタグの `<fieldset>` を `{{template "combobox" .TagField}}` に置き換え、`script` ブロックで `/static/js/combobox.js` を読み込む。
- `docs/domain/catalog.md` の「管理画面は通常のフォーム・リンクだけ」を、項目編集のタグ選択だけ部品のJSを使う（JSなしでも同じ操作ができる）と直す。不変条件の追加は不要の見込み（送信内容・保存の検査はサーバー側のまま）。

## 5. テスト

**Goの通常のテスト（`go test ./...`）**
- 部品のテンプレート: エスケープ、ラベル・`aria-*` の関連付け、JSなし用のチェックボックスが全候補分あり選択状態が残ること、コンボボックスが `hidden` で描かれること。
- `test/template_test.go`: JSの許可リストに `internal/web/static/js/combobox.js` を足し、`<script src>` を書けるテンプレートを `admin_form.html` に限る。
- JSの静的検査: `innerHTML`・`insertAdjacentHTML`・`eval`・`new Function`・`fetch`・`XMLHttpRequest`・ストレージ・`document.cookie`・`.style` を含まないこと。
- CSP: 項目の作成・編集画面だけ `script-src 'self'`、管理一覧・分類管理・公開詳細・エラー応答は `'none'`。

**ブラウザテスト（`make test-ui`、別モジュール `test/browser/`）**
- 独自の `go.mod` に `github.com/chromedp/chromedp`（v0.21.0、MIT。依存は同じ開発元の `cdproto` と、Go 1.27では読み込まれない `go-json-experiment/json`）だけを置く。キット本体の `go.mod`・リンクされるモジュールは変えない。
- テスト用のサーバーを `httptest` で立て、部品だけの画面とカタログの項目編集画面（テスト用DB・テスト用のセッション）を開く。
- Chromeは `CHROME_PATH` か `PATH` の `chromium`・`google-chrome` を使い、無ければテストを飛ばす。普段の `go test ./...` には含めない（入れ子のモジュールは親の `./...` に入らない）。
- 試作で確かめた操作を、そのままテストにする: キー操作一式、クリック、変換中のEnter、上限と通知、×の後のフォーカスと閉じたままの候補、外側を押す・Tabで閉じる、送信内容、幅320pxで横スクロールなし、JS無効、`combobox.js` の読込失敗、CSP違反・JSのエラーが無いこと。
- 読み上げソフトでの確認は自動化しない。未確認として残す。

## 6. 文書

AGENTS.md（JSの例外にCoreの `combobox.js` と項目編集画面を足す）、`docs/ui-guidelines.md`（3段目を「素のJS」にし、部品の作り方の決まりを書く）、`docs/components.md`（部品と `web.AllowScripts`）、README（JSの説明、`make test-ui`）、実装・監査スキル（JSの例外の一覧）、`docs/domain/catalog.md`。

## 7. 進め方

1. この計画を外部レビューにかけ、指摘を反映する。
2. 部品（JS・テンプレート・型）と通常のテスト。
3. `web.AllowScripts` とカタログでの利用。公開一覧の置き換えも揃える。
4. `test/browser/` と `make test-ui`。
5. 文書の更新、全テスト・`make verify`・`make test-ui`・ビルド。紹介サイトへ取り込む。

## 8. 守らない範囲・残るリスク

- `'self'` は、その画面で自サイトのどのJSでも読める。読み込むJSはテンプレートの検査で限る。
- HTMLを差し込まれた場合、`data-combobox` を付けた要素に部品の振る舞いが付く。部品の操作は画面内のチェックボックスの切り替えだけで、送信・通信・削除はしないため、被害は画面の表示の範囲にとどまる見込み。HTMLの差し込み自体は `html/template` のエスケープで防ぐ。
- Chromeの版が上がると、ブラウザテストの結果が変わることがある。テストは操作の結果（属性・フォーカス・送信内容）で判定し、見た目のピクセルは比べない。

## 9. 外部レビューの反映（2026-10-27）

上の節より、この節を優先する。

- **今いる候補の決め方**: 入力・フォーカス・クリックでは候補を開くだけで、今いる候補（`aria-activedescendant`）は決めない。↓・↑で初めて候補を指す。Enterは候補を指しているときだけ選択・解除し、指していないときは何もしない（送信はしない）。1文字ごとに先頭の候補名が読まれ、件数の通知と重なるのを避けるため（WAI-ARIA APGの手動選択の形）。
- **1画面に複数**: 部品は `<div data-combobox data-limit>` の中に、JSなし用の `<fieldset data-combobox-fallback>` と隠したUIを包む。JSはその中だけを探す。候補の要素IDは値ではなく連番（`接頭辞-option-番号`）にする。外側を押したときの処理は、文書に1つだけ登録する。
- **見出し**: 部品の中に見出し要素を固定しない。JSなし用は `<fieldset>`/`<legend>`、UIは `role="group"` と `aria-labelledby`、選択済みの一覧は `aria-label` を付けた `<ul>`。
- **CSP**: 既定のCSPを定数にし、`web.AllowScripts` は現在値が既定と一致するときだけ置き換える。描画に成功してから呼ぶ（テンプレートの実行に失敗した500では `'none'` のまま）。カタログでは項目の作成・編集画面の描画の1か所で呼ぶ。`internal/web` に単体テストを1本足す。
- **CSS**: `style.css` に部品のクラス（`combobox-*`）を足す。今いる候補は色だけでなく枠線でも示す。`[hidden]` の規則は末尾のまま。
- **ブラウザテストの組み方**: 入れ子のモジュールの名前は `minimal-web-kit/test/browser` にし、`replace minimal-web-kit => ../..` で本体を参照する（`internal` を使うため）。管理画面は、`features.Admin` のスタブ（常に管理者として通し、固定のCSRFトークンとレイアウトを返す）で組む。認証機能の本体にテスト用の口は足さない。`go.work` は置かない。Chromeのプロファイルは `t.TempDir()` に置く。`make test-ui` では `go vet` も回す。
- **削るもの**: 閉じた状態のEnterで開く動き。`htmx:load` での付け直し（管理画面にhtmxは無く、検証もしないため）。
- **未確認に加えるもの**: タッチ操作・iOS VoiceOverでの候補の選択、`aria-multiselectable` の読み上げの差。

## 10. 実装の結果（2026-10-27）

- 部品: `internal/web/static/js/combobox.js`（外部ライブラリなし）、画面部品 `combobox`、`form.Combobox`・`form.Option`、`style.css` の `combobox-*`。カタログの項目の作成・編集画面のタグ選択で使う。
- CSP: `web.AllowScripts` を足し、カタログの公開一覧の置き換えも同じ関数にした。描画に成功したときだけ呼ぶ。
- ブラウザテスト: `test/browser/`（`make test-ui`、chromedp v0.21.0）。9節の決めごとに対し、管理画面はスタブではなく、既存のテスト用ヘルパー `features/auth/authtest` でDBにセッションを作り、本物の管理者認証で組んだ（認証機能に新しい口は足していない）。部品だけの画面（2つ置く）と、カタログの項目の作成・保存・再表示を確かめる。
- chromedpの `Poll` はページ内で `new Function` を使い、キットのCSP（`unsafe-eval` なし）で止められるため使わない。`KeyEvent` はEnterを keyDown と char に分けて送り、char がフォームを送信してしまうため、特別なキーは keyDown・keyUp で、文字は `Input.insertText` で送る。
- わざと部品を壊して、テストが落ちることを確かめた: ×の後に候補が開く、変換中のEnterで選ぶ、上限を超える、フォーカスで先頭の候補を指す。外側を押したときに閉じる処理は、フォーカスが外れたときに閉じる処理と二重の守りのため、片方を壊してもテストは落ちない。
- 未確認: 画面読み上げソフトでの通知・操作、タッチ操作・iOS VoiceOverでの候補の選択、`aria-multiselectable` の読み上げの差。
