package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/internal/config"
	"p2pstream/internal/db"
	"p2pstream/stats"
)

func TestGetDashboardIncludesCacheSummary(t *testing.T) {
	ctx := context.Background()
	app := NewApp(nil, newServerTestDB(t))
	header := createTestAdminSession(t, app)

	insertEvent := func(cacheStatus string, cacheBytes int64) {
		t.Helper()
		if err := app.insertProxyRequestEventWithRollups(ctx, db.InsertProxyRequestEventAtParams{
			OccurredAt:   time.Now().UTC(),
			StatusCode:   http.StatusOK,
			DurationMs:   10,
			CacheStatus:  cacheStatus,
			CacheBytes:   cacheBytes,
			RequestBytes: 0,
		}); err != nil {
			t.Fatalf("insert proxy request event: %v", err)
		}
	}

	insertEvent(publicCacheStatusHit, 100)
	insertEvent(publicCacheStatusHit, 200)
	insertEvent(publicCacheStatusMiss, 0)
	insertEvent(publicCacheStatusStored, 300)
	insertEvent(publicCacheStatusStoreFailed, 0)
	insertEvent(publicCacheStatusBypass, 0)
	insertEvent("", 0)

	req := connect.NewRequest(&p2pstreamv1.GetDashboardRequest{})
	req.Header().Set("Cookie", header.Get("Cookie"))
	resp, err := app.GetDashboard(ctx, req)
	if err != nil {
		t.Fatalf("get dashboard: %v", err)
	}

	window := dashboardTestWindow(t, resp.Msg.Windows, "5m")
	if window.ProxyRequests != 7 {
		t.Fatalf("proxy requests = %d, want 7", window.ProxyRequests)
	}
	if window.ProxyCacheHits != 2 {
		t.Fatalf("proxy cache hits = %d, want 2", window.ProxyCacheHits)
	}
	if window.ProxyCacheMisses != 3 {
		t.Fatalf("proxy cache misses = %d, want 3", window.ProxyCacheMisses)
	}
	if window.ProxyCacheBypasses != 1 {
		t.Fatalf("proxy cache bypasses = %d, want 1", window.ProxyCacheBypasses)
	}
	if window.ProxyCacheStored != 1 {
		t.Fatalf("proxy cache stored = %d, want 1", window.ProxyCacheStored)
	}
	if window.ProxyCacheStoreFailed != 1 {
		t.Fatalf("proxy cache store failed = %d, want 1", window.ProxyCacheStoreFailed)
	}
	if window.ProxyCacheHitBytes != 300 {
		t.Fatalf("proxy cache hit bytes = %d, want 300", window.ProxyCacheHitBytes)
	}
	if window.ProxyCacheStoredBytes != 300 {
		t.Fatalf("proxy cache stored bytes = %d, want 300", window.ProxyCacheStoredBytes)
	}
}

func TestProxyRequestContextPreservesRequestPathForDefaultRoute(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "http://example.test/private/value", nil)
	resolution := publicRouteResolution{
		DefaultRoute: true,
		Route: publicRouteConfig{
			PathPrefix: "/api",
		},
	}

	got := proxyRequestContextFromResolution(req, resolution)
	if got.PathPrefix != "/..." {
		t.Fatalf("default route path prefix = %q, want request-derived redaction", got.PathPrefix)
	}

	resolution.DefaultRoute = false
	got = proxyRequestContextFromResolution(req, resolution)
	if got.PathPrefix != "/api" {
		t.Fatalf("matched route path prefix = %q, want /api", got.PathPrefix)
	}
}

func TestGetDashboardDiagnosticsRequiresAuth(t *testing.T) {
	ctx := context.Background()
	app := NewApp(nil, newServerTestDB(t))

	req := connect.NewRequest(&p2pstreamv1.GetDashboardDiagnosticsRequest{})
	if _, err := app.GetDashboardDiagnostics(ctx, req); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("GetDashboardDiagnostics error code = %s, want unauthenticated: %v", connect.CodeOf(err), err)
	}
}

func TestGetDashboardDiagnosticsAggregatesStatusesFailuresAndSamples(t *testing.T) {
	ctx := context.Background()
	app := NewApp(nil, newServerTestDB(t))
	header := createTestAdminSession(t, app)
	seedDashboardRollupDimensionFixtures(t, app.DB)
	insertDashboardRollupAgentFixture(t, app.DB, 10)

	now := time.Now().UTC()
	insertDiagnosticsProxyEvent(t, app, now.Add(-10*time.Minute), http.StatusOK, "", proxyRequestContext{Method: "GET", Host: "example.test", PathPrefix: "/ok"}, sqlNullInt64(1), sqlNullInt64(1), sqlNullInt64(1), sqlNullInt64(10), 10, 100, 25)
	insertDiagnosticsProxyEvent(t, app, now.Add(-9*time.Minute), http.StatusNotFound, "no_route", proxyRequestContext{Method: "GET", Host: "example.test", PathPrefix: "/api/users/..."}, sqlNullInt64(1), sql.NullInt64{}, sql.NullInt64{}, sql.NullInt64{}, 11, 101, 35)
	insertDiagnosticsProxyEvent(t, app, now.Add(-8*time.Minute), http.StatusTooManyRequests, "", proxyRequestContext{Method: "POST", Host: "example.test", PathPrefix: "/api/rate"}, sqlNullInt64(1), sqlNullInt64(1), sql.NullInt64{}, sql.NullInt64{}, 12, 102, 45)
	insertDiagnosticsProxyEvent(t, app, now.Add(-7*time.Minute), http.StatusBadGateway, "direct_proxy_failed", proxyRequestContext{Method: "GET", Host: "example.test", PathPrefix: "/api/proxy"}, sqlNullInt64(1), sqlNullInt64(1), sqlNullInt64(1), sql.NullInt64{}, 13, 103, 55)
	insertDiagnosticsProxyEvent(t, app, now.Add(-6*time.Minute), http.StatusGatewayTimeout, "agent_dial_timeout", proxyRequestContext{}, sqlNullInt64(1), sqlNullInt64(1), sqlNullInt64(1), sqlNullInt64(10), 14, 104, 65)

	req := connect.NewRequest(&p2pstreamv1.GetDashboardDiagnosticsRequest{
		WindowLabel: "bogus",
		SampleLimit: 500,
	})
	req.Header().Set("Cookie", header.Get("Cookie"))
	resp, err := app.GetDashboardDiagnostics(ctx, req)
	if err != nil {
		t.Fatalf("get diagnostics: %v", err)
	}

	if resp.Msg.Label != "1h" {
		t.Fatalf("diagnostics label = %q, want 1h", resp.Msg.Label)
	}
	outcome := resp.Msg.Outcome
	if outcome == nil {
		t.Fatal("missing diagnostics outcome")
	}
	if outcome.Requests != 5 || outcome.Success != 1 || outcome.ClientError != 2 || outcome.ServerError != 2 {
		t.Fatalf("unexpected outcome status counts: %+v", outcome)
	}
	if outcome.NonSuccess != 4 || outcome.ProxyFailure != 3 {
		t.Fatalf("non-success/proxy failure = %d/%d, want 4/3", outcome.NonSuccess, outcome.ProxyFailure)
	}

	statuses := diagnosticsStatusCounts(resp.Msg.StatusCodes)
	for _, status := range []int64{200, 404, 429, 502, 504} {
		if statuses[status] != 1 {
			t.Fatalf("status %d count = %d, want 1 in %+v", status, statuses[status], resp.Msg.StatusCodes)
		}
	}
	for _, status := range resp.Msg.StatusCodes {
		if status.StatusCode == http.StatusNotFound && status.ProxyFailure != 1 {
			t.Fatalf("404 proxy failures = %d, want 1", status.ProxyFailure)
		}
	}
	if !dashboardDimensionHasLabel(resp.Msg.ErrorKinds, "no_route") || !dashboardDimensionHasLabel(resp.Msg.ErrorKinds, "direct_proxy_failed") {
		t.Fatalf("diagnostics error kinds missing expected labels: %+v", resp.Msg.ErrorKinds)
	}
	if !dashboardDimensionHasLabel(resp.Msg.ProblemListeners, "listener-one") ||
		!dashboardDimensionHasLabel(resp.Msg.ProblemRoutes, "example.com /api") ||
		!dashboardDimensionHasLabel(resp.Msg.ProblemRouteTargets, "target-one") ||
		!dashboardDimensionHasLabel(resp.Msg.ProblemAgents, "agent-10") {
		t.Fatalf("diagnostics problem dimensions missing expected labels: listeners=%+v routes=%+v targets=%+v agents=%+v", resp.Msg.ProblemListeners, resp.Msg.ProblemRoutes, resp.Msg.ProblemRouteTargets, resp.Msg.ProblemAgents)
	}

	if len(resp.Msg.RecentSamples) != 4 {
		t.Fatalf("recent sample count = %d, want 4", len(resp.Msg.RecentSamples))
	}
	var sawNoRoute, sawBlankContext bool
	for _, sample := range resp.Msg.RecentSamples {
		if sample.StatusCode < 400 && sample.ErrorKind == "" {
			t.Fatalf("sample includes success without proxy failure: %+v", sample)
		}
		if sample.StatusCode == http.StatusNotFound && sample.ErrorKind == "no_route" {
			sawNoRoute = true
			if sample.Method != "GET" || sample.Host != "example.test" || sample.PathPrefix != "/api/users/..." {
				t.Fatalf("no_route sample context = method %q host %q path %q", sample.Method, sample.Host, sample.PathPrefix)
			}
			if sample.ListenerLabel != "listener-one" {
				t.Fatalf("no_route sample listener = %q, want listener-one", sample.ListenerLabel)
			}
		}
		if sample.StatusCode == http.StatusGatewayTimeout {
			sawBlankContext = sample.Method == "" && sample.Host == "" && sample.PathPrefix == ""
		}
	}
	if !sawNoRoute || !sawBlankContext {
		t.Fatalf("recent samples missing no_route or blank-context row: %+v", resp.Msg.RecentSamples)
	}
}

func TestDashboardDiagnosticsSampleLimitClamp(t *testing.T) {
	if got := dashboardDiagnosticsSampleLimit(0); got != diagnosticsDefaultSampleLimit {
		t.Fatalf("default sample limit = %d, want %d", got, diagnosticsDefaultSampleLimit)
	}
	if got := dashboardDiagnosticsSampleLimit(500); got != diagnosticsMaxSampleLimit {
		t.Fatalf("max sample limit = %d, want %d", got, diagnosticsMaxSampleLimit)
	}
	if got := dashboardDiagnosticsSampleLimit(12); got != 12 {
		t.Fatalf("sample limit = %d, want 12", got)
	}
}

func TestRedactedProxyPathPrefix(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{path: "", want: "/"},
		{path: "/", want: "/"},
		{path: "/api?token=secret", want: "/..."},
		{path: "/api/users", want: "/..."},
		{path: "/api/users/123/token", want: "/..."},
		{path: "api/users/123", want: "/..."},
	}
	for _, tt := range tests {
		if got := redactedProxyPathPrefix(tt.path); got != tt.want {
			t.Fatalf("redactedProxyPathPrefix(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}

func TestProxyRequestEventRollupsAreWrittenWithRawEvent(t *testing.T) {
	ctx := context.Background()
	app := NewApp(nil, newServerTestDB(t))
	insertDashboardRollupAgentFixture(t, app.DB, 10)

	app.recordProxyRequestEventWithCache(
		ctx,
		http.StatusGatewayTimeout,
		1500*time.Millisecond,
		"agent_timeout",
		sqlNullInt64(7),
		sqlNullInt64(9),
		sql.NullInt64{},
		"",
		sqlNullInt64(10),
		sql.NullInt64{},
		publicCacheStatusHit,
		123,
		40,
		400,
	)
	if err := app.flushObservabilityRecorder(ctx); err != nil {
		t.Fatalf("flush observability recorder: %v", err)
	}

	var rawEvents int64
	if err := app.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM proxy_request_events`).Scan(&rawEvents); err != nil {
		t.Fatalf("count raw proxy events: %v", err)
	}
	if rawEvents != 1 {
		t.Fatalf("raw proxy events = %d, want 1", rawEvents)
	}

	var requests, serverError, internalError, slowRequests, cacheHits, cacheHitBytes int64
	if err := app.DB.QueryRowContext(ctx, `
		SELECT requests, server_error, internal_error, slow_requests, cache_hits, cache_hit_bytes
		FROM proxy_request_rollup_minutes
	`).Scan(&requests, &serverError, &internalError, &slowRequests, &cacheHits, &cacheHitBytes); err != nil {
		t.Fatalf("read proxy rollup: %v", err)
	}
	if requests != 1 || serverError != 1 || internalError != 1 || slowRequests != 1 || cacheHits != 1 || cacheHitBytes != 123 {
		t.Fatalf("unexpected proxy rollup metrics: requests=%d server=%d internal=%d slow=%d cache_hits=%d cache_hit_bytes=%d", requests, serverError, internalError, slowRequests, cacheHits, cacheHitBytes)
	}

	var listenerID, routeID, agentID, statusClass int64
	var errorKind string
	if err := app.DB.QueryRowContext(ctx, `
		SELECT listener_id, route_id, agent_id, error_kind, status_class
		FROM proxy_request_tuple_rollup_minutes
	`).Scan(&listenerID, &routeID, &agentID, &errorKind, &statusClass); err != nil {
		t.Fatalf("read proxy tuple rollup: %v", err)
	}
	if listenerID != 7 || routeID != 9 || agentID != 10 || errorKind != "agent_timeout" || statusClass != 5 {
		t.Fatalf("unexpected tuple rollup dimensions: listener=%d route=%d agent=%d error=%q status_class=%d", listenerID, routeID, agentID, errorKind, statusClass)
	}

	var statusCode int64
	if err := app.DB.QueryRowContext(ctx, `
		SELECT status_code, requests, server_error, internal_error
		FROM proxy_request_status_rollup_minutes
	`).Scan(&statusCode, &requests, &serverError, &internalError); err != nil {
		t.Fatalf("read proxy status rollup: %v", err)
	}
	if statusCode != http.StatusGatewayTimeout || requests != 1 || serverError != 1 || internalError != 1 {
		t.Fatalf("unexpected status rollup: status=%d requests=%d server=%d internal=%d", statusCode, requests, serverError, internalError)
	}
}

func TestObservabilityRecorderFlushesQueuedEventsAsAggregatedRollups(t *testing.T) {
	ctx := context.Background()
	app := NewApp(nil, newServerTestDB(t))
	seedDashboardRollupDimensionFixtures(t, app.DB)

	for _, statusCode := range []int{http.StatusOK, http.StatusCreated, http.StatusBadGateway} {
		app.recordProxyRequestEventWithIDsAndContext(
			ctx,
			statusCode,
			time.Duration(statusCode)*time.Millisecond,
			"",
			sqlNullInt64(1),
			sqlNullInt64(1),
			sqlNullInt64(1),
			1,
			2,
			proxyRequestContext{Method: "GET", Host: "example.test", PathPrefix: "/api"},
		)
	}
	if err := app.flushObservabilityRecorder(ctx); err != nil {
		t.Fatalf("flush observability recorder: %v", err)
	}

	var rawEvents int64
	if err := app.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM proxy_request_events`).Scan(&rawEvents); err != nil {
		t.Fatalf("count raw proxy events: %v", err)
	}
	if rawEvents != 3 {
		t.Fatalf("raw proxy events = %d, want 3", rawEvents)
	}

	var requests, success, serverError, durationMsSum, requestBytes, responseBytes int64
	if err := app.DB.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(requests), 0), COALESCE(SUM(success), 0), COALESCE(SUM(server_error), 0),
		       COALESCE(SUM(duration_ms_sum), 0), COALESCE(SUM(request_bytes), 0), COALESCE(SUM(response_bytes), 0)
		FROM proxy_request_rollup_minutes
	`).Scan(&requests, &success, &serverError, &durationMsSum, &requestBytes, &responseBytes); err != nil {
		t.Fatalf("read proxy rollup totals: %v", err)
	}
	if requests != 3 || success != 2 || serverError != 1 || durationMsSum != 903 || requestBytes != 3 || responseBytes != 6 {
		t.Fatalf("unexpected rollup totals: requests=%d success=%d server=%d duration=%d request_bytes=%d response_bytes=%d", requests, success, serverError, durationMsSum, requestBytes, responseBytes)
	}

	var tupleRows, tupleRequests int64
	if err := app.DB.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(SUM(requests), 0)
		FROM proxy_request_tuple_rollup_minutes
		WHERE listener_id = 1 AND route_id = 1 AND agent_id = 1 AND error_kind = '' AND status_class = 2
	`).Scan(&tupleRows, &tupleRequests); err != nil {
		t.Fatalf("read tuple rollups: %v", err)
	}
	if tupleRows != 1 || tupleRequests != 2 {
		t.Fatalf("tuple rollup rows/requests = %d/%d, want 1/2", tupleRows, tupleRequests)
	}
}

func TestObservabilityRecorderCoalescesPublicCacheTouches(t *testing.T) {
	ctx := context.Background()
	app := NewApp(nil, newServerTestDB(t))
	keyDigest := "cache-touch-key"
	storedAt := time.Now().UTC().Round(0)
	if _, err := app.DB.ExecContext(ctx, `INSERT INTO public_cache_rules (id, name) VALUES (1, 'cache-touch-rule')`); err != nil {
		t.Fatalf("insert cache rule: %v", err)
	}
	if _, err := app.DB.ExecContext(ctx, `
		INSERT INTO public_cache_entries (
			key_digest, rule_id, scope, listener_protocol, host, path, query_key, method,
			vary_headers_json, response_headers_json, status_code, body_path, size_bytes, stored_at, expires_at
		) VALUES (?, 1, 'selected_backend', 'http', 'example.test', '/asset', '', 'GET', '[]', '{}', 200, '/tmp/body', 10, ?, ?)
	`, keyDigest, storedAt, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("insert cache entry: %v", err)
	}

	recorder := app.observabilityRecorderService()
	firstAccess := storedAt.Add(time.Second)
	recorder.touchPublicCacheEntry(keyDigest, storedAt, firstAccess)
	recorder.touchPublicCacheEntry(keyDigest, storedAt, firstAccess.Add(time.Second))
	recorder.touchPublicCacheEntry(keyDigest, storedAt, firstAccess.Add(2*time.Second))
	if err := app.flushObservabilityRecorder(ctx); err != nil {
		t.Fatalf("flush observability recorder: %v", err)
	}

	var hitCount int64
	var lastAccessedAt time.Time
	if err := app.DB.QueryRowContext(ctx, `SELECT hit_count, last_accessed_at FROM public_cache_entries WHERE key_digest = ?`, keyDigest).Scan(&hitCount, &lastAccessedAt); err != nil {
		t.Fatalf("read cache hit count: %v", err)
	}
	if hitCount != 3 {
		t.Fatalf("cache hit count = %d, want all 3 coalesced hits", hitCount)
	}
	if want := firstAccess.Add(2 * time.Second); !lastAccessedAt.Equal(want) {
		t.Fatalf("cache last access = %s, want latest hit %s", lastAccessedAt, want)
	}
}

func TestObservabilityRecorderPreservesLatestCacheAccessAcrossPrunedState(t *testing.T) {
	ctx := context.Background()
	app := NewApp(nil, newServerTestDB(t))
	keyDigest := "out-of-order-cache-touch"
	storedAt := time.Unix(1_700_000_000, 123_456_789).UTC()
	olderAccess := storedAt.Add(time.Second)
	newerAccess := storedAt.Add(2 * time.Second)
	if _, err := app.DB.ExecContext(ctx, `INSERT INTO public_cache_rules (id, name) VALUES (1, 'out-of-order-cache-rule')`); err != nil {
		t.Fatalf("insert cache rule: %v", err)
	}
	if _, err := app.DB.ExecContext(ctx, `
		INSERT INTO public_cache_entries (
			key_digest, rule_id, scope, listener_protocol, host, path, query_key, method,
			vary_headers_json, response_headers_json, status_code, body_path, size_bytes,
			stored_at, expires_at, last_accessed_at
		) VALUES (?, 1, 'selected_backend', 'http', 'example.test', '/asset', '', 'GET',
			'[]', '{}', 200, '/tmp/body', 10, ?, ?, ?)
	`, keyDigest, storedAt, storedAt.Add(time.Hour), storedAt); err != nil {
		t.Fatalf("insert cache entry: %v", err)
	}

	recorder := newObservabilityRecorder(app)
	touches := make(map[publicCacheTouchKey]*publicCacheTouchState)
	batch := make([]db.InsertProxyRequestEventAtParams, 0)
	recorder.queuePublicCacheTouch(touches, newPublicCacheTouch(keyDigest, storedAt, newerAccess))
	firstFlushAt := newerAccess.Add(time.Second)
	if err := recorder.flushBatch(ctx, &batch, touches, firstFlushAt, true); err != nil {
		t.Fatalf("flush newer cache touch: %v", err)
	}

	assertEntry := func(wantHits int64, wantAccess time.Time) {
		t.Helper()
		var hitCount int64
		var lastAccessedAt time.Time
		if err := app.DB.QueryRowContext(ctx, `
			SELECT hit_count, last_accessed_at
			FROM public_cache_entries
			WHERE key_digest = ?
		`, keyDigest).Scan(&hitCount, &lastAccessedAt); err != nil {
			t.Fatalf("read cache touch state: %v", err)
		}
		if hitCount != wantHits || !lastAccessedAt.Equal(wantAccess) {
			t.Fatalf("cache touch state = hits %d/access %s, want %d/%s", hitCount, lastAccessedAt, wantHits, wantAccess)
		}
	}
	assertEntry(1, newerAccess)

	recorder.prunePublicCacheTouches(touches, firstFlushAt.Add(publicCacheTouchCoalesceInterval))
	if len(touches) != 0 || recorder.pendingTouches.Load() != 0 {
		t.Fatalf("flushed cache touch state was not pruned: len=%d pending=%d", len(touches), recorder.pendingTouches.Load())
	}

	recorder.queuePublicCacheTouch(touches, newPublicCacheTouch(keyDigest, storedAt, olderAccess))
	if err := recorder.flushBatch(ctx, &batch, touches, firstFlushAt.Add(publicCacheTouchCoalesceInterval), true); err != nil {
		t.Fatalf("flush older cache touch: %v", err)
	}
	assertEntry(2, newerAccess)
}

func TestObservabilityRecorderTouchesLegacySQLiteTimestampGeneration(t *testing.T) {
	ctx := context.Background()
	app := NewApp(nil, newServerTestDB(t))
	keyDigest := "legacy-sqlite-cache-touch"
	if _, err := app.DB.ExecContext(ctx, `INSERT INTO public_cache_rules (id, name) VALUES (1, 'legacy-cache-rule')`); err != nil {
		t.Fatalf("insert cache rule: %v", err)
	}
	if _, err := app.DB.ExecContext(ctx, `
		INSERT INTO public_cache_entries (
			key_digest, rule_id, scope, listener_protocol, host, path, query_key, method,
			vary_headers_json, response_headers_json, status_code, body_path, size_bytes, expires_at
		) VALUES (?, 1, 'selected_backend', 'http', 'example.test', '/legacy', '', 'GET',
			'[]', '{}', 200, '/tmp/legacy-body', 10, datetime('now', '+1 hour'))
	`, keyDigest); err != nil {
		t.Fatalf("insert legacy cache entry: %v", err)
	}

	var storedText string
	var storedLength int
	if err := app.DB.QueryRowContext(ctx, `
		SELECT CAST(stored_at AS TEXT), length(stored_at)
		FROM public_cache_entries
		WHERE key_digest = ?
	`, keyDigest).Scan(&storedText, &storedLength); err != nil {
		t.Fatalf("read raw legacy stored_at: %v", err)
	}
	if storedLength != len("2006-01-02 15:04:05") {
		t.Fatalf("legacy stored_at = %q (length %d), want SQLite CURRENT_TIMESTAMP format", storedText, storedLength)
	}
	entry, err := app.DB.GetPublicCacheEntry(ctx, keyDigest)
	if err != nil {
		t.Fatalf("load legacy cache entry: %v", err)
	}
	accessedAt := entry.StoredAt.Add(time.Second)
	recorder := newObservabilityRecorder(app)
	touches := make(map[publicCacheTouchKey]*publicCacheTouchState)
	recorder.queuePublicCacheTouch(touches, newPublicCacheTouch(keyDigest, entry.StoredAt, accessedAt))
	batch := make([]db.InsertProxyRequestEventAtParams, 0)
	if err := recorder.flushBatch(ctx, &batch, touches, accessedAt, true); err != nil {
		t.Fatalf("flush legacy cache touch: %v", err)
	}

	updated, err := app.DB.GetPublicCacheEntry(ctx, keyDigest)
	if err != nil {
		t.Fatalf("reload legacy cache entry: %v", err)
	}
	if updated.HitCount != 1 || !updated.LastAccessedAt.Equal(accessedAt) {
		t.Fatalf("legacy cache touch = hits %d/access %s, want 1/%s", updated.HitCount, updated.LastAccessedAt, accessedAt)
	}
}

func TestObservabilityRecorderBoundsPendingEventsWhenDatabaseFails(t *testing.T) {
	database := newServerTestDB(t)
	app := NewApp(nil, database)
	recorder := app.observabilityRecorderService()
	flushFailure := errors.New("injected observability flush failure")
	failFlush := true
	insertBatch := recorder.insertBatch
	recorder.insertBatch = func(ctx context.Context, events []db.InsertProxyRequestEventAtParams, touches []db.TouchPublicCacheEntryParams) error {
		if failFlush {
			return flushFailure
		}
		return insertBatch(ctx, events, touches)
	}

	for i := 0; i < observabilityRecorderMaxBatch; i++ {
		recorder.recordProxyRequestEvent(context.Background(), proxyRequestEvent{StatusCode: http.StatusOK})
	}
	deadline := time.Now().Add(2 * time.Second)
	for recorder.pendingEvents.Load() < observabilityRecorderMaxBatch && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	for i := 0; i < observabilityRecorderQueueSize+2048; i++ {
		recorder.recordProxyRequestEvent(context.Background(), proxyRequestEvent{StatusCode: http.StatusOK})
	}

	pending := recorder.pendingEvents.Load()
	queued := len(recorder.events)
	if pending != observabilityRecorderMaxBatch {
		t.Fatalf("pending event batch = %d, want hard cap %d", pending, observabilityRecorderMaxBatch)
	}
	if queued != observabilityRecorderQueueSize {
		t.Fatalf("queued events = %d, want bounded queue filled to %d", queued, observabilityRecorderQueueSize)
	}
	if got := pending + int64(queued); got != observabilityRecorderMaxBatch+observabilityRecorderQueueSize {
		t.Fatalf("retained events = %d, want %d", got, observabilityRecorderMaxBatch+observabilityRecorderQueueSize)
	}
	if recorder.droppedEvents.Load() == 0 {
		t.Fatal("expected excess events to be dropped")
	}

	time.Sleep(2 * observabilityRecorderFlushInterval)
	if got := recorder.pendingEvents.Load(); got != pending {
		t.Fatalf("pending batch grew across failed retries: got %d, want %d", got, pending)
	}
	if got := len(recorder.events); got != queued {
		t.Fatalf("bounded queue changed across failed retries: got %d, want %d", got, queued)
	}

	closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	if err := recorder.close(closeCtx); !errors.Is(err, flushFailure) {
		t.Fatalf("close error = %v, want injected flush failure", err)
	}
	cancel()
	failFlush = false
	retryCtx, retryCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer retryCancel()
	if err := recorder.close(retryCtx); err != nil {
		t.Fatalf("retry close after database recovery: %v", err)
	}
}

func TestObservabilityRecorderRateLimitsDropLogs(t *testing.T) {
	recorder := newObservabilityRecorder(nil)
	recorder.dropEvent()
	nextLog := recorder.nextDropLog.Load()
	if nextLog <= time.Now().UnixNano() {
		t.Fatalf("next drop log deadline = %d, want a future deadline", nextLog)
	}
	for i := 0; i < 100; i++ {
		recorder.dropEvent()
		recorder.dropTouch()
	}
	if got := recorder.nextDropLog.Load(); got != nextLog {
		t.Fatalf("drop log deadline changed under amplification: got %d, want %d", got, nextLog)
	}
	if got := recorder.droppedEvents.Load(); got != 101 {
		t.Fatalf("dropped events = %d, want 101", got)
	}
	if got := recorder.droppedTouches.Load(); got != 100 {
		t.Fatalf("dropped touches = %d, want 100", got)
	}
}

func TestObservabilityRecorderBoundsAndPrunesCacheTouchState(t *testing.T) {
	recorder := newObservabilityRecorder(nil)
	recorder.nextDropLog.Store(time.Now().Add(time.Hour).UnixNano())
	touches := make(map[publicCacheTouchKey]*publicCacheTouchState)
	storedAt := time.Unix(1_700_000_000, 0).UTC()
	for i := 0; i < observabilityRecorderMaxTouchKeys; i++ {
		recorder.queuePublicCacheTouch(touches, newPublicCacheTouch(fmt.Sprintf("key-%d", i), storedAt, storedAt.Add(time.Second)))
	}
	keyZero := newPublicCacheTouch("key-0", storedAt, storedAt.Add(2*time.Second))
	recorder.queuePublicCacheTouch(touches, keyZero)
	if got := touches[keyZero.key].hitCount; got != 2 {
		t.Fatalf("existing cache key hit count = %d, want 2", got)
	}
	recorder.queuePublicCacheTouch(touches, newPublicCacheTouch("overflow", storedAt, storedAt.Add(time.Second)))
	if got := len(touches); got != observabilityRecorderMaxTouchKeys {
		t.Fatalf("cache touch state cardinality = %d, want cap %d", got, observabilityRecorderMaxTouchKeys)
	}
	if got := recorder.droppedTouches.Load(); got != 1 {
		t.Fatalf("dropped cache touches = %d, want 1", got)
	}

	now := time.Now().UTC()
	for _, touch := range touches {
		touch.hitCount = 0
		touch.lastFlushedAt = now.Add(-publicCacheTouchCoalesceInterval)
	}
	recorder.prunePublicCacheTouches(touches, now)
	if len(touches) != 0 || recorder.pendingTouches.Load() != 0 {
		t.Fatalf("expired cache touch state was not pruned: len=%d pending=%d", len(touches), recorder.pendingTouches.Load())
	}
}

func TestObservabilityRecorderCoalescesCacheTouchesPerGeneration(t *testing.T) {
	recorder := newObservabilityRecorder(nil)
	touches := make(map[publicCacheTouchKey]*publicCacheTouchState)
	storedAtA := time.Unix(1_700_000_000, 1).UTC()
	storedAtB := storedAtA.Add(time.Nanosecond)
	keyA := newPublicCacheTouch("same-digest", storedAtA, storedAtA.Add(time.Second))
	keyB := newPublicCacheTouch("same-digest", storedAtB, storedAtB.Add(time.Second))

	recorder.queuePublicCacheTouch(touches, keyA)
	recorder.queuePublicCacheTouch(touches, newPublicCacheTouch("same-digest", storedAtA, storedAtA.Add(2*time.Second)))
	recorder.queuePublicCacheTouch(touches, keyB)
	recorder.queuePublicCacheTouch(touches, newPublicCacheTouch("same-digest", storedAtB, storedAtB.Add(2*time.Second)))
	recorder.queuePublicCacheTouch(touches, newPublicCacheTouch("same-digest", storedAtB, storedAtB.Add(3*time.Second)))
	if len(touches) != 2 || touches[keyA.key].hitCount != 2 || touches[keyB.key].hitCount != 3 {
		t.Fatalf("generation touch state = len %d/A %d/B %d, want 2/2/3", len(touches), touches[keyA.key].hitCount, touches[keyB.key].hitCount)
	}
	if got := touches[keyA.key].lastAccessedAt; !got.Equal(storedAtA.Add(2 * time.Second)) {
		t.Fatalf("generation A latest access = %s, want %s", got, storedAtA.Add(2*time.Second))
	}
	if got := touches[keyB.key].lastAccessedAt; !got.Equal(storedAtB.Add(3 * time.Second)) {
		t.Fatalf("generation B latest access = %s, want %s", got, storedAtB.Add(3*time.Second))
	}

	batch := publicCacheTouchesToFlush(touches, time.Now().UTC(), true)
	counts := make(map[time.Time]int64)
	accesses := make(map[time.Time]time.Time)
	for _, touch := range batch {
		if touch.KeyDigest != "same-digest" {
			t.Fatalf("touch digest = %q, want same-digest", touch.KeyDigest)
		}
		counts[touch.StoredAt] = touch.HitCount
		accesses[touch.StoredAt] = touch.LastAccessedAt
	}
	if counts[storedAtA] != 2 || counts[storedAtB] != 3 {
		t.Fatalf("generation flush counts = A %d/B %d, want 2/3", counts[storedAtA], counts[storedAtB])
	}
	if !accesses[storedAtA].Equal(storedAtA.Add(2*time.Second)) || !accesses[storedAtB].Equal(storedAtB.Add(3*time.Second)) {
		t.Fatalf("generation flush access times = A %s/B %s", accesses[storedAtA], accesses[storedAtB])
	}
}

func TestCloseObservabilityRecorderFlushesQueuedEvents(t *testing.T) {
	ctx := context.Background()
	app := NewApp(nil, newServerTestDB(t))

	app.recordProxyRequestEventWithIDsAndContext(
		ctx,
		http.StatusOK,
		25*time.Millisecond,
		"",
		sql.NullInt64{},
		sql.NullInt64{},
		sql.NullInt64{},
		3,
		7,
		proxyRequestContext{Method: "GET", Host: "example.test", PathPrefix: "/close"},
	)
	if err := app.CloseObservabilityRecorder(ctx); err != nil {
		t.Fatalf("close observability recorder: %v", err)
	}
	if err := app.CloseObservabilityRecorder(ctx); err != nil {
		t.Fatalf("close observability recorder twice: %v", err)
	}

	var rawEvents int64
	if err := app.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM proxy_request_events`).Scan(&rawEvents); err != nil {
		t.Fatalf("count raw proxy events: %v", err)
	}
	if rawEvents != 1 {
		t.Fatalf("raw proxy events = %d, want 1", rawEvents)
	}
}

func TestCloseObservabilityRecorderCancelsActiveFlushAndDrains(t *testing.T) {
	app := NewApp(nil, newServerTestDB(t))
	recorder := newObservabilityRecorder(app)
	flushStarted := make(chan struct{})
	flushCanceled := make(chan struct{})
	flushCalls := 0
	recorder.insertBatch = func(ctx context.Context, events []db.InsertProxyRequestEventAtParams, touches []db.TouchPublicCacheEntryParams) error {
		flushCalls++
		if flushCalls == 1 {
			close(flushStarted)
			<-ctx.Done()
			close(flushCanceled)
			return ctx.Err()
		}
		return app.insertProxyRequestEventsWithRollupsAndCacheTouches(ctx, events, touches)
	}

	for i := 0; i < observabilityRecorderMaxBatch; i++ {
		recorder.recordProxyRequestEvent(context.Background(), proxyRequestEvent{StatusCode: http.StatusOK})
	}
	select {
	case <-flushStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for background observability flush to start")
	}

	closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := recorder.close(closeCtx); err != nil {
		t.Fatalf("close recorder after canceling active flush: %v", err)
	}
	select {
	case <-flushCanceled:
	default:
		t.Fatal("active background flush did not observe close cancellation")
	}
	if flushCalls != 2 {
		t.Fatalf("flush calls = %d, want canceled background flush plus stop drain", flushCalls)
	}
	if recorder.pendingEvents.Load() != 0 || len(recorder.events) != 0 {
		t.Fatalf("events remained after close drain: pending=%d queued=%d", recorder.pendingEvents.Load(), len(recorder.events))
	}

	var rawEvents int64
	if err := app.DB.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM proxy_request_events`).Scan(&rawEvents); err != nil {
		t.Fatalf("count drained proxy events: %v", err)
	}
	if rawEvents != observabilityRecorderMaxBatch {
		t.Fatalf("drained proxy events = %d, want %d", rawEvents, observabilityRecorderMaxBatch)
	}
}

func TestObservabilityRecorderFailedDeliveredCloseWakesConcurrentRetry(t *testing.T) {
	app := NewApp(nil, newServerTestDB(t))
	recorder := newObservabilityRecorder(app)
	flushStarted := make(chan struct{})
	secondCloseWaiting := make(chan struct{})
	var waitOnce sync.Once
	recorder.closeWaitHook = func() {
		waitOnce.Do(func() { close(secondCloseWaiting) })
	}
	flushCalls := 0
	var firstEvents, firstTouches, retryEvents, retryTouches int
	recorder.insertBatch = func(ctx context.Context, events []db.InsertProxyRequestEventAtParams, touches []db.TouchPublicCacheEntryParams) error {
		flushCalls++
		if flushCalls == 1 {
			firstEvents, firstTouches = len(events), len(touches)
			close(flushStarted)
			<-ctx.Done()
			return ctx.Err()
		}
		retryEvents, retryTouches = len(events), len(touches)
		return app.insertProxyRequestEventsWithRollupsAndCacheTouches(ctx, events, touches)
	}

	recorder.startOnce.Do(func() {})
	storedAt := time.Now().Add(-time.Minute).UTC()
	recorder.recordProxyRequestEvent(context.Background(), proxyRequestEvent{StatusCode: http.StatusOK})
	recorder.touchPublicCacheEntry("retry-touch", storedAt, storedAt.Add(time.Second))
	recorder.mu.Lock()
	recorder.startOnce = sync.Once{}
	recorder.mu.Unlock()
	firstCtx, cancelFirst := context.WithCancel(context.Background())
	firstDone := make(chan error, 1)
	go func() { firstDone <- recorder.close(firstCtx) }()
	select {
	case <-flushStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for delivered stop flush")
	}

	secondCtx, cancelSecond := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelSecond()
	secondDone := make(chan error, 1)
	go func() { secondDone <- recorder.close(secondCtx) }()
	select {
	case <-secondCloseWaiting:
	case <-time.After(2 * time.Second):
		t.Fatal("concurrent close did not wait on the active close attempt")
	}
	cancelFirst()
	if err := <-firstDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("first close error = %v, want context.Canceled", err)
	}
	if err := <-secondDone; err != nil {
		t.Fatalf("concurrent retry close: %v", err)
	}
	if flushCalls != 2 {
		t.Fatalf("flush calls = %d, want failed stop plus retry", flushCalls)
	}
	if firstEvents != 1 || firstTouches != 1 || retryEvents != 1 || retryTouches != 1 {
		t.Fatalf("retained batches = first events/touches %d/%d retry %d/%d, want 1/1 then 1/1", firstEvents, firstTouches, retryEvents, retryTouches)
	}
	if recorder.pendingEvents.Load() != 0 || recorder.pendingTouches.Load() != 0 || len(recorder.events) != 0 || len(recorder.touches) != 0 {
		t.Fatalf("records remained after retry close: pending=%d/%d queued=%d/%d", recorder.pendingEvents.Load(), recorder.pendingTouches.Load(), len(recorder.events), len(recorder.touches))
	}
	var rawEvents int64
	if err := app.DB.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM proxy_request_events`).Scan(&rawEvents); err != nil {
		t.Fatalf("count retry-drained proxy events: %v", err)
	}
	if rawEvents != 1 {
		t.Fatalf("retry-drained proxy events = %d, want 1", rawEvents)
	}
}

func TestObservabilityRecorderCanceledCloseCanRetryWithFreshContext(t *testing.T) {
	app := NewApp(nil, newServerTestDB(t))
	recorder := newObservabilityRecorder(app)
	// Suppress the worker until after the first close so its unbuffered control
	// send deterministically observes the canceled context.
	recorder.startOnce.Do(func() {})
	recorder.recordProxyRequestEvent(context.Background(), proxyRequestEvent{StatusCode: http.StatusOK})

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := recorder.close(canceledCtx); !errors.Is(err, context.Canceled) {
		t.Fatalf("close error = %v, want context.Canceled", err)
	}
	if recorder.stopped || recorder.closing.Load() {
		t.Fatalf("recorder remained stopped after failed pre-send close: stopped=%t closing=%t", recorder.stopped, recorder.closing.Load())
	}

	recorder.mu.Lock()
	recorder.startOnce = sync.Once{}
	recorder.mu.Unlock()
	flushCtx, flushCancel := context.WithTimeout(context.Background(), time.Second)
	defer flushCancel()
	if err := recorder.flush(flushCtx); err != nil {
		t.Fatalf("flush recorder with fresh context: %v", err)
	}
	closeCtx, closeCancel := context.WithTimeout(context.Background(), time.Second)
	defer closeCancel()
	if err := recorder.close(closeCtx); err != nil {
		t.Fatalf("close recorder with fresh context: %v", err)
	}

	var rawEvents int64
	if err := app.DB.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM proxy_request_events`).Scan(&rawEvents); err != nil {
		t.Fatalf("count raw proxy events: %v", err)
	}
	if rawEvents != 1 {
		t.Fatalf("raw proxy events after canceled close and fresh flush = %d, want 1", rawEvents)
	}
}

func TestAgentStatRollupsAreWrittenWithRawStat(t *testing.T) {
	ctx := context.Background()
	app := NewApp(nil, newServerTestDB(t))
	insertDashboardRollupAgentFixture(t, app.DB, 1)

	if err := app.insertAgentStatWithRollup(ctx, db.InsertAgentStatAtParams{
		ReportedAt:       time.Now().UTC(),
		AgentID:          sqlNullInt64(1),
		MemoryMb:         128,
		Goroutines:       11,
		ReqSuccess:       3,
		ReqClientError:   4,
		ReqServerError:   5,
		ReqInternalError: 6,
		BytesRx:          700,
		BytesTx:          800,
		CpuPercent:       12.5,
	}); err != nil {
		t.Fatalf("insert agent stat with rollup: %v", err)
	}

	var rawStats int64
	if err := app.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_stats`).Scan(&rawStats); err != nil {
		t.Fatalf("count raw agent stats: %v", err)
	}
	if rawStats != 1 {
		t.Fatalf("raw agent stats = %d, want 1", rawStats)
	}

	var samples, reqSuccess, memorySum, maxMemory int64
	var cpuSum, maxCPU float64
	if err := app.DB.QueryRowContext(ctx, `
		SELECT samples, req_success, memory_mb_sum, max_memory_mb, cpu_percent_sum, max_cpu_percent
		FROM agent_stat_rollup_minutes
	`).Scan(&samples, &reqSuccess, &memorySum, &maxMemory, &cpuSum, &maxCPU); err != nil {
		t.Fatalf("read agent stat rollup: %v", err)
	}
	if samples != 1 || reqSuccess != 3 || memorySum != 128 || maxMemory != 128 || cpuSum != 12.5 || maxCPU != 12.5 {
		t.Fatalf("unexpected agent stat rollup: samples=%d req_success=%d memory_sum=%d max_memory=%d cpu_sum=%f max_cpu=%f", samples, reqSuccess, memorySum, maxMemory, cpuSum, maxCPU)
	}
}

func TestDashboardUsesBackfilledRollups(t *testing.T) {
	ctx := context.Background()
	app := NewApp(nil, newServerTestDB(t))
	header := createTestAdminSession(t, app)
	seedDashboardRollupDimensionFixtures(t, app.DB)

	now := time.Now().UTC()
	insertDashboardRollupProxyEvent(t, app.DB, now.Add(-2*time.Minute), http.StatusOK, 100, "", sqlNullInt64(1), sqlNullInt64(1), sqlNullInt64(1), sqlNullInt64(1), 10, 100)
	insertDashboardRollupProxyEvent(t, app.DB, now.Add(-30*time.Minute), http.StatusBadGateway, 1300, "agent_timeout", sqlNullInt64(1), sqlNullInt64(1), sql.NullInt64{}, sql.NullInt64{}, 20, 200)
	insertDashboardRollupAgentStat(t, app.DB, now.Add(-2*time.Minute), 100, 8, 10, 1, 2, 3, 1000, 2000)
	insertDashboardRollupAgentStat(t, app.DB, now.Add(-30*time.Minute), 150, 10, 20, 2, 3, 4, 3000, 4000)
	resetRollupStateToRawMax(t, app.DB)

	for {
		progress, err := app.backfillObservabilityRollupBatch(ctx)
		if err != nil {
			t.Fatalf("backfill rollup batch: %v", err)
		}
		if !progress {
			break
		}
	}
	if progress, err := app.backfillObservabilityRollupBatch(ctx); err != nil || progress {
		t.Fatalf("second backfill progress=%v err=%v, want no progress", progress, err)
	}

	req := connect.NewRequest(&p2pstreamv1.GetDashboardRequest{})
	req.Header().Set("Cookie", header.Get("Cookie"))
	resp, err := app.GetDashboard(ctx, req)
	if err != nil {
		t.Fatalf("get dashboard: %v", err)
	}

	windows := dashboardTestWindowsByLabel(resp.Msg.Windows)
	fiveMinutes := windows["5m"]
	if fiveMinutes == nil {
		t.Fatal("missing 5m dashboard window")
	}
	if fiveMinutes.ProxyRequests != 1 || fiveMinutes.ProxySuccess != 1 || fiveMinutes.AgentSamples != 1 {
		t.Fatalf("unexpected 5m rollup window: %+v", fiveMinutes)
	}
	oneHour := windows["1h"]
	if oneHour == nil {
		t.Fatal("missing 1h dashboard window")
	}
	if oneHour.ProxyRequests != 2 || oneHour.ProxyServerError != 1 || oneHour.ProxyInternalError != 1 || oneHour.AgentSamples != 2 {
		t.Fatalf("unexpected 1h rollup window: %+v", oneHour)
	}
	if len(resp.Msg.TopListeners) == 0 || resp.Msg.TopListeners[0].Label != "listener-one" || resp.Msg.TopListeners[0].Requests != 2 {
		t.Fatalf("unexpected rollup top listeners: %+v", resp.Msg.TopListeners)
	}
	if len(resp.Msg.TopErrorKinds) != 1 || resp.Msg.TopErrorKinds[0].Label != "agent_timeout" {
		t.Fatalf("unexpected rollup top error kinds: %+v", resp.Msg.TopErrorKinds)
	}
	var bucketRequests int64
	for _, bucket := range resp.Msg.TrafficBuckets {
		bucketRequests += bucket.Requests
	}
	if bucketRequests != 2 {
		t.Fatalf("rollup bucket requests = %d, want 2", bucketRequests)
	}
}

func TestGetDashboardDoesNotRunObservabilityCleanup(t *testing.T) {
	ctx := context.Background()
	app := NewApp(&config.Config{ObservabilityRetentionDays: 30}, newServerTestDB(t))
	header := createTestAdminSession(t, app)

	if _, err := app.DB.ExecContext(ctx, `
		INSERT INTO proxy_request_events (occurred_at, status_code, duration_ms)
		VALUES (?, ?, ?)
	`, time.Now().UTC().AddDate(0, 0, -31), http.StatusOK, 10); err != nil {
		t.Fatalf("insert old proxy event: %v", err)
	}

	req := connect.NewRequest(&p2pstreamv1.GetDashboardRequest{})
	req.Header().Set("Cookie", header.Get("Cookie"))
	if _, err := app.GetDashboard(ctx, req); err != nil {
		t.Fatalf("get dashboard: %v", err)
	}

	var oldRows int64
	if err := app.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM proxy_request_events`).Scan(&oldRows); err != nil {
		t.Fatalf("count old proxy events: %v", err)
	}
	if oldRows != 1 {
		t.Fatalf("old proxy rows after dashboard = %d, want 1", oldRows)
	}
}

func TestCleanupObservabilityDeletesOldExactStatusRollups(t *testing.T) {
	ctx := context.Background()
	app := NewApp(&config.Config{ObservabilityRetentionDays: 30}, newServerTestDB(t))
	now := time.Now().UTC()
	oldBucket := rollupBucketUnixMillis(now.AddDate(0, 0, -31))
	recentBucket := rollupBucketUnixMillis(now.Add(-time.Hour))

	if err := app.DB.UpsertProxyRequestStatusRollupMinute(ctx, db.UpsertProxyRequestStatusRollupMinuteParams{
		BucketUnixMillis: oldBucket,
		StatusCode:       http.StatusNotFound,
		Requests:         1,
		ClientError:      1,
	}); err != nil {
		t.Fatalf("insert old status rollup: %v", err)
	}
	if err := app.DB.UpsertProxyRequestStatusRollupMinute(ctx, db.UpsertProxyRequestStatusRollupMinuteParams{
		BucketUnixMillis: recentBucket,
		StatusCode:       http.StatusOK,
		Requests:         1,
		Success:          1,
	}); err != nil {
		t.Fatalf("insert recent status rollup: %v", err)
	}

	app.cleanupObservability(ctx, now)

	if countRows(t, app.DB, `SELECT COUNT(*) FROM proxy_request_status_rollup_minutes WHERE bucket_unix_millis = ?`, oldBucket) != 0 {
		t.Fatal("expected old status rollup to be cleaned up")
	}
	if countRows(t, app.DB, `SELECT COUNT(*) FROM proxy_request_status_rollup_minutes WHERE bucket_unix_millis = ?`, recentBucket) != 1 {
		t.Fatal("expected recent status rollup to remain")
	}
}

func TestCleanupObservabilityEnforcesProxyRequestRowCap(t *testing.T) {
	ctx := context.Background()
	app := NewApp(&config.Config{ObservabilityRetentionDays: 30, ObservabilityMaxRows: 3}, newServerTestDB(t))

	for i := 0; i < 5; i++ {
		if err := app.DB.InsertProxyRequestEvent(ctx, db.InsertProxyRequestEventParams{
			StatusCode: http.StatusOK,
			DurationMs: int64(i),
		}); err != nil {
			t.Fatalf("insert proxy request event %d: %v", i, err)
		}
	}

	app.cleanupObservability(ctx, time.Now().UTC())

	var count int
	if err := app.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM proxy_request_events`).Scan(&count); err != nil {
		t.Fatalf("count proxy request events: %v", err)
	}
	if count != 3 {
		t.Fatalf("proxy request event count = %d, want 3", count)
	}
}

func TestDashboardCacheReturnsCachedMetricsWithLiveStatus(t *testing.T) {
	ctx := context.Background()
	app := NewApp(nil, newServerTestDB(t))
	header := createTestAdminSession(t, app)
	app.DashboardCache.started.Store(true)

	insertRollupEvent(t, app, http.StatusOK, "")
	app.refreshDashboardCache(ctx)

	insertRollupEvent(t, app, http.StatusOK, "")
	app.ProxyIsRunning.Store(true)

	req := connect.NewRequest(&p2pstreamv1.GetDashboardRequest{})
	req.Header().Set("Cookie", header.Get("Cookie"))
	resp, err := app.GetDashboard(ctx, req)
	if err != nil {
		t.Fatalf("get dashboard: %v", err)
	}

	window := dashboardTestWindow(t, resp.Msg.Windows, "5m")
	if window.ProxyRequests != 1 {
		t.Fatalf("cached proxy requests = %d, want 1", window.ProxyRequests)
	}
	if resp.Msg.Status == nil || !resp.Msg.Status.ProxyRunning {
		t.Fatalf("expected live proxy status overlay, got %+v", resp.Msg.Status)
	}
}

func TestDashboardCacheColdAndRollupOnlyPathsDoNotReadRawEvents(t *testing.T) {
	ctx := context.Background()
	app := NewApp(nil, newServerTestDB(t))
	header := createTestAdminSession(t, app)
	app.DashboardCache.started.Store(true)

	if err := app.DB.InsertProxyRequestEvent(ctx, db.InsertProxyRequestEventParams{
		StatusCode: http.StatusOK,
		DurationMs: 10,
	}); err != nil {
		t.Fatalf("insert raw proxy event: %v", err)
	}

	req := connect.NewRequest(&p2pstreamv1.GetDashboardRequest{})
	req.Header().Set("Cookie", header.Get("Cookie"))
	coldResp, err := app.GetDashboard(ctx, req)
	if err != nil {
		t.Fatalf("get cold dashboard: %v", err)
	}
	if dashboardTestWindow(t, coldResp.Msg.Windows, "5m").ProxyRequests != 0 {
		t.Fatalf("cold cache should not fall back to raw events: %+v", coldResp.Msg.Windows)
	}

	app.refreshDashboardCache(ctx)
	cachedResp, err := app.GetDashboard(ctx, req)
	if err != nil {
		t.Fatalf("get cached dashboard: %v", err)
	}
	if dashboardTestWindow(t, cachedResp.Msg.Windows, "5m").ProxyRequests != 0 {
		t.Fatalf("rollup cache should ignore raw-only events: %+v", cachedResp.Msg.Windows)
	}
}

func TestDashboardCacheSnapshotAggregatesRollups(t *testing.T) {
	ctx := context.Background()
	app := NewApp(nil, newServerTestDB(t))
	now := time.Date(2026, 6, 2, 12, 34, 20, 0, time.UTC)

	listenerID := insertPublicListenerRow(t, app, "rollup-listener")
	routeID := insertPublicRouteRow(t, app, listenerID)
	targetID := insertPublicRouteTargetRow(t, app, routeID, "rollup-target")
	agent, err := app.DB.CreateAgent(ctx, db.CreateAgentParams{
		PublicID:  "agent-rollup",
		Name:      "rollup-agent",
		TokenHash: "hash",
		Enabled:   1,
	})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	if err := app.refreshPublicProxySnapshot(ctx); err != nil {
		t.Fatalf("refresh public proxy snapshot: %v", err)
	}

	insertRollupEventAt(t, app, now.Add(-2*time.Minute), http.StatusOK, "", listenerID, targetID, routeID, agent.ID, publicCacheStatusHit, 25, 10, 20, 100)
	insertRollupEventAt(t, app, now.Add(-4*time.Minute), http.StatusBadGateway, "upstream", listenerID, targetID, routeID, agent.ID, publicCacheStatusStored, 45, 30, 40, 1500)
	insertRollupEventAt(t, app, now.Add(-2*time.Hour), http.StatusNotFound, "", listenerID, targetID, routeID, agent.ID, publicCacheStatusBypass, 0, 50, 60, 20)
	if err := app.insertAgentStatWithRollup(ctx, db.InsertAgentStatAtParams{
		ReportedAt:       now.Add(-3 * time.Minute),
		AgentID:          sql.NullInt64{Int64: agent.ID, Valid: true},
		MemoryMb:         128,
		Goroutines:       14,
		ReqSuccess:       9,
		ReqClientError:   1,
		ReqServerError:   2,
		ReqInternalError: 3,
		BytesRx:          400,
		BytesTx:          800,
		CpuPercent:       7.5,
	}); err != nil {
		t.Fatalf("insert agent stat rollup: %v", err)
	}

	resp, err := app.buildDashboardCacheSnapshot(ctx, now)
	if err != nil {
		t.Fatalf("build dashboard cache snapshot: %v", err)
	}

	fiveMinute := dashboardTestWindow(t, resp.Windows, "5m")
	if fiveMinute.ProxyRequests != 2 || fiveMinute.ProxySuccess != 1 || fiveMinute.ProxyServerError != 1 || fiveMinute.ProxyInternalError != 1 {
		t.Fatalf("unexpected 5m proxy summary: %+v", fiveMinute)
	}
	if fiveMinute.ProxyAvgDurationMs != 800 || fiveMinute.ProxyMaxDurationMs != 1500 || fiveMinute.ProxySlowRequests != 1 {
		t.Fatalf("unexpected 5m duration summary: %+v", fiveMinute)
	}
	if fiveMinute.ProxyRequestBytes != 40 || fiveMinute.ProxyResponseBytes != 60 || fiveMinute.ProxyTotalBytes != 100 {
		t.Fatalf("unexpected 5m byte summary: %+v", fiveMinute)
	}
	if fiveMinute.ProxyCacheHits != 1 || fiveMinute.ProxyCacheMisses != 1 || fiveMinute.ProxyCacheStored != 1 || fiveMinute.ProxyCacheHitBytes != 25 || fiveMinute.ProxyCacheStoredBytes != 45 {
		t.Fatalf("unexpected 5m cache summary: %+v", fiveMinute)
	}
	if fiveMinute.AgentSamples != 1 || fiveMinute.AgentAvgMemoryMb != 128 || fiveMinute.AgentAvgGoroutines != 14 || fiveMinute.AgentAvgCpuPercent != 7.5 {
		t.Fatalf("unexpected 5m agent summary: %+v", fiveMinute)
	}

	day := dashboardTestWindow(t, resp.Windows, "24h")
	if day.ProxyRequests != 3 || day.ProxyClientError != 1 {
		t.Fatalf("unexpected 24h proxy summary: %+v", day)
	}
	if len(resp.TrafficBuckets) != 1 || resp.TrafficBuckets[0].Requests != 2 || resp.TrafficBuckets[0].AvgDurationMs != 800 {
		t.Fatalf("unexpected traffic buckets: %+v", resp.TrafficBuckets)
	}
	if len(resp.TopListeners) != 1 || resp.TopListeners[0].Label != "rollup-listener" || resp.TopListeners[0].Requests != 2 {
		t.Fatalf("unexpected top listeners: %+v", resp.TopListeners)
	}
	if len(resp.TopRouteTargets) != 1 || resp.TopRouteTargets[0].Label != "rollup-target" || resp.TopRouteTargets[0].Requests != 2 {
		t.Fatalf("unexpected top route targets: %+v", resp.TopRouteTargets)
	}
	if len(resp.TopRoutes) != 1 || resp.TopRoutes[0].Label != "rollup.example /api" || resp.TopRoutes[0].Requests != 2 {
		t.Fatalf("unexpected top routes: %+v", resp.TopRoutes)
	}
	if len(resp.TopAgents) != 1 || resp.TopAgents[0].Label != "rollup-agent" || resp.TopAgents[0].Requests != 2 {
		t.Fatalf("unexpected top agents: %+v", resp.TopAgents)
	}
	if len(resp.TopErrorKinds) != 1 || resp.TopErrorKinds[0].Label != "upstream" || resp.TopErrorKinds[0].Requests != 1 {
		t.Fatalf("unexpected top error kinds: %+v", resp.TopErrorKinds)
	}
	if len(resp.StatusClasses) != 2 || resp.StatusClasses[0].Label != "2xx" || resp.StatusClasses[1].Label != "5xx" {
		t.Fatalf("unexpected status classes: %+v", resp.StatusClasses)
	}
}

func TestPublicProxyConfigResponseUsesCachedRowsUntilRefresh(t *testing.T) {
	ctx := context.Background()
	app := NewApp(nil, newServerTestDB(t))

	first, err := app.publicProxyConfigResponse(ctx)
	if err != nil {
		t.Fatalf("load public config: %v", err)
	}
	if publicConfigHasTarget(first, "direct-only") {
		t.Fatal("unexpected direct-only target before insert")
	}

	var routeID int64
	if err := app.DB.QueryRowContext(ctx, `SELECT id FROM public_routes ORDER BY id LIMIT 1`).Scan(&routeID); err != nil {
		t.Fatalf("select route: %v", err)
	}
	if _, err := app.DB.ExecContext(ctx, `
		INSERT INTO public_route_targets (route_id, name, position, target_type, url, transport, enabled)
		VALUES (?, 'direct-only', 99, 'proxy', 'http://direct-only.local', 'direct', 1)
	`, routeID); err != nil {
		t.Fatalf("insert direct target: %v", err)
	}

	cached, err := app.publicProxyConfigResponse(ctx)
	if err != nil {
		t.Fatalf("load cached public config: %v", err)
	}
	if publicConfigHasTarget(cached, "direct-only") {
		t.Fatal("cached public config unexpectedly reflected direct DB change")
	}

	if err := app.refreshPublicProxySnapshot(ctx); err != nil {
		t.Fatalf("refresh public proxy snapshot: %v", err)
	}
	refreshed, err := app.publicProxyConfigResponse(ctx)
	if err != nil {
		t.Fatalf("load refreshed public config: %v", err)
	}
	if !publicConfigHasTarget(refreshed, "direct-only") {
		t.Fatal("refreshed public config did not include direct-only target")
	}
}

func TestPublicProxyConfigAgentsUseMemoryLatestStats(t *testing.T) {
	ctx := context.Background()
	app := NewApp(nil, newServerTestDB(t))
	agent, err := app.DB.CreateAgent(ctx, db.CreateAgentParams{
		PublicID:  "agent-memory",
		Name:      "agent-memory",
		TokenHash: "hash",
		Enabled:   1,
	})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	if err := app.refreshPublicProxySnapshot(ctx); err != nil {
		t.Fatalf("refresh public proxy snapshot: %v", err)
	}
	app.storeLatestAgentStats(agent.ID, stats.AgentStats{
		Timestamp:        time.Unix(1700000000, 0).UTC(),
		NumGoroutine:     12,
		MemorySysMB:      256,
		ReqSuccess:       7,
		ReqClientError:   1,
		ReqServerError:   2,
		ReqInternalError: 3,
		BytesReceived:    400,
		BytesSent:        800,
		ActiveRequests:   5,
		CPUPercent:       9.5,
	})

	resp, err := app.publicProxyConfigResponse(ctx)
	if err != nil {
		t.Fatalf("load public config: %v", err)
	}
	for _, item := range resp.Agents {
		if item.Id != agent.ID {
			continue
		}
		if item.LatestStats == nil || item.LatestStats.NumGoroutine != 12 || item.LatestStats.ActiveRequests != 5 {
			t.Fatalf("unexpected memory latest stats: %+v", item.LatestStats)
		}
		return
	}
	t.Fatalf("agent %d not found in public config", agent.ID)
}

func TestDashboardAgentUptimeSummaryConnectedAgent(t *testing.T) {
	ctx := context.Background()
	app := NewApp(&config.Config{ObservabilityRetentionDays: 30}, newServerTestDB(t))
	now := time.Unix(1_800_000_000, 0).UTC()
	agent := createDashboardUptimeAgent(t, app.DB, "agent-connected", now.Add(-2*time.Hour))
	connectedAt := now.Add(-90 * time.Minute)
	connID := insertDashboardConnection(t, app.DB, agent.ID, connectedAt, sql.NullTime{})
	setDashboardAgentTimes(t, app.DB, agent.ID, sql.NullTime{Time: connectedAt, Valid: true}, sql.NullTime{})

	conn := testAgentConn(agent.ID, agent.PublicID)
	conn.ConnectedAt = connectedAt
	conn.ConnectionDBID = connID
	if err := app.AgentHub.connect(conn); err != nil {
		t.Fatalf("connect agent: %v", err)
	}
	t.Cleanup(func() { app.AgentHub.disconnect(conn) })

	summaries, err := app.agentUptimeSummaries(ctx, now)
	if err != nil {
		t.Fatalf("agent uptime summaries: %v", err)
	}
	summary := dashboardUptimeSummaryByAgentID(t, summaries, agent.ID)

	if !summary.Connected {
		t.Fatal("summary connected = false, want true")
	}
	if summary.CurrentConnectedAtUnixMillis != connectedAt.UnixMilli() {
		t.Fatalf("current connected at = %d, want %d", summary.CurrentConnectedAtUnixMillis, connectedAt.UnixMilli())
	}
	if summary.CurrentUptimeMillis != int64((90 * time.Minute).Milliseconds()) {
		t.Fatalf("current uptime = %d, want 90m", summary.CurrentUptimeMillis)
	}
	if summary.UptimeMillis != int64((90*time.Minute).Milliseconds()) || summary.DowntimeMillis != int64((30*time.Minute).Milliseconds()) {
		t.Fatalf("uptime/downtime = %d/%d, want 90m/30m", summary.UptimeMillis, summary.DowntimeMillis)
	}
	assertDashboardFloatClose(t, summary.UptimePercent, 0.75)
	if summary.ConnectionCount != 1 || summary.DisconnectCount != 0 {
		t.Fatalf("connection/disconnect count = %d/%d, want 1/0", summary.ConnectionCount, summary.DisconnectCount)
	}
}

func TestDashboardAgentUptimeSummaryDisconnectedAgent(t *testing.T) {
	ctx := context.Background()
	app := NewApp(&config.Config{ObservabilityRetentionDays: 30}, newServerTestDB(t))
	now := time.Unix(1_800_000_000, 0).UTC()
	agent := createDashboardUptimeAgent(t, app.DB, "agent-disconnected", now.Add(-2*time.Hour))
	connectedAt := now.Add(-90 * time.Minute)
	disconnectedAt := now.Add(-30 * time.Minute)
	insertDashboardConnection(t, app.DB, agent.ID, connectedAt, sql.NullTime{Time: disconnectedAt, Valid: true})
	setDashboardAgentTimes(t, app.DB, agent.ID, sql.NullTime{Time: connectedAt, Valid: true}, sql.NullTime{Time: disconnectedAt, Valid: true})

	summaries, err := app.agentUptimeSummaries(ctx, now)
	if err != nil {
		t.Fatalf("agent uptime summaries: %v", err)
	}
	summary := dashboardUptimeSummaryByAgentID(t, summaries, agent.ID)

	if summary.Connected {
		t.Fatal("summary connected = true, want false")
	}
	if summary.CurrentOfflineSinceUnixMillis != disconnectedAt.UnixMilli() {
		t.Fatalf("offline since = %d, want %d", summary.CurrentOfflineSinceUnixMillis, disconnectedAt.UnixMilli())
	}
	if summary.CurrentDowntimeMillis != int64((30 * time.Minute).Milliseconds()) {
		t.Fatalf("current downtime = %d, want 30m", summary.CurrentDowntimeMillis)
	}
	if summary.UptimeMillis != int64((60*time.Minute).Milliseconds()) || summary.DowntimeMillis != int64((60*time.Minute).Milliseconds()) {
		t.Fatalf("uptime/downtime = %d/%d, want 60m/60m", summary.UptimeMillis, summary.DowntimeMillis)
	}
	assertDashboardFloatClose(t, summary.UptimePercent, 0.5)
	if summary.ConnectionCount != 1 || summary.DisconnectCount != 1 {
		t.Fatalf("connection/disconnect count = %d/%d, want 1/1", summary.ConnectionCount, summary.DisconnectCount)
	}
}

func TestDashboardAgentUptimeClipsToRetentionWindow(t *testing.T) {
	ctx := context.Background()
	app := NewApp(&config.Config{ObservabilityRetentionDays: 1}, newServerTestDB(t))
	now := time.Unix(1_800_000_000, 0).UTC()
	agent := createDashboardUptimeAgent(t, app.DB, "agent-retention", now.Add(-48*time.Hour))
	insertDashboardConnection(t, app.DB, agent.ID, now.Add(-36*time.Hour), sql.NullTime{Time: now.Add(-12 * time.Hour), Valid: true})

	summaries, err := app.agentUptimeSummaries(ctx, now)
	if err != nil {
		t.Fatalf("agent uptime summaries: %v", err)
	}
	summary := dashboardUptimeSummaryByAgentID(t, summaries, agent.ID)

	if summary.ObservedSinceUnixMillis != now.Add(-24*time.Hour).UnixMilli() {
		t.Fatalf("observed since = %d, want retention boundary", summary.ObservedSinceUnixMillis)
	}
	if summary.UptimeMillis != int64((12*time.Hour).Milliseconds()) || summary.DowntimeMillis != int64((12*time.Hour).Milliseconds()) {
		t.Fatalf("uptime/downtime = %d/%d, want 12h/12h", summary.UptimeMillis, summary.DowntimeMillis)
	}
	assertDashboardFloatClose(t, summary.UptimePercent, 0.5)
}

func TestDashboardAgentUptimeClipsToAgentCreationTime(t *testing.T) {
	ctx := context.Background()
	app := NewApp(&config.Config{ObservabilityRetentionDays: 30}, newServerTestDB(t))
	now := time.Unix(1_800_000_000, 0).UTC()
	createdAt := now.Add(-2 * time.Hour)
	agent := createDashboardUptimeAgent(t, app.DB, "agent-created", createdAt)
	insertDashboardConnection(t, app.DB, agent.ID, now.Add(-5*time.Hour), sql.NullTime{Time: now.Add(-1 * time.Hour), Valid: true})

	summaries, err := app.agentUptimeSummaries(ctx, now)
	if err != nil {
		t.Fatalf("agent uptime summaries: %v", err)
	}
	summary := dashboardUptimeSummaryByAgentID(t, summaries, agent.ID)

	if summary.ObservedSinceUnixMillis != createdAt.UnixMilli() {
		t.Fatalf("observed since = %d, want %d", summary.ObservedSinceUnixMillis, createdAt.UnixMilli())
	}
	if summary.UptimeMillis != int64((1*time.Hour).Milliseconds()) || summary.DowntimeMillis != int64((1*time.Hour).Milliseconds()) {
		t.Fatalf("uptime/downtime = %d/%d, want 1h/1h", summary.UptimeMillis, summary.DowntimeMillis)
	}
	assertDashboardFloatClose(t, summary.UptimePercent, 0.5)
}

func TestDashboardRecentAgentConnectionSessions(t *testing.T) {
	ctx := context.Background()
	app := NewApp(&config.Config{ObservabilityRetentionDays: 30}, newServerTestDB(t))
	now := time.Unix(1_800_000_000, 0).UTC()
	agent := createDashboardUptimeAgent(t, app.DB, "agent-recent", now.Add(-2*time.Hour))
	oldID := insertDashboardConnection(t, app.DB, agent.ID, now.Add(-10*time.Minute), sql.NullTime{Time: now.Add(-7 * time.Minute), Valid: true})
	activeID := insertDashboardConnection(t, app.DB, agent.ID, now.Add(-5*time.Minute), sql.NullTime{})
	newID := insertDashboardConnection(t, app.DB, agent.ID, now.Add(-2*time.Minute), sql.NullTime{Time: now.Add(-1 * time.Minute), Valid: true})

	sessions, err := app.recentAgentConnectionSessions(ctx, now)
	if err != nil {
		t.Fatalf("recent agent connection sessions: %v", err)
	}
	if len(sessions) < 3 {
		t.Fatalf("sessions length = %d, want at least 3", len(sessions))
	}
	if sessions[0].Id != newID || sessions[1].Id != activeID || sessions[2].Id != oldID {
		t.Fatalf("session order = %d/%d/%d, want %d/%d/%d", sessions[0].Id, sessions[1].Id, sessions[2].Id, newID, activeID, oldID)
	}
	if sessions[0].DurationMillis != int64((1*time.Minute).Milliseconds()) || sessions[0].Active {
		t.Fatalf("new session duration/active = %d/%v, want 1m/false", sessions[0].DurationMillis, sessions[0].Active)
	}
	if sessions[1].DurationMillis != int64((5*time.Minute).Milliseconds()) || !sessions[1].Active {
		t.Fatalf("active session duration/active = %d/%v, want 5m/true", sessions[1].DurationMillis, sessions[1].Active)
	}
}

func TestGetAgentAvailabilityRequiresAuthAndValidWindow(t *testing.T) {
	ctx := context.Background()
	app := NewApp(nil, newServerTestDB(t))

	unauthenticated := connect.NewRequest(&p2pstreamv1.GetAgentAvailabilityRequest{
		AgentPublicId: "agent-availability",
		WindowLabel:   "24h",
	})
	if _, err := app.GetAgentAvailability(ctx, unauthenticated); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("GetAgentAvailability unauthenticated code = %s, want unauthenticated: %v", connect.CodeOf(err), err)
	}

	header := createTestAdminSession(t, app)
	invalid := connect.NewRequest(&p2pstreamv1.GetAgentAvailabilityRequest{
		AgentPublicId: "agent-availability",
		WindowLabel:   "forever",
	})
	invalid.Header().Set("Cookie", header.Get("Cookie"))
	if _, err := app.GetAgentAvailability(ctx, invalid); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("GetAgentAvailability invalid window code = %s, want invalid argument: %v", connect.CodeOf(err), err)
	}
}

func TestAgentAvailabilityBuildsClippedTimeline(t *testing.T) {
	ctx := context.Background()
	app := NewApp(&config.Config{ObservabilityRetentionDays: 30}, newServerTestDB(t))
	now := time.Unix(1_800_000_000, 0).UTC()
	agent := createDashboardUptimeAgent(t, app.DB, "agent-availability", now.Add(-48*time.Hour))

	insertDashboardConnection(t, app.DB, agent.ID, now.Add(-23*time.Hour), sql.NullTime{Time: now.Add(-20 * time.Hour), Valid: true})
	insertDashboardConnection(t, app.DB, agent.ID, now.Add(-18*time.Hour), sql.NullTime{Time: now.Add(-12 * time.Hour), Valid: true})
	insertDashboardConnection(t, app.DB, agent.ID, now.Add(-2*time.Hour), sql.NullTime{})
	active := testAgentConn(agent.ID, agent.PublicID)
	active.ConnectedAt = now.Add(-2 * time.Hour)
	if err := app.AgentHub.connect(active); err != nil {
		t.Fatalf("connect active agent: %v", err)
	}

	availability, err := app.agentAvailability(ctx, agent, "24h", 24*time.Hour, now)
	if err != nil {
		t.Fatalf("agent availability: %v", err)
	}
	if availability.ObservedSinceUnixMillis != now.Add(-24*time.Hour).UnixMilli() || availability.ObservedUntilUnixMillis != now.UnixMilli() {
		t.Fatalf("observed range = %d..%d, want %d..%d", availability.ObservedSinceUnixMillis, availability.ObservedUntilUnixMillis, now.Add(-24*time.Hour).UnixMilli(), now.UnixMilli())
	}
	if availability.UptimeMillis != int64((11*time.Hour).Milliseconds()) || availability.DowntimeMillis != int64((13*time.Hour).Milliseconds()) {
		t.Fatalf("uptime/downtime = %d/%d, want 11h/13h", availability.UptimeMillis, availability.DowntimeMillis)
	}
	assertDashboardFloatClose(t, availability.UptimePercent, 11.0/24.0)
	if availability.DisconnectCount != 2 || availability.LongestDowntimeMillis != int64((10*time.Hour).Milliseconds()) {
		t.Fatalf("disconnects/longest downtime = %d/%d, want 2/10h", availability.DisconnectCount, availability.LongestDowntimeMillis)
	}
	if !availability.Connected {
		t.Fatal("availability connected = false, want true")
	}
	if len(availability.Intervals) != 3 || !availability.Intervals[2].Active {
		t.Fatalf("intervals = %#v, want three with final active", availability.Intervals)
	}
}

func TestAgentAvailabilityClipsObservationToAgentCreation(t *testing.T) {
	ctx := context.Background()
	app := NewApp(&config.Config{ObservabilityRetentionDays: 30}, newServerTestDB(t))
	now := time.Unix(1_800_000_000, 0).UTC()
	createdAt := now.Add(-90 * time.Minute)
	agent := createDashboardUptimeAgent(t, app.DB, "agent-new-availability", createdAt)
	insertDashboardConnection(t, app.DB, agent.ID, now.Add(-60*time.Minute), sql.NullTime{Time: now.Add(-30 * time.Minute), Valid: true})

	availability, err := app.agentAvailability(ctx, agent, "24h", 24*time.Hour, now)
	if err != nil {
		t.Fatalf("agent availability: %v", err)
	}
	if availability.ObservedSinceUnixMillis != createdAt.UnixMilli() {
		t.Fatalf("observed since = %d, want creation time %d", availability.ObservedSinceUnixMillis, createdAt.UnixMilli())
	}
	if availability.UptimeMillis != int64((30*time.Minute).Milliseconds()) || availability.DowntimeMillis != int64((60*time.Minute).Milliseconds()) {
		t.Fatalf("uptime/downtime = %d/%d, want 30m/60m", availability.UptimeMillis, availability.DowntimeMillis)
	}
	if availability.LongestDowntimeMillis != int64((30 * time.Minute).Milliseconds()) {
		t.Fatalf("longest downtime = %d, want 30m", availability.LongestDowntimeMillis)
	}
}

func TestNewAppClosesStaleOpenAgentConnections(t *testing.T) {
	ctx := context.Background()
	database := newServerTestDB(t)
	agent, err := database.CreateAgent(ctx, db.CreateAgentParams{
		PublicID:  "agent-stale",
		Name:      "agent-stale",
		TokenHash: "hash",
		Enabled:   1,
	})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	connID := insertDashboardConnection(t, database, agent.ID, time.Unix(1_800_000_000, 0).UTC(), sql.NullTime{})

	_ = NewApp(&config.Config{}, database)

	var connectionDisconnectedAt sql.NullTime
	if err := database.QueryRowContext(ctx, `SELECT disconnected_at FROM connections WHERE id = ?`, connID).Scan(&connectionDisconnectedAt); err != nil {
		t.Fatalf("read connection disconnected_at: %v", err)
	}
	var agentLastDisconnectedAt sql.NullTime
	if err := database.QueryRowContext(ctx, `SELECT last_disconnected_at FROM agents WHERE id = ?`, agent.ID).Scan(&agentLastDisconnectedAt); err != nil {
		t.Fatalf("read agent last_disconnected_at: %v", err)
	}
	if !connectionDisconnectedAt.Valid || !agentLastDisconnectedAt.Valid {
		t.Fatalf("disconnected timestamps valid = connection %v agent %v, want both true", connectionDisconnectedAt.Valid, agentLastDisconnectedAt.Valid)
	}
	if !connectionDisconnectedAt.Time.Equal(agentLastDisconnectedAt.Time) {
		t.Fatalf("connection disconnected_at %s != agent last_disconnected_at %s", connectionDisconnectedAt.Time, agentLastDisconnectedAt.Time)
	}
}

func dashboardTestWindow(t *testing.T, windows []*p2pstreamv1.DashboardWindowSummary, label string) *p2pstreamv1.DashboardWindowSummary {
	t.Helper()
	for _, window := range windows {
		if window.GetLabel() == label {
			return window
		}
	}
	t.Fatalf("dashboard window %q not found", label)
	return nil
}

func dashboardTestWindowsByLabel(windows []*p2pstreamv1.DashboardWindowSummary) map[string]*p2pstreamv1.DashboardWindowSummary {
	byLabel := make(map[string]*p2pstreamv1.DashboardWindowSummary, len(windows))
	for _, window := range windows {
		byLabel[window.Label] = window
	}
	return byLabel
}

func diagnosticsStatusCounts(rows []*p2pstreamv1.DashboardStatusCodeSummary) map[int64]int64 {
	counts := make(map[int64]int64, len(rows))
	for _, row := range rows {
		counts[row.StatusCode] = row.Requests
	}
	return counts
}

func dashboardDimensionHasLabel(rows []*p2pstreamv1.DashboardProxyDimensionSummary, label string) bool {
	for _, row := range rows {
		if row.Label == label {
			return true
		}
	}
	return false
}

func countRows(t *testing.T, database *db.DB, query string, args ...any) int64 {
	t.Helper()
	var count int64
	if err := database.QueryRowContext(context.Background(), query, args...).Scan(&count); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	return count
}

func sqlNullInt64(value int64) sql.NullInt64 {
	return sql.NullInt64{Int64: value, Valid: true}
}

func createDashboardUptimeAgent(t *testing.T, database *db.DB, publicID string, createdAt time.Time) db.Agent {
	t.Helper()
	agent, err := database.CreateAgent(context.Background(), db.CreateAgentParams{
		PublicID:  publicID,
		Name:      publicID,
		TokenHash: "hash-" + publicID,
		Enabled:   1,
	})
	if err != nil {
		t.Fatalf("create uptime agent: %v", err)
	}
	if _, err := database.ExecContext(context.Background(), `UPDATE agents SET created_at = ?, updated_at = ? WHERE id = ?`, createdAt.UTC(), createdAt.UTC(), agent.ID); err != nil {
		t.Fatalf("update uptime agent created_at: %v", err)
	}
	agent.CreatedAt = createdAt.UTC()
	return agent
}

func insertDashboardConnection(t *testing.T, database *db.DB, agentID int64, connectedAt time.Time, disconnectedAt sql.NullTime) int64 {
	t.Helper()
	row := database.QueryRowContext(context.Background(), `
		INSERT INTO connections (agent_id, connected_at, disconnected_at)
		VALUES (?, ?, ?)
		RETURNING id`,
		sql.NullInt64{Int64: agentID, Valid: true},
		connectedAt.UTC(),
		disconnectedAt,
	)
	var id int64
	if err := row.Scan(&id); err != nil {
		t.Fatalf("insert dashboard connection: %v", err)
	}
	return id
}

func setDashboardAgentTimes(t *testing.T, database *db.DB, agentID int64, lastConnectedAt, lastDisconnectedAt sql.NullTime) {
	t.Helper()
	if lastConnectedAt.Valid {
		lastConnectedAt.Time = lastConnectedAt.Time.UTC()
	}
	if lastDisconnectedAt.Valid {
		lastDisconnectedAt.Time = lastDisconnectedAt.Time.UTC()
	}
	if _, err := database.ExecContext(context.Background(), `
		UPDATE agents
		SET last_connected_at = ?,
		    last_disconnected_at = ?,
		    updated_at = CURRENT_TIMESTAMP
		WHERE id = ?`,
		lastConnectedAt,
		lastDisconnectedAt,
		agentID,
	); err != nil {
		t.Fatalf("update dashboard agent times: %v", err)
	}
}

func dashboardUptimeSummaryByAgentID(t *testing.T, summaries []*p2pstreamv1.AgentUptimeSummary, agentID int64) *p2pstreamv1.AgentUptimeSummary {
	t.Helper()
	for _, summary := range summaries {
		if summary.GetAgentId() == agentID {
			return summary
		}
	}
	t.Fatalf("agent uptime summary %d not found", agentID)
	return nil
}

func assertDashboardFloatClose(t *testing.T, got, want float64) {
	t.Helper()
	if got < want-0.000001 || got > want+0.000001 {
		t.Fatalf("float = %f, want %f", got, want)
	}
}

func seedDashboardRollupDimensionFixtures(t *testing.T, database *db.DB) {
	t.Helper()
	insertDashboardRollupAgentFixture(t, database, 1)
	if _, err := database.ExecContext(
		context.Background(),
		`INSERT INTO public_listeners (id, name, bind_address, port, protocol, enabled) VALUES
			(1, 'listener-one', '127.0.0.1', 18080, 'http', 1)`,
	); err != nil {
		t.Fatalf("insert dashboard listener fixtures: %v", err)
	}
	if _, err := database.ExecContext(
		context.Background(),
		`INSERT INTO public_routes (id, listener_id, priority, host_pattern, path_prefix, target_load_balancing, action, enabled) VALUES
			(1, 1, 10, 'example.com', '/api', 'round_robin', 'forward', 1)`,
	); err != nil {
		t.Fatalf("insert dashboard route fixtures: %v", err)
	}
	if _, err := database.ExecContext(
		context.Background(),
		`INSERT INTO public_route_targets (id, route_id, name, position, target_type, url, transport, enabled) VALUES
			(1, 1, 'target-one', 0, 'proxy', 'http://target-one.local', 'direct', 1)`,
	); err != nil {
		t.Fatalf("insert dashboard route target fixtures: %v", err)
	}
}

func insertDashboardRollupAgentFixture(t *testing.T, database *db.DB, id int64) {
	t.Helper()
	publicID := fmt.Sprintf("agent-%d-public", id)
	name := fmt.Sprintf("agent-%d", id)
	if id == 1 {
		publicID = "agent-one-public"
		name = "agent-one"
	}
	if _, err := database.ExecContext(
		context.Background(),
		`INSERT INTO agents (id, public_id, name, token_hash, enabled)
		VALUES (?, ?, ?, ?, 1)`,
		id,
		publicID,
		name,
		"hash-"+publicID,
	); err != nil {
		t.Fatalf("insert dashboard agent fixture: %v", err)
	}
}

func insertDashboardRollupProxyEvent(
	t *testing.T,
	database *db.DB,
	occurredAt time.Time,
	statusCode int,
	durationMs int64,
	errorKind string,
	listenerID sql.NullInt64,
	routeTargetID sql.NullInt64,
	routeID sql.NullInt64,
	agentID sql.NullInt64,
	requestBytes int64,
	responseBytes int64,
) {
	t.Helper()
	if _, err := database.ExecContext(
		context.Background(),
		`INSERT INTO proxy_request_events (
			occurred_at, status_code, duration_ms, error_kind, listener_id, route_target_id, route_id,
			agent_id, request_bytes, response_bytes
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		occurredAt,
		statusCode,
		durationMs,
		errorKind,
		listenerID,
		routeTargetID,
		routeID,
		agentID,
		requestBytes,
		responseBytes,
	); err != nil {
		t.Fatalf("insert proxy event with ids: %v", err)
	}
}

func insertDashboardRollupAgentStat(
	t *testing.T,
	database *db.DB,
	reportedAt time.Time,
	memoryMb int64,
	goroutines int64,
	reqSuccess int64,
	reqClientError int64,
	reqServerError int64,
	reqInternalError int64,
	bytesRx int64,
	bytesTx int64,
) {
	t.Helper()
	if _, err := database.ExecContext(
		context.Background(),
		`INSERT INTO agent_stats (
			reported_at, memory_mb, goroutines, req_success, req_client_error, req_server_error,
			req_internal_error, bytes_rx, bytes_tx
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		reportedAt,
		memoryMb,
		goroutines,
		reqSuccess,
		reqClientError,
		reqServerError,
		reqInternalError,
		bytesRx,
		bytesTx,
	); err != nil {
		t.Fatalf("insert agent stat: %v", err)
	}
}

func resetRollupStateToRawMax(t *testing.T, database *db.DB) {
	t.Helper()
	if _, err := database.ExecContext(context.Background(), `
		UPDATE observability_rollup_state
		SET proxy_backfill_upper_id = CAST(COALESCE((SELECT MAX(id) FROM proxy_request_events), 0) AS INTEGER),
		    proxy_backfilled_through_id = 0,
		    agent_backfill_upper_id = CAST(COALESCE((SELECT MAX(id) FROM agent_stats), 0) AS INTEGER),
		    agent_backfilled_through_id = 0,
		    updated_at = CURRENT_TIMESTAMP
		WHERE id = 1
	`); err != nil {
		t.Fatalf("reset rollup state: %v", err)
	}
}

func insertRollupEvent(t *testing.T, app *App, statusCode int, errorKind string) {
	t.Helper()
	if err := app.insertProxyRequestEventWithRollups(context.Background(), db.InsertProxyRequestEventAtParams{
		OccurredAt:   time.Now().UTC(),
		StatusCode:   int64(statusCode),
		DurationMs:   10,
		ErrorKind:    errorKind,
		RequestBytes: 1,
	}); err != nil {
		t.Fatalf("insert rollup event: %v", err)
	}
}

func insertRollupEventAt(
	t *testing.T,
	app *App,
	occurredAt time.Time,
	statusCode int,
	errorKind string,
	listenerID int64,
	routeTargetID int64,
	routeID int64,
	agentID int64,
	cacheStatus string,
	cacheBytes uint64,
	requestBytes uint64,
	responseBytes uint64,
	durationMs int64,
) {
	t.Helper()
	if err := app.insertProxyRequestEventWithRollups(context.Background(), db.InsertProxyRequestEventAtParams{
		OccurredAt:    occurredAt.UTC(),
		StatusCode:    int64(statusCode),
		DurationMs:    durationMs,
		ErrorKind:     errorKind,
		ListenerID:    sql.NullInt64{Int64: listenerID, Valid: true},
		RouteID:       sql.NullInt64{Int64: routeID, Valid: true},
		RouteTargetID: sql.NullInt64{Int64: routeTargetID, Valid: true},
		AgentID:       sql.NullInt64{Int64: agentID, Valid: true},
		CacheStatus:   cacheStatus,
		CacheBytes:    int64FromUint64(cacheBytes),
		RequestBytes:  int64FromUint64(requestBytes),
		ResponseBytes: int64FromUint64(responseBytes),
	}); err != nil {
		t.Fatalf("insert rollup event: %v", err)
	}
}

func insertDiagnosticsProxyEvent(
	t *testing.T,
	app *App,
	occurredAt time.Time,
	statusCode int,
	errorKind string,
	requestContext proxyRequestContext,
	listenerID sql.NullInt64,
	routeID sql.NullInt64,
	routeTargetID sql.NullInt64,
	agentID sql.NullInt64,
	requestBytes uint64,
	responseBytes uint64,
	durationMs int64,
) {
	t.Helper()
	if err := app.insertProxyRequestEventWithRollups(context.Background(), db.InsertProxyRequestEventAtParams{
		OccurredAt:    occurredAt.UTC(),
		StatusCode:    int64(statusCode),
		DurationMs:    durationMs,
		ErrorKind:     errorKind,
		Method:        requestContext.Method,
		Host:          requestContext.Host,
		PathPrefix:    requestContext.PathPrefix,
		ListenerID:    listenerID,
		RouteID:       routeID,
		RouteTargetID: routeTargetID,
		AgentID:       agentID,
		RequestBytes:  int64FromUint64(requestBytes),
		ResponseBytes: int64FromUint64(responseBytes),
	}); err != nil {
		t.Fatalf("insert diagnostics proxy event: %v", err)
	}
}

func insertPublicListenerRow(t *testing.T, app *App, name string) int64 {
	t.Helper()
	var id int64
	if err := app.DB.QueryRowContext(context.Background(), `
		INSERT INTO public_listeners (name, bind_address, port, protocol, enabled)
		VALUES (?, '127.0.0.1', 19081, 'http', 1)
		RETURNING id
	`, name).Scan(&id); err != nil {
		t.Fatalf("insert public listener: %v", err)
	}
	return id
}

func insertPublicRouteRow(t *testing.T, app *App, listenerID int64) int64 {
	t.Helper()
	var id int64
	if err := app.DB.QueryRowContext(context.Background(), `
		INSERT INTO public_routes (listener_id, priority, host_pattern, path_prefix, target_load_balancing, enabled)
		VALUES (?, 10, 'rollup.example', '/api', 'round_robin', 1)
		RETURNING id
	`, listenerID).Scan(&id); err != nil {
		t.Fatalf("insert public route: %v", err)
	}
	return id
}

func insertPublicRouteTargetRow(t *testing.T, app *App, routeID int64, name string) int64 {
	t.Helper()
	var id int64
	if err := app.DB.QueryRowContext(context.Background(), `
		INSERT INTO public_route_targets (route_id, name, position, target_type, url, transport, enabled)
		VALUES (?, ?, 0, 'proxy', ?, 'direct', 1)
		RETURNING id
	`, routeID, name, "http://"+name+".local").Scan(&id); err != nil {
		t.Fatalf("insert public route target: %v", err)
	}
	return id
}

func publicConfigHasTarget(resp *p2pstreamv1.GetPublicProxyConfigResponse, name string) bool {
	for _, target := range resp.RouteTargets {
		if target.Name == name {
			return true
		}
	}
	return false
}
