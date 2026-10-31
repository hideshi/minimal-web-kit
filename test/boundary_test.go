package test

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"minimal-web-kit/features"
	"minimal-web-kit/features/documents"
	"minimal-web-kit/internal/app"
	"minimal-web-kit/internal/store"
	"minimal-web-kit/internal/web"
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

// strangeValues は、SQLiteの層へ届く値の見本と、経路ごとの期待です。
var strangeValues = []struct {
	value   string
	stored  string // 文書の本文として保存される値。空なら拒否される
	catalog int    // /catalog?q= の応答
	search  int    // /admin/documents?q= の応答
}{
	{"\x00", "", 400, 200},
	{"a\x00b", "", 400, 200},
	{"\xff\xfe", "", 400, 200},
	{"'; DROP TABLE documents;--", "'; DROP TABLE documents;--", 200, 200},
	{`" OR 1=1 --`, `" OR 1=1 --`, 200, 200},
	{`%_\`, `%_\`, 200, 200},
	{"\U0001F600", "\U0001F600", 200, 200},
	{strings.Repeat("あ", 500), strings.Repeat("あ", 500), 400, 400},
	{"\u202e", "\u202e", 200, 200},
	{"改\r\n行", "改\n行", 400, 200},
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

// TestBoundary_StrangeValuesAreHandled は、変な文字列を検索と保存へ流して、経路ごとに期待どおりに断るか保存することを確かめる。
// 保存した値を読み返すので、値がSQLiteまで届いたことも分かる。
func TestBoundary_StrangeValuesAreHandled(t *testing.T) {
	ui := setupAdminUI(t, true)
	docs := documents.NewStore(ui.db, time.Now)
	owner, err := strconv.ParseInt(ui.ids["self"], 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range strangeValues {
		if w := ui.getQuery("/admin/documents", "q", c.value); w.Code != c.search {
			t.Errorf("document search %q: %d", c.value, w.Code)
		}
		if w := ui.getQuery("/catalog", "q", c.value); w.Code != c.catalog {
			t.Errorf("catalog search %q: %d", c.value, w.Code)
		}
		w := ui.post("admin", "/admin/documents", url.Values{"title": {"題"}, "body": {c.value}})
		if c.stored == "" {
			if w.Code != http.StatusBadRequest {
				t.Errorf("document %q: %d", c.value, w.Code)
			}
			continue
		}
		if w.Code != http.StatusSeeOther {
			t.Errorf("document %q: %d", c.value, w.Code)
			continue
		}
		id, err := strconv.ParseInt(strings.Split(w.Header().Get("Location"), "/")[3], 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		_, v, err := docs.Draft(context.Background(), owner, id)
		if err != nil || v.Body != c.stored {
			t.Errorf("stored %q: %q %v", c.value, v.Body, err)
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
		if !strings.Contains(err.Error(), "malformed") {
			t.Fatal(err)
		}
		return // 壊れていて開けないのも、検出の1つ
	}
	defer damaged.Close()
	if result, err := checkDatabase(damaged.DB()); err == nil && result == "ok" {
		t.Fatal("damage must be found")
	}
}

// internalNoticeFeature は、社内向けの説明だけを持つ機能の見本です。
type internalNoticeFeature struct{}

func (internalNoticeFeature) Name() string                                       { return "internal-notice" }
func (internalNoticeFeature) Init(*sql.DB) error                                 { return nil }
func (internalNoticeFeature) RegisterRoutes(*http.ServeMux, *web.SessionManager) {}
func (internalNoticeFeature) PrivacyNotices() []web.PrivacyNotice {
	return []web.PrivacyNotice{{Title: "社内向け", Internal: true}}
}

// 社内向けの説明に受け手が無ければ、どこにも出ないまま起動しない。
// 不変条件: AUTH-INV-28
func TestNotices_InternalNeedAPage(t *testing.T) {
	core, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { core.Close() })
	reg := features.NewRegistry()
	reg.Register(internalNoticeFeature{})
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := app.BuildRoutes(core, reg, false, logger); err == nil || !strings.Contains(err.Error(), "after login") {
		t.Fatalf("err = %v", err)
	}
}
