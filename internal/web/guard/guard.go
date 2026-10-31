// Package guard は、CoreのCSRF検査を通らない独立経路（features.IsolatedRoutesFeature）で、
// 状態を変える要求を守る部品です。独立経路のPOSTでは、SameOrigin と CSRF の両方を確認します。
// あわせて、セッションのCookieは SameSite=Strict にします。
package guard

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// uploadTimeout は、認証を通したファイルの送信の、読み書きの期限です。遅い回線でも送り切れる長さにします。
const uploadTimeout = 2 * time.Minute

// maxTokenBytes は、multipart の csrf_token の部品の上限です。
const maxTokenBytes = 256

// ErrUploadForm は、multipart の形が決まりと違うことを示します。
var ErrUploadForm = errors.New("guard: invalid upload form")

// UploadToken は、multipart の先頭の部品から csrf_token を読み、続きを読む Reader と一緒に返します。
// 必ず同一オリジンとセッションを確かめてから呼びます。読み書きの期限をこの要求だけ延ばします。
// ParseMultipartForm は一時ファイルを書き得るため使わず、ファイルを読む前にCSRFを確かめられるよう、
// csrf_token を最初の部品に決めます。
func UploadToken(w http.ResponseWriter, r *http.Request) (*multipart.Reader, string, error) {
	rc := http.NewResponseController(w)
	deadline := time.Now().Add(uploadTimeout)
	_ = rc.SetReadDeadline(deadline)
	_ = rc.SetWriteDeadline(deadline)
	reader, err := r.MultipartReader()
	if err != nil {
		return nil, "", ErrUploadForm
	}
	part, err := reader.NextRawPart()
	if err != nil || part.FormName() != "csrf_token" || part.FileName() != "" {
		return nil, "", ErrUploadForm
	}
	defer part.Close()
	raw, err := io.ReadAll(io.LimitReader(part, maxTokenBytes+1))
	if err != nil || len(raw) > maxTokenBytes {
		return nil, "", ErrUploadForm
	}
	return reader, string(raw), nil
}

// SameOrigin は、要求が同じオリジンから送られたことを確認します。
// Coreが Referrer-Policy: no-referrer を送るため、同一オリジンでも Origin は null になり得ます。
// そのため Sec-Fetch-Site: same-origin を必須とし、Origin が null 以外で届いた場合は origin との一致も確認します。
// Sec-Fetch-Site を送らない古いブラウザは拒否されます。
func SameOrigin(r *http.Request, origin string) bool {
	if r.Header.Get("Sec-Fetch-Site") != "same-origin" {
		return false
	}
	o := r.Header.Get("Origin")
	return o == "" || o == "null" || o == origin
}

// CSRF は、セッションのトークンに結びつけたCSRFトークンを発行・検証します。
// 鍵は起動時に乱数で作るため、再起動すると発行済みのトークンは無効になります（単一プロセスの前提）。
type CSRF struct {
	key []byte
}

// NewCSRF は乱数の鍵でCSRFを作ります。
func NewCSRF() *CSRF {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		panic("guard: generate csrf key: " + err.Error())
	}
	return &CSRF{key: key}
}

// Token は、セッションのトークンに対応するCSRFトークンを返します。フォームの隠し項目やヘッダーで送らせます。
func (c *CSRF) Token(session string) string {
	m := hmac.New(sha256.New, c.key)
	m.Write([]byte(session))
	return hex.EncodeToString(m.Sum(nil))
}

// Valid は、CSRFトークンがセッションに対応するかを、時間差の出ない比較で確認します。
func (c *CSRF) Valid(session, token string) bool {
	return session != "" && token != "" && hmac.Equal([]byte(c.Token(session)), []byte(token))
}

// NormalizeOrigin は scheme://host[:port] の形式だけを受け付け、正規化して返します。
// http は localhost のみ許可します。既定のポートは省略します（ブラウザが送るOriginと揃えるため）。
func NormalizeOrigin(raw string) (origin, host string, err error) {
	if raw == "*" || raw == "null" {
		return "", "", errors.New("wildcard and opaque origins are not allowed")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", "", errors.New("invalid origin")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", "", errors.New("origin must use http or https")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || u.Opaque != "" {
		return "", "", errors.New("origin must not contain user info, path, query or fragment")
	}
	host = strings.ToLower(u.Hostname())
	if host == "" {
		return "", "", errors.New("origin must have a host")
	}
	if u.Scheme == "http" && host != "localhost" {
		return "", "", errors.New("http is allowed only for localhost")
	}
	port := u.Port()
	if (u.Scheme == "https" && port == "443") || (u.Scheme == "http" && port == "80") {
		port = ""
	}
	hostport := host
	if strings.Contains(host, ":") {
		hostport = "[" + host + "]"
	}
	if port != "" {
		hostport += ":" + port
	}
	return u.Scheme + "://" + hostport, host, nil
}
