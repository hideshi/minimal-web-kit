# アクセス解析の計画

状態: 計画のレビュー前（2026-11-01）。対象の時点は `85919c5`。実装の後、今の仕様は `docs/domain/analytics.md` へ移す。

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

## 仕組み

### 有効にする方法

- `features/analytics`、`make add-analytics`。環境変数 `ANALYTICS_GA4_ID` に測定IDを入れる。形式 `^G-[A-Z0-9]{4,20}$` に合わなければ起動を止める。
- 機能を登録しなければ、公開ページのHTML、CSP、通信は今と同じ。

### 画面への組み込み

- 共通の部品 `analytics` を `components.html` に足し、公開のレイアウト（Core、Site、news、catalog、pages）から呼ぶ。お問い合わせと管理画面のレイアウトからは呼ばない。
- 測定IDは `web.EnableAnalytics(id)` で起動時に1回だけ設定し、部品の関数が読む。画面ごとのデータに足さない（全機能の描画に手を入れずに済むため）。未設定なら部品は何も出さない。
- 部品は、案内の `<section hidden>` と、`<script src="/static/js/analytics.js" data-ga4-id="..." defer>` を出す。

### CSP

`SecurityHeaders` で、有効なときだけ、公開の経路に次のCSPを使う。

```text
default-src 'self'; style-src 'self';
script-src 'self' https://www.googletagmanager.com;
connect-src 'self' https://www.googletagmanager.com https://*.google-analytics.com https://*.google.com;
img-src 'self' https://www.googletagmanager.com https://*.google-analytics.com;
form-action 'self'; frame-ancestors 'none'; object-src 'none'; base-uri 'self'
```

- `/admin`、`/contact`、`/mcp`、`/oidc`、`/api`、`/agent` の経路は既定のCSPのまま。
- 除外する経路の判定を1つの関数にする。経路を足したときに緩む向きなので、レイアウトの部品が無ければスクリプトは読まれないことを2重目の守りにする。
- `AllowScripts` は既定のCSPのときだけ置き換えるので、カタログの公開一覧でも解析用のCSPが残り、`'self'` のJSも動く。
- `Referrer-Policy` は緩めない。サイト内の遷移の参照元は取れない。

### 同意の動き（`internal/web/static/js/analytics.js`）

インラインのJSは使わない。

1. `localStorage` の `mwk-analytics-consent` を読む。`granted` なら読み込み、`denied` なら何もしない。無ければ案内を出す。
2. 同意: 保存してから `gtag.js` を読み込む。
3. 拒否: 保存して案内を閉じる。
4. `/privacy` の「Cookieの設定」: 案内をもう一度出す。同意を取り消したら、`window['ga-disable-<ID>'] = true` にして、`_ga` で始まるCookieを消し、ページを読み直す。
5. `localStorage` が使えないときは、未回答として扱い、計測しない。
6. `config` には `allow_google_signals: false` と `allow_ad_personalization_signals: false` を渡す。

JSが動かなければ案内は `hidden` のままで、計測もしない。

### 案内の画面

- `<section aria-label="Cookieの設定">` を本文の後に置く。ページの操作を妨げず、フォーカスも奪わない。
- ボタンは「同意する」「同意しない」の2つで、同じ見た目にする。`/privacy` へのリンクを添える。
- CSSは `style.css` に足す。ファイルは増やさない。

文面の案:

> このサイトは、利用状況を知るためにGoogle AnalyticsのCookieを使います。同意した場合だけ、閲覧の情報がGoogleへ送られます。

### プライバシー

- `PrivacyFeature` で公開の説明を足す（`Internal` なし。閲覧者の情報なので）。項目: GoogleのCookie、閲覧の情報、送り先、目的、同意の保存。
- `privacy.html` に「Cookieの設定」のボタンを、有効なときだけ出す。
- `docs/data-inventory.md` に節を足す。サーバーには保存しない。
- プライバシーのスキルに、外部送信規律と外国への提供の確認を、運営者の確認項目として足す。

### 静的書き出し

`export-static` は全ページに同じヘッダーを求める。書き出す公開ページは全て同じCSPになるはずだが、実装で確かめる。

## 守らない範囲

- 法令への適合は保証しない。EU等の要件を満たすかは運営者が確かめる。
- 同意を拒否した人、案内を操作しない人は数えない。
- 同意の後に読み込んだ `gtag.js` の中身はキットの管理外で、そのページでは外部のスクリプトが動く。

## 不変条件の案（`ANALYTICS-INV-n`）

1. 機能を登録しなければ、HTML、CSP、JSの読み込みは変わらない。
2. 同意するまで、Googleの配信元へ通信しない。
3. 管理画面、ログイン画面、お問い合わせの画面に、解析の部品もCSPの緩和も出ない。
4. 拒否の後は読み込まず、案内も再び出さない。
5. 同意の取り消しで、以後の計測を止め、`_ga` のCookieを消す。
6. 測定IDの形式が誤っていれば起動しない。
7. サーバーはCookieを発行せず、同意の結果を受け取らない。

## テスト

- 既存の `test/template_test.go` の、JSの許可の一覧に足す。レイアウトごとに部品の有無を固定する。
- 無効のときの回帰: 公開ページのHTMLとCSPが今と同じ。
- 有効のときのCSP: 公開の経路だけが変わり、除外の経路は `script-src 'none'` のまま。
- `test/browser/`: 外部への通信を遮断して記録し、同意前に0件、同意でgtagへの要求が1件、拒否で0件、再訪の動き、取り消しの動き、キーボード操作、`localStorage` が使えないときを確かめる。
- 実機の確認（受け入れ）: 実際の測定IDで、GA4のリアルタイムに表示が出ること、CSPの違反が出ないこと。

## 進め方

1. この計画のレビューを受ける（外部通信とCSPに触れるため）。
2. 実装、仕様、台帳、AGENTS.md の例外の追記。
3. 完成のレビュー。
4. 実機の確認は、測定IDを持つ利用者が行う。紹介サイトには入れない。
