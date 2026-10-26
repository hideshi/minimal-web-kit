-- migration.sql: 管理者認証（アカウント・パスキー）のスキーマ定義
-- アカウント・パスキー・管理者セッションは匿名の所有者（owners）に紐づけない。CoreのセッションGCやセッション破棄では削除されない。

-- 管理画面にログインできるアカウント。呼び名は運用上のラベルで、実名・メールアドレスを入れない運用とする。
CREATE TABLE IF NOT EXISTS auth_accounts (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL UNIQUE,
    role TEXT NOT NULL CHECK (role IN ('admin', 'member')),
    webauthn_user_id BLOB NOT NULL UNIQUE,
    created_at INTEGER NOT NULL,
    disabled_at INTEGER,
    last_login_at INTEGER
);

-- 登録済みパスキー（端末固定の認証器のみ）。アカウントを削除すると一緒に削除する。
CREATE TABLE IF NOT EXISTS auth_credentials (
    id BLOB NOT NULL PRIMARY KEY,
    account_id INTEGER NOT NULL REFERENCES auth_accounts(id) ON DELETE CASCADE,
    public_key BLOB NOT NULL,
    alg INTEGER NOT NULL,
    sign_count INTEGER NOT NULL DEFAULT 0,
    label TEXT NOT NULL,
    disabled_at INTEGER,
    created_at INTEGER NOT NULL,
    last_used_at INTEGER
);

CREATE INDEX IF NOT EXISTS idx_auth_credentials_account ON auth_credentials(account_id);

-- 管理者セッション。DBにはトークンのSHA-256ハッシュのみ保存する。
CREATE TABLE IF NOT EXISTS auth_sessions (
    token_hash TEXT NOT NULL PRIMARY KEY,
    account_id INTEGER NOT NULL REFERENCES auth_accounts(id) ON DELETE CASCADE,
    created_at INTEGER NOT NULL,
    last_seen_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_auth_sessions_account ON auth_sessions(account_id);

-- パスキーの登録用の1回限りのトークン。特定のアカウントに結びつく。DBにはSHA-256ハッシュのみ保存する。
CREATE TABLE IF NOT EXISTS auth_setup_tokens (
    token_hash TEXT NOT NULL PRIMARY KEY,
    account_id INTEGER NOT NULL REFERENCES auth_accounts(id) ON DELETE CASCADE,
    expires_at INTEGER NOT NULL,
    used_at INTEGER
);

CREATE INDEX IF NOT EXISTS idx_auth_setup_tokens_account ON auth_setup_tokens(account_id);
