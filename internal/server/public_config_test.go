package server

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/internal/config"
	"p2pstream/internal/db"
	secretspkg "p2pstream/internal/secrets"
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

func TestPublicRouteSecretsEncryptedAtRestAndPreserved(t *testing.T) {
	app := NewApp(&config.Config{
		SecretsEncryptionKey:   testSecretsEncryptionKey(),
		SecretsEncryptionKeyID: "test-key",
	}, newServerTestDB(t))
	if err := app.InitializeSecretStorage(context.Background()); err != nil {
		t.Fatalf("initialize secret storage: %v", err)
	}
	header := createTestAdminSession(t, app)
	listener := seedPublicConfigTestListener(t, app.DB)

	createReq := testPublicRouteRequest(listener.ID, "/encrypted-secret", []*p2pstreamv1.PublicRouteTargetUpstreamHeader{
		{Name: "X-Secret", Value: "top-secret", Sensitive: true, ValueSet: true},
		{Name: "Cookie", Value: "session=abc", ValueSet: true},
	})
	createReq.Msg.Targets[0].UpstreamBasicAuth = &p2pstreamv1.PublicRouteTargetBasicAuth{
		Enabled:  true,
		Username: "origin",
		Password: "origin-password",
	}
	createReq.Header().Set("Cookie", header.Get("Cookie"))
	createResp, err := app.CreatePublicRoute(context.Background(), createReq)
	if err != nil {
		t.Fatalf("create route: %v", err)
	}
	if got := createResp.Msg.Route.Targets[0].UpstreamBasicAuth; got == nil || !got.PasswordSet || got.Password != "" {
		t.Fatalf("basic auth proto = %+v, want masked password_set", got)
	}
	for _, got := range createResp.Msg.Route.Targets[0].UpstreamRequestHeaders {
		if got.Value != "" || got.ValueSet {
			t.Fatalf("sensitive header proto = %+v, want masked", got)
		}
	}
	assertEncryptedRouteTargetSecrets(t, app, createResp.Msg.Route.Id, map[string]string{
		"x-secret": "top-secret",
		"cookie":   "session=abc",
	}, "origin-password")

	updateTarget := createResp.Msg.Route.Targets[0]
	updateTarget.Name = "renamed-target"
	updateReq := testPublicRouteUpdateRequest(createResp.Msg.Route, "/encrypted-secret-updated", []*p2pstreamv1.PublicRouteTarget{updateTarget})
	updateReq.Header().Set("Cookie", header.Get("Cookie"))
	updateResp, err := app.UpdatePublicRoute(context.Background(), updateReq)
	if err != nil {
		t.Fatalf("update route with masked secrets: %v", err)
	}
	if updateResp.Msg.Route.Targets[0].Id == createResp.Msg.Route.Targets[0].Id {
		t.Fatal("expected route update to recreate target with a new ID")
	}
	assertEncryptedRouteTargetSecrets(t, app, updateResp.Msg.Route.Id, map[string]string{
		"x-secret": "top-secret",
		"cookie":   "session=abc",
	}, "origin-password")
}

func TestPublicRouteSensitiveHeaderEncryptionBindsHeaderRow(t *testing.T) {
	app := NewApp(&config.Config{
		SecretsEncryptionKey:   testSecretsEncryptionKey(),
		SecretsEncryptionKeyID: "test-key",
	}, newServerTestDB(t))
	if err := app.InitializeSecretStorage(context.Background()); err != nil {
		t.Fatalf("initialize secret storage: %v", err)
	}
	header := createTestAdminSession(t, app)
	listener := seedPublicConfigTestListener(t, app.DB)

	createReq := testPublicRouteRequest(listener.ID, "/header-aad", []*p2pstreamv1.PublicRouteTargetUpstreamHeader{
		{Name: "Authorization", Value: "Bearer route-token", ValueSet: true},
		{Name: "Cookie", Value: "session=abc", ValueSet: true},
	})
	createReq.Header().Set("Cookie", header.Get("Cookie"))
	createResp, err := app.CreatePublicRoute(context.Background(), createReq)
	if err != nil {
		t.Fatalf("create route: %v", err)
	}
	targets, err := app.DB.ListPublicRouteTargetsByRoute(context.Background(), createResp.Msg.Route.Id)
	if err != nil {
		t.Fatalf("list route targets: %v", err)
	}
	if len(targets) != 1 {
		t.Fatalf("targets len = %d, want 1", len(targets))
	}
	headers, err := app.DB.ListPublicRouteTargetUpstreamHeadersByTarget(context.Background(), targets[0].ID)
	if err != nil {
		t.Fatalf("list upstream headers: %v", err)
	}
	if len(headers) != 2 {
		t.Fatalf("headers len = %d, want 2", len(headers))
	}

	for _, header := range headers {
		if !secretspkg.IsEncrypted(header.Value) {
			t.Fatalf("stored header %s = %q, want encrypted", header.Name, header.Value)
		}
		if _, _, err := app.decryptSecret(secretspkg.PurposePublicRouteTargetSensitiveHeader, header.ID, header.Value); err != nil {
			t.Fatalf("decrypt header %s with own ID: %v", header.Name, err)
		}
	}
	if _, _, err := app.decryptSecret(secretspkg.PurposePublicRouteTargetSensitiveHeader, headers[1].ID, headers[0].Value); err == nil {
		t.Fatal("expected first header ciphertext to reject second header ID")
	}
	if _, _, err := app.decryptSecret(secretspkg.PurposePublicRouteTargetSensitiveHeader, headers[0].ID, headers[1].Value); err == nil {
		t.Fatal("expected second header ciphertext to reject first header ID")
	}
}

func TestForcedSensitiveUpstreamHeadersMaskLegacyRows(t *testing.T) {
	headers := publicRouteTargetUpstreamHeadersToProto([]db.PublicRouteTargetUpstreamHeader{{
		ID:        1,
		TargetID:  2,
		Name:      "Cookie",
		Value:     "session=legacy",
		Sensitive: 0,
	}})
	if len(headers) != 1 {
		t.Fatalf("headers len = %d, want 1", len(headers))
	}
	if got := headers[0]; got.Value != "" || got.ValueSet || !got.Sensitive {
		t.Fatalf("forced-sensitive header proto = %+v, want masked sensitive", got)
	}
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

func assertEncryptedRouteTargetSecrets(t *testing.T, app *App, routeID int64, wantHeaders map[string]string, wantPassword string) {
	t.Helper()
	targets, err := app.DB.ListPublicRouteTargetsByRoute(context.Background(), routeID)
	if err != nil {
		t.Fatalf("list targets: %v", err)
	}
	if len(targets) != 1 {
		t.Fatalf("targets len = %d, want 1", len(targets))
	}
	target := targets[0]
	if !secretspkg.IsEncrypted(target.UpstreamBasicAuthPassword) || strings.Contains(target.UpstreamBasicAuthPassword, wantPassword) {
		t.Fatalf("stored basic auth password = %q, want encrypted without plaintext", target.UpstreamBasicAuthPassword)
	}
	gotPassword, _, err := app.decryptSecret(secretspkg.PurposePublicRouteTargetBasicAuthPassword, target.ID, target.UpstreamBasicAuthPassword)
	if err != nil {
		t.Fatalf("decrypt basic auth password: %v", err)
	}
	if gotPassword != wantPassword {
		t.Fatalf("decrypted basic auth password = %q, want %q", gotPassword, wantPassword)
	}

	headers, err := app.DB.ListPublicRouteTargetUpstreamHeadersByTarget(context.Background(), target.ID)
	if err != nil {
		t.Fatalf("list upstream headers: %v", err)
	}
	gotHeaders := make(map[string]string)
	for _, header := range headers {
		if !secretspkg.IsEncrypted(header.Value) {
			t.Fatalf("stored header %s = %q, want encrypted", header.Name, header.Value)
		}
		value, _, err := app.decryptSecret(secretspkg.PurposePublicRouteTargetSensitiveHeader, header.ID, header.Value)
		if err != nil {
			t.Fatalf("decrypt header %s: %v", header.Name, err)
		}
		gotHeaders[strings.ToLower(header.Name)] = value
	}
	if len(gotHeaders) != len(wantHeaders) {
		t.Fatalf("headers = %+v, want %+v", gotHeaders, wantHeaders)
	}
	for name, want := range wantHeaders {
		if gotHeaders[name] != want {
			t.Fatalf("header %s = %q, want %q (all %+v)", name, gotHeaders[name], want, gotHeaders)
		}
	}
}

func testSecretsEncryptionKey() string {
	return "AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA"
}
