package server

import (
	"net"
	"strings"
	"sync"
	"time"
)

const (
	loginThrottleMaxFailures       = 5
	loginThrottleClientMaxFailures = 25
	loginThrottleWindow            = 15 * time.Minute
	loginThrottleBlock             = 5 * time.Minute
	defaultLoginThrottleMaxKeys    = 50000
)

// newLoginThrottleBuckets keeps username and client address accounting
// independent. Otherwise a single small bounded map lets each new username
// evict the address-wide defence before it reaches its higher failure limit.
func newLoginThrottleBuckets(maxEntries int) (username, client *loginThrottle) {
	if maxEntries <= 0 {
		maxEntries = defaultLoginThrottleMaxKeys
	}
	perBucket := max(1, maxEntries/2)
	return newLoginThrottle(perBucket), newLoginThrottle(perBucket)
}

type loginThrottle struct {
	mu         sync.Mutex
	entries    map[string]*loginThrottleEntry
	maxEntries int
}

type loginThrottleEntry struct {
	failures     int
	inFlight     int
	windowStart  time.Time
	blockedUntil time.Time
}

type loginThrottleReservation struct {
	usernameThrottle *loginThrottle
	clientThrottle   *loginThrottle
	usernameKey      string
	clientKey        string
	settled          bool
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

func reserveLoginThrottleAttempt(
	usernameThrottle *loginThrottle,
	clientThrottle *loginThrottle,
	usernameKey string,
	clientKey string,
	now time.Time,
) (*loginThrottleReservation, bool) {
	if !usernameThrottle.tryReserveWithLimit(usernameKey, now, loginThrottleMaxFailures) {
		return nil, false
	}
	if usernameThrottle != nil && clientThrottle == usernameThrottle {
		return &loginThrottleReservation{
			usernameThrottle: usernameThrottle,
			usernameKey:      usernameKey,
		}, true
	}
	if !clientThrottle.tryReserveWithLimit(clientKey, now, loginThrottleClientMaxFailures) {
		usernameThrottle.releaseReservation(usernameKey)
		return nil, false
	}
	return &loginThrottleReservation{
		usernameThrottle: usernameThrottle,
		clientThrottle:   clientThrottle,
		usernameKey:      usernameKey,
		clientKey:        clientKey,
	}, true
}

func (t *loginThrottle) tryReserveWithLimit(key string, now time.Time, maxFailures int) bool {
	if t == nil || key == "" {
		return true
	}
	if maxFailures <= 0 {
		maxFailures = loginThrottleMaxFailures
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	entry := t.entries[key]
	if loginThrottleEntryBlocked(entry, now) {
		return false
	}
	if entry != nil && !entry.blockedUntil.IsZero() {
		entry.failures = 0
		entry.blockedUntil = time.Time{}
		entry.windowStart = now
	}
	if entry != nil && entry.inFlight == 0 && loginThrottleEntryExpired(entry, now) {
		delete(t.entries, key)
		entry = nil
	}
	if entry == nil {
		t.pruneLocked(now)
		if len(t.entries) >= t.maxEntries && !t.evictOldestUnlockedEntryLocked(now) {
			return false
		}
		entry = &loginThrottleEntry{windowStart: now}
		t.entries[key] = entry
	}
	if entry.failures+entry.inFlight >= maxFailures {
		return false
	}
	entry.inFlight++
	return true
}

func (t *loginThrottle) releaseReservation(key string) {
	if t == nil || key == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	entry := t.entries[key]
	if entry == nil {
		return
	}
	if entry.inFlight > 0 {
		entry.inFlight--
	}
	if entry.inFlight == 0 && entry.failures == 0 && entry.blockedUntil.IsZero() {
		delete(t.entries, key)
	}
}

func (t *loginThrottle) recordReservedFailureWithLimit(key string, now time.Time, maxFailures int) {
	if t == nil || key == "" {
		return
	}
	if maxFailures <= 0 {
		maxFailures = loginThrottleMaxFailures
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	entry := t.entries[key]
	if entry == nil {
		t.pruneLocked(now)
		if len(t.entries) >= t.maxEntries && !t.evictOldestUnlockedEntryLocked(now) {
			return
		}
		entry = &loginThrottleEntry{windowStart: now}
		t.entries[key] = entry
	}
	if entry.inFlight > 0 {
		entry.inFlight--
	}
	entry.failures++
	if entry.failures >= maxFailures {
		entry.blockedUntil = now.Add(loginThrottleBlock)
	}
}

func (r *loginThrottleReservation) release() {
	if r == nil || r.settled {
		return
	}
	r.settled = true
	r.usernameThrottle.releaseReservation(r.usernameKey)
	r.clientThrottle.releaseReservation(r.clientKey)
}

func (r *loginThrottleReservation) recordFailure(now time.Time) {
	if r == nil || r.settled {
		return
	}
	r.settled = true
	r.usernameThrottle.recordReservedFailureWithLimit(r.usernameKey, now, loginThrottleMaxFailures)
	r.clientThrottle.recordReservedFailureWithLimit(r.clientKey, now, loginThrottleClientMaxFailures)
}

func (r *loginThrottleReservation) recordSuccess() {
	if r == nil || r.settled {
		return
	}
	r.settled = true
	r.usernameThrottle.recordSuccess(r.usernameKey)
	r.clientThrottle.recordSuccess(r.clientKey)
}

func (t *loginThrottle) recordFailure(key string, now time.Time) {
	t.recordFailureWithLimit(key, now, loginThrottleMaxFailures)
}

func (t *loginThrottle) recordFailureWithLimit(key string, now time.Time, maxFailures int) {
	if t == nil || key == "" {
		return
	}
	if maxFailures <= 0 {
		maxFailures = loginThrottleMaxFailures
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
	if entry.failures >= maxFailures {
		entry.blockedUntil = now.Add(loginThrottleBlock)
	}
}

func (t *loginThrottle) pruneLocked(now time.Time) {
	for key, entry := range t.entries {
		if entry == nil || (entry.inFlight == 0 && !loginThrottleEntryBlocked(entry, now) && loginThrottleEntryExpired(entry, now)) {
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
		if entry.inFlight == 0 && !loginThrottleEntryBlocked(entry, now) && loginThrottleEntryExpired(entry, now) {
			delete(t.entries, key)
			return true
		}
		if loginThrottleEntryBlocked(entry, now) || entry.inFlight > 0 {
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
	return "user\x00" + loginThrottlePeer(peerAddr) + "\x00" + strings.TrimSpace(strings.ToLower(username))
}

func loginThrottleClientKey(peerAddr string) string {
	return "client\x00" + loginThrottlePeer(peerAddr)
}

func loginThrottlePeer(peerAddr string) string {
	host := strings.TrimSpace(peerAddr)
	if splitHost, _, err := net.SplitHostPort(host); err == nil {
		host = splitHost
	}
	if host == "" {
		host = "unknown"
	}
	return host
}
