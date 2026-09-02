package server

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"p2pstream/internal/config"
	"p2pstream/internal/sysmetrics"
	"p2pstream/internal/tunnel"
)

func TestAgentProxySustains100RPSAcrossFourLegacyAgents(t *testing.T) {
	const (
		requestCount = 120
		requestRate  = 100
		upstreamTime = time.Second
	)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(upstreamTime)
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	origin, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("parse upstream: %v", err)
	}

	run := func(t *testing.T, totalStreams int64) (okResponses, unavailableResponses int64) {
		t.Helper()
		app := NewApp(&config.Config{ServerTunnelMaxConcurrentStreams: totalStreams}, nil)
		installDeterministicServerResourceController(t, app)
		t.Cleanup(app.CloseAgentTransports)
		target := publicRouteTargetConfig{
			ID:                            991,
			Name:                          "100-rps-capacity",
			Enabled:                       true,
			TargetType:                    publicRouteTargetTypeProxy,
			Transport:                     publicRouteTargetTransportAgent,
			ParsedURL:                     origin,
			UpstreamResponseHeaderTimeout: 3 * time.Second,
		}
		agents := make([]*AgentConn, 0, 4)
		for index := range 4 {
			agent, _ := newFakeYamuxAgent(t, int64(index+1), fmt.Sprintf("load-agent-%d", index+1))
			agents = append(agents, agent)
			sessionKey := agentStreamCapacitySessionKey(agent, agent.Session)
			app.agentStreamCapacity.registerSessionWithLimit(sessionKey, int(tunnel.DefaultMaxConcurrentAgentRequests))
			t.Cleanup(func() { app.agentStreamCapacity.unregisterSession(sessionKey) })
		}

		var successes atomic.Int64
		var unavailable atomic.Int64
		var other atomic.Int64
		var wg sync.WaitGroup
		interval := time.Second / time.Duration(requestRate)
		started := time.Now()
		for index := range requestCount {
			due := started.Add(time.Duration(index) * interval)
			if delay := time.Until(due); delay > 0 {
				time.Sleep(delay)
			}
			wg.Add(1)
			go func(index int) {
				defer wg.Done()
				recorder := httptest.NewRecorder()
				request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "http://public.test/load", nil)
				proxyAgentTargetForTest(app, recorder, request, target, agents[index%len(agents)])
				switch recorder.Code {
				case http.StatusOK:
					successes.Add(1)
				case http.StatusServiceUnavailable:
					unavailable.Add(1)
				default:
					other.Add(1)
				}
			}(index)
		}
		wg.Wait()
		if got := other.Load(); got != 0 {
			t.Fatalf("unexpected non-200/non-503 responses = %d", got)
		}
		if err := app.agentStreamCapacity.validateInvariants(); err != nil {
			t.Fatalf("capacity invariants after load: %v", err)
		}
		return successes.Load(), unavailable.Load()
	}

	t.Run("legacy global 64 reproduces capacity failures", func(t *testing.T) {
		okResponses, unavailableResponses := run(t, tunnel.DefaultMaxConcurrentAgentRequests)
		if unavailableResponses == 0 {
			t.Fatalf("legacy capacity unexpectedly served all requests: 200=%d 503=%d", okResponses, unavailableResponses)
		}
	})

	t.Run("dedicated 256 aggregates four legacy agents", func(t *testing.T) {
		okResponses, unavailableResponses := run(t, tunnel.DefaultServerMaxConcurrentStreams)
		if okResponses != requestCount || unavailableResponses != 0 {
			t.Fatalf("adaptive capacity results: 200=%d 503=%d, want %d/0", okResponses, unavailableResponses, requestCount)
		}
	})
}

func TestPublicHandlerSustains100RPSWithoutLegacyRouteTargetQuarterCap(t *testing.T) {
	previousLogLevel := zerolog.GlobalLevel()
	zerolog.SetGlobalLevel(zerolog.WarnLevel)
	t.Cleanup(func() { zerolog.SetGlobalLevel(previousLogLevel) })

	const (
		listenerID = int64(1)
		routeID    = int64(990)
		targetID   = int64(991)
	)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		delayMillis, err := strconv.Atoi(r.URL.Query().Get("delay_ms"))
		if err != nil || delayMillis < 0 {
			http.Error(w, "invalid test latency", http.StatusBadRequest)
			return
		}
		time.Sleep(time.Duration(delayMillis) * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstream.Close)
	origin, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("parse upstream: %v", err)
	}

	run := func(
		t *testing.T,
		perTargetLimit int64,
		serverStreamCapacity int64,
		sessionStreamCapacity int,
		agentCount int,
		requestCount int,
		requestRate int,
		latencyForRequest func(int) time.Duration,
	) (okResponses, routeTargetUnavailable, agentCapacityUnavailable, otherResponses int64) {
		t.Helper()
		adaptive := serverStreamCapacity == tunnel.MaxServerConcurrentStreamsLimit
		app := NewApp(&config.Config{
			PublicMaxConcurrentRequests:      2048,
			PublicMaxConcurrentPerTarget:     perTargetLimit,
			PublicMaxConcurrentPerClient:     512,
			ServerTunnelMaxConcurrentStreams: serverStreamCapacity,
			ServerTunnelCapacityAuto:         adaptive,
		}, nil)
		installDeterministicServerResourceController(t, app)
		t.Cleanup(app.CloseAgentTransports)

		target := publicRouteTargetConfig{
			ID:                            targetID,
			RouteID:                       routeID,
			Name:                          "full-pipeline-100-rps",
			Enabled:                       true,
			Position:                      0,
			Weight:                        100,
			TargetType:                    publicRouteTargetTypeProxy,
			Transport:                     publicRouteTargetTransportAgent,
			AgentLoadBalancing:            publicRouteTargetLoadBalancingRoundRobin,
			AgentSelector:                 publicAgentSelectorConfig{MatchLabels: map[string]string{"pool": "load"}},
			ParsedURL:                     origin,
			UpstreamResponseHeaderTimeout: 5 * time.Second,
		}
		snapshot := &publicProxySnapshot{
			Listeners: map[int64]publicListenerConfig{listenerID: {
				ID:       listenerID,
				Protocol: publicListenerProtocolHTTP,
				Enabled:  true,
			}},
			RoutesByListener: map[int64][]publicRouteConfig{listenerID: {{
				ID:               routeID,
				Enabled:          true,
				HostPattern:      "public.test",
				PathPrefix:       "/",
				Action:           publicRouteActionForward,
				PathSecurityMode: publicRoutePathSecurityModeStrict,
				Targets:          []publicRouteTargetConfig{target},
			}}},
			RouteTargets: map[int64]publicRouteTargetConfig{targetID: target},
			Agents:       make(map[int64]publicAgentConfig, agentCount),
		}
		for index := range agentCount {
			agentID := int64(index + 1)
			publicID := fmt.Sprintf("full-pipeline-agent-%d", index+1)
			agent, _ := newFakeYamuxAgent(t, agentID, publicID)
			if err := app.AgentHub.connect(agent); err != nil {
				t.Fatalf("connect agent %d: %v", agentID, err)
			}
			t.Cleanup(func() { app.AgentHub.disconnect(agent) })
			sessionKey := agentStreamCapacitySessionKey(agent, agent.Session)
			app.agentStreamCapacity.registerSessionWithLimit(sessionKey, sessionStreamCapacity)
			t.Cleanup(func() { app.agentStreamCapacity.unregisterSession(sessionKey) })
			snapshot.Agents[agentID] = publicAgentConfig{
				ID:       agentID,
				PublicID: publicID,
				Enabled:  true,
				Labels:   map[string]string{"pool": "load"},
			}
		}
		snapshot.RoutesByListener[listenerID][0].Targets[0] = target
		snapshot.RouteTargets[targetID] = target
		setPublicSnapshotForTest(t, app, snapshot)
		app.TargetHealth.reconcile(app, snapshot, false)
		handler := app.publicProxyHandler(listenerID)
		proxy := httptest.NewServer(handler)
		t.Cleanup(proxy.Close)
		clientTransport := &http.Transport{
			MaxIdleConns:        requestCount,
			MaxIdleConnsPerHost: requestCount,
		}
		t.Cleanup(clientTransport.CloseIdleConnections)
		client := &http.Client{Transport: clientTransport, Timeout: 10 * time.Second}

		var successes atomic.Int64
		var targetCapacityFailures atomic.Int64
		var agentCapacityFailures atomic.Int64
		var other atomic.Int64
		var wg sync.WaitGroup
		interval := time.Second / time.Duration(requestRate)
		started := time.Now()
		for index := range requestCount {
			due := started.Add(time.Duration(index) * interval)
			if delay := time.Until(due); delay > 0 {
				time.Sleep(delay)
			}
			wg.Add(1)
			go func(requestIndex int) {
				defer wg.Done()
				latency := latencyForRequest(requestIndex)
				requestURL := proxy.URL + "/load?delay_ms=" + strconv.FormatInt(latency.Milliseconds(), 10)
				request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, requestURL, nil)
				if err != nil {
					other.Add(1)
					return
				}
				request.Host = "public.test"
				response, err := client.Do(request)
				if err != nil {
					other.Add(1)
					return
				}
				body, readErr := io.ReadAll(response.Body)
				closeErr := response.Body.Close()
				if readErr != nil || closeErr != nil {
					other.Add(1)
					return
				}
				switch {
				case response.StatusCode == http.StatusOK:
					successes.Add(1)
				case response.StatusCode == http.StatusServiceUnavailable && strings.Contains(string(body), "Route target capacity reached"):
					targetCapacityFailures.Add(1)
				case response.StatusCode == http.StatusServiceUnavailable && strings.Contains(string(body), "Service Unavailable"):
					agentCapacityFailures.Add(1)
				default:
					other.Add(1)
				}
			}(index)
		}
		wg.Wait()
		if err := app.agentStreamCapacity.validateInvariants(); err != nil {
			t.Fatalf("capacity invariants after full-pipeline load: %v", err)
		}
		return successes.Load(), targetCapacityFailures.Load(), agentCapacityFailures.Load(), other.Load()
	}

	t.Run("legacy fixed 256 reproduces route target capacity failures", func(t *testing.T) {
		const requestCount = 270
		okResponses, targetCapacityFailures, agentCapacityFailures, otherResponses := run(t, 256, 2048, 2048, 4, requestCount, 100, func(int) time.Duration {
			return 3 * time.Second
		})
		if targetCapacityFailures == 0 {
			t.Fatalf("legacy route-target capacity unexpectedly served all requests: 200=%d route-target-503=%d agent-capacity-503=%d other=%d", okResponses, targetCapacityFailures, agentCapacityFailures, otherResponses)
		}
	})

	t.Run("automatic per-target capacity follows global ceiling", func(t *testing.T) {
		const requestCount = 270
		okResponses, targetCapacityFailures, agentCapacityFailures, otherResponses := run(t, 0, 2048, 2048, 4, requestCount, 100, func(int) time.Duration {
			return 3 * time.Second
		})
		if okResponses != requestCount || targetCapacityFailures != 0 || agentCapacityFailures != 0 || otherResponses != 0 {
			t.Fatalf("automatic route-target capacity results: 200=%d route-target-503=%d agent-capacity-503=%d other=%d, want %d/0/0/0", okResponses, targetCapacityFailures, agentCapacityFailures, otherResponses, requestCount)
		}
	})

	t.Run("automatic sustains 100 rps with 800ms to 2500ms agent latency", func(t *testing.T) {
		const (
			requestCount = 500
			requestRate  = 100
		)
		okResponses, targetCapacityFailures, agentCapacityFailures, otherResponses := run(t, 0, 2048, 2048, 4, requestCount, requestRate, func(index int) time.Duration {
			// Cycle through every 100 ms step, including both requested bounds.
			return time.Duration(800+(index%18)*100) * time.Millisecond
		})
		if okResponses != requestCount || targetCapacityFailures != 0 || agentCapacityFailures != 0 || otherResponses != 0 {
			t.Fatalf("geo-latency load results: 200=%d route-target-503=%d agent-capacity-503=%d other=%d, want %d/0/0/0", okResponses, targetCapacityFailures, agentCapacityFailures, otherResponses, requestCount)
		}
		t.Logf("served %d/%d requests at %d req/s with per-request latency cycling from 800ms to 2500ms", okResponses, requestCount, requestRate)
	})

	t.Run("adaptive one-agent mode sustains 100 rps with 800ms to 2500ms latency", func(t *testing.T) {
		const (
			requestCount = 500
			requestRate  = 100
		)
		okResponses, targetCapacityFailures, agentCapacityFailures, otherResponses := run(t, 0, tunnel.MaxServerConcurrentStreamsLimit, int(tunnel.MaxConcurrentAgentRequestsLimit), 1, requestCount, requestRate, func(index int) time.Duration {
			return time.Duration(800+(index%18)*100) * time.Millisecond
		})
		if okResponses != requestCount || targetCapacityFailures != 0 || agentCapacityFailures != 0 || otherResponses != 0 {
			t.Fatalf("adaptive one-agent results: 200=%d route-target-503=%d agent-capacity-503=%d other=%d, want %d/0/0/0", okResponses, targetCapacityFailures, agentCapacityFailures, otherResponses, requestCount)
		}
	})

	t.Run("v0.1.49 negotiated agents sustain the geo-latency profile at the 256 stream fallback", func(t *testing.T) {
		const (
			requestCount = 500
			requestRate  = 100
		)
		okResponses, targetCapacityFailures, agentCapacityFailures, otherResponses := run(t, 0, 256, 256, 4, requestCount, requestRate, func(index int) time.Duration {
			return time.Duration(800+(index%18)*100) * time.Millisecond
		})
		if okResponses != requestCount || targetCapacityFailures != 0 || agentCapacityFailures != 0 || otherResponses != 0 {
			t.Fatalf("v0.1.49-compatible capacity results: 200=%d route-target-503=%d agent-capacity-503=%d other=%d, want %d/0/0/0", okResponses, targetCapacityFailures, agentCapacityFailures, otherResponses, requestCount)
		}
	})

	t.Run("one negotiated agent needs enough streams for the geo-latency concurrency", func(t *testing.T) {
		const (
			requestCount = 300
			requestRate  = 100
		)
		okResponses, targetCapacityFailures, agentCapacityFailures, otherResponses := run(t, 0, 256, 256, 1, requestCount, requestRate, func(index int) time.Duration {
			return time.Duration(800+(index%18)*100) * time.Millisecond
		})
		if okResponses != requestCount || targetCapacityFailures != 0 || agentCapacityFailures != 0 || otherResponses != 0 {
			t.Fatalf("single-agent negotiated capacity results: 200=%d route-target-503=%d agent-capacity-503=%d other=%d, want %d/0/0/0", okResponses, targetCapacityFailures, agentCapacityFailures, otherResponses, requestCount)
		}
	})

	t.Run("one agent negotiated at legacy 64 reproduces 50-rps agent server capacity", func(t *testing.T) {
		const (
			requestCount = 300
			requestRate  = 50
		)
		okResponses, targetCapacityFailures, agentCapacityFailures, otherResponses := run(t, 0, 256, 64, 1, requestCount, requestRate, func(index int) time.Duration {
			return time.Duration(800+(index%18)*100) * time.Millisecond
		})
		if agentCapacityFailures == 0 || targetCapacityFailures != 0 || otherResponses != 0 {
			t.Fatalf("64-stream reproduction results: 200=%d route-target-503=%d agent-capacity-503=%d other=%d, want some agent-capacity failures only", okResponses, targetCapacityFailures, agentCapacityFailures, otherResponses)
		}
	})

	t.Run("single-agent 3000-request 50-rps geo-latency soak", func(t *testing.T) {
		if os.Getenv("P2PSTREAM_RUN_SOAK") != "1" {
			t.Skip("set P2PSTREAM_RUN_SOAK=1 to run the 60-second production-profile soak")
		}
		const (
			requestCount = 3000
			requestRate  = 50
		)
		okResponses, targetCapacityFailures, agentCapacityFailures, otherResponses := run(t, 0, 256, 256, 1, requestCount, requestRate, func(index int) time.Duration {
			return time.Duration(800+(index%18)*100) * time.Millisecond
		})
		if okResponses != requestCount || targetCapacityFailures != 0 || agentCapacityFailures != 0 || otherResponses != 0 {
			t.Fatalf("single-agent soak results: 200=%d route-target-503=%d agent-capacity-503=%d other=%d, want %d/0/0/0", okResponses, targetCapacityFailures, agentCapacityFailures, otherResponses, requestCount)
		}
		t.Logf("served %d/%d through one 256-stream agent at %d req/s with 800ms-2500ms latency", okResponses, requestCount, requestRate)
	})
}

func installDeterministicServerResourceController(t testing.TB, app *App) {
	t.Helper()
	controller, err := sysmetrics.NewAdaptiveMemoryController(
		sysmetrics.DefaultAdaptiveMemoryConfig(),
		sysmetrics.MemoryUsageSamplerFunc(func() (sysmetrics.MemoryUsage, error) {
			return sysmetrics.MemoryUsage{UsedBytes: 64 << 20, LimitBytes: 512 << 20, Source: "test"}, nil
		}),
	)
	if err != nil {
		t.Fatalf("deterministic server resource controller: %v", err)
	}
	app.agentStreamCapacity.enableAdaptiveMemory(controller)
}
