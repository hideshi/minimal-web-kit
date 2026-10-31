package mcp

import (
	"sync"
	"time"
)

const (
	maxConcurrent  = 16
	limitPerSecond = 5
	limitBurst     = 30
	limitMaxKeys   = 1024
)

type bucket struct {
	tokens float64
	at     time.Time
}

// limiter は、アカウントとクライアントの組ごとの回数を制限します。メモリだけに持ち、件数に上限があります。
type limiter struct {
	mu   sync.Mutex
	now  func() time.Time
	keys map[string]*bucket
}

func newLimiter(now func() time.Time) *limiter {
	return &limiter{now: now, keys: map[string]*bucket{}}
}

func (l *limiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	b, ok := l.keys[key]
	if !ok {
		if len(l.keys) >= limitMaxKeys {
			for k, v := range l.keys {
				if now.Sub(v.at) > time.Minute {
					delete(l.keys, k)
				}
			}
			if len(l.keys) >= limitMaxKeys {
				return false
			}
		}
		b = &bucket{tokens: limitBurst, at: now}
		l.keys[key] = b
	}
	b.tokens = min(limitBurst, b.tokens+now.Sub(b.at).Seconds()*limitPerSecond)
	b.at = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
