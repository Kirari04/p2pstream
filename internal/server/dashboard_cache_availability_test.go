package server

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/internal/db"
)

type dashboardCacheQueryDBTX struct {
	db.DBTX
	queries int
	before  func(string, []any) error
}

func (d *dashboardCacheQueryDBTX) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	d.queries++
	if d.before != nil {
		if err := d.before(query, args); err != nil {
			return nil, err
		}
	}
	return d.DBTX.QueryContext(ctx, query, args...)
}

func TestDashboardCacheQueriesOnlyNeededTupleWindow(t *testing.T) {
	ctx := context.Background()
	app := NewApp(nil, newServerTestDB(t))
	now := time.Date(2026, 10, 22, 12, 34, 20, 0, time.UTC)
	topSince := rollupBucketUnixMillis(now.Add(-dashboardTopWindow))
	longSince := rollupBucketUnixMillis(now.Add(-30 * 24 * time.Hour))

	// Retained history can contain many dimension tuples per minute. Its
	// totals still belong in the long windows, but not in the top dimensions.
	for i := int64(1); i <= 128; i++ {
		if err := app.insertProxyRequestEventWithRollups(ctx, db.InsertProxyRequestEventAtParams{
			OccurredAt: now.Add(-29 * 24 * time.Hour), StatusCode: http.StatusOK,
			RouteTargetID: sql.NullInt64{Int64: i, Valid: true},
		}); err != nil {
			t.Fatal(err)
		}
	}
	for _, at := range []time.Time{time.UnixMilli(topSince - 60_000), time.UnixMilli(topSince), now} {
		if err := app.insertProxyRequestEventWithRollups(ctx, db.InsertProxyRequestEventAtParams{
			OccurredAt: at, StatusCode: http.StatusOK,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := app.insertAgentStatWithRollup(ctx, db.InsertAgentStatAtParams{ReportedAt: now.Add(-29 * 24 * time.Hour)}); err != nil {
		t.Fatal(err)
	}

	seen := make(map[string]bool)
	spy := &dashboardCacheQueryDBTX{DBTX: app.DB.DB}
	spy.before = func(query string, args []any) error {
		for name, want := range map[string]int64{
			"ListProxyRequestRollupMinutesSince":      longSince,
			"ListAgentStatRollupMinutesSince":         longSince,
			"ListProxyRequestTupleRollupMinutesSince": topSince,
		} {
			if strings.HasPrefix(query, "-- name: "+name+" ") {
				seen[name] = true
				if len(args) != 1 || args[0] != want {
					t.Errorf("%s cutoff = %v, want [%d]", name, args, want)
				}
			}
		}
		return nil
	}
	app.DB.Queries = db.New(spy)
	resp, err := app.buildDashboardCacheSnapshot(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != 3 {
		t.Fatalf("rollup queries observed = %v, want all three", seen)
	}
	if window := dashboardTestWindow(t, resp.Windows, "30d"); window.ProxyRequests != 131 || window.AgentSamples != 1 {
		t.Fatalf("long-window history lost: %+v", window)
	}
	if window := dashboardTestWindow(t, resp.Windows, "1h"); window.ProxyRequests != 2 {
		t.Fatalf("1h boundary requests = %d, want 2", window.ProxyRequests)
	}
	if len(resp.TopRouteTargets) != 1 || resp.TopRouteTargets[0].Requests != 2 {
		t.Fatalf("top targets must include only boundary/current traffic: %+v", resp.TopRouteTargets)
	}
}

func TestDashboardCacheFailureIsUnavailableUntilRecovery(t *testing.T) {
	ctx := context.Background()
	app := NewApp(nil, newServerTestDB(t))
	header := createTestAdminSession(t, app)
	insertRollupEvent(t, app, http.StatusOK, "")
	app.DashboardCache.started.Store(true)

	failure := errors.New("private database failure detail")
	spy := &dashboardCacheQueryDBTX{DBTX: app.DB.DB, before: func(query string, _ []any) error {
		if strings.HasPrefix(query, "-- name: ListAgentStatRollupMinutesSince ") {
			return failure
		}
		return nil
	}}
	app.DB.Queries = db.New(spy)
	_, snapshotErr := app.buildDashboardCacheSnapshot(ctx, time.Now().UTC())
	if !errors.Is(snapshotErr, failure) {
		t.Fatalf("snapshot error must preserve cause: %v", snapshotErr)
	}
	app.refreshDashboardCache(ctx)
	if _, ok := app.DashboardCache.clone(); ok {
		t.Fatal("failed first refresh stored a snapshot")
	}
	var requests int64
	if err := app.DB.QueryRowContext(ctx, "SELECT SUM(requests) FROM proxy_request_rollup_minutes").Scan(&requests); err != nil || requests != 1 {
		t.Fatalf("persisted requests = %d, %v; want 1", requests, err)
	}

	req := connect.NewRequest(&p2pstreamv1.GetDashboardRequest{})
	req.Header().Set("Cookie", header.Get("Cookie"))
	queriesBefore := spy.queries
	for i := 0; i < 2; i++ {
		resp, err := app.GetDashboard(ctx, req)
		if connect.CodeOf(err) != connect.CodeUnavailable || resp != nil {
			t.Fatalf("failed cache dashboard = %v, %v; want unavailable without response", resp, err)
		}
		if strings.Contains(err.Error(), failure.Error()) {
			t.Fatalf("dashboard leaked database failure: %v", err)
		}
	}
	if spy.queries != queriesBefore {
		t.Fatal("dashboard request synchronously queried snapshot data")
	}
	if !strings.Contains(snapshotErr.Error(), "dashboard agent rollup minutes") {
		t.Fatalf("snapshot error must identify the failing stage: %v", snapshotErr)
	}

	spy.before = nil
	app.DashboardCache.mu.Lock()
	app.DashboardCache.lastFailureAt = time.Time{} // Advance past backoff without sleeping.
	app.DashboardCache.mu.Unlock()
	app.refreshDashboardCache(ctx)
	recovered, err := app.GetDashboard(ctx, req)
	if err != nil {
		t.Fatalf("dashboard did not recover: %v", err)
	}
	if window := dashboardTestWindow(t, recovered.Msg.Windows, "5m"); window.ProxyRequests != 1 {
		t.Fatalf("recovered requests = %d, want persisted request", window.ProxyRequests)
	}

	// A later refresh failure retains the useful successful snapshot and
	// its timestamp while current status continues to update.
	insertRollupEvent(t, app, http.StatusOK, "")
	spy.before = func(string, []any) error { return failure }
	app.refreshDashboardCache(ctx)
	app.ProxyIsRunning.Store(true)
	cached, err := app.GetDashboard(ctx, req)
	if err != nil {
		t.Fatalf("valid cached dashboard unavailable after refresh failure: %v", err)
	}
	if dashboardTestWindow(t, cached.Msg.Windows, "5m").ProxyRequests != 1 || cached.Msg.GeneratedAtUnixMillis != recovered.Msg.GeneratedAtUnixMillis {
		t.Fatal("refresh failure replaced the successful snapshot")
	}
	if !cached.Msg.GetStatus().GetProxyRunning() {
		t.Fatal("cached snapshot did not receive live status overlay")
	}
}

func TestDashboardCacheSuccessfulEmptySnapshotReturnsZeroTraffic(t *testing.T) {
	ctx := context.Background()
	app := NewApp(nil, newServerTestDB(t))
	header := createTestAdminSession(t, app)
	app.DashboardCache.started.Store(true)
	app.refreshDashboardCache(ctx)
	req := connect.NewRequest(&p2pstreamv1.GetDashboardRequest{})
	req.Header().Set("Cookie", header.Get("Cookie"))
	resp, err := app.GetDashboard(ctx, req)
	if err != nil {
		t.Fatalf("successful empty snapshot must be available: %v", err)
	}
	if len(resp.Msg.Windows) != len(dashboardWindows) || len(resp.Msg.TrafficBuckets) != 0 {
		t.Fatalf("unexpected empty snapshot: %+v", resp.Msg)
	}
	for _, window := range resp.Msg.Windows {
		if window.ProxyRequests != 0 || window.AgentSamples != 0 {
			t.Fatalf("empty snapshot contains traffic: %+v", window)
		}
	}
}
