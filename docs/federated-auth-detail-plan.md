# 共通認証の詳細設計案

状態: 実装済み（2026-10-30）。今の仕様の正本は [docs/domain/federatedauth.md](domain/federatedauth.md)・[docs/domain/auth.md](domain/auth.md)・[データ台帳](data-inventory.md)で、本書は設計の経緯である。実装で変えた点は12節にある。本書の「案」「未検証」の記述は設計時点のもの。

## 1. 推奨と対象範囲

- Aの提供機能は `features/auth/oidcop`（仮称）に置き、登録時に明示的に選ぶ。失効・保存・復旧の処理は外部依存を持たないauth本体で一体管理する。
- Bは別の任意機能（仮称 `features/federatedauth`）で `features.AdminProvider` を提供する。ローカルauthとの同時登録を起動時に拒否する。認証提供者のない管理機能も起動時に拒否する。
- ログインはOIDC Authorization Code Flow＋PKCE S256＋confidential clientの認証。継続利用は独自のsub・失効世代照会。OIDC対応だけのシステムでは停止反映の契約を満たさないため、B側の照会処理を別途必須にする。
- 初版は自社が管理するサーバー型Webアプリだけ。公開クライアント、SPA、モバイル、APIキー認証、refresh token、動的なclient登録、第三者への開放、バックグラウンドジョブ、長時間接続は対象外。
- 推奨ライブラリはAに `zitadel/oidc`、Bに `coreos/go-oidc`＋`x/oauth2`。[比較記録](federated-auth-library-review.md)の版・修正版と導入条件を満たす場合に限る。

## 2. 配置・依存と既存機能の接続

### 単一モジュール内のパッケージ分割を第一案にする

Goのリンク対象はgo.modのrequire行でなく、到達するimportで決まる。`features/auth` 本体にはOIDCライブラリをimportせず、世代・保存・失効・GCの標準ライブラリだけの処理を置く。`features/auth/oidcop` がauth本体を組み合わせて使い、SDKの型をauth本体のAPIに漏らさない。親authから子oidcopはimportせず、循環を作らない。

`cmd/server/features.go` のA構成だけでoidcopの組立口をimportし、通常のauth登録を置き換える。Aのラッパーは既存authのFeature/AdminProvider等の口を委譲し、保存処理はauthが所有する型付きのトランザクション境界を使う。一般の機能に任意SQLやDBの口を公開する設計にはしない。連携用スキーマがある通常auth構成でも、停止・復旧時の連携失効を継続する。

root go.modの直接依存は現状1本から最大5本へ増えるが、それだけでBaseや通常authのバイナリにSDKがリンクされるわけではない。Core/通常auth→oidcop/federatedauthへのimportを禁止し、構成別の実ビルドでリンク済みモジュールとサイズを記録する。登録だけを替える実行時フラグとは区別する。

別Goモジュールは、依存取得・更新の単位まで隔離したい場合の代替とする。リンク分離だけが目的ならパッケージ分割で足りるため、初版では別モジュールを作らない。READMEは「root go.modの直接依存」と「構成ごとにリンクされる依存」を分けて改訂する。現時点ではgo.modを変更しない。

`BuildRoutes` は現在最初のAdminProviderだけを選ぶため、重複をエラーに変える必要がある。`cmd/kit` の同梱機能追加はauthを自動登録するので、Bの提供者がある場合はそれを使うよう依存の定義を変更する。news/catalog/contact/pagesのハンドラは `features.Admin` を使い続ける。BのPage/FormはA照会に加えてBの最新ロール・同一オリジン・CSRF・キャッシュ禁止を検査し、Aのadmin/memberをBにコピーしない。

## 3. 識別子と失効の契約

Aに256ビットのランダムなpublic subを追加する。WebAuthn user.idは鍵リセットで変わるため流用しない。自社アプリ間で共通の仮名識別子となり、相関可能なデータとして台帳に記載する。pairwise化は初版では採らない。

署名されたIDトークンに、private claim `mwk_auth` として次の文字列を含める。標準の `iss`・`sub`・`aud`・`exp`・`iat`・`auth_time`・`nonce` とは別の契約である。

| 値 | 用途 |
|---|---|
| account_epoch | アカウント全体の過去の認証を失効させる |
| client_epoch | Bの登録を停止・再開した場合の復活を防ぐ |
| grant_epoch | そのアカウントのB利用許可を取り消し・再付与した場合の復活を防ぐ |

各世代はDBの正の64ビット整数。更新が上限を超える場合は失敗させ、0や過去の値に戻さない。JSONでは10進文字列とし、先頭ゼロ・符号・小数・桁あふれを拒否する。Bはログイン時に得た3つの値をセッションに固定し、照会結果の新しい値で古いセッションを更新しない。Bの局所IDは `(issuer, sub)` に一意に対応する非再利用の整数であり、subから整数を計算しない。

### 失効対象

| 操作 | 原子的に行うこと |
|---|---|
| Aの停止・削除・鍵リセット・複製の疑い・CLIのアカウント復旧 | account_epoch更新、関係する認可要求・コードの無効化。既存adminセッション削除も同じTx |
| Aのロール変更 | 既存のadminセッションだけ失効。Bの権限・世代は変更しない（AとBの権限を分離する） |
| clientの停止・再有効化 | client_epoch更新、対応する認可要求・コードを無効化 |
| 利用許可の取り消し・再付与 | grant_epoch更新、対応する認可要求・コードを無効化 |
| クライアント認証とコード束縛の検査を通った交換済みコードの再利用 | 対象のgrant_epochを更新し、その利用者のBへの既存ログインをすべて失効させる（個別セッションだけの失効にはしない） |
| Aの通常ログアウト・adminセッションの通常期限切れ | Aだけ失効。Bへ全体ログアウトはしない |
| Bの通常ログアウト | Bの当該セッションのみ削除 |

ログイン後に停止したら、停止コミット後に始まるAの照会は必ず拒否する。照会済みのBの処理まで巻き戻す保証はない。照会は新しいDB読み取りで行い、要求をまたぐ肯定キャッシュや長寿命の読み取りスナップショットは使わない。

Aの提供機能を後で無効化しても、保存済みの連携状態に対する失効処理は止めない。再有効化で古い世代が復活しないようにする。`auth-recover`・`-reset-schema` は連携状態も対象に広げ、既存のAUTH-INV-16を改訂する。全体リセット後に同じsubを再割り当てしない。バックアップから状態を巻き戻す場合は、提供機能を閉じたまま全連携の失効境界を更新し、古いBセッションを拒否できることを確認してから再開する。

## 4. 保存データ案

名前は仮称。実装承認後に現行の `docs/data-inventory.md` と `docs/domain/auth.md` を更新する。今回の文書だけで既存スキーマを変更しない。

### A

| 表・置き場 | 項目案 | 目的・保持・削除 |
|---|---|---|
| auth_federated_subjects | sub（主キー）、account_id（unique・削除時null）、account_epoch、deleted_at | 安定した識別子と失効状態。初回利用時に作成。アカウント削除後は最長セッション寿命12時間＋時計ずれ2分だけ失効の墓標を残しGCする。未知subもactive:falseとし、削除済みsubを手動で再指定できる口は設けない。新規subは毎回256ビット乱数から発行し、再利用の回避は暗号学的な乱数性に依拠する |
| auth_oidc_clients | client_id、label、redirect_uri、enabled、client_epoch、created_at | Bの登録。初版はclientにつき戻り先1件・完全一致。廃止は停止で閉じ、世代を再利用しない。履歴を除去する場合はclient_idも再利用しない |
| auth_oidc_client_secrets | client_id、secret_hash、key_id、expires_at | クライアント資格情報のハッシュ。新旧2件までの更新猶予。削除・期限後は直ちに利用不可、GCで削除 |
| auth_oidc_grants | client_id、sub、allowed、grant_epoch | Bの利用許可。取り消しても世代の墓標を残す。関連する利用者またはclientの廃止後、最大セッション寿命と時計ずれを過ぎてGC可能 |
| auth_oidc_requests | request_id、flow_cookie_hash、client_id、redirect_uri、state、nonce、S256 challenge、scope、created_at、expires_at、認証後のsub/auth_time/3世代 | 進行中の要求。5分で失効。ブラウザのflow Cookieと結び付ける。state/nonceは返送に必要な一時値で、ログには出さない。未認証の要求は利用者と結び付けず、認証後に世代を固定する |
| auth_oidc_codes | code_hash、request_id、client_id、sub、redirect_uri、challenge、nonce、auth_time、3世代、expires_at、consumed_at | コード60秒・一回限り。交換済み記録は再利用時の失効のため12時間＋時計ずれ2分保持。コードの生値は保存しない |
| DB外のファイル | 署名秘密鍵、SDKの暗号化鍵 | 所有者だけが読める設定ファイル。バックアップを含め保護し、秘密鍵はログ・公開リポジトリに置かない |

request_idも推測困難な値とするが、ID単独では認証済み要求を続行できず、flow Cookie・adminセッション・CSRFを検査する。未認証要求はアカウント停止時に特定できないため一律削除せず、後の本人確認で最新世代を固定する。認証済みの古い要求は停止時に無効化する。

OIDCのトークン応答のaccess_tokenは省略しないが、初版はUserInfoやBearerで利用できる資源APIを提供しない。SDKのCreateAccessTokenではランダムIDと5分の期限だけを返し、IDを永続保存しない。SDKが生成するopaque値を受け付ける経路も広告もしないため、照合用の表を作らない。BはIDトークンとat_hash等の整合を検証後、access token・IDトークンの生値を破棄する。refresh tokenも発行しない。これは汎用の資源アクセス用OAuthサーバーではない。UserInfoを後で追加する場合は、保存・失効を含む別の承認を要する。

### B

| 表・置き場 | 項目案 | 目的・保持・削除 |
|---|---|---|
| federated_accounts | id（AUTOINCREMENT）、issuer、sub、role、disabled_at、created_at | ID対応とBの権限。初回ログイン時はmemberに限定し、adminはCLIで指定する。停止はセッションを同時削除。利用終了時は業務データとの対応を確認して削除し、整数IDは再利用しない |
| federated_sessions | token_hash、account_id、3世代、auth_time、created_at、last_seen_at、expires_at | BのCookieをハッシュで照合。無操作2時間・最大12時間。最大期限はAのauth_timeから12時間も超えない。ログアウト・無効確認・期限後に削除 |
| メモリのみ | flow Cookieのハッシュ、state/nonceのハッシュ、PKCE verifier、戻り先、有効期限 | 5分・ブラウザにつき進行中1件。新しい試行で古い試行を失効。成功・失敗・期限で削除。B再起動時は試行を失い、ログインをやり直す。verifierの復元が必要なためハッシュだけでは保存できず、DBには置かない |
| DB外のファイル | issuer、client_id、クライアント秘密値、許可する通信先・戻り先 | 設定値。秘密値は実名と無関係な256ビット乱数。ファイル権限・更新・廃棄を定める |

Bのメモリ試行は上限1024件。期限切れの掃除後も満杯なら最古の未完了試行を破棄して新しい試行を受け付け、容量が満杯のまま新規ログインを5分間止める方式は採らない。破棄された試行はstate照合で拒否し、再開始を案内する。既存のログイン済みセッションは消さない。

同じ試行Cookieからの開始は10秒に1回、全体は毎秒2件・バースト16件を初期値として、上限超過は429で新しい試行を作らない。すべて短命のメモリ管理とし、IP/User-Agentを新規保存しない。Cookieを捨てる攻撃や分散送信による共有枠の占有まで完全に防げるとは扱わない。満杯時の最古破棄と流量制限にはそれぞれ可用性上の限界があるため、連続送信下の正常利用者の完了率も結合検証で測る。複数Bインスタンス間の試行共有は初版の対象外とし、黙って不安定な構成を許さない。PKCE verifierをブラウザCookieやDBに平文保存しないため、既存の「セッションのDB保存はハッシュのみ」の例外を増やさない。

### Cookie

| 所有者 | 名前案・属性 | 寿命 |
|---|---|---|
| A | 既存admin_session、Path=/admin、Strictを維持 | 既存どおり |
| A | oidc_flow、Path=/admin、Strict・Secure・HttpOnly・Domainなし | 5分、完了・中止時に削除 |
| B | sso_attempt、Path=/admin/sso、Lax・Secure・HttpOnly・Domainなし | 5分、試行終了時に削除 |
| B | federated_session、Path=/admin、Lax・Secure・HttpOnly・Domainなし | 最大12時間、サーバーでも無操作期限を検査 |

利用者の氏名・メール・電話は追加しない。識別子と権限も仮名化した利用者データとして台帳に記載する。公開閲覧だけではCookieや保存行を作らない。

## 5. 経路とログインの流れ

以下はAのissuerをオリジン直下に固定した初版の案。管理者Cookieを使わないtoken・keys・checkは `/oidc/` に分け、管理画面とは別のクライアント認証・経路検査で守る。実際のSDKの経路設定を固定し、列挙した経路だけを外側のServeMuxに渡す。SDKを広いprefixでそのまま公開しない。全体のprovider.Handler（chi/CORS込み）を使わず、必要なハンドラだけを登録する。CORSは許可しない。

| 所有者・経路案 | 用途・守り |
|---|---|
| A GET /.well-known/openid-configuration | 有効時のみ、アプリが組み立てたDiscoveryConfigurationを返す。実際に有効な機能だけを広告 |
| A GET /oidc/keys | 公開鍵。秘密鍵を含めない |
| A GET /admin/oidc/authorize | 外部Bからの認可要求を検査して保存し、flow Cookie付き200継続ページを返す |
| A GET /admin/oidc/continue | flowと既存adminセッションを照合。未認証なら既存パスキー画面に進み、認証後は保存済みの要求だけへ復帰 |
| A POST /admin/oidc/approve | 同一オリジン＋CSRF＋adminセッション＋flow・利用許可・世代を検査し、一回限りのコードを返す |
| A POST /oidc/token | Basicのクライアント認証、コード・S256 verifier・戻り先を検査。ブラウザのadmin Cookieは認証に使わない |
| A POST /oidc/check | BasicのB認証、sub・ログイン時の3世代を確認する独自API |
| B GET /admin/login | 既存の画面の型を使う外部ログイン案内。Aへの遷移は明示的な操作 |
| B POST /admin/sso/start | 同一オリジン・Fetch Metadataを検査し、試行を作成。既存ログインの置換はせず、既存セッションがある場合はそのCSRFも検査 |
| B GET /admin/sso/callback | state＋試行Cookieを照合し一回限りで取得。コード交換・IDトークン検証・Aへの初回照会後にBセッションを作る |
| B POST /admin/logout | 同一オリジン＋Bセッション＋CSRF。Bのセッションだけ消す |

Bのログイン開始は利用者の操作とする。未ログイン時にはセッション由来のCSRFを持たないため、Originの完全一致とSec-Fetch-Site:same-originを必須にし、単に既存のログイン済み用Formを使い回さない。対応しないブラウザへの緩和は初版に入れない。

SDKのop.Authorizeは最後に302でLoginURLへ送るため、そのまま使わない。ParseAuthorizeRequest・ValidateAuthRequest等の検証とCreateAuthRequestを組み合わせて200ページを返し、approveのPOSTからAuthResponse/SaveAuthCodeに進む。SDKの検証を省略する理由にはしない。AのCreateAuthRequest保存アダプターは、confidential clientを含む全clientに対してS256のchallenge必須・形式・長さを検査し、欠落・plainを保存前に拒否する。SDKのCodeMethodS256設定は広告であり、必須検査の代替ではない。

Aの外部到着時にStrict Cookieが付く前提を置かない。200ページから利用者が「続ける」リンクを押して同一サイトに移り、本人確認後に「Bへログイン」のPOSTで完了する。flow CookieのないSDKの認可コールバックは拒否し、URLのrequest_idだけで続行させない。Aのflowもブラウザにつき進行中1件とし、新しい有効な認可要求を受けたら古いflowを失効させる。Aの未完了要求も期限切れの掃除・件数上限1024・毎秒2件/バースト16の受付制限を初期案とし、無認証の要求でDBが無制限に増えないようにする。受付超過は429とし、交換済みコードの再利用検知用の記録を容量対策で早期削除しない。

既存のhandleLoginFinishは、flow Cookieと保存済み要求が有効な場合だけ固定の `/admin/oidc/continue` をnextに返す。それ以外は既存の `/admin`。URL入力からnextを決めず、既存webauthn.jsのresult.nextによる移動を使う。

利用許可がない場合と利用者が中止した場合は、検証済みのclientと完全一致の戻り先にstateと `error=access_denied` を返し、flowを削除する。client/戻り先自体が不正な場合はA内で400を返し、そのURLには転送しない。BはstateとCookieの照合後に試行を削除し、中止を表示する（エラー説明を生表示しない）。

パスキーのJSは既存画面だけで使用し、新規JS・読込画面・CSP緩和は予定しない。もし既存画面で安全な復帰を作れなければ、実装前に例外を再提案する。

Bの元ページは試行のメモリに保存し、stateには入れない。`/admin`以下の許可した相対パスだけを認め、`//`・バックスラッシュ・制御文字・符号化した迂回を拒否する。多重state/codeパラメータも拒否する。コールバック後は303でクエリのない画面に移し、全認証応答をno-store・no-transform・Referrer-Policy:no-referrerにする。

BのOIDC検証はES256だけ、issuer完全一致、audience/azp、期限・iat・auth_time・nonce、at_hashがある場合の整合を確認する。時刻ずれの許容は最大2分、IDトークン5分を初期値とする。nonceは署名検証後に試行のハッシュと照合する。IDトークンの世代を、後から読み直した最新世代で上書きしない。prompt/max_age等のSDKの対応範囲を検証し、未実装の機能をdiscoveryで広告しない。SDKのCreateDiscoveryConfigは未公開のendpointも含めるため使用せず、アプリがoidc.DiscoveryConfigurationを組み立てる。issuer・authorization/token/jwks・code・authorization_code・openid・public sub・ES256・Basic・S256等の実装済み項目だけを載せ、userinfo/introspection/revocation/end_session/device_authorization/iframeは省略する。op.DiscoverのJSONシリアライズだけは使用可能とする。

## 6. check APIと拒否表

リクエストはPOST JSONで `sub` と3つのepoch文字列。client_idはBasic認証から確定し、本文から任意のclientを選ばせない。本文最大4KiB、応答最大8KiB、余分な項目・重複キー・型違いを拒否する。

判定は最新のアカウント・client・grantを一つのSQL読み取りで照合し、別々の読み取りの間に状態が変わった結果を混ぜない。

有効なら200で `active:true` とissuer/sub/client_id・照合済みの3世代を返す。無効なら200で `active:false` だけを返し、存在しない利用者か停止かを外へ区別しない。無効の理由はDBの内部エラーとして返さない。資格情報不良は401、形式不良は400、DB障害・過負荷は503。すべてno-storeとする。

| Aからの結果 | Bの動作 |
|---|---|
| 200・正しい形式・active:true・識別と全世代が一致 | Bの停止・ロール・期限も検査して実行 |
| 200・正しい形式・active:false | セッションを削除。画面はBのログイン案内、APIは401。POSTを自動再送しない |
| 200・active:trueだが世代や識別が不一致 | 確認不能として503。照会契約上はAが不一致をactive:falseで返すべきなので、本文の不整合で大量破棄しない |
| 200以外の2xx・400/401/403/429/5xx・タイムアウト・TLS失敗・Content-Type不一致・不正本文 | 503。セッションは保持し、再確認まで業務処理をしない |

通常の失効時は、正しい形式のactive:falseで古い世代を拒否する。正常な無効と、通信・設定不備による確認不能を混同しない。2秒の全体タイムアウト、B全体32件の同時照会上限、待ち行列なしを初期案とし、超過は503。自動再試行なし。上流5xx・ネットワーク障害で5秒だけ失敗側を遮断してよいが、成功結果の要求間キャッシュはしない。private networkを利用してもクライアント認証・HTTPSを省略しない。

## 7. SDKの保存処理と原子性

zitadelのStorageにはAuthRequestByCode・CreateAccessToken・DeleteAuthRequestなど分離したメソッドがあり、SDK利用だけでは原子性が保証されない。まずコード交換ラッパーでBasicを解析し、資格情報をSDK呼出しより前に検証する。未認証・無効な資格情報は副作用なしのinvalid_clientで拒否する。SDKはAuthRequestByCodeをクライアント秘密値の照合より先に呼ぶため、コードの検索時点で失効を発火させない。認証済みclientとコードの所有client、戻り先、PKCEの束縛を検査した後だけ、再利用に対する失効を許す。無関係なclientによる再送やverifier不正には世代更新をしない。

資格情報の事前確認後、コード交換ハンドラの外側で専用の `*sql.Conn` に生SQLで `BEGIN IMMEDIATE` を発行し、request context経由で保存処理すべてを同じTxに渡す案とする。この機能のために共有DSNの_txlockや別プールは追加しない。既存busy_timeoutは5秒なので、保持中は他の書き込みが最大5秒待ち得る。コード交換のcontext全体期限は5秒、保持時間を計測し、取消し・ロールバック後の接続再利用も検査する。Tx内でも資格情報とclient状態を再確認する。

SDKの署名・JSON生成までレスポンスをバッファに置き、必要な失効・コード消費・発行状態のコミット成功後にだけ送信する。

- 最新のアカウント・client・利用許可と、認証時に固定した世代を照合してコードを一回だけ消費する。
- 署名・生成・DB処理が失敗したらロールバックして成功トークンを送らない。保存していないaccess tokenが返る場合も、コード消費とIDトークン生成の原子性は必要。認可要求を消すSDKの呼び出しでは、再利用検知用の消費記録まで削除しない。
- 認証済みclientによる、コードと戻り先とPKCEを束縛検査済みの既知の交換済みコードの再利用では、grant_epoch更新をコミットしたうえでinvalid_grantを返す。4xxだから常にロールバック、という実装にはしない。
- 認証結果の世代は要求の不変なスナップショットから作り、後続のメソッドで新しい世代に差し替えない。IDトークン側はCanSetUserinfoFromRequest/SetUserinfoFromRequestで要求を受け取り、追加claimに保存済みの世代を入れる。初版のclientはIDTokenUserinfoClaimsAssertionを有効にしてこの経路を使い、scopeがopenidだけでも呼ばれることを結合検証する。GetPrivateClaimsFromRequestはaccess token側の拡張であり、これだけでIDトークンへ値が入るとは扱わない。
- 署名秘密鍵の読み込み・外部通信はTx内で行わない。準備済み鍵で署名し、Tx保持時間を計測する。コード交換で未知の鍵の取得などを行わない。
- 送信開始後に失敗しても同じコードは再利用させず、Bは再ログインを案内する。二度目のhttp.Errorを書かない。

保存なしのSDK access token応答とat_hash、カスタムclaimの保存時点、並行コールバック、再利用検知は、承認後の最初の結合検証で確認する。これが成立しなければSDKへの当てはめ方を変え、原子性を緩めない。

## 8. 設定・鍵・権限の管理案

設定名は未確定。Aは提供機能の有効化・固定issuer・署名鍵ファイル・SDK暗号化鍵ファイルを明示する。Bは固定issuer・client_id・秘密値ファイル・固定callback URL・許可する通信先を明示する。起動時に検証し、不備は値をログに出さず起動失敗とする。

初版のBはProviderConfigでissuer・authorization/token/JWKS URLを設定から与え、起動時にdiscoveryを取りに行かない。Aのdiscoveryと設定の一致は結合テストで確認する。checkも既知の固定URLとする。token/JWKS/checkのURLは許可したHTTPS originと既知のパスに限定し、HTTPリダイレクトを追わない。ユーザー入力URL・Hostヘッダーで接続先を決めない。ループバック・プライベートアドレスを使う検証と、本番向けの許可設定を分ける。

HTTPクライアントはgo-oidcのoidc.ClientContextとx/oauth2のoauth2.HTTPClientの両方に明示して渡す。oauth2.AuthStyleInHeaderを固定し、自動判定による秘密値の本文送信への切替をさせない。token交換の全体期限5秒、JWKS取得2秒、check2秒を初期値とする。本文サイズもtoken32KiB・JWKS64KiBまでに制限する。URL制限とリダイレクト禁止はすべてのクライアントに適用する。

go-oidcは未知kidの取得を同時実行では合流するが、30秒の最短間隔は保証しない。自前のKeySetラッパーで最短30秒・同時1件に制限し、有効な既知鍵は使えるようにする。IDトークンはBのバックチャネルでAから受け取るものだけとし、任意のブラウザ入力JWTを検証する経路は作らない。通常の鍵更新時に既知鍵を突然拒否しないことも検証する。

ES256の現用鍵は1件。通常の更新では旧公開鍵をIDトークンの有効期限5分＋時計ずれ2分以上残す。秘密鍵漏洩時は鍵の取り消しに加えて全clientの世代を更新し、署名済み結果で既に作られたBセッションも失効させる。クライアント秘密値は初回生成時だけ表示し、AはSHA-256ハッシュで比較。更新時は最大24時間の新旧併用後に旧値を削除。漏洩の場合は猶予なしで旧値を無効化しclient_epochも更新する。

初版の連携先登録・利用許可・Bの権限変更はCLIだけに絞り、新規の管理フォームを作らない。秘密値をCLI引数に渡さず、保護されたファイルまたは標準入力を使う。IDとsubは設定対象として扱い、アプリの通常ログには出さない。

| CLI操作案 | 役割 |
|---|---|
| A: client作成・停止・秘密値更新 | 生成したclient_idと固定戻り先を登録し、資格情報を管理 |
| A: clientとアカウントを指定してgrant/revoke | 利用許可を設定。再付与でも世代を再利用しない |
| B: issuer/subを指定して初回admin・ロール変更・停止・削除 | Bの局所権限とセッションを管理 |
| B: 管理者復旧 | 存在する局所利用者へadminを割り当てる。実際の利用時はAの有効性・利用許可が必要 |

Bの初回管理者設定・復旧は、対象者が一度OIDCログインして局所行を作った後に行う。CLIは局所行の存在と指定されたissuer/subだけを確認し、Aへ通信しない。CLIで付与しても、実際の要求ではAの有効性とBの権限を必ず確認する。CLIがセッションの世代を書き換えたり認証済みセッションを作ったりすることは禁止する。A障害中にロールの準備はできても、Aの確認を迂回して利用できるようにはしない。

Aで唯一のB管理者を停止する操作は妨げない。BのCLIはAの確認を迂回するログインを作らない。A停止中にBだけを緊急ログイン可能にする機能も初版では設けない。Aで再開するか、別の利用許可済み利用者をB管理者にする。

## 9. 承認を求める変更と実装順

この文書への意見を得た後、次を一括で具体的な承認対象にする。まだ実行しない。

1. ライブラリの直接依存4本追加（現状SQLite1本から計5本）、推移依存、READMEと依存検査の更新。単一モジュール内のパッケージ分割と、TestArchitecture_LinkedModulesをBase/通常auth/A/Bの構成別期待値に分ける変更。
2. 4節の保存データ・新Cookie3種類・設定用の秘密ファイル・CLI入力項目。12時間＋2分後の墓標GC・再起動時の試行喪失・満杯時の最古破棄と流量制限を含む。
3. B→AのHTTPS通信（JWKS/token/check。Bの起動時discovery取得はしない）。初回ログインと毎要求の照会で、可用性がAに依存する。
4. OIDC/独自APIの公開経路、Aの既存ログインからの安全な復帰、Bの新しい認証画面。待受は127.0.0.1のまま、既存CSPは緩和しない。新規JS/CSSファイルは作らない。
5. authの停止・復旧の対象拡大、AdminProvider重複拒否、cmd/kitの依存登録変更。

承認後は、まずライブラリと保存アダプターの小さな結合検証、次にAの失効とCLI、BのAdmin互換とCLI、最後にパスキーからの実際の往復を順に実装する。AとBは別プロセス・別DB・異なるサイトで検証する。

## 10. 完了条件

- 全体計画8節の条件を維持し、実際のパスキー→認可コード→Bログイン→A停止→BのGET/POST/API拒否まで検査する。
- S256 challengeの欠落・plainをconfidential clientでも拒否し、CodeMethodS256の広告だけに依存しない。未認証の再利用コード送信・他clientのコード送信・PKCE不正で世代が変わらず、正しく束縛された再利用だけで失効する。
- SDKのstate/nonce/PKCE・クライアント認証・戻り先・署名と時刻・IDトークン世代を、正常と拒否の両方で固定する。ライブラリの既定値だけで満たしたと判断しない。
- 停止・再開・削除・許可の取り消し・client再有効化・鍵リセット・複製異常・Recover・提供機能の無効/再有効・全体リセット・コード再利用をテストする。Aだけのロール変更ではBのセッションが維持されることも確認する。
- 複数接続のファイルDBで停止と交換、二重交換、交換後の応答送信失敗、DBコミット失敗を再現する。署名前後でエラーを起こして、成功応答が出ないことを確認する。
- SDKのprovider.Handlerを登録せず、CORSが閉じ、UserInfo等の未実装endpointがdiscoveryにも経路にも出ないことを検査する。
- B再起動・A/Bの複数タブ・ログイン試行の流量制限と満杯時の破棄・連続送信下の正常利用者の完了率、Cookieの削除・期限・Path、Aログイン済み/未済みをChromiumとFirefoxで確認する。Safariは未検証ならその制限を明記する。
- A障害・資格情報ミス・制限超過・不正応答で503となり、業務データが変わらず、セッションの大量削除や無限転送をしない。
- 生コード・state/nonce・トークン・PKCE verifier・クライアント秘密値・request_idを、アプリ・SDK・アクセスログ・トレース・panicに出さない。外部SDKのロガー・telemetryも対象にし、SDK導入だけで外部送信を開始させない。
- 台帳、PrivacyNotices、ドメイン不変条件、画面・経路・漏洩検査、依存とリンク済みモジュールの検査を更新する。キット外のB実装でも照会契約を満たすことを確認する。
- `go test -race ./...`、`make verify`、ビルド、該当するブラウザ検証を通し、検証部分のFableレビューを再度行う。本番への導入は別途承認する。

本案はFableのP-64を反映した実装承認待ちの提案。未検証のSDK結合や依存量を、採用済み・実装済みとして扱わない。

## 11. FableレビューP-64への対応

コード再利用の副作用より前のクライアント認証・束縛検査、全clientのS256必須化、discoveryの自前構成、import境界によるリンク分離、墓標の有期限化を反映した。UserInfoと永続トークン表を省略し、BのCLI照会・起動時discoveryを削除、Aのロール変更によるBの過剰失効も外した。

「最古の試行を捨てる」だけでDoSが解決するとはせず、流量制限・破棄された試行の扱い・残る共有枠占有の制約を追記した。SDKからIDトークンへのスナップショット受け渡し、保存なしのaccess token応答、KeySetラッパー、同一接続Txの結合は依然として未検証であり、実装承認後の最初の検証対象とする。

参照したSDKソース: [コード交換の呼出し順](https://github.com/zitadel/oidc/blob/v3.51.16/pkg/op/token_code.go)、[認可要求と応答](https://github.com/zitadel/oidc/blob/v3.51.16/pkg/op/auth_request.go)、[discovery構成](https://github.com/zitadel/oidc/blob/v3.51.16/pkg/op/discovery.go)、[IDトークンのclaim生成](https://github.com/zitadel/oidc/blob/v3.51.16/pkg/op/token.go)。ソースの読み合わせであり結合の実証ではない。

## 12. 実装で変えた点（2026-10-30）

承認の範囲（依存・保存データ・Cookie・通信・経路）は超えていない。実装と検証で次を変えた。

| 設計 | 実装 | 理由 |
|---|---|---|
| Bの開始POST・Aの承認POSTの応答を303で別サイトへ転送する | 200の中継ページ（`Refresh` ヘッダーと通常のリンク）で移動する | Chromiumで、CSPの `form-action 'self'` がフォーム送信後の別オリジンへの転送を止めることを確かめた。CSPは緩めない。`<meta refresh>` は `html/template` の自動エスケープの回避が要るため使わない |
| Bの開始はOriginの完全一致を必須にする | `guard.SameOrigin`（`Sec-Fetch-Site: same-origin` 必須、Originは空・`null`・一致） | キットの `Referrer-Policy: no-referrer` により、同一オリジンのフォーム送信でもOriginは `null` になる |
| 既存セッションがあるときの開始はCSRFも検査する | ログイン中の開始では何も作らず `/admin` へ移す | セッションの置き換えの経路そのものを無くした |
| 同じCookieからの開始は10秒に1回で、超過は429 | 10秒以内の再送は同じ認可要求へ進める | 二度押しをエラーにしない。新しい試行は作らない |
| 連携の表の列に `created_at`・`key_id`・`scope` を持つ | 持たない | scopeは `openid` 固定で、他は用途が無い（データの最小化） |
| コードを別の表 `auth_oidc_codes` に置く | 認可要求の行にまとめる | コードの消費と認証時の世代を切り離さない |
| 停止時の失効は処理の中に書く | `migration.sql` のトリガーで行う（全体の復旧だけは `Recover` の文） | 経路を増やしても漏れないようにする。提供機能を外しても働く |
| `go-oidc` の鍵の取得をラッパーで包む | 自前の `KeySet` で取得する | 最短間隔・同時1件・本文の上限を同じ場所で守る |
| Aのコマンドは連携先と利用許可だけ | `oidc-revoke-all` を足す | 3節・8節の「全連携先の世代更新」の運用の口 |

SDKの結合で確かめたこと: `IDTokenUserinfoClaimsAssertion` の経路で世代のclaimが入る（ただし `SetUserinfoFromRequest` で `Subject` も設定しないと `sub` が消える）。保存しないアクセストークンで `at_hash` が検証できる。`op.AuthResponse` は失敗してもエラーの転送を返すので、コードの有無を確かめてロールバックする。

検証の結果: 別のHTTPSサーバー・別のDBのA・Bで、拒否の表、全ての失効の操作と復活の防止、コードの再利用、停止とコード交換の同時実行、署名失敗のロールバック、送信失敗後の再利用の拒否をテストした（`test/federated_auth_test.go`、`features/auth/oidcop`、`features/federatedauth`）。Chromiumで、別サイトの a.test・b.test を、DevToolsの仮想の認証器のパスキーで往復し、Aでの停止でBが拒否し、再開しても復活しないことを確かめた（`test/browser/federated_test.go`）。

未検証: Firefox・Safariの実機、連続送信の下での正常な利用者の完了率、本番のTLS・リバースプロキシの構成、キット以外のシステムの接続。複数の管理者によるGUI操作は無い（CLIだけ）。
