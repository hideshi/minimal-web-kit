-- migration.sql: 共通認証の利用側のスキーマ定義
-- 本人確認は認証基盤（issuer）が行う。ここには氏名・メールアドレス・パスキーを保存しない。

-- 認証基盤の識別子と、このシステムのIDとロールの対応。IDは再利用しない。
-- 初回ログインではメンバーとして作り、管理者はコマンドで指定する。
CREATE TABLE IF NOT EXISTS federated_accounts (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    issuer TEXT NOT NULL,
    sub TEXT NOT NULL,
    role TEXT NOT NULL DEFAULT 'member' CHECK (role IN ('admin', 'member')),
    disabled_at INTEGER,
    created_at INTEGER NOT NULL,
    UNIQUE (issuer, sub)
);

-- ログイン状態。CookieのトークンはSHA-256ハッシュだけを保存する。世代はログイン時の値に固定し、更新しない。
CREATE TABLE IF NOT EXISTS federated_sessions (
    token_hash TEXT NOT NULL PRIMARY KEY,
    account_id INTEGER NOT NULL REFERENCES federated_accounts(id) ON DELETE CASCADE,
    account_epoch TEXT NOT NULL,
    client_epoch TEXT NOT NULL,
    grant_epoch TEXT NOT NULL,
    auth_time INTEGER NOT NULL,
    created_at INTEGER NOT NULL,
    last_seen_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_federated_sessions_account ON federated_sessions(account_id);
