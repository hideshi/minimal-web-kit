-- MCP機能（features/mcp）のテーブル。保存項目は docs/data-inventory.md の10節。
-- 行が無い機能は無効。世代は変更のたびに進め、確認の途中で設定が変わったら実行しない。
CREATE TABLE IF NOT EXISTS mcp_feature_settings (
    feature TEXT PRIMARY KEY,
    enabled INTEGER NOT NULL DEFAULT 0 CHECK (enabled IN (0, 1)),
    generation INTEGER NOT NULL DEFAULT 1 CHECK (typeof(generation) = 'integer' AND generation > 0),
    updated_at INTEGER NOT NULL
);
