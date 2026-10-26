package auth

import (
	"log/slog"
	"strings"
	"testing"
)

func env(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func TestLoadConfig_Defaults(t *testing.T) {
	cfg, err := LoadConfig(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RPID != "localhost" || cfg.Origin != "http://localhost:8080" || cfg.SecureCookie {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
}

func TestLoadConfig_ProductionAndNormalization(t *testing.T) {
	cfg, err := LoadConfig(env(map[string]string{
		"AUTH_ORIGIN":   "https://Admin.Example.com:443/",
		"AUTH_RP_ID":    "Example.com",
		"SECURE_COOKIE": "true",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Origin != "https://admin.example.com" || cfg.RPID != "example.com" || !cfg.SecureCookie {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

func TestLoadConfig_Rejects(t *testing.T) {
	cases := map[string]map[string]string{
		"http non-localhost origin":   {"AUTH_ORIGIN": "http://example.com", "AUTH_RP_ID": "example.com"},
		"origin with path":            {"AUTH_ORIGIN": "https://example.com/admin", "AUTH_RP_ID": "example.com", "SECURE_COOKIE": "true"},
		"origin with query":           {"AUTH_ORIGIN": "https://example.com?x=1", "AUTH_RP_ID": "example.com", "SECURE_COOKIE": "true"},
		"origin with userinfo":        {"AUTH_ORIGIN": "https://u@example.com", "AUTH_RP_ID": "example.com", "SECURE_COOKIE": "true"},
		"non-http scheme":             {"AUTH_ORIGIN": "ftp://example.com", "AUTH_RP_ID": "example.com"},
		"https without secure cookie": {"AUTH_ORIGIN": "https://example.com", "AUTH_RP_ID": "example.com"},
		"ip rp id":                    {"AUTH_ORIGIN": "https://example.com", "AUTH_RP_ID": "127.0.0.1", "SECURE_COOKIE": "true"},
		"rp id not parent of origin":  {"AUTH_ORIGIN": "https://example.com", "AUTH_RP_ID": "other.com", "SECURE_COOKIE": "true"},
		"rp id suffix but not domain": {"AUTH_ORIGIN": "https://badexample.com", "AUTH_RP_ID": "example.com", "SECURE_COOKIE": "true"},
		"single label rp id":          {"AUTH_ORIGIN": "https://example.com", "AUTH_RP_ID": "com", "SECURE_COOKIE": "true"},
		"localhost rp with ip origin": {"AUTH_ORIGIN": "http://127.0.0.1:8080"},
	}
	for name, values := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := LoadConfig(env(values))
			if err == nil {
				t.Fatal("expected error")
			}
			for _, v := range values {
				if v != "" && v != "true" && strings.Contains(err.Error(), v) {
					t.Fatalf("error must not contain the configured value %q: %v", v, err)
				}
			}
		})
	}
}

// 設定エラーは、設定値を含まない理由とともにログに記録し、起動を止めること。
func TestInit_LogsConfigReasonWithoutValues(t *testing.T) {
	t.Setenv("AUTH_ORIGIN", "https://secret-host.example")
	t.Setenv("AUTH_RP_ID", "secret-host.example")
	var buf strings.Builder
	f := NewFeature(slog.New(slog.NewTextHandler(&buf, nil)))
	_, db := newTestDB(t)
	if err := f.Init(db); err == nil {
		t.Fatal("expected config error")
	}
	if !strings.Contains(buf.String(), "auth config invalid") || !strings.Contains(buf.String(), "SECURE_COOKIE") {
		t.Fatalf("missing reason in log: %s", buf.String())
	}
	if strings.Contains(buf.String(), "secret-host") {
		t.Fatal("configured value leaked into log")
	}
}
