package test

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"minimal-web-kit/internal/store"
)

// checkDatabase は、DBのファイルの構造と外部キーが壊れていないことを確かめます。
func checkDatabase(db *sql.DB) (string, error) {
	var result string
	if err := db.QueryRow("PRAGMA integrity_check(1)").Scan(&result); err != nil {
		return "", err
	}
	if result != "ok" {
		return result, nil
	}
	var broken int
	if err := db.QueryRow("SELECT COUNT(*) FROM pragma_foreign_key_check").Scan(&broken); err != nil {
		return "", err
	}
	if broken > 0 {
		return "foreign key violations", nil
	}
	return "ok", nil
}

// strangeValues は、SQLiteの層へ届く値として試す文字列です。
var strangeValues = []string{
	"\x00", "a\x00b", "\xff\xfe", "'; DROP TABLE documents;--", `" OR 1=1 --`, `%_\`,
	"\U0001F600", strings.Repeat("あ", 500), "‮", "\r\n",
}

func (ui *adminUI) getQuery(path, name, value string) *httptest.ResponseRecorder {
	return ui.get("admin", path+"?"+url.Values{name: {value}}.Encode())
}

// TestBoundary_LongInputsAreRefused は、SQLへ届く代表の入力に長さの上限があることを確かめる。
func TestBoundary_LongInputsAreRefused(t *testing.T) {
	ui := setupAdminUI(t, true)
	long := strings.Repeat("長", 60000)
	if w := ui.getQuery("/admin/documents", "q", strings.Repeat("長", 101)); w.Code != http.StatusBadRequest {
		t.Errorf("document search: %d", w.Code)
	}
	if w := ui.getQuery("/catalog", "q", strings.Repeat("長", 201)); w.Code < 400 || w.Code >= 500 {
		t.Errorf("catalog search: %d", w.Code)
	}
	for _, c := range []struct {
		path   string
		values url.Values
	}{
		{"/admin/documents", url.Values{"title": {long}, "body": {"本文"}}},
		{"/admin/documents", url.Values{"title": {"題"}, "body": {long}}},
		{"/admin/document-folders", url.Values{"name": {long}}},
		{"/admin/news", url.Values{"title": {long}, "body": {"本文"}}},
		{"/admin/news", url.Values{"title": {"題"}, "body": {long}}},
		{"/admin/pages", url.Values{"slug": {"long-page"}, "title": {"題"}, "body": {long}}},
		{"/admin/catalog", url.Values{"title": {"題"}, "body": {long}}},
	} {
		if w := ui.post("admin", c.path, c.values); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", c.path, w.Code)
		}
	}
	// リクエスト全体の上限を超えた送信は、保存まで届かない
	huge := strings.Repeat("x", 2<<20)
	if w := ui.post("admin", "/admin/documents", url.Values{"title": {"題"}, "body": {huge}}); w.Code < 400 || w.Code >= 500 {
		t.Errorf("oversized request: %d", w.Code)
	}
}

// TestBoundary_StrangeValuesAreHandled は、変な文字列を検索と保存へ流しても、500にならずDBも壊れないことを確かめる。
func TestBoundary_StrangeValuesAreHandled(t *testing.T) {
	ui := setupAdminUI(t, true)
	for _, value := range strangeValues {
		for _, c := range []struct{ path, name string }{{"/admin/documents", "q"}, {"/catalog", "q"}, {"/news", "page"}} {
			if w := ui.getQuery(c.path, c.name, value); w.Code >= 500 {
				t.Errorf("GET %s %q: %d", c.path, value, w.Code)
			}
		}
		for _, c := range []struct {
			path   string
			values url.Values
		}{
			{"/admin/documents", url.Values{"title": {value}, "body": {value}}},
			{"/admin/document-folders", url.Values{"name": {value}}},
			{"/admin/news", url.Values{"title": {value}, "body": {value}}},
			{"/admin/catalog", url.Values{"title": {value}, "body": {value}}},
		} {
			if w := ui.post("admin", c.path, c.values); w.Code >= 500 {
				t.Errorf("POST %s %q: %d", c.path, value, w.Code)
			}
		}
	}
	if result, err := checkDatabase(ui.db); err != nil || result != "ok" {
		t.Fatalf("database after strange values: %q %v", result, err)
	}
}

// TestDatabase_IntegrityAfterOperations は、全機能の見本の操作の後に、DBの構造が壊れていないことを確かめる。
// 依存を更新したときに、変換されたSQLiteの層の不具合でファイルが壊れないかを確かめる目的です。
func TestDatabase_IntegrityAfterOperations(t *testing.T) {
	ui := setupAdminUI(t, true)
	for _, path := range []string{"/admin/news/" + ui.ids["post_draft"] + "/delete", "/admin/documents/" + ui.ids["doc_published"] + "/delete"} {
		if w := ui.post("admin", path, nil); w.Code != http.StatusSeeOther {
			t.Fatalf("%s: %d", path, w.Code)
		}
	}
	if _, err := ui.db.Exec("VACUUM"); err != nil {
		t.Fatal(err)
	}
	if result, err := checkDatabase(ui.db); err != nil || result != "ok" {
		t.Fatalf("database after operations: %q %v", result, err)
	}
}

// TestDatabase_CheckFindsDamage は、検査が壊れたファイルを見つけることを確かめる。
func TestDatabase_CheckFindsDamage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "damaged.db")
	s, err := store.NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec("PRAGMA wal_checkpoint(TRUNCATE); CREATE TABLE sample (v TEXT); CREATE INDEX idx_sample ON sample(v)"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 500; i++ {
		if _, err := s.DB().Exec("INSERT INTO sample VALUES (?)", strings.Repeat("v", 200)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.DB().Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := 4096 * 3; i < len(raw)-4096; i += 4096 * 2 {
		copy(raw[i:i+64], strings.Repeat("\xff", 64))
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	damaged, err := store.NewSQLiteStore(path)
	if err != nil {
		return // 開けないのも、壊れたことの検出
	}
	defer damaged.Close()
	if result, err := checkDatabase(damaged.DB()); err == nil && result == "ok" {
		t.Fatal("damage must be found")
	}
}
