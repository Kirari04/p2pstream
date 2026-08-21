package server

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/internal/db"
)

func TestPublicRoutePathSecurityModeManagementAPI(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "public-config-test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()
	app := NewApp(nil, database)
	header := createTestAdminSession(t, app)
	listener, err := app.DB.CreatePublicListener(context.Background(), db.CreatePublicListenerParams{
		Name:        "public-http",
		BindAddress: "",
		Port:        8080,
		Protocol:    publicListenerProtocolHTTP,
		Enabled:     1,
	})
	if err != nil {
		t.Fatalf("create listener: %v", err)
	}

	createDefault := connect.NewRequest(&p2pstreamv1.CreatePublicRouteRequest{
		ListenerId:            listener.ID,
		Priority:              10,
		PathPrefix:            "/default",
		Enabled:               true,
		Action:                p2pstreamv1.PublicRouteAction_PUBLIC_ROUTE_ACTION_REDIRECT,
		RedirectTargetMode:    p2pstreamv1.PublicRouteRedirectTargetMode_PUBLIC_ROUTE_REDIRECT_TARGET_MODE_SAME_HOST_PATH,
		RedirectTarget:        "/target",
		RedirectStatusCode:    302,
		RedirectPreserveQuery: true,
	})
	createDefault.Header().Set("Cookie", header.Get("Cookie"))
	defaultResp, err := app.CreatePublicRoute(context.Background(), createDefault)
	if err != nil {
		t.Fatalf("create default route: %v", err)
	}
	if got := defaultResp.Msg.Route.PathSecurityMode; got != p2pstreamv1.PublicRoutePathSecurityMode_PUBLIC_ROUTE_PATH_SECURITY_MODE_STRICT {
		t.Fatalf("default path security mode = %v, want strict", got)
	}

	createCompat := connect.NewRequest(&p2pstreamv1.CreatePublicRouteRequest{
		ListenerId:            listener.ID,
		Priority:              20,
		PathPrefix:            "/git",
		Enabled:               true,
		Action:                p2pstreamv1.PublicRouteAction_PUBLIC_ROUTE_ACTION_REDIRECT,
		RedirectTargetMode:    p2pstreamv1.PublicRouteRedirectTargetMode_PUBLIC_ROUTE_REDIRECT_TARGET_MODE_SAME_HOST_PATH,
		RedirectTarget:        "/target",
		RedirectStatusCode:    302,
		RedirectPreserveQuery: true,
		PathSecurityMode:      p2pstreamv1.PublicRoutePathSecurityMode_PUBLIC_ROUTE_PATH_SECURITY_MODE_ALLOW_ENCODED_SEPARATORS,
	})
	createCompat.Header().Set("Cookie", header.Get("Cookie"))
	compatResp, err := app.CreatePublicRoute(context.Background(), createCompat)
	if err != nil {
		t.Fatalf("create compatibility route: %v", err)
	}
	if got := compatResp.Msg.Route.PathSecurityMode; got != p2pstreamv1.PublicRoutePathSecurityMode_PUBLIC_ROUTE_PATH_SECURITY_MODE_ALLOW_ENCODED_SEPARATORS {
		t.Fatalf("compatibility path security mode = %v, want allow encoded separators", got)
	}

	updateStrict := connect.NewRequest(&p2pstreamv1.UpdatePublicRouteRequest{
		Id:                    compatResp.Msg.Route.Id,
		ListenerId:            listener.ID,
		Priority:              20,
		PathPrefix:            "/git",
		Enabled:               true,
		Action:                p2pstreamv1.PublicRouteAction_PUBLIC_ROUTE_ACTION_REDIRECT,
		RedirectTargetMode:    p2pstreamv1.PublicRouteRedirectTargetMode_PUBLIC_ROUTE_REDIRECT_TARGET_MODE_SAME_HOST_PATH,
		RedirectTarget:        "/target",
		RedirectStatusCode:    302,
		RedirectPreserveQuery: true,
		PathSecurityMode:      p2pstreamv1.PublicRoutePathSecurityMode_PUBLIC_ROUTE_PATH_SECURITY_MODE_STRICT,
	})
	updateStrict.Header().Set("Cookie", header.Get("Cookie"))
	updateResp, err := app.UpdatePublicRoute(context.Background(), updateStrict)
	if err != nil {
		t.Fatalf("update compatibility route to strict: %v", err)
	}
	if got := updateResp.Msg.Route.PathSecurityMode; got != p2pstreamv1.PublicRoutePathSecurityMode_PUBLIC_ROUTE_PATH_SECURITY_MODE_STRICT {
		t.Fatalf("updated path security mode = %v, want strict", got)
	}
}

func TestPublicRouteUpdatePreservesMaskedSensitiveUpstreamHeaders(t *testing.T) {
	app := NewApp(nil, newServerTestDB(t))
	header := createTestAdminSession(t, app)
	listener := seedPublicConfigTestListener(t, app.DB)

	createReq := testPublicRouteRequest(listener.ID, "/masked-secret", []*p2pstreamv1.PublicRouteTargetUpstreamHeader{
		{Name: "X-Secret", Value: "top-secret", Sensitive: true, ValueSet: true},
		{Name: "Cookie", Value: "session=abc", ValueSet: true},
	})
	createReq.Header().Set("Cookie", header.Get("Cookie"))
	createResp, err := app.CreatePublicRoute(context.Background(), createReq)
	if err != nil {
		t.Fatalf("create route: %v", err)
	}
	if len(createResp.Msg.Route.Targets) != 1 || len(createResp.Msg.Route.Targets[0].UpstreamRequestHeaders) != 2 {
		t.Fatalf("created route headers = %+v", createResp.Msg.Route.Targets)
	}
	for _, got := range createResp.Msg.Route.Targets[0].UpstreamRequestHeaders {
		if got.Value != "" || got.ValueSet {
			t.Fatalf("sensitive header proto = %+v, want masked with value_set=false", got)
		}
	}

	updateTarget := createResp.Msg.Route.Targets[0]
	updateTarget.Name = "renamed-target"
	updateReq := testPublicRouteUpdateRequest(createResp.Msg.Route, "/masked-secret-updated", []*p2pstreamv1.PublicRouteTarget{updateTarget})
	updateReq.Header().Set("Cookie", header.Get("Cookie"))
	updateResp, err := app.UpdatePublicRoute(context.Background(), updateReq)
	if err != nil {
		t.Fatalf("update route with masked headers: %v", err)
	}
	if got := updateResp.Msg.Route.Targets[0].UpstreamRequestHeaders[0]; got.Value != "" || got.ValueSet {
		t.Fatalf("updated sensitive header proto = %+v, want masked with value_set=false", got)
	}
	assertPublicRouteUpstreamHeaderValues(t, app.DB, updateResp.Msg.Route.Id, map[string]string{
		"x-secret": "top-secret",
		"cookie":   "session=abc",
	})
}

func TestPublicRouteUpdateRejectsMaskedSensitiveHeaderFromAnotherRoute(t *testing.T) {
	app := NewApp(nil, newServerTestDB(t))
	header := createTestAdminSession(t, app)
	listener := seedPublicConfigTestListener(t, app.DB)

	firstReq := testPublicRouteRequest(listener.ID, "/first-secret", []*p2pstreamv1.PublicRouteTargetUpstreamHeader{
		{Name: "X-Secret", Value: "first-secret", Sensitive: true, ValueSet: true},
	})
	firstReq.Header().Set("Cookie", header.Get("Cookie"))
	firstResp, err := app.CreatePublicRoute(context.Background(), firstReq)
	if err != nil {
		t.Fatalf("create first route: %v", err)
	}

	secondReq := testPublicRouteRequest(listener.ID, "/second-secret", []*p2pstreamv1.PublicRouteTargetUpstreamHeader{
		{Name: "X-Secret", Value: "second-secret", Sensitive: true, ValueSet: true},
	})
	secondReq.Header().Set("Cookie", header.Get("Cookie"))
	secondResp, err := app.CreatePublicRoute(context.Background(), secondReq)
	if err != nil {
		t.Fatalf("create second route: %v", err)
	}

	forgedHeader := firstResp.Msg.Route.Targets[0].UpstreamRequestHeaders[0]
	forgedHeader.TargetId = secondResp.Msg.Route.Targets[0].Id
	forgedTarget := secondResp.Msg.Route.Targets[0]
	forgedTarget.UpstreamRequestHeaders = []*p2pstreamv1.PublicRouteTargetUpstreamHeader{forgedHeader}
	updateReq := testPublicRouteUpdateRequest(secondResp.Msg.Route, "/second-secret", []*p2pstreamv1.PublicRouteTarget{forgedTarget})
	updateReq.Header().Set("Cookie", header.Get("Cookie"))
	_, err = app.UpdatePublicRoute(context.Background(), updateReq)
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("update with cross-route masked header error = %v, want invalid argument", err)
	}
	assertPublicRouteUpstreamHeaderValues(t, app.DB, secondResp.Msg.Route.Id, map[string]string{
		"x-secret": "second-secret",
	})
}

func TestPublicRouteUpdateOmitsUpstreamHeaderToDelete(t *testing.T) {
	app := NewApp(nil, newServerTestDB(t))
	header := createTestAdminSession(t, app)
	listener := seedPublicConfigTestListener(t, app.DB)

	createReq := testPublicRouteRequest(listener.ID, "/delete-secret", []*p2pstreamv1.PublicRouteTargetUpstreamHeader{
		{Name: "X-Secret", Value: "top-secret", Sensitive: true, ValueSet: true},
	})
	createReq.Header().Set("Cookie", header.Get("Cookie"))
	createResp, err := app.CreatePublicRoute(context.Background(), createReq)
	if err != nil {
		t.Fatalf("create route: %v", err)
	}

	updateTarget := createResp.Msg.Route.Targets[0]
	updateTarget.UpstreamRequestHeaders = nil
	updateReq := testPublicRouteUpdateRequest(createResp.Msg.Route, "/delete-secret", []*p2pstreamv1.PublicRouteTarget{updateTarget})
	updateReq.Header().Set("Cookie", header.Get("Cookie"))
	updateResp, err := app.UpdatePublicRoute(context.Background(), updateReq)
	if err != nil {
		t.Fatalf("update route without header: %v", err)
	}
	assertPublicRouteUpstreamHeaderValues(t, app.DB, updateResp.Msg.Route.Id, map[string]string{})
}

func seedPublicConfigTestListener(t *testing.T, database *db.DB) db.PublicListener {
	t.Helper()
	listener, err := database.CreatePublicListener(context.Background(), db.CreatePublicListenerParams{
		Name:        "public-http",
		BindAddress: "",
		Port:        8080,
		Protocol:    publicListenerProtocolHTTP,
		Enabled:     1,
	})
	if err != nil {
		t.Fatalf("create listener: %v", err)
	}
	return listener
}

func testPublicRouteRequest(listenerID int64, pathPrefix string, headers []*p2pstreamv1.PublicRouteTargetUpstreamHeader) *connect.Request[p2pstreamv1.CreatePublicRouteRequest] {
	return connect.NewRequest(&p2pstreamv1.CreatePublicRouteRequest{
		ListenerId:       listenerID,
		Priority:         10,
		PathPrefix:       pathPrefix,
		Enabled:          true,
		Action:           p2pstreamv1.PublicRouteAction_PUBLIC_ROUTE_ACTION_FORWARD,
		PathSecurityMode: p2pstreamv1.PublicRoutePathSecurityMode_PUBLIC_ROUTE_PATH_SECURITY_MODE_STRICT,
		Targets: []*p2pstreamv1.PublicRouteTarget{{
			Name:                   "target-1",
			Enabled:                true,
			TargetType:             p2pstreamv1.PublicRouteTargetType_PUBLIC_ROUTE_TARGET_TYPE_PROXY,
			Url:                    "http://127.0.0.1:9000",
			Transport:              p2pstreamv1.PublicRouteTargetTransport_PUBLIC_ROUTE_TARGET_TRANSPORT_DIRECT,
			Weight:                 100,
			UpstreamRequestHeaders: headers,
		}},
	})
}

func testPublicRouteUpdateRequest(route *p2pstreamv1.PublicRoute, pathPrefix string, targets []*p2pstreamv1.PublicRouteTarget) *connect.Request[p2pstreamv1.UpdatePublicRouteRequest] {
	return connect.NewRequest(&p2pstreamv1.UpdatePublicRouteRequest{
		Id:                  route.Id,
		ListenerId:          route.ListenerId,
		Priority:            route.Priority,
		PathPrefix:          pathPrefix,
		Enabled:             route.Enabled,
		Action:              p2pstreamv1.PublicRouteAction_PUBLIC_ROUTE_ACTION_FORWARD,
		TargetLoadBalancing: route.TargetLoadBalancing,
		IsDefault:           route.IsDefault,
		Targets:             targets,
		PathSecurityMode:    route.PathSecurityMode,
	})
}

func assertPublicRouteUpstreamHeaderValues(t *testing.T, database *db.DB, routeID int64, want map[string]string) {
	t.Helper()
	targets, err := database.ListPublicRouteTargetsByRoute(context.Background(), routeID)
	if err != nil {
		t.Fatalf("list targets: %v", err)
	}
	got := make(map[string]string)
	for _, target := range targets {
		headers, err := database.ListPublicRouteTargetUpstreamHeadersByTarget(context.Background(), target.ID)
		if err != nil {
			t.Fatalf("list upstream headers: %v", err)
		}
		for _, header := range headers {
			got[strings.ToLower(header.Name)] = header.Value
		}
	}
	if len(got) != len(want) {
		t.Fatalf("upstream headers = %+v, want %+v", got, want)
	}
	for name, wantValue := range want {
		if got[name] != wantValue {
			t.Fatalf("upstream header %s = %q, want %q (all headers %+v)", name, got[name], wantValue, got)
		}
	}
}
