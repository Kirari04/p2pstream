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
