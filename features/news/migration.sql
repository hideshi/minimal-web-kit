-- migration.sql: お知らせ機能のスキーマ定義
-- 記事は匿名の所有者（owners）に紐づけない。CoreのセッションGCやセッション破棄では削除されない。
-- 管理者のパスキーとセッションは、同梱機能の管理者認証（features/auth）が持つ。

-- お知らせのカテゴリ。カタログの分類とは共有しない。削除するまで保持する。AUTOINCREMENTで削除済みIDを再利用しない
-- （公開の絞り込みのURL /news?category= が、消した後に別のカテゴリを指さないため）。
CREATE TABLE IF NOT EXISTS news_categories (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL UNIQUE,
    created_at INTEGER NOT NULL
);

-- お知らせ。削除するまで保持する。AUTOINCREMENTで削除済みIDを再利用しない（APIの利用側の誤参照を防ぐ）。
CREATE TABLE IF NOT EXISTS news_posts (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    title TEXT NOT NULL,
    body TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('draft', 'published')),
    published_at INTEGER,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    -- 代表画像のファイル名。画像機能が無い構成でも保存できるよう、外部キーは付けない
    cover_file TEXT,
    -- カテゴリ（1記事に1つ。無ければNULL）。カテゴリを消すと NULL に戻る
    category_id INTEGER REFERENCES news_categories(id) ON DELETE SET NULL
);

CREATE INDEX IF NOT EXISTS idx_news_posts_published ON news_posts(status, published_at DESC, id DESC);
