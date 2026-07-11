package agent

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"

	"github.com/hashicorp/yamux"

	"p2pstream/internal/tunnel"
)

func TestAgentReconnectBackoffBounds(t *testing.T) {
	originalMin := agentReconnectBackoffMin
	originalMax := agentReconnectBackoffMax
	agentReconnectBackoffMin = time.Second
	agentReconnectBackoffMax = 30 * time.Second
	t.Cleanup(func() {
		agentReconnectBackoffMin = originalMin
		agentReconnectBackoffMax = originalMax
	})

	if got := nextAgentReconnectBackoff(0); got != time.Second {
		t.Fatalf("next backoff from zero = %s, want 1s", got)
	}
	if got := nextAgentReconnectBackoff(time.Second); got != 2*time.Second {
		t.Fatalf("next backoff from 1s = %s, want 2s", got)
	}
	if got := nextAgentReconnectBackoff(20 * time.Second); got != 30*time.Second {
		t.Fatalf("next backoff from 20s = %s, want capped 30s", got)
	}

	for range 20 {
		got := jitterAgentReconnectBackoff(10 * time.Second)
		if got < 8*time.Second || got > 12*time.Second {
			t.Fatalf("jittered backoff = %s, want within +/-20%%", got)
		}
	}
}

func TestTunnelSessionRelaysTCPStream(t *testing.T) {
	resetAgentRequestCounters()
	t.Cleanup(resetAgentRequestCounters)

	upstream := startEchoListener(t)
	clientConn, serverConn := net.Pipe()
	agentSession, err := yamux.Client(clientConn, tunnel.DefaultYamuxConfig(nil))
	if err != nil {
		t.Fatalf("agent yamux client: %v", err)
	}
	serverSession, err := yamux.Server(serverConn, tunnel.DefaultYamuxConfig(nil))
	if err != nil {
		t.Fatalf("server yamux session: %v", err)
	}
	defer agentSession.Close()
	defer serverSession.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- serveTunnelSession(ctx, agentSession, nil)
	}()

	stream, err := serverSession.Open()
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	defer stream.Close()
	if err := tunnel.WriteOpenRequest(stream, tunnel.NewOpenRequest("req-1", "tcp", upstream.Addr().String())); err != nil {
		t.Fatalf("write open request: %v", err)
	}
	resp, err := tunnel.ReadOpenResponse(stream)
	if err != nil {
		t.Fatalf("read open response: %v", err)
	}
	if !resp.OK {
		t.Fatalf("open response = %+v, want ok", resp)
	}
	if _, err := stream.Write([]byte("ping")); err != nil {
		t.Fatalf("write stream: %v", err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(stream, buf); err != nil {
		t.Fatalf("read stream: %v", err)
	}
	if string(buf) != "ping" {
		t.Fatalf("echo = %q, want ping", buf)
	}

	cancel()
	agentSession.Close()
	select {
	case <-serveDone:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for tunnel session to stop")
	}
}

func TestTunnelSessionBoundsConcurrentRequests(t *testing.T) {
	resetAgentRequestCounters()
	t.Cleanup(resetAgentRequestCounters)

	upstream := startEchoListener(t)
	clientConn, serverConn := net.Pipe()
	agentSession, err := yamux.Client(clientConn, tunnel.DefaultYamuxConfig(nil))
	if err != nil {
		t.Fatalf("agent yamux client: %v", err)
	}
	serverSession, err := yamux.Server(serverConn, tunnel.DefaultYamuxConfig(nil))
	if err != nil {
		t.Fatalf("server yamux session: %v", err)
	}
	defer agentSession.Close()
	defer serverSession.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = serveTunnelSessionWithLimit(ctx, agentSession, 1)
	}()

	first, err := serverSession.Open()
	if err != nil {
		t.Fatalf("open first stream: %v", err)
	}
	if err := tunnel.WriteOpenRequest(first, tunnel.NewOpenRequest("req-1", "tcp", upstream.Addr().String())); err != nil {
		t.Fatalf("write first open request: %v", err)
	}
	if resp, err := tunnel.ReadOpenResponse(first); err != nil || !resp.OK {
		t.Fatalf("first open response = %+v, err=%v, want ok", resp, err)
	}

	second, err := serverSession.Open()
	if err != nil {
		t.Fatalf("open second stream: %v", err)
	}
	if err := tunnel.WriteOpenRequest(second, tunnel.NewOpenRequest("req-2", "tcp", upstream.Addr().String())); err != nil {
		t.Fatalf("write second open request: %v", err)
	}
	_ = second.SetReadDeadline(time.Now().Add(time.Second))
	resp, err := tunnel.ReadOpenResponse(second)
	if err != nil {
		t.Fatalf("read capacity response: %v", err)
	}
	if resp.OK || resp.ErrorKind != "agent_capacity" {
		t.Fatalf("second open response = %+v, want agent_capacity", resp)
	}
	_ = second.Close()

	_ = first.Close()
	deadline := time.Now().Add(2 * time.Second)
	for activeRequests.Load() != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := activeRequests.Load(); got != 0 {
		t.Fatalf("active requests after first close = %d, want 0", got)
	}

	third, err := serverSession.Open()
	if err != nil {
		t.Fatalf("open third stream: %v", err)
	}
	defer third.Close()
	if err := tunnel.WriteOpenRequest(third, tunnel.NewOpenRequest("req-3", "tcp", upstream.Addr().String())); err != nil {
		t.Fatalf("write third open request: %v", err)
	}
	if resp, err := tunnel.ReadOpenResponse(third); err != nil || !resp.OK {
		t.Fatalf("third open response = %+v, err=%v, want capacity to recover", resp, err)
	}
}

func TestTunnelSessionReturnsDialFailure(t *testing.T) {
	resetAgentRequestCounters()
	t.Cleanup(resetAgentRequestCounters)

	clientConn, serverConn := net.Pipe()
	agentSession, err := yamux.Client(clientConn, tunnel.DefaultYamuxConfig(nil))
	if err != nil {
		t.Fatalf("agent yamux client: %v", err)
	}
	serverSession, err := yamux.Server(serverConn, tunnel.DefaultYamuxConfig(nil))
	if err != nil {
		t.Fatalf("server yamux session: %v", err)
	}
	defer agentSession.Close()
	defer serverSession.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = serveTunnelSession(ctx, agentSession, nil)
	}()

	stream, err := serverSession.Open()
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	defer stream.Close()
	if err := tunnel.WriteOpenRequest(stream, tunnel.NewOpenRequest("req-1", "tcp", "127.0.0.1:1")); err != nil {
		t.Fatalf("write open request: %v", err)
	}
	resp, err := tunnel.ReadOpenResponse(stream)
	if err != nil {
		t.Fatalf("read open response: %v", err)
	}
	if resp.OK || resp.ErrorKind != "dial_failed" {
		t.Fatalf("open response = %+v, want dial_failed", resp)
	}
}

func TestTunnelSessionDestinationAllowlistDeniesWithoutClosingSession(t *testing.T) {
	resetAgentRequestCounters()
	t.Cleanup(resetAgentRequestCounters)

	upstream := startEchoListener(t)
	clientConn, serverConn := net.Pipe()
	agentSession, err := yamux.Client(clientConn, tunnel.DefaultYamuxConfig(nil))
	if err != nil {
		t.Fatalf("agent yamux client: %v", err)
	}
	serverSession, err := yamux.Server(serverConn, tunnel.DefaultYamuxConfig(nil))
	if err != nil {
		t.Fatalf("server yamux session: %v", err)
	}
	defer agentSession.Close()
	defer serverSession.Close()

	policy, err := newAgentDestinationPolicy([]string{"127.0.0.1"})
	if err != nil {
		t.Fatalf("newAgentDestinationPolicy() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = serveTunnelSession(ctx, agentSession, policy)
	}()

	dialCalled := make(chan struct{}, 1)
	restoreDial := replaceAgentTunnelDialContext(func(ctx context.Context, network string, address string) (net.Conn, error) {
		dialCalled <- struct{}{}
		return nil, context.Canceled
	})
	t.Cleanup(restoreDial)
	denied, err := serverSession.Open()
	if err != nil {
		t.Fatalf("open denied stream: %v", err)
	}
	if err := tunnel.WriteOpenRequest(denied, tunnel.NewOpenRequest("req-denied", "tcp", "127.0.0.2:8080")); err != nil {
		t.Fatalf("write denied open request: %v", err)
	}
	resp, err := tunnel.ReadOpenResponse(denied)
	if err != nil {
		t.Fatalf("read denied open response: %v", err)
	}
	if resp.OK || resp.ErrorKind != "dial_forbidden" {
		t.Fatalf("denied response = %+v, want dial_forbidden", resp)
	}
	select {
	case <-dialCalled:
		t.Fatal("dialer was called for forbidden destination")
	default:
	}
	waitForAgentActiveRequests(t, 0)
	denied.Close()
	restoreDial()

	allowed, err := serverSession.Open()
	if err != nil {
		t.Fatalf("open allowed stream after denied stream: %v", err)
	}
	defer allowed.Close()
	if err := tunnel.WriteOpenRequest(allowed, tunnel.NewOpenRequest("req-allowed", "tcp", upstream.Addr().String())); err != nil {
		t.Fatalf("write allowed open request: %v", err)
	}
	resp, err = tunnel.ReadOpenResponse(allowed)
	if err != nil {
		t.Fatalf("read allowed open response: %v", err)
	}
	if !resp.OK {
		t.Fatalf("allowed response = %+v, want ok", resp)
	}
	if _, err := allowed.Write([]byte("ok")); err != nil {
		t.Fatalf("write allowed stream: %v", err)
	}
	buf := make([]byte, 2)
	if _, err := io.ReadFull(allowed, buf); err != nil {
		t.Fatalf("read allowed echo: %v", err)
	}
	if string(buf) != "ok" {
		t.Fatalf("allowed stream echo = %q, want ok", buf)
	}
}

func TestTunnelSessionOpenRequestReadDeadlineKeepsSessionUsable(t *testing.T) {
	resetAgentRequestCounters()
	t.Cleanup(resetAgentRequestCounters)
	withAgentTunnelOpenRequestTimeout(t, 25*time.Millisecond)

	upstream := startEchoListener(t)
	clientConn, serverConn := net.Pipe()
	agentSession, err := yamux.Client(clientConn, tunnel.DefaultYamuxConfig(nil))
	if err != nil {
		t.Fatalf("agent yamux client: %v", err)
	}
	serverSession, err := yamux.Server(serverConn, tunnel.DefaultYamuxConfig(nil))
	if err != nil {
		t.Fatalf("server yamux session: %v", err)
	}
	defer agentSession.Close()
	defer serverSession.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = serveTunnelSession(ctx, agentSession, nil)
	}()

	silent, err := serverSession.Open()
	if err != nil {
		t.Fatalf("open silent stream: %v", err)
	}
	started := time.Now()
	resp, err := tunnel.ReadOpenResponse(silent)
	if err != nil {
		t.Fatalf("read timeout open response: %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("open request timeout took %s, want under 1s", elapsed)
	}
	if resp.OK || resp.ErrorKind != "open_request_timeout" {
		t.Fatalf("timeout response = %+v, want open_request_timeout", resp)
	}
	silent.Close()

	partial, err := serverSession.Open()
	if err != nil {
		t.Fatalf("open partial stream: %v", err)
	}
	if _, err := partial.Write([]byte{0, 0}); err != nil {
		t.Fatalf("write partial open request frame: %v", err)
	}
	resp, err = tunnel.ReadOpenResponse(partial)
	if err != nil {
		t.Fatalf("read partial-frame timeout response: %v", err)
	}
	if resp.OK || resp.ErrorKind != "open_request_timeout" {
		t.Fatalf("partial-frame timeout response = %+v, want open_request_timeout", resp)
	}
	partial.Close()

	valid, err := serverSession.Open()
	if err != nil {
		t.Fatalf("open valid stream after timeout: %v", err)
	}
	defer valid.Close()
	if err := tunnel.WriteOpenRequest(valid, tunnel.NewOpenRequest("req-valid", "tcp", upstream.Addr().String())); err != nil {
		t.Fatalf("write valid open request: %v", err)
	}
	resp, err = tunnel.ReadOpenResponse(valid)
	if err != nil {
		t.Fatalf("read valid open response: %v", err)
	}
	if !resp.OK {
		t.Fatalf("valid response = %+v, want ok", resp)
	}
	time.Sleep(2 * agentTunnelOpenRequestTimeout)
	if _, err := valid.Write([]byte("ok")); err != nil {
		t.Fatalf("write valid stream after open deadline: %v", err)
	}
	buf := make([]byte, 2)
	if _, err := io.ReadFull(valid, buf); err != nil {
		t.Fatalf("read valid stream echo after open deadline: %v", err)
	}
	if string(buf) != "ok" {
		t.Fatalf("valid stream echo = %q, want ok", buf)
	}
}

func TestTunnelSessionDialTimeoutReturnsDialTimeout(t *testing.T) {
	resetAgentRequestCounters()
	t.Cleanup(resetAgentRequestCounters)
	withAgentTunnelDialTimeout(t, 25*time.Millisecond)

	clientConn, serverConn := net.Pipe()
	agentSession, err := yamux.Client(clientConn, tunnel.DefaultYamuxConfig(nil))
	if err != nil {
		t.Fatalf("agent yamux client: %v", err)
	}
	serverSession, err := yamux.Server(serverConn, tunnel.DefaultYamuxConfig(nil))
	if err != nil {
		t.Fatalf("server yamux session: %v", err)
	}
	defer agentSession.Close()
	defer serverSession.Close()

	upstream := startEchoListener(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = serveTunnelSession(ctx, agentSession, nil)
	}()

	var dialNetwork string
	var dialAddress string
	restoreDial := replaceAgentTunnelDialContext(func(ctx context.Context, network string, address string) (net.Conn, error) {
		dialNetwork = network
		dialAddress = address
		<-ctx.Done()
		return nil, ctx.Err()
	})
	t.Cleanup(restoreDial)

	timeoutStream, err := serverSession.Open()
	if err != nil {
		t.Fatalf("open timeout stream: %v", err)
	}
	if err := tunnel.WriteOpenRequest(timeoutStream, tunnel.NewOpenRequest("req-timeout", "tcp", "203.0.113.10:443")); err != nil {
		t.Fatalf("write timeout open request: %v", err)
	}
	resp, err := tunnel.ReadOpenResponse(timeoutStream)
	if err != nil {
		t.Fatalf("read timeout open response: %v", err)
	}
	if resp.OK || resp.ErrorKind != "dial_timeout" {
		t.Fatalf("timeout response = %+v, want dial_timeout", resp)
	}
	if dialNetwork != "tcp" || dialAddress != "203.0.113.10:443" {
		t.Fatalf("dialed network/address = %q %q", dialNetwork, dialAddress)
	}
	waitForAgentActiveRequests(t, 0)
	timeoutStream.Close()

	restoreDial()
	valid, err := serverSession.Open()
	if err != nil {
		t.Fatalf("open valid stream after dial timeout: %v", err)
	}
	defer valid.Close()
	if err := tunnel.WriteOpenRequest(valid, tunnel.NewOpenRequest("req-valid", "tcp", upstream.Addr().String())); err != nil {
		t.Fatalf("write valid open request: %v", err)
	}
	resp, err = tunnel.ReadOpenResponse(valid)
	if err != nil {
		t.Fatalf("read valid open response: %v", err)
	}
	if !resp.OK {
		t.Fatalf("valid response = %+v, want ok", resp)
	}
}

func TestAgentTunnelDialerUsesConfiguredTimeout(t *testing.T) {
	withAgentTunnelDialTimeout(t, 37*time.Millisecond)
	if got := agentTunnelDialer().Timeout; got != 37*time.Millisecond {
		t.Fatalf("agent tunnel dial timeout = %s, want 37ms", got)
	}
}

func TestTunnelSessionInvalidOpenRequestKeepsSessionUsable(t *testing.T) {
	resetAgentRequestCounters()
	t.Cleanup(resetAgentRequestCounters)

	upstream := startEchoListener(t)
	clientConn, serverConn := net.Pipe()
	agentSession, err := yamux.Client(clientConn, tunnel.DefaultYamuxConfig(nil))
	if err != nil {
		t.Fatalf("agent yamux client: %v", err)
	}
	serverSession, err := yamux.Server(serverConn, tunnel.DefaultYamuxConfig(nil))
	if err != nil {
		t.Fatalf("server yamux session: %v", err)
	}
	defer agentSession.Close()
	defer serverSession.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = serveTunnelSession(ctx, agentSession, nil)
	}()

	unsupported, err := serverSession.Open()
	if err != nil {
		t.Fatalf("open unsupported-version stream: %v", err)
	}
	if err := tunnel.WriteFrame(unsupported, tunnel.OpenRequest{Version: 2, Network: "tcp", Address: upstream.Addr().String()}); err != nil {
		t.Fatalf("write unsupported request: %v", err)
	}
	resp, err := tunnel.ReadOpenResponse(unsupported)
	if err != nil {
		t.Fatalf("read unsupported response: %v", err)
	}
	if resp.OK || resp.ErrorKind != "unsupported_version" {
		t.Fatalf("unsupported response = %+v, want unsupported_version", resp)
	}
	unsupported.Close()

	malformed, err := serverSession.Open()
	if err != nil {
		t.Fatalf("open malformed stream: %v", err)
	}
	if err := writeMalformedControlFrame(malformed); err != nil {
		t.Fatalf("write malformed request: %v", err)
	}
	resp, err = tunnel.ReadOpenResponse(malformed)
	if err != nil {
		t.Fatalf("read malformed response: %v", err)
	}
	if resp.OK || resp.ErrorKind != "invalid_open_request" {
		t.Fatalf("malformed response = %+v, want invalid_open_request", resp)
	}
	malformed.Close()

	valid, err := serverSession.Open()
	if err != nil {
		t.Fatalf("open valid stream after invalid requests: %v", err)
	}
	defer valid.Close()
	if err := tunnel.WriteOpenRequest(valid, tunnel.NewOpenRequest("req-valid", "tcp", upstream.Addr().String())); err != nil {
		t.Fatalf("write valid open request: %v", err)
	}
	resp, err = tunnel.ReadOpenResponse(valid)
	if err != nil {
		t.Fatalf("read valid open response: %v", err)
	}
	if !resp.OK {
		t.Fatalf("valid response = %+v, want ok", resp)
	}
	if _, err := valid.Write([]byte("ok")); err != nil {
		t.Fatalf("write valid stream: %v", err)
	}
	buf := make([]byte, 2)
	if _, err := io.ReadFull(valid, buf); err != nil {
		t.Fatalf("read valid stream echo: %v", err)
	}
	if string(buf) != "ok" {
		t.Fatalf("valid stream echo = %q, want ok", buf)
	}
}

func TestTunnelSessionReturnsWhenSessionCloses(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	agentSession, err := yamux.Client(clientConn, tunnel.DefaultYamuxConfig(nil))
	if err != nil {
		t.Fatalf("agent yamux client: %v", err)
	}
	serverSession, err := yamux.Server(serverConn, tunnel.DefaultYamuxConfig(nil))
	if err != nil {
		t.Fatalf("server yamux session: %v", err)
	}
	defer serverSession.Close()

	done := make(chan error, 1)
	go func() {
		done <- serveTunnelSession(context.Background(), agentSession, nil)
	}()
	agentSession.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for serveTunnelSession to return after session close")
	}
}

func TestTunnelSessionWaitsForAcceptedHandlerOnSessionClose(t *testing.T) {
	resetAgentRequestCounters()

	clientConn, serverConn := net.Pipe()
	agentSession, err := yamux.Client(clientConn, tunnel.DefaultYamuxConfig(nil))
	if err != nil {
		t.Fatalf("agent yamux client: %v", err)
	}
	serverSession, err := yamux.Server(serverConn, tunnel.DefaultYamuxConfig(nil))
	if err != nil {
		_ = agentSession.Close()
		t.Fatalf("server yamux session: %v", err)
	}

	dialStarted := make(chan struct{})
	releaseDial := make(chan struct{})
	released := false
	release := func() {
		if released {
			return
		}
		released = true
		close(releaseDial)
	}
	restoreDial := replaceAgentTunnelDialContext(func(context.Context, string, string) (net.Conn, error) {
		close(dialStarted)
		<-releaseDial
		return nil, context.Canceled
	})
	t.Cleanup(restoreDial)

	serveDone := make(chan struct{})
	go func() {
		_ = serveTunnelSession(context.Background(), agentSession, nil)
		close(serveDone)
	}()
	t.Cleanup(func() {
		defer resetAgentRequestCounters()
		release()
		_ = agentSession.Close()
		_ = serverSession.Close()
		select {
		case <-serveDone:
		case <-time.After(2 * time.Second):
			t.Error("timed out waiting for tunnel session handlers to stop")
		}
		waitForAgentActiveRequests(t, 0)
	})

	stream, err := serverSession.Open()
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	if err := tunnel.WriteOpenRequest(stream, tunnel.NewOpenRequest("req-blocked", "tcp", "127.0.0.1:1")); err != nil {
		t.Fatalf("write open request: %v", err)
	}
	select {
	case <-dialStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for tunnel handler to start")
	}
	waitForAgentActiveRequests(t, 1)

	_ = agentSession.Close()
	select {
	case <-serveDone:
		t.Fatal("tunnel session returned before its accepted handler stopped")
	case <-time.After(50 * time.Millisecond):
	}

	release()
	select {
	case <-serveDone:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for tunnel session after handler stopped")
	}
	waitForAgentActiveRequests(t, 0)
}

func writeMalformedControlFrame(w io.Writer) error {
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], 1)
	if _, err := w.Write(header[:]); err != nil {
		return err
	}
	_, err := w.Write([]byte("{"))
	return err
}

func startEchoListener(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen echo: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
	return ln
}

func startTestTunnelSession(t *testing.T, agentSession *yamux.Session, destinationPolicy *agentDestinationPolicy) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- serveTunnelSession(ctx, agentSession, destinationPolicy)
	}()

	t.Cleanup(func() {
		defer resetAgentRequestCounters()
		cancel()
		_ = agentSession.Close()
		select {
		case <-serveDone:
		case <-time.After(2 * time.Second):
			t.Error("timed out waiting for tunnel session to stop")
		}
		waitForAgentActiveRequests(t, 0)
	})
}

func resetAgentRequestCounters() {
	activeRequests.Store(0)
	reqSuccess.Store(0)
	reqClientError.Store(0)
	reqServerError.Store(0)
	reqInternalError.Store(0)
}

func withAgentTunnelOpenRequestTimeout(t *testing.T, timeout time.Duration) {
	t.Helper()
	previous := agentTunnelOpenRequestTimeout
	agentTunnelOpenRequestTimeout = timeout
	t.Cleanup(func() {
		agentTunnelOpenRequestTimeout = previous
	})
}

func withAgentTunnelDialTimeout(t *testing.T, timeout time.Duration) {
	t.Helper()
	previous := agentTunnelDialTimeout
	agentTunnelDialTimeout = timeout
	t.Cleanup(func() {
		agentTunnelDialTimeout = previous
	})
}

func replaceAgentTunnelDialContext(fn func(context.Context, string, string) (net.Conn, error)) func() {
	previous := agentTunnelDialNetwork
	restored := false
	agentTunnelDialNetwork = fn
	return func() {
		if restored {
			return
		}
		restored = true
		agentTunnelDialNetwork = previous
	}
}

func waitForAgentActiveRequests(t *testing.T, want int32) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got := activeRequests.Load(); got == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("activeRequests = %d, want %d", activeRequests.Load(), want)
}
