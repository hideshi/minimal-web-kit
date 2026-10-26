-- migration.sql: CMS（お知らせ機能）のスキーマ定義
-- 記事は匿名の所有者（owners）に紐づけない。CoreのセッションGCやセッション破棄では削除されない。
-- 管理者のパスキーとセッションは、同梱機能の管理者認証（features/auth）が持つ。

-- お知らせ。削除するまで保持する。AUTOINCREMENTで削除済みIDを再利用しない（APIの利用側の誤参照を防ぐ）。
CREATE TABLE IF NOT EXISTS cms_posts (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    title TEXT NOT NULL,
    body TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('draft', 'published')),
    published_at INTEGER,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_cms_posts_published ON cms_posts(status, published_at DESC, id DESC);
