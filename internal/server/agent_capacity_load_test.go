package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"p2pstream/internal/config"
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
		interval := time.Second / requestRate
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
