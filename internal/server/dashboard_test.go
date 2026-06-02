package server

import (
	"context"
	"database/sql"
	"net/http"
	"testing"
	"time"

	"connectrpc.com/connect"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/internal/config"
	"p2pstream/internal/db"
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
		sqlNullInt64(8),
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

	var listenerID, backendID, routeID, agentID, statusClass int64
	var errorKind string
	if err := app.DB.QueryRowContext(ctx, `
		SELECT listener_id, backend_id, route_id, agent_id, error_kind, status_class
		FROM proxy_request_tuple_rollup_minutes
	`).Scan(&listenerID, &backendID, &routeID, &agentID, &errorKind, &statusClass); err != nil {
		t.Fatalf("read proxy tuple rollup: %v", err)
	}
	if listenerID != 7 || backendID != 8 || routeID != 9 || agentID != 10 || errorKind != "agent_timeout" || statusClass != 5 {
		t.Fatalf("unexpected tuple rollup dimensions: listener=%d backend=%d route=%d agent=%d error=%q status_class=%d", listenerID, backendID, routeID, agentID, errorKind, statusClass)
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
	// A second pass must not double-count already backfilled rows.
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

func sqlNullInt64(value int64) sql.NullInt64 {
	return sql.NullInt64{Int64: value, Valid: true}
}

func seedDashboardRollupDimensionFixtures(t *testing.T, database *db.DB) {
	t.Helper()
	insertDashboardRollupAgentFixture(t, database, 1)
	if _, err := database.ExecContext(
		context.Background(),
		`INSERT INTO public_backends (id, name, target_origin, backend_type, enabled) VALUES
			(1, 'backend-one', 'http://backend-one.local', 'proxy_forward', 1)`,
	); err != nil {
		t.Fatalf("insert dashboard backend fixtures: %v", err)
	}
	if _, err := database.ExecContext(
		context.Background(),
		`INSERT INTO public_listeners (id, name, bind_address, port, protocol, enabled, default_backend_id) VALUES
			(1, 'listener-one', '127.0.0.1', 18080, 'http', 1, 1)`,
	); err != nil {
		t.Fatalf("insert dashboard listener fixtures: %v", err)
	}
	if _, err := database.ExecContext(
		context.Background(),
		`INSERT INTO public_routes (id, listener_id, priority, host_pattern, path_prefix, backend_id, action, enabled) VALUES
			(1, 1, 10, 'example.com', '/api', 1, 'forward', 1)`,
	); err != nil {
		t.Fatalf("insert dashboard route fixtures: %v", err)
	}
}

func insertDashboardRollupAgentFixture(t *testing.T, database *db.DB, id int64) {
	t.Helper()
	if _, err := database.ExecContext(
		context.Background(),
		`INSERT INTO agents (id, public_id, name, token_hash, enabled)
		VALUES (?, ?, ?, ?, 1)`,
		id,
		"agent-one-public",
		"agent-one",
		"hash-one",
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
	backendID sql.NullInt64,
	routeID sql.NullInt64,
	agentID sql.NullInt64,
	requestBytes int64,
	responseBytes int64,
) {
	t.Helper()
	if _, err := database.ExecContext(
		context.Background(),
		`INSERT INTO proxy_request_events (
			occurred_at, status_code, duration_ms, error_kind, listener_id, backend_id, route_id,
			agent_id, request_bytes, response_bytes
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		occurredAt,
		statusCode,
		durationMs,
		errorKind,
		listenerID,
		backendID,
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
