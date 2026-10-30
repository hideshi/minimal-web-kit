# ドメインモデル: 共通認証（features/auth/oidcop・features/federatedauth）

自社の複数のシステムで、アカウントとログインを共通にする仕組みの今の仕様の正本です。提供側の不変条件は [管理者認証の仕様](auth.md) の AUTH-INV-17・AUTH-INV-18 にあります。設計の経緯は `docs/federated-auth-detail-plan.md`、保存する列は `docs/data-inventory.md` の4節と9節にあります。

## 1. 用語

| 用語 | 意味 |
|---|---|
| 認証基盤 | アカウントとパスキーを持ち、本人を確認するシステム（`features/auth/oidcop`） |
| 連携先 | 認証基盤でログインするシステム（`features/federatedauth`、または同じ契約を実装した他のシステム） |
| 世代 | `account_epoch`・`client_epoch`・`grant_epoch`。ログイン時に固定する |
| 照会 | 連携先が保護された要求のたびに、固定した世代が今も有効かを認証基盤に問うこと（`POST /oidc/check`） |
| 確認不能 | 照会の結果が、正しい形式の有効・無効のどちらでもないこと |

## 2. ログインの流れ

```mermaid
sequenceDiagram
    actor U as 利用者のブラウザ
    participant B as 連携先B
    participant A as 認証基盤A

    U->>B: 保護された画面を開く
    B-->>U: ログインの案内へ移す
    U->>B: 「ログインする」（POST /admin/sso/start）
    Note over B: 試行を作る（state・nonce・PKCE）
    B-->>U: 中継のページ
    U->>A: 自動で移動（GET /admin/oidc/authorize）
    Note over A: 要求を検査して保存する
    A-->>U: 継続ページと手続きのCookie
    U->>A: 「続ける」（GET /admin/oidc/continue）
    opt Aに未ログイン
        A-->>U: パスキーの画面
        U->>A: パスキーで本人確認
    end
    A-->>U: ログイン先の確認画面
    U->>A: 「ログインする」（POST /admin/oidc/approve）
    Note over A: 利用許可を確かめ、世代を固定して、コードを発行する
    A-->>U: 中継のページ（コード付き）
    U->>B: 自動で移動（GET /admin/sso/callback）
    Note over B: stateと試行のCookieを照合する（一回限り）
    B->>A: コードの交換（POST /oidc/token）
    Note over A: クライアント認証の後で、コードを消費する
    A-->>B: IDトークン
    Note over B: 署名・issuer・audience・nonce・世代を検証する
    B->>A: 初回の照会（POST /oidc/check）
    A-->>B: 有効
    Note over B: セッションを作る
    B-->>U: 元の画面へ移す
```

- 中継のページは、`Refresh` ヘッダーと通常のリンクで移動する。CSPの `form-action 'self'` はフォーム送信の後の別サイトへの転送を止めるため、303は使わない。
- 別サイトからの到着には `SameSite=Strict` のCookieが付かないため、`/admin/oidc/authorize` ではログイン状態を判定しない。利用者が「続ける」を押すと、同じサイトの操作としてCookieが付く。
- 交換と照会はサーバー間の通信で、ブラウザを通らない。
- 中止したとき、利用許可が無いときは、コードの代わりに `error=access_denied` を付けて連携先へ戻す。

## 3. 照会の結果と連携先の動作

```mermaid
sequenceDiagram
    actor U as 利用者のブラウザ
    participant B as 連携先B
    participant A as 認証基盤A

    U->>B: 保護された画面・操作
    Note over B: セッションの期限と、利用者の停止を確かめる
    B->>A: 照会（subと、ログイン時の3つの世代）
    Note over A: 最新の状態を1つの文で読んで照合する
    alt 有効
        A-->>B: active:true
        B-->>U: 処理を実行する
    else 無効
        A-->>B: active:false
        Note over B: セッションを削除する
        B-->>U: ログインの案内へ移す
    else 確認不能
        A--xB: 障害・不正な応答など
        B-->>U: 503（処理をせず、セッションは残す）
    end
```

| 認証基盤の応答 | 連携先の動作 |
|---|---|
| 200、正しい形式、`active:true`、識別と世代が一致 | 連携先の停止・ロール・期限も確かめて実行 |
| 200、正しい形式、`active:false` だけ | セッションを削除し、ログインの案内へ移す。POSTは再送しない |
| 200で `active:true` だが識別や世代が不一致 | 確認不能 |
| 2xxの他・4xx・5xx・タイムアウト・TLSの失敗・Content-Typeの不一致・不正な本文 | 確認不能 |

確認不能は503で、セッションを残し、業務の処理をしない。接続の失敗と5xxの後の5秒は問い合わせず、同時照会は32件までで待ち行列を作らない。

### 停止の反映

```mermaid
sequenceDiagram
    actor O as 運用者
    participant A as 認証基盤A
    participant B as 連携先B
    actor U as 利用者のブラウザ

    O->>A: アカウントを停止する
    Note over A: 同じトランザクションで、停止の記録、世代の更新、進行中の要求とコードの削除を行う
    U->>B: 次の操作
    B->>A: 照会（ログイン時の世代）
    A-->>B: active:false
    Note over B: セッションを削除する
    B-->>U: ログインの案内へ移す
    O->>A: アカウントを再開する
    Note over A: 世代は戻さない
    Note over B,U: 古いログインは使えず、ログインし直す
```

再開しても世代は戻らないため、古いログインは一致せず、復活しない。

## 4. 不変条件

| 番号 | 条件 | 守る場所 |
|---|---|---|
| FEDAUTH-INV-1 | 保護された要求は、毎回、認証基盤に照会して有効と確認できたときだけ実行する。肯定の結果を要求をまたいでキャッシュしない。正しい形式の無効の応答を受けたらセッションを削除する。世代はログイン時の値を使い、照会の結果で更新しない | `authorize`・`upstream.check` |
| FEDAUTH-INV-2 | 確認不能のときは503で業務の処理をせず、セッションを残す。設定の誤りや障害で全員のログインを消さない | `upstream.check`・`authorize` |
| FEDAUTH-INV-3 | ログインは、試行のCookieとstateが合うときだけ、一回限りで完了する。ログイン中のセッションは、開始でもコールバックでも置き換えない。別のブラウザにコールバックを踏ませても、ログインさせない。開始は同一オリジンのPOSTだけで、戻り先は管理画面の相対パスに限る | `attempts`・`handleStart`・`handleCallback`・`validNext` |
| FEDAUTH-INV-4 | 権限は連携先のロールで決め、認証基盤のロールをコピーしない。初回はメンバーで、管理者はコマンドで指定する。セッションは無操作2時間、または本人確認から12時間で失効する | `sessionStore` |

## 5. 守らない範囲

- 公開クライアント・SPA・モバイル、APIキー認証、refresh token、動的な連携先の登録、第三者への開放。
- 照会の後に始まった処理の巻き戻しと、開いている画面の自動の遷移。停止の反映は次の要求からで、照会済みの処理は取り消さない。
- 認証基盤の障害中の、連携先への緊急のログイン。連携先のコマンドも、照会を迂回するログインを作らない。
- 複数のインスタンスでのログイン試行の共有（試行はメモリにある）。
- 同じ登録可能ドメインの別ホストが侵害され、未ログインのブラウザに試行のCookieを植えられた場合のログインCSRF。`__Host-` 接頭辞は `Path=/` が要り、Cookieが公開ページにも送られるため採らない。ログイン中のセッションは置き換えない。
- 連携先どうしの相関の防止。識別子は連携先の間で共通（pairwiseにしていない）。
- バックグラウンドの処理と長時間の接続の、途中での停止の反映。
