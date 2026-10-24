package test

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestDataSchema_MatchesDataLedger は DATA.md の定義とマイグレーションSQLのテーブル・列定義が一致することを検証します。
func TestDataSchema_MatchesDataLedger(t *testing.T) {
	dataMd, err := os.ReadFile("../DATA.md")
	if err != nil {
		t.Fatalf("failed to read DATA.md: %v", err)
	}

	sqlBytes, err := os.ReadFile("../internal/store/migrations/001_init.sql")
	if err != nil {
		t.Fatalf("failed to read migration SQL: %v", err)
	}

	expectedSchema := parseDataMd(string(dataMd))
	actualSchema := parseMigrationSQL(string(sqlBytes))

	if len(expectedSchema) == 0 {
		t.Fatal("no tables found in DATA.md")
	}

	for table, expectedCols := range expectedSchema {
		actualCols, ok := actualSchema[table]
		if !ok {
			t.Errorf("Table %s defined in DATA.md does not exist in migration SQL", table)
			continue
		}

		for _, col := range expectedCols {
			if !contains(actualCols, col) {
				t.Errorf("Column %s.%s defined in DATA.md does not exist in migration SQL", table, col)
			}
		}

		for _, col := range actualCols {
			if !contains(expectedCols, col) {
				t.Errorf("Column %s.%s found in migration SQL but not documented in DATA.md. Update DATA.md first!", table, col)
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
			// ヘッダー行や区切り行をスキップ
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

func parseMigrationSQL(content string) map[string][]string {
	schema := make(map[string][]string)
	tableRe := regexp.MustCompile(`(?i)CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?([a-zA-Z0-9_]+)\s*\(([^;]+)\)`)
	matches := tableRe.FindAllStringSubmatch(content, -1)

	for _, m := range matches {
		tableName := m[1]
		body := m[2]
		var cols []string

		for _, colDef := range strings.Split(body, ",") {
			colDef = strings.TrimSpace(colDef)
			if colDef == "" || strings.HasPrefix(strings.ToUpper(colDef), "PRIMARY KEY") ||
				strings.HasPrefix(strings.ToUpper(colDef), "FOREIGN KEY") ||
				strings.HasPrefix(strings.ToUpper(colDef), "UNIQUE") {
				continue
			}
			parts := strings.Fields(colDef)
			if len(parts) >= 2 {
				cols = append(cols, parts[0])
			}
		}
		schema[tableName] = cols
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
