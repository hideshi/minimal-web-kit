-- お問い合わせ機能（features/contact）のテーブル。保存項目は docs/data-inventory.md の7節。
-- IPアドレス・User-Agent・Cookieは保存しない。owners には紐づけない（保持期間まで残す）。
CREATE TABLE IF NOT EXISTS contact_messages (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL DEFAULT '',
    reply_to TEXT NOT NULL DEFAULT '',
    body TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'unread' CHECK (status IN ('unread', 'read', 'spam')),
    created_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_contact_messages_created ON contact_messages(created_at);
