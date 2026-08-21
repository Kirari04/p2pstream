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

func TestLoginThrottleClientBucketUsesHigherFailureLimit(t *testing.T) {
	throttle := newLoginThrottle(100)
	now := time.Unix(1, 0)
	key := loginThrottleClientKey("198.51.100.10")
	for i := 0; i < loginThrottleClientMaxFailures-1; i++ {
		throttle.recordFailureWithLimit(key, now, loginThrottleClientMaxFailures)
	}
	if retry := throttle.retryAfter(key, now); retry != 0 {
		t.Fatalf("client bucket blocked early after %d failures", loginThrottleClientMaxFailures-1)
	}
	throttle.recordFailureWithLimit(key, now, loginThrottleClientMaxFailures)
	if retry := throttle.retryAfter(key, now); retry <= 0 {
		t.Fatal("client bucket did not block at configured failure limit")
	}
}

func TestLoginThrottleBucketsKeepClientProtectionAtMinimumCapacity(t *testing.T) {
	usernameThrottle, clientThrottle := newLoginThrottleBuckets(1)
	now := time.Unix(1, 0)
	clientKey := loginThrottleClientKey("198.51.100.10")
	for i := 0; i < loginThrottleClientMaxFailures; i++ {
		usernameThrottle.recordFailure(loginThrottleKey("198.51.100.10", "user"+strconv.Itoa(i)), now.Add(time.Duration(i)*time.Millisecond))
		clientThrottle.recordFailureWithLimit(clientKey, now.Add(time.Duration(i)*time.Millisecond), loginThrottleClientMaxFailures)
	}
	if retry := clientThrottle.retryAfter(clientKey, now.Add(time.Second)); retry <= 0 {
		t.Fatal("client-wide protection was evicted by distinct usernames")
	}
}

func TestLoginThrottleReservationsBoundConcurrentAttempts(t *testing.T) {
	usernameThrottle, clientThrottle := newLoginThrottleBuckets(100)
	now := time.Unix(1, 0)
	usernameKey := loginThrottleKey("198.51.100.10", "admin")
	clientKey := loginThrottleClientKey("198.51.100.10")
	const attempts = 64
	start := make(chan struct{})
	results := make(chan *loginThrottleReservation, attempts)

	for range attempts {
		go func() {
			<-start
			reservation, admitted := reserveLoginThrottleAttempt(
				usernameThrottle,
				clientThrottle,
				usernameKey,
				clientKey,
				now,
			)
			if !admitted {
				results <- nil
				return
			}
			results <- reservation
		}()
	}
	close(start)

	accepted := make([]*loginThrottleReservation, 0, loginThrottleMaxFailures)
	for range attempts {
		if reservation := <-results; reservation != nil {
			accepted = append(accepted, reservation)
		}
	}
	if got := len(accepted); got != loginThrottleMaxFailures {
		t.Fatalf("concurrent reservations admitted = %d, want %d", got, loginThrottleMaxFailures)
	}
	for _, reservation := range accepted {
		reservation.release()
	}

	reservation, admitted := reserveLoginThrottleAttempt(
		usernameThrottle,
		clientThrottle,
		usernameKey,
		clientKey,
		now.Add(time.Second),
	)
	if !admitted {
		t.Fatal("released reservations prevented a legitimate later attempt")
	}
	reservation.recordSuccess()
}

func TestLoginThrottleReservationRecordsFailuresAndBlocks(t *testing.T) {
	usernameThrottle, clientThrottle := newLoginThrottleBuckets(100)
	now := time.Unix(1, 0)
	usernameKey := loginThrottleKey("198.51.100.10", "admin")
	clientKey := loginThrottleClientKey("198.51.100.10")

	for i := 0; i < loginThrottleMaxFailures; i++ {
		reservation, admitted := reserveLoginThrottleAttempt(
			usernameThrottle,
			clientThrottle,
			usernameKey,
			clientKey,
			now.Add(time.Duration(i)*time.Millisecond),
		)
		if !admitted {
			t.Fatalf("attempt %d rejected before failure threshold", i+1)
		}
		reservation.recordFailure(now.Add(time.Duration(i) * time.Millisecond))
	}
	if _, admitted := reserveLoginThrottleAttempt(
		usernameThrottle,
		clientThrottle,
		usernameKey,
		clientKey,
		now.Add(time.Second),
	); admitted {
		t.Fatal("attempt admitted after the username failure threshold")
	}
}

func TestLoginThrottleReservationSupportsLegacySharedTracker(t *testing.T) {
	throttle := newLoginThrottle(1)
	reservation, admitted := reserveLoginThrottleAttempt(
		throttle,
		throttle,
		loginThrottleKey("198.51.100.10", "admin"),
		loginThrottleClientKey("198.51.100.10"),
		time.Unix(1, 0),
	)
	if !admitted {
		t.Fatal("legacy shared tracker rejected an otherwise valid attempt")
	}
	reservation.recordSuccess()
}

func TestLoginThrottleSuccessPreservesSiblingReservations(t *testing.T) {
	usernameThrottle, clientThrottle := newLoginThrottleBuckets(100)
	now := time.Unix(1, 0)
	usernameKey := loginThrottleKey("198.51.100.10", "admin")
	clientKey := loginThrottleClientKey("198.51.100.10")
	reservations := make([]*loginThrottleReservation, 0, loginThrottleMaxFailures)
	for i := 0; i < loginThrottleMaxFailures; i++ {
		reservation, admitted := reserveLoginThrottleAttempt(
			usernameThrottle,
			clientThrottle,
			usernameKey,
			clientKey,
			now,
		)
		if !admitted {
			t.Fatalf("initial reservation %d rejected", i+1)
		}
		reservations = append(reservations, reservation)
	}

	reservations[0].recordSuccess()
	extra, admitted := reserveLoginThrottleAttempt(
		usernameThrottle,
		clientThrottle,
		usernameKey,
		clientKey,
		now.Add(time.Second),
	)
	if !admitted {
		t.Fatal("successful reservation did not free exactly one username slot")
	}
	if unexpected, admitted := reserveLoginThrottleAttempt(
		usernameThrottle,
		clientThrottle,
		usernameKey,
		clientKey,
		now.Add(time.Second),
	); admitted {
		unexpected.release()
		t.Fatal("success discarded sibling in-flight reservations")
	}

	extra.release()
	for _, reservation := range reservations[1:] {
		reservation.release()
	}
}

func TestLoginThrottleSuccessPreservesClientFailureBudget(t *testing.T) {
	usernameThrottle, clientThrottle := newLoginThrottleBuckets(100)
	now := time.Unix(1, 0)
	clientKey := loginThrottleClientKey("198.51.100.10")
	adminKey := loginThrottleKey("198.51.100.10", "admin")

	for i := 0; i < 4; i++ {
		reservation, admitted := reserveLoginThrottleAttempt(
			usernameThrottle,
			clientThrottle,
			adminKey,
			clientKey,
			now.Add(time.Duration(i)*time.Millisecond),
		)
		if !admitted {
			t.Fatalf("initial failure reservation %d rejected", i+1)
		}
		reservation.recordFailure(now.Add(time.Duration(i) * time.Millisecond))
	}
	reservation, admitted := reserveLoginThrottleAttempt(
		usernameThrottle,
		clientThrottle,
		adminKey,
		clientKey,
		now.Add(time.Second),
	)
	if !admitted {
		t.Fatal("valid username reservation rejected below the failure threshold")
	}
	reservation.recordSuccess()

	for i := 0; i < loginThrottleClientMaxFailures-4; i++ {
		reservation, admitted = reserveLoginThrottleAttempt(
			usernameThrottle,
			clientThrottle,
			loginThrottleKey("198.51.100.10", "spray"+strconv.Itoa(i)),
			clientKey,
			now.Add(time.Duration(i+2)*time.Second),
		)
		if !admitted {
			t.Fatalf("spray failure reservation %d rejected early", i+1)
		}
		reservation.recordFailure(now.Add(time.Duration(i+2) * time.Second))
	}
	if _, admitted := reserveLoginThrottleAttempt(
		usernameThrottle,
		clientThrottle,
		loginThrottleKey("198.51.100.10", "next"),
		clientKey,
		now.Add(time.Minute),
	); admitted {
		t.Fatal("successful login reset the client-wide failure budget")
	}
}

func blockLoginThrottleKey(throttle *loginThrottle, key string, now time.Time) {
	for i := 0; i < loginThrottleMaxFailures; i++ {
		throttle.recordFailure(key, now)
	}
}
