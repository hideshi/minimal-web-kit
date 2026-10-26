---
name: minimal-web-kit-implementation
description: minimal-web-kitで機能を実装・変更する際に、Go・SQLite・SSR・データ保護・HTML/CSSの規約を適用する。機能別の実装スキルと併用し、このキット固有の構造と安全策を維持する。
---

# 実装・変更スキル (minimal-web-kit-implementation)

このスキルは、ひな形の構造と標準ライブラリの既定解に従ってコードを追加・変更するための手順です。

他の専門スキルと併用し、minimal-web-kit固有の判断・規約を補完します。一般的な開発手順を置き換えるものではありません。

## 1. 標準機能での既定解一覧
AIが安易に外部ライブラリを追加しないよう、以下の標準解を優先して使用します。

| 目的・課題 | 推奨する標準解 | 避けるべき外部依存 |
|---|---|---|
| ルーティング | `http.NewServeMux` (Go 1.22+ パターン構文) | gin, echo, chi |
| 画面描画 | `html/template` (SSR, 自動HTMLエスケープ) | react, vue, 外部テンプレートエンジン |
| 識別子・トークン | `crypto/rand` (16バイト乱数のHex文字列) | google/uuid などの外部ライブラリ |
| ハッシュ化 | `crypto/sha256` | 外部ハッシュライブラリ |
| 構造化ログ | `log/slog` ＋ `slog.LogValuer` | zap, logrus |
| アセット配信 | `embed.FS` ＋ `http.FS` | 外部アセットサーバー、外部ビルドツール |
| 入力検証 | Go標準の手書き条件分岐と文字数チェック | go-playground/validator |
| マイグレーション | 埋め込みSQLファイル (001_init.sql等) | migrate, goose |

## 2. 機能（Feature）の作り方
新しく書く前に `docs/components.md` の部品（入力の検査、独立経路の守り、ページ送り、完了メッセージ、画面部品）で作れないかを確かめる。テンプレートは `web.ParseWithComponents` で解析し、部品を使う。
1. **配置**: `features/<名前>/` に機能を作り、`cmd/server/features.go` で登録する。`features/site`・`features/cms` は同梱テンプレートの予約名で、自作機能には使わない。
2. **スキーマ**: テーブルは `features/<名前>/migration.sql` に書き、`Init` から同じファイルを適用する。同時に `docs/data-inventory.md` を更新する（台帳テストは全 `features/*/migration.sql` を照合する）。`internal/store/migrations/` はCoreのスキーマだけに使う。
3. **経路**: 匿名セッションを使う画面は `RegisterRoutes` に載せる（CoreのCSRF検査とOwnerIDの解決を通る）。データはOwnerIDで分離する。Cookieを使わない公開ページや独自認証の管理画面は `IsolatedRoutesFeature` に載せ、認証・CSRF・Origin検査を機能自身で実装する。
4. **任意のインターフェース**（`features/feature.go`）: トップ画面の差し替えは `CustomRootFeature`、期限切れデータの削除は `PurgeFeature`、ナビへのリンクは `NavLinkFeature`、運用コマンドは `CommandFeature`（`minimal-web-kit <コマンド>`）。「他の機能があるか」で分岐するコードは書かず、これらの口を使う。
5. **型**: 機密や本文を含む型には `slog.LogValuer` を実装する。
6. **画面とCSS**: テンプレートは機能の `views/` に置き、`html/template` で描画する。インライン `style`・`<style>`・`<script>` を書かず、意匠は `style.css` のクラスかセマンティックなHTMLで表す。フォーカス表示（`:focus-visible`）とコントラストを維持する。

## 3. データ保護とセッションの拡張
- 取得する入力は形式・UTF-8・長さ・本文サイズをサーバー側で検証し、解析エラーを扱う。文章と1行の値は `internal/web/form`（`form.Text`・`form.Line`）で検査する。`template.HTML` 等による自動エスケープの回避は行わない。
- Featureの検索・更新・削除は、リクエストの識別子だけを信用せずOwnerIDで絞る。共有・管理者操作は別途権限を検証する。
- 管理機能には明示的な認証を実装する。無記名セッションを持つだけで管理者にしない。状態変更をGETで実装せず、CSRF保護と認可を両方適用する。
- SQLiteの接続別PRAGMAを全接続に適用し、外部キーのCASCADE、期限切れ照会、GC、削除後の再アクセスを確認する。
- `owners` へのCASCADEは、セッション破棄と30日放置のGCで消えてよいブラウザ単位の一時データに限る。CMSの記事・業務データ等の残すべきデータは `owners` に紐づけず、管理者アカウント等の別の持ち主と保持・削除方法を設計する。
- 公開ページでは `EnsureSession` を呼ばない。ハンドラ単体でなく、Routes経由でCookie発行と保存件数を確認する。サーバー起動時のDB初期化とページ表示時の保存は区別する。
- ログには生の入力・URLパス・クエリ・ヘッダー・panic値・DBエラーを安易に渡さず、固定の分類とサーバー生成IDを使う。`LogValuer` だけで文字列や外部ライブラリのエラーは保護できない。
- 削除の説明は稼働DB上のレコード削除の範囲に限定し、WAL・バックアップ・外部フォームの保持を別途説明する。
- 独立経路の状態変更（POST）では、`internal/web/guard` の `guard.SameOrigin` と `guard.CSRF` を使い、自前で書き直さない。`Sec-Fetch-Site: same-origin` を必須にする。`Origin` は、Coreの `Referrer-Policy: no-referrer` により同一オリジンでも `null` になるため、`null` か自サイトとの一致を認める。加えて、セッションに結びつけたCSRFトークンと `SameSite=Strict` のCookieを使う。
- 日時はUNIX秒で保存し、表示はサーバーのタイムゾーン（`TZ`）で行い、`<time datetime>` を添える。

## 4. UIを拡張するとき
- 見出しをh要素で表し、lang、ランドマーク、スキップリンク、入力ラベル、表見出しと外部リンクの説明を維持する。
- 通常・hover・focusの文字色と背景色、入力境界とフォーカスの識別を確認する。カード数に応じた列数と狭い画面での1列表示を設計し、固定幅・固定高さで文字を切らない。
- JavaScriptを使わず、標準のリンク・button・details/summaryで操作を提供する。唯一の例外はCMSの認証画面の `webauthn.js` で、例外の拡大は承認境界4・5に当たる。開閉・モーダル・メニューは `<details>`・`<dialog>`・`popover` で作り、承認を得てhtmx等を使う場合は `docs/ui-guidelines.md` の決まり（CSP、CSRF、変化の通知、フォーカス、JSなしでの動作）に従う。外部フォームURLは実際の窓口に設定し、新規タブの通知と `noopener noreferrer` を維持する。
- `hidden` 属性が `.card`・`.btn` 等の `display` 指定に負けないよう、`style.css` 末尾の `[hidden]` 規則を保つ。`<button>` と `a.btn` を並べる画面では、高さが揃うかを確認する。
- ナビ・見出し・ボタンの用語は既存の画面に揃える（例: 「ホーム」「プライバシーポリシー」）。

## 5. コマンドと生成するファイル
- サブコマンドの結果は標準出力に出し、ログは標準エラーにエラーだけを出す。登録URL等の表示にログを混ぜない。
- コードを生成・書き換えるツールは、元のファイルがgofmt済みなら結果もgofmt済みに保ち、整形されていない利用者のコードの見た目は変えない。
