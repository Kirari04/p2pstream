package server

import (
	"context"
	"net/http"
	"testing"

	"connectrpc.com/connect"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/internal/db"
)

func TestGetDashboardIncludesCacheSummary(t *testing.T) {
	ctx := context.Background()
	app := NewApp(nil, newServerTestDB(t))
	header := createTestAdminSession(t, app)

	insertEvent := func(cacheStatus string, cacheBytes int64) {
		t.Helper()
		if err := app.DB.InsertProxyRequestEvent(ctx, db.InsertProxyRequestEventParams{
			StatusCode:  http.StatusOK,
			DurationMs:  10,
			CacheStatus: cacheStatus,
			CacheBytes:  cacheBytes,
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
