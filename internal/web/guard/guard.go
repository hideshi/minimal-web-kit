// Package guard は、CoreのCSRF検査を通らない独立経路（features.IsolatedRoutesFeature）で、
// 状態を変える要求を守る部品です。独立経路のPOSTでは、SameOrigin と CSRF の両方を確認します。
// あわせて、セッションのCookieは SameSite=Strict にします。
package guard

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
)

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
