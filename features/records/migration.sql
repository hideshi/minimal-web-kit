-- 業務記録の記録の表。種類ごとの表（records_<種類>）は、定義の手順から作る（docs/domain/records.md の8節）。
-- 種類の記録: 種類ごとの状態。表の名前は records_<種類>。
CREATE TABLE IF NOT EXISTS records_tables (
    table_name TEXT PRIMARY KEY,
    state TEXT NOT NULL CHECK (state IN ('active', 'dropped'))
);

-- 手順の記録: その環境に当てた手順ごとの番号と、手順の文字列（例: add:work_date:date）。
CREATE TABLE IF NOT EXISTS records_steps (
    table_name TEXT NOT NULL,
    version INTEGER NOT NULL CHECK (version >= 1),
    step TEXT NOT NULL,
    PRIMARY KEY (table_name, version)
);
