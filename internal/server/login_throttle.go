package server

import (
	"net"
	"strings"
	"sync"
	"time"
)

const (
	loginThrottleMaxFailures = 5
	loginThrottleWindow      = 15 * time.Minute
	loginThrottleBlock       = 5 * time.Minute
)

type loginThrottle struct {
	mu      sync.Mutex
	entries map[string]*loginThrottleEntry
}

type loginThrottleEntry struct {
	failures     int
	windowStart  time.Time
	blockedUntil time.Time
}

func newLoginThrottle() *loginThrottle {
	return &loginThrottle{entries: make(map[string]*loginThrottleEntry)}
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
	if entry == nil || now.Sub(entry.windowStart) > loginThrottleWindow {
		entry = &loginThrottleEntry{windowStart: now}
		t.entries[key] = entry
	}
	entry.failures++
	if entry.failures >= loginThrottleMaxFailures {
		entry.blockedUntil = now.Add(loginThrottleBlock)
	}
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
