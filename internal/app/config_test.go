package app

import (
	"net"
	"testing"
	"time"
)

// 既定の待受は外から届かないアドレスで、広げるには ADDR を明示するしかないことを確かめる。
func TestLoadConfig_Listen(t *testing.T) {
	cases := []struct {
		name, addr, port, want string
	}{
		{"既定", "", "", "127.0.0.1:8080"},
		{"PORTだけ", "", "8081", "127.0.0.1:8081"},
		{"ADDRを明示", "0.0.0.0:9000", "", "0.0.0.0:9000"},
		{"ADDRがPORTより優先", "127.0.0.1:9000", "8081", "127.0.0.1:9000"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("ADDR", c.addr)
			t.Setenv("PORT", c.port)
			if got := LoadConfig().Addr; got != c.want {
				t.Fatalf("Addr = %q, want %q", got, c.want)
			}
		})
	}

	// ADDR を設定しない限り、PORT に何を入れてもループバックのまま
	t.Setenv("ADDR", "")
	t.Setenv("PORT", "8081")
	host, _, err := net.SplitHostPort(LoadConfig().Addr)
	if err != nil {
		t.Fatal(err)
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		t.Fatalf("host = %q, want loopback", host)
	}
}

func TestLoadConfig_SecureCookieOnlyWithTrue(t *testing.T) {
	for value, want := range map[string]bool{"": false, "false": false, "1": false, "TRUE": false, "yes": false, "true": true} {
		t.Run("value="+value, func(t *testing.T) {
			t.Setenv("SECURE_COOKIE", value)
			if got := LoadConfig().SecureCookie; got != want {
				t.Fatalf("SecureCookie = %v, want %v", got, want)
			}
		})
	}
}

func TestLoadConfig_Defaults(t *testing.T) {
	t.Setenv("DB_PATH", "")
	cfg := LoadConfig()
	if cfg.DBPath != "data/app.db" {
		t.Errorf("DBPath = %q", cfg.DBPath)
	}
	// 30日は /privacy と docs/data-inventory.md に書いた保持期間
	if cfg.GCSessionAge != 30*24*time.Hour {
		t.Errorf("GCSessionAge = %v", cfg.GCSessionAge)
	}
	if cfg.GCInterval != time.Hour {
		t.Errorf("GCInterval = %v", cfg.GCInterval)
	}
	if cfg.ReadTimeout <= 0 || cfg.WriteTimeout <= 0 || cfg.IdleTimeout <= 0 {
		t.Errorf("timeouts must be set: %+v", cfg)
	}

	t.Setenv("DB_PATH", "/tmp/other.db")
	if got := LoadConfig().DBPath; got != "/tmp/other.db" {
		t.Errorf("DBPath = %q", got)
	}
}
