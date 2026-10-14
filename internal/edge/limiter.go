package edge

import (
	"sync"
	"time"
)

// limiter is a set of token buckets, one per key, refilling at a rate per
// minute with a burst of a tenth of a minute's worth (at least 10).
type limiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	swept   time.Time
	now     func() time.Time
}

type bucket struct {
	tokens float64
	at     time.Time
}

func newLimiter() *limiter {
	return &limiter{buckets: map[string]*bucket{}, now: time.Now}
}

func (l *limiter) allow(key string, perMinute int) bool {
	if perMinute <= 0 {
		return true
	}
	now := l.now()
	burst := max(float64(perMinute)/6, 10)
	rate := float64(perMinute) / 60 // per second
	l.mu.Lock()
	defer l.mu.Unlock()
	if now.Sub(l.swept) > time.Minute {
		// Full buckets carry no state: forget them.
		for k, b := range l.buckets {
			if now.Sub(b.at) > 2*time.Minute {
				delete(l.buckets, k)
			}
		}
		l.swept = now
	}
	b := l.buckets[key]
	if b == nil {
		b = &bucket{tokens: burst, at: now}
		l.buckets[key] = b
	}
	b.tokens = min(burst, b.tokens+now.Sub(b.at).Seconds()*rate)
	b.at = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
