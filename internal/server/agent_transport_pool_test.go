package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"p2pstream/internal/tunnel"
)

func TestAgentTransportPoolReusesPublicRouteTargetConnection(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = w.Write([]byte("ok"))
	}))
	defer upstream.Close()

	app, target, agent, fake := newAgentProxyTunnelTestApp(t, 7, upstream.URL, 2*time.Second)
	for i := 0; i < 3; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "http://public.test/reuse", nil)
		proxyAgentTargetForTest(app, rec, req, target, agent)
		if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
			t.Fatalf("request %d response = status %d body %q, want 200 ok", i, rec.Code, rec.Body.String())
		}
	}

	fake.waitOpenRequestCount(t, 1)
	if got := app.AgentTransports.len(); got != 1 {
		t.Fatalf("agent transport pool len = %d, want 1", got)
	}
}

func TestAgentTransportPoolPressureReclaimPrefersSessionAndScalesWithDemand(t *testing.T) {
	pool := newAgentTransportPool()
	preferred := &AgentConn{AgentID: 7}
	other := &AgentConn{AgentID: 8}
	now := time.Now()
	for index, agent := range []*AgentConn{preferred, preferred, other, other} {
		key := agentTransportKey{Kind: agentTransportKindRouteTarget, AgentID: agent.AgentID, RouteTargetID: int64(index + 1)}
		pool.entries[key] = &pooledAgentTransport{
			pool:      pool,
			key:       key,
			agent:     agent,
			transport: &http.Transport{},
			lastUsed:  now.Add(time.Duration(index-10) * time.Second),
			idleHint:  true,
		}
	}

	for remainingPreferred := 1; remainingPreferred >= 0; remainingPreferred-- {
		if !pool.reclaimOldestIdle(preferred, true) {
			t.Fatal("session-preferred pressure reclaim found no candidate")
		}
		gotPreferred := 0
		for _, entry := range pool.entries {
			if entry.agent == preferred {
				gotPreferred++
			}
		}
		if gotPreferred != remainingPreferred {
			t.Fatalf("preferred session entries after reclaim = %d, want %d", gotPreferred, remainingPreferred)
		}
	}
	for remaining := 1; remaining >= 0; remaining-- {
		if !pool.reclaimOldestIdle(preferred, true) {
			t.Fatal("demand-driven global pressure reclaim found no candidate")
		}
		if got := len(pool.entries); got != remaining {
			t.Fatalf("entries after demand-driven reclaim = %d, want %d", got, remaining)
		}
	}
	if pool.reclaimOldestIdle(preferred, true) {
		t.Fatal("empty pool reported a reclaimed idle shard")
	}
}

func TestAgentTransportPoolReusesWarmConnectionForBodyRequest(t *testing.T) {
	var receivedBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		receivedBody = string(body)
		_, _ = w.Write([]byte("ok"))
	}))
	defer upstream.Close()

	app, target, agent, fake := newAgentProxyTunnelTestApp(t, 7, upstream.URL, 2*time.Second)
	warm := httptest.NewRecorder()
	proxyAgentTargetForTest(app, warm, httptest.NewRequest(http.MethodGet, "http://public.test/warm", nil), target, agent)
	if warm.Code != http.StatusOK {
		t.Fatalf("warm request status = %d, want 200", warm.Code)
	}

	post := httptest.NewRecorder()
	proxyAgentTargetForTest(app, post, httptest.NewRequest(http.MethodPost, "http://public.test/post", strings.NewReader("payload")), target, agent)
	if post.Code != http.StatusOK || receivedBody != "payload" {
		t.Fatalf("POST response/body = %d/%q, want 200/payload", post.Code, receivedBody)
	}
	fake.waitOpenRequestCount(t, 1)
}

func TestAgentTransportPoolPressureReclaimsIdleShardAndSafelyHandsOffBody(t *testing.T) {
	holdOneShot := make(chan struct{})
	holdPooled := make(chan struct{})
	oneShotStarted := make(chan struct{}, 1)
	pooledStarted := make(chan struct{}, 1)
	var releaseOnce sync.Once
	releaseHolds := func() {
		releaseOnce.Do(func() {
			close(holdOneShot)
			close(holdPooled)
		})
	}
	defer releaseHolds()
	var coldRequests int
	var coldBody string
	var upstreamMu sync.Mutex
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/hold-one-shot":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("holding"))
			w.(http.Flusher).Flush()
			oneShotStarted <- struct{}{}
			<-holdOneShot
		case "/hold-pooled":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("holding"))
			w.(http.Flusher).Flush()
			pooledStarted <- struct{}{}
			<-holdPooled
		case "/cold":
			body, _ := io.ReadAll(r.Body)
			upstreamMu.Lock()
			coldRequests++
			coldBody = string(body)
			upstreamMu.Unlock()
			_, _ = w.Write([]byte("cold-ok"))
		default:
			_, _ = w.Write([]byte("warm"))
		}
	}))
	defer func() {
		releaseHolds()
		upstream.Close()
	}()

	app, targetA, agent, fake := newAgentProxyTunnelTestApp(t, 7, upstream.URL, 2*time.Second)
	manager, err := newAgentStreamCapacityManager(agentStreamCapacityConfig{
		Total: 3, Public: 3, Pooled: 2,
		MaxWaiters: 4, MaxWaitersPerKey: 2, MaxOpeningPerSession: 3,
	})
	if err != nil {
		t.Fatalf("new stream capacity manager: %v", err)
	}
	app.agentStreamCapacity = manager
	targetB := targetA
	targetB.ID++

	for _, target := range []publicRouteTargetConfig{targetA, targetB} {
		rec := httptest.NewRecorder()
		proxyAgentTargetForTest(app, rec, httptest.NewRequest(http.MethodGet, "http://public.test/warm", nil), target, agent)
		if rec.Code != http.StatusOK {
			t.Fatalf("warm target %d status = %d", target.ID, rec.Code)
		}
	}
	waitForAgentStreamCapacityUsage(t, app, 2)

	oneShotReq := httptest.NewRequest(http.MethodGet, upstream.URL+"/hold-one-shot", nil)
	oneShotResp, err := app.agentTargetOneShotTransport(agent, targetA).RoundTrip(oneShotReq)
	if err != nil {
		t.Fatalf("one-shot hold round trip: %v", err)
	}
	<-oneShotStarted

	pooledReq := httptest.NewRequest(http.MethodGet, upstream.URL+"/hold-pooled", nil)
	pooledResp, err := app.agentTargetTransport(agent, targetA).RoundTrip(pooledReq)
	if err != nil {
		t.Fatalf("pooled hold round trip: %v", err)
	}
	<-pooledStarted
	waitForAgentStreamCapacityUsage(t, app, 3)

	cold := httptest.NewRecorder()
	proxyAgentTargetForTest(app, cold, httptest.NewRequest(http.MethodPost, "http://public.test/cold", strings.NewReader("payload")), targetA, agent)
	if cold.Code != http.StatusOK || cold.Body.String() != "cold-ok" {
		t.Fatalf("cold pressure response = %d/%q, want 200/cold-ok", cold.Code, cold.Body.String())
	}
	upstreamMu.Lock()
	gotRequests, gotBody := coldRequests, coldBody
	upstreamMu.Unlock()
	if gotRequests != 1 || gotBody != "payload" {
		t.Fatalf("cold upstream deliveries/body = %d/%q, want exactly 1/payload", gotRequests, gotBody)
	}
	stats := app.AgentTransports.stats()
	if stats.ReclaimAttempts != 1 || stats.ReclaimSuccesses != 1 || stats.ReclaimNoCandidate != 0 || stats.FallbackAttempts != 1 || stats.FallbackRecovered != 1 || stats.FallbackFailed != 0 || stats.TerminalCapacityFailure != 0 {
		t.Fatalf("pressure recovery stats = %+v, want one successful reclaim and fallback", stats)
	}
	fake.waitOpenRequestCount(t, 4)

	releaseHolds()
	_ = oneShotResp.Body.Close()
	_ = pooledResp.Body.Close()
	app.AgentTransports.closeAll()
	waitForAgentStreamCapacityUsage(t, app, 0)
}

func waitForAgentStreamCapacityUsage(t *testing.T, app *App, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if got := app.agentStreamCapacity.snapshot().Total.InUse; got == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	snapshot := app.agentStreamCapacity.snapshot()
	t.Fatalf("agent stream capacity usage = total=%d public=%d pooled=%d control=%d states=%+v, want total %d", snapshot.Total.InUse, snapshot.Public.InUse, snapshot.Pooled.InUse, snapshot.Control.InUse, snapshot.States, want)
}

func waitForAgentStreamCapacityUsageRange(t *testing.T, app *App, minimum, maximum int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		snapshot := app.agentStreamCapacity.snapshot()
		if snapshot.States.Closing == 0 && snapshot.Total.InUse >= minimum && snapshot.Total.InUse <= maximum {
			return
		}
		time.Sleep(time.Millisecond)
	}
	snapshot := app.agentStreamCapacity.snapshot()
	t.Fatalf("agent stream capacity usage = total=%d states=%+v, want stable range %d..%d", snapshot.Total.InUse, snapshot.States, minimum, maximum)
}

func TestAgentTransportPoolIdleStreamUsesPooledBudgetAndOneShotHeadroom(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer upstream.Close()

	app, firstTarget, agent, fake := newAgentProxyTunnelTestApp(t, 7, upstream.URL, 2*time.Second)
	capacity, err := newAgentStreamCapacityManager(agentStreamCapacityConfig{
		Total: 2, Public: 2, Pooled: 1, Control: 0,
		MaxWaiters: 2, MaxWaitersPerKey: 1, MaxOpeningPerSession: 1,
	})
	if err != nil {
		t.Fatalf("new stream capacity manager: %v", err)
	}
	app.agentStreamCapacity = capacity

	first := httptest.NewRecorder()
	proxyAgentTargetForTest(app, first, httptest.NewRequest(http.MethodGet, "http://public.test/first", nil), firstTarget, agent)
	if first.Code != http.StatusOK {
		t.Fatalf("first request status = %d body=%q, want 200", first.Code, first.Body.String())
	}
	fake.waitOpenRequestCount(t, 1)
	if got := capacity.snapshot(); got.Total.InUse != 1 || got.Pooled.InUse != 1 {
		t.Fatalf("capacity after pooled request = total %d pooled %d, want 1/1", got.Total.InUse, got.Pooled.InUse)
	}

	secondTarget := firstTarget
	secondTarget.ID++
	secondTarget.Name = "second-target"
	second := httptest.NewRecorder()
	proxyAgentTargetForTest(app, second, httptest.NewRequest(http.MethodGet, "http://public.test/second", nil), secondTarget, agent)
	if second.Code != http.StatusOK {
		t.Fatalf("second target status = %d body=%q, want 200 via one-shot headroom", second.Code, second.Body.String())
	}
	fake.waitOpenRequestCount(t, 2)
	if got := capacity.snapshot(); got.Total.InUse > 1 || got.Pooled.InUse > 1 || got.Control.InUse != 0 {
		t.Fatalf("capacity after cold target = total %d pooled %d control %d, want at most one draining or pooled stream", got.Total.InUse, got.Pooled.InUse, got.Control.InUse)
	}
	if !app.TargetHealth.agentAvailable(secondTarget.ID, agent.AgentID) {
		t.Fatal("server stream capacity should not mark the agent passively unhealthy")
	}

	// The bounded shard pool retires the older idle target before admitting the
	// second key. Depending on FIN scheduling, the new request either acquires
	// the released pooled permit or safely uses one-shot headroom.
	app.AgentTransports.closeRouteTarget(firstTarget.ID)
	app.AgentTransports.closeRouteTarget(secondTarget.ID)
	waitForAgentStreamCapacityUsage(t, app, 0)

	third := httptest.NewRecorder()
	proxyAgentTargetForTest(app, third, httptest.NewRequest(http.MethodGet, "http://public.test/third", nil), secondTarget, agent)
	if third.Code != http.StatusOK {
		t.Fatalf("request after pool close status = %d body=%q, want 200", third.Code, third.Body.String())
	}
	fake.waitOpenRequestCount(t, 3)
}

func TestAgentTransportPoolDefaultReservedHeadroomKeepsColdTargetAvailableAcrossFourAgents(t *testing.T) {
	suppressInfoLogsForTest(t)
	releaseUpstream := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(releaseUpstream) }) })
	reachedUpstream := make(chan struct{}, int(tunnel.DefaultMaxConcurrentAgentRequests))
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reachedUpstream <- struct{}{}
		<-releaseUpstream
		_, _ = w.Write([]byte("ok"))
	}))
	defer upstream.Close()

	app, firstTarget, firstAgent, firstFake := newAgentProxyTunnelTestApp(t, 7, upstream.URL, 2*time.Second)
	agents := []*AgentConn{firstAgent}
	fakes := []*fakeYamuxAgent{firstFake}
	for index := 1; index < 4; index++ {
		agentID := int64(7 + index)
		agent, fake := newFakeYamuxAgent(t, agentID, fmt.Sprintf("agent-capacity-%d", index+1))
		if err := app.AgentHub.connect(agent); err != nil {
			t.Fatalf("connect agent %d: %v", index+1, err)
		}
		t.Cleanup(func() { app.AgentHub.disconnect(agent) })
		agents = append(agents, agent)
		fakes = append(fakes, fake)
	}

	initialCapacity := app.agentStreamCapacity.snapshot()
	requestCount := initialCapacity.Public.Capacity
	if requestCount != 60 || initialCapacity.Total.Capacity != int(tunnel.DefaultMaxConcurrentAgentRequests) || initialCapacity.Pooled.Capacity != 45 || initialCapacity.Control.Capacity != 4 {
		t.Fatalf("default stream budgets = total=%d public=%d pooled=%d control=%d, want 64/60/45/4", initialCapacity.Total.Capacity, requestCount, initialCapacity.Pooled.Capacity, initialCapacity.Control.Capacity)
	}
	if requestCount%len(agents) != 0 {
		t.Fatalf("request count %d is not divisible across %d agents", requestCount, len(agents))
	}

	statuses := make(chan int, requestCount)
	var requests sync.WaitGroup
	for index := 0; index < requestCount; index++ {
		requests.Add(1)
		agent := agents[index%len(agents)]
		go func(index int) {
			defer requests.Done()
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("http://public.test/burst/%d", index), nil)
			proxyAgentTargetForTest(app, rec, req, firstTarget, agent)
			statuses <- rec.Code
		}(index)
	}

	burstDeadline := time.NewTimer(5 * time.Second)
	defer burstDeadline.Stop()
	for index := 0; index < requestCount; index++ {
		select {
		case <-reachedUpstream:
		case <-burstDeadline.C:
			t.Fatalf("only %d/%d burst requests reached the upstream", index, requestCount)
		}
	}
	burstCapacity := app.agentStreamCapacity.snapshot()
	if burstCapacity.Total.InUse != requestCount || burstCapacity.Public.InUse != requestCount || burstCapacity.Pooled.InUse != initialCapacity.Pooled.Capacity {
		t.Fatalf("stream usage during burst = total=%d public=%d pooled=%d, want %d/%d/%d", burstCapacity.Total.InUse, burstCapacity.Public.InUse, burstCapacity.Pooled.InUse, requestCount, requestCount, initialCapacity.Pooled.Capacity)
	}

	releaseOnce.Do(func() { close(releaseUpstream) })
	requests.Wait()
	close(statuses)
	for status := range statuses {
		if status != http.StatusOK {
			t.Fatalf("burst response status = %d, want 200", status)
		}
	}
	wantIdleAfterBurst := initialCapacity.Pooled.Capacity
	waitForAgentStreamCapacityUsage(t, app, wantIdleAfterBurst)
	afterBurst := app.agentStreamCapacity.snapshot()
	if afterBurst.Public.InUse != wantIdleAfterBurst || afterBurst.Pooled.InUse != wantIdleAfterBurst {
		t.Fatalf("usage after one-shot drain = public=%d pooled=%d, want %d/%d", afterBurst.Public.InUse, afterBurst.Pooled.InUse, wantIdleAfterBurst, wantIdleAfterBurst)
	}
	for index, fake := range fakes {
		fake.waitOpenRequestCount(t, requestCount/len(fakes))
		if agents[index].Session.IsClosed() {
			t.Fatalf("agent %d disconnected after burst", index+1)
		}
	}

	app.AgentTransports.closeRouteTarget(firstTarget.ID)
	waitForAgentStreamCapacityUsage(t, app, 0)

	// Fill the pooled budget with high-cardinality targets. The manager, rather
	// than any individual shard, is the authoritative global idle-stream bound.
	for index := 0; index < initialCapacity.Pooled.Capacity; index++ {
		warmTarget := firstTarget
		warmTarget.ID = int64(10_000 + index)
		warmTarget.Name = fmt.Sprintf("pooled-target-%d", index)
		rec := httptest.NewRecorder()
		proxyAgentTargetForTest(app, rec, httptest.NewRequest(http.MethodGet, fmt.Sprintf("http://public.test/warm/%d", index), nil), warmTarget, agents[index%len(agents)])
		if rec.Code != http.StatusOK {
			t.Fatalf("warm target %d status = %d, want 200", index, rec.Code)
		}
	}
	waitForAgentStreamCapacityUsage(t, app, initialCapacity.Pooled.Capacity)

	// A cold target cannot acquire another pooled permit, but it can still use
	// the structurally reserved one-shot lane without disconnecting or retrying
	// another agent.
	secondTarget := firstTarget
	secondTarget.ID = 20_000
	secondTarget.Name = "cold-target-at-capacity"
	cold := httptest.NewRecorder()
	proxyAgentTargetForTest(app, cold, httptest.NewRequest(http.MethodGet, "http://public.test/cold", nil), secondTarget, agents[0])
	if cold.Code != http.StatusOK {
		t.Fatalf("cold target status with pooled budget full = %d, want 200", cold.Code)
	}
	if agents[0].Session.IsClosed() {
		t.Fatal("selected agent disconnected after local capacity rejection")
	}
	// Creating the cold key retires one oldest idle shard at the structural
	// shard boundary. If peer FIN is observed before the new pooled Dial, the
	// cold key replaces it; otherwise the request uses one-shot headroom and the
	// pool temporarily has one fewer live idle stream.
	waitForAgentStreamCapacityUsageRange(t, app, initialCapacity.Pooled.Capacity-1, initialCapacity.Pooled.Capacity)
}

func TestAgentTransportPoolConcurrentPublicRouteTargetRequestsOpenParallelStreams(t *testing.T) {
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		_, _ = w.Write([]byte("ok"))
	}))
	defer upstream.Close()

	app, target, agent, fake := newAgentProxyTunnelTestApp(t, 7, upstream.URL, 2*time.Second)
	const requestCount = 3
	var wg sync.WaitGroup
	errCh := make(chan string, requestCount)
	for i := 0; i < requestCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "http://public.test/concurrent", nil)
			proxyAgentTargetForTest(app, rec, req, target, agent)
			if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
				errCh <- rec.Body.String()
			}
		}()
	}
	fake.waitOpenRequestCount(t, requestCount)
	close(release)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("unexpected concurrent response body: %q", err)
	}
}

func TestAgentTransportPoolSeparatesAgentsAndRouteTargets(t *testing.T) {
	app := NewApp(nil, nil)
	first, firstFake := newFakeYamuxAgent(t, 7, "agent-7")
	defer firstFake.close()
	second, secondFake := newFakeYamuxAgent(t, 8, "agent-8")
	defer secondFake.close()

	target := publicRouteTargetConfig{
		ID:                            70,
		URL:                           "http://upstream.test:9000",
		UpstreamResponseHeaderTimeout: time.Second,
	}
	firstTransport := app.agentTargetTransport(first, target)
	secondAgentTransport := app.agentTargetTransport(second, target)
	if firstTransport == secondAgentTransport {
		t.Fatal("different agents shared a pooled transport")
	}

	secondTarget := target
	secondTarget.ID = 71
	secondTarget.URL = "http://upstream.test:9000"
	secondTargetTransport := app.agentTargetTransport(first, secondTarget)
	if firstTransport == secondTargetTransport {
		t.Fatal("different route target ids shared a pooled transport")
	}

	timeoutTarget := target
	timeoutTarget.UpstreamResponseHeaderTimeout = 2 * time.Second
	timeoutTransport := app.agentTargetTransport(first, timeoutTarget)
	if firstTransport == timeoutTransport {
		t.Fatal("different route target timeout config shared a pooled transport")
	}

	if got := app.AgentTransports.len(); got != 4 {
		t.Fatalf("agent transport pool len = %d, want 4", got)
	}
}

func TestAgentTransportPoolCloseRouteTargetForcesNewPublicStream(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer upstream.Close()

	app, target, agent, fake := newAgentProxyTunnelTestApp(t, 7, upstream.URL, 2*time.Second)
	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "http://public.test/reuse", nil)
		proxyAgentTargetForTest(app, rec, req, target, agent)
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d status = %d, want 200", i, rec.Code)
		}
		if i == 0 {
			fake.waitOpenRequestCount(t, 1)
			app.AgentTransports.closeRouteTarget(target.ID)
		}
	}
	fake.waitOpenRequestCount(t, 2)
}

func TestAgentTransportPoolAgentDisconnectInvalidatesPool(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer upstream.Close()

	app, target, agent, fake := newAgentProxyTunnelTestApp(t, 7, upstream.URL, 2*time.Second)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://public.test/first", nil)
	proxyAgentTargetForTest(app, rec, req, target, agent)
	if rec.Code != http.StatusOK {
		t.Fatalf("first request status = %d, want 200", rec.Code)
	}
	fake.waitOpenRequestCount(t, 1)
	if got := app.AgentTransports.len(); got != 1 {
		t.Fatalf("pool len before disconnect = %d, want 1", got)
	}

	app.AgentHub.disconnect(agent)
	fake.close()
	if got := app.AgentTransports.len(); got != 0 {
		t.Fatalf("pool len after disconnect = %d, want 0", got)
	}

	reconnected, reconnectedFake := newFakeYamuxAgent(t, 7, "agent-timeout-test")
	if err := app.AgentHub.connect(reconnected); err != nil {
		t.Fatalf("connect reconnected agent: %v", err)
	}
	t.Cleanup(func() { app.AgentHub.disconnect(reconnected) })
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "http://public.test/second", nil)
	proxyAgentTargetForTest(app, rec, req, target, reconnected)
	if rec.Code != http.StatusOK {
		t.Fatalf("second request status = %d, want 200", rec.Code)
	}
	reconnectedFake.waitOpenRequestCount(t, 1)
}

func TestAgentDialRequestIDContextFallback(t *testing.T) {
	ctx := withAgentDialRequestID(context.Background(), "request-id")
	if got := agentDialRequestID(ctx); got != "request-id" {
		t.Fatalf("agentDialRequestID = %q, want request-id", got)
	}
}
