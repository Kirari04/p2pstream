package server

import (
	"context"
	"path/filepath"
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
