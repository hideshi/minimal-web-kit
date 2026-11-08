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

// サイトの名前（SITE_NAME）が不正なら、サーバーは待ち受けを始めずに失敗し、ログに値を出さないこと。
// 試験の実行ファイルを、main を呼ぶ子のプロセスとして起動し直して確かめる。
func TestMain_RejectsInvalidSiteName(t *testing.T) {
	if os.Getenv("MWK_TEST_RUN_MAIN") == "1" {
		os.Args = []string{"minimal-web-kit"}
		main()
		return
	}
	const canary = "カナリアの名前\x07"
	// 検査が無いとサーバーが待ち受けを続けるため、時間で打ち切って失敗にする
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestMain_RejectsInvalidSiteName$")
	cmd.Env = append(os.Environ(),
		"MWK_TEST_RUN_MAIN=1",
		"SITE_NAME="+canary,
		"DB_PATH="+filepath.Join(t.TempDir(), "app.db"),
		"PORT=0",
	)
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("the server kept running with an invalid SITE_NAME:\n%s", out)
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 {
		t.Fatalf("want exit status 1, got %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "site name invalid") {
		t.Errorf("want the startup error in the log:\n%s", out)
	}
	if strings.Contains(string(out), "カナリア") {
		t.Errorf("the log must not contain the value:\n%s", out)
	}
	if strings.Contains(string(out), "server starting") {
		t.Errorf("the server must not start:\n%s", out)
	}
}
