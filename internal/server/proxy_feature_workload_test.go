package server

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const featureWorkloadBodyBytes = 16 << 10

type featureWorkloadPhase struct {
	name, variant, credential, cacheHeader, cacheStatus string
	requests, concurrency, originCalls                  int
	denied                                              bool
}

// A finite composition workload, not a throughput benchmark or saturation test.
// WAF must precede an already populated cache under repeated concurrent traffic.
func TestPublicFeatureCacheWAFWorkload(t *testing.T) {
	rounds := 1
	if value := os.Getenv("P2PSTREAM_FEATURE_WORKLOAD_ROUNDS"); value != "" {
		if value != "4" {
			t.Fatal("P2PSTREAM_FEATURE_WORKLOAD_ROUNDS must be empty or 4")
		}
		rounds = 4
	}
	app, resolution, closeDB := newTestPublicCacheApp(t)
	defer closeDB()
	installDeterministicServerResourceController(t, app)
	app.publicProxyRequests = newRequestCapacityLimiter(32, 32)
	app.publicTargetRequests = newKeyedRequestCapacityLimiter(32, 32)
	app.publicClientRequests = newKeyedStringRequestCapacityLimiter(32)
	var originCalls atomic.Int64
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		originCalls.Add(1)
		if r.Method != http.MethodGet || r.ProtoMajor != 1 || r.URL.Path != "/assets/app.txt" ||
			r.Header.Get("X-Forwarded-For") != "127.0.0.1" || r.Header.Get("X-Real-Ip") != "127.0.0.1" {
			http.Error(w, "unexpected origin request", http.StatusBadRequest)
			return
		}
		credential := "public"
		if r.Header.Get("Cookie") != "" {
			credential = "cookie"
		} else if r.Header.Get("Authorization") != "" {
			credential = "authorization"
		}
		body := featureWorkloadBody(r.URL.Query().Get("v"), credential)
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		// Deliberately cache-eligible even for credential requests: request-side
		// bypass, rather than a private response directive, must protect them.
		w.Header().Set("Cache-Control", "public, max-age=300")
		_, _ = w.Write(body)
	}))
	defer origin.Close()
	parsedOrigin, err := url.Parse(origin.URL)
	if err != nil {
		t.Fatal(err)
	}
	resolution.Target.URL, resolution.Target.ParsedURL = origin.URL, parsedOrigin
	resolution.Target.Weight = 100
	resolution.Route = publicRouteConfig{ID: resolution.Route.ID, ListenerID: 1, Enabled: true,
		HostPattern: "assets.example.test", PathPrefix: "/assets", Action: publicRouteActionForward,
		PathSecurityMode: publicRoutePathSecurityModeStrict, Targets: []publicRouteTargetConfig{resolution.Target}}
	listener := publicListenerConfig{ID: 1, Name: "feature-workload", BindAddress: "127.0.0.1", Protocol: publicListenerProtocolHTTP, Enabled: true}
	waf := testWafRule(71, publicWafActionBlock)
	waf.Match = mustPublicPolicyMatchCEL(t, `headers["x-feature-mode"].exists(v, v == "deny")`)
	waf.Fingerprint = publicWafRuleFingerprint(waf)
	snapshot := *app.currentPublicSnapshot()
	snapshot.Listeners = map[int64]publicListenerConfig{1: listener}
	snapshot.RoutesByListener = map[int64][]publicRouteConfig{1: {resolution.Route}}
	snapshot.RouteTargets = map[int64]publicRouteTargetConfig{resolution.Target.ID: resolution.Target}
	snapshot.WafRules = []publicWafRuleConfig{waf}
	setPublicSnapshotForTest(t, app, &snapshot)
	app.PublicWAF.reconcile(&snapshot)
	if _, err := app.startPublicListenerFromSnapshot(listener, &snapshot); err != nil {
		t.Fatal(err)
	}
	app.proxyMu.Lock()
	address := app.publicListenerState[1].BoundAddress
	app.proxyMu.Unlock()
	if address == "" {
		t.Fatal("public listener did not bind")
	}
	transport := &http.Transport{Proxy: nil, DisableCompression: true, MaxIdleConns: 8, MaxIdleConnsPerHost: 8}
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	closed := false
	closeRuntime := func() {
		if closed {
			return
		}
		closed = true
		transport.CloseIdleConnections()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := app.stopPublicListenerRuntime(ctx, 1); err != nil {
			t.Error(err)
		}
		app.DirectTransports.closeAll()
		if err := app.CloseObservabilityRecorder(ctx); err != nil {
			t.Error(err)
		}
	}
	defer closeRuntime()
	events, origins := 0, 0
	for round := 0; round < rounds; round++ {
		warm, cold := fmt.Sprintf("warm-%d", round), fmt.Sprintf("cold-%d", round)
		phases := []featureWorkloadPhase{
			{name: "prime", variant: warm, requests: 1, concurrency: 1, originCalls: 1, cacheHeader: "MISS", cacheStatus: publicCacheStatusStored},
			{name: "hits", variant: warm, requests: 128, concurrency: 8, cacheHeader: "HIT", cacheStatus: publicCacheStatusHit},
			{name: "deny-cached", variant: warm, requests: 128, concurrency: 8, denied: true},
			{name: "recovery-hits", variant: warm, requests: 128, concurrency: 8, cacheHeader: "HIT", cacheStatus: publicCacheStatusHit},
			{name: "cookie-warm", variant: warm, credential: "cookie", requests: 1, concurrency: 1, originCalls: 1, cacheStatus: publicCacheStatusBypass},
			{name: "authorization-warm", variant: warm, credential: "authorization", requests: 1, concurrency: 1, originCalls: 1, cacheStatus: publicCacheStatusBypass},
			{name: "public-still-public", variant: warm, requests: 1, concurrency: 1, cacheHeader: "HIT", cacheStatus: publicCacheStatusHit},
			{name: "cookie-cold", variant: cold, credential: "cookie", requests: 1, concurrency: 1, originCalls: 1, cacheStatus: publicCacheStatusBypass},
			{name: "public-cold-miss", variant: cold, requests: 1, concurrency: 1, originCalls: 1, cacheHeader: "MISS", cacheStatus: publicCacheStatusStored},
			{name: "public-cold-hit", variant: cold, requests: 1, concurrency: 1, cacheHeader: "HIT", cacheStatus: publicCacheStatusHit},
		}
		for _, phase := range phases {
			started := time.Now()
			errors := make(chan error, phase.requests)
			var workers sync.WaitGroup
			for range phase.concurrency {
				workers.Go(func() {
					for range phase.requests / phase.concurrency {
						errors <- featureWorkloadRequest(t.Context(), client, "http://"+address, phase)
					}
				})
			}
			workers.Wait()
			close(errors)
			for err := range errors {
				if err != nil {
					t.Fatalf("round %d phase %s: %v", round, phase.name, err)
				}
			}
			origins += phase.originCalls
			if got := originCalls.Load(); got != int64(origins) {
				t.Fatalf("phase %s origin calls=%d, want %d", phase.name, got, origins)
			}
			featureWorkloadAwaitIdle(t, app, false)
			featureWorkloadEvents(t, app, phase, events, resolution.Target.ID, resolution.CacheRuleID, waf.ID)
			events += phase.requests
			t.Logf("feature_phase round=%d name=%s requests=%d concurrency=%d origin_total=%d events_total=%d elapsed_ms=%d", round, phase.name, phase.requests, phase.concurrency, origins, events, time.Since(started).Milliseconds())
		}
	}
	assertPublicCacheStorageStats(t, app, int64(rounds*2*featureWorkloadBodyBytes), int64(rounds*2*featureWorkloadBodyBytes), int64(rounds*2))
	closeRuntime()
	featureWorkloadAwaitIdle(t, app, true)
	recorder := app.observabilityRecorder
	if recorder == nil || recorder.pendingEvents.Load() != 0 || recorder.pendingTouches.Load() != 0 ||
		recorder.droppedEvents.Load() != 0 || recorder.droppedTouches.Load() != 0 || len(recorder.events) != 0 || len(recorder.touches) != 0 {
		t.Fatal("terminal recorder retained or dropped work")
	}
	if events != 391*rounds || origins != 5*rounds {
		t.Fatalf("workload totals events=%d origins=%d", events, origins)
	}
	t.Logf("feature_terminal rounds=%d requests=%d events=%d origin_calls=%d cache_hits=%d cache_stored=%d cache_bypass=%d waf_denied=%d response_bytes=%d all_ledger_owners=0 recorder_pending=0 recorder_dropped=0", rounds, events, events, origins, 258*rounds, 2*rounds, 3*rounds, 128*rounds, (263*featureWorkloadBodyBytes+128*len("blocked\n"))*rounds)
}

func featureWorkloadBody(variant, credential string) []byte {
	seed := []byte("feature-asset:" + variant + ":" + credential + "|")
	return bytes.Repeat(seed, featureWorkloadBodyBytes/len(seed)+1)[:featureWorkloadBodyBytes]
}

func featureWorkloadRequest(ctx context.Context, client *http.Client, base string, phase featureWorkloadPhase) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/assets/app.txt?v="+url.QueryEscape(phase.variant), nil)
	if err != nil {
		return err
	}
	req.Host = "assets.example.test"
	req.Header.Set("X-Feature-Mode", "allow")
	req.Header.Set("X-Forwarded-For", "198.51.100.91")
	req.Header.Set("X-Real-Ip", "198.51.100.92")
	credential := "public"
	if phase.credential == "cookie" {
		req.Header.Set("Cookie", "feature_session=fixture-private")
		credential = "cookie"
	} else if phase.credential == "authorization" {
		req.Header.Set("Authorization", "Bearer fixture-private")
		credential = "authorization"
	}
	wantStatus, wantBody := http.StatusOK, featureWorkloadBody(phase.variant, credential)
	if phase.denied {
		req.Header.Set("X-Feature-Mode", "deny")
		wantStatus, wantBody = http.StatusForbidden, []byte("blocked\n")
	}
	response, err := client.Do(req)
	if err != nil {
		return err
	}
	body, readErr := io.ReadAll(io.LimitReader(response.Body, featureWorkloadBodyBytes+1))
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil {
		return fmt.Errorf("read=%v close=%v", readErr, closeErr)
	}
	if response.ProtoMajor != 1 || response.StatusCode != wantStatus || !bytes.Equal(body, wantBody) || response.Header.Get("X-P2pstream-Cache") != phase.cacheHeader {
		return fmt.Errorf("response proto=%s status=%d body_bytes=%d body_equal=%v cache=%q", response.Proto, response.StatusCode, len(body), bytes.Equal(body, wantBody), response.Header.Get("X-P2pstream-Cache"))
	}
	return nil
}

func featureWorkloadAwaitIdle(t *testing.T, app *App, terminal bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		snapshot := app.agentStreamCapacity.snapshot()
		idle := len(app.publicProxyRequests.slots) == 0 && app.PublicWAF.proxyActiveRequests.Load() == 0 && app.TargetHealth.activeRequests(30) == 0
		app.publicClientRequests.mu.Lock()
		idle = idle && len(app.publicClientRequests.inUse) == 0
		app.publicClientRequests.mu.Unlock()
		app.publicTargetRequests.mu.Lock()
		for _, limiter := range app.publicTargetRequests.entries {
			idle = idle && len(limiter.slots) == 0
		}
		app.publicTargetRequests.mu.Unlock()
		for owner := adaptiveResourceOwner(0); owner < adaptiveResourceOwnerCount; owner++ {
			if terminal || owner == adaptiveResourceOwnerPublicRequest || owner == adaptiveResourceOwnerPublicResponse {
				idle = idle && snapshot.AdaptiveExternalBytesByOwner[owner] == 0 && snapshot.AdaptiveExternalFDsByOwner[owner] == 0 && snapshot.AdaptiveExternalReservationsByOwner[owner] == 0
			}
		}
		if idle {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("ownership did not drain terminal=%v ledger=%+v", terminal, snapshot)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func featureWorkloadEvents(t *testing.T, app *App, phase featureWorkloadPhase, previous int, targetID, cacheID, wafID int64) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := app.FlushObservabilityRecorder(ctx); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := app.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM proxy_request_events`).Scan(&count); err != nil || count != previous+phase.requests {
		t.Fatalf("phase %s event count=%d want=%d err=%v", phase.name, count, previous+phase.requests, err)
	}
	rows, err := app.DB.QueryContext(ctx, `SELECT status_code,error_kind,method,host,path_prefix,listener_id,route_id,route_target_id,agent_id,waf_rule_id,waf_action,request_bytes,response_bytes,cache_rule_id,cache_status,cache_bytes FROM proxy_request_events ORDER BY id LIMIT -1 OFFSET ?`, previous)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var status int
		var kind, method, host, prefix, action, cacheStatus string
		var listener, route, target, agent, waf, cache sql.NullInt64
		var requestBytes, responseBytes, cacheBytes int64
		if err := rows.Scan(&status, &kind, &method, &host, &prefix, &listener, &route, &target, &agent, &waf, &action, &requestBytes, &responseBytes, &cache, &cacheStatus, &cacheBytes); err != nil {
			t.Fatal(err)
		}
		if listener != (sql.NullInt64{Int64: 1, Valid: true}) || method != "GET" || host != "assets.example.test" || requestBytes != 0 || agent.Valid || cacheStatus != phase.cacheStatus {
			t.Fatalf("phase %s attribution listener=%v method=%s host=%s request_bytes=%d agent=%v cache_status=%s", phase.name, listener, method, host, requestBytes, agent, cacheStatus)
		}
		if phase.denied {
			if status != 403 || kind != "waf_blocked" || action != publicWafActionBlock || waf != (sql.NullInt64{Int64: wafID, Valid: true}) || route.Valid || target.Valid || cache.Valid || cacheBytes != 0 || responseBytes != int64(len("blocked\n")) || prefix != "/..." {
				t.Fatalf("WAF event violated pre-cache denial contract: status=%d kind=%s action=%s waf=%v route=%v target=%v cache=%v bytes=%d/%d prefix=%s", status, kind, action, waf, route, target, cache, responseBytes, cacheBytes, prefix)
			}
		} else {
			wantCache := sql.NullInt64{}
			wantCacheBytes := int64(0)
			if phase.cacheStatus != publicCacheStatusBypass {
				wantCache, wantCacheBytes = sql.NullInt64{Int64: cacheID, Valid: true}, featureWorkloadBodyBytes
			}
			if status != 200 || kind != "" || action != "" || waf.Valid || route != (sql.NullInt64{Int64: 10, Valid: true}) || target != (sql.NullInt64{Int64: targetID, Valid: true}) || prefix != "/assets" || responseBytes != featureWorkloadBodyBytes || cache != wantCache || cacheBytes != wantCacheBytes {
				t.Fatalf("phase %s success event status=%d kind=%s action=%s waf=%v route=%v target=%v prefix=%s response_bytes=%d cache=%v cache_bytes=%d", phase.name, status, kind, action, waf, route, target, prefix, responseBytes, cache, cacheBytes)
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}
