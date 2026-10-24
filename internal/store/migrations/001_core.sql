-- 001_core.sql: コアスキーマ定義
-- PRAGMA foreign_keys = ON 前提で、owner_id によるカスケード削除を実現します。

CREATE TABLE IF NOT EXISTS owners (
    id TEXT NOT NULL PRIMARY KEY,
    created_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS sessions (
    token_hash TEXT NOT NULL PRIMARY KEY,
    owner_id TEXT NOT NULL REFERENCES owners(id) ON DELETE CASCADE,
    created_at INTEGER NOT NULL,
    last_seen_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_sessions_last_seen ON sessions(last_seen_at);
CREATE INDEX IF NOT EXISTS idx_sessions_owner ON sessions(owner_id);
