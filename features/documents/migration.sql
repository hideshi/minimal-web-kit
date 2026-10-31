-- migration.sql: 社内文書のスキーマ定義
-- アカウントIDは、認証の表と外部キーでつながない。削除済みのIDは「削除済みのアカウント」として扱う。

-- フォルダ。全アカウントで共通の1階層。権限を持たない。文書が入っている間は削除できない。
CREATE TABLE IF NOT EXISTS document_folders (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL UNIQUE,
    created_by INTEGER NOT NULL,
    created_at INTEGER NOT NULL
);

-- 文書の本体。タイトルと本文は版が持つ。current_version_id は共有先に見せる版で、公開の前は NULL。
CREATE TABLE IF NOT EXISTS documents (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    owner_account_id INTEGER NOT NULL,
    folder_id INTEGER REFERENCES document_folders(id) ON DELETE RESTRICT,
    audience TEXT NOT NULL DEFAULT 'private' CHECK (audience IN ('private', 'everyone', 'selected')),
    current_version_id INTEGER,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_documents_owner ON documents(owner_account_id);
CREATE INDEX IF NOT EXISTS idx_documents_folder ON documents(folder_id);

-- 版。draft の行だけ書き換えられる。依頼や公開の後は、タイトルと本文を変えない。
CREATE TABLE IF NOT EXISTS document_versions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    document_id INTEGER NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    number INTEGER NOT NULL,
    title TEXT NOT NULL,
    body TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('draft', 'pending', 'rejected', 'withdrawn', 'published')),
    created_at INTEGER NOT NULL,
    requested_at INTEGER,
    published_at INTEGER,
    UNIQUE (document_id, number)
);

-- 下書きと承認待ちは、合わせて文書ごとに1つまで。承認待ちの間は改定を始められない。
CREATE UNIQUE INDEX IF NOT EXISTS idx_document_versions_open ON document_versions(document_id) WHERE state IN ('draft', 'pending');

CREATE TRIGGER IF NOT EXISTS document_versions_frozen BEFORE UPDATE OF title, body ON document_versions
WHEN OLD.state != 'draft'
BEGIN
    SELECT RAISE(ABORT, 'document version is frozen');
END;

-- 指定の共有先。audience が selected のときだけ使う。
CREATE TABLE IF NOT EXISTS document_shares (
    document_id INTEGER NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    account_id INTEGER NOT NULL,
    PRIMARY KEY (document_id, account_id)
);

-- 版の承認者と判断。
CREATE TABLE IF NOT EXISTS document_approvers (
    version_id INTEGER NOT NULL REFERENCES document_versions(id) ON DELETE CASCADE,
    account_id INTEGER NOT NULL,
    decision TEXT CHECK (decision IN ('approved', 'rejected')),
    reason TEXT NOT NULL DEFAULT '',
    decided_at INTEGER,
    PRIMARY KEY (version_id, account_id)
);

CREATE INDEX IF NOT EXISTS idx_document_approvers_account ON document_approvers(account_id);

-- 版ごとの既読。最初に開いた日時だけを残す。
CREATE TABLE IF NOT EXISTS document_reads (
    version_id INTEGER NOT NULL REFERENCES document_versions(id) ON DELETE CASCADE,
    account_id INTEGER NOT NULL,
    first_read_at INTEGER NOT NULL,
    PRIMARY KEY (version_id, account_id)
);

-- 所有者の移管の記録。文書の削除でも消えず、3年で削除する。
CREATE TABLE IF NOT EXISTS document_transfers (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    from_account_id INTEGER NOT NULL,
    to_account_id INTEGER NOT NULL,
    by_account_id INTEGER NOT NULL,
    count INTEGER NOT NULL,
    transferred_at INTEGER NOT NULL
);
