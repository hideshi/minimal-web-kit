---
name: implement-and-change
description: minimal-web-kitのアーキテクチャ規約、標準の既定解、およびCSS/HTML規約に沿って安全に機能を実装・変更する。
---

# 実装・変更スキル (implement-and-change)

このスキルは、ひな形の構造と標準ライブラリの既定解に従ってコードを追加・変更するための手順です。

## 1. 標準機能での既定解一覧
AIが安易に外部ライブラリを追加しないよう、以下の標準解を優先して使用します。

| 目的・課題 | 推奨する標準解 | 避けるべき外部依存 |
|---|---|---|
| ルーティング | `http.NewServeMux` (Go 1.22+ パターン構文) | gin, echo, chi |
| 画面描画 | `html/template` (SSR, 自動HTMLエスケープ) | react, vue, 外部テンプレートエンジン |
| 識別子・トークン | `crypto/rand` (16バイト乱数のHex文字列) | google/uuid などの外部ライブラリ |
| ハッシュ化 | `crypto/sha256` | 外部ハッシュライブラリ |
| 構造化ログ | `log/slog` ＋ `slog.LogValuer` | zap, logrus |
| アセット配信 | `embed.FS` ＋ `http.FS` | 外部アセットサーバー、外部ビルドツール |
| 入力検証 | Go標準の手書き条件分岐と文字数チェック | go-playground/validator |
| マイグレーション | 埋め込みSQLファイル (001_init.sql等) | migrate, goose |

## 2. レイヤーと変更手順
1. **Domain (`internal/domain/`)**:
   - 業務エンティティを定義する。
   - 機密情報や本文を含む型には必ず `slog.LogValuer` を実装し、ログへの値流出をブロックする。
2. **Store (`internal/store/`)**:
   - 既存の `SessionStore` とFeature固有の責務を確認し、必要なインターフェースに操作を追加する。対応するメモリ・SQLite実装の振る舞いを揃える。
   - DBスキーマを変更する場合は `internal/store/migrations/` にSQLを追加し、同時に `DATA.md` を更新する。
3. **Web (`internal/web/`)**:
   - `Handler` にルートとハンドラ関数を追加する。
   - Handlerは `OwnerID` を使ってデータの所有権を分離し、他人のデータにアクセスさせない。
   - 状態変更（POST等）には必ずCSRFトークンを検証する。
4. **Views & CSS (`internal/web/views/`, `static/css/`)**:
   - テンプレートにインライン `style="..."` や `<style>`, `<script>` を書かない。
   - 意匠は `style.css` のクラスまたはHTMLセマンティックタグで表現する。
   - フォーカスインジケータ（`:focus-visible`）や十分なコントラスト比を維持する。

## 3. データ保護とセッションの拡張
- 取得する入力は形式・UTF-8・長さ・本文サイズをサーバー側で検証し、解析エラーを扱う。`template.HTML` 等による自動エスケープの回避は行わない。
- Featureの検索・更新・削除は、リクエストの識別子だけを信用せずOwnerIDで絞る。共有・管理者操作は別途権限を検証する。
- 管理機能には明示的な認証を実装する。無記名セッションを持つだけで管理者にしない。状態変更をGETで実装せず、CSRF保護と認可を両方適用する。
- SQLiteの接続別PRAGMAを全接続に適用し、外部キーのCASCADE、期限切れ照会、GC、削除後の再アクセスを確認する。
- 公開ページでは `EnsureSession` を呼ばない。ハンドラ単体でなく、Routes経由でCookie発行と保存件数を確認する。サーバー起動時のDB初期化とページ表示時の保存は区別する。
- ログには生の入力・URLパス・クエリ・ヘッダー・panic値・DBエラーを安易に渡さず、固定の分類とサーバー生成IDを使う。`LogValuer` だけで文字列や外部ライブラリのエラーは保護できない。
- 削除の説明は稼働DB上のレコード削除の範囲に限定し、WAL・バックアップ・外部フォームの保持を別途説明する。

## 4. UIを拡張するとき
- 見出しをh要素で表し、lang、ランドマーク、スキップリンク、入力ラベル、表見出しと外部リンクの説明を維持する。
- 通常・hover・focusの文字色と背景色、入力境界とフォーカスの識別を確認する。カード数に応じた列数と狭い画面での1列表示を設計し、固定幅・固定高さで文字を切らない。
- JavaScriptを使わず、標準のリンク・button・details/summaryで操作を提供する。外部フォームURLは実際の窓口に設定し、新規タブの通知と `noopener noreferrer` を維持する。
