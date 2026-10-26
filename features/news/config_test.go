package news

import (
	"log/slog"
	"slices"
	"strings"
	"testing"
)

func env(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

// 不変条件: NEWS-INV-8
func TestLoadConfig_CORS(t *testing.T) {
	cfg, err := LoadConfig(env(nil))
	if err != nil || len(cfg.CORSOrigins) != 0 {
		t.Fatalf("CORS must be disabled by default: %+v %v", cfg, err)
	}
	cfg, err = LoadConfig(env(map[string]string{
		"NEWS_CORS_ORIGINS": " https://www.example.com , http://localhost:3000,, https://app.example.net:8443",
	}))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"https://www.example.com", "http://localhost:3000", "https://app.example.net:8443"}
	if !slices.Equal(cfg.CORSOrigins, want) {
		t.Fatalf("CORS origins = %v, want %v", cfg.CORSOrigins, want)
	}
	for name, value := range map[string]string{
		"wildcard":        "*",
		"null":            "null",
		"with path":       "https://example.com/app",
		"http non-local":  "http://example.com",
		"non-http scheme": "ftp://example.com",
	} {
		_, err := LoadConfig(env(map[string]string{"NEWS_CORS_ORIGINS": value}))
		if err == nil {
			t.Errorf("%s: expected error", name)
		} else if strings.Contains(err.Error(), value) && value != "null" && value != "*" {
			t.Errorf("%s: error must not contain the configured value: %v", name, err)
		}
	}

	// 改名前の設定名が残っていれば、黙ってCORSを外さずに起動を止める
	if _, err := LoadConfig(env(map[string]string{"CMS_CORS_ORIGINS": "https://allowed.example"})); err == nil || !strings.Contains(err.Error(), "NEWS_CORS_ORIGINS") || strings.Contains(err.Error(), "allowed.example") {
		t.Fatalf("the legacy setting must be refused with guidance: %v", err)
	}
}

// 設定エラーは、設定値を含まない理由とともにログに記録し、起動を止めること。
// 不変条件: NEWS-INV-11
func TestInit_LogsConfigReasonWithoutValues(t *testing.T) {
	t.Setenv("NEWS_CORS_ORIGINS", "http://secret-host.example")
	var buf strings.Builder
	f := NewFeature(slog.New(slog.NewTextHandler(&buf, nil)))
	_, db := newTestDB(t)
	if err := f.Init(db); err == nil {
		t.Fatal("expected config error")
	}
	if !strings.Contains(buf.String(), "news config invalid") || !strings.Contains(buf.String(), "NEWS_CORS_ORIGINS") {
		t.Fatalf("missing reason in log: %s", buf.String())
	}
	if strings.Contains(buf.String(), "secret-host") {
		t.Fatal("configured value leaked into log")
	}
}
