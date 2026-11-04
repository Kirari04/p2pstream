package server

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/yamux"
	"p2pstream/internal/tunnel"
)

func awaitLaneRetirement(t *testing.T, why string, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !f() {
		if time.Now().After(deadline) {
			t.Fatal(why)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestRealAggregateRetirementPreservesOldBytesAndNewPublicRequests(t *testing.T) {
	for _, ending := range []string{"drain", "revoke", "generation"} {
		t.Run(ending, func(t *testing.T) { testRealAggregateRetirement(t, ending) })
	}
}
func testRealAggregateRetirement(t *testing.T, ending string) {
	f := newRealAgentProxyFixture(t, 1, 4<<20, 0, 300*time.Millisecond)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var origins sync.WaitGroup
	origins.Add(1)
	go func() {
		defer origins.Done()
		for {
			c, err := listener.Accept()
			if err != nil {
				return
			}
			origins.Add(1)
			go func() { defer origins.Done(); defer c.Close(); io.Copy(c, c) }()
		}
	}()
	defer func() { listener.Close(); origins.Wait() }()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	conn, err := f.app.dialViaAgent(ctx, f.agent, "tcp", listener.Addr().String(), "retirement-old")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	managed := conn.(*agentTunnelStreamConn)
	stream := managed.Conn.(*tunnel.GrowingConn).Conn.(*yamux.Stream)
	old := stream.Session()
	owner := f.agent.tunnelOwners.Find(old)
	if owner == nil {
		t.Fatal("missing authenticated owner")
	}
	baseline := f.agent.tunnelOwners.ReservedBytes()
	httpPrefix := bytes.Repeat([]byte("http-prefix"), 1024)
	httpTail := bytes.Repeat([]byte("http-tail"), 8192)
	releaseBody := make(chan struct{})
	var releaseOnce sync.Once
	finishBody := func() { releaseOnce.Do(func() { close(releaseBody) }) }
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/held" {
			io.WriteString(w, "ok")
			return
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(httpPrefix)+len(httpTail)))
		w.Write(httpPrefix)
		w.(http.Flusher).Flush()
		select {
		case <-releaseBody:
			w.Write(httpTail)
		case <-r.Context().Done():
		}
	}))
	defer origin.Close()
	defer finishBody()
	public := startPressurePublicListener(t, f, origin.URL)
	client := &http.Client{Timeout: 5 * time.Second}
	heldReq, _ := http.NewRequestWithContext(ctx, "GET", "http://"+public+"/held", nil)
	heldReq.Host = "pressure.test"
	heldResponse, err := client.Do(heldReq)
	if err != nil {
		t.Fatal(err)
	}
	defer heldResponse.Body.Close()
	prefix := make([]byte, len(httpPrefix))
	if _, err := io.ReadFull(heldResponse.Body, prefix); err != nil || !bytes.Equal(prefix, httpPrefix) {
		t.Fatalf("HTTP prefix: %v", err)
	}
	// An update drain must fence rotation without aborting existing streams.
	if ending == "drain" {
		f.app.setAgentUpdateCordon(f.agent.AgentID)
	}
	if ok, err := owner.Growth().Grow(stream, 4<<20); !ok || err != nil {
		t.Fatalf("real owner growth %v %v", ok, err)
	}
	if old.AggregateReceiveWindowStats().ReceiveLimit != 16<<20 {
		t.Fatal("pool did not physically grow")
	}
	if ending == "drain" {
		time.Sleep(650 * time.Millisecond) // more than two negotiated probe periods
		if f.agent.tunnelSession(1) != old || old.IsClosed() || len(f.agent.tunnelOwners.Snapshot()) != 2 {
			t.Fatal("update cordon allowed replacement or truncated active work")
		}
		if f.app.agentUpdateAgentDrained(f.agent.AgentID) {
			t.Fatal("update readiness ignored active owners")
		}
		f.app.clearAgentUpdateCordon(f.agent.AgentID)
	}
	time.Sleep(650 * time.Millisecond) // active traffic must survive multiple idle probes
	replacement := f.agent.tunnelSession(1)
	if replacement != old || old.IsClosed() || owner.State() != tunnel.LaneServing {
		t.Fatal("active old lane truncated")
	}
	if f.agent.tunnelAggregateGrowth(1, old) != owner.Growth() {
		t.Fatal("old request looked up replacement growth")
	}
	if f.app.agentUpdateAgentDrained(f.agent.AgentID) {
		t.Fatal("updater ignored draining owner")
	}
	// Keep the grown lane active past several probe periods. Automatic retirement
	// must preserve it, including while pooled public requests continue.

	for i := 0; i < 5; i++ {
		payload := bytes.Repeat([]byte{byte(i + 1)}, 64<<10)
		done := make(chan error, 1)
		go func() { _, err := conn.Write(payload); done <- err }()
		got := make([]byte, len(payload))
		if _, err := io.ReadFull(conn, got); err != nil || !bytes.Equal(got, payload) {
			t.Fatalf("old bytes changed: %v", err)
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		req, _ := http.NewRequestWithContext(ctx, "GET", "http://"+public+"/api", nil)
		req.Host = "pressure.test"
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil || resp.StatusCode != 200 || string(body) != "ok" {
			t.Fatalf("fresh public response %d %q %v", resp.StatusCode, body, err)
		}
		if f.agent.tunnelSession(1) != replacement || len(f.agent.tunnelOwners.Snapshot()) != 2 {
			t.Fatal("unbounded replacement while old drains")
		}
		time.Sleep(100 * time.Millisecond)
	}
	if ending != "drain" {
		if ending == "revoke" {
			if !f.app.revokeAgentConnection(f.agent.AgentID) {
				t.Fatal("revocation failed")
			}
		} else {
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, f.management.URL+"/agent/tunnel", nil)
			req.Header.Set("Authorization", "Bearer test-token")
			req.Header.Set("X-P2PStream-Agent-ID", f.agent.PublicID)
			req.Header.Set("Connection", "Upgrade")
			req.Header.Set("Upgrade", tunnel.UpgradeToken)
			req.Header.Set(tunnel.TunnelVersionHeader, "1")
			req.Header.Set(tunnel.TunnelMaxConcurrentStreamsHeader, "64")
			req.Header.Set(tunnel.TunnelCapacityModeHeader, tunnel.TunnelCapacityModeFixed)
			req.Header.Set(tunnel.TunnelGroupHeader, "fresh-generation-0123456789")
			req.Header.Set(tunnel.TunnelLaneHeader, "0")
			tunnel.SetAggregateFlowControlHeaders(req.Header, tunnel.TunnelLaneRoleControl, tunnel.DefaultAggregateFlowControlConfig())
			response, err := f.management.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			if response.StatusCode != 101 {
				response.Body.Close()
				t.Fatalf("new generation status%d", response.StatusCode)
			}
			config, err := tunnel.NewNegotiatedControlYamuxConfig(nil, tunnel.DefaultAdaptiveReceiveWindowBytes, -1)
			if err != nil {
				t.Fatal(err)
			}
			peer, err := yamux.Client(response.Body.(io.ReadWriteCloser), config)
			if err != nil {
				t.Fatal(err)
			}
			defer peer.Close()
		}
		awaitLaneRetirement(t, "generation failed to join serving and draining owners", func() bool {
			return f.agent.tunnelOwners.ReservedBytes() == 0 && len(f.agent.tunnelOwners.Snapshot()) == 0
		})
		for _, session := range []*yamux.Session{old, replacement} {
			if stats := session.AggregateReceiveWindowStats(); !stats.Closed || stats.TotalUnits != 0 {
				t.Fatalf("generation leaked slabs: %+v", stats)
			}
		}
		conn.SetReadDeadline(time.Now().Add(time.Second))
		if _, err := conn.Read(make([]byte, 1)); err == nil {
			t.Fatal("revoked old stream stayed live")
		}
		conn.Close()
		f.app.CloseAgentTransports()
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), time.Second)
		defer cleanupCancel()
		f.app.CloseObservabilityRecorder(cleanupCtx)
		return
	}
	finishBody()
	tail, err := io.ReadAll(heldResponse.Body)
	heldResponse.Body.Close()
	if err != nil || !bytes.Equal(tail, httpTail) {
		t.Fatalf("active HTTP body truncated across replacement: %v", err)
	}
	conn.Close()
	f.app.CloseAgentTransports() // explicitly dispose idle HTTP keep-alives in this lifecycle test
	awaitLaneRetirement(t, "grown pool/lease not reclaimed", func() bool {
		return old.AggregateReceiveWindowStats().Closed && old.AggregateReceiveWindowStats().TotalUnits == 0 && f.agent.tunnelOwners.ReservedBytes() == baseline
	})
	if f.agent.tunnelOwners.Find(old) != nil {
		t.Fatal("old owner retained after physical cleanup")
	}
	if len(f.agent.tunnelOwners.Snapshot()) != 2 {
		t.Fatal("draining registry entry retained")
	}
	// Repeated grow/quiesce cycles cannot accumulate draining owners or leases.
	for range 3 {
		c, err := f.app.dialViaAgent(ctx, f.agent, "tcp", listener.Addr().String(), "repeat")
		if err != nil {
			t.Fatal(err)
		}
		stream := c.(*agentTunnelStreamConn).Conn.(*tunnel.GrowingConn).Conn.(*yamux.Stream)
		session := stream.Session()
		owner := f.agent.tunnelOwners.Find(session)
		if ok, err := owner.Growth().Grow(stream, 1<<20); !ok || err != nil {
			t.Fatalf("repeat growth %v %v", ok, err)
		}
		c.Close()
		awaitLaneRetirement(t, "repeat retirement retained pool", func() bool {
			return session.AggregateReceiveWindowStats().Closed && session.AggregateReceiveWindowStats().TotalUnits == 0 && f.agent.tunnelOwners.ReservedBytes() == baseline
		})
	}
	f.app.CloseAgentTransports()
	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), time.Second)
	defer cleanupCancel()
	f.app.CloseObservabilityRecorder(cleanupCtx)
}
