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

type postReplayRequest struct {
	phase   string
	chunked bool
	calls   atomic.Int64
	ready   chan struct{}
	release chan struct{}
	once    sync.Once
	done    [2]chan struct{}
}

func (r *postReplayRequest) openGate() { r.once.Do(func() { close(r.release) }) }

// The configured listener and real peers compose contracts otherwise covered
// independently by the injected buffered/unbuffered POST retry transports.
func TestPublicFeaturePostReplayWorkload(t *testing.T) {
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
			{"buffered-known", 0, 4}, {"buffered-chunked", 4, 4}, {"never", 8, 4}, {"deny", 12, 4},
		}
		var requests [16]postReplayRequest
		for _, phase := range phases {
			for id := phase.first; id < phase.first+phase.count; id++ {
				requests[id].phase = phase.name
				requests[id].chunked = phase.name == "buffered-chunked"
				requests[id].ready, requests[id].release = make(chan struct{}), make(chan struct{})
				requests[id].done = [2]chan struct{}{make(chan struct{}), make(chan struct{})}
			}
		}
		payload := bytes.Repeat([]byte{0, 1, 0x7f, 0xff}, 1024)
		stop := make(chan struct{})
		var stopOnce sync.Once
		stopOrigins := func() { stopOnce.Do(func() { close(stop) }) }
		originErrors := make(chan error, 128)
		var originCalls, originBytes, liveGates atomic.Int64
		var originWorkers sync.WaitGroup
		origin := httptest.NewUnstartedServer(nil)
		originAuthority := origin.Listener.Addr().String()
		origin.Config.ReadTimeout, origin.Config.WriteTimeout = mixedSaturationWait, mixedSaturationWait
		origin.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			originWorkers.Add(1)
			defer originWorkers.Done()
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
			if item.phase == "buffered-known" || item.phase == "buffered-chunked" {
				limit = 2
			}
			if item.phase == "deny" || attempt > limit {
				bad("denied request or extra retry reached origin")
				return
			}
			defer close(item.done[attempt-1])
			path := "/buffered"
			if item.phase == "never" {
				path = "/never"
			}
			if r.Method != "POST" || r.ProtoMajor != 1 || r.URL.RequestURI() != path || r.Host != originAuthority ||
				r.Header.Get("X-Forwarded-For") != "127.0.0.1" || r.Header.Get("X-Real-Ip") != "127.0.0.1" ||
				r.Header.Get("X-Forwarded-Host") != "post.example.test" || r.Header.Get("X-Forwarded-Proto") != "http" {
				bad("request/protocol/trusted forwarding mismatch")
				return
			}
			for _, name := range []string{"Forwarded", "Cf-Connecting-Ip", "X-Auth-Request-User", "X-Workload-Hop"} {
				if r.Header.Get(name) != "" {
					bad("spoofed identity or hop header retained: " + name)
					return
				}
			}
			body, err := io.ReadAll(io.LimitReader(r.Body, int64(len(payload)+1)))
			if err != nil || !bytes.Equal(body, payload) {
				bad("request body differs or exceeds bound")
				return
			}
			originBytes.Add(int64(len(body)))
			w.Header().Set("Connection", "close")
			w.Header().Set("Content-Type", "text/plain")
			if item.phase != "never" && attempt == 1 {
				// Each worker releases its own first attempt independently. No
				// second worker/stream must enter before this one may proceed.
				close(item.ready)
				select {
				case <-item.release:
				case <-r.Context().Done():
					return
				case <-stop:
					return
				case <-time.After(mixedSaturationWait):
					bad("first buffered origin gate timed out")
					return
				}
			}
			if attempt == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = io.WriteString(w, "retry\n")
				return
			}
			_, _ = io.WriteString(w, "ok\n")
		})
		origin.Start()
		t.Cleanup(func() {
			stopOrigins()
			origin.Close()
			done := make(chan struct{})
			go func() { originWorkers.Wait(); close(done) }()
			select {
			case <-done:
			case <-time.After(mixedSaturationWait):
				t.Error("POST origin handlers did not join")
			}
		})
		t.Cleanup(stopOrigins)
		target := f.target
		target.RouteID, target.Weight = 10, 100
		target.URL, target.ParsedURL = origin.URL, mustParseAggregateTestURL(t, origin.URL)
		target.AgentLoadBalancing = publicRouteTargetLoadBalancingRoundRobin
		target.AgentSelector = publicAgentSelectorConfig{MatchLabels: map[string]string{"post-workload": "yes"}}
		cache, retries := postReplayWorkloadRules(t, app)
		listener := publicListenerConfig{ID: 1, Name: "post-replay-workload", BindAddress: "127.0.0.1", Protocol: publicListenerProtocolHTTP, Enabled: true}
		waf := testWafRule(71, publicWafActionBlock)
		waf.Match = mustPublicPolicyMatchCEL(t, `headers["x-workload-mode"].exists(v, v == "deny")`)
		waf.Fingerprint = publicWafRuleFingerprint(waf)
		settings := defaultPublicCacheSettings()
		snapshot := &publicProxySnapshot{
			Listeners: map[int64]publicListenerConfig{1: listener},
			RoutesByListener: map[int64][]publicRouteConfig{1: {{ID: 10, ListenerID: 1, Enabled: true,
				HostPattern: "post.example.test", PathPrefix: "/", PathSecurityMode: publicRoutePathSecurityModeStrict,
				Action: publicRouteActionForward, TargetLoadBalancing: publicRouteTargetLoadBalancingRoundRobin,
				Targets: []publicRouteTargetConfig{target}}}},
			RouteTargets: map[int64]publicRouteTargetConfig{target.ID: target}, Agents: make(map[int64]publicAgentConfig),
			WafRules: []publicWafRuleConfig{waf}, RetryRules: retries,
			CacheSettings: settings, CacheRules: []publicCacheRuleConfig{cache},
			CacheFingerprint: publicCacheRuntimeFingerprint(settings, []publicCacheRuleConfig{cache}),
			// Configured identity headers are stripped even on this public route;
			// no access policy or forged client header supplies a principal.
			AccessHeaderNames: []string{"X-Auth-Request-User"},
		}
		for _, agent := range agents {
			snapshot.Agents[agent.AgentID] = publicAgentConfig{ID: agent.AgentID, PublicID: agent.PublicID, Enabled: true, Labels: map[string]string{"post-workload": "yes"}}
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
						results <- postReplayWorkloadRequest(work, client, "http://"+address, id, &requests[id], payload, app, agents, &liveGates)
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
					t.Error("POST replay workers did not join")
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
				if phase.name == "buffered-known" || phase.name == "buffered-chunked" {
					want = 2
				} else if phase.name == "deny" {
					want = 0
				}
				for attempt := int64(0); attempt < want; attempt++ {
					mixedSaturationAwaitChannel(t, "POST replay origin joined", item.done[attempt])
				}
				if item.calls.Load() != want {
					t.Fatalf("ID %d calls=%d/%d", id, item.calls.Load(), want)
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
			mixedSaturationAwait(t, "POST replay HTTP owners drained", func() bool {
				return sustainedIdle(app, agents, [2]publicRouteTargetConfig{target, target}) && app.PublicWAF.proxyActiveRequests.Load() == 0 && app.TargetHealth.activeRequests(target.ID) == 0 && app.retryReplayBudget.used.Load() == 0
			})
			postReplayWorkloadEvents(t, app, phase.name, previous, phase.count, target.ID, retries, waf.ID, agents)
			previous += phase.count
			assertPublicCacheStorageStats(t, app, 0, 0, 0)
			t.Logf("post_replay_phase name=%s requests=%d events_total=%d origin_total=%d", phase.name, phase.count, previous, origins)
		}
		if previous != 16 || origins != 20 || originBytes.Load() != 81920 || liveGates.Load() != 8 {
			t.Fatalf("POST replay totals requests/events=%d origins=%d bytes=%d gates=%d", previous, origins, originBytes.Load(), liveGates.Load())
		}
		closeRuntime()
		mixedSaturationAwait(t, "POST replay closed transports drained", func() bool {
			s := app.agentStreamCapacity.snapshot()
			return sustainedIdle(app, agents, [2]publicRouteTargetConfig{target, target}) && app.AgentTransports.len() == 0 && s.Total.InUse == 0 && s.ActiveLeases == 0 && s.Waiters == 0 && s.Granted == s.Released
		})
	})
	if !ok || app == nil {
		return
	}
	mixedSaturationAwait(t, "POST replay joined peers refund all owners", func() bool {
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
		t.Fatal("POST replay recorder retained/dropped touches")
	}
	t.Log("post_replay_terminal requests=16 events=16 origin=20 recovered=8 skipped=4 denied=4 retry_count=8 known=4 chunked=4 live_replay_gates=8 request_bytes=49152 origin_body_bytes=81920 response_bytes=80 cache_entries=0 health=0 capacity_rejected=0 joined_agents=2 all_owners=0 recorder_pending=0 recorder_dropped=0")
}

func postReplayWorkloadRequest(work context.Context, client *http.Client, base string, id int, item *postReplayRequest, payload []byte, app *App, agents [2]*AgentConn, liveGates *atomic.Int64) (resultErr error) {
	ctx, cancel := context.WithTimeout(work, mixedSaturationWait)
	defer cancel()
	path := "/buffered"
	if item.phase == "never" {
		path = "/never"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	// Chunked input has neither a known length nor a client replay callback.
	// The public server also receives no GetBody on known-length requests.
	req.GetBody = nil
	if item.chunked {
		req.ContentLength = -1
		req.TransferEncoding = []string{"chunked"}
	}
	req.Host = "post.example.test"
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
	type response struct {
		resp *http.Response
		err  error
	}
	responses, done := make(chan response, 1), make(chan struct{})
	go func() { defer close(done); resp, err := client.Do(req); responses <- response{resp, err} }()
	var outcome response
	received := false
	defer func() {
		cancel()
		item.openGate()
		// Join Do on every path, including an ownership/gate assertion error.
		select {
		case <-done:
			if !received {
				outcome = <-responses
			}
			if outcome.resp != nil && outcome.resp.Body != nil {
				ownedSaturationBodyClose(outcome.resp.Body)()
			}
		case <-time.After(mixedSaturationWait):
			resultErr = fmt.Errorf("ID %d HTTP worker did not join; prior=%v", id, resultErr)
		}
	}()
	if item.phase == "buffered-known" || item.phase == "buffered-chunked" {
		select {
		case <-item.ready:
		case outcome = <-responses:
			received = true
			return fmt.Errorf("ID %d response arrived before first-attempt body gate: %v", id, outcome.err)
		case <-ctx.Done():
			return ctx.Err()
		}
		reservation := int64(len(payload))
		if item.chunked {
			reservation = 8192
		}
		s := app.agentStreamCapacity.snapshot()
		if app.retryReplayBudget.used.Load() < reservation || s.AdaptiveExternalBytesByOwner[adaptiveResourceOwnerReplayBuffer] < reservation*3 || s.AdaptiveExternalReservationsByOwner[adaptiveResourceOwnerReplayBuffer] == 0 || s.AdaptiveExternalReservationsByOwner[adaptiveResourceOwnerPublicRequest] == 0 || agents[0].ActiveRequests.Load()+agents[1].ActiveRequests.Load() == 0 {
			return fmt.Errorf("ID %d first consumed body lost live replay/HTTP ownership", id)
		}
		liveGates.Add(1)
		item.openGate()
	}
	select {
	case outcome = <-responses:
		received = true
	case <-ctx.Done():
		return ctx.Err()
	}
	if outcome.err != nil {
		return outcome.err
	}
	status, want := http.StatusOK, []byte("ok\n")
	if item.phase == "never" {
		status, want = 503, []byte("retry\n")
	} else if item.phase == "deny" {
		status, want = 403, []byte("blocked\n")
	}
	resp := outcome.resp
	if resp.ProtoMajor != 1 || resp.StatusCode != status || resp.Header.Get("X-P2pstream-Cache") != "" {
		return fmt.Errorf("ID %d protocol=%s status=%d/%d cache=%q", id, resp.Proto, resp.StatusCode, status, resp.Header.Get("X-P2pstream-Cache"))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(len(want)+1)))
	if err != nil || !bytes.Equal(body, want) {
		return fmt.Errorf("ID %d response body=%q equal=%v err=%v", id, body, bytes.Equal(body, want), err)
	}
	return work.Err()
}

func postReplayWorkloadRules(t *testing.T, app *App) (publicCacheRuleConfig, []publicRetryRuleConfig) {
	t.Helper()
	match, err := json.Marshal(mustPublicPolicyMatchCEL(t, `method == "POST" && host_match(host, "post.example.test")`))
	if err != nil {
		t.Fatal(err)
	}
	cacheRow, err := app.DB.CreatePublicCacheRule(t.Context(), db.CreatePublicCacheRuleParams{
		Name: "post-replay", Priority: 10, Enabled: 1, MatchJson: string(match), RouteIdsJson: "[]", TargetIdsJson: "[]",
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
	var retries []publicRetryRuleConfig
	for index, path := range []string{"/buffered", "/never"} {
		mode, maxBody := p2pstreamv1.PublicRetryBodyMode_PUBLIC_RETRY_BODY_MODE_BUFFERED, int64(8192)
		if path == "/never" {
			mode, maxBody = p2pstreamv1.PublicRetryBodyMode_PUBLIC_RETRY_BODY_MODE_NEVER, 0
		}
		// POST status retries can duplicate side effects. The operator-style
		// acknowledgement is explicit; this fixture performs no real mutation.
		input, err := app.validatePublicRetryRuleInput(t.Context(), "post-replay-"+strconv.Itoa(index), int64(10+index), true, []string{"POST"}, 1,
			p2pstreamv1.PublicRetryFailureMode_PUBLIC_RETRY_FAILURE_MODE_CONNECTION_FAILURES,
			mode, maxBody, p2pstreamv1.PublicRetryResponseBodyMode_PUBLIC_RETRY_RESPONSE_BODY_MODE_STREAM, 0, 0,
			[]int64{503}, nil, nil, publicPolicyMatchRuleToProto(mustPublicPolicyMatchCEL(t, `path == "`+path+`"`)), true)
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
		retries = append(retries, retry)
	}
	return cache, retries
}

func postReplayWorkloadEvents(t *testing.T, app *App, phase string, previous, count int, targetID int64, retryRules []publicRetryRuleConfig, wafID int64, agents [2]*AgentConn) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), mixedSaturationWait)
	defer cancel()
	mixedSaturationAwait(t, "POST replay exact terminal events", func() bool {
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
		if listener != (sql.NullInt64{Int64: 1, Valid: true}) || method != "POST" || host != "post.example.test" || cacheBytes != 0 {
			t.Fatalf("phase %s invalid common attribution listener=%v method=%s host=%s request/cache bytes=%d/%d", phase, listener, method, host, requestBytes, cacheBytes)
		}
		if phase == "deny" {
			if requestBytes != 0 || status != 403 || kind != "waf_blocked" || waf != (sql.NullInt64{Int64: wafID, Valid: true}) || wafAction != publicWafActionBlock || route.Valid || target.Valid || agent.Valid || cache.Valid || cacheStatus != "" || retry.Valid || retries != 0 || outcome != "" || retryKind != "" || failed.Valid || responseBytes != 8 || prefix != "/..." {
				t.Fatalf("denied event escaped WAF boundary: status=%d kind=%s route=%v target=%v agent=%v cache=%v/%s retry=%v/%d/%s/%s failed=%v bytes=%d prefix=%s", status, kind, route, target, agent, cache, cacheStatus, retry, retries, outcome, retryKind, failed, responseBytes, prefix)
			}
			continue
		}
		statusWant, bytesWant, kindWant, retryID := int64(200), int64(3), "", retryRules[0].ID
		if phase == "never" {
			statusWant, bytesWant, kindWant, retryID = 503, 6, "upstream_status_503", retryRules[1].ID
		}
		if status != statusWant || kind != kindWant || requestBytes != 4096 || responseBytes != bytesWant || route != (sql.NullInt64{Int64: 10, Valid: true}) || target != (sql.NullInt64{Int64: targetID, Valid: true}) || !isAgent(agent) || retry != (sql.NullInt64{Int64: retryID, Valid: true}) || waf.Valid || wafAction != "" || prefix != "/" || cache.Valid || cacheStatus != publicCacheStatusBypass {
			t.Fatalf("phase %s forwarded attribution status=%d/%d kind=%s bytes=%d/%d route=%v target=%v agent=%v retry=%v cache=%v/%s", phase, status, statusWant, kind, requestBytes, responseBytes, route, target, agent, retry, cache, cacheStatus)
		}
		// Status retry records the first authenticated agent in both cases.
		// ReplaySkippedReason is not persisted in proxy_request_events: source
		// fixes the consumed-body branch, while this oracle proves one attempt.
		wantOutcome, wantRetries := publicRetryOutcomeRecovered, int64(1)
		if phase == "never" {
			wantOutcome, wantRetries = publicRetryOutcomeSkipped, 0
		}
		if retries != wantRetries || outcome != wantOutcome || retryKind != "upstream_status_503" || !isAgent(failed) || (phase == "never") != (failed == agent) {
			t.Fatalf("phase %s retry=%d/%s/%s failed/final=%v/%v", phase, retries, outcome, retryKind, failed, agent)
		}
	}
	if err := rows.Err(); err != nil || seen != count {
		t.Fatalf("phase %s rows=%d/%d err=%v", phase, seen, count, err)
	}
}
