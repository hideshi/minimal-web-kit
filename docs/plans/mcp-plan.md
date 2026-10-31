# MCP対応の計画

状態: 計画のレビュー待ち（2026-10-31）。今の仕様ではない。

## 目的

AIエージェントが、MCPを通じて、お知らせ・カタログ・固定ページを読み、登録・編集できるようにする。読み取りは確認なしで行い、変更はクライアントの利用者の確認を経る。別の機能も同じ口で足せるようにし、機能ごとの有効・無効を管理者が決める。認証はキットの共通認証基盤を使う。

## 確定した判断

- 確認はクライアント側で利用者に求める。サーバーに承認待ちの表や承認画面は作らない。
- サーバーは、確認の手順を通らない書き込みを拒否する。人の操作かは証明できない。クライアントを自動応答に設定した場合は、その利用者の責任とする。
- 読み取りは確認なしで、下書きも読める。
- 初版に削除は入れない。公開と非公開は編集と別のツールにする。
- 境界2（設定とトークンの表）と境界5（`/mcp` と `.well-known` の公開経路）は承認済み。
- まずキットに入れる。紹介サイトへの適用は別に決める。

## 実測（Claude Code 2.1.316、手元の調査用サーバー）

| 確かめたこと | 結果 |
|---|---|
| 接続 | `MCP-Protocol-Version: 2026-07-28`、`server/discover`、`Mcp-Method`・`Mcp-Name`、応答はJSONで動く |
| 能力の宣言 | 要求の `_meta` の `clientCapabilities` に `elicitation` がある |
| `_meta["anthropic/requiresUserInteraction"]` | 非対話では許可の設定があってもサーバーに届かない。対話では確認が出る |
| `input_required` の `elicitation/create` | 対話では画面が出て、サーバーの文がそのまま表示され、`requestState` と `inputResponses` で送り直す |
| 応答 | 承諾は `accept` で実行。却下は `decline`、非対話は `cancel` で実行しない |
| 確認の回数 | 2回（許可の確認、elicitation） |
| 確認の形 | 必須の欄を置くと承諾だけで進めない。空の `properties` なら承諾・却下だけになる |

確かめていないこと: Claude Code以外のクライアント、事前登録のクライアントIDでのOAuth接続、`bypassPermissions` やフックでの動き。

## 全体の形

| 部品 | 役割 |
|---|---|
| `internal/mcp` | JSON-RPC、ツールの一覧と呼び出し、確認の組み立てと検証。標準ライブラリのみ |
| `features.MCPFeature` | 機能がツールを申告する口 |
| `features/mcp` | 任意の機能。`/mcp`、保護リソースの情報、`/admin/mcp` の設定画面 |
| `features.TokenVerifier` | Bearerトークンの検証の口。`auth/oidcop` が実装し、後で `federatedauth` も実装できる |

```go
type MCPEffect int

const (
	MCPWrite MCPEffect = iota // ゼロ値は書き込み。宣言を忘れても確認なしにしない
	MCPRead
)

type MCPTool struct {
	Name, Title, Description string
	InputSchema              json.RawMessage
	Effect                   MCPEffect
	Call                     func(ctx context.Context, p MCPPrincipal, args json.RawMessage) (MCPResult, error) // 読み取りの実行
	Prepare                  func(ctx context.Context, p MCPPrincipal, args json.RawMessage) (MCPPlan, error)   // 検査して、確認の文と対象の版を返す。変更しない
	Apply                    func(ctx context.Context, p MCPPrincipal, args json.RawMessage, version string) (MCPResult, error)
}

type MCPFeature interface {
	Feature
	MCPTools() []MCPTool
}
```

書き込みのツールで `Prepare` か `Apply` が無ければ、起動時にエラーにする。ツール名は機能名を接頭辞にし、重複は起動時にエラーにする。

## 書き込みの流れ

1. トークンのスコープ `mcp:write`、機能の有効、引数を確かめる。
2. クライアントが `elicitation` のフォームを宣言していなければ、変更せずにツールのエラーを返す。
3. `Prepare` で検査し、サーバーが作った文と `requestState` を付けて `input_required` を返す。
4. 送り直しで `requestState` を検証する。アカウント、クライアント、ツール、引数のハッシュ、対象の版、期限、一回限りの番号を、HMACで結びつける。
5. `accept` のときだけ、機能の有効とスコープをもう一度確かめて、`Apply` を実行する。対象の版が変わっていたら実行しない。
6. `decline` と `cancel` では何も変更しない。

- `requestState` の鍵は起動ごとにメモリで作る。使用済みの番号もメモリに持ち、期限（10分）で捨てる。再起動で確認は無効になる。DBには保存しない。
- 確認の文はサーバーが作る。エージェントの説明は混ぜない。本文が長いときの見せ方は未決（下の論点）。
- `requiresUserInteraction` は書き込みのツールに付ける。確認は2回になるが、1回目は設定で飛ばしにくく、2回目は内容を見せられるため、両方残す。
- `annotations` はヒントとして付ける。守りの根拠にしない。
- `Apply` は管理画面と同じ検査・保存の関数を通す。固定ページのナビの更新やスラッグの検査のような処理を別に書かない。今はハンドラにある処理を、機能ごとの関数へ切り出す。

## 初版のツール

| 機能 | 読み取り | 書き込み |
|---|---|---|
| お知らせ | 一覧、取得 | 作成、編集、公開、非公開 |
| カタログ | 一覧、取得、カテゴリとタグの一覧 | 作成、編集、公開、非公開 |
| 固定ページ | 一覧、取得 | 作成、編集、公開、非公開 |

カタログのカテゴリとタグの追加・編集は入れない。項目の編集の `NewTags` の扱いは実装の前に決める。

## 有効・無効

- 表 `mcp_feature_settings`（機能名、有効、更新日時）。行が無ければ無効。
- `/admin/mcp` は管理者だけが変えられる（`AdminPage`・`AdminForm`）。画面の型は一覧。
- 無効の機能は `tools/list` に出さず、呼び出しも拒否する。確認の途中で無効にされたら実行しない。
- `MCPFeature` を実装した機能が増えても、既定で無効のまま。

## 認証

MCPサーバーはリソースサーバーとして動く。今の `oidcop` はログイン専用で、次を足す。

| 項目 | 内容 |
|---|---|
| アクセストークン | 短命のJWT（ES256）。`aud` はMCPのリソース、スコープは `mcp:read`・`mcp:write`、世代を含む |
| `resource` | RFC 8707の値を検査し、別のリソース向けのトークンを受け付けない |
| 公開クライアント | クライアント秘密なし、PKCE S256必須、ループバックの戻り先（ポート可変） |
| リフレッシュトークン | ハッシュを保存し、使うたびに入れ替える。再利用を検出したら系列を失効させる |
| 要求ごとの検証 | 署名、期限、`aud`、スコープに加えて、世代でアカウント停止や許可の取り消しを反映する |
| サーバーの案内 | `/.well-known/oauth-protected-resource`、認可サーバーの情報、401の `WWW-Authenticate` |
| 同意の画面 | ログイン先の確認に、クライアント名と読み取り・書き込みの別を出す |
| 登録 | コマンドで事前に登録する。CIMDとDCRは初版に入れない（外部通信と保存の範囲が増える） |

`internal/mcp` と `features/mcp` はOIDCのライブラリをimportしない。`TestArchitecture_FederationDependenciesStayOptional` を保つ。検証の口が無ければ `features/mcp` は起動時にエラーにする。

## HTTPの守り

- `POST /mcp` だけ。GETとDELETEは405。応答はJSONのみで、SSEと `Mcp-Session-Id` は使わない。
- CookieもCSRFトークンも使わない独立経路。`Origin` があって自サイトでなければ403。CORSは許可しない。
- 本文の大きさ、JSONの深さ、バッチなしを固定で制限する。
- `MCP-Protocol-Version` が未対応なら400。ヘッダーと `_meta` の不一致も400。対応するのは `2026-07-28` だけで始める。
- トークンごとの回数制限をメモリで持つ。超えたら429。
- ログには固定の分類とツール名だけを出す。引数、本文、トークンを出さない。カナリアで確かめる。

## 保存データ（境界2）

`mcp_feature_settings` と、リフレッシュトークンのハッシュ、公開クライアントの列。書き込みの引数は保存しない。保持と削除は `docs/data-inventory.md` に書き、プライバシーポリシーの見直しを `minimal-web-kit-privacy` で行う。

## 不変条件（案）

| 番号 | 条件 |
|---|---|
| MCP-INV-1 | 宣言の無いツールは書き込みとして扱う |
| MCP-INV-2 | 書き込みは、検証済みの確認の `accept` が無ければ実行しない |
| MCP-INV-3 | 確認は、アカウント、クライアント、ツール、引数、対象の版に結びつき、一度だけ使える |
| MCP-INV-4 | 無効の機能のツールは一覧に出ず、呼び出せず、確認の途中でも実行しない |
| MCP-INV-5 | 他のリソース向けのトークン、失効したアカウント、足りないスコープは拒否する |
| MCP-INV-6 | 書き込みは管理画面と同じ検査と保存を通る |
| MCP-INV-7 | 引数・本文・トークンをログに出さない |
| MCP-INV-8 | 確認の文はサーバーが作り、利用者の入力をエージェントの指示として混ぜない |

## 進める順番

1. レビューの指摘を反映する。OAuth接続の試作で、事前登録が実際のクライアントで動くか確かめる。
2. `docs/domain/mcp.md`、`docs/ui/use-cases.md`、`docs/data-inventory.md` を先に更新する。
3. `internal/mcp` とトークンの検証。
4. 有効・無効の設定と `features/mcp`。
5. 3機能の処理の切り出しとツール。
6. Claude Codeで通し、トップ・READMEと同梱機能のカードを更新し、完成のレビューを受ける。

## 論点

1. 本文が長いとき、確認の文に何を出すか。全文は読めず、抜粋では見ていない部分が通る。
2. 事前登録のクライアントIDを、主なクライアントが受け付けるか。受け付けなければCIMDが要り、外部通信（境界3）とSSRF対策が増える。
3. 公開と編集を分けたとき、公開中の記事の編集は確認の文で強調するか。
4. 記事の本文が他者由来のとき、読み取りの結果がプロンプトインジェクションの経路になる。ツールの結果で信頼できない内容と示す以上の対策が要るか。
5. リフレッシュトークンの期限と、アクセストークンの期限。

## 試作品（everything-mcp）の扱い

コードは使わない。依存（Gin、GORM等）、APIの中継と多テナントの構成、`2025-03-26` の仕様がキットと合わない。承認の検査でエラーが起きると実行へ進む点は、まねをしない。

## 作らないもの

サーバー側の承認待ち、削除のツール、お問い合わせのツール、SSE、Tasks、CIMD、DCR、stdioの配布。
