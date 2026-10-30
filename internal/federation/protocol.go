// Package federation は、共通認証の提供側（features/auth/oidcop）と利用側（features/federatedauth）が
// 共有する照会の契約と、通信の守りの部品です。外部ライブラリに依存しません。
package federation

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ClaimName は、IDトークンで3つの世代を運ぶ追加のclaimの名前です。
const ClaimName = "mwk_auth"

// Epochs は、ログイン時に固定する3つの失効世代です。後の照会の結果で更新しません。
type Epochs struct {
	Account string `json:"account_epoch"`
	Client  string `json:"client_epoch"`
	Grant   string `json:"grant_epoch"`
}

// Valid は、3つとも正の64ビット整数の10進文字列（先頭ゼロ・符号なし）かを確かめます。
func (e Epochs) Valid() bool {
	for _, s := range []string{e.Account, e.Client, e.Grant} {
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil || n <= 0 || strconv.FormatInt(n, 10) != s {
			return false
		}
	}
	return true
}

// LogValue は、世代をログに出さないようにします。
func (Epochs) LogValue() slog.Value { return slog.StringValue("[redacted]") }

// CheckRequest は、利用側が提供側に送る照会です。clientはBasic認証で確定します。
type CheckRequest struct {
	Subject string `json:"sub"`
	Epochs
}

// CheckResponse は照会の応答です。無効のときは Active だけを返します。
type CheckResponse struct {
	Active   bool    `json:"active"`
	Issuer   string  `json:"iss,omitempty"`
	Subject  string  `json:"sub,omitempty"`
	ClientID string  `json:"client_id,omitempty"`
	Auth     *Epochs `json:"mwk_auth,omitempty"`
}

// Token は256ビットの乱数をURLで安全な文字列にして返します。
func Token() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("federation: random source unavailable")
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// Hash はトークンのSHA-256を16進で返します。
func Hash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// Origin は、パス等を持たないHTTPSのオリジンかを確かめ、そのまま返します。
func Origin(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("federation: invalid HTTPS origin")
	}
	return raw, nil
}

// SecretFile は、所有者だけが読める通常のファイルを読みます。エラーにパスや内容を含めません。
func SecretFile(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() > 64<<10 {
		return nil, errors.New("federation: private file required")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.New("federation: private file unreadable")
	}
	return b, nil
}

// Decode は、どの深さでも重複したキーを拒否し、未知の項目のない1つのJSONだけを読みます。
// 大きさと深さに上限があり、各トークンを一度だけ見ます。
func Decode(data []byte, target any) error {
	if len(data) > 64<<10 {
		return errors.New("federation: JSON too large")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	if err := rejectDuplicates(d, 0); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return errors.New("federation: trailing JSON")
	}
	d = json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	return d.Decode(target)
}

func rejectDuplicates(d *json.Decoder, depth int) error {
	if depth > 8 {
		return errors.New("federation: JSON too deep")
	}
	v, err := d.Token()
	if err != nil {
		return err
	}
	switch v {
	case json.Delim('{'):
		seen := map[string]bool{}
		for d.More() {
			k, err := d.Token()
			if err != nil {
				return err
			}
			key, ok := k.(string)
			if !ok || seen[key] {
				return errors.New("federation: duplicate JSON key")
			}
			seen[key] = true
			if err := rejectDuplicates(d, depth+1); err != nil {
				return err
			}
		}
		_, err = d.Token()
		return err
	case json.Delim('['):
		for d.More() {
			if err := rejectDuplicates(d, depth+1); err != nil {
				return err
			}
		}
		_, err = d.Token()
		return err
	}
	return nil
}

// JSON はキャッシュさせないJSONの応答を書きます。
func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store, no-transform")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// Client は、決められたHTTPSのURLにだけ接続し、転送を追わず、応答の本文を limit までに制限するクライアントを返します。
func Client(allowed []string, timeout time.Duration, limit int64, base http.RoundTripper) *http.Client {
	if base == nil {
		base = http.DefaultTransport
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: pinned{allowed: allowed, limit: limit, base: base},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("federation: redirect rejected")
		},
	}
}

type pinned struct {
	allowed []string
	limit   int64
	base    http.RoundTripper
}

func (p pinned) RoundTrip(r *http.Request) (*http.Response, error) {
	for _, u := range p.allowed {
		if r.URL.String() == u && r.URL.Scheme == "https" {
			res, err := p.base.RoundTrip(r)
			if err != nil {
				return nil, err
			}
			res.Body = &limitedBody{&io.LimitedReader{R: res.Body, N: p.limit + 1}, res.Body}
			return res, nil
		}
	}
	return nil, errors.New("federation: endpoint rejected")
}

// limitedBody は、上限を超えた本文を切り詰めて渡さず、読み取りエラーにします。
type limitedBody struct {
	reader *io.LimitedReader
	closer io.Closer
}

func (b *limitedBody) Close() error { return b.closer.Close() }

func (b *limitedBody) Read(p []byte) (int, error) {
	n, err := b.reader.Read(p)
	if b.reader.N <= 0 {
		return 0, errors.New("federation: response too large")
	}
	return n, err
}

// Body は本文を n バイトまで読みます。超えたらエラーです。
func Body(r *http.Request, n int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r.Body, n+1))
	if err != nil || int64(len(b)) > n {
		return nil, errors.New("federation: body rejected")
	}
	return b, nil
}

// Form は、クエリのないフォームの本文で、各項目が1つだけのときに true を返します。
func Form(r *http.Request) bool {
	if r.URL.RawQuery != "" || strings.Split(r.Header.Get("Content-Type"), ";")[0] != "application/x-www-form-urlencoded" {
		return false
	}
	if err := r.ParseForm(); err != nil {
		return false
	}
	for _, v := range r.PostForm {
		if len(v) != 1 {
			return false
		}
	}
	return true
}

// Limiter は、新しい手続きの受付を毎秒2件・バースト16件に制限します。
type Limiter struct {
	mu     sync.Mutex
	at     time.Time
	tokens float64
}

// Allow は、受け付けてよければ true を返します。
func (l *Limiter) Allow() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if l.at.IsZero() {
		l.tokens = 16
	} else {
		l.tokens = min(16, l.tokens+now.Sub(l.at).Seconds()*2)
	}
	l.at = now
	if l.tokens < 1 {
		return false
	}
	l.tokens--
	return true
}
