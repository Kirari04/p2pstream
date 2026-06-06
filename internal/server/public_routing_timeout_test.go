package server

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/yamux"

	"p2pstream/internal/tunnel"
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

func TestAgentProxyRelaysThroughYamuxTunnel(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read upstream body: %v", err)
		}
		if r.URL.Path != "/agent" {
			t.Errorf("upstream path = %q, want /agent", r.URL.Path)
		}
		w.Header().Set("X-Upstream", "yamux")
		_, _ = w.Write([]byte("upstream:" + string(body)))
	}))
	defer upstream.Close()

	app, backend, _, fake := newAgentProxyTunnelTestApp(t, 7, upstream.URL, 2*time.Second)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "http://public.test/agent", strings.NewReader("payload"))
	app.proxyAgentRequest(rec, req, publicRouteResolution{Backend: backend}, nil, nil, nil, proxyRequestObservability{})

	if rec.Code != http.StatusOK || rec.Body.String() != "upstream:payload" {
		t.Fatalf("agent proxy response = status %d body %q, want 200 upstream:payload", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("X-Upstream") != "yamux" {
		t.Fatalf("response header X-Upstream = %q, want yamux", rec.Header().Get("X-Upstream"))
	}
	select {
	case open := <-fake.requests:
		if open.Network != "tcp" {
			t.Fatalf("open request network = %q, want tcp", open.Network)
		}
		if open.Address != backend.ParsedOrigin.Host {
			t.Fatalf("open request address = %q, want %q", open.Address, backend.ParsedOrigin.Host)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for fake agent open request")
	}
}

func TestAgentProxyClientCancelBeforeFirstResponseDoesNotMarkPassiveFailure(t *testing.T) {
	reached := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(reached)
		<-r.Context().Done()
	}))
	defer upstream.Close()

	app, backend, agent, _ := newAgentProxyTunnelTestApp(t, 7, upstream.URL, 500*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "http://public.test/agent", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		app.proxyAgentRequest(rec, req, publicRouteResolution{Backend: backend}, nil, nil, nil, proxyRequestObservability{})
	}()

	select {
	case <-reached:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for upstream request")
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

func TestAgentProxyResponseTimeoutMarksPassiveFailure(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(150 * time.Millisecond)
		_, _ = w.Write([]byte("too late"))
	}))
	defer upstream.Close()

	app, backend, agent, _ := newAgentProxyTunnelTestApp(t, 7, upstream.URL, 25*time.Millisecond)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://public.test/agent", nil)
	app.proxyAgentRequest(rec, req, publicRouteResolution{Backend: backend}, nil, nil, nil, proxyRequestObservability{})

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

func TestAgentProxyClosedSessionMarksPassiveFailure(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("should not be reached"))
	}))
	defer upstream.Close()

	app, backend, agent, fake := newAgentProxyTunnelTestApp(t, 7, upstream.URL, 500*time.Millisecond)
	fake.close()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://public.test/agent", nil)
	app.proxyAgentRequest(rec, req, publicRouteResolution{Backend: backend}, nil, nil, nil, proxyRequestObservability{})

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("disconnect status = %d body=%q, want 502", rec.Code, rec.Body.String())
	}
	if app.BackendHealth.agentAvailable(backend.ID, agent.AgentID) {
		t.Fatal("agent disconnect should make the agent unavailable during passive cooldown")
	}
	traces, _ := app.BackendHealth.listHealthTraces(backend.ID, agent.AgentID, 10, false)
	if len(traces) == 0 || traces[0].ErrorKind != "passive_failure" || !strings.Contains(traces[0].Error, errAgentDisconnected.Error()) {
		t.Fatalf("latest trace = %+v, want passive agent_disconnected failure", traces)
	}
}

func newAgentProxyTunnelTestApp(t *testing.T, agentID int64, upstreamURL string, responseHeaderTimeout time.Duration) (*App, publicBackendConfig, *AgentConn, *fakeYamuxAgent) {
	t.Helper()
	origin, err := url.Parse(upstreamURL)
	if err != nil {
		t.Fatalf("parse upstream URL: %v", err)
	}
	app := NewApp(nil, nil)
	agent, fake := newFakeYamuxAgent(t, agentID, "agent-timeout-test")
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
	return app, backend, agent, fake
}

type fakeYamuxAgent struct {
	serverSession *yamux.Session
	agentSession  *yamux.Session
	requests      chan tunnel.OpenRequest
	wg            sync.WaitGroup
	closeOnce     sync.Once
}

func newFakeYamuxAgent(t *testing.T, agentID int64, publicID string) (*AgentConn, *fakeYamuxAgent) {
	t.Helper()
	agentConn, serverConn := net.Pipe()
	agentSession, err := yamux.Client(agentConn, tunnel.DefaultYamuxConfig(nil))
	if err != nil {
		t.Fatalf("agent yamux client: %v", err)
	}
	serverSession, err := yamux.Server(serverConn, tunnel.DefaultYamuxConfig(nil))
	if err != nil {
		t.Fatalf("server yamux session: %v", err)
	}
	fake := &fakeYamuxAgent{
		serverSession: serverSession,
		agentSession:  agentSession,
		requests:      make(chan tunnel.OpenRequest, 16),
	}
	fake.wg.Add(1)
	go fake.acceptLoop()
	t.Cleanup(func() {
		fake.close()
		done := make(chan struct{})
		go func() {
			defer close(done)
			fake.wg.Wait()
		}()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("timed out waiting for fake yamux agent shutdown")
		}
	})
	return &AgentConn{
		AgentID:     agentID,
		PublicID:    publicID,
		Name:        publicID,
		ConnectedAt: time.Now(),
		Done:        make(chan struct{}),
		Session:     serverSession,
	}, fake
}

func (f *fakeYamuxAgent) close() {
	f.closeOnce.Do(func() {
		_ = f.serverSession.Close()
		_ = f.agentSession.Close()
	})
}

func (f *fakeYamuxAgent) acceptLoop() {
	defer f.wg.Done()
	for {
		stream, err := f.agentSession.Accept()
		if err != nil {
			return
		}
		f.wg.Add(1)
		go func() {
			defer f.wg.Done()
			f.handleStream(stream)
		}()
	}
}

func (f *fakeYamuxAgent) handleStream(stream net.Conn) {
	defer stream.Close()
	open, err := tunnel.ReadOpenRequest(stream)
	if err != nil {
		return
	}
	select {
	case f.requests <- open:
	default:
	}
	upstream, err := (&net.Dialer{}).DialContext(context.Background(), open.Network, open.Address)
	if err != nil {
		_ = tunnel.WriteOpenResponse(stream, tunnel.OpenResponse{OK: false, ErrorKind: "dial_failed", Error: err.Error()})
		return
	}
	if err := tunnel.WriteOpenResponse(stream, tunnel.OpenResponse{OK: true}); err != nil {
		_ = upstream.Close()
		return
	}
	relayDone := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(upstream, stream)
		_ = upstream.Close()
		_ = stream.Close()
		relayDone <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(stream, upstream)
		_ = stream.Close()
		_ = upstream.Close()
		relayDone <- struct{}{}
	}()
	<-relayDone
}
