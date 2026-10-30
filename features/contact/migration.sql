-- お問い合わせ機能（features/contact）のテーブル。保存項目は docs/data-inventory.md の7節。
-- IPアドレス・User-Agent・Cookieは保存しない。owners には紐づけない（削除の時期まで残す）。
-- last_activity_at に既定値を付けないのは、古い実行ファイルが行を足して削除の時期が狂うのを防ぐため。
-- 旧形式の表からの移行は migrate.go が行う。
CREATE TABLE IF NOT EXISTS contact_messages (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL DEFAULT '',
    reply_to TEXT NOT NULL DEFAULT '',
    body TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'unread' CHECK (status IN ('unread', 'active', 'done', 'spam')),
    note TEXT NOT NULL DEFAULT '',
    last_activity_at INTEGER NOT NULL,
    created_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_contact_messages_created ON contact_messages(created_at);
