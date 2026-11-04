package server

import (
	"bytes"
	"encoding/binary"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/yamux"
	"p2pstream/internal/tunnel"
)

func TestAuthenticatedRetirementDiscardsPreparedLaneWhenTrafficResumes(t *testing.T) {
	testAuthenticatedRetirementWithBufferedSYN(t, false)
}

// Exercise the server handler's defensive barrier independently of normal idle
// publication. Removing that barrier must lose the admitted opening on this
// forced path, even though the ordinary idle-policy regression still passes.
func TestAuthenticatedRetirementBarrierPreservesBufferedSYN(t *testing.T) {
	testAuthenticatedRetirementWithBufferedSYN(t, true)
}

func testAuthenticatedRetirementWithBufferedSYN(t *testing.T, forcePublish bool) {
	t.Helper()
	ready, resume := make(chan struct{}), make(chan struct{})
	var once sync.Once
	var unblock sync.Once
	defer unblock.Do(func() { close(resume) })
	var wire atomic.Pointer[retirementBufferedSYNConn]
	f := newConfiguredRealAgentProxyFixture(t, 1, 4<<20, 0, func(a *App) {
		a.agentTunnelForceRetirementPublish = forcePublish
		a.agentTunnelWrapConn = func(lane int, c net.Conn) net.Conn {
			if lane != 1 {
				return c
			}
			gate := &retirementBufferedSYNConn{Conn: c, sent: make(chan struct{}), release: make(chan struct{})}
			wire.CompareAndSwap(nil, gate)
			return gate
		}
		a.agentTunnelBeforeRetirementPublish = func() { once.Do(func() { close(ready); <-resume }) }
	}, 100*time.Millisecond)
	origin, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		for {
			c, err := origin.Accept()
			if err != nil {
				return
			}
			workers.Add(1)
			go func() { defer workers.Done(); defer c.Close(); io.Copy(c, c) }()
		}
	}()
	defer func() { origin.Close(); workers.Wait() }()
	dial := func() net.Conn {
		c, err := f.app.dialViaAgent(t.Context(), f.agent, "tcp", origin.Addr().String(), "idle-resume")
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	first := dial()
	defer first.Close()
	stream := first.(*agentTunnelStreamConn).Conn.(*tunnel.GrowingConn).Conn.(*yamux.Stream)
	old := stream.Session()
	owner := f.agent.tunnelOwners.Find(old)
	baseline := f.agent.tunnelOwners.ReservedBytes()
	if ok, err := owner.Growth().Grow(stream, 1<<20); !ok || err != nil {
		t.Fatalf("grow %v %v", ok, err)
	}
	first.Close()
	select {
	case <-ready:
	case <-time.After(3 * time.Second):
		t.Fatal("replacement readiness not reached")
	}
	// This is a real authenticated server+agent TCP opening, admitted while the
	// replacement readiness protocol is complete but publication is held.
	gate := wire.Load()
	gate.armed.Store(true)
	opened := make(chan net.Conn, 1)
	openErr := make(chan error, 1)
	go func() {
		c, err := f.app.dialViaAgent(t.Context(), f.agent, "tcp", origin.Addr().String(), "in-transit")
		opened <- c
		openErr <- err
	}()
	openJoined := false
	defer func() {
		gate.unblock()
		if !openJoined {
			c := <-opened
			<-openErr
			if c != nil {
				c.Close()
			}
		}
	}()
	select {
	case <-gate.sent:
	case <-time.After(time.Second):
		t.Fatal("SYN not published")
	}
	unblock.Do(func() { close(resume) })
	var preserved bool
	if forcePublish {
		awaitLaneRetirement(t, "forced replacement not published", func() bool { return f.agent.tunnelSession(1) != old })
		// Keep the SYN below the old wire through multiple 100ms drain checks.
		// A premature new-wire commit would let the agent cancel its locally
		// empty receiver. The server barrier must keep that receiver alive.
		time.Sleep(300 * time.Millisecond)
		preserved = !old.IsClosed() && owner.State() == tunnel.LaneDraining
	} else {
		awaitLaneRetirement(t, "prepared replacement not discarded", func() bool { return len(f.agent.tunnelOwners.Snapshot()) == 2 })
		preserved = f.agent.tunnelSession(1) == old && owner.State() == tunnel.LaneServing
	}
	gate.unblock()
	active := <-opened
	err = <-openErr
	openJoined = true
	if active != nil {
		defer active.Close()
	}
	if err != nil || !preserved {
		t.Fatalf("in-transit admitted opening lost: preserved=%v err=%v", preserved, err)
	}
	payload := bytes.Repeat([]byte("nonreplayable"), 1024)
	active.SetDeadline(time.Now().Add(time.Second))
	if _, err := active.Write(payload); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(active, got); err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("resumed bytes: %v", err)
	}
	if forcePublish {
		// A fresh request must also pass through the committed replacement; route
		// publication alone would not prove the agent finished its readiness wait.
		if n, err := f.roundTrip(t.Context(), "/api", nil); err != nil || n != 2 {
			t.Fatalf("replacement request: %d %v", n, err)
		}
		if owner.State() != tunnel.LaneDraining || old.IsClosed() {
			t.Fatal("commit discarded the still-owned old stream")
		}
	}
	active.Close()
	awaitLaneRetirement(t, "idle growth not reclaimed", func() bool { return old.IsClosed() && f.agent.tunnelOwners.ReservedBytes() == baseline })
}

func TestAuthenticatedIdleRetirementWaitsForHTTPKeepAliveExpiry(t *testing.T) {
	f := newRealAgentProxyFixture(t, 1, 4<<20, 0, 100*time.Millisecond)
	if n, err := f.roundTrip(t.Context(), "/api", nil); err != nil || n != 2 {
		t.Fatalf("warmup %d %v", n, err)
	}
	old := f.agent.tunnelSession(1)
	owner := f.agent.tunnelOwners.Find(old)
	baseline := f.agent.tunnelOwners.ReservedBytes()
	// Grow through a separately owned production TCP stream, leaving the pooled
	// HTTP connection as the sole owner after this one is disposed.
	c, err := f.app.dialViaAgent(t.Context(), f.agent, "tcp", f.upstream.Listener.Addr().String(), "grow-idle")
	if err != nil {
		t.Fatal(err)
	}
	stream := c.(*agentTunnelStreamConn).Conn.(*tunnel.GrowingConn).Conn.(*yamux.Stream)
	if ok, err := owner.Growth().Grow(stream, 1<<20); !ok || err != nil {
		c.Close()
		t.Fatalf("grow %v %v", ok, err)
	}
	c.Close()
	for range 3 {
		time.Sleep(220 * time.Millisecond)
		if n, err := f.roundTrip(t.Context(), "/api", nil); err != nil || n != 2 {
			t.Fatalf("ongoing use %d %v", n, err)
		}
		if f.agent.tunnelSession(1) != old || owner.Quiescent() {
			t.Fatal("pooled keepalive ownership bypassed")
		}
	}
	// Use the production 30s transport idle timeout, not a test override or an
	// explicit CloseIdleConnections. Add quiet/probe and bounded FIN completion.
	deadline := time.Now().Add(35 * time.Second)
	for !old.IsClosed() || f.agent.tunnelOwners.ReservedBytes() != baseline {
		if time.Now().After(deadline) {
			t.Fatal("idle keepalive/growth did not reclaim within bound")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if stats := old.AggregateReceiveWindowStats(); stats.TotalUnits != 0 {
		t.Fatalf("physical pool retained: %+v", stats)
	}
}

// Model a SYN accepted by the local TCP send buffer but not yet delivered to
// the peer. No sender retry is involved: the next write flushes it exactly once.
// Close wakes the held writer, so failure cleanup remains finite and joined.
type retirementBufferedSYNConn struct {
	net.Conn
	armed   atomic.Bool
	sent    chan struct{}
	release chan struct{}
	once    sync.Once
	pending []byte // owned exclusively by Yamux's single writer
}

func (c *retirementBufferedSYNConn) unblock()     { c.once.Do(func() { close(c.release) }) }
func (c *retirementBufferedSYNConn) Close() error { c.unblock(); return c.Conn.Close() }
func (c *retirementBufferedSYNConn) Write(p []byte) (int, error) {
	if len(c.pending) > 0 {
		<-c.release
		if _, err := c.Conn.Write(c.pending); err != nil {
			return 0, err
		}
		c.pending = nil
	}
	if len(p) >= 12 && p[1] == 1 && binary.BigEndian.Uint16(p[2:4])&1 != 0 && c.armed.CompareAndSwap(true, false) {
		c.pending = append([]byte(nil), p...)
		close(c.sent)
		return len(p), nil
	}
	return c.Conn.Write(p)
}
