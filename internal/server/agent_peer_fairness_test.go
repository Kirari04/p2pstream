package server

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"
	agentruntime "p2pstream/internal/agent"
	"p2pstream/internal/config"
	"p2pstream/internal/db"
	"p2pstream/internal/tunnel"
)

type peerFairnessGate struct {
	finish  chan struct{}
	stopped chan struct{}
	calls   atomic.Int64
}

type peerFairnessRequest struct {
	done     chan struct{}
	cancel   context.CancelFunc
	response *http.Response
	close    func()
	err      error
	gate     *peerFairnessGate
}

type peerFairnessHealthGate struct {
	peerFairnessGate
	started    chan struct{}
	finishOnce sync.Once
}

func (g *peerFairnessHealthGate) release() {
	g.finishOnce.Do(func() { close(g.finish) })
}

type peerFairnessHealthCall struct {
	done   chan struct{}
	cancel context.CancelFunc
	result chan publicRouteTargetHealthCheckAttempt
}

type peerFairnessPublicLeaseIdentity struct {
	lease      *agentStreamCapacityLease
	class      agentStreamCapacityClass
	queueKey   string
	sessionKey string
	state      agentStreamLeaseState
}

func peerFairnessPublicLeases(manager *agentStreamCapacityManager) map[uint64]peerFairnessPublicLeaseIdentity {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	leases := make(map[uint64]peerFairnessPublicLeaseIdentity)
	for id, lease := range manager.activeLeases {
		if lease.class != agentStreamCapacityTrustedHealth {
			leases[id] = peerFairnessPublicLeaseIdentity{lease, lease.class, lease.queueKey, lease.sessionKey, lease.state}
		}
	}
	return leases
}

func peerFairnessControlGate(agent *AgentConn) (int, int) {
	agent.controlGateMu.Lock()
	defer agent.controlGateMu.Unlock()
	return len(agent.controlGate), cap(agent.controlGate)
}

// The contract is future-grant fairness between authenticated agent generations,
// not tenant isolation, equal throughput, or preemption. H2 public requests use
// H1 origins so each held response owns a separate physical agent stream.
func TestRealAgentPeerBorrowerYieldsQueuedStream(t *testing.T) {
	var app *App
	var agents [2]*AgentConn
	ok := t.Run("both_borrowers", func(t *testing.T) {
		f := newPeerFairnessFixture(t)
		app = f.app
		agents = [2]*AgentConn{f.agent, mixedSaturationAddAgent(t, f)}
		app.publicProxyRequests = newRequestCapacityLimiter(8, 8)
		app.publicTargetRequests = newKeyedRequestCapacityLimiter(8, 8)
		app.publicClientRequests = newKeyedStringRequestCapacityLimiter(8)
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), mixedSaturationWait)
			defer cancel()
			if err := app.CloseObservabilityRecorder(ctx); err != nil {
				t.Error(err)
			}
		})
		workCtx, cancelWork := context.WithTimeout(t.Context(), 45*time.Second)
		defer cancelWork()
		stop := make(chan struct{})
		defer close(stop)
		gates := make(map[string]*peerFairnessGate, 12)
		for i := range 12 {
			gates[fmt.Sprintf("/request-%d", i)] = &peerFairnessGate{finish: make(chan struct{}), stopped: make(chan struct{})}
		}
		healthGates := make(map[string]*peerFairnessHealthGate, 2)
		for i := range agents {
			healthGates[fmt.Sprintf("/health-%d", i)] = &peerFairnessHealthGate{
				peerFairnessGate: peerFairnessGate{finish: make(chan struct{}), stopped: make(chan struct{})},
				started:          make(chan struct{}),
			}
		}
		prefixPayload := bytes.Repeat([]byte{'x'}, mixedSaturationPrefix)
		var nonH1 atomic.Bool
		origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if gate := healthGates[r.URL.Path]; gate != nil {
				if gate.calls.Add(1) != 1 {
					http.Error(w, "duplicate health request", http.StatusInternalServerError)
					return
				}
				defer close(gate.stopped)
				if r.ProtoMajor != 1 {
					nonH1.Store(true)
				}
				w.Header().Set("Connection", "close")
				_, _ = io.WriteString(w, "health-")
				w.(http.Flusher).Flush()
				close(gate.started)
				select {
				case <-r.Context().Done():
				case <-stop:
				case <-gate.finish:
					_, _ = io.WriteString(w, "ok")
				}
				return
			}
			gate := gates[r.URL.Path]
			if gate == nil {
				http.Error(w, "unexpected request", 500)
				return
			}
			if gate.calls.Add(1) != 1 {
				http.Error(w, "duplicate request", 500)
				return
			}
			defer close(gate.stopped)
			if r.ProtoMajor != 1 {
				nonH1.Store(true)
			}
			// The held prefix is genuinely streaming: unknown length makes
			// ReverseProxy flush each write instead of waiting for a buffered tail.
			// Completion must close the socket rather than retain the physical lease.
			w.Header().Set("Connection", "close")
			_, _ = w.Write(prefixPayload)
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
				return
			case <-stop:
				return
			case <-gate.finish:
				_, _ = io.WriteString(w, "y")
			}
		}))
		t.Cleanup(origin.Close)
		targets := [2]publicRouteTargetConfig{}
		snapshot := &publicProxySnapshot{
			Listeners:        map[int64]publicListenerConfig{1: {ID: 1, Protocol: publicListenerProtocolHTTP, Enabled: true}},
			RoutesByListener: make(map[int64][]publicRouteConfig), RouteTargets: make(map[int64]publicRouteTargetConfig), Agents: make(map[int64]publicAgentConfig),
		}
		keys := [2]string{}
		for i, agent := range agents {
			keys[i] = agentStreamCapacitySessionKey(agent, agent.tunnelSession(1))
			target := f.target
			target.ID, target.RouteID = int64(370+i), int64(30+i)
			target.URL, target.ParsedURL = origin.URL, mustParseAggregateTestURL(t, origin.URL)
			target.AgentSelector = publicAgentSelectorConfig{MatchLabels: map[string]string{"fairness-peer": fmt.Sprint(i)}}
			target.AgentLoadBalancing, target.Weight = publicRouteTargetLoadBalancingRoundRobin, 100
			targets[i] = target
			snapshot.RouteTargets[target.ID] = target
			snapshot.Agents[agent.AgentID] = publicAgentConfig{ID: agent.AgentID, PublicID: agent.PublicID, Enabled: true, Labels: map[string]string{"fairness-peer": fmt.Sprint(i)}}
			snapshot.RoutesByListener[1] = append(snapshot.RoutesByListener[1], publicRouteConfig{
				ID: target.RouteID, ListenerID: 1, HostPattern: fmt.Sprintf("*.%c.fairness.test", 'a'+i), PathPrefix: "/", Action: publicRouteActionForward,
				PathSecurityMode: publicRoutePathSecurityModeStrict, TargetLoadBalancing: publicRouteTargetLoadBalancingRoundRobin, Enabled: true, Targets: []publicRouteTargetConfig{target},
			})
		}
		setPublicSnapshotForTest(t, app, snapshot)
		app.TargetHealth.reconcile(app, snapshot, false)
		public := httptest.NewUnstartedServer(app.publicProxyHandler(1))
		public.EnableHTTP2 = true
		public.StartTLS()
		t.Cleanup(public.Close)
		transport := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, ForceAttemptHTTP2: true} // fixture certificate
		client := &http.Client{Transport: transport}
		t.Cleanup(transport.CloseIdleConnections)
		expected := make(map[string]sustainedExpectedEvent)
		requests := make([]*peerFairnessRequest, 0, 12)
		healthCalls := make([]*peerFairnessHealthCall, 0, 4)
		// Every client goroutine is joined and every returned body closed on failure
		// as well as success, before the public/origin servers and real agents close.
		t.Cleanup(func() {
			cancelWork()
			for _, request := range requests {
				request.cancel()
			}
			joinCtx, cancel := context.WithTimeout(context.Background(), mixedSaturationWait)
			defer cancel()
			for _, request := range requests {
				select {
				case <-request.done:
					if request.close != nil {
						request.close()
					}
				case <-joinCtx.Done():
					t.Error("fairness clients did not stop")
					return
				}
			}
		})
		// Own every added health call before it starts, and join it and any
		// started origin handler before the existing public/server cleanup.
		t.Cleanup(func() {
			if t.Failed() {
				t.Logf("peer_fairness_health_failure capacity=%+v", app.agentStreamCapacity.snapshot())
			}
			for _, call := range healthCalls {
				call.cancel()
			}
			for _, gate := range healthGates {
				gate.release()
			}
			joinCtx, cancel := context.WithTimeout(context.Background(), mixedSaturationWait)
			defer cancel()
			for _, call := range healthCalls {
				select {
				case <-call.done:
				case <-joinCtx.Done():
					t.Error("fairness health calls did not stop")
					return
				}
			}
			for _, gate := range healthGates {
				select {
				case <-gate.started:
					select {
					case <-gate.stopped:
					case <-joinCtx.Done():
						t.Error("fairness health origin did not stop")
					}
				default:
				}
			}
		})
		startHealth := func(health publicRouteTargetHealthConfig, agent *AgentConn) *peerFairnessHealthCall {
			t.Helper()
			ctx, cancel := context.WithCancel(workCtx)
			call := &peerFairnessHealthCall{done: make(chan struct{}), cancel: cancel, result: make(chan publicRouteTargetHealthCheckAttempt, 1)}
			healthCalls = append(healthCalls, call)
			go func() {
				defer close(call.done)
				call.result <- app.runPublicRouteTargetHealthCheckViaAgent(ctx, health, agent)
			}()
			return call
		}
		awaitHealth := func(call *peerFairnessHealthCall) publicRouteTargetHealthCheckAttempt {
			t.Helper()
			mixedSaturationAwaitChannel(t, "trusted health call completion", call.done)
			call.cancel()
			return <-call.result
		}
		start := func(route int, canceledBody bool) *peerFairnessRequest {
			t.Helper()
			path := fmt.Sprintf("/request-%d", len(requests))
			ctx, cancel := context.WithTimeout(workCtx, 20*time.Second)
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, public.URL+path, nil)
			if err != nil {
				cancel()
				t.Fatal(err)
			}
			req.Host = fmt.Sprintf("request-%d.%c.fairness.test", len(requests), 'a'+route)
			size := int64(len(prefixPayload) + 1)
			if canceledBody {
				size = int64(len(prefixPayload))
			}
			expected[req.Host] = sustainedExpectedEvent{route: route, path: path, bytes: size}
			request := &peerFairnessRequest{done: make(chan struct{}), cancel: cancel, gate: gates[path]}
			requests = append(requests, request)
			go func() {
				defer close(request.done)
				request.response, request.err = client.Do(req)
				if request.err != nil {
					return
				}
				response := request.response
				request.close = ownedSaturationBodyClose(response.Body)
				if response.StatusCode != 200 || response.ProtoMajor != 2 || response.ContentLength != -1 {
					request.err = fmt.Errorf("response status/protocol/length=%d/%s/%d", response.StatusCode, response.Proto, response.ContentLength)
					return
				}
				prefix := make([]byte, len(prefixPayload))
				_, request.err = io.ReadFull(response.Body, prefix)
				if request.err == nil && !bytes.Equal(prefix, prefixPayload) {
					request.err = fmt.Errorf("response prefix mismatch")
				}
			}()
			return request
		}
		await := func(request *peerFairnessRequest) {
			t.Helper()
			mixedSaturationAwaitChannel(t, "public response prefix", request.done)
			if request.err != nil {
				t.Fatal(request.err)
			}
		}
		finish := func(request *peerFairnessRequest) {
			t.Helper()
			await(request)
			close(request.gate.finish)
			timer := time.AfterFunc(mixedSaturationWait, request.cancel)
			payload, err := io.ReadAll(io.LimitReader(request.response.Body, 3))
			timer.Stop()
			request.close()
			request.cancel()
			if err != nil || string(payload) != "y" {
				t.Fatalf("response suffix=%q err=%v", payload, err)
			}
			mixedSaturationAwaitChannel(t, "origin completion", request.gate.stopped)
		}
		controlSessions := [2]any{agents[0].tunnelSession(0), agents[1].tunnelSession(0)}
		bulkSessions := [2]any{agents[0].tunnelSession(1), agents[1].tunnelSession(1)}
		manager := app.agentStreamCapacity
		initial := manager.snapshot()
		if initial.Total.Capacity != 3 || initial.Public.Capacity != 2 || initial.Pooled.Capacity != 1 || initial.Control.Capacity != 1 || initial.ContendedSessionPublicLimit != 1 || initial.RegisteredSessions != 2 {
			t.Fatalf("unexpected configured fairness budgets: %+v", initial)
		}
		healthSuccesses, healthSkips := 0, 0
		for borrower := range agents {
			competitor := 1 - borrower
			first := start(borrower, true)
			await(first)
			second := start(borrower, false)
			await(second)
			mixedSaturationAwait(t, "borrow all idle public headroom", func() bool {
				s := manager.snapshot()
				return s.Public.InUse == 2 && s.Pooled.InUse == 1 && s.PublicBySession[keys[borrower]] == 2 && s.PublicBySession[keys[competitor]] == 0 && s.Waiters == 0
			})
			queuedBorrower := start(borrower, false)
			queueA := agentStreamCapacityQueueKey(agentStreamCapacityPublicOneShot, fmt.Sprintf("route-target:%d", targets[borrower].ID))
			queueB := agentStreamCapacityQueueKey(agentStreamCapacityPublicOneShot, fmt.Sprintf("route-target:%d", targets[competitor].ID))
			mixedSaturationAwait(t, "borrower queued first", func() bool { s := manager.snapshot(); return s.Waiters == 1 && s.WaitersByKey[queueA] == 1 })
			queuedCompetitor := start(competitor, false)
			mixedSaturationAwait(t, "competitor queued second", func() bool {
				s := manager.snapshot()
				return s.Waiters == 2 && s.WaitersByKey[queueA] == 1 && s.WaitersByKey[queueB] == 1
			})
			// Both waiters must still be outside the origin. No held owner is preempted.
			if queuedBorrower.gate.calls.Load() != 0 || queuedCompetitor.gate.calls.Load() != 0 || first.gate.calls.Load() != 1 || second.gate.calls.Load() != 1 {
				t.Fatal("queue barrier did not preserve held requests")
			}
			// A real health body must use the protected control lane while both
			// public physical streams and both public scheduler waiters stay put.
			preHealth := manager.snapshot()
			publicLeases := peerFairnessPublicLeases(manager)
			publicHard := len(app.publicProxyRequests.slots)
			targetHard := [2]int{mixedSaturationTargetUsage(app, targets[0].ID), mixedSaturationTargetUsage(app, targets[1].ID)}
			clients := func() map[string]int64 {
				app.publicClientRequests.mu.Lock()
				defer app.publicClientRequests.mu.Unlock()
				return maps.Clone(app.publicClientRequests.inUse)
			}
			publicClients := clients()
			if len(publicLeases) != 2 {
				t.Fatalf("public lease identities=%v, want two", publicLeases)
			}
			assertHealthState := func(phase string, control int, grants, releases uint64) {
				t.Helper()
				mixedSaturationAwait(t, fmt.Sprintf("health %s borrower=%d preserves public owners and queues", phase, borrower), func() bool {
					s := manager.snapshot()
					if s.Total.InUse != 2+control || s.Public.InUse != 2 || s.Pooled.InUse != 1 || s.Control.InUse != control || s.ActiveLeases != 2+control ||
						s.Granted != preHealth.Granted+grants || s.Released != preHealth.Released+releases || s.RegisteredSessions != 2 ||
						s.Waiters != 2 || s.WaitersByKey[queueA] != 1 || s.WaitersByKey[queueB] != 1 || s.WaitersByClass[agentStreamCapacityTrustedHealth] != 0 ||
						s.PublicBySession[keys[borrower]] != 2 || s.PublicBySession[keys[competitor]] != 0 || !maps.Equal(publicLeases, peerFairnessPublicLeases(manager)) {
						return false
					}
					for _, owner := range []adaptiveResourceOwner{adaptiveResourceOwnerPublicRequest, adaptiveResourceOwnerPublicResponse} {
						if s.AdaptiveExternalBytesByOwner[owner] != preHealth.AdaptiveExternalBytesByOwner[owner] || s.AdaptiveExternalFDsByOwner[owner] != preHealth.AdaptiveExternalFDsByOwner[owner] || s.AdaptiveExternalReservationsByOwner[owner] != preHealth.AdaptiveExternalReservationsByOwner[owner] {
							return false
						}
					}
					used, capacity := peerFairnessControlGate(agents[borrower])
					otherUsed, _ := peerFairnessControlGate(agents[competitor])
					if used != control || otherUsed != 0 || (control == 1 && capacity != tunnel.NegotiatedControlStreamLimit) ||
						agents[borrower].tunnelSession(1).NumStreams() != 2 || agents[competitor].tunnelSession(1).NumStreams() != 0 ||
						agents[borrower].tunnelSession(0).NumStreams() != control || agents[competitor].tunnelSession(0).NumStreams() != 0 ||
						len(app.publicProxyRequests.slots) != publicHard || mixedSaturationTargetUsage(app, targets[0].ID) != targetHard[0] || mixedSaturationTargetUsage(app, targets[1].ID) != targetHard[1] || !maps.Equal(publicClients, clients()) ||
						queuedBorrower.gate.calls.Load() != 0 || queuedCompetitor.gate.calls.Load() != 0 || first.gate.calls.Load() != 1 || second.gate.calls.Load() != 1 {
						return false
					}
					for _, request := range []*peerFairnessRequest{first, second} {
						select {
						case <-request.gate.stopped:
							return false
						default:
						}
					}
					return true
				})
				for i, agent := range agents {
					if app.AgentHub.connectedByID(agent.AgentID) != agent || agent.tunnelSession(0) != controlSessions[i] || agent.tunnelSession(1) != bulkSessions[i] {
						t.Fatal("health phase replaced peer generation or lane")
					}
				}
				if err := manager.validateInvariants(); err != nil {
					t.Fatal(err)
				}
				t.Logf("peer_fairness_health borrower=%d phase=%s public=2 pooled=1 control=%d waiters=2 grants_delta=%d releases_delta=%d", borrower, phase, control, grants, releases)
			}
			assertHealthState("before", 0, 0, 0)
			healthPath := fmt.Sprintf("/health-%d", borrower)
			healthGate := healthGates[healthPath]
			health := testHealthTarget(t, targets[borrower].ID, publicRouteTargetTransportAgent, origin.URL)
			health.HealthCheck.Path, health.HealthCheck.Timeout = healthPath, mixedSaturationWait
			checkHealthIdentity := func(attempt publicRouteTargetHealthCheckAttempt) {
				t.Helper()
				if attempt.AgentID != agents[borrower].AgentID || attempt.AgentPublicID != agents[borrower].PublicID || attempt.AgentName != agents[borrower].Name ||
					attempt.URL != origin.URL+healthPath || attempt.Method != http.MethodGet || attempt.Timeout != mixedSaturationWait ||
					attempt.DebugAttributes["transport"] != "agent_health_one_shot" || attempt.DebugAttributes["check_path"] != healthPath {
					t.Fatalf("health result identity=%+v", attempt)
				}
			}
			heldHealth := startHealth(health, agents[borrower])
			mixedSaturationAwaitChannel(t, "trusted health response prefix", healthGate.started)
			assertHealthState("held", 1, 1, 0)
			skipped := awaitHealth(startHealth(health, agents[borrower]))
			checkHealthIdentity(skipped)
			var dialErr agentDialError
			// Both server total and control ceilings are full. Their internal
			// check order is not part of the protected-control contract.
			if !skipped.Skipped || skipped.StatusCode != 0 || skipped.ErrorKind != "agent_server_health_capacity" || skipped.Err == nil ||
				!errors.As(skipped.Err, &dialErr) || dialErr.Kind != "server_health_capacity" || (!errors.Is(skipped.Err, errAgentStreamCapacityTotalBudget) && !errors.Is(skipped.Err, errAgentStreamCapacityControlBudget)) {
				t.Fatalf("second health attempt=%+v, want local server-capacity skip", skipped)
			}
			healthSkips++
			if healthGate.calls.Load() != 1 {
				t.Fatal("locally skipped health reached origin")
			}
			assertHealthState("skipped", 1, 1, 0)
			healthGate.release()
			completed := awaitHealth(heldHealth)
			checkHealthIdentity(completed)
			if completed.Skipped || completed.Err != nil || completed.StatusCode != http.StatusOK || completed.ErrorKind != "success" {
				t.Fatalf("held health completion=%+v", completed)
			}
			healthSuccesses++
			mixedSaturationAwaitChannel(t, "trusted health origin completion", healthGate.stopped)
			assertHealthState("drained", 0, 1, 1)
			// Keep the original public handoff counters relative to this point;
			// finishing control must not itself grant either public waiter.
			before := manager.snapshot()
			first.cancel()
			first.close()
			mixedSaturationAwaitChannel(t, "canceled origin exits", first.gate.stopped)
			await(queuedCompetitor)
			mixedSaturationAwait(t, "released borrower lease grants competitor before borrower", func() bool {
				s := manager.snapshot()
				return s.Public.InUse == 2 && s.ActiveLeases == 2 && s.Released == before.Released+1 && s.Granted == before.Granted+1 && s.PublicBySession[keys[borrower]] == 1 && s.PublicBySession[keys[competitor]] == 1 && s.Waiters == 1 && s.WaitersByKey[queueA] == 1 && s.WaitersByKey[queueB] == 0 && agents[borrower].tunnelSession(1).NumStreams() == 1 && agents[competitor].tunnelSession(1).NumStreams() == 1
			})
			if queuedBorrower.gate.calls.Load() != 0 {
				t.Fatal("borrower barged ahead of competing session")
			}
			select {
			case <-second.gate.stopped:
				t.Fatal("uncanceled borrower stream was preempted")
			default:
			}
			finish(queuedCompetitor)
			await(queuedBorrower)
			finish(queuedBorrower)
			finish(second)
			mixedSaturationAwait(t, "held requests and physical leases drain", func() bool {
				return sustainedIdle(app, agents, targets) && manager.snapshot().Total.InUse == 0 && manager.snapshot().Waiters == 0
			})
			for _, route := range []int{borrower, competitor} {
				recovery := start(route, false)
				finish(recovery)
			}
			mixedSaturationAwait(t, "both peers recover", func() bool { return sustainedIdle(app, agents, targets) && manager.snapshot().Total.InUse == 0 })
			app.CloseAgentTransports()
			for i, agent := range agents {
				if app.AgentHub.connectedByID(agent.AgentID) != agent || !realAgentAggregateLaneShape(agent, 1) || agent.tunnelSession(0) != controlSessions[i] || agent.tunnelSession(1) != bulkSessions[i] {
					t.Fatal("fairness phase replaced the original peer generation or lane")
				}
			}
			if err := manager.validateInvariants(); err != nil {
				t.Fatal(err)
			}
			t.Logf("peer_fairness borrower=%d competitor=%d first_waiter_yields=true requests=6 canceled_held=1", borrower, competitor)
		}
		if len(requests) != 12 || nonH1.Load() {
			t.Fatalf("requests=%d non_h1_origin=%v", len(requests), nonH1.Load())
		}
		for path, gate := range gates {
			if gate.calls.Load() != 1 {
				t.Fatalf("origin path=%s calls=%d", path, gate.calls.Load())
			}
			mixedSaturationAwaitChannel(t, "all origin handlers joined", gate.stopped)
		}
		if len(healthCalls) != 4 || healthSuccesses != 2 || healthSkips != 2 {
			t.Fatalf("health calls/success/skips=%d/%d/%d, want 4/2/2", len(healthCalls), healthSuccesses, healthSkips)
		}
		for path, gate := range healthGates {
			if gate.calls.Load() != 1 {
				t.Fatalf("health origin path=%s calls=%d, want one", path, gate.calls.Load())
			}
			mixedSaturationAwaitChannel(t, "all health origin handlers joined", gate.stopped)
		}
		t.Log("peer_fairness_health_terminal attempts=4 successes=2 skipped=2 origin=2 public_events_unchanged=12")
		persisted, rejects, partials := 0, 0, 0
		sustainedCheckEvents(t, app, targets, agents, expected, &persisted, &rejects, &partials)
		if persisted != 12 || rejects != 0 || partials != 0 {
			t.Fatalf("events=%d rejects=%d unexpected_partials=%d", persisted, rejects, partials)
		}
		app.CloseAgentTransports()
		mixedSaturationAwait(t, "terminal live stream accounting", func() bool {
			s := manager.snapshot()
			return sustainedIdle(app, agents, targets) && app.AgentTransports.len() == 0 && s.Total.InUse == 0 && s.Waiters == 0 && s.ActiveLeases == 0 && s.Granted == s.Released && agents[0].tunnelSession(1).NumStreams() == 0 && agents[1].tunnelSession(1).NumStreams() == 0 && agents[0].tunnelSession(0).NumStreams() == 0 && agents[1].tunnelSession(0).NumStreams() == 0
		})
	})
	if !ok || app == nil {
		return
	}
	mixedSaturationAwait(t, "joined peers refund session and external owners", func() bool {
		s := app.agentStreamCapacity.snapshot()
		if s.Total.InUse != 0 || s.ActiveLeases != 0 || s.Waiters != 0 || s.RegisteredSessions != 0 || s.AdaptiveExternalBytes != 0 || s.AdaptiveExternalFDs != 0 || s.Granted != s.Released {
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
	t.Log("peer_fairness_terminal requests=12 events=12 origin=12 canceled_held=2 joined_agents=2 all_owners=0")
}

// This local fixture fixes the stream budget before either real runtime starts;
// no live manager replacement or mutable-config override is involved.
func newPeerFairnessFixture(tb testing.TB) *realAgentProxyFixture {
	tb.Helper()
	previousLevel := zerolog.GlobalLevel()
	zerolog.SetGlobalLevel(zerolog.WarnLevel)
	tb.Cleanup(func() { zerolog.SetGlobalLevel(previousLevel) })
	ctx, cancel := context.WithCancel(tb.Context())
	tb.Cleanup(cancel)
	database, err := db.Open(filepath.Join(tb.TempDir(), "agent-performance.db"))
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { _ = database.Close() })
	app := NewApp(&config.Config{
		ManagementUIDisabled: true, ConfigDir: tb.TempDir(),
		ServerTunnelMaxConcurrentStreams: 3,
		ServerTunnelCapacityAuto:         true, TunnelMaxStreamWindowBytes: 2 << 20,
	}, database)
	installDeterministicServerResourceController(tb, app)
	app.StartAdaptiveTunnelCapacity(ctx)
	row, err := database.CreateAgent(ctx, db.CreateAgentParams{PublicID: "real-performance-agent", Name: "Real performance agent", TokenHash: hashAgentToken("test-token"), Enabled: 1})
	if err != nil {
		tb.Fatal(err)
	}
	mux := http.NewServeMux()
	app.RegisterManagementRoutes(mux)
	management := httptest.NewUnstartedServer(mux)
	management.StartTLS()
	tb.Cleanup(management.Close)
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: management.Certificate().Raw})
	agentDone := make(chan error, 1)
	go func() {
		agentDone <- agentruntime.RunContext(ctx, agentruntime.Options{
			ManagementURL: management.URL, PublicID: row.PublicID, Token: "test-token", AllowAnyTarget: true,
			ManagementCAPEMBase64: base64.StdEncoding.EncodeToString(ca),
			TunnelConnections:     1, TunnelMaxStreamWindowBytes: 2 << 20,
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
		if connected != nil && realAgentAggregateLaneShape(connected, 1) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if connected == nil || !realAgentAggregateLaneShape(connected, 1) {
		tb.Fatalf("real agent did not establish negotiated control+%d bulk lanes", 1)
	}
	return &realAgentProxyFixture{app: app, agent: connected, management: management,
		target: publicRouteTargetConfig{ID: 70, Enabled: true, TargetType: publicRouteTargetTypeProxy,
			Transport: publicRouteTargetTransportAgent, UpstreamResponseHeaderTimeout: 10 * time.Second}}
}
