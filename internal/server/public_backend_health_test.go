package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/httpmsg"
	"p2pstream/msg"
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
	backend := testHealthBackend(t, 10, publicBackendForwardModeAgentPool, "http://127.0.0.1:8888/base?x=1")
	backend.HealthCheck.Method = http.MethodHead
	backend.TLSSkipVerify = true
	backend.UpstreamRequestHeaders = []publicRequestHeader{{Name: "X-Health", Value: "ok"}}
	backend.UpstreamBasicAuth = publicBackendBasicAuthConfig{Enabled: true, Username: "user", Password: "pass"}
	backend.AgentAssignments = []publicBackendAgentConfig{{BackendID: backend.ID, AgentID: 7, Position: 0, Weight: 100, Enabled: true}}
	agent := testAgentConn(7, "agent-7")
	if err := app.AgentHub.connect(agent); err != nil {
		t.Fatalf("connect agent: %v", err)
	}
	defer app.AgentHub.disconnect(agent)

	served := serveOneAgentHealthCheck(t, app, agent, http.StatusOK, func(req *http.Request, first *msg.Request) {
		if req.Method != http.MethodHead {
			t.Fatalf("health method = %s, want HEAD", req.Method)
		}
		if req.URL.Scheme != "http" || req.URL.Host != "127.0.0.1:8888" || req.URL.Path != "/health" {
			t.Fatalf("health request url = %s, want http://127.0.0.1:8888/health", req.URL.String())
		}
		if req.Header.Get(httpmsg.MetadataHealthCheck) != "" || req.Header.Get(httpmsg.MetadataTLSSkipVerify) != "" {
			t.Fatal("internal health metadata was forwarded as upstream headers")
		}
		if httpmsg.FirstHeaderValue(first.Headers, httpmsg.MetadataHealthCheck) != "true" {
			t.Fatal("missing health check metadata on agent request")
		}
		if httpmsg.FirstHeaderValue(first.Headers, httpmsg.MetadataTLSSkipVerify) != "true" {
			t.Fatal("missing tls skip metadata on agent request")
		}
		if req.Header.Get("X-Health") != "ok" {
			t.Fatalf("missing upstream health header, got %q", req.Header.Get("X-Health"))
		}
		if got, _, ok := req.BasicAuth(); !ok || got != "user" {
			t.Fatal("upstream basic auth was not applied")
		}
	})

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
	waitForHealthStatus(t, app.BackendHealth, backend.ID, p2pstreamv1.PublicBackendHealthStatus_PUBLIC_BACKEND_HEALTH_STATUS_HEALTHY)
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
		WriteCh:  make(chan *msg.Request, 10),
		Done:     make(chan struct{}),
	}
}

func serveOneAgentHealthCheck(t *testing.T, app *App, agent *AgentConn, status int, assert func(*http.Request, *msg.Request)) <-chan struct{} {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		var first *msg.Request
		select {
		case first = <-agent.WriteCh:
		case <-time.After(2 * time.Second):
			t.Errorf("timed out waiting for health check request")
			return
		}
		req, err := httpmsg.DecodeRequest(first, &httpmsg.ChannelStream{Ctx: context.Background(), Ch: agent.WriteCh})
		if err != nil {
			t.Errorf("decode health check request: %v", err)
			return
		}
		if assert != nil {
			assert(req, first)
		}
		pendingValue, ok := app.PendingRequests.Load(first.ID)
		if !ok {
			t.Errorf("pending request %s not registered", first.ID.String())
			return
		}
		pending := pendingValue.(*pendingAgentRequest)
		resp := &http.Response{
			StatusCode:    status,
			Status:        http.StatusText(status),
			Header:        make(http.Header),
			Body:          io.NopCloser(strings.NewReader("")),
			ContentLength: 0,
		}
		enc := httpmsg.NewResponseEncoder(first.ID, resp)
		for {
			chunk, err := enc.Next()
			if err == io.EOF {
				return
			}
			if err != nil {
				t.Errorf("encode health response: %v", err)
				return
			}
			pending.ResponseCh <- chunk
		}
	}()
	return done
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
