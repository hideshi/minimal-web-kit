-- 画像機能（features/media）のテーブル。保存項目は docs/data-inventory.md の12節。
-- 運営者が公開のために登録する画像で、閲覧者の情報は保存しない。登録した人のIDも保存しない。owners には紐づけない。
CREATE TABLE IF NOT EXISTS media_items (
    id TEXT PRIMARY KEY CHECK (length(id) = 32),
    format TEXT NOT NULL CHECK (format IN ('jpg', 'png')),
    width INTEGER NOT NULL CHECK (width > 0),
    height INTEGER NOT NULL CHECK (height > 0),
    bytes INTEGER NOT NULL CHECK (bytes > 0),
    alt TEXT NOT NULL,
    created_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_media_items_created ON media_items(created_at DESC, id);

-- 本体。一覧や判定で読まないよう、別の表にする
CREATE TABLE IF NOT EXISTS media_blobs (
    id TEXT PRIMARY KEY REFERENCES media_items(id) ON DELETE CASCADE,
    data BLOB NOT NULL
);

-- どの記事がどの画像を使うか。公開中かは保存せず、機能に毎回問い合わせる
CREATE TABLE IF NOT EXISTS media_refs (
    media_id TEXT NOT NULL REFERENCES media_items(id) ON DELETE RESTRICT,
    owner TEXT NOT NULL,
    ref_key TEXT NOT NULL,
    PRIMARY KEY (media_id, owner, ref_key)
);

CREATE INDEX IF NOT EXISTS idx_media_refs_owner ON media_refs(owner, ref_key);
