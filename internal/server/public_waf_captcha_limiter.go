package server

import (
	"fmt"
	"sync"
	"time"
)

type publicWafCaptchaVerifyLimiter struct {
	mu        sync.Mutex
	keys      map[string]*publicWafFixedWindowBucket
	lastPrune time.Time
}

type publicWafReservedEndpointLimiter struct {
	mu        sync.Mutex
	keys      map[string]*publicWafFixedWindowBucket
	lastPrune time.Time
}

type publicWafFixedWindowBucket struct {
	windowStart int64
	count       int64
	lastSeenAt  time.Time
}

func newPublicWafCaptchaVerifyLimiter() *publicWafCaptchaVerifyLimiter {
	return &publicWafCaptchaVerifyLimiter{keys: make(map[string]*publicWafFixedWindowBucket)}
}

func newPublicWafReservedEndpointLimiter() *publicWafReservedEndpointLimiter {
	return &publicWafReservedEndpointLimiter{keys: make(map[string]*publicWafFixedWindowBucket)}
}

func (l *publicWafCaptchaVerifyLimiter) allow(listenerID int64, ruleID int64, remoteIP string, now time.Time) (time.Duration, bool) {
	if l == nil {
		return 0, true
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	l.pruneLocked(now)
	ipKey := fmt.Sprintf("ip:%d:%d:%s", listenerID, ruleID, remoteIP)
	ruleKey := fmt.Sprintf("rule:%d:%d", listenerID, ruleID)
	ipBucket := l.bucketLocked(ipKey, now)
	ruleBucket := l.bucketLocked(ruleKey, now)

	ipRetryAfter, ipAllowed := ipBucket.canAllow(publicWafCaptchaVerifyIPLimit, publicWafCaptchaVerifyWindow, now)
	ruleRetryAfter, ruleAllowed := ruleBucket.canAllow(publicWafCaptchaVerifyRuleLimit, publicWafCaptchaVerifyWindow, now)
	if !ipAllowed || !ruleAllowed {
		return maxDuration(ipRetryAfter, ruleRetryAfter), false
	}

	ipBucket.count++
	ipBucket.lastSeenAt = now
	ruleBucket.count++
	ruleBucket.lastSeenAt = now
	return 0, true
}

func (l *publicWafCaptchaVerifyLimiter) bucketLocked(key string, now time.Time) *publicWafFixedWindowBucket {
	bucket := l.keys[key]
	if bucket == nil {
		if len(l.keys) >= publicWafCaptchaVerifyMaxKeys {
			l.evictOldestLocked()
		}
		bucket = &publicWafFixedWindowBucket{}
		l.keys[key] = bucket
	}
	bucket.rollWindow(publicWafCaptchaVerifyWindow, now)
	bucket.lastSeenAt = now
	return bucket
}

func (b *publicWafFixedWindowBucket) canAllow(limit int64, window time.Duration, now time.Time) (time.Duration, bool) {
	b.rollWindow(window, now)
	if b.count < limit {
		return 0, true
	}
	windowEnd := b.windowStart + window.Milliseconds()
	retryAfter := time.Duration(maxInt64(1, windowEnd-now.UnixMilli())) * time.Millisecond
	return retryAfter, false
}

func (b *publicWafFixedWindowBucket) rollWindow(window time.Duration, now time.Time) {
	windowMs := window.Milliseconds()
	windowStart := (now.UnixMilli() / windowMs) * windowMs
	if b.windowStart != windowStart {
		b.windowStart = windowStart
		b.count = 0
	}
}

func (l *publicWafCaptchaVerifyLimiter) pruneLocked(now time.Time) {
	if !l.lastPrune.IsZero() && now.Sub(l.lastPrune) < publicWafCaptchaVerifyPruneInterval {
		return
	}
	l.lastPrune = now
	for key, bucket := range l.keys {
		if bucket == nil || bucket.lastSeenAt.IsZero() || now.Sub(bucket.lastSeenAt) > publicWafCaptchaVerifyIdleTTL {
			delete(l.keys, key)
		}
	}
}

func (l *publicWafCaptchaVerifyLimiter) evictOldestLocked() {
	var oldestKey string
	var oldestTime time.Time
	for key, bucket := range l.keys {
		if bucket == nil {
			delete(l.keys, key)
			return
		}
		if oldestKey == "" || bucket.lastSeenAt.Before(oldestTime) {
			oldestKey = key
			oldestTime = bucket.lastSeenAt
		}
	}
	if oldestKey != "" {
		delete(l.keys, oldestKey)
	}
}

func (l *publicWafReservedEndpointLimiter) allow(listenerID int64, path string, remoteIP string, now time.Time) (time.Duration, bool) {
	if l == nil {
		return 0, true
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	l.pruneLocked(now)
	ipKey := fmt.Sprintf("reserved:ip:%d:%s:%s", listenerID, path, remoteIP)
	ipBucket := l.bucketLocked(ipKey, now)

	ipRetryAfter, ipAllowed := ipBucket.canAllow(publicWafReservedEndpointIPLimit, publicWafReservedEndpointWindow, now)
	if !ipAllowed {
		return ipRetryAfter, false
	}
	var pathBucket *publicWafFixedWindowBucket
	if path != publicWafWaitingRoomStatusPath {
		pathKey := fmt.Sprintf("reserved:path:%d:%s", listenerID, path)
		pathBucket = l.bucketLocked(pathKey, now)
		pathRetryAfter, pathAllowed := pathBucket.canAllow(publicWafReservedEndpointPathLimit, publicWafReservedEndpointWindow, now)
		if !pathAllowed {
			return pathRetryAfter, false
		}
	}

	ipBucket.count++
	ipBucket.lastSeenAt = now
	if pathBucket != nil {
		pathBucket.count++
		pathBucket.lastSeenAt = now
	}
	return 0, true
}

func (l *publicWafReservedEndpointLimiter) bucketLocked(key string, now time.Time) *publicWafFixedWindowBucket {
	bucket := l.keys[key]
	if bucket == nil {
		if len(l.keys) >= publicWafReservedEndpointMaxKeys {
			l.evictOldestLocked()
		}
		bucket = &publicWafFixedWindowBucket{}
		l.keys[key] = bucket
	}
	bucket.rollWindow(publicWafReservedEndpointWindow, now)
	bucket.lastSeenAt = now
	return bucket
}

func (l *publicWafReservedEndpointLimiter) pruneLocked(now time.Time) {
	if !l.lastPrune.IsZero() && now.Sub(l.lastPrune) < publicWafReservedEndpointPruneInterval {
		return
	}
	l.lastPrune = now
	for key, bucket := range l.keys {
		if bucket == nil || bucket.lastSeenAt.IsZero() || now.Sub(bucket.lastSeenAt) > publicWafReservedEndpointIdleTTL {
			delete(l.keys, key)
		}
	}
}

func (l *publicWafReservedEndpointLimiter) evictOldestLocked() {
	var oldestKey string
	var oldestTime time.Time
	for key, bucket := range l.keys {
		if bucket == nil {
			delete(l.keys, key)
			return
		}
		if oldestKey == "" || bucket.lastSeenAt.Before(oldestTime) {
			oldestKey = key
			oldestTime = bucket.lastSeenAt
		}
	}
	if oldestKey != "" {
		delete(l.keys, oldestKey)
	}
}

func maxDuration(a time.Duration, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}
