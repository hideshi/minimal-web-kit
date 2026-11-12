package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// runMain は、試験の実行ファイルを、main を呼ぶ子のプロセスとして起動し直します。args は main に渡す引数です。
func runMain(t *testing.T, args []string, env ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestMain_NotifyHelper$")
	var base []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "SITE_NAME=") && !strings.HasPrefix(kv, "NOTIFY_") {
			base = append(base, kv)
		}
	}
	cmd.Env = append(base, append([]string{
		"MWK_TEST_NOTIFY_MAIN=" + strings.Join(args, "\x1f"),
		"DB_PATH=" + filepath.Join(t.TempDir(), "app.db"),
		"PORT=0",
	}, env...)...)
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("the process kept running:\n%s", out)
	}
	return string(out), err
}

func TestMain_NotifyHelper(t *testing.T) {
	raw, ok := os.LookupEnv("MWK_TEST_NOTIFY_MAIN")
	if !ok {
		t.Skip("helper for the notify tests")
	}
	os.Args = []string{"minimal-web-kit"}
	if raw != "" {
		os.Args = append(os.Args, strings.Split(raw, "\x1f")...)
	}
	main()
}

func urlFile(t *testing.T, content string, perm os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "PATHCANARY")
	if err := os.WriteFile(path, []byte(content), perm); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, perm); err != nil { // umask に左右されないように
		t.Fatal(err)
	}
	return path
}

// 通知の設定が誤っていれば、サーバーは待ち受けを始めずに失敗し、ログに URL もパスも出さない。
// サブコマンドでは、通知の設定を検査しない。
// 不変条件: NOTIFY-INV-2, NOTIFY-INV-3, NOTIFY-INV-5, NOTIFY-INV-9
func TestMain_NotifyConfig(t *testing.T) {
	bad := urlFile(t, "https://evil.example/services/SECRET/B/C\n", 0o600)
	good := urlFile(t, "https://hooks.slack.com/services/T0/B0/SECRET\n", 0o600)
	cases := []struct {
		name string
		args []string
		env  []string
		fail bool
	}{
		{"unknown host", nil, []string{"NOTIFY_WEBHOOK_URL_FILE=" + bad, "SITE_NAME=サイト"}, true},
		{"no site name", nil, []string{"NOTIFY_WEBHOOK_URL_FILE=" + good}, true},
		{"loose permission", nil, []string{"NOTIFY_WEBHOOK_URL_FILE=" + urlFile(t, "https://hooks.slack.com/services/T0/B0/SECRET", 0o644), "SITE_NAME=サイト"}, true},
		{"subcommand", []string{"no-such-command"}, []string{"NOTIFY_WEBHOOK_URL_FILE=" + bad}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, err := runMain(t, c.args, c.env...)
			if strings.Contains(out, "SECRET") || strings.Contains(out, "evil") || strings.Contains(out, "PATHCANARY") {
				t.Errorf("the log leaks the URL or the path:\n%s", out)
			}
			failed := strings.Contains(out, "notify setup failed")
			if failed != c.fail {
				t.Errorf("notify setup failed = %v, want %v:\n%s", failed, c.fail, out)
			}
			if c.fail {
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != 1 || strings.Contains(out, "server starting") {
					t.Errorf("want exit 1 before listening, got %v:\n%s", err, out)
				}
			}
		})
	}
}
