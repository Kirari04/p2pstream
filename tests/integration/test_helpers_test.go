package integration_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/gen/proto/p2pstream/v1/p2pstreamv1connect"
	"p2pstream/internal/config"
	"p2pstream/internal/db"
	"p2pstream/internal/server"
)

const (
	testAdminUsername = "admin"
	testAdminPassword = "correct horse battery staple"
	testSetupToken    = "test-setup-token"
)

func testManagementConfig(cfg config.Config) *config.Config {
	if cfg.ManagementSetupToken == "" {
		cfg.ManagementSetupToken = testSetupToken
	}
	return &cfg
}

func newTestDB(t *testing.T) *db.DB {
	t.Helper()

	database, err := db.Open(filepath.Join(t.TempDir(), "p2pstream-test.db"))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Errorf("close test db: %v", err)
		}
	})
	return database
}

func seedTestHTTPPublicListener(t *testing.T, database *db.DB, targetOrigin string) db.PublicListener {
	t.Helper()

	listener, err := database.CreatePublicListener(context.Background(), db.CreatePublicListenerParams{
		Name:        "test-http",
		BindAddress: "127.0.0.1",
		Port:        0,
		Protocol:    "http",
		Enabled:     1,
	})
	if err != nil {
		t.Fatalf("seed test listener: %v", err)
	}
	route, err := database.CreatePublicRoute(context.Background(), db.CreatePublicRouteParams{
		ListenerID:                 listener.ID,
		Priority:                   1000,
		PathPrefix:                 "/",
		TargetLoadBalancing:        "round_robin",
		IsDefault:                  1,
		Action:                     "forward",
		RedirectStatusCode:         http.StatusFound,
		RedirectPreservePathSuffix: 1,
		RedirectPreserveQuery:      1,
		Enabled:                    1,
	})
	if err != nil {
		t.Fatalf("seed test route: %v", err)
	}
	if _, err := database.CreatePublicRouteTarget(context.Background(), db.CreatePublicRouteTargetParams{
		RouteID:                             route.ID,
		Name:                                "test-default",
		Position:                            0,
		PriorityGroup:                       0,
		Weight:                              100,
		Enabled:                             1,
		TargetType:                          "proxy",
		Url:                                 targetOrigin,
		Transport:                           "direct",
		AgentSelectorJson:                   "{}",
		AgentLoadBalancing:                  "round_robin",
		UpstreamResponseHeaderTimeoutMillis: 60000,
		HealthCheckMethod:                   http.MethodGet,
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
		t.Fatalf("seed test route target: %v", err)
	}
	return listener
}

func newTestManagementClient(
	t *testing.T,
	app *server.App,
) (*httptest.Server, p2pstreamv1connect.AgentManagementServiceClient) {
	t.Helper()

	mgmtMux := http.NewServeMux()
	app.RegisterManagementRoutes(mgmtMux)

	p := new(http.Protocols)
	p.SetHTTP1(true)
	p.SetUnencryptedHTTP2(true)

	mgmtSrv := httptest.NewUnstartedServer(mgmtMux)
	mgmtSrv.Config.Protocols = p
	mgmtSrv.Start()
	t.Cleanup(mgmtSrv.Close)

	client := p2pstreamv1connect.NewAgentManagementServiceClient(
		http.DefaultClient,
		mgmtSrv.URL,
		connect.WithGRPC(),
	)
	return mgmtSrv, client
}

func createAdminSession(t *testing.T, client p2pstreamv1connect.AgentManagementServiceClient) string {
	t.Helper()

	ctx := context.Background()
	_, err := client.SetupAdmin(ctx, connect.NewRequest(&p2pstreamv1.SetupAdminRequest{
		Username:   testAdminUsername,
		Password:   testAdminPassword,
		SetupToken: testSetupToken,
	}))
	if err != nil {
		t.Fatalf("setup admin: %v", err)
	}

	loginResp, err := client.Login(ctx, connect.NewRequest(&p2pstreamv1.LoginRequest{
		Username: testAdminUsername,
		Password: testAdminPassword,
	}))
	if err != nil {
		t.Fatalf("login admin: %v", err)
	}
	return cookieHeaderFromSetCookie(t, loginResp.Header().Get("Set-Cookie"))
}

func cookieHeaderFromSetCookie(t *testing.T, setCookie string) string {
	t.Helper()
	if setCookie == "" {
		t.Fatal("missing Set-Cookie header")
	}
	return strings.Split(setCookie, ";")[0]
}

func requireConnectCode(t *testing.T, err error, code connect.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected connect code %s, got nil", code)
	}
	var connectErr *connect.Error
	if !errors.As(err, &connectErr) {
		t.Fatalf("expected connect error %s, got %T: %v", code, err, err)
	}
	if connectErr.Code() != code {
		t.Fatalf("expected connect code %s, got %s: %v", code, connectErr.Code(), err)
	}
}
