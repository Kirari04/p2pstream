package main_test

import (
	"bytes"
	"context"
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"

	"connectrpc.com/connect"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/internal/config"
	"p2pstream/internal/db"
	"p2pstream/internal/server"
)

func TestE2E_GetDashboardRequiresSession(t *testing.T) {
	app := server.NewApp(&config.Config{}, newTestDB(t))
	_, client := newTestManagementClient(t, app)
	createAdminSession(t, client)

	_, err := client.GetDashboard(context.Background(), connect.NewRequest(&p2pstreamv1.GetDashboardRequest{}))
	requireConnectCode(t, err, connect.CodeUnauthenticated)
}

func TestE2E_GetDashboardSummaries(t *testing.T) {
	database := newTestDB(t)
	app := server.NewApp(&config.Config{
		ObservabilityRetentionDays: 30,
	}, database)
	_, client := newTestManagementClient(t, app)
	cookie := createAdminSession(t, client)

	now := time.Now().UTC()
	insertAgentStatAt(t, database, now.Add(-2*time.Minute), 100, 8, 10, 1, 2, 3, 1000, 2000)
	insertAgentStatAt(t, database, now.Add(-30*time.Minute), 150, 10, 20, 2, 3, 4, 3000, 4000)
	insertAgentStatAt(t, database, now.Add(-2*time.Hour), 200, 12, 30, 3, 4, 5, 5000, 6000)

	seedDashboardDimensionFixtures(t, database)
	insertProxyEventWithIDsAt(t, database, now.Add(-2*time.Minute), http.StatusOK, 100, "", validInt64(1), validInt64(1), validInt64(1), validInt64(1), 10, 100)
	insertProxyEventWithIDsAt(t, database, now.Add(-30*time.Minute), http.StatusNotFound, 200, "", validInt64(1), validInt64(1), sql.NullInt64{}, sql.NullInt64{}, 20, 200)
	insertProxyEventWithIDsAt(t, database, now.Add(-2*time.Hour), http.StatusBadGateway, 1300, "", validInt64(2), validInt64(2), sql.NullInt64{}, sql.NullInt64{}, 30, 300)
	insertProxyEventWithIDsAt(t, database, now.Add(-2*time.Minute), http.StatusGatewayTimeout, 1400, "agent_timeout", validInt64(2), validInt64(2), sql.NullInt64{}, sql.NullInt64{}, 40, 400)

	req := connect.NewRequest(&p2pstreamv1.GetDashboardRequest{})
	req.Header().Set("Cookie", cookie)
	resp, err := client.GetDashboard(context.Background(), req)
	if err != nil {
		t.Fatalf("get dashboard: %v", err)
	}

	if resp.Msg.Status == nil {
		t.Fatal("expected dashboard status")
	}
	if resp.Msg.RetentionDays != 30 {
		t.Fatalf("expected retention days 30, got %d", resp.Msg.RetentionDays)
	}

	windows := dashboardWindowsByLabel(resp.Msg.Windows)
	for _, label := range []string{"5m", "1h", "24h", "30d"} {
		if windows[label] == nil {
			t.Fatalf("expected dashboard window %q", label)
		}
	}

	fiveMinutes := windows["5m"]
	if fiveMinutes.ProxyRequests != 2 {
		t.Fatalf("expected 2 proxy requests in 5m, got %d", fiveMinutes.ProxyRequests)
	}
	if fiveMinutes.ProxySuccess != 1 || fiveMinutes.ProxyServerError != 1 || fiveMinutes.ProxyInternalError != 1 {
		t.Fatalf("unexpected 5m proxy counts: %+v", fiveMinutes)
	}
	if fiveMinutes.AgentSamples != 1 || fiveMinutes.AgentReqSuccess != 10 || fiveMinutes.AgentReqInternalError != 3 {
		t.Fatalf("unexpected 5m agent counts: %+v", fiveMinutes)
	}
	if fiveMinutes.AgentBytesReceived != 1000 || fiveMinutes.AgentBytesSent != 2000 {
		t.Fatalf("unexpected 5m bytes: %+v", fiveMinutes)
	}
	if fiveMinutes.ProxyRequestBytes != 50 || fiveMinutes.ProxyResponseBytes != 500 || fiveMinutes.ProxyTotalBytes != 550 {
		t.Fatalf("unexpected 5m proxy bytes: %+v", fiveMinutes)
	}
	if fiveMinutes.ProxyAvgRequestBytes != 25 || fiveMinutes.ProxyAvgResponseBytes != 250 || fiveMinutes.ProxyMaxDurationMs != 1400 || fiveMinutes.ProxySlowRequests != 1 {
		t.Fatalf("unexpected 5m proxy byte/latency summary: %+v", fiveMinutes)
	}

	oneHour := windows["1h"]
	if oneHour.ProxyRequests != 3 || oneHour.ProxyClientError != 1 || oneHour.ProxyAvgDurationMs != 566 {
		t.Fatalf("unexpected 1h proxy summary: %+v", oneHour)
	}
	if oneHour.ProxyRequestBytes != 70 || oneHour.ProxyResponseBytes != 700 || oneHour.ProxyAvgRequestBytes != 23 || oneHour.ProxyAvgResponseBytes != 233 {
		t.Fatalf("unexpected 1h proxy byte summary: %+v", oneHour)
	}
	if oneHour.AgentSamples != 2 || oneHour.AgentReqSuccess != 30 || oneHour.AgentAvgMemoryMb != 125 {
		t.Fatalf("unexpected 1h agent summary: %+v", oneHour)
	}

	day := windows["24h"]
	if day.ProxyRequests != 4 || day.ProxyServerError != 2 || day.AgentReqSuccess != 60 {
		t.Fatalf("unexpected 24h summary: %+v", day)
	}
	if day.ProxyRequestBytes != 100 || day.ProxyResponseBytes != 1000 || day.ProxyMaxDurationMs != 1400 || day.ProxySlowRequests != 2 {
		t.Fatalf("unexpected 24h proxy byte/latency summary: %+v", day)
	}
	if day.AgentMaxMemoryMb != 200 || day.AgentMaxGoroutines != 12 {
		t.Fatalf("unexpected 24h max health summary: %+v", day)
	}
	if len(resp.Msg.TopListeners) < 2 || resp.Msg.TopListeners[0].Label != "listener-one" || resp.Msg.TopListeners[0].Requests != 2 {
		t.Fatalf("unexpected top listeners: %+v", resp.Msg.TopListeners)
	}
	if len(resp.Msg.TopBackends) < 2 || resp.Msg.TopBackends[0].Label != "backend-one" || resp.Msg.TopBackends[0].Requests != 2 {
		t.Fatalf("unexpected top backends: %+v", resp.Msg.TopBackends)
	}
	if len(resp.Msg.TopRoutes) < 2 || resp.Msg.TopRoutes[0].Label != "Default route" || resp.Msg.TopRoutes[0].Requests != 2 {
		t.Fatalf("unexpected top routes: %+v", resp.Msg.TopRoutes)
	}
	if len(resp.Msg.TopAgents) != 1 || resp.Msg.TopAgents[0].Label != "agent-one" || resp.Msg.TopAgents[0].Requests != 1 {
		t.Fatalf("unexpected top agents: %+v", resp.Msg.TopAgents)
	}
	if len(resp.Msg.TopErrorKinds) != 1 || resp.Msg.TopErrorKinds[0].Label != "agent_timeout" || resp.Msg.TopErrorKinds[0].Requests != 1 {
		t.Fatalf("unexpected top error kinds: %+v", resp.Msg.TopErrorKinds)
	}
	statusClasses := dashboardRowsByLabel(resp.Msg.StatusClasses)
	if statusClasses["2xx"] == nil || statusClasses["2xx"].Requests != 1 || statusClasses["4xx"] == nil || statusClasses["4xx"].Requests != 1 || statusClasses["5xx"] == nil || statusClasses["5xx"].Requests != 1 {
		t.Fatalf("unexpected status classes: %+v", resp.Msg.StatusClasses)
	}
	var bucketRequests int64
	var bucketRequestBytes uint64
	var bucketResponseBytes uint64
	for _, bucket := range resp.Msg.TrafficBuckets {
		bucketRequests += bucket.Requests
		bucketRequestBytes += bucket.RequestBytes
		bucketResponseBytes += bucket.ResponseBytes
	}
	if bucketRequests != 3 || bucketRequestBytes != 70 || bucketResponseBytes != 700 {
		t.Fatalf("unexpected traffic buckets: requests=%d request_bytes=%d response_bytes=%d buckets=%+v", bucketRequests, bucketRequestBytes, bucketResponseBytes, resp.Msg.TrafficBuckets)
	}
}

func TestProxyRequestEventRecordedCountsOnly(t *testing.T) {
	targetMux := http.NewServeMux()
	targetMux.HandleFunc("/request-event", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
		io.Copy(w, r.Body)
	})
	targetSrv := httptest.NewServer(targetMux)
	defer targetSrv.Close()

	database := newTestDB(t)
	listener := seedTestHTTPPublicListener(t, database, targetSrv.URL)
	app := server.NewApp(&config.Config{
		BootstrapAgentID:    "observability-agent",
		BootstrapAgentName:  "Observability Agent",
		BootstrapAgentToken: "observability-token",
	}, database)
	status, err := app.StartProxyListener(context.Background())
	if err != nil {
		t.Fatalf("start proxy listener: %v", err)
	}
	proxyURL := "http://" + publicListenerBoundAddress(t, status, listener.ID)
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = app.StopProxyListener(shutdownCtx)
	})

	mgmtMux := http.NewServeMux()
	app.RegisterManagementRoutes(mgmtMux)

	p := new(http.Protocols)
	p.SetHTTP1(true)
	p.SetUnencryptedHTTP2(true)
	mgmtSrv := httptest.NewUnstartedServer(mgmtMux)
	mgmtSrv.Config.Protocols = p
	mgmtSrv.Start()
	defer mgmtSrv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	agentDone := make(chan struct{})
	go func() {
		_ = runAgent(ctx, "ws"+mgmtSrv.URL[4:]+"/ws", "observability-agent", "observability-token")
		close(agentDone)
	}()

	time.Sleep(200 * time.Millisecond)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, proxyURL+"/request-event", bytes.NewReader([]byte("payload")))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("proxy request: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}

	summary, err := database.GetProxyRequestSummarySince(context.Background(), time.Now().UTC().Add(-time.Minute))
	if err != nil {
		t.Fatalf("get proxy request summary: %v", err)
	}
	if summary.TotalRequests != 1 || summary.Success != 1 {
		t.Fatalf("expected one successful proxy event, got %+v", summary)
	}
	if summary.RequestBytes != 7 || summary.ResponseBytes != 9 || summary.TotalBytes != 16 {
		t.Fatalf("expected recorded request/response bytes, got %+v", summary)
	}

	columns := proxyRequestEventColumns(t, database)
	expected := []string{"agent_id", "backend_id", "duration_ms", "error_kind", "id", "listener_id", "occurred_at", "request_bytes", "response_bytes", "route_id", "status_code", "waf_action", "waf_rule_id"}
	if !equalStringSlices(columns, expected) {
		t.Fatalf("proxy_request_events columns changed: got %v, want %v", columns, expected)
	}

	cancel()
	<-agentDone
}

func TestObservabilityRetentionCleanup(t *testing.T) {
	database := newTestDB(t)
	app := server.NewApp(&config.Config{ObservabilityRetentionDays: 30}, database)
	_, client := newTestManagementClient(t, app)
	cookie := createAdminSession(t, client)

	now := time.Now().UTC()
	insertProxyEventAt(t, database, now.AddDate(0, 0, -31), http.StatusOK, 100, "")
	insertProxyEventAt(t, database, now.Add(-time.Hour), http.StatusOK, 100, "")
	insertAgentStatAt(t, database, now.AddDate(0, 0, -31), 100, 8, 1, 0, 0, 0, 1, 1)
	insertAgentStatAt(t, database, now.Add(-time.Hour), 100, 8, 1, 0, 0, 0, 1, 1)

	oldDisconnected := now.AddDate(0, 0, -31)
	if _, err := database.ExecContext(
		context.Background(),
		`INSERT INTO connections (connected_at, disconnected_at) VALUES (?, ?)`,
		oldDisconnected.Add(-time.Hour),
		oldDisconnected,
	); err != nil {
		t.Fatalf("insert old disconnected connection: %v", err)
	}
	if _, err := database.ExecContext(
		context.Background(),
		`INSERT INTO connections (connected_at, disconnected_at) VALUES (?, NULL)`,
		now.AddDate(0, 0, -31),
	); err != nil {
		t.Fatalf("insert active old connection: %v", err)
	}

	req := connect.NewRequest(&p2pstreamv1.GetDashboardRequest{})
	req.Header().Set("Cookie", cookie)
	if _, err := client.GetDashboard(context.Background(), req); err != nil {
		t.Fatalf("get dashboard: %v", err)
	}

	cutoff := time.Now().UTC().AddDate(0, 0, -30)
	if countRows(t, database, `SELECT COUNT(*) FROM proxy_request_events WHERE occurred_at < ?`, cutoff) != 0 {
		t.Fatal("expected old proxy events to be cleaned up")
	}
	if countRows(t, database, `SELECT COUNT(*) FROM proxy_request_events WHERE occurred_at >= ?`, cutoff) != 1 {
		t.Fatal("expected recent proxy event to remain")
	}
	if countRows(t, database, `SELECT COUNT(*) FROM agent_stats WHERE reported_at < ?`, cutoff) != 0 {
		t.Fatal("expected old agent stats to be cleaned up")
	}
	if countRows(t, database, `SELECT COUNT(*) FROM agent_stats WHERE reported_at >= ?`, cutoff) != 1 {
		t.Fatal("expected recent agent stat to remain")
	}
	if countRows(t, database, `SELECT COUNT(*) FROM connections WHERE disconnected_at IS NOT NULL AND disconnected_at < ?`, cutoff) != 0 {
		t.Fatal("expected old disconnected connection to be cleaned up")
	}
	if countRows(t, database, `SELECT COUNT(*) FROM connections WHERE disconnected_at IS NULL`) != 1 {
		t.Fatal("expected active connection to remain")
	}
}

func insertAgentStatAt(
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

func insertProxyEventAt(
	t *testing.T,
	database *db.DB,
	occurredAt time.Time,
	statusCode int,
	durationMs int64,
	errorKind string,
) {
	t.Helper()
	if _, err := database.ExecContext(
		context.Background(),
		`INSERT INTO proxy_request_events (
			occurred_at, status_code, duration_ms, error_kind
		) VALUES (?, ?, ?, ?)`,
		occurredAt,
		statusCode,
		durationMs,
		errorKind,
	); err != nil {
		t.Fatalf("insert proxy event: %v", err)
	}
}

func seedDashboardDimensionFixtures(t *testing.T, database *db.DB) {
	t.Helper()
	if _, err := database.ExecContext(
		context.Background(),
		`INSERT INTO agents (id, public_id, name, token_hash, enabled) VALUES
			(1, 'agent-one-public', 'agent-one', 'hash-one', 1)`,
	); err != nil {
		t.Fatalf("insert dashboard agent fixture: %v", err)
	}
	if _, err := database.ExecContext(
		context.Background(),
		`INSERT INTO public_backends (id, name, target_origin, backend_type, enabled) VALUES
			(1, 'backend-one', 'http://backend-one.local', 'proxy_forward', 1),
			(2, 'backend-two', 'http://backend-two.local', 'proxy_forward', 1)`,
	); err != nil {
		t.Fatalf("insert dashboard backend fixtures: %v", err)
	}
	if _, err := database.ExecContext(
		context.Background(),
		`INSERT INTO public_listeners (id, name, bind_address, port, protocol, enabled, default_backend_id) VALUES
			(1, 'listener-one', '127.0.0.1', 18080, 'http', 1, 1),
			(2, 'listener-two', '127.0.0.1', 18081, 'http', 1, 2)`,
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

func insertProxyEventWithIDsAt(
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

func validInt64(value int64) sql.NullInt64 {
	return sql.NullInt64{Int64: value, Valid: true}
}

func dashboardWindowsByLabel(windows []*p2pstreamv1.DashboardWindowSummary) map[string]*p2pstreamv1.DashboardWindowSummary {
	byLabel := make(map[string]*p2pstreamv1.DashboardWindowSummary, len(windows))
	for _, window := range windows {
		byLabel[window.Label] = window
	}
	return byLabel
}

func dashboardRowsByLabel(rows []*p2pstreamv1.DashboardProxyDimensionSummary) map[string]*p2pstreamv1.DashboardProxyDimensionSummary {
	byLabel := make(map[string]*p2pstreamv1.DashboardProxyDimensionSummary, len(rows))
	for _, row := range rows {
		byLabel[row.Label] = row
	}
	return byLabel
}

func proxyRequestEventColumns(t *testing.T, database *db.DB) []string {
	t.Helper()

	rows, err := database.QueryContext(context.Background(), `PRAGMA table_info(proxy_request_events)`)
	if err != nil {
		t.Fatalf("read proxy_request_events schema: %v", err)
	}
	defer rows.Close()

	var columns []string
	for rows.Next() {
		var cid int
		var name string
		var typ string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			t.Fatalf("scan table info: %v", err)
		}
		columns = append(columns, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read table info rows: %v", err)
	}
	sort.Strings(columns)
	return columns
}

func countRows(t *testing.T, database *db.DB, query string, args ...any) int64 {
	t.Helper()

	var count int64
	if err := database.QueryRowContext(context.Background(), query, args...).Scan(&count); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	return count
}

func equalStringSlices(a []string, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for idx := range a {
		if a[idx] != b[idx] {
			return false
		}
	}
	return true
}
