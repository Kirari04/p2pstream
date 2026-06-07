package main_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strings"
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
	configDir := t.TempDir()
	database := newTestDB(t)
	app := server.NewApp(&config.Config{
		ConfigDir:            configDir,
		CertsDir:             filepath.Join(configDir, "certs"),
		ManagementSetupToken: testSetupToken,
	}, database)
	_, client := newTestManagementClient(t, app)
	cookie := createAdminSession(t, client)

	cfg := getPublicProxyConfig(t, client, cookie)
	if len(cfg.GetRouteTargets()) != 2 {
		t.Fatalf("expected two seeded route targets, got %d", len(cfg.GetRouteTargets()))
	}
	for _, target := range cfg.GetRouteTargets() {
		if target.GetName() != "default" || !target.GetEnabled() {
			t.Fatalf("unexpected seeded target: %+v", target)
		}
		if target.GetTargetType() != p2pstreamv1.PublicRouteTargetType_PUBLIC_ROUTE_TARGET_TYPE_STATIC ||
			target.GetStaticStatusCode() != http.StatusOK ||
			!strings.Contains(target.GetStaticResponseBody(), "Welcome to p2pstream proxy") {
			t.Fatalf("unexpected seeded target type/options: %+v", target)
		}
		staticHeaders := map[string]string{}
		for _, header := range target.GetStaticResponseHeaders() {
			staticHeaders[header.GetName()] = header.GetValue()
		}
		if staticHeaders["Content-Type"] != "text/html; charset=utf-8" ||
			staticHeaders["X-Content-Type-Options"] != "nosniff" ||
			staticHeaders["Cache-Control"] != "no-store" {
			t.Fatalf("unexpected seeded static headers: %+v", target.GetStaticResponseHeaders())
		}
	}

	httpListener := publicListenerByName(t, cfg, "public-http")
	if httpListener.Port != 80 ||
		httpListener.Protocol != p2pstreamv1.PublicListenerProtocol_PUBLIC_LISTENER_PROTOCOL_HTTP ||
		!httpListener.Enabled {
		t.Fatalf("unexpected seeded HTTP listener: %+v", httpListener)
	}

	httpsListener := publicListenerByName(t, cfg, "public-https")
	if httpsListener.Port != 443 ||
		httpsListener.Protocol != p2pstreamv1.PublicListenerProtocol_PUBLIC_LISTENER_PROTOCOL_HTTPS ||
		!httpsListener.Enabled {
		t.Fatalf("unexpected seeded HTTPS listener: %+v", httpsListener)
	}
	if len(cfg.Routes) != 2 {
		t.Fatalf("expected two seeded routes, got %d", len(cfg.Routes))
	}
	assertSeededWelcomeRoute(t, cfg, httpListener.GetId())
	assertSeededWelcomeRoute(t, cfg, httpsListener.GetId())

	if len(cfg.TlsCertificates) != 1 {
		t.Fatalf("expected one seeded TLS certificate, got %d", len(cfg.TlsCertificates))
	}
	seededCert := cfg.TlsCertificates[0]
	if seededCert.GetListenerId() != httpsListener.GetId() || seededCert.GetHostnamePattern() != "p2pstream.local" || !seededCert.GetEnabled() {
		t.Fatalf("unexpected seeded TLS certificate: %+v", seededCert)
	}
	wantCertPath := filepath.Join(configDir, "certs", fmt.Sprintf("public-listener-%d", httpsListener.GetId()), fmt.Sprintf("tls-%d.crt.pem", seededCert.GetId()))
	wantKeyPath := filepath.Join(configDir, "certs", fmt.Sprintf("public-listener-%d", httpsListener.GetId()), fmt.Sprintf("tls-%d.key.pem", seededCert.GetId()))
	if seededCert.GetCertPath() != wantCertPath || seededCert.GetKeyPath() != wantKeyPath {
		t.Fatalf("unexpected seeded TLS paths: cert=%q key=%q", seededCert.GetCertPath(), seededCert.GetKeyPath())
	}
	assertFileMode(t, seededCert.GetCertPath(), 0600)
	assertFileMode(t, seededCert.GetKeyPath(), 0600)
	if seededCert.GetIssuedAtUnixMillis() == 0 || seededCert.GetExpiresAtUnixMillis() == 0 || seededCert.GetExpiresAtUnixMillis() <= seededCert.GetIssuedAtUnixMillis() {
		t.Fatalf("expected seeded TLS certificate validity, got issued=%d expires=%d", seededCert.GetIssuedAtUnixMillis(), seededCert.GetExpiresAtUnixMillis())
	}

	cfgAgain := getPublicProxyConfig(t, client, cookie)
	if len(cfgAgain.Listeners) != 2 || len(cfgAgain.GetRouteTargets()) != 2 || len(cfgAgain.Routes) != 2 || len(cfgAgain.TlsCertificates) != 1 {
		t.Fatalf("expected idempotent seed, got %d listeners, %d targets, %d routes, and %d TLS certs", len(cfgAgain.Listeners), len(cfgAgain.GetRouteTargets()), len(cfgAgain.Routes), len(cfgAgain.TlsCertificates))
	}

	if _, err := database.UpdatePublicListener(context.Background(), db.UpdatePublicListenerParams{
		ID:          httpListener.GetId(),
		Name:        httpListener.GetName(),
		BindAddress: "127.0.0.1",
		Port:        0,
		Protocol:    "http",
		Enabled:     1,
	}); err != nil {
		t.Fatalf("move seeded HTTP listener to test port: %v", err)
	}
	if _, err := database.SetPublicListenerEnabled(context.Background(), db.SetPublicListenerEnabledParams{
		ID:      httpsListener.GetId(),
		Enabled: 0,
	}); err != nil {
		t.Fatalf("disable seeded HTTPS listener: %v", err)
	}
	status, err := app.StartProxyListener(context.Background())
	if err != nil {
		t.Fatalf("start seeded proxy: %v", err)
	}
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = app.StopProxyListener(shutdownCtx)
	})
	resp, err := http.Get("http://" + publicListenerBoundAddress(t, status, httpListener.GetId()) + "/")
	if err != nil {
		t.Fatalf("seeded welcome request: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read seeded welcome response: %v", err)
	}
	if resp.StatusCode != http.StatusOK ||
		resp.Header.Get("Content-Type") != "text/html; charset=utf-8" ||
		!strings.Contains(string(body), "Welcome to p2pstream proxy") {
		t.Fatalf("unexpected seeded welcome response: status=%d content-type=%q body=%q", resp.StatusCode, resp.Header.Get("Content-Type"), string(body))
	}
}

func TestStaticPublicRouteTargetRespondsWithoutAgent(t *testing.T) {
	database := newTestDB(t)
	listener, err := database.CreatePublicListener(context.Background(), db.CreatePublicListenerParams{
		Name:        "static-http",
		BindAddress: "127.0.0.1",
		Port:        0,
		Protocol:    "http",
		Enabled:     1,
	})
	if err != nil {
		t.Fatalf("seed static listener: %v", err)
	}
	route, err := database.CreatePublicRoute(context.Background(), db.CreatePublicRouteParams{
		ListenerID:                 listener.ID,
		Priority:                   1000,
		HostPattern:                "",
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
		t.Fatalf("seed static route: %v", err)
	}
	target, err := database.CreatePublicRouteTarget(context.Background(), db.CreatePublicRouteTargetParams{
		RouteID:                             route.ID,
		Name:                                "static-default",
		Position:                            0,
		PriorityGroup:                       0,
		Weight:                              100,
		Enabled:                             1,
		TargetType:                          "static",
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
		StaticStatusCode:                    http.StatusAccepted,
		StaticResponseBody:                  "static body",
		StaticResponseBodyMode:              "inline",
	})
	if err != nil {
		t.Fatalf("seed static target: %v", err)
	}
	for idx, header := range []struct {
		name  string
		value string
	}{
		{"Content-Type", "text/plain"},
		{"X-Static", "yes"},
	} {
		if _, err := database.CreatePublicRouteTargetResponseHeader(context.Background(), db.CreatePublicRouteTargetResponseHeaderParams{
			TargetID: target.ID,
			Position: int64(idx),
			Name:     header.name,
			Value:    header.value,
		}); err != nil {
			t.Fatalf("seed static target header: %v", err)
		}
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
	boundAddress := publicListenerBoundAddress(t, status, listener.ID)

	resp, err := http.Get("http://" + boundAddress + "/static")
	if err != nil {
		t.Fatalf("static target request: %v", err)
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

func TestPublicRouteTargetStaticConfigValidationAndReadback(t *testing.T) {
	app := server.NewApp(testManagementConfig(config.Config{}), newTestDB(t))
	_, client := newTestManagementClient(t, app)
	cookie := createAdminSession(t, client)
	cfg := getPublicProxyConfig(t, client, cookie)
	listener := publicListenerByName(t, cfg, "public-http")

	invalidStatusReq := connect.NewRequest(&p2pstreamv1.CreatePublicRouteRequest{
		ListenerId: listener.GetId(),
		Priority:   20,
		PathPrefix: "/bad-status",
		Enabled:    true,
		Targets: []*p2pstreamv1.PublicRouteTarget{{
			Name:             "bad-status",
			TargetType:       p2pstreamv1.PublicRouteTargetType_PUBLIC_ROUTE_TARGET_TYPE_STATIC,
			StaticStatusCode: 99,
			Enabled:          true,
		}},
	})
	invalidStatusReq.Header().Set("Cookie", cookie)
	if _, err := client.CreatePublicRoute(context.Background(), invalidStatusReq); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("expected invalid status error, got %v", err)
	}

	invalidHeaderReq := connect.NewRequest(&p2pstreamv1.CreatePublicRouteRequest{
		ListenerId: listener.GetId(),
		Priority:   30,
		PathPrefix: "/bad-header",
		Enabled:    true,
		Targets: []*p2pstreamv1.PublicRouteTarget{{
			Name:             "bad-header",
			TargetType:       p2pstreamv1.PublicRouteTargetType_PUBLIC_ROUTE_TARGET_TYPE_STATIC,
			StaticStatusCode: http.StatusOK,
			StaticResponseHeaders: []*p2pstreamv1.PublicHeader{
				{Name: "Content-Length", Value: "3"},
			},
			Enabled: true,
		}},
	})
	invalidHeaderReq.Header().Set("Cookie", cookie)
	if _, err := client.CreatePublicRoute(context.Background(), invalidHeaderReq); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("expected invalid header error, got %v", err)
	}

	createReq := connect.NewRequest(&p2pstreamv1.CreatePublicRouteRequest{
		ListenerId: listener.GetId(),
		Priority:   40,
		PathPrefix: "/static-api",
		Enabled:    true,
		Targets: []*p2pstreamv1.PublicRouteTarget{{
			Name:             "static-api",
			TargetType:       p2pstreamv1.PublicRouteTargetType_PUBLIC_ROUTE_TARGET_TYPE_STATIC,
			StaticStatusCode: http.StatusCreated,
			StaticResponseHeaders: []*p2pstreamv1.PublicHeader{
				{Name: "X-First", Value: "1"},
				{Name: "X-First", Value: "2"},
			},
			StaticResponseBody: "created",
			Enabled:            true,
		}},
	})
	createReq.Header().Set("Cookie", cookie)
	createResp, err := client.CreatePublicRoute(context.Background(), createReq)
	if err != nil {
		t.Fatalf("create static route target: %v", err)
	}
	created := createResp.Msg.GetRoute().GetTargets()[0]
	if created.GetTargetType() != p2pstreamv1.PublicRouteTargetType_PUBLIC_ROUTE_TARGET_TYPE_STATIC ||
		created.GetUrl() != "" ||
		created.GetStaticStatusCode() != http.StatusCreated ||
		len(created.GetStaticResponseHeaders()) != 2 ||
		created.GetStaticResponseBody() != "created" {
		t.Fatalf("unexpected created static target: %+v", created)
	}

	cfg = getPublicProxyConfig(t, client, cookie)
	readBack := publicRouteTargetByName(t, cfg, "static-api")
	if readBack.GetTargetType() != p2pstreamv1.PublicRouteTargetType_PUBLIC_ROUTE_TARGET_TYPE_STATIC || len(readBack.GetStaticResponseHeaders()) != 2 {
		t.Fatalf("unexpected static target readback: %+v", readBack)
	}
}

func TestPublicListenerDisablePersistsAndReenableRestarts(t *testing.T) {
	database := newTestDB(t)
	listener := seedTestHTTPPublicListener(t, database, "https://example.com")
	app := server.NewApp(testManagementConfig(config.Config{}), database)
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

	restartedApp := server.NewApp(testManagementConfig(config.Config{}), database)
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
	listener, err := database.CreatePublicListener(context.Background(), db.CreatePublicListenerParams{
		Name:        "test-https",
		BindAddress: "127.0.0.1",
		Port:        0,
		Protocol:    "https",
		Enabled:     1,
	})
	if err != nil {
		t.Fatalf("seed https listener: %v", err)
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
		t.Fatalf("seed https route: %v", err)
	}
	if _, err := database.CreatePublicRouteTarget(context.Background(), db.CreatePublicRouteTargetParams{
		RouteID:                             route.ID,
		Name:                                "https-default",
		Position:                            0,
		PriorityGroup:                       0,
		Weight:                              100,
		Enabled:                             1,
		TargetType:                          "static",
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
		StaticStatusCode:                    http.StatusNoContent,
		StaticResponseBodyMode:              "inline",
	}); err != nil {
		t.Fatalf("seed https target: %v", err)
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
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected TLS handshake then static response 204, got %d", resp.StatusCode)
	}
}

func TestPublicTLSCertificateUploadStoresManagedFiles(t *testing.T) {
	database := newTestDB(t)
	listener, err := database.CreatePublicListener(context.Background(), db.CreatePublicListenerParams{
		Name:        "upload-https",
		BindAddress: "127.0.0.1",
		Port:        0,
		Protocol:    "https",
		Enabled:     1,
	})
	if err != nil {
		t.Fatalf("seed https listener: %v", err)
	}

	configDir := t.TempDir()
	app := server.NewApp(&config.Config{
		ConfigDir:            configDir,
		CertsDir:             filepath.Join(configDir, "certs"),
		ManagementSetupToken: testSetupToken,
	}, database)
	_, client := newTestManagementClient(t, app)
	cookie := createAdminSession(t, client)
	certPEM, keyPEM := testTLSKeyPair(t, "upload.example.com")

	req := connect.NewRequest(&p2pstreamv1.CreatePublicTlsCertificateRequest{
		ListenerId:      listener.ID,
		HostnamePattern: "Upload.Example.COM",
		Enabled:         true,
		CertPem:         certPEM,
		KeyPem:          keyPEM,
	})
	req.Header().Set("Cookie", cookie)
	resp, err := client.CreatePublicTlsCertificate(context.Background(), req)
	if err != nil {
		t.Fatalf("create uploaded TLS certificate: %v", err)
	}
	cert := resp.Msg.GetTlsCertificate()
	if cert.GetHostnamePattern() != "upload.example.com" {
		t.Fatalf("hostname pattern = %q, want normalized upload.example.com", cert.GetHostnamePattern())
	}
	if cert.GetIssuedAtUnixMillis() == 0 || cert.GetExpiresAtUnixMillis() == 0 || cert.GetExpiresAtUnixMillis() <= cert.GetIssuedAtUnixMillis() {
		t.Fatalf("expected uploaded TLS validity, got issued=%d expires=%d", cert.GetIssuedAtUnixMillis(), cert.GetExpiresAtUnixMillis())
	}

	wantCertPath := filepath.Join(configDir, "certs", fmt.Sprintf("public-listener-%d", listener.ID), fmt.Sprintf("tls-%d.crt.pem", cert.GetId()))
	wantKeyPath := filepath.Join(configDir, "certs", fmt.Sprintf("public-listener-%d", listener.ID), fmt.Sprintf("tls-%d.key.pem", cert.GetId()))
	if cert.GetCertPath() != wantCertPath {
		t.Fatalf("cert path = %q, want %q", cert.GetCertPath(), wantCertPath)
	}
	if cert.GetKeyPath() != wantKeyPath {
		t.Fatalf("key path = %q, want %q", cert.GetKeyPath(), wantKeyPath)
	}
	assertFileBytes(t, wantCertPath, certPEM)
	assertFileBytes(t, wantKeyPath, keyPEM)
	assertFileMode(t, wantCertPath, 0600)
	assertFileMode(t, wantKeyPath, 0600)

	updateReq := connect.NewRequest(&p2pstreamv1.UpdatePublicTlsCertificateRequest{
		Id:              cert.GetId(),
		ListenerId:      listener.ID,
		HostnamePattern: "renamed.example.com",
		Enabled:         false,
	})
	updateReq.Header().Set("Cookie", cookie)
	updateResp, err := client.UpdatePublicTlsCertificate(context.Background(), updateReq)
	if err != nil {
		t.Fatalf("update uploaded TLS certificate without re-upload: %v", err)
	}
	updated := updateResp.Msg.GetTlsCertificate()
	if updated.GetCertPath() != wantCertPath || updated.GetKeyPath() != wantKeyPath {
		t.Fatalf("update should preserve managed paths, got cert=%q key=%q", updated.GetCertPath(), updated.GetKeyPath())
	}
	if updated.GetIssuedAtUnixMillis() != cert.GetIssuedAtUnixMillis() || updated.GetExpiresAtUnixMillis() != cert.GetExpiresAtUnixMillis() {
		t.Fatalf("update should preserve validity, got issued=%d expires=%d", updated.GetIssuedAtUnixMillis(), updated.GetExpiresAtUnixMillis())
	}
}

func TestPublicTLSCertificateGeneratedSelfSignedMaterial(t *testing.T) {
	database := newTestDB(t)
	listener, err := database.CreatePublicListener(context.Background(), db.CreatePublicListenerParams{
		Name:        "generated-https",
		BindAddress: "127.0.0.1",
		Port:        0,
		Protocol:    "https",
		Enabled:     1,
	})
	if err != nil {
		t.Fatalf("seed https listener: %v", err)
	}

	configDir := t.TempDir()
	app := server.NewApp(&config.Config{
		ConfigDir:            configDir,
		CertsDir:             filepath.Join(configDir, "certs"),
		ManagementSetupToken: testSetupToken,
	}, database)
	_, client := newTestManagementClient(t, app)
	cookie := createAdminSession(t, client)

	req := connect.NewRequest(&p2pstreamv1.CreatePublicTlsCertificateRequest{
		ListenerId:             listener.ID,
		HostnamePattern:        "Generated.Example.COM",
		Enabled:                true,
		GenerateSelfSigned:     true,
		SelfSignedValidityDays: 30,
	})
	req.Header().Set("Cookie", cookie)
	resp, err := client.CreatePublicTlsCertificate(context.Background(), req)
	if err != nil {
		t.Fatalf("create generated self-signed TLS certificate: %v", err)
	}
	cert := resp.Msg.GetTlsCertificate()
	if cert.GetHostnamePattern() != "generated.example.com" {
		t.Fatalf("hostname pattern = %q, want normalized generated.example.com", cert.GetHostnamePattern())
	}
	if cert.GetIssuedAtUnixMillis() == 0 || cert.GetExpiresAtUnixMillis() == 0 || cert.GetExpiresAtUnixMillis() <= cert.GetIssuedAtUnixMillis() {
		t.Fatalf("expected generated TLS validity, got issued=%d expires=%d", cert.GetIssuedAtUnixMillis(), cert.GetExpiresAtUnixMillis())
	}
	leaf := parseTestCertificateFile(t, cert.GetCertPath())
	if err := leaf.VerifyHostname("generated.example.com"); err != nil {
		t.Fatalf("generated certificate hostname verification failed: %v", err)
	}
	if leaf.NotAfter.Before(time.Now().Add(29*24*time.Hour)) || leaf.NotAfter.After(time.Now().Add(31*24*time.Hour)) {
		t.Fatalf("generated certificate validity = %s, want around 30 days", leaf.NotAfter)
	}

	updateReq := connect.NewRequest(&p2pstreamv1.UpdatePublicTlsCertificateRequest{
		Id:                     cert.GetId(),
		ListenerId:             listener.ID,
		HostnamePattern:        "Renewed.Example.COM",
		Enabled:                true,
		GenerateSelfSigned:     true,
		SelfSignedValidityDays: 14,
	})
	updateReq.Header().Set("Cookie", cookie)
	updateResp, err := client.UpdatePublicTlsCertificate(context.Background(), updateReq)
	if err != nil {
		t.Fatalf("update generated self-signed TLS certificate: %v", err)
	}
	updated := updateResp.Msg.GetTlsCertificate()
	if updated.GetCertPath() != cert.GetCertPath() || updated.GetKeyPath() != cert.GetKeyPath() {
		t.Fatalf("generated update should preserve managed paths, got cert=%q key=%q", updated.GetCertPath(), updated.GetKeyPath())
	}
	updatedLeaf := parseTestCertificateFile(t, updated.GetCertPath())
	if err := updatedLeaf.VerifyHostname("renewed.example.com"); err != nil {
		t.Fatalf("updated generated certificate hostname verification failed: %v", err)
	}
}

func TestPublicTLSDNSCredentialHidesAndPreservesToken(t *testing.T) {
	app := server.NewApp(testManagementConfig(config.Config{}), newTestDB(t))
	_, client := newTestManagementClient(t, app)
	cookie := createAdminSession(t, client)

	createReq := connect.NewRequest(&p2pstreamv1.CreatePublicTlsDnsCredentialRequest{
		Name:             "cf",
		Provider:         p2pstreamv1.PublicDnsProvider_PUBLIC_DNS_PROVIDER_CLOUDFLARE,
		CloudflareZoneId: "zone-id",
		ApiToken:         "secret-token",
		Enabled:          true,
	})
	createReq.Header().Set("Cookie", cookie)
	createResp, err := client.CreatePublicTlsDnsCredential(context.Background(), createReq)
	if err != nil {
		t.Fatalf("create DNS credential: %v", err)
	}
	credential := createResp.Msg.GetCredential()
	if !credential.GetApiTokenSet() {
		t.Fatalf("expected token set flag in create response: %+v", credential)
	}

	cfg := getPublicProxyConfig(t, client, cookie)
	if len(cfg.GetTlsDnsCredentials()) != 1 || !cfg.GetTlsDnsCredentials()[0].GetApiTokenSet() {
		t.Fatalf("unexpected DNS credentials in config: %+v", cfg.GetTlsDnsCredentials())
	}

	updateReq := connect.NewRequest(&p2pstreamv1.UpdatePublicTlsDnsCredentialRequest{
		Id:               credential.GetId(),
		Name:             "cf-renamed",
		Provider:         p2pstreamv1.PublicDnsProvider_PUBLIC_DNS_PROVIDER_CLOUDFLARE,
		CloudflareZoneId: "zone-id-2",
		Enabled:          true,
	})
	updateReq.Header().Set("Cookie", cookie)
	updateResp, err := client.UpdatePublicTlsDnsCredential(context.Background(), updateReq)
	if err != nil {
		t.Fatalf("update DNS credential: %v", err)
	}
	if !updateResp.Msg.GetCredential().GetApiTokenSet() {
		t.Fatalf("expected token to be preserved: %+v", updateResp.Msg.GetCredential())
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

func publicRouteTargetByName(t *testing.T, cfg *p2pstreamv1.GetPublicProxyConfigResponse, name string) *p2pstreamv1.PublicRouteTarget {
	t.Helper()
	for _, target := range cfg.GetRouteTargets() {
		if target.GetName() == name {
			return target
		}
	}
	t.Fatalf("route target %q not found in %+v", name, cfg.GetRouteTargets())
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

func assertSeededWelcomeRoute(t *testing.T, cfg *p2pstreamv1.GetPublicProxyConfigResponse, listenerID int64) {
	t.Helper()
	for _, route := range cfg.Routes {
		if route.GetListenerId() != listenerID {
			continue
		}
		if route.GetPriority() != 1000 ||
			route.GetHostPattern() != "" ||
			route.GetPathPrefix() != "/" ||
			!route.GetIsDefault() ||
			route.GetAction() != p2pstreamv1.PublicRouteAction_PUBLIC_ROUTE_ACTION_FORWARD ||
			!route.GetEnabled() ||
			len(route.GetTargets()) != 1 ||
			route.GetTargets()[0].GetTargetType() != p2pstreamv1.PublicRouteTargetType_PUBLIC_ROUTE_TARGET_TYPE_STATIC {
			t.Fatalf("unexpected seeded route for listener %d: %+v", listenerID, route)
		}
		return
	}
	t.Fatalf("seeded route for listener %d not found in %+v", listenerID, cfg.Routes)
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

func testTLSKeyPair(t *testing.T, hostname string) ([]byte, []byte) {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate test TLS key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			CommonName: hostname,
		},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{hostname},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create test TLS certificate: %v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	return certPEM, keyPEM
}

func assertFileBytes(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(got) != string(want) {
		t.Fatalf("%s contents did not match uploaded bytes", path)
	}
}

func assertFileMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %o, want %o", path, got, want)
	}
}

func parseTestCertificateFile(t *testing.T, path string) *x509.Certificate {
	t.Helper()
	certPEM, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read certificate %s: %v", path, err)
	}
	block, _ := pem.Decode(certPEM)
	if block == nil {
		t.Fatalf("certificate %s is not PEM", path)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse certificate %s: %v", path, err)
	}
	return cert
}
