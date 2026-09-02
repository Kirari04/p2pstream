package server

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type agentStreamAcquireResult struct {
	lease *agentStreamCapacityLease
	err   error
}

func TestAgentStreamCapacityNestedBudgetsAndLifecycle(t *testing.T) {
	manager := newTestAgentStreamCapacityManager(t, agentStreamCapacityConfig{
		Total: 4, Public: 3, Pooled: 2, Control: 1,
		MaxWaiters: 8, MaxWaitersPerKey: 4, MaxOpeningPerSession: 2,
	})

	pooledA := acquireAgentStreamForTest(t, manager, agentStreamCapacityPublicPooled, "route-a", "session-a")
	pooledB := acquireAgentStreamForTest(t, manager, agentStreamCapacityPublicPooled, "route-b", "session-b")
	oneShot := acquireAgentStreamForTest(t, manager, agentStreamCapacityPublicOneShot, "route-c", "session-c")
	health := acquireAgentStreamForTest(t, manager, agentStreamCapacityTrustedHealth, "health", "session-a")

	snapshot := manager.snapshot()
	if snapshot.Total.InUse != 4 || snapshot.Public.InUse != 3 || snapshot.Pooled.InUse != 2 || snapshot.Control.InUse != 1 {
		t.Fatalf("nested usage = total/public/pooled/control %d/%d/%d/%d, want 4/3/2/1",
			snapshot.Total.InUse, snapshot.Public.InUse, snapshot.Pooled.InUse, snapshot.Control.InUse)
	}
	if snapshot.States.Opening != 4 || snapshot.ActiveLeases != 4 || snapshot.Granted != 4 || snapshot.Released != 0 {
		t.Fatalf("opening snapshot = %+v", snapshot)
	}

	if !pooledA.markLive() || pooledA.currentState() != agentStreamLeaseLive {
		t.Fatal("opening pooled lease did not transition to live")
	}
	if pooledA.markLive() {
		t.Fatal("second live transition succeeded")
	}
	if !pooledA.markClosing() || pooledA.currentState() != agentStreamLeaseClosing {
		t.Fatal("live pooled lease did not transition to closing")
	}
	if pooledA.markClosing() {
		t.Fatal("second closing transition succeeded")
	}
	snapshot = manager.snapshot()
	if snapshot.Total.InUse != 4 || snapshot.Pooled.InUse != 2 || snapshot.States.Closing != 1 {
		t.Fatalf("closing lease released capacity early: %+v", snapshot)
	}

	if !pooledA.release() || pooledA.currentState() != agentStreamLeaseReleased {
		t.Fatal("closing pooled lease was not released")
	}
	if pooledA.release() {
		t.Fatal("second release succeeded")
	}
	for _, lease := range []*agentStreamCapacityLease{pooledB, oneShot, health} {
		if !lease.release() {
			t.Fatalf("release failed for lease %d", lease.id)
		}
	}
	assertAgentStreamCapacityClean(t, manager)
}

func TestAgentStreamCapacityOneShotBorrowsUnusedPooledCapacity(t *testing.T) {
	manager := newTestAgentStreamCapacityManager(t, agentStreamCapacityConfig{
		Total: 5, Public: 4, Pooled: 2, Control: 1,
		MaxWaiters: 4, MaxWaitersPerKey: 2, MaxOpeningPerSession: 4,
	})

	leases := []*agentStreamCapacityLease{
		acquireAgentStreamForTest(t, manager, agentStreamCapacityPublicOneShot, "route-a", "session-a"),
		acquireAgentStreamForTest(t, manager, agentStreamCapacityPublicOneShot, "route-b", "session-b"),
		acquireAgentStreamForTest(t, manager, agentStreamCapacityPublicOneShot, "route-c", "session-c"),
		acquireAgentStreamForTest(t, manager, agentStreamCapacityPublicOneShot, "route-d", "session-d"),
		acquireAgentStreamForTest(t, manager, agentStreamCapacityTrustedHealth, "health", "health-session"),
	}
	snapshot := manager.snapshot()
	if snapshot.Public.InUse != 4 || snapshot.Pooled.InUse != 0 || snapshot.Control.InUse != 1 || snapshot.Total.InUse != 5 {
		t.Fatalf("one-shot borrowing usage = %+v", snapshot)
	}
	for _, lease := range leases {
		lease.release()
	}
	assertAgentStreamCapacityClean(t, manager)
}

func TestAgentStreamCapacityTryAcquireReportsConstraintAndDoesNotBarge(t *testing.T) {
	manager := newTestAgentStreamCapacityManager(t, agentStreamCapacityConfig{
		Total: 3, Public: 2, Pooled: 1, Control: 1,
		MaxWaiters: 3, MaxWaitersPerKey: 2, MaxOpeningPerSession: 1,
	})
	pooled := acquireAgentStreamForTest(t, manager, agentStreamCapacityPublicPooled, "route-a", "session-a")
	if !pooled.markLive() {
		t.Fatal("pooled lease did not transition to live")
	}
	if _, err := manager.tryAcquire(agentStreamCapacityPublicPooled, "route-b", "session-b"); !errors.Is(err, errAgentStreamCapacityPooledBudget) {
		t.Fatalf("pooled try-acquire error = %v, want pooled budget", err)
	} else {
		var capacityErr *agentStreamCapacityAcquireError
		if !errors.As(err, &capacityErr) || capacityErr.QueueKey != "route-b" || capacityErr.SessionKey != "session-b" {
			t.Fatalf("pooled try-acquire typed error = %#v", err)
		}
	}
	oneShot := acquireAgentStreamForTest(t, manager, agentStreamCapacityPublicOneShot, "route-b", "session-b")
	if _, err := manager.tryAcquire(agentStreamCapacityPublicOneShot, "route-c", "session-c"); !errors.Is(err, errAgentStreamCapacityPublicBudget) {
		t.Fatalf("one-shot try-acquire error = %v, want public budget", err)
	}
	health := acquireAgentStreamForTest(t, manager, agentStreamCapacityTrustedHealth, "health", "health-session")
	if _, err := manager.tryAcquire(agentStreamCapacityTrustedHealth, "health-2", "health-session-2"); !errors.Is(err, errAgentStreamCapacityTotalBudget) {
		t.Fatalf("health try-acquire at total limit error = %v, want total budget", err)
	}
	health.release()
	oneShot.release()
	pooled.release()

	controlManager := newTestAgentStreamCapacityManager(t, agentStreamCapacityConfig{
		Total: 2, Control: 1,
		MaxWaiters: 1, MaxWaitersPerKey: 1, MaxOpeningPerSession: 1,
	})
	health = acquireAgentStreamForTest(t, controlManager, agentStreamCapacityTrustedHealth, "health", "health-session")
	if _, err := controlManager.tryAcquire(agentStreamCapacityTrustedHealth, "health-2", "health-session-2"); !errors.Is(err, errAgentStreamCapacityControlBudget) {
		t.Fatalf("health try-acquire error = %v, want control budget", err)
	}
	health.release()

	fairManager := newTestAgentStreamCapacityManager(t, agentStreamCapacityConfig{
		Total: 2, Public: 2, Pooled: 2,
		MaxWaiters: 2, MaxWaitersPerKey: 2, MaxOpeningPerSession: 1,
	})
	holder := acquireAgentStreamForTest(t, fairManager, agentStreamCapacityPublicPooled, "holder", "session-a")
	ctx, cancel := context.WithCancel(context.Background())
	waiting := acquireAgentStreamAsync(fairManager, ctx, agentStreamCapacityPublicPooled, "route-a", "session-a")
	waitForAgentStreamWaiters(t, fairManager, 1)
	otherSession, err := fairManager.tryAcquire(agentStreamCapacityPublicPooled, "route-b", "session-b")
	if err != nil {
		t.Fatalf("grantable session behind session-local waiter: %v", err)
	}
	otherSession.release()
	cancel()
	if result := receiveAgentStreamResult(t, waiting); !errors.Is(result.err, context.Canceled) {
		t.Fatalf("canceled fair waiter error = %v", result.err)
	}
	holder.release()
	assertAgentStreamCapacityClean(t, fairManager)
}

func TestAgentStreamCapacityAcquireTimeoutReportsBlockingBudget(t *testing.T) {
	manager := newTestAgentStreamCapacityManager(t, agentStreamCapacityConfig{
		Total: 2, Public: 2, Pooled: 1,
		MaxWaiters: 1, MaxWaitersPerKey: 1, MaxOpeningPerSession: 1,
	})
	holder := acquireAgentStreamForTest(t, manager, agentStreamCapacityPublicPooled, "holder", "session-a")
	if !holder.markLive() {
		t.Fatal("holder did not transition to live")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := manager.acquire(ctx, agentStreamCapacityPublicPooled, "route", "session-b")
	if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, errAgentStreamCapacityPooledBudget) {
		t.Fatalf("timed acquire error = %v, want deadline and pooled budget", err)
	}
	holder.release()
	assertAgentStreamCapacityClean(t, manager)
}

func TestAgentStreamCapacityRoundRobinFairnessAndNoBarging(t *testing.T) {
	manager := newTestAgentStreamCapacityManager(t, agentStreamCapacityConfig{
		Total: 1, Public: 1, Pooled: 1,
		MaxWaiters: 8, MaxWaitersPerKey: 4, MaxOpeningPerSession: 1,
	})
	holder := acquireAgentStreamForTest(t, manager, agentStreamCapacityPublicOneShot, "holder", "holder-session")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a1 := acquireAgentStreamAsync(manager, ctx, agentStreamCapacityPublicOneShot, "route-a", "session-a1")
	waitForAgentStreamWaiters(t, manager, 1)
	a2 := acquireAgentStreamAsync(manager, ctx, agentStreamCapacityPublicOneShot, "route-a", "session-a2")
	waitForAgentStreamWaiters(t, manager, 2)
	b1 := acquireAgentStreamAsync(manager, ctx, agentStreamCapacityPublicOneShot, "route-b", "session-b1")
	waitForAgentStreamWaiters(t, manager, 3)
	b2 := acquireAgentStreamAsync(manager, ctx, agentStreamCapacityPublicOneShot, "route-b", "session-b2")
	waitForAgentStreamWaiters(t, manager, 4)

	holder.release()
	leaseA1 := receiveAgentStreamLease(t, a1)
	assertAcquireStillWaiting(t, a2)
	assertAcquireStillWaiting(t, b1)
	assertAcquireStillWaiting(t, b2)

	leaseA1.release()
	leaseB1 := receiveAgentStreamLease(t, b1)
	assertAcquireStillWaiting(t, a2)
	assertAcquireStillWaiting(t, b2)

	leaseB1.release()
	leaseA2 := receiveAgentStreamLease(t, a2)
	leaseA2.release()
	leaseB2 := receiveAgentStreamLease(t, b2)
	leaseB2.release()
	assertAgentStreamCapacityClean(t, manager)
}

func TestAgentStreamCapacitySkipsSessionBlockedQueue(t *testing.T) {
	manager := newTestAgentStreamCapacityManager(t, agentStreamCapacityConfig{
		Total: 2, Public: 2, Pooled: 2,
		MaxWaiters: 4, MaxWaitersPerKey: 2, MaxOpeningPerSession: 1,
	})
	holder := acquireAgentStreamForTest(t, manager, agentStreamCapacityPublicPooled, "route-holder", "session-a")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	blocked := acquireAgentStreamAsync(manager, ctx, agentStreamCapacityPublicPooled, "route-a", "session-a")
	waitForAgentStreamWaiters(t, manager, 1)
	grantable := acquireAgentStreamAsync(manager, ctx, agentStreamCapacityPublicPooled, "route-a", "session-b")
	leaseB := receiveAgentStreamLease(t, grantable)

	snapshot := manager.snapshot()
	if snapshot.Total.InUse != 2 || snapshot.Waiters != 1 || snapshot.OpeningBySession["session-a"] != 1 || snapshot.OpeningBySession["session-b"] != 1 {
		t.Fatalf("blocked-session accounting = %+v", snapshot)
	}
	assertAcquireStillWaiting(t, blocked)

	if !holder.markLive() {
		t.Fatal("holder did not transition to live")
	}
	assertAcquireStillWaiting(t, blocked)
	leaseB.release()
	leaseA := receiveAgentStreamLease(t, blocked)
	leaseA.release()
	holder.release()
	assertAgentStreamCapacityClean(t, manager)
}

func TestAgentStreamCapacityQueueBoundsAndCancellation(t *testing.T) {
	manager := newTestAgentStreamCapacityManager(t, agentStreamCapacityConfig{
		Total: 1, Public: 1, Pooled: 1,
		MaxWaiters: 3, MaxWaitersPerKey: 2, MaxOpeningPerSession: 1,
	})
	holder := acquireAgentStreamForTest(t, manager, agentStreamCapacityPublicOneShot, "holder", "holder-session")

	ctxA1, cancelA1 := context.WithCancel(context.Background())
	ctxA2, cancelA2 := context.WithCancel(context.Background())
	ctxB1, cancelB1 := context.WithCancel(context.Background())
	defer cancelA1()
	defer cancelA2()
	defer cancelB1()
	a1 := acquireAgentStreamAsync(manager, ctxA1, agentStreamCapacityPublicOneShot, "route-a", "session-a1")
	a2 := acquireAgentStreamAsync(manager, ctxA2, agentStreamCapacityPublicOneShot, "route-a", "session-a2")
	waitForAgentStreamWaiters(t, manager, 2)

	if _, err := manager.acquire(context.Background(), agentStreamCapacityPublicOneShot, "route-a", "session-a3"); !errors.Is(err, errAgentStreamCapacityKeyQueueFull) {
		t.Fatalf("third per-key waiter error = %v, want per-key full", err)
	}
	b1 := acquireAgentStreamAsync(manager, ctxB1, agentStreamCapacityPublicOneShot, "route-b", "session-b1")
	waitForAgentStreamWaiters(t, manager, 3)
	if _, err := manager.acquire(context.Background(), agentStreamCapacityPublicOneShot, "route-c", "session-c1"); !errors.Is(err, errAgentStreamCapacityQueueFull) {
		t.Fatalf("fourth global waiter error = %v, want global full", err)
	}

	cancelA1()
	if result := receiveAgentStreamResult(t, a1); !errors.Is(result.err, context.Canceled) || result.lease != nil {
		t.Fatalf("canceled waiter result = %+v", result)
	}
	waitForAgentStreamWaiters(t, manager, 2)

	holder.release()
	leaseA2 := receiveAgentStreamLease(t, a2)
	leaseA2.release()
	leaseB1 := receiveAgentStreamLease(t, b1)
	leaseB1.release()
	assertAgentStreamCapacityClean(t, manager)
}

func TestAgentStreamCapacityCancellationGrantRaceDoesNotLeak(t *testing.T) {
	manager := newTestAgentStreamCapacityManager(t, agentStreamCapacityConfig{
		Total: 1, Public: 1, Pooled: 1,
		MaxWaiters: 2, MaxWaitersPerKey: 2, MaxOpeningPerSession: 1,
	})

	for iteration := 0; iteration < 250; iteration++ {
		holder := acquireAgentStreamForTest(t, manager, agentStreamCapacityPublicOneShot, "holder", "holder-session")
		ctx, cancel := context.WithCancel(context.Background())
		resultCh := acquireAgentStreamAsync(manager, ctx, agentStreamCapacityPublicOneShot, "route", "session")
		waitForAgentStreamWaiters(t, manager, 1)

		start := make(chan struct{})
		var actions sync.WaitGroup
		actions.Add(2)
		go func() {
			defer actions.Done()
			<-start
			cancel()
		}()
		go func() {
			defer actions.Done()
			<-start
			holder.release()
		}()
		close(start)
		actions.Wait()
		result := receiveAgentStreamResult(t, resultCh)
		if result.lease != nil {
			result.lease.release()
		} else if !errors.Is(result.err, context.Canceled) {
			t.Fatalf("iteration %d result error = %v, want cancellation or an already-returned lease", iteration, result.err)
		}
		assertAgentStreamCapacityClean(t, manager)
	}
}

func TestAgentStreamCapacityReservesHeadroomForAnotherRegisteredSession(t *testing.T) {
	manager := newTestAgentStreamCapacityManager(t, agentStreamCapacityConfig{
		Total: 8, Public: 8, Pooled: 8,
		MaxWaiters: 8, MaxWaitersPerKey: 4, MaxOpeningPerSession: 8,
		ReservedPublicForOtherSessions: 2,
	})
	manager.registerSession("session-a")

	leases := make([]*agentStreamCapacityLease, 0, 8)
	for index := 0; index < 8; index++ {
		lease, err := manager.tryAcquire(agentStreamCapacityPublicPooled, "route-a", "session-a")
		if err != nil {
			t.Fatalf("single registered session acquire %d: %v", index, err)
		}
		if !lease.markLive() {
			t.Fatalf("single registered session lease %d did not become live", index)
		}
		leases = append(leases, lease)
	}
	for _, lease := range leases {
		lease.release()
	}

	manager.registerSession("session-b")
	leases = leases[:0]
	for index := 0; index < 6; index++ {
		lease, err := manager.tryAcquire(agentStreamCapacityPublicPooled, "route-a", "session-a")
		if err != nil {
			t.Fatalf("contended session acquire %d: %v", index, err)
		}
		if !lease.markLive() {
			t.Fatalf("contended lease %d did not become live", index)
		}
		leases = append(leases, lease)
	}
	if _, err := manager.tryAcquire(agentStreamCapacityPublicPooled, "route-a", "session-a"); !errors.Is(err, errAgentStreamCapacitySessionBudget) {
		t.Fatalf("seventh contended session acquire = %v, want session budget", err)
	}
	other, err := manager.tryAcquire(agentStreamCapacityPublicPooled, "route-b", "session-b")
	if err != nil {
		t.Fatalf("reserved capacity unavailable to second session: %v", err)
	}
	other.release()
	for _, lease := range leases {
		lease.release()
	}
	assertAgentStreamCapacityClean(t, manager)
}

func TestAgentStreamCapacityClosingPermitAndConcurrentRelease(t *testing.T) {
	manager := newTestAgentStreamCapacityManager(t, agentStreamCapacityConfig{
		Total: 1, Public: 1, Pooled: 1,
		MaxWaiters: 1, MaxWaitersPerKey: 1, MaxOpeningPerSession: 1,
	})
	lease := acquireAgentStreamForTest(t, manager, agentStreamCapacityPublicPooled, "route", "session")
	if !lease.markClosing() {
		t.Fatal("opening lease did not transition directly to closing")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := manager.acquire(ctx, agentStreamCapacityPublicPooled, "blocked", "other-session"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("acquire while closing error = %v, want deadline exceeded", err)
	}
	if snapshot := manager.snapshot(); snapshot.Total.InUse != 1 || snapshot.States.Closing != 1 {
		t.Fatalf("closing snapshot = %+v", snapshot)
	}

	var successful atomic.Int64
	var releases sync.WaitGroup
	for index := 0; index < 32; index++ {
		releases.Add(1)
		go func() {
			defer releases.Done()
			if lease.release() {
				successful.Add(1)
			}
		}()
	}
	releases.Wait()
	if got := successful.Load(); got != 1 {
		t.Fatalf("successful releases = %d, want 1", got)
	}
	assertAgentStreamCapacityClean(t, manager)
}

func TestAgentStreamCapacityConcurrentStressPreservesInvariants(t *testing.T) {
	manager := newTestAgentStreamCapacityManager(t, agentStreamCapacityConfig{
		Total: 12, Public: 10, Pooled: 6, Control: 2,
		MaxWaiters: 256, MaxWaitersPerKey: 64, MaxOpeningPerSession: 2,
	})

	const requestCount = 600
	start := make(chan struct{})
	errorsCh := make(chan error, requestCount)
	var requests sync.WaitGroup
	for index := 0; index < requestCount; index++ {
		index := index
		requests.Add(1)
		go func() {
			defer requests.Done()
			<-start
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			class := agentStreamCapacityPublicOneShot
			switch {
			case index%13 == 0:
				class = agentStreamCapacityTrustedHealth
			case index%3 == 0:
				class = agentStreamCapacityPublicPooled
			}
			queueKey := "route-" + string(rune('a'+index%11))
			sessionKey := "session-" + string(rune('a'+index%7))
			lease, err := manager.acquire(ctx, class, queueKey, sessionKey)
			if err != nil {
				if errors.Is(err, errAgentStreamCapacityQueueFull) || errors.Is(err, errAgentStreamCapacityKeyQueueFull) ||
					errors.Is(err, context.DeadlineExceeded) {
					return
				}
				errorsCh <- err
				return
			}
			if !lease.markLive() {
				errorsCh <- errors.New("stress lease did not transition to live")
				lease.release()
				return
			}
			if index%5 == 0 && !lease.markClosing() {
				errorsCh <- errors.New("stress lease did not transition to closing")
				lease.release()
				return
			}
			lease.release()
		}()
	}
	close(start)

	stopValidation := make(chan struct{})
	validationDone := make(chan struct{})
	go func() {
		defer close(validationDone)
		for {
			if err := manager.validateInvariants(); err != nil {
				errorsCh <- err
				return
			}
			select {
			case <-time.After(time.Millisecond):
			case <-stopValidation:
				return
			}
		}
	}()
	requests.Wait()
	close(stopValidation)
	<-validationDone
	close(errorsCh)
	for err := range errorsCh {
		t.Errorf("concurrent stress: %v", err)
	}
	assertAgentStreamCapacityClean(t, manager)
}

func TestAgentStreamCapacitySnapshotIsCopiedAndInvariantChecked(t *testing.T) {
	manager := newTestAgentStreamCapacityManager(t, agentStreamCapacityConfig{
		Total: 2, Public: 1, Pooled: 1, Control: 1,
		MaxWaiters: 2, MaxWaitersPerKey: 1, MaxOpeningPerSession: 1,
	})
	lease := acquireAgentStreamForTest(t, manager, agentStreamCapacityTrustedHealth, "health", "session")
	snapshot := manager.snapshot()
	snapshot.OpeningBySession["session"] = 99
	snapshot.PublicOpeningBySession["fake"] = 99
	snapshot.WaitersByKey["fake"] = 99
	snapshot.StatesByClass[agentStreamCapacityTrustedHealth] = agentStreamCapacityStateSnapshot{Opening: 99}

	fresh := manager.snapshot()
	if fresh.OpeningBySession["session"] != 1 || fresh.PublicOpeningBySession["fake"] != 0 || fresh.WaitersByKey["fake"] != 0 || fresh.StatesByClass[agentStreamCapacityTrustedHealth].Opening != 1 {
		t.Fatalf("snapshot mutation affected manager: %+v", fresh)
	}
	if err := manager.validateInvariants(); err != nil {
		t.Fatalf("invariant validation: %v", err)
	}
	lease.release()
	assertAgentStreamCapacityClean(t, manager)
}

func TestAgentStreamCapacityRejectsInvalidConfigurationAndRequests(t *testing.T) {
	configs := []agentStreamCapacityConfig{
		{},
		{Total: 1, Public: 2, MaxOpeningPerSession: 1},
		{Total: 2, Public: 1, Pooled: 2, MaxOpeningPerSession: 1},
		{Total: 2, Public: 1, Control: 2, MaxOpeningPerSession: 1},
		{Total: 2, Public: 1, MaxWaiters: 1, MaxWaitersPerKey: 2, MaxOpeningPerSession: 1},
		{Total: 2, Public: 1, MaxOpeningPerSession: 0},
	}
	for _, config := range configs {
		if _, err := newAgentStreamCapacityManager(config); !errors.Is(err, errAgentStreamCapacityInvalidConfig) {
			t.Errorf("config %+v error = %v, want invalid config", config, err)
		}
	}

	manager := newTestAgentStreamCapacityManager(t, agentStreamCapacityConfig{
		Total: 1, Public: 1, MaxWaiters: 1, MaxWaitersPerKey: 1, MaxOpeningPerSession: 1,
	})
	for _, test := range []struct {
		class      agentStreamCapacityClass
		queueKey   string
		sessionKey string
		want       error
	}{
		{class: agentStreamCapacityClassCount, queueKey: "route", sessionKey: "session", want: errAgentStreamCapacityInvalidRequest},
		{class: agentStreamCapacityPublicOneShot, sessionKey: "session", want: errAgentStreamCapacityInvalidRequest},
		{class: agentStreamCapacityPublicOneShot, queueKey: "route", want: errAgentStreamCapacityInvalidRequest},
		{class: agentStreamCapacityPublicPooled, queueKey: "route", sessionKey: "session", want: errAgentStreamCapacityClassDisabled},
		{class: agentStreamCapacityTrustedHealth, queueKey: "health", sessionKey: "session", want: errAgentStreamCapacityClassDisabled},
	} {
		if _, err := manager.acquire(context.Background(), test.class, test.queueKey, test.sessionKey); !errors.Is(err, test.want) {
			t.Errorf("acquire class/key/session %d/%q/%q error = %v, want %v", test.class, test.queueKey, test.sessionKey, err, test.want)
		}
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := manager.acquire(canceled, agentStreamCapacityPublicOneShot, "route", "session"); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled acquire error = %v, want context canceled", err)
	}
}

func newTestAgentStreamCapacityManager(t *testing.T, config agentStreamCapacityConfig) *agentStreamCapacityManager {
	t.Helper()
	manager, err := newAgentStreamCapacityManager(config)
	if err != nil {
		t.Fatalf("new capacity manager: %v", err)
	}
	return manager
}

func acquireAgentStreamForTest(
	t *testing.T,
	manager *agentStreamCapacityManager,
	class agentStreamCapacityClass,
	queueKey string,
	sessionKey string,
) *agentStreamCapacityLease {
	t.Helper()
	lease, err := manager.acquire(context.Background(), class, queueKey, sessionKey)
	if err != nil {
		t.Fatalf("acquire %d/%s/%s: %v", class, queueKey, sessionKey, err)
	}
	return lease
}

func acquireAgentStreamAsync(
	manager *agentStreamCapacityManager,
	ctx context.Context,
	class agentStreamCapacityClass,
	queueKey string,
	sessionKey string,
) <-chan agentStreamAcquireResult {
	result := make(chan agentStreamAcquireResult, 1)
	go func() {
		lease, err := manager.acquire(ctx, class, queueKey, sessionKey)
		result <- agentStreamAcquireResult{lease: lease, err: err}
	}()
	return result
}

func receiveAgentStreamLease(t *testing.T, result <-chan agentStreamAcquireResult) *agentStreamCapacityLease {
	t.Helper()
	acquired := receiveAgentStreamResult(t, result)
	if acquired.err != nil || acquired.lease == nil {
		t.Fatalf("acquire result = %+v", acquired)
	}
	return acquired.lease
}

func receiveAgentStreamResult(t *testing.T, result <-chan agentStreamAcquireResult) agentStreamAcquireResult {
	t.Helper()
	select {
	case acquired := <-result:
		return acquired
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for capacity acquisition")
		return agentStreamAcquireResult{}
	}
}

func assertAcquireStillWaiting(t *testing.T, result <-chan agentStreamAcquireResult) {
	t.Helper()
	select {
	case acquired := <-result:
		if acquired.lease != nil {
			acquired.lease.release()
		}
		t.Fatalf("acquire completed unexpectedly: %+v", acquired)
	case <-time.After(10 * time.Millisecond):
	}
}

func waitForAgentStreamWaiters(t *testing.T, manager *agentStreamCapacityManager, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if manager.snapshot().Waiters == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("waiters = %d, want %d", manager.snapshot().Waiters, want)
}

func assertAgentStreamCapacityClean(t *testing.T, manager *agentStreamCapacityManager) {
	t.Helper()
	if err := manager.validateInvariants(); err != nil {
		t.Fatalf("capacity invariant: %v", err)
	}
	snapshot := manager.snapshot()
	if snapshot.Total.InUse != 0 || snapshot.Public.InUse != 0 || snapshot.Pooled.InUse != 0 || snapshot.Control.InUse != 0 ||
		snapshot.Waiters != 0 || snapshot.ActiveLeases != 0 || snapshot.States.Opening != 0 || snapshot.States.Live != 0 || snapshot.States.Closing != 0 {
		t.Fatalf("capacity manager not clean: %+v", snapshot)
	}
}
