package server

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// These POSTs have no idempotency guarantee. The operator-acknowledged 503
// policy may only replay a body that fits the configured replay limit.
func TestPublicFeaturePostReplayEdgesWorkload(t *testing.T) {
	var app *App
	var agents [2]*AgentConn
	if !t.Run("listener", func(t *testing.T) {
		f := newPeerFairnessFixture(t)
		app, agents = f.app, [2]*AgentConn{f.agent, mixedSaturationAddAgent(t, f)}
		app.retryReplayBudget = newRetryReplayBudget(8192)
		app.PublicCache = newPublicProxyCache(t.TempDir())
		work, cancelWork := context.WithTimeout(t.Context(), 45*time.Second)
		t.Cleanup(cancelWork)
		payload := bytes.Repeat([]byte{0, 0xff, 0x7f, 0x38, 1}, 2500)
		var calls [2]atomic.Int64
		var originCalls atomic.Int64
		ready := [2]chan struct{}{make(chan struct{}), make(chan struct{})}
		release := [2]chan struct{}{make(chan struct{}), make(chan struct{})}
		var releaseOnce [2]sync.Once
		openGate := func(id int) { releaseOnce[id].Do(func() { close(release[id]) }) }
		joined := [2]chan struct{}{make(chan struct{}), make(chan struct{})}
		stop := make(chan struct{})
		var stopOnce sync.Once
		stopOrigins := func() { stopOnce.Do(func() { close(stop) }) }
		t.Cleanup(stopOrigins)
		originErrors := make(chan error, 8)
		var originWorkers sync.WaitGroup
		origin := httptest.NewUnstartedServer(nil)
		authority := origin.Listener.Addr().String()
		origin.Config.ReadTimeout, origin.Config.WriteTimeout = mixedSaturationWait, mixedSaturationWait
		origin.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			originWorkers.Add(1)
			defer originWorkers.Done()
			originCalls.Add(1)
			bad := func(message string) {
				originErrors <- fmt.Errorf("POST edge origin: %s", message)
				http.Error(w, message, 500)
			}
			id, err := strconv.Atoi(r.Header.Get("X-Replay-Edge"))
			if err != nil || id < 0 || id >= len(calls) {
				bad("unexpected or cancelled input reached origin")
				return
			}
			if calls[id].Add(1) != 1 {
				bad("consumed oversize POST was retried")
				return
			}
			defer close(joined[id])
			if r.Method != http.MethodPost || r.ProtoMajor != 1 || r.URL.RequestURI() != "/buffered" || r.Host != authority ||
				r.Header.Get("X-Forwarded-For") != "127.0.0.1" || r.Header.Get("X-Real-Ip") != "127.0.0.1" ||
				r.Header.Get("X-Forwarded-Host") != "post.example.test" || r.Header.Get("X-Forwarded-Proto") != "http" {
				bad("request semantics or trusted forwarding differ")
				return
			}
			if (id == 0 && (r.ContentLength != int64(len(payload)) || len(r.TransferEncoding) != 0)) ||
				(id == 1 && (r.ContentLength != -1 || len(r.TransferEncoding) != 1 || r.TransferEncoding[0] != "chunked")) {
				bad("known/chunked framing differs")
				return
			}
			for _, name := range []string{"Forwarded", "Cf-Connecting-Ip", "X-Auth-Request-User", "X-Replay-Hop"} {
				if r.Header.Get(name) != "" {
					bad("untrusted identity or hop header retained: " + name)
					return
				}
			}
			body, err := io.ReadAll(io.LimitReader(r.Body, int64(len(payload)+1)))
			if err != nil || !bytes.Equal(body, payload) {
				bad("bounded prefix plus tail changed")
				return
			}
			close(ready[id])
			select {
			case <-release[id]:
			case <-stop:
				return
			case <-r.Context().Done():
				return
			case <-time.After(mixedSaturationWait):
				bad("consumed-body gate timed out")
				return
			}
			w.Header().Set("Content-Length", "6")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, "retry\n")
		})
		origin.Start()
		t.Cleanup(func() {
			stopOrigins()
			origin.Close()
			done := make(chan struct{})
			go func() { originWorkers.Wait(); close(done) }()
			mixedSaturationAwaitChannel(t, "POST edge origin handlers joined", done)
		})
		target := f.target
		target.RouteID, target.Weight = 10, 100
		target.URL, target.ParsedURL = origin.URL, mustParseAggregateTestURL(t, origin.URL)
		target.AgentLoadBalancing = publicRouteTargetLoadBalancingRoundRobin
		target.AgentSelector = publicAgentSelectorConfig{MatchLabels: map[string]string{"post-edge": "yes"}}
		cache, retries := postReplayWorkloadRules(t, app)
		listener := publicListenerConfig{ID: 1, Name: "post-replay-edges", BindAddress: "127.0.0.1", Protocol: publicListenerProtocolHTTP, Enabled: true}
		settings := defaultPublicCacheSettings()
		snapshot := &publicProxySnapshot{
			Listeners: map[int64]publicListenerConfig{1: listener},
			RoutesByListener: map[int64][]publicRouteConfig{1: {{ID: 10, ListenerID: 1, Enabled: true, HostPattern: "post.example.test", PathPrefix: "/",
				PathSecurityMode: publicRoutePathSecurityModeStrict, Action: publicRouteActionForward,
				TargetLoadBalancing: publicRouteTargetLoadBalancingRoundRobin, Targets: []publicRouteTargetConfig{target}}}},
			RouteTargets: map[int64]publicRouteTargetConfig{target.ID: target}, Agents: make(map[int64]publicAgentConfig), RetryRules: retries,
			CacheSettings: settings, CacheRules: []publicCacheRuleConfig{cache}, CacheFingerprint: publicCacheRuntimeFingerprint(settings, []publicCacheRuleConfig{cache}),
			AccessHeaderNames: []string{"X-Auth-Request-User"},
		}
		for _, agent := range agents {
			snapshot.Agents[agent.AgentID] = publicAgentConfig{ID: agent.AgentID, PublicID: agent.PublicID, Enabled: true, Labels: map[string]string{"post-edge": "yes"}}
		}
		setPublicSnapshotForTest(t, app, snapshot)
		app.PublicCache.reconcile(settings, snapshot.CacheRules)
		app.TargetHealth.reconcile(app, snapshot, false)
		if _, err := app.startPublicListenerFromSnapshot(listener, snapshot); err != nil {
			t.Fatal(err)
		}
		app.proxyMu.Lock()
		address := app.publicListenerState[1].BoundAddress
		app.proxyMu.Unlock()
		transport := &http.Transport{Proxy: nil, DisableCompression: true, MaxIdleConns: 1, MaxIdleConnsPerHost: 1}
		client := &http.Client{Transport: transport}
		closed := false
		closeRuntime := func() {
			if closed {
				return
			}
			closed = true
			cancelWork()
			stopOrigins()
			transport.CloseIdleConnections()
			ctx, cancel := context.WithTimeout(context.Background(), mixedSaturationWait)
			defer cancel()
			if _, err := app.stopPublicListenerRuntime(ctx, 1); err != nil {
				t.Error(err)
			}
			app.CloseAgentTransports()
			if err := app.CloseObservabilityRecorder(ctx); err != nil {
				t.Error(err)
			}
		}
		t.Cleanup(closeRuntime)
		for id, name := range []string{"known-oversize", "chunked-oversize"} {
			if !t.Run(name, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(work, mixedSaturationWait)
				defer cancel()
				var reused atomic.Bool
				ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) { reused.Store(info.Reused) }})
				req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+address+"/buffered", bytes.NewReader(payload))
				if err != nil {
					t.Fatal(err)
				}
				req.GetBody = nil
				if id == 1 {
					req.ContentLength, req.TransferEncoding = -1, []string{"chunked"}
				}
				req.Host = "post.example.test"
				req.Header.Set("X-Replay-Edge", strconv.Itoa(id))
				for _, name := range []string{"Forwarded", "X-Forwarded-For", "X-Real-Ip", "Cf-Connecting-Ip"} {
					req.Header.Set(name, "198.51.100.91")
				}
				req.Header.Set("X-Auth-Request-User", "forged-principal")
				req.Header.Set("Connection", "X-Replay-Hop")
				req.Header.Set("X-Replay-Hop", "remove-me")
				type result struct {
					resp *http.Response
					err  error
				}
				results, done := make(chan result, 1), make(chan struct{})
				go func() { defer close(done); resp, err := client.Do(req); results <- result{resp, err} }()
				var outcome result
				received := false
				t.Cleanup(func() {
					cancel()
					openGate(id)
					mixedSaturationAwaitChannel(t, "POST edge client joined", done)
					if !received {
						outcome = <-results
					}
					if outcome.resp != nil {
						_ = outcome.resp.Body.Close()
					}
				})
				select {
				case <-ready[id]:
				case outcome = <-results:
					received = true
					t.Fatalf("response preceded consumed-body gate: %v", outcome.err)
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				reservation, working := int64(0), int64(0)
				if id == 1 {
					reservation, working = 8192, 24579
				}
				assertRequestOwnershipReservation(t, app, reservation, working)
				s := app.agentStreamCapacity.snapshot()
				if s.AdaptiveExternalReservationsByOwner[adaptiveResourceOwnerPublicRequest] != 1 || agents[0].ActiveRequests.Load()+agents[1].ActiveRequests.Load() != 1 || originCalls.Load() != int64(id+1) {
					t.Fatal("consumed-body gate lost request ownership or gained an extra origin attempt")
				}
				openGate(id)
				select {
				case outcome = <-results:
					received = true
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				if outcome.err != nil || outcome.resp == nil {
					t.Fatalf("response=%v err=%v", outcome.resp, outcome.err)
				}
				resp := outcome.resp
				body, err := io.ReadAll(io.LimitReader(resp.Body, 7))
				_ = resp.Body.Close()
				if err != nil || !bytes.Equal(body, []byte("retry\n")) || resp.StatusCode != 503 || resp.ProtoMajor != 1 || resp.Close || resp.Header.Get("X-P2pstream-Cache") != "" || (id == 1 && !reused.Load()) {
					t.Fatalf("response status=%d proto=%s close=%t bytes=%q reused=%t err=%v", resp.StatusCode, resp.Proto, resp.Close, body, reused.Load(), err)
				}
				mixedSaturationAwaitChannel(t, "oversize origin joined", joined[id])
				postReplayEdgesAwaitIdle(t, app, agents, target)
				postReplayEdgesEvent(t, app, id+1, target.ID, retries[0].ID, agents, int64(len(payload)), false)
			}) {
				return
			}
		}
		// A partial chunked upload blocks before any agent/origin attempt. The
		// ledger state is the handshake proving that buffering has started.
		conn, err := net.DialTimeout("tcp", address, mixedSaturationWait)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		if err := conn.SetDeadline(time.Now().Add(mixedSaturationWait)); err != nil {
			t.Fatal(err)
		}
		prefix := bytes.Repeat([]byte{0x9c}, 4096)
		if _, err := fmt.Fprintf(conn, "POST /buffered HTTP/1.1\r\nHost: post.example.test\r\nTransfer-Encoding: chunked\r\nX-Replay-Edge: cancelled\r\n\r\n%x\r\n%s\r\n", len(prefix), prefix); err != nil {
			t.Fatal(err)
		}
		mixedSaturationAwait(t, "partial upload owns replay reservation", func() bool {
			s := app.agentStreamCapacity.snapshot()
			return app.retryReplayBudget.used.Load() == 8192 && s.AdaptiveExternalReservationsByOwner[adaptiveResourceOwnerReplayBuffer] == 1 && s.AdaptiveExternalReservationsByOwner[adaptiveResourceOwnerPublicRequest] == 1
		})
		assertRequestOwnershipReservation(t, app, 8192, 24579)
		if originCalls.Load() != 2 || agents[0].ActiveRequests.Load()+agents[1].ActiveRequests.Load() != 0 {
			t.Fatal("partial upload reached an agent or origin")
		}
		if release, ok := app.tryAcquireRetryReplayBudget(1); ok {
			release()
			t.Fatal("partial upload reservation was spent twice")
		}
		_ = conn.Close()
		postReplayEdgesAwaitIdle(t, app, agents, target)
		postReplayEdgesEvent(t, app, 3, target.ID, retries[0].ID, agents, 4096, true)
		if originCalls.Load() != 2 || calls[0].Load() != 1 || calls[1].Load() != 1 {
			t.Fatalf("origin calls=%d known/chunked=%d/%d", originCalls.Load(), calls[0].Load(), calls[1].Load())
		}
		select {
		case err := <-originErrors:
			t.Fatal(err)
		default:
		}
		assertPublicCacheStorageStats(t, app, 0, 0, 0)
		closeRuntime()
		mixedSaturationAwait(t, "POST edge transports drained", func() bool {
			s := app.agentStreamCapacity.snapshot()
			return app.AgentTransports.len() == 0 && s.Total.InUse == 0 && s.ActiveLeases == 0 && s.Waiters == 0 && s.Granted == s.Released
		})
	}) || app == nil {
		return
	}
	mixedSaturationAwait(t, "POST edge peer resource owners joined", func() bool {
		s := app.agentStreamCapacity.snapshot()
		if s.RegisteredSessions != 0 || s.AdaptiveExternalBytes != 0 || s.AdaptiveExternalFDs != 0 || s.Total.InUse != 0 || s.ActiveLeases != 0 || s.Waiters != 0 || s.Granted != s.Released {
			return false
		}
		for owner := adaptiveResourceOwner(0); owner < adaptiveResourceOwnerCount; owner++ {
			if s.AdaptiveExternalReservationsByOwner[owner] != 0 {
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
	t.Log("post_replay_edges_terminal events=3 origin=2 known_oversize=1 chunked_oversize=1 pre_origin_cancel=1 retries=0 joined_agents=2 all_owners=0")
}

func postReplayEdgesAwaitIdle(t *testing.T, app *App, agents [2]*AgentConn, target publicRouteTargetConfig) {
	t.Helper()
	mixedSaturationAwait(t, "POST edge terminal request owners refunded", func() bool {
		return sustainedIdle(app, agents, [2]publicRouteTargetConfig{target, target}) && app.PublicWAF.proxyActiveRequests.Load() == 0 && app.TargetHealth.activeRequests(target.ID) == 0 && app.retryReplayBudget.used.Load() == 0
	})
	assertRequestOwnershipReservation(t, app, 0, 0)
}

func postReplayEdgesEvent(t *testing.T, app *App, total int, targetID, retryID int64, agents [2]*AgentConn, requestBytes int64, cancelled bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), mixedSaturationWait)
	defer cancel()
	mixedSaturationAwait(t, "POST edge exact terminal event count", func() bool {
		if err := app.FlushObservabilityRecorder(ctx); err != nil {
			t.Fatal(err)
		}
		var got int
		if err := app.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM proxy_request_events`).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got > total {
			t.Fatalf("events=%d exceeds %d", got, total)
		}
		return got == total
	})
	var listener, route, target, agent, retry, failed, cache, waf sql.NullInt64
	var status, read, written, retries, cacheBytes int64
	var method, host, path, kind, outcome, retryKind, cacheStatus, wafAction string
	err := app.DB.QueryRowContext(ctx, `SELECT listener_id,route_id,route_target_id,agent_id,retry_rule_id,retry_failed_agent_id,cache_rule_id,waf_rule_id,status_code,request_bytes,response_bytes,retry_count,cache_bytes,method,host,path_prefix,error_kind,retry_outcome,retry_error_kind,cache_status,waf_action FROM proxy_request_events ORDER BY id LIMIT 1 OFFSET ?`, total-1).
		Scan(&listener, &route, &target, &agent, &retry, &failed, &cache, &waf, &status, &read, &written, &retries, &cacheBytes, &method, &host, &path, &kind, &outcome, &retryKind, &cacheStatus, &wafAction)
	if err != nil {
		t.Fatal(err)
	}
	if listener != (sql.NullInt64{Int64: 1, Valid: true}) || route != (sql.NullInt64{Int64: 10, Valid: true}) || target != (sql.NullInt64{Int64: targetID, Valid: true}) || retry != (sql.NullInt64{Int64: retryID, Valid: true}) || !agent.Valid || (agent.Int64 != agents[0].AgentID && agent.Int64 != agents[1].AgentID) || method != "POST" || host != "post.example.test" || path != "/" || read != requestBytes || retries != 0 || cache.Valid || cacheStatus != publicCacheStatusBypass || cacheBytes != 0 || waf.Valid || wafAction != "" {
		t.Fatalf("POST edge attribution listener=%v route/target=%v/%v agent=%v retry=%v method/host/path=%s/%s/%s bytes=%d/%d retries=%d cache=%v/%s/%d waf=%v/%s", listener, route, target, agent, retry, method, host, path, read, requestBytes, retries, cache, cacheStatus, cacheBytes, waf, wafAction)
	}
	if cancelled {
		if status != 504 || kind != "client_cancelled" || written != 0 || outcome != "" || retryKind != "" || failed.Valid {
			t.Fatalf("cancelled edge status=%d kind=%s bytes=%d outcome=%s retry=%s failed=%v", status, kind, written, outcome, retryKind, failed)
		}
	} else if status != 503 || kind != "upstream_status_503" || written != 6 || outcome != publicRetryOutcomeSkipped || retryKind != "upstream_status_503" || failed != agent {
		t.Fatalf("oversize edge status=%d kind=%s bytes=%d outcome=%s retry=%s failed/final=%v/%v", status, kind, written, outcome, retryKind, failed, agent)
	}
}
