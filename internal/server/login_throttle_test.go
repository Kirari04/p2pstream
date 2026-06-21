package server

import (
	"strconv"
	"testing"
	"time"
)

func TestLoginThrottleCapsEntries(t *testing.T) {
	throttle := newLoginThrottle(3)
	now := time.Unix(1, 0)

	for i := 0; i < 4; i++ {
		throttle.recordFailure(loginThrottleKey("198.51.100.10:1234", "user"+strconv.Itoa(i)), now.Add(time.Duration(i)*time.Millisecond))
	}

	throttle.mu.Lock()
	defer throttle.mu.Unlock()
	if got := len(throttle.entries); got != 3 {
		t.Fatalf("entries = %d, want capped at 3", got)
	}
	if _, ok := throttle.entries[loginThrottleKey("198.51.100.10:1234", "user0")]; ok {
		t.Fatal("oldest throttle key was not evicted")
	}
}

func TestLoginThrottleDoesNotEvictBlockedKey(t *testing.T) {
	throttle := newLoginThrottle(3)
	now := time.Unix(1, 0)
	adminKey := loginThrottleKey("198.51.100.10:1234", "admin")
	blockLoginThrottleKey(throttle, adminKey, now)

	for i := 0; i < 10; i++ {
		throttle.recordFailure(loginThrottleKey("198.51.100.10:1234", "filler"+strconv.Itoa(i)), now.Add(time.Duration(i+1)*time.Millisecond))
	}

	if retryAfter := throttle.retryAfter(adminKey, now.Add(time.Second)); retryAfter <= 0 {
		t.Fatalf("admin retryAfter = %v, want active block", retryAfter)
	}
	throttle.mu.Lock()
	defer throttle.mu.Unlock()
	if got := len(throttle.entries); got > 3 {
		t.Fatalf("entries = %d, want capped at 3", got)
	}
	if _, ok := throttle.entries[adminKey]; !ok {
		t.Fatal("blocked admin key was evicted")
	}
}

func TestLoginThrottleDropsNewKeyWhenAllEntriesBlocked(t *testing.T) {
	throttle := newLoginThrottle(2)
	now := time.Unix(1, 0)
	firstKey := loginThrottleKey("198.51.100.10:1234", "admin")
	secondKey := loginThrottleKey("198.51.100.10:1234", "operator")
	thirdKey := loginThrottleKey("198.51.100.10:1234", "filler")

	blockLoginThrottleKey(throttle, firstKey, now)
	blockLoginThrottleKey(throttle, secondKey, now.Add(time.Millisecond))
	throttle.recordFailure(thirdKey, now.Add(2*time.Millisecond))

	if retryAfter := throttle.retryAfter(firstKey, now.Add(time.Second)); retryAfter <= 0 {
		t.Fatalf("first retryAfter = %v, want active block", retryAfter)
	}
	if retryAfter := throttle.retryAfter(secondKey, now.Add(time.Second)); retryAfter <= 0 {
		t.Fatalf("second retryAfter = %v, want active block", retryAfter)
	}
	throttle.mu.Lock()
	defer throttle.mu.Unlock()
	if got := len(throttle.entries); got != 2 {
		t.Fatalf("entries = %d, want 2", got)
	}
	if _, ok := throttle.entries[thirdKey]; ok {
		t.Fatal("new filler key was inserted by evicting an active block")
	}
}

func TestLoginThrottleEvictsOldestNonBlockedKey(t *testing.T) {
	throttle := newLoginThrottle(2)
	now := time.Unix(1, 0)
	oldKey := loginThrottleKey("198.51.100.10:1234", "old")
	freshKey := loginThrottleKey("198.51.100.10:1234", "fresh")
	newKey := loginThrottleKey("198.51.100.10:1234", "new")

	throttle.recordFailure(oldKey, now)
	throttle.recordFailure(freshKey, now.Add(time.Second))
	throttle.recordFailure(newKey, now.Add(2*time.Second))

	throttle.mu.Lock()
	defer throttle.mu.Unlock()
	if _, ok := throttle.entries[oldKey]; ok {
		t.Fatal("oldest non-blocked key was not evicted")
	}
	if _, ok := throttle.entries[freshKey]; !ok {
		t.Fatal("fresh non-blocked key was evicted")
	}
	if _, ok := throttle.entries[newKey]; !ok {
		t.Fatal("new key was not inserted")
	}
}

func TestLoginThrottlePrunesExpiredBeforeEviction(t *testing.T) {
	throttle := newLoginThrottle(2)
	old := time.Unix(1, 0)
	throttle.recordFailure(loginThrottleKey("198.51.100.10:1234", "old"), old)
	throttle.recordFailure(loginThrottleKey("198.51.100.10:1234", "fresh"), old.Add(time.Second))

	now := old.Add(loginThrottleWindow + time.Second)
	throttle.recordFailure(loginThrottleKey("198.51.100.10:1234", "new"), now)

	throttle.mu.Lock()
	defer throttle.mu.Unlock()
	if _, ok := throttle.entries[loginThrottleKey("198.51.100.10:1234", "old")]; ok {
		t.Fatal("expired throttle key was not pruned")
	}
	if got := len(throttle.entries); got > 2 {
		t.Fatalf("entries = %d, want at most 2", got)
	}
}

func TestLoginThrottlePrunesExpiredBlockedKey(t *testing.T) {
	throttle := newLoginThrottle(1)
	old := time.Unix(1, 0)
	blockedKey := loginThrottleKey("198.51.100.10:1234", "admin")
	newKey := loginThrottleKey("198.51.100.10:1234", "new")
	blockLoginThrottleKey(throttle, blockedKey, old)

	now := old.Add(loginThrottleWindow + loginThrottleBlock + time.Second)
	throttle.recordFailure(newKey, now)

	throttle.mu.Lock()
	defer throttle.mu.Unlock()
	if _, ok := throttle.entries[blockedKey]; ok {
		t.Fatal("expired blocked key was not pruned")
	}
	if _, ok := throttle.entries[newKey]; !ok {
		t.Fatal("new key was not inserted after pruning expired block")
	}
	if got := len(throttle.entries); got != 1 {
		t.Fatalf("entries = %d, want 1", got)
	}
}

func TestLoginThrottleRecordSuccessClearsKey(t *testing.T) {
	throttle := newLoginThrottle(3)
	now := time.Unix(1, 0)
	key := loginThrottleKey("198.51.100.10:1234", "admin")
	blockLoginThrottleKey(throttle, key, now)

	if retryAfter := throttle.retryAfter(key, now.Add(time.Second)); retryAfter <= 0 {
		t.Fatalf("retryAfter before success = %v, want active block", retryAfter)
	}
	throttle.recordSuccess(key)
	if retryAfter := throttle.retryAfter(key, now.Add(time.Second)); retryAfter != 0 {
		t.Fatalf("retryAfter after success = %v, want 0", retryAfter)
	}
	throttle.mu.Lock()
	defer throttle.mu.Unlock()
	if _, ok := throttle.entries[key]; ok {
		t.Fatal("successful login did not clear throttle key")
	}
}

func blockLoginThrottleKey(throttle *loginThrottle, key string, now time.Time) {
	for i := 0; i < loginThrottleMaxFailures; i++ {
		throttle.recordFailure(key, now)
	}
}
