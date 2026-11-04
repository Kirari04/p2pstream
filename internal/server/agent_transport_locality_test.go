package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type localityRoundTripper func(*http.Request) (*http.Response, error)

func (f localityRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func localityFixture(t *testing.T, aggregate bool) (*agentTransportPool, *AgentConn, *publicRouteAgentTransport) {
	t.Helper()
	pool := newAgentTransportPool()
	agent := &AgentConn{AgentID: 7, AggregateFlowControl: aggregate}
	first := 0
	if aggregate {
		first = 1
	}
	for lane := first; lane < first+4; lane++ {
		session, _ := newAgentTunnelTestSession(t)
		agent.installTunnel(lane, session)
	}
	key := agentTransportKey{Kind: agentTransportKindRouteTarget, AgentID: 7, TunnelLane: -1, RouteTargetID: 70, TargetOrigin: "http://origin.test", ResponseHeaderTimeoutMillis: 2000}
	rt := &publicRouteAgentTransport{pool: pool, agent: agent, key: key}
	t.Cleanup(pool.closeAll)
	return pool, agent, rt
}

func localityEntry(pool *agentTransportPool, agent *AgentConn, key agentTransportKey) *pooledAgentTransport {
	entry := &pooledAgentTransport{pool: pool, agent: agent, key: key, transport: &http.Transport{}, idleHint: true, lastUsed: time.Now()}
	pool.entries[key] = entry
	return entry
}

func TestAgentRouteLocalityConcurrentClaims(t *testing.T) {
	for _, aggregate := range []bool{false, true} {
		t.Run(fmt.Sprintf("aggregate=%t", aggregate), func(t *testing.T) {
			pool, agent, rt := localityFixture(t, aggregate)
			first := 0
			if aggregate {
				first = 1
			}
			for lane := first; lane < first+4; lane++ {
				key := rt.key
				key.TunnelLane = lane
				entry := localityEntry(pool, agent, key)
				entry.transport.RegisterProtocol("locality", localityRoundTripper(func(req *http.Request) (*http.Response, error) {

					return &http.Response{StatusCode: 200, Header: http.Header{"Lane": []string{strconv.Itoa(lane)}}, Body: io.NopCloser(strings.NewReader("body"))}, nil
				}))
			}
			start := make(chan struct{})
			type result struct {
				response *http.Response
				err      error
			}
			results := make(chan result, 4)
			var wg sync.WaitGroup
			for range 4 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					req, _ := http.NewRequestWithContext(t.Context(), "GET", "locality://origin.test", nil)
					resp, err := rt.RoundTrip(req)
					results <- result{resp, err}
				}()
			}
			close(start)
			seen := map[string]bool{}
			var responses []*http.Response
			for range 4 {
				select {
				case got := <-results:
					if got.err != nil {
						t.Fatal(got.err)
					}
					lane := got.response.Header.Get("Lane")
					if seen[lane] {
						t.Fatalf("concurrent bodies claimed lane %s twice", lane)
					}
					seen[lane] = true
					responses = append(responses, got.response)
				case <-time.After(2 * time.Second):
					t.Fatal("concurrent selection did not finish")
				}
			}
			wg.Wait()
			if rt.claimIdle() != nil {
				t.Fatal("all busy entries should fall back rather than wait")
			}
			for _, resp := range responses {
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
			}
			for _, entry := range pool.entries {
				if entry.inFlight != 0 {
					t.Fatal("body claim leaked")
				}
			}
		})
	}
}

func TestAgentRouteLocalityRejectsIneligibleEntry(t *testing.T) {
	cases := map[string]func(*pooledAgentTransport, *agentTransportKey, *AgentConn){
		"kind": func(_ *pooledAgentTransport, k *agentTransportKey, _ *AgentConn) {
			k.Kind = agentTransportKindEnvironment
		},
		"agent ID":    func(_ *pooledAgentTransport, k *agentTransportKey, _ *AgentConn) { k.AgentID++ },
		"target":      func(_ *pooledAgentTransport, k *agentTransportKey, _ *AgentConn) { k.RouteTargetID++ },
		"environment": func(_ *pooledAgentTransport, k *agentTransportKey, _ *AgentConn) { k.EnvironmentID++ },
		"origin":      func(_ *pooledAgentTransport, k *agentTransportKey, _ *AgentConn) { k.TargetOrigin += "/other" },
		"management":  func(_ *pooledAgentTransport, k *agentTransportKey, _ *AgentConn) { k.ManagementURL = "https://other" },
		"TLS":         func(_ *pooledAgentTransport, k *agentTransportKey, _ *AgentConn) { k.TLSSkipVerify = true },
		"certificate": func(_ *pooledAgentTransport, k *agentTransportKey, _ *AgentConn) {
			k.TrustedCertificateSHA256 = "other"
		},
		"timeout":           func(_ *pooledAgentTransport, k *agentTransportKey, _ *AgentConn) { k.ResponseHeaderTimeoutMillis++ },
		"generation":        func(e *pooledAgentTransport, _ *agentTransportKey, _ *AgentConn) { e.agent = &AgentConn{AgentID: 7} },
		"retired":           func(e *pooledAgentTransport, _ *agentTransportKey, _ *AgentConn) { e.retired = true },
		"missing transport": func(e *pooledAgentTransport, _ *agentTransportKey, _ *AgentConn) { e.transport = nil },
		"busy":              func(e *pooledAgentTransport, _ *agentTransportKey, _ *AgentConn) { e.inFlight = 1 },
		"no idle hint":      func(e *pooledAgentTransport, _ *agentTransportKey, _ *AgentConn) { e.idleHint = false },
		"removed lane":      func(_ *pooledAgentTransport, k *agentTransportKey, a *AgentConn) { a.tunnels[k.TunnelLane] = nil },
		"closed lane":       func(_ *pooledAgentTransport, k *agentTransportKey, a *AgentConn) { _ = a.tunnels[k.TunnelLane].Close() },
		"control lane": func(_ *pooledAgentTransport, k *agentTransportKey, a *AgentConn) {
			k.TunnelLane = 0
			a.tunnels[0] = a.tunnels[1]
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			pool, agent, rt := localityFixture(t, true)
			key := rt.key
			key.TunnelLane = 1
			entry := &pooledAgentTransport{pool: pool, agent: agent, transport: &http.Transport{}, idleHint: true}
			mutate(entry, &key, agent)
			entry.key = key
			pool.entries[key] = entry
			if rt.claimIdle() != nil {
				t.Fatal("selected ineligible or different security key")
			}
		})
	}
}

type localityErrorBody struct {
	readErr error
	writes  int
}

func (b *localityErrorBody) Read([]byte) (int, error)    { return 0, b.readErr }
func (b *localityErrorBody) Close() error                { return nil }
func (b *localityErrorBody) Write(p []byte) (int, error) { b.writes++; return len(p), nil }

func TestAgentRouteLocalityBodyOwnership(t *testing.T) {
	for _, outcome := range []string{"EOF", "read error", "Close", "upgrade", "transport error", "retired during request"} {
		t.Run(outcome, func(t *testing.T) {
			pool, agent, rt := localityFixture(t, false)
			key := rt.key
			key.TunnelLane = 0
			entry := localityEntry(pool, agent, key)
			body := &localityErrorBody{readErr: io.EOF}
			if outcome == "read error" {
				body.readErr = io.ErrUnexpectedEOF
			}
			entry.transport.RegisterProtocol("locality", localityRoundTripper(func(req *http.Request) (*http.Response, error) {
				if !pool.mu.TryLock() {
					t.Fatal("pool lock spans I/O")
				}
				pool.mu.Unlock()
				if !agent.tunnelMu.TryLock() {
					t.Fatal("tunnel lock spans I/O")
				}
				agent.tunnelMu.Unlock()
				if entry.inFlight != 1 {
					t.Errorf("missing invocation claim")
				}
				if outcome == "transport error" {
					return nil, errors.New("injected transport error")
				}
				if outcome == "retired during request" {
					pool.closeAll()
				}
				status := 200
				if outcome == "upgrade" {
					status = http.StatusSwitchingProtocols
				}
				return &http.Response{StatusCode: status, Header: make(http.Header), Body: body}, nil
			}))
			req, _ := http.NewRequestWithContext(t.Context(), "GET", "locality://origin.test", nil)
			resp, err := rt.RoundTrip(req)
			if outcome == "transport error" {
				if err == nil || entry.inFlight != 0 {
					t.Fatal("transport error did not release claim")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if entry.inFlight != 1 {
				t.Fatal("claim released before body completion")
			}
			if outcome == "upgrade" {
				if _, err := resp.Body.(io.Writer).Write([]byte("x")); err != nil {
					t.Fatal(err)
				}
			}
			if outcome != "Close" {
				_, _ = resp.Body.Read(make([]byte, 1))
			}
			_ = resp.Body.Close()
			_ = resp.Body.Close()
			if entry.inFlight != 0 || len(pool.draining) != 0 {
				t.Fatal("claim/draining entry leaked or double released")
			}
			if outcome == "upgrade" && body.writes != 1 {
				t.Fatal("upgrade writer not preserved")
			}
		})
	}
}

func TestAgentRouteLocalityFactoryDoesNotClaimOrAllocatePoolEntry(t *testing.T) {
	pool := newAgentTransportPool()
	agent := &AgentConn{AgentID: 7}
	for range 100 {
		_ = pool.publicRouteTargetTransport(nil, agent, publicRouteTargetConfig{ID: 70})
	}
	if len(pool.entries) != 0 || len(pool.draining) != 0 || agent.nextTunnel != 0 {
		t.Fatal("uninvoked factories retained entries or advanced lane ownership")
	}
}

func TestAgentRouteLocalityWarmupCloseAllPreservesRoundRobin(t *testing.T) {
	app, target, agent, fakes, upstream := localityLiveFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = w.Write([]byte("ok"))
	}))
	rt := app.agentTargetTransport(agent, target)
	for sequence := 0; sequence < 8; sequence++ {
		method := http.MethodGet
		if sequence >= 4 {
			method = http.MethodPost
		}
		req, _ := http.NewRequestWithContext(t.Context(), method, upstream.URL, strings.NewReader("warmup"))
		resp, err := rt.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		_, err = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		// This is the same retirement operation invoked by the qualification
		// admin endpoint after EACH of its four down and four up warmups.
		app.AgentTransports.closeAll()
		if app.AgentTransports.len() != 0 {
			t.Fatal("closeAll retained a preferred entry")
		}
		fakes[sequence%4].waitOpenRequestCount(t, sequence/4+1)
	}
	for lane, fake := range fakes {
		if got := fake.openRequestCount(); got != 2 {
			t.Fatalf("lane %d warmed %d times, want both directions", lane, got)
		}
	}
	// Without retirement, method/path changes retain the exact target key.
	for sequence := 0; sequence < 2; sequence++ {
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, upstream.URL+"/retained", nil)
		resp, err := rt.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
	if got := fakes[0].openRequestCount(); got != 3 {
		t.Fatalf("retained same-target connection opened %d times, want three total", got)
	}
	for lane := 1; lane < 4; lane++ {
		if got := fakes[lane].openRequestCount(); got != 2 {
			t.Fatalf("retained request rotated to lane %d", lane)
		}
	}
}

func TestAgentRouteLocalityBoundedWindowAndMaximumLane(t *testing.T) {
	for _, aggregate := range []bool{false, true} {
		t.Run(fmt.Sprintf("aggregate=%t", aggregate), func(t *testing.T) {
			pool, agent, rt := localityFixture(t, aggregate)
			first := 0
			if aggregate {
				first = 1
			}
			fifth := first + 4
			session, _ := newAgentTunnelTestSession(t)
			agent.installTunnel(fifth, session)
			key := rt.key
			key.TunnelLane = fifth
			entry := localityEntry(pool, agent, key)
			agent.nextTunnel = first
			if rt.claimIdle() != nil {
				t.Fatal("preference exceeded four live full-key lookups")
			}
			agent.nextTunnel = first + 1
			if rt.claimIdle() != entry {
				t.Fatal("cursor window did not include fifth live lane")
			}
			if agent.nextTunnel != fifth+1 {
				t.Fatal("successful preference did not advance RR cursor")
			}
			entry.roundTripDone(true)
			pool.closeAll()
			maximum := len(agent.tunnels) - 1
			session, _ = newAgentTunnelTestSession(t)
			agent.installTunnel(maximum, session)
			key.TunnelLane = maximum
			entry = localityEntry(pool, agent, key)
			agent.nextTunnel = maximum
			if rt.claimIdle() != entry || agent.nextTunnel != first {
				t.Fatal("maximum lane selection/cursor wrap differs from RR topology")
			}
			entry.roundTripDone(true)
		})
	}
}

func TestAgentRouteLocalityAllBusyUsesRoundRobinWithoutWaiting(t *testing.T) {
	pool, agent, rt := localityFixture(t, false)
	for lane := 0; lane < 4; lane++ {
		key := rt.key
		key.TunnelLane = lane
		entry := localityEntry(pool, agent, key)
		entry.inFlight = 1 // another response already owns this entry
		entry.transport.RegisterProtocol("locality", localityRoundTripper(func(req *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Header: http.Header{"Lane": []string{strconv.Itoa(lane)}}, Body: io.NopCloser(strings.NewReader("body"))}, nil
		}))
	}
	for lane := 0; lane < 4; lane++ {
		req, _ := http.NewRequestWithContext(t.Context(), "GET", "locality://origin.test", nil)
		resp, err := rt.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		if got := resp.Header.Get("Lane"); got != strconv.Itoa(lane) {
			t.Fatalf("busy fallback lane %s, want %d", got, lane)
		}
		_ = resp.Body.Close()
	}
	if len(pool.entries) != 4 {
		t.Fatal("fallback retained extra entries")
	}
	for _, entry := range pool.entries {
		if entry.inFlight != 1 {
			t.Fatal("fallback released another response's claim")
		}
		entry.roundTripDone(false)
	}
}

func localityLiveFixture(t *testing.T, handler http.Handler) (*App, publicRouteTargetConfig, *AgentConn, []*fakeYamuxAgent, *httptest.Server) {
	t.Helper()
	upstream := httptest.NewServer(handler)
	t.Cleanup(upstream.Close)
	app, target, agent, first := newAgentProxyTunnelTestApp(t, 7, upstream.URL, 2*time.Second)
	fakes := []*fakeYamuxAgent{first}
	agent.installTunnel(0, first.serverSession)
	for lane := 1; lane < 4; lane++ {
		_, fake := newFakeYamuxAgent(t, 7, fmt.Sprintf("lane-%d", lane))
		agent.installTunnel(lane, fake.serverSession)
		fakes = append(fakes, fake)
	}
	t.Cleanup(app.AgentTransports.closeAll)
	return app, target, agent, fakes, upstream
}

func TestAgentRouteLocalityConcurrentColdLiveLanes(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	app, target, agent, fakes, upstream := localityLiveFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("start"))
		w.(http.Flusher).Flush()
		<-release
		_, _ = w.Write([]byte("end"))
	}))
	defer unblock()
	rt := app.agentTargetTransport(agent, target)
	start := make(chan struct{})
	type result struct {
		resp *http.Response
		err  error
	}
	results := make(chan result, 4)
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, upstream.URL, nil)
			resp, err := rt.RoundTrip(req)
			results <- result{resp, err}
		}()
	}
	close(start)
	var responses []*http.Response
	for range 4 {
		select {
		case got := <-results:
			if got.err != nil {
				t.Fatal(got.err)
			}
			responses = append(responses, got.resp)
		case <-time.After(3 * time.Second):
			t.Fatal("cold concurrent requests failed to establish four live responses")
		}
	}
	wg.Wait()
	for _, fake := range fakes {
		fake.waitOpenRequestCount(t, 1)
	}
	app.AgentTransports.mu.Lock()
	count := len(app.AgentTransports.entries)
	for _, entry := range app.AgentTransports.entries {
		if entry.inFlight != 1 {
			t.Errorf("held response inFlight=%d, want1", entry.inFlight)
		}
	}
	app.AgentTransports.mu.Unlock()
	if count != 4 {
		t.Fatalf("cold concurrent pool entries=%d, want4", count)
	}
	unblock()
	for _, resp := range responses {
		data, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil || string(data) != "startend" {
			t.Fatalf("body=%q err=%v", data, err)
		}
	}
	for _, entry := range app.AgentTransports.entries {
		if entry.inFlight != 0 {
			t.Fatal("live body claim leaked")
		}
	}
}

func TestAgentRouteLocalityStaleIdleHintAndCancellation(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("start"))
		if r.URL.Path == "/hold" {
			w.(http.Flusher).Flush()
			<-release
		}
	}))
	defer func() { unblock(); upstream.Close() }()
	app, target, agent, fake := newAgentProxyTunnelTestApp(t, 7, upstream.URL, 2*time.Second)
	defer app.AgentTransports.closeAll()
	rt := app.agentTargetTransport(agent, target)
	consume := func() {
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, upstream.URL, nil)
		resp, err := rt.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		_, err = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	consume()
	var entry *pooledAgentTransport
	for _, e := range app.AgentTransports.entries {
		entry = e
	}
	if entry == nil || !entry.idleHint {
		t.Fatal("missing reusable entry")
	}
	entry.transport.CloseIdleConnections() // expire actual socket, retaining advisory hint
	consume()
	consume()
	fake.waitOpenRequestCount(t, 2) // stale hint redials once; reusable wrapper then reuses
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, upstream.URL+"/hold", nil)
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	if entry.inFlight != 1 {
		t.Fatal("streaming response released ownership early")
	}
	cancel()
	if _, err = io.ReadAll(resp.Body); err == nil {
		t.Fatal("cancellation did not terminate held body")
	}
	_ = resp.Body.Close()
	_ = resp.Body.Close()
	if entry.inFlight != 0 {
		t.Fatal("canceled body retained claim")
	}
	unblock()
	consume()
	fake.waitOpenRequestCount(t, 3)
	if app.AgentTransports.len() != 1 {
		t.Fatal("stale hint or canceled response expanded retained pool")
	}
}
