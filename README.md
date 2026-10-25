# minimal-web-kit

個人情報の取得と外部依存ライブラリを最小限に抑え、安全で身軽なWebアプリやWebサイトを手早く作れる「Go実装のひな形 ＋ 少数のAIスキル」です。

---

## 特徴

- **完全クリーンな初期状態**:
  リポジトリをクローンした直後は、サンプルの残骸が一切ない純粋な「クリーン基盤（Base）」です。
- **自作機能を壊さないフィーチャー分離構造**:
  機能モジュールは `features/<機能名>/` に独立して作成できます。テンプレートの追加・削除を行っても自作コードは1ミリも上書きされません。
- **実用的なLP / ホームページテンプレート**:
  JavaScript不要、新規セッションCookieを発行せず、表示処理ではDBに保存しないアクセシブルなLPテンプレート（ヒーロー、特長カード、仕組み、HTML標準`<details>`によるFAQアコーディオン）を同梱。ワンコマンドで着脱できます。同梱サーバーは起動時にSQLiteを初期化します。CMSは拡張例であり、CMSテンプレート・管理者認証は未同梱です。
- **個人情報の取得最小化と保護**:
  氏名・連絡先等は既定で取得しません。必要な情報を追加する際は利用目的・保存項目・保持期間を整理し、承認を得て台帳・検査を更新します。OwnerIDによるデータ分離は、管理者認証や業務上の権限管理の代替ではありません。
- **Go標準中心・約14MBの単一バイナリ**:
  Go標準機能（`net/http`、`html/template`、`embed.FS`、`log/slog`）を中心に構成。直接依存は純Go版SQLiteドライバ1本のみに抑え、フロントエンドビルド（Node.js/npm）やCGOを排除。どこでも動く約14MBの単一バイナリを生成します。
- **公的Webアクセシビリティ指針に基づく機能的UI**:
  派手な装飾を排し、JIS X 8341-3 / WCAG 2.1 AA レベルの高いコントラスト比、明瞭なフォーカスインジケータ、セマンティックなHTML構造によるアクセシブルな自前CSS 1本を同梱。
- **データ保護・ログ契約**:
  アクセスログはサーバー生成のリクエストID、既知のHTTPメソッド（その他はOTHER）、応答状況、処理時間に限定します。URLパス・クエリ・Cookie・IP・User-Agent・本文や、生のpanic値・エラー内容はアプリケーションログに記録しません。追加機能や公開環境のログも別途確認が必要です。`owners` テーブルと `ON DELETE CASCADE` により、セッション破棄時に稼働DB上の関連レコードが安全に連動削除されます。
- **規約はスキルに、強制はテストに**:
  3本の軽量なAIスキル（計画・実装・検証）と、依存関係やデータ台帳の逸脱を検知する機械的テストを同梱。

---

## 起動方法

### 前提
- Go 1.27.1 以上（`go.mod` の指定に従う）

### 実行（クリーン基盤）
```bash
# クローンと起動
git clone <repository-url>
cd minimal-web-kit
go run ./cmd/server
```
ブラウザで `http://127.0.0.1:8080` にアクセスしてください。

### LP / ホームページテンプレートを試す
製品やサービスの紹介Webサイトとしてすぐに公開できるLPテンプレートを、ワンコマンドで有効化・無効化できます：
```bash
# LP / ホームページテンプレートの有効化
make add-site

# 起動して試す（トップページがLPになります）
go run ./cmd/server

# LPテンプレートを無効化（クリーン基盤に戻ります。自作機能には影響しません）
make remove-site
```

### 推奨するAI開発ツールとスキル
Claude Code、Codex、Cursor、Antigravityのいずれかでリポジトリのルートを開き、計画・実装・検証のスキルを利用する編集方法を推奨します。ツールの導入・認証は別途必要です。

| ツール | リポジトリ内のスキル探索先 |
|---|---|
| Claude Code | `.claude/skills/<name>/SKILL.md` |
| Codex | `.agents/skills/<name>/SKILL.md` |
| Cursor | `.agents/skills/<name>/SKILL.md` |
| Antigravity | `.agents/skills/<name>/SKILL.md` |

3本の名前は `minimal-web-kit-design`（設計）、`minimal-web-kit-implementation`（実装）、`minimal-web-kit-audit`（監査）です。一般的な要件定義・機能別実装・テストのスキルと併用し、このキット固有の判断と規約を補完します。

正本は `skills/<name>/SKILL.md` です。探索先のスキルディレクトリは正本への相対シンボリックリンクで、コピーの二重管理を避けています。シンボリックリンクを保持する環境でクローンしてください。Windows等でリンクが通常ファイルになった場合は、利用するツールの探索先に正本のフォルダをコピーし、更新時も同期してください。

最初に `AGENTS.md` を読み込ませてください。Claude Codeには `CLAUDE.md` からの参照も用意しています。依頼例：

> AGENTS.mdを読み、minimal-web-kit-designで変更を計画し、minimal-web-kit-implementationで実装、minimal-web-kit-auditで検証してください。

開始後、ツールのスキル一覧で3本が認識されていることを確認してください。表示されない場合は再起動するか、`skills/<name>/SKILL.md` を明示して読ませてください。配置と形式を検査していますが、4ツールそれぞれでの実機動作は未検証です。

配置仕様の参照先：[Claude Code](https://code.claude.com/docs/en/skills)、[Codex](https://learn.chatgpt.com/docs/build-skills)、[Cursor](https://cursor.com/docs/skills)、[Antigravity](https://antigravity.google/docs/skills)。

Site有効時の `/privacy` は公開サイトの閲覧・お問い合わせ・ログを説明し、セッション表と破棄リンクを表示しません。Baseではセッション機能の保存項目を説明します。Siteの `/` と `/privacy` はGET/HEAD時に既存Cookieも照合しません。サーバー起動時のSQLite初期化やログ出力は別に行います。

### 自作機能（Feature）の作り方
1. `features/<機能名>/` ディレクトリを作成し、モデル・SQL・ハンドラを配置します。
2. `cmd/server/features.go` に登録コードを1行追記します。
3. DB列を追加する場合は `docs/data-inventory.md` に保存項目・利用目的・保持期間・削除方法を追記します。

### テスト実行
```bash
go test -race -v ./...
```

---

開発時に作成する仕様・設計などのドキュメントは `docs/` にまとめます。取得・保存データの管理方針の正本（SoT）は `docs/data-inventory.md` とし、同じ台帳を別の場所に重複させません。`AGENTS.md` はエージェント向けの入口としてルートに置きます。

台帳は利用目的・保存先・保持期間・削除方法を管理します。別途テーブル定義書を作成する場合、DBの型・制約・関連はそちらで管理し、台帳と相互参照して管理方針を二重に記載しません。現状は独立したテーブル定義書を持たず、台帳に照合用のテーブル・列一覧と型等の補助情報を併記しています。台帳テストが検査するのは実スキーマとのテーブル名・列名の一致です。

`make add-site` / `make remove-site` は台帳を更新しません。`make test` は台帳を読み取って検査します。台帳の更新は、承認された変更に合わせて開発者・AIが行います。

## プロジェクト構成

```
.
├── AGENTS.md               # 開発規約・承認必須境界・コミット規約
├── docs/                   # 開発ドキュメント・正本（SoT）
│   └── data-inventory.md   # 取得・保存データ台帳（目的・保持・削除の正本）
├── LICENSE                 # MIT License
├── Makefile                # ビルド・テスト・機能管理コマンド
├── cmd/
│   ├── server/             # 起動エントリーポイント
│   │   ├── main.go
│   │   └── features.go     # 機能モジュールの登録（1行追記で有効化）
│   └── kit/                # 機能モジュール管理ツール
├── internal/
│   ├── app/                # アプリケーション初期化・サーバー管理
│   ├── domain/             # Coreドメイン型 (OwnerID)
│   ├── store/              # Core永続化 (owners, sessions, CASCADE削除, SQLite)
│   └── web/                # Core Web基盤 (セッション, CSRF, ミドルウェア, style.css)
├── features/               # 独立した機能モジュール群（自作機能もここに追加）
│   └── site/               # 実用的なLP・ホームページテンプレート（JS不要・表示時のDB保存なし）
├── skills/                 # AIペアプログラミング用スキル（各150行以内）
└── test/                   # 機械的強制テスト（依存、テンプレート、docs/data-inventory.md、カナリア）
```

---

## ライセンス
[MIT License](LICENSE)
