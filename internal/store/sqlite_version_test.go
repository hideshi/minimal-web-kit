package store

import (
	"path/filepath"
	"testing"
)

// TestSQLiteVersion は、組み込まれているSQLite本体の版を表示します（-v で見る）。
// 版は modernc.org/sqlite に変換されて入っていて、govulncheck では見えないので、脆弱性の点検で使います。
func TestSQLiteVersion(t *testing.T) {
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "version.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var version string
	if err := s.DB().QueryRow("SELECT sqlite_version()").Scan(&version); err != nil || version == "" {
		t.Fatalf("sqlite version: %q %v", version, err)
	}
	t.Log("SQLite", version)
}
