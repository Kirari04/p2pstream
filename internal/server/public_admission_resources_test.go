package server

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"p2pstream/internal/sysmetrics"
)

func TestPublicAdmissionIdleReclaimRecoversDirectSocketReservation(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer upstream.Close()

	app := NewApp(nil, nil)
	usage := sysmetrics.MemoryUsage{
		// The 90% hard watermark leaves exactly 3.75 MiB of headroom, or
		// three default 1.25 MiB admission units. The protected control slot
		// leaves two units for public external owners.
		UsedBytes:  (896 << 20) + (256 << 10),
		LimitBytes: 1000 << 20,
		Source:     "test",
	}
	app.agentStreamCapacity = newAdaptiveServerCapacityForTest(t, 65_536, &usage)
	target := directTransportPoolTestTarget(t, 71, upstream.URL, time.Second)
	transport := app.directTargetTransport(target)
	request, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, upstream.URL, nil)
	response, err := transport.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	if got := app.agentStreamCapacity.snapshot().AdaptiveExternalBytes; got != 768<<10 {
		t.Fatalf("idle direct reservation = %d, want %d", got, 768<<10)
	}

	release, ok, constrained := app.tryReservePublicResource(2<<20, 0)
	if !constrained || !ok {
		t.Fatalf("reservation after idle reclaim = ok %t constrained %t snapshot=%+v", ok, constrained, app.agentStreamCapacity.snapshot())
	}
	if got := app.agentStreamCapacity.snapshot().AdaptiveExternalBytes; got != 2<<20 {
		t.Fatalf("post-reclaim reservation = %d, want %d", got, 2<<20)
	}
	release()
	app.DirectTransports.closeAll()
	if got := app.agentStreamCapacity.snapshot().AdaptiveExternalBytes; got != 0 {
		t.Fatalf("released reservation = %d, want zero", got)
	}
}

func TestPublicAdmissionReclaimCooldownBoundsSequentialMisses(t *testing.T) {
	usage := sysmetrics.MemoryUsage{UsedBytes: 470 << 20, LimitBytes: 512 << 20, Source: "test"}
	now := time.Unix(1_000, 0)
	app := &App{
		agentStreamCapacity:       newAdaptiveServerCapacityForTest(t, 64, &usage),
		AgentTransports:           newAgentTransportPool(),
		publicAdmissionReclaimNow: func() time.Time { return now },
	}
	for range 2 {
		if _, ok, constrained := app.tryReservePublicResource(1, 0); ok || !constrained {
			t.Fatalf("critical admission = ok %t constrained %t", ok, constrained)
		}
	}
	if got := app.AgentTransports.stats().ReclaimAttempts; got != 1 {
		t.Fatalf("same-window reclaim attempts = %d, want 1", got)
	}
	now = now.Add(publicAdmissionReclaimCooldown)
	if _, ok, _ := app.tryReservePublicResource(1, 0); ok {
		t.Fatal("critical admission recovered without resource recovery")
	}
	if got := app.AgentTransports.stats().ReclaimAttempts; got != 2 {
		t.Fatalf("next-window reclaim attempts = %d, want 2", got)
	}
}

func TestPublicAdmissionMissDoesNotWaitForSweepLock(t *testing.T) {
	usage := sysmetrics.MemoryUsage{UsedBytes: 470 << 20, LimitBytes: 512 << 20, Source: "test"}
	app := &App{agentStreamCapacity: newAdaptiveServerCapacityForTest(t, 64, &usage)}
	app.publicAdmissionReclaimMu.Lock()
	defer app.publicAdmissionReclaimMu.Unlock()

	done := make(chan struct{})
	go func() {
		_, _, _ = app.tryReservePublicResource(1, 0)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("public admission waited behind an in-progress idle sweep")
	}
}

func TestPublicAdmissionFailsClosedWhenResourceSensorIsDegraded(t *testing.T) {
	manager := mustNewDefaultAgentStreamCapacityManager(64)
	config := sysmetrics.DefaultAdaptiveMemoryConfig()
	controller, err := sysmetrics.NewAdaptiveMemoryController(config, sysmetrics.MemoryUsageSamplerFunc(func() (sysmetrics.MemoryUsage, error) {
		return sysmetrics.MemoryUsage{}, errors.New("sensor unavailable")
	}))
	if err != nil {
		t.Fatal(err)
	}
	manager.enableAdaptiveMemory(controller)
	app := &App{agentStreamCapacity: manager}
	if _, ok, constrained := app.tryReservePublicResource(1, 0); ok || !constrained {
		t.Fatalf("degraded-sensor admission = ok %t constrained %t", ok, constrained)
	}
	if snapshot := manager.snapshot(); !snapshot.Adaptive || snapshot.MemoryPressure != "unknown" || snapshot.ResourceSampleError == "" {
		t.Fatalf("degraded-sensor snapshot = %+v", snapshot)
	}
}

func TestDirectIdleReclaimRotatesPastActiveOldestTransport(t *testing.T) {
	activeFinish := make(chan struct{})
	var finishOnce sync.Once
	finish := func() { finishOnce.Do(func() { close(activeFinish) }) }
	activeUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("start"))
		w.(http.Flusher).Flush()
		<-activeFinish
		_, _ = w.Write([]byte("done"))
	}))
	defer func() {
		finish()
		activeUpstream.Close()
	}()
	idleUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("idle"))
	}))
	defer idleUpstream.Close()

	app := NewApp(nil, nil)
	usage := sysmetrics.MemoryUsage{UsedBytes: 64 << 20, LimitBytes: 512 << 20, Source: "test"}
	app.agentStreamCapacity = newAdaptiveServerCapacityForTest(t, 65_536, &usage)
	activeTarget := directTransportPoolTestTarget(t, 80, activeUpstream.URL, time.Second)
	idleTarget := directTransportPoolTestTarget(t, 81, idleUpstream.URL, time.Second)

	activeRequest, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, activeUpstream.URL, nil)
	activeResponse, err := app.directTargetTransport(activeTarget).RoundTrip(activeRequest)
	if err != nil {
		t.Fatal(err)
	}
	idleRequest, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, idleUpstream.URL, nil)
	idleResponse, err := app.directTargetTransport(idleTarget).RoundTrip(idleRequest)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, idleResponse.Body)
	_ = idleResponse.Body.Close()
	if got := app.agentStreamCapacity.snapshot().AdaptiveExternalBytes; got != 2*(768<<10) {
		t.Fatalf("two direct reservations = %d, want %d", got, 2*(768<<10))
	}

	if !app.DirectTransports.reclaimOldestIdle() {
		t.Fatal("first bounded reclaim found no transport")
	}
	if got := app.agentStreamCapacity.snapshot().AdaptiveExternalBytes; got != 2*(768<<10) {
		t.Fatalf("active-only reclaim released live work: %d", got)
	}
	if !app.DirectTransports.reclaimOldestIdle() {
		t.Fatal("second bounded reclaim found no transport")
	}
	if got := app.agentStreamCapacity.snapshot().AdaptiveExternalBytes; got != 768<<10 {
		t.Fatalf("rotated reclaim left idle reservation: %d", got)
	}

	finish()
	data, err := io.ReadAll(activeResponse.Body)
	if err != nil || string(data) != "startdone" {
		t.Fatalf("active response after reclaim = %q, %v", data, err)
	}
	_ = activeResponse.Body.Close()
	app.DirectTransports.closeAll()
}
