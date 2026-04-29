package main_test

import (
	"context"
	"crypto/tls"
	"io"
	"net/http"
	"testing"
	"time"

	"connectrpc.com/connect"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/gen/proto/p2pstream/v1/p2pstreamv1connect"
	"p2pstream/internal/config"
	"p2pstream/internal/db"
	"p2pstream/internal/server"
)

func TestPublicProxyConfigSeedsDefaults(t *testing.T) {
	app := server.NewApp(&config.Config{
		Port:         "8080",
		TargetOrigin: "https://example.com",
	}, newTestDB(t))
	_, client := newTestManagementClient(t, app)
	cookie := createAdminSession(t, client)

	cfg := getPublicProxyConfig(t, client, cookie)
	if len(cfg.Backends) != 1 {
		t.Fatalf("expected one seeded backend, got %d", len(cfg.Backends))
	}
	if cfg.Backends[0].Name != "default" || cfg.Backends[0].TargetOrigin != "https://example.com" || !cfg.Backends[0].Enabled {
		t.Fatalf("unexpected seeded backend: %+v", cfg.Backends[0])
	}
	if cfg.Backends[0].GetBackendType() != p2pstreamv1.PublicBackendType_PUBLIC_BACKEND_TYPE_PROXY_FORWARD || cfg.Backends[0].GetTlsSkipVerify() {
		t.Fatalf("unexpected seeded backend type/options: %+v", cfg.Backends[0])
	}

	httpListener := publicListenerByName(t, cfg, "public-http")
	if httpListener.Port != 8080 || httpListener.Protocol != p2pstreamv1.PublicListenerProtocol_PUBLIC_LISTENER_PROTOCOL_HTTP || !httpListener.Enabled {
		t.Fatalf("unexpected seeded HTTP listener: %+v", httpListener)
	}

	httpsListener := publicListenerByName(t, cfg, "public-https")
	if httpsListener.Port != 443 || httpsListener.Protocol != p2pstreamv1.PublicListenerProtocol_PUBLIC_LISTENER_PROTOCOL_HTTPS || !httpsListener.Enabled {
		t.Fatalf("unexpected seeded HTTPS listener: %+v", httpsListener)
	}

	cfgAgain := getPublicProxyConfig(t, client, cookie)
	if len(cfgAgain.Listeners) != 2 || len(cfgAgain.Backends) != 1 {
		t.Fatalf("expected idempotent seed, got %d listeners and %d backends", len(cfgAgain.Listeners), len(cfgAgain.Backends))
	}
}

func TestStaticPublicBackendRespondsWithoutAgent(t *testing.T) {
	database := newTestDB(t)
	backend, err := database.CreatePublicBackend(context.Background(), db.CreatePublicBackendParams{
		Name:               "static-default",
		BackendType:        "static",
		StaticStatusCode:   http.StatusAccepted,
		StaticResponseBody: "static body",
		Enabled:            1,
	})
	if err != nil {
		t.Fatalf("seed static backend: %v", err)
	}
	if _, err := database.CreatePublicBackendHeader(context.Background(), db.CreatePublicBackendHeaderParams{
		BackendID: backend.ID,
		Position:  0,
		Name:      "Content-Type",
		Value:     "text/plain",
	}); err != nil {
		t.Fatalf("seed static content-type header: %v", err)
	}
	if _, err := database.CreatePublicBackendHeader(context.Background(), db.CreatePublicBackendHeaderParams{
		BackendID: backend.ID,
		Position:  1,
		Name:      "X-Static",
		Value:     "yes",
	}); err != nil {
		t.Fatalf("seed static custom header: %v", err)
	}
	listener, err := database.CreatePublicListener(context.Background(), db.CreatePublicListenerParams{
		Name:             "static-http",
		BindAddress:      "127.0.0.1",
		Port:             0,
		Protocol:         "http",
		Enabled:          1,
		DefaultBackendID: backend.ID,
	})
	if err != nil {
		t.Fatalf("seed static listener: %v", err)
	}

	app := server.NewApp(&config.Config{TargetOrigin: "https://example.com"}, database)
	status, err := app.StartProxyListener(context.Background())
	if err != nil {
		t.Fatalf("start proxy: %v", err)
	}
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = app.StopProxyListener(shutdownCtx)
	})
	boundAddress := publicListenerBoundAddress(t, status, listener.ID)

	resp, err := http.Get("http://" + boundAddress + "/static")
	if err != nil {
		t.Fatalf("static backend request: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read static response body: %v", err)
	}
	if resp.StatusCode != http.StatusAccepted || string(body) != "static body" {
		t.Fatalf("unexpected static response: status=%d body=%q", resp.StatusCode, string(body))
	}
	if resp.Header.Get("Content-Type") != "text/plain" || resp.Header.Get("X-Static") != "yes" {
		t.Fatalf("unexpected static headers: %+v", resp.Header)
	}

	headReq, err := http.NewRequest(http.MethodHead, "http://"+boundAddress+"/static", nil)
	if err != nil {
		t.Fatalf("new HEAD request: %v", err)
	}
	headResp, err := http.DefaultClient.Do(headReq)
	if err != nil {
		t.Fatalf("static HEAD request: %v", err)
	}
	defer headResp.Body.Close()
	headBody, err := io.ReadAll(headResp.Body)
	if err != nil {
		t.Fatalf("read static HEAD body: %v", err)
	}
	if headResp.StatusCode != http.StatusAccepted || len(headBody) != 0 {
		t.Fatalf("unexpected static HEAD response: status=%d body=%q", headResp.StatusCode, string(headBody))
	}
}

func TestPublicBackendStaticConfigValidationAndReadback(t *testing.T) {
	app := server.NewApp(&config.Config{TargetOrigin: "https://example.com"}, newTestDB(t))
	_, client := newTestManagementClient(t, app)
	cookie := createAdminSession(t, client)

	invalidStatusReq := connect.NewRequest(&p2pstreamv1.CreatePublicBackendRequest{
		Name:             "bad-status",
		BackendType:      p2pstreamv1.PublicBackendType_PUBLIC_BACKEND_TYPE_STATIC,
		StaticStatusCode: 99,
		Enabled:          true,
	})
	invalidStatusReq.Header().Set("Cookie", cookie)
	if _, err := client.CreatePublicBackend(context.Background(), invalidStatusReq); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("expected invalid status error, got %v", err)
	}

	invalidHeaderReq := connect.NewRequest(&p2pstreamv1.CreatePublicBackendRequest{
		Name:             "bad-header",
		BackendType:      p2pstreamv1.PublicBackendType_PUBLIC_BACKEND_TYPE_STATIC,
		StaticStatusCode: http.StatusOK,
		StaticResponseHeaders: []*p2pstreamv1.PublicHeader{
			{Name: "Content-Length", Value: "3"},
		},
		Enabled: true,
	})
	invalidHeaderReq.Header().Set("Cookie", cookie)
	if _, err := client.CreatePublicBackend(context.Background(), invalidHeaderReq); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("expected invalid header error, got %v", err)
	}

	createReq := connect.NewRequest(&p2pstreamv1.CreatePublicBackendRequest{
		Name:             "static-api",
		BackendType:      p2pstreamv1.PublicBackendType_PUBLIC_BACKEND_TYPE_STATIC,
		StaticStatusCode: http.StatusCreated,
		StaticResponseHeaders: []*p2pstreamv1.PublicHeader{
			{Name: "X-First", Value: "1"},
			{Name: "X-First", Value: "2"},
		},
		StaticResponseBody: "created",
		Enabled:            true,
	})
	createReq.Header().Set("Cookie", cookie)
	createResp, err := client.CreatePublicBackend(context.Background(), createReq)
	if err != nil {
		t.Fatalf("create static backend: %v", err)
	}
	created := createResp.Msg.GetBackend()
	if created.GetBackendType() != p2pstreamv1.PublicBackendType_PUBLIC_BACKEND_TYPE_STATIC ||
		created.GetTargetOrigin() != "" ||
		created.GetTlsSkipVerify() ||
		created.GetStaticStatusCode() != http.StatusCreated ||
		len(created.GetStaticResponseHeaders()) != 2 ||
		created.GetStaticResponseBody() != "created" {
		t.Fatalf("unexpected created static backend: %+v", created)
	}

	cfg := getPublicProxyConfig(t, client, cookie)
	readBack := publicBackendByName(t, cfg, "static-api")
	if readBack.GetBackendType() != p2pstreamv1.PublicBackendType_PUBLIC_BACKEND_TYPE_STATIC || len(readBack.GetStaticResponseHeaders()) != 2 {
		t.Fatalf("unexpected static backend readback: %+v", readBack)
	}
}

func TestPublicListenerDisablePersistsAndReenableRestarts(t *testing.T) {
	database := newTestDB(t)
	listener := seedTestHTTPPublicListener(t, database, "https://example.com")
	app := server.NewApp(&config.Config{TargetOrigin: "https://example.com"}, database)
	_, client := newTestManagementClient(t, app)
	cookie := createAdminSession(t, client)

	startReq := connect.NewRequest(&p2pstreamv1.StartProxyRequest{})
	startReq.Header().Set("Cookie", cookie)
	startResp, err := client.StartProxy(context.Background(), startReq)
	if err != nil {
		t.Fatalf("start proxy: %v", err)
	}
	if startResp.Msg.Proxy.GetState() != p2pstreamv1.ProxyState_PROXY_STATE_RUNNING {
		t.Fatalf("expected running proxy, got %s", startResp.Msg.Proxy.GetState())
	}
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = app.StopProxyListener(shutdownCtx)
	})

	disableReq := connect.NewRequest(&p2pstreamv1.DisablePublicListenerRequest{Id: listener.ID})
	disableReq.Header().Set("Cookie", cookie)
	disableResp, err := client.DisablePublicListener(context.Background(), disableReq)
	if err != nil {
		t.Fatalf("disable listener: %v", err)
	}
	if disableResp.Msg.Listener.GetEnabled() {
		t.Fatal("expected disabled listener config")
	}
	if !disableResp.Msg.Status.GetDisabled() || disableResp.Msg.Status.GetRunning() {
		t.Fatalf("expected disabled stopped status, got %+v", disableResp.Msg.Status)
	}

	cfg := getPublicProxyConfig(t, client, cookie)
	disabledListener := publicListenerByName(t, cfg, "test-http")
	if disabledListener.Enabled {
		t.Fatal("expected disabled listener to remain visible in config")
	}

	restartedApp := server.NewApp(&config.Config{TargetOrigin: "https://example.com"}, database)
	if status, err := restartedApp.StartProxyListener(context.Background()); err != nil {
		t.Fatalf("restart app proxy: %v", err)
	} else if status.GetState() != p2pstreamv1.ProxyState_PROXY_STATE_STOPPED {
		t.Fatalf("expected disabled listener to be skipped on restart, got %s", status.GetState())
	}

	enableReq := connect.NewRequest(&p2pstreamv1.EnablePublicListenerRequest{Id: listener.ID})
	enableReq.Header().Set("Cookie", cookie)
	enableResp, err := client.EnablePublicListener(context.Background(), enableReq)
	if err != nil {
		t.Fatalf("enable listener: %v", err)
	}
	if !enableResp.Msg.Listener.GetEnabled() || !enableResp.Msg.Status.GetRunning() {
		t.Fatalf("expected re-enabled listener to start while proxy service is active, got listener=%+v status=%+v", enableResp.Msg.Listener, enableResp.Msg.Status)
	}
}

func TestHTTPSPublicListenerUsesFallbackSelfSignedCertificate(t *testing.T) {
	database := newTestDB(t)
	backend, err := database.CreatePublicBackend(context.Background(), db.CreatePublicBackendParams{
		Name:         "https-default",
		TargetOrigin: "https://example.com",
		Enabled:      1,
	})
	if err != nil {
		t.Fatalf("seed backend: %v", err)
	}
	listener, err := database.CreatePublicListener(context.Background(), db.CreatePublicListenerParams{
		Name:             "test-https",
		BindAddress:      "127.0.0.1",
		Port:             0,
		Protocol:         "https",
		Enabled:          1,
		DefaultBackendID: backend.ID,
	})
	if err != nil {
		t.Fatalf("seed https listener: %v", err)
	}

	app := server.NewApp(&config.Config{TargetOrigin: "https://example.com"}, database)
	status, err := app.StartProxyListener(context.Background())
	if err != nil {
		t.Fatalf("start proxy: %v", err)
	}
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = app.StopProxyListener(shutdownCtx)
	})
	if status.GetState() != p2pstreamv1.ProxyState_PROXY_STATE_RUNNING {
		t.Fatalf("expected running proxy, got %s", status.GetState())
	}

	var boundAddress string
	for _, listenerStatus := range status.GetListeners() {
		if listenerStatus.GetListenerId() == listener.ID {
			boundAddress = listenerStatus.GetBoundAddress()
			break
		}
	}
	if boundAddress == "" {
		t.Fatal("expected HTTPS listener bound address")
	}

	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // Test fallback self-signed certificate.
		},
	}
	resp, err := client.Get("https://" + boundAddress + "/")
	if err != nil {
		t.Fatalf("https request through fallback cert: %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected TLS handshake then no-agent response 503, got %d", resp.StatusCode)
	}
}

func getPublicProxyConfig(
	t *testing.T,
	client p2pstreamv1connect.AgentManagementServiceClient,
	cookie string,
) *p2pstreamv1.GetPublicProxyConfigResponse {
	t.Helper()

	req := connect.NewRequest(&p2pstreamv1.GetPublicProxyConfigRequest{})
	req.Header().Set("Cookie", cookie)
	resp, err := client.GetPublicProxyConfig(context.Background(), req)
	if err != nil {
		t.Fatalf("get public proxy config: %v", err)
	}
	return resp.Msg
}

func publicBackendByName(t *testing.T, cfg *p2pstreamv1.GetPublicProxyConfigResponse, name string) *p2pstreamv1.PublicBackend {
	t.Helper()
	for _, backend := range cfg.Backends {
		if backend.GetName() == name {
			return backend
		}
	}
	t.Fatalf("backend %q not found in %+v", name, cfg.Backends)
	return nil
}

func publicListenerByName(t *testing.T, cfg *p2pstreamv1.GetPublicProxyConfigResponse, name string) *p2pstreamv1.PublicListener {
	t.Helper()
	for _, listener := range cfg.Listeners {
		if listener.GetName() == name {
			return listener
		}
	}
	t.Fatalf("listener %q not found in %+v", name, cfg.Listeners)
	return nil
}

func publicListenerBoundAddress(t *testing.T, status *p2pstreamv1.ProxyStatus, listenerID int64) string {
	t.Helper()
	for _, listenerStatus := range status.GetListeners() {
		if listenerStatus.GetListenerId() == listenerID {
			if listenerStatus.GetBoundAddress() == "" {
				t.Fatalf("listener %d has no bound address in %+v", listenerID, listenerStatus)
			}
			return listenerStatus.GetBoundAddress()
		}
	}
	t.Fatalf("listener %d not found in status %+v", listenerID, status)
	return ""
}
