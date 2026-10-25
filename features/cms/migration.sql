-- migration.sql: CMSテンプレートのスキーマ定義
-- 記事・パスキーは匿名の所有者（owners）に紐づけない。CoreのセッションGCやセッション破棄では削除されない。

-- WebAuthnの user.id（ランダム16バイト）。1行のみ。
CREATE TABLE IF NOT EXISTS cms_settings (
    id INTEGER NOT NULL PRIMARY KEY CHECK (id = 1),
    webauthn_user_id BLOB NOT NULL,
    created_at INTEGER NOT NULL
);

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

-- 登録済みパスキー（端末固定の認証器のみ）。
CREATE TABLE IF NOT EXISTS cms_credentials (
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
CREATE TABLE IF NOT EXISTS cms_admin_sessions (
    token_hash TEXT NOT NULL PRIMARY KEY,
    created_at INTEGER NOT NULL,
    last_seen_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL
);

-- 初回登録・復旧用の1回限りのトークン。DBにはSHA-256ハッシュのみ保存する。
CREATE TABLE IF NOT EXISTS cms_setup_tokens (
    token_hash TEXT NOT NULL PRIMARY KEY,
    expires_at INTEGER NOT NULL,
    used_at INTEGER
);
