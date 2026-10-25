---
name: minimal-web-kit-audit
description: minimal-web-kitの変更を検証する際に、付属テストと追加検証で依存・保存データ・ログ・権限・UI・公開前設定を監査する。汎用のテストやレビューのスキルと併用し、このキットの規約との整合を確認する。
---

# 検証・監査スキル (minimal-web-kit-audit)

このスキルは、コードの変更後に規約が守られているかを機械的に検証し、漏れがないか確認するための手順です。

他の専門スキルと併用し、minimal-web-kit固有の判断・規約を補完します。一般的な開発手順を置き換えるものではありません。

## 1. 自動テストによる検証
変更完了後、必ず以下のテストを実行し、すべてPASSすることを確認します。

```bash
# 全テストとデータ競合検出の実行
go test -race -v ./...
```

### 自動検証される項目:
- **`TestArchitecture_DependencyAllowedList`**: `go.mod` の直接依存が許可リスト（`modernc.org/sqlite` のみ）と一致しているか。
- **`TestDataSchema_MatchesDataLedger`**: `docs/data-inventory.md` に記載されたテーブル・列定義と、インメモリDB上の実スキーマ（`PRAGMA table_info`）のテーブル名・列名が双方向で一致しているか。Featureは有効化の状態にかかわらず全 `features/*/migration.sql` を適用する。型・NULL制約・外部キーや、`migration.sql` 以外で作るテーブルは検査しない。
- **`TestTemplate_NoInlineStylesOrScripts`**: HTMLテンプレートにインライン `style=`、`<style>`、`<script>`、および `on*` イベントハンドラが含まれていないか。
- **`TestCSS_SingleFileAndNoImportant`**: CSSファイルが単一であり、`!important` が含まれていないか。
- **`TestCanary_LogAndPrivacyProtection`**: 公開ページの新規セッション非発行、未知Cookieの拒否、Request-ID注入や削除ログからのトークン・OwnerID漏洩を検査する。任意の業務データを網羅した保証ではない。URL・panic・削除エラーの経路は `TestCanary_RequestAndPanicLogProtection` と `TestCanary_StoreErrorLogProtection`、CMSの公開ページ・API・管理画面は `TestCanary_CMSRoutesDoNotLeakInput` で検査する。

## 2. 人手による差分チェック観点
自動テストに加え、`git diff` で以下の観点を確認します。
1. **承認必須境界の確認**:
   - `go.mod` に意図しない依存が増えていないか。
   - 新しいDB列やCookieが、利用者の承認なく追加されていないか。
   - 外部ホストへのHTTP通信が発生していないか。
2. **待受アドレスの確認**:
   - 既定の待受アドレスが `127.0.0.1` のままであるか（`0.0.0.0` に不用意に広げていないか）。
3. **エラーメッセージの確認**:
   - 内部エラーや機密情報が、HTTPレスポンスの画面に生テキストとして露出していないか。

## 3. 約束に応じた追加検証
- `make verify` と `make build` を実行する。GoはPATH外にある場合も確認し、未導入や失敗なら検証済みと報告しない。Goバージョンは `go.mod` に従い、CGO無効・アセット埋め込みのビルド結果とサイズを確認する。
- 新しい個人情報・機密情報を扱う変更では、承認、docs/data-inventory.md、プライバシー説明、保持期間、削除手段を照合する。既存テストのPASSだけで追加項目が安全と判断しない。
- 機密値をパス、クエリ、ヘッダー、本文、panic、エラーへ投入する経路を確認し、変更箇所に応じた漏洩カナリアを追加する。AccessLogのURL・任意メソッド名の除外、Recovererのpanic値の除外、生エラーの非出力を維持する。追加機能やリバースプロキシ等のログは別途検証する。
- 動的機能を追加したら、別Ownerからの読み書き・削除の拒否、未認証の管理操作、CSRF不正・欠落、期限切れCookieを検証する。SQLiteは複数接続で制約と削除を確認する。
- 公開ページと404はRoutes経由で新規Cookieと保存行の増加がないか確認する。Site有効時とBaseの差、既存Cookieを送った場合も区別する。

## 4. 実画面・公開前の確認
- 320 CSS px幅、文字200%拡大、行高1.5・段落後2em・字間0.12em・単語間0.16emを適用し、欠落やページ全体の横スクロールを確認する。表の横スクロールは本文と分離する。
- Tab順序、スキップリンク、フォーカスの見切れ、リンクのEnter、buttonとsummaryのEnter/Spaceを実操作する。支援技術で見出し・リスト・表・入力ラベルを確認する。
- 色の計算や静的検査だけでJIS/WCAGの完全適合を宣言しない。対象ページ・状態・ブラウザ・実施項目・未確認項目を報告する。
- 本番公開する場合はHTTPS終端、`SECURE_COOKIE=true`、待受・公開範囲の承認、DB/ディレクトリ権限、ログとバックアップの保持・削除方針を確認する。公開操作自体の承認は別途得る。
- `CONTACT_FORM_URL` が実際のフォームを指すか、入力項目と利用目的・保持期間・削除窓口がポリシーに一致するかを確認する。仮URLを実用の窓口として案内しない。
- LP・README・スキル・コードを照合し、同梱済み機能（Site・CMS）と範囲外を区別する。未実装の権限管理・画像アップロード等や未確認の性能を提供済みと表現しない。
- CMSを有効にした場合は、パスキーでの登録・ログイン、同期型パスキーの拒否、`/news`・`/api/news` がCookieを扱わないこと、CORSが既定で無許可であること、JSが認証画面だけで動くことを確認する。
