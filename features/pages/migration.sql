-- 固定ページ機能（features/pages）のテーブル。保存項目は docs/data-inventory.md の8節。
-- 公開する文章で、個人情報は入れない運用。作者のアカウントは保存しない。owners には紐づけない。
CREATE TABLE IF NOT EXISTS pages (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    slug TEXT NOT NULL UNIQUE CHECK (length(slug) BETWEEN 2 AND 40),
    title TEXT NOT NULL,
    body TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'published')),
    nav_order INTEGER CHECK (nav_order IS NULL OR nav_order >= 0),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
