package server

import (
	"context"
	"errors"
	"testing"
	"time"

	"p2pstream/internal/tunnel"
)

func TestDefaultAgentStreamCapacityConfigReservesPublicAndHealthHeadroom(t *testing.T) {
	config := defaultAgentStreamCapacityConfig(tunnel.DefaultMaxConcurrentAgentRequests)
	if config.Total != 64 || config.Public != 60 || config.Control != 4 || config.Pooled != 45 {
		t.Fatalf("default capacity budgets = total=%d public=%d pooled=%d control=%d, want 64/60/45/4", config.Total, config.Public, config.Pooled, config.Control)
	}
	if config.Public-config.Pooled != 15 {
		t.Fatalf("one-shot public reserve = %d, want 15", config.Public-config.Pooled)
	}
	if config.ReservedPublicForOtherSessions != 15 {
		t.Fatalf("cross-session public reserve = %d, want 15", config.ReservedPublicForOtherSessions)
	}
	if config.MaxWaiters != 64 || config.MaxWaitersPerKey != 16 {
		t.Fatalf("waiter budgets = %d/%d, want 64/16", config.MaxWaiters, config.MaxWaitersPerKey)
	}
	if err := validateAgentStreamCapacityConfig(config); err != nil {
		t.Fatalf("default capacity config: %v", err)
	}
	manager, err := newAgentStreamCapacityManager(config)
	if err != nil {
		t.Fatalf("default capacity manager: %v", err)
	}
	if got := manager.publicOpeningLimitPerSessionLocked(); got != 64 {
		t.Fatalf("default public opening limit = %d, want 64 (lifetime public budget is 60)", got)
	}
}

func TestHighTotalReservesPerSessionOpeningCapacityForHealth(t *testing.T) {
	config := defaultAgentStreamCapacityConfig(300)
	manager, err := newAgentStreamCapacityManager(config)
	if err != nil {
		t.Fatalf("new high-total capacity manager: %v", err)
	}
	wantPublicOpeningLimit := config.MaxOpeningPerSession - config.Control
	if got := manager.publicOpeningLimitPerSessionLocked(); got != wantPublicOpeningLimit {
		t.Fatalf("public opening limit = %d, want %d", got, wantPublicOpeningLimit)
	}

	publicLeases := make([]*agentStreamCapacityLease, 0, wantPublicOpeningLimit)
	for range wantPublicOpeningLimit {
		lease, acquireErr := manager.tryAcquire(agentStreamCapacityPublicOneShot, "public", "shared-session")
		if acquireErr != nil {
			t.Fatalf("fill public opening reserve at %d: %v", len(publicLeases), acquireErr)
		}
		publicLeases = append(publicLeases, lease)
	}
	if _, acquireErr := manager.tryAcquire(agentStreamCapacityPublicOneShot, "public-blocked", "shared-session"); !errors.Is(acquireErr, errAgentStreamCapacitySessionOpeningLimit) {
		t.Fatalf("public acquire beyond opening share = %v, want session opening limit", acquireErr)
	}

	healthLeases := make([]*agentStreamCapacityLease, 0, config.Control)
	for range config.Control {
		lease, acquireErr := manager.tryAcquire(agentStreamCapacityTrustedHealth, "health", "shared-session")
		if acquireErr != nil {
			t.Fatalf("health acquire %d behind full public opening share: %v", len(healthLeases), acquireErr)
		}
		healthLeases = append(healthLeases, lease)
	}
	snapshot := manager.snapshot()
	if snapshot.OpeningBySession["shared-session"] != config.MaxOpeningPerSession ||
		snapshot.PublicOpeningBySession["shared-session"] != wantPublicOpeningLimit ||
		snapshot.Control.InUse != config.Control {
		t.Fatalf("reserved health opening accounting = %+v", snapshot)
	}
	if invariantErr := manager.validateInvariants(); invariantErr != nil {
		t.Fatalf("high-total reserved opening invariants: %v", invariantErr)
	}

	for _, lease := range healthLeases {
		lease.release()
	}
	for _, lease := range publicLeases {
		lease.release()
	}
	assertAgentStreamCapacityClean(t, manager)
}

func TestDefaultAgentStreamCapacityConfigSupportsSmallLegacyTotals(t *testing.T) {
	for _, total := range []int64{1, 2, 4, 5, 16} {
		config := defaultAgentStreamCapacityConfig(total)
		if config.Total != int(total) {
			t.Fatalf("total %d normalized to %d", total, config.Total)
		}
		if err := validateAgentStreamCapacityConfig(config); err != nil {
			t.Fatalf("total %d produced invalid config: %v", total, err)
		}
	}
	five := defaultAgentStreamCapacityConfig(5)
	if five.Control != 1 || five.Public != 4 {
		t.Fatalf("five-stream budgets = public %d control %d, want 4/1", five.Public, five.Control)
	}
}

func TestDefaultAgentStreamCapacityConfigFallsBackForInvalidTotal(t *testing.T) {
	for _, total := range []int64{0, -1, tunnel.MaxConcurrentAgentRequestsLimit + 1} {
		config := defaultAgentStreamCapacityConfig(total)
		if config.Total != int(tunnel.DefaultMaxConcurrentAgentRequests) {
			t.Fatalf("invalid total %d normalized to %d, want %d", total, config.Total, tunnel.DefaultMaxConcurrentAgentRequests)
		}
	}
}

func TestBlockedHealthWaiterDoesNotDisablePooledOrOneShotPublicAdmission(t *testing.T) {
	manager, err := newAgentStreamCapacityManager(agentStreamCapacityConfig{
		Total: 3, Public: 2, Pooled: 1, Control: 1,
		MaxWaiters: 3, MaxWaitersPerKey: 2, MaxOpeningPerSession: 2,
	})
	if err != nil {
		t.Fatalf("new capacity manager: %v", err)
	}
	healthLease, err := manager.tryAcquire(agentStreamCapacityTrustedHealth, "occupied-health", "health-session")
	if err != nil || !healthLease.markLive() {
		t.Fatalf("occupy health capacity: lease=%v err=%v", healthLease, err)
	}
	defer healthLease.release()

	healthCtx, cancelHealth := context.WithCancel(context.Background())
	healthResult := make(chan error, 1)
	go func() {
		_, acquireErr := manager.acquire(healthCtx, agentStreamCapacityTrustedHealth, "waiting-health", "other-health-session")
		healthResult <- acquireErr
	}()
	waitForAgentStreamCapacityWaiters(t, manager, 1)

	pooled, err := manager.tryAcquire(agentStreamCapacityPublicPooled, "public-pooled", "public-session")
	if err != nil {
		t.Fatalf("pooled fast-path behind disjoint health waiter: %v", err)
	}
	pooled.release()

	mapped := agentStreamCapacityDialError(
		context.Background(),
		agentStreamCapacityPublicPooled,
		newAgentStreamCapacityAcquireError(nil, errAgentStreamCapacityWaitTurn, "public-pooled", "public-session"),
	)
	var dialErr agentDialError
	if !errors.As(mapped, &dialErr) || dialErr.Kind != "server_capacity" || !errors.Is(mapped, errAgentStreamCapacityWaitTurn) {
		t.Fatalf("mapped pooled fair-turn error = %v, want typed server_capacity", mapped)
	}

	oneShotCtx, cancelOneShot := context.WithTimeout(context.Background(), time.Second)
	defer cancelOneShot()
	oneShot, err := manager.acquire(oneShotCtx, agentStreamCapacityPublicOneShot, "public-one-shot", "public-session")
	if err != nil {
		t.Fatalf("one-shot admission behind blocked health waiter: %v", err)
	}
	oneShot.release()
	cancelHealth()
	if err := <-healthResult; !errors.Is(err, context.Canceled) {
		t.Fatalf("blocked health waiter error = %v, want context.Canceled", err)
	}
}

func TestPooledCapacityHandoffOnlyAcceptsRecoverableConstraints(t *testing.T) {
	tests := []struct {
		name        string
		constraint  error
		kind        string
		handoff     bool
		reclaimIdle bool
	}{
		{name: "class disabled", constraint: errAgentStreamCapacityClassDisabled, kind: "server_pooled_capacity", handoff: true},
		{name: "total budget", constraint: errAgentStreamCapacityTotalBudget, kind: "server_pooled_capacity", handoff: true, reclaimIdle: true},
		{name: "public budget", constraint: errAgentStreamCapacityPublicBudget, kind: "server_pooled_capacity", handoff: true, reclaimIdle: true},
		{name: "pooled budget", constraint: errAgentStreamCapacityPooledBudget, kind: "server_pooled_capacity", handoff: true},
		{name: "session budget", constraint: errAgentStreamCapacitySessionBudget, kind: "server_pooled_capacity", handoff: true, reclaimIdle: true},
		{name: "session opening", constraint: errAgentStreamCapacitySessionOpeningLimit, kind: "server_capacity"},
		{name: "fair turn", constraint: errAgentStreamCapacityWaitTurn, kind: "server_capacity"},
		{name: "global queue", constraint: errAgentStreamCapacityQueueFull, kind: "server_capacity"},
		{name: "key queue", constraint: errAgentStreamCapacityKeyQueueFull, kind: "server_capacity"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			acquireErr := newAgentStreamCapacityAcquireError(nil, test.constraint, "public:route", "session-a")
			mapped := agentStreamCapacityDialError(context.Background(), agentStreamCapacityPublicPooled, acquireErr)
			var dialErr agentDialError
			if !errors.As(mapped, &dialErr) || dialErr.Kind != test.kind {
				t.Fatalf("mapped error = %v, want kind %q", mapped, test.kind)
			}
			if !errors.Is(mapped, test.constraint) {
				t.Fatalf("mapped error %v lost typed constraint %v", mapped, test.constraint)
			}
			if got := agentStreamCapacityAllowsPooledHandoff(mapped); got != test.handoff {
				t.Fatalf("handoff eligibility = %v, want %v", got, test.handoff)
			}
			if got := agentStreamCapacityRequiresIdleReclaim(mapped); got != test.reclaimIdle {
				t.Fatalf("idle reclaim eligibility = %v, want %v", got, test.reclaimIdle)
			}
		})
	}
}

func TestHealthQueueKeyIsProtectedFromSameTargetPublicHeadOfLineBlocking(t *testing.T) {
	manager, err := newAgentStreamCapacityManager(agentStreamCapacityConfig{
		Total: 2, Public: 1, Pooled: 0, Control: 1,
		MaxWaiters: 1, MaxWaitersPerKey: 1, MaxOpeningPerSession: 2,
	})
	if err != nil {
		t.Fatalf("new capacity manager: %v", err)
	}
	publicLease, err := manager.tryAcquire(agentStreamCapacityPublicOneShot, "occupied-public", "public-session")
	if err != nil || !publicLease.markLive() {
		t.Fatalf("occupy public capacity: lease=%v err=%v", publicLease, err)
	}
	defer publicLease.release()

	baseKey := "route-target:7"
	publicKey := agentStreamCapacityQueueKey(agentStreamCapacityPublicOneShot, baseKey)
	healthKey := agentStreamCapacityQueueKey(agentStreamCapacityTrustedHealth, baseKey)
	if publicKey == healthKey {
		t.Fatalf("public and health queue keys collide: %q", publicKey)
	}
	publicCtx, cancelPublic := context.WithCancel(context.Background())
	publicResult := make(chan error, 1)
	go func() {
		_, acquireErr := manager.acquire(publicCtx, agentStreamCapacityPublicOneShot, publicKey, "other-public-session")
		publicResult <- acquireErr
	}()
	waitForAgentStreamCapacityWaiters(t, manager, 1)

	healthCtx, cancelHealth := context.WithTimeout(context.Background(), time.Second)
	defer cancelHealth()
	healthLease, err := manager.acquire(healthCtx, agentStreamCapacityTrustedHealth, healthKey, "health-session")
	if err != nil {
		t.Fatalf("health admission behind same-target public waiter: %v", err)
	}
	healthLease.release()
	cancelPublic()
	if err := <-publicResult; !errors.Is(err, context.Canceled) {
		t.Fatalf("blocked public waiter error = %v, want context.Canceled", err)
	}
}

func TestHealthCapacityTimeoutRetainsLocalCapacityClassification(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	<-ctx.Done()
	err := newAgentStreamCapacityAcquireError(ctx.Err(), errAgentStreamCapacityControlBudget, "health:route-target:7", "health-session")
	mapped := agentStreamCapacityDialError(ctx, agentStreamCapacityTrustedHealth, err)
	var dialErr agentDialError
	if !errors.As(mapped, &dialErr) || dialErr.Kind != "server_health_capacity" {
		t.Fatalf("health capacity timeout = %v, want server_health_capacity", mapped)
	}
}

func TestDashboardAgentStreamCapacitySummaryExportsLiveStateAndRecovery(t *testing.T) {
	manager, err := newAgentStreamCapacityManager(agentStreamCapacityConfig{
		Total: 2, Public: 1, Pooled: 1, Control: 1,
		MaxWaiters: 1, MaxWaitersPerKey: 1, MaxOpeningPerSession: 1,
	})
	if err != nil {
		t.Fatalf("new capacity manager: %v", err)
	}
	lease, err := manager.tryAcquire(agentStreamCapacityPublicPooled, "route", "session")
	if err != nil || !lease.markLive() || !lease.markClosing() {
		t.Fatalf("prepare closing lease: lease=%v err=%v", lease, err)
	}
	if _, err := manager.tryAcquire(agentStreamCapacityPublicPooled, "blocked", "other-session"); !errors.Is(err, errAgentStreamCapacityPublicBudget) {
		t.Fatalf("blocked acquire = %v, want public budget", err)
	}
	time.Sleep(2 * time.Millisecond)
	pool := newAgentTransportPool()
	pool.reclaimAttempts.Add(2)
	pool.reclaimSuccesses.Add(1)
	pool.reclaimNoCandidate.Add(1)
	pool.fallbackAttempts.Add(3)
	pool.fallbackRecovered.Add(2)
	pool.fallbackFailed.Add(1)
	pool.terminalCapacityFailure.Add(1)
	app := &App{agentStreamCapacity: manager, AgentTransports: pool}
	summary := app.dashboardAgentStreamCapacitySummary()
	if summary.TotalCapacity != 2 || summary.TotalInUse != 1 || summary.PublicCapacity != 1 || summary.PooledInUse != 1 || summary.Closing != 1 {
		t.Fatalf("capacity summary budgets/states = %+v", summary)
	}
	if summary.OldestClosingAgeMillis < 1 || summary.AdmissionMissesByConstraint["public_budget"] != 1 || summary.MaxSessionPublicInUse != 1 {
		t.Fatalf("capacity summary age/admission/session = %+v", summary)
	}
	if summary.ReclaimAttempts != 2 || summary.ReclaimSuccesses != 1 || summary.ReclaimNoCandidate != 1 || summary.FallbackAttempts != 3 || summary.FallbackRecovered != 2 || summary.FallbackFailed != 1 || summary.TerminalCapacityFailures != 1 {
		t.Fatalf("capacity recovery summary = %+v", summary)
	}
	lease.release()
}

func waitForAgentStreamCapacityWaiters(t *testing.T, manager *agentStreamCapacityManager, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if manager.snapshot().Waiters == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("capacity waiters = %d, want %d", manager.snapshot().Waiters, want)
}
