# アクセス解析の計画

状態: 計画のレビューを反映済み（2026-11-01）。対象の時点は `85919c5`。実装の後、今の仕様は `docs/domain/analytics.md` へ移す。

## 目的

他社向けのホームページやLPで、Google Analytics 4（GA4）を入れたいと言われたときに使える任意の機能を用意する。標準では何も入らず、紹介サイトでは有効にしない。

## 確定した判断（利用者の承認済み）

- 対象はGA4だけ。他のサービスは必要になってから足す。
- 初版から同意の画面を入れる。同意するまで `gtag.js` を読み込まず、Googleへの通信もCookieも発生させない。
- 同意と拒否は同じ大きさのボタンにする。拒否の後は案内を再び出さない。`/privacy` からいつでも変えられる。
- Consent Modeは使わない。
- 管理画面、ログイン画面、お問い合わせの画面は計測しない。
- 承認境界の2（ブラウザの保存項目）、3（外部通信）、4（JSファイル）、5（CSPの緩和）は承認済み。

## 公式の資料で確かめたこと

`https://developers.google.com/tag-platform/security/guides/csp`（2026-09-18更新）の、広告の機能を使わないGAの指定:

| 指令 | 値 |
|---|---|
| `script-src-elem` | `https://www.googletagmanager.com` |
| `img-src` | `https://www.googletagmanager.com https://*.google-analytics.com` |
| `connect-src` | `https://www.googletagmanager.com https://*.google-analytics.com https://*.google.com` |

確かめられなかったこと:

- 同じオリジンの外部JSから初期化してよいとは明記されていない（禁止もされていない）。インラインのJSを使わない方針なので、実機の確認を受け入れの条件にする。
- 外部送信規律の総務省のページは、取得で文字化けして読めなかった。キットは法的な判断をせず、運営者が確かめる項目にする。

## 仕組み（計画レビューを反映）

### 有効にする方法

- `features/analytics`、`make add-analytics`。環境変数 `ANALYTICS_GA4_ID` に測定IDを入れる。登録したのに空か、形式 `^G-[A-Z0-9]{4,20}$` に合わなければ起動を止める。
- 機能は `features.AnalyticsProvider` で設定を返し、`BuildRoutes` が配る。`Init` を通らない `export-static` でも同じ検査が走る。

### 出す画面は応答ごとに決める

レイアウトでは決めない。お問い合わせはSiteのレイアウトを使うことがあるため。

- 計測する画面の処理が `AddAnalytics(w, data)` を明示的に呼ぶ。これが部品のデータとCSPの緩和を同時に行う。呼ばない画面は、部品もCSPも変わらない。経路の除外の一覧は持たない。
- 呼ぶ画面: トップ（CoreとSite）、`/privacy`、お知らせの一覧と記事、カタログの一覧と項目、固定ページ。
- 呼ばない画面: お問い合わせ、管理画面、ログイン、404、削除の確認。
- 設定はインスタンスごとに持つ（`web.PublicNavigation`・`Handler`・Site）。全体の変数にしない。

### CSP

`AllowAnalytics(w)` は、応答のCSPが既定か `scriptCSP` のときだけ、次に置き換える。

```text
default-src 'self'; style-src 'self';
script-src 'self' https://www.googletagmanager.com;
connect-src 'self' https://www.googletagmanager.com https://*.google-analytics.com https://*.google.com;
img-src 'self' https://www.googletagmanager.com https://*.google-analytics.com;
form-action 'self'; frame-ancestors 'none'; object-src 'none'; base-uri 'self';
```

`Referrer-Policy` は緩めない。サイト内の遷移の参照元は取れない。

### 同意の動き（`internal/web/static/js/analytics.js`）

インラインのJSは使わない。部品は `{{asset "js/analytics.js"}}` で読み込む。

1. `localStorage` の `mwk-analytics-consent` を読む。値は `granted`・`denied` だけを認める。保存領域が使えなければ、案内を出さず計測もしない。
2. 同意: 保存に成功してから、`dataLayer` と `config` を準備し、`gtag.js` を1度だけ挿入する。保存に失敗したら読み込まず、案内の中に伝える。
3. 拒否: 保存して案内を閉じる。
4. 取り消し: `ga-disable-<ID>` を立て、`denied` を保存し、`_ga` で始まるCookieを消して読み直す。保存に失敗したら完了と言わない。
5. `config` には `allow_google_signals: false`、`allow_ad_personalization_signals: false`、`cookie_domain: "none"` を渡す。ドメインを指定しないCookieにして、消す時に同じ指定を使うため（完成レビューで変更）。
6. 複数タブ: `storage` イベントで他のタブの回答を受ける。`denied` なら止めて読み直し、`granted` なら読み込み、どちらも案内を閉じる。BFCacheからの復帰は `pageshow` で読み直す。

JSが動かなければ案内も設定のボタンも `hidden` のままで、計測もしない。

### 案内の画面

- ページの先頭（スキップリンクの次）の `<section>` に見出し `<h2>` を置く。本文に重ねない（完成レビューで変更。`sticky` は狭い画面で本文を覆った）。
- 初回の自動表示ではフォーカスを動かさない。`/privacy` の「Cookieの設定」から開いたときは案内へ移し、閉じたらボタンへ戻す。
- ボタンは「同意する」「同意しない」の2つで、同じ見た目にする。今の選択を文で示す。

文面の案:

> このサイトは、利用状況を知るためにGoogle AnalyticsのCookieを使います。同意した場合だけ、閲覧の情報がGoogleへ送られます。

### プライバシー

- `PrivacyFeature` で公開の説明を足す（閲覧者の情報なので `Internal` なし）。
- `privacy.html` に「Cookieの設定」のボタンを、有効な応答だけ出す。
- `docs/data-inventory.md` に節を足す。サーバーには保存しない。
- プライバシーのスキルに、外部送信規律と外国への提供の確認を足す。

### 静的書き出し

解析の機能は経路もDBも持たないので、書き出しを許す。`analytics.js` を書き出しの対象に足す。`_headers` は全パス共通なので、404にも解析用のCSPが付く（スクリプトは読まれない）。

### 規約

`AGENTS.md` の例外、`docs/ui-guidelines.md`（素のJSは保存領域と通信を使わない、の例外）、`docs/components.md`、監査と実装のスキル、READMEを同じ変更で直す。

## 守らない範囲

- 法令への適合は保証しない。EU等の要件を満たすかは運営者が確かめる。
- 同意を拒否した人、案内を操作しない人は数えない。
- 同意の後に読み込んだ `gtag.js` の中身はキットの管理外で、そのページでは外部のスクリプトが動く。
- ブラウザが保存領域を消すと、案内が再び出ることがある。
- Google広告との連携は対象外。使うならCSPの許可先を足す必要がある。

## 不変条件の案（`ANALYTICS-INV-n`）

1. 機能を登録しなければ、HTML、CSP、JSの読み込みは変わらない。
2. 同意するまで、Googleの配信元へ通信しない。
3. 管理画面、ログイン画面、お問い合わせの画面に、解析の部品もCSPの緩和も出ない。
4. 拒否の後は読み込まず、案内も再び出さない。
5. 同意の取り消しで、以後の計測を止め、`_ga` のCookieを消す。
6. 測定IDの形式が誤っていれば起動しない。
7. サーバーはCookieを発行せず、同意の結果を受け取らない。

## テスト

- テンプレート検査、無効のときの回帰、有効のときの画面ごとのCSPと部品（Site＋お問い合わせの `/contact` に出ないことを含む）、書き出し。
- `test/browser/`: 外部への通信を遮断し、スクリプトの挿入を数える。同意前0、同意で1、拒否で0、再訪、取り消し、他のタブの取り消し、保存領域が使えないとき。Chromeが要るのでCIの `go test` では走らない。
- 実機の確認（受け入れ）: 実際の測定IDで、GA4のリアルタイムの表示、CSPの違反が無いこと、取り消しでCookieが消え送信が止まること。測定IDを持つ利用者が行う。

## 進め方

1. 実装、仕様、台帳、規約の更新。
2. 完成のレビュー。
3. 紹介サイトには入れない。
