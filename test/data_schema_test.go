package test

import (
	"database/sql"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// TestDataSchema_MatchesDataLedger は DATA.md の定義とマイグレーションSQL実行後の実DBスキーマが一致することを検証します。
func TestDataSchema_MatchesDataLedger(t *testing.T) {
	dataMd, err := os.ReadFile("../DATA.md")
	if err != nil {
		t.Fatalf("failed to read DATA.md: %v", err)
	}

	expectedSchema := parseDataMd(string(dataMd))
	if len(expectedSchema) == 0 {
		t.Fatal("no tables found in DATA.md")
	}

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory sqlite: %v", err)
	}
	defer db.Close()

	if _, err := db.Exec("PRAGMA foreign_keys = ON;"); err != nil {
		t.Fatalf("failed to enable foreign keys: %v", err)
	}

	// 1. Coreマイグレーションを実行
	coreSQL, err := os.ReadFile("../internal/store/migrations/001_core.sql")
	if err != nil {
		t.Fatalf("failed to read core migration: %v", err)
	}
	if _, err := db.Exec(string(coreSQL)); err != nil {
		t.Fatalf("failed to execute core migration: %v", err)
	}

	// 2. 有効化されている各Featureのマイグレーションを実行（もし存在すれば）
	featureMigrations, _ := filepath.Glob("../features/*/migration.sql")
	// cmd/server/features.go で登録されているか確認
	featuresGoBytes, _ := os.ReadFile("../cmd/server/features.go")
	featuresGo := string(featuresGoBytes)

	for _, migFile := range featureMigrations {
		featureName := filepath.Base(filepath.Dir(migFile))
		// features.go で有効化されている機能のみマイグレーションを実行
		if strings.Contains(featuresGo, featureName+".") {
			migBytes, err := os.ReadFile(migFile)
			if err != nil {
				t.Fatalf("failed to read feature migration %s: %v", migFile, err)
			}
			if _, err := db.Exec(string(migBytes)); err != nil {
				t.Fatalf("failed to execute feature migration %s: %v", migFile, err)
			}
		}
	}

	// 3. PRAGMA table_info で実際の全テーブルと列を取得
	actualSchema := make(map[string][]string)
	for table := range expectedSchema {
		rows, err := db.Query("PRAGMA table_info(" + table + ")")
		if err != nil {
			t.Fatalf("failed to query table_info for %s: %v", table, err)
		}

		var cols []string
		for rows.Next() {
			var cid int
			var name, colType string
			var notnull, pk int
			var dfltValue sql.NullString
			if err := rows.Scan(&cid, &name, &colType, &notnull, &dfltValue, &pk); err != nil {
				rows.Close()
				t.Fatalf("failed to scan column info: %v", err)
			}
			cols = append(cols, name)
		}
		rows.Close()

		if len(cols) == 0 {
			t.Errorf("Table %s defined in DATA.md does not exist in migration SQL database", table)
			continue
		}
		actualSchema[table] = cols
	}

	// 4. DATA.md と実DBスキーマの双方向突合チェック
	for table, expectedCols := range expectedSchema {
		actualCols := actualSchema[table]

		for _, col := range expectedCols {
			if !contains(actualCols, col) {
				t.Errorf("Column %s.%s defined in DATA.md does not exist in database schema", table, col)
			}
		}

		for _, col := range actualCols {
			if !contains(expectedCols, col) {
				t.Errorf("Column %s.%s found in database schema but not documented in DATA.md. Update DATA.md first!", table, col)
			}
		}
	}
}

func parseDataMd(content string) map[string][]string {
	schema := make(map[string][]string)
	lines := strings.Split(content, "\n")
	currentTable := ""

	tableRe := regexp.MustCompile(`### テーブル:\s*` + "`" + `?([a-zA-Z0-9_]+)` + "`" + `?`)
	colRe := regexp.MustCompile(`^\|\s*` + "`" + `?([a-zA-Z0-9_]+)` + "`" + `?\s*\|`)

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if m := tableRe.FindStringSubmatch(line); len(m) > 1 {
			currentTable = m[1]
			continue
		}

		if currentTable != "" && strings.HasPrefix(line, "|") {
			if strings.Contains(line, "列名") || strings.Contains(line, "---") {
				continue
			}
			if m := colRe.FindStringSubmatch(line); len(m) > 1 {
				col := m[1]
				schema[currentTable] = append(schema[currentTable], col)
			}
		}
	}
	return schema
}

func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}
