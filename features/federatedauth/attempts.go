package federatedauth

import (
	"crypto/subtle"
	"errors"
	"strings"
	"sync"
	"time"

	"minimal-web-kit/internal/federation"
)

const (
	attemptTTL      = 5 * time.Minute
	attemptInterval = 10 * time.Second
	maxAttempts     = 1024
)

var errAttemptLimited = errors.New("federatedauth: too many login attempts")

// attempt は進行中のログインです。メモリだけに置き、再起動で失います（PKCEのverifierをDBやCookieに置かないため）。
type attempt struct {
	stateHash string
	nonceHash string
	verifier  string
	next      string
	authURL   string
	started   time.Time
}

// attempts は、試行Cookieのハッシュをキーにした、進行中のログインの一覧です。ブラウザにつき1件で、上限は1024件です。
type attempts struct {
	mu      sync.Mutex
	items   map[string]*attempt
	limiter federation.Limiter
	now     func() time.Time
}

func newAttempts(now func() time.Time) *attempts {
	return &attempts{items: map[string]*attempt{}, now: now}
}

// recent は、同じCookieで10秒以内に始めた試行を返します。二度押しで新しい試行を作らず、同じ認可要求へ進めます。
func (s *attempts) recent(cookie string) (*attempt, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.items[federation.Hash(cookie)]
	if !ok || s.now().Sub(a.started) >= attemptInterval {
		return nil, false
	}
	return a, true
}

// add は新しい試行を登録します。同じCookieの古い試行は置き換えます。
// 満杯なら、新規のログインを止めないよう、最古の試行を捨てます。受付の超過は errAttemptLimited です。
func (s *attempts) add(cookie string, a *attempt) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.limiter.Allow() {
		return errAttemptLimited
	}
	now := s.now()
	var oldestKey string
	var oldest time.Time
	for key, item := range s.items {
		if now.Sub(item.started) >= attemptTTL {
			delete(s.items, key)
		} else if oldestKey == "" || item.started.Before(oldest) {
			oldestKey, oldest = key, item.started
		}
	}
	key := federation.Hash(cookie)
	if _, replacing := s.items[key]; !replacing && len(s.items) >= maxAttempts {
		delete(s.items, oldestKey)
	}
	a.started = now
	s.items[key] = a
	return nil
}

// take は、Cookieとstateが合う試行を取り出します。どちらの結果でも、Cookieの試行は一回限りで消えます。
func (s *attempts) take(cookie, state string) (*attempt, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := federation.Hash(cookie)
	a, ok := s.items[key]
	if !ok {
		return nil, false
	}
	delete(s.items, key)
	if s.now().Sub(a.started) >= attemptTTL || subtle.ConstantTimeCompare([]byte(a.stateHash), []byte(federation.Hash(state))) != 1 {
		return nil, false
	}
	return a, true
}

// validNext は、ログイン後の戻り先として認める管理画面のパスかを確かめます。符号化による迂回を避けるため、% も認めません。
func validNext(next string) bool {
	if next != "/admin" && !strings.HasPrefix(next, "/admin/") && !strings.HasPrefix(next, "/admin?") {
		return false
	}
	if strings.HasPrefix(next, "/admin/login") || strings.HasPrefix(next, "/admin/sso") {
		return false
	}
	if strings.Contains(next, "//") || strings.Contains(next, "..") || strings.ContainsAny(next, "\\%#") {
		return false
	}
	for _, r := range next {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}
