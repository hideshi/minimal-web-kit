---
name: verify-and-audit
description: 機械的テストを実行し、依存許可リスト、HTML/CSS規約、DATA.md台帳突合、およびログ漏洩防止を検証する。
---

# 検証・監査スキル (verify-and-audit)

このスキルは、コードの変更後に規約が守られているかを機械的に検証し、漏れがないか確認するための手順です。

## 1. 自動テストによる検証
変更完了後、必ず以下のテストを実行し、すべてPASSすることを確認します。

```bash
# 全テストとデータ競合検出の実行
go test -race -v ./...
```

### 自動検証される項目:
- **`TestArchitecture_DependencyAllowedList`**: `go.mod` の直接依存が許可リスト（`modernc.org/sqlite` のみ）と一致しているか。
- **`TestDataSchema_MatchesDataLedger`**: `DATA.md` に記載されたテーブル・列定義と、実際のマイグレーションSQLが完全に一致しているか。
- **`TestTemplate_NoInlineStylesOrScripts`**: HTMLテンプレートにインライン `style=` や `<style>`, `<script>` が含まれていないか。
- **`TestCSS_SingleFileAndNoImportant`**: CSSファイルが単一であり、`!important` が含まれていないか。
- **`TestCanary_LogProtection`**: メモ本文の機密データが、ログやエラー応答に一切現れていないか（カナリアテスト）。

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
