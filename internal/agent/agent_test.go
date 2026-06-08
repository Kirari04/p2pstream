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
		_ = serveTunnelSession(ctx, agentSession)
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
		done <- serveTunnelSession(context.Background(), agentSession)
	}()
	agentSession.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for serveTunnelSession to return after session close")
	}
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

func resetAgentRequestCounters() {
	activeRequests.Store(0)
	reqSuccess.Store(0)
	reqClientError.Store(0)
	reqServerError.Store(0)
	reqInternalError.Store(0)
}
