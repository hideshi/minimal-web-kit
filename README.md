# minimal-web-kit

個人情報の取得と依存関係を最小限に抑える、Webサイト・業務アプリの開発基盤です。Go製のひな形、Site・CMSテンプレート、3本のAI開発スキル、規約を検査するテストを同梱しています。

## 特徴と提供範囲

- **Go標準機能を中心とした構成**：`net/http`、`html/template`、`embed.FS`、`log/slog` を使用します。直接依存は純Go版SQLiteドライバ1件で、間接依存もあります（[一覧](#依存モジュール)）。Node.js/npmや外部CSSフレームワークは使用しません。
- **機能ごとの分離**：自作機能は `features/<機能名>/` に配置します。Site・CMSの切り替えコマンドは登録ファイルだけを変更し、機能ディレクトリ内のファイルは変更しません。
- **取得データの最小化**：氏名や連絡先は既定で取得しません。必要な情報を追加するときは、利用目的・保存先・保持期間・削除方法を整理し、開発規約に沿って承認を得て台帳と検査を更新します。
- **セッションとデータ保護の基盤**：ランダムトークンとDBへのSHA-256ハッシュ保存、CSRF保護、セキュリティヘッダー、期限切れセッションの削除処理を備えています。OwnerIDはデータ分離の識別子であり、管理者認証や業務上の権限管理の代わりにはなりません。
- **自前CSSとHTMLによるUI**：CSSは `style.css` 1本です。JavaScriptは使用しません。唯一の例外は、CMSの管理画面でパスキー（WebAuthn）を扱う `webauthn.js` 1本で、登録・ログイン・パスキー管理の画面だけで読み込みます。高コントラスト、フォーカス表示、見出し・ランドマーク、スキップリンク等を備えています。JIS X 8341-3 / WCAG 2.1 AAを意識した設計ですが、完全適合を宣言するものではなく、作成したページごとの実画面・支援技術での検証が必要です。
- **アプリケーションログの最小化**：アクセスログはサーバー生成のリクエストID、既知のHTTPメソッド（その他は `OTHER`）、応答状況、処理時間に限定します。URLパス・クエリ・Cookie・IP・User-Agent・本文や、生のpanic値・エラー内容を記録しない実装です。追加機能やリバースプロキシ等のログは別途確認してください。
- **スキルと自動検査**：キット固有の設計・実装・監査を支えるスキルと、依存・テンプレート・データ台帳・ログ等のテストがあります。スキルはAIへの手順であり、すべての規約を機械的に強制するものではありません。

企業ホームページ、社内ツール、Webユーティリティ等の出発点として利用できます。お知らせを管理する簡易CMS（パスキーでログインする管理画面、公開ページ、読み取り専用のJSON API）も同梱しています。複数の編集者・権限管理・画像のアップロード等は含みません。

## 起動方法

### 前提

- Git、make（以下のコマンド例はPOSIXシェル向け）
- Go 1.27.1以上（`go.mod` の指定に従い、GoコマンドをPATHに含める）

```bash
git clone https://github.com/hideshi/minimal-web-kit.git
cd minimal-web-kit
make run
```

`http://127.0.0.1:8080/` を開いてください。`make run` はフォアグラウンドで起動し、停止は `Ctrl+C` です。コードやテンプレートを変更したら、停止して再実行します。自動リロード機能はありません。

同じポートが使用中なら、既存サーバーを停止するか `PORT=8081 make run` のように別ポートを指定します。`ADDR` が設定されている場合は、`PORT` より優先されます。

### BaseとSiteの違い

クローン直後はSiteが**無効**で、共通基盤の確認用トップページ（Base）が表示されます。Siteのソースファイル自体は最初から `features/site/` に同梱されています。

```bash
# 起動中ならCtrl+Cで停止し、Siteを有効化
make add-site
make run

# Ctrl+Cで停止し、Siteを無効化して再起動
make remove-site
make run
```

切り替えるのはトップページとプライバシー表示です。Siteの `/` と `/privacy` はGET/HEAD時にセッションCookieを発行・照合せず、セッション情報を保存・更新しません。Siteの `/privacy` ではセッション表や破棄リンクを表示しません。Baseの `/privacy` はセッションを利用する機能を追加した場合の保存項目を説明します。Baseでも、Cookieなしの閲覧だけで新規セッションは発行しません。

Siteを有効化しても、サーバーは起動時にSQLiteのDBファイルを開き、必要なテーブルが無い場合だけ作成します。既存のデータは削除しません。ログも出力します。別のDBサーバーは不要ですが、「DBファイルもログも一切作らないサーバー」ではありません。Coreのセッション破棄ルートも残ります。

### Siteの編集とお問い合わせ窓口

本文は `features/site/views/index.html`、ヘッダー・フッターは `features/site/views/layout.html`、共通CSSは `internal/web/static/css/style.css` で編集します。GitHubへのリンク（ヒーロー、3ステップの直後、ヘッダーのナビの3か所）はこのキットの公開リポジトリを指します。別の製品サイトへ流用する際は、リンク先や紹介文も見直してください。

お問い合わせボタンのURLは `CONTACT_FORM_URL` 環境変数で、`https://` で始まるURLを設定します。未設定やhttps以外の値の場合はボタンを表示せず「お問い合わせ窓口は現在準備中です。」と表示し、起動時に警告ログを出します（設定値はログに記録しません）。ボタンの補足文は「Googleフォーム」なので、別のサービスを使う場合は `index.html` の文言も変更してください。検索結果やSNSのカードに出る説明文は `features/site/site.go` の `description`、カード用の画像（1200×630px）は `internal/web/static/img/ogp.png` です。`SITE_URL` を設定すると、canonical・OGP・Xカードのタグと `sitemap.xml` が有効になります。`robots.txt` は常に返します。同梱の画像はこのキットの紹介用なので、別のサイトでは差し替えてください。

`/privacy` の末尾の「免責事項」（Site有効時だけ表示）は、このキットを紹介するサイトを前提にした例です。運営者と掲載内容に合わせて書き換えてください。公開前に実際のフォームURLを設定し、フォームの入力項目・利用目的・保持期間・削除窓口・外部サービスの扱いに合わせて `/privacy` の文面を編集してください。外部フォームの情報は、サイト内のセッション削除では消えません。

### CMS（お知らせ）

お知らせ1種類を管理する簡易CMSです。サーバーがHTMLを返す使い方（`/news`）と、別のフロントエンドがJSONを読む使い方（`/api/news`）の両方に対応します。Siteとは別に有効化でき、同時に使うとSiteのナビに「お知らせ」のリンクが出ます。

```bash
make add-cms
make run
# 別のターミナルで、初回登録用のURLを表示
make cms-setup
```

表示された `http://localhost:8080/admin/setup#token=...` を15分以内に開いてパスキーを登録し、`http://localhost:8080/admin/login` からログインします。WebAuthnはIPアドレスでは動かないため、`127.0.0.1` ではなく `localhost` で開いてください。

| 経路 | 内容 |
|---|---|
| `/news`、`/news/{id}` | 公開中のお知らせ。Cookieを発行・照合しません |
| `/api/news`、`/api/news/{id}` | 公開中のお知らせのJSON（`limit`・`offset`）。別ドメインからの読み取り（CORS）は `CMS_CORS_ORIGINS` で許可した接続元だけで、既定は許可なしです |
| `/admin/...` | お知らせの作成・編集・公開・予約公開・削除、パスキーの追加・名前変更・削除 |

- **ログインはパスキーだけ**です。パスワードはありません。複製を検出できない同期型のパスキー（スマートフォン、Googleパスワードマネージャー、iCloudキーチェーン等）は拒否し、Windows Helloやセキュリティキー等、端末に固定された認証器だけを登録できます。Windows Helloの指紋認証で登録・ログインを確認しています。ChromeOSの指紋認証は同期型として拒否されることを確認しています。MacのTouch IDも、iCloudキーチェーンの同期型のため使えない見込みです。
- 認証器は2つ以上の登録を推奨します。すべて失った場合は、サーバーで `make cms-recover`（本番は `minimal-web-kit cms-recover`）を実行し、登録し直します。記事は残ります。
- 管理者は1人で、権限の分離や操作の記録はありません。本文はプレーンテキスト（HTMLは文字として表示）で、画像のアップロードはありません。
- 公開日時に未来の日時を指定して公開すると、その日時まで公開ページとAPIに表示しません（予約公開）。時刻はサーバーのタイムゾーン（`TZ`）で扱います。
- 記事は匿名のOwnerIDに紐づけず、30日の自動削除の対象外です。保存するデータは[取得・保存データ台帳](docs/data-inventory.md)の4節にあります。
- 本番では `CMS_ORIGIN`・`CMS_RP_ID` を公開するURLに合わせ、`SECURE_COOKIE=true` にします。手順は [docs/deploy.md](docs/deploy.md) を参照してください。設計の詳細と、守る範囲・守らない範囲は [docs/cms-plan.md](docs/cms-plan.md) にあります。
- `make remove-cms` で登録を外しても、DBのデータは残ります。

### 切り替えコマンドの保護範囲

`features/site` と `features/cms` は同梱テンプレート用の予約ディレクトリです。自作機能には別名を使用してください。コマンドは `site.go`・`cms.go` 冒頭の識別コメント（例: `// minimal-web-kit: bundled-site-template`）と登録ファイルの構造を確認します。識別できない同名ディレクトリ、シンボリックリンク、マーカーの欠落・重複、部分的な登録、別名import等は書き換えずに拒否します。識別コメントは同梱元の目印で、真正性や未改変を証明するものではありません。

コマンドはテンプレートをコピー・削除せず、`cmd/server/features.go` のimportと登録行だけを変更します。登録ファイルがgofmt済みなら、変更後もgofmt済みに保ちます。将来コピー機能を追加する場合は、既存のコピー先を上書きしない保護が別途必要です。

## コマンドと設定

| コマンド | 動作 |
|---|---|
| `make run` | `go run ./cmd/server` で開発サーバーを起動 |
| `make build` | CGO無効で `bin/minimal-web-kit` を作成 |
| `make test` | `go test -race -v ./...` で全テストを実行 |
| `make verify` | `go mod verify` と `go vet ./...` を実行 |
| `make add-site` / `make remove-site` | Siteの登録を有効化・無効化 |
| `make add-cms` / `make remove-cms` | CMSの登録を有効化・無効化（無効化してもDBのデータは残る） |
| `make cms-setup` | CMSの初回登録用URLを表示（パスキーが未登録のときだけ）。本番は `minimal-web-kit cms-setup` |
| `go run ./cmd/server export-static <dir>` | Siteの公開ページ（`/`・`/privacy`）とCSS、セキュリティヘッダーの `_headers` を静的ファイルとして書き出す（Siteのみ有効な場合）。本番は `minimal-web-kit export-static <dir>` |
| `make cms-recover` | CMSのパスキーと管理者セッションを全削除し、登録用URLを再発行（確認あり） |
| `make` / `make all` | テスト後にビルド（サーバーは起動しない） |
| `make clean` | `bin/` と **`data/`（既定のDBを含む）を削除** |

| 環境変数 | 既定値・用途 |
|---|---|
| `PORT` | `8080`。`ADDR` 未設定時に `127.0.0.1:<PORT>` で待受 |
| `ADDR` | 未設定。設定時は待受アドレス全体を指定し、`PORT` より優先 |
| `DB_PATH` | `data/app.db`。SQLiteの保存先 |
| `SECURE_COOKIE` | `true` の場合のみCookieのSecure属性を有効化。既定は無効 |
| `CMS_ORIGIN` | `http://localhost:8080`。CMSを公開するオリジン（WebAuthnの検証と登録URLに使用）。httpsなら `SECURE_COOKIE=true` が必須 |
| `CMS_RP_ID` | `localhost`。WebAuthnのRP ID。`CMS_ORIGIN` のホストか、その上位のドメイン |
| `CMS_CORS_ORIGINS` | 未設定（許可なし）。`/api/` を別ドメインから読むことを許す接続元をカンマ区切りで指定。`*` は不可 |
| `SITE_URL` | 未設定。Siteを公開するURL（例: `https://example.com`）。設定するとcanonical・OGP・Xカード・`sitemap.xml` を出力する（絶対URLが必要なため） |
| `CONTACT_FORM_URL` | 未設定。Siteのお問い合わせリンク先（`https://` のみ有効。未設定時はボタン非表示） |

`make build` はテンプレート・CSS・JS・マイグレーションを埋め込んだ単一実行ファイルを作ります。対象OS・CPUに合わせたビルドが必要で、サイズはGoバージョン・構成・対象環境により変わります。SQLiteのデータはバイナリの外に保存されます。

本番公開には、HTTPS終端、Secure Cookie、待受・公開範囲、DBと保存ディレクトリの権限、バックアップ、ログの保持・削除方針等の設定が必要です。Linuxサーバー（VPS・EC2等）へのビルド・配置・systemd・リバースプロキシ・バックアップの手順例と、Siteだけを静的ホスティング（Cloudflare等）に置く手順は [docs/deploy.md](docs/deploy.md) を参照してください。公開範囲の変更や外部通信等は `AGENTS.md` の承認境界に従ってください。

## 依存モジュール

直接依存は `modernc.org/sqlite`（純Go版SQLiteドライバ）だけです。このドライバを通じて、次のモジュールが実行ファイルに組み込まれます（Linuxのamd64・arm64、CGO無効で確認）。

| モジュール | 用途 | ライセンス |
|---|---|---|
| `modernc.org/sqlite` | SQLiteドライバ（直接依存） | BSD系 |
| `modernc.org/libc` | C言語のSQLiteをGoに変換して動かすための標準ライブラリ相当 | BSD系 |
| `modernc.org/mathutil` | 数値計算の補助 | BSD系 |
| `modernc.org/memory` | メモリ管理 | BSD系 |
| `golang.org/x/sys` | OSの機能の呼び出し（Go公式） | BSD系 |
| `github.com/google/uuid` | UUIDの生成 | BSD系 |
| `github.com/dustin/go-humanize` | 数値の表示形式 | MIT |
| `github.com/remyoudompheng/bigfft` | 大きな数の乗算 | BSD系 |

ライセンスは各モジュールのLICENSEファイルの冒頭で分類したもので、正確な条件は各LICENSEを参照してください。`go.mod` にはこのほか、上記の環境では組み込まれない間接依存（`github.com/mattn/go-isatty`、`github.com/ncruces/go-strftime`）も記載されています。

`test/architecture_test.go` の `TestArchitecture_LinkedModules` が、組み込まれるモジュールをこの一覧と照合します。SQLiteドライバの更新等で増減した場合はテストが失敗するため、追加されたモジュールの用途・ライセンス・保守状況を確認してから、テストの一覧とこの表を更新してください。

## 推奨するAI開発ツールとスキル

Claude Code、Codex、Cursor、Antigravityのいずれかでリポジトリのルートを開き、同梱スキルを使って編集する方法を推奨します。ツールの導入・認証は別途必要です。

| ツール | リポジトリ内のスキル探索先 |
|---|---|
| Claude Code | `.claude/skills/<name>/SKILL.md` |
| Codex | `.agents/skills/<name>/SKILL.md` |
| Cursor | `.agents/skills/<name>/SKILL.md` |
| Antigravity | `.agents/skills/<name>/SKILL.md` |

| スキル | 用途 |
|---|---|
| `minimal-web-kit-design` | データ取得・依存・認証と権限・保持と削除・承認境界の設計 |
| `minimal-web-kit-implementation` | Go・SQLite・SSR・データ保護・HTML/CSS規約に沿った実装 |
| `minimal-web-kit-audit` | 付属テストと追加検証によるキットの規約との照合 |

一般的な要件定義・機能別実装・テストのスキルと併用し、このキット固有の判断と規約を補完します。正本は `skills/<name>/SKILL.md` で、探索先からは相対シンボリックリンクで参照します。Windows等でリンクが通常ファイルになった場合は、利用するツールの探索先に正本のフォルダをコピーし、更新時も同期してください。

最初に `AGENTS.md` を読み込ませてください。Claude Codeには `CLAUDE.md` からの参照もあります。依頼例：

> AGENTS.mdを読み、minimal-web-kit-designで変更を計画し、minimal-web-kit-implementationで実装、minimal-web-kit-auditで検証してください。

ツールのスキル一覧で3本が認識されていることを確認してください。表示されなければ再起動するか、`skills/<name>/SKILL.md` を明示して読ませてください。配置・形式は検査していますが、4ツールそれぞれでの実機動作は未検証です。

配置仕様の参照先：[Claude Code](https://code.claude.com/docs/en/skills)、[Codex](https://learn.chatgpt.com/docs/build-skills)、[Cursor](https://cursor.com/docs/skills)、[Antigravity](https://antigravity.google/docs/skills)。

## 自作機能（Feature）の追加

1. `features/<機能名>/` に機能を作成し、`features.Feature` の `Name`、`Init`、`RegisterRoutes` を実装します。必要なら `CustomRootFeature` の `HandleRoot` も実装します。複数のトップページ提供機能を登録すると、先に登録したものが使われます。
2. `cmd/server/features.go` にパッケージのimportと `r.Register(...)` を追記します。自作機能用の生成コマンドはありません。
3. DBを利用する機能は `Init` で必要な初期化を実装します。保存データの追加前に承認を得て、[取得・保存データ台帳](docs/data-inventory.md)と検査を更新します。
4. 入力の検査には部品の `internal/web/form`（`form.Text`・`form.Line`）を使います。動的機能では必要な操作に限ってセッションを発行し、OwnerIDによる分離、入力検証、CSRF保護、用途に応じた認証・認可を実装します。追加Featureの保護が自動的に完成するわけではありません。
5. データの寿命に合わせて、`owners` に紐づけるかを決めます。下記の「OwnerIDに紐づくデータの寿命」を確認してください。
6. 必要に応じて、次の任意のインターフェースを実装します（`features/feature.go`）。
   - `IsolatedRoutesFeature`：CoreのCSRF検査とセッション照合を通さない経路（Cookieを使わない公開ページ、独自認証の管理画面）。認証・CSRF・Origin検査は機能自身が行い、部品の `internal/web/guard`（`SameOrigin`・`CSRF`）を使います。
   - `PurgeFeature`：期限切れデータの削除。起動時と1時間ごとに呼ばれます。
   - `NavLinkFeature`：ナビゲーションへのリンク（例: CMSの「お知らせ」）。機能を外せばリンクも消えます。
   - `CommandFeature`：サーバーの実行ファイルのサブコマンド（例: `minimal-web-kit cms-setup`）。
7. `make test`、`make verify`、`make build` を実行し、画面も確認します。

### OwnerIDに紐づくデータの寿命

OwnerIDはブラウザ単位の無記名の識別子で、セッション発行時に作られます。所有者（`owners` の行）は次の場合に削除され、`ON DELETE CASCADE` で紐づけたFeatureのデータも一緒に削除されます。

- 利用者がセッション破棄（`/account/delete-all`）を実行したとき。
- 有効なセッションが1件も残っていないとき。最終アクセスから30日を過ぎたセッションは、起動時と1時間ごとの処理で削除されます。

このため、OwnerIDへのCASCADEは、ブラウザ単位の一時メモ・匿名ツールの作業データ等に向いています。CMSの記事や業務データのように、編集者がしばらくアクセスしなくても残すべきデータは、OwnerIDに紐づけないでください。管理者アカウント等を別に設計し、データの保持・削除方法を台帳に定めてください。

## ドキュメントと検査の範囲

開発時に作成する仕様・設計書は `docs/` にまとめます。[取得・保存データ台帳](docs/data-inventory.md)は、利用目的・保存先・保持期間・削除方法の正本（SoT）です。別途テーブル定義書を作る場合はDB構造の詳細をそちらで管理し、方針を重複させず相互参照します。現在は独立したテーブル定義書がなく、台帳に照合用のテーブル・列一覧と型等の補助情報を併記しています。

台帳テストはテーブル名・列名を実スキーマと双方向で照合します。型・NULL制約・外部キーや全保持・削除方針を検証するものではありません。Featureは有効化の状態にかかわらず、すべての `features/*/migration.sql` を適用して照合します。`Init` でこのファイル以外の方法でテーブルを作る機能は検査されないため、スキーマは `migration.sql` に書き、`Init` から同じファイルを適用してください。

`make` の各コマンドは台帳を自動生成・更新しません。承認された変更に合わせて開発者・AIが更新します。ログのカナリア検査も投入した経路についての検証で、あらゆる追加機能の非漏洩を保証するものではありません。

セッション破棄は稼働DBのOwnerと関連セッションを削除します。Featureのデータも連動削除するには、適切な外部キーと `ON DELETE CASCADE` 等の設計・検証が必要です。WAL・バックアップ・外部フォームまで即座に物理消去する保証ではありません。

## プロジェクト構成

```text
.
├── AGENTS.md / CLAUDE.md    # 開発規約とClaude Code向けの入口
├── .agents/skills/         # Codex・Cursor・Antigravity向けの参照
├── .claude/skills/         # Claude Code向けの参照
├── docs/
│   ├── cms-plan.md         # CMSテンプレートの実装計画
│   ├── deploy.md           # 本番デプロイの手順例（Linux）
│   ├── ui-guidelines.md    # 動きのあるUIの作り方（標準の要素 → htmx → Alpine.js等）
│   └── data-inventory.md   # 取得・保存データ台帳
├── LICENSE                # MIT License
├── SECURITY.md            # 脆弱性の報告窓口
├── Makefile               # 起動・ビルド・検査・Site/CMS切り替え・CMSの登録URL発行
├── cmd/
│   ├── server/            # 起動処理・Featureの登録・Featureのサブコマンド
│   └── kit/               # Site・CMS登録の切り替えCLIとテスト
├── internal/
│   ├── app/               # 設定・サーバー管理
│   ├── domain/            # OwnerID等のCoreドメイン型
│   ├── store/             # メモリ・SQLite・Coreマイグレーション
│   └── web/               # SSR・セッション・CSRF・ログ・CSS
│       ├── form/          # 部品: 入力の検査（文章・1行の値）
│       └── guard/         # 部品: 独立経路の同一オリジン確認とCSRF
├── features/
│   ├── site/              # LP・ホームページテンプレート
│   └── cms/               # お知らせのCMSテンプレート（パスキー認証・公開ページ・API）
├── skills/                # 3本のAI開発スキルの正本（各150行以内）
└── test/                  # 依存・テンプレート・台帳・ログ等の検査
```

## 脆弱性の報告

脆弱性を見つけた場合は、公開のIssueには書かず、[SECURITY.md](SECURITY.md) の手順に沿って非公開で報告してください。

## ライセンス

[MIT License](LICENSE)

概要（参考訳。正式な条件は英語の原文に従います）：このソフトウェアは、著作権表示と許諾表示を含めることを条件に、無償で、使用・複製・改変・結合・公開・頒布・再許諾・販売を含む制限の無い利用を許諾します。ソフトウェアは「現状のまま」提供され、商品性、特定の目的への適合性、権利の非侵害を含むいかなる保証もありません。作者・著作権者は、ソフトウェアやその利用に関して生じたいかなる請求・損害・その他の責任も負いません。
