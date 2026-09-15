//go:build linux

package agent

import (
	"context"
	"net"
	"testing"

	"p2pstream/internal/sysmetrics"
	"p2pstream/internal/tcpsocket"
)

func TestAgentAutotunedOriginSocketOwnsAdditionalCredit(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	usage := sysmetrics.MemoryUsage{UsedBytes: 64 << 20, LimitBytes: 16 << 30, Source: "test"}
	capacity := newTestAgentAdaptiveCapacity(t, &usage)
	releaseStream, _, ok := capacity.tryAcquire()
	if !ok {
		t.Fatal("stream rejected")
	}
	defer releaseStream()
	ctx := context.WithValue(t.Context(), upstreamCapacityContextKey{}, capacity)
	conn, err := dialTunnelNetwork(ctx, "tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	tracked := conn.(*tcpsocket.AccountedConn)
	budget, err := tcpsocket.Configure(tracked.TCPConn, 0)
	if err != nil {
		t.Fatal(err)
	}
	capacity.mu.Lock()
	charged := capacity.windowBytes
	capacity.mu.Unlock()
	if charged != max(0, budget-tcpsocket.BaseMemoryBytes) {
		t.Fatalf("additional charge %d, budget %d", charged, budget)
	}
	_ = tracked.CloseWrite()
	capacity.mu.Lock()
	retained := capacity.windowBytes
	capacity.mu.Unlock()
	if retained != charged {
		t.Fatal("half-close released socket credit")
	}
	_ = tracked.Close()
	_ = tracked.Close()
	capacity.mu.Lock()
	remaining := capacity.windowBytes
	capacity.mu.Unlock()
	if remaining != 0 || capacity.snapshot().InUse != 1 {
		t.Fatal("socket close leaked credit or released its parent stream")
	}

	usage.UsedBytes = 15 << 30
	capacity.forceRefresh()
	if conn, err := dialTunnelNetwork(ctx, "tcp", ln.Addr().String()); err == nil {
		_ = conn.Close()
		t.Fatal("unreserved autotuned socket admitted under pressure")
	} else if tunnelDialErrorKind(err) != "agent_resource_pressure" {
		t.Fatalf("local pressure misreported as origin failure: %v", err)
	}
	capacity.mu.Lock()
	remaining = capacity.windowBytes
	capacity.mu.Unlock()
	if remaining != 0 {
		t.Fatal("rejected socket leaked credit")
	}
}
