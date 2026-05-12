package main_test

import (
	"context"
	"net/http"
	"testing"

	"connectrpc.com/connect"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/gen/proto/p2pstream/v1/p2pstreamv1connect"
	"p2pstream/internal/config"
	"p2pstream/internal/db"
	"p2pstream/internal/server"
)

func TestPublicRouteBackendPoolAPIRoundTrip(t *testing.T) {
	database := newTestDB(t)
	app := server.NewApp(&config.Config{}, database)
	_, client := newTestManagementClient(t, app)
	cookie := createAdminSession(t, client)

	backendA := createRoutePoolBackend(t, client, cookie, "route-pool-a", "http://127.0.0.1:18081")
	backendB := createRoutePoolBackend(t, client, cookie, "route-pool-b", "http://127.0.0.1:18082")
	fallback := createRoutePoolBackend(t, client, cookie, "route-pool-fallback", "http://127.0.0.1:18083")
	listener, err := database.CreatePublicListener(context.Background(), db.CreatePublicListenerParams{
		Name:             "route-pool-listener",
		BindAddress:      "127.0.0.1",
		Port:             0,
		Protocol:         "http",
		Enabled:          1,
		DefaultBackendID: fallback.GetId(),
	})
	if err != nil {
		t.Fatalf("create listener: %v", err)
	}

	createReq := connect.NewRequest(&p2pstreamv1.CreatePublicRouteRequest{
		ListenerId:        listener.ID,
		Priority:          10,
		PathPrefix:        "/pool",
		Action:            p2pstreamv1.PublicRouteAction_PUBLIC_ROUTE_ACTION_FORWARD,
		LoadBalancing:     p2pstreamv1.PublicBackendLoadBalancing_PUBLIC_BACKEND_LOAD_BALANCING_WEIGHTED_RANDOM,
		FallbackBackendId: fallback.GetId(),
		BackendAssignments: []*p2pstreamv1.PublicRouteBackend{
			{BackendId: backendA.GetId(), Weight: 25, Enabled: true},
			{BackendId: backendB.GetId(), Weight: 75, Enabled: false},
		},
		Enabled: true,
	})
	createReq.Header().Set("Cookie", cookie)
	createResp, err := client.CreatePublicRoute(context.Background(), createReq)
	if err != nil {
		t.Fatalf("create route pool: %v", err)
	}
	route := createResp.Msg.GetRoute()
	if route.GetBackendId() != backendA.GetId() || route.GetLoadBalancing() != p2pstreamv1.PublicBackendLoadBalancing_PUBLIC_BACKEND_LOAD_BALANCING_WEIGHTED_RANDOM {
		t.Fatalf("unexpected route readback: %+v", route)
	}
	if route.GetFallbackBackendId() != fallback.GetId() {
		t.Fatalf("fallback backend id = %d, want %d", route.GetFallbackBackendId(), fallback.GetId())
	}
	if len(route.GetBackendAssignments()) != 2 || route.GetBackendAssignments()[1].GetWeight() != 75 || route.GetBackendAssignments()[1].GetEnabled() {
		t.Fatalf("unexpected route assignments: %+v", route.GetBackendAssignments())
	}

	cfg := getPublicProxyConfig(t, client, cookie)
	if len(cfg.GetRouteBackends()) < 2 {
		t.Fatalf("expected route backends in config, got %+v", cfg.GetRouteBackends())
	}
}

func TestPublicRouteBackendPoolValidation(t *testing.T) {
	database := newTestDB(t)
	app := server.NewApp(&config.Config{}, database)
	_, client := newTestManagementClient(t, app)
	cookie := createAdminSession(t, client)

	backend := createRoutePoolBackend(t, client, cookie, "route-pool-validation", "http://127.0.0.1:18181")
	listener, err := database.CreatePublicListener(context.Background(), db.CreatePublicListenerParams{
		Name:             "route-pool-validation-listener",
		BindAddress:      "127.0.0.1",
		Port:             0,
		Protocol:         "http",
		Enabled:          1,
		DefaultBackendID: backend.GetId(),
	})
	if err != nil {
		t.Fatalf("create listener: %v", err)
	}

	duplicateReq := connect.NewRequest(&p2pstreamv1.CreatePublicRouteRequest{
		ListenerId:    listener.ID,
		Priority:      10,
		PathPrefix:    "/duplicate",
		Action:        p2pstreamv1.PublicRouteAction_PUBLIC_ROUTE_ACTION_FORWARD,
		LoadBalancing: p2pstreamv1.PublicBackendLoadBalancing_PUBLIC_BACKEND_LOAD_BALANCING_ROUND_ROBIN,
		BackendAssignments: []*p2pstreamv1.PublicRouteBackend{
			{BackendId: backend.GetId(), Weight: 100, Enabled: true},
			{BackendId: backend.GetId(), Weight: 100, Enabled: true},
		},
		Enabled: true,
	})
	duplicateReq.Header().Set("Cookie", cookie)
	if _, err := client.CreatePublicRoute(context.Background(), duplicateReq); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("duplicate route backend error = %v, want invalid argument", err)
	}

	healthReq := connect.NewRequest(&p2pstreamv1.CreatePublicBackendRequest{
		Name:         "bad-health",
		TargetOrigin: "http://127.0.0.1:1",
		Enabled:      true,
		BackendType:  p2pstreamv1.PublicBackendType_PUBLIC_BACKEND_TYPE_PROXY_FORWARD,
		ForwardMode:  p2pstreamv1.PublicBackendForwardMode_PUBLIC_BACKEND_FORWARD_MODE_DIRECT,
		HealthCheck: &p2pstreamv1.PublicBackendHealthCheck{
			Enabled:        true,
			Method:         "POST",
			Path:           "/health",
			IntervalMillis: 10000,
			TimeoutMillis:  2000,
		},
	})
	healthReq.Header().Set("Cookie", cookie)
	if _, err := client.CreatePublicBackend(context.Background(), healthReq); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("invalid health check error = %v, want invalid argument", err)
	}
}

func createRoutePoolBackend(t *testing.T, client p2pstreamv1connect.AgentManagementServiceClient, cookie string, name string, target string) *p2pstreamv1.PublicBackend {
	t.Helper()
	req := connect.NewRequest(&p2pstreamv1.CreatePublicBackendRequest{
		Name:         name,
		TargetOrigin: target,
		Enabled:      true,
		BackendType:  p2pstreamv1.PublicBackendType_PUBLIC_BACKEND_TYPE_PROXY_FORWARD,
		ForwardMode:  p2pstreamv1.PublicBackendForwardMode_PUBLIC_BACKEND_FORWARD_MODE_DIRECT,
		HealthCheck: &p2pstreamv1.PublicBackendHealthCheck{
			Enabled:            false,
			Method:             http.MethodGet,
			Path:               "/",
			IntervalMillis:     10000,
			TimeoutMillis:      2000,
			HealthyThreshold:   2,
			UnhealthyThreshold: 2,
			ExpectedStatusMin:  200,
			ExpectedStatusMax:  399,
		},
	})
	req.Header().Set("Cookie", cookie)
	resp, err := client.CreatePublicBackend(context.Background(), req)
	if err != nil {
		t.Fatalf("create backend %s: %v", name, err)
	}
	return resp.Msg.GetBackend()
}
