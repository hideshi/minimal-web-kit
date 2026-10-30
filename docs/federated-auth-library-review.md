# 共通認証のライブラリ比較

調査日: 2026-10-30。設計のために公式の版付きソース・リリース・go.mod・ライセンスを読み合わせた。キットのgo.mod/go.sumは変更せず、ライブラリを組み込む検証は未実施。採用済みという意味ではない。

## 1. 推奨案

Aの提供側に `github.com/zitadel/oidc/v3 v3.51.16`、Bの利用側に `github.com/coreos/go-oidc/v3 v3.21.0` と `golang.org/x/oauth2 v0.37.0` を候補として推奨する。署名鍵のアダプターで使う `github.com/go-jose/go-jose/v4` は `v4.1.6` を明示する。

OIDC提供側の経路・検証・応答全体を独自に組む範囲を減らし、Bには利用側だけのライブラリを使う。AとBで別の実装を使うため、双方の独自な癖にだけ依存して動く状態を結合検証で発見しやすい。これは設計上の判断であり、今回互換性を実証した結果ではない。

| 候補 | 役割・ライセンス | 上流go.modの宣言（非indirect / indirect） | 判断 |
|---|---|---|---|
| [zitadel/oidc v3.51.16](https://github.com/zitadel/oidc/tree/v3.51.16) | OP/RP、Apache-2.0 | 17 / 9、Go 1.26.0 | Aの第一候補。Storageアダプター・経路の制限・原子性はキットが実装する |
| [coreos/go-oidc v3.21.0](https://github.com/coreos/go-oidc/tree/v3.21.0) | RPのIDトークン検証・discovery/JWKS、Apache-2.0 | 2 / 0、Go 1.25.0 | Bの第一候補。ログイン試行・nonce・世代・Bセッションはアプリが検査する |
| [x/oauth2 v0.37.0](https://github.com/golang/oauth2/tree/v0.37.0) | 認可URL・コード交換・PKCEの補助、BSD-3-Clause | 1 / 0、Go 1.26.0 | Bでコード交換とBasic認証を明示する。トークンの自動更新機能は使わない |
| [go-jose v4.1.6](https://github.com/go-jose/go-jose/tree/v4.1.6) | JOSE/JWK、Apache-2.0 | 0 / 0、Go 1.24.0 | Aの署名アダプターと推移依存の修正版として明示 |
| 標準ライブラリ＋限定した自前OIDC | HTTP・乱数・暗号は標準 | 追加0 | 全プロトコルの適合・攻撃対策・更新を自分で保守する負担が大きいため第一候補にしない |
| A/BともzitadelのOP/RP | 上記SDKを共用 | 同じSDKの宣言 | 次点。構成次第で直接依存を最大2本減らせるが（Basic認証等を明示するためx/oauth2を直接importするなら1本の削減）、BにもSDK側の依存・既定のCookie処理との接続が必要になる |
| 独立した既存IdPを置く | 別サービス | キットへの組込みとは別 | 運用サービスが増え、Aの既存アカウント管理をそのまま提供者にする要望とは異なるため今回は選ばない |

数値は版付きgo.modのrequire行を数えたもので、テスト・ツールを含む。実際にリンクされるモジュール数でも、完全な推移依存グラフの数でもない。将来のRoot go.modの非indirect行数は最大5本（既存SQLite＋上記4本）と見込む。x/oauth2は上流同士の要求の高い方に合わせてv0.37.0を候補にしたが、全体のバージョン選択・互換性は組込み時に再確認する。

通常の公開鍵署名・HTTP処理にCGOを要求する設計ではない。ソースの読み合わせだけで全推移依存のCGO非使用を保証せず、承認後に `CGO_ENABLED=0` でA/Bのビルドと実行を確認する。サイズも未測定であり、「軽量」との保証はしない。

## 2. 保守と修正版

公式リリース情報では、調査時の比較版はzitadelが2026-10-15、coreosが2026-09-01、go-joseが2026-10-14に公開されている。これを将来の保守継続の保証には使わない。実装前に版・セキュリティ勧告を再確認する。

両OIDC候補のgo.modはgo-jose v4.1.4を要求していた。上流の最小要求のまま採用しない。

- [JSON解析の勧告](https://github.com/go-jose/go-jose/security/advisories/GHSA-vvm8-gf78-28g4)ではv4.1.5が修正版。
- [CBC-HMAC処理の勧告](https://github.com/go-jose/go-jose/security/advisories/GHSA-4w3g-jfj2-cw2q)もv4.1.5が修正版。初版はJWEを使わないが、依存の更新対象として扱う。
- [RSA JWK解析の勧告](https://github.com/go-jose/go-jose/security/advisories/GHSA-mgcc-cww5-fp2q)はv4.1.5以前を対象にし、修正版はv4.1.6。

そのため候補をv4.1.6に引き上げる。これは「既知の全問題がない」との宣言ではなく、読んだ勧告への対応。採用時は実際の選択バージョンに対してgovulncheck等で到達可能性を確認し、より新しい修正があれば差し替えを提案する。現時点ではその検査は未実施。

## 3. SDKが解決すること・アプリが解決すること

| SDKに任せる候補 | アプリで必ず検査・実装すること |
|---|---|
| 認可要求・トークン応答の標準形式 | 有効化するgrant/scope/経路の制限と実際のdiscoveryの一致 |
| JWT/JWKの標準処理 | 許可アルゴリズムES256、鍵取得先とサイズ・タイムアウト・更新頻度の制限 |
| discovery・署名・issuer/audience/期限等の検証機能 | 設定と試行に結び付いたissuer・nonce・PKCE、auth_time・独自の失効世代の確認 |
| コード交換のHTTP処理 | Basic認証を固定、戻り先固定、Cookie/state照合、一回限りの試行、元画面への安全な復帰 |
| Storageインターフェース | コード消費・発行・失効の原子性、停止・復旧・GC、初版では不要なUserInfo/トークン保存を実装しないこと |

[zitadelのStorage](https://github.com/zitadel/oidc/blob/v3.51.16/pkg/op/storage.go)と[トークン生成](https://github.com/zitadel/oidc/blob/v3.51.16/pkg/op/token.go)は、保存メソッドの分割を確認する根拠。SDKがDeleteAuthRequestを呼ぶタイミングと、消費済みコードの墓標保持をアダプターで調整する必要がある。署名に使うclaimの世代を最新値に差し替えると古いログインが復活するため、認証済み要求のスナップショットを使う。

[coreosのVerify](https://github.com/coreos/go-oidc/blob/v3.21.0/oidc/verify.go)を使っても、nonce等をアプリが試行と結び付ける責任は残る。検証を省略するSkip系オプションは使わない。SDKのログやOpenTelemetryの導入は外部への送信の承認を意味しない。外部exporterを設定せず、内部の生エラーをレスポンスや通常ログに流さない。

## 4. 採用の条件

1. 利用者が依存の増加と、単一モジュールに入れる範囲を承認する。最小のBaseや通常authに不要なライブラリをリンクさせない。SDKは子パッケージとB機能だけでimportし、登録ファイルで構成を選ぶ。require行数とリンク済みモジュール数を区別する。
2. 版を固定した結合検証で、コードの二重交換・停止・ロール変更・再開・SDKの生成失敗でも原子性が保たれる。
3. Base／ローカルauth／A／Bごとに、実際のビルドでリンク済みモジュール・推移依存・CGO非使用・バイナリサイズを記録する。go.modの宣言数で代用しない。
4. ライセンスのNOTICE等の必要事項を一覧化し、承認後に依存検査の許可リスト・READMEを更新する。
5. 標準OIDCクライアントとの相互運用と、キット独自の停止照会の結合を分けて検査する。SDKの既定で動いただけでは完了にしない。

この条件を満たさない場合、依存数を隠したり原子性を緩めたりせず、同一SDK案または別モジュール案へ戻して設計を見直す。

## 5. 調査の出典

- [zitadelのリリース](https://github.com/zitadel/oidc/releases/tag/v3.51.16)、[go.mod](https://github.com/zitadel/oidc/blob/v3.51.16/go.mod)、[LICENSE](https://github.com/zitadel/oidc/blob/v3.51.16/LICENSE)。
- [coreosのリリース](https://github.com/coreos/go-oidc/releases/tag/v3.21.0)、[go.mod](https://github.com/coreos/go-oidc/blob/v3.21.0/go.mod)、[LICENSE](https://github.com/coreos/go-oidc/blob/v3.21.0/LICENSE)。
- [x/oauth2のgo.mod](https://github.com/golang/oauth2/blob/v0.37.0/go.mod)、[LICENSE](https://github.com/golang/oauth2/blob/v0.37.0/LICENSE)。
- [go-joseのリリース](https://github.com/go-jose/go-jose/releases/tag/v4.1.6)、[go.mod](https://github.com/go-jose/go-jose/blob/v4.1.6/go.mod)、[LICENSE](https://github.com/go-jose/go-jose/blob/v4.1.6/LICENSE)。
- [OIDC Core](https://openid.net/specs/openid-connect-core-1_0.html)、[OAuth Security BCP](https://www.rfc-editor.org/rfc/rfc9700.html)。

## 6. FableレビューP-64後の修正

候補と版の根拠はレビューでも一致した。SDKの認証順・PKCE必須化・discovery広告はアプリ側で補う。直接依存は最大5本のままだが、単一モジュール内のパッケージ分割でBase/通常authへのSDKリンクを防ぐ案へ具体化した。同一SDK案の直接依存削減は最大2本に訂正。コード再利用の失効は事前のクライアント認証とコード束縛検査の後だけに限定する。

IDトークンの世代スナップショットは、GetPrivateClaimsFromRequestではなくIDトークン用のSetUserinfoFromRequest経路を使う。トークン生成ソースで用途を区別したが、実装承認後にopenidだけのscopeでの動作を確認する。
