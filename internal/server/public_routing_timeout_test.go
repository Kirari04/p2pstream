package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
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
	app.proxyDirectTargetRequest(rec, req, publicRouteResolution{
		Target: publicRouteTargetConfig{
			ID:                            1,
			Name:                          "slow-direct",
			Enabled:                       true,
			TargetType:                    publicRouteTargetTypeProxy,
			Transport:                     publicRouteTargetTransportDirect,
			ParsedURL:                     origin,
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

	app, target, agent, fake := newAgentProxyTunnelTestApp(t, 7, upstream.URL, 2*time.Second)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "http://public.test/agent", strings.NewReader("payload"))
	proxyAgentTargetForTest(app, rec, req, target, agent)

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
		if open.Address != target.ParsedURL.Host {
			t.Fatalf("open request address = %q, want %q", open.Address, target.ParsedURL.Host)
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

	app, target, agent, _ := newAgentProxyTunnelTestApp(t, 7, upstream.URL, 500*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "http://public.test/agent", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		proxyAgentTargetForTest(app, rec, req, target, agent)
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

	if !app.TargetHealth.agentAvailable(target.ID, agent.AgentID) {
		t.Fatal("client cancellation should not make the agent unavailable")
	}
	traces, _ := app.TargetHealth.listHealthTraces(target.ID, agent.AgentID, 10, false)
	if len(traces) != 0 {
		t.Fatalf("unexpected health traces after client cancellation: %+v", traces)
	}
}

func TestAgentDialCancelWhileOpeningStreamDoesNotCloseSession(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()

	session, err := yamux.Server(serverConn, tunnel.DefaultYamuxConfig(nil))
	if err != nil {
		t.Fatalf("yamux server: %v", err)
	}
	defer session.Close()

	app := NewApp(nil, nil)
	agent := &AgentConn{
		AgentID:     7,
		PublicID:    "agent-cancel-open",
		Name:        "agent-cancel-open",
		ConnectedAt: time.Now(),
		Done:        make(chan struct{}),
		Session:     session,
	}
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		conn, err := app.dialViaAgent(ctx, agent, "tcp", "127.0.0.1:80", "cancel-open")
		if conn != nil {
			_ = conn.Close()
		}
		errCh <- err
	}()

	cancel()
	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("dial error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for cancelled dial")
	}
	select {
	case <-session.CloseChan():
		t.Fatal("request cancellation closed shared agent session")
	default:
	}
}

func TestAgentStreamOpenDelayedSuccessAfterCancelClosesStream(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	agent := &AgentConn{Done: make(chan struct{})}
	openStarted := make(chan struct{})
	releaseOpen := make(chan struct{})
	stream, peer := net.Pipe()
	defer stream.Close()
	defer peer.Close()

	results := startAgentStreamOpen(ctx, agent, func() (net.Conn, error) {
		close(openStarted)
		<-releaseOpen
		return stream, nil
	})

	<-openStarted
	cancel()
	close(releaseOpen)

	if err := peer.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("set peer deadline: %v", err)
	}
	buf := make([]byte, 1)
	if _, err := peer.Read(buf); !errors.Is(err, io.EOF) {
		t.Fatalf("read after delayed open = %v, want EOF from closed abandoned stream", err)
	}
	select {
	case result := <-results:
		if result.conn != nil {
			_ = result.conn.Close()
		}
		t.Fatal("delayed open transferred stream ownership after cancellation")
	default:
	}
}

func TestAgentStreamOpenAdmissionCanceledWaitersSpawnNoOpen(t *testing.T) {
	agent := &AgentConn{
		Done:                     make(chan struct{}),
		streamOpenAdmissionLimit: 1,
	}
	releaseOpen := make(chan struct{})
	openStarted := make(chan struct{})
	var openCalls atomic.Int64
	firstResults := startAgentStreamOpen(context.Background(), agent, func() (net.Conn, error) {
		openCalls.Add(1)
		close(openStarted)
		<-releaseOpen
		return nil, errors.New("first open finished")
	})
	<-openStarted

	gate := agent.streamOpenAdmissionGate()
	if cap(gate) != 1 || len(gate) != 1 {
		t.Fatalf("open admission gate = len %d/cap %d, want 1/1", len(gate), cap(gate))
	}
	if max := tunnel.DefaultYamuxConfig(nil).AcceptBacklog; cap(gate) > max {
		t.Fatalf("open admission capacity = %d, exceeds Yamux AcceptBacklog %d", cap(gate), max)
	}

	const canceledWaiters = 512
	for i := 0; i < canceledWaiters; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		results := startAgentStreamOpen(ctx, agent, func() (net.Conn, error) {
			openCalls.Add(1)
			return nil, errors.New("canceled waiter unexpectedly opened")
		})
		if results != nil {
			t.Fatalf("canceled waiter %d received a result channel", i)
		}
	}
	if got := openCalls.Load(); got != 1 {
		t.Fatalf("Open callbacks after canceled waiters = %d, want only the admitted callback", got)
	}

	close(releaseOpen)
	select {
	case result := <-firstResults:
		if result.err == nil {
			t.Fatal("first Open result unexpectedly succeeded")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for admitted Open to return")
	}
}

func TestAgentStreamOpenAdmissionHeldUntilOpenReturnsAndReusable(t *testing.T) {
	agent := &AgentConn{
		Done:                     make(chan struct{}),
		streamOpenAdmissionLimit: 1,
	}
	firstCtx, cancelFirst := context.WithCancel(context.Background())
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	_ = startAgentStreamOpen(firstCtx, agent, func() (net.Conn, error) {
		close(firstStarted)
		<-releaseFirst
		return nil, errors.New("first open finished")
	})
	<-firstStarted
	cancelFirst()
	if got := len(agent.streamOpenAdmissionGate()); got != 1 {
		t.Fatalf("gate permits after caller cancellation = %d, want 1 held by blocked Open", got)
	}

	secondCalling := make(chan struct{})
	secondStarted := make(chan struct{})
	secondReturned := make(chan (<-chan agentStreamOpenResult), 1)
	go func() {
		close(secondCalling)
		secondReturned <- startAgentStreamOpen(context.Background(), agent, func() (net.Conn, error) {
			close(secondStarted)
			return nil, errors.New("second open finished")
		})
	}()
	<-secondCalling
	select {
	case <-secondStarted:
		t.Fatal("caller cancellation released the permit before the underlying Open returned")
	case <-secondReturned:
		t.Fatal("waiting admission returned before the underlying Open released its permit")
	case <-time.After(25 * time.Millisecond):
	}

	close(releaseFirst)

	var secondResults <-chan agentStreamOpenResult
	select {
	case secondResults = <-secondReturned:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for second admission")
	}
	select {
	case <-secondStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for second Open callback")
	}

	thirdStarted := make(chan struct{})
	thirdResults := startAgentStreamOpen(context.Background(), agent, func() (net.Conn, error) {
		close(thirdStarted)
		return nil, errors.New("third open finished")
	})
	if thirdResults == nil {
		t.Fatal("permit was not reusable after second Open returned")
	}
	select {
	case <-thirdStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for third Open callback")
	}
	for name, results := range map[string]<-chan agentStreamOpenResult{
		"second": secondResults,
		"third":  thirdResults,
	} {
		select {
		case result := <-results:
			if result.err == nil {
				t.Fatalf("%s Open result unexpectedly succeeded", name)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out receiving %s Open result", name)
		}
	}
}

func TestAgentProxyClientCancelDuringUploadClosesUpstream(t *testing.T) {
	reached := make(chan struct{})
	upstreamClosed := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(reached)
		_, _ = io.Copy(io.Discard, r.Body)
		close(upstreamClosed)
	}))
	defer upstream.Close()

	app, target, agent, _ := newAgentProxyTunnelTestApp(t, 7, upstream.URL, 2*time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	reader, writer := io.Pipe()
	req := httptest.NewRequest(http.MethodPost, "http://public.test/agent", reader).WithContext(ctx)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		proxyAgentTargetForTest(app, rec, req, target, agent)
	}()

	if _, err := writer.Write([]byte(strings.Repeat("x", 4096))); err != nil {
		t.Fatalf("write request body: %v", err)
	}
	select {
	case <-reached:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for upstream upload")
	}
	cancel()
	_ = writer.CloseWithError(context.Canceled)

	select {
	case <-upstreamClosed:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for upstream body reader to close")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for cancelled upload proxy request")
	}
	waitForAgentActiveRequests(t, agent, 0)
	if !app.TargetHealth.agentAvailable(target.ID, agent.AgentID) {
		t.Fatal("client upload cancellation should not make the agent unavailable")
	}
}

func TestAgentProxyAgentDisconnectDuringResponseReleasesActiveRequest(t *testing.T) {
	responseStarted := make(chan struct{})
	releaseResponse := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("partial\n"))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		close(responseStarted)
		<-releaseResponse
	}))
	defer upstream.Close()
	defer close(releaseResponse)

	app, target, agent, fake := newAgentProxyTunnelTestApp(t, 7, upstream.URL, 2*time.Second)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://public.test/agent", nil)
	done := make(chan struct{})
	go func() {
		defer close(done)
		proxyAgentTargetForTest(app, rec, req, target, agent)
	}()

	select {
	case <-responseStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for upstream response start")
	}
	fake.close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for proxy request after agent session close")
	}
	waitForAgentActiveRequests(t, agent, 0)
}

func TestAgentProxyResponseTimeoutMarksPassiveFailure(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(150 * time.Millisecond)
		_, _ = w.Write([]byte("too late"))
	}))
	defer upstream.Close()

	app, target, agent, _ := newAgentProxyTunnelTestApp(t, 7, upstream.URL, 25*time.Millisecond)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://public.test/agent", nil)
	proxyAgentTargetForTest(app, rec, req, target, agent)

	if rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("timeout status = %d body=%q, want 504", rec.Code, rec.Body.String())
	}
	if app.TargetHealth.agentAvailable(target.ID, agent.AgentID) {
		t.Fatal("agent timeout should make the agent unavailable during passive cooldown")
	}
	traces, _ := app.TargetHealth.listHealthTraces(target.ID, agent.AgentID, 10, false)
	if len(traces) == 0 || traces[0].ErrorKind != "passive_failure" {
		t.Fatalf("latest trace = %+v, want passive_failure", traces)
	}
}

func TestDialViaAgentOpenHandshakeTimesOutWithoutContextDeadline(t *testing.T) {
	withAgentOpenHandshakeTimeout(t, 25*time.Millisecond)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("upstream should not be reached before agent open response")
	}))
	defer upstream.Close()

	app, target, agent, fake := newAgentProxyTunnelTestApp(t, 7, upstream.URL, 2*time.Second)
	fake.suppressOpenResponse = true
	ctx := context.Background()
	if _, ok := ctx.Deadline(); ok {
		t.Fatal("test context unexpectedly has a deadline")
	}

	started := time.Now()
	conn, err := app.dialViaAgent(ctx, agent, "tcp", target.ParsedURL.Host, "request-timeout-test")
	if conn != nil {
		_ = conn.Close()
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("agent open handshake timeout took %s, want under 1s", elapsed)
	}
	var dialErr agentDialError
	if !errors.As(err, &dialErr) || dialErr.Kind != "dial_timeout" {
		t.Fatalf("dialViaAgent error = %T %[1]v, want agent dial_timeout", err)
	}
	fake.waitOpenRequestCount(t, 1)
}

func TestAgentProxyOpenHandshakeTimeoutWithoutRequestDeadline(t *testing.T) {
	withAgentOpenHandshakeTimeout(t, 25*time.Millisecond)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("upstream should not be reached before agent open response")
	}))
	defer upstream.Close()

	app, target, agent, fake := newAgentProxyTunnelTestApp(t, 7, upstream.URL, 2*time.Second)
	fake.suppressOpenResponse = true
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://public.test/agent", nil)
	if _, ok := req.Context().Deadline(); ok {
		t.Fatal("test request unexpectedly has a context deadline")
	}

	started := time.Now()
	proxyAgentTargetForTest(app, rec, req, target, agent)
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("agent open handshake timeout took %s, want under 1s", elapsed)
	}

	if rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("open handshake timeout status = %d body=%q, want 504", rec.Code, rec.Body.String())
	}
	waitForAgentActiveRequests(t, agent, 0)
	fake.waitOpenRequestCount(t, 1)
	if app.TargetHealth.agentAvailable(target.ID, agent.AgentID) {
		t.Fatal("agent open handshake timeout should make the agent unavailable during passive cooldown")
	}
	traces, _ := app.TargetHealth.listHealthTraces(target.ID, agent.AgentID, 10, false)
	if len(traces) == 0 ||
		traces[0].ErrorKind != "passive_failure" ||
		!strings.Contains(traces[0].Error, "dial_timeout") {
		t.Fatalf("latest trace = %+v, want passive dial_timeout failure", traces)
	}
}

func TestAgentOpenHandshakeDeadlineUsesEarlierRequestDeadline(t *testing.T) {
	withAgentOpenHandshakeTimeout(t, 10*time.Second)
	now := time.Now()

	if got, want := agentOpenHandshakeDeadline(context.Background(), now), now.Add(10*time.Second); !got.Equal(want) {
		t.Fatalf("handshake deadline without context deadline = %s, want %s", got, want)
	}

	earlier := now.Add(time.Second)
	earlierCtx, cancelEarlier := context.WithDeadline(context.Background(), earlier)
	defer cancelEarlier()
	if got := agentOpenHandshakeDeadline(earlierCtx, now); !got.Equal(earlier) {
		t.Fatalf("handshake deadline with earlier context = %s, want %s", got, earlier)
	}

	later := now.Add(time.Minute)
	laterCtx, cancelLater := context.WithDeadline(context.Background(), later)
	defer cancelLater()
	if got, want := agentOpenHandshakeDeadline(laterCtx, now), now.Add(10*time.Second); !got.Equal(want) {
		t.Fatalf("handshake deadline with later context = %s, want %s", got, want)
	}
}

func TestAgentProxyClosedSessionMarksPassiveFailure(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("should not be reached"))
	}))
	defer upstream.Close()

	app, target, agent, fake := newAgentProxyTunnelTestApp(t, 7, upstream.URL, 500*time.Millisecond)
	fake.close()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://public.test/agent", nil)
	proxyAgentTargetForTest(app, rec, req, target, agent)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("disconnect status = %d body=%q, want 502", rec.Code, rec.Body.String())
	}
	if app.TargetHealth.agentAvailable(target.ID, agent.AgentID) {
		t.Fatal("agent disconnect should make the agent unavailable during passive cooldown")
	}
	traces, _ := app.TargetHealth.listHealthTraces(target.ID, agent.AgentID, 10, false)
	if len(traces) == 0 || traces[0].ErrorKind != "passive_failure" || !strings.Contains(traces[0].Error, errAgentDisconnected.Error()) {
		t.Fatalf("latest trace = %+v, want passive agent_disconnected failure", traces)
	}
}

func TestAgentProxyDialForbiddenDoesNotMarkPassiveFailure(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("upstream should not be reached when agent rejects destination")
	}))
	defer upstream.Close()

	app, target, agent, fake := newAgentProxyTunnelTestApp(t, 7, upstream.URL, 500*time.Millisecond)
	fake.openResponse = func(open tunnel.OpenRequest) tunnel.OpenResponse {
		return tunnel.OpenResponse{OK: false, ErrorKind: "dial_forbidden", Error: "agent destination forbidden by allowlist"}
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://public.test/agent", nil)
	proxyAgentTargetForTest(app, rec, req, target, agent)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("dial forbidden status = %d body=%q, want 502", rec.Code, rec.Body.String())
	}
	if !app.TargetHealth.agentAvailable(target.ID, agent.AgentID) {
		t.Fatal("agent dial_forbidden should not make the agent unavailable")
	}
	traces, _ := app.TargetHealth.listHealthTraces(target.ID, agent.AgentID, 10, false)
	if len(traces) != 0 {
		t.Fatalf("unexpected health traces after dial_forbidden: %+v", traces)
	}
	fake.waitOpenRequestCount(t, 1)
}

func TestAgentProxyHTTPSOriginTLSVerification(t *testing.T) {
	tlsUpstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("secure upstream"))
	}))
	defer tlsUpstream.Close()

	t.Run("rejects unknown self signed cert by default", func(t *testing.T) {
		app, target, agent, _ := newAgentProxyTunnelTestApp(t, 7, tlsUpstream.URL, 2*time.Second)
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "http://public.test/agent", nil)
		proxyAgentTargetForTest(app, rec, req, target, agent)
		if rec.Code != http.StatusBadGateway {
			t.Fatalf("self-signed upstream status = %d body=%q, want 502", rec.Code, rec.Body.String())
		}
	})

	t.Run("succeeds with trusted root", func(t *testing.T) {
		withTrustedHTTPDefaultTransport(t, tlsUpstream.Certificate())
		app, target, agent, _ := newAgentProxyTunnelTestApp(t, 7, tlsUpstream.URL, 2*time.Second)
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "http://public.test/agent", nil)
		proxyAgentTargetForTest(app, rec, req, target, agent)
		if rec.Code != http.StatusOK || rec.Body.String() != "secure upstream" {
			t.Fatalf("trusted upstream response = status %d body=%q, want 200 secure upstream", rec.Code, rec.Body.String())
		}
	})

	t.Run("succeeds with tls skip verify", func(t *testing.T) {
		app, target, agent, _ := newAgentProxyTunnelTestApp(t, 7, tlsUpstream.URL, 2*time.Second)
		target.TLSSkipVerify = true
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "http://public.test/agent", nil)
		proxyAgentTargetForTest(app, rec, req, target, agent)
		if rec.Code != http.StatusOK || rec.Body.String() != "secure upstream" {
			t.Fatalf("skip-verify upstream response = status %d body=%q, want 200 secure upstream", rec.Code, rec.Body.String())
		}
	})
}

func proxyAgentTargetForTest(app *App, rec *httptest.ResponseRecorder, req *http.Request, target publicRouteTargetConfig, agent *AgentConn) {
	app.proxyAgentTargetRequest(rec, req, publicRouteResolution{Target: target, Agent: agent}, nil, nil, nil, proxyRequestObservability{})
}

func newAgentProxyTunnelTestApp(t *testing.T, agentID int64, upstreamURL string, responseHeaderTimeout time.Duration) (*App, publicRouteTargetConfig, *AgentConn, *fakeYamuxAgent) {
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

	target := publicRouteTargetConfig{
		ID:                            70,
		Name:                          "agent-timeout-test",
		Enabled:                       true,
		TargetType:                    publicRouteTargetTypeProxy,
		Transport:                     publicRouteTargetTransportAgent,
		AgentLoadBalancing:            publicRouteTargetLoadBalancingRoundRobin,
		AgentSelector:                 publicAgentSelectorConfig{MatchLabels: map[string]string{agentIDSystemLabelKey: agent.PublicID}},
		ParsedURL:                     origin,
		UpstreamResponseHeaderTimeout: responseHeaderTimeout,
		HealthCheck: publicRouteTargetHealthCheckConfig{
			Enabled:            true,
			Method:             http.MethodGet,
			Path:               "/",
			Timeout:            2 * time.Second,
			HealthyThreshold:   2,
			UnhealthyThreshold: 2,
			ExpectedStatusMin:  200,
			ExpectedStatusMax:  399,
		},
	}
	snap := &publicProxySnapshot{
		RouteTargets: map[int64]publicRouteTargetConfig{target.ID: target},
		Agents:       map[int64]publicAgentConfig{agentID: {ID: agentID, PublicID: agent.PublicID, Enabled: true, Labels: map[string]string{agentIDSystemLabelKey: agent.PublicID}}},
	}
	setPublicSnapshotForTest(t, app, snap)
	app.TargetHealth.reconcile(app, snap, false)
	return app, target, agent, fake
}

type fakeYamuxAgent struct {
	serverSession        *yamux.Session
	agentSession         *yamux.Session
	requests             chan tunnel.OpenRequest
	openResponse         func(tunnel.OpenRequest) tunnel.OpenResponse
	suppressOpenResponse bool
	mu                   sync.Mutex
	openRequests         int
	wg                   sync.WaitGroup
	closeOnce            sync.Once
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
	f.mu.Lock()
	f.openRequests++
	f.mu.Unlock()
	select {
	case f.requests <- open:
	default:
	}
	if f.suppressOpenResponse {
		_, _ = io.Copy(io.Discard, stream)
		return
	}
	if f.openResponse != nil {
		resp := f.openResponse(open)
		_ = tunnel.WriteOpenResponse(stream, resp)
		return
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

func (f *fakeYamuxAgent) openRequestCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.openRequests
}

func (f *fakeYamuxAgent) waitOpenRequestCount(t *testing.T, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got := f.openRequestCount(); got == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("fake yamux open requests = %d, want %d", f.openRequestCount(), want)
}

func withAgentOpenHandshakeTimeout(t *testing.T, timeout time.Duration) {
	t.Helper()
	previous := agentOpenHandshakeTimeout
	agentOpenHandshakeTimeout = timeout
	t.Cleanup(func() {
		agentOpenHandshakeTimeout = previous
	})
}

func waitForAgentActiveRequests(t *testing.T, agent *AgentConn, want int64) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got := agent.ActiveRequests.Load(); got == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("agent active requests = %d, want %d", agent.ActiveRequests.Load(), want)
}

func withTrustedHTTPDefaultTransport(t *testing.T, cert *x509.Certificate) {
	t.Helper()
	oldDefault := http.DefaultTransport
	roots := x509.NewCertPool()
	roots.AddCert(cert)
	base, ok := oldDefault.(*http.Transport)
	if !ok {
		t.Fatalf("http.DefaultTransport is %T, want *http.Transport", oldDefault)
	}
	transport := base.Clone()
	if transport.TLSClientConfig == nil {
		transport.TLSClientConfig = &tls.Config{}
	} else {
		transport.TLSClientConfig = transport.TLSClientConfig.Clone()
	}
	transport.TLSClientConfig.RootCAs = roots
	http.DefaultTransport = transport
	t.Cleanup(func() {
		http.DefaultTransport = oldDefault
	})
}
