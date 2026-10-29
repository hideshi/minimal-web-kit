package contact

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"sync"
	"time"
)

const (
	tokenMinAge = 3 * time.Second // これより速い送信はボットとみなす
	tokenMaxAge = time.Hour       // これより古いフォームの値は期限切れ
	nonceSize   = 16
)

// verdict はフォームの値の判定です。
type verdict string

const (
	tokenOK       verdict = ""
	tokenBad      verdict = "bad_token"
	tokenExpired  verdict = "expired"
	tokenTooFast  verdict = "too_fast"
	tokenReplayed verdict = "replayed"
)

// tokens はフォームの値（発行時刻＋使い捨ての乱数＋HMAC-SHA256の署名）を発行・検証します。
// 鍵と使用済みの記録はメモリにだけ置き、保存しません。再起動すると、発行済みの値は使えなくなります（1台で動かす前提）。
type tokens struct {
	key  []byte
	mu   sync.Mutex
	used map[string]time.Time // 使用済みの乱数と、使った時刻
}

func newTokens() *tokens {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		panic(err)
	}
	return &tokens{key: key, used: map[string]time.Time{}}
}

func (t *tokens) sign(payload []byte) []byte {
	mac := hmac.New(sha256.New, t.key)
	mac.Write(payload)
	return mac.Sum(nil)
}

// issue は、now に発行したフォームの値を返します。
func (t *tokens) issue(now time.Time) string {
	payload := make([]byte, 8+nonceSize)
	binary.BigEndian.PutUint64(payload, uint64(now.Unix()))
	if _, err := rand.Read(payload[8:]); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(append(payload, t.sign(payload)...))
}

// check は値の署名と時刻を確かめ、使い捨ての乱数を返します。使用済みかどうかは claim で確かめます。
func (t *tokens) check(value string, now time.Time) (nonce string, v verdict) {
	raw, err := base64.RawURLEncoding.Strict().DecodeString(value)
	if err != nil || len(raw) != 8+nonceSize+sha256.Size {
		return "", tokenBad
	}
	payload, sum := raw[:8+nonceSize], raw[8+nonceSize:]
	if !hmac.Equal(sum, t.sign(payload)) {
		return "", tokenBad
	}
	issued := time.Unix(int64(binary.BigEndian.Uint64(payload)), 0)
	age := now.Sub(issued)
	switch {
	case age > tokenMaxAge || age < -time.Minute:
		return "", tokenExpired
	case age < tokenMinAge:
		return "", tokenTooFast
	}
	return string(payload[8:]), tokenOK
}

// claim は乱数を使用済みにします。すでに使われていれば false です。保存に失敗したら release で戻します。
// 記録は保存に成功した件数（全体の受付上限）以下に収まり、1時間で消えます（CONTACT-INV-4）。
func (t *tokens) claim(nonce string, now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	for n, at := range t.used {
		if now.Sub(at) > tokenMaxAge+time.Minute {
			delete(t.used, n)
		}
	}
	if _, ok := t.used[nonce]; ok {
		return false
	}
	t.used[nonce] = now
	return true
}

func (t *tokens) release(nonce string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.used, nonce)
}
