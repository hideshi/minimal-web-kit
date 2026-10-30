# 本番デプロイ手順 (deploy.md)

Linuxサーバーに単一の実行ファイルを置き、前段のリバースプロキシでHTTPSを終端する構成の手順です。
想定する環境は、さくらのVPS・XserverのVPS・AWS EC2などのLinuxサーバーです。Windows Server・BSD等は対象外です。

この手順は一般的な構成の例です。各サービス上での動作は確認していません。公開範囲の変更や外部通信は `AGENTS.md` の承認境界に従ってください。

## 1. 置き場所の選び方

| 置き場所 | 可否 | 理由 |
|---|---|---|
| VPS・EC2等のLinuxサーバー | 対象 | 常駐プロセスを動かし、SQLiteのファイルを保存できる |
| 常駐プロセスを動かせない共用レンタルサーバー | 対象外 | サーバーを常駐させられない |
| Cloudflare Workers・Pages等のサーバーレス環境 | 対象外 | Goの実行ファイルとSQLiteのファイルを置けない |
| Cloudflareのプロキシ・Tunnelを前段に置く | 可 | アプリはVPS等で動かし、Cloudflareは前段のHTTPS終端として使う。通信内容とアクセス記録がCloudflareを通る点を、利用者への説明（`/privacy` 等）に含める |

## 2. ビルド

開発機で、サーバーのCPUに合わせてビルドします。SQLiteドライバはGoのみで書かれているため、CGOは不要です。

```sh
make test && make verify

# x86_64（多くのVPS、EC2のIntel・AMD）
GOOS=linux GOARCH=amd64 make build
# arm64（EC2のGraviton等）
GOOS=linux GOARCH=arm64 make build
```

`bin/minimal-web-kit` 1本に、テンプレート・CSS・JS・マイグレーションが埋め込まれます。有効にした機能（`cmd/server/features.go` の登録）もビルド時に決まるため、Site・お知らせ機能の有効化はビルドの前に行います。

## 3. サーバーの準備

専用のユーザーとデータ用ディレクトリを作ります。DBには利用者のデータが入るため、他のユーザーから読めないようにします。

```sh
sudo useradd --system --home /var/lib/minimal-web-kit --shell /usr/sbin/nologin mwk
sudo install -d -o mwk -g mwk -m 0700 /var/lib/minimal-web-kit
sudo install -d -m 0755 /etc/minimal-web-kit
```

実行ファイルを転送して配置します。

```sh
scp bin/minimal-web-kit user@server:/tmp/
ssh user@server 'sudo install -m 0755 /tmp/minimal-web-kit /usr/local/bin/minimal-web-kit && rm /tmp/minimal-web-kit'
```

## 4. 設定

`/etc/minimal-web-kit/env` に環境変数を書きます（`sudo install -m 0640 -o root -g mwk` で作成）。`example.com` は実際のドメインに置き換えてください。

```sh
DB_PATH=/var/lib/minimal-web-kit/app.db
# HTTPSで公開するため必須
SECURE_COOKIE=true
# 表示する日時のタイムゾーン（EC2等は既定がUTCのことが多い）
TZ=Asia/Tokyo

# Siteを有効にした場合
CONTACT_FORM_URL=https://forms.example.com/...

# 管理者認証（お知らせ機能を含む）を有効にした場合
AUTH_ORIGIN=https://example.com
AUTH_RP_ID=example.com
# 別ドメインのフロントエンドからAPIを読む場合だけ（既定は許可なし）
# NEWS_CORS_ORIGINS=https://front.example.com
```

- 値に空白や `&` 等の記号を含む場合は、`"..."` で囲みます（7節でシェルからも読み込むため）。
- 待受は既定の `127.0.0.1:8080` のままにします。外部からの接続はリバースプロキシだけが受けます。`ADDR` で `127.0.0.1` 以外に変える場合は、承認が必要です。
- `AUTH_ORIGIN` が https で `SECURE_COOKIE=true` でない場合、`AUTH_RP_ID` が `AUTH_ORIGIN` のホストと合わない場合は、起動時にエラーで止まります。
- 同期型のパスキー（Googleパスワードマネージャー等）を使わせる場合だけ、`AUTH_ALLOW_SYNCED_PASSKEYS=true` を足します。締め出されにくくなる代わりに、同期の提供者のアカウントの守りに頼り、複製を検出できなくなります（README の管理者認証の節）。綴りを間違えると起動時にエラーで止まります。
- 許可を後で外すと、同期型のパスキーではログインできなくなります。外す前に、各管理者が端末固定型（Windows Hello・セキュリティキー等）を1つ以上持っているかを、パスキーの端末名で確かめてください（登録時に種類が分かる端末名を付けておく）。締め出された場合は7節の `auth-recover -account <ID>` で戻します。ログでは `reason=synced_credential` が原因の目印です。

## 5. 常駐させる（systemd）

`/etc/systemd/system/minimal-web-kit.service`:

```ini
[Unit]
Description=minimal-web-kit
After=network.target

[Service]
User=mwk
Group=mwk
EnvironmentFile=/etc/minimal-web-kit/env
WorkingDirectory=/var/lib/minimal-web-kit
ExecStart=/usr/local/bin/minimal-web-kit
Restart=on-failure
# 権限の制限
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
ReadWritePaths=/var/lib/minimal-web-kit

[Install]
WantedBy=multi-user.target
```

```sh
sudo systemctl daemon-reload
sudo systemctl enable --now minimal-web-kit
sudo systemctl status minimal-web-kit
```

アプリのログは標準出力に出て、journaldに保存されます。保持期間は `/etc/systemd/journald.conf` の `MaxRetentionSec` 等で決めてください。停止時（SIGTERM）は処理中のリクエストを待ってから終了します。

## 6. HTTPSの終端（リバースプロキシ）

例として、証明書を自動で取得・更新するCaddyの設定です（`/etc/caddy/Caddyfile`）。

```text
example.com {
	reverse_proxy 127.0.0.1:8080
}
```

- nginx等を使う場合も、`127.0.0.1:8080` へ転送し、HTTPSで受けます。
- **プロキシのアクセスログに注意してください。** nginxは既定で、IPアドレス・User-Agent・URLを記録します。アプリはこれらを記録しない作りなので、プロキシ側で記録しないか、保持期間を決めて `/privacy` の説明と合わせます。Caddyは `log` を書かなければアクセスログを出しません。
- ファイアウォールでは、SSHと80・443番だけを開けます。8080番は外部に開けません。
- 大量のリクエストへの対策（レート制限等）は、キットの範囲外です。プロキシやCloudflare等で行います。
- **`/static/` のキャッシュ**: 同梱のレイアウトは、CSS・アイコンを中身の版付きのURL（`?v=` に中身のハッシュ）で読み込みます。キットは、版が一致すれば `public, max-age=31536000, immutable`、版が無いか古ければ `public, max-age=600` を指定します。前段にCDNを置くときは、次の2つを別々に確かめてください。
  - **ブラウザ向けの期間**: Cloudflare の Browser Cache TTL は、元の応答の期間が設定より短いと、設定の値まで引き上げます。版の無いURL（OGPの画像等）も10分で返したいなら、「Respect Existing Headers」にします。版付きのURLは1年を指定しているため、この設定の影響を受けません。
  - **キャッシュのキー**: `/static/` のキャッシュのキーにクエリ（`v`）を含めます。Cloudflare のキャッシュレベルを「Ignore Query String」にすると、違う版のURLが同じキャッシュになり、更新が届きません（既定の「Standard」は含めます）。
  - **確かめ方**: 配備の前後で、HTMLの中のCSSのURL（`?v=` の値）が変わり、新しいURLが新しい中身を返すことを確かめます。古い版のURLにも今の中身を返す作りのため、同じ稼働版に違う `v` を送って中身を比べても確かめられません。
- **前段がページを書き換える機能に注意してください。** Cloudflareの Email Address Obfuscation（登録時に自動でオン）は、HTMLのメールアドレスを書き換えて、復元用のJavaScriptを差し込みます。このキットのCSPではそのJavaScriptが動かないため、書き換えられたメールアドレスは人にも「[email protected]」と見えます。管理画面の応答には `Cache-Control: no-transform` を付けているため、書き換えられません（受信箱の返信先などはそのまま読めます）。公開ページは書き換えの対象のままなので、メールアドレスは訪問者にも読めなくなります。公開ページにメールアドレスを載せず、お問い合わせのフォームを案内してください。Rocket Loader・Zaraz・Web Analyticsの自動挿入も、CSPで動かないため使いません。

## 7. 管理者のパスキーの初回登録（管理者認証・お知らせ機能を有効にした場合）

サーバー上で、サーバーと同じ設定（同じ `DB_PATH`・`AUTH_ORIGIN`）で実行します。Goは不要です。

```sh
sudo -u mwk sh -c 'set -a; . /etc/minimal-web-kit/env; exec /usr/local/bin/minimal-web-kit auth-setup'
```

表示された `https://example.com/admin/setup#token=...` を15分以内に開き、パスキーを登録します。2人目からは、管理画面の「アカウントの管理」で登録URLを発行します。

管理者全員がパスキーを失った場合は、同じ形で `auth-recover -account <ID>` を実行します（確認の入力あり。`-account` を省くと管理者の一覧を表示します）。1人分だけ初期化する場合は `-only <ID>` を使います。サーバーの稼働中に実行して構いません。

## 8. バックアップ

稼働中のDBファイルをそのままコピーすると、書き込み途中の内容（WAL）が欠けることがあります。SQLiteの機能で整合した複製を作ります（`sqlite3` コマンドが必要）。

```sh
sudo -u mwk sqlite3 /var/lib/minimal-web-kit/app.db ".backup '/var/lib/minimal-web-kit/backup-$(date +%F).db'"
```

- 複製には利用者のデータと記事が含まれます。保存先の権限と保持期間を決め、期限を過ぎたものは削除します。
- 管理画面やアカウント削除で消したデータも、削除前の複製には残ります。この点は `docs/data-inventory.md` の説明と合わせてください。

## 9. 更新

1. 開発機でテストとビルドを行う（2節）。
2. サーバーでバックアップを取る（8節）。
3. 実行ファイルを置き換えて再起動する。

```sh
sudo install -m 0755 /tmp/minimal-web-kit /usr/local/bin/minimal-web-kit
sudo systemctl restart minimal-web-kit
```

テーブルの追加は起動時に自動で行います。既存のテーブル・データは原則として変更しません。例外は、旧形式の表の移行です。お問い合わせの表 `contact_messages` は、状態が既読（`read`）の版から更新すると、起動時に作り直します（既読は完了になり、行は残ります）。この更新の後は古い実行ファイルに戻せないため、戻すときは更新の前のバックアップからDBごと戻してください。再起動が終わるまで、別のバックアップを重ねないでください。お知らせ機能の旧称「CMS」の版から更新する場合は、環境ファイルの `CMS_CORS_ORIGINS` を `NEWS_CORS_ORIGINS` に改名してください（旧名が残っていると起動を止めます）。記事の表は起動時に自動で改名し、記事は残ります。ただし、管理者認証がアカウント導入前の版だった場合は、起動を止めてログに `auth old schema found` を出します。バックアップの後に `auth-recover -reset-schema` を7節と同じ形で実行し、表示されたURLでパスキーを登録し直してください（管理者認証の表だけを作り直し、お知らせの記事等は残ります）。管理者のログイン状態は再起動後も続きますが、ログインの途中だった操作や、再起動前に開いた管理画面のフォームの送信は失敗します。ページを開き直してください。

お問い合わせの表の作り直しの途中で止まると、作業用の表 `contact_messages_legacy` が残り、起動を止めます。この場合はバックアップから戻してください。

## 10. 公開前の確認

- `https://example.com/` が表示され、`http://` はHTTPSへ転送される。
- Cookieが発行される画面（Base版の画面、管理画面のログイン後）で、Cookieに `Secure` が付いている（ブラウザの開発者ツールで確認）。
- `http://<サーバーのIP>:8080/` に外部から接続できない。
- 管理者認証を有効にした場合、パスキーで登録・ログインできる。お知らせ機能を有効にした場合、`/news` と `/api/news` が表示される。
- DBのディレクトリが `mwk` 以外から読めない（`ls -ld /var/lib/minimal-web-kit`）。
- プロキシ・journald・バックアップの保持期間を決めてある。
- CSSの中身を変えて配備したら、本番のHTMLのCSSのURLの `?v=` が配備の前と変わり、その応答が `Cache-Control: public, max-age=31536000, immutable` で新しい中身を返す（6節の `/static/` のキャッシュ）。版はファイルごとの中身から作るため、テンプレートだけの変更ではCSSの版は変わらない。そのときは、HTMLに変更が出ていることを確かめる。

## 11. Siteだけを静的ホスティングに置く

SiteのLP（`/`）とプライバシーポリシー（`/privacy`）は、Cookieも保存データも使わないため、静的ファイルとして書き出して、Cloudflare Workersの静的アセット等の静的ホスティングに置けます。サーバーやDBは不要です。

```sh
# Siteだけを有効にした状態で
SITE_URL=https://example.com CONTACT_FORM_URL=https://forms.example.com/... go run ./cmd/server export-static dist
```

| 書き出すファイル | 内容 |
|---|---|
| `index.html` | `/` |
| `privacy.html` | `/privacy`（Cloudflare Workersの静的アセットは、既定の設定で `privacy.html` を `/privacy` で配信します） |
| `static/css/style.css` | CSS |
| `static/img/ogp.png` | SNSのカード用画像 |
| `favicon.ico`、`static/img/favicon.svg` 等 | ブラウザのタブ等に出るアイコン |
| `robots.txt`、`sitemap.xml` | 検索エンジン向け。`sitemap.xml` は `SITE_URL` を設定した場合だけ |
| `404.html` | 存在しないパスに返すページ。Cloudflare Workersでは `wrangler.jsonc` の `assets.not_found_handling` を `"404-page"` にすると使われます |
| `_headers` | Goのサーバーと同じセキュリティヘッダー（CSP、`X-Content-Type-Options`、`X-Frame-Options`、`Referrer-Policy`）を全経路に付ける設定 |

- お知らせ機能等、セッションや管理画面を使う機能が有効な場合は、静的にできないため書き出しを拒否します。
- 書き出し先に、この処理が作るもの以外のファイルがある場合も拒否します（上書き・削除しません）。
- `CONTACT_FORM_URL` は書き出すときにHTMLへ埋め込まれます。文言や設定を変えたら、書き出し直して配信し直します。
- アクセスの記録は配信サービス側に残ります。`/privacy` の文面を、実際に使うサービスに合わせて編集してください。
- 書き出すHTMLは、CSS・アイコンを版付きのURL（`/static/css/style.css?v=…`）で参照します。書き出したHTMLが、書き出しに含まれない `/static/` のファイルを参照していれば、書き出しを拒否します。配信先が `?v=` 付きのURLにファイルを返すかは、使うサービスで確かめてください。
- `_headers` の書式は [Cloudflare Workersの静的アセットのヘッダー](https://developers.cloudflare.com/workers/static-assets/headers/) に従います。他のサービスでは、同等の設定に置き換えてください。


## カタログを使う場合

`make add-catalog` はAuthも有効にします。`/admin/catalog` は管理者・メンバーの両方が使えます。AuthのOrigin・RP ID・Secure Cookieは既存の管理者認証の設定を適用してください。カタログはSQLiteを使うため静的書き出しはできません。
公開一覧だけ同梱htmxとcatalog.jsを自サイトから配信し、CSPのscript-srcをselfにします。CDNや外部APIへの通信はありません。プロキシでCSPを上書きする場合はこの画面だけの例外と、他画面のnoneを維持してください。カタログのHTMLはno-storeを維持し、通常/断片/履歴復元のVaryを削除しないでください。
データは明示削除まで保持し、機能を外しても残ります。バックアップとWALの保持・削除は運営者が決めてください。取得・保存内容はデータ台帳6節にあります。

## お問い合わせを使う場合

`make add-contact` は管理者認証も有効にします。`/contact` の送信は、管理者認証と同じ `AUTH_ORIGIN` と比べて、他のサイトからの送信を拒否します（`Sec-Fetch-Site` を送らない古いブラウザからは送れません）。`/contact` はセッションもCookieも使わず、応答は `no-store` です。前段でキャッシュさせないでください。

- **前段のレート制限（推奨）**: アプリはIPアドレスを扱わないため、IPごとの連打は前段で抑えます。Cloudflareの無料プランのレート制限のルールは、1つだけ・条件はパスだけ（GETとPOSTを区別できない）・数える時間と止める時間は10秒です（2026-10-29に公式の資料で確認）。条件を `/contact` で始まるパスに限り、「同じIPから10秒に5回まで」のように掛けると、連打を少し抑えられます。
- **保持期間とバックアップ**: お問い合わせは受付から `CONTACT_RETENTION_DAYS`（既定90日）で消えます。対応中のものは最長 `CONTACT_ACTIVE_MAX_DAYS`（既定365日）まで残ります。8節の複製には、その複製の保持期間のあいだ残ります。プライバシーポリシーの雛形の表（自動で載る）に加え、サイト独自の説明で、合わせた最長の期間を書いてください。
- **効き目の確認**: 公開後しばらく、ログの `contact submission` の `result`（`saved`・`honeypot`・`too_fast`・`expired`・`bad_token`・`replayed`・`invalid_*`・`over_limit`）の件数を見ます。`honeypot` が人の送信らしい時間帯に多ければ、自動入力で見えない欄が埋まっている可能性があります。
- **通知はありません**: 管理画面の受信箱を定期的に見てください。保持期間を過ぎると、未読のままでも消えます。
- 外部のCAPTCHA（Cloudflare Turnstile等）は、上の対策で防げなくなったときの最後の手段です。外部のスクリプトとJavaScriptが要るため、承認境界3・4に当たります。
