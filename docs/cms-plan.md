# CMSテンプレート 実装計画

最終更新: 2026-10-25（Fable（claude-fable-5-1）のレビュー、challenge・端末固定の認証器・異常時の案内・Origin検査・実機確認に関する利用者の決定を反映）

## 1. 目的と範囲

- `features/cms/` に、お知らせ1種類を管理する同梱テンプレートを追加する。Siteと同じく `make add-cms` / `make remove-cms` で登録を切り替える。
- ヘッドあり（サーバーがHTMLを返す）とヘッドレス（JSON APIで記事を返す）の両方に対応する。記事の読み書きは共通のストアを使い、HTMLとAPIはその上の表示層とする。
- 管理者は1人。ログインはWebAuthn（パスキー）のみのパスワードレスとする。
- 登録できるのは、同期・バックアップできない端末固定の認証器（外付けのセキュリティキー、Windows Hello等）に限る（7節）。
- 最初の版に含めないもの: 複数の編集者と権限、画像アップロード、Markdown/HTML本文、下書きのAPI、書き込みAPI、APIトークン、APIのキャッシュ。

## 2. 利用者の承認済み事項（AGENTS.mdの境界）

| 境界 | 内容 |
|---|---|
| #2 保存データ | 下記5テーブルの追加、管理者セッションCookie `cms_admin` の追加 |
| #4 JSファイル | WebAuthn用の自前JS 1本（`features/cms/static/webauthn.js`。認証画面だけで読み込む） |
| #5 セキュリティヘッダー | 認証画面だけ CSP の `script-src` を `'self'` に緩和。他のページは `'none'` のまま |
| #5 公開範囲 | 読み取りAPIのCORS。許可する接続元を環境変数で指定し、既定は許可なし |
| #1 go.mod | 変更しない（WebAuthn検証は標準ライブラリで実装） |

上記以外のCookie・保存データ・外部通信は追加しない。追加が必要になった場合は改めて承認を得る。

## 3. Coreの拡張（前提作業）

現在のFeatureのルートは `appMux` に登録され、Coreの `CSRFProtect`（Core `session` Cookie必須）と `ResolveOwner`（Cookie照合・`last_seen_at` 更新・未知Cookieの消去）を必ず通る（`internal/web/handler.go` の `Routes`）。このままでは、CMSの公開ページがCoreのCookieを照合し、CMS独自のCSRFを使うPOSTはCoreに拒否される。

- `features/feature.go` に任意インターフェース `IsolatedRoutesFeature`（例: `RegisterIsolatedRoutes(mux *http.ServeMux)`）を追加する。
- `Routes` で、そのルートを `rootMux`（`/static/` と同じ階層）に登録し、CoreのCSRF・セッション解決を通さない。アクセスログ・本文サイズ制限・パニック回復・セキュリティヘッダーは共通で適用する。
- 独立経路では、Feature自身が認証・CSRF・Origin検査を実装する責任を負う。経路の衝突（Coreや他Featureと同じパターン）は起動時にエラーにする。
- Featureが期限切れデータを削除できるよう、任意インターフェース `PurgeFeature`（例: `Purge(ctx context.Context) error`）を追加し、Coreの起動時GCと1時間ごとのGCから呼ぶ。

## 4. 画面とAPI

| 経路 | 内容 | 認証・セッション |
|---|---|---|
| `GET /news` | 公開中のお知らせ一覧（`published_at DESC, id DESC`、20件ずつ、`page`） | なし。Cookieの発行・照合をしない |
| `GET /news/{id}` | 公開中のお知らせ詳細。下書き・存在しないIDは404 | なし |
| `GET /api/news` | 公開中の一覧JSON（`limit` 1〜50、既定20、`offset`） | なし。CORS許可リストのみ |
| `GET /api/news/{id}` | 公開中の詳細JSON。下書き・存在しないIDは404 | なし |
| `GET /admin/setup` | 初回・復旧時のパスキー登録画面 | 登録トークン（下記6節） |
| `GET /admin/login` | パスキーでのログイン画面 | なし |
| `POST /admin/webauthn/register/{begin,finish}` | 登録のchallenge発行と検証 | 未使用の登録トークン、または有効な管理者セッション＋CSRF |
| `POST /admin/webauthn/login/{begin,finish}` | 認証のchallenge発行と検証 | 同一オリジン検査（challengeはサーバーに保持しない） |
| `GET/POST /admin/posts...` | 一覧・作成・編集・公開/非公開・削除 | 管理者セッション＋CSRF |
| `GET/POST /admin/passkeys...` | パスキーの一覧・追加・名前変更・削除 | 管理者セッション＋CSRF |
| `POST /admin/logout` | ログアウト | 管理者セッション＋CSRF |
| `GET /admin/static/webauthn.js` | 認証画面用JS | なし（CMSが埋め込みFSで配信） |

- `page`・`limit`・`offset`・`{id}` が数値でない、または範囲外なら400（`{id}` の範囲外は404）。
- 本文はプレーンテキスト。空行で段落を分け、`html/template` でエスケープして表示する。
- 公開日時: 初めて公開した時に `published_at` を設定する。非公開に戻して再公開しても維持し、変更したい場合は編集画面で明示的に更新する。
- 管理の編集画面はJSなしの通常のフォーム。JSは認証画面（setup・login・passkeys）だけで読み込む。JSが無効な場合は、有効にするよう案内を表示する。
- 管理画面の見た目は `style.css` への追記で賄い、新しいCSSファイルは作らない。
- APIのJSONは `id`、`title`、`body`、`published_at`、`updated_at`（RFC 3339）のみ。下書きや内部の列は返さない。エラーの本文は固定文言とし、入力値を反射しない。

## 5. データ設計

記事・パスキーは匿名の所有者（`owners`）に紐づけない。Coreの30日GCやセッション破棄では消えない。マイグレーションは `features/cms/migration.sql` 1本にまとめ（`test/data_schema_test.go` の照合対象）、全テーブル・列を台帳に載せる。

| テーブル | 列 | 保持・削除 |
|---|---|---|
| `cms_settings` | `id`（1固定）、`webauthn_user_id`（ランダム16バイト）、`created_at` | CMSの利用中は保持。`cms-recover` で再生成 |
| `cms_posts` | `id`、`title`（200字以内）、`body`（5万字以内）、`status`（draft/published）、`published_at`、`created_at`、`updated_at` | 管理者が削除するまで保持。削除は物理削除（`secure_delete` 有効） |
| `cms_credentials` | `id`（credential ID）、`public_key`（SPKI DER）、`alg`、`sign_count`、`label`（端末名・50字以内）、`disabled_at`、`created_at`、`last_used_at` | 管理者が削除するまで保持。最後の有効な1件は画面から削除不可。復旧コマンドで全削除 |
| `cms_admin_sessions` | `token_hash`（SHA-256）、`created_at`、`last_seen_at`、`expires_at` | 無操作2時間または発行から12時間で失効。`Purge` はセッションだけを削除 |
| `cms_setup_tokens` | `token_hash`（SHA-256）、`expires_at`、`used_at` | 発行から15分で失効。使用済み・失効分は `Purge` で削除 |

- WebAuthnの `user.name` は `admin`、`displayName` は「管理者」とし、個人名は持たない。
- challengeはDBに保存しない（7節）。
- 端末名は自由入力のため、個人名を入れないよう画面で案内する。
- 生体情報・パスワードは保存しない。生体認証は端末内で完結する。

## 6. 初回登録と復旧（パスワードなし）

- `make cms-setup`: パスキーが未登録の場合だけ、既存の未使用トークンを無効化してから、1回限り・15分有効の登録URLをターミナルに表示する。トークンはハッシュのみDBに保存する。
- `make cms-recover`: 既存のパスキー・管理者セッション・未使用の登録トークンをすべて削除し、`webauthn_user_id` を再生成して登録URLを再発行する。端末の紛失・盗難時に使う。
- 両コマンドは `cmd/kit` のサブコマンドとし、CMS未初期化のDBにはマイグレーションを先に適用する。`DB_PATH` の既定値は相対パス `data/app.db` のため、リポジトリ直下で実行する（サーバーと同じ `DB_PATH` を指定する）。サーバー稼働中でもWALとbusy_timeoutで動作する。
- **トークンはURLのフラグメントで渡す**（`CMS_ORIGIN/admin/setup#token=...`）。フラグメントはサーバーにもRefererにも送られない。画面のJSが読み取って登録APIのPOST本文で送り、直後に `history.replaceState` でアドレスバーと履歴から消す。追加のCookieは使わない。
- 画面の表示（GET）ではトークンを消費しない。`register/begin` で有効性を確認し、`register/finish` の検証成功時に `UPDATE ... SET used_at = ? WHERE token_hash = ? AND used_at IS NULL AND expires_at > ?` で原子的に消費する（更新0件なら拒否）。
- サーバーを操作できる人を管理者とみなす。登録URLがターミナルの画面共有・スクロールバック・シェル履歴の出力に残る点は、READMEで注意する。
- 認証器は**2つ以上の登録を強く推奨**する。1つを失うと、残りが無い限り `cms-recover` までログインできない。追加は管理画面から、ログイン中にのみ行う。
- 登録の直後にはセッションを発行せず、登録したパスキーで一度ログインしてもらう。attestation none では登録時に秘密鍵の所持を確認できないため、使えない鍵だけが登録されて閉め出される事故を、登録トークンが有効なうちに気づけるようにする。

## 7. WebAuthn（標準ライブラリのみ）

ブラウザの `AuthenticatorAttestationResponse.getPublicKey()` と `getAuthenticatorData()` を使い、CBOR/COSEの解析を避ける。`toJSON()` や `parseCreationOptionsFromJSON()` は使わず、base64urlの変換は自前で行う。`getPublicKey()` が `null`（未対応アルゴリズム）の場合はJSでエラー表示する。

**challenge**
- 32バイト乱数＋有効期限（5分）＋用途（register/login）をHMAC-SHA256で署名した自己完結値とし、発行時にサーバーは何も保持しない。未認証で `begin` を大量に呼ばれても、保持枠が埋まって管理者のログインが妨げられることはない。HMAC鍵は起動時に乱数生成する（再起動で未完了のchallengeは無効）。
- 発行回数の制限は設けない。全体での回数制限は、第三者が上限まで使い切ると管理者も拒否される別のロックアウト経路になり、接続元IPごとの制限はIPを扱わない方針に反するため。
- 再利用（リプレイ）防止のため、**検証に成功したchallengeだけ**を有効期限までメモリに記録する。パスキーを持たない第三者は検証に成功できないため、記録は正当な操作の分しか増えない。「使用済みか確認して記録する」処理はロック内で一体に行い、同じ応答の並行送信は1件だけ成功させる。
- 大量リクエストによるCPU負荷などの負荷攻撃は、キットの範囲外とし、リバースプロキシ等で対策する前提をREADMEに書く。

**登録オプション**
- `authenticatorSelection: { residentKey: "required", userVerification: "required" }`、`attestation: "none"`、`pubKeyCredParams` はES256（-7）・EdDSA（-8）・RS256（-257）。`authenticatorAttachment` は指定しない（`cross-platform` を指定するとWindows Hello等の内蔵認証器が選択肢に出なくなるため）。どの認証器を通すかは、サーバー側のBEフラグ検査だけで決める。

**端末固定の認証器のみ**
- authenticatorDataのBE（Backup Eligibility、ビット3）フラグが立っている認証器は、登録時に拒否する。iCloudキーチェーンやGoogleパスワードマネージャー等の同期型パスキーはBEが立つため登録できない。
- 認証時もBEまたはBS（Backup State、ビット4）が立っていれば拒否する。
- サーバーは機種を証明できない（attestationを検証しない）ため、正確には「同期・バックアップできない認証器」に限定する。外付けのセキュリティキーのほか、端末内蔵で同期しない認証器（Windows HelloのTPMに保存するパスキー等）も通る。機種の証明は、CBOR解析と証明書チェーン検証が必要になるため最初の版では行わない。
- 指紋でログインする方法は、Windows Hello（指紋リーダー）か、指紋センサー付きのセキュリティキー。指紋の無いキーは、ログインのたびにPINの入力を求める（UV必須のため）。
- 同期型として拒否される見込みのもの: MacのTouch ID（iCloudキーチェーン）、スマートフォンのパスキー、Googleパスワードマネージャーに保存するChrome（ChromeOS内蔵の指紋を含む）。最終的な可否は、ブラウザが返すBEフラグで決まる。
- `excludeCredentials` に既存のcredential IDを入れる。

**登録の検証**
1. 登録トークン（未使用・有効期限内）または有効な管理者セッション＋CSRFがあること。
2. `clientDataJSON` の `type` が `webauthn.create`、`challenge` が署名・期限・用途とも正しく未使用、`origin` が `CMS_ORIGIN` と完全一致。`crossOrigin` が真なら拒否。
3. authenticatorDataの `rpIdHash` が `sha256(CMS_RP_ID)` と一致。UP・UV・ATフラグが立っており、BEフラグが立っていない。
4. attested credential dataのcredential IDが `rawId` と一致（ID長の2バイトを読み、CBOR部分は解析しない）。credential IDは1023バイト以下。
5. `getPublicKey()` のSPKIを `x509.ParsePKIXPublicKey` で読み、宣言されたアルゴリズムと鍵の型が一致すること。ES256は曲線がP-256、RS256は法が2048ビット以上、EdDSAはEd25519。
6. 既存のcredential IDとの重複を拒否。attestationは検証しない（`none`）。

**認証オプションと検証**
- `allowCredentials` は空、`userVerification: "required"`（ユーザー名を入力しないdiscoverable credentialでのログイン）。
1. `type` が `webauthn.get`、challenge・origin・`crossOrigin` を登録と同様に確認。
2. `rawId` で `cms_credentials` を検索し、存在しない・無効化済みなら拒否。`userHandle` は必須とし、`webauthn_user_id` と一致すること（`allowCredentials` 空の認証では仕様上必須。`residentKey: required` のため正当な認証器は必ず返す）。
3. `rpIdHash`、UP・UVフラグが立ち、BE・BSフラグが立っていないことを確認。
4. 署名対象は `authenticatorData || sha256(clientDataJSON)`。ES256は `sha256(対象)` に `ecdsa.VerifyASN1`、RS256は `sha256(対象)` に `rsa.VerifyPKCS1v15`、Ed25519は**ハッシュせずに**対象そのものに `ed25519.Verify`。
5. 署名カウンタ: 新値と保存値の**どちらかが0でなければ**、新値 <= 保存値を拒否する（複製の疑い）。拒否時は、そのパスキーを無効化（`disabled_at`）し、**管理者セッションをすべて削除**する。複製したキーで先にログインした者がいても、どのセッションか見分けられないため。案内は下表のとおり。
6. 成功したら管理者セッションを新規発行し、`last_used_at` と `sign_count` を更新する。`sign_count` は `UPDATE ... WHERE id = ? AND sign_count < ?` で単調増加を保つ（並行ログインで小さい値が後から書かれないように）。

**呼び出し側で保証すること**
- credential IDの重複は、`cms_credentials.id` の主キー制約によるINSERTの失敗で拒否する（アプリ側の事前検索だけに頼らない）。
- WebAuthnのPOSTには、Coreの1MBより小さい本文サイズ上限（64KB）を掛ける。
- `CMS_ORIGIN` は起動時に正規化済み（既定ポート・末尾スラッシュを除去）のものを、clientDataの `origin` と完全一致で比較する。

**複製の疑いを検出したときの案内**

検出はログインの途中で起きるため、画面を見ているのが正規の管理者か攻撃者か区別できない。案内の場所と内容を分ける。

| 場所 | 見る人 | 内容 |
|---|---|---|
| ログイン画面のエラー | ログインを試みた人 | 「このパスキーではログインできません。別のパスキーでログインするか、サーバーで `make cms-recover` を実行してください。」複製の疑いとは書かない |
| サーバーのログ | サーバーの運用者 | 固定のイベント名 `cms passkey disabled`。credential ID等は書かない |
| 管理画面の上部 | 別のキーでログインした管理者 | 「無効化されたパスキーがあります」の警告と、パスキー管理画面へのリンク |
| パスキー管理画面 | 同上 | 該当キーに「無効（複製の疑い）」と無効化日時を表示。削除できる |

カウンタを増やさない認証器では、この検出は働かない。多くの外付けセキュリティキーはカウンタを増やすが、全機種・Windows Helloでの動作は保証しない。

**ブラウザ対応**: `getPublicKey()` / `getAuthenticatorData()` の対応バージョンは、READMEに書く前にMDN等の一次資料で確認する。

## 8. セッション・CSRF・ヘッダー・CORS

- 管理者Cookie: 名前 `cms_admin`、`Path=/admin`、HttpOnly、`SameSite=Strict`、`CMS_ORIGIN` がhttpsなら必ずSecure。トークンは32バイト乱数、DBにはSHA-256のみ。
- CSRF: 管理者セッショントークンのHMACをフォームに埋め込み、全POSTで検証。加えて全POSTで `Sec-Fetch-Site: same-origin` であることを確認し、`Origin` ヘッダーが `null` 以外の値で送られた場合は `CMS_ORIGIN` との一致も確認する。
  - Originの一致を必須にしない理由: Coreは全ページで `Referrer-Policy: no-referrer` を送るため、Fetchの仕様上、同一オリジンのフォーム送信・fetchでも `Origin: null` になる。`Referrer-Policy` の緩和は承認境界#5に当たるため行わない。
- ログイン成功時にセッションを新規発行（固定化対策）。ログアウト・失効時はDBから削除しCookieを消す。
- パスキーを削除・無効化したときは、現在のセッション以外の管理者セッションをすべて削除する。
- CSP: 認証画面のハンドラだけ、ポリシー全文を `default-src 'self'; style-src 'self'; script-src 'self'; connect-src 'self'; form-action 'self'; frame-ancestors 'none'; object-src 'none'; base-uri 'self';` に置き換える。インラインスクリプトは使わない。
- 公開ページ（`/news`、`/api/news`）は独立経路のため、CoreのCookieも `cms_admin` も照合しない（`cms_admin` は `Path=/admin` のため送られない）。
- CORS: 許可リストの各値を起動時に `scheme://host[:port]` へ正規化し、`*`・パス付き・不正値は起動エラー。`Origin` が許可リストと一致する場合のみ `/api/` の応答に `Access-Control-Allow-Origin: <その値>` を付ける。`Origin: null` は拒否。`Vary: Origin` は `/api/` の全応答に付ける。資格情報は許可しない。`OPTIONS` は許可元のみ204で `Access-Control-Allow-Methods: GET, HEAD` を返す。`/admin` と `/news` には一切CORSヘッダーを出さない。
- `Cache-Control` はCoreの `no-store` を維持する。

## 9. 設定と起動時検査

| 環境変数 | 既定値 | 内容 |
|---|---|---|
| `CMS_RP_ID` | `localhost` | WebAuthnのRP ID（ドメイン名。IPアドレス不可） |
| `CMS_ORIGIN` | `http://localhost:8080` | 許可するオリジン。`localhost` 以外は `https://` 必須 |
| `CMS_CORS_ORIGINS` | 空（許可なし） | APIを読める別オリジンのカンマ区切り一覧 |

- `Init(db)` でマイグレーション適用後に検査し、errorを返して起動を止める（Siteのpanic方式ではなく、Coreの `Init` のエラー処理に合わせる）。
- 検査内容: `CMS_ORIGIN` の形式、`CMS_RP_ID` が `CMS_ORIGIN` のホストと同一またはその上位の登録可能ドメインであること、`CMS_ORIGIN` がhttpsなら `SECURE_COOKIE=true`、`CMS_CORS_ORIGINS` の形式。
- 開発時は `http://127.0.0.1:8080` ではなく `http://localhost:8080` で開く。

## 10. ログ

- 記録する: リクエストID、固定のイベント名（例: `cms login succeeded`、`cms login rejected`、`cms passkey disabled`、`cms post updated`）、記事ID。
- 記録しない: credential ID、公開鍵、userHandle、各種トークン、challenge、記事のタイトル・本文、端末名、`Origin` ヘッダー値、CORS許可元、拒否理由の詳細値。
- カナリアテストで、トークン・challenge・credential ID・本文・端末名がログと応答本文に出ないことを確認する。

## 11. キットとの統合と既存記述の更新

- `cmd/kit` を一般化し、Site・CMSの登録切り替えを同じ保護（識別コメント `// minimal-web-kit: bundled-cms-template`、マーカー検査、シンボリックリンク拒否）で行う。
- CMSはトップページを置き換えない。Siteと同時に有効化できる。
- 更新対象: `docs/data-inventory.md`、README（「JavaScriptは使用しません」の例外、CMSの説明、`cms-setup` の実行場所と注意）、AGENTS.md（JS・CSPの例外範囲）、`skills/minimal-web-kit-design` と `skills/minimal-web-kit-audit` の「CMSは拡張例」の記述、`test/template_test.go` のJS禁止の例外（認証画面に限定）、`test/canary_test.go` の公開パス一覧（`/news`、`/api/news`）、LPのCMS説明。

## 12. テスト

- Core拡張: 独立経路がCoreのCSRF・セッション解決を通らないこと、経路衝突の起動エラー、`Purge` の呼び出し。
- WebAuthn: テスト内で生成した鍵（ES256/Ed25519/RS256）での正常な登録・認証。challengeの署名不正・期限切れ・用途違い・再利用・並行送信（1件だけ成功）、検証失敗では使用済みとして記録しないこと、`begin` を大量に呼んでも正当なログインが成功すること、origin・RP ID・`crossOrigin` の不一致、UV未設定、BEフラグ付きの登録拒否、BE・BSフラグ付きの認証拒否、署名不正、カウンタ逆行（保存5→新0を含む）での無効化と全セッション削除、credential ID不一致・重複、P-256以外の曲線・2048ビット未満のRSA、`userHandle` 不一致。
- 登録トークン: GETで消費しないこと、1回性（並行finishで1件だけ成功）、期限、`cms-setup` の再発行で旧トークンが無効になること、`cms-recover` で既存パスキー・セッション・トークンが消えること。
- セッション: 無操作・絶対期限の失効、ログアウト、パスキー削除時の他セッション削除、`Purge` がセッションとトークンだけを消し記事・パスキーは残ること。
- CSRF・Origin検査、未ログインでの管理画面アクセス拒否。
- 公開ページ・APIで下書きが見えないこと、Set-Cookieが出ないこと、パラメータ不正の400、並び順。
- CORS: 許可元のみヘッダー付与、`null` 拒否、`/admin`・`/news` にヘッダーが出ないこと、起動時の設定検査。
- CSPが認証画面だけ緩和され、他のページは `script-src 'none'` のままであること。
- `make add-cms` / `remove-cms` の保護（Siteと同等）、台帳と実スキーマの照合、ログ・応答本文のカナリア。

## 13. 実機での確認

利用者の手元の認証器は、Windows Hello用の外付け指紋リーダー。WebAuthnは `localhost` かHTTPSでしか動かないため、Windows上でキットを動かして確認する。

1. WindowsにGit・Goを入れ、リポジトリをクローンする（makeが無ければ `go run ./cmd/kit ...` と `go run ./cmd/server` を直接実行する）。
2. CMSを有効化し、`http://localhost:8080` で起動する。
3. `cms-setup` の登録URLを開き、Windows Helloの指紋で登録する。BEフラグが立って拒否された場合は、エラー表示とログで判別できるようにする。
4. ログイン・ログアウト・記事の作成と公開・パスキーの追加と削除・`cms-recover` を確認する。

## 14. 進め方

1. 本計画とデータ台帳の更新案の確定。
2. Core拡張（独立経路・`Purge`）、`cmd/kit` の一般化、CMSの骨組み、マイグレーション、記事ストア、台帳の更新。
3. 公開HTML・読み取りAPI・CORS（ログイン不要で動作確認できる部分）。
4. WebAuthnの登録・認証、管理者セッション、`cms-setup` / `cms-recover`。次の3つに分けてコミットする。
   1. WebAuthnの検証ロジック（標準ライブラリのみ・テスト内で生成した鍵で検証）。完成時に外部モデルのレビューを受ける。
   2. 管理者セッション・登録トークン・`cms-setup` / `cms-recover`。
   3. 認証画面・JS・経路。Windows Helloの実機で登録・ログインを確認する。
5. 記事の管理画面、パスキー管理。
6. README・LP・AGENTS.md・スキル・検査の更新、監査。

各段階でテスト・`make verify`・`make build` を通してからコミットする。

## 15. 守る範囲・守らない範囲

- WebAuthn検証を自前で書くため、仕様の取りこぼしが認証の欠陥に直結する。12節のテストで個別に固定する。
- 古いブラウザや、discoverable credential・`getPublicKey()` に対応しない認証器では登録できない。
- 端末固定の認証器に限るため、キーを全て紛失した場合はサーバーで `cms-recover` を実行するまでログインできない。
- 複製の検出は、署名カウンタを増やす認証器でのみ働く。
- 大量リクエストによる負荷攻撃は、リバースプロキシ等で対策する。
- 管理者1人の前提のため、権限分離や操作の監査証跡は持たない。
- challengeのHMAC鍵・使用済み一覧はメモリ上のため、複数プロセス構成には対応しない（単一バイナリ・単一プロセスの前提）。
