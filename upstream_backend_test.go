package main_test

import (
	"bytes"
	"context"
	"database/sql"
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

func TestPublicBackendUpstreamConfigAPIValidationAndSecretSemantics(t *testing.T) {
	database := newTestDB(t)
	app := server.NewApp(testManagementConfig(config.Config{}), database)
	_, client := newTestManagementClient(t, app)
	cookie := createAdminSession(t, client)

	createReq := connect.NewRequest(&p2pstreamv1.CreatePublicBackendRequest{
		Name:         "upstream-api",
		TargetOrigin: "http://example.com",
		Enabled:      true,
		BackendType:  p2pstreamv1.PublicBackendType_PUBLIC_BACKEND_TYPE_PROXY_FORWARD,
		ForwardMode:  p2pstreamv1.PublicBackendForwardMode_PUBLIC_BACKEND_FORWARD_MODE_DIRECT,
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
	})
	createReq.Header().Set("Cookie", cookie)
	createResp, err := client.CreatePublicBackend(context.Background(), createReq)
	if err != nil {
		t.Fatalf("create backend with upstream config: %v", err)
	}
	created := createResp.Msg.GetBackend()
	if got := created.GetUpstreamBasicAuth(); !got.GetEnabled() || got.GetUsername() != "service" || got.GetPassword() != "" || !got.GetPasswordSet() {
		t.Fatalf("unexpected masked basic auth readback: %+v", got)
	}
	assertUpstreamHeaderProto(t, created, "X-Visible", "visible", false, true)
	cookieHeader := assertUpstreamHeaderProto(t, created, "Cookie", "", true, true)
	secretHeader := assertUpstreamHeaderProto(t, created, "X-Secret", "", true, true)
	if cookieHeader.GetId() == 0 || secretHeader.GetId() == 0 {
		t.Fatalf("expected persisted header ids, got cookie=%d secret=%d", cookieHeader.GetId(), secretHeader.GetId())
	}

	cfg := getPublicProxyConfig(t, client, cookie)
	readBack := publicBackendByName(t, cfg, "upstream-api")
	assertUpstreamHeaderProto(t, readBack, "X-Visible", "visible", false, true)
	assertUpstreamHeaderProto(t, readBack, "Cookie", "", true, true)
	assertUpstreamHeaderProto(t, readBack, "X-Secret", "", true, true)
	if readBack.GetUpstreamBasicAuth().GetPassword() != "" || !readBack.GetUpstreamBasicAuth().GetPasswordSet() {
		t.Fatalf("expected masked saved basic auth password in config, got %+v", readBack.GetUpstreamBasicAuth())
	}

	preserveReq := connect.NewRequest(&p2pstreamv1.UpdatePublicBackendRequest{
		Id:           created.GetId(),
		Name:         created.GetName(),
		TargetOrigin: created.GetTargetOrigin(),
		Enabled:      created.GetEnabled(),
		BackendType:  p2pstreamv1.PublicBackendType_PUBLIC_BACKEND_TYPE_PROXY_FORWARD,
		ForwardMode:  p2pstreamv1.PublicBackendForwardMode_PUBLIC_BACKEND_FORWARD_MODE_DIRECT,
		UpstreamRequestHeaders: []*p2pstreamv1.PublicBackendUpstreamHeader{
			{Id: secretHeader.GetId(), Name: "X-Secret", Sensitive: true, ValueSet: false},
		},
		UpstreamBasicAuth: &p2pstreamv1.PublicBackendBasicAuth{
			Enabled:     true,
			Username:    "service",
			PasswordSet: false,
		},
	})
	preserveReq.Header().Set("Cookie", cookie)
	preserveResp, err := client.UpdatePublicBackend(context.Background(), preserveReq)
	if err != nil {
		t.Fatalf("update backend preserving upstream secrets: %v", err)
	}
	preservedHeader := assertUpstreamHeaderProto(t, preserveResp.Msg.GetBackend(), "X-Secret", "", true, true)
	assertStoredUpstreamHeader(t, database, created.GetId(), "X-Secret", "hidden", true)
	storedBackend, err := database.GetPublicBackend(context.Background(), created.GetId())
	if err != nil {
		t.Fatalf("get stored backend after preserve: %v", err)
	}
	if storedBackend.UpstreamBasicAuthPassword != "old-password" {
		t.Fatalf("basic auth password after preserve = %q, want old-password", storedBackend.UpstreamBasicAuthPassword)
	}

	replaceReq := connect.NewRequest(&p2pstreamv1.UpdatePublicBackendRequest{
		Id:           created.GetId(),
		Name:         created.GetName(),
		TargetOrigin: created.GetTargetOrigin(),
		Enabled:      created.GetEnabled(),
		BackendType:  p2pstreamv1.PublicBackendType_PUBLIC_BACKEND_TYPE_PROXY_FORWARD,
		ForwardMode:  p2pstreamv1.PublicBackendForwardMode_PUBLIC_BACKEND_FORWARD_MODE_DIRECT,
		UpstreamRequestHeaders: []*p2pstreamv1.PublicBackendUpstreamHeader{
			{Id: preservedHeader.GetId(), Name: "X-Secret", Value: "new-hidden", Sensitive: true, ValueSet: true},
		},
		UpstreamBasicAuth: &p2pstreamv1.PublicBackendBasicAuth{
			Enabled:     true,
			Username:    "service",
			Password:    "new-password",
			PasswordSet: true,
		},
	})
	replaceReq.Header().Set("Cookie", cookie)
	if _, err := client.UpdatePublicBackend(context.Background(), replaceReq); err != nil {
		t.Fatalf("update backend replacing upstream secrets: %v", err)
	}
	assertStoredUpstreamHeader(t, database, created.GetId(), "X-Secret", "new-hidden", true)
	storedBackend, err = database.GetPublicBackend(context.Background(), created.GetId())
	if err != nil {
		t.Fatalf("get stored backend after replacement: %v", err)
	}
	if storedBackend.UpstreamBasicAuthPassword != "new-password" {
		t.Fatalf("basic auth password after replacement = %q, want new-password", storedBackend.UpstreamBasicAuthPassword)
	}
}

func TestPublicBackendUpstreamResponseHeaderTimeoutAPI(t *testing.T) {
	app := server.NewApp(testManagementConfig(config.Config{}), newTestDB(t))
	_, client := newTestManagementClient(t, app)
	cookie := createAdminSession(t, client)

	createReq := connect.NewRequest(&p2pstreamv1.CreatePublicBackendRequest{
		Name:         "timeout-api",
		TargetOrigin: "http://example.com",
		Enabled:      true,
		BackendType:  p2pstreamv1.PublicBackendType_PUBLIC_BACKEND_TYPE_PROXY_FORWARD,
		ForwardMode:  p2pstreamv1.PublicBackendForwardMode_PUBLIC_BACKEND_FORWARD_MODE_DIRECT,
	})
	createReq.Header().Set("Cookie", cookie)
	createResp, err := client.CreatePublicBackend(context.Background(), createReq)
	if err != nil {
		t.Fatalf("create backend with default timeout: %v", err)
	}
	if got := createResp.Msg.GetBackend().GetUpstreamResponseHeaderTimeoutMillis(); got != 60000 {
		t.Fatalf("default upstream response header timeout = %d, want 60000", got)
	}

	updateReq := connect.NewRequest(&p2pstreamv1.UpdatePublicBackendRequest{
		Id:                                  createResp.Msg.GetBackend().GetId(),
		Name:                                "timeout-api",
		TargetOrigin:                        "http://example.com",
		Enabled:                             true,
		BackendType:                         p2pstreamv1.PublicBackendType_PUBLIC_BACKEND_TYPE_PROXY_FORWARD,
		ForwardMode:                         p2pstreamv1.PublicBackendForwardMode_PUBLIC_BACKEND_FORWARD_MODE_DIRECT,
		UpstreamResponseHeaderTimeoutMillis: 45000,
	})
	updateReq.Header().Set("Cookie", cookie)
	updateResp, err := client.UpdatePublicBackend(context.Background(), updateReq)
	if err != nil {
		t.Fatalf("update backend timeout: %v", err)
	}
	if got := updateResp.Msg.GetBackend().GetUpstreamResponseHeaderTimeoutMillis(); got != 45000 {
		t.Fatalf("updated upstream response header timeout = %d, want 45000", got)
	}

	for _, timeoutMillis := range []int64{999, 3600001} {
		req := connect.NewRequest(&p2pstreamv1.CreatePublicBackendRequest{
			Name:                                "timeout-invalid",
			TargetOrigin:                        "http://example.com",
			Enabled:                             true,
			BackendType:                         p2pstreamv1.PublicBackendType_PUBLIC_BACKEND_TYPE_PROXY_FORWARD,
			ForwardMode:                         p2pstreamv1.PublicBackendForwardMode_PUBLIC_BACKEND_FORWARD_MODE_DIRECT,
			UpstreamResponseHeaderTimeoutMillis: timeoutMillis,
		})
		req.Header().Set("Cookie", cookie)
		if _, err := client.CreatePublicBackend(context.Background(), req); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("timeout %d: expected invalid argument, got %v", timeoutMillis, err)
		}
	}
}

func TestPublicBackendUpstreamConfigValidationRejectsInvalidInputs(t *testing.T) {
	app := server.NewApp(testManagementConfig(config.Config{}), newTestDB(t))
	_, client := newTestManagementClient(t, app)
	cookie := createAdminSession(t, client)

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
		{
			name:    "blocked-header",
			headers: []*p2pstreamv1.PublicBackendUpstreamHeader{{Name: "Host", Value: "example.com", ValueSet: true}},
		},
		{
			name:    "authorization-with-basic-auth",
			headers: []*p2pstreamv1.PublicBackendUpstreamHeader{{Name: "Authorization", Value: "Bearer token", ValueSet: true}},
			auth:    &p2pstreamv1.PublicBackendBasicAuth{Enabled: true, Username: "service", Password: "secret", PasswordSet: true},
		},
		{
			name: "missing-basic-auth-username",
			auth: &p2pstreamv1.PublicBackendBasicAuth{Enabled: true, Password: "secret", PasswordSet: true},
		},
		{
			name: "missing-basic-auth-password",
			auth: &p2pstreamv1.PublicBackendBasicAuth{Enabled: true, Username: "service"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := connect.NewRequest(&p2pstreamv1.CreatePublicBackendRequest{
				Name:                   tc.name,
				TargetOrigin:           "http://example.com",
				Enabled:                true,
				BackendType:            p2pstreamv1.PublicBackendType_PUBLIC_BACKEND_TYPE_PROXY_FORWARD,
				ForwardMode:            p2pstreamv1.PublicBackendForwardMode_PUBLIC_BACKEND_FORWARD_MODE_DIRECT,
				UpstreamRequestHeaders: tc.headers,
				UpstreamBasicAuth:      tc.auth,
			})
			req.Header().Set("Cookie", cookie)
			if _, err := client.CreatePublicBackend(context.Background(), req); connect.CodeOf(err) != connect.CodeInvalidArgument {
				t.Fatalf("expected invalid argument, got %v", err)
			}
		})
	}
}

func TestStaticPublicBackendClearsUpstreamConfig(t *testing.T) {
	app := server.NewApp(testManagementConfig(config.Config{}), newTestDB(t))
	_, client := newTestManagementClient(t, app)
	cookie := createAdminSession(t, client)

	req := connect.NewRequest(&p2pstreamv1.CreatePublicBackendRequest{
		Name:             "static-clears-upstream",
		Enabled:          true,
		BackendType:      p2pstreamv1.PublicBackendType_PUBLIC_BACKEND_TYPE_STATIC,
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
	})
	req.Header().Set("Cookie", cookie)
	resp, err := client.CreatePublicBackend(context.Background(), req)
	if err != nil {
		t.Fatalf("create static backend with ignored upstream config: %v", err)
	}
	backend := resp.Msg.GetBackend()
	if len(backend.GetUpstreamRequestHeaders()) != 0 || backend.GetUpstreamBasicAuth().GetEnabled() || backend.GetUpstreamBasicAuth().GetPasswordSet() {
		t.Fatalf("static backend should clear upstream config, got headers=%+v auth=%+v", backend.GetUpstreamRequestHeaders(), backend.GetUpstreamBasicAuth())
	}
}

func TestDirectPublicBackendAppliesUpstreamRequestConfig(t *testing.T) {
	targetSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		_, _ = w.Write([]byte("direct upstream ok"))
	}))
	defer targetSrv.Close()

	database := newTestDB(t)
	backend := createDirectBackendWithUpstreamConfig(t, database, "direct-upstream", targetSrv.URL)
	listener, err := database.CreatePublicListener(context.Background(), db.CreatePublicListenerParams{
		Name:             "direct-upstream-listener",
		BindAddress:      "127.0.0.1",
		Port:             0,
		Protocol:         "http",
		Enabled:          1,
		DefaultBackendID: backend.ID,
	})
	if err != nil {
		t.Fatalf("create listener: %v", err)
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
		_, _ = w.Write([]byte("default backend"))
	}))
	defer defaultSrv.Close()
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("api backend"))
	}))
	defer apiSrv.Close()

	database := newTestDB(t)
	listener := seedTestHTTPPublicListener(t, database, defaultSrv.URL)
	apiBackend, err := database.CreatePublicBackend(context.Background(), db.CreatePublicBackendParams{
		Name:         "api-backend",
		TargetOrigin: apiSrv.URL,
		BackendType:  "proxy_forward",
		ForwardMode:  "direct",
		Enabled:      1,
	})
	if err != nil {
		t.Fatalf("create api backend: %v", err)
	}
	if _, err := database.CreatePublicRoute(context.Background(), db.CreatePublicRouteParams{
		ListenerID: listener.ID,
		Priority:   1,
		PathPrefix: "/api",
		BackendID:  sql.NullInt64{Int64: apiBackend.ID, Valid: true},
		Enabled:    1,
	}); err != nil {
		t.Fatalf("create api route: %v", err)
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
	if body := mustReadResponseBody(t, apiResp); body != "api backend" {
		t.Fatalf("/api response body = %q, want api backend", body)
	}

	defaultResp, err := http.Get(baseURL + "/apiv2/users")
	if err != nil {
		t.Fatalf("apiv2 request: %v", err)
	}
	defer defaultResp.Body.Close()
	if body := mustReadResponseBody(t, defaultResp); body != "default backend" {
		t.Fatalf("/apiv2 response body = %q, want default backend", body)
	}
}

func TestAgentPoolPublicBackendAppliesUpstreamRequestConfig(t *testing.T) {
	targetSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, password, ok := r.BasicAuth()
		w.Header().Set("X-Got-Upstream", r.Header.Get("X-Upstream"))
		w.Header().Set("X-Got-Override", r.Header.Get("X-Override"))
		w.Header().Set("X-Got-Forwarded", r.Header.Get("Forwarded"))
		w.Header().Set("X-Got-Forwarded-For", r.Header.Get("X-Forwarded-For"))
		w.Header().Set("X-Got-Forwarded-Host", r.Header.Get("X-Forwarded-Host"))
		w.Header().Set("X-Got-Forwarded-Proto", r.Header.Get("X-Forwarded-Proto"))
		w.Header().Set("X-Got-Forwarded-Port", r.Header.Get("X-Forwarded-Port"))
		w.Header().Set("X-Got-Real-IP", r.Header.Get("X-Real-IP"))
		if ok && user == "service" && password == "secret" {
			w.Header().Set("X-Got-Basic-Auth", "true")
		}
		_, _ = w.Write([]byte("agent upstream ok"))
	}))
	defer targetSrv.Close()

	database := newTestDB(t)
	app := server.NewApp(testManagementConfig(config.Config{}), database)
	mgmtSrv, client := newTestManagementClient(t, app)
	cookie := createAdminSession(t, client)
	agent, token := createRegisteredAgent(t, client, cookie, "upstream-agent", "Upstream Agent")

	backend := createAgentPoolBackend(t, database, "agent-upstream", targetSrv.URL, agent.GetId(), 100)
	if _, err := database.UpdatePublicBackend(context.Background(), db.UpdatePublicBackendParams{
		ID:                        backend.ID,
		Name:                      backend.Name,
		TargetOrigin:              backend.TargetOrigin,
		BackendType:               backend.BackendType,
		ForwardMode:               backend.ForwardMode,
		LoadBalancing:             backend.LoadBalancing,
		TlsSkipVerify:             backend.TlsSkipVerify,
		StaticStatusCode:          backend.StaticStatusCode,
		StaticResponseBody:        backend.StaticResponseBody,
		UpstreamBasicAuthEnabled:  1,
		UpstreamBasicAuthUsername: "service",
		UpstreamBasicAuthPassword: "secret",
		Enabled:                   backend.Enabled,
	}); err != nil {
		t.Fatalf("update backend upstream basic auth: %v", err)
	}
	if _, err := database.CreatePublicBackendUpstreamHeader(context.Background(), db.CreatePublicBackendUpstreamHeaderParams{
		BackendID: backend.ID,
		Position:  0,
		Name:      "X-Upstream",
		Value:     "configured",
	}); err != nil {
		t.Fatalf("create upstream header: %v", err)
	}
	if _, err := database.CreatePublicBackendUpstreamHeader(context.Background(), db.CreatePublicBackendUpstreamHeaderParams{
		BackendID: backend.ID,
		Position:  1,
		Name:      "X-Override",
		Value:     "configured",
	}); err != nil {
		t.Fatalf("create override header: %v", err)
	}
	listener, err := database.CreatePublicListener(context.Background(), db.CreatePublicListenerParams{
		Name:             "agent-upstream-listener",
		BindAddress:      "127.0.0.1",
		Port:             0,
		Protocol:         "http",
		Enabled:          1,
		DefaultBackendID: backend.ID,
	})
	if err != nil {
		t.Fatalf("create listener: %v", err)
	}

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
		_ = runAgent(ctx, "ws"+mgmtSrv.URL[4:]+"/ws", agent.GetPublicId(), token)
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
		resp.Header.Get("X-Got-Basic-Auth") != "true" ||
		resp.Header.Get("X-Mock-Agent") != agent.GetPublicId() {
		t.Fatalf("agent upstream request config was not applied, response headers=%+v", resp.Header)
	}
	assertTrustedForwardedHeaders(t, resp.Header, listenerAddr)

	cancel()
	<-agentDone
}

func createDirectBackendWithUpstreamConfig(t *testing.T, database *db.DB, name string, targetOrigin string) db.PublicBackend {
	t.Helper()
	backend, err := database.CreatePublicBackend(context.Background(), db.CreatePublicBackendParams{
		Name:                      name,
		TargetOrigin:              targetOrigin,
		BackendType:               "proxy_forward",
		ForwardMode:               "direct",
		LoadBalancing:             "round_robin",
		UpstreamBasicAuthEnabled:  1,
		UpstreamBasicAuthUsername: "service",
		UpstreamBasicAuthPassword: "secret",
		Enabled:                   1,
	})
	if err != nil {
		t.Fatalf("create backend %s: %v", name, err)
	}
	for index, header := range []struct {
		name  string
		value string
	}{
		{name: "X-Upstream", value: "configured"},
		{name: "X-Override", value: "configured"},
	} {
		if _, err := database.CreatePublicBackendUpstreamHeader(context.Background(), db.CreatePublicBackendUpstreamHeaderParams{
			BackendID: backend.ID,
			Position:  int64(index),
			Name:      header.name,
			Value:     header.value,
		}); err != nil {
			t.Fatalf("create upstream header %s: %v", header.name, err)
		}
	}
	return backend
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

func assertUpstreamHeaderProto(t *testing.T, backend *p2pstreamv1.PublicBackend, name string, wantValue string, wantSensitive bool, wantValueSet bool) *p2pstreamv1.PublicBackendUpstreamHeader {
	t.Helper()
	for _, header := range backend.GetUpstreamRequestHeaders() {
		if header.GetName() != name {
			continue
		}
		if header.GetValue() != wantValue || header.GetSensitive() != wantSensitive || header.GetValueSet() != wantValueSet {
			t.Fatalf("header %s = value %q sensitive %v value_set %v, want value %q sensitive %v value_set %v", name, header.GetValue(), header.GetSensitive(), header.GetValueSet(), wantValue, wantSensitive, wantValueSet)
		}
		return header
	}
	t.Fatalf("header %s not found in %+v", name, backend.GetUpstreamRequestHeaders())
	return nil
}

func assertStoredUpstreamHeader(t *testing.T, database *db.DB, backendID int64, name string, wantValue string, wantSensitive bool) {
	t.Helper()
	headers, err := database.ListPublicBackendUpstreamHeadersByBackend(context.Background(), backendID)
	if err != nil {
		t.Fatalf("list stored upstream headers: %v", err)
	}
	for _, header := range headers {
		if header.Name != name {
			continue
		}
		if header.Value != wantValue || (header.Sensitive != 0) != wantSensitive {
			t.Fatalf("stored header %s = value %q sensitive %d, want value %q sensitive %v", name, header.Value, header.Sensitive, wantValue, wantSensitive)
		}
		return
	}
	t.Fatalf("stored header %s not found in %+v", name, headers)
}
