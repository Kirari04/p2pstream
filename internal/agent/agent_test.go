package agent

import (
	"context"
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
		serveDone <- serveTunnelSession(ctx, agentSession)
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
		_ = serveTunnelSession(ctx, agentSession)
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

func resetAgentRequestCounters() {
	activeRequests.Store(0)
	reqSuccess.Store(0)
	reqClientError.Store(0)
	reqServerError.Store(0)
	reqInternalError.Store(0)
}
