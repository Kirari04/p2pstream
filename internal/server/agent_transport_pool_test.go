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

func TestAgentTransportPoolReusesPublicBackendConnection(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = w.Write([]byte("ok"))
	}))
	defer upstream.Close()

	app, backend, _, fake := newAgentProxyTunnelTestApp(t, 7, upstream.URL, 2*time.Second)
	for i := 0; i < 3; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "http://public.test/reuse", nil)
		app.proxyAgentRequest(rec, req, publicRouteResolution{Backend: backend}, nil, nil, nil, proxyRequestObservability{})
		if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
			t.Fatalf("request %d response = status %d body %q, want 200 ok", i, rec.Code, rec.Body.String())
		}
	}

	fake.waitOpenRequestCount(t, 1)
	if got := app.AgentTransports.len(); got != 1 {
		t.Fatalf("agent transport pool len = %d, want 1", got)
	}
}

func TestAgentTransportPoolConcurrentPublicBackendRequestsOpenParallelStreams(t *testing.T) {
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		_, _ = w.Write([]byte("ok"))
	}))
	defer upstream.Close()

	app, backend, _, fake := newAgentProxyTunnelTestApp(t, 7, upstream.URL, 2*time.Second)
	const requestCount = 3
	var wg sync.WaitGroup
	errCh := make(chan string, requestCount)
	for i := 0; i < requestCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "http://public.test/concurrent", nil)
			app.proxyAgentRequest(rec, req, publicRouteResolution{Backend: backend}, nil, nil, nil, proxyRequestObservability{})
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

func TestAgentTransportPoolSeparatesAgentsAndBackends(t *testing.T) {
	app := NewApp(nil, nil)
	first, firstFake := newFakeYamuxAgent(t, 7, "agent-7")
	defer firstFake.close()
	second, secondFake := newFakeYamuxAgent(t, 8, "agent-8")
	defer secondFake.close()

	backend := publicBackendConfig{
		ID:                            70,
		TargetOrigin:                  "http://upstream.test:9000",
		UpstreamResponseHeaderTimeout: time.Second,
	}
	firstTransport := app.agentProxyTransport(first, backend)
	secondAgentTransport := app.agentProxyTransport(second, backend)
	if firstTransport == secondAgentTransport {
		t.Fatal("different agents shared a pooled transport")
	}

	secondBackend := backend
	secondBackend.ID = 71
	secondBackend.TargetOrigin = "http://upstream.test:9000"
	secondBackendTransport := app.agentProxyTransport(first, secondBackend)
	if firstTransport == secondBackendTransport {
		t.Fatal("different backend ids shared a pooled transport")
	}

	timeoutBackend := backend
	timeoutBackend.UpstreamResponseHeaderTimeout = 2 * time.Second
	timeoutTransport := app.agentProxyTransport(first, timeoutBackend)
	if firstTransport == timeoutTransport {
		t.Fatal("different backend timeout config shared a pooled transport")
	}

	if got := app.AgentTransports.len(); got != 4 {
		t.Fatalf("agent transport pool len = %d, want 4", got)
	}
}

func TestAgentTransportPoolCloseBackendForcesNewPublicStream(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer upstream.Close()

	app, backend, _, fake := newAgentProxyTunnelTestApp(t, 7, upstream.URL, 2*time.Second)
	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "http://public.test/reuse", nil)
		app.proxyAgentRequest(rec, req, publicRouteResolution{Backend: backend}, nil, nil, nil, proxyRequestObservability{})
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d status = %d, want 200", i, rec.Code)
		}
		if i == 0 {
			fake.waitOpenRequestCount(t, 1)
			app.AgentTransports.closeBackend(backend.ID)
		}
	}
	fake.waitOpenRequestCount(t, 2)
}

func TestAgentTransportPoolAgentDisconnectInvalidatesPool(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer upstream.Close()

	app, backend, agent, fake := newAgentProxyTunnelTestApp(t, 7, upstream.URL, 2*time.Second)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://public.test/first", nil)
	app.proxyAgentRequest(rec, req, publicRouteResolution{Backend: backend}, nil, nil, nil, proxyRequestObservability{})
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
	app.proxyAgentRequest(rec, req, publicRouteResolution{Backend: backend}, nil, nil, nil, proxyRequestObservability{})
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
