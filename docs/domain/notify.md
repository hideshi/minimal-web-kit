# ドメインモデル: 運営者への通知（internal/notify）

運営者への通知の今の仕様の正本です。経緯は [notify-plan.md](../plans/notify-plan.md) にあります。不変条件の番号（`NOTIFY-INV-n`）の扱いは `docs/domain/auth.md` と同じです。

複数のサイトを運営するとき、お問い合わせの到着や管理者の認証に関わる出来事に、サイトごとの管理画面を見に行かずに気づくための仕組みです。気づくだけにして、読む・対応するのは各サイトの管理画面で行います。Coreの部品で、`NOTIFY_WEBHOOK_URL_FILE` を設定したときだけ動きます。

## 1. 用語

| 用語 | 英語名 | 意味 |
|---|---|---|
| 通知 | `Notifier` | ログから出来事を数え、まとめて Webhook へ送る仕組み |
| Webhook の URL | `NOTIFY_WEBHOOK_URL_FILE` | 送り先。この環境変数が指す、所有者だけが読めるファイルに1行で書く。Slack の Incoming Webhook か Discord の Webhook |
| 拾うログの表 | `rules` | 数えるログのメッセージの名前と、属性の条件の表 |
| 見張り役 | `Handler` | ログの出口（`slog.Handler`）を包み、表にあるログを数える |
| 見回り | `tick` | 1分ごとに件数を取り出し、送る文を作る |

## 2. 知らせる出来事

| 種類 | 元のログ | 送り方 |
|---|---|---|
| お問い合わせの新着 | `contact submission`（属性 `result` が `saved`） | 1時間ごとにまとめる。0件なら送らない。運営者が後から手でスパムにするものも含む |
| パスキーの登録・削除・無効化 | `auth passkey registered`・`auth passkey deleted`・`auth passkey disabled` | すぐ。削除は、本人と管理者の操作を区別しない |
| アカウントの作成・削除・権限の変更・停止・再開 | `auth account created`・`deleted`・`role changed`・`disabled`・`enabled` | すぐ |
| 登録URLの発行 | `auth setup url issued` | すぐ。パスキーを消して発行する場合も含む |
| AIエージェントの接続の承認 | `auth agent approved` | すぐ |
| MCPのクライアントの追加・変更、設定の変更 | `mcp client added`・`mcp client changed`・`mcp setting changed` | すぐ |
| 認証の拒否の連続 | `auth login rejected`・`auth passkey registration rejected` | 直近10分で10件以上で1通。続く間は10分に1通まで |
| 予期しない停止 | `panic recovered` | すぐ |
| 起動 | — | 起動から最初の見回り（1分後）まで動いていたとき1通 |

- 「すぐ」は、1分ごとの見回りで送る。同じ種類が重なれば、件数を足して1通にする。
- サーバーの停止のときは、まとめ待ちの件数を5秒以内に送ってから止まる。

## 3. 不変条件

| 番号 | 条件 | 守る場所 |
|---|---|---|
| NOTIFY-INV-1 | 送る文は、サイト名・出来事の種類・件数・時間の幅・固定の案内文だけで作る。ログの属性の値を文に入れない | `tick`・`immediate` |
| NOTIFY-INV-2 | 送り先は、`https://hooks.slack.com/services/…` か `https://discord.com/api/webhooks/…` の形に、解析した結果で合う URL だけ（ポート・利用者情報・クエリ・断片・`..` を許さない）。違えば起動を止める。送るときは、その URL との完全一致だけを通し、転送に従わない | `parseURL`・`federation.Client` |
| NOTIFY-INV-3 | URL は所有者だけが読めるファイルから読む。URL・ファイルのパス・応答の本文・送信のエラーの文を、ログとエラーに出さない | `New`・`logFailure` |
| NOTIFY-INV-4 | 送信の失敗・遅れ・待ち行列の満杯・通知のゴルーチンの panic で、要求の処理やログの出力を止めたり遅らせたりしない。数えるときだけ短くロックを持つ | `observe`・`tick`・`recoverPanic` |
| NOTIFY-INV-5 | 設定しなければ、外へ通信しない。サブコマンドでは動かず、設定も検査しない | `New`・`main` |
| NOTIFY-INV-6 | 拾うログの名前は固定の表で持ち、表の名前はすべてコードに実在する | `rules`・`Messages` |
| NOTIFY-INV-7 | Slack の文は `&`・`<`・`>` を置き換え、Discord は `allowed_mentions` で呼び出しを効かせない | `payload` |
| NOTIFY-INV-8 | 通知自身のログ（`notify` で始まる名前）は数えない | `rules` |
| NOTIFY-INV-9 | 通知を有効にするときは、`SITE_NAME` が設定されている。無ければ起動を止める | `New`・`ConfiguredSiteName` |

## 4. 送信

- 送る形: Slack は `{"text": …}`、Discord は `{"content": …, "allowed_mentions": {"parse": []}}`。
- 1本のゴルーチンで1通ずつ送る。待ち行列は32通で、満杯なら新しい文を捨てる。
- タイムアウトは10秒。429・5xx・接続の失敗では、5秒後に1回だけ送り直す。停止の途中は送り直さず、送り直しの待ちの最中に停止の合図が来たら、待ちをやめる。転送（3xx）には従わず、送り直さない。404 が返ったら、再起動まで送らない。
- 「1時間ごと」「10分に1通まで」の区切りは、見回りの起きる時刻の揺れを見込み、30秒早くても区切りに達したとみなす。
- 失敗の記録は `notify send failed`（応答のコードか `connection`）、`notify webhook gone`、`notify stopped` だけ。

## 5. 他の機能との関係

- 各機能のログのメッセージの名前を変えると、通知が止まる。`TestRules_MessagesExistInCode` が、表の名前がコードにあることを確かめる。名前を変えるときは、表も直す。
- 判定に使う属性（`result`）は、記録の最上位の属性だけを見る。`logger.With` で先に束ねた属性や、グループの中の属性は使わない。
- 通知は機能が logger を受け取る前に包む必要がある。`cmd/server` の `main` で、サブコマンドの無い起動のときだけ包み、`slog.SetDefault` にも渡す。

## 6. 守らない範囲

- 予期しない停止（強制終了、OSの停止）では、まとめ待ちの件数（最大1時間分）が失われる。お問い合わせ自体はDBに残る。通常の停止でも、送り先が5秒以内に応答しないか、1回目の送信に失敗すると、まとめ待ちの件数は送られない。
- 1分もたずに落ちる再起動の繰り返しは、起動の通知が出ない。systemd 等で見張る。
- サブコマンド（`auth-recover`・`agent-client` 等）での操作は知らせない。サーバーに入れる人の操作は対象外。
- 共通認証で利用する側（`federatedauth`）のログインの拒否は数えない。
- エラーの件数による通知は無い（閲覧者の切断でも Error のログが出る画面があるため）。
- 手でスパムにしたお問い合わせは区別できない。
- 複製の疑いを見つけたのにパスキーを無効にできなかった（DBの障害等）ときは、すぐの通知は出ない。そのログイン自体は拒否され、拒否が続けば「認証の拒否の連続」で知らせる。サーバーのログの `auth disable passkey failed` で確かめる。
- 送り先のサービス（Slack・Discord）に残る記録（サイト名、出来事の種類と件数、時刻）は、そのサービスの規約で扱われる。
- 送り先は1つだけ。メール、ntfy、Discord のスレッドの指定、出来事ごとの選択、しきい値の設定、通知の履歴の保存は無い。
- 管理者のログインの成功は知らせない。
