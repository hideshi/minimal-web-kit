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

開発機で、サーバーのCPUに合わせてビルドします。SQLiteドライバはC言語のSQLiteをGoへ変換したものなので、CGOは不要です。ただし、SQLiteの層はメモリ安全ではありません（READMEの「依存モジュール」）。

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
- 許可を後で外すと、同期型のパスキーではログインできなくなります。外す前に、各システム管理者が端末固定型（Windows Hello・セキュリティキー等）を1つ以上持っているかを、パスキーの端末名で確かめてください（登録時に種類が分かる端末名を付けておく）。締め出された場合は7節の `auth-recover -account <ID>` で戻します。ログでは `reason=synced_credential` が原因の目印です。

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
- **`/static/` のキャッシュ**: 同梱のレイアウトは、CSS・アイコンを中身の版付きのURL（`?v=` に中身のハッシュ）で読み込みます（カタログの公開一覧のJS `/catalog/static/` も同じです）。キットは、版が一致すれば `public, max-age=31536000, immutable`、版が無いか古ければ `public, max-age=600` を指定します。前段にCDNを置くときは、次の2つを別々に確かめてください。
  - **ブラウザ向けの期間**: Cloudflare の Browser Cache TTL は、元の応答の期間が設定より短いと、設定の値まで引き上げます。版の無いURL（OGPの画像等）も10分で返したいなら、「Respect Existing Headers」にします。版付きのURLは1年を指定しているため、この設定の影響を受けません。
  - **キャッシュのキー**: `/static/` のキャッシュのキーにクエリ（`v`）を含めます。Cloudflare のキャッシュレベルを「Ignore Query String」にすると、違う版のURLが同じキャッシュになり、更新が届きません（既定の「Standard」は含めます）。
  - **確かめ方**: 配備の前後で、HTMLの中のCSSのURL（`?v=` の値）が変わり、新しいURLが新しい中身を返すことを確かめます。古い版のURLにも今の中身を返す作りのため、同じ稼働版に違う `v` を送って中身を比べても確かめられません。
- **前段がページを書き換える機能に注意してください。** Cloudflareの Email Address Obfuscation（登録時に自動でオン）は、HTMLのメールアドレスを書き換えて、復元用のJavaScriptを差し込みます。このキットのCSPではそのJavaScriptが動かないため、書き換えられたメールアドレスは人にも「[email protected]」と見えます。管理画面の応答には `Cache-Control: no-transform` を付けているため、書き換えられません（受信箱の返信先などはそのまま読めます）。公開ページは書き換えの対象のままなので、メールアドレスは訪問者にも読めなくなります。公開ページにメールアドレスを載せず、お問い合わせのフォームを案内してください。Rocket Loader・Zaraz・Web Analyticsの自動挿入も、CSPで動かないため使いません。

MCPを有効にする場合、サーバーは `Host` を `AUTH_ORIGIN` のホストと比べます。上の設定はそのまま通ります。nginxでは `proxy_set_header Host $host;` が要ります（既定の書き換えでは `/mcp` が403になります）。

## 7. システム管理者のパスキーの初回登録（管理者認証・お知らせ機能を有効にした場合）

サーバー上で、サーバーと同じ設定（同じ `DB_PATH`・`AUTH_ORIGIN`）で実行します。Goは不要です。

```sh
sudo -u mwk sh -c 'set -a; . /etc/minimal-web-kit/env; exec /usr/local/bin/minimal-web-kit auth-setup'
```

表示された `https://example.com/admin/setup#token=...` を15分以内に開き、パスキーを登録します。2人目からは、管理画面の「アカウントの管理」で登録URLを発行します。

システム管理者全員がパスキーを失った場合は、同じ形で `auth-recover -account <ID>` を実行します（確認の入力あり。`-account` を省くとシステム管理者の一覧を表示します）。1人分だけ初期化する場合は `-only <ID>` を使います。サーバーの稼働中に実行して構いません。

## 8. バックアップ

データベースはSQLiteだけを対象にします。1台のサーバーで動かす構成で、自動の切り替えや複数台の構成はありません。そのぶん、**サーバーごと失っても戻せること**を運用で守ります。

稼働中のDBファイルをそのままコピーすると、書き込み途中の内容（WAL）が欠けることがあります。`backup` コマンドで整合した複製を作ります。作った直後に検査し、既にあるファイルは上書きしません。権限は所有者だけが読める0600です。

```sh
sudo -u mwk sh -c 'set -a; . /etc/minimal-web-kit/env; exec minimal-web-kit backup /var/lib/minimal-web-kit/backup-$(date +%F).db'
```

- **サーバーの外に置く**: 同じサーバーの複製は、ディスクの故障やサーバーの解約で一緒に失います。毎日、別の場所へ送ります。キットは外へ送りません。`rclone` 等の既存のツールで送り、送り先での暗号化と、期限で消す設定を決めます。送り先はプライバシーポリシーの説明に足します。
- **失う量の目安**: 毎日送るなら、失うのは最大1日分です。送っていなければ、サーバーを失うと全部失います。
- 複製には利用者のデータと記事が含まれます。保存先の権限と保持期間を決め、期限を過ぎたものは削除します。
- 管理画面やアカウント削除で消したデータも、削除前の複製には残ります。この点は `docs/data-inventory.md` の説明と合わせてください。

### 複製を確かめる・戻す

`verify-backup` は、複製が壊れていないこと、外部キーの違反が無いこと、このキットのDBであることを確かめ、表ごとの行数を表示します。中身は表示せず、複製も変えません。

```sh
minimal-web-kit verify-backup /path/to/backup.db
```

戻す手順です。**複製を取っただけでは戻せるか分かりません。** 数か月に1回、手元や別のサーバーで試してください。

次の手順は、途中で失敗すると止まり、それ以降を実行しません。サーバーを止め、複製を検査し、今のDBを一式残してから戻します。戻す間は、運用コマンドを含めて、他のプロセスからDBを開かないでください。`-wal`・`-shm` のファイルが残ると、古いDBの続きとして読まれ、戻した内容が壊れます。

```sh
sudo sh -eu -s -- /path/to/backup.db <<'EOF'
systemctl stop minimal-web-kit
if systemctl is-active --quiet minimal-web-kit; then exit 1; fi
/usr/local/bin/minimal-web-kit verify-backup "$1"
cd /var/lib/minimal-web-kit
saved=before-restore-$(date +%Y%m%d-%H%M%S)
mkdir -m 0700 "$saved"
cp -p app.db "$saved/"
for f in app.db-wal app.db-shm; do
  if [ -e "$f" ]; then cp -p "$f" "$saved/"; fi
done
install -m 0600 -o mwk -g mwk "$1" app.db.restore
rm -f app.db-wal app.db-shm
mv app.db.restore app.db
EOF
```

起動の前に、次を確かめます。

- 複製より後に、アカウントの停止、ロールの変更、パスキーの削除をしていたら、複製の時点に戻ります。戻した後でやり直してください。急ぐなら、外からの経路を閉じたまま起動して行います。
- 共通認証の提供機能を使っているなら、起動の前に全連携先のログインを失効させます。

```sh
sudo -u mwk sh -c 'set -a; . /etc/minimal-web-kit/env; exec minimal-web-kit oidc-revoke-all -yes'
```

起動して画面を確かめます: `sudo systemctl start minimal-web-kit`。古い版の複製でも、起動時に今の表へ移行します。確かめたら、残した `before-restore-...` のディレクトリを消します。個人情報を含むため、残し続けないでください。

複製より後の書き込みは戻りません。別のサーバーへ戻すときは、環境ファイルと、共通認証の鍵のファイルも必要です。これらは複製に入らないため、別に保管してください。

## 9. 更新

1. 開発機でテストとビルドを行う（2節）。
2. サーバーでバックアップを取る（8節）。
3. 戻せるように今の実行ファイルを残し、置き換えて再起動する。

```sh
sudo cp -p /usr/local/bin/minimal-web-kit /usr/local/bin/minimal-web-kit.prev
sudo install -m 0755 /tmp/minimal-web-kit /usr/local/bin/minimal-web-kit
sudo systemctl restart minimal-web-kit
```

テーブルの追加は起動時に自動で行います。既存のテーブル・データは原則として変更しません。例外は、旧形式の表の移行です。お問い合わせの表 `contact_messages` は、状態が既読（`read`）の版から更新すると、起動時に作り直します（既読は完了になり、行は残ります）。この更新の後は古い実行ファイルに戻せないため、戻すときは更新の前のバックアップからDBごと戻してください。再起動が終わるまで、別のバックアップを重ねないでください。お知らせ機能の旧称「CMS」の版から更新する場合は、環境ファイルの `CMS_CORS_ORIGINS` を `NEWS_CORS_ORIGINS` に改名してください（旧名が残っていると起動を止めます）。記事の表は起動時に自動で改名し、記事は残ります。ただし、管理者認証がアカウント導入前の版だった場合は、起動を止めてログに `auth old schema found` を出します。バックアップの後に `auth-recover -reset-schema` を7節と同じ形で実行し、表示されたURLでパスキーを登録し直してください（管理者認証の表だけを作り直し、お知らせの記事等は残ります）。一般ユーザーのロールを足す版へ更新すると、`auth_accounts`（共通認証の利用側は `federated_accounts`）を起動時に作り直し、旧称の `member` を `site_admin` に写します（DBと同じ場所に `pre-migration-*-roles-*.db` の複製を作ります）。この更新の後に古い実行ファイルへ戻すと、古い版は一般ユーザーを止められず、公開サイトの編集を許してしまいます。古い版の画面ではロールも変えられません。戻すときは、先に一般ユーザーを停止してください。ログイン状態は再起動後も続きますが、ログインの途中だった操作や、再起動前に開いた管理画面のフォームの送信は失敗します。ページを開き直してください。

お知らせ・カタログの本文でリンクの記法を読む版（[本文の書き方](richtext.md) 7節）へ更新すると、既存の本文のうち、`[文字](URL)` の形と、`\` の後ろに `\`・`[`・`]`・`(`・`)` が続く所の見え方が変わります（前は文字のまま、後はリンクか1文字）。更新の前に、8節の複製に対して次のSQLを実行し、当たる記事を確かめます。出るのは機能の名前と記事の番号だけで、本文は出ません。下書きも含みます。SQLでは記法を正確に読めないため、広めに拾います（当たっても変わらない記事があります）。管理画面で当たった記事を開き、意図しないリンクや `\` の消え方が無いかを見て、必要なら `\[` と書き直します。`sqlite3` が無ければ `sudo apt install sqlite3` で入れます。お知らせかカタログの一方しか使っていなければ、使っていない方の `SELECT` を消します（表が無いと失敗します）。

```sh
sqlite3 -readonly /path/to/backup.db <<'EOF'
SELECT 'news', id FROM news_posts
 WHERE instr(body, '\\') OR instr(body, '\[') OR instr(body, '\]') OR instr(body, '\(') OR instr(body, '\)')
    OR instr(body, '](https://') OR instr(body, '](/')
UNION ALL
SELECT 'catalog', id FROM catalog_items
 WHERE instr(body, '\\') OR instr(body, '\[') OR instr(body, '\]') OR instr(body, '\(') OR instr(body, '\)')
    OR instr(body, '](https://') OR instr(body, '](/');
EOF
```

お問い合わせの表の作り直しの途中で止まると、作業用の表 `contact_messages_legacy` が残り、起動を止めます。この場合はバックアップから戻してください。

表を作り直す移行は、その前に、DBと同じ場所へ `pre-migration-<機能>-<日時>.db` の複製を自動で作ります。複製に失敗したら移行せず、起動を止めます。この複製は自動では消えません。個人情報を含むため、更新を確かめたら削除してください。

### 戻し方

更新の前に、前の版が更新後のDBを読み書きできるかを確かめておきます。起動できるだけでは足りません。主な読み書きを、DBの複製で試します。

| 前の版との関係 | 戻し方 |
|---|---|
| 読み書きできると確かめた（表の追加だけの更新は、ここに当たることが多い） | 実行ファイルだけを戻す。データは失わない |
| 読み書きできない（列の必須化、値の意味の変更、表の作り直し等） | 8節の手順で更新の前の複製を戻し、前の版で起動する。複製より後のデータは失う |
| 分からない、移行の途中で止まった | 複製を戻す扱いにする。先に、失うデータを確かめる |
| 設定の誤りで起動しない | 戻さずに、設定を直す |

実行ファイルを戻す手順です。

```sh
sudo install -m 0755 /usr/local/bin/minimal-web-kit.prev /usr/local/bin/minimal-web-kit
sudo systemctl restart minimal-web-kit
```

複製も戻すときは、8節の手順の後、起動の前に、実行ファイルを戻します。実行ファイルと一緒に、環境ファイルとsystemdの設定も前の組み合わせに戻します。漏洩した秘密の値は、前の値に戻さないでください。

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

`make add-catalog` はAuthも有効にします。`/admin/catalog` はシステム管理者・サイト管理者の両方が使えます。AuthのOrigin・RP ID・Secure Cookieは既存の管理者認証の設定を適用してください。カタログはSQLiteを使うため静的書き出しはできません。
公開一覧だけ同梱htmxとcatalog.jsを自サイトから配信し、CSPのscript-srcをselfにします。CDNや外部APIへの通信はありません。プロキシでCSPを上書きする場合はこの画面だけの例外と、他画面のnoneを維持してください。カタログのHTMLはno-storeを維持し、通常/断片/履歴復元のVaryを削除しないでください。
データは明示削除まで保持し、機能を外しても残ります。バックアップとWALの保持・削除は運営者が決めてください。取得・保存内容はデータ台帳6節にあります。

## 画像を使う場合

`make add-media` は管理者認証も有効にします。画像はDBに入るため、書き込み先のフォルダは増えません。

- **バックアップの容量**: 毎日DB全体を複製するため、画像の容量は世代の分だけ増えます。`MEDIA_TOTAL_LIMIT_MB`（既定300）いっぱいなら、14世代で約4GB、30世代で約9GBです。画像は圧縮が効きにくいです。`VACUUM INTO` は複製の間、DBと同じ容量を追加で使い、`pre-migration-*.db` も画像の分だけ大きくなります。
- **容量の監視**: 全体の上限は画像の合計で、DBファイルの大きさの上限ではありません。消した領域はSQLiteの中で再利用され、ファイルは自動では縮みません。DBの実サイズ、WAL、複製を作る間の空き容量を確かめます。空きが無いと、画像以外の書き込みも失敗します。その場合は古い複製を消して空きを作ります。
- **メモリ**: 加工は同時に1件です。手元の測定で、4MB・1,250万画素の写真で約57MB、2,400万画素の通常のJPEGで約89MB、1,200万画素のプログレッシブ（4:4:4）で約225MBでした（[測定](experiments/media-image-processing.md)）。本番の値は実機で確かめ、小さなサーバーでは systemd の `MemoryMax=` を検討します。超えるとサイト全体が再起動します。
- **`/media/` のキャッシュ**: 公開の画像は `public, max-age=86400` です。公開を取り消しても、CDNとブラウザに残った分は最長1日表示され得ます。Browser Cache TTL を1日より長くすると、この期間も延びます。CDNの消去は作っていません。急ぐときはCDNの管理画面で消します。404は `no-store` で、公開した直後に404が残りません。Cloudflareは拡張子でキャッシュを決めるため、`/media/*.jpg`・`/media/*.png` は既定の設定でキャッシュされます。
- **前段の本文の上限**: 追加の経路は本文が約8MBになります。リバースプロキシを使うなら、`client_max_body_size` 等を `/admin/media` だけ広げます。
- **プライバシー**: 画像には人の顔や名札が写り得ます。削除した画像も、複製の保持期間のあいだ残ります。プライバシーポリシーは `minimal-web-kit-privacy` のスキルで見直してください。
- 取得・保存内容はデータ台帳の12節にあります。画像機能を有効にすると、静的書き出しは使えません。

## お問い合わせを使う場合

`make add-contact` は管理者認証も有効にします。`/contact` の送信は、管理者認証と同じ `AUTH_ORIGIN` と比べて、他のサイトからの送信を拒否します（`Sec-Fetch-Site` を送らない古いブラウザからは送れません）。`/contact` はセッションもCookieも使わず、応答は `no-store` です。前段でキャッシュさせないでください。

- **前段のレート制限（推奨）**: アプリはIPアドレスを扱わないため、IPごとの連打は前段で抑えます。Cloudflareの無料プランのレート制限のルールは、1つだけ・条件はパスだけ（GETとPOSTを区別できない）・数える時間と止める時間は10秒です（2026-10-29に公式の資料で確認）。条件を `/contact` で始まるパスに限り、「同じIPから10秒に5回まで」のように掛けると、連打を少し抑えられます。
- **保持期間とバックアップ**: お問い合わせは受付から `CONTACT_RETENTION_DAYS`（既定90日）で消えます。対応中のものは最長 `CONTACT_ACTIVE_MAX_DAYS`（既定365日）まで残ります。8節の複製には、その複製の保持期間のあいだ残ります。プライバシーポリシーの雛形の表（自動で載る）に加え、サイト独自の説明で、合わせた最長の期間を書いてください。
- **効き目の確認**: 公開後しばらく、ログの `contact submission` の `result`（`saved`・`honeypot`・`too_fast`・`expired`・`bad_token`・`replayed`・`invalid_*`・`over_limit`）の件数を見ます。`honeypot` が人の送信らしい時間帯に多ければ、自動入力で見えない欄が埋まっている可能性があります。
- **通知はありません**: 管理画面の受信箱を定期的に見てください。保持期間を過ぎると、未読のままでも消えます。
- 外部のCAPTCHA（Cloudflare Turnstile等）は、上の対策で防げなくなったときの最後の手段です。外部のスクリプトとJavaScriptが要るため、承認境界3・4に当たります。

## WordPressからの移行

`cmd/wpimport` は、WordPressの書き出し（WXR）と、手元に取ったアップロードのフォルダから、お知らせ・固定ページ・画像へ移す道具です。サーバーとは別の実行ファイルで、外部へ通信しません。設計と決めたことは [wordpress-import-plan.md](plans/wordpress-import-plan.md)。

### 移るもの・移らないもの

| WordPress | キット | 扱い |
|---|---|---|
| 投稿（公開・予約・下書き・承認待ち） | お知らせの下書き | 段落・行・リンクだけ。見出しは段落、リストは「・」の行、表は「 / 」でつないだ行になる。カテゴリを1つ（未分類を除き、Yoastの主カテゴリがあればそれ）。公開日時は残す |
| 固定ページ（同上） | 固定ページの下書き | 見出し・段落・リスト・表・リンク・強調・画像の行。親子は1階層になる。使えないスラッグは `page-<ID>` |
| 添付のJPEG・PNG | 画像 | 画像機能と同じ縮小・再エンコード（EXIFを落とす）。説明は代替テキスト → 本文の alt → キャプション → 仮の文 |
| アイキャッチ | お知らせの代表画像 | 無ければ本文の最初の画像 |
| お知らせの本文の途中の画像 | 移さない | 報告に記事ごとの旧URLと説明を出す |
| パスワード保護の記事 | 保護なしの下書き | 報告に出す。転送の対象にしない |
| 非公開・ゴミ箱、メニュー、その他の投稿タイプ（お問い合わせのプラグインの受信等） | 移さない | 種類と件数だけ報告する。題名は出さない |
| コメント、作者（名前・メールアドレス） | 移さない | 読み飛ばし、値を持たない |
| WebP・GIF・SVG・PDF・動画・音声 | 移さない | 拡張子ごとの件数を報告する。必要なら手で変換して画像機能で足す |
| テーマ・ページビルダーの見た目、プラグインの機能、ショートコード（`[caption]`・`[gallery]` 以外） | 移さない | 表示の時に中身を作るブロック（最新の投稿・検索等）は名前と数を報告する |
| タグ、2つ目以降のカテゴリ | 移さない | 数を報告する |

### 手順

1. **準備（手元）**: WordPressの管理画面の「ツール → エクスポート」で「すべてのコンテンツ」を書き出す。`wp-content/uploads` をフォルダごと手元に取る（FTP・ホスティングの管理画面等。道具はダウンロードしない）。キットの側で、移す先の機能を有効にしておく（`make add-news`・`make add-pages`・`make add-media`）。表の無い機能の記事は取り込まず、報告に出す。
2. **道具を作る**: `CGO_ENABLED=0 go build -trimpath -o bin/wpimport ./cmd/wpimport`（サーバーと同じ `GOOS`・`GOARCH` で）。
3. **試しの実行**: 8節の複製を手元に取り、それに対して試す。DBは変えず、報告だけを書きます。画像も実際に加工するので、容量の上限（`MEDIA_TOTAL_LIMIT_MB`。サーバーと同じ値を環境変数で渡す）に当たるかが分かります。

   ```sh
   bin/wpimport -wxr export.xml -uploads ./uploads -db backup.db -report report-dry.md -dry-run
   ```

   報告の「記事ごと」「移せなかった画像」「お知らせの本文から外した画像」を読み、移らない物を確かめます。古いサイトが `www` の有り無しや `http` でも配信していたなら、`-old-origin` を複数指定します（既定は書き出しの `base_blog_url`）。画像のURLが `/wp-content/uploads/` の下でなければ、`-uploads-url` で接頭辞を指定します。
4. **取り込み（サーバー）**: サーバーを止め（同時の書き込みを避ける。止めないと複製を作れないことがあります）、8節のバックアップを取ってから実行します。稼働DBは読むだけで、`-out` の作業DBに取り込みます（読むために、稼働DBの横に `-wal`・`-shm` の空のファイルができることがあります。稼働DBの中身は変わりません）。

   ```sh
   sudo systemctl stop minimal-web-kit
   sudo -u mwk sh -c 'set -a; . /etc/minimal-web-kit/env; exec /usr/local/bin/wpimport -wxr /tmp/export.xml -uploads /tmp/uploads -db /var/lib/minimal-web-kit/app.db -out /var/lib/minimal-web-kit/app.import.db -report /tmp/report.md -redirects /tmp/redirects.csv -map /var/lib/minimal-web-kit/wpimport-map.csv -new-origin https://example.com'
   ```

   失敗したら作業DBは消え、稼働DBはそのままです。成功したら、8節の「戻す」と同じ手順で、作業DBを稼働DBの位置に置き換えます。置き換える前の稼働DBは、`before-restore-...` のディレクトリに移行前の複製として残ります。

   ```sh
   sudo sh -eu -s -- /var/lib/minimal-web-kit/app.import.db <<'EOF'
   if systemctl is-active --quiet minimal-web-kit; then exit 1; fi
   /usr/local/bin/minimal-web-kit verify-backup "$1"
   cd /var/lib/minimal-web-kit
   saved=before-restore-$(date +%Y%m%d-%H%M%S)
   mkdir -m 0700 "$saved"
   cp -p app.db "$saved/"
   for f in app.db-wal app.db-shm; do
     if [ -e "$f" ]; then cp -p "$f" "$saved/"; fi
   done
   install -m 0600 -o mwk -g mwk "$1" app.db.restore
   rm -f app.db-wal app.db-shm
   mv app.db.restore app.db
   rm "$1"
   EOF
   sudo systemctl start minimal-web-kit
   ```
5. **確かめる**: サーバーを起動し、管理画面で下書きを開いて、本文・画像・カテゴリを確かめてから公開します。取り込んだ記事は全部下書きです。仮の説明の画像は、報告の一覧から説明を書き直します。本文に個人情報が無いかも、人が確かめます（道具は作者とコメントを移しませんが、本文の中は確かめません）。
6. **転送（Cloudflare）**: 移行先を公開して確かめた後に、`-redirects` のCSVを Cloudflare の「一括リダイレクト（Bulk Redirects）」の一覧に読み込み、一括リダイレクトのルールで有効にします（[公式の案内](https://developers.cloudflare.com/rules/url-forwarding/bulk-redirects/)）。全件下書きのまま有効にすると、古いURLが404へ転送されます。先に1件で試し、日本語のスラッグの記事が転送されるか（パーセント符号化の扱い）を確かめます。CSVは見出しの行の無い7列（旧URL・新URL・301・4つの FALSE）で、旧URLはスキームを除いた「ホスト＋パス」です（http と https の両方に当たる）。転送の対象は、公開・予約公開で保護の無い記事と、取り込んだ画像の旧URL（元の画像と、本文に出た小さい版）です。画像は、使っている記事を公開するまで404です。画像を古いサイトとは別のホスト（CDN等）から配信していた場合、そのホストはCloudflareの外のため、画像の行は出しません。無料プランは一括転送の合計が10,000件まで、クエリ（`?p=` 等）は旧URLに書けません（2026-11-03に公式の資料で確認）。
7. **後片付け**: 確かめたら、手元とサーバーの WXR・アップロードのフォルダの複製・試しに使ったDBの複製・移行前の稼働DBを消します。WXRには作者のメールアドレスやコメントが入っています。対応表（`-map`）は、二重に取り込まないための印なので、移行を終えるまで残します。題名や個人情報は書いていません。

- **検証した範囲（2026-11-03、手元）**: お知らせ・固定ページ・画像を有効にしたサーバーで、手順2〜5を手元のコマンドに置き換えて通した（WordPressの公式のテーマの試験データ。お知らせ58件・固定ページ21件・画像17枚。取り込みは0.9秒、道具のメモリの最大値は44MB）。稼働DBの中身が変わらないこと、置き換えた後にサーバーがエラーなく起動すること、管理画面から公開した記事・固定ページの表示、転送のCSVの転送先が公開した記事で200になることを確かめた。systemd・sudo・ファイルの所有者、Cloudflareの一括転送、日本語のスラッグの転送、実際のサイトの書き出し、大きな写真（数MB）の多いサイトでのメモリは確かめていない（画像は1枚ずつ加工するため、メモリは最大の1枚で決まる）。
- 同じ対応表のパスがあると、道具は止まります。誤って2回取り込んだときは、移行前の複製へ戻します。途中からの再開や、後日の差分の追加はできません。
- 道具の報告・CSV・対応表・標準出力には、取り込んだ記事の題名と、外した画像の旧URL・説明のほかは、本文も個人情報も書きません。取り込まなかった記事は題名も書きません。
