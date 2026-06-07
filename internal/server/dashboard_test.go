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
	insertDashboardRollupProxyEvent(t, app.DB, now.Add(-2*time.Minute), http.StatusOK, 100, "", sqlNullInt64(1), sql.NullInt64{}, sqlNullInt64(1), sqlNullInt64(1), sqlNullInt64(1), 10, 100)
	insertDashboardRollupProxyEvent(t, app.DB, now.Add(-30*time.Minute), http.StatusBadGateway, 1300, "agent_timeout", sqlNullInt64(1), sql.NullInt64{}, sqlNullInt64(1), sql.NullInt64{}, sql.NullInt64{}, 20, 200)
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
		AllocAllocated:   256,
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
			occurred_at, status_code, duration_ms, error_kind, listener_id, backend_id, route_target_id, route_id,
			agent_id, request_bytes, response_bytes
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		occurredAt,
		statusCode,
		durationMs,
		errorKind,
		listenerID,
		backendID,
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
		BackendID:     sql.NullInt64{},
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
