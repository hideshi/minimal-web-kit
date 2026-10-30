# ドキュメントの案内

今の仕様と手順は、この階層と `domain/` が正本です。`plans/` は、決めた経緯の記録です。今の動作と食い違うときは、正本とコードを優先します。

| 場所 | 内容 |
|---|---|
| [data-inventory.md](data-inventory.md) | 取得・保存データの台帳（`test/data_schema_test.go` が照合する） |
| [components.md](components.md) | 共通部品、画面の型、用語とメッセージ |
| [ui-guidelines.md](ui-guidelines.md) | 動きのあるUIの作り方 |
| [richtext.md](richtext.md) | 本文の記法の仕様 |
| [deploy.md](deploy.md) | 本番デプロイの手順例 |
| [ui/use-cases.md](ui/use-cases.md) | 誰が何を達成するために使うか（`test/usecase_test.go` が照合する） |
| [domain/](domain/) | 機能ごとの用語・状態・不変条件・権限（`test/domain_test.go` が照合する） |
| [plans/](plans/) | 機能の計画書、棚卸し、比較の記録 |
| [experiments/](experiments/) | 方式の検証の試作と結果 |

新しい計画書は `plans/` に置きます。
