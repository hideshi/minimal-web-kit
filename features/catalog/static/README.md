# カタログのJavaScript

- `htmx.min.js`: htmx 2.0.11、Zero-Clause BSD（同梱の LICENSE-htmx）。外部依存なし。Goの直接・間接依存には含まれない、ブラウザ側の依存。
- 取得元: https://raw.githubusercontent.com/bigskysoftware/htmx/v2.0.11/dist/htmx.min.js
- ライセンス取得元: https://raw.githubusercontent.com/bigskysoftware/htmx/v2.0.11/LICENSE
- SHA-256: `d6fdc75f204e6bdefa99b69bf1e6d4ac69b8a364f77929f45c13476b4000f717`
- 公式設定資料: https://htmx.org/docs/#config
- `catalog.js`: 公開GETの読込状態・失敗時の再試行・読み上げ通知・ページ送り後のフォーカスを扱う自前コード。

npm/Node.jsや実行時のCDNは使わない。配信はCatalog有効時の `/catalog/static/htmx.min.js` と `/catalog/static/catalog.js` だけ。公開一覧の全ページ応答だけで2本を読み込む。

meta設定でeval・応答script評価・スタイル挿入・外部要求・localStorageへの履歴HTML保存を無効にする。`attributesToSettle` もclassだけに限定し、style属性の転記を避ける。履歴復元は全ページ再取得。設定の自動検査とGoのHTTPテストに加え、更新時には実ブラウザでCSP違反・通信失敗・戻る進むを確認する。

更新時は固定タグのソース・リリース内容・ライセンス・差分を確認し、このハッシュと検証テストを一緒に更新する。外部ライブラリを自前実装へ置換して依存を隠さない。Alpine.jsは含めない。
