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

-- 共通認証（提供側）の表。提供機能を登録していない通常の構成でも、下のトリガーで失効を保ちます。
-- 他のシステムに渡す仮名の識別子と、アカウント全体の失効世代。削除後は墓標を残し、GCで消す。
CREATE TABLE IF NOT EXISTS auth_federated_subjects (
    sub TEXT PRIMARY KEY,
    account_id INTEGER UNIQUE REFERENCES auth_accounts(id) ON DELETE SET NULL,
    account_epoch INTEGER NOT NULL DEFAULT 1 CHECK (typeof(account_epoch) = 'integer' AND account_epoch > 0),
    deleted_at INTEGER
);

-- 連携先の登録。戻り先は1件で、完全一致で比べる。
CREATE TABLE IF NOT EXISTS auth_oidc_clients (
    client_id TEXT PRIMARY KEY,
    label TEXT NOT NULL,
    redirect_uri TEXT NOT NULL,
    enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    client_epoch INTEGER NOT NULL DEFAULT 1 CHECK (typeof(client_epoch) = 'integer' AND client_epoch > 0)
);

-- クライアント秘密値のハッシュ。更新の猶予のため、新旧2件まで持つ。
CREATE TABLE IF NOT EXISTS auth_oidc_client_secrets (
    client_id TEXT NOT NULL REFERENCES auth_oidc_clients(client_id) ON DELETE CASCADE,
    secret_hash TEXT NOT NULL,
    expires_at INTEGER NOT NULL,
    PRIMARY KEY (client_id, secret_hash)
);

-- 利用許可。取り消しても世代のために行を残す。
CREATE TABLE IF NOT EXISTS auth_oidc_grants (
    client_id TEXT NOT NULL REFERENCES auth_oidc_clients(client_id) ON DELETE CASCADE,
    sub TEXT NOT NULL REFERENCES auth_federated_subjects(sub) ON DELETE CASCADE,
    allowed INTEGER NOT NULL DEFAULT 1 CHECK (allowed IN (0, 1)),
    grant_epoch INTEGER NOT NULL DEFAULT 1 CHECK (typeof(grant_epoch) = 'integer' AND grant_epoch > 0),
    PRIMARY KEY (client_id, sub)
);

-- 認可要求とコード。1行にまとめ、コードの消費と認証時の世代が切り離されないようにする。
-- 消費済みの行は、再利用の検出のために12時間2分残す。
CREATE TABLE IF NOT EXISTS auth_oidc_requests (
    request_id TEXT PRIMARY KEY,
    flow_hash TEXT NOT NULL,
    client_id TEXT NOT NULL REFERENCES auth_oidc_clients(client_id) ON DELETE CASCADE,
    redirect_uri TEXT NOT NULL,
    state TEXT NOT NULL,
    nonce TEXT NOT NULL,
    challenge TEXT NOT NULL,
    sub TEXT NOT NULL DEFAULT '',
    account_epoch TEXT NOT NULL DEFAULT '',
    client_epoch TEXT NOT NULL DEFAULT '',
    grant_epoch TEXT NOT NULL DEFAULT '',
    auth_time INTEGER NOT NULL DEFAULT 0,
    expires_at INTEGER NOT NULL,
    code_hash TEXT UNIQUE,
    code_expires_at INTEGER NOT NULL DEFAULT 0,
    consumed_at INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_auth_oidc_requests_flow ON auth_oidc_requests(flow_hash);
CREATE INDEX IF NOT EXISTS idx_auth_oidc_requests_sub ON auth_oidc_requests(sub);

-- 停止・鍵のリセット・削除・複製の疑いは、どの経路から行われても、同じトランザクションで全連携先のログインを失効させる（AUTH-INV-17）。
CREATE TRIGGER IF NOT EXISTS auth_federated_stop AFTER UPDATE OF disabled_at ON auth_accounts
WHEN OLD.disabled_at IS NULL AND NEW.disabled_at IS NOT NULL
BEGIN
    UPDATE auth_federated_subjects SET account_epoch = account_epoch + 1 WHERE account_id = NEW.id;
    DELETE FROM auth_oidc_requests WHERE sub IN (SELECT sub FROM auth_federated_subjects WHERE account_id = NEW.id);
END;

CREATE TRIGGER IF NOT EXISTS auth_federated_reset AFTER UPDATE OF webauthn_user_id ON auth_accounts
WHEN OLD.webauthn_user_id != NEW.webauthn_user_id
BEGIN
    UPDATE auth_federated_subjects SET account_epoch = account_epoch + 1 WHERE account_id = NEW.id;
    DELETE FROM auth_oidc_requests WHERE sub IN (SELECT sub FROM auth_federated_subjects WHERE account_id = NEW.id);
END;

CREATE TRIGGER IF NOT EXISTS auth_federated_delete BEFORE DELETE ON auth_accounts
BEGIN
    UPDATE auth_federated_subjects SET account_epoch = account_epoch + 1, deleted_at = unixepoch() WHERE account_id = OLD.id;
    DELETE FROM auth_oidc_requests WHERE sub IN (SELECT sub FROM auth_federated_subjects WHERE account_id = OLD.id);
END;

CREATE TRIGGER IF NOT EXISTS auth_federated_anomaly AFTER UPDATE OF disabled_at ON auth_credentials
WHEN OLD.disabled_at IS NULL AND NEW.disabled_at IS NOT NULL
BEGIN
    UPDATE auth_federated_subjects SET account_epoch = account_epoch + 1 WHERE account_id = NEW.account_id;
    DELETE FROM auth_oidc_requests WHERE sub IN (SELECT sub FROM auth_federated_subjects WHERE account_id = NEW.account_id);
END;
