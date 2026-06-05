package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"p2pstream/httpmsg"
	"p2pstream/msg"
)

func TestDirectProxyResponseHeaderTimeoutReturnsGatewayTimeout(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(150 * time.Millisecond)
		_, _ = w.Write([]byte("too late"))
	}))
	defer upstream.Close()

	origin, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("parse upstream URL: %v", err)
	}
	app := NewApp(nil, nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://public.test/slow", nil)
	app.proxyDirectRequest(rec, req, publicRouteResolution{
		Backend: publicBackendConfig{
			ID:                            1,
			Name:                          "slow-direct",
			Enabled:                       true,
			BackendType:                   publicBackendTypeProxyForward,
			ForwardMode:                   publicBackendForwardModeDirect,
			ParsedOrigin:                  origin,
			UpstreamResponseHeaderTimeout: 25 * time.Millisecond,
		},
	}, nil, nil, nil, proxyRequestObservability{})

	if rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("direct timeout status = %d body=%q, want 504", rec.Code, rec.Body.String())
	}
}

func TestAgentProxySendsResponseHeaderTimeoutMetadata(t *testing.T) {
	origin, err := url.Parse("http://127.0.0.1:8888")
	if err != nil {
		t.Fatalf("parse upstream URL: %v", err)
	}
	app := NewApp(nil, nil)
	agent := testAgentConn(7, "agent-7")
	if err := app.AgentHub.connect(agent); err != nil {
		t.Fatalf("connect agent: %v", err)
	}
	defer app.AgentHub.disconnect(agent)

	backend := publicBackendConfig{
		ID:                            1,
		Name:                          "agent-timeout",
		Enabled:                       true,
		BackendType:                   publicBackendTypeProxyForward,
		ForwardMode:                   publicBackendForwardModeAgentPool,
		ParsedOrigin:                  origin,
		UpstreamResponseHeaderTimeout: 45 * time.Second,
		AgentAssignments: []publicBackendAgentConfig{{
			BackendID: 1,
			AgentID:   7,
			Position:  0,
			Weight:    100,
			Enabled:   true,
		}},
	}
	snap := &publicProxySnapshot{
		Backends: map[int64]publicBackendConfig{backend.ID: backend},
		Agents:   map[int64]publicAgentConfig{7: {ID: 7, PublicID: "agent-7", Enabled: true}},
	}
	app.proxyMu.Lock()
	app.publicSnapshot = snap
	app.proxyMu.Unlock()
	app.BackendHealth.reconcile(app, snap, false)

	served := make(chan struct{})
	go func() {
		defer close(served)
		var first *msg.Request
		select {
		case first = <-agent.WriteCh:
		case <-time.After(2 * time.Second):
			t.Errorf("timed out waiting for agent request")
			return
		}
		if got := httpmsg.FirstHeaderValue(first.Headers, httpmsg.MetadataResponseHeaderTimeoutMillis); got != "45000" {
			t.Errorf("response header timeout metadata = %q, want 45000", got)
		}
		req, err := httpmsg.DecodeRequest(first, &httpmsg.ChannelStream{Ctx: context.Background(), Ch: agent.WriteCh})
		if err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		if req.Header.Get(httpmsg.MetadataResponseHeaderTimeoutMillis) != "" {
			t.Errorf("internal timeout metadata was forwarded upstream")
		}
		pendingValue, ok := app.PendingRequests.Load(first.ID)
		if !ok {
			t.Errorf("pending request %s not registered", first.ID.String())
			return
		}
		resp := &http.Response{
			StatusCode:    http.StatusOK,
			Status:        http.StatusText(http.StatusOK),
			Header:        make(http.Header),
			Body:          io.NopCloser(strings.NewReader("ok")),
			ContentLength: 2,
		}
		enc := httpmsg.NewResponseEncoder(first.ID, resp)
		pending := pendingValue.(*pendingAgentRequest)
		for {
			chunk, err := enc.Next()
			if err == io.EOF {
				return
			}
			if err != nil {
				t.Errorf("encode response: %v", err)
				return
			}
			pending.ResponseCh <- chunk
		}
	}()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://public.test/agent", nil)
	app.proxyAgentRequest(rec, req, publicRouteResolution{Backend: backend}, nil, nil, nil, proxyRequestObservability{})
	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Fatalf("agent proxy response = status %d body %q, want 200 ok", rec.Code, rec.Body.String())
	}
	select {
	case <-served:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for fake agent")
	}
}

func TestAgentProxyClientCancelBeforeFirstResponseDoesNotMarkPassiveFailure(t *testing.T) {
	app, backend, agent := newAgentProxyTimeoutTestApp(t, 7, 500*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "http://public.test/agent", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		app.proxyAgentRequest(rec, req, publicRouteResolution{Backend: backend}, nil, nil, nil, proxyRequestObservability{})
	}()

	select {
	case <-agent.WriteCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for agent request")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for cancelled proxy request")
	}

	if !app.BackendHealth.agentAvailable(backend.ID, agent.AgentID) {
		t.Fatal("client cancellation should not make the agent unavailable")
	}
	traces, _ := app.BackendHealth.listHealthTraces(backend.ID, agent.AgentID, 10, false)
	if len(traces) != 0 {
		t.Fatalf("unexpected health traces after client cancellation: %+v", traces)
	}
}

func TestAgentProxyClientCancelDuringResponseBodyDoesNotMarkPassiveFailure(t *testing.T) {
	app, backend, agent := newAgentProxyTimeoutTestApp(t, 7, 500*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "http://public.test/agent", nil).WithContext(ctx)
	rec := newSignalResponseWriter()
	done := make(chan struct{})
	go func() {
		defer close(done)
		app.proxyAgentRequest(rec, req, publicRouteResolution{Backend: backend}, nil, nil, nil, proxyRequestObservability{})
	}()

	var first *msg.Request
	select {
	case first = <-agent.WriteCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for agent request")
	}
	pendingValue, ok := app.PendingRequests.Load(first.ID)
	if !ok {
		t.Fatalf("pending request %s not registered", first.ID.String())
	}
	pending := pendingValue.(*pendingAgentRequest)
	pending.ResponseCh <- msg.NewRequest(first.ID, msg.RequestTypeHeader, map[string][]string{":status": {"200"}}, nil, 0)

	select {
	case <-rec.wroteHeader:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for response headers")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for cancelled body copy")
	}

	if rec.code != http.StatusOK {
		t.Fatalf("response status = %d, want 200 before client cancellation", rec.code)
	}
	if !app.BackendHealth.agentAvailable(backend.ID, agent.AgentID) {
		t.Fatal("client cancellation during response body should not make the agent unavailable")
	}
	traces, _ := app.BackendHealth.listHealthTraces(backend.ID, agent.AgentID, 10, false)
	if len(traces) != 0 {
		t.Fatalf("unexpected health traces after body cancellation: %+v", traces)
	}
}

func TestAgentProxyResponseTimeoutMarksPassiveFailure(t *testing.T) {
	originalGrace := publicAgentResponseGracePeriod
	publicAgentResponseGracePeriod = time.Millisecond
	t.Cleanup(func() { publicAgentResponseGracePeriod = originalGrace })

	app, backend, agent := newAgentProxyTimeoutTestApp(t, 7, time.Millisecond)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://public.test/agent", nil)
	done := make(chan struct{})
	go func() {
		defer close(done)
		app.proxyAgentRequest(rec, req, publicRouteResolution{Backend: backend}, nil, nil, nil, proxyRequestObservability{})
	}()

	select {
	case <-agent.WriteCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for agent request")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for proxy timeout")
	}

	if rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("timeout status = %d body=%q, want 504", rec.Code, rec.Body.String())
	}
	if app.BackendHealth.agentAvailable(backend.ID, agent.AgentID) {
		t.Fatal("agent timeout should make the agent unavailable during passive cooldown")
	}
	traces, _ := app.BackendHealth.listHealthTraces(backend.ID, agent.AgentID, 10, false)
	if len(traces) == 0 || traces[0].ErrorKind != "passive_failure" {
		t.Fatalf("latest trace = %+v, want passive_failure", traces)
	}
}

func TestAgentProxyDisconnectMarksPassiveFailure(t *testing.T) {
	app, backend, agent := newAgentProxyTimeoutTestApp(t, 7, 500*time.Millisecond)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://public.test/agent", nil)
	done := make(chan struct{})
	go func() {
		defer close(done)
		app.proxyAgentRequest(rec, req, publicRouteResolution{Backend: backend}, nil, nil, nil, proxyRequestObservability{})
	}()

	select {
	case <-agent.WriteCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for agent request")
	}
	app.failPendingRequestsForAgent(agent.AgentID, errAgentDisconnected)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for disconnected proxy request")
	}

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("disconnect status = %d body=%q, want 502", rec.Code, rec.Body.String())
	}
	if app.BackendHealth.agentAvailable(backend.ID, agent.AgentID) {
		t.Fatal("agent disconnect should make the agent unavailable during passive cooldown")
	}
	traces, _ := app.BackendHealth.listHealthTraces(backend.ID, agent.AgentID, 10, false)
	if len(traces) == 0 || traces[0].ErrorKind != "passive_failure" || traces[0].Error != errAgentDisconnected.Error() {
		t.Fatalf("latest trace = %+v, want passive agent_disconnected failure", traces)
	}
}

func newAgentProxyTimeoutTestApp(t *testing.T, agentID int64, responseHeaderTimeout time.Duration) (*App, publicBackendConfig, *AgentConn) {
	t.Helper()
	origin, err := url.Parse("http://127.0.0.1:8888")
	if err != nil {
		t.Fatalf("parse upstream URL: %v", err)
	}
	app := NewApp(nil, nil)
	agent := testAgentConn(agentID, "agent-timeout-test")
	if err := app.AgentHub.connect(agent); err != nil {
		t.Fatalf("connect agent: %v", err)
	}
	t.Cleanup(func() { app.AgentHub.disconnect(agent) })

	backend := publicBackendConfig{
		ID:                            70,
		Name:                          "agent-timeout-test",
		Enabled:                       true,
		BackendType:                   publicBackendTypeProxyForward,
		ForwardMode:                   publicBackendForwardModeAgentPool,
		ParsedOrigin:                  origin,
		UpstreamResponseHeaderTimeout: responseHeaderTimeout,
		HealthCheck: publicBackendHealthCheckConfig{
			Enabled:            true,
			Method:             http.MethodGet,
			Path:               "/",
			Timeout:            2 * time.Second,
			HealthyThreshold:   2,
			UnhealthyThreshold: 2,
			ExpectedStatusMin:  200,
			ExpectedStatusMax:  399,
		},
		AgentAssignments: []publicBackendAgentConfig{{
			BackendID: 70,
			AgentID:   agentID,
			Position:  0,
			Weight:    100,
			Enabled:   true,
		}},
	}
	snap := &publicProxySnapshot{
		Backends: map[int64]publicBackendConfig{backend.ID: backend},
		Agents:   map[int64]publicAgentConfig{agentID: {ID: agentID, PublicID: agent.PublicID, Enabled: true}},
	}
	app.proxyMu.Lock()
	app.publicSnapshot = snap
	app.proxyMu.Unlock()
	app.BackendHealth.reconcile(app, snap, false)
	return app, backend, agent
}

type signalResponseWriter struct {
	header      http.Header
	code        int
	body        strings.Builder
	wroteHeader chan struct{}
}

func newSignalResponseWriter() *signalResponseWriter {
	return &signalResponseWriter{
		header:      make(http.Header),
		wroteHeader: make(chan struct{}),
	}
}

func (w *signalResponseWriter) Header() http.Header {
	return w.header
}

func (w *signalResponseWriter) WriteHeader(statusCode int) {
	if w.code != 0 {
		return
	}
	w.code = statusCode
	close(w.wroteHeader)
}

func (w *signalResponseWriter) Write(p []byte) (int, error) {
	if w.code == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.body.Write(p)
}
