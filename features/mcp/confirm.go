package mcp

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"
)

const (
	confirmTTL      = 10 * time.Minute
	confirmPurpose  = "mcp-confirm-v1"
	confirmMaxLen   = 2048
	confirmMaxUsed  = 4096
	confirmKeyBytes = 32
)

var errConfirm = errors.New("mcp: invalid confirmation")

// confirmState は、確認を結びつける内容です。クライアントには不透明で、書き換えは署名で検出します（MCP-INV-3）。
type confirmState struct {
	Purpose    string `json:"p"`
	Account    int64  `json:"a"`
	Client     string `json:"c"`
	Generation string `json:"g"`
	Tool       string `json:"t"`
	Args       string `json:"h"`
	Target     string `json:"s"`
	Settings   int64  `json:"f"`
	Expires    int64  `json:"e"`
	Nonce      string `json:"n"`
}

// confirmer は、確認の発行と一回限りの消費を行います。鍵と使用済みの番号はメモリだけに持ち、
// 再起動で全ての確認が無効になります。単一プロセスの前提です。
type confirmer struct {
	key  []byte
	now  func() time.Time
	mu   sync.Mutex
	used map[string]int64
}

func newConfirmer(now func() time.Time) (*confirmer, error) {
	key := make([]byte, confirmKeyBytes)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	return &confirmer{key: key, now: now, used: map[string]int64{}}, nil
}

func (c *confirmer) sign(payload string) []byte {
	mac := hmac.New(sha256.New, c.key)
	mac.Write([]byte(confirmPurpose + "\n" + payload))
	return mac.Sum(nil)
}

// issue は、期限と番号を付けて署名します。
func (c *confirmer) issue(s confirmState) (string, error) {
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	s.Purpose = confirmPurpose
	s.Nonce = hex.EncodeToString(nonce)
	s.Expires = c.now().Add(confirmTTL).Unix()
	raw, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	payload := base64.RawURLEncoding.EncodeToString(raw)
	return payload + "." + base64.RawURLEncoding.EncodeToString(c.sign(payload)), nil
}

// open は、署名・用途・期限を確かめます。ここでは消費しません。
func (c *confirmer) open(token string) (confirmState, error) {
	var s confirmState
	payload, sig, ok := strings.Cut(token, ".")
	if !ok || len(token) > confirmMaxLen {
		return s, errConfirm
	}
	got, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || !hmac.Equal(got, c.sign(payload)) {
		return s, errConfirm
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil || json.Unmarshal(raw, &s) != nil {
		return s, errConfirm
	}
	if s.Purpose != confirmPurpose || s.Expires <= c.now().Unix() || s.Nonce == "" {
		return confirmState{}, errConfirm
	}
	return s, nil
}

// consume は、番号を排他的に使用済みにします。2回目は false です。上限に達したら安全側で false を返します。
func (c *confirmer) consume(s confirmState) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, seen := c.used[s.Nonce]; seen {
		return false
	}
	if len(c.used) >= confirmMaxUsed {
		now := c.now().Unix()
		for k, exp := range c.used {
			if exp <= now {
				delete(c.used, k)
			}
		}
		if len(c.used) >= confirmMaxUsed {
			return false
		}
	}
	c.used[s.Nonce] = s.Expires
	return true
}
