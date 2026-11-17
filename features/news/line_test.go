package news

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"minimal-web-kit/features/auth/authtest"
)

const (
	lineSecretCanary = "SECRETCANARY0123456789abcdef"
	lineTokenCanary  = "TOKENCANARY-eyJhbGciOi"
	lineBodyCanary   = "BODYCANARY-response-text"
)

// fakeLine は、LINE のトークンの発行と一斉送信の偽の送り先です。実際の LINE には送りません。
type fakeLine struct {
	mu          sync.Mutex
	tokenStatus int
	status      int
	header      http.Header
	onBroadcast func() // 一斉送信を受けたときに呼ぶ（試験の割り込み）
	hijack      bool   // 一斉送信の接続を応答なしで切る
	delay       time.Duration
	tokenForms  []url.Values
	broadcasts  []fakeBroadcast
}

type fakeBroadcast struct {
	Auth, Key, Body string
}

func (fl *fakeLine) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		fl.mu.Lock()
		switch r.URL.Path {
		case "/oauth2/v3/token":
			_ = r.ParseForm()
			fl.tokenForms = append(fl.tokenForms, r.PostForm)
			status := fl.tokenStatus
			fl.mu.Unlock()
			if status != 0 && status != http.StatusOK {
				http.Error(w, `{"error":"invalid_request","error_description":"`+lineBodyCanary+`"}`, status)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"token_type":"Bearer","access_token":"` + lineTokenCanary + `","expires_in":900}`))
		case "/v2/bot/message/broadcast":
			body := new(strings.Builder)
			buf := make([]byte, 4096)
			for {
				n, err := r.Body.Read(buf)
				body.Write(buf[:n])
				if err != nil {
					break
				}
			}
			fl.broadcasts = append(fl.broadcasts, fakeBroadcast{Auth: r.Header.Get("Authorization"), Key: r.Header.Get("X-Line-Retry-Key"), Body: body.String()})
			status, header, hook, hijack, delay := fl.status, fl.header, fl.onBroadcast, fl.hijack, fl.delay
			fl.mu.Unlock()
			if hook != nil {
				hook()
			}
			if delay > 0 {
				time.Sleep(delay)
			}
			if hijack {
				conn, _, err := w.(http.Hijacker).Hijack()
				if err == nil {
					_ = conn.Close()
				}
				return
			}
			for k, v := range header {
				w.Header()[k] = v
			}
			if status == 0 {
				status = http.StatusOK
			}
			if status == http.StatusFound {
				w.Header().Set("Location", "/elsewhere")
			}
			w.WriteHeader(status)
			_, _ = w.Write([]byte(lineBodyCanary))
		default:
			fl.mu.Unlock()
			t.Errorf("unexpected LINE path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}
}

func (fl *fakeLine) set(status int, header http.Header) {
	fl.mu.Lock()
	defer fl.mu.Unlock()
	fl.status, fl.header = status, header
}

func (fl *fakeLine) count() (tokens, broadcasts int) {
	fl.mu.Lock()
	defer fl.mu.Unlock()
	return len(fl.tokenForms), len(fl.broadcasts)
}

func (fl *fakeLine) last() fakeBroadcast {
	fl.mu.Lock()
	defer fl.mu.Unlock()
	return fl.broadcasts[len(fl.broadcasts)-1]
}

// setLineEnv は、LINE での知らせを有効にする環境変数とシークレットのファイルを用意します。
func setLineEnv(t *testing.T) {
	t.Helper()
	file := filepath.Join(t.TempDir(), "line-secret")
	if err := os.WriteFile(file, []byte(lineSecretCanary+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SITE_URL", "https://example.com")
	t.Setenv("SITE_NAME", "見本の店")
	t.Setenv("LINE_CHANNEL_ID", "1234567890")
	t.Setenv("LINE_CHANNEL_SECRET_FILE", file)
}

type lineFixture struct {
	*publicFixture
	line   *fakeLine
	cookie *http.Cookie
}

// newLineFixture は、LINE での知らせを有効にし、送り先を偽の送り先に差し替えた構成を作ります。
func newLineFixture(t *testing.T) *lineFixture {
	t.Helper()
	setLineEnv(t)
	fx := newPublicFixture(t, "")
	fl := &fakeLine{}
	srv := httptest.NewTLSServer(fl.handler(t))
	t.Cleanup(srv.Close)
	fx.feature.line = newLineClientAt(fx.feature.cfg.Line, srv.URL+"/oauth2/v3/token", srv.URL+"/v2/bot/message/broadcast", srv.Client().Transport)
	return &lineFixture{publicFixture: fx, line: fl, cookie: authtest.NewSessionWithRole(t, fx.db, "site_admin")}
}

func (lf *lineFixture) path(id int64) string { return "/admin/news/" + strconv.FormatInt(id, 10) }

// confirm は確認の画面を開き、フォームの digest を返します。
func (lf *lineFixture) confirm(t *testing.T, id int64) (string, *httptest.ResponseRecorder) {
	t.Helper()
	rec := lf.do(t, http.MethodGet, lf.path(id)+"/line", nil, lf.cookie)
	m := regexp.MustCompile(`name="digest" value="([0-9a-f]{64})"`).FindStringSubmatch(rec.Body.String())
	if rec.Code != http.StatusOK || m == nil {
		t.Fatalf("confirm page: %d %s", rec.Code, rec.Body.String())
	}
	return m[1], rec
}

func (lf *lineFixture) send(t *testing.T, id int64, digest string) *httptest.ResponseRecorder {
	t.Helper()
	return lf.postForm(t, lf.path(id)+"/line", withCSRF(lf.publicFixture, lf.cookie, url.Values{"digest": {digest}}), "same-origin", lf.cookie)
}

func (lf *lineFixture) record(t *testing.T, id int64) *lineRecord {
	t.Helper()
	rec, err := lf.feature.lines.get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

// 不変条件: NEWS-INV-19, NEWS-INV-20, NEWS-INV-22
// ユースケース: UC-NEWS-10
func TestLine_SendOnce(t *testing.T) {
	lf := newLineFixture(t)
	id := lf.publish(t, "春の<b>セール</b>", "本文のカナリア BODYOFPOST")

	edit := lf.do(t, http.MethodGet, lf.path(id), nil, lf.cookie)
	if edit.Code != http.StatusOK || !strings.Contains(edit.Body.String(), `href="`+lf.path(id)+`/line"`) {
		t.Fatalf("edit page must link to the LINE confirmation: %d", edit.Code)
	}
	digest, page := lf.confirm(t, id)
	want := "見本の店のお知らせ\n春の<b>セール</b>\nhttps://example.com/news/" + strconv.FormatInt(id, 10)
	if digest != lineDigest(want) || !strings.Contains(page.Body.String(), "春の&lt;b&gt;セール&lt;/b&gt;") {
		t.Fatalf("confirm page must show the exact message, escaped: %s", page.Body.String())
	}
	if tokens, sent := lf.line.count(); tokens != 0 || sent != 0 {
		t.Fatal("opening the confirmation must not contact LINE")
	}

	rec := lf.send(t, id, digest)
	if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "line_sent") {
		t.Fatalf("send: %d %s", rec.Code, rec.Body.String())
	}
	form := lf.line.tokenForms[0]
	if form.Get("grant_type") != "client_credentials" || form.Get("client_id") != "1234567890" || form.Get("client_secret") != lineSecretCanary {
		t.Fatalf("token request: %v", form)
	}
	got := lf.line.last()
	if got.Auth != "Bearer "+lineTokenCanary || !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(got.Key) {
		t.Fatalf("broadcast headers: %+v", got)
	}
	// 送る本文は、テキスト1通で、確認の画面で見せた文面と完全に一致する（通知を消す指定は付けない）
	var sentBody struct {
		Messages []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"messages"`
		NotificationDisabled *bool `json:"notificationDisabled"`
	}
	if err := json.Unmarshal([]byte(got.Body), &sentBody); err != nil || len(sentBody.Messages) != 1 ||
		sentBody.Messages[0].Type != "text" || sentBody.Messages[0].Text != want || sentBody.NotificationDisabled != nil {
		t.Fatalf("broadcast body: %s", got.Body)
	}
	if r := lf.record(t, id); r == nil || r.SentAt == 0 || r.InFlight != 0 || r.Message != want {
		t.Fatalf("record after send: %+v", r)
	}

	// 送信済みの記事は、もう送らない
	again := lf.send(t, id, digest)
	if _, sent := lf.line.count(); again.Code != http.StatusConflict || sent != 1 {
		t.Fatalf("second send: %d, broadcasts %d", again.Code, sent)
	}
	if rec := lf.do(t, http.MethodGet, lf.path(id)+"/line", nil, lf.cookie); rec.Code != http.StatusConflict {
		t.Fatalf("confirm page of a sent post: %d", rec.Code)
	}
	edit = lf.do(t, http.MethodGet, lf.path(id), nil, lf.cookie)
	if strings.Contains(edit.Body.String(), `href="`+lf.path(id)+`/line"`) || !strings.Contains(edit.Body.String(), "LINE の友だちに知らせました") {
		t.Fatal("edit page of a sent post must not link to sending again")
	}
}

// 不変条件: NEWS-INV-19
func TestLine_OnlyPublicPosts(t *testing.T) {
	lf := newLineFixture(t)
	ctx := context.Background()
	draft, _ := lf.posts.Create(ctx, "下書き", "")
	scheduled, _ := lf.posts.CreateWithDate(ctx, "予約", "", time.Now().Add(time.Hour))
	if err := lf.posts.Publish(ctx, scheduled); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{draft, scheduled} {
		edit := lf.do(t, http.MethodGet, lf.path(id), nil, lf.cookie)
		if strings.Contains(edit.Body.String(), `href="`+lf.path(id)+`/line"`) {
			t.Errorf("post %d: link must not be shown", id)
		}
		if rec := lf.do(t, http.MethodGet, lf.path(id)+"/line", nil, lf.cookie); rec.Code != http.StatusConflict {
			t.Errorf("post %d: confirm page %d", id, rec.Code)
		}
		msg, _ := lineMessage(lf.feature.cfg.Line, Post{ID: id, Title: "下書き"})
		if rec := lf.send(t, id, lineDigest(msg)); rec.Code != http.StatusConflict {
			t.Errorf("post %d: send %d", id, rec.Code)
		}
		if lf.record(t, id) != nil {
			t.Errorf("post %d: record must not be created", id)
		}
	}
	if _, sent := lf.line.count(); sent != 0 {
		t.Fatalf("non-public posts must not be broadcast: %d", sent)
	}
	if rec := lf.send(t, 999999, "x"); rec.Code != http.StatusNotFound {
		t.Fatalf("missing post: %d", rec.Code)
	}
}

// 不変条件: NEWS-INV-19, NEWS-INV-21
func TestLine_MessageIsFixedAfterConfirmation(t *testing.T) {
	lf := newLineFixture(t)
	ctx := context.Background()
	id := lf.publish(t, "最初の題名", "")
	digest, _ := lf.confirm(t, id)
	// 確認の後に題名が直されたら、送らずに確認の画面を出し直す
	if err := lf.posts.Update(ctx, id, "直した題名", ""); err != nil {
		t.Fatal(err)
	}
	rec := lf.send(t, id, digest)
	if _, sent := lf.line.count(); rec.Code != http.StatusConflict || sent != 0 || !strings.Contains(rec.Body.String(), "直した題名") || lf.record(t, id) != nil {
		t.Fatalf("changed message: %d, broadcasts %d", rec.Code, sent)
	}

	// 結果が分からなかった後は、題名を直しても、保存した文面と鍵で送り直す
	digest, _ = lf.confirm(t, id)
	lf.line.set(http.StatusInternalServerError, nil)
	if rec := lf.send(t, id, digest); rec.Code != http.StatusBadGateway {
		t.Fatalf("uncertain send: %d", rec.Code)
	}
	first := lf.line.last()
	if r := lf.record(t, id); r.state(time.Now()) != linePending {
		t.Fatalf("after 500 the record must be pending: %+v", r)
	}
	if err := lf.posts.Update(ctx, id, "もう一度直した題名", ""); err != nil {
		t.Fatal(err)
	}
	digest2, page := lf.confirm(t, id)
	if digest2 != digest || !strings.Contains(page.Body.String(), "直した題名") || strings.Contains(page.Body.String(), "もう一度直した題名") {
		t.Fatal("confirm page must show the stored message")
	}
	lf.line.set(http.StatusConflict, http.Header{"X-Line-Accepted-Request-Id": {"abc"}})
	if rec := lf.send(t, id, digest2); rec.Code != http.StatusSeeOther {
		t.Fatalf("retry: %d", rec.Code)
	}
	second := lf.line.last()
	if second.Key != first.Key || second.Body != first.Body {
		t.Fatalf("retry must reuse the key and the message: %+v %+v", first, second)
	}
	if r := lf.record(t, id); r.state(time.Now()) != lineDone {
		t.Fatalf("409 with the accepted id must mark sent: %+v", r)
	}
}

// 不変条件: NEWS-INV-20, NEWS-INV-21
func TestLine_ResponseClassification(t *testing.T) {
	cases := []struct {
		name   string
		status int
		header http.Header
		hijack bool
		code   int
		state  lineState
	}{
		{"200", http.StatusOK, nil, false, http.StatusSeeOther, lineDone},
		{"409 accepted", http.StatusConflict, http.Header{"X-Line-Accepted-Request-Id": {"r"}}, false, http.StatusSeeOther, lineDone},
		{"409 without header", http.StatusConflict, nil, false, http.StatusBadGateway, linePending},
		{"400", http.StatusBadRequest, nil, false, http.StatusBadGateway, lineReady},
		{"401", http.StatusUnauthorized, nil, false, http.StatusBadGateway, lineReady},
		{"403", http.StatusForbidden, nil, false, http.StatusBadGateway, lineReady},
		{"429", http.StatusTooManyRequests, nil, false, http.StatusBadGateway, lineReady},
		{"500", http.StatusInternalServerError, nil, false, http.StatusBadGateway, linePending},
		{"302", http.StatusFound, nil, false, http.StatusBadGateway, linePending},
		{"418", http.StatusTeapot, nil, false, http.StatusBadGateway, linePending},
		{"connection closed", 0, nil, true, http.StatusBadGateway, linePending},
	}
	lf := newLineFixture(t)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			id := lf.publish(t, "題名 "+c.name, "")
			digest, _ := lf.confirm(t, id)
			lf.line.set(c.status, c.header)
			lf.line.mu.Lock()
			lf.line.hijack = c.hijack
			lf.line.mu.Unlock()
			rec := lf.send(t, id, digest)
			if rec.Code != c.code {
				t.Fatalf("status %d, want %d", rec.Code, c.code)
			}
			r := lf.record(t, id)
			if got := r.state(time.Now()); got != c.state || r.InFlight != 0 {
				t.Fatalf("state %v (in_flight %d), want %v", got, r.InFlight, c.state)
			}
		})
	}
	lf.line.mu.Lock()
	lf.line.hijack = false
	lf.line.mu.Unlock()

	// 時間切れも、受け付けたかもしれない
	lf.feature.line.http.Timeout = 100 * time.Millisecond
	lf.line.set(http.StatusOK, nil)
	lf.line.mu.Lock()
	lf.line.delay = time.Second
	lf.line.mu.Unlock()
	id := lf.publish(t, "時間切れ", "")
	digest, _ := lf.confirm(t, id)
	if rec := lf.send(t, id, digest); rec.Code != http.StatusBadGateway || lf.record(t, id).state(time.Now()) != linePending {
		t.Fatalf("timeout: %d", rec.Code)
	}
}

// 不変条件: NEWS-INV-20
func TestLine_TokenFailureDoesNotSend(t *testing.T) {
	lf := newLineFixture(t)
	id := lf.publish(t, "題名", "")
	digest, _ := lf.confirm(t, id)
	lf.line.mu.Lock()
	lf.line.tokenStatus = http.StatusBadRequest
	lf.line.mu.Unlock()
	rec := lf.send(t, id, digest)
	if _, sent := lf.line.count(); rec.Code != http.StatusBadGateway || sent != 0 || lf.record(t, id) != nil {
		t.Fatalf("token failure: %d, broadcasts %d", rec.Code, sent)
	}
	// 鍵を作れない（記録をコミットできない）ときも送らない
	lf.line.mu.Lock()
	lf.line.tokenStatus = 0
	lf.line.mu.Unlock()
	lf.feature.newKey = func() (string, error) { return "", os.ErrInvalid }
	rec = lf.send(t, id, digest)
	if _, sent := lf.line.count(); rec.Code != http.StatusInternalServerError || sent != 0 || lf.record(t, id) != nil {
		t.Fatalf("prepare failure: %d, broadcasts %d", rec.Code, sent)
	}
}

// 不変条件: NEWS-INV-20, NEWS-INV-22
func TestLine_RecordFailureAfterSendIsPending(t *testing.T) {
	lf := newLineFixture(t)
	id := lf.publish(t, "題名", "")
	digest, _ := lf.confirm(t, id)
	// 送った後の書き込みを失敗させる（LINE は受け付けた）
	lf.line.mu.Lock()
	lf.line.onBroadcast = func() {
		if _, err := lf.db.Exec("CREATE TRIGGER block_line BEFORE UPDATE ON news_line_broadcasts BEGIN SELECT RAISE(ABORT, 'blocked'); END"); err != nil {
			t.Error(err)
		}
	}
	lf.line.mu.Unlock()
	rec := lf.send(t, id, digest)
	if _, err := lf.db.Exec("DROP TRIGGER block_line"); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("record failure: %d", rec.Code)
	}
	r := lf.record(t, id)
	if r.state(time.Now()) != linePending || r.InFlight != 1 {
		t.Fatalf("an unfinished request must leave the record pending: %+v", r)
	}
	// 同じ鍵で送り直すと、受け付け済みの 409 で送信済みになる
	lf.line.mu.Lock()
	lf.line.onBroadcast = nil
	lf.line.mu.Unlock()
	lf.line.set(http.StatusConflict, http.Header{"X-Line-Accepted-Request-Id": {"r"}})
	first := lf.line.last()
	if rec := lf.send(t, id, digest); rec.Code != http.StatusSeeOther || lf.line.last().Key != first.Key {
		t.Fatalf("retry after record failure: %d", rec.Code)
	}
	if lf.record(t, id).state(time.Now()) != lineDone {
		t.Fatal("must be sent")
	}
	// 送信済みの後に遅れて返った失敗は、記録を変えない
	if err := lf.feature.lines.finish(context.Background(), id, lineUncertain); err != nil {
		t.Fatal(err)
	}
	if r := lf.record(t, id); r.state(time.Now()) != lineDone || r.Uncertain {
		t.Fatalf("late failure changed a sent record: %+v", r)
	}
}

// 不変条件: NEWS-INV-20, NEWS-INV-22
func TestLine_ConcurrentSendsShareTheKey(t *testing.T) {
	lf := newLineFixture(t)
	id := lf.publish(t, "題名", "")
	digest, _ := lf.confirm(t, id)
	// 1つ目の要求が LINE に届いている間に、別の管理者が送る
	// 割り込みは1回だけ（2つ目の要求の一斉送信では、もう割り込まない）
	var started atomic.Bool
	var inner *httptest.ResponseRecorder
	done := make(chan struct{})
	lf.line.mu.Lock()
	lf.line.onBroadcast = func() {
		if started.CompareAndSwap(false, true) {
			inner = lf.send(t, id, digest)
			close(done)
		}
	}
	lf.line.mu.Unlock()
	outer := lf.send(t, id, digest)
	<-done
	if outer.Code != http.StatusSeeOther || inner == nil || inner.Code != http.StatusSeeOther {
		t.Fatalf("concurrent sends: %d %v", outer.Code, inner)
	}
	lf.line.mu.Lock()
	defer lf.line.mu.Unlock()
	if len(lf.line.broadcasts) != 2 || lf.line.broadcasts[0].Key != lf.line.broadcasts[1].Key {
		t.Fatalf("both requests must use the same key so that LINE sends once: %+v", lf.line.broadcasts)
	}
	// 後から返った2つ目の応答は、送信済みの記録を変えない（in_flight も減らさない）
	if r := lf.record(t, id); r.state(time.Now()) != lineDone {
		t.Fatalf("record: %+v", r)
	}
}

// 不変条件: NEWS-INV-21
func TestLineStore_RetryWindowAndKeyRotation(t *testing.T) {
	lf := newLineFixture(t)
	ctx := context.Background()
	// 記事を公開した時刻（秒）より後にする。同じ秒の境目で、公開前と判定されないように
	base := time.Now().Add(time.Minute)
	now := base
	st := &lineStore{db: lf.db, now: func() time.Time { return now }}
	keys := 0
	newKey := func() (string, error) { keys++; return "key-" + strconv.Itoa(keys), nil }
	prepare := func(id int64) (lineRecord, error) {
		p, err := lf.posts.Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		msg, _ := lineMessage(lf.feature.cfg.Line, p)
		if r, _ := st.get(ctx, id); r != nil {
			msg = r.Message
		}
		return st.prepare(ctx, lf.feature.cfg.Line, id, lineDigest(msg), newKey)
	}

	// 受け付けなかった要求だけなら、23時間のうちは同じ鍵、過ぎたら鍵だけを入れ替える
	rejected := lf.publish(t, "断られた", "")
	r1, err := prepare(rejected)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.finish(ctx, rejected, lineRejected); err != nil {
		t.Fatal(err)
	}
	now = base.Add(lineRetryWindow - time.Second)
	r2, err := prepare(rejected)
	if err != nil || r2.Key != r1.Key {
		t.Fatalf("within the window: %v %s %s", err, r1.Key, r2.Key)
	}
	_ = st.finish(ctx, rejected, lineRateLimited)
	now = base.Add(lineRetryWindow)
	r3, err := prepare(rejected)
	if err != nil || r3.Key == r1.Key || r3.Message != r1.Message || r3.KeyFirstSentAt != now.Unix() {
		t.Fatalf("after the window the key must be replaced: %v %+v", err, r3)
	}
	_ = st.finish(ctx, rejected, lineRejected)

	// 受け付けたかもしれない要求の後は、断られても鍵を変えず、23時間を過ぎたら送らない
	now = base
	uncertain := lf.publish(t, "分からない", "")
	u1, err := prepare(uncertain)
	if err != nil {
		t.Fatal(err)
	}
	_ = st.finish(ctx, uncertain, lineUncertain)
	now = base.Add(time.Hour)
	u2, err := prepare(uncertain)
	if err != nil || u2.Key != u1.Key {
		t.Fatalf("pending retry: %v", err)
	}
	_ = st.finish(ctx, uncertain, lineRejected)
	now = base.Add(lineRetryWindow - time.Second)
	if r, _ := st.get(ctx, uncertain); r.state(now) != linePending {
		t.Fatal("a rejection after an uncertain request must stay pending")
	}
	now = base.Add(lineRetryWindow)
	if _, err := prepare(uncertain); err != errLineUnknown {
		t.Fatalf("after the window: %v", err)
	}

	// 結果を書けなかった要求（in_flight が残る）も、23時間を過ぎたら送らない
	now = base
	stuck := lf.publish(t, "止まった", "")
	if _, err := prepare(stuck); err != nil {
		t.Fatal(err)
	}
	now = base.Add(lineRetryWindow)
	if _, err := prepare(stuck); err != errLineUnknown {
		t.Fatalf("stuck in flight: %v", err)
	}
}

// 不変条件: NEWS-INV-23
func TestLine_Permissions(t *testing.T) {
	lf := newLineFixture(t)
	id := lf.publish(t, "題名", "")
	digest, _ := lf.confirm(t, id)
	path := lf.path(id) + "/line"
	if rec := lf.do(t, http.MethodGet, path, nil); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/login" {
		t.Errorf("GET without session: %d", rec.Code)
	}
	form := url.Values{"digest": {digest}}
	if rec := lf.postForm(t, path, form, "same-origin", lf.cookie); rec.Code != http.StatusForbidden {
		t.Errorf("POST without CSRF token: %d", rec.Code)
	}
	if rec := lf.postForm(t, path, withCSRF(lf.publicFixture, lf.cookie, form), "cross-site", lf.cookie); rec.Code != http.StatusForbidden {
		t.Errorf("POST cross-site: %d", rec.Code)
	}
	user := authtest.NewSessionWithRole(t, lf.db, "user")
	if rec := lf.do(t, http.MethodGet, path, nil, user); rec.Code == http.StatusOK {
		t.Errorf("GET by a general user: %d", rec.Code)
	}
	// 一般ユーザー自身の有効なCSRFトークンを付けて、ロールの検査まで届かせる
	userForm := url.Values{"digest": {digest}, "csrf_token": {authtest.CSRFToken(t, lf.routes, "/admin/passkeys", user)}}
	if rec := lf.postForm(t, path, userForm, "same-origin", user); rec.Code != http.StatusForbidden {
		t.Errorf("POST by a general user with a valid CSRF token: %d", rec.Code)
	}
	if lf.record(t, id) != nil {
		t.Error("a general user must not create a record")
	}
	if tokens, sent := lf.line.count(); tokens != 0 || sent != 0 {
		t.Fatalf("rejected requests must not contact LINE: %d %d", tokens, sent)
	}
	for _, tool := range lf.feature.MCPTools() {
		if strings.Contains(strings.ToLower(tool.Name), "line") {
			t.Errorf("MCP must not expose LINE: %s", tool.Name)
		}
	}
	// システム管理者も送れる
	admin := authtest.NewSessionWithRole(t, lf.db, "admin")
	if rec := lf.postForm(t, path, withCSRF(lf.publicFixture, admin, form), "same-origin", admin); rec.Code != http.StatusSeeOther {
		t.Errorf("POST by a system administrator: %d", rec.Code)
	}
}

// 不変条件: NEWS-INV-24
func TestLine_SecretsDoNotLeak(t *testing.T) {
	lf := newLineFixture(t)
	var bodies []string
	for _, status := range []int{http.StatusBadRequest, http.StatusInternalServerError, http.StatusOK} {
		id := lf.publish(t, "題名", "")
		digest, _ := lf.confirm(t, id)
		lf.line.set(status, nil)
		bodies = append(bodies, lf.send(t, id, digest).Body.String())
	}
	id := lf.publish(t, "題名", "")
	digest, _ := lf.confirm(t, id)
	lf.line.mu.Lock()
	lf.line.tokenStatus = http.StatusBadRequest
	lf.line.mu.Unlock()
	bodies = append(bodies, lf.send(t, id, digest).Body.String())
	bodies = append(bodies, lf.logs.String())
	for _, b := range bodies {
		for _, canary := range []string{lineSecretCanary, lineTokenCanary, lineBodyCanary, "Bearer", "api.line.me", "127.0.0.1"} {
			if strings.Contains(b, canary) {
				t.Errorf("%q leaked into a response or the log", canary)
			}
		}
	}
	if !strings.Contains(lf.logs.String(), "news line broadcast") {
		t.Fatal("broadcasts must be logged by result")
	}
}

// 不変条件: NEWS-INV-25
func TestLine_DisabledByDefault(t *testing.T) {
	t.Setenv("LINE_CHANNEL_ID", "")
	t.Setenv("LINE_CHANNEL_SECRET_FILE", "")
	fx := newPublicFixture(t, "")
	if fx.feature.line != nil {
		t.Fatal("LINE must be disabled without settings")
	}
	id := fx.publish(t, "題名", "")
	_, cookie := fx.loggedIn(t)
	path := "/admin/news/" + strconv.FormatInt(id, 10)
	if rec := fx.do(t, http.MethodGet, path+"/line", nil, cookie); rec.Code == http.StatusOK {
		t.Fatalf("confirm page must not exist: %d", rec.Code)
	}
	if rec := fx.do(t, http.MethodGet, path, nil, cookie); strings.Contains(rec.Body.String(), "LINE") {
		t.Fatal("edit page must not mention LINE")
	}
}

// 不変条件: NEWS-INV-24, NEWS-INV-25
func TestLineConfig_Validation(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string, mode os.FileMode) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, mode); err != nil {
			t.Fatal(err)
		}
		return p
	}
	good := write("good", lineSecretCanary+"\n", 0o600)
	t.Setenv("SITE_URL", "https://example.com")
	t.Setenv("SITE_NAME", "見本の店")
	env := func(id, file string) func(string) string {
		return func(k string) string {
			switch k {
			case "LINE_CHANNEL_ID":
				return id
			case "LINE_CHANNEL_SECRET_FILE":
				return file
			}
			return ""
		}
	}
	if cfg, err := loadLineConfig(env("", "")); cfg != nil || err != nil {
		t.Fatal("unset must disable LINE")
	}
	cfg, err := loadLineConfig(env("1234567890", good))
	if err != nil || string(cfg.Secret) != lineSecretCanary || cfg.SiteURL != "https://example.com" {
		t.Fatalf("good config: %v", err)
	}
	bad := map[string]func(string) string{
		"id only":         env("1234567890", ""),
		"file only":       env("", good),
		"id not digits":   env("12a4", good),
		"id too long":     env("123456789012345678901", good),
		"loose file":      env("1", write("loose", lineSecretCanary, 0o644)),
		"missing file":    env("1", filepath.Join(dir, "none")),
		"empty file":      env("1", write("empty", "\n", 0o600)),
		"inner space":     env("1", write("space", "abc def", 0o600)),
		"two lines":       env("1", write("lines", "abc\ndef\n", 0o600)),
		"control char":    env("1", write("ctrl", "abc\x01def", 0o600)),
		"non-ASCII value": env("1", write("wide", "秘密", 0o600)),
	}
	for name, getenv := range bad {
		cfg, err := loadLineConfig(getenv)
		if err == nil || cfg != nil {
			t.Errorf("%s: must stop the start-up", name)
			continue
		}
		if strings.Contains(err.Error(), lineSecretCanary) || strings.Contains(err.Error(), dir) {
			t.Errorf("%s: error must not contain the value: %v", name, err)
		}
	}
	t.Setenv("SITE_URL", "")
	if _, err := loadLineConfig(env("1", good)); err == nil {
		t.Error("SITE_URL must be required")
	}
	t.Setenv("SITE_URL", "https://example.com")
	t.Setenv("SITE_NAME", "")
	if _, err := loadLineConfig(env("1", good)); err == nil {
		t.Error("SITE_NAME must be required")
	}
	if s := lineSecret(lineSecretCanary); strings.Contains(s.String(), lineSecretCanary) || strings.Contains(s.LogValue().String(), lineSecretCanary) {
		t.Error("the secret must be redacted when printed")
	}
}

// 不変条件: NEWS-INV-20
func TestLineStore_ConcurrentFirstPrepareCreatesOneRecord(t *testing.T) {
	lf := newLineFixture(t)
	ctx := context.Background()
	id := lf.publish(t, "題名", "")
	p, err := lf.posts.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	msg, _ := lineMessage(lf.feature.cfg.Line, p)
	// 記録の無い記事に、送る前の書き込みを同時に始める。行の作成は1つだけ通り、他は保存済みの鍵を使う
	const n = 8
	keys := make([]string, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			rec, err := lf.feature.lines.prepare(ctx, lf.feature.cfg.Line, id, lineDigest(msg), func() (string, error) {
				return "key-" + strconv.Itoa(i), nil
			})
			keys[i], errs[i] = rec.Key, err
		}()
	}
	close(start)
	wg.Wait()
	for i := range n {
		if errs[i] != nil {
			t.Fatalf("prepare %d: %v", i, errs[i])
		}
		if keys[i] != keys[0] {
			t.Fatalf("all requests must use the stored key: %v", keys)
		}
	}
	r := lf.record(t, id)
	if r.Key != keys[0] || r.InFlight != n {
		t.Fatalf("record: %+v", r)
	}
}

// lineTransport は、LINE の偽の通信路です。一斉送信の応答と、通信の失敗を差し込みます。
type lineTransport struct {
	broadcast func(*http.Request) (*http.Response, error)
}

func (lt lineTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if strings.HasSuffix(r.URL.Path, "/oauth2/v3/token") {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Request: r,
			Body: io.NopCloser(strings.NewReader(`{"token_type":"Bearer","access_token":"` + lineTokenCanary + `"}`))}, nil
	}
	return lt.broadcast(r)
}

// failingBody は、読むと失敗する応答の本文です。
type failingBody struct{}

func (failingBody) Read([]byte) (int, error) { return 0, errors.New("read failed " + lineBodyCanary) }
func (failingBody) Close() error             { return nil }

// 不変条件: NEWS-INV-21, NEWS-INV-24
func TestLine_TransportErrorsAndUnreadableBodies(t *testing.T) {
	lf := newLineFixture(t)
	cfg := lf.feature.cfg.Line
	const tokenURL, broadcastURL = "https://line.test/oauth2/v3/token", "https://line.test/v2/bot/message/broadcast"
	respond := func(status int, header http.Header) func(*http.Request) (*http.Response, error) {
		return func(r *http.Request) (*http.Response, error) {
			if header == nil {
				header = http.Header{}
			}
			return &http.Response{StatusCode: status, Header: header, Body: failingBody{}, Request: r}, nil
		}
	}
	// 本文を読めなくても、状態コードと見出しで分ける
	for _, c := range []struct {
		status int
		header http.Header
		want   lineOutcome
	}{
		{http.StatusOK, nil, lineSent},
		{http.StatusConflict, http.Header{"X-Line-Accepted-Request-Id": {"r"}}, lineAlreadyAccepted},
		{http.StatusTooManyRequests, nil, lineRateLimited},
		{http.StatusInternalServerError, nil, lineUncertain},
	} {
		client := newLineClientAt(cfg, tokenURL, broadcastURL, lineTransport{broadcast: respond(c.status, c.header)})
		if got := client.broadcast(context.Background(), "t", "k", "m"); got != c.want {
			t.Errorf("status %d with an unreadable body: %s, want %s", c.status, got, c.want)
		}
	}

	// 通信の失敗の文（送り先やカナリアを含む）は、受け付けたかもしれないとして扱い、ログにも画面にも出さない
	lf.feature.line = newLineClientAt(cfg, tokenURL, broadcastURL, lineTransport{broadcast: func(*http.Request) (*http.Response, error) {
		return nil, errors.New("dial tcp line.test: connection reset " + lineBodyCanary)
	}})
	id := lf.publish(t, "題名", "")
	digest, _ := lf.confirm(t, id)
	rec := lf.send(t, id, digest)
	if rec.Code != http.StatusBadGateway || lf.record(t, id).state(time.Now()) != linePending {
		t.Fatalf("transport error: %d", rec.Code)
	}
	for _, out := range []string{rec.Body.String(), lf.logs.String()} {
		for _, canary := range []string{lineBodyCanary, lineTokenCanary, "line.test", "connection reset"} {
			if strings.Contains(out, canary) {
				t.Errorf("%q leaked into a response or the log", canary)
			}
		}
	}
}
