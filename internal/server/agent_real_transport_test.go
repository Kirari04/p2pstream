package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	agentruntime "p2pstream/internal/agent"
	"p2pstream/internal/config"
	"p2pstream/internal/db"
	"p2pstream/internal/tunnel"
)

type realAgentProxyFixture struct {
	app        *App
	agent      *AgentConn
	target     publicRouteTargetConfig
	upstream   *httptest.Server
	management *httptest.Server
}

func newRealAgentProxyFixture(tb testing.TB, lanes int, window int64, rtt time.Duration) *realAgentProxyFixture {
	tb.Helper()
	previousLevel := zerolog.GlobalLevel()
	zerolog.SetGlobalLevel(zerolog.WarnLevel)
	tb.Cleanup(func() { zerolog.SetGlobalLevel(previousLevel) })
	ctx, cancel := context.WithCancel(tb.Context())
	database, err := db.Open(filepath.Join(tb.TempDir(), "agent-performance.db"))
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { _ = database.Close() })
	app := NewApp(&config.Config{
		ManagementUIDisabled: true, ConfigDir: tb.TempDir(),
		ServerTunnelMaxConcurrentStreams: tunnel.MaxServerConcurrentStreamsLimit,
		ServerTunnelCapacityAuto:         true, TunnelMaxStreamWindowBytes: window,
	}, database)
	app.StartAdaptiveTunnelCapacity(ctx)
	row, err := database.CreateAgent(ctx, db.CreateAgentParams{PublicID: "real-performance-agent", Name: "Real performance agent", TokenHash: hashAgentToken("test-token"), Enabled: 1})
	if err != nil {
		tb.Fatal(err)
	}
	mux := http.NewServeMux()
	app.RegisterManagementRoutes(mux)
	management := httptest.NewUnstartedServer(mux)
	if rtt > 0 {
		management.Listener = newDelayedTestListener(management.Listener, rtt/2)
	}
	management.StartTLS()
	tb.Cleanup(management.Close)
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: management.Certificate().Raw})
	agentDone := make(chan error, 1)
	go func() {
		agentDone <- agentruntime.RunContext(ctx, agentruntime.Options{
			ManagementURL: management.URL, PublicID: row.PublicID, Token: "test-token", AllowAnyTarget: true,
			ManagementCAPEMBase64: base64.StdEncoding.EncodeToString(ca),
			TunnelConnections:     lanes, TunnelMaxStreamWindowBytes: window,
			TunnelMaxConcurrentRequests: tunnel.MaxConcurrentAgentRequestsLimit, TunnelCapacityAdaptive: true,
		})
	}()
	tb.Cleanup(func() {
		cancel()
		app.CloseAgentTransports()
		select {
		case <-agentDone:
		case <-time.After(5 * time.Second):
			tb.Error("real agent did not stop")
		}
		deadline := time.Now().Add(5 * time.Second)
		for app.AgentHub.connectedByID(row.ID) != nil && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		// Cleanup removes the hub entry before writing its disconnected row;
		// wait for its generation lock before closing the fixture database.
		unlock := app.lockAgentAuth(row.ID)
		unlock()
	})
	var connected *AgentConn
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		connected = app.AgentHub.connectedByID(row.ID)
		if connected != nil && realAgentLaneCount(connected) == lanes {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if connected == nil || realAgentLaneCount(connected) != lanes {
		tb.Fatal("real agent did not establish all lanes")
	}
	data := bytes.Repeat([]byte("download"), 512*1024)
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		if r.URL.Path == "/bulk" {
			w.Header().Set("Content-Length", fmt.Sprint(len(data)))
			_, _ = w.Write(data)
		} else {
			_, _ = w.Write([]byte("ok"))
		}
	}))
	upstream.EnableHTTP2 = true
	upstream.StartTLS()
	tb.Cleanup(upstream.Close)
	parsed, _ := url.Parse(upstream.URL)
	return &realAgentProxyFixture{app: app, agent: connected, upstream: upstream, management: management,
		target: publicRouteTargetConfig{ID: 70, Name: "real-agent", Enabled: true, TargetType: publicRouteTargetTypeProxy,
			Transport: publicRouteTargetTransportAgent, URL: upstream.URL, ParsedURL: parsed, TLSSkipVerify: true,
			UpstreamResponseHeaderTimeout: 10 * time.Second}}
}

func realAgentLaneCount(agent *AgentConn) int {
	agent.tunnelMu.RLock()
	defer agent.tunnelMu.RUnlock()
	count := 0
	for _, session := range agent.tunnels {
		if session != nil && !session.IsClosed() {
			count++
		}
	}
	return count
}

func (f *realAgentProxyFixture) roundTrip(ctx context.Context, path string, trace *trafficRequestTrace) (int64, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, f.upstream.URL+path, nil)
	rt := &publicAgentAttemptRoundTripper{app: f.app, initial: f.agent, resolution: publicRouteResolution{Target: f.target}, trace: trace, requestID: "real-agent-test"}
	resp, err := rt.RoundTrip(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("upstream status %d", resp.StatusCode)
	}
	return io.Copy(io.Discard, resp.Body)
}

func TestRealAgentParallelTunnelsSurvivePrimaryLoss(t *testing.T) {
	f := newRealAgentProxyFixture(t, 4, 2<<20, 0)
	for range 8 {
		if n, err := f.roundTrip(t.Context(), "/api", nil); err != nil || n != 2 {
			t.Fatalf("proxy: %d, %v", n, err)
		}
	}
	if got := f.app.AgentTransports.len(); got != 4 {
		t.Fatalf("pooled lanes=%d, want 4", got)
	}
	initial := f.agent
	_ = initial.Session.Close()
	for range 12 {
		if n, err := f.roundTrip(t.Context(), "/api", nil); err != nil || n != 2 {
			t.Fatalf("proxy after primary loss: %d, %v", n, err)
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for realAgentLaneCount(initial) != 4 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := f.app.AgentHub.connectedByID(initial.AgentID); got != initial || realAgentLaneCount(initial) != 4 {
		t.Fatal("primary reconnect replaced the agent generation or failed to restore its lane")
	}
	if got := f.app.agentStreamCapacity.snapshot().RegisteredSessions; got != 1 {
		t.Fatalf("lanes multiplied admission budgets: %d", got)
	}
	if _, err := f.app.DB.ExecContext(t.Context(), "UPDATE agents SET token_hash=? WHERE id=?", hashAgentToken("rotated-token"), initial.AgentID); err != nil {
		t.Fatal(err)
	}
	f.app.revokeAgentConnection(initial.AgentID)
	deadline = time.Now().Add(time.Second)
	for realAgentLaneCount(initial) > 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if realAgentLaneCount(initial) != 0 {
		t.Fatal("revocation left an authenticated lane active")
	}
}

func BenchmarkRealAgentWANDownload(b *testing.B) {
	for _, lanes := range []int{1, 4} {
		for _, window := range []int64{512 << 10, 2 << 20} {
			b.Run(fmt.Sprintf("lanes%d/window%dKiB/rtt40ms", lanes, window>>10), func(b *testing.B) {
				b.StopTimer()
				f := newRealAgentProxyFixture(b, lanes, window, 40*time.Millisecond)
				for range lanes {
					if _, err := f.roundTrip(b.Context(), "/api", nil); err != nil {
						b.Fatal(err)
					}
				}
				b.SetBytes(4 << 20)
				b.ReportAllocs()
				b.ResetTimer()
				b.StartTimer()
				for range b.N {
					if _, err := f.roundTrip(b.Context(), "/bulk", nil); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

func TestRealAgentProxyPhaseTimingAndReuse(t *testing.T) {
	f := newRealAgentProxyFixture(t, 1, 2<<20, 0)
	cold := &trafficRequestTrace{level: p2pstreamv1.TrafficTraceLevel_TRAFFIC_TRACE_LEVEL_DEBUG}
	if _, err := f.roundTrip(t.Context(), "/api", cold); err != nil {
		t.Fatal(err)
	}
	p := cold.upstreamTiming.Load()
	if p == nil || p.reused.Load() || p.open.Load() <= 0 || p.handshake.Load() <= 0 || p.agentDial.Load() <= 0 || p.tls.Load() <= 0 || p.firstByte.Load() <= 0 {
		t.Fatal("cold request omitted dial, tunnel, TLS or first-byte timings")
	}
	warm := &trafficRequestTrace{level: p2pstreamv1.TrafficTraceLevel_TRAFFIC_TRACE_LEVEL_DEBUG}
	if _, err := f.roundTrip(t.Context(), "/api", warm); err != nil {
		t.Fatal(err)
	}
	p = warm.upstreamTiming.Load()
	if p == nil || !p.reused.Load() || p.open.Load() != 0 || p.firstByte.Load() <= 0 {
		t.Fatal("warm request did not report connection reuse")
	}
}

// Measure interactive latency while sustained bulk requests occupy the same
// origin. RTT is simulated; this is not a packet-loss/congestion benchmark.
func BenchmarkRealAgentWANMixed(b *testing.B) {
	for _, lanes := range []int{1, 4} {
		b.Run(fmt.Sprintf("lanes%d/rtt40ms", lanes), func(b *testing.B) {
			b.StopTimer()
			f := newRealAgentProxyFixture(b, lanes, 2<<20, 40*time.Millisecond)
			for range lanes {
				if _, err := f.roundTrip(b.Context(), "/api", nil); err != nil {
					b.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(b.Context())
			var wg sync.WaitGroup
			for range 2 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for ctx.Err() == nil {
						if _, err := f.roundTrip(ctx, "/bulk", nil); err != nil && ctx.Err() == nil {
							b.Error(err)
							return
						}
					}
				}()
			}
			samples := make([]time.Duration, 0, b.N)
			b.ResetTimer()
			b.StartTimer()
			for range b.N {
				started := time.Now()
				if _, err := f.roundTrip(b.Context(), "/api", nil); err != nil {
					b.Error(err)
					break
				}
				samples = append(samples, time.Since(started))
			}
			b.StopTimer()
			cancel()
			wg.Wait()
			sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
			if len(samples) > 0 {
				b.ReportMetric(float64(samples[(len(samples)-1)*50/100])/float64(time.Millisecond), "api_p50_ms")
				b.ReportMetric(float64(samples[(len(samples)-1)*95/100])/float64(time.Millisecond), "api_p95_ms")
			}
		})
	}
}
