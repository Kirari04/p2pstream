package server

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"p2pstream/internal/sysmetrics"
)

func newPublicOwnedResourceTestApp(t *testing.T) *App {
	t.Helper()
	usage := sysmetrics.MemoryUsage{UsedBytes: 64 << 20, LimitBytes: 4 << 30, Source: "test"}
	a := NewApp(nil, nil)
	a.agentStreamCapacity = newAdaptiveServerCapacityForTest(t, 65_536, &usage)
	return a
}

func TestPublicConnectionPhaseLeaseRetainsParserAndSocketUntilClose(t *testing.T) {
	for _, tlsWrapped := range []bool{false, true} {
		t.Run(fmt.Sprint("TLS=", tlsWrapped), func(t *testing.T) {
			a := newPublicOwnedResourceTestApp(t)
			local, peer := net.Pipe()
			defer peer.Close()
			release, active, ok := a.tryReservePublicConnectionPhased(local)
			if !ok {
				t.Fatal("initial reservation rejected")
			}
			tracked := &resourceBoundedPublicConn{Conn: local, release: release, active: active}
			var conn net.Conn = tracked
			if tlsWrapped {
				conn = tls.Server(tracked, &tls.Config{MinVersion: tls.VersionTLS12})
			}
			before := a.agentStreamCapacity.snapshot()
			if got := before.AdaptiveExternalBytesByOwner[adaptiveResourceOwnerPublicHandshake]; got != publicHandshakeWorkingBytes {
				t.Fatalf("pending handshake = %d", got)
			}
			publicResourceConnState(conn, http.StateIdle)
			if got := a.agentStreamCapacity.snapshot().AdaptiveExternalBytes; got != before.AdaptiveExternalBytes {
				t.Fatal("idle before active released handshake credit")
			}
			publicResourceConnState(conn, http.StateActive)
			publicResourceConnState(conn, http.StateIdle)
			publicResourceConnState(conn, http.StateActive)
			after := a.agentStreamCapacity.snapshot()
			if after.AdaptiveExternalBytes != before.AdaptiveExternalBytes-publicHandshakeWorkingBytes || after.AdaptiveExternalFDs != 1 {
				t.Fatalf("active lost parser/socket credit: before=%+v after=%+v", before, after)
			}
			if got := after.AdaptiveExternalBytesByOwner[adaptiveResourceOwnerPublicConnection]; got != publicConnectionWorkingBytes+2*defaultPublicMaxHeaderBytes {
				t.Fatalf("active parser/runtime envelope = %d", got)
			}
			// Close racing with the first/another activation must return each lease
			// once. Closing the tracked wire avoids a synthetic TLS close handshake.
			var wg sync.WaitGroup
			for range 10 {
				wg.Go(func() { publicResourceConnState(conn, http.StateActive) })
				wg.Go(func() { _ = tracked.Close() })
			}
			wg.Wait()
			if s := a.agentStreamCapacity.snapshot(); s.AdaptiveExternalBytes != 0 || s.AdaptiveExternalFDs != 0 || a.publicConnections.inUse() != 0 {
				t.Fatalf("phase leases leaked: %+v", s)
			}
		})
	}
}

func TestPublicSlowHeaderKeepsInitialEnvelopeAndLargeReuseRemainsBounded(t *testing.T) {
	a := newPublicOwnedResourceTestApp(t)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	configurePublicHTTPServer(srv.Config)
	srv.Config.ConnState = publicResourceConnState
	srv.Listener = resourceBoundedPublicListener{Listener: srv.Listener, acquirePhased: a.tryReservePublicConnectionPhased}
	srv.Start()
	defer srv.Close()
	conn, err := net.DialTimeout("tcp", srv.Listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	_, _ = io.WriteString(conn, "GET / HTTP/1.1\r\nHost: test\r\nX-Slow: ")
	waitPublicOwnedBytes(t, a, adaptiveResourceOwnerPublicHandshake, publicHandshakeWorkingBytes)
	_, _ = io.WriteString(conn, strings.Repeat("a", 40<<10)+"\r\n\r\n")
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatal(response.Status)
	}
	waitPublicOwnedBytes(t, a, adaptiveResourceOwnerPublicHandshake, 0)
	steady := a.agentStreamCapacity.snapshot().AdaptiveExternalBytesByOwner[adaptiveResourceOwnerPublicConnection]
	if steady < 2*defaultPublicMaxHeaderBytes+publicConnectionWorkingBytes {
		t.Fatalf("missing parser envelope: %d", steady)
	}
	// A keepalive connection still enforces the parser limit after returning
	// its handshake lease; it cannot grow an unbounded second request header.
	_, _ = io.WriteString(conn, "GET / HTTP/1.1\r\nHost: test\r\nX-Huge: "+strings.Repeat("b", 80<<10)+"\r\n\r\n")
	response, err = http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusRequestHeaderFieldsTooLarge {
		t.Fatal(response.Status)
	}
	_ = conn.Close()
	srv.CloseClientConnections()
	waitPublicOwnedBytes(t, a, adaptiveResourceOwnerPublicConnection, 0)
}

func TestPublicHTTP2ActivationReturnsOnlyHandshakeLease(t *testing.T) {
	a := newPublicOwnedResourceTestApp(t)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor != 2 {
			t.Errorf("protocol = %s", r.Proto)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	srv.EnableHTTP2 = true
	configurePublicHTTPServer(srv.Config)
	srv.Config.ConnState = publicResourceConnState
	srv.Listener = resourceBoundedPublicListener{Listener: srv.Listener, acquirePhased: a.tryReservePublicConnectionPhased}
	srv.StartTLS()
	defer srv.Close()
	client := srv.Client()
	for range 3 {
		response, err := client.Get(srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
	}
	waitPublicOwnedBytes(t, a, adaptiveResourceOwnerPublicHandshake, 0)
	if s := a.agentStreamCapacity.snapshot(); s.AdaptiveExternalFDs != 1 || s.AdaptiveExternalBytesByOwner[adaptiveResourceOwnerPublicConnection] == 0 {
		t.Fatalf("multiplexed connection ownership = %+v", s)
	}
	client.CloseIdleConnections()
	srv.CloseClientConnections()
	waitPublicOwnedBytes(t, a, adaptiveResourceOwnerPublicConnection, 0)
}

func TestPublicRequestChargesDecodedHeadersAndDefersResponseWorkingSet(t *testing.T) {
	a := newPublicOwnedResourceTestApp(t)
	small := httptest.NewRequest(http.MethodGet, "http://public.test/", nil)
	ctx := newPublicProxyContext(a, 1, httptest.NewRecorder(), small)
	defer ctx.runCleanup()
	if publicRequestAdmissionStage(ctx) != publicProxyStageContinue {
		t.Fatal("small request rejected")
	}
	before := a.agentStreamCapacity.snapshot()
	if before.AdaptiveExternalBytesByOwner[adaptiveResourceOwnerPublicResponse] != 0 {
		t.Fatal("ingress reserved a relay buffer")
	}
	if charge := before.AdaptiveExternalBytesByOwner[adaptiveResourceOwnerPublicRequest]; charge >= 64<<10 {
		t.Fatalf("small request envelope = %d", charge)
	}
	large := small.Clone(t.Context())
	large.Header["X-Values"] = make([]string, 1000)
	for i := range large.Header["X-Values"] {
		large.Header["X-Values"][i] = "x"
	}
	if extra := publicRequestHeaderMemory(large) - publicRequestHeaderMemory(small); extra < 64_000 {
		t.Fatalf("decoded value metadata omitted: %d", extra)
	}
	large.Header.Set("X-Long", strings.Repeat("x", 40<<10))
	if publicRequestHeaderMemory(large) < 80<<10 {
		t.Fatal("large headers undercharged")
	}
	if publicResponseAdmissionStage(ctx) != publicProxyStageContinue {
		t.Fatal("response rejected")
	}
	if got := a.agentStreamCapacity.snapshot().AdaptiveExternalBytesByOwner[adaptiveResourceOwnerPublicResponse]; got != reverseProxyCopyBufferSize {
		t.Fatalf("response buffer = %d", got)
	}
	ctx.runCleanup()
	if s := a.agentStreamCapacity.snapshot(); s.AdaptiveExternalBytes != 0 {
		t.Fatalf("request leaked: %+v", s)
	}
}

func TestPublicHTTP2ConcurrentDecodedHeadersKeepIndependentLeases(t *testing.T) {
	const streams = 96 // Below the advertised 250-stream connection ceiling.
	a := newPublicOwnedResourceTestApp(t)
	entered := make(chan int64, streams)
	finish := make(chan struct{})
	var finishOnce sync.Once
	unblock := func() { finishOnce.Do(func() { close(finish) }) }
	defer unblock()
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := newPublicProxyContext(a, 1, w, r)
		defer ctx.runCleanup()
		if publicRequestAdmissionStage(ctx) != publicProxyStageContinue {
			return
		}
		entered <- publicRequestWorkingBytes + publicRequestHeaderMemory(r)
		select {
		case <-finish:
			w.WriteHeader(http.StatusNoContent)
		case <-r.Context().Done():
		}
	}))
	srv.EnableHTTP2 = true
	configurePublicHTTPServer(srv.Config)
	srv.Config.ConnState = publicResourceConnState
	srv.Listener = resourceBoundedPublicListener{Listener: srv.Listener, acquirePhased: a.tryReservePublicConnectionPhased}
	srv.StartTLS()
	defer func() { unblock(); srv.Close() }()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	client := srv.Client()
	errs := make(chan error, streams)
	for range streams {
		go func() {
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
			if err != nil {
				errs <- err
				return
			}
			// Tiny repeated values have a small wire representation but retain
			// slice/string metadata after HPACK decode. The second field stresses
			// the byte envelope on the same multiplexed connection.
			request.Header["X-Many"] = make([]string, 1000)
			for i := range request.Header["X-Many"] {
				request.Header["X-Many"][i] = "a"
			}
			request.Header.Set("X-Large", strings.Repeat("b", 12<<10))
			response, err := client.Do(request)
			if err == nil {
				_ = response.Body.Close()
				if response.StatusCode != http.StatusNoContent || response.ProtoMajor != 2 {
					err = fmt.Errorf("unexpected response %s %s", response.Proto, response.Status)
				}
			}
			errs <- err
		}()
	}
	var expected int64
	for range streams {
		select {
		case bytes := <-entered:
			expected += bytes
		case err := <-errs:
			t.Fatalf("request completed before the header barrier: %v", err)
		case <-ctx.Done():
			t.Fatal("HTTP/2 header barrier timed out")
		}
	}
	s := a.agentStreamCapacity.snapshot()
	if s.AdaptiveExternalBytesByOwner[adaptiveResourceOwnerPublicRequest] != expected || expected < streams*(96<<10) {
		t.Fatalf("decoded header leases = %d, want %d >= %d", s.AdaptiveExternalBytesByOwner[adaptiveResourceOwnerPublicRequest], expected, streams*(96<<10))
	}
	if s.AdaptiveExternalReservationsByOwner[adaptiveResourceOwnerPublicRequest] != streams {
		t.Fatalf("request owners = %d, want %d", s.AdaptiveExternalReservationsByOwner[adaptiveResourceOwnerPublicRequest], streams)
	}
	// Cancellation must return all per-stream headers even though the physical
	// H2 connection is independently pooled. Closing it then returns its lease.
	cancel()
	unblock()
	for range streams {
		<-errs
	}
	waitPublicOwnedBytes(t, a, adaptiveResourceOwnerPublicRequest, 0)
	client.CloseIdleConnections()
	srv.CloseClientConnections()
	waitPublicOwnedBytes(t, a, adaptiveResourceOwnerPublicConnection, 0)
}

func waitPublicOwnedBytes(t *testing.T, a *App, owner adaptiveResourceOwner, expected int64) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if a.agentStreamCapacity.snapshot().AdaptiveExternalBytesByOwner[owner] == expected {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("owner %d bytes = %d, want %d", owner, a.agentStreamCapacity.snapshot().AdaptiveExternalBytesByOwner[owner], expected)
}
