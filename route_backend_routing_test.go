package main_test

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"p2pstream/internal/config"
	"p2pstream/internal/db"
	"p2pstream/internal/server"
)

func TestRouteBackendPoolRoundRobinRouting(t *testing.T) {
	targetA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("backend-a"))
	}))
	defer targetA.Close()
	targetB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("backend-b"))
	}))
	defer targetB.Close()

	database := newTestDB(t)
	listener := seedTestHTTPPublicListener(t, database, targetA.URL)
	backendA := createDirectBackendRow(t, database, "pool-a", targetA.URL, true)
	backendB := createDirectBackendRow(t, database, "pool-b", targetB.URL, true)
	route := createRouteBackendPoolRow(t, database, listener.ID, "/pool", 0, backendA.ID, backendB.ID)

	app := server.NewApp(testManagementConfig(config.Config{}), database)
	status, err := app.StartProxyListener(context.Background())
	if err != nil {
		t.Fatalf("start proxy: %v", err)
	}
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = app.StopProxyListener(shutdownCtx)
	})

	baseURL := "http://" + publicListenerBoundAddress(t, status, listener.ID)
	got := []string{
		httpGetBody(t, baseURL+"/pool"),
		httpGetBody(t, baseURL+"/pool"),
		httpGetBody(t, baseURL+"/pool"),
		httpGetBody(t, baseURL+"/pool"),
	}
	want := []string{"backend-a", "backend-b", "backend-a", "backend-b"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("route %d body = %q, want %q (all=%v, route=%d)", i, got[i], want[i], got, route.ID)
		}
	}
}

func TestRouteBackendPoolFallbackAndNoFallback(t *testing.T) {
	fallbackSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("fallback"))
	}))
	defer fallbackSrv.Close()

	database := newTestDB(t)
	listener := seedTestHTTPPublicListener(t, database, fallbackSrv.URL)
	disabledBackend := createDirectBackendRow(t, database, "disabled-pool", fallbackSrv.URL, false)
	fallbackBackend := createDirectBackendRow(t, database, "route-fallback", fallbackSrv.URL, true)
	createRouteBackendPoolRow(t, database, listener.ID, "/fallback", fallbackBackend.ID, disabledBackend.ID)
	createRouteBackendPoolRow(t, database, listener.ID, "/unavailable", 0, disabledBackend.ID)

	app := server.NewApp(testManagementConfig(config.Config{}), database)
	status, err := app.StartProxyListener(context.Background())
	if err != nil {
		t.Fatalf("start proxy: %v", err)
	}
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = app.StopProxyListener(shutdownCtx)
	})

	baseURL := "http://" + publicListenerBoundAddress(t, status, listener.ID)
	if body := httpGetBody(t, baseURL+"/fallback"); body != "fallback" {
		t.Fatalf("fallback body = %q, want fallback", body)
	}
	resp, body := httpGet(t, baseURL+"/unavailable")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable || !strings.Contains(body, "No backend is available") {
		t.Fatalf("no fallback response status=%d body=%q", resp.StatusCode, body)
	}
}

func TestRouteBackendPassiveFailureSkipsLaterRequests(t *testing.T) {
	targetSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("healthy"))
	}))
	defer targetSrv.Close()

	database := newTestDB(t)
	listener := seedTestHTTPPublicListener(t, database, targetSrv.URL)
	badBackend := createDirectBackendRow(t, database, "bad-passive", "http://127.0.0.1:1", true)
	goodBackend := createDirectBackendRow(t, database, "good-passive", targetSrv.URL, true)
	createRouteBackendPoolRow(t, database, listener.ID, "/passive", 0, badBackend.ID, goodBackend.ID)

	app := server.NewApp(testManagementConfig(config.Config{}), database)
	status, err := app.StartProxyListener(context.Background())
	if err != nil {
		t.Fatalf("start proxy: %v", err)
	}
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = app.StopProxyListener(shutdownCtx)
	})

	baseURL := "http://" + publicListenerBoundAddress(t, status, listener.ID)
	resp, _ := httpGet(t, baseURL+"/passive")
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("first passive failure status = %d, want 502", resp.StatusCode)
	}
	if body := httpGetBody(t, baseURL+"/passive"); body != "healthy" {
		t.Fatalf("second passive request body = %q, want healthy", body)
	}
}

func createDirectBackendRow(t *testing.T, database *db.DB, name string, target string, enabled bool) db.PublicBackend {
	t.Helper()
	backend, err := database.CreatePublicBackend(context.Background(), db.CreatePublicBackendParams{
		Name:                          name,
		TargetOrigin:                  target,
		BackendType:                   "proxy_forward",
		ForwardMode:                   "direct",
		LoadBalancing:                 "round_robin",
		HealthCheckMethod:             "GET",
		HealthCheckPath:               "/",
		HealthCheckIntervalMillis:     10000,
		HealthCheckTimeoutMillis:      2000,
		HealthCheckHealthyThreshold:   2,
		HealthCheckUnhealthyThreshold: 2,
		HealthCheckExpectedStatusMin:  200,
		HealthCheckExpectedStatusMax:  399,
		Enabled:                       boolIntForTest(enabled),
	})
	if err != nil {
		t.Fatalf("create backend row %s: %v", name, err)
	}
	return backend
}

func createRouteBackendPoolRow(t *testing.T, database *db.DB, listenerID int64, path string, fallbackBackendID int64, backendIDs ...int64) db.PublicRoute {
	t.Helper()
	backendID := sql.NullInt64{}
	if len(backendIDs) > 0 {
		backendID = sql.NullInt64{Int64: backendIDs[0], Valid: true}
	}
	fallbackID := sql.NullInt64{}
	if fallbackBackendID > 0 {
		fallbackID = sql.NullInt64{Int64: fallbackBackendID, Valid: true}
	}
	route, err := database.CreatePublicRoute(context.Background(), db.CreatePublicRouteParams{
		ListenerID:                 listenerID,
		Priority:                   int64(10 + len(path)),
		PathPrefix:                 path,
		BackendID:                  backendID,
		LoadBalancing:              "round_robin",
		FallbackBackendID:          fallbackID,
		Action:                     "forward",
		RedirectStatusCode:         http.StatusFound,
		RedirectPreservePathSuffix: 1,
		RedirectPreserveQuery:      1,
		Enabled:                    1,
	})
	if err != nil {
		t.Fatalf("create route row: %v", err)
	}
	for index, backendID := range backendIDs {
		if _, err := database.CreatePublicRouteBackend(context.Background(), db.CreatePublicRouteBackendParams{
			RouteID:   route.ID,
			BackendID: backendID,
			Position:  int64(index),
			Weight:    100,
			Enabled:   1,
		}); err != nil {
			t.Fatalf("create route backend row: %v", err)
		}
	}
	return route
}

func httpGetBody(t *testing.T, url string) string {
	t.Helper()
	resp, body := httpGet(t, url)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s status = %d body=%q", url, resp.StatusCode, body)
	}
	return body
}

func httpGet(t *testing.T, url string) (*http.Response, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	body := mustReadResponseBody(t, resp)
	return resp, body
}

func boolIntForTest(value bool) int64 {
	if value {
		return 1
	}
	return 0
}
