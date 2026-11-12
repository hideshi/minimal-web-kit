// Package notify は、運営者への通知を Slack・Discord の Webhook へ送ります（docs/domain/notify.md）。
//
// 出来事は、各機能がすでに出しているログを、決まった名前の表（rules）で数えて拾います。
// 送る文は、サイト名・出来事の種類・件数・時間の幅・固定の案内文だけで作ります。
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"minimal-web-kit/internal/federation"
)

// kind は、知らせる出来事の種類です。
type kind int

const (
	kindContact kind = iota
	kindPasskeyRegistered
	kindPasskeyDeleted
	kindPasskeyDisabled
	kindAccountCreated
	kindAccountDeleted
	kindAccountRoleChanged
	kindAccountDisabled
	kindAccountEnabled
	kindSetupURL
	kindAgentApproved
	kindMCPClient
	kindMCPSetting
	kindAuthRejected
	kindPanic
	numKinds
)

// rule は、拾うログの1行です。attr が空でなければ、記録の最上位の属性 attr が value のときだけ数えます。
type rule struct {
	message string
	kind    kind
	attr    string
	value   string
}

// rules は、拾うログの名前の表です。名前はすべてコードに実在します（NOTIFY-INV-6）。
// 通知自身のログ（notify で始まる名前）は載せません（NOTIFY-INV-8）。
var rules = []rule{
	{message: "contact submission", kind: kindContact, attr: "result", value: "saved"},
	{message: "auth passkey registered", kind: kindPasskeyRegistered},
	{message: "auth passkey deleted", kind: kindPasskeyDeleted},
	{message: "auth passkey disabled", kind: kindPasskeyDisabled},
	{message: "auth account created", kind: kindAccountCreated},
	{message: "auth account deleted", kind: kindAccountDeleted},
	{message: "auth account role changed", kind: kindAccountRoleChanged},
	{message: "auth account disabled", kind: kindAccountDisabled},
	{message: "auth account enabled", kind: kindAccountEnabled},
	{message: "auth setup url issued", kind: kindSetupURL},
	{message: "auth agent approved", kind: kindAgentApproved},
	{message: "mcp client added", kind: kindMCPClient},
	{message: "mcp client changed", kind: kindMCPClient},
	{message: "mcp setting changed", kind: kindMCPSetting},
	{message: "auth login rejected", kind: kindAuthRejected},
	{message: "auth passkey registration rejected", kind: kindAuthRejected},
	{message: "panic recovered", kind: kindPanic},
}

// Messages は、拾うログの名前を返します。表の名前がコードに実在するかの試験に使います。
func Messages() []string {
	out := make([]string, 0, len(rules))
	for _, r := range rules {
		out = append(out, r.message)
	}
	return out
}

// immediate は、1分ごとの見回りですぐ送る出来事の文です。%d に件数が入ります。
var immediate = map[kind]string{
	kindPasskeyRegistered:  "パスキーが登録されました（%d件）。覚えがなければ、管理画面で確かめてください。",
	kindPasskeyDeleted:     "パスキーが削除されました（%d件）。覚えがなければ、管理画面で確かめてください。",
	kindPasskeyDisabled:    "複製の疑いで、パスキーが無効になりました（%d件）。管理画面で確かめてください。",
	kindAccountCreated:     "アカウントが作成されました（%d件）。覚えがなければ、管理画面で確かめてください。",
	kindAccountDeleted:     "アカウントが削除されました（%d件）。覚えがなければ、管理画面で確かめてください。",
	kindAccountRoleChanged: "アカウントの権限が変更されました（%d件）。覚えがなければ、管理画面で確かめてください。",
	kindAccountDisabled:    "アカウントが停止されました（%d件）。覚えがなければ、管理画面で確かめてください。",
	kindAccountEnabled:     "アカウントが再開されました（%d件）。覚えがなければ、管理画面で確かめてください。",
	kindSetupURL:           "パスキーの登録URLが発行されました（%d件）。覚えがなければ、管理画面で確かめてください。",
	kindAgentApproved:      "AIエージェントの接続が承認されました（%d件）。覚えがなければ、管理画面で確かめてください。",
	kindMCPClient:          "MCPのクライアントが追加・変更されました（%d件）。覚えがなければ、管理画面で確かめてください。",
	kindMCPSetting:         "MCPの設定が変更されました（%d件）。覚えがなければ、管理画面で確かめてください。",
	kindPanic:              "予期しないエラーで処理が止まりました（%d件）。サーバーのログを確かめてください。",
}

const (
	tickInterval   = time.Minute
	digestInterval = time.Hour
	rejectWindow   = 10 // 分
	rejectLimit    = 10
	queueSize      = 32
	sendTimeout    = 10 * time.Second
	retryDelay     = 5 * time.Second
	stopTimeout    = 5 * time.Second
	responseLimit  = 64 << 10
)

// target は、送り先のサービスです。
type target int

const (
	slack target = iota + 1
	discord
)

var (
	slackPath   = regexp.MustCompile(`^/services/[A-Za-z0-9]+/[A-Za-z0-9]+/[A-Za-z0-9]+$`)
	discordPath = regexp.MustCompile(`^/api/webhooks/[0-9]+/[A-Za-z0-9_-]+$`)
)

// parseURL は、Webhook の URL を確かめます。エラーに URL を含めません（NOTIFY-INV-2・NOTIFY-INV-3）。
func parseURL(content []byte) (string, target, error) {
	invalid := errors.New("notify: NOTIFY_WEBHOOK_URL_FILE must contain one Slack (https://hooks.slack.com/services/...) or Discord (https://discord.com/api/webhooks/...) webhook URL")
	raw := strings.TrimSpace(string(content))
	for _, r := range raw {
		if r <= ' ' || r == 0x7f || unicode.IsSpace(r) || unicode.IsControl(r) {
			return "", 0, invalid
		}
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Opaque != "" || u.RawQuery != "" || u.ForceQuery ||
		u.Fragment != "" || u.String() != raw {
		return "", 0, invalid
	}
	switch {
	case u.Host == "hooks.slack.com" && slackPath.MatchString(u.EscapedPath()):
		return raw, slack, nil
	case u.Host == "discord.com" && discordPath.MatchString(u.EscapedPath()):
		return raw, discord, nil
	}
	return "", 0, invalid
}

// payload は、送り先の形の JSON を作ります（NOTIFY-INV-7）。
func payload(t target, text string) ([]byte, error) {
	if t == slack {
		esc := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(text)
		return json.Marshal(map[string]string{"text": esc})
	}
	return json.Marshal(struct {
		Content         string              `json:"content"`
		AllowedMentions map[string][]string `json:"allowed_mentions"`
	}{text, map[string][]string{"parse": {}}})
}

// Notifier は、ログから出来事を数え、まとめて Webhook へ送ります。
type Notifier struct {
	site   string
	url    string
	target target
	client *http.Client
	now    func() time.Time
	sleep  func(time.Duration)

	mu     sync.Mutex
	counts [numKinds]int

	// 以下は見回りのゴルーチンだけが触る
	started        bool
	digestFrom     time.Time
	contactPending int
	rejects        [rejectWindow]int
	rejectAt       int
	lastReject     time.Time

	queue   chan string
	gone    atomic.Bool
	logger  *slog.Logger
	stop    chan struct{}
	stopped chan struct{}
	done    chan struct{}
}

// New は、通知を用意します。urlFile が空なら、通知は無効で nil を返します（NOTIFY-INV-5）。
// URL は所有者だけが読めるファイルから読みます（NOTIFY-INV-3）。site は設定された SITE_NAME で、空なら断ります（NOTIFY-INV-9）。
func New(urlFile, site string) (*Notifier, error) {
	if urlFile == "" {
		return nil, nil
	}
	if site == "" {
		return nil, errors.New("notify: SITE_NAME is required when NOTIFY_WEBHOOK_URL_FILE is set")
	}
	content, err := federation.SecretFile(urlFile)
	if err != nil {
		return nil, errors.New("notify: NOTIFY_WEBHOOK_URL_FILE must be a regular file readable only by its owner")
	}
	raw, t, err := parseURL(content)
	if err != nil {
		return nil, err
	}
	return newNotifier(site, raw, t, nil), nil
}

func newNotifier(site, raw string, t target, base http.RoundTripper) *Notifier {
	return &Notifier{
		site:    site,
		url:     raw,
		target:  t,
		client:  federation.Client([]string{raw}, sendTimeout, responseLimit, base),
		now:     time.Now,
		sleep:   time.Sleep,
		queue:   make(chan string, queueSize),
		stop:    make(chan struct{}),
		stopped: make(chan struct{}),
		done:    make(chan struct{}),
	}
}

// Handler は、next を包んで、拾うログを数える slog.Handler を返します。
func (n *Notifier) Handler(next slog.Handler) slog.Handler {
	return handler{next: next, n: n}
}

type handler struct {
	next    slog.Handler
	n       *Notifier
	grouped bool
}

func (h handler) Enabled(ctx context.Context, l slog.Level) bool { return h.next.Enabled(ctx, l) }

// Handle は、数えてから、元の出力へ渡します。数えるときだけロックを持ち、送信を待ちません（NOTIFY-INV-4）。
func (h handler) Handle(ctx context.Context, r slog.Record) error {
	h.n.observe(r, h.grouped)
	return h.next.Handle(ctx, r)
}

// WithAttrs は、先に束ねた属性を判定に使わず、内側へ渡して包み直すだけです。
func (h handler) WithAttrs(as []slog.Attr) slog.Handler {
	return handler{next: h.next.WithAttrs(as), n: h.n, grouped: h.grouped}
}

// WithGroup の後は、属性が最上位でなくなるため、属性の条件を持つ規則には当てません。
func (h handler) WithGroup(name string) slog.Handler {
	return handler{next: h.next.WithGroup(name), n: h.n, grouped: h.grouped || name != ""}
}

func (n *Notifier) observe(r slog.Record, grouped bool) {
	for _, ru := range rules {
		if ru.message != r.Message {
			continue
		}
		if ru.attr != "" && (grouped || !hasAttr(r, ru.attr, ru.value)) {
			continue
		}
		n.mu.Lock()
		n.counts[ru.kind]++
		n.mu.Unlock()
	}
}

func hasAttr(r slog.Record, key, value string) bool {
	found := false
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == key && a.Value.Kind() == slog.KindString && a.Value.String() == value {
			found = true
			return false
		}
		return true
	})
	return found
}

// take は、数えた件数を取り出して0に戻します。
func (n *Notifier) take() [numKinds]int {
	n.mu.Lock()
	defer n.mu.Unlock()
	c := n.counts
	n.counts = [numKinds]int{}
	return c
}

// Start は、見回りと送信のゴルーチンを始めます。logger は送信の失敗の記録に使います。
func (n *Notifier) Start(logger *slog.Logger) {
	n.logger = logger
	n.digestFrom = n.now()
	go n.loop()
	go n.sender()
}

// Stop は、まとめ待ちの件数を送ってから止めます。待つのは stopTimeout までです。
func (n *Notifier) Stop() {
	close(n.stop)
	select {
	case <-n.done:
	case <-time.After(stopTimeout):
	}
}

func (n *Notifier) loop() {
	defer close(n.queue)
	defer n.recoverPanic()
	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()
	for {
		select {
		case <-n.stop:
			n.tick(n.now(), true)
			return
		case <-ticker.C:
			n.tick(n.now(), false)
		}
	}
}

// tick は、件数から送る文を作り、待ち行列へ入れます。final は停止のときで、お問い合わせの件数も送り切ります。
func (n *Notifier) tick(now time.Time, final bool) {
	c := n.take()
	var msgs []string
	if !n.started && !final {
		n.started = true
		msgs = append(msgs, "起動しました。")
	}
	for k := kind(0); k < numKinds; k++ {
		if f, ok := immediate[k]; ok && c[k] > 0 {
			msgs = append(msgs, fmt.Sprintf(f, c[k]))
		}
	}
	n.rejectAt = (n.rejectAt + 1) % rejectWindow
	n.rejects[n.rejectAt] = c[kindAuthRejected]
	total := 0
	for _, v := range n.rejects {
		total += v
	}
	if total >= rejectLimit && (n.lastReject.IsZero() || now.Sub(n.lastReject) >= rejectWindow*time.Minute) {
		n.lastReject = now
		msgs = append(msgs, fmt.Sprintf("認証の拒否が続いています（直近%d分で%d件）。覚えがなければ、管理画面とサーバーのログを確かめてください。", rejectWindow, total))
	}
	n.contactPending += c[kindContact]
	if pending := n.contactPending; pending > 0 && (final || now.Sub(n.digestFrom) >= digestInterval) {
		msgs = append(msgs, fmt.Sprintf("お問い合わせの新着 %d件（%s〜%s）。スパムを含む場合があります。管理画面で確かめてください。",
			pending, n.digestFrom.Format("15:04"), now.Format("15:04")))
		n.contactPending = 0
		n.digestFrom = now
	} else if pending == 0 && now.Sub(n.digestFrom) >= digestInterval {
		n.digestFrom = now
	}
	for _, m := range msgs {
		select {
		case n.queue <- "[" + n.site + "] " + m:
		default: // 満杯なら捨てる。要求の処理を待たせない（NOTIFY-INV-4）
		}
	}
}

// sender は、待ち行列の文を1通ずつ送ります。見回りが待ち行列を閉じたら、残りを送って終わります。
func (n *Notifier) sender() {
	defer close(n.done)
	defer n.recoverPanic()
	for text := range n.queue {
		n.send(text)
	}
}

// recoverPanic は、通知のゴルーチンの panic を止め、自分だけを止めます。panic の値は記録しません（NOTIFY-INV-4）。
func (n *Notifier) recoverPanic() {
	if recover() != nil && n.logger != nil {
		n.logger.Error("notify stopped")
	}
}

// send は、1通を送ります。429 と 5xx だけ、1回だけ送り直します。404 の後は送りません。
// 失敗の記録には、応答のコードか分類だけを残し、URL・本文・エラーの文を出しません（NOTIFY-INV-3）。
func (n *Notifier) send(text string) {
	if n.gone.Load() {
		return
	}
	body, err := payload(n.target, text)
	if err != nil {
		n.logFailure(0)
		return
	}
	for attempt := 0; attempt < 2; attempt++ {
		status := n.post(body)
		switch {
		case status >= 200 && status < 300:
			return
		case status == http.StatusNotFound:
			n.gone.Store(true)
			if n.logger != nil {
				n.logger.Error("notify webhook gone")
			}
			return
		case (status == http.StatusTooManyRequests || status >= 500 || status == 0) && attempt == 0:
			n.sleep(retryDelay)
		default:
			n.logFailure(status)
			return
		}
	}
}

// post は、送って応答のコードを返します。接続の失敗は0です。
func (n *Notifier) post(body []byte) int {
	ctx, cancel := context.WithTimeout(context.Background(), sendTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.url, bytes.NewReader(body))
	if err != nil {
		return 0
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := n.client.Do(req) // #nosec G107 G704 -- 送り先は起動時に Slack・Discord の Webhook の形だけを許した URL で、federation.Client がその URL との完全一致だけを通す
	if err != nil {
		return 0
	}
	_, _ = io.Copy(io.Discard, res.Body)
	_ = res.Body.Close()
	return res.StatusCode
}

func (n *Notifier) logFailure(status int) {
	if n.logger == nil {
		return
	}
	if status == 0 {
		n.logger.Error("notify send failed", slog.String("cause", "connection"))
		return
	}
	n.logger.Error("notify send failed", slog.Int("status", status))
}
