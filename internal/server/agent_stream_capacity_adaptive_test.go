package server

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"p2pstream/internal/config"
	"p2pstream/internal/sysmetrics"
	"p2pstream/internal/tunnel"
)

type blockingAdaptiveMemorySampler struct {
	mu      sync.Mutex
	usage   sysmetrics.MemoryUsage
	entered chan struct{}
	proceed chan struct{}
	once    sync.Once
}

func TestServerAdaptiveControllerAcceptsProtocolFloorWindow(t *testing.T) {
	want := tunnel.ServerStreamBaseResourceModel()
	controller := newServerAdaptiveMemoryController(&config.Config{
		TunnelMaxStreamWindowBytes: tunnel.InitialStreamWindowSizeBytes,
	})
	if controller == nil {
		t.Fatal("protocol-floor server resource model disabled adaptive safety")
	}
	snapshot := controller.ForceRefresh(64, 0)
	if snapshot.AdmissionRole != sysmetrics.AdaptiveAdmissionRoleServerStream || snapshot.StreamChargeByte != want.MemoryBytes || snapshot.StreamChargeFD != 0 {
		t.Fatalf("protocol-floor snapshot dimensions = %+v, want charge %d/0", snapshot, want.MemoryBytes)
	}
}

func (s *blockingAdaptiveMemorySampler) SampleMemoryUsage() (sysmetrics.MemoryUsage, error) {
	s.mu.Lock()
	usage, entered, proceed := s.usage, s.entered, s.proceed
	s.mu.Unlock()
	if entered != nil {
		s.once.Do(func() { close(entered) })
		<-proceed
	}
	return usage, nil
}

func newAdaptiveServerCapacityForTest(t testing.TB, total int, usage *sysmetrics.MemoryUsage) *agentStreamCapacityManager {
	t.Helper()
	manager, err := newAgentStreamCapacityManager(agentStreamCapacityConfig{
		Total: total, Public: total - 1, Pooled: total - 1, Control: 1,
		MaxWaiters: total, MaxWaitersPerKey: total, MaxOpeningPerSession: total,
	})
	if err != nil {
		t.Fatal(err)
	}
	config := sysmetrics.DefaultAdaptiveMemoryConfig()
	model := tunnel.ServerStreamBaseResourceModel()
	config.AdmissionRole = sysmetrics.AdaptiveAdmissionRoleServerStream
	config.EstimatedBytesPerAdmission = model.MemoryBytes
	config.FileDescriptorsPerAdmission = 0
	config.SampleInterval = time.Hour
	controller, err := sysmetrics.NewAdaptiveMemoryController(config, sysmetrics.MemoryUsageSamplerFunc(func() (sysmetrics.MemoryUsage, error) {
		return *usage, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	manager.enableAdaptiveMemory(controller)
	return manager
}

func TestServerAdaptiveCapacityHealthyPathExceedsLegacyCeiling(t *testing.T) {
	usage := sysmetrics.MemoryUsage{UsedBytes: 64 << 20, LimitBytes: 512 << 20, Source: "test"}
	manager := newAdaptiveServerCapacityForTest(t, 2048, &usage)
	manager.registerSessionWithLimit("session", 2048)
	defer manager.unregisterSession("session")

	leases := make([]*agentStreamCapacityLease, 0, 300)
	for index := range 300 {
		lease, err := manager.tryAcquire(agentStreamCapacityPublicPooled, "route", "session")
		if err != nil {
			t.Fatalf("healthy admission %d: %v", index, err)
		}
		lease.markLive()
		leases = append(leases, lease)
	}
	snapshot := manager.snapshot()
	if !snapshot.Adaptive || snapshot.AdaptiveAdmissionLimit <= 300 || snapshot.Total.InUse != 300 || snapshot.MemoryPressure != "healthy" {
		t.Fatalf("healthy adaptive snapshot = %+v", snapshot)
	}
	for _, lease := range leases {
		lease.release()
	}
	assertAgentStreamCapacityClean(t, manager)
}

func TestServerOwnedResourceEnvelopeExceedsFormerCapWithPublicReservations(t *testing.T) {
	const limit = int64(4 << 30)
	usage := sysmetrics.MemoryUsage{UsedBytes: 28 * limit / 100, LimitBytes: limit, Source: "test"}
	manager := newAdaptiveServerCapacityForTest(t, 65_536, &usage)
	release, ok, constrained := manager.tryReserveAdaptiveResource(adaptiveResourceOwnerPublicConnection, 256<<20, 128)
	if !ok || !constrained {
		t.Fatalf("public reservation = ok %t constrained %t", ok, constrained)
	}
	defer release()
	snapshot := manager.snapshot()
	if snapshot.AdaptiveAdmissionRole != sysmetrics.AdaptiveAdmissionRoleServerStream || snapshot.AdaptiveStreamChargeFDs != 0 {
		t.Fatalf("server dimensions = %+v", snapshot)
	}
	if snapshot.AdaptiveAdmissionLimit <= 950 {
		t.Fatalf("allowance with public reservations = %d, want beyond former 950-stream cap", snapshot.AdaptiveAdmissionLimit)
	}
	if snapshot.AdaptiveExternalBytesByOwner[adaptiveResourceOwnerPublicConnection] != 256<<20 ||
		snapshot.AdaptiveExternalFDsByOwner[adaptiveResourceOwnerPublicConnection] != 128 ||
		snapshot.AdaptiveExternalReservationsByOwner[adaptiveResourceOwnerPublicConnection] != 1 {
		t.Fatalf("public owner ledger = %+v", snapshot)
	}
}

func TestAdaptiveResourceOwnerLedgerConservesConcurrentReceiveCredit(t *testing.T) {
	usage := sysmetrics.MemoryUsage{UsedBytes: 64 << 20, LimitBytes: 4 << 30, Source: "test"}
	manager := newAdaptiveServerCapacityForTest(t, 65_536, &usage)
	const (
		reservations = 128
		bytesEach    = int64(64 << 10)
	)
	releases := make(chan func(), reservations)
	errs := make(chan error, reservations)
	var acquire sync.WaitGroup
	for range reservations {
		acquire.Go(func() {
			release, ok, constrained := manager.tryReserveAdaptiveResource(adaptiveResourceOwnerReceiveWindow, bytesEach, 0)
			if !ok || !constrained {
				errs <- fmt.Errorf("receive credit reservation = ok %t constrained %t", ok, constrained)
				return
			}
			releases <- release
		})
	}
	acquire.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	snapshot := manager.snapshot()
	if got, want := snapshot.AdaptiveExternalBytesByOwner[adaptiveResourceOwnerReceiveWindow], reservations*bytesEach; got != want {
		t.Fatalf("receive-window bytes = %d, want %d", got, want)
	}
	if got := snapshot.AdaptiveExternalReservationsByOwner[adaptiveResourceOwnerReceiveWindow]; got != reservations {
		t.Fatalf("receive-window reservations = %d, want %d", got, reservations)
	}
	// Snapshots own their fixed arrays; mutating a prior value cannot corrupt
	// the manager's ledger or later discrepancy telemetry.
	snapshot.AdaptiveExternalBytesByOwner[adaptiveResourceOwnerReceiveWindow] = 0
	if got := manager.snapshot().AdaptiveExternalBytesByOwner[adaptiveResourceOwnerReceiveWindow]; got != reservations*bytesEach {
		t.Fatalf("snapshot mutation changed live ledger: %d", got)
	}
	close(releases)
	var cleanup sync.WaitGroup
	for release := range releases {
		cleanup.Go(release)
	}
	cleanup.Wait()
	clean := manager.snapshot()
	if clean.AdaptiveExternalBytes != 0 || clean.AdaptiveExternalReservationsByOwner[adaptiveResourceOwnerReceiveWindow] != 0 {
		t.Fatalf("released owner ledger = %+v", clean)
	}
	if err := manager.validateInvariants(); err != nil {
		t.Fatal(err)
	}
}

func TestServerAdaptiveCapacityCanUseResourcesBeyondProtocolV1FixedMaximum(t *testing.T) {
	usage := sysmetrics.MemoryUsage{UsedBytes: 128 << 20, LimitBytes: 16 << 30, Source: "test"}
	manager := newAdaptiveServerCapacityForTest(t, 65_536, &usage)
	manager.registerSessionWithLimit("session", 65_536)
	defer manager.unregisterSession("session")

	const wanted = 4_096
	leases := make([]*agentStreamCapacityLease, 0, wanted)
	for index := range wanted {
		lease, err := manager.tryAcquire(agentStreamCapacityPublicPooled, "route", "session")
		if err != nil {
			t.Fatalf("adaptive admission %d: %v", index, err)
		}
		lease.markLive()
		leases = append(leases, lease)
	}
	if snapshot := manager.snapshot(); snapshot.Total.InUse != wanted || snapshot.AdaptiveAdmissionLimit <= wanted {
		t.Fatalf("adaptive snapshot = %+v, want %d live above the old 2048 maximum", snapshot, wanted)
	}
	for _, lease := range leases {
		lease.release()
	}
	assertAgentStreamCapacityClean(t, manager)
}

func TestServerAdaptiveCapacityDrainsAtCriticalAndRecovers(t *testing.T) {
	usage := sysmetrics.MemoryUsage{UsedBytes: 64 << 20, LimitBytes: 512 << 20, Source: "test"}
	manager := newAdaptiveServerCapacityForTest(t, 32, &usage)
	manager.registerSessionWithLimit("session", 32)
	defer manager.unregisterSession("session")

	holder, err := manager.tryAcquire(agentStreamCapacityPublicOneShot, "holder", "session")
	if err != nil {
		t.Fatal(err)
	}
	holder.markLive()
	usage.UsedBytes = 470 << 20
	manager.refreshAdaptiveCapacity(true)
	if _, err := manager.tryAcquire(agentStreamCapacityPublicOneShot, "blocked", "session"); !errors.Is(err, errAgentStreamCapacityResourcePressure) {
		t.Fatalf("critical acquire error = %v, want resource pressure", err)
	}
	if err := manager.validateInvariants(); err != nil {
		t.Fatalf("lower dynamic ceiling invalidated existing lease: %v", err)
	}

	usage.UsedBytes = 300 << 20
	manager.refreshAdaptiveCapacity(true)
	recovered, err := manager.tryAcquire(agentStreamCapacityPublicOneShot, "recovered", "session")
	if err != nil {
		t.Fatalf("recovered acquire: %v", err)
	}
	recovered.release()
	holder.release()
	assertAgentStreamCapacityClean(t, manager)
}

func TestServerAdaptiveCapacityZeroSoftAllowanceRejectsAdmission(t *testing.T) {
	usage := sysmetrics.MemoryUsage{UsedBytes: 899, LimitBytes: 1000, Source: "test"}
	manager := newAdaptiveServerCapacityForTest(t, 32, &usage)
	manager.registerSessionWithLimit("session", 32)
	defer manager.unregisterSession("session")

	manager.refreshAdaptiveCapacity(true)
	snapshot := manager.snapshot()
	if snapshot.MemoryPressure != "soft" || snapshot.AdaptiveAdmissionLimit != 0 {
		t.Fatalf("near-hard snapshot = %+v, want soft with zero allowance", snapshot)
	}
	if _, err := manager.tryAcquire(agentStreamCapacityPublicOneShot, "blocked", "session"); !errors.Is(err, errAgentStreamCapacityResourcePressure) {
		t.Fatalf("zero soft allowance acquire = %v, want resource pressure", err)
	}
}

func TestServerAdaptiveCapacityProtectsTrustedHealthInsideDynamicLimit(t *testing.T) {
	usage := sysmetrics.MemoryUsage{UsedBytes: 64 << 20, LimitBytes: 512 << 20, Source: "test"}
	manager := newAdaptiveServerCapacityForTest(t, 32, &usage)
	manager.registerSessionWithLimit("session", 32)
	defer manager.unregisterSession("session")
	manager.publishAdaptiveCapacity(sysmetrics.AdaptiveMemorySnapshot{
		Generation: 100, Level: sysmetrics.MemoryPressureSoft,
		AdmissionLimit: 5, Maximum: 32,
	})

	publicLeases := make([]*agentStreamCapacityLease, 0, 4)
	for index := range 4 {
		lease, err := manager.tryAcquire(agentStreamCapacityPublicOneShot, "public", "session")
		if err != nil {
			t.Fatalf("public admission %d: %v", index, err)
		}
		publicLeases = append(publicLeases, lease)
	}
	if _, err := manager.tryAcquire(agentStreamCapacityPublicOneShot, "public-5", "session"); !errors.Is(err, errAgentStreamCapacityResourcePressure) {
		t.Fatalf("public borrowed protected dynamic control capacity: %v", err)
	}
	health, err := manager.tryAcquire(agentStreamCapacityTrustedHealth, "health", "session")
	if err != nil {
		t.Fatalf("protected health admission: %v", err)
	}
	for _, lease := range publicLeases {
		lease.release()
	}
	health.release()
	assertAgentStreamCapacityClean(t, manager)
}

func TestDefaultAdaptiveCapacityProtectsOnlyBoundedHealthReserve(t *testing.T) {
	manager := mustNewDefaultAgentStreamCapacityManager(65_536)
	usage := sysmetrics.MemoryUsage{UsedBytes: 64 << 20, LimitBytes: 512 << 20, Source: "test"}
	config := sysmetrics.DefaultAdaptiveMemoryConfig()
	config.SampleInterval = time.Hour
	controller, err := sysmetrics.NewAdaptiveMemoryController(config, sysmetrics.MemoryUsageSamplerFunc(func() (sysmetrics.MemoryUsage, error) {
		return usage, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	manager.enableAdaptiveMemory(controller)
	manager.registerSessionWithLimit("session", 65_536)
	defer manager.unregisterSession("session")
	manager.publishAdaptiveCapacity(sysmetrics.AdaptiveMemorySnapshot{
		Generation:       100,
		Level:            sysmetrics.MemoryPressureSoft,
		AdmissionLimit:   40,
		Maximum:          65_536,
		Usage:            usage,
		StreamChargeByte: config.EstimatedBytesPerAdmission,
	})

	publicLeases := make([]*agentStreamCapacityLease, 0, 36)
	for index := range 36 {
		lease, acquireErr := manager.tryAcquire(agentStreamCapacityPublicPooled, "public", "session")
		if acquireErr != nil {
			t.Fatalf("public admission %d of 36: %v", index+1, acquireErr)
		}
		lease.markLive()
		publicLeases = append(publicLeases, lease)
	}
	if _, acquireErr := manager.tryAcquire(agentStreamCapacityPublicOneShot, "public-blocked", "session"); !errors.Is(acquireErr, errAgentStreamCapacityResourcePressure) {
		t.Fatalf("37th public admission = %v, want four-slot health reserve", acquireErr)
	}
	healthLeases := make([]*agentStreamCapacityLease, 0, 4)
	for index := range 4 {
		lease, acquireErr := manager.tryAcquire(agentStreamCapacityTrustedHealth, "health", "session")
		if acquireErr != nil {
			t.Fatalf("health admission %d of 4: %v", index+1, acquireErr)
		}
		lease.markLive()
		healthLeases = append(healthLeases, lease)
	}
	for _, lease := range publicLeases {
		lease.release()
	}
	for _, lease := range healthLeases {
		lease.release()
	}
	assertAgentStreamCapacityClean(t, manager)
}

func TestAdaptivePressureReclaimOnlyRunsOnTransitions(t *testing.T) {
	tests := []struct {
		name              string
		previous, current sysmetrics.MemoryPressureLevel
		want              int
	}{
		{name: "enter soft", previous: sysmetrics.MemoryPressureHealthy, current: sysmetrics.MemoryPressureSoft, want: 8},
		{name: "remain soft", previous: sysmetrics.MemoryPressureSoft, current: sysmetrics.MemoryPressureSoft},
		{name: "enter critical", previous: sysmetrics.MemoryPressureSoft, current: sysmetrics.MemoryPressureCritical, want: 32},
		{name: "remain critical", previous: sysmetrics.MemoryPressureCritical, current: sysmetrics.MemoryPressureCritical},
		{name: "recover", previous: sysmetrics.MemoryPressureSoft, current: sysmetrics.MemoryPressureHealthy},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := adaptivePressureTransitionReclaim(test.previous, test.current); got != test.want {
				t.Fatalf("reclaim count = %d, want %d", got, test.want)
			}
		})
	}
}

func TestAdaptivePublicFairSharesLeaveCapacityForAnotherPeer(t *testing.T) {
	manager := mustNewDefaultAgentStreamCapacityManager(65_536)
	usage := sysmetrics.MemoryUsage{UsedBytes: 64 << 20, LimitBytes: 512 << 20, Source: "test"}
	config := sysmetrics.DefaultAdaptiveMemoryConfig()
	controller, err := sysmetrics.NewAdaptiveMemoryController(config, sysmetrics.MemoryUsageSamplerFunc(func() (sysmetrics.MemoryUsage, error) {
		return usage, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	manager.enableAdaptiveMemory(controller)
	manager.publishAdaptiveCapacity(sysmetrics.AdaptiveMemorySnapshot{
		Generation: 100, Level: sysmetrics.MemoryPressureHealthy,
		AdmissionLimit: 40, Maximum: 65_536, Usage: usage,
		StreamChargeByte: config.EstimatedBytesPerAdmission,
	})
	peerBytes, peerFDs, adaptive := manager.adaptiveExternalPeerLimits()
	const protectedControl = int64(defaultAgentStreamCapacityControlStreams)
	fairSlots := (int64(40) - protectedControl) * adaptivePublicFairSharePercent / 100
	wantBytes := fairSlots * config.EstimatedBytesPerAdmission
	if !adaptive || peerBytes != wantBytes {
		t.Fatalf("external peer fair share = %d adaptive=%t, want %d/true", peerBytes, adaptive, wantBytes)
	}
	wantFDs := fairSlots * 2
	if peerFDs != wantFDs {
		t.Fatalf("external peer FD fair share = %d, want %d", peerFDs, wantFDs)
	}
	clientRequests, adaptive := manager.adaptivePublicClientRequestLimit()
	wantRequests := (int64(40) - protectedControl) * adaptivePublicFairSharePercent / 100
	if !adaptive || clientRequests != wantRequests {
		t.Fatalf("client request fair share = %d adaptive=%t, want %d/true", clientRequests, adaptive, wantRequests)
	}
}

func TestAdaptivePublicClientShareKeepsOneSlotLive(t *testing.T) {
	manager := mustNewDefaultAgentStreamCapacityManager(65_536)
	usage := sysmetrics.MemoryUsage{UsedBytes: 64 << 20, LimitBytes: 512 << 20, Source: "test"}
	config := sysmetrics.DefaultAdaptiveMemoryConfig()
	controller, err := sysmetrics.NewAdaptiveMemoryController(config, sysmetrics.MemoryUsageSamplerFunc(func() (sysmetrics.MemoryUsage, error) {
		return usage, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	manager.enableAdaptiveMemory(controller)
	manager.publishAdaptiveCapacity(sysmetrics.AdaptiveMemorySnapshot{
		Generation: 100, Level: sysmetrics.MemoryPressureSoft,
		AdmissionLimit: 5, Maximum: 65_536, Usage: usage,
		StreamChargeByte: config.EstimatedBytesPerAdmission,
	})
	limit, adaptive := manager.adaptivePublicClientRequestLimit()
	if !adaptive || limit != 1 {
		t.Fatalf("one-slot adaptive client limit = %d adaptive=%t, want 1/true", limit, adaptive)
	}
	limiter := newKeyedStringRequestCapacityLimiter(512)
	release, ok := limiter.tryAcquire("192.0.2.40", limit)
	if !ok {
		t.Fatal("the sole resource-admissible public request was rejected")
	}
	if _, ok := limiter.tryAcquire("192.0.2.40", limit); ok {
		t.Fatal("one-slot adaptive client share admitted a second concurrent request")
	}
	release()
}

func TestFixedStreamCeilingDoesNotBecomeTheResourceLedgerCeiling(t *testing.T) {
	usage := sysmetrics.MemoryUsage{UsedBytes: 64 << 20, LimitBytes: 512 << 20, Source: "test"}
	manager := mustNewDefaultAgentStreamCapacityManager(1)
	config := sysmetrics.DefaultAdaptiveMemoryConfig()
	config.SampleInterval = time.Hour
	controller, err := sysmetrics.NewAdaptiveMemoryController(config, sysmetrics.MemoryUsageSamplerFunc(func() (sysmetrics.MemoryUsage, error) {
		return usage, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	manager.enableAdaptiveMemory(controller)

	snapshot := manager.snapshot()
	if snapshot.AdaptiveRawAdmissionLimit <= manager.config.Total {
		t.Fatalf("raw resource allowance = %d, want above fixed structural ceiling %d", snapshot.AdaptiveRawAdmissionLimit, manager.config.Total)
	}
	releaseExternal, ok, constrained := manager.tryReserveAdaptiveExternal(640<<10, 1)
	if !constrained || !ok {
		t.Fatalf("well-resourced fixed mode external reservation = ok %t constrained %t", ok, constrained)
	}
	lease, acquireErr := manager.tryAcquire(agentStreamCapacityPublicOneShot, "fixed", "session")
	if acquireErr != nil {
		t.Fatalf("public stream under fixed ceiling after socket reservation: %v", acquireErr)
	}
	if _, acquireErr = manager.tryAcquire(agentStreamCapacityPublicOneShot, "fixed-2", "session"); !errors.Is(acquireErr, errAgentStreamCapacityTotalBudget) {
		t.Fatalf("second public stream = %v, want structural total budget", acquireErr)
	}
	lease.release()
	releaseExternal()
	assertAgentStreamCapacityClean(t, manager)
}

func TestServerAdaptiveCapacityRejectsOutOfOrderSnapshotPublication(t *testing.T) {
	usage := sysmetrics.MemoryUsage{UsedBytes: 64 << 20, LimitBytes: 512 << 20, Source: "test"}
	manager := newAdaptiveServerCapacityForTest(t, 32, &usage)
	manager.registerSessionWithLimit("session", 32)
	defer manager.unregisterSession("session")

	critical := sysmetrics.AdaptiveMemorySnapshot{
		Generation: 100, Level: sysmetrics.MemoryPressureCritical,
		AdmissionLimit: 0, Maximum: 32, RejectNew: true,
	}
	staleHealthy := sysmetrics.AdaptiveMemorySnapshot{
		Generation: 99, Level: sysmetrics.MemoryPressureHealthy,
		AdmissionLimit: 32, Maximum: 32,
	}
	manager.publishAdaptiveCapacity(critical)
	got := manager.publishAdaptiveCapacity(staleHealthy)
	if got.Generation != critical.Generation || got.Level != sysmetrics.MemoryPressureCritical || !got.RejectNew {
		t.Fatalf("published snapshot = %+v, want newer critical snapshot", got)
	}
	if _, err := manager.tryAcquire(agentStreamCapacityPublicOneShot, "blocked", "session"); !errors.Is(err, errAgentStreamCapacityResourcePressure) {
		t.Fatalf("acquire after stale publication = %v, want resource pressure", err)
	}
}

func TestServerAdaptiveRefreshAllowsReleaseWithoutRegrantingSampledBaseline(t *testing.T) {
	manager, err := newAgentStreamCapacityManager(agentStreamCapacityConfig{
		Total: 64, Public: 63, Pooled: 63, Control: 1,
		MaxWaiters: 64, MaxWaitersPerKey: 64, MaxOpeningPerSession: 64,
	})
	if err != nil {
		t.Fatal(err)
	}
	sampler := &blockingAdaptiveMemorySampler{usage: sysmetrics.MemoryUsage{
		UsedBytes: 64 << 20, LimitBytes: 512 << 20, Source: "test",
	}}
	config := sysmetrics.DefaultAdaptiveMemoryConfig()
	config.SampleInterval = time.Hour
	controller, err := sysmetrics.NewAdaptiveMemoryController(config, sampler)
	if err != nil {
		t.Fatal(err)
	}
	manager.enableAdaptiveMemory(controller)
	manager.registerSessionWithLimit("session", 64)
	defer manager.unregisterSession("session")

	leases := make([]*agentStreamCapacityLease, 0, 16)
	for range 16 {
		lease, err := manager.tryAcquire(agentStreamCapacityPublicPooled, "route", "session")
		if err != nil {
			t.Fatal(err)
		}
		lease.markLive()
		leases = append(leases, lease)
	}
	releaseExternal, ok, constrained := manager.tryReserveAdaptiveExternal(config.EstimatedBytesPerAdmission, 0)
	if !ok || !constrained {
		t.Fatal("initial external reservation rejected")
	}
	sampler.mu.Lock()
	// Only two stream charges remain below the hard watermark. The sample's
	// old sixteen-stream floor must not become fresh credit after release.
	sampler.usage.UsedBytes = sampler.usage.LimitBytes*int64(config.HardLimitPercent)/100 - 2*config.EstimatedBytesPerAdmission
	sampler.entered = make(chan struct{})
	sampler.proceed = make(chan struct{})
	sampler.once = sync.Once{}
	entered, proceed := sampler.entered, sampler.proceed
	sampler.mu.Unlock()

	refreshed := make(chan struct{})
	go func() {
		manager.refreshAdaptiveCapacity(true)
		close(refreshed)
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("refresh did not enter blocking resource sample")
	}
	released := make(chan struct{})
	go func() {
		for _, lease := range leases {
			lease.release()
		}
		releaseExternal()
		close(released)
	}()
	select {
	case <-released:
	case <-time.After(time.Second):
		close(proceed)
		t.Fatal("resource sampling blocked lease or external reservation cleanup")
	}
	close(proceed)
	select {
	case <-refreshed:
	case <-time.After(time.Second):
		t.Fatal("resource refresh did not publish")
	}
	snapshot := manager.snapshot()
	if snapshot.AdaptiveRawAdmissionLimit != 2 || snapshot.AdaptiveExternalBytes != 0 || snapshot.Total.InUse != 0 {
		t.Fatalf("released sample retained spendable old baseline: %+v", snapshot)
	}
	lease, err := manager.tryAcquire(agentStreamCapacityPublicOneShot, "new", "session")
	if err != nil {
		t.Fatalf("one public stream plus protected control should fit: %v", err)
	}
	if _, err := manager.tryAcquire(agentStreamCapacityPublicOneShot, "overdraw", "session"); !errors.Is(err, errAgentStreamCapacityResourcePressure) {
		t.Fatalf("old sampled baseline became new credit: %v", err)
	}
	lease.release()
	assertAgentStreamCapacityClean(t, manager)
}

func TestServerAdaptiveRefreshCannotPublishReplacedController(t *testing.T) {
	usage := sysmetrics.MemoryUsage{UsedBytes: 64 << 20, LimitBytes: 512 << 20, Source: "test"}
	manager := newAdaptiveServerCapacityForTest(t, 32, &usage)
	config := sysmetrics.DefaultAdaptiveMemoryConfig()
	config.SampleInterval = time.Hour
	sampler := &blockingAdaptiveMemorySampler{usage: usage}
	oldController, err := sysmetrics.NewAdaptiveMemoryController(config, sampler)
	if err != nil {
		t.Fatal(err)
	}
	manager.enableAdaptiveMemory(oldController)
	sampler.mu.Lock()
	sampler.entered, sampler.proceed = make(chan struct{}), make(chan struct{})
	entered, proceed := sampler.entered, sampler.proceed
	sampler.mu.Unlock()
	var unblock sync.Once
	t.Cleanup(func() { unblock.Do(func() { close(proceed) }) })
	refreshed := make(chan struct{})
	go func() {
		manager.refreshAdaptiveCapacity(true)
		close(refreshed)
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("old controller did not enter sampler")
	}
	newController, err := sysmetrics.NewAdaptiveMemoryController(config, sysmetrics.MemoryUsageSamplerFunc(func() (sysmetrics.MemoryUsage, error) {
		return sysmetrics.MemoryUsage{UsedBytes: 500 << 20, LimitBytes: 512 << 20, Source: "replacement"}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	manager.enableAdaptiveMemory(newController)
	unblock.Do(func() { close(proceed) })
	select {
	case <-refreshed:
	case <-time.After(time.Second):
		t.Fatal("old resource sample did not finish")
	}
	manager.mu.Lock()
	got := manager.resourceSnapshot
	manager.mu.Unlock()
	if got.Generation != 1 || !got.RejectNew || got.Usage.Source != "replacement" {
		t.Fatalf("old controller overwrote replacement: %+v", got)
	}
}

func TestServerAdaptiveOverlappingSamplesPauseGrantsButNotCancellation(t *testing.T) {
	usage := sysmetrics.MemoryUsage{UsedBytes: 64 << 20, LimitBytes: 512 << 20, Source: "test"}
	manager := newAdaptiveServerCapacityForTest(t, 3, &usage)
	manager.registerSessionWithLimit("session", 3)
	defer manager.unregisterSession("session")
	firstEntered, secondEntered := make(chan struct{}), make(chan struct{})
	firstProceed, secondProceed := make(chan struct{}), make(chan struct{})
	var firstOnce, secondOnce sync.Once
	unblockFirst := func() { firstOnce.Do(func() { close(firstProceed) }) }
	unblockSecond := func() { secondOnce.Do(func() { close(secondProceed) }) }
	t.Cleanup(unblockFirst)
	t.Cleanup(unblockSecond)
	var calls atomic.Int32
	config := sysmetrics.DefaultAdaptiveMemoryConfig()
	config.SampleInterval = time.Hour
	controller, err := sysmetrics.NewAdaptiveMemoryController(config, sysmetrics.MemoryUsageSamplerFunc(func() (sysmetrics.MemoryUsage, error) {
		switch calls.Add(1) {
		case 2:
			close(firstEntered)
			<-firstProceed
		case 3:
			close(secondEntered)
			<-secondProceed
		}
		return usage, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	manager.enableAdaptiveMemory(controller)
	leases := make([]*agentStreamCapacityLease, 0, 2)
	for range 2 {
		lease, err := manager.tryAcquire(agentStreamCapacityPublicOneShot, "route", "session")
		if err != nil {
			t.Fatal(err)
		}
		leases = append(leases, lease)
		defer lease.release()
	}
	canceledContext, cancel := context.WithCancel(t.Context())
	defer cancel()
	canceled := acquireAgentStreamAsync(manager, canceledContext, agentStreamCapacityPublicOneShot, "cancel", "session")
	waiting := acquireAgentStreamAsync(manager, t.Context(), agentStreamCapacityPublicOneShot, "wait", "session")
	waitForAgentStreamCapacityWaiters(t, manager, 2)
	firstDone, secondDone := make(chan struct{}), make(chan struct{})
	go func() { manager.refreshAdaptiveCapacity(true); close(firstDone) }()
	select {
	case <-firstEntered:
	case <-time.After(time.Second):
		t.Fatal("first sampler did not enter")
	}
	go func() { manager.refreshAdaptiveCapacity(true); close(secondDone) }()
	deadline := time.Now().Add(time.Second)
	for {
		manager.mu.Lock()
		pending := manager.adaptiveSampling
		manager.mu.Unlock()
		if pending == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("overlapping refresh was not tracked")
		}
		time.Sleep(time.Millisecond)
	}
	leases[0].release()
	cancel()
	if result := receiveAgentStreamResult(t, canceled); !errors.Is(result.err, context.Canceled) || result.lease != nil {
		t.Fatalf("queued cancellation while sampling = %+v", result)
	}
	unblockFirst()
	select {
	case <-secondEntered:
	case <-time.After(time.Second):
		t.Fatal("second sampler did not enter")
	}
	select {
	case <-firstDone:
	case <-time.After(time.Second):
		t.Fatal("first publication did not complete")
	}
	// Emulate a newer completed sample winning publication before the final
	// older sampler can reacquire the ledger. Its stale completion must still
	// resume the queue when it clears the last in-flight guard.
	manager.mu.Lock()
	newer := manager.resourceSnapshot
	newer.Generation += 100
	manager.mu.Unlock()
	manager.publishAdaptiveCapacity(newer)
	select {
	case result := <-waiting:
		if result.lease != nil {
			result.lease.release()
		}
		t.Fatalf("first completion granted against an in-flight second sample: %+v", result)
	default:
	}
	leases[1].release()
	unblockSecond()
	select {
	case <-secondDone:
	case <-time.After(time.Second):
		t.Fatal("second publication did not complete")
	}
	receiveAgentStreamLease(t, waiting).release()
	assertAgentStreamCapacityClean(t, manager)
}

func TestServerAdaptiveAdmissionRechecksSampleStartedAfterRefresh(t *testing.T) {
	usage := sysmetrics.MemoryUsage{UsedBytes: 64 << 20, LimitBytes: 512 << 20, Source: "test"}
	manager := newAdaptiveServerCapacityForTest(t, 32, &usage)
	manager.registerSessionWithLimit("session", 32)
	defer manager.unregisterSession("session")
	config := sysmetrics.DefaultAdaptiveMemoryConfig()
	config.SampleInterval = time.Hour
	sampler := &blockingAdaptiveMemorySampler{usage: usage}
	controller, err := sysmetrics.NewAdaptiveMemoryController(config, sampler)
	if err != nil {
		t.Fatal(err)
	}
	manager.enableAdaptiveMemory(controller)
	// This is the first half of each direct admission path. A background
	// refresh starts before that path locks the ledger to spend its credit.
	manager.refreshAdaptiveCapacity(false)
	sampler.mu.Lock()
	sampler.usage.UsedBytes = 500 << 20
	sampler.entered, sampler.proceed = make(chan struct{}), make(chan struct{})
	entered, proceed := sampler.entered, sampler.proceed
	sampler.mu.Unlock()
	var once sync.Once
	unblock := func() { once.Do(func() { close(proceed) }) }
	t.Cleanup(unblock)
	refreshed := make(chan struct{})
	go func() { manager.refreshAdaptiveCapacity(true); close(refreshed) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("background sampler did not start")
	}
	spentOldSample := make(chan bool, 1)
	go func() {
		_ = manager.lockAdaptiveAdmission(context.Background())
		spentOldSample <- manager.canGrantLocked(agentStreamCapacityPublicOneShot, "session")
		manager.mu.Unlock()
	}()
	select {
	case <-spentOldSample:
		t.Fatal("direct admission did not wait for the intervening sample")
	case <-time.After(20 * time.Millisecond):
	}
	ctx, cancel := context.WithCancel(t.Context())
	canceled := make(chan error, 1)
	go func() {
		err := manager.lockAdaptiveAdmission(ctx)
		manager.mu.Unlock()
		canceled <- err
	}()
	cancel()
	select {
	case err := <-canceled:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled admission wait = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("sampling blocked admission cancellation")
	}
	unblock()
	select {
	case granted := <-spentOldSample:
		if granted {
			t.Fatal("direct admission spent old healthy credit after critical sample")
		}
	case <-time.After(time.Second):
		t.Fatal("direct admission did not resume after sample")
	}
	<-refreshed
}

func TestSessionFairnessBorrowsIdleCapacityAndYieldsUnderContention(t *testing.T) {
	manager, err := newAgentStreamCapacityManager(agentStreamCapacityConfig{
		Total: 8, Public: 8, Pooled: 8, Control: 0,
		MaxWaiters: 8, MaxWaitersPerKey: 8, MaxOpeningPerSession: 8,
		ReservedPublicForOtherSessions: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	manager.registerSessionWithLimit("a", 8)
	manager.registerSessionWithLimit("b", 8)
	defer manager.unregisterSession("a")
	defer manager.unregisterSession("b")

	holders := make([]*agentStreamCapacityLease, 0, 8)
	for range 8 {
		lease, err := manager.tryAcquire(agentStreamCapacityPublicPooled, "route-a", "a")
		if err != nil {
			t.Fatalf("idle capacity was not work-conserving: %v", err)
		}
		lease.markLive()
		holders = append(holders, lease)
	}

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	waiting := acquireAgentStreamAsync(manager, ctx, agentStreamCapacityPublicPooled, "route-b", "b")
	waitForAgentStreamCapacityWaiters(t, manager, 1)
	holders[0].release()
	result := <-waiting
	if result.err != nil || result.lease == nil {
		t.Fatalf("contending session did not receive released capacity: %+v", result)
	}
	result.lease.release()
	for _, lease := range holders[1:] {
		lease.release()
	}
	assertAgentStreamCapacityClean(t, manager)
}

func BenchmarkServerAdaptiveCapacityAcquireRelease(b *testing.B) {
	usage := sysmetrics.MemoryUsage{UsedBytes: 64 << 20, LimitBytes: 512 << 20, Source: "benchmark"}
	manager := newAdaptiveServerCapacityForTest(b, 2048, &usage)
	manager.registerSessionWithLimit("session", 2048)
	b.Cleanup(func() { manager.unregisterSession("session") })
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			lease, err := manager.tryAcquire(agentStreamCapacityPublicPooled, "route", "session")
			if err == nil {
				lease.release()
			}
		}
	})
}

func BenchmarkServerFixedCapacityAcquireRelease(b *testing.B) {
	manager, err := newAgentStreamCapacityManager(agentStreamCapacityConfig{
		Total: 2048, Public: 2047, Pooled: 2047, Control: 1,
		MaxWaiters: 2048, MaxWaitersPerKey: 2048, MaxOpeningPerSession: 2048,
	})
	if err != nil {
		b.Fatal(err)
	}
	manager.registerSessionWithLimit("session", 2048)
	b.Cleanup(func() { manager.unregisterSession("session") })
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			lease, acquireErr := manager.tryAcquire(agentStreamCapacityPublicPooled, "route", "session")
			if acquireErr == nil {
				lease.release()
			}
		}
	})
}
