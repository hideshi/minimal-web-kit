# タグ選択の比較試作

本体のカタログには組み込まない、架空データだけの比較です。リポジトリのルートで起動します。

```sh
go run ./cmd/catalog-ui-compare
```

標準出力に出る2つのURLを開きます。127.0.0.1の空きポートだけで待ち受け、Ctrl+Cで終了します。CSSは起動時に既存の `internal/web/static/css/style.css` を読みます。実DB、アカウント、Cookie、localStorage、sessionStorageは使いません。「確認する」はPOSTした内容を表示するだけで保存しません。

10・50・200件から選び、同じタグを選択・解除・見直して比較してください。標準HTML版ではブラウザのページ内検索も使えます。方式を変えると入力を初期化するので、両方を別タブで開くと比較しやすくなります。操作履歴や計測値は自動保存しません。

Alpine版は、検索・選択済みの一覧・個別解除・既存タグ20個時の未選択項目の抑止を追加します。新規タグを含む合計上限は両版とも確認時にサーバーが検査します。JSが無効・読込失敗の場合は全件のチェックボックスに戻ります。検索欄は初期化できた場合だけ表示します。独自のコンボボックスやstyle属性への依存は作りません。

## ブラウザ依存

- `static/alpine-csp.min.js`: `@alpinejs/csp` 3.17.5、MIT。ライセンスは `static/LICENSE-Alpine`。
- 取得元: https://cdn.jsdelivr.net/npm/@alpinejs/csp@3.17.5/dist/cdn.min.js
- 公式資料: https://alpinejs.dev/advanced/csp
- SHA-256: `48c170c92032e527cbae71b8ce75cf1f96ca06fa869a6120ef0fe438d8e92c36`
- 本体サイズ: 72,242バイト（圧縮転送前）。パッケージの依存にVueのリアクティビティがあり、取得したブラウザ用ビルドに内蔵されます。VueのMITライセンスも `static/LICENSE-Vue` に同梱します。外部モジュールの実行時取得はしません。
- Vueライセンス取得元: https://raw.githubusercontent.com/vuejs/core/v3.5.40/LICENSE
- ライセンス取得元: https://raw.githubusercontent.com/alpinejs/alpine/v3.17.5/LICENSE.md
- `static/tags.js`: 名前付きAlpine部品の登録。属性には状態参照・名前付きメソッドの呼び出しだけを書き、処理をこのファイルへ置きます。

試作用2本はAlpine版だけで読み込みます。CSPのscript-src selfはこの画面だけ、標準版はnoneです。通常のサーバーにはJSも画面も登録しません。Node.js/npmやCDNの実行時読み込みは不要です。更新する場合は固定版の公式資料・ライセンス・差分・ハッシュを照合し、ブラウザでCSP・キーボード・フォーカス・JS無効時を検証します。

比較計画と結果は [catalog-ui-comparison-plan.md](../../docs/catalog-ui-comparison-plan.md) にあります。読み上げソフトでの評価と利用者による操作感の比較が済むまで、アクセシビリティの完全適合や採用の決定は宣言しません。
