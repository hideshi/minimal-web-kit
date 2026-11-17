-- migration.sql: お知らせ機能のスキーマ定義
-- 記事は匿名の所有者（owners）に紐づけない。CoreのセッションGCやセッション破棄では削除されない。
-- 管理者のパスキーとセッションは、同梱機能の管理者認証（features/auth）が持つ。

-- お知らせのカテゴリ。カタログの分類とは共有しない。削除するまで保持する。AUTOINCREMENTで削除済みIDを再利用しない
-- （公開の絞り込みのURL /news?category= が、消した後に別のカテゴリを指さないため）。
CREATE TABLE IF NOT EXISTS news_categories (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL UNIQUE,
    created_at INTEGER NOT NULL
);

-- お知らせ。削除するまで保持する。AUTOINCREMENTで削除済みIDを再利用しない（APIの利用側の誤参照を防ぐ）。
CREATE TABLE IF NOT EXISTS news_posts (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    title TEXT NOT NULL,
    body TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('draft', 'published')),
    published_at INTEGER,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    -- 代表画像のファイル名。画像機能が無い構成でも保存できるよう、外部キーは付けない
    cover_file TEXT,
    -- カテゴリ（1記事に1つ。無ければNULL）。カテゴリを消すと NULL に戻る
    category_id INTEGER REFERENCES news_categories(id) ON DELETE SET NULL
);

CREATE INDEX IF NOT EXISTS idx_news_posts_published ON news_posts(status, published_at DESC, id DESC);

-- LINE での知らせの送った記録（記事ごとに1行。docs/domain/news.md の「LINE での知らせの状態」）。
-- 個人の情報は持たない（友だちの情報は LINE が持つ）。記事を消すと一緒に消える。
-- message は公開済みの題名と記事の URL だけで作った送る文面。トークンとチャネルシークレットは保存しない。
CREATE TABLE IF NOT EXISTS news_line_broadcasts (
    post_id INTEGER PRIMARY KEY REFERENCES news_posts(id) ON DELETE CASCADE,
    -- 再送の鍵（UUID）。LINE の側で24時間、同じ鍵の要求は1回に絞られる
    retry_key TEXT NOT NULL,
    message TEXT NOT NULL,
    -- 今の鍵で初めて要求を出そうとした日時。まだならNULL
    key_first_sent_at INTEGER,
    -- 結果をまだ書いていない要求の数。残っていれば、受け付けたかもしれない
    in_flight INTEGER NOT NULL DEFAULT 0 CHECK (in_flight >= 0),
    -- 受け付けたかもしれない要求があったか。1になったら0に戻さない
    uncertain INTEGER NOT NULL DEFAULT 0 CHECK (uncertain IN (0, 1)),
    created_at INTEGER NOT NULL,
    -- 送信済みになった日時。まだならNULL
    sent_at INTEGER
);
