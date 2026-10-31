# minimal-web-kit

個人情報の取得と依存関係を最小限に抑える、Webサイト・業務アプリの開発基盤です。Go製のひな形、同梱機能（LPテンプレートのSite、お知らせ機能のNews、パスキーの管理者認証、カタログ、お問い合わせ、固定ページ、自社システム間の共通認証、AIエージェント向けのMCP）、6本のAI開発スキルを同梱しています。規約は文書に書くだけでなく、テストで確かめます。依存や保存データ、ログ、画面の決まりを破る変更はテストが落ちるので、AIと作っても崩れに早く気づけます。

## 特徴と提供範囲

- **規約をテストで検査**：依存の許可リスト、データ台帳との突き合わせ、仕様とテストの対応、ログへの漏洩、画面の標準をテストで確かめます。範囲は「[ドキュメントと検査の範囲](#ドキュメントと検査の範囲)」にあります。安全や適合を保証するものではありません。
- **Go標準機能を中心とした構成**：`net/http`、`html/template`、`embed.FS`、`log/slog` を使用します。BaseとSite・管理者認証等の直接依存は純Go版SQLiteドライバ1件で、間接依存もあります（[一覧](#依存モジュール)）。任意の共通認証を登録したときだけ、OIDCのライブラリが組み込まれます。Node.js/npmや外部CSSフレームワークは使用しません。
- **機能ごとの分離**：自作機能は `features/<機能名>/` に配置します。Site・お知らせ機能の切り替えコマンドは登録ファイルだけを変更し、機能ディレクトリ内のファイルは変更しません。
- **取得データの最小化**：氏名や連絡先は既定で取得しません（お問い合わせ機能を有効にした場合だけ、送る人が任意で書くお名前・返信先を保存します）。必要な情報を追加するときは、利用目的・保存先・保持期間・削除方法を整理し、開発規約に沿って承認を得て台帳と検査を更新します。
- **セッションとデータ保護の基盤**：ランダムトークンとDBへのSHA-256ハッシュ保存、CSRF保護、セキュリティヘッダー、期限切れセッションの削除処理を備えています。OwnerIDはデータ分離の識別子であり、管理者認証や業務上の権限管理の代わりにはなりません。
- **自前CSSとHTMLによるUI**：CSSは `style.css` 1本です。JavaScriptは承認した画面だけで使用します。管理者認証では、管理者認証でパスキー（WebAuthn）を扱う `webauthn.js` 1本で、登録・ログイン・パスキー管理の画面だけで読み込みます。カタログの公開一覧では固定版htmxとcatalog.jsの2本を、カタログの項目の作成・編集画面ではタグ選択の部品 `combobox.js`（外部ライブラリなし）を、同一オリジンから読み込みます。高コントラスト、フォーカス表示、見出し・ランドマーク、スキップリンク等を備えています。JIS X 8341-3 / WCAG 2.1 AAを意識した設計ですが、完全適合を宣言するものではなく、作成したページごとの実画面・支援技術での検証が必要です。
- **アプリケーションログの最小化**：アクセスログはサーバー生成のリクエストID、既知のHTTPメソッド（その他は `OTHER`）、応答状況、処理時間に限定します。URLパス・クエリ・Cookie・IP・User-Agent・本文や、生のpanic値・エラー内容を記録しない実装です。追加機能やリバースプロキシ等のログは別途確認してください。
- **スキルと自動検査**：キット固有の設計・実装・監査を支えるスキルと、依存・テンプレート・データ台帳・ログ等のテストがあります。スキルはAIへの手順であり、すべての規約を機械的に強制するものではありません。

企業ホームページ、社内ツール、Webユーティリティ等の出発点として利用できます。依存の最小化をUIの豊かさより優先するため、状態を持つ部品が多い画面、リッチなエディタ、リアルタイムの共同編集、SPAのような画面遷移が必要な場合は、React・Vue等のフロントエンドフレームワーク（Next.js・Nuxt等）と、Tailwind等のCSSフレームワークの採用を検討してください（[UIの作り方の方針](docs/ui-guidelines.md)）。お知らせを管理する簡易CMS（公開ページ、読み取り専用のJSON API、記事の管理画面）と、管理画面にパスキーでログインする管理者認証も同梱しています。管理者認証はお知らせ機能以外の自作の管理画面にも使えます。管理者認証は複数のアカウントとロール3種類（システム管理者・サイト管理者・一般ユーザー）までで、機能ごとの細かい権限・画像のアップロード等は含みません。

## 起動方法

### 前提

- Git、make（以下のコマンド例はPOSIXシェル向け）
- Go 1.27.3以上（`go.mod` の指定に従い、GoコマンドをPATHに含める）

```bash
git clone https://github.com/hideshi/minimal-web-kit.git
cd minimal-web-kit
make run
```

`http://127.0.0.1:8080/` を開いてください。`make run` はフォアグラウンドで起動し、停止は `Ctrl+C` です。コードやテンプレートを変更したら、停止して再実行します。自動リロード機能はありません。

同じポートが使用中なら、既存サーバーを停止するか `PORT=8081 make run` のように別ポートを指定します。`ADDR` が設定されている場合は、`PORT` より優先されます。

### BaseとSite（LPテンプレート）の違い

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

本文は `features/site/views/index.html`、ヘッダー・フッターは `features/site/views/layout.html`、共通CSSは `internal/web/static/css/style.css` で編集します。GitHubへのリンク（ヒーロー、3ステップの直後、ヘッダーのナビの3か所）はこのキットの公開リポジトリを指します。別の製品サイトへ流用する際は、リンク先や紹介文も見直してください。トップを別のサイトに書き換えたら、`index.html` の先頭の印 `kit-landing` も消してください。`test/landing_test.go` の紹介ページの検査が止まります（テストを消す必要はありません）。

お問い合わせボタンのURLは `CONTACT_FORM_URL` 環境変数で、`https://` で始まるURLを設定します。未設定やhttps以外の値の場合はボタンを表示せず「お問い合わせ窓口は現在準備中です。」と表示し、起動時に警告ログを出します（設定値はログに記録しません）。ボタンの補足文は「Googleフォーム」なので、別のサービスを使う場合は `index.html` の文言も変更してください。検索結果やSNSのカードに出る説明文は `features/site/site.go` の `description`、カード用の画像（1200×630px）は `internal/web/static/img/ogp.png` です。`SITE_URL` を設定すると、canonical・OGP・Xカードのタグと `sitemap.xml` が有効になります。`robots.txt` は常に返します。お知らせ機能が有効なら、お知らせの記事は本文の冒頭を説明文にし、`og:type` を `article` にして、`sitemap.xml` に更新日時付きで載せます。アイコンは `internal/web/static/img/` の `favicon.svg`・`favicon-32.png`・`apple-touch-icon.png` です。同梱の画像とアイコンはこのキットの紹介用なので、別のサイトでは差し替えてください。

Cookieも利用者ごとの内容も持たない公開ページ（Siteの `/`・`/privacy`、`robots.txt`、`sitemap.xml`、お知らせ機能の `/news`・`/api/news`）は、60秒のキャッシュを許可します。お知らせの公開・更新が見えるまで、最大でその時間だけ遅れることがあります。`/static/` のCSS・アイコンは、同梱のレイアウトが中身の版付きのURL（`/static/css/style.css?v=…`）で読み込み、版が一致すれば1年のキャッシュを許可します。CSSを変えるとURLが変わるため、更新はすぐ反映されます。版の無いURL（OGPの画像、自作のレイアウトで直接書いた参照）には10分を指定します。前段のCDNがこの期間を引き上げることがあります（[配備の手順](docs/deploy.md)の6節）。セッションを扱う画面と管理画面は、これまでどおりキャッシュさせません。

`/privacy` の末尾の「免責事項」（Site有効時だけ表示）は、このキットを紹介するサイトを前提にした例です。運営者と掲載内容に合わせて書き換えてください。公開前に実際のフォームURLを設定し、フォームの入力項目・利用目的・保持期間・削除窓口・外部サービスの扱いに合わせて `/privacy` の文面を編集してください。外部フォームの情報は、サイト内のセッション削除では消えません。お問い合わせの行き先があるときは、`/privacy` に「運営者と、開示等の請求・苦情の窓口」の節が載ります。運営者の名称を載せる場合も含め、実際の窓口と手続に合わせて書き換えてください（`minimal-web-kit-privacy` スキル）。

### 管理者認証（パスキー）

管理画面を持つ機能のための、ロールを持つ複数のアカウントのログインを提供する同梱機能です。お知らせ機能を使わずに、自作の業務アプリ等の管理画面にも使えます。

```bash
make add-auth        # お知らせ機能を使う場合は make add-news で一緒に有効になる
make run
# 別のターミナルで、初回登録用のURLを表示
make auth-setup
```

表示された `http://localhost:8080/admin/setup#token=...` を15分以内に開いてパスキーを登録し、`http://localhost:8080/admin/login` からログインします。最初のアカウントは、ロールがシステム管理者で呼び名が「管理者」です。WebAuthnはIPアドレスでは動かないため、`127.0.0.1` ではなく `localhost` で開いてください。

2人目からは、システム管理者が「アカウントの管理」でアカウント（呼び名とロール）を作り、表示された登録URL（24時間有効・1回限り）を本人に渡します。このキットはメールを送りません。URLはチャット等で本人だけに渡してください。

| ロール | できること |
|---|---|
| システム管理者（`admin`） | 各機能の管理画面、自分のパスキーの管理、アカウントの追加・ロール変更・停止・削除・登録URLの発行 |
| サイト管理者（`member`） | 各機能の管理画面、自分のパスキーの管理 |
| 一般ユーザー（`user`） | `AccountPage`・`AccountForm` で開いた画面（文書の機能）、自分のパスキーの管理。公開サイトの編集はできない |

| 経路 | 内容 |
|---|---|
| `/admin/login`、`/admin/setup` | パスキーでのログイン、初回登録・復旧時の登録 |
| `/admin/passkeys` | 自分のパスキーの追加・名前変更・削除 |
| `/admin/accounts` | アカウントの管理（システム管理者のみ） |
| `/admin` | 管理画面の入り口（他の機能の管理画面、無ければパスキーの管理へ移る） |

- **ログインはパスキーだけ**です。パスワードはありません。既定では、複製を検出できない同期型のパスキー（スマートフォン、Googleパスワードマネージャー、iCloudキーチェーン等）は拒否し、Windows Helloやセキュリティキー等、端末に固定された認証器だけ（認証器の申告で判定し、機種は証明しません）を登録できます。Windows Helloの指紋認証で登録・ログインを確認しています。既定では、ChromeOSの指紋認証は同期型として拒否されることを確認しています。MacのTouch IDも、iCloudキーチェーンの同期型のため使えない見込みです。
- `AUTH_ALLOW_SYNCED_PASSKEYS=true` にすると、同期型のパスキーも登録・ログインできます。端末を失っても他の端末から入れ、ChromeOSの指紋認証やスマートフォンも使えます（許可した設定で、Chromebookでの登録・ログインを確認しています）。その代わり、パスキーの安全は同期の提供者のアカウント（Googleアカウント等）の守りに頼り、複製を署名カウンタで検出できません。端末ごとに別のカウンタを持つ実装では、正規の複数端末の利用が複製の疑いとして無効化されることがあり、その場合は別のパスキーで入って削除・登録し直します。
- 認証器は2つ以上の登録を推奨します。サイト管理者や他のシステム管理者がパスキーを失った場合は、システム管理者がアカウントの詳細で登録URLを発行し直します（紛失・盗難時は、既存のパスキーを削除して発行）。
- 有効なシステム管理者（停止中でなく、有効なパスキーを持つシステム管理者）が0人になる操作（最後のシステム管理者の降格・停止・削除・鍵のリセット）は拒否します。自分自身のロール変更・停止・削除もできません。システム管理者を2人以上にしておくことを推奨します。
- システム管理者全員がログインできなくなった場合は、サーバーで `make auth-recover ARGS="-account <ID>"`（本番は `minimal-web-kit auth-recover -account <ID>`）を実行します。全アカウントのパスキーとセッションを削除し、指定したシステム管理者の登録URLを表示します。1人分だけ初期化する場合は `-only <ID>` を使います。アカウントと、他の機能のデータ（お知らせの記事等）は残ります。`-account` を省くとシステム管理者の一覧を表示します。
- ロール変更・停止・削除の操作はログ（アカウント番号のみ。呼び名は記録しない）に残しますが、DBに操作の記録は持ちません。権限はロール2種類だけで、機能ごとの細かい権限はありません。
- アカウント導入前の版のDBでは起動を止めます。`make auth-recover ARGS="-reset-schema"` で管理者認証の表だけを作り直し、パスキーを登録し直してください。他の機能のデータは残ります。
- 自作機能の管理画面は、`features.AdminConsumer` を実装して管理者認証の口（`features.Admin`）を受け取り、`Page`（画面）・`Form`（送信）で守ります。この2つは一般ユーザーを403で止めます。一般ユーザーにも開く画面だけ `AccountPage`・`AccountForm` を使い、ナビのリンクに `AllRoles` を付けます。管理画面のナビへのリンクは `features.AdminNavFeature` で足します。画面は `admin.Layout()` のレイアウトで描きます。システム管理者だけに許す画面は `AdminPage`・`AdminForm` で守ります（ログイン中のアカウントは `AdminSession` の `AccountID`・`Role`・`IsAdmin` で分かります）。
- 本番では `AUTH_ORIGIN`・`AUTH_RP_ID` を公開するURLに合わせ、`SECURE_COOKIE=true` にします。手順は [docs/deploy.md](docs/deploy.md) を参照してください。保存するデータは[取得・保存データ台帳](docs/data-inventory.md)の4節にあります。用語・状態・不変条件・権限の正本は [docs/domain/auth.md](docs/domain/auth.md)（ドメインモデル）です。
- お知らせ機能が有効な間は `make remove-auth` で外せません。登録を外しても、DBのデータは残ります。

### 共通認証（自社の別システムでのログイン）

管理者認証のアカウントとパスキーで、自社の別システムにログインできるようにします。アカウントを持つシステムA（認証基盤）と、ログインを任せるシステムB（連携先）で、それぞれ機能を登録します。AとBは別のドメインで構いません。Aで利用者を停止すると、Bのログイン済みの人も次の要求から使えなくなります。

```bash
# A（認証基盤）: 管理者認証を、提供機能付きに置き換える。アカウントとパスキーはそのまま使える
make add-oidcop

# B（連携先）: 管理者認証を、Aでのログインに置き換える。お知らせ等の管理画面はそのまま動く
make add-federatedauth
```

Aの設定（`AUTH_ORIGIN` はHTTPSのオリジンで、これが issuer になります）:

```bash
openssl ecparam -name prime256v1 -genkey -noout -out oidc-signing.pem   # 署名鍵（P-256）
head -c 32 /dev/urandom > oidc-crypto.key                               # コードの暗号化鍵（32バイト）
chmod 600 oidc-signing.pem oidc-crypto.key                              # 所有者だけが読めないと起動しない
export OIDC_SIGNING_KEY_FILE=/path/oidc-signing.pem OIDC_CRYPTO_KEY_FILE=/path/oidc-crypto.key

# 連携先を登録する。client_secret は1度だけ表示する。出力をログや共有の画面に残さない
minimal-web-kit oidc-client -create -label "業務アプリB" -redirect https://b.example.com/admin/sso/callback
# 利用を許可する。sub（連携先に渡す識別子）を表示する
minimal-web-kit oidc-grant -client <client_id> -account <アカウントのID>
```

Bの設定（`AUTH_ORIGIN` はBのHTTPSのオリジン）:

```bash
export OIDC_ISSUER=https://a.example.com OIDC_CLIENT_ID=<client_id>
export OIDC_CLIENT_SECRET_FILE=/path/client-secret   # 所有者だけが読めるファイル
# 利用者は最初の1回のログインでサイト管理者になる。その後でシステム管理者を指定する
minimal-web-kit federated-account -sub <sub> -role admin
```

- Aの役割のシステム管理者が、そのままBのシステム管理者になることはありません。Bの権限はBのコマンドで決めます。
- Bは保護された要求のたびにAへ確認します。Aが止まっている間、Bの保護された機能は503になります（ログアウトはできます）。可用性をAに依存することを前提に使ってください。
- 連携先を止めている間も、Bは認証基盤の障害と区別できず503になります。`-rotate -compromised` の後も、Bの秘密ファイルを更新するまで全員503です。
- リバースプロキシのアクセスログには、Bの `/admin/sso/callback` のコードとstate、Aの `/admin/oidc/authorize` のstate・nonceが残ります。この2つの経路はクエリを記録しない設定にしてください。
- 秘密値の更新は `oidc-client -client <ID> -rotate`（旧値は24時間有効）、漏洩時は `-rotate -compromised` です。連携先の停止は `-disable`、利用許可の取り消しは `oidc-grant -revoke` です。どれも、戻しても古いログインは復活しません。
- 署名鍵の更新は、新しい鍵に替え、旧公開鍵をJWKSのファイル（`OIDC_OLD_JWKS_FILE`）で7分以上公開し続けます。漏洩時とバックアップからの復元の後は `oidc-revoke-all -yes` を実行します。
- 対応するのはサーバー側でセッションを持つ自社のWebアプリだけです。SPA・モバイル・refresh token・第三者への開放は対象外です。キット以外のシステムをつなぐ場合は、OIDCに加えて、要求ごとの照会（`POST /oidc/check`）の実装が必要です。
- 仕様と不変条件は [docs/domain/federatedauth.md](docs/domain/federatedauth.md)、保存内容は[データ台帳](docs/data-inventory.md)の4節と9節にあります。

### お知らせ機能

お知らせ1種類を管理する同梱機能です。サーバーがHTMLを返す使い方（`/news`）と、別のフロントエンドがJSONを読む使い方（`/api/news`）の両方に対応します。Siteとは別に有効化でき、同時に使うとSiteのナビに「お知らせ」のリンクが出ます。管理画面へのログインには管理者認証を使い、`make add-news` で一緒に有効になります。

| 経路 | 内容 |
|---|---|
| `/news`、`/news/{id}` | 公開中のお知らせ。Cookieを発行・照合しません |
| `/api/news`、`/api/news/{id}` | 公開中のお知らせのJSON（`limit`・`offset`）。別ドメインからの読み取り（CORS）は `NEWS_CORS_ORIGINS` で許可した接続元だけで、既定は許可なしです |
| `/admin/news` | お知らせの作成・編集・公開・予約公開・削除 |

- 本文はプレーンテキスト（HTMLは文字として表示）で、画像のアップロードはありません。
- 公開日時に未来の日時を指定して公開すると、その日時まで公開ページとAPIに表示しません（予約公開）。時刻はサーバーのタイムゾーン（`TZ`）で扱います。
- 記事は匿名のOwnerIDに紐づけず、30日の自動削除の対象外です。保存するデータは[取得・保存データ台帳](docs/data-inventory.md)の5節にあります。用語・状態・不変条件・権限の正本は [docs/domain/news.md](docs/domain/news.md)（ドメインモデル）、設計の経緯は [docs/plans/cms-plan.md](docs/plans/cms-plan.md) にあります。
- `make remove-news` で登録を外しても、DBのデータは残ります。
- 旧称は「CMS」（`features/cms`）です。旧版から更新する場合は、`cmd/server/features.go` の `minimal-web-kit/features/cms` を `minimal-web-kit/features/news` に、`cms.NewFeature` を `news.NewFeature` に書き換えます。設定の `CMS_CORS_ORIGINS` は `NEWS_CORS_ORIGINS` に改名しました（旧名が残っていると起動を止めます）。表 `cms_posts` は起動時に、記事とIDの採番を残したまま `news_posts` に名前を変えます。管理画面のURLは `/admin/posts` から `/admin/news` に変わりました。

### カタログ機能

`make add-catalog` で有効化します（管理者認証も有効になります）。`make remove-catalog` は登録だけを外し、保存データは保持します。Newsとは独立し、`/news` の仕様は変わりません。

- 公開: `/catalog` と `/catalog/{id}`。項目のタイトル・本文、任意のカテゴリ1つとタグ20個までを管理します。画像・価格・在庫・予約公開・JSON API・静的書き出しは含みません。
- 管理: `/admin/catalog`。システム管理者・サイト管理者が同じ権限で下書き作成・編集・公開・非公開・削除と分類管理を行えます。認証は既存のパスキー認証を使います。
- 公開一覧はキーワード・カテゴリID・タグID（すべてに一致）で絞り込み、20件ずつページ送りします。検索条件はURLとブラウザ履歴に残るので、秘密を入力しないでください。公開閲覧でCookieを扱わず、検索語や閲覧履歴はサーバーに保存しません。
- 公開一覧だけhtmx 2.0.11とcatalog.jsを読み込みます。JavaScriptなしでもリンク・GETフォームで検索と移動ができ、管理操作も通常フォームです。項目の作成・編集画面のタグ選択は、Coreのコンボボックス部品で入力しながら絞り込めます（JSなしではチェックボックスの一覧）。Alpine.jsは比較試作（`experiment/catalog-tags` ブランチ、[比較計画と結果](docs/plans/catalog-ui-comparison-plan.md)）で評価し、採用していません。
- 公開・非公開の反映遅れを避けるため、カタログでは公開キャッシュを使いません。全文LIKE検索には走査コストがあり、大規模な検索基盤は提供しません。
- カタログまたはNewsが有効な間はAuthを外せません。作者IDや操作履歴は保存せず、データは明示削除まで保持します。

仕様と不変条件は [domain/catalog.md](docs/domain/catalog.md)、保存内容は[データ台帳](docs/data-inventory.md)の6節、ブラウザ依存の記録は [カタログのJavaScript](features/catalog/static/README.md) を参照してください。独自レイアウトで強化UIを使う場合は、head内に空の `{{block "page_head" .}}{{end}}` を用意します（Siteと既定レイアウトには用意済み）。

### お問い合わせ機能

`make add-contact` で有効化します（管理者認証も有効になります）。`make remove-contact` は登録だけを外し、保存データは保持します。メールは送らず、届いた内容は管理画面の受信箱で読みます。

| 経路 | 内容 |
|---|---|
| `/contact` | お問い合わせのフォーム。内容（必須）・お名前（任意）・返信先（任意）。Cookieを発行・照合せず、IPアドレス・User-Agentを保存しません。JavaScriptは使いません |
| `/admin/contact` | 受信箱。**システム管理者だけ**が見られます（サイト管理者にはナビのリンクも出ません）。既定で未読と対応中を表示します。未読・対応中・完了・スパムの状態、運営者のメモ（2,000文字まで）、1件ずつとスパムのまとめての削除 |
| `/admin/contact/all` | 全件の受信箱 |

- 受付から `CONTACT_RETENTION_DAYS`（既定90日）で自動で削除します。対応中のものだけ、最後の更新（メモの保存か、対応中への変更）から同じ日数で削除し、受付から `CONTACT_ACTIVE_MAX_DAYS`（既定365日）を超えて残しません。削除前のバックアップの複製には、その保持期間のあいだ残るため、プライバシーポリシーには合わせた最長の期間を書いてください。
- メモは対応の引き継ぎ用です。送った人の情報を含みうるため、保存項目としてプライバシーポリシーに載ります。担当者の割り当てや、やり取りの履歴はありません。
- スパム対策は、IPアドレスと外部のCAPTCHAを使わずに、見えない入力欄、署名つきのフォームの値（3秒未満・1時間超え・使用済みを判定）、URLは2つまで、全体の受付上限（`CONTACT_HOURLY_LIMIT`・`CONTACT_DAILY_LIMIT`・`CONTACT_UNREAD_LIMIT`。保存した件数で数える）を組み合わせます。人が手で書く宣伝は防げません。判定の結果は固定の分類（`result=saved`・`honeypot`・`too_fast` 等）でログに出すので、効き目を確かめられます。連打は前段（Cloudflare等）のレート制限で抑えます（[デプロイ手順](docs/deploy.md)）。
- 送信の前に、利用目的・保持期間とプライバシーポリシーへのリンクを表示します。同意のチェック欄はありません（取得に同意は要らず、利用目的の明示が必要なため。法的な判断は運営者が行ってください）。
- Siteと一緒に使うと、LPのお問い合わせのボタンが `/contact` を指します。`CONTACT_FORM_URL`（外部のフォーム）と同時に有効にすると、起動を止めます。
- 仕様と不変条件は [domain/contact.md](docs/domain/contact.md)、保存内容は[データ台帳](docs/data-inventory.md)の7節、設計の経緯は [contact-plan.md](docs/plans/contact-plan.md) にあります。

### MCP（AIエージェント）

`make add-mcp` で有効化します（管理者認証も有効になります）。AIエージェントが、許可した機能の内容を読み、下書きや編集を行えます。お知らせ・カタログ・固定ページに対応し、別の機能も `features.MCPFeature` を実装すれば足せます。

| 経路 | 内容 |
|---|---|
| `POST /mcp` | MCP（`2026-07-28`、JSONの応答のみ、セッションなし）。Bearerトークンで認証し、Cookieは使いません |
| `/admin/mcp` | 機能ごとの有効・無効。既定は全て無効で、変更はシステム管理者だけ |
| `/admin/agent/*`・`/agent/token` | OAuthの認可（PKCE必須、公開クライアント）。パスキーでログインして同意します |

- 読み取りは確認なしで、下書きも読めます。作成・編集・公開・非公開は、実行の前にクライアントで利用者の確認を求めます。確認に対応しないクライアントでは実行しません。
- 確認の手順は強制しますが、人の操作かは証明できません。クライアントを自動応答に設定した場合は、その利用者の責任です。
- 初版に削除とタグの新規作成はありません。全文を確認に出せない大きな変更（4000文字を超える確認）は拒否するので、管理画面を使います。
- トークンの期限は発行から8時間で固定で、切れたらクライアントから認可し直します。アカウントの停止などは次の要求から効きます。
- クライアントは事前に登録します。`/admin/mcp` の「接続の追加」で名前を入れると、追加したシステム管理者に利用が許可され、Claude Codeに登録するコマンドが表示されます。同じ画面で、許可するアカウントの変更と接続の停止ができます。
- 管理画面に入れないときのために、コマンド（`minimal-web-kit agent-client`）も残しています。

使い方:

1. `/admin/mcp` で、AIエージェントに使わせる機能を有効にします。
2. 同じ画面の「接続の追加」で名前を入れて追加します。
3. 接続の詳細に出るコマンドを、手元で1回だけ実行します（`claude mcp add ...`）。ほかの人も使うなら、詳細の画面で許可してから、同じコマンドを渡します。
4. Claude Codeの `/mcp` で接続を選び、`Authenticate` を押します。ブラウザでログインして「許可する」を押します。
5. 書き込みのときは、Claude Codeの確認に答えます。

トークンが切れたら、4をやり直します。コマンドの実行は要りません。機能を有効にしてもツールが見えないときは、`/mcp` から接続し直します。

- 通常の管理者認証と共通認証の提供機能で動き、OIDCのライブラリは要りません。共通認証の利用機能（`make add-federatedauth`）とは組み合わせられません。
- 仕様と不変条件は [domain/mcp.md](docs/domain/mcp.md)、保存内容は[データ台帳](docs/data-inventory.md)の4節と10節にあります。

### 固定ページ

`make add-pages` で有効化します（管理者認証も有効になります）。`make remove-pages` は登録だけを外し、保存データは保持します。会社概要・採用情報などのページを、管理画面で `/about` のような1階層のURLで作ります。

| 経路 | 内容 |
|---|---|
| `/{スラッグ}` | 公開中のページ。下書き・存在しないページはSiteの404。Cookieを発行・照合せず、公開キャッシュも使いません |
| `/admin/pages` | 一覧・作成・編集・プレビュー（保存しない）・公開・削除。システム管理者とサイト管理者 |

- 本文は書き方を絞ったMarkdown（見出し2段、段落、箇条書き、番号付き、表、リンク、強調）で、キットのコードで読みます（[書き方の仕様](docs/richtext.md)）。HTMLは書けず、リンクは `https://` とサイト内のパスだけです。画像・添付・版の履歴はありません。
- スラッグは英小文字・数字・ハイフンで2〜40文字です。`/privacy` や `/news` など、Coreと他の機能が使う経路と同じものは使えません。公開ページは「どの経路にも当たらなかった要求」だけを受けるため、既存の経路は取られません。
- 「ナビに出す順」を決めた公開中のページは、Siteのナビと `sitemap.xml` に載ります（再起動は不要）。ナビは公開キャッシュのある画面（`/` 等）では最大60秒遅れて見えます。
- スラッグを変えると、前のURLは404になります（転送はありません）。固定ページを有効にすると、静的書き出し（`export-static`）は使えません。
- 仕様と不変条件は [domain/pages.md](docs/domain/pages.md)、保存内容は[データ台帳](docs/data-inventory.md)の8節、設計の経緯は [pages-plan.md](docs/plans/pages-plan.md) にあります。

### 切り替えコマンドの保護範囲

`features/site`・`features/news`・`features/auth`・`features/catalog`・`features/federatedauth` は同梱機能用の予約ディレクトリです。自作機能には別名を使用してください。コマンドは `site.go`・`news.go`・`auth.go`・`catalog.go` 冒頭の識別コメント（例: `// minimal-web-kit: bundled-feature site`）と登録ファイルの構造を確認します。識別できない同名ディレクトリ、シンボリックリンク、マーカーの欠落・重複、部分的な登録、別名import等は書き換えずに拒否します。識別コメントは同梱元の目印で、真正性や未改変を証明するものではありません。

コマンドはテンプレートをコピー・削除せず、`cmd/server/features.go` のimportと登録行だけを変更します。登録ファイルがgofmt済みなら、変更後もgofmt済みに保ちます。将来コピー機能を追加する場合は、既存のコピー先を上書きしない保護が別途必要です。

## コマンドと設定

| コマンド | 動作 |
|---|---|
| `make run` | `go run ./cmd/server` で開発サーバーを起動 |
| `make build` | CGO無効で `bin/minimal-web-kit` を作成 |
| `make test` | `go test -race -v ./...` で全テストを実行 |
| `make verify` | `go mod verify` と `go vet ./...` を実行 |
| `make hooks` | プッシュの前に、Goが古ければ警告する（止めない。クローンごとに1回） |
| `make vuln` | Go本体と依存の既知の脆弱性を調べる（`govulncheck`。本体のソースと、`test/browser` のテストの実行ファイルを見る。SQLite本体とhtmxは見えないので、手順は `minimal-web-kit-vuln`） |
| `make test-ui` | 画面部品をChrome・Chromiumで操作して確かめ、主な管理画面を `test/browser/shots/` に撮る（`test/browser/` の別モジュール。chromedpを使い、キット本体の依存には含めない。Chromeが無ければ飛ばす。`CHROME_PATH` で指定可） |
| `make add-site` / `make remove-site` | Siteの登録を有効化・無効化 |
| `make add-auth` / `make remove-auth` | 管理者認証（パスキー）の登録を有効化・無効化（お知らせ・カタログ・お問い合わせ・固定ページが有効な間は無効化できない） |
| `make add-catalog` / `make remove-catalog` | カタログの登録を有効化・無効化（Authも有効化。データは保持） |
| `make add-contact` / `make remove-contact` | お問い合わせの登録を有効化・無効化（Authも有効化。データは保持） |
| `make add-pages` / `make remove-pages` | 固定ページの登録を有効化・無効化（Authも有効化。データは保持） |
| `make add-mcp` / `make remove-mcp` | MCPサーバーの登録を有効化・無効化（Authも有効化。設定は保持）。機能ごとの有効化は `/admin/mcp` |
| `make add-oidcop` / `make remove-oidcop` | 共通認証の提供機能。管理者認証を置き換え、外すと通常の管理者認証に戻る（連携の失効の状態は残る） |
| `make add-federatedauth` / `make remove-federatedauth` | 共通認証の利用機能。管理者認証を置き換える（お知らせ等が有効な間は無効化できない） |
| `make add-news` / `make remove-news` | お知らせ機能の登録を有効化・無効化（有効化すると管理者認証も有効になる。無効化してもDBのデータは残る） |
| `make auth-setup` | 最初のシステム管理者のパスキーの登録用URLを表示（有効なパスキーが1件も無いときだけ）。本番は `minimal-web-kit auth-setup` |
| `go run ./cmd/server export-static <dir>` | Siteの公開ページ（`/`・`/privacy`）とCSS、セキュリティヘッダーの `_headers` を静的ファイルとして書き出す（Siteのみ有効な場合）。本番は `minimal-web-kit export-static <dir>` |
| `make auth-recover ARGS="-account <ID>"` | 全アカウントのパスキーとセッションを削除し、指定したシステム管理者の登録用URLを再発行（確認あり）。`-only <ID>` で1人分だけ、`-reset-schema` で古い形式の表を作り直す |
| `minimal-web-kit backup <file>` | 稼働中のDBの整合した複製を作り、検査する（`sqlite3` コマンドは要らない。上書きしない。手元では `go run ./cmd/server backup <file>`） |
| `minimal-web-kit verify-backup <file>` | 複製が壊れていないことを確かめ、表ごとの行数を表示する（中身は表示しない） |
| `make` / `make all` | テスト後にビルド（サーバーは起動しない） |
| `make clean` | `bin/` と **`data/`（既定のDBを含む）を削除** |

| 環境変数 | 既定値・用途 |
|---|---|
| `PORT` | `8080`。`ADDR` 未設定時に `127.0.0.1:<PORT>` で待受 |
| `ADDR` | 未設定。設定時は待受アドレス全体を指定し、`PORT` より優先 |
| `DB_PATH` | `data/app.db`。SQLiteの保存先 |
| `SECURE_COOKIE` | `true` の場合のみCookieのSecure属性を有効化。既定は無効 |
| `AUTH_ORIGIN` | `http://localhost:8080`。管理画面を公開するオリジン（WebAuthnの検証と登録URLに使用）。httpは `localhost` だけ。httpsなら `SECURE_COOKIE=true` が必須 |
| `AUTH_RP_ID` | `localhost`。WebAuthnのRP ID。`AUTH_ORIGIN` のホストか、その上位のドメイン |
| `AUTH_ALLOW_SYNCED_PASSKEYS` | 無効。`true` のときだけ同期型のパスキーを受け入れる。`true`・`false`・空以外の値は起動時にエラー |
| `NEWS_CORS_ORIGINS` | 未設定（許可なし）。`/api/` を別ドメインから読むことを許す接続元をカンマ区切りで指定。`*` は不可 |
| `SITE_URL` | 未設定。Siteを公開するURL（例: `https://example.com`）。設定するとcanonical・OGP・Xカード・`sitemap.xml` を出力する（絶対URLが必要なため） |
| `CONTACT_FORM_URL` | 未設定。Siteのお問い合わせリンク先（外部のフォーム。`https://` のみ有効。未設定時はボタン非表示）。お問い合わせ機能と同時に有効にすると起動時にエラー |
| `CONTACT_RETENTION_DAYS` | 90。お問い合わせの保持日数（1〜365。範囲外・数字以外は起動時にエラー） |
| `CONTACT_ACTIVE_MAX_DAYS` | 365。対応中のお問い合わせを受付から残す上限の日数（`CONTACT_RETENTION_DAYS` 以上、365以下。同じ値にすると延長は効かない。範囲外は起動時にエラー） |
| `CONTACT_HOURLY_LIMIT` / `CONTACT_DAILY_LIMIT` / `CONTACT_UNREAD_LIMIT` | 20 / 100 / 500。お問い合わせの全体の受付上限（保存した件数で数える） |

`make build` はテンプレート・CSS・JS・マイグレーションを埋め込んだ単一実行ファイルを作ります。対象OS・CPUに合わせたビルドが必要で、サイズはGoバージョン・構成・対象環境により変わります。SQLiteのデータはバイナリの外に保存されます。

データベースはSQLiteだけを対象にします。小さな企業・団体が1台のサーバーで動かす前提で、MySQL・PostgreSQLへの対応、複数台の構成、自動の切り替えは提供しません。必要な規模なら、キットを使わない構成を検討してください。障害には、毎日サーバーの外へ複製を送り、そこから戻すことで備えます。毎日送っていれば、1台の障害で失うのは最大1日分です（[バックアップの手順](docs/deploy.md)の8節）。

本番公開には、HTTPS終端、Secure Cookie、待受・公開範囲、DBと保存ディレクトリの権限、バックアップ、ログの保持・削除方針等の設定が必要です。Linuxサーバー（VPS・EC2等）へのビルド・配置・systemd・リバースプロキシ・バックアップの手順例と、Siteだけを静的ホスティング（Cloudflare等）に置く手順は [docs/deploy.md](docs/deploy.md) を参照してください。公開範囲の変更や外部通信等は `AGENTS.md` の承認境界に従ってください。

## 依存モジュール

通常の構成（共通認証を登録しない構成）に組み込まれる直接依存は `modernc.org/sqlite`（純Go版SQLiteドライバ）だけです。このドライバを通じて、次のモジュールが実行ファイルに組み込まれます（Linuxのamd64・arm64、CGO無効で確認）。

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

共通認証の機能を登録したときだけ、次のモジュールも組み込まれます。`go.mod` の直接依存には載りますが、組み込まれるかは登録したかで決まり、BaseとSite・通常の管理者認証の実行ファイルには入りません（`TestArchitecture_FederationDependenciesStayOptional`）。実行ファイルの大きさと外部モジュールの数は、Base 約13.9MB・8個、管理者認証 約14.2MB・8個、提供機能 約16.7MB・26個、利用機能 約14.9MB・11個です（Linux amd64、`make build` 相当、2026-10-30）。

| モジュール | 用途 | 組み込まれる構成 | ライセンス |
|---|---|---|---|
| `github.com/zitadel/oidc/v3` | OIDCの提供（直接依存） | 提供機能 | Apache-2.0 |
| `github.com/coreos/go-oidc/v3` | IDトークンの検証（直接依存） | 利用機能 | Apache-2.0 |
| `golang.org/x/oauth2` | コードの交換（直接依存） | 提供機能・利用機能 | BSD系 |
| `github.com/go-jose/go-jose/v4` | JWTの署名と検証（直接依存。脆弱性の修正版に固定） | 提供機能・利用機能 | Apache-2.0 |
| `github.com/go-chi/chi/v5`・`github.com/rs/cors`・`github.com/gorilla/securecookie`・`github.com/zitadel/schema`・`github.com/muhlemmer/httpforwarded`・`github.com/bmatcuk/doublestar/v4`・`github.com/cespare/xxhash/v2` | OIDCのライブラリの内部で使う。公開の経路には使わない | 提供機能 | MIT・BSD系 |
| `github.com/muhlemmer/gu` | 同上 | 提供機能 | Unlicense |
| `go.opentelemetry.io/otel` 一式・`go.opentelemetry.io/auto/sdk`・`github.com/go-logr/logr`・`github.com/go-logr/stdr` | 同上。計測のAPIだけで、送信先を設定しないので外部へは送らない | 提供機能 | Apache-2.0 |
| `golang.org/x/text` | 同上 | 提供機能 | BSD系 |

ライセンスは各モジュールのLICENSEファイルの冒頭で分類したもので、正確な条件は各LICENSEを参照してください。`go.mod` にはこのほか、上記の環境では組み込まれない間接依存（`github.com/mattn/go-isatty`、`github.com/ncruces/go-strftime`）も記載されています。

ブラウザテスト（`make test-ui`）の `test/browser/` は独立したモジュールで、`github.com/chromedp/chromedp`（MIT）と、同じ開発元の `github.com/chromedp/cdproto`（MIT）を使います。開発時のテストだけで使い、キット本体の `go.mod` と実行ファイルには含まれません。

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
| `minimal-web-kit-deploy` | 個別のアプリの本番環境の手順書の作成と、リリース・戻し方の進め方 |
| `minimal-web-kit-vuln` | Go本体・依存・同梱ファイルの既知の脆弱性の点検と版上げ |
| `minimal-web-kit-privacy` | 有効な機能・保存データ・インフラ・外部サービスからのプライバシーポリシーの作成と見直し（法的な判断は運営者が行う） |

一般的な要件定義・機能別実装・テストのスキルと併用し、このキット固有の判断と規約を補完します。正本は `skills/<name>/SKILL.md` で、探索先からは相対シンボリックリンクで参照します。Windows等でリンクが通常ファイルになった場合は、利用するツールの探索先に正本のフォルダをコピーし、更新時も同期してください。

最初に `AGENTS.md` を読み込ませてください。Claude Codeには `CLAUDE.md` からの参照もあります。依頼例：

> AGENTS.mdを読み、minimal-web-kit-designで変更を計画し、minimal-web-kit-implementationで実装、minimal-web-kit-auditで検証してください。

ツールのスキル一覧で6本が認識されていることを確認してください。表示されなければ再起動するか、`skills/<name>/SKILL.md` を明示して読ませてください。配置・形式は検査していますが、4ツールそれぞれでの実機動作は未検証です。

配置仕様の参照先：[Claude Code](https://code.claude.com/docs/en/skills)、[Codex](https://learn.chatgpt.com/docs/build-skills)、[Cursor](https://cursor.com/docs/skills)、[Antigravity](https://antigravity.google/docs/skills)。

## 自作機能（Feature）の追加

入力の検査、独立経路の守り、ページ送り、完了メッセージ、入力欄・お知らせ・ページ送りの画面部品は、共通の部品として用意しています。新しく書く前に[部品カタログ](docs/components.md)を確認してください。

経路が衝突すると（Coreや他の機能と同じパターン等）、起動時にエラーになります。

1. `features/<機能名>/` に機能を作成し、`features.Feature` の `Name`、`Init`、`RegisterRoutes` を実装します。必要なら `CustomRootFeature` の `HandleRoot` も実装します。複数のトップページ提供機能を登録すると、先に登録したものが使われます。
2. `cmd/server/features.go` にパッケージのimportと `r.Register(...)` を追記します。自作機能用の生成コマンドはありません。
3. DBを利用する機能は `Init` で必要な初期化を実装します。保存データの追加前に承認を得て、[取得・保存データ台帳](docs/data-inventory.md)と検査を更新します。
4. 入力の検査には部品の `internal/web/form`（`form.Text`・`form.Line`）を使います。動的機能では必要な操作に限ってセッションを発行し、OwnerIDによる分離、入力検証、CSRF保護、用途に応じた認証・認可を実装します。追加Featureの保護が自動的に完成するわけではありません。
5. データの寿命に合わせて、`owners` に紐づけるかを決めます。下記の「OwnerIDに紐づくデータの寿命」を確認してください。
6. 必要に応じて、次の任意のインターフェースを実装します（`features/feature.go`）。
   - `IsolatedRoutesFeature`：CoreのCSRF検査とセッション照合を通さない経路（Cookieを使わない公開ページ、独自認証の管理画面）。認証・CSRF・Origin検査は機能自身が行い、部品の `internal/web/guard`（`SameOrigin`・`CSRF`）を使います。
   - `PurgeFeature`：期限切れデータの削除。起動時と1時間ごとに呼ばれます。
   - `NavLinkFeature`：ナビゲーションへのリンク（例: お知らせ機能の「お知らせ」）。機能を外せばリンクも消えます。
   - `AdminConsumer`・`AdminNavFeature`：管理画面。管理者認証（`features/auth`）の口で、ログイン・CSRFの確認を受けます。
   - `LayoutFeature`・`LayoutConsumer`：サイト全体のヘッダー・フッター。Siteが有効なら、`/privacy` 等のCoreの画面と、お知らせ機能の公開ページ（`/news`）もSiteのレイアウトで描画します。
   - `CommandFeature`：サーバーの実行ファイルのサブコマンド（例: `minimal-web-kit auth-setup`）。
7. `make test`、`make verify`、`make build` を実行し、画面も確認します。

### OwnerIDに紐づくデータの寿命

OwnerIDはブラウザ単位の無記名の識別子で、セッション発行時に作られます。所有者（`owners` の行）は次の場合に削除され、`ON DELETE CASCADE` で紐づけたFeatureのデータも一緒に削除されます。

- 利用者がセッション破棄（`/account/delete-all`）を実行したとき。
- 有効なセッションが1件も残っていないとき。最終アクセスから30日を過ぎたセッションは、起動時と1時間ごとの処理で削除されます。

このため、OwnerIDへのCASCADEは、ブラウザ単位の一時メモ・匿名ツールの作業データ等に向いています。お知らせの記事や業務データのように、編集者がしばらくアクセスしなくても残すべきデータは、OwnerIDに紐づけないでください。システム管理者アカウント等を別に設計し、データの保持・削除方法を台帳に定めてください。

## ドキュメントと検査の範囲

開発時に作成する仕様・設計書は `docs/` にまとめます（[案内](docs/README.md)。今の仕様の正本は `docs/` 直下と `docs/domain/`、計画書は `docs/plans/`）。[取得・保存データ台帳](docs/data-inventory.md)は、利用目的・保存先・保持期間・削除方法の正本（SoT）です。別途テーブル定義書を作る場合はDB構造の詳細をそちらで管理し、方針を重複させず相互参照します。現在は独立したテーブル定義書がなく、台帳に照合用のテーブル・列一覧と型等の補助情報を併記しています。

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
│   ├── plans/              # 計画書・棚卸し・比較の記録（経緯。今の正本ではない）
│   ├── components.md       # 再利用する部品の一覧（Go・画面・CSS）
│   ├── deploy.md           # 本番デプロイの手順例（Linux）
│   ├── ui-guidelines.md    # 動きのあるUIの作り方（標準の要素 → htmx → 素のJS）
│   └── data-inventory.md   # 取得・保存データ台帳
├── LICENSE                # MIT License
├── SECURITY.md            # 脆弱性の報告窓口
├── Makefile               # 起動・ビルド・検査・Site・お知らせ機能の切り替え・システム管理者の登録URL発行
├── cmd/
│   ├── server/            # 起動処理・Featureの登録・Featureのサブコマンド
│   └── kit/               # Site・お知らせ機能・管理者認証の登録の切り替えCLIとテスト
├── internal/
│   ├── app/               # 設定・サーバー管理
│   ├── federation/        # 共通認証の照会の契約と通信の守り
│   ├── store/             # メモリ・SQLite・Coreマイグレーション
│   └── web/               # SSR・セッション・CSRF・ログ・CSS
│       ├── form/          # 部品: 入力の検査（文章・1行の値・整数）と入力欄の値
│       ├── guard/         # 部品: 独立経路の同一オリジン確認とCSRF
│       ├── notice/        # 部品: 完了メッセージの受け渡し
│       └── page/          # 部品: 一覧のページ送り
├── features/
│   ├── site/              # 同梱機能: LPテンプレート（Site）
│   ├── news/              # 同梱機能: お知らせ機能（公開ページ・API・記事の管理画面）
│   ├── auth/              # 同梱機能: 管理者認証（パスキーでのログイン・パスキーの管理）
│   │   └── oidcop/        # 同梱機能: 共通認証の提供（OIDC）
│   ├── mcp/               # 同梱機能: AIエージェント向けのMCPサーバー
│   └── federatedauth/     # 同梱機能: 共通認証の利用（別システムの認証基盤でのログイン）
├── skills/                # 6本のAI開発スキルの正本（各150行以内）
└── test/                  # 依存・テンプレート・台帳・ログ等の検査
```

## 脆弱性の報告

脆弱性を見つけた場合は、公開のIssueには書かず、[SECURITY.md](SECURITY.md) の手順に沿って非公開で報告してください。

## ライセンスと免責事項

このキットは [MIT License](LICENSE) で提供します。

MIT Licenseの要点は次のとおりです。参考の要約で、正式な条件は英語の原文（[LICENSE](LICENSE)）に従います。

- 著作権表示とライセンスの文を残せば、商用を含めて、使用、複製、変更、配布ができます。
- ソフトウェアは「現状のまま」提供され、商品性、特定の目的への適合性、権利の非侵害を含めて、明示か黙示かを問わず保証はありません。
- 著作者と権利者は、ソフトウェアやその使用に関して生じた請求、損害、その他の責任を負いません。

同梱の依存モジュールは、それぞれのライセンスに従います（[依存モジュール](#依存モジュール)）。キットの説明や検査は、法令への適合やセキュリティを保証するものではありません。キットから作ったアプリの運用と法令への対応は、運営者が判断してください。
