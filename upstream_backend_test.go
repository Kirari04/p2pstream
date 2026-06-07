package main_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/internal/config"
	"p2pstream/internal/db"
	"p2pstream/internal/server"
)

func TestPublicRouteTargetUpstreamConfigAPIValidationAndReadback(t *testing.T) {
	app := server.NewApp(testManagementConfig(config.Config{}), newTestDB(t))
	_, client := newTestManagementClient(t, app)
	cookie := createAdminSession(t, client)
	cfg := getPublicProxyConfig(t, client, cookie)
	listener := publicListenerByName(t, cfg, "public-http")

	createReq := connect.NewRequest(&p2pstreamv1.CreatePublicRouteRequest{
		ListenerId: listener.GetId(),
		Priority:   20,
		PathPrefix: "/upstream-api",
		Enabled:    true,
		Targets: []*p2pstreamv1.PublicRouteTarget{{
			Name:       "upstream-api",
			TargetType: p2pstreamv1.PublicRouteTargetType_PUBLIC_ROUTE_TARGET_TYPE_PROXY,
			Transport:  p2pstreamv1.PublicRouteTargetTransport_PUBLIC_ROUTE_TARGET_TRANSPORT_DIRECT,
			Url:        "http://example.com",
			Enabled:    true,
			UpstreamRequestHeaders: []*p2pstreamv1.PublicBackendUpstreamHeader{
				{Name: "X-Visible", Value: "visible", ValueSet: true},
				{Name: "Cookie", Value: "session=secret", ValueSet: true},
				{Name: "X-Secret", Value: "hidden", Sensitive: true, ValueSet: true},
			},
			UpstreamBasicAuth: &p2pstreamv1.PublicBackendBasicAuth{
				Enabled:     true,
				Username:    "service",
				Password:    "old-password",
				PasswordSet: true,
			},
		}},
	})
	createReq.Header().Set("Cookie", cookie)
	createResp, err := client.CreatePublicRoute(context.Background(), createReq)
	if err != nil {
		t.Fatalf("create route target with upstream config: %v", err)
	}
	created := createResp.Msg.GetRoute().GetTargets()[0]
	if got := created.GetUpstreamBasicAuth(); !got.GetEnabled() || got.GetUsername() != "service" || got.GetPassword() != "" || !got.GetPasswordSet() {
		t.Fatalf("unexpected masked basic auth readback: %+v", got)
	}
	assertUpstreamHeaderProto(t, created, "X-Visible", "visible", false, true)
	assertUpstreamHeaderProto(t, created, "Cookie", "", true, true)
	assertUpstreamHeaderProto(t, created, "X-Secret", "", true, true)

	cfg = getPublicProxyConfig(t, client, cookie)
	readBack := publicRouteTargetByName(t, cfg, "upstream-api")
	assertUpstreamHeaderProto(t, readBack, "X-Visible", "visible", false, true)
	assertUpstreamHeaderProto(t, readBack, "Cookie", "", true, true)
	assertUpstreamHeaderProto(t, readBack, "X-Secret", "", true, true)
	if readBack.GetUpstreamBasicAuth().GetPassword() != "" || !readBack.GetUpstreamBasicAuth().GetPasswordSet() {
		t.Fatalf("expected masked saved basic auth password in config, got %+v", readBack.GetUpstreamBasicAuth())
	}
}

func TestPublicRouteTargetUpstreamResponseHeaderTimeoutAPI(t *testing.T) {
	app := server.NewApp(testManagementConfig(config.Config{}), newTestDB(t))
	_, client := newTestManagementClient(t, app)
	cookie := createAdminSession(t, client)
	cfg := getPublicProxyConfig(t, client, cookie)
	listener := publicListenerByName(t, cfg, "public-http")

	createReq := connect.NewRequest(&p2pstreamv1.CreatePublicRouteRequest{
		ListenerId: listener.GetId(),
		Priority:   30,
		PathPrefix: "/timeout-api",
		Enabled:    true,
		Targets: []*p2pstreamv1.PublicRouteTarget{{
			Name:       "timeout-api",
			TargetType: p2pstreamv1.PublicRouteTargetType_PUBLIC_ROUTE_TARGET_TYPE_PROXY,
			Transport:  p2pstreamv1.PublicRouteTargetTransport_PUBLIC_ROUTE_TARGET_TRANSPORT_DIRECT,
			Url:        "http://example.com",
			Enabled:    true,
		}},
	})
	createReq.Header().Set("Cookie", cookie)
	createResp, err := client.CreatePublicRoute(context.Background(), createReq)
	if err != nil {
		t.Fatalf("create target with default timeout: %v", err)
	}
	if got := createResp.Msg.GetRoute().GetTargets()[0].GetUpstreamResponseHeaderTimeoutMillis(); got != 60000 {
		t.Fatalf("default upstream response header timeout = %d, want 60000", got)
	}

	updateReq := connect.NewRequest(&p2pstreamv1.UpdatePublicRouteRequest{
		Id:         createResp.Msg.GetRoute().GetId(),
		ListenerId: listener.GetId(),
		Priority:   30,
		PathPrefix: "/timeout-api",
		Enabled:    true,
		Targets: []*p2pstreamv1.PublicRouteTarget{{
			Name:                                "timeout-api",
			TargetType:                          p2pstreamv1.PublicRouteTargetType_PUBLIC_ROUTE_TARGET_TYPE_PROXY,
			Transport:                           p2pstreamv1.PublicRouteTargetTransport_PUBLIC_ROUTE_TARGET_TRANSPORT_DIRECT,
			Url:                                 "http://example.com",
			Enabled:                             true,
			UpstreamResponseHeaderTimeoutMillis: 45000,
		}},
	})
	updateReq.Header().Set("Cookie", cookie)
	updateResp, err := client.UpdatePublicRoute(context.Background(), updateReq)
	if err != nil {
		t.Fatalf("update target timeout: %v", err)
	}
	if got := updateResp.Msg.GetRoute().GetTargets()[0].GetUpstreamResponseHeaderTimeoutMillis(); got != 45000 {
		t.Fatalf("updated upstream response header timeout = %d, want 45000", got)
	}

	for _, timeoutMillis := range []int64{999, 3600001} {
		req := connect.NewRequest(&p2pstreamv1.CreatePublicRouteRequest{
			ListenerId: listener.GetId(),
			Priority:   40 + timeoutMillis,
			PathPrefix: "/timeout-invalid",
			Enabled:    true,
			Targets: []*p2pstreamv1.PublicRouteTarget{{
				Name:                                "timeout-invalid",
				TargetType:                          p2pstreamv1.PublicRouteTargetType_PUBLIC_ROUTE_TARGET_TYPE_PROXY,
				Transport:                           p2pstreamv1.PublicRouteTargetTransport_PUBLIC_ROUTE_TARGET_TRANSPORT_DIRECT,
				Url:                                 "http://example.com",
				Enabled:                             true,
				UpstreamResponseHeaderTimeoutMillis: timeoutMillis,
			}},
		})
		req.Header().Set("Cookie", cookie)
		if _, err := client.CreatePublicRoute(context.Background(), req); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("timeout %d: expected invalid argument, got %v", timeoutMillis, err)
		}
	}
}

func TestPublicRouteTargetUpstreamConfigValidationRejectsInvalidInputs(t *testing.T) {
	app := server.NewApp(testManagementConfig(config.Config{}), newTestDB(t))
	_, client := newTestManagementClient(t, app)
	cookie := createAdminSession(t, client)
	cfg := getPublicProxyConfig(t, client, cookie)
	listener := publicListenerByName(t, cfg, "public-http")

	cases := []struct {
		name    string
		headers []*p2pstreamv1.PublicBackendUpstreamHeader
		auth    *p2pstreamv1.PublicBackendBasicAuth
	}{
		{
			name: "duplicate-header",
			headers: []*p2pstreamv1.PublicBackendUpstreamHeader{
				{Name: "X-Dupe", Value: "1", ValueSet: true},
				{Name: "x-dupe", Value: "2", ValueSet: true},
			},
		},
		{name: "blocked-header", headers: []*p2pstreamv1.PublicBackendUpstreamHeader{{Name: "Host", Value: "example.com", ValueSet: true}}},
		{
			name:    "authorization-with-basic-auth",
			headers: []*p2pstreamv1.PublicBackendUpstreamHeader{{Name: "Authorization", Value: "Bearer token", ValueSet: true}},
			auth:    &p2pstreamv1.PublicBackendBasicAuth{Enabled: true, Username: "service", Password: "secret", PasswordSet: true},
		},
		{name: "missing-basic-auth-username", auth: &p2pstreamv1.PublicBackendBasicAuth{Enabled: true, Password: "secret", PasswordSet: true}},
		{name: "missing-basic-auth-password", auth: &p2pstreamv1.PublicBackendBasicAuth{Enabled: true, Username: "service"}},
	}

	for idx, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := connect.NewRequest(&p2pstreamv1.CreatePublicRouteRequest{
				ListenerId: listener.GetId(),
				Priority:   int64(100 + idx),
				PathPrefix: "/" + tc.name,
				Enabled:    true,
				Targets: []*p2pstreamv1.PublicRouteTarget{{
					Name:                   tc.name,
					TargetType:             p2pstreamv1.PublicRouteTargetType_PUBLIC_ROUTE_TARGET_TYPE_PROXY,
					Transport:              p2pstreamv1.PublicRouteTargetTransport_PUBLIC_ROUTE_TARGET_TRANSPORT_DIRECT,
					Url:                    "http://example.com",
					Enabled:                true,
					UpstreamRequestHeaders: tc.headers,
					UpstreamBasicAuth:      tc.auth,
				}},
			})
			req.Header().Set("Cookie", cookie)
			if _, err := client.CreatePublicRoute(context.Background(), req); connect.CodeOf(err) != connect.CodeInvalidArgument {
				t.Fatalf("expected invalid argument, got %v", err)
			}
		})
	}
}

func TestStaticPublicRouteTargetClearsUpstreamConfig(t *testing.T) {
	app := server.NewApp(testManagementConfig(config.Config{}), newTestDB(t))
	_, client := newTestManagementClient(t, app)
	cookie := createAdminSession(t, client)
	cfg := getPublicProxyConfig(t, client, cookie)
	listener := publicListenerByName(t, cfg, "public-http")

	req := connect.NewRequest(&p2pstreamv1.CreatePublicRouteRequest{
		ListenerId: listener.GetId(),
		Priority:   200,
		PathPrefix: "/static-clears-upstream",
		Enabled:    true,
		Targets: []*p2pstreamv1.PublicRouteTarget{{
			Name:             "static-clears-upstream",
			TargetType:       p2pstreamv1.PublicRouteTargetType_PUBLIC_ROUTE_TARGET_TYPE_STATIC,
			StaticStatusCode: http.StatusNoContent,
			UpstreamRequestHeaders: []*p2pstreamv1.PublicBackendUpstreamHeader{
				{Name: "X-Ignored", Value: "ignored", ValueSet: true},
			},
			UpstreamBasicAuth: &p2pstreamv1.PublicBackendBasicAuth{
				Enabled:     true,
				Username:    "ignored",
				Password:    "ignored",
				PasswordSet: true,
			},
			Enabled: true,
		}},
	})
	req.Header().Set("Cookie", cookie)
	resp, err := client.CreatePublicRoute(context.Background(), req)
	if err != nil {
		t.Fatalf("create static target with ignored upstream config: %v", err)
	}
	target := resp.Msg.GetRoute().GetTargets()[0]
	if len(target.GetUpstreamRequestHeaders()) != 0 || target.GetUpstreamBasicAuth().GetEnabled() || target.GetUpstreamBasicAuth().GetPasswordSet() {
		t.Fatalf("static target should clear upstream config, got headers=%+v auth=%+v", target.GetUpstreamRequestHeaders(), target.GetUpstreamBasicAuth())
	}
}

func TestDirectPublicRouteTargetAppliesUpstreamRequestConfig(t *testing.T) {
	targetSrv := upstreamConfigEchoServer(t, "direct upstream ok")
	defer targetSrv.Close()

	database := newTestDB(t)
	listener := seedProxyRouteTargetListener(t, database, "direct-upstream-listener", targetSrv.URL, "direct", "")
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

	listenerAddr := publicListenerBoundAddress(t, status, listener.ID)
	req, err := http.NewRequest(http.MethodPost, "http://"+listenerAddr+"/upstream", bytes.NewReader([]byte("payload")))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("X-Upstream", "incoming")
	req.Header.Set("X-Override", "incoming")
	req.Header.Set("X-Client", "client-kept")
	setSpoofedForwardedHeaders(req)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("direct upstream request: %v", err)
	}
	defer resp.Body.Close()
	if body := mustReadResponseBody(t, resp); resp.StatusCode != http.StatusOK || body != "direct upstream ok" {
		t.Fatalf("unexpected direct upstream response: status=%d body=%q", resp.StatusCode, body)
	}
	if resp.Header.Get("X-Got-Upstream") != "configured" ||
		resp.Header.Get("X-Got-Override") != "configured" ||
		resp.Header.Get("X-Got-Client") != "client-kept" ||
		resp.Header.Get("X-Got-Basic-Auth") != "true" {
		t.Fatalf("upstream request config was not applied, response headers=%+v", resp.Header)
	}
	assertTrustedForwardedHeaders(t, resp.Header, listenerAddr)
}

func TestPublicRoutePathPrefixUsesSegmentBoundaries(t *testing.T) {
	defaultSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("default target"))
	}))
	defer defaultSrv.Close()
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("api target"))
	}))
	defer apiSrv.Close()

	database := newTestDB(t)
	listener := seedProxyRouteTargetListener(t, database, "prefix-listener", defaultSrv.URL, "direct", "")
	if _, err := createProxyRouteTarget(t, database, listener.ID, 1, "/api", apiSrv.URL, "direct", "", false); err != nil {
		t.Fatalf("create api route target: %v", err)
	}
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
	apiResp, err := http.Get(baseURL + "/api/users")
	if err != nil {
		t.Fatalf("api request: %v", err)
	}
	defer apiResp.Body.Close()
	if body := mustReadResponseBody(t, apiResp); body != "api target" {
		t.Fatalf("/api response body = %q, want api target", body)
	}

	defaultResp, err := http.Get(baseURL + "/apiv2/users")
	if err != nil {
		t.Fatalf("apiv2 request: %v", err)
	}
	defer defaultResp.Body.Close()
	if body := mustReadResponseBody(t, defaultResp); body != "default target" {
		t.Fatalf("/apiv2 response body = %q, want default target", body)
	}
}

func TestAgentPublicRouteTargetAppliesUpstreamRequestConfig(t *testing.T) {
	targetSrv := upstreamConfigEchoServer(t, "agent upstream ok")
	defer targetSrv.Close()

	database := newTestDB(t)
	app := server.NewApp(testManagementConfig(config.Config{}), database)
	mgmtSrv, client := newTestManagementClient(t, app)
	cookie := createAdminSession(t, client)
	agent, token := createRegisteredAgent(t, client, cookie, "upstream-agent", "Upstream Agent")
	listener := seedProxyRouteTargetListener(t, database, "agent-upstream-listener", targetSrv.URL, "agent", agent.GetPublicId())

	status, err := app.StartProxyListener(context.Background())
	if err != nil {
		t.Fatalf("start proxy: %v", err)
	}
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = app.StopProxyListener(shutdownCtx)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	agentDone := make(chan struct{})
	go func() {
		_ = runAgent(ctx, mgmtSrv.URL, agent.GetPublicId(), token)
		close(agentDone)
	}()
	time.Sleep(250 * time.Millisecond)

	listenerAddr := publicListenerBoundAddress(t, status, listener.ID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+listenerAddr+"/upstream", nil)
	if err != nil {
		t.Fatalf("new agent upstream request: %v", err)
	}
	req.Header.Set("X-Upstream", "incoming")
	req.Header.Set("X-Override", "incoming")
	setSpoofedForwardedHeaders(req)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("agent upstream request: %v", err)
	}
	defer resp.Body.Close()
	if body := mustReadResponseBody(t, resp); resp.StatusCode != http.StatusOK || body != "agent upstream ok" {
		t.Fatalf("unexpected agent upstream response: status=%d body=%q", resp.StatusCode, body)
	}
	if resp.Header.Get("X-Got-Upstream") != "configured" ||
		resp.Header.Get("X-Got-Override") != "configured" ||
		resp.Header.Get("X-Got-Basic-Auth") != "true" {
		t.Fatalf("agent upstream request config was not applied, response headers=%+v", resp.Header)
	}
	assertTrustedForwardedHeaders(t, resp.Header, listenerAddr)

	cancel()
	<-agentDone
}

func upstreamConfigEchoServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, password, ok := r.BasicAuth()
		w.Header().Set("X-Got-Upstream", r.Header.Get("X-Upstream"))
		w.Header().Set("X-Got-Override", r.Header.Get("X-Override"))
		w.Header().Set("X-Got-Client", r.Header.Get("X-Client"))
		w.Header().Set("X-Got-Forwarded", r.Header.Get("Forwarded"))
		w.Header().Set("X-Got-Forwarded-For", r.Header.Get("X-Forwarded-For"))
		w.Header().Set("X-Got-Forwarded-Host", r.Header.Get("X-Forwarded-Host"))
		w.Header().Set("X-Got-Forwarded-Proto", r.Header.Get("X-Forwarded-Proto"))
		w.Header().Set("X-Got-Forwarded-Port", r.Header.Get("X-Forwarded-Port"))
		w.Header().Set("X-Got-Real-IP", r.Header.Get("X-Real-IP"))
		if ok && user == "service" && password == "secret" {
			w.Header().Set("X-Got-Basic-Auth", "true")
		}
		_, _ = w.Write([]byte(body))
	}))
}

func seedProxyRouteTargetListener(t *testing.T, database *db.DB, name string, targetOrigin string, transport string, agentPublicID string) db.PublicListener {
	t.Helper()
	backend, err := database.CreatePublicBackend(context.Background(), db.CreatePublicBackendParams{
		Name:          name + "-legacy",
		TargetOrigin:  targetOrigin,
		BackendType:   "proxy_forward",
		ForwardMode:   "direct",
		LoadBalancing: "round_robin",
		Enabled:       1,
	})
	if err != nil {
		t.Fatalf("create legacy backend %s: %v", name, err)
	}
	listener, err := database.CreatePublicListener(context.Background(), db.CreatePublicListenerParams{
		Name:             name,
		BindAddress:      "127.0.0.1",
		Port:             0,
		Protocol:         "http",
		Enabled:          1,
		DefaultBackendID: backend.ID,
	})
	if err != nil {
		t.Fatalf("create listener %s: %v", name, err)
	}
	if _, err := createProxyRouteTarget(t, database, listener.ID, 1000, "/", targetOrigin, transport, agentPublicID, true); err != nil {
		t.Fatalf("create default route target %s: %v", name, err)
	}
	return listener
}

func createProxyRouteTarget(t *testing.T, database *db.DB, listenerID int64, priority int64, pathPrefix string, targetOrigin string, transport string, agentPublicID string, isDefault bool) (db.PublicRouteTarget, error) {
	t.Helper()
	route, err := database.CreatePublicRoute(context.Background(), db.CreatePublicRouteParams{
		ListenerID:                 listenerID,
		Priority:                   priority,
		PathPrefix:                 pathPrefix,
		BackendID:                  sql.NullInt64{},
		LoadBalancing:              "round_robin",
		FallbackBackendID:          sql.NullInt64{},
		TargetLoadBalancing:        "round_robin",
		IsDefault:                  routeTargetBoolIntForTest(isDefault),
		Action:                     "forward",
		RedirectStatusCode:         http.StatusFound,
		RedirectPreservePathSuffix: 1,
		RedirectPreserveQuery:      1,
		Enabled:                    1,
	})
	if err != nil {
		return db.PublicRouteTarget{}, err
	}
	selectorJSON := "{}"
	if transport == "agent" {
		payload, err := json.Marshal(map[string]map[string]string{
			"match_labels": {"p2pstream.io/agent-id": agentPublicID},
		})
		if err != nil {
			return db.PublicRouteTarget{}, err
		}
		selectorJSON = string(payload)
	}
	target, err := database.CreatePublicRouteTarget(context.Background(), db.CreatePublicRouteTargetParams{
		RouteID:                             route.ID,
		Name:                                pathPrefix + "-target",
		Position:                            0,
		PriorityGroup:                       0,
		Weight:                              100,
		Enabled:                             1,
		TargetType:                          "proxy",
		Url:                                 targetOrigin,
		Transport:                           transport,
		AgentSelectorJson:                   selectorJSON,
		AgentLoadBalancing:                  "round_robin",
		UpstreamBasicAuthEnabled:            1,
		UpstreamBasicAuthUsername:           "service",
		UpstreamBasicAuthPassword:           "secret",
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
	})
	if err != nil {
		return db.PublicRouteTarget{}, err
	}
	for index, header := range []struct {
		name  string
		value string
	}{
		{name: "X-Upstream", value: "configured"},
		{name: "X-Override", value: "configured"},
	} {
		if _, err := database.CreatePublicRouteTargetUpstreamHeader(context.Background(), db.CreatePublicRouteTargetUpstreamHeaderParams{
			TargetID: target.ID,
			Position: int64(index),
			Name:     header.name,
			Value:    header.value,
		}); err != nil {
			return db.PublicRouteTarget{}, err
		}
	}
	return target, nil
}

func routeTargetBoolIntForTest(value bool) int64 {
	if value {
		return 1
	}
	return 0
}

func setSpoofedForwardedHeaders(req *http.Request) {
	req.Header.Set("Forwarded", "for=203.0.113.66;proto=https;host=evil.example")
	req.Header.Set("X-Forwarded-For", "203.0.113.66")
	req.Header.Set("X-Forwarded-Host", "evil.example")
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Port", "443")
	req.Header.Set("X-Real-IP", "203.0.113.66")
}

func assertTrustedForwardedHeaders(t *testing.T, got http.Header, listenerAddr string) {
	t.Helper()

	_, wantPort, err := net.SplitHostPort(listenerAddr)
	if err != nil {
		t.Fatalf("listener address %q did not include a port: %v", listenerAddr, err)
	}
	if got.Get("X-Got-Forwarded") != "" {
		t.Fatalf("Forwarded header was not stripped: %+v", got)
	}
	if forwardedFor := got.Get("X-Got-Forwarded-For"); forwardedFor == "" || forwardedFor == "203.0.113.66" {
		t.Fatalf("X-Forwarded-For = %q, want trusted client IP", forwardedFor)
	}
	if realIP := got.Get("X-Got-Real-IP"); realIP == "" || realIP == "203.0.113.66" {
		t.Fatalf("X-Real-IP = %q, want trusted client IP", realIP)
	}
	if got.Get("X-Got-Forwarded-Host") != listenerAddr {
		t.Fatalf("X-Forwarded-Host = %q, want %q", got.Get("X-Got-Forwarded-Host"), listenerAddr)
	}
	if got.Get("X-Got-Forwarded-Proto") != "http" {
		t.Fatalf("X-Forwarded-Proto = %q, want http", got.Get("X-Got-Forwarded-Proto"))
	}
	if got.Get("X-Got-Forwarded-Port") != wantPort {
		t.Fatalf("X-Forwarded-Port = %q, want %q", got.Get("X-Got-Forwarded-Port"), wantPort)
	}
}

func assertUpstreamHeaderProto(t *testing.T, target *p2pstreamv1.PublicRouteTarget, name string, value string, sensitive bool, valueSet bool) *p2pstreamv1.PublicBackendUpstreamHeader {
	t.Helper()
	for _, header := range target.GetUpstreamRequestHeaders() {
		if header.GetName() == name {
			if header.GetValue() != value || header.GetSensitive() != sensitive || header.GetValueSet() != valueSet {
				t.Fatalf("header %s = value %q sensitive %v value_set %v, want %q/%v/%v", name, header.GetValue(), header.GetSensitive(), header.GetValueSet(), value, sensitive, valueSet)
			}
			return header
		}
	}
	t.Fatalf("header %s not found in %+v", name, target.GetUpstreamRequestHeaders())
	return nil
}
