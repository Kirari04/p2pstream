package server

import (
	"net"
	"strings"
	"sync"
	"time"
)

const (
	loginThrottleMaxFailures    = 5
	loginThrottleWindow         = 15 * time.Minute
	loginThrottleBlock          = 5 * time.Minute
	defaultLoginThrottleMaxKeys = 50000
)

type loginThrottle struct {
	mu         sync.Mutex
	entries    map[string]*loginThrottleEntry
	maxEntries int
}

type loginThrottleEntry struct {
	failures     int
	windowStart  time.Time
	blockedUntil time.Time
}

func newLoginThrottle(maxEntries int) *loginThrottle {
	if maxEntries <= 0 {
		maxEntries = defaultLoginThrottleMaxKeys
	}
	return &loginThrottle{entries: make(map[string]*loginThrottleEntry), maxEntries: maxEntries}
}

func (t *loginThrottle) retryAfter(key string, now time.Time) time.Duration {
	if t == nil || key == "" {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	entry := t.entries[key]
	if entry == nil {
		return 0
	}
	if !entry.blockedUntil.IsZero() && now.Before(entry.blockedUntil) {
		return entry.blockedUntil.Sub(now)
	}
	if now.Sub(entry.windowStart) > loginThrottleWindow {
		delete(t.entries, key)
	}
	return 0
}

func (t *loginThrottle) recordFailure(key string, now time.Time) {
	if t == nil || key == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	entry := t.entries[key]
	if entry == nil || (!loginThrottleEntryBlocked(entry, now) && loginThrottleEntryExpired(entry, now)) {
		t.pruneLocked(now)
		if len(t.entries) >= t.maxEntries {
			if !t.evictOldestUnlockedEntryLocked(now) {
				return
			}
		}
		entry = &loginThrottleEntry{windowStart: now}
		t.entries[key] = entry
	}
	entry.failures++
	if entry.failures >= loginThrottleMaxFailures {
		entry.blockedUntil = now.Add(loginThrottleBlock)
	}
}

func (t *loginThrottle) pruneLocked(now time.Time) {
	for key, entry := range t.entries {
		if entry == nil || (!loginThrottleEntryBlocked(entry, now) && loginThrottleEntryExpired(entry, now)) {
			delete(t.entries, key)
		}
	}
}

func (t *loginThrottle) evictOldestUnlockedEntryLocked(now time.Time) bool {
	var oldestKey string
	var oldestStart time.Time
	for key, entry := range t.entries {
		// recordFailure prunes before eviction; keep these checks defensive for callers that do not.
		if entry == nil {
			delete(t.entries, key)
			return true
		}
		if !loginThrottleEntryBlocked(entry, now) && loginThrottleEntryExpired(entry, now) {
			delete(t.entries, key)
			return true
		}
		if loginThrottleEntryBlocked(entry, now) {
			continue
		}
		if oldestKey == "" || entry.windowStart.Before(oldestStart) {
			oldestKey = key
			oldestStart = entry.windowStart
		}
	}
	if oldestKey != "" {
		delete(t.entries, oldestKey)
		return true
	}
	return false
}

func loginThrottleEntryBlocked(entry *loginThrottleEntry, now time.Time) bool {
	return entry != nil && !entry.blockedUntil.IsZero() && now.Before(entry.blockedUntil)
}

func loginThrottleEntryExpired(entry *loginThrottleEntry, now time.Time) bool {
	return entry == nil || now.Sub(entry.windowStart) > loginThrottleWindow
}

func (t *loginThrottle) recordSuccess(key string) {
	if t == nil || key == "" {
		return
	}
	t.mu.Lock()
	delete(t.entries, key)
	t.mu.Unlock()
}

func loginThrottleKey(peerAddr string, username string) string {
	host := strings.TrimSpace(peerAddr)
	if splitHost, _, err := net.SplitHostPort(host); err == nil {
		host = splitHost
	}
	if host == "" {
		host = "unknown"
	}
	return host + "\x00" + strings.TrimSpace(strings.ToLower(username))
}
