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

func TestPublicRouteTargetHealthCheckURLPreservesSchemeHostAndPort(t *testing.T) {
	origin, err := url.Parse("http://127.0.0.1:8888/base?x=1")
	if err != nil {
		t.Fatalf("parse origin: %v", err)
	}
	got, err := publicRouteTargetHealthCheckURL(publicRouteTargetHealthConfig{
		ParsedOrigin: origin,
		HealthCheck:  publicRouteTargetHealthCheckConfig{Path: "/health"},
	})
	if err != nil {
		t.Fatalf("health check url: %v", err)
	}
	if got.String() != "http://127.0.0.1:8888/health" {
		t.Fatalf("health check url = %q, want http://127.0.0.1:8888/health", got.String())
	}
}

func TestDirectTargetHealthStillChecksFromServer(t *testing.T) {
	var status atomic.Int64
	status.Store(http.StatusInternalServerError)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			t.Fatalf("direct health path = %q, want /health", r.URL.Path)
		}
		w.WriteHeader(int(status.Load()))
	}))
	defer srv.Close()
	backend := testHealthTarget(t, 1, publicRouteTargetTransportDirect, srv.URL)

	monitor := newPublicRouteTargetHealthMonitor()
	snap := testHealthSnapshot(backend)
	monitor.reconcile(nil, snap, true)
	t.Cleanup(func() { monitor.reconcile(nil, nil, false) })

	waitForHealthStatus(t, monitor, 1, p2pstreamv1.PublicRouteTargetHealthStatus_PUBLIC_ROUTE_TARGET_HEALTH_STATUS_UNHEALTHY)
	if monitor.available(backend) {
		t.Fatal("backend should be unavailable after explicit unhealthy check")
	}
	status.Store(http.StatusOK)
	waitForHealthStatus(t, monitor, 1, p2pstreamv1.PublicRouteTargetHealthStatus_PUBLIC_ROUTE_TARGET_HEALTH_STATUS_HEALTHY)
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

	backend := testHealthTarget(t, 10, publicRouteTargetTransportAgent, upstream.URL+"/base?x=1")
	backend.HealthCheck.Method = http.MethodHead
	backend.TLSSkipVerify = true
	backend.UpstreamRequestHeaders = []publicRequestHeader{{Name: "X-Health", Value: "ok"}}
	backend.UpstreamBasicAuth = publicRouteTargetBasicAuthConfig{Enabled: true, Username: "user", Password: "pass"}
	backend.AgentAssignments = []publicRouteTargetAgentAssignment{{TargetID: backend.ID, AgentID: 7, Position: 0, Weight: 100, Enabled: true}}
	agent, fake := newFakeYamuxAgent(t, 7, "agent-7")
	if err := app.AgentHub.connect(agent); err != nil {
		t.Fatalf("connect agent: %v", err)
	}
	defer app.AgentHub.disconnect(agent)
	defer fake.close()

	snap := testHealthSnapshot(backend)
	snap.Agents[7] = publicAgentConfig{ID: 7, PublicID: "agent-7", Enabled: true, Labels: map[string]string{"pool": "health-test"}}
	app.TargetHealth.reconcile(app, snap, true)
	t.Cleanup(func() { app.TargetHealth.reconcile(app, nil, false) })

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
	waitForHealthStatus(t, app.TargetHealth, backend.ID, p2pstreamv1.PublicRouteTargetHealthStatus_PUBLIC_ROUTE_TARGET_HEALTH_STATUS_HEALTHY)
}

func TestDirectHealthTraceRecordsSuccessAndFailure(t *testing.T) {
	var status atomic.Int64
	status.Store(http.StatusOK)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(int(status.Load()))
	}))
	defer srv.Close()
	backend := testHealthTarget(t, 101, publicRouteTargetTransportDirect, srv.URL+"?token=secret")
	monitor := newPublicRouteTargetHealthMonitor()
	monitor.reconcile(nil, testHealthSnapshot(backend), false)

	monitor.recordDirectExplicitCheck(backend.ID, runPublicRouteTargetHealthCheck(context.Background(), backend))
	traces, retained := monitor.listHealthTraces(backend.ID, 0, 100, false)
	if retained != 1 || len(traces) != 1 {
		t.Fatalf("trace count = retained %d len %d, want 1", retained, len(traces))
	}
	trace := traces[0]
	if trace.Outcome != p2pstreamv1.PublicRouteTargetHealthTraceOutcome_PUBLIC_ROUTE_TARGET_HEALTH_TRACE_OUTCOME_SUCCESS || trace.StatusCode != http.StatusOK {
		t.Fatalf("success trace = %+v", trace)
	}
	if trace.StatusAfter != p2pstreamv1.PublicRouteTargetHealthStatus_PUBLIC_ROUTE_TARGET_HEALTH_STATUS_HEALTHY || trace.HealthyStreakAfter != 1 {
		t.Fatalf("success trace state = %+v, want healthy streak", trace)
	}
	if strings.Contains(trace.Url, "secret") || strings.Contains(trace.Url, "token") {
		t.Fatalf("health trace URL leaked sensitive query: %q", trace.Url)
	}

	status.Store(http.StatusServiceUnavailable)
	monitor.recordDirectExplicitCheck(backend.ID, runPublicRouteTargetHealthCheck(context.Background(), backend))
	traces, retained = monitor.listHealthTraces(backend.ID, 0, 100, false)
	if retained != 2 || len(traces) != 2 {
		t.Fatalf("trace count after failure = retained %d len %d, want 2", retained, len(traces))
	}
	trace = traces[0]
	if trace.Outcome != p2pstreamv1.PublicRouteTargetHealthTraceOutcome_PUBLIC_ROUTE_TARGET_HEALTH_TRACE_OUTCOME_FAILURE ||
		trace.ErrorKind != "unexpected_status" ||
		trace.StatusCode != http.StatusServiceUnavailable ||
		trace.UnhealthyStreakAfter != 1 {
		t.Fatalf("failure trace = %+v", trace)
	}
	failures, retained := monitor.listHealthTraces(backend.ID, 0, 100, true)
	if retained != 2 || len(failures) != 1 || failures[0].Outcome != p2pstreamv1.PublicRouteTargetHealthTraceOutcome_PUBLIC_ROUTE_TARGET_HEALTH_TRACE_OUTCOME_FAILURE {
		t.Fatalf("failure filter = retained %d traces %+v", retained, failures)
	}
}

func TestListHealthTracesReturnsDeepClonedTrace(t *testing.T) {
	if cloneHealthTrace(nil) != nil {
		t.Fatal("nil health trace clone should remain nil")
	}
	nilDebugClone := cloneHealthTrace(&p2pstreamv1.PublicRouteTargetHealthTrace{Sequence: 1})
	if nilDebugClone == nil {
		t.Fatal("nil-debug health trace clone is nil")
	}
	if nilDebugClone.DebugAttributes != nil {
		t.Fatalf("nil debug attributes clone = %+v, want nil", nilDebugClone.DebugAttributes)
	}

	backend := testHealthTarget(t, 105, publicRouteTargetTransportDirect, "http://127.0.0.1:8888")
	monitor := newPublicRouteTargetHealthMonitor()
	monitor.reconcile(nil, testHealthSnapshot(backend), false)

	attempt := newPublicRouteTargetHealthCheckAttempt(backend)
	attempt.StatusCode = http.StatusOK
	finishPublicRouteTargetHealthCheckAttempt(&attempt)
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

	backend := testHealthTarget(t, 102, publicRouteTargetTransportAgent, upstream.URL)
	backend.AgentAssignments = []publicRouteTargetAgentAssignment{{TargetID: backend.ID, AgentID: 7, Position: 0, Weight: 100, Enabled: true}}
	agent, fake := newFakeYamuxAgent(t, 7, "agent-7")
	agent.Name = "Agent Seven"
	if err := app.AgentHub.connect(agent); err != nil {
		t.Fatalf("connect agent: %v", err)
	}
	defer app.AgentHub.disconnect(agent)
	defer fake.close()
	snap := testHealthSnapshot(backend)
	snap.Agents[7] = publicAgentConfig{ID: 7, PublicID: "agent-7", Name: "Agent Seven", Enabled: true, Labels: map[string]string{"pool": "health-test"}}
	app.TargetHealth.reconcile(app, snap, false)

	attempt := app.runPublicRouteTargetHealthCheckViaAgent(context.Background(), backend, agent)
	app.TargetHealth.recordAgentExplicitCheck(backend.ID, 7, attempt)
	traces, retained := app.TargetHealth.listHealthTraces(backend.ID, 7, 100, false)
	if retained != 1 || len(traces) != 1 {
		t.Fatalf("agent trace count = retained %d len %d", retained, len(traces))
	}
	trace := traces[0]
	if trace.Outcome != p2pstreamv1.PublicRouteTargetHealthTraceOutcome_PUBLIC_ROUTE_TARGET_HEALTH_TRACE_OUTCOME_SUCCESS ||
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
	backend := testHealthTarget(t, 103, publicRouteTargetTransportAgent, "http://127.0.0.1:8888")
	backend.AgentAssignments = []publicRouteTargetAgentAssignment{{TargetID: backend.ID, AgentID: 9, Position: 0, Weight: 100, Enabled: true}}
	snap := testHealthSnapshot(backend)
	snap.Agents[9] = publicAgentConfig{ID: 9, PublicID: "agent-9", Enabled: true, Labels: map[string]string{"pool": "health-test"}}
	app.TargetHealth.reconcile(app, snap, false)

	app.TargetHealth.recordAgentDisconnected(backend.ID, 9)
	app.TargetHealth.recordAgentActiveCheckSkipped(backend.ID, 9, backend, "agent_disconnected", errAgentDisconnected)
	traces, _ := app.TargetHealth.listHealthTraces(backend.ID, 9, 100, false)
	if len(traces) != 1 {
		t.Fatalf("skipped trace count = %d, want 1", len(traces))
	}
	trace := traces[0]
	if trace.Outcome != p2pstreamv1.PublicRouteTargetHealthTraceOutcome_PUBLIC_ROUTE_TARGET_HEALTH_TRACE_OUTCOME_SKIPPED ||
		trace.ErrorKind != "agent_disconnected" ||
		trace.StatusCode != 0 {
		t.Fatalf("skipped trace = %+v", trace)
	}
}

func TestPassiveAndConnectivityHealthTraces(t *testing.T) {
	app, backend := testAgentPoolApp(t)
	app.TargetHealth.markAgentPassiveFailure(backend.ID, 1, errors.New("agent timeout"))
	agentOneTraces, _ := app.TargetHealth.listHealthTraces(backend.ID, 1, 100, false)
	agentTwoTraces, _ := app.TargetHealth.listHealthTraces(backend.ID, 2, 100, false)
	if len(agentOneTraces) != 1 || len(agentTwoTraces) != 0 {
		t.Fatalf("agent passive traces agent1=%d agent2=%d, want 1/0", len(agentOneTraces), len(agentTwoTraces))
	}
	if agentOneTraces[0].Source != p2pstreamv1.PublicRouteTargetHealthTraceSource_PUBLIC_ROUTE_TARGET_HEALTH_TRACE_SOURCE_PASSIVE_FAILURE ||
		agentOneTraces[0].PassiveUnhealthyUntilUnixMillis == 0 {
		t.Fatalf("passive trace = %+v", agentOneTraces[0])
	}

	app.TargetHealth.recordAgentConnected(backend.ID, 1, "agent-a")
	app.TargetHealth.recordAgentDisconnected(backend.ID, 1)
	traces, _ := app.TargetHealth.listHealthTraces(backend.ID, 1, 100, false)
	if len(traces) < 3 {
		t.Fatalf("connectivity traces = %+v, want passive plus connect/disconnect", traces)
	}
	if traces[0].Source != p2pstreamv1.PublicRouteTargetHealthTraceSource_PUBLIC_ROUTE_TARGET_HEALTH_TRACE_SOURCE_AGENT_CONNECTIVITY ||
		traces[0].ErrorKind != "agent_disconnected_event" {
		t.Fatalf("latest connectivity trace = %+v", traces[0])
	}
}

func TestDirectHealthTraceRetentionCapsPerTarget(t *testing.T) {
	backend := testHealthTarget(t, 104, publicRouteTargetTransportDirect, "http://127.0.0.1:8888")
	monitor := newPublicRouteTargetHealthMonitor()
	monitor.reconcile(nil, testHealthSnapshot(backend), false)
	for range publicRouteTargetHealthTraceLimitPerTarget + 5 {
		attempt := newPublicRouteTargetHealthCheckAttempt(backend)
		attempt.StatusCode = http.StatusOK
		finishPublicRouteTargetHealthCheckAttempt(&attempt)
		monitor.recordDirectExplicitCheck(backend.ID, attempt)
	}
	traces, retained := monitor.listHealthTraces(backend.ID, 0, 200, false)
	if retained != publicRouteTargetHealthTraceLimitPerTarget || len(traces) != publicRouteTargetHealthTraceLimitPerTarget {
		t.Fatalf("retention = retained %d len %d, want %d", retained, len(traces), publicRouteTargetHealthTraceLimitPerTarget)
	}
	if traces[0].Sequence <= traces[len(traces)-1].Sequence {
		t.Fatalf("traces not newest first: first=%d last=%d", traces[0].Sequence, traces[len(traces)-1].Sequence)
	}
}

func TestAgentPoolHealthCheckUnhealthySkipsOnlyThatAgent(t *testing.T) {
	app, backend := testAgentPoolApp(t)
	target := testRouteTargetFromBackend(backend)
	app.TargetHealth.markAgentPassiveFailure(backend.ID, 1, nil)

	for range 10 {
		selected := app.selectTargetAgent(target)
		if selected == nil {
			t.Fatal("expected an eligible agent")
		}
		if selected.AgentID == 1 {
			t.Fatal("unhealthy agent was selected")
		}
	}
	if !app.TargetHealth.available(backend) {
		t.Fatal("backend should remain available while another agent is eligible")
	}
}

func TestAgentPoolSelectionSkipsDisconnectedAssignments(t *testing.T) {
	app := NewApp(nil, nil)
	backend := testHealthTarget(t, 21, publicRouteTargetTransportAgent, "http://127.0.0.1:8888")
	for i := int64(1); i <= 5; i++ {
		backend.AgentAssignments = append(backend.AgentAssignments, publicRouteTargetAgentAssignment{
			TargetID: backend.ID,
			AgentID:  i,
			Position: i - 1,
			Weight:   100,
			Enabled:  true,
		})
	}
	target := testRouteTargetFromBackend(backend)
	snap := testHealthSnapshot(backend)
	snap.RouteTargets[target.ID] = target
	snap.Agents = make(map[int64]publicAgentConfig)
	for i := int64(1); i <= 5; i++ {
		snap.Agents[i] = publicAgentConfig{ID: i, PublicID: "agent-" + strconv.FormatInt(i, 10), Enabled: true, Labels: map[string]string{"pool": "health-test"}}
	}
	app.proxyMu.Lock()
	app.publicSnapshot = snap
	app.proxyMu.Unlock()
	app.TargetHealth.reconcile(app, snap, false)
	for i := int64(1); i <= 4; i++ {
		agent := testAgentConn(i, "agent-"+strconv.FormatInt(i, 10))
		if err := app.AgentHub.connect(agent); err != nil {
			t.Fatalf("connect agent %d: %v", i, err)
		}
		t.Cleanup(func() { app.AgentHub.disconnect(agent) })
	}
	t.Cleanup(func() { app.TargetHealth.reconcile(app, nil, false) })

	for range 25 {
		selected := app.selectTargetAgent(target)
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
	target := testRouteTargetFromBackend(backend)
	app.TargetHealth.markAgentPassiveFailure(backend.ID, 1, nil)
	app.TargetHealth.markAgentPassiveFailure(backend.ID, 2, nil)

	if app.TargetHealth.available(backend) {
		t.Fatal("backend should be unavailable when all connected assigned agents are unhealthy")
	}
	route := publicRouteConfig{
		ID:      50,
		Enabled: true,
		Action:  publicRouteActionForward,
		Targets: []publicRouteTargetConfig{target},
	}
	snap := publicProxySnapshot{
		RouteTargets: map[int64]publicRouteTargetConfig{target.ID: target},
		Agents: map[int64]publicAgentConfig{
			1: {ID: 1, PublicID: "agent-a", Enabled: true, Labels: map[string]string{"pool": "health-test"}},
			2: {ID: 2, PublicID: "agent-b", Enabled: true, Labels: map[string]string{"pool": "health-test"}},
		},
	}
	if _, _, ok := app.selectRouteTarget(snap, route); ok {
		t.Fatal("route target should be unavailable when all agents are unhealthy")
	}
}

func TestDefaultBackendRequiresEligibleAgent(t *testing.T) {
	app := NewApp(nil, nil)
	targetURL, err := parsePublicTargetOrigin("http://127.0.0.1:8888")
	if err != nil {
		t.Fatalf("parse target URL: %v", err)
	}
	target := publicRouteTargetConfig{
		ID:            70,
		RouteID:       700,
		Enabled:       true,
		TargetType:    publicRouteTargetTypeProxy,
		URL:           targetURL.String(),
		Transport:     publicRouteTargetTransportAgent,
		ParsedURL:     targetURL,
		AgentSelector: publicAgentSelectorConfig{MatchLabels: map[string]string{agentIDSystemLabelKey: "agent-a"}},
	}
	route := publicRouteConfig{
		ID:        700,
		Enabled:   true,
		IsDefault: true,
		Action:    publicRouteActionForward,
		Targets:   []publicRouteTargetConfig{target},
	}
	snap := &publicProxySnapshot{
		RouteTargets:     map[int64]publicRouteTargetConfig{target.ID: target},
		Agents:           map[int64]publicAgentConfig{1: {ID: 1, PublicID: "agent-a", Enabled: true, Labels: map[string]string{agentIDSystemLabelKey: "agent-a"}}},
		Listeners:        map[int64]publicListenerConfig{10: {ID: 10, Enabled: true}},
		RoutesByListener: map[int64][]publicRouteConfig{10: {route}},
	}
	app.proxyMu.Lock()
	app.publicSnapshot = snap
	app.proxyMu.Unlock()

	req := httptest.NewRequest(http.MethodGet, "http://example.test/", nil)
	_, err = app.resolvePublicRoute(10, req)
	if !errors.Is(err, errNoRouteTargetAvailable) {
		t.Fatalf("resolve default target error = %v, want %v", err, errNoRouteTargetAvailable)
	}
}

func TestRouteFallbackSelectedWhenPrimaryAgentPoolUnavailable(t *testing.T) {
	app := NewApp(nil, nil)
	primary := testHealthTarget(t, 80, publicRouteTargetTransportAgent, "http://127.0.0.1:8888")
	primary.AgentAssignments = []publicRouteTargetAgentAssignment{{TargetID: primary.ID, AgentID: 1, Position: 0, Weight: 100, Enabled: true}}
	fallback := testHealthTarget(t, 81, publicRouteTargetTransportDirect, "http://127.0.0.1:9999")
	primaryTarget := testRouteTargetFromBackend(primary)
	fallbackTarget := testRouteTargetFromBackend(fallback)
	primaryTarget.PriorityGroup = 0
	fallbackTarget.PriorityGroup = 1
	route := publicRouteConfig{
		ID:      90,
		Enabled: true,
		Action:  publicRouteActionForward,
		Targets: []publicRouteTargetConfig{primaryTarget, fallbackTarget},
	}
	snap := publicProxySnapshot{
		RouteTargets: map[int64]publicRouteTargetConfig{
			primaryTarget.ID:  primaryTarget,
			fallbackTarget.ID: fallbackTarget,
		},
		Agents: map[int64]publicAgentConfig{1: {ID: 1, PublicID: "agent-a", Enabled: true, Labels: map[string]string{"pool": "health-test"}}},
	}
	app.TargetHealth.reconcile(app, &snap, false)
	t.Cleanup(func() { app.TargetHealth.reconcile(app, nil, false) })

	selected, _, ok := app.selectRouteTarget(snap, route)
	if !ok || selected.ID != fallbackTarget.ID {
		t.Fatalf("route target selection = target=%d ok=%v, want fallback %d", selected.ID, ok, fallbackTarget.ID)
	}
}

func TestAgentPassiveFailureAppliesWhenHealthCheckEnabled(t *testing.T) {
	app, backend := testAgentPoolApp(t)
	app.TargetHealth.markAgentPassiveFailure(backend.ID, 1, nil)

	if app.TargetHealth.agentAvailable(backend.ID, 1) {
		t.Fatal("agent 1 should be unavailable during passive cooldown")
	}
	if !app.TargetHealth.agentAvailable(backend.ID, 2) {
		t.Fatal("agent 2 should remain available")
	}
	if !app.TargetHealth.available(backend) {
		t.Fatal("backend should remain available while agent 2 is eligible")
	}
}

func TestAgentReconnectClearsPassiveCooldownToUnknown(t *testing.T) {
	app := NewApp(nil, nil)
	backend := testHealthTarget(t, 22, publicRouteTargetTransportAgent, "http://127.0.0.1:8888")
	backend.AgentAssignments = []publicRouteTargetAgentAssignment{{TargetID: backend.ID, AgentID: 1, Position: 0, Weight: 100, Enabled: true}}
	snap := testHealthSnapshot(backend)
	snap.Agents[1] = publicAgentConfig{ID: 1, PublicID: "agent-a", Enabled: true, Labels: map[string]string{"pool": "health-test"}}
	app.TargetHealth.reconcile(app, snap, false)
	agent := testAgentConn(1, "agent-a")
	if err := app.AgentHub.connect(agent); err != nil {
		t.Fatalf("connect agent: %v", err)
	}
	app.TargetHealth.recordAgentConnected(backend.ID, 1, "agent-a")
	app.TargetHealth.markAgentPassiveFailure(backend.ID, 1, nil)
	if app.TargetHealth.agentAvailable(backend.ID, 1) {
		t.Fatal("agent should be unavailable during passive cooldown")
	}
	app.AgentHub.disconnect(agent)
	app.TargetHealth.recordAgentDisconnected(backend.ID, 1)
	reconnected := testAgentConn(1, "agent-a")
	if err := app.AgentHub.connect(reconnected); err != nil {
		t.Fatalf("reconnect agent: %v", err)
	}
	t.Cleanup(func() { app.AgentHub.disconnect(reconnected) })
	t.Cleanup(func() { app.TargetHealth.reconcile(app, nil, false) })

	app.TargetHealth.recordAgentConnected(backend.ID, 1, "agent-a")
	snapshot := app.TargetHealth.agentSnapshot(backend.ID, 1, true, true)
	if snapshot == nil || snapshot.Status != p2pstreamv1.PublicRouteTargetHealthStatus_PUBLIC_ROUTE_TARGET_HEALTH_STATUS_UNKNOWN || !snapshot.Available || snapshot.PassiveUnhealthyUntilUnixMillis != 0 {
		t.Fatalf("agent snapshot after reconnect = %+v, want available UNKNOWN without passive cooldown", snapshot)
	}
}

func TestPublicRouteTargetProtoIncludesHealth(t *testing.T) {
	app := NewApp(nil, nil)
	backend := testHealthTarget(t, 23, publicRouteTargetTransportAgent, "http://127.0.0.1:8888")
	backend.AgentAssignments = []publicRouteTargetAgentAssignment{{TargetID: backend.ID, AgentID: 1, Position: 0, Weight: 100, Enabled: true}}
	snap := testHealthSnapshot(backend)
	snap.Agents[1] = publicAgentConfig{ID: 1, PublicID: "agent-a", Enabled: true, Labels: map[string]string{"pool": "health-test"}}
	app.TargetHealth.reconcile(app, snap, false)
	agent := testAgentConn(1, "agent-a")
	agent.ActiveRequests.Store(3)
	if err := app.AgentHub.connect(agent); err != nil {
		t.Fatalf("connect agent: %v", err)
	}
	t.Cleanup(func() { app.AgentHub.disconnect(agent) })
	t.Cleanup(func() { app.TargetHealth.reconcile(app, nil, false) })
	var done []func()
	for range 3 {
		done = append(done, app.TargetHealth.beginRequest(backend.ID))
	}
	t.Cleanup(func() {
		for _, finish := range done {
			finish()
		}
	})

	target := db.PublicRouteTarget{ID: backend.ID, Enabled: 1, TargetType: publicRouteTargetTypeProxy, Transport: publicRouteTargetTransportAgent}
	health := publicRouteTargetHealthToProto(target, app.TargetHealth)
	if !health.Connected || !health.Available || health.Status != p2pstreamv1.PublicRouteTargetHealthStatus_PUBLIC_ROUTE_TARGET_HEALTH_STATUS_UNKNOWN || health.ActiveRequests != 3 {
		t.Fatalf("target health = %+v, want connected available UNKNOWN with active requests", health)
	}
}

func TestAgentPassiveFailureIgnoredWhenHealthCheckDisabled(t *testing.T) {
	app, backend := testAgentPoolAppWithHealth(t, false)
	app.TargetHealth.markAgentPassiveFailure(backend.ID, 1, nil)

	if !app.TargetHealth.agentAvailable(backend.ID, 1) {
		t.Fatal("agent 1 should remain available when health checks are disabled")
	}
	if !app.TargetHealth.available(backend) {
		t.Fatal("backend should remain available when health checks are disabled")
	}
}

func TestAgentHealthCheckDisconnectedAgentIsUnknownNotUnhealthy(t *testing.T) {
	app := NewApp(nil, nil)
	backend := testHealthTarget(t, 40, publicRouteTargetTransportAgent, "http://127.0.0.1:8888")
	backend.AgentAssignments = []publicRouteTargetAgentAssignment{{TargetID: backend.ID, AgentID: 99, Position: 0, Weight: 100, Enabled: true}}
	snap := testHealthSnapshot(backend)
	snap.Agents[99] = publicAgentConfig{ID: 99, PublicID: "agent-missing", Enabled: true, Labels: map[string]string{"pool": "health-test"}}
	app.TargetHealth.reconcile(app, snap, true)
	t.Cleanup(func() { app.TargetHealth.reconcile(app, nil, false) })

	snapshot := app.TargetHealth.snapshot(publicRouteTargetHealthDBAdapter{id: backend.ID, enabled: true})
	if snapshot == nil || snapshot.Status != p2pstreamv1.PublicRouteTargetHealthStatus_PUBLIC_ROUTE_TARGET_HEALTH_STATUS_UNKNOWN {
		t.Fatalf("disconnected agent aggregate status = %+v, want UNKNOWN", snapshot)
	}
	if app.TargetHealth.available(backend) {
		t.Fatal("backend route eligibility should still require a connected agent")
	}
}

func TestPassiveFailureIgnoredWhenHealthCheckDisabledDirect(t *testing.T) {
	monitor := newPublicRouteTargetHealthMonitor()
	backend := testHealthTarget(t, 2, publicRouteTargetTransportDirect, "http://127.0.0.1:8080")
	backend.HealthCheck.Enabled = false
	snap := testHealthSnapshot(backend)
	monitor.reconcile(nil, snap, false)

	monitor.markPassiveFailure(backend.ID, nil)
	if !monitor.available(backend) {
		t.Fatal("backend should remain available when health checks are disabled")
	}
	snapshot := monitor.snapshot(publicRouteTargetHealthDBAdapter{id: backend.ID, enabled: true})
	if snapshot == nil || snapshot.Status != p2pstreamv1.PublicRouteTargetHealthStatus_PUBLIC_ROUTE_TARGET_HEALTH_STATUS_UNKNOWN || snapshot.PassiveUnhealthyUntilUnixMillis != 0 {
		t.Fatalf("unexpected disabled health snapshot: %+v", snapshot)
	}
}

func TestPassiveFailureAppliesWhenHealthCheckEnabledDirect(t *testing.T) {
	monitor := newPublicRouteTargetHealthMonitor()
	backend := testHealthTarget(t, 3, publicRouteTargetTransportDirect, "http://127.0.0.1:8080")
	snap := testHealthSnapshot(backend)
	monitor.reconcile(nil, snap, false)

	monitor.markPassiveFailure(backend.ID, nil)
	if monitor.available(backend) {
		t.Fatal("backend should be unavailable during passive cooldown")
	}
	snapshot := monitor.snapshot(publicRouteTargetHealthDBAdapter{id: backend.ID, enabled: true})
	if snapshot == nil || snapshot.Status != p2pstreamv1.PublicRouteTargetHealthStatus_PUBLIC_ROUTE_TARGET_HEALTH_STATUS_UNHEALTHY || snapshot.PassiveUnhealthyUntilUnixMillis == 0 {
		t.Fatalf("unexpected passive health snapshot: %+v", snapshot)
	}
}

func TestDisablingHealthChecksClearsPassiveState(t *testing.T) {
	monitor := newPublicRouteTargetHealthMonitor()
	backend := testHealthTarget(t, 4, publicRouteTargetTransportDirect, "http://127.0.0.1:8080")
	monitor.reconcile(nil, testHealthSnapshot(backend), false)
	monitor.markPassiveFailure(backend.ID, nil)
	if monitor.available(backend) {
		t.Fatal("backend should be unavailable while health checks are enabled")
	}

	disabled := backend
	disabled.HealthCheck.Enabled = false
	monitor.reconcile(nil, testHealthSnapshot(disabled), false)
	if !monitor.available(disabled) {
		t.Fatal("backend should become available after disabling health checks")
	}
	snapshot := monitor.snapshot(publicRouteTargetHealthDBAdapter{id: disabled.ID, enabled: true})
	if snapshot == nil || snapshot.Status != p2pstreamv1.PublicRouteTargetHealthStatus_PUBLIC_ROUTE_TARGET_HEALTH_STATUS_UNKNOWN || snapshot.PassiveUnhealthyUntilUnixMillis != 0 {
		t.Fatalf("unexpected snapshot after disabling health checks: %+v", snapshot)
	}
}

func TestPassiveCooldownExpiryStillRecoversWhenHealthEnabled(t *testing.T) {
	monitor := newPublicRouteTargetHealthMonitor()
	backend := testHealthTarget(t, 5, publicRouteTargetTransportDirect, "http://127.0.0.1:8080")
	monitor.reconcile(nil, testHealthSnapshot(backend), false)
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
	monitor := newPublicRouteTargetHealthMonitor()
	backend := testHealthTarget(t, 55, publicRouteTargetTransportDirect, "http://127.0.0.1:8080")
	backend.HealthCheck.UnhealthyThreshold = 1
	monitor.reconcile(nil, testHealthSnapshot(backend), false)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	attempt := runPublicRouteTargetHealthCheck(ctx, backend)
	if !attempt.Skipped || attempt.ErrorKind != "health_check_cancelled" {
		t.Fatalf("cancelled attempt skipped=%v errorKind=%q err=%v, want skipped health_check_cancelled", attempt.Skipped, attempt.ErrorKind, attempt.Err)
	}
	monitor.recordDirectExplicitCheck(backend.ID, attempt)

	monitor.mu.Lock()
	state := monitor.states[backend.ID].direct
	monitor.mu.Unlock()
	if state.unhealthyStreak != 0 || state.explicitStatus != p2pstreamv1.PublicRouteTargetHealthStatus_PUBLIC_ROUTE_TARGET_HEALTH_STATUS_UNKNOWN {
		t.Fatalf("state after cancelled check = %+v, want no unhealthy streak and UNKNOWN", state)
	}
	traces, _ := monitor.listHealthTraces(backend.ID, 0, 10, false)
	if len(traces) != 1 ||
		traces[0].Outcome != p2pstreamv1.PublicRouteTargetHealthTraceOutcome_PUBLIC_ROUTE_TARGET_HEALTH_TRACE_OUTCOME_SKIPPED ||
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

	monitor := newPublicRouteTargetHealthMonitor()
	backend := testHealthTarget(t, 56, publicRouteTargetTransportDirect, upstream.URL)
	backend.HealthCheck.Timeout = time.Millisecond
	backend.HealthCheck.UnhealthyThreshold = 1
	monitor.reconcile(nil, testHealthSnapshot(backend), false)

	attempt := runPublicRouteTargetHealthCheck(context.Background(), backend)
	if attempt.Skipped || attempt.ErrorKind != "health_check_timeout" {
		t.Fatalf("timeout attempt skipped=%v errorKind=%q err=%v, want health_check_timeout", attempt.Skipped, attempt.ErrorKind, attempt.Err)
	}
	monitor.recordDirectExplicitCheck(backend.ID, attempt)

	monitor.mu.Lock()
	state := monitor.states[backend.ID].direct
	monitor.mu.Unlock()
	if state.unhealthyStreak != 1 || state.explicitStatus != p2pstreamv1.PublicRouteTargetHealthStatus_PUBLIC_ROUTE_TARGET_HEALTH_STATUS_UNHEALTHY {
		t.Fatalf("state after timeout = %+v, want unhealthy streak 1 and UNHEALTHY", state)
	}
}

func TestRouteKeepsBackendEligibleAfterPassiveFailureWhenHealthDisabled(t *testing.T) {
	app := NewApp(nil, nil)
	backend := testHealthTarget(t, 6, publicRouteTargetTransportDirect, "http://127.0.0.1:8080")
	backend.HealthCheck.Enabled = false
	target := testRouteTargetFromBackend(backend)
	snap := publicProxySnapshot{
		RouteTargets: map[int64]publicRouteTargetConfig{target.ID: target},
	}
	app.TargetHealth.reconcile(app, &snap, false)
	app.TargetHealth.markPassiveFailure(backend.ID, nil)

	route := publicRouteConfig{
		ID:      60,
		Enabled: true,
		Action:  publicRouteActionForward,
		Targets: []publicRouteTargetConfig{target},
	}
	selected, _, ok := app.selectRouteTarget(snap, route)
	if !ok || selected.ID != target.ID {
		t.Fatalf("route target selection = target=%d ok=%v, want target %d", selected.ID, ok, target.ID)
	}
}

func TestAgentPoolRouteKeepsAgentEligibleAfterPassiveFailureWhenHealthDisabled(t *testing.T) {
	app, backend := testAgentPoolAppWithHealth(t, false)
	target := testRouteTargetFromBackend(backend)
	app.TargetHealth.markAgentPassiveFailure(backend.ID, 1, nil)

	selected := app.selectTargetAgent(target)
	if selected == nil {
		t.Fatal("expected an eligible agent")
	}
	if selected.AgentID != 1 {
		t.Fatalf("selected agent = %d, want passively failed agent to remain eligible", selected.AgentID)
	}
}

func testHealthTarget(t *testing.T, id int64, transport string, originText string) publicRouteTargetHealthConfig {
	t.Helper()
	origin, err := url.Parse(originText)
	if err != nil {
		t.Fatalf("parse backend origin: %v", err)
	}
	return publicRouteTargetHealthConfig{
		ID:           id,
		Enabled:      true,
		TargetType:   publicRouteTargetTypeProxy,
		Transport:    transport,
		ParsedOrigin: origin,
		HealthCheck: publicRouteTargetHealthCheckConfig{
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

func testAgentPoolApp(t *testing.T) (*App, publicRouteTargetHealthConfig) {
	t.Helper()
	return testAgentPoolAppWithHealth(t, true)
}

func testAgentPoolAppWithHealth(t *testing.T, healthEnabled bool) (*App, publicRouteTargetHealthConfig) {
	t.Helper()
	app := NewApp(nil, nil)
	backend := testHealthTarget(t, 20, publicRouteTargetTransportAgent, "http://127.0.0.1:8888")
	backend.HealthCheck.Enabled = healthEnabled
	backend.AgentAssignments = []publicRouteTargetAgentAssignment{
		{TargetID: backend.ID, AgentID: 1, Position: 0, Weight: 100, Enabled: true},
		{TargetID: backend.ID, AgentID: 2, Position: 1, Weight: 100, Enabled: true},
	}
	target := testRouteTargetFromBackend(backend)
	snap := testHealthSnapshot(backend)
	snap.RouteTargets[target.ID] = target
	snap.Agents = map[int64]publicAgentConfig{
		1: {ID: 1, PublicID: "agent-a", Enabled: true, Labels: map[string]string{"pool": "health-test"}},
		2: {ID: 2, PublicID: "agent-b", Enabled: true, Labels: map[string]string{"pool": "health-test"}},
	}
	app.proxyMu.Lock()
	app.publicSnapshot = snap
	app.proxyMu.Unlock()
	app.TargetHealth.reconcile(app, snap, false)
	for _, agent := range []*AgentConn{testAgentConn(1, "agent-a"), testAgentConn(2, "agent-b")} {
		if err := app.AgentHub.connect(agent); err != nil {
			t.Fatalf("connect agent: %v", err)
		}
		t.Cleanup(func() { app.AgentHub.disconnect(agent) })
	}
	t.Cleanup(func() { app.TargetHealth.reconcile(app, nil, false) })
	return app, backend
}

func testHealthSnapshot(backends ...publicRouteTargetHealthConfig) *publicProxySnapshot {
	snap := &publicProxySnapshot{
		RouteTargets: map[int64]publicRouteTargetConfig{},
		Agents:       map[int64]publicAgentConfig{},
	}
	for _, backend := range backends {
		target := testRouteTargetFromBackend(backend)
		snap.RouteTargets[target.ID] = target
		for _, assignment := range backend.AgentAssignments {
			publicID := "agent-" + strconv.FormatInt(assignment.AgentID, 10)
			snap.Agents[assignment.AgentID] = publicAgentConfig{
				ID:       assignment.AgentID,
				PublicID: publicID,
				Enabled:  assignment.Enabled,
				Labels:   map[string]string{"pool": "health-test", agentIDSystemLabelKey: publicID},
			}
		}
	}
	return snap
}

func testRouteTargetFromBackend(backend publicRouteTargetHealthConfig) publicRouteTargetConfig {
	target := publicRouteTargetConfigFromHealthTarget(backend)
	if target.Transport == publicRouteTargetTransportAgent {
		target.AgentSelector = publicAgentSelectorConfig{MatchLabels: map[string]string{"pool": "health-test"}}
	}
	return target
}

func waitForHealthStatus(t *testing.T, monitor *publicRouteTargetHealthMonitor, targetID int64, want p2pstreamv1.PublicRouteTargetHealthStatus) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		snapshot := monitor.snapshot(publicRouteTargetHealthDBAdapter{id: targetID, enabled: true})
		if snapshot != nil && snapshot.Status == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	snapshot := monitor.snapshot(publicRouteTargetHealthDBAdapter{id: targetID, enabled: true})
	t.Fatalf("health status = %+v, want %s", snapshot, want)
}
