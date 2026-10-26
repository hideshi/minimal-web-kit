-- カタログはサイト全体の共有内容。Coreの匿名所有者や認証アカウントには紐づけない。
CREATE TABLE IF NOT EXISTS catalog_categories (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 name TEXT NOT NULL UNIQUE
);
CREATE TABLE IF NOT EXISTS catalog_tags (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 name TEXT NOT NULL UNIQUE
);
CREATE TABLE IF NOT EXISTS catalog_items (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 title TEXT NOT NULL,
 body TEXT NOT NULL,
 status TEXT NOT NULL CHECK(status IN ('draft', 'published')),
 category_id INTEGER REFERENCES catalog_categories(id) ON DELETE SET NULL,
 created_at INTEGER NOT NULL,
 updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS catalog_item_tags (
 item_id INTEGER NOT NULL REFERENCES catalog_items(id) ON DELETE CASCADE,
 tag_id INTEGER NOT NULL REFERENCES catalog_tags(id) ON DELETE CASCADE,
 PRIMARY KEY(item_id, tag_id)
);
CREATE INDEX IF NOT EXISTS idx_catalog_public ON catalog_items(status, id DESC);
CREATE INDEX IF NOT EXISTS idx_catalog_category ON catalog_items(category_id);
CREATE INDEX IF NOT EXISTS idx_catalog_tag_items ON catalog_item_tags(tag_id, item_id);
