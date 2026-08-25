package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"p2pstream/internal/tunnel"
)

func TestAgentRequestLimiterBoundsAndReleasesCapacity(t *testing.T) {
	limiter := newAgentRequestLimiter(2)
	releaseFirst, ok := limiter.TryAcquire()
	if !ok {
		t.Fatal("first acquire failed")
	}
	releaseSecond, ok := limiter.TryAcquire()
	if !ok {
		t.Fatal("second acquire failed")
	}
	if got := limiter.InUse(); got != 2 {
		t.Fatalf("in-use requests = %d, want 2", got)
	}
	if release, ok := limiter.TryAcquire(); ok {
		release()
		t.Fatal("acquire beyond configured capacity succeeded")
	}

	releaseFirst()
	releaseFirst()
	if got := limiter.InUse(); got != 1 {
		t.Fatalf("in-use requests after idempotent release = %d, want 1", got)
	}
	releaseThird, ok := limiter.TryAcquire()
	if !ok {
		t.Fatal("capacity was not reusable after release")
	}
	releaseThird()
	releaseSecond()
	if got := limiter.InUse(); got != 0 {
		t.Fatalf("in-use requests after release = %d, want 0", got)
	}
}

func TestAgentCapacityDoesNotMarkPassiveFailure(t *testing.T) {
	err := fmt.Errorf("dial agent: %w", agentDialError{Kind: "agent_capacity", Err: "full"})
	if shouldMarkAgentPassiveFailure(context.Background(), err) {
		t.Fatal("agent capacity should not mark the agent passively unhealthy")
	}
}

func TestAgentRequestLimiterUsesSafeDefaultForUnsetOrInvalidLimit(t *testing.T) {
	for _, limit := range []int64{0, -1} {
		limiter := newAgentRequestLimiter(limit)
		if got := limiter.Capacity(); got != int(tunnel.DefaultMaxConcurrentAgentRequests) {
			t.Fatalf("limit %d created capacity %d, want %d", limit, got, tunnel.DefaultMaxConcurrentAgentRequests)
		}
	}
}

func TestAgentTunnelStreamConnReleasesAfterRemoteClose(t *testing.T) {
	limiter := newAgentRequestLimiter(1)
	release, ok := limiter.TryAcquire()
	if !ok {
		t.Fatal("acquire stream slot failed")
	}
	local, peer := net.Pipe()
	defer local.Close()
	defer peer.Close()
	remoteClosed := make(chan struct{})
	readStarted := make(chan struct{})
	conn := newAgentTunnelStreamConn(&delayedRemoteCloseConn{
		Conn:         local,
		remoteClosed: remoteClosed,
		readStarted:  readStarted,
	}, nil, release)
	readerDone := make(chan error, 1)
	go func() {
		var buf [1]byte
		_, err := conn.Read(buf[:])
		readerDone <- err
	}()
	select {
	case <-readStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for existing connection reader")
	}

	if err := conn.Close(); err != nil {
		t.Fatalf("close connection: %v", err)
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("close connection again: %v", err)
	}
	if got := limiter.InUse(); got != 1 {
		t.Fatalf("stream slots after local close = %d, want 1 until remote close", got)
	}

	close(remoteClosed)
	select {
	case err := <-readerDone:
		if !errors.Is(err, io.EOF) {
			t.Fatalf("existing reader error = %v, want EOF", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("existing reader did not finish after remote close")
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if limiter.InUse() == 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("stream slots after remote close = %d, want 0", limiter.InUse())
}

func TestAgentTunnelStreamConnDistinguishesAgentLossFromOriginEOF(t *testing.T) {
	local, peer := net.Pipe()
	agent := &AgentConn{Done: make(chan struct{})}
	conn := newAgentTunnelStreamConn(local, agent, nil)
	close(agent.Done)
	if err := peer.Close(); err != nil {
		t.Fatalf("close peer: %v", err)
	}
	var buf [1]byte
	_, err := conn.Read(buf[:])
	if !errors.Is(err, errAgentDisconnected) || !errors.Is(err, io.EOF) {
		t.Fatalf("agent-loss read error = %v, want agent disconnected wrapping EOF", err)
	}
	_ = conn.Close()
}

type delayedRemoteCloseConn struct {
	net.Conn
	remoteClosed  <-chan struct{}
	readStarted   chan struct{}
	readStartOnce sync.Once
}

func (c *delayedRemoteCloseConn) Close() error {
	return nil
}

func (c *delayedRemoteCloseConn) Read([]byte) (int, error) {
	c.readStartOnce.Do(func() { close(c.readStarted) })
	<-c.remoteClosed
	return 0, io.EOF
}
