package news

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"minimal-web-kit/internal/federation"
	"minimal-web-kit/internal/web"
)

// LINEでの一斉配信（公開中の記事の題名と URL を、LINE 公式アカウントの友だち全員へ一斉送信する）。
// 仕様は docs/domain/news.md の「LINEでの一斉配信の状態」と NEWS-INV-19〜25。

const (
	lineTokenURL      = "https://api.line.me/oauth2/v3/token"
	lineBroadcastURL  = "https://api.line.me/v2/bot/message/broadcast"
	lineTimeout       = 10 * time.Second
	lineResponseLimit = 16 << 10
	// lineMaxUTF16 は、LINE のテキストのメッセージの上限（UTF-16 の単位）です。
	lineMaxUTF16 = 5000
	// lineMaxChannelID は、チャネル ID の桁数の上限です（数字だけを許す）。
	lineMaxChannelID = 20
)

// lineSecret はチャネルシークレットです。ログと fmt には値を出しません（NEWS-INV-24）。
type lineSecret string

func (lineSecret) LogValue() slog.Value { return slog.StringValue("[REDACTED]") }
func (lineSecret) String() string       { return "[REDACTED]" }
func (lineSecret) GoString() string     { return "[REDACTED]" }

// lineConfig は LINEでの一斉配信の設定です。
type lineConfig struct {
	ChannelID string
	Secret    lineSecret
	SiteURL   string
	SiteName  string
}

// loadLineConfig は、LINE_CHANNEL_ID と LINE_CHANNEL_SECRET_FILE を読みます。両方とも空なら無効（nil）です。
// 片方だけ、値の形が違う、SITE_URL・SITE_NAME が無いときは、起動を止めるエラーを返します。エラーに値を含めません（NEWS-INV-24・25）。
func loadLineConfig(getenv func(string) string) (*lineConfig, error) {
	id, file := strings.TrimSpace(getenv("LINE_CHANNEL_ID")), getenv("LINE_CHANNEL_SECRET_FILE")
	if id == "" && file == "" {
		return nil, nil
	}
	if id == "" || file == "" {
		return nil, errors.New("LINE_CHANNEL_ID and LINE_CHANNEL_SECRET_FILE must be set together")
	}
	if len(id) > lineMaxChannelID || strings.Trim(id, "0123456789") != "" {
		return nil, errors.New("LINE_CHANNEL_ID must be digits")
	}
	content, err := federation.SecretFile(file)
	if err != nil {
		return nil, errors.New("LINE_CHANNEL_SECRET_FILE must be a regular file readable only by its owner")
	}
	secret := strings.TrimSpace(string(content))
	if secret == "" || !printableASCII(secret) {
		return nil, errors.New("LINE_CHANNEL_SECRET_FILE must contain one line with the channel secret")
	}
	siteURL, ok := web.SiteURLFromEnv()
	if !ok || siteURL == "" {
		return nil, errors.New("SITE_URL is required when LINE_CHANNEL_ID is set")
	}
	siteName := web.ConfiguredSiteName()
	if siteName == "" {
		return nil, errors.New("SITE_NAME is required when LINE_CHANNEL_ID is set")
	}
	return &lineConfig{ChannelID: id, Secret: lineSecret(secret), SiteURL: siteURL, SiteName: siteName}, nil
}

// printableASCII は、空白・制御文字・ASCII の外の文字を含まないかを返します。
func printableASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] <= ' ' || s[i] >= 0x7f {
			return false
		}
	}
	return true
}

// lineMessage は、LINE へ送る文面を作ります。サイト名・題名・記事の URL だけで作ります（NEWS-INV-19）。
func lineMessage(cfg *lineConfig, p Post) (string, error) {
	msg := cfg.SiteName + "のお知らせ\n" + p.Title + "\n" + cfg.SiteURL + "/news/" + strconv.FormatInt(p.ID, 10)
	if len(utf16.Encode([]rune(msg))) > lineMaxUTF16 {
		return "", errors.New("news: LINE message too long")
	}
	return msg, nil
}

// lineDigest は、確認の画面で見せた文面と、送るときの文面を照らすためのハッシュです。
func lineDigest(msg string) string {
	sum := sha256.Sum256([]byte(msg))
	return hex.EncodeToString(sum[:])
}

// newRetryKey は、再送の鍵（UUID v4 の16進の形）を作ります。
func newRetryKey() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b[:])
	return fmt.Sprintf("%s-%s-%s-%s-%s", h[0:8], h[8:12], h[12:16], h[16:20], h[20:32]), nil
}

// lineOutcome は、一斉送信の応答の分類です。ログの result にもこの値だけを出します。
type lineOutcome string

const (
	lineSent            lineOutcome = "sent"             // 200: 受け付けた
	lineAlreadyAccepted lineOutcome = "already_accepted" // 409 と x-line-accepted-request-id: 同じ鍵の要求を受け付け済み
	lineRateLimited     lineOutcome = "rate_limited"     // 429: 受け付けなかった（回数か月の通数の上限）
	lineRejected        lineOutcome = "rejected"         // 400・401・403: 受け付けなかった（文面か設定の誤り）
	lineUncertain       lineOutcome = "uncertain"        // それ以外・時間切れ・接続の失敗: 受け付けたかもしれない
)

// accepted は、LINE が受け付けたと分かる分類かを返します。
func (o lineOutcome) accepted() bool { return o == lineSent || o == lineAlreadyAccepted }

// classifyBroadcast は、一斉送信の応答を分けます。表に無い応答は、受け付けたかもしれないとします（NEWS-INV-21）。
func classifyBroadcast(status int, header http.Header) lineOutcome {
	switch {
	case status == http.StatusOK:
		return lineSent
	case status == http.StatusConflict && header.Get("x-line-accepted-request-id") != "":
		return lineAlreadyAccepted
	case status == http.StatusTooManyRequests:
		return lineRateLimited
	case status == http.StatusBadRequest || status == http.StatusUnauthorized || status == http.StatusForbidden:
		return lineRejected
	}
	return lineUncertain
}

// errLineToken は、トークンを発行できなかったことです。原因の文（応答の本文・通信のエラー）は持ちません（NEWS-INV-24）。
var errLineToken = errors.New("news: LINE token could not be issued")

// lineClient は、LINE の2つの URL（トークンの発行と一斉送信）だけに送るクライアントです。
type lineClient struct {
	cfg                    *lineConfig
	tokenURL, broadcastURL string
	http                   *http.Client
}

// newLineClient は、本番の LINE の URL に送るクライアントを作ります。
func newLineClient(cfg *lineConfig) *lineClient {
	return newLineClientAt(cfg, lineTokenURL, lineBroadcastURL, nil)
}

// newLineClientAt は、送り先を指定してクライアントを作ります（試験で偽の送り先に差し替える）。
// federation.Client が、この2つの URL との完全一致・HTTPS だけを通し、転送に従いません（NEWS-INV-24）。
func newLineClientAt(cfg *lineConfig, tokenURL, broadcastURL string, base http.RoundTripper) *lineClient {
	return &lineClient{
		cfg:          cfg,
		tokenURL:     tokenURL,
		broadcastURL: broadcastURL,
		http:         federation.Client([]string{tokenURL, broadcastURL}, lineTimeout, lineResponseLimit, base),
	}
}

// issueToken は、チャネル ID とシークレットで、15分有効のステートレスのトークンを発行します。トークンは保存しません。
func (c *lineClient) issueToken(ctx context.Context) (string, error) {
	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {c.cfg.ChannelID},
		"client_secret": {string(c.cfg.Secret)},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", errLineToken
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := c.http.Do(req) // #nosec G107 G704 -- 送り先は固定の LINE の URL で、federation.Client がその URL との完全一致だけを通す
	if err != nil {
		return "", errLineToken
	}
	defer func() { _ = res.Body.Close() }()
	body, err := io.ReadAll(res.Body)
	if err != nil || res.StatusCode != http.StatusOK {
		return "", errLineToken
	}
	var got struct {
		TokenType   string `json:"token_type"`
		AccessToken string `json:"access_token"`
	}
	if json.Unmarshal(body, &got) != nil || got.TokenType != "Bearer" || got.AccessToken == "" || !printableASCII(got.AccessToken) {
		return "", errLineToken
	}
	return got.AccessToken, nil
}

// broadcast は、文面を友だち全員へ一斉送信し、応答の分類を返します。鍵と文面は、保存してコミットした値を渡します（NEWS-INV-20）。
func (c *lineClient) broadcast(ctx context.Context, token, key, message string) lineOutcome {
	body, err := json.Marshal(map[string]any{
		"messages": []map[string]string{{"type": "text", "text": message}},
	})
	if err != nil {
		return lineUncertain
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.broadcastURL, bytes.NewReader(body))
	if err != nil {
		return lineUncertain
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Line-Retry-Key", key)
	res, err := c.http.Do(req) // #nosec G107 G704 -- 送り先は固定の LINE の URL で、federation.Client がその URL との完全一致だけを通す
	if err != nil {
		// 時間切れ・接続の失敗・転送は、受け付けたかもしれない
		return lineUncertain
	}
	_, _ = io.Copy(io.Discard, res.Body)
	_ = res.Body.Close()
	return classifyBroadcast(res.StatusCode, res.Header)
}
