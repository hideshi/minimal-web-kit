-- migration.sql: 管理者認証（パスキー）のスキーマ定義
-- パスキー・管理者セッションは匿名の所有者（owners）に紐づけない。CoreのセッションGCやセッション破棄では削除されない。

-- WebAuthnの user.id（ランダム16バイト）。1行のみ。
CREATE TABLE IF NOT EXISTS auth_settings (
    id INTEGER NOT NULL PRIMARY KEY CHECK (id = 1),
    webauthn_user_id BLOB NOT NULL,
    created_at INTEGER NOT NULL
);

-- 登録済みパスキー（端末固定の認証器のみ）。
CREATE TABLE IF NOT EXISTS auth_credentials (
    id BLOB NOT NULL PRIMARY KEY,
    public_key BLOB NOT NULL,
    alg INTEGER NOT NULL,
    sign_count INTEGER NOT NULL DEFAULT 0,
    label TEXT NOT NULL,
    disabled_at INTEGER,
    created_at INTEGER NOT NULL,
    last_used_at INTEGER
);

-- 管理者セッション。DBにはトークンのSHA-256ハッシュのみ保存する。
CREATE TABLE IF NOT EXISTS auth_sessions (
    token_hash TEXT NOT NULL PRIMARY KEY,
    created_at INTEGER NOT NULL,
    last_seen_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL
);

-- 初回登録・復旧用の1回限りのトークン。DBにはSHA-256ハッシュのみ保存する。
CREATE TABLE IF NOT EXISTS auth_setup_tokens (
    token_hash TEXT NOT NULL PRIMARY KEY,
    expires_at INTEGER NOT NULL,
    used_at INTEGER
);
