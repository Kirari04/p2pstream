package server

import (
	"sync"
	"time"
)

type agentUpdateRateBucket struct {
	tokens   float64
	updated  time.Time
	lastSeen time.Time
}

// agentUpdateRateLimiter bounds serialized abuse as well as concurrency. Its
// key set is capped so attacker-controlled source addresses cannot grow memory
// without bound.
type agentUpdateRateLimiter struct {
	mu         sync.Mutex
	buckets    map[string]*agentUpdateRateBucket
	maxEntries int
	burst      float64
	perSecond  float64
	idleTTL    time.Duration
	nextPrune  time.Time
}

func newAgentUpdateRateLimiter(limit int, window time.Duration, burst, maxEntries int) *agentUpdateRateLimiter {
	if limit < 1 || window <= 0 || burst < 1 || maxEntries < 1 {
		return nil
	}
	return &agentUpdateRateLimiter{
		buckets: make(map[string]*agentUpdateRateBucket), maxEntries: maxEntries,
		burst: float64(burst), perSecond: float64(limit) / window.Seconds(), idleTTL: window,
	}
}

func (l *agentUpdateRateLimiter) allow(key string, now time.Time) bool {
	if l == nil {
		return true
	}
	if key == "" {
		key = "unknown"
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	bucket := l.buckets[key]
	if bucket == nil {
		if len(l.buckets) >= l.maxEntries {
			// Unknown attacker-controlled keys must not turn a bounded map into an
			// O(n) scan on every request. Prune at most once per refill window, then
			// fail closed while the bounded active-key set remains full.
			if l.nextPrune.IsZero() || !now.Before(l.nextPrune) {
				cutoff := now.Add(-l.idleTTL)
				for candidate, entry := range l.buckets {
					if entry.lastSeen.Before(cutoff) {
						delete(l.buckets, candidate)
					}
				}
				l.nextPrune = now.Add(l.idleTTL)
			}
			if len(l.buckets) >= l.maxEntries {
				return false
			}
		}
		bucket = &agentUpdateRateBucket{tokens: l.burst, updated: now}
		l.buckets[key] = bucket
	}
	if elapsed := now.Sub(bucket.updated).Seconds(); elapsed > 0 {
		bucket.tokens += elapsed * l.perSecond
		if bucket.tokens > l.burst {
			bucket.tokens = l.burst
		}
		bucket.updated = now
	}
	bucket.lastSeen = now
	if bucket.tokens < 1 {
		return false
	}
	bucket.tokens--
	return true
}
