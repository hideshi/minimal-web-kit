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

`bin/minimal-web-kit` 1本に、テンプレート・CSS・JS・マイグレーションが埋め込まれます。有効にした機能（`cmd/server/features.go` の登録）もビルド時に決まるため、Site・CMSの有効化はビルドの前に行います。

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

# CMSを有効にした場合
CMS_ORIGIN=https://example.com
CMS_RP_ID=example.com
# 別ドメインのフロントエンドからAPIを読む場合だけ（既定は許可なし）
# CMS_CORS_ORIGINS=https://front.example.com
```

- 値に空白や `&` 等の記号を含む場合は、`"..."` で囲みます（7節でシェルからも読み込むため）。
- 待受は既定の `127.0.0.1:8080` のままにします。外部からの接続はリバースプロキシだけが受けます。`ADDR` で `127.0.0.1` 以外に変える場合は、承認が必要です。
- `CMS_ORIGIN` が https で `SECURE_COOKIE=true` でない場合、`CMS_RP_ID` が `CMS_ORIGIN` のホストと合わない場合は、起動時にエラーで止まります。

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

## 7. CMSの初回登録

サーバー上で、サーバーと同じ設定（同じ `DB_PATH`・`CMS_ORIGIN`）で実行します。Goは不要です。

```sh
sudo -u mwk sh -c 'set -a; . /etc/minimal-web-kit/env; exec /usr/local/bin/minimal-web-kit cms-setup'
```

表示された `https://example.com/admin/setup#token=...` を15分以内に開き、パスキーを登録します。パスキーをすべて失った場合は、同じ形で `cms-recover` を実行します（確認の入力あり）。サーバーの稼働中に実行して構いません。

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

テーブルの追加は起動時に自動で行います（既存のテーブル・データは変更しません）。CMSのログイン状態は再起動後も続きますが、ログインの途中だった操作や、再起動前に開いた管理画面のフォームの送信は失敗します。ページを開き直してください。

## 10. 公開前の確認

- `https://example.com/` が表示され、`http://` はHTTPSへ転送される。
- Cookieが発行される画面（Base版の画面、CMSのログイン後）で、Cookieに `Secure` が付いている（ブラウザの開発者ツールで確認）。
- `http://<サーバーのIP>:8080/` に外部から接続できない。
- CMSを有効にした場合、パスキーで登録・ログインでき、`/news` と `/api/news` が表示される。
- DBのディレクトリが `mwk` 以外から読めない（`ls -ld /var/lib/minimal-web-kit`）。
- プロキシ・journald・バックアップの保持期間を決めてある。
