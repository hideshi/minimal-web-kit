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
   - `Store` インターフェースに操作を追加し、`MemoryStore` と `SQLiteStore` の両方に実装する。
   - DBスキーマを変更する場合は `internal/store/migrations/` にSQLを追加し、同時に `DATA.md` を更新する。
3. **Web (`internal/web/`)**:
   - `Handler` にルートとハンドラ関数を追加する。
   - Handlerは `OwnerID` を使ってデータの所有権を分離し、他人のデータにアクセスさせない。
   - 状態変更（POST等）には必ずCSRFトークンを検証する。
4. **Views & CSS (`internal/web/views/`, `static/css/`)**:
   - テンプレートにインライン `style="..."` や `<style>`, `<script>` を書かない。
   - 意匠は `style.css` のクラスまたはHTMLセマンティックタグで表現する。
   - フォーカスインジケータ（`:focus-visible`）や十分なコントラスト比を維持する。
