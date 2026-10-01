package auth

import (
	"sync"
	"time"
)

// Limiter is a sliding-window limiter keyed by client address, for login,
// TOTP, re-auth, and setup attempts. It is per process; the per-account
// lockout in the database covers attackers spreading over addresses.
type Limiter struct {
	max    int
	window time.Duration
	now    func() time.Time

	mu   sync.Mutex
	hits map[string][]time.Time
	last time.Time // last sweep
}

// NewLimiter allows max events per key per window.
func NewLimiter(limit int, window time.Duration) *Limiter {
	return &Limiter{max: limit, window: window, now: time.Now, hits: map[string][]time.Time{}}
}

// Allow records an attempt for key and reports whether it is within limits.
func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	cutoff := now.Add(-l.window)
	if now.Sub(l.last) > l.window {
		for k, ts := range l.hits {
			if len(ts) == 0 || ts[len(ts)-1].Before(cutoff) {
				delete(l.hits, k)
			}
		}
		l.last = now
	}
	ts := l.hits[key]
	i := 0
	for i < len(ts) && ts[i].Before(cutoff) {
		i++
	}
	ts = ts[i:]
	if len(ts) >= l.max {
		l.hits[key] = ts
		return false
	}
	l.hits[key] = append(ts, now)
	return true
}
