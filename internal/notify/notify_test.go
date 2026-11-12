package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const (
	slackURL   = "https://hooks.slack.com/services/T0000/B0000/abcDEF123"
	discordURL = "https://discord.com/api/webhooks/123456/abc_DEF-123"
)

// drain は、待ち行列にある文を全部取り出します。
func drain(n *Notifier) []string {
	var out []string
	for {
		select {
		case m := <-n.queue:
			out = append(out, m)
		default:
			return out
		}
	}
}

func testNotifier(t *testing.T) (*Notifier, *slog.Logger) {
	t.Helper()
	n := newNotifier("テスト", slackURL, slack, nil)
	n.digestFrom = time.Date(2026, 11, 13, 10, 0, 0, 0, time.Local)
	n.started = true
	return n, slog.New(n.Handler(slog.NewTextHandler(io.Discard, nil)))
}

// 不変条件: NOTIFY-INV-2, NOTIFY-INV-3
func TestParseURL(t *testing.T) {
	ok := map[string]target{
		slackURL:               slack,
		discordURL:             discord,
		"  " + slackURL + "\n": slack,
	}
	for raw, want := range ok {
		got, tg, err := parseURL([]byte(raw))
		if err != nil || tg != want || got != strings.TrimSpace(raw) {
			t.Errorf("%q: got %q %v %v", raw, got, tg, err)
		}
	}
	bad := []string{
		"",
		"http://hooks.slack.com/services/T0000/B0000/abc",
		"https://hooks.slack.com.example/services/T0000/B0000/abc",
		"https://HOOKS.slack.com/services/T0000/B0000/abc",
		"https://hooks.slack.com:443/services/T0000/B0000/abc",
		"https://x@hooks.slack.com/services/T0000/B0000/abc",
		"https://hooks.slack.com/services/../x/B0000/abc",
		"https://hooks.slack.com/services/%2e%2e/B0000/abc",
		"https://hooks.slack.com/services/T0000/B0000/abc?x=1",
		"https://hooks.slack.com/services/T0000/B0000/abc?",
		"https://hooks.slack.com/services/T0000/B0000/abc#x",
		"https://hooks.slack.com/services/T0000/B0000/abc/",
		"https://hooks.slack.com/services/T0000/B0000",
		slackURL + "\n" + slackURL,
		slackURL + "\rx",
		"https://discordapp.com/api/webhooks/123/abc",
		"https://discord.com/api/webhooks/abc/abc",
		"https://discord.com/api/v10/webhooks/123/abc",
		"https://evil.example/services/T0000/B0000/abc",
	}
	for _, raw := range bad {
		_, _, err := parseURL([]byte(raw))
		if err == nil {
			t.Errorf("%q: accepted", raw)
			continue
		}
		if strings.Contains(err.Error(), "T0000") || strings.Contains(err.Error(), "evil") {
			t.Errorf("%q: error leaks the URL: %v", raw, err)
		}
	}
}

// 不変条件: NOTIFY-INV-3, NOTIFY-INV-5, NOTIFY-INV-9
func TestNew_Config(t *testing.T) {
	if n, err := New("", "site"); n != nil || err != nil {
		t.Fatalf("unset: %v %v", n, err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "url")
	if err := os.WriteFile(path, []byte(slackURL+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(path, ""); err == nil {
		t.Error("SITE_NAME is required")
	}
	n, err := New(path, "site")
	if err != nil || n == nil || n.target != slack {
		t.Fatalf("valid: %v", err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = New(path, "site")
	if err == nil {
		t.Fatal("a file readable by others was accepted")
	}
	if strings.Contains(err.Error(), dir) || strings.Contains(err.Error(), "T0000") {
		t.Errorf("error leaks the path or URL: %v", err)
	}
}

// 不変条件: NOTIFY-INV-7
func TestPayload(t *testing.T) {
	text := "[<!channel> & @everyone] a<b>"
	b, err := payload(slack, text)
	if err != nil {
		t.Fatal(err)
	}
	var s map[string]string
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	if s["text"] != "[&lt;!channel&gt; &amp; @everyone] a&lt;b&gt;" || len(s) != 1 {
		t.Errorf("slack: %q", s)
	}
	b, err = payload(discord, text)
	if err != nil {
		t.Fatal(err)
	}
	var d struct {
		Content         string               `json:"content"`
		AllowedMentions *map[string][]string `json:"allowed_mentions"`
	}
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatal(err)
	}
	if d.Content != text || d.AllowedMentions == nil {
		t.Fatalf("discord: %s", b)
	}
	if p, ok := (*d.AllowedMentions)["parse"]; !ok || p == nil || len(p) != 0 {
		t.Errorf("discord mentions must be disabled: %s", b)
	}
}

// 出来事が、サイト名と件数だけの文になって待ち行列に入る。
// ユースケース: UC-OPS-4
// 不変条件: NOTIFY-INV-1
func TestTick_TextHasNoLogValues(t *testing.T) {
	n, logger := testNotifier(t)
	canary := "CANARY-本文-reply@example.com"
	logger.Info("contact submission", slog.String("result", "saved"), slog.String("body", canary))
	logger.Info("auth passkey registered", slog.String("label", canary), slog.Int64("account_id", 987654))
	logger.Warn("auth login rejected", slog.String("reason", canary))
	logger.Info("contact submission", slog.String("result", "honeypot"))
	n.tick(n.digestFrom.Add(time.Hour), false)
	msgs := drain(n)
	all := strings.Join(msgs, "\n")
	if strings.Contains(all, "CANARY") || strings.Contains(all, "987654") || strings.Contains(all, "example.com") {
		t.Errorf("log values leaked: %s", all)
	}
	want := []string{
		"[テスト] パスキーが登録されました（1件）。",
		"[テスト] お問い合わせの新着 1件（10:00〜11:00）。",
	}
	for _, w := range want {
		if !strings.Contains(all, w) {
			t.Errorf("missing %q in %s", w, all)
		}
	}
}

// 先に束ねた属性や、グループの中の属性は、判定に使わない。
func TestHandler_OnlyTopLevelAttrsMatch(t *testing.T) {
	n, logger := testNotifier(t)
	logger.With(slog.String("result", "saved")).Info("contact submission")
	logger.WithGroup("g").Info("contact submission", slog.String("result", "saved"))
	logger.Info("contact submission", slog.Int("result", 1))
	if c := n.take(); c[kindContact] != 0 {
		t.Errorf("counted %d", c[kindContact])
	}
	logger.With(slog.String("x", "y")).Info("contact submission", slog.String("result", "saved"))
	if c := n.take(); c[kindContact] != 1 {
		t.Errorf("counted %d, want 1", c[kindContact])
	}
}

// お問い合わせは1時間ごとにまとめ、0件なら送らない。停止のときは送り切る。起動の文は最初の見回りだけ。
func TestTick_DigestAndStartup(t *testing.T) {
	n, logger := testNotifier(t)
	n.started = false
	start := n.digestFrom
	logger.Info("contact submission", slog.String("result", "saved"))
	n.tick(start.Add(time.Minute), false)
	if msgs := drain(n); len(msgs) != 1 || msgs[0] != "[テスト] 起動しました。" {
		t.Fatalf("first tick: %q", msgs)
	}
	logger.Info("contact submission", slog.String("result", "saved"))
	n.tick(start.Add(30*time.Minute), false)
	if msgs := drain(n); len(msgs) != 0 {
		t.Fatalf("sent before an hour: %q", msgs)
	}
	n.tick(start.Add(time.Hour), false)
	if msgs := drain(n); len(msgs) != 1 || !strings.Contains(msgs[0], "新着 2件（10:00〜11:00）") {
		t.Fatalf("digest: %q", msgs)
	}
	n.tick(start.Add(2*time.Hour), false)
	if msgs := drain(n); len(msgs) != 0 {
		t.Fatalf("empty digest sent: %q", msgs)
	}
	logger.Info("contact submission", slog.String("result", "saved"))
	n.tick(start.Add(2*time.Hour+time.Minute), true)
	if msgs := drain(n); len(msgs) != 1 || !strings.Contains(msgs[0], "新着 1件") {
		t.Fatalf("final flush: %q", msgs)
	}
}

// 認証の拒否は10分に10件以上で1通。続く間は10分に1通まで。
func TestTick_RejectThreshold(t *testing.T) {
	n, logger := testNotifier(t)
	at := n.digestFrom
	for i := 0; i < 9; i++ {
		logger.Warn("auth login rejected")
	}
	n.tick(at.Add(time.Minute), false)
	if msgs := drain(n); len(msgs) != 0 {
		t.Fatalf("below the limit: %q", msgs)
	}
	logger.Warn("auth passkey registration rejected")
	n.tick(at.Add(2*time.Minute), false)
	if msgs := drain(n); len(msgs) != 1 || !strings.Contains(msgs[0], "直近10分で10件") {
		t.Fatalf("at the limit: %q", msgs)
	}
	for m := 3; m <= 11; m++ {
		logger.Warn("auth login rejected")
		n.tick(at.Add(time.Duration(m)*time.Minute), false)
		if msgs := drain(n); len(msgs) != 0 {
			t.Fatalf("minute %d: repeated within 10 minutes: %q", m, msgs)
		}
	}
	for i := 0; i < 10; i++ {
		logger.Warn("auth login rejected")
	}
	n.tick(at.Add(12*time.Minute), false)
	if msgs := drain(n); len(msgs) != 1 {
		t.Fatalf("after 10 minutes: %q", msgs)
	}
}

// 不変条件: NOTIFY-INV-6, NOTIFY-INV-8
func TestRules_MessagesExistInCode(t *testing.T) {
	var src strings.Builder
	for _, dir := range []string{"../../features", "../../internal"} {
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || strings.HasPrefix(filepath.ToSlash(path), "../../internal/notify/") {
				return nil
			}
			b, err := os.ReadFile(path) // #nosec G304 -- 試験がリポジトリのソースを読む
			if err != nil {
				return err
			}
			src.Write(b)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	code := src.String()
	for _, m := range Messages() {
		if !strings.Contains(code, `"`+m+`"`) {
			t.Errorf("log message %q is not in the code", m)
		}
		if strings.HasPrefix(m, "notify") {
			t.Errorf("the notifier must not count its own log %q", m)
		}
	}
	// 属性の条件の値（例: お問い合わせの result の saved）も、コードに文字列として在る
	for _, r := range rules {
		if r.attr != "" && (!strings.Contains(code, `"`+r.attr+`"`) || !strings.Contains(code, `"`+r.value+`"`)) {
			t.Errorf("attribute %s=%s of %q is not in the code", r.attr, r.value, r.message)
		}
	}
	for k := kind(0); k < numKinds; k++ {
		if _, ok := immediate[k]; !ok && k != kindContact && k != kindAuthRejected {
			t.Errorf("kind %d has no text", k)
		}
	}
}

// 通知自身の失敗のログは数えない。
// 不変条件: NOTIFY-INV-8
func TestHandler_OwnLogsAreNotCounted(t *testing.T) {
	n, logger := testNotifier(t)
	n.logger = logger
	n.logFailure(500)
	n.logFailure(0)
	if c := n.take(); c != ([numKinds]int{}) {
		t.Errorf("counted its own logs: %v", c)
	}
}

// blockingTransport は、止めるまで応答を返しません。
type blockingTransport struct {
	release chan struct{}
	calls   atomic.Int32
}

func (b *blockingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	b.calls.Add(1)
	select {
	case <-b.release:
	case <-r.Context().Done():
	}
	return nil, context.Canceled
}

// 送り先が応答しない・待ち行列が満杯・見回りが panic しても、ログの出力はすぐ返る。
// 不変条件: NOTIFY-INV-4
func TestNotifier_NeverBlocksLogging(t *testing.T) {
	bt := &blockingTransport{release: make(chan struct{})}
	defer close(bt.release)
	n := newNotifier("テスト", slackURL, slack, bt)
	n.sleep = func(time.Duration) {}
	logger := slog.New(n.Handler(slog.NewTextHandler(io.Discard, nil)))
	n.Start(logger)
	for i := 0; i < queueSize*3; i++ {
		n.queue <- "x" // 送信が詰まって待ち行列が満杯になるまで入れる
		if len(n.queue) == queueSize {
			break
		}
	}
	start := time.Now()
	for i := 0; i < 1000; i++ {
		logger.Info("auth passkey registered")
	}
	n.tick(time.Now(), false) // 満杯でも待たない
	if d := time.Since(start); d > time.Second {
		t.Errorf("logging blocked for %v", d)
	}

	p := newNotifier("テスト", slackURL, slack, nil)
	p.now = func() time.Time { panic("boom") }
	var buf bytes.Buffer
	pl := slog.New(p.Handler(slog.NewTextHandler(&buf, nil)))
	p.logger = pl
	close(p.stop)
	p.loop() // panic は止まり、待ち行列が閉じる
	if _, open := <-p.queue; open {
		t.Error("queue should be closed after a panic")
	}
	if strings.Contains(buf.String(), "boom") {
		t.Error("panic value was logged")
	}
	pl.Info("auth passkey registered")
}

// recordingServer は、受けた要求のパスと本文を記録します。
type recordingServer struct {
	mu     sync.Mutex
	paths  []string
	bodies []string
}

func (s *recordingServer) handler(status func(path string, n int) int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.paths = append(s.paths, r.URL.Path)
		s.bodies = append(s.bodies, string(b))
		count := len(s.paths)
		s.mu.Unlock()
		code := status(r.URL.Path, count)
		if code == http.StatusFound {
			http.Redirect(w, r, "/elsewhere", http.StatusFound)
			return
		}
		w.WriteHeader(code)
	}
}

func serverNotifier(t *testing.T, srv *httptest.Server, path string, logs *bytes.Buffer) *Notifier {
	t.Helper()
	n := newNotifier("テスト", srv.URL+path, slack, srv.Client().Transport)
	n.sleep = func(time.Duration) {}
	n.logger = slog.New(n.Handler(slog.NewTextHandler(logs, nil)))
	return n
}

// 転送に従わず、失敗のログに URL を出さない。
// 不変条件: NOTIFY-INV-2, NOTIFY-INV-3
func TestSend_RedirectAndFailureLogs(t *testing.T) {
	rec := &recordingServer{}
	srv := httptest.NewTLSServer(rec.handler(func(string, int) int { return http.StatusFound }))
	defer srv.Close()
	var logs bytes.Buffer
	n := serverNotifier(t, srv, "/services/SECRETPATH", &logs)
	n.send("hello")
	for _, p := range rec.paths {
		if p == "/elsewhere" {
			t.Error("followed a redirect")
		}
	}
	if len(rec.paths) != 1 {
		t.Errorf("a redirect is not retried, got %d requests", len(rec.paths))
	}
	if !strings.Contains(logs.String(), "notify send failed") || !strings.Contains(logs.String(), "status=302") ||
		strings.Contains(logs.String(), "SECRETPATH") || strings.Contains(logs.String(), srv.URL) {
		t.Errorf("failure log: %s", logs.String())
	}

	srv.Close()
	logs.Reset()
	n.send("hello") // 接続の失敗
	if strings.Contains(logs.String(), "SECRETPATH") || strings.Contains(logs.String(), "127.0.0.1") {
		t.Errorf("connection failure log leaks the URL: %s", logs.String())
	}
}

// 429 と 5xx は1回だけ送り直す。400 は送り直さない。404 の後は送らない。
func TestSend_RetryAndGone(t *testing.T) {
	cases := []struct {
		first, second int
		want          int
	}{
		{http.StatusTooManyRequests, http.StatusOK, 2},
		{http.StatusInternalServerError, http.StatusInternalServerError, 2},
		{http.StatusBadRequest, http.StatusOK, 1},
		{http.StatusOK, http.StatusOK, 1},
	}
	for _, c := range cases {
		rec := &recordingServer{}
		srv := httptest.NewTLSServer(rec.handler(func(_ string, n int) int {
			if n == 1 {
				return c.first
			}
			return c.second
		}))
		var logs bytes.Buffer
		n := serverNotifier(t, srv, "/services/a", &logs)
		n.send("hello")
		if len(rec.paths) != c.want {
			t.Errorf("%d then %d: %d requests, want %d", c.first, c.second, len(rec.paths), c.want)
		}
		srv.Close()
	}

	rec := &recordingServer{}
	srv := httptest.NewTLSServer(rec.handler(func(string, int) int { return http.StatusNotFound }))
	defer srv.Close()
	var logs bytes.Buffer
	n := serverNotifier(t, srv, "/services/a", &logs)
	n.send("one")
	n.send("two")
	if len(rec.paths) != 1 || !strings.Contains(logs.String(), "notify webhook gone") {
		t.Errorf("after 404: %d requests, logs %s", len(rec.paths), logs.String())
	}
}

// 停止のときは、まとめ待ちの件数を送ってから止まる。
func TestNotifier_StopFlushes(t *testing.T) {
	rec := &recordingServer{}
	srv := httptest.NewTLSServer(rec.handler(func(string, int) int { return http.StatusOK }))
	defer srv.Close()
	var logs bytes.Buffer
	n := serverNotifier(t, srv, "/services/a", &logs)
	logger := n.logger
	n.Start(logger)
	logger.Info("contact submission", slog.String("result", "saved"))
	n.Stop()
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.bodies) != 1 || !strings.Contains(rec.bodies[0], "新着 1件") || strings.Contains(rec.bodies[0], "起動しました") {
		t.Errorf("stop flush: %q", rec.bodies)
	}
	n.Stop() // 2回目は何もしない
}

// Start の前の Stop は待たない。停止の途中は送り直さない。
func TestNotifier_StopEdges(t *testing.T) {
	idle := newNotifier("テスト", slackURL, slack, nil)
	start := time.Now()
	idle.Stop()
	idle.Stop()
	if d := time.Since(start); d > time.Second {
		t.Errorf("Stop before Start waited %v", d)
	}

	rec := &recordingServer{}
	srv := httptest.NewTLSServer(rec.handler(func(string, int) int { return http.StatusServiceUnavailable }))
	defer srv.Close()
	var logs bytes.Buffer
	n := serverNotifier(t, srv, "/services/a", &logs)
	slept := false
	n.sleep = func(time.Duration) { slept = true }
	close(n.stop)
	n.send("hello")
	if slept || len(rec.paths) != 1 || !strings.Contains(logs.String(), "status=503") {
		t.Errorf("retried while stopping: slept=%v requests=%d logs=%s", slept, len(rec.paths), logs.String())
	}
}

// 見回りの起きる時刻が少し早くても、1時間・10分の区切りを1回分遅らせない。
func TestTick_ToleratesTickerJitter(t *testing.T) {
	n, logger := testNotifier(t)
	start := n.digestFrom
	logger.Info("contact submission", slog.String("result", "saved"))
	n.tick(start.Add(time.Hour-50*time.Millisecond), false)
	if msgs := drain(n); len(msgs) != 1 {
		t.Fatalf("digest delayed by jitter: %q", msgs)
	}
	for i := 0; i < 10; i++ {
		logger.Warn("auth login rejected")
	}
	n.tick(start.Add(61*time.Minute), false)
	if msgs := drain(n); len(msgs) != 1 {
		t.Fatalf("first reject alert: %q", msgs)
	}
	for i := 0; i < 10; i++ {
		logger.Warn("auth login rejected")
	}
	n.tick(start.Add(71*time.Minute-50*time.Millisecond), false)
	if msgs := drain(n); len(msgs) != 1 {
		t.Fatalf("reject alert delayed by jitter: %q", msgs)
	}
}
