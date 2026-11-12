# 運営者への通知（Webhook） 実装計画

状態: 2026-11-13、利用者が承認境界3（Slack・Discord への送信）、件数のメモリでの集計、管理者のログインの成功を知らせないことを承認した。外部のモデル（Fable 5.2）の計画のレビューを受け、10節のとおり直した。利用者がレビュー後の変更（エラーの件数の通知を外す、`SITE_NAME` を必須にする、出来事を足す）を承認し、実装した。実装では、接続の失敗も1回だけ送り直す対象にした。

対象の時点: `d20c406` から。

正本: 今の仕様は [domain/notify.md](../domain/notify.md)。

## 1. 目的と範囲

複数のサイトを運営するとき、お問い合わせの到着や、管理者の認証に関わる出来事に、サイトごとの管理画面を見に行かずに気づけるようにする。気づくだけにして、読む・対応するのは今までどおり各サイトの管理画面で行う。

- 送り先は、Slack の Incoming Webhook か、Discord の Webhook のどちらか1つ。複数のサイトが同じ URL を使えば、1つのチャンネルに集まる。
- 送る文は、サイト名・出来事の種類・件数・時間の幅・固定の案内文だけにする。個人の情報、本文、IP アドレス、アカウントの番号、パスキーの名前は載せない。
- 設定しなければ、外への通信は一切しない。

作らないもの: メール、ntfy、その他の送り先、Discord のスレッドの指定、送り先ごとの出来事の選択、しきい値の設定、通知の履歴の保存、通知からの操作（既読にする等）、監査ログ、管理者のログインの成功の通知、エラーの件数による通知（10節の H1）。

## 2. 拾う出来事

出来事は、各機能がすでに出しているログ（slog）から、決まった名前の表で拾う。機能のコードは、AIエージェントの接続の承認のログを1行足す以外は変えない。

| 種類 | 元のログ（メッセージ） | 条件 | 送り方 |
|---|---|---|---|
| お問い合わせの新着 | `contact submission` | 属性 `result` が `saved` のときだけ | 1時間ごとにまとめる。0件なら送らない |
| パスキーの登録 | `auth passkey registered` | なし | すぐ |
| パスキーの削除 | `auth passkey deleted` | なし。本人の削除と、管理者による他人の分の削除を区別しない | すぐ |
| パスキーの無効化（複製の疑い） | `auth passkey disabled` | なし | すぐ |
| アカウントの作成・削除 | `auth account created`・`auth account deleted` | なし | すぐ |
| アカウントの権限の変更・停止・再開 | `auth account role changed`・`auth account disabled`・`auth account enabled` | なし | すぐ |
| 登録URLの発行 | `auth setup url issued` | なし。パスキーを消して発行する場合も含む | すぐ |
| AIエージェントの接続の承認 | `auth agent approved`（新規） | なし | すぐ |
| MCPのクライアントの追加・変更 | `mcp client added`・`mcp client changed` | なし | すぐ |
| MCPの設定の変更 | `mcp setting changed` | なし | すぐ |
| 認証の拒否の連続 | `auth login rejected`・`auth passkey registration rejected` | 10分に10件以上 | 超えたら1通。続く間は10分に1通まで |
| 予期しない停止 | `panic recovered` | 1件で | すぐ |
| 起動 | — | 起動から最初の見回り（1分後）まで動いていたとき | 1通 |

- 「すぐ」は、1分ごとの見回りで送る。1分の間に同じ種類が重なれば、件数を足して1通にする。
- `auth agent approved` は、`features/auth/agent_http.go` の `approve` で、承認の保存と戻り先の作成がどちらも成功した後に出す。属性は要求の番号だけにし、クライアントの識別子や戻り先を出さない。
- 共通認証で利用する側（`federatedauth`）の拒否は数えない。`federated login rejected` は認証基盤との交換の失敗で、基盤の不調でも出るため。
- お問い合わせの新着には、運営者が後から手でスパムにするものも含まれる。文に「スパムを含む場合があります」と添える。

文の例:

- `[サイト名] お問い合わせの新着 3件（10:00〜11:00）。スパムを含む場合があります。管理画面で確かめてください。`
- `[サイト名] パスキーが登録されました（1件）。覚えがなければ、管理画面で確かめてください。`
- `[サイト名] 起動しました。`

## 3. 仕組み

```
各機能 ─ slog ─▶ 見張り役（Handler）─▶ メモリの集計 ─ 1分ごと ─▶ 送り役 ─ HTTPS ─▶ Slack / Discord
                 └─▶ 元の出力（標準出力の JSON）はそのまま
```

- `internal/notify` を新しく作る（Core）。
- **組み込む位置**: `cmd/server/main.go` で、サブコマンドの無い起動（サーバー）のときだけ、`setupFeatures` より前に logger を包む。各機能は生成のときに logger を受け取るため、後から包むと見えない。`slog.SetDefault` にも包んだ logger を渡す。サブコマンド（`backup`・`auth-recover`・`agent-client` 等）では、通知を動かさず、設定の検査もしない。
- **見張り役**: `slog.Handler` を包む。
  - `Handle` は、ロックして数える → ロックを外す → 内側の `Handle` を呼ぶ、の順にする。ロックを持ったまま内側を呼ばない。
  - 判定に使う属性（`result`）は、記録の最上位の属性だけを見る。`WithAttrs`・`WithGroup` は内側に渡して包み直すだけにし、先に束ねた属性は判定に使わない。
  - `Enabled` は内側に任せる。
- **集計**: メモリだけに持つ。DB は変えない。
- **見回り**: 1分ごとのゴルーチン。ロックの中で件数を取り出して0に戻し、文の作成と待ち行列への投入はロックの外で行う。投入は待たない（満杯なら捨てる）。
- **送り役**: 1本のゴルーチンで、1通ずつ送る（Slack の目安の毎秒1通を超えない）。待ち行列は32通。
- **panic**: 見回りと送り役は、panic を `recover` して自分だけ止まる。要求の処理を巻き込まない。
- **停止**: `app.Server.Run` が戻った直後に、まとめ待ちの件数を5秒以内に送ってから止まる。Run が失敗で戻ったときも呼ぶ。HTTPの停止（最大10秒）と合わせて最大15秒になるため、systemd の停止の待ち時間との関係を `docs/deploy.md` に書く。予期しない停止では、まとめ待ちの件数（最大1時間分）が失われる。お問い合わせ自体は DB に残る。

## 4. 送り先と送信

- **設定**: `NOTIFY_WEBHOOK_URL_FILE` の1つ。`internal/app/config.go` の `LoadConfig` で読む。指すファイルに URL を1行で書く。読み込みは `federation.SecretFile`（通常のファイル、所有者だけが読める、64KiB以下）を使う。
- **サイト名**: 通知を有効にするときは `SITE_NAME` を必須にする。既定の名前のままでは、同じチャンネルで複数のサイトを区別できないため。
- **URL の検査**: 接頭辞の文字列の比較ではなく、解析した結果で確かめる。どれかに外れたら起動を止め、エラーとログに URL を含めない。
  1. ファイルの中身の前後の空白を除き、残りに空白・改行・制御文字があれば断る。
  2. `url.Parse` の結果が、`Scheme` が `https`、`User` が無い、`Opaque`・`RawQuery`・`Fragment` が空、`Host` がポート無しで次の完全一致（大文字小文字も区別する）。
  3. `EscapedPath()` が、Slack は `^/services/[A-Za-z0-9]+/[A-Za-z0-9]+/[A-Za-z0-9]+$`、Discord は `^/api/webhooks/[0-9]+/[A-Za-z0-9_-]+$` に合う。

  | ホスト | 形 |
  |---|---|
  | `hooks.slack.com` | Slack |
  | `discord.com` | Discord |

  旧ドメインの `discordapp.com` と、GovSlack の `slack-gov.com` は、公式の資料で確かめられなかったため、許さない。
- **送る形**:
  - Slack: `{"text": "…"}`。文の中の `&`・`<`・`>` を `&amp;`・`&lt;`・`&gt;` に置き換える。
  - Discord: `{"content": "…", "allowed_mentions": {"parse": []}}`。呼び出し（`@everyone` 等）を効かせない。
- **HTTP**: 既存の `federation.Client` を使う（検査した URL との完全一致、HTTPS だけ、転送の拒否、本文の上限）。タイムアウトは10秒。土台は `http.DefaultTransport` で、環境変数のプロキシの設定に従う。成功は 2xx。
- **送り直し**: 429 と 5xx のときだけ、5秒後に1回だけ送り直す。それでも失敗したら、その通は捨てる。
- **送り先が消えたとき**: 404 が返ったら、それ以降は再起動まで送らない（Discord は 404 の後に同じ Webhook を使い続けないよう求めている）。ログに1回だけ残す。
- **失敗のログ**: `notify send failed` に、応答のコードか、接続の失敗の分類だけを付ける。URL・応答の本文・送る文・`err.Error()` は出さない（`*url.Error` の文には URL が入る）。このログは見張り役が数えない。
- **文の長さ**: Discord の上限（2,000文字）より十分短い文だけを作る。
- **gosec**: 変数の URL への要求の指摘には、規則の番号と理由を付けて `#nosec` を書く。

### 確かめた公式の資料（2026-11-13）

| 項目 | 確かめたこと | 出典 |
|---|---|---|
| Slack の URL | 例として `https://hooks.slack.com/services/T…/B…/…`。接頭辞の規則は書かれていない | docs.slack.dev の Sending messages using incoming webhooks |
| Slack の形と成功 | `{"text": …}` を JSON で POST。成功は多くの場合 200 と本文 `ok` | 同上 |
| Slack の失敗 | 400・403・404 等と、本文の短いエラー名（`invalid_payload` 等）。直さずに送り直さない | 同上 |
| Slack の置き換え | `&`・`<`・`>` だけを `&amp;`・`&lt;`・`&gt;` にする。文全体を符号化しない | docs.slack.dev の Formatting message text |
| Discord の形と成功 | `POST /webhooks/{id}/{token}`、`content` は2,000文字まで。`wait` の既定は false で、そのときは 204 | docs.discord.com の Webhook |
| Discord の 429 | `Retry-After` の見出しか、本文の `retry_after`（秒）に従う。404 の後は同じ Webhook を使わない | docs.discord.com の Rate Limits |

確かめられなかったこと: Slack の Webhook の回数の上限、Discord の実行の URL のホスト（`discord.com/api` は資料の中で明記されていない）、`allowed_mentions` の `parse: []` の原文（実装の前に読み直す）、`discordapp.com` が今も使えるか、Slack・Discord の Webhook の URL の文字の種類（3節の正規表現は実際の URL の形からの推測。実装の前に確かめる）。

## 5. 不変条件（案）

守る場所は、実装の後に識別子で埋める（`test/domain_refs_test.go`）。

| 番号 | 条件 |
|---|---|
| NOTIFY-INV-1 | 送る文は、サイト名・出来事の種類・件数・時間の幅・固定の案内文だけで作る。ログの属性の値を文に入れない |
| NOTIFY-INV-2 | 送り先は、4節の検査に合う HTTPS の URL だけ。違えば起動を止める。転送には従わない |
| NOTIFY-INV-3 | URL は所有者だけが読めるファイルから読み、URL・応答の本文・送信のエラーの文をログとエラーに出さない |
| NOTIFY-INV-4 | 送信の失敗・遅れ・待ち行列の満杯・通知のゴルーチンの panic で、要求の処理やログの出力を止めたり遅らせたりしない |
| NOTIFY-INV-5 | 設定しなければ、外へ通信しない。サブコマンドでは動かない |
| NOTIFY-INV-6 | 拾うログの名前は固定の表で持ち、表の名前はすべてコードに実在する |
| NOTIFY-INV-7 | Slack の文は `&`・`<`・`>` を置き換え、Discord は呼び出しを効かせない |
| NOTIFY-INV-8 | 通知自身のログは数えない |
| NOTIFY-INV-9 | 通知を有効にするときは、`SITE_NAME` が設定されている |

## 6. 承認境界とプライバシー

| 境界 | 判定 |
|---|---|
| 1 `go.mod` | 当たらない。標準の `net/http`・`encoding/json`・`log/slog` と既存の `internal/federation` だけで作る |
| 2 保存データ | 当たらない。集計はメモリだけ。設定の URL は秘密のファイルで、台帳の「DBの外のファイル」に書き足す |
| 3 外部への通信 | 当たる。承認済み（2026-11-13） |
| 4・5 | 当たらない |

- Slack・Discord に個人の情報は渡らない。`minimal-web-kit-privacy` でポリシーの変更が要らないことを確かめる。サイト名と出来事の件数が外部のサービスに残ることは、`docs/deploy.md` に書く。

## 7. 試験

- 文に属性の値が出ないこと: お問い合わせの本文・返信先・パスキーの名前等をカナリアにしてログを流し、送る文に出ないこと（INV-1）。
- URL の検査（INV-2・3）: `http://`、別のホスト、`hooks.slack.com.example`、`HOOKS.slack.com`、`:443`、`https://x@hooks.slack.com/…`、`/services/../x`、`%2e%2e`、`?wait=true`、`#x`、末尾の改行が2つ、`\r` で、起動が止まり、エラーに URL が出ないこと。
- 転送の応答（302）に従わないこと（INV-2）。
- 権限の緩いファイルを断ること（INV-3）。
- 接続の失敗のときも、ログに URL が出ないこと（INV-3）。
- 送り先が応答しない・遅い・待ち行列が満杯・見回りが panic するときも、ログの出力がすぐ返ること（INV-4）。
- 未設定で、送り役が動かず HTTP の通信が起きないこと。サブコマンドで設定の誤りがあっても止まらないこと（INV-5）。
- 表のメッセージの名前が、`features/`・`internal/` のコードに文字列として実在すること（INV-6）。
- Slack・Discord の送る JSON の形と置き換え。サイト名に `<!channel>` と `@everyone` を入れたカナリア（INV-7）。
- 送信の失敗のログを流しても、件数が増えないこと（INV-8）。
- `SITE_NAME` が無いと起動が止まること（INV-9）。
- `contact submission` の `result` を `With` で先に束ねたら数えないこと（3節の決まり）。
- しきい値・まとめ・1分の見回りは、時刻を差し替えて確かめる。
- 守りを外して、対応する試験が落ちることを確かめる（置き換え、転送、権限、循環、表の実在、URL の検査）。

## 8. 文書と機械の試験

- `docs/domain/notify.md` を新規に作る。「用語」「不変条件」「守らない範囲」の節は必須（`test/domain_format_test.go`）。不変条件の表は「番号・条件・守る場所」の3列で、守る場所に実在する識別子を書く（`test/domain_refs_test.go`）。各 `NOTIFY-INV-n` を試験の関数のコメントから参照する（`test/domain_test.go`）。
- 守らない範囲に書くこと: サブコマンドの操作（サーバーに入れる人の操作）、1分もたずに落ちる再起動の繰り返し、予期しない停止で失われる件数、共通認証で利用する側の拒否、手で付けるスパムの区別、送り先のサービスに残る記録。
- `docs/ui/use-cases.md` に、運営者が複数のサイトの出来事に1か所で気づくユースケースを足し、試験から参照する。アクターは、既存の「運用者」（サーバーでコマンドを実行する人）の定義を広げずに、合うアクターを選ぶ。
- `docs/data-inventory.md` の「DBの外のファイル」に、`NOTIFY_WEBHOOK_URL_FILE` を足す。
- `docs/deploy.md` に、Slack の Incoming Webhook の作り方、ファイルの置き方と権限、`SITE_NAME` が要ること、確かめ方、停止の待ち時間、外部のサービスに残るもの、止め方を足す。
- README の環境変数の表（`test/readme_test.go`）、構成の図への `notify` の追加、機能の説明。
- `docs/domain/auth.md` に `auth agent approved` のログを書く必要があるかを確かめる。
- `AGENTS.md` の承認境界の例外の記述は変えない（外部へのスクリプトの読み込みではないため）。

## 9. 実装の順とコミット

1. 計画（この文書）
2. `internal/notify`（見張り役・集計・送り役・URL の検査）と試験
3. `cmd/server` での組み込み、`LoadConfig`、`auth agent approved` のログ、起動の通知
4. 文書（domain・use-cases・data-inventory・deploy・README）

## 10. 計画のレビューで直したこと（2026-11-13、Fable 5.2）

| 指摘 | 直したこと |
|---|---|
| H1 公開側の DB の読み取りは、利用者の切断（`context.Canceled`）も Error にしている。エラーの件数のしきい値は、クローラーの切断で鳴る | エラーの件数による通知を初版から外した。`panic recovered` と起動だけを残す |
| H2 `auth passkey deleted` は本人と管理者の2経路で出る。パスキーを消して登録URLを発行しても出ない | 区別しないと明記し、登録URLの発行に含まれると書いた |
| H3 権限の変更・停止・再開、パスキーの登録の拒否が漏れていた。`federated login rejected` は基盤の不調でも出る | 前の2つを表に足した。共通認証で利用する側の拒否は数えないと書いた。サブコマンドは範囲の外と書いた |
| H4 `auth agent approved` は実在しない | 出す位置と属性を書いた |
| M1 組み込む位置、サブコマンド、停止の送信が決まっていない | 3節に書いた |
| M2 `slog.SetDefault` と、ロックの持ち方 | 3節に書いた |
| M3 `WithAttrs`・`WithGroup` の扱い | 最上位の属性だけで判定すると決め、試験を足した |
| M4 URL を接頭辞で比べると `..` や `?` を通す | 解析した結果で確かめる手順にした |
| M5 既存の `federation.Client` を使える。エラーの文に URL が入る | 再利用を書き、INV-3 にエラーの文を足した |
| M6 起動の通知が再起動の繰り返しで連投になる。起動時の Warn の件数は価値が小さい | 最初の見回りで送り、件数を外した |
| M7 機械の試験が求めるものが足りない | 8節に書いた |
| L1・L3〜L6 | スパムの注記、サイト名のカナリア、`SITE_NAME` の必須（INV-9）、捨てた件数と `Retry-After` の削除、通知のゴルーチンの `recover` を取り込んだ |
