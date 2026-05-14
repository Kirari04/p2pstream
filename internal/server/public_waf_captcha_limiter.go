package server

import (
	"fmt"
	"sync"
	"time"
)

type publicWafCaptchaVerifyLimiter struct {
	mu        sync.Mutex
	keys      map[string]*publicWafCaptchaVerifyBucket
	lastPrune time.Time
}

type publicWafCaptchaVerifyBucket struct {
	windowStart int64
	count       int64
	lastSeenAt  time.Time
}

func newPublicWafCaptchaVerifyLimiter() *publicWafCaptchaVerifyLimiter {
	return &publicWafCaptchaVerifyLimiter{keys: make(map[string]*publicWafCaptchaVerifyBucket)}
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

	ipRetryAfter, ipAllowed := ipBucket.canAllow(publicWafCaptchaVerifyIPLimit, now)
	ruleRetryAfter, ruleAllowed := ruleBucket.canAllow(publicWafCaptchaVerifyRuleLimit, now)
	if !ipAllowed || !ruleAllowed {
		return maxDuration(ipRetryAfter, ruleRetryAfter), false
	}

	ipBucket.count++
	ipBucket.lastSeenAt = now
	ruleBucket.count++
	ruleBucket.lastSeenAt = now
	return 0, true
}

func (l *publicWafCaptchaVerifyLimiter) bucketLocked(key string, now time.Time) *publicWafCaptchaVerifyBucket {
	bucket := l.keys[key]
	if bucket == nil {
		if len(l.keys) >= publicWafCaptchaVerifyMaxKeys {
			l.evictOldestLocked()
		}
		bucket = &publicWafCaptchaVerifyBucket{}
		l.keys[key] = bucket
	}
	bucket.rollWindow(now)
	bucket.lastSeenAt = now
	return bucket
}

func (b *publicWafCaptchaVerifyBucket) canAllow(limit int64, now time.Time) (time.Duration, bool) {
	b.rollWindow(now)
	if b.count < limit {
		return 0, true
	}
	windowEnd := b.windowStart + publicWafCaptchaVerifyWindow.Milliseconds()
	retryAfter := time.Duration(maxInt64(1, windowEnd-now.UnixMilli())) * time.Millisecond
	return retryAfter, false
}

func (b *publicWafCaptchaVerifyBucket) rollWindow(now time.Time) {
	windowMs := publicWafCaptchaVerifyWindow.Milliseconds()
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

func maxDuration(a time.Duration, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}
