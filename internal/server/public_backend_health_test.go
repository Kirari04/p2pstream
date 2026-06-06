package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/internal/db"
)

func TestPublicBackendHealthCheckURLPreservesSchemeHostAndPort(t *testing.T) {
	origin, err := url.Parse("http://127.0.0.1:8888/base?x=1")
	if err != nil {
		t.Fatalf("parse origin: %v", err)
	}
	got, err := publicBackendHealthCheckURL(publicBackendConfig{
		ParsedOrigin: origin,
		HealthCheck:  publicBackendHealthCheckConfig{Path: "/health"},
	})
	if err != nil {
		t.Fatalf("health check url: %v", err)
	}
	if got.String() != "http://127.0.0.1:8888/health" {
		t.Fatalf("health check url = %q, want http://127.0.0.1:8888/health", got.String())
	}
}

func TestDirectBackendHealthStillChecksFromServer(t *testing.T) {
	var status atomic.Int64
	status.Store(http.StatusInternalServerError)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			t.Fatalf("direct health path = %q, want /health", r.URL.Path)
		}
		w.WriteHeader(int(status.Load()))
	}))
	defer srv.Close()
	backend := testHealthBackend(t, 1, publicBackendForwardModeDirect, srv.URL)

	monitor := newPublicBackendHealthMonitor()
	snap := &publicProxySnapshot{Backends: map[int64]publicBackendConfig{1: backend}}
	monitor.reconcile(nil, snap, true)
	t.Cleanup(func() { monitor.reconcile(nil, nil, false) })

	waitForHealthStatus(t, monitor, 1, p2pstreamv1.PublicBackendHealthStatus_PUBLIC_BACKEND_HEALTH_STATUS_UNHEALTHY)
	if monitor.available(backend) {
		t.Fatal("backend should be unavailable after explicit unhealthy check")
	}
	status.Store(http.StatusOK)
	waitForHealthStatus(t, monitor, 1, p2pstreamv1.PublicBackendHealthStatus_PUBLIC_BACKEND_HEALTH_STATUS_HEALTHY)
	if !monitor.available(backend) {
		t.Fatal("backend should be available after explicit health recovery")
	}
}

func TestAgentPoolHealthCheckRunsThroughAssignedAgent(t *testing.T) {
	app := NewApp(nil, nil)
	served := make(chan struct{})
	var closeServed sync.Once
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		closeServed.Do(func() { close(served) })
		if r.Method != http.MethodHead {
			t.Fatalf("health method = %s, want HEAD", r.Method)
		}
		if r.URL.Path != "/health" {
			t.Fatalf("health request path = %s, want /health", r.URL.Path)
		}
		if r.Header.Get("X-Health") != "ok" {
			t.Fatalf("missing upstream health header, got %q", r.Header.Get("X-Health"))
		}
		if got, _, ok := r.BasicAuth(); !ok || got != "user" {
			t.Fatal("upstream basic auth was not applied")
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	backend := testHealthBackend(t, 10, publicBackendForwardModeAgentPool, upstream.URL+"/base?x=1")
	backend.HealthCheck.Method = http.MethodHead
	backend.TLSSkipVerify = true
	backend.UpstreamRequestHeaders = []publicRequestHeader{{Name: "X-Health", Value: "ok"}}
	backend.UpstreamBasicAuth = publicBackendBasicAuthConfig{Enabled: true, Username: "user", Password: "pass"}
	backend.AgentAssignments = []publicBackendAgentConfig{{BackendID: backend.ID, AgentID: 7, Position: 0, Weight: 100, Enabled: true}}
	agent, fake := newFakeYamuxAgent(t, 7, "agent-7")
	if err := app.AgentHub.connect(agent); err != nil {
		t.Fatalf("connect agent: %v", err)
	}
	defer app.AgentHub.disconnect(agent)
	defer fake.close()

	snap := &publicProxySnapshot{
		Backends: map[int64]publicBackendConfig{backend.ID: backend},
		Agents:   map[int64]publicAgentConfig{7: {ID: 7, PublicID: "agent-7", Enabled: true}},
	}
	app.BackendHealth.reconcile(app, snap, true)
	t.Cleanup(func() { app.BackendHealth.reconcile(app, nil, false) })

	select {
	case <-served:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for agent health check")
	}
	select {
	case open := <-fake.requests:
		if open.Address != backend.ParsedOrigin.Host {
			t.Fatalf("agent open address = %q, want %q", open.Address, backend.ParsedOrigin.Host)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for fake agent open request")
	}
	waitForHealthStatus(t, app.BackendHealth, backend.ID, p2pstreamv1.PublicBackendHealthStatus_PUBLIC_BACKEND_HEALTH_STATUS_HEALTHY)
}

func TestDirectHealthTraceRecordsSuccessAndFailure(t *testing.T) {
	var status atomic.Int64
	status.Store(http.StatusOK)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(int(status.Load()))
	}))
	defer srv.Close()
	backend := testHealthBackend(t, 101, publicBackendForwardModeDirect, srv.URL+"?token=secret")
	monitor := newPublicBackendHealthMonitor()
	monitor.reconcile(nil, &publicProxySnapshot{Backends: map[int64]publicBackendConfig{backend.ID: backend}}, false)

	monitor.recordDirectExplicitCheck(backend.ID, runPublicBackendHealthCheck(context.Background(), backend))
	traces, retained := monitor.listHealthTraces(backend.ID, 0, 100, false)
	if retained != 1 || len(traces) != 1 {
		t.Fatalf("trace count = retained %d len %d, want 1", retained, len(traces))
	}
	trace := traces[0]
	if trace.Outcome != p2pstreamv1.PublicBackendHealthTraceOutcome_PUBLIC_BACKEND_HEALTH_TRACE_OUTCOME_SUCCESS || trace.StatusCode != http.StatusOK {
		t.Fatalf("success trace = %+v", trace)
	}
	if trace.StatusAfter != p2pstreamv1.PublicBackendHealthStatus_PUBLIC_BACKEND_HEALTH_STATUS_HEALTHY || trace.HealthyStreakAfter != 1 {
		t.Fatalf("success trace state = %+v, want healthy streak", trace)
	}
	if strings.Contains(trace.Url, "secret") || strings.Contains(trace.Url, "token") {
		t.Fatalf("health trace URL leaked sensitive query: %q", trace.Url)
	}

	status.Store(http.StatusServiceUnavailable)
	monitor.recordDirectExplicitCheck(backend.ID, runPublicBackendHealthCheck(context.Background(), backend))
	traces, retained = monitor.listHealthTraces(backend.ID, 0, 100, false)
	if retained != 2 || len(traces) != 2 {
		t.Fatalf("trace count after failure = retained %d len %d, want 2", retained, len(traces))
	}
	trace = traces[0]
	if trace.Outcome != p2pstreamv1.PublicBackendHealthTraceOutcome_PUBLIC_BACKEND_HEALTH_TRACE_OUTCOME_FAILURE ||
		trace.ErrorKind != "unexpected_status" ||
		trace.StatusCode != http.StatusServiceUnavailable ||
		trace.UnhealthyStreakAfter != 1 {
		t.Fatalf("failure trace = %+v", trace)
	}
	failures, retained := monitor.listHealthTraces(backend.ID, 0, 100, true)
	if retained != 2 || len(failures) != 1 || failures[0].Outcome != p2pstreamv1.PublicBackendHealthTraceOutcome_PUBLIC_BACKEND_HEALTH_TRACE_OUTCOME_FAILURE {
		t.Fatalf("failure filter = retained %d traces %+v", retained, failures)
	}
}

func TestListHealthTracesReturnsDeepClonedTrace(t *testing.T) {
	if cloneHealthTrace(nil) != nil {
		t.Fatal("nil health trace clone should remain nil")
	}
	nilDebugClone := cloneHealthTrace(&p2pstreamv1.PublicBackendHealthTrace{Sequence: 1})
	if nilDebugClone == nil {
		t.Fatal("nil-debug health trace clone is nil")
	}
	if nilDebugClone.DebugAttributes != nil {
		t.Fatalf("nil debug attributes clone = %+v, want nil", nilDebugClone.DebugAttributes)
	}

	backend := testHealthBackend(t, 105, publicBackendForwardModeDirect, "http://127.0.0.1:8888")
	monitor := newPublicBackendHealthMonitor()
	monitor.reconcile(nil, &publicProxySnapshot{Backends: map[int64]publicBackendConfig{backend.ID: backend}}, false)

	attempt := newPublicBackendHealthCheckAttempt(backend)
	attempt.StatusCode = http.StatusOK
	finishPublicBackendHealthCheckAttempt(&attempt)
	monitor.recordDirectExplicitCheck(backend.ID, attempt)

	traces, retained := monitor.listHealthTraces(backend.ID, 0, 100, false)
	if retained != 1 || len(traces) != 1 {
		t.Fatalf("trace count = retained %d len %d, want 1", retained, len(traces))
	}
	if traces[0] == nil {
		t.Fatal("returned trace is nil")
	}
	if traces[0].DebugAttributes == nil {
		t.Fatal("returned trace debug attributes are nil")
	}
	if traces[0].ErrorKind != "success" {
		t.Fatalf("returned trace error kind = %q, want success", traces[0].ErrorKind)
	}
	if traces[0].DebugAttributes["transport"] != "direct" {
		t.Fatalf("returned trace transport = %q, want direct", traces[0].DebugAttributes["transport"])
	}

	traces[0].ErrorKind = "caller_mutated"
	traces[0].DebugAttributes["transport"] = "caller_mutated"
	traces[0].DebugAttributes["caller_added"] = "true"

	traces, retained = monitor.listHealthTraces(backend.ID, 0, 100, false)
	if retained != 1 || len(traces) != 1 {
		t.Fatalf("trace count after mutation = retained %d len %d, want 1", retained, len(traces))
	}
	if traces[0].ErrorKind != "success" {
		t.Fatalf("stored trace error kind = %q, want success", traces[0].ErrorKind)
	}
	if traces[0].DebugAttributes["transport"] != "direct" {
		t.Fatalf("stored trace transport = %q, want direct", traces[0].DebugAttributes["transport"])
	}
	if _, ok := traces[0].DebugAttributes["caller_added"]; ok {
		t.Fatalf("stored trace retained caller mutation: %+v", traces[0].DebugAttributes)
	}
}

func TestAgentHealthTraceRecordsSuccessAndDebugAttributes(t *testing.T) {
	app := NewApp(nil, nil)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	backend := testHealthBackend(t, 102, publicBackendForwardModeAgentPool, upstream.URL)
	backend.AgentAssignments = []publicBackendAgentConfig{{BackendID: backend.ID, AgentID: 7, Position: 0, Weight: 100, Enabled: true}}
	agent, fake := newFakeYamuxAgent(t, 7, "agent-7")
	agent.Name = "Agent Seven"
	if err := app.AgentHub.connect(agent); err != nil {
		t.Fatalf("connect agent: %v", err)
	}
	defer app.AgentHub.disconnect(agent)
	defer fake.close()
	app.BackendHealth.reconcile(app, &publicProxySnapshot{
		Backends: map[int64]publicBackendConfig{backend.ID: backend},
		Agents:   map[int64]publicAgentConfig{7: {ID: 7, PublicID: "agent-7", Name: "Agent Seven", Enabled: true}},
	}, false)

	attempt := app.runPublicBackendHealthCheckViaAgent(context.Background(), backend, agent)
	app.BackendHealth.recordAgentExplicitCheck(backend.ID, 7, attempt)
	traces, retained := app.BackendHealth.listHealthTraces(backend.ID, 7, 100, false)
	if retained != 1 || len(traces) != 1 {
		t.Fatalf("agent trace count = retained %d len %d", retained, len(traces))
	}
	trace := traces[0]
	if trace.Outcome != p2pstreamv1.PublicBackendHealthTraceOutcome_PUBLIC_BACKEND_HEALTH_TRACE_OUTCOME_SUCCESS ||
		trace.AgentId != 7 ||
		trace.AgentPublicId != "agent-7" ||
		trace.AgentName != "Agent Seven" ||
		trace.DebugAttributes["agent_request_id"] == "" ||
		trace.DebugAttributes["transport"] != "agent_pool" {
		t.Fatalf("agent trace = %+v", trace)
	}
}

func TestHealthTraceRecordsSkippedDisconnectedAgent(t *testing.T) {
	app := NewApp(nil, nil)
	backend := testHealthBackend(t, 103, publicBackendForwardModeAgentPool, "http://127.0.0.1:8888")
	backend.AgentAssignments = []publicBackendAgentConfig{{BackendID: backend.ID, AgentID: 9, Position: 0, Weight: 100, Enabled: true}}
	app.BackendHealth.reconcile(app, &publicProxySnapshot{
		Backends: map[int64]publicBackendConfig{backend.ID: backend},
		Agents:   map[int64]publicAgentConfig{9: {ID: 9, PublicID: "agent-9", Enabled: true}},
	}, false)

	app.BackendHealth.recordAgentDisconnected(backend.ID, 9)
	app.BackendHealth.recordAgentActiveCheckSkipped(backend.ID, 9, backend, "agent_disconnected", errAgentDisconnected)
	traces, _ := app.BackendHealth.listHealthTraces(backend.ID, 9, 100, false)
	if len(traces) != 1 {
		t.Fatalf("skipped trace count = %d, want 1", len(traces))
	}
	trace := traces[0]
	if trace.Outcome != p2pstreamv1.PublicBackendHealthTraceOutcome_PUBLIC_BACKEND_HEALTH_TRACE_OUTCOME_SKIPPED ||
		trace.ErrorKind != "agent_disconnected" ||
		trace.StatusCode != 0 {
		t.Fatalf("skipped trace = %+v", trace)
	}
}

func TestPassiveAndConnectivityHealthTraces(t *testing.T) {
	app, backend := testAgentPoolApp(t)
	app.BackendHealth.markAgentPassiveFailure(backend.ID, 1, errors.New("agent timeout"))
	agentOneTraces, _ := app.BackendHealth.listHealthTraces(backend.ID, 1, 100, false)
	agentTwoTraces, _ := app.BackendHealth.listHealthTraces(backend.ID, 2, 100, false)
	if len(agentOneTraces) != 1 || len(agentTwoTraces) != 0 {
		t.Fatalf("agent passive traces agent1=%d agent2=%d, want 1/0", len(agentOneTraces), len(agentTwoTraces))
	}
	if agentOneTraces[0].Source != p2pstreamv1.PublicBackendHealthTraceSource_PUBLIC_BACKEND_HEALTH_TRACE_SOURCE_PASSIVE_FAILURE ||
		agentOneTraces[0].PassiveUnhealthyUntilUnixMillis == 0 {
		t.Fatalf("passive trace = %+v", agentOneTraces[0])
	}

	app.BackendHealth.recordAgentConnected(backend.ID, 1, "agent-a")
	app.BackendHealth.recordAgentDisconnected(backend.ID, 1)
	traces, _ := app.BackendHealth.listHealthTraces(backend.ID, 1, 100, false)
	if len(traces) < 3 {
		t.Fatalf("connectivity traces = %+v, want passive plus connect/disconnect", traces)
	}
	if traces[0].Source != p2pstreamv1.PublicBackendHealthTraceSource_PUBLIC_BACKEND_HEALTH_TRACE_SOURCE_AGENT_CONNECTIVITY ||
		traces[0].ErrorKind != "agent_disconnected_event" {
		t.Fatalf("latest connectivity trace = %+v", traces[0])
	}
}

func TestDirectHealthTraceRetentionCapsPerTarget(t *testing.T) {
	backend := testHealthBackend(t, 104, publicBackendForwardModeDirect, "http://127.0.0.1:8888")
	monitor := newPublicBackendHealthMonitor()
	monitor.reconcile(nil, &publicProxySnapshot{Backends: map[int64]publicBackendConfig{backend.ID: backend}}, false)
	for range publicBackendHealthTraceLimitPerTarget + 5 {
		attempt := newPublicBackendHealthCheckAttempt(backend)
		attempt.StatusCode = http.StatusOK
		finishPublicBackendHealthCheckAttempt(&attempt)
		monitor.recordDirectExplicitCheck(backend.ID, attempt)
	}
	traces, retained := monitor.listHealthTraces(backend.ID, 0, 200, false)
	if retained != publicBackendHealthTraceLimitPerTarget || len(traces) != publicBackendHealthTraceLimitPerTarget {
		t.Fatalf("retention = retained %d len %d, want %d", retained, len(traces), publicBackendHealthTraceLimitPerTarget)
	}
	if traces[0].Sequence <= traces[len(traces)-1].Sequence {
		t.Fatalf("traces not newest first: first=%d last=%d", traces[0].Sequence, traces[len(traces)-1].Sequence)
	}
}

func TestAgentPoolHealthCheckUnhealthySkipsOnlyThatAgent(t *testing.T) {
	app, backend := testAgentPoolApp(t)
	app.BackendHealth.markAgentPassiveFailure(backend.ID, 1, nil)

	for range 10 {
		selected := app.selectBackendAgent(backend)
		if selected == nil {
			t.Fatal("expected an eligible agent")
		}
		if selected.AgentID == 1 {
			t.Fatal("unhealthy agent was selected")
		}
	}
	if !app.BackendHealth.available(backend) {
		t.Fatal("backend should remain available while another agent is eligible")
	}
}

func TestAgentPoolSelectionSkipsDisconnectedAssignments(t *testing.T) {
	app := NewApp(nil, nil)
	backend := testHealthBackend(t, 21, publicBackendForwardModeAgentPool, "http://127.0.0.1:8888")
	snap := &publicProxySnapshot{
		Backends: map[int64]publicBackendConfig{backend.ID: backend},
		Agents:   make(map[int64]publicAgentConfig),
	}
	for i := int64(1); i <= 5; i++ {
		backend.AgentAssignments = append(backend.AgentAssignments, publicBackendAgentConfig{
			BackendID: backend.ID,
			AgentID:   i,
			Position:  i - 1,
			Weight:    100,
			Enabled:   true,
		})
		snap.Agents[i] = publicAgentConfig{ID: i, PublicID: "agent-" + strconv.FormatInt(i, 10), Enabled: true}
	}
	snap.Backends[backend.ID] = backend
	app.proxyMu.Lock()
	app.publicSnapshot = snap
	app.proxyMu.Unlock()
	app.BackendHealth.reconcile(app, snap, false)
	for i := int64(1); i <= 4; i++ {
		agent := testAgentConn(i, "agent-"+strconv.FormatInt(i, 10))
		if err := app.AgentHub.connect(agent); err != nil {
			t.Fatalf("connect agent %d: %v", i, err)
		}
		t.Cleanup(func() { app.AgentHub.disconnect(agent) })
	}
	t.Cleanup(func() { app.BackendHealth.reconcile(app, nil, false) })

	for range 25 {
		selected := app.selectBackendAgent(backend)
		if selected == nil {
			t.Fatal("expected an eligible agent")
		}
		if selected.AgentID == 5 {
			t.Fatal("disconnected agent was selected")
		}
	}
}

func TestAgentPoolHealthCheckAllAgentsUnhealthyMakesBackendUnavailable(t *testing.T) {
	app, backend := testAgentPoolApp(t)
	app.BackendHealth.markAgentPassiveFailure(backend.ID, 1, nil)
	app.BackendHealth.markAgentPassiveFailure(backend.ID, 2, nil)

	if app.BackendHealth.available(backend) {
		t.Fatal("backend should be unavailable when all connected assigned agents are unhealthy")
	}
	route := publicRouteConfig{
		ID:        50,
		Enabled:   true,
		Action:    publicRouteActionForward,
		BackendID: backend.ID,
		BackendAssignments: []publicRouteBackendConfig{{
			RouteID:   50,
			BackendID: backend.ID,
			Position:  0,
			Weight:    100,
			Enabled:   true,
		}},
	}
	snap := publicProxySnapshot{
		Backends: map[int64]publicBackendConfig{backend.ID: backend},
		Agents: map[int64]publicAgentConfig{
			1: {ID: 1, PublicID: "agent-a", Enabled: true},
			2: {ID: 2, PublicID: "agent-b", Enabled: true},
		},
	}
	if _, ok, _ := app.selectRouteBackend(snap, route); ok {
		t.Fatal("route backend should be unavailable when all agents are unhealthy")
	}
}

func TestDefaultBackendRequiresEligibleAgent(t *testing.T) {
	app := NewApp(nil, nil)
	backend := testHealthBackend(t, 70, publicBackendForwardModeAgentPool, "http://127.0.0.1:8888")
	backend.AgentAssignments = []publicBackendAgentConfig{{BackendID: backend.ID, AgentID: 1, Position: 0, Weight: 100, Enabled: true}}
	snap := &publicProxySnapshot{
		Backends:  map[int64]publicBackendConfig{backend.ID: backend},
		Agents:    map[int64]publicAgentConfig{1: {ID: 1, PublicID: "agent-a", Enabled: true}},
		Listeners: map[int64]publicListenerConfig{10: {ID: 10, Enabled: true, DefaultBackendID: backend.ID}},
	}
	app.proxyMu.Lock()
	app.publicSnapshot = snap
	app.proxyMu.Unlock()
	app.BackendHealth.reconcile(app, snap, false)
	t.Cleanup(func() { app.BackendHealth.reconcile(app, nil, false) })

	req := httptest.NewRequest(http.MethodGet, "http://example.test/", nil)
	_, err := app.resolvePublicRoute(10, req)
	if !errors.Is(err, errNoRouteBackendAvailable) {
		t.Fatalf("resolve default backend error = %v, want %v", err, errNoRouteBackendAvailable)
	}
}

func TestRouteFallbackSelectedWhenPrimaryAgentPoolUnavailable(t *testing.T) {
	app := NewApp(nil, nil)
	primary := testHealthBackend(t, 80, publicBackendForwardModeAgentPool, "http://127.0.0.1:8888")
	primary.AgentAssignments = []publicBackendAgentConfig{{BackendID: primary.ID, AgentID: 1, Position: 0, Weight: 100, Enabled: true}}
	fallback := testHealthBackend(t, 81, publicBackendForwardModeDirect, "http://127.0.0.1:9999")
	route := publicRouteConfig{
		ID:                90,
		Enabled:           true,
		Action:            publicRouteActionForward,
		BackendID:         primary.ID,
		FallbackBackendID: fallback.ID,
		BackendAssignments: []publicRouteBackendConfig{{
			RouteID:   90,
			BackendID: primary.ID,
			Position:  0,
			Weight:    100,
			Enabled:   true,
		}},
	}
	snap := publicProxySnapshot{
		Backends: map[int64]publicBackendConfig{
			primary.ID:  primary,
			fallback.ID: fallback,
		},
		Agents: map[int64]publicAgentConfig{1: {ID: 1, PublicID: "agent-a", Enabled: true}},
	}
	app.BackendHealth.reconcile(app, &snap, false)
	t.Cleanup(func() { app.BackendHealth.reconcile(app, nil, false) })

	selected, ok, fallbackSelected := app.selectRouteBackend(snap, route)
	if !ok || !fallbackSelected || selected.ID != fallback.ID {
		t.Fatalf("route backend selection = backend=%d ok=%v fallback=%v, want fallback %d", selected.ID, ok, fallbackSelected, fallback.ID)
	}
}

func TestAgentPassiveFailureAppliesWhenHealthCheckEnabled(t *testing.T) {
	app, backend := testAgentPoolApp(t)
	app.BackendHealth.markAgentPassiveFailure(backend.ID, 1, nil)

	if app.BackendHealth.agentAvailable(backend.ID, 1) {
		t.Fatal("agent 1 should be unavailable during passive cooldown")
	}
	if !app.BackendHealth.agentAvailable(backend.ID, 2) {
		t.Fatal("agent 2 should remain available")
	}
	if !app.BackendHealth.available(backend) {
		t.Fatal("backend should remain available while agent 2 is eligible")
	}
}

func TestAgentReconnectClearsPassiveCooldownToUnknown(t *testing.T) {
	app := NewApp(nil, nil)
	backend := testHealthBackend(t, 22, publicBackendForwardModeAgentPool, "http://127.0.0.1:8888")
	backend.AgentAssignments = []publicBackendAgentConfig{{BackendID: backend.ID, AgentID: 1, Position: 0, Weight: 100, Enabled: true}}
	snap := &publicProxySnapshot{
		Backends: map[int64]publicBackendConfig{backend.ID: backend},
		Agents:   map[int64]publicAgentConfig{1: {ID: 1, PublicID: "agent-a", Enabled: true}},
	}
	app.BackendHealth.reconcile(app, snap, false)
	agent := testAgentConn(1, "agent-a")
	if err := app.AgentHub.connect(agent); err != nil {
		t.Fatalf("connect agent: %v", err)
	}
	app.BackendHealth.recordAgentConnected(backend.ID, 1, "agent-a")
	app.BackendHealth.markAgentPassiveFailure(backend.ID, 1, nil)
	if app.BackendHealth.agentAvailable(backend.ID, 1) {
		t.Fatal("agent should be unavailable during passive cooldown")
	}
	app.AgentHub.disconnect(agent)
	app.BackendHealth.recordAgentDisconnected(backend.ID, 1)
	reconnected := testAgentConn(1, "agent-a")
	if err := app.AgentHub.connect(reconnected); err != nil {
		t.Fatalf("reconnect agent: %v", err)
	}
	t.Cleanup(func() { app.AgentHub.disconnect(reconnected) })
	t.Cleanup(func() { app.BackendHealth.reconcile(app, nil, false) })

	app.BackendHealth.recordAgentConnected(backend.ID, 1, "agent-a")
	snapshot := app.BackendHealth.agentSnapshot(backend.ID, 1, true, true)
	if snapshot == nil || snapshot.Status != p2pstreamv1.PublicBackendHealthStatus_PUBLIC_BACKEND_HEALTH_STATUS_UNKNOWN || !snapshot.Available || snapshot.PassiveUnhealthyUntilUnixMillis != 0 {
		t.Fatalf("agent snapshot after reconnect = %+v, want available UNKNOWN without passive cooldown", snapshot)
	}
}

func TestPublicBackendAgentProtoIncludesHealth(t *testing.T) {
	app := NewApp(nil, nil)
	backend := testHealthBackend(t, 23, publicBackendForwardModeAgentPool, "http://127.0.0.1:8888")
	backend.AgentAssignments = []publicBackendAgentConfig{{BackendID: backend.ID, AgentID: 1, Position: 0, Weight: 100, Enabled: true}}
	snap := &publicProxySnapshot{
		Backends: map[int64]publicBackendConfig{backend.ID: backend},
		Agents:   map[int64]publicAgentConfig{1: {ID: 1, PublicID: "agent-a", Enabled: true}},
	}
	app.BackendHealth.reconcile(app, snap, false)
	agent := testAgentConn(1, "agent-a")
	agent.ActiveRequests.Store(3)
	if err := app.AgentHub.connect(agent); err != nil {
		t.Fatalf("connect agent: %v", err)
	}
	t.Cleanup(func() { app.AgentHub.disconnect(agent) })
	t.Cleanup(func() { app.BackendHealth.reconcile(app, nil, false) })

	assignments := publicBackendAgentsToProto([]db.PublicBackendAgent{{
		BackendID: backend.ID,
		AgentID:   1,
		Position:  0,
		Weight:    100,
		Enabled:   1,
	}}, map[int64]bool{1: true}, app.BackendHealth)
	if len(assignments) != 1 || assignments[0].Health == nil {
		t.Fatalf("agent assignments = %+v, want health", assignments)
	}
	health := assignments[0].Health
	if !health.Connected || !health.Available || health.Status != p2pstreamv1.PublicBackendHealthStatus_PUBLIC_BACKEND_HEALTH_STATUS_UNKNOWN || health.ActiveRequests != 3 {
		t.Fatalf("agent health = %+v, want connected available UNKNOWN with active requests", health)
	}
}

func TestAgentPassiveFailureIgnoredWhenHealthCheckDisabled(t *testing.T) {
	app, backend := testAgentPoolAppWithHealth(t, false)
	app.BackendHealth.markAgentPassiveFailure(backend.ID, 1, nil)

	if !app.BackendHealth.agentAvailable(backend.ID, 1) {
		t.Fatal("agent 1 should remain available when health checks are disabled")
	}
	if !app.BackendHealth.available(backend) {
		t.Fatal("backend should remain available when health checks are disabled")
	}
}

func TestAgentHealthCheckDisconnectedAgentIsUnknownNotUnhealthy(t *testing.T) {
	app := NewApp(nil, nil)
	backend := testHealthBackend(t, 40, publicBackendForwardModeAgentPool, "http://127.0.0.1:8888")
	backend.AgentAssignments = []publicBackendAgentConfig{{BackendID: backend.ID, AgentID: 99, Position: 0, Weight: 100, Enabled: true}}
	snap := &publicProxySnapshot{
		Backends: map[int64]publicBackendConfig{backend.ID: backend},
		Agents:   map[int64]publicAgentConfig{99: {ID: 99, PublicID: "agent-missing", Enabled: true}},
	}
	app.BackendHealth.reconcile(app, snap, true)
	t.Cleanup(func() { app.BackendHealth.reconcile(app, nil, false) })

	snapshot := app.BackendHealth.snapshot(publicBackendHealthDBAdapter{id: backend.ID, enabled: true})
	if snapshot == nil || snapshot.Status != p2pstreamv1.PublicBackendHealthStatus_PUBLIC_BACKEND_HEALTH_STATUS_UNKNOWN {
		t.Fatalf("disconnected agent aggregate status = %+v, want UNKNOWN", snapshot)
	}
	if app.BackendHealth.available(backend) {
		t.Fatal("backend route eligibility should still require a connected agent")
	}
}

func TestPassiveFailureIgnoredWhenHealthCheckDisabledDirect(t *testing.T) {
	monitor := newPublicBackendHealthMonitor()
	backend := testHealthBackend(t, 2, publicBackendForwardModeDirect, "http://127.0.0.1:8080")
	backend.HealthCheck.Enabled = false
	snap := &publicProxySnapshot{Backends: map[int64]publicBackendConfig{backend.ID: backend}}
	monitor.reconcile(nil, snap, false)

	monitor.markPassiveFailure(backend.ID, nil)
	if !monitor.available(backend) {
		t.Fatal("backend should remain available when health checks are disabled")
	}
	snapshot := monitor.snapshot(publicBackendHealthDBAdapter{id: backend.ID, enabled: true})
	if snapshot == nil || snapshot.Status != p2pstreamv1.PublicBackendHealthStatus_PUBLIC_BACKEND_HEALTH_STATUS_UNKNOWN || snapshot.PassiveUnhealthyUntilUnixMillis != 0 {
		t.Fatalf("unexpected disabled health snapshot: %+v", snapshot)
	}
}

func TestPassiveFailureAppliesWhenHealthCheckEnabledDirect(t *testing.T) {
	monitor := newPublicBackendHealthMonitor()
	backend := testHealthBackend(t, 3, publicBackendForwardModeDirect, "http://127.0.0.1:8080")
	snap := &publicProxySnapshot{Backends: map[int64]publicBackendConfig{backend.ID: backend}}
	monitor.reconcile(nil, snap, false)

	monitor.markPassiveFailure(backend.ID, nil)
	if monitor.available(backend) {
		t.Fatal("backend should be unavailable during passive cooldown")
	}
	snapshot := monitor.snapshot(publicBackendHealthDBAdapter{id: backend.ID, enabled: true})
	if snapshot == nil || snapshot.Status != p2pstreamv1.PublicBackendHealthStatus_PUBLIC_BACKEND_HEALTH_STATUS_UNHEALTHY || snapshot.PassiveUnhealthyUntilUnixMillis == 0 {
		t.Fatalf("unexpected passive health snapshot: %+v", snapshot)
	}
}

func TestDisablingHealthChecksClearsPassiveState(t *testing.T) {
	monitor := newPublicBackendHealthMonitor()
	backend := testHealthBackend(t, 4, publicBackendForwardModeDirect, "http://127.0.0.1:8080")
	monitor.reconcile(nil, &publicProxySnapshot{Backends: map[int64]publicBackendConfig{backend.ID: backend}}, false)
	monitor.markPassiveFailure(backend.ID, nil)
	if monitor.available(backend) {
		t.Fatal("backend should be unavailable while health checks are enabled")
	}

	disabled := backend
	disabled.HealthCheck.Enabled = false
	monitor.reconcile(nil, &publicProxySnapshot{Backends: map[int64]publicBackendConfig{disabled.ID: disabled}}, false)
	if !monitor.available(disabled) {
		t.Fatal("backend should become available after disabling health checks")
	}
	snapshot := monitor.snapshot(publicBackendHealthDBAdapter{id: disabled.ID, enabled: true})
	if snapshot == nil || snapshot.Status != p2pstreamv1.PublicBackendHealthStatus_PUBLIC_BACKEND_HEALTH_STATUS_UNKNOWN || snapshot.PassiveUnhealthyUntilUnixMillis != 0 {
		t.Fatalf("unexpected snapshot after disabling health checks: %+v", snapshot)
	}
}

func TestPassiveCooldownExpiryStillRecoversWhenHealthEnabled(t *testing.T) {
	monitor := newPublicBackendHealthMonitor()
	backend := testHealthBackend(t, 5, publicBackendForwardModeDirect, "http://127.0.0.1:8080")
	monitor.reconcile(nil, &publicProxySnapshot{Backends: map[int64]publicBackendConfig{backend.ID: backend}}, false)
	monitor.markPassiveFailure(backend.ID, nil)
	if monitor.available(backend) {
		t.Fatal("backend should be unavailable during passive cooldown")
	}

	monitor.mu.Lock()
	monitor.states[backend.ID].direct.passiveUnhealthyUntil = time.Now().Add(-time.Second)
	monitor.mu.Unlock()
	if !monitor.available(backend) {
		t.Fatal("backend should recover after passive cooldown expires")
	}
}

func TestCancelledActiveHealthCheckIsSkippedWithoutUnhealthyStreak(t *testing.T) {
	monitor := newPublicBackendHealthMonitor()
	backend := testHealthBackend(t, 55, publicBackendForwardModeDirect, "http://127.0.0.1:8080")
	backend.HealthCheck.UnhealthyThreshold = 1
	monitor.reconcile(nil, &publicProxySnapshot{Backends: map[int64]publicBackendConfig{backend.ID: backend}}, false)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	attempt := runPublicBackendHealthCheck(ctx, backend)
	if !attempt.Skipped || attempt.ErrorKind != "health_check_cancelled" {
		t.Fatalf("cancelled attempt skipped=%v errorKind=%q err=%v, want skipped health_check_cancelled", attempt.Skipped, attempt.ErrorKind, attempt.Err)
	}
	monitor.recordDirectExplicitCheck(backend.ID, attempt)

	monitor.mu.Lock()
	state := monitor.states[backend.ID].direct
	monitor.mu.Unlock()
	if state.unhealthyStreak != 0 || state.explicitStatus != p2pstreamv1.PublicBackendHealthStatus_PUBLIC_BACKEND_HEALTH_STATUS_UNKNOWN {
		t.Fatalf("state after cancelled check = %+v, want no unhealthy streak and UNKNOWN", state)
	}
	traces, _ := monitor.listHealthTraces(backend.ID, 0, 10, false)
	if len(traces) != 1 ||
		traces[0].Outcome != p2pstreamv1.PublicBackendHealthTraceOutcome_PUBLIC_BACKEND_HEALTH_TRACE_OUTCOME_SKIPPED ||
		traces[0].ErrorKind != "health_check_cancelled" ||
		traces[0].DebugAttributes["cancel_source"] != "health_monitor" {
		t.Fatalf("cancelled trace = %+v", traces)
	}
}

func TestActiveHealthCheckTimeoutIncrementsUnhealthyStreak(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	monitor := newPublicBackendHealthMonitor()
	backend := testHealthBackend(t, 56, publicBackendForwardModeDirect, upstream.URL)
	backend.HealthCheck.Timeout = time.Millisecond
	backend.HealthCheck.UnhealthyThreshold = 1
	monitor.reconcile(nil, &publicProxySnapshot{Backends: map[int64]publicBackendConfig{backend.ID: backend}}, false)

	attempt := runPublicBackendHealthCheck(context.Background(), backend)
	if attempt.Skipped || attempt.ErrorKind != "health_check_timeout" {
		t.Fatalf("timeout attempt skipped=%v errorKind=%q err=%v, want health_check_timeout", attempt.Skipped, attempt.ErrorKind, attempt.Err)
	}
	monitor.recordDirectExplicitCheck(backend.ID, attempt)

	monitor.mu.Lock()
	state := monitor.states[backend.ID].direct
	monitor.mu.Unlock()
	if state.unhealthyStreak != 1 || state.explicitStatus != p2pstreamv1.PublicBackendHealthStatus_PUBLIC_BACKEND_HEALTH_STATUS_UNHEALTHY {
		t.Fatalf("state after timeout = %+v, want unhealthy streak 1 and UNHEALTHY", state)
	}
}

func TestRouteKeepsBackendEligibleAfterPassiveFailureWhenHealthDisabled(t *testing.T) {
	app := NewApp(nil, nil)
	backend := testHealthBackend(t, 6, publicBackendForwardModeDirect, "http://127.0.0.1:8080")
	backend.HealthCheck.Enabled = false
	snap := publicProxySnapshot{Backends: map[int64]publicBackendConfig{backend.ID: backend}}
	app.BackendHealth.reconcile(app, &snap, false)
	app.BackendHealth.markPassiveFailure(backend.ID, nil)

	route := publicRouteConfig{
		ID:        60,
		Enabled:   true,
		Action:    publicRouteActionForward,
		BackendID: backend.ID,
		BackendAssignments: []publicRouteBackendConfig{{
			RouteID:   60,
			BackendID: backend.ID,
			Position:  0,
			Weight:    100,
			Enabled:   true,
		}},
	}
	selected, ok, fallback := app.selectRouteBackend(snap, route)
	if !ok || fallback || selected.ID != backend.ID {
		t.Fatalf("route backend selection = backend=%d ok=%v fallback=%v, want backend %d", selected.ID, ok, fallback, backend.ID)
	}
}

func TestAgentPoolRouteKeepsAgentEligibleAfterPassiveFailureWhenHealthDisabled(t *testing.T) {
	app, backend := testAgentPoolAppWithHealth(t, false)
	app.BackendHealth.markAgentPassiveFailure(backend.ID, 1, nil)

	selected := app.selectBackendAgent(backend)
	if selected == nil {
		t.Fatal("expected an eligible agent")
	}
	if selected.AgentID != 1 {
		t.Fatalf("selected agent = %d, want passively failed agent to remain eligible", selected.AgentID)
	}
}

func testHealthBackend(t *testing.T, id int64, forwardMode string, originText string) publicBackendConfig {
	t.Helper()
	origin, err := url.Parse(originText)
	if err != nil {
		t.Fatalf("parse backend origin: %v", err)
	}
	return publicBackendConfig{
		ID:           id,
		Enabled:      true,
		BackendType:  publicBackendTypeProxyForward,
		ForwardMode:  forwardMode,
		ParsedOrigin: origin,
		HealthCheck: publicBackendHealthCheckConfig{
			Enabled:            true,
			Method:             http.MethodGet,
			Path:               "/health",
			Interval:           10 * time.Millisecond,
			Timeout:            time.Second,
			HealthyThreshold:   1,
			UnhealthyThreshold: 1,
			ExpectedStatusMin:  200,
			ExpectedStatusMax:  399,
		},
	}
}

func testAgentConn(id int64, publicID string) *AgentConn {
	return &AgentConn{
		AgentID:  id,
		PublicID: publicID,
		Done:     make(chan struct{}),
	}
}

func testAgentPoolApp(t *testing.T) (*App, publicBackendConfig) {
	t.Helper()
	return testAgentPoolAppWithHealth(t, true)
}

func testAgentPoolAppWithHealth(t *testing.T, healthEnabled bool) (*App, publicBackendConfig) {
	t.Helper()
	app := NewApp(nil, nil)
	backend := testHealthBackend(t, 20, publicBackendForwardModeAgentPool, "http://127.0.0.1:8888")
	backend.HealthCheck.Enabled = healthEnabled
	backend.AgentAssignments = []publicBackendAgentConfig{
		{BackendID: backend.ID, AgentID: 1, Position: 0, Weight: 100, Enabled: true},
		{BackendID: backend.ID, AgentID: 2, Position: 1, Weight: 100, Enabled: true},
	}
	snap := &publicProxySnapshot{
		Backends: map[int64]publicBackendConfig{backend.ID: backend},
		Agents: map[int64]publicAgentConfig{
			1: {ID: 1, PublicID: "agent-a", Enabled: true},
			2: {ID: 2, PublicID: "agent-b", Enabled: true},
		},
	}
	app.proxyMu.Lock()
	app.publicSnapshot = snap
	app.proxyMu.Unlock()
	app.BackendHealth.reconcile(app, snap, false)
	for _, agent := range []*AgentConn{testAgentConn(1, "agent-a"), testAgentConn(2, "agent-b")} {
		if err := app.AgentHub.connect(agent); err != nil {
			t.Fatalf("connect agent: %v", err)
		}
		t.Cleanup(func() { app.AgentHub.disconnect(agent) })
	}
	t.Cleanup(func() { app.BackendHealth.reconcile(app, nil, false) })
	return app, backend
}

func waitForHealthStatus(t *testing.T, monitor *publicBackendHealthMonitor, backendID int64, want p2pstreamv1.PublicBackendHealthStatus) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		snapshot := monitor.snapshot(publicBackendHealthDBAdapter{id: backendID, enabled: true})
		if snapshot != nil && snapshot.Status == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	snapshot := monitor.snapshot(publicBackendHealthDBAdapter{id: backendID, enabled: true})
	t.Fatalf("health status = %+v, want %s", snapshot, want)
}
