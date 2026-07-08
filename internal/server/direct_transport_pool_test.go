package server

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"
)

func TestDirectTransportPoolReusesPublicRouteTargetConnection(t *testing.T) {
	var connections atomic.Int64
	upstream := newCountingUpstream(t, &connections, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer upstream.Close()

	app := NewApp(nil, nil)
	target := directTransportPoolTestTarget(t, 70, upstream.URL, time.Second)
	for i := 0; i < 3; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "http://public.test/reuse", nil)
		proxyDirectTargetForTest(app, rec, req, target)
		if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
			t.Fatalf("request %d response = status %d body %q, want 200 ok", i, rec.Code, rec.Body.String())
		}
	}

	if got := connections.Load(); got != 1 {
		t.Fatalf("upstream connections = %d, want 1", got)
	}
	if got := app.DirectTransports.len(); got != 1 {
		t.Fatalf("direct transport pool len = %d, want 1", got)
	}
}

func TestDirectTransportPoolSeparatesRouteTargetsTLSAndTimeouts(t *testing.T) {
	app := NewApp(nil, nil)
	target := directTransportPoolTestTarget(t, 70, "http://upstream.test:9000/base?x=1", time.Second)
	first := app.directTargetTransport(target)
	if first != app.directTargetTransport(target) {
		t.Fatal("same direct route target did not reuse pooled transport")
	}

	secondTarget := target
	secondTarget.ID = 71
	if first == app.directTargetTransport(secondTarget) {
		t.Fatal("different route target ids shared a direct transport")
	}

	skipVerifyTarget := target
	skipVerifyTarget.TLSSkipVerify = true
	skipVerifyTransport := app.directTargetTransport(skipVerifyTarget)
	if first == skipVerifyTransport {
		t.Fatal("different TLS skip-verify config shared a direct transport")
	}
	skipVerifyHTTPTransport, ok := skipVerifyTransport.(*http.Transport)
	if !ok || skipVerifyHTTPTransport.TLSClientConfig == nil || !skipVerifyHTTPTransport.TLSClientConfig.InsecureSkipVerify {
		t.Fatalf("skip-verify transport TLS config = %#v, want InsecureSkipVerify", skipVerifyHTTPTransport.TLSClientConfig)
	}

	timeoutTarget := target
	timeoutTarget.UpstreamResponseHeaderTimeout = 2 * time.Second
	timeoutTransport := app.directTargetTransport(timeoutTarget)
	if first == timeoutTransport {
		t.Fatal("different response header timeout shared a direct transport")
	}
	timeoutHTTPTransport, ok := timeoutTransport.(*http.Transport)
	if !ok || timeoutHTTPTransport.ResponseHeaderTimeout != 2*time.Second {
		t.Fatalf("timeout transport ResponseHeaderTimeout = %s, want 2s", timeoutHTTPTransport.ResponseHeaderTimeout)
	}
}

func TestDirectTransportPoolNormalizesDefaultTimeout(t *testing.T) {
	app := NewApp(nil, nil)
	zeroTimeoutTarget := directTransportPoolTestTarget(t, 70, "http://upstream.test:9000", 0)
	defaultTimeoutTarget := zeroTimeoutTarget
	defaultTimeoutTarget.UpstreamResponseHeaderTimeout = time.Duration(defaultTargetUpstreamResponseHeaderTimeoutMillis) * time.Millisecond

	if app.directTargetTransport(zeroTimeoutTarget) != app.directTargetTransport(defaultTimeoutTarget) {
		t.Fatal("zero and explicit default response header timeouts should share a direct transport")
	}
}

func TestDirectTransportPoolCloseRouteTargetForcesNewConnection(t *testing.T) {
	var connections atomic.Int64
	upstream := newCountingUpstream(t, &connections, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer upstream.Close()

	app := NewApp(nil, nil)
	target := directTransportPoolTestTarget(t, 70, upstream.URL, time.Second)
	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "http://public.test/reconnect", nil)
		proxyDirectTargetForTest(app, rec, req, target)
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d status = %d, want 200", i, rec.Code)
		}
		if i == 0 {
			if got := connections.Load(); got != 1 {
				t.Fatalf("upstream connections after first request = %d, want 1", got)
			}
			app.DirectTransports.closeRouteTarget(target.ID)
		}
	}

	if got := connections.Load(); got != 2 {
		t.Fatalf("upstream connections after invalidation = %d, want 2", got)
	}
	if got := app.DirectTransports.len(); got != 1 {
		t.Fatalf("direct transport pool len = %d, want 1", got)
	}
}

func TestRouteTargetTransportReconcileEvictsChangedAndRemovedTargets(t *testing.T) {
	app := NewApp(nil, nil)
	agent := &AgentConn{AgentID: 7}
	keptDirect := directTransportPoolTestTarget(t, 70, "http://direct.test:9000", time.Second)
	changedDirect := directTransportPoolTestTarget(t, 71, "http://direct.test:9001", time.Second)
	removedDirect := directTransportPoolTestTarget(t, 72, "http://direct.test:9002", time.Second)
	keptAgent := directTransportPoolTestTarget(t, 80, "http://agent.test:9000", time.Second)
	keptAgent.Transport = publicRouteTargetTransportAgent
	changedAgent := directTransportPoolTestTarget(t, 81, "http://agent.test:9001", time.Second)
	changedAgent.Transport = publicRouteTargetTransportAgent

	keptDirectTransport := app.directTargetTransport(keptDirect)
	changedDirectTransport := app.directTargetTransport(changedDirect)
	_ = app.directTargetTransport(removedDirect)
	keptAgentTransport := app.agentTargetTransport(agent, keptAgent)
	changedAgentTransport := app.agentTargetTransport(agent, changedAgent)

	previous := &publicProxySnapshot{RouteTargets: map[int64]publicRouteTargetConfig{
		keptDirect.ID:    keptDirect,
		changedDirect.ID: changedDirect,
		removedDirect.ID: removedDirect,
		keptAgent.ID:     keptAgent,
		changedAgent.ID:  changedAgent,
	}}
	keptDirect.Name = "renamed"
	changedDirect.TLSSkipVerify = true
	keptAgent.Weight = 20
	changedAgent.UpstreamResponseHeaderTimeout = 2 * time.Second
	current := &publicProxySnapshot{RouteTargets: map[int64]publicRouteTargetConfig{
		keptDirect.ID:    keptDirect,
		changedDirect.ID: changedDirect,
		keptAgent.ID:     keptAgent,
		changedAgent.ID:  changedAgent,
	}}

	app.reconcileRouteTargetTransports(previous, current)

	if got := app.DirectTransports.len(); got != 1 {
		t.Fatalf("direct transport pool len after reconcile = %d, want 1", got)
	}
	if got := app.AgentTransports.len(); got != 1 {
		t.Fatalf("agent transport pool len after reconcile = %d, want 1", got)
	}
	if got := app.directTargetTransport(keptDirect); got != keptDirectTransport {
		t.Fatal("unchanged direct target did not keep its pooled transport")
	}
	if got := app.directTargetTransport(changedDirect); got == changedDirectTransport {
		t.Fatal("changed direct target kept stale pooled transport")
	}
	if got := app.agentTargetTransport(agent, keptAgent); got != keptAgentTransport {
		t.Fatal("unchanged agent target did not keep its pooled transport")
	}
	if got := app.agentTargetTransport(agent, changedAgent); got == changedAgentTransport {
		t.Fatal("changed agent target kept stale pooled transport")
	}
}

func newCountingUpstream(t *testing.T, connections *atomic.Int64, handler http.Handler) *httptest.Server {
	t.Helper()
	upstream := httptest.NewUnstartedServer(handler)
	upstream.Config.ConnContext = func(ctx context.Context, conn net.Conn) context.Context {
		connections.Add(1)
		return ctx
	}
	upstream.Start()
	return upstream
}

func proxyDirectTargetForTest(app *App, rec *httptest.ResponseRecorder, req *http.Request, target publicRouteTargetConfig) {
	app.proxyDirectTargetRequest(rec, req, publicRouteResolution{Target: target}, nil, nil, nil, proxyRequestObservability{})
}

func directTransportPoolTestTarget(t *testing.T, id int64, origin string, responseHeaderTimeout time.Duration) publicRouteTargetConfig {
	t.Helper()
	parsed, err := url.Parse(origin)
	if err != nil {
		t.Fatalf("parse origin: %v", err)
	}
	return publicRouteTargetConfig{
		ID:                            id,
		Name:                          "direct-transport-test",
		Enabled:                       true,
		TargetType:                    publicRouteTargetTypeProxy,
		Transport:                     publicRouteTargetTransportDirect,
		URL:                           origin,
		ParsedURL:                     parsed,
		UpstreamResponseHeaderTimeout: responseHeaderTimeout,
	}
}
