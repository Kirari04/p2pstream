package integration_test

import (
	"context"
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
	backendA := directTargetSeed{name: "pool-a", targetOrigin: targetA.URL, enabled: true}
	backendB := directTargetSeed{name: "pool-b", targetOrigin: targetB.URL, enabled: true}
	route := createRouteTargetPoolRow(t, database, listener.ID, "/pool", nil, backendA, backendB)

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
	disabledBackend := directTargetSeed{name: "disabled-pool", targetOrigin: fallbackSrv.URL, enabled: false}
	fallbackBackend := directTargetSeed{name: "route-fallback", targetOrigin: fallbackSrv.URL, enabled: true}
	createRouteTargetPoolRow(t, database, listener.ID, "/fallback", &fallbackBackend, disabledBackend)
	createRouteTargetPoolRow(t, database, listener.ID, "/unavailable", nil, disabledBackend)

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
	if resp.StatusCode != http.StatusServiceUnavailable || !strings.Contains(body, "No target is available") {
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
	badBackend := directTargetSeed{name: "bad-passive", targetOrigin: "http://127.0.0.1:1", enabled: true}
	goodBackend := directTargetSeed{name: "good-passive", targetOrigin: targetSrv.URL, enabled: true}
	createRouteTargetPoolRow(t, database, listener.ID, "/passive", nil, badBackend, goodBackend)

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

type directTargetSeed struct {
	name          string
	targetOrigin  string
	tlsSkipVerify bool
	enabled       bool
}

func createRouteTargetPoolRow(t *testing.T, database *db.DB, listenerID int64, path string, fallbackTarget *directTargetSeed, targets ...directTargetSeed) db.PublicRoute {
	t.Helper()
	route, err := database.CreatePublicRoute(context.Background(), db.CreatePublicRouteParams{
		ListenerID:                 listenerID,
		Priority:                   int64(10 + len(path)),
		PathPrefix:                 path,
		TargetLoadBalancing:        "round_robin",
		Action:                     "forward",
		RedirectStatusCode:         http.StatusFound,
		RedirectPreservePathSuffix: 1,
		RedirectPreserveQuery:      1,
		Enabled:                    1,
	})
	if err != nil {
		t.Fatalf("create route row: %v", err)
	}
	for index, target := range targets {
		createDirectRouteTargetRow(t, database, route.ID, int64(index), 0, target)
	}
	if fallbackTarget != nil {
		createDirectRouteTargetRow(t, database, route.ID, int64(len(targets)), 1, *fallbackTarget)
	}
	return route
}

func createDirectRouteTargetRow(t *testing.T, database *db.DB, routeID int64, position int64, priorityGroup int64, target directTargetSeed) {
	t.Helper()
	if _, err := database.CreatePublicRouteTarget(context.Background(), db.CreatePublicRouteTargetParams{
		RouteID:                             routeID,
		Name:                                target.name,
		Position:                            position,
		PriorityGroup:                       priorityGroup,
		Weight:                              100,
		Enabled:                             boolIntForTest(target.enabled),
		TargetType:                          "proxy",
		Url:                                 target.targetOrigin,
		Transport:                           "direct",
		AgentSelectorJson:                   "{}",
		AgentLoadBalancing:                  "round_robin",
		TlsSkipVerify:                       boolIntForTest(target.tlsSkipVerify),
		UpstreamResponseHeaderTimeoutMillis: 60000,
		HealthCheckMethod:                   "GET",
		HealthCheckPath:                     "/",
		HealthCheckIntervalMillis:           10000,
		HealthCheckTimeoutMillis:            2000,
		HealthCheckHealthyThreshold:         2,
		HealthCheckUnhealthyThreshold:       2,
		HealthCheckExpectedStatusMin:        200,
		HealthCheckExpectedStatusMax:        399,
		StaticStatusCode:                    http.StatusOK,
		StaticResponseBodyMode:              "inline",
	}); err != nil {
		t.Fatalf("create route target row: %v", err)
	}
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
