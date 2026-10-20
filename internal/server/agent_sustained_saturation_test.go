package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

const (
	sustainedBulkBytes = 2 << 20
	sustainedSlowBytes = 8 << 20
)

type sustainedExpectedEvent struct {
	route  int
	path   string
	bytes  int64
	slow   bool
	reject bool
}

type sustainedBody struct {
	response *http.Response
	cancel   context.CancelFunc
	close    func()
	hash     hash.Hash
	read     int64
}

type sustainedCycleGate struct {
	bulk    [2]*mixedSaturationBulk
	stopped [2]chan struct{}
}

// This saturates configured global public admission. It does not measure
// hardware saturation or tenant fairness. Timings are diagnostic; five-second
// guards detect lack of progress. Both real runtimes share this test process.
func TestRealAgentSustainedGlobalSaturationRecovery(t *testing.T) {
	soak := time.Duration(0)
	if raw := os.Getenv("P2PSTREAM_SUSTAINED_SOAK_SECONDS"); raw != "" {
		seconds, err := strconv.Atoi(raw)
		if err != nil || (seconds != 60 && seconds != 120) {
			t.Fatal("P2PSTREAM_SUSTAINED_SOAK_SECONDS must be 60 or 120")
		}
		soak = time.Duration(seconds) * time.Second
	}
	var app *App
	var agents [2]*AgentConn
	// Existing fixture cleanups cancel AND join both runtimes. Only after the
	// subtest returns may live-session receive-pool promises be required to be
	// zero. Closing idle HTTP transports alone does not retire those promises.
	ok := t.Run("cycles", func(t *testing.T) {
		f := newRealAgentProxyFixture(t, 1, 2<<20, 0)
		app = f.app
		agents = [2]*AgentConn{f.agent, mixedSaturationAddAgent(t, f)}
		installDeterministicServerResourceController(t, app)
		app.publicProxyRequests = newRequestCapacityLimiter(4, 4)
		app.publicTargetRequests = newKeyedRequestCapacityLimiter(4, 4)
		app.publicClientRequests = newKeyedStringRequestCapacityLimiter(8)
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), mixedSaturationWait)
			defer cancel()
			if err := app.CloseObservabilityRecorder(ctx); err != nil {
				t.Error(err)
			}
		})
		started := time.Now()
		lifetime := 45 * time.Second
		if soak > 0 {
			lifetime += soak
		}
		workCtx, stopWork := context.WithTimeout(t.Context(), lifetime)
		t.Cleanup(stopWork)
		var gate atomic.Pointer[sustainedCycleGate]
		var publicOriginCalls, healthCalls atomic.Int64
		var nonH2 atomic.Bool
		block := bytes.Repeat([]byte{0x6d}, mixedSaturationStep)
		origin := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.ProtoMajor != 2 {
				nonH2.Store(true)
			}
			if r.URL.Path == "/health" {
				healthCalls.Add(1)
				_, _ = io.WriteString(w, "healthy")
				return
			}
			publicOriginCalls.Add(1)
			w.Header().Set("Content-Type", "application/octet-stream")
			if r.URL.Path != "/bulk/0" && r.URL.Path != "/bulk/1" && r.URL.Path != "/slow/0" && r.URL.Path != "/slow/1" {
				_, _ = io.WriteString(w, "small:"+r.URL.Path)
				return
			}
			current := gate.Load()
			if current == nil {
				http.Error(w, "missing cycle", 500)
				return
			}
			index := int(r.URL.Path[len(r.URL.Path)-1] - '0')
			if r.URL.Path[:6] == "/slow/" {
				// A withheld final byte keeps cancellation observable even if
				// every byte of this finite prefix fits in transport buffers.
				w.Header().Set("Content-Length", fmt.Sprint(sustainedSlowBytes+1))
				for written := 0; written < sustainedSlowBytes; {
					n, err := w.Write(block[:min(len(block), sustainedSlowBytes-written)])
					written += n
					w.(http.Flusher).Flush()
					if err != nil {
						break
					}
				}
				<-r.Context().Done()
				close(current.stopped[index])
				return
			}
			w.Header().Set("Content-Length", fmt.Sprint(sustainedBulkBytes))
			written, err := w.Write(block[:mixedSaturationPrefix])
			w.(http.Flusher).Flush()
			for err == nil && written < sustainedBulkBytes {
				select {
				case <-r.Context().Done():
					return
				case <-current.bulk[index].release:
				case <-current.bulk[index].advance:
				}
				n, writeErr := w.Write(block[:min(len(block), sustainedBulkBytes-written)])
				written, err = written+n, writeErr
				w.(http.Flusher).Flush()
			}
		}))
		origin.EnableHTTP2 = true
		origin.StartTLS()
		t.Cleanup(origin.Close)
		const listenerID = int64(1)
		targets := [2]publicRouteTargetConfig{}
		snapshot := &publicProxySnapshot{
			Listeners:        map[int64]publicListenerConfig{listenerID: {ID: listenerID, Protocol: publicListenerProtocolHTTP, Enabled: true}},
			RoutesByListener: make(map[int64][]publicRouteConfig), RouteTargets: make(map[int64]publicRouteTargetConfig), Agents: make(map[int64]publicAgentConfig),
		}
		for i, agent := range agents {
			target := f.target
			target.ID, target.RouteID = int64(270+i), int64(20+i)
			target.URL, target.ParsedURL = origin.URL, mustParseAggregateTestURL(t, origin.URL)
			target.AgentSelector = publicAgentSelectorConfig{MatchLabels: map[string]string{"sustained-route": fmt.Sprint(i)}}
			target.AgentLoadBalancing, target.Weight = publicRouteTargetLoadBalancingRoundRobin, 100
			targets[i] = target
			snapshot.RouteTargets[target.ID] = target
			snapshot.Agents[agent.AgentID] = publicAgentConfig{ID: agent.AgentID, PublicID: agent.PublicID, Enabled: true, Labels: map[string]string{"sustained-route": fmt.Sprint(i)}}
			snapshot.RoutesByListener[listenerID] = append(snapshot.RoutesByListener[listenerID], publicRouteConfig{
				ID: target.RouteID, ListenerID: listenerID, HostPattern: fmt.Sprintf("*.%c.sustained.test", 'a'+i), PathPrefix: "/", Action: publicRouteActionForward,
				PathSecurityMode: publicRoutePathSecurityModeStrict, TargetLoadBalancing: publicRouteTargetLoadBalancingRoundRobin, Enabled: true, Targets: []publicRouteTargetConfig{target},
			})
		}
		setPublicSnapshotForTest(t, app, snapshot)
		app.TargetHealth.reconcile(app, snapshot, false)
		public := httptest.NewUnstartedServer(app.publicProxyHandler(listenerID))
		public.EnableHTTP2 = true
		public.StartTLS()
		t.Cleanup(public.Close)
		transport := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, ForceAttemptHTTP2: true} // fixture certificate
		client := &http.Client{Transport: transport}
		t.Cleanup(transport.CloseIdleConnections)
		requestCount := 0
		expected := make(map[string]sustainedExpectedEvent)
		request := func(route int, path string) (*http.Response, context.CancelFunc, func()) {
			t.Helper()
			ctx, cancel := context.WithTimeout(workCtx, 35*time.Second)
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, public.URL+path, nil)
			if err != nil {
				cancel()
				t.Fatal(err)
			}
			requestCount++
			req.Host = fmt.Sprintf("request-%d.%c.sustained.test", requestCount, 'a'+route)
			item := sustainedExpectedEvent{route: route, path: path, bytes: int64(len("small:" + path))}
			switch path {
			case "/overflow":
				item.reject, item.bytes = true, int64(len("Public proxy capacity reached\n"))
			case "/bulk/0", "/bulk/1":
				item.bytes = sustainedBulkBytes
			case "/slow/0", "/slow/1":
				item.slow = true
			}
			expected[req.Host] = item
			timer := time.AfterFunc(mixedSaturationWait, cancel)
			resp, err := client.Do(req)
			timer.Stop()
			if err != nil {
				cancel()
				t.Fatalf("route %d %s: %v", route, path, err)
			}
			closeBody := ownedSaturationBodyClose(resp.Body)
			if resp.ProtoMajor != 2 {
				closeBody()
				cancel()
				t.Fatalf("public protocol=%s", resp.Proto)
			}
			return resp, cancel, closeBody
		}
		small := func(route int, path string) time.Duration {
			t.Helper()
			start := time.Now()
			resp, cancel, closeBody := request(route, path)
			defer cancel()
			defer closeBody()
			timer := time.AfterFunc(mixedSaturationWait, cancel)
			defer timer.Stop()
			payload, err := io.ReadAll(io.LimitReader(resp.Body, 1024))
			if err != nil || resp.StatusCode != 200 || string(payload) != "small:"+path {
				t.Fatalf("small route=%d path=%s status=%d payload=%q err=%v", route, path, resp.StatusCode, payload, err)
			}
			return time.Since(start)
		}
		for route := range agents {
			small(route, "/warm")
		}
		mixedSaturationAwait(t, "warm ownership drain", func() bool { return sustainedIdle(app, agents, targets) })
		control := [2]any{agents[0].tunnelSession(0), agents[1].tunnelSession(0)}
		bulkSessions := [2]any{agents[0].tunnelSession(1), agents[1].tunnelSession(1)}
		baselineLeases, baselineEntries := app.agentStreamCapacity.snapshot().Total.InUse, app.AgentTransports.len()
		baselineStreams := [2]int{agents[0].tunnelSession(1).NumStreams(), agents[1].tunnelSession(1).NumStreams()}
		if baselineLeases != 2 || baselineEntries != 2 || baselineStreams != [2]int{1, 1} {
			t.Fatalf("warm leases/entries/streams=%d/%d/%v, want 2/2/[1 1]", baselineLeases, baselineEntries, baselineStreams)
		}
		cycles, persisted, rejects, partials := 0, 0, 0, 0
		maxCycles := 3
		if soak > 0 {
			maxCycles = int(soak / (500 * time.Millisecond))
		}
		for cycle := 0; cycle < maxCycles; cycle++ {
			if soak > 0 {
				if elapsed := time.Since(started); elapsed >= soak {
					break
				}
			}
			cycleStarted := time.Now()
			func() {
				current := &sustainedCycleGate{stopped: [2]chan struct{}{make(chan struct{}), make(chan struct{})}}
				for i := range current.bulk {
					current.bulk[i] = &mixedSaturationBulk{advance: make(chan struct{}), release: make(chan struct{})}
					defer current.bulk[i].finish()
				}
				gate.Store(current)
				bulks, slows := [2]sustainedBody{}, [2]sustainedBody{}
				consume := func(i, size int) {
					t.Helper()
					body := &bulks[i]
					payload := make([]byte, size)
					timer := time.AfterFunc(mixedSaturationWait, body.cancel)
					n, err := io.ReadFull(body.response.Body, payload)
					timer.Stop()
					if err != nil || !bytes.Equal(payload, bytes.Repeat([]byte{0x6d}, size)) {
						t.Fatalf("bulk route=%d bytes=%d err=%v", i, n, err)
					}
					_, _ = body.hash.Write(payload)
					body.read += int64(n)
				}
				for route := range agents {
					resp, cancel, closeBody := request(route, fmt.Sprintf("/bulk/%d", route))
					defer cancel()
					defer closeBody()
					if resp.StatusCode != 200 || resp.ContentLength != sustainedBulkBytes {
						t.Fatalf("bulk status/length=%d/%d", resp.StatusCode, resp.ContentLength)
					}
					bulks[route] = sustainedBody{response: resp, cancel: cancel, close: closeBody, hash: sha256.New()}
					consume(route, mixedSaturationPrefix)
					resp, cancel, closeBody = request(route, fmt.Sprintf("/slow/%d", route))
					defer cancel()
					defer closeBody()
					if resp.StatusCode != 200 || resp.ContentLength != sustainedSlowBytes+1 {
						t.Fatalf("slow status/length=%d/%d", resp.StatusCode, resp.ContentLength)
					}
					slows[route] = sustainedBody{response: resp, cancel: cancel, close: closeBody}
					prefix := make([]byte, mixedSaturationPrefix)
					timer := time.AfterFunc(mixedSaturationWait, cancel)
					_, err := io.ReadFull(resp.Body, prefix)
					timer.Stop()
					if err != nil || !bytes.Equal(prefix, block[:mixedSaturationPrefix]) {
						t.Fatalf("slow prefix route=%d: %v", route, err)
					}
				}
				mixedSaturationAwait(t, "global four held owners", func() bool {
					if len(app.publicProxyRequests.slots) != 4 {
						return false
					}
					for i, agent := range agents {
						if mixedSaturationTargetUsage(app, targets[i].ID) != 2 || agent.ActiveRequests.Load() != 2 || app.AgentTransports.inFlightForAgent(agent.AgentID) != 2 {
							return false
						}
					}
					return true
				})
				before := publicOriginCalls.Load()
				for route := range agents {
					resp, cancel, closeBody := request(route, "/overflow")
					timer := time.AfterFunc(mixedSaturationWait, cancel)
					payload, err := io.ReadAll(io.LimitReader(resp.Body, 1024))
					timer.Stop()
					closeBody()
					cancel()
					if err != nil || resp.StatusCode != 503 || string(payload) != "Public proxy capacity reached\n" || resp.Header.Get("Retry-After") != "1" {
						t.Fatalf("global overflow route=%d status=%d payload=%q err=%v", route, resp.StatusCode, payload, err)
					}
					health := testHealthTarget(t, targets[route].ID, publicRouteTargetTransportAgent, origin.URL)
					health.TLSSkipVerify = true
					health.HealthCheck.Timeout = mixedSaturationWait
					if err := app.checkPublicTargetHealthViaAgent(workCtx, health, agents[route]); err != nil {
						t.Fatalf("reserved control progress route=%d: %v", route, err)
					}
					if len(app.publicProxyRequests.slots) != 4 {
						t.Fatal("global ownership changed during rejection/control progress")
					}
				}
				if publicOriginCalls.Load() != before {
					t.Fatal("global rejection reached origin")
				}
				if soak > 0 {
					// Keep all four owners held until this cycle has lasted at
					// least 500ms. This bounds launches without catch-up bursts.
					if wait := time.Until(cycleStarted.Add(500 * time.Millisecond)); wait > 0 {
						select {
						case <-time.After(wait):
						case <-workCtx.Done():
							t.Fatal(workCtx.Err())
						}
					}
				}
				// Alternate the first canceled route each cycle. The returned
				// global slot must serve both routes despite the other held bodies.
				first := cycle % 2
				slows[first].cancel()
				slows[first].close()
				mixedSaturationAwaitChannel(t, "first slow cancellation", current.stopped[first])
				heldCorrect := func() bool {
					if len(app.publicProxyRequests.slots) != 3 {
						return false
					}
					for route, agent := range agents {
						want := 2
						if route == first {
							want = 1
						}
						if mixedSaturationTargetUsage(app, targets[route].ID) != want || agent.ActiveRequests.Load() != int64(want) || app.AgentTransports.inFlightForAgent(agent.AgentID) != want {
							return false
						}
					}
					return true
				}
				mixedSaturationAwait(t, "global cancellation returns one slot", heldCorrect)
				samples := [2][]time.Duration{}
				for i := range 8 {
					if !heldCorrect() {
						t.Fatalf("cycle=%d lost expected held request/body ownership", cycle)
					}
					route := i % 2
					samples[route] = append(samples[route], small(route, fmt.Sprintf("/probe/%d/%d", cycle, i)))
					select {
					case current.bulk[route].advance <- struct{}{}:
					case <-time.After(mixedSaturationWait):
						t.Fatal("bulk progress blocked")
					}
					consume(route, mixedSaturationStep)
					// Handler cleanup can follow receipt of the last response byte.
					mixedSaturationAwait(t, "probe releases global slot and body", heldCorrect)
				}
				second := 1 - first
				slows[second].cancel()
				slows[second].close()
				mixedSaturationAwaitChannel(t, "second slow cancellation", current.stopped[second])
				for route, item := range bulks {
					current.bulk[route].finish()
					timer := time.AfterFunc(mixedSaturationWait, item.cancel)
					n, err := io.Copy(item.hash, item.response.Body)
					timer.Stop()
					item.close()
					item.cancel()
					want := sha256.Sum256(bytes.Repeat([]byte{0x6d}, sustainedBulkBytes))
					if err != nil || item.read+n != sustainedBulkBytes || !bytes.Equal(item.hash.Sum(nil), want[:]) {
						t.Fatalf("bulk final route=%d bytes=%d err=%v", route, item.read+n, err)
					}
				}
				mixedSaturationAwait(t, "cycle body ownership drain", func() bool { return sustainedIdle(app, agents, targets) })
				for route := range agents {
					for i := range 2 {
						samples[route] = append(samples[route], small(route, fmt.Sprintf("/recovery/%d/%d", cycle, i)))
					}
				}
				mixedSaturationAwait(t, "recovery ownership drain", func() bool { return sustainedIdle(app, agents, targets) })
				mixedSaturationAwait(t, "cycle returns physical/pool ownership to warm baseline", func() bool {
					if app.AgentTransports.len() != baselineEntries || app.agentStreamCapacity.snapshot().Total.InUse != baselineLeases {
						return false
					}
					for i, agent := range agents {
						if agent.tunnelSession(1).NumStreams() != baselineStreams[i] || agent.tunnelSession(0).NumStreams() != 0 {
							return false
						}
					}
					return true
				})
				for i, agent := range agents {
					if app.AgentHub.connectedByID(agent.AgentID) != agent || !realAgentAggregateLaneShape(agent, 1) || agent.tunnelSession(0) != control[i] || agent.tunnelSession(1) != bulkSessions[i] {
						t.Fatal("cycle replaced original aggregate generation/session")
					}
				}
				if err := app.agentStreamCapacity.validateInvariants(); err != nil {
					t.Fatal(err)
				}
				sustainedCheckEvents(t, app, targets, agents, expected, &persisted, &rejects, &partials)
				for route := range agents {
					mixedSaturationLogLatency(t, fmt.Sprintf("sustained_cycle_%d_route_%d", cycle, route), append([]time.Duration(nil), samples[route]...))
				}
				sustainedResourceSnapshot(t, app, agents, cycle, time.Since(cycleStarted), samples)
			}()
			cycles++
		}
		if cycles < 3 || requestCount != 2+18*cycles || persisted != requestCount || rejects != 2*cycles || partials != 2*cycles || publicOriginCalls.Load() != int64(requestCount-rejects) || healthCalls.Load() != int64(2*cycles) || nonH2.Load() {
			t.Fatalf("conservation cycles=%d requests=%d events=%d rejects=%d partials=%d public_origin=%d health=%d non_h2=%v", cycles, requestCount, persisted, rejects, partials, publicOriginCalls.Load(), healthCalls.Load(), nonH2.Load())
		}
		app.CloseAgentTransports()
		mixedSaturationAwait(t, "terminal physical/pool/request drain", func() bool {
			if !sustainedIdle(app, agents, targets) || app.AgentTransports.len() != 0 || app.agentStreamCapacity.snapshot().Total.InUse != 0 {
				return false
			}
			for _, agent := range agents {
				if agent.tunnelSession(1).NumStreams() != 0 {
					return false
				}
			}
			return true
		})
		sustainedResourceSnapshot(t, app, agents, cycles, 0, [2][]time.Duration{})
		t.Logf("sustained_global requests=%d events=%d origin=%d health=%d rejected=%d cycles=%d bulk_bytes=%d slow_production_bound=%d elapsed=%s configured_global_limit=4 target_limit=4 agents=2", requestCount, persisted, publicOriginCalls.Load(), healthCalls.Load(), rejects, cycles, 2*cycles*sustainedBulkBytes, 2*cycles*sustainedSlowBytes, time.Since(started))
	})
	if app == nil || !ok {
		return
	}
	mixedSaturationAwait(t, "joined runtimes release every ledger owner", func() bool {
		s := app.agentStreamCapacity.snapshot()
		if s.Total.InUse != 0 || s.ActiveLeases != 0 || s.RegisteredSessions != 0 || s.AdaptiveExternalBytes != 0 || s.AdaptiveExternalFDs != 0 || s.Waiters != 0 {
			return false
		}
		for owner := adaptiveResourceOwner(0); owner < adaptiveResourceOwnerCount; owner++ {
			if s.AdaptiveExternalBytesByOwner[owner] != 0 || s.AdaptiveExternalFDsByOwner[owner] != 0 || s.AdaptiveExternalReservationsByOwner[owner] != 0 {
				return false
			}
		}
		for _, agent := range agents {
			if app.AgentHub.connectedByID(agent.AgentID) != nil || agent.ActiveRequests.Load() != 0 || agent.tunnelResourceSnapshot().SessionResourceBytes != 0 {
				return false
			}
		}
		return true
	})
	if err := app.agentStreamCapacity.validateInvariants(); err != nil {
		t.Fatal(err)
	}
	sustainedRecorderDrained(t, app)
	t.Log("sustained_terminal joined_runtimes=2 all_ledger_owners=0 physical_leases=0 registered_sessions=0 recorder_pending=0 recorder_dropped=0")
}

func sustainedIdle(app *App, agents [2]*AgentConn, targets [2]publicRouteTargetConfig) bool {
	if len(app.publicProxyRequests.slots) != 0 {
		return false
	}
	app.publicClientRequests.mu.Lock()
	clients := len(app.publicClientRequests.inUse)
	app.publicClientRequests.mu.Unlock()
	if clients != 0 {
		return false
	}
	s := app.agentStreamCapacity.snapshot()
	for _, owner := range []adaptiveResourceOwner{adaptiveResourceOwnerPublicRequest, adaptiveResourceOwnerPublicResponse} {
		if s.AdaptiveExternalBytesByOwner[owner] != 0 || s.AdaptiveExternalFDsByOwner[owner] != 0 || s.AdaptiveExternalReservationsByOwner[owner] != 0 {
			return false
		}
	}
	for i, agent := range agents {
		if agent.ActiveRequests.Load() != 0 || app.AgentTransports.inFlightForAgent(agent.AgentID) != 0 || mixedSaturationTargetUsage(app, targets[i].ID) != 0 {
			return false
		}
	}
	return true
}

func sustainedRecorderDrained(t *testing.T, app *App) {
	t.Helper()
	r := app.observabilityRecorder
	if r == nil || r.droppedEvents.Load() != 0 || r.pendingEvents.Load() != 0 || len(r.events) != 0 {
		t.Fatal("recorder retained or dropped terminal events")
	}
}

func sustainedCheckEvents(t *testing.T, app *App, targets [2]publicRouteTargetConfig, agents [2]*AgentConn, expected map[string]sustainedExpectedEvent, persisted, rejects, partials *int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), mixedSaturationWait)
	defer cancel()
	want := *persisted + len(expected)
	mixedSaturationAwait(t, "cycle terminal event conservation", func() bool {
		if err := app.FlushObservabilityRecorder(ctx); err != nil {
			t.Fatal(err)
		}
		var count int
		if err := app.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM proxy_request_events`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count > want {
			t.Fatalf("events=%d exceed expected=%d", count, want)
		}
		return count == want
	})
	rows, err := app.DB.QueryContext(ctx, `SELECT host,path_prefix,listener_id,route_id,route_target_id,agent_id,status_code,error_kind,response_bytes FROM proxy_request_events ORDER BY id LIMIT -1 OFFSET ?`, *persisted)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var host, prefix, kind string
		var listener, route, target, agent sql.NullInt64
		var status, responseBytes int64
		if err := rows.Scan(&host, &prefix, &listener, &route, &target, &agent, &status, &kind, &responseBytes); err != nil {
			t.Fatal(err)
		}
		item, exists := expected[host]
		if !exists {
			t.Fatalf("unexpected/duplicate event host=%s", host)
		}
		delete(expected, host)
		if !listener.Valid || listener.Int64 != 1 {
			t.Fatalf("listener attribution host=%s listener=%v", host, listener)
		}
		if item.reject {
			// Global admission precedes routing: only listener + the HTTP
			// request's redacted context are known at this rejection stage.
			if status != 503 || kind != "public_request_capacity" || route.Valid || target.Valid || agent.Valid || prefix != "/..." || responseBytes != item.bytes {
				t.Fatalf("global event host=%s status=%d kind=%q route=%v target=%v agent=%v prefix=%q bytes=%d", host, status, kind, route, target, agent, prefix, responseBytes)
			}
			*rejects++
		} else {
			if status != 200 || kind != "" || prefix != "/" || !route.Valid || route.Int64 != targets[item.route].RouteID || !target.Valid || target.Int64 != targets[item.route].ID || !agent.Valid || agent.Int64 != agents[item.route].AgentID {
				t.Fatalf("success attribution host=%s status=%d kind=%q route=%v target=%v agent=%v", host, status, kind, route, target, agent)
			}
			if item.slow {
				if responseBytes < mixedSaturationPrefix || responseBytes > sustainedSlowBytes {
					t.Fatalf("slow event host=%s bytes=%d", host, responseBytes)
				}
				*partials++
			} else if responseBytes != item.bytes {
				t.Fatalf("response event path=%s bytes=%d want=%d", item.path, responseBytes, item.bytes)
			}
		}
		*persisted++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if *persisted != want || len(expected) != 0 {
		t.Fatalf("missing cycle events persisted=%d want=%d remaining=%v", *persisted, want, expected)
	}
	sustainedRecorderDrained(t, app)
}

func sustainedResourceSnapshot(t *testing.T, app *App, agents [2]*AgentConn, cycle int, elapsed time.Duration, samples [2][]time.Duration) {
	t.Helper()
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	s := app.agentStreamCapacity.snapshot()
	// These are Go-process heap diagnostics and server ownership promises,
	// not process-separated RSS or measured committed pool memory. The stable
	// resource sampler is synthetic and is deliberately not reported as RSS.
	payload := struct {
		Cycle             int                               `json:"cycle"`
		ElapsedNS         int64                             `json:"elapsed_ns"`
		LatencyNS         [2][]time.Duration                `json:"latency_ns"`
		GoHeapAlloc       uint64                            `json:"shared_process_go_heap_alloc"`
		GoHeapInuse       uint64                            `json:"shared_process_go_heap_inuse"`
		Goroutines        int                               `json:"shared_process_goroutines"`
		PoolEntries       int                               `json:"pool_entries"`
		PhysicalLeases    int                               `json:"physical_leases"`
		OwnerBytes        [adaptiveResourceOwnerCount]int64 `json:"server_ledger_owner_bytes"`
		OwnerReservations [adaptiveResourceOwnerCount]int64 `json:"server_ledger_owner_reservations"`
		SessionResources  [2]agentTunnelResourceSnapshot    `json:"live_session_pool_promises"`
	}{cycle, int64(elapsed), samples, memory.HeapAlloc, memory.HeapInuse, runtime.NumGoroutine(), app.AgentTransports.len(), s.Total.InUse, s.AdaptiveExternalBytesByOwner, s.AdaptiveExternalReservationsByOwner, [2]agentTunnelResourceSnapshot{agents[0].tunnelResourceSnapshot(), agents[1].tunnelResourceSnapshot()}}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("sustained_resource %s", encoded)
}
