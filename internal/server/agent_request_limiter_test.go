package server

import (
	"context"
	"fmt"
	"io"
	"net"
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
	conn := newAgentTunnelStreamConn(&delayedRemoteCloseConn{
		Conn:         local,
		remoteClosed: remoteClosed,
	}, release)

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
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if limiter.InUse() == 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("stream slots after remote close = %d, want 0", limiter.InUse())
}

type delayedRemoteCloseConn struct {
	net.Conn
	remoteClosed <-chan struct{}
}

func (c *delayedRemoteCloseConn) Close() error {
	return nil
}

func (c *delayedRemoteCloseConn) Read([]byte) (int, error) {
	<-c.remoteClosed
	return 0, io.EOF
}
