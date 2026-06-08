package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
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
