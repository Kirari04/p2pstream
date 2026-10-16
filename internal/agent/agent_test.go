package agent

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/yamux"

	"p2pstream/internal/sysmetrics"
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

func TestNegotiatedAgentTunnelCapacityRequiredHeaders(t *testing.T) {
	tests := []struct {
		name              string
		adaptiveRequested bool
		responseMode      string
		responseLimit     string
		want              int64
		wantErr           bool
	}{
		{name: "fixed mode", responseMode: tunnel.TunnelCapacityModeFixed, responseLimit: "256", want: 256},
		{name: "missing capacity", responseMode: tunnel.TunnelCapacityModeFixed, wantErr: true},
		{name: "new agent new server", adaptiveRequested: true, responseMode: tunnel.TunnelCapacityModeAdaptive, responseLimit: "65536", want: 65536},
		{name: "new agent old server", adaptiveRequested: true, responseLimit: "256", wantErr: true},
		{name: "new agent rejects unacknowledged adaptive limit", adaptiveRequested: true, responseLimit: "4096", wantErr: true},
		{name: "fixed agent rejects adaptive limit", responseMode: tunnel.TunnelCapacityModeAdaptive, responseLimit: "65536", wantErr: true},
		{name: "missing old server response header", adaptiveRequested: true, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			headers := make(http.Header)
			if tt.responseMode != "" {
				headers.Set(tunnel.TunnelCapacityModeHeader, tt.responseMode)
			}
			if tt.responseLimit != "" {
				headers.Set(tunnel.TunnelMaxConcurrentStreamsHeader, tt.responseLimit)
			}
			got, err := negotiatedAgentTunnelCapacity(headers, tunnel.MaxConcurrentAgentRequestsLimit, tt.adaptiveRequested)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("negotiated capacity = %d, want error", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("negotiated capacity = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestTunnelSessionRelaysTCPStream(t *testing.T) {
	resetAgentRequestCounters()

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

	startTestTunnelSession(t, agentSession, nil)

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
}

func TestTunnelSessionBoundsConcurrentRequests(t *testing.T) {
	resetAgentRequestCounters()

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

	startTestTunnelSessionWithLimit(t, agentSession, 1)

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

func TestTunnelCapacityLeaseWaitsForPeerFIN(t *testing.T) {
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	go func() {
		conn, acceptErr := upstream.Accept()
		if acceptErr == nil {
			_ = conn.Close()
		}
	}()

	clientConn, serverConn := net.Pipe()
	config := tunnel.DefaultYamuxConfig(nil)
	config.StreamCloseTimeout = 2 * time.Second
	agentSession, err := yamux.Client(clientConn, config)
	if err != nil {
		t.Fatal(err)
	}
	serverSession, err := yamux.Server(serverConn, config)
	if err != nil {
		t.Fatal(err)
	}
	defer agentSession.Close()
	defer serverSession.Close()
	capacity := newAgentTunnelCapacityRuntime(1, false, nil)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- serveTunnelSessionWithPolicyAndCapacity(ctx, agentSession, nil, capacity) }()

	first, err := serverSession.Open()
	if err != nil {
		t.Fatal(err)
	}
	if err := tunnel.WriteOpenRequest(first, tunnel.NewOpenRequest("fin-1", "tcp", upstream.Addr().String())); err != nil {
		t.Fatal(err)
	}
	if response, readErr := tunnel.ReadOpenResponse(first); readErr != nil || !response.OK {
		t.Fatalf("first response = %+v err=%v", response, readErr)
	}
	deadline := time.Now().Add(time.Second)
	for capacity.snapshot().InUse != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	// Wait until the agent has locally closed after the upstream EOF, while
	// intentionally withholding the server peer FIN.
	_ = first.SetReadDeadline(time.Now().Add(time.Second))
	var one [1]byte
	if _, readErr := first.Read(one[:]); readErr == nil {
		t.Fatal("first stream did not reach local FIN")
	}
	if got := capacity.snapshot().InUse; got != 1 {
		t.Fatalf("capacity in use before peer FIN = %d, want 1", got)
	}

	second, err := serverSession.Open()
	if err != nil {
		t.Fatal(err)
	}
	if err := tunnel.WriteOpenRequest(second, tunnel.NewOpenRequest("fin-2", "tcp", upstream.Addr().String())); err != nil {
		t.Fatal(err)
	}
	_ = second.SetReadDeadline(time.Now().Add(time.Second))
	response, err := tunnel.ReadOpenResponse(second)
	if err != nil {
		t.Fatal(err)
	}
	if response.OK || response.ErrorKind != "agent_capacity" {
		t.Fatalf("response before peer FIN = %+v, want agent_capacity", response)
	}
	_ = second.Close()

	_ = first.Close()
	deadline = time.Now().Add(time.Second)
	for capacity.snapshot().InUse != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := capacity.snapshot().InUse; got != 0 {
		t.Fatalf("capacity after peer FIN = %d, want 0", got)
	}
	cancel()
	_ = serverSession.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("agent session did not stop")
	}
}

func TestAggregateCapacityRejectionSlotsBoundUncooperativePeers(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	agentConfig, err := tunnel.NewAggregateYamuxConfig(nil, tunnel.DefaultMaxStreamWindowSizeBytes, 1024)
	if err != nil {
		t.Fatal(err)
	}
	serverConfig, err := tunnel.NewAggregateYamuxConfig(nil, tunnel.DefaultMaxStreamWindowSizeBytes, -1)
	if err != nil {
		t.Fatal(err)
	}
	agentSession, err := yamux.Client(clientConn, agentConfig)
	if err != nil {
		t.Fatal(err)
	}
	serverSession, err := yamux.Server(serverConn, serverConfig)
	if err != nil {
		t.Fatal(err)
	}
	capacity := newAgentTunnelCapacityRuntime(1, false, nil)
	releaseHeld, _, ok := capacity.tryAcquire()
	if !ok {
		t.Fatal("failed to occupy agent capacity")
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- serveTunnelSessionWithPolicyAndCapacity(ctx, agentSession, nil, capacity) }()
	t.Cleanup(func() {
		releaseHeld()
		cancel()
		serverSession.Close()
		agentSession.Close()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("aggregate rejection session did not stop")
		}
	})

	peers := make([]*yamux.Stream, 0, tunnel.AggregateRejectedStreamLimit)
	for index := range tunnel.AggregateRejectedStreamLimit + 1 {
		stream, openErr := serverSession.OpenStream()
		if openErr != nil {
			t.Fatalf("open rejected stream %d: %v", index, openErr)
		}
		_ = stream.SetDeadline(time.Now().Add(time.Second))
		writeErr := tunnel.WriteOpenRequest(stream, tunnel.NewOpenRequest(strconv.Itoa(index), "tcp", "127.0.0.1:1"))
		if writeErr != nil && index < tunnel.AggregateRejectedStreamLimit {
			t.Fatalf("write rejected stream %d: %v", index, writeErr)
		}
		var response tunnel.OpenResponse
		readErr := writeErr
		if writeErr == nil {
			response, readErr = tunnel.ReadOpenResponse(stream)
		}
		if index < tunnel.AggregateRejectedStreamLimit {
			if readErr != nil || response.OK || response.ErrorKind != "agent_capacity" {
				t.Fatalf("bounded rejection %d = %+v, %v", index, response, readErr)
			}
			// Deliberately withhold peer FIN so the local rejection slot remains
			// retained through the full closing lifetime.
			peers = append(peers, stream)
			continue
		}
		if !errors.Is(readErr, yamux.ErrConnectionReset) {
			t.Fatalf("excess rejection = %+v, %v; want immediate reset", response, readErr)
		}
		_ = stream.Close()
	}

	if _, err := serverSession.Ping(); err != nil {
		t.Fatalf("control traffic stalled behind rejected streams: %v", err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		stats := agentSession.AggregateReceiveWindowStats()
		if stats.BufferedUnits == 0 && stats.PendingRefund == 0 && stats.FreeUnits == stats.TotalUnits {
			break
		}
		time.Sleep(time.Millisecond)
	}
	stats := agentSession.AggregateReceiveWindowStats()
	if stats.BufferedUnits != 0 || stats.PendingRefund != 0 || stats.FreeUnits != stats.TotalUnits {
		t.Fatalf("rejection payload pool not conserved: %+v", stats)
	}
	if got := agentSession.NumStreams(); got > tunnel.AggregateRejectedStreamLimit {
		t.Fatalf("retained rejected streams = %d, limit %d", got, tunnel.AggregateRejectedStreamLimit)
	}
	for _, stream := range peers {
		_ = stream.Close()
	}
}

func TestLegacyCapacityRejectionSlotsRetainReceiveSupplements(t *testing.T) {
	resetAgentRequestCounters()
	t.Cleanup(resetAgentRequestCounters)
	clientConn, serverConn := net.Pipe()
	agentConfig, err := tunnel.NewAgentLegacyYamuxConfig(nil, tunnel.DefaultAdaptiveReceiveWindowBytes, 1)
	if err != nil {
		t.Fatal(err)
	}
	serverConfig, err := tunnel.NewYamuxConfig(nil, tunnel.DefaultAdaptiveReceiveWindowBytes)
	if err != nil {
		t.Fatal(err)
	}
	agentSession, err := yamux.Client(clientConn, agentConfig)
	if err != nil {
		t.Fatal(err)
	}
	serverSession, err := yamux.Server(serverConn, serverConfig)
	if err != nil {
		t.Fatal(err)
	}
	usage := sysmetrics.MemoryUsage{UsedBytes: 64 << 20, LimitBytes: 2 << 30, Source: "test"}
	capacity := newTestAgentAdaptiveCapacity(t, &usage)
	capacity.setMaximum(1)
	capacity.forceRefresh()
	envelopeBytes, err := tunnel.AgentLegacySessionResourceBytes(tunnel.DefaultAdaptiveReceiveWindowBytes)
	if err != nil {
		t.Fatal(err)
	}
	releaseEnvelope, ok := capacity.tryReserveLegacySessionEnvelope(envelopeBytes)
	if !ok {
		t.Fatal("failed to reserve legacy session envelope")
	}
	releaseHeld, _, ok := capacity.tryAcquire()
	if !ok {
		t.Fatal("failed to occupy agent capacity")
	}
	// Force all new per-stream supplement reservations to fail. The sixteen
	// session-prepaid rejection promises must still deliver structured errors;
	// the next stream is reset without exceeding the resident bound.
	usage.UsedBytes = 1900 << 20
	if pressured := capacity.forceRefresh(); pressured.Pressure != sysmetrics.MemoryPressureCritical {
		t.Fatalf("legacy rejection pressure = %+v", pressured)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		done <- serveTunnelSessionWithPolicyCapacityAndResources(ctx, agentSession, nil, capacity, nil, true, tunnel.DefaultAdaptiveReceiveWindowBytes)
	}()
	t.Cleanup(func() {
		releaseHeld()
		cancel()
		serverSession.Close()
		agentSession.Close()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("legacy rejection session did not stop")
		}
		releaseEnvelope()
	})

	peers := make([]*yamux.Stream, 0, tunnel.AgentLegacyRejectedStreamLimit)
	for index := range tunnel.AgentLegacyRejectedStreamLimit + 1 {
		stream, openErr := serverSession.OpenStream()
		if openErr != nil {
			t.Fatalf("open rejected stream %d: %v", index, openErr)
		}
		_ = stream.SetDeadline(time.Now().Add(time.Second))
		writeErr := tunnel.WriteOpenRequest(stream, tunnel.NewOpenRequest(strconv.Itoa(index), "tcp", "127.0.0.1:1"))
		var response tunnel.OpenResponse
		readErr := writeErr
		if writeErr == nil {
			response, readErr = tunnel.ReadOpenResponse(stream)
		}
		if index < tunnel.AgentLegacyRejectedStreamLimit {
			if readErr != nil || response.OK || (response.ErrorKind != "agent_capacity" && response.ErrorKind != "agent_resource_pressure") {
				t.Fatalf("bounded legacy rejection %d = %+v, %v", index, response, readErr)
			}
			peers = append(peers, stream)
			continue
		}
		if !errors.Is(readErr, yamux.ErrConnectionReset) {
			t.Fatalf("excess legacy rejection = %+v, %v; want immediate reset", response, readErr)
		}
		_ = stream.Close()
	}

	snapshot := capacity.snapshot()
	if snapshot.LegacySessionEnvelopeBytes != envelopeBytes || snapshot.LegacyStreamSupplementBytes != 0 {
		t.Fatalf("legacy rejection ownership = %+v, want envelope=%d and transferred supplements", snapshot, envelopeBytes)
	}
	wantRejected := uint64(tunnel.AgentLegacyRejectedStreamLimit + 1)
	if snapshot.RejectedPressure != wantRejected {
		t.Fatalf("legacy pressure rejection count = %d, want %d", snapshot.RejectedPressure, wantRejected)
	}
	if got := reqServerError.Load(); got != int32(wantRejected) {
		t.Fatalf("legacy server error count = %d, want %d", got, wantRejected)
	}
	if _, err := serverSession.Ping(); err != nil {
		t.Fatalf("legacy control traffic stalled behind rejected streams: %v", err)
	}
	for _, stream := range peers {
		_ = stream.Close()
	}
	deadline := time.Now().Add(2 * time.Second)
	for capacity.snapshot().LegacyStreamSupplementBytes != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := capacity.snapshot().LegacyStreamSupplementBytes; got != 0 {
		t.Fatalf("legacy rejection cleanup retained %d supplement bytes", got)
	}
}

func TestLegacyBacklogDrainsSimultaneousOldPeerBurst(t *testing.T) {
	const burst = tunnel.AgentLegacyAcceptBacklogStreams
	echo := startEchoListener(t)
	clientConn, serverConn := net.Pipe()
	agentConfig, err := tunnel.NewAgentLegacyYamuxConfig(nil, tunnel.DefaultAdaptiveReceiveWindowBytes, burst)
	if err != nil {
		t.Fatal(err)
	}
	serverConfig, err := tunnel.NewYamuxConfig(nil, tunnel.DefaultAdaptiveReceiveWindowBytes)
	if err != nil {
		t.Fatal(err)
	}
	agentSession, err := yamux.Client(clientConn, agentConfig)
	if err != nil {
		t.Fatal(err)
	}
	serverSession, err := yamux.Server(serverConn, serverConfig)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		done <- serveTunnelSessionWithPolicyCapacityAndResources(
			ctx,
			agentSession,
			nil,
			newAgentTunnelCapacityRuntime(burst, false, nil),
			nil,
			true,
			tunnel.DefaultAdaptiveReceiveWindowBytes,
		)
	}()
	t.Cleanup(func() {
		cancel()
		_ = serverSession.Close()
		_ = agentSession.Close()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("legacy burst session did not stop")
		}
	})

	start := make(chan struct{})
	errs := make(chan error, burst)
	var workers sync.WaitGroup
	for index := range burst {
		workers.Add(1)
		go func(index int) {
			defer workers.Done()
			<-start
			stream, openErr := serverSession.OpenStream()
			if openErr != nil {
				errs <- fmt.Errorf("open %d: %w", index, openErr)
				return
			}
			defer stream.Close()
			_ = stream.SetDeadline(time.Now().Add(3 * time.Second))
			if writeErr := tunnel.WriteOpenRequest(stream, tunnel.NewOpenRequest(strconv.Itoa(index), "tcp", echo.Addr().String())); writeErr != nil {
				errs <- fmt.Errorf("write request %d: %w", index, writeErr)
				return
			}
			response, readErr := tunnel.ReadOpenResponse(stream)
			if readErr != nil {
				errs <- fmt.Errorf("read response %d: %w", index, readErr)
				return
			}
			if !response.OK {
				errs <- fmt.Errorf("response %d: %+v", index, response)
				return
			}
			payload := []byte{byte(index)}
			if _, writeErr := stream.Write(payload); writeErr != nil {
				errs <- fmt.Errorf("write payload %d: %w", index, writeErr)
				return
			}
			var echoed [1]byte
			if _, readErr := io.ReadFull(stream, echoed[:]); readErr != nil {
				errs <- fmt.Errorf("read payload %d: %w", index, readErr)
				return
			}
			if echoed[0] != payload[0] {
				errs <- fmt.Errorf("echo %d = %d", index, echoed[0])
			}
		}(index)
	}
	close(start)
	workers.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if t.Failed() {
		return
	}
	if _, err := serverSession.Ping(); err != nil {
		t.Fatalf("legacy session did not recover after burst: %v", err)
	}
}

func TestTunnelSessionReturnsDialFailure(t *testing.T) {
	resetAgentRequestCounters()

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

	startTestTunnelSession(t, agentSession, nil)

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
	dialCalled := make(chan struct{}, 1)
	restoreDial := replaceAgentTunnelDialContext(func(ctx context.Context, network string, address string) (net.Conn, error) {
		dialCalled <- struct{}{}
		return nil, context.Canceled
	})
	t.Cleanup(restoreDial)
	startTestTunnelSession(t, agentSession, policy)

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

	startTestTunnelSession(t, agentSession, nil)

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
	var dialNetwork string
	var dialAddress string
	restoreDial := replaceAgentTunnelDialContext(func(ctx context.Context, network string, address string) (net.Conn, error) {
		dialNetwork = network
		dialAddress = address
		<-ctx.Done()
		return nil, ctx.Err()
	})
	t.Cleanup(restoreDial)
	startTestTunnelSession(t, agentSession, nil)

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

	startTestTunnelSession(t, agentSession, nil)

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

func startTestTunnelSessionWithLimit(t *testing.T, agentSession *yamux.Session, maxConcurrentRequests int64) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- serveTunnelSessionWithLimit(ctx, agentSession, maxConcurrentRequests)
	}()

	t.Cleanup(func() {
		defer resetAgentRequestCounters()
		cancel()
		_ = agentSession.Close()
		select {
		case <-serveDone:
		case <-time.After(2 * time.Second):
			t.Error("timed out waiting for limited tunnel session to stop")
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
