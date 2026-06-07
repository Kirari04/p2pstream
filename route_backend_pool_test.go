package main_test

import (
	"context"
	"net/http"
	"testing"

	"connectrpc.com/connect"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/internal/config"
	"p2pstream/internal/server"
)

func TestPublicRouteTargetPoolAPIRoundTrip(t *testing.T) {
	app := server.NewApp(testManagementConfig(config.Config{}), newTestDB(t))
	_, client := newTestManagementClient(t, app)
	cookie := createAdminSession(t, client)
	cfg := getPublicProxyConfig(t, client, cookie)
	listener := publicListenerByName(t, cfg, "public-http")

	createReq := connect.NewRequest(&p2pstreamv1.CreatePublicRouteRequest{
		ListenerId:          listener.GetId(),
		Priority:            10,
		PathPrefix:          "/pool",
		Action:              p2pstreamv1.PublicRouteAction_PUBLIC_ROUTE_ACTION_FORWARD,
		TargetLoadBalancing: p2pstreamv1.PublicBackendLoadBalancing_PUBLIC_BACKEND_LOAD_BALANCING_WEIGHTED_RANDOM,
		Targets: []*p2pstreamv1.PublicRouteTarget{
			routePoolTarget("route-pool-a", "http://127.0.0.1:18081", true, 0, 25),
			routePoolTarget("route-pool-b", "http://127.0.0.1:18082", false, 0, 75),
			routePoolTarget("route-pool-fallback", "http://127.0.0.1:18083", true, 1, 100),
		},
		Enabled: true,
	})
	createReq.Header().Set("Cookie", cookie)
	createResp, err := client.CreatePublicRoute(context.Background(), createReq)
	if err != nil {
		t.Fatalf("create route target pool: %v", err)
	}
	route := createResp.Msg.GetRoute()
	if route.GetTargetLoadBalancing() != p2pstreamv1.PublicBackendLoadBalancing_PUBLIC_BACKEND_LOAD_BALANCING_WEIGHTED_RANDOM {
		t.Fatalf("unexpected target load balancing: %+v", route)
	}
	if len(route.GetTargets()) != 3 ||
		route.GetTargets()[0].GetName() != "route-pool-a" ||
		route.GetTargets()[1].GetWeight() != 75 ||
		route.GetTargets()[1].GetEnabled() ||
		route.GetTargets()[2].GetPriorityGroup() != 1 {
		t.Fatalf("unexpected route targets: %+v", route.GetTargets())
	}

	cfg = getPublicProxyConfig(t, client, cookie)
	if publicRouteTargetByName(t, cfg, "route-pool-a").GetWeight() != 25 {
		t.Fatalf("expected route target in config readback")
	}
}

func TestPublicRouteTargetPoolValidation(t *testing.T) {
	app := server.NewApp(testManagementConfig(config.Config{}), newTestDB(t))
	_, client := newTestManagementClient(t, app)
	cookie := createAdminSession(t, client)
	cfg := getPublicProxyConfig(t, client, cookie)
	listener := publicListenerByName(t, cfg, "public-http")

	healthReq := connect.NewRequest(&p2pstreamv1.CreatePublicRouteRequest{
		ListenerId: listener.GetId(),
		Priority:   10,
		PathPrefix: "/bad-health",
		Action:     p2pstreamv1.PublicRouteAction_PUBLIC_ROUTE_ACTION_FORWARD,
		Targets: []*p2pstreamv1.PublicRouteTarget{{
			Name:       "bad-health",
			TargetType: p2pstreamv1.PublicRouteTargetType_PUBLIC_ROUTE_TARGET_TYPE_PROXY,
			Url:        "http://127.0.0.1:1",
			Transport:  p2pstreamv1.PublicRouteTargetTransport_PUBLIC_ROUTE_TARGET_TRANSPORT_DIRECT,
			Enabled:    true,
			HealthCheck: &p2pstreamv1.PublicBackendHealthCheck{
				Enabled:        true,
				Method:         "POST",
				Path:           "/health",
				IntervalMillis: 10000,
				TimeoutMillis:  2000,
			},
		}},
		Enabled: true,
	})
	healthReq.Header().Set("Cookie", cookie)
	if _, err := client.CreatePublicRoute(context.Background(), healthReq); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("invalid health check error = %v, want invalid argument", err)
	}
}

func routePoolTarget(name string, target string, enabled bool, priorityGroup int64, weight int64) *p2pstreamv1.PublicRouteTarget {
	return &p2pstreamv1.PublicRouteTarget{
		Name:                                name,
		TargetType:                          p2pstreamv1.PublicRouteTargetType_PUBLIC_ROUTE_TARGET_TYPE_PROXY,
		Url:                                 target,
		Transport:                           p2pstreamv1.PublicRouteTargetTransport_PUBLIC_ROUTE_TARGET_TRANSPORT_DIRECT,
		PriorityGroup:                       priorityGroup,
		Weight:                              weight,
		Enabled:                             enabled,
		UpstreamResponseHeaderTimeoutMillis: 60000,
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
	}
}
