package db

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestPublicRouteTargetUpstreamHeadersRoundTrip(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "p2pstream-test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer func() { _ = database.Close() }()

	listener, err := database.CreatePublicListener(context.Background(), CreatePublicListenerParams{
		Name:        "headers-listener",
		BindAddress: "",
		Port:        18080,
		Protocol:    "http",
		Enabled:     1,
	})
	if err != nil {
		t.Fatalf("create listener: %v", err)
	}
	route, err := database.CreatePublicRoute(context.Background(), CreatePublicRouteParams{
		ListenerID:                 listener.ID,
		Priority:                   10,
		HostPattern:                "",
		PathPrefix:                 "/",
		TargetLoadBalancing:        "round_robin",
		IsDefault:                  1,
		Action:                     "forward",
		RedirectTargetMode:         "",
		RedirectTarget:             "",
		RedirectStatusCode:         302,
		RedirectPreservePathSuffix: 1,
		RedirectPreserveQuery:      1,
		PathSecurityMode:           "strict",
		Enabled:                    1,
	})
	if err != nil {
		t.Fatalf("create route: %v", err)
	}
	target, err := database.CreatePublicRouteTarget(context.Background(), CreatePublicRouteTargetParams{
		RouteID:                             route.ID,
		Name:                                "upstream-headers",
		Position:                            0,
		PriorityGroup:                       0,
		Weight:                              100,
		Enabled:                             1,
		TargetType:                          "proxy",
		Url:                                 "http://example.com",
		Transport:                           "direct",
		AgentSelectorJson:                   "{}",
		AgentLoadBalancing:                  "round_robin",
		TlsSkipVerify:                       0,
		UpstreamBasicAuthEnabled:            0,
		UpstreamBasicAuthUsername:           "",
		UpstreamBasicAuthPassword:           "",
		UpstreamResponseHeaderTimeoutMillis: 60000,
		HealthCheckEnabled:                  0,
		HealthCheckMethod:                   "GET",
		HealthCheckPath:                     "/",
		HealthCheckIntervalMillis:           10000,
		HealthCheckTimeoutMillis:            2000,
		HealthCheckHealthyThreshold:         2,
		HealthCheckUnhealthyThreshold:       2,
		HealthCheckExpectedStatusMin:        200,
		HealthCheckExpectedStatusMax:        399,
		StaticStatusCode:                    200,
		StaticResponseBody:                  "",
		StaticResponseBodyMode:              "inline",
		StaticResponseTemplateID:            sql.NullInt64{},
	})
	if err != nil {
		t.Fatalf("create target: %v", err)
	}
	otherTarget, err := database.CreatePublicRouteTarget(context.Background(), CreatePublicRouteTargetParams{
		RouteID:                             route.ID,
		Name:                                "other-upstream-headers",
		Position:                            1,
		PriorityGroup:                       0,
		Weight:                              100,
		Enabled:                             1,
		TargetType:                          "proxy",
		Url:                                 "http://other.example.com",
		Transport:                           "direct",
		AgentSelectorJson:                   "{}",
		AgentLoadBalancing:                  "round_robin",
		TlsSkipVerify:                       0,
		UpstreamBasicAuthEnabled:            0,
		UpstreamBasicAuthUsername:           "",
		UpstreamBasicAuthPassword:           "",
		UpstreamResponseHeaderTimeoutMillis: 60000,
		HealthCheckEnabled:                  0,
		HealthCheckMethod:                   "GET",
		HealthCheckPath:                     "/",
		HealthCheckIntervalMillis:           10000,
		HealthCheckTimeoutMillis:            2000,
		HealthCheckHealthyThreshold:         2,
		HealthCheckUnhealthyThreshold:       2,
		HealthCheckExpectedStatusMin:        200,
		HealthCheckExpectedStatusMax:        399,
		StaticStatusCode:                    200,
		StaticResponseBody:                  "",
		StaticResponseBodyMode:              "inline",
		StaticResponseTemplateID:            sql.NullInt64{},
	})
	if err != nil {
		t.Fatalf("create other target: %v", err)
	}
	first, err := database.CreatePublicRouteTargetUpstreamHeader(context.Background(), CreatePublicRouteTargetUpstreamHeaderParams{
		TargetID:  target.ID,
		Position:  0,
		Name:      "X-Upstream-One",
		Value:     "one",
		Sensitive: 0,
	})
	if err != nil {
		t.Fatalf("create upstream header: %v", err)
	}
	second, err := database.CreatePublicRouteTargetUpstreamHeader(context.Background(), CreatePublicRouteTargetUpstreamHeaderParams{
		TargetID:  target.ID,
		Position:  1,
		Name:      "Authorization",
		Value:     "Bearer secret",
		Sensitive: 1,
	})
	if err != nil {
		t.Fatalf("create sensitive upstream header: %v", err)
	}
	otherHeader, err := database.CreatePublicRouteTargetUpstreamHeader(context.Background(), CreatePublicRouteTargetUpstreamHeaderParams{
		TargetID:  otherTarget.ID,
		Position:  0,
		Name:      "X-Other",
		Value:     "other",
		Sensitive: 0,
	})
	if err != nil {
		t.Fatalf("create other upstream header: %v", err)
	}

	byTarget, err := database.ListPublicRouteTargetUpstreamHeadersByTarget(context.Background(), target.ID)
	if err != nil {
		t.Fatalf("list upstream headers by target: %v", err)
	}
	if len(byTarget) != 2 || byTarget[0].ID != first.ID || byTarget[1].ID != second.ID {
		t.Fatalf("unexpected upstream headers by target: %+v", byTarget)
	}
	all, err := database.ListPublicRouteTargetUpstreamHeaders(context.Background())
	if err != nil {
		t.Fatalf("list upstream headers: %v", err)
	}
	if len(all) != 3 || all[0].Name != "X-Upstream-One" || all[1].Sensitive != 1 || all[2].ID != otherHeader.ID {
		t.Fatalf("unexpected upstream headers: %+v", all)
	}
	if err := database.DeletePublicRouteTargetUpstreamHeaders(context.Background(), target.ID); err != nil {
		t.Fatalf("delete upstream headers: %v", err)
	}
	empty, err := database.ListPublicRouteTargetUpstreamHeadersByTarget(context.Background(), target.ID)
	if err != nil {
		t.Fatalf("list deleted upstream headers: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("expected deleted upstream headers, got %+v", empty)
	}
	remaining, err := database.ListPublicRouteTargetUpstreamHeadersByTarget(context.Background(), otherTarget.ID)
	if err != nil {
		t.Fatalf("list other upstream headers: %v", err)
	}
	if len(remaining) != 1 || remaining[0].ID != otherHeader.ID || remaining[0].Name != "X-Other" || remaining[0].Value != "other" {
		t.Fatalf("other target upstream headers were not preserved: %+v", remaining)
	}
}

func TestPublicRoutePathSecurityModeRoundTrip(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "p2pstream-route-mode-test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer func() { _ = database.Close() }()

	listener, err := database.CreatePublicListener(context.Background(), CreatePublicListenerParams{
		Name:        "route-mode-listener",
		BindAddress: "",
		Port:        18081,
		Protocol:    "http",
		Enabled:     1,
	})
	if err != nil {
		t.Fatalf("create listener: %v", err)
	}
	route, err := database.CreatePublicRoute(context.Background(), CreatePublicRouteParams{
		ListenerID:                 listener.ID,
		Priority:                   10,
		HostPattern:                "",
		PathPrefix:                 "/git",
		TargetLoadBalancing:        "round_robin",
		IsDefault:                  0,
		Action:                     "forward",
		RedirectTargetMode:         "",
		RedirectTarget:             "",
		RedirectStatusCode:         302,
		RedirectPreservePathSuffix: 1,
		RedirectPreserveQuery:      1,
		PathSecurityMode:           "allow_encoded_separators",
		Enabled:                    1,
	})
	if err != nil {
		t.Fatalf("create route: %v", err)
	}
	if route.PathSecurityMode != "allow_encoded_separators" {
		t.Fatalf("created route path security mode = %q, want allow_encoded_separators", route.PathSecurityMode)
	}
	got, err := database.GetPublicRoute(context.Background(), route.ID)
	if err != nil {
		t.Fatalf("get route: %v", err)
	}
	if got.PathSecurityMode != "allow_encoded_separators" {
		t.Fatalf("got route path security mode = %q, want allow_encoded_separators", got.PathSecurityMode)
	}
	updated, err := database.UpdatePublicRoute(context.Background(), UpdatePublicRouteParams{
		ID:                         route.ID,
		ListenerID:                 listener.ID,
		Priority:                   10,
		HostPattern:                "",
		PathPrefix:                 "/git",
		TargetLoadBalancing:        "round_robin",
		IsDefault:                  0,
		Action:                     "forward",
		RedirectTargetMode:         "",
		RedirectTarget:             "",
		RedirectStatusCode:         302,
		RedirectPreservePathSuffix: 1,
		RedirectPreserveQuery:      1,
		PathSecurityMode:           "strict",
		Enabled:                    1,
	})
	if err != nil {
		t.Fatalf("update route: %v", err)
	}
	if updated.PathSecurityMode != "strict" {
		t.Fatalf("updated route path security mode = %q, want strict", updated.PathSecurityMode)
	}
}
