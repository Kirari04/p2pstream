package main_test

import (
	"context"
	"database/sql"
	"net/http"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/gen/proto/p2pstream/v1/p2pstreamv1connect"
	"p2pstream/internal/config"
	"p2pstream/internal/server"
)

func TestPublicRouteRedirectValidationAndConfig(t *testing.T) {
	database := newTestDB(t)
	listener := seedTestHTTPPublicListener(t, database, "https://example.com")
	app := server.NewApp(&config.Config{}, database)
	_, client := newTestManagementClient(t, app)
	cookie := createAdminSession(t, client)

	route := createRedirectRoute(t, client, cookie, listener.ID, &p2pstreamv1.CreatePublicRouteRequest{
		Priority:                   10,
		PathPrefix:                 "/old",
		RedirectTargetMode:         p2pstreamv1.PublicRouteRedirectTargetMode_PUBLIC_ROUTE_REDIRECT_TARGET_MODE_SAME_HOST_PATH,
		RedirectTarget:             "/new",
		RedirectStatusCode:         http.StatusMovedPermanently,
		RedirectPreservePathSuffix: true,
		RedirectPreserveQuery:      true,
		Enabled:                    true,
	})
	if route.GetAction() != p2pstreamv1.PublicRouteAction_PUBLIC_ROUTE_ACTION_REDIRECT {
		t.Fatalf("route action = %s, want REDIRECT", route.GetAction())
	}
	if route.GetBackendId() != 0 {
		t.Fatalf("redirect route backend id = %d, want 0", route.GetBackendId())
	}
	if route.GetRedirectStatusCode() != http.StatusMovedPermanently {
		t.Fatalf("redirect status = %d, want 301", route.GetRedirectStatusCode())
	}

	invalidStatusReq := connect.NewRequest(&p2pstreamv1.CreatePublicRouteRequest{
		ListenerId:                 listener.ID,
		Priority:                   11,
		PathPrefix:                 "/bad-status",
		Action:                     p2pstreamv1.PublicRouteAction_PUBLIC_ROUTE_ACTION_REDIRECT,
		RedirectTargetMode:         p2pstreamv1.PublicRouteRedirectTargetMode_PUBLIC_ROUTE_REDIRECT_TARGET_MODE_SAME_HOST_PATH,
		RedirectTarget:             "/new",
		RedirectStatusCode:         http.StatusSeeOther,
		RedirectPreservePathSuffix: true,
		RedirectPreserveQuery:      true,
		Enabled:                    true,
	})
	invalidStatusReq.Header().Set("Cookie", cookie)
	_, err := client.CreatePublicRoute(context.Background(), invalidStatusReq)
	requireConnectCode(t, err, connect.CodeInvalidArgument)

	invalidTargetReq := connect.NewRequest(&p2pstreamv1.CreatePublicRouteRequest{
		ListenerId:                 listener.ID,
		Priority:                   12,
		PathPrefix:                 "/bad-target",
		Action:                     p2pstreamv1.PublicRouteAction_PUBLIC_ROUTE_ACTION_REDIRECT,
		RedirectTargetMode:         p2pstreamv1.PublicRouteRedirectTargetMode_PUBLIC_ROUTE_REDIRECT_TARGET_MODE_SAME_HOST_PATH,
		RedirectTarget:             "https://example.net/new",
		RedirectStatusCode:         http.StatusFound,
		RedirectPreservePathSuffix: true,
		RedirectPreserveQuery:      true,
		Enabled:                    true,
	})
	invalidTargetReq.Header().Set("Cookie", cookie)
	_, err = client.CreatePublicRoute(context.Background(), invalidTargetReq)
	requireConnectCode(t, err, connect.CodeInvalidArgument)
}

func TestPublicRouteRedirectResponses(t *testing.T) {
	tests := []struct {
		name     string
		request  *p2pstreamv1.CreatePublicRouteRequest
		path     string
		wantCode int
		wantLoc  string
	}{
		{
			name: "same host path preserves suffix and query",
			request: &p2pstreamv1.CreatePublicRouteRequest{
				PathPrefix:                 "/old",
				RedirectTargetMode:         p2pstreamv1.PublicRouteRedirectTargetMode_PUBLIC_ROUTE_REDIRECT_TARGET_MODE_SAME_HOST_PATH,
				RedirectTarget:             "/new?fixed=1",
				RedirectStatusCode:         http.StatusFound,
				RedirectPreservePathSuffix: true,
				RedirectPreserveQuery:      true,
				Enabled:                    true,
			},
			path:     "/old/thing?x=1",
			wantCode: http.StatusFound,
			wantLoc:  "/new/thing?fixed=1&x=1",
		},
		{
			name: "external origin keeps original path",
			request: &p2pstreamv1.CreatePublicRouteRequest{
				PathPrefix:                 "/keep",
				RedirectTargetMode:         p2pstreamv1.PublicRouteRedirectTargetMode_PUBLIC_ROUTE_REDIRECT_TARGET_MODE_EXTERNAL_ORIGIN_KEEP_PATH,
				RedirectTarget:             "https://new.example.com",
				RedirectStatusCode:         http.StatusTemporaryRedirect,
				RedirectPreservePathSuffix: true,
				RedirectPreserveQuery:      true,
				Enabled:                    true,
			},
			path:     "/keep/path?x=1",
			wantCode: http.StatusTemporaryRedirect,
			wantLoc:  "https://new.example.com/keep/path?x=1",
		},
		{
			name: "absolute url preserves suffix query and fragment",
			request: &p2pstreamv1.CreatePublicRouteRequest{
				PathPrefix:                 "/from",
				RedirectTargetMode:         p2pstreamv1.PublicRouteRedirectTargetMode_PUBLIC_ROUTE_REDIRECT_TARGET_MODE_ABSOLUTE_URL,
				RedirectTarget:             "https://target.example/base?fixed=1#frag",
				RedirectStatusCode:         http.StatusPermanentRedirect,
				RedirectPreservePathSuffix: true,
				RedirectPreserveQuery:      true,
				Enabled:                    true,
			},
			path:     "/from/deep?x=1",
			wantCode: http.StatusPermanentRedirect,
			wantLoc:  "https://target.example/base/deep?fixed=1&x=1#frag",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			database := newTestDB(t)
			listener := seedTestHTTPPublicListener(t, database, "https://example.com")
			app := server.NewApp(&config.Config{}, database)
			_, client := newTestManagementClient(t, app)
			cookie := createAdminSession(t, client)
			route := createRedirectRoute(t, client, cookie, listener.ID, tt.request)

			status, err := app.StartProxyListener(context.Background())
			if err != nil {
				t.Fatalf("start proxy: %v", err)
			}
			t.Cleanup(func() {
				shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				_, _ = app.StopProxyListener(shutdownCtx)
			})

			proxyClient := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			}}
			resp, err := proxyClient.Get("http://" + publicListenerBoundAddress(t, status, listener.ID) + tt.path)
			if err != nil {
				t.Fatalf("redirect request: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tt.wantCode {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tt.wantCode)
			}
			if got := resp.Header.Get("Location"); got != tt.wantLoc {
				t.Fatalf("Location = %q, want %q", got, tt.wantLoc)
			}

			var backendID sql.NullInt64
			var routeID sql.NullInt64
			var statusCode int64
			if err := database.QueryRowContext(context.Background(), `
				SELECT backend_id, route_id, status_code
				FROM proxy_request_events
				ORDER BY id DESC
				LIMIT 1
			`).Scan(&backendID, &routeID, &statusCode); err != nil {
				t.Fatalf("read proxy request event: %v", err)
			}
			if backendID.Valid {
				t.Fatalf("redirect event backend_id = %d, want NULL", backendID.Int64)
			}
			if !routeID.Valid || routeID.Int64 != route.GetId() {
				t.Fatalf("redirect event route_id = %+v, want %d", routeID, route.GetId())
			}
			if statusCode != int64(tt.wantCode) {
				t.Fatalf("redirect event status = %d, want %d", statusCode, tt.wantCode)
			}
		})
	}
}

func TestPublicRouteRedirectTraceStages(t *testing.T) {
	database := newTestDB(t)
	listener := seedTestHTTPPublicListener(t, database, "https://example.com")
	app := server.NewApp(&config.Config{}, database)
	_, client := newTestManagementClient(t, app)
	cookie := createAdminSession(t, client)
	createRedirectRoute(t, client, cookie, listener.ID, &p2pstreamv1.CreatePublicRouteRequest{
		Priority:                   1,
		PathPrefix:                 "/trace-redirect",
		RedirectTargetMode:         p2pstreamv1.PublicRouteRedirectTargetMode_PUBLIC_ROUTE_REDIRECT_TARGET_MODE_SAME_HOST_PATH,
		RedirectTarget:             "/target",
		RedirectStatusCode:         http.StatusFound,
		RedirectPreservePathSuffix: true,
		RedirectPreserveQuery:      true,
		Enabled:                    true,
	})
	setTrafficTraceSettings(t, client, cookie, true, p2pstreamv1.TrafficTraceLevel_TRAFFIC_TRACE_LEVEL_HEADERS)

	status, err := app.StartProxyListener(context.Background())
	if err != nil {
		t.Fatalf("start proxy: %v", err)
	}
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = app.StopProxyListener(shutdownCtx)
	})

	proxyClient := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := proxyClient.Get("http://" + publicListenerBoundAddress(t, status, listener.ID) + "/trace-redirect/a?x=1")
	if err != nil {
		t.Fatalf("redirect request: %v", err)
	}
	_ = mustReadResponseBody(t, resp)
	resp.Body.Close()

	stream, stop := openTrafficTraceStream(t, client, cookie, true, 0, 2*time.Second)
	defer stop()
	_ = receiveTrafficTraceResponse(t, stream)
	events := collectTrafficTraceEventsUntil(t, stream, p2pstreamv1.TrafficTraceStage_TRAFFIC_TRACE_STAGE_RESPONSE_SENT)

	assertTrafficTraceStages(t, events,
		p2pstreamv1.TrafficTraceStage_TRAFFIC_TRACE_STAGE_RECEIVED,
		p2pstreamv1.TrafficTraceStage_TRAFFIC_TRACE_STAGE_ROUTE_RESOLVED,
		p2pstreamv1.TrafficTraceStage_TRAFFIC_TRACE_STAGE_RESPONSE_SENT,
	)
	finalEvent := events[len(events)-1]
	if finalEvent.GetBackendId() != 0 {
		t.Fatalf("redirect trace backend id = %d, want 0", finalEvent.GetBackendId())
	}
	if finalEvent.GetResponseHeaders()["Location"] != "/target/a?x=1" {
		t.Fatalf("redirect trace Location header = %q", finalEvent.GetResponseHeaders()["Location"])
	}
}

func createRedirectRoute(
	t *testing.T,
	client p2pstreamv1connect.AgentManagementServiceClient,
	cookie string,
	listenerID int64,
	request *p2pstreamv1.CreatePublicRouteRequest,
) *p2pstreamv1.PublicRoute {
	t.Helper()
	payload := proto.Clone(request).(*p2pstreamv1.CreatePublicRouteRequest)
	payload.ListenerId = listenerID
	payload.Action = p2pstreamv1.PublicRouteAction_PUBLIC_ROUTE_ACTION_REDIRECT
	if payload.Priority == 0 {
		payload.Priority = 1
	}
	req := connect.NewRequest(payload)
	req.Header().Set("Cookie", cookie)
	resp, err := client.CreatePublicRoute(context.Background(), req)
	if err != nil {
		t.Fatalf("create redirect route: %v", err)
	}
	return resp.Msg.GetRoute()
}
