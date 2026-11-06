package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/internal/db"
)

type retrySSERequest struct {
	phase    string
	calls    atomic.Int64
	canceled atomic.Bool
	tail     chan struct{}
	done     [2]chan struct{}
}

// The configured listener and real peers compose contracts otherwise covered
// independently by the injected retry transports and standalone upgrade proxy.
func TestPublicFeatureRetrySSEWorkload(t *testing.T) {
	var app *App
	var agents [2]*AgentConn
	ok := t.Run("workload", func(t *testing.T) {
		f := newPeerFairnessFixture(t)
		app = f.app
		agents = [2]*AgentConn{f.agent, mixedSaturationAddAgent(t, f)}
		budget := app.agentStreamCapacity.snapshot()
		if budget.Total.Capacity != 3 || budget.Public.Capacity != 2 || budget.Pooled.Capacity != 1 || budget.Control.Capacity != 1 {
			t.Fatalf("unexpected two-worker fixture envelope: %+v", budget)
		}
		app.publicProxyRequests = newRequestCapacityLimiter(8, 8)
		app.publicTargetRequests = newKeyedRequestCapacityLimiter(8, 8)
		app.publicClientRequests = newKeyedStringRequestCapacityLimiter(8)
		app.PublicCache = newPublicProxyCache(t.TempDir())
		work, cancelWork := context.WithTimeout(t.Context(), 45*time.Second)
		t.Cleanup(cancelWork)
		phases := []struct {
			name         string
			first, count int
		}{
			{"recovery", 0, 16}, {"sse-complete", 16, 8}, {"sse-cancel", 24, 8}, {"deny", 32, 8},
		}
		var requests [40]retrySSERequest
		for _, phase := range phases {
			for id := phase.first; id < phase.first+phase.count; id++ {
				requests[id].phase = phase.name
				requests[id].tail = make(chan struct{})
				requests[id].done = [2]chan struct{}{make(chan struct{}), make(chan struct{})}
			}
		}
		prefix := append([]byte("data: "), bytes.Repeat([]byte{'x'}, 4088)...)
		prefix = append(prefix, '\n', '\n')
		tail, recovered := []byte("data: done\n\n"), bytes.Repeat([]byte{'r'}, 4096)
		stop := make(chan struct{})
		var stopOnce sync.Once
		stopOrigins := func() { stopOnce.Do(func() { close(stop) }) }
		originErrors := make(chan error, 128)
		var originCalls atomic.Int64
		origin := httptest.NewUnstartedServer(nil)
		originAuthority := origin.Listener.Addr().String()
		origin.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			originCalls.Add(1)
			bad := func(message string) {
				originErrors <- fmt.Errorf("origin: %s", message)
				http.Error(w, message, http.StatusInternalServerError)
			}
			id, err := strconv.Atoi(r.Header.Get("X-Workload-Id"))
			if err != nil || id < 0 || id >= len(requests) {
				bad("invalid untrusted fixture ID")
				return
			}
			item := &requests[id]
			attempt := item.calls.Add(1)
			limit := int64(1)
			if item.phase == "recovery" {
				limit = 2
			}
			if item.phase == "deny" || attempt > limit {
				bad("denied request or extra retry reached origin")
				return
			}
			defer close(item.done[attempt-1])
			path := "/stream/feed"
			if item.phase == "recovery" {
				path = "/retry/read"
			}
			if r.Method != "GET" || r.ProtoMajor != 1 || r.URL.RequestURI() != path || r.Host != originAuthority ||
				r.Header.Get("X-Forwarded-For") != "127.0.0.1" || r.Header.Get("X-Real-Ip") != "127.0.0.1" ||
				r.Header.Get("X-Forwarded-Host") != "retry.example.test" || r.Header.Get("X-Forwarded-Proto") != "http" {
				bad("request/protocol/trusted forwarding mismatch")
				return
			}
			for _, name := range []string{"Forwarded", "Cf-Connecting-Ip", "X-Auth-Request-User", "X-Workload-Hop"} {
				if r.Header.Get(name) != "" {
					bad("spoofed identity or hop header retained: " + name)
					return
				}
			}
			// Completed H1 responses retire physical streams, independent of the
			// HTTP ownership checks; no idle slot is counted as active HTTP work.
			w.Header().Set("Connection", "close")
			if item.phase == "recovery" {
				w.Header().Set("Content-Type", "application/octet-stream")
				if attempt == 1 {
					w.WriteHeader(http.StatusServiceUnavailable)
					_, _ = io.WriteString(w, "retry\n")
					return
				}
				w.Header().Set("Content-Length", strconv.Itoa(len(recovered)))
				_, _ = w.Write(recovered)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "public, max-age=300")
			if _, err := w.Write(prefix); err != nil {
				bad("write SSE prefix: " + err.Error())
				return
			}
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
				item.canceled.Store(true)
			case <-stop:
			case <-item.tail:
				_, _ = w.Write(tail)
			}
		})
		origin.Start()
		t.Cleanup(origin.Close)
		t.Cleanup(stopOrigins)
		target := f.target
		target.RouteID, target.Weight = 10, 100
		target.URL, target.ParsedURL = origin.URL, mustParseAggregateTestURL(t, origin.URL)
		target.AgentLoadBalancing = publicRouteTargetLoadBalancingRoundRobin
		target.AgentSelector = publicAgentSelectorConfig{MatchLabels: map[string]string{"retry-workload": "yes"}}
		cache, retry := retrySSEWorkloadRules(t, app)
		listener := publicListenerConfig{ID: 1, Name: "retry-sse-workload", BindAddress: "127.0.0.1", Protocol: publicListenerProtocolHTTP, Enabled: true}
		waf := testWafRule(71, publicWafActionBlock)
		waf.Match = mustPublicPolicyMatchCEL(t, `headers["x-workload-mode"].exists(v, v == "deny")`)
		waf.Fingerprint = publicWafRuleFingerprint(waf)
		settings := defaultPublicCacheSettings()
		snapshot := &publicProxySnapshot{
			Listeners: map[int64]publicListenerConfig{1: listener},
			RoutesByListener: map[int64][]publicRouteConfig{1: {{ID: 10, ListenerID: 1, Enabled: true,
				HostPattern: "retry.example.test", PathPrefix: "/", PathSecurityMode: publicRoutePathSecurityModeStrict,
				Action: publicRouteActionForward, TargetLoadBalancing: publicRouteTargetLoadBalancingRoundRobin,
				Targets: []publicRouteTargetConfig{target}}}},
			RouteTargets: map[int64]publicRouteTargetConfig{target.ID: target}, Agents: make(map[int64]publicAgentConfig),
			WafRules: []publicWafRuleConfig{waf}, RetryRules: []publicRetryRuleConfig{retry},
			CacheSettings: settings, CacheRules: []publicCacheRuleConfig{cache},
			CacheFingerprint: publicCacheRuntimeFingerprint(settings, []publicCacheRuleConfig{cache}),
			// Configured identity headers are stripped even on this public route;
			// no access policy or forged client header supplies a principal.
			AccessHeaderNames: []string{"X-Auth-Request-User"},
		}
		for _, agent := range agents {
			snapshot.Agents[agent.AgentID] = publicAgentConfig{ID: agent.AgentID, PublicID: agent.PublicID, Enabled: true, Labels: map[string]string{"retry-workload": "yes"}}
		}
		setPublicSnapshotForTest(t, app, snapshot)
		app.PublicCache.reconcile(settings, snapshot.CacheRules)
		app.PublicWAF.reconcile(snapshot)
		app.TargetHealth.reconcile(app, snapshot, false)
		if _, err := app.startPublicListenerFromSnapshot(listener, snapshot); err != nil {
			t.Fatal(err)
		}
		app.proxyMu.Lock()
		address := app.publicListenerState[1].BoundAddress
		app.proxyMu.Unlock()
		transport := &http.Transport{Proxy: nil, DisableCompression: true, MaxIdleConns: 2, MaxIdleConnsPerHost: 2}
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
		previous, origins := 0, int64(0)
		for _, phase := range phases {
			jobs, results := make(chan int, phase.count), make(chan error, phase.count)
			for id := phase.first; id < phase.first+phase.count; id++ {
				jobs <- id
			}
			close(jobs)
			var workers sync.WaitGroup
			for range 2 {
				workers.Go(func() {
					for id := range jobs {
						if work.Err() != nil {
							return
						}
						results <- retrySSEWorkloadRequest(work, client, "http://"+address, id, &requests[id], prefix, tail, recovered, app, agents)
					}
				})
			}
			done := make(chan struct{})
			go func() { workers.Wait(); close(done) }()
			// On any assertion failure, cancel and join before shutting the
			// listener, origin, peers or database. Completed phases join at once.
			t.Cleanup(func() {
				cancelWork()
				stopOrigins()
				select {
				case <-done:
				case <-time.After(mixedSaturationWait):
					t.Error("retry/SSE workers did not join")
				}
			})
			select {
			case <-done:
			case <-work.Done():
				t.Fatal(work.Err())
			}
			close(results)
			if len(results) != phase.count {
				t.Fatalf("phase %s returned %d/%d requests", phase.name, len(results), phase.count)
			}
			for err := range results {
				if err != nil {
					t.Fatalf("phase %s: %v", phase.name, err)
				}
			}
			for id := phase.first; id < phase.first+phase.count; id++ {
				item := &requests[id]
				want := int64(1)
				if phase.name == "recovery" {
					want = 2
				} else if phase.name == "deny" {
					want = 0
				}
				for attempt := int64(0); attempt < want; attempt++ {
					mixedSaturationAwaitChannel(t, "retry/SSE origin joined", item.done[attempt])
				}
				if item.calls.Load() != want || item.canceled.Load() != (phase.name == "sse-cancel") {
					t.Fatalf("ID %d calls=%d/%d canceled=%v", id, item.calls.Load(), want, item.canceled.Load())
				}
				origins += want
			}
			select {
			case err := <-originErrors:
				t.Fatal(err)
			default:
			}
			if originCalls.Load() != origins {
				t.Fatalf("phase %s origin calls=%d want=%d", phase.name, originCalls.Load(), origins)
			}
			mixedSaturationAwait(t, "retry/SSE HTTP owners drained", func() bool {
				return sustainedIdle(app, agents, [2]publicRouteTargetConfig{target, target}) && app.PublicWAF.proxyActiveRequests.Load() == 0 && app.TargetHealth.activeRequests(target.ID) == 0 && app.retryReplayBudget.used.Load() == 0
			})
			retrySSEWorkloadEvents(t, app, phase.name, previous, phase.count, target.ID, cache.ID, retry.ID, waf.ID, agents)
			previous += phase.count
			assertPublicCacheStorageStats(t, app, 0, 0, 0)
			t.Logf("retry_sse_phase name=%s requests=%d events_total=%d origin_total=%d", phase.name, phase.count, previous, origins)
		}
		if previous != 40 || origins != 48 {
			t.Fatalf("retry/SSE totals requests/events=%d origins=%d", previous, origins)
		}
		closeRuntime()
		mixedSaturationAwait(t, "retry/SSE closed transports drained", func() bool {
			s := app.agentStreamCapacity.snapshot()
			return sustainedIdle(app, agents, [2]publicRouteTargetConfig{target, target}) && app.AgentTransports.len() == 0 && s.Total.InUse == 0 && s.ActiveLeases == 0 && s.Waiters == 0 && s.Granted == s.Released
		})
	})
	if !ok || app == nil {
		return
	}
	mixedSaturationAwait(t, "retry/SSE joined peers refund all owners", func() bool {
		s := app.agentStreamCapacity.snapshot()
		if s.RegisteredSessions != 0 || s.Total.InUse != 0 || s.ActiveLeases != 0 || s.Waiters != 0 || s.Granted != s.Released || s.AdaptiveExternalBytes != 0 || s.AdaptiveExternalFDs != 0 {
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
	r := app.observabilityRecorder
	if r.pendingTouches.Load() != 0 || r.droppedTouches.Load() != 0 || len(r.touches) != 0 {
		t.Fatal("retry/SSE recorder retained/dropped touches")
	}
	t.Log("retry_sse_terminal requests=40 events=40 origin=48 completed=24 canceled=8 denied=8 recovered=16 retry_count=16 cache_entries=0 response_bytes=131232 health=0 capacity_rejected=0 joined_agents=2 all_owners=0 recorder_pending=0 recorder_dropped=0")
}

func retrySSEWorkloadRequest(work context.Context, client *http.Client, base string, id int, item *retrySSERequest, prefix, tail, recovered []byte, app *App, agents [2]*AgentConn) error {
	ctx, cancel := context.WithTimeout(work, mixedSaturationWait)
	defer cancel()
	path := "/stream/feed"
	if item.phase == "recovery" {
		path = "/retry/read"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
	if err != nil {
		return err
	}
	req.Host = "retry.example.test"
	req.Header.Set("X-Workload-Id", strconv.Itoa(id))
	req.Header.Set("X-Workload-Mode", "allow")
	if item.phase == "deny" {
		req.Header.Set("X-Workload-Mode", "deny")
	}
	for _, name := range []string{"Forwarded", "X-Forwarded-For", "X-Real-Ip", "Cf-Connecting-Ip"} {
		req.Header.Set(name, "198.51.100.91")
	}
	req.Header.Set("X-Auth-Request-User", "forged-principal")
	req.Header.Set("Connection", "X-Workload-Hop")
	req.Header.Set("X-Workload-Hop", "remove-me")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	closeBody := ownedSaturationBodyClose(resp.Body)
	defer closeBody()
	status, want, cacheHeader := http.StatusOK, recovered, ""
	if item.phase == "deny" {
		status, want = http.StatusForbidden, []byte("blocked\n")
	} else if item.phase != "recovery" {
		cacheHeader = "MISS"
	}
	if resp.ProtoMajor != 1 || resp.StatusCode != status || resp.Header.Get("X-P2pstream-Cache") != cacheHeader {
		return fmt.Errorf("ID %d protocol=%s status=%d cache=%q", id, resp.Proto, resp.StatusCode, resp.Header.Get("X-P2pstream-Cache"))
	}
	if item.phase == "recovery" || item.phase == "deny" {
		payload, err := io.ReadAll(io.LimitReader(resp.Body, int64(len(want)+1)))
		if err != nil || !bytes.Equal(payload, want) {
			return fmt.Errorf("ID %d complete body=%d equal=%v err=%v", id, len(payload), bytes.Equal(payload, want), err)
		}
		return nil
	}
	if resp.ContentLength != -1 || resp.Header.Get("Content-Type") != "text/event-stream" {
		return fmt.Errorf("ID %d SSE length/type=%d/%q", id, resp.ContentLength, resp.Header.Get("Content-Type"))
	}
	payload := make([]byte, len(prefix))
	if _, err := io.ReadFull(resp.Body, payload); err != nil || !bytes.Equal(payload, prefix) {
		return fmt.Errorf("ID %d flushed prefix equal=%v err=%v", id, bytes.Equal(payload, prefix), err)
	}
	// The tail cannot exist yet. Buffering to EOF would have timed out before
	// this point; physical peer slots remain distinct from these HTTP owners.
	s := app.agentStreamCapacity.snapshot()
	if s.AdaptiveExternalReservationsByOwner[adaptiveResourceOwnerPublicResponse] == 0 || agents[0].ActiveRequests.Load()+agents[1].ActiveRequests.Load() == 0 || app.retryReplayBudget.used.Load() != 0 {
		return fmt.Errorf("ID %d held SSE lost HTTP ownership or retained replay memory", id)
	}
	select {
	case <-item.done[0]:
		return fmt.Errorf("ID %d SSE origin completed before gate", id)
	default:
	}
	if item.phase == "sse-cancel" {
		cancel()
		closeBody()
		return nil
	}
	close(item.tail)
	payload, err = io.ReadAll(io.LimitReader(resp.Body, int64(len(tail)+1)))
	if err != nil || !bytes.Equal(payload, tail) {
		return fmt.Errorf("ID %d SSE tail=%q err=%v", id, payload, err)
	}
	return nil
}

func retrySSEWorkloadRules(t *testing.T, app *App) (publicCacheRuleConfig, publicRetryRuleConfig) {
	t.Helper()
	match, err := json.Marshal(mustPublicPolicyMatchCEL(t, `method == "GET" && host_match(host, "retry.example.test") && path == "/stream/feed"`))
	if err != nil {
		t.Fatal(err)
	}
	cacheRow, err := app.DB.CreatePublicCacheRule(t.Context(), db.CreatePublicCacheRuleParams{
		Name: "retry-sse", Priority: 10, Enabled: 1, MatchJson: string(match), RouteIdsJson: "[]", TargetIdsJson: "[]",
		Scope: publicCacheScopeSelectedBackend, TtlMode: publicCacheTTLModeFixed, TtlMillis: 300000,
		QueryMode: publicCacheQueryModeFull, QueryParamsJson: "[]", VaryHeadersJson: "[]", CacheStatusCodesJson: "[200]",
		MaxObjectBytes: 8192, AddCacheStatusHeader: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	cache, err := publicCacheRuleRowToConfig(cacheRow)
	if err != nil {
		t.Fatal(err)
	}
	// Exercise normal validation and row conversion; status/buffered retries
	// require duplicate-risk acknowledgement even for these bodyless GETs.
	input, err := app.validatePublicRetryRuleInput(t.Context(), "retry-sse", 10, true, []string{"GET"}, 1,
		p2pstreamv1.PublicRetryFailureMode_PUBLIC_RETRY_FAILURE_MODE_CONNECTION_FAILURES,
		p2pstreamv1.PublicRetryBodyMode_PUBLIC_RETRY_BODY_MODE_NEVER, 0,
		p2pstreamv1.PublicRetryResponseBodyMode_PUBLIC_RETRY_RESPONSE_BODY_MODE_BUFFERED, 8192, 1000,
		[]int64{503}, nil, nil, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	row, err := app.DB.CreatePublicRetryRule(t.Context(), db.CreatePublicRetryRuleParams{
		Name: input.Name, Priority: input.Priority, Enabled: input.Enabled, MethodsJson: input.MethodsJSON,
		MaxRetries: input.MaxRetries, FailureMode: input.FailureMode, BodyMode: input.BodyMode, MaxReplayBodyBytes: input.MaxReplayBodyBytes,
		RetryStatusCodesJson: input.RetryStatusJSON, RouteIdsJson: input.RouteIDsJSON, TargetIdsJson: input.TargetIDsJSON, MatchJson: input.MatchJSON,
		ResponseBodyMode: input.ResponseBodyMode, MaxBufferedResponseBodyBytes: input.MaxBufferedResponseBodyBytes,
		MaxBufferedResponseWaitMillis: input.MaxBufferedResponseWaitMillis,
	})
	if err != nil {
		t.Fatal(err)
	}
	retry, err := publicRetryRuleRowToConfig(row)
	if err != nil {
		t.Fatal(err)
	}
	return cache, retry
}

func retrySSEWorkloadEvents(t *testing.T, app *App, phase string, previous, count int, targetID, cacheID, retryID, wafID int64, agents [2]*AgentConn) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), mixedSaturationWait)
	defer cancel()
	mixedSaturationAwait(t, "retry/SSE exact terminal events", func() bool {
		if err := app.FlushObservabilityRecorder(ctx); err != nil {
			t.Fatal(err)
		}
		var got int
		if err := app.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM proxy_request_events`).Scan(&got); err != nil || got > previous+count {
			t.Fatalf("events=%d want=%d err=%v", got, previous+count, err)
		}
		return got == previous+count
	})
	rows, err := app.DB.QueryContext(ctx, `SELECT listener_id,route_id,route_target_id,agent_id,status_code,error_kind,method,host,path_prefix,request_bytes,response_bytes,cache_rule_id,cache_status,cache_bytes,waf_rule_id,waf_action,retry_rule_id,retry_count,retry_outcome,retry_error_kind,retry_failed_agent_id FROM proxy_request_events ORDER BY id LIMIT -1 OFFSET ?`, previous)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	isAgent := func(id sql.NullInt64) bool {
		return id.Valid && (id.Int64 == agents[0].AgentID || id.Int64 == agents[1].AgentID)
	}
	seen := 0
	for rows.Next() {
		var listener, route, target, agent, cache, waf, retry, failed sql.NullInt64
		var status, requestBytes, responseBytes, cacheBytes, retries int64
		var kind, method, host, prefix, cacheStatus, wafAction, outcome, retryKind string
		if err := rows.Scan(&listener, &route, &target, &agent, &status, &kind, &method, &host, &prefix, &requestBytes, &responseBytes, &cache, &cacheStatus, &cacheBytes, &waf, &wafAction, &retry, &retries, &outcome, &retryKind, &failed); err != nil {
			t.Fatal(err)
		}
		seen++
		if listener != (sql.NullInt64{Int64: 1, Valid: true}) || method != "GET" || host != "retry.example.test" || requestBytes != 0 || cacheBytes != 0 {
			t.Fatalf("phase %s invalid common attribution listener=%v method=%s host=%s request/cache bytes=%d/%d", phase, listener, method, host, requestBytes, cacheBytes)
		}
		if phase == "deny" {
			if status != 403 || kind != "waf_blocked" || waf != (sql.NullInt64{Int64: wafID, Valid: true}) || wafAction != publicWafActionBlock || route.Valid || target.Valid || agent.Valid || cache.Valid || cacheStatus != "" || retry.Valid || retries != 0 || outcome != "" || retryKind != "" || failed.Valid || responseBytes != 8 || prefix != "/..." {
				t.Fatalf("denied event escaped WAF boundary: status=%d kind=%s route=%v target=%v agent=%v cache=%v/%s retry=%v/%d/%s/%s failed=%v bytes=%d prefix=%s", status, kind, route, target, agent, cache, cacheStatus, retry, retries, outcome, retryKind, failed, responseBytes, prefix)
			}
			continue
		}
		if status != 200 || kind != "" || route != (sql.NullInt64{Int64: 10, Valid: true}) || target != (sql.NullInt64{Int64: targetID, Valid: true}) || !isAgent(agent) || retry != (sql.NullInt64{Int64: retryID, Valid: true}) || waf.Valid || wafAction != "" || prefix != "/" {
			t.Fatalf("phase %s invalid forwarded attribution status=%d kind=%s route=%v target=%v agent=%v retry=%v waf=%v/%s prefix=%s", phase, status, kind, route, target, agent, retry, waf, wafAction, prefix)
		}
		if phase == "recovery" {
			if cache.Valid || cacheStatus != publicCacheStatusBypass || retries != 1 || outcome != publicRetryOutcomeRecovered || retryKind != "upstream_status_503" || !isAgent(failed) || failed == agent || responseBytes != 4096 {
				t.Fatalf("recovery event cache=%v/%s retry=%d/%s/%s failed/final=%v/%v bytes=%d", cache, cacheStatus, retries, outcome, retryKind, failed, agent, responseBytes)
			}
		} else {
			wantBytes := int64(4096)
			if phase == "sse-complete" {
				wantBytes += 12
			}
			// MIME exclusion does not mutate the cache miss decision; canceled
			// request contexts do not become upstream retry/body-failure errors.
			if cache != (sql.NullInt64{Int64: cacheID, Valid: true}) || cacheStatus != publicCacheStatusMiss || retries != 0 || outcome != publicRetryOutcomeSkipped || retryKind != "" || failed.Valid || responseBytes != wantBytes {
				t.Fatalf("phase %s cache=%v/%s retry=%d/%s/%s failed=%v bytes=%d/%d", phase, cache, cacheStatus, retries, outcome, retryKind, failed, responseBytes, wantBytes)
			}
		}
	}
	if err := rows.Err(); err != nil || seen != count {
		t.Fatalf("phase %s rows=%d/%d err=%v", phase, seen, count, err)
	}
}
