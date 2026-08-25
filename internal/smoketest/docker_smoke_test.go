//go:build docker_smoke

package smoketest

import (
	"bufio"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/gen/proto/p2pstream/v1/p2pstreamv1connect"
)

const (
	smokeAdminUsername = "smoke_admin"
	smokeAdminPassword = "correct horse battery staple"
)

func TestDockerSmoke(t *testing.T) {
	ctx := context.Background()
	managementURL := envOrDefault("MANAGEMENT_URL", "https://server:8081")
	publicDefaultURL := envOrDefault("PUBLIC_DEFAULT_URL", "http://server:8080")
	publicAgentURL := envOrDefault("PUBLIC_AGENT_URL", "http://server:8089")
	publicStaticURL := envOrDefault("PUBLIC_STATIC_URL", "http://server:8088")
	publicHTTPSURL := envOrDefault("PUBLIC_HTTPS_URL", "https://server:443")
	upstreamURL := envOrDefault("UPSTREAM_URL", "http://upstream:9000")

	client := p2pstreamv1connect.NewAgentManagementServiceClient(
		managementHTTPClient(t),
		managementURL,
	)

	waitManagement(ctx, t, client)
	cookie := setupAndLogin(ctx, t, client)

	cfg := getPublicProxyConfig(ctx, t, client, cookie)
	ensureDefaultListeners(ctx, t, client, cookie, cfg)
	waitHTTPBody(t, httpClient(), publicDefaultURL, http.StatusOK, "Welcome to p2pstream proxy", "seeded static welcome listener")

	cfg = getPublicProxyConfig(ctx, t, client, cookie)
	defaultListener := requireListener(t, cfg, "public-http")
	upsertDefaultRouteTarget(ctx, t, client, cookie, cfg, defaultListener.GetId(), "default", proxyTarget("default-direct", upstreamURL, p2pstreamv1.PublicRouteTargetTransport_PUBLIC_ROUTE_TARGET_TRANSPORT_DIRECT, "", 60000))
	cfg = getPublicProxyConfig(ctx, t, client, cookie)
	httpsListener := requireListener(t, cfg, "public-https")
	upsertDefaultRouteTarget(ctx, t, client, cookie, cfg, httpsListener.GetId(), "default-https", proxyTarget("default-https-direct", upstreamURL, p2pstreamv1.PublicRouteTargetTransport_PUBLIC_ROUTE_TARGET_TRANSPORT_DIRECT, "", 60000))
	createWebSocketTrafficShaper(ctx, t, client, cookie)

	waitAgentConnected(ctx, t, client, cookie)
	waitHTTPBody(t, httpClient(), publicDefaultURL, http.StatusOK, "smoke upstream ok", "proxy-forward default listener")
	t.Run("direct baseline", func(t *testing.T) {
		waitHTTPBody(t, httpClient(), smokeURL(publicDefaultURL, "/"), http.StatusOK, "smoke upstream ok", "direct GET")
		smokePostEcho(t, smokeURL(publicDefaultURL, "/echo"))
		smokeStream(t, smokeURL(publicDefaultURL, "/stream"))
		t.Run("shaped websocket", func(t *testing.T) {
			smokeWebSocketEcho(t, smokeURL(publicDefaultURL, "/ws"))
		})
		t.Run("shaped websocket TLS", func(t *testing.T) {
			smokeWebSocketEcho(t, smokeURL(publicHTTPSURL, "/ws"))
		})
	})

	cfg = getPublicProxyConfig(ctx, t, client, cookie)
	dockerAgent := requireAgent(t, cfg, "docker-agent")
	dockerAgent = updateAgentLabels(ctx, t, client, cookie, dockerAgent, map[string]string{"smoke": "true", "site": "docker"})
	agentSelector := map[string]string{"smoke": "true", "site": "docker"}
	agentListener := upsertListener(ctx, t, client, cookie, cfg, "docker-agent", "", 8089, p2pstreamv1.PublicListenerProtocol_PUBLIC_LISTENER_PROTOCOL_HTTP, true)
	upsertDefaultRouteTarget(ctx, t, client, cookie, getPublicProxyConfig(ctx, t, client, cookie), agentListener.GetId(), "docker-agent", proxyTargetWithSelector("docker-agent-target", upstreamURL, p2pstreamv1.PublicRouteTargetTransport_PUBLIC_ROUTE_TARGET_TRANSPORT_AGENT, agentSelector, 60000))
	waitHTTPBody(t, httpClient(), publicAgentURL, http.StatusOK, "smoke upstream ok", "agent-routed listener")
	t.Run("agent pool forwarding", func(t *testing.T) {
		waitHTTPBody(t, httpClient(), smokeURL(publicAgentURL, "/"), http.StatusOK, "smoke upstream ok", "agent GET")
		smokePostEcho(t, smokeURL(publicAgentURL, "/echo"))
		smokeStream(t, smokeURL(publicAgentURL, "/stream"))
		smokeHeaders(t, smokeURL(publicAgentURL, "/headers"), mustURLHost(t, upstreamURL), mustURLHostname(t, publicAgentURL))
		t.Run("shaped websocket", func(t *testing.T) {
			smokeWebSocketEcho(t, smokeURL(publicAgentURL, "/ws"))
		})

		upsertDefaultRouteTarget(ctx, t, client, cookie, getPublicProxyConfig(ctx, t, client, cookie), agentListener.GetId(), "docker-agent", proxyTargetWithSelector("docker-agent-target", upstreamURL, p2pstreamv1.PublicRouteTargetTransport_PUBLIC_ROUTE_TARGET_TRANSPORT_AGENT, agentSelector, 1000))
		waitHTTPStatus(t, httpClient(), smokeURL(publicAgentURL, "/slow-headers"), func(resp *http.Response, body string) error {
			if resp.StatusCode != http.StatusGatewayTimeout {
				return fmt.Errorf("expected status 504, got %d with body %q", resp.StatusCode, body)
			}
			return nil
		}, "agent response-header timeout")

		upsertDefaultRouteTarget(ctx, t, client, cookie, getPublicProxyConfig(ctx, t, client, cookie), agentListener.GetId(), "docker-agent", proxyTargetWithSelector("docker-agent-target", upstreamURL, p2pstreamv1.PublicRouteTargetTransport_PUBLIC_ROUTE_TARGET_TRANSPORT_AGENT, agentSelector, 60000))
		smokeCloseEarly(t, smokeURL(publicAgentURL, "/close-early"))
		waitHTTPBody(t, httpClient(), smokeURL(publicAgentURL, "/"), http.StatusOK, "smoke upstream ok", "agent listener after close-early")
	})
	t.Run("agent failover retries disconnects and HTTP statuses", func(t *testing.T) {
		retryLabels := map[string]string{"smoke": "retry", "scenario": "agent-disconnect"}
		primary, primaryToken := createSmokeAgent(ctx, t, client, cookie, "Docker Retry Primary", retryLabels)
		backup, backupToken := createSmokeAgent(ctx, t, client, cookie, "Docker Retry Backup", retryLabels)

		controlDir := envOrDefault("SMOKE_CONTROL_DIR", "/control")
		writeSmokeAgentCredentials(t, controlDir, "retry-primary", primary.GetPublicId(), primaryToken)
		writeSmokeAgentCredentials(t, controlDir, "retry-backup", backup.GetPublicId(), backupToken)
		startSmokeAgent(t, controlDir, "retry-primary")
		waitAgentConnectionState(ctx, t, client, cookie, primary.GetPublicId(), true)
		waitAgentConnectionState(ctx, t, client, cookie, backup.GetPublicId(), false)

		retryRoute := upsertDefaultRouteTarget(
			ctx,
			t,
			client,
			cookie,
			getPublicProxyConfig(ctx, t, client, cookie),
			agentListener.GetId(),
			"docker-agent-retry",
			proxyTargetWithSelector(
				"docker-agent-retry-target",
				upstreamURL,
				p2pstreamv1.PublicRouteTargetTransport_PUBLIC_ROUTE_TARGET_TRANSPORT_AGENT,
				retryLabels,
				60000,
			),
		)
		retryRule := createAgentDisconnectRetryRule(ctx, t, client, cookie, retryRoute.GetId())

		assets := []string{"app.js", "vendor.js", "site.css"}
		type requestResult struct {
			asset   string
			status  int
			body    string
			attempt string
			err     error
		}
		resultCh := make(chan requestResult, len(assets))
		for _, asset := range assets {
			go func() {
				requestClient := httpClient()
				requestClient.Timeout = 20 * time.Second
				resp, err := requestClient.Get(smokeURL(publicAgentURL, "/retry-assets/"+asset))
				if err != nil {
					resultCh <- requestResult{asset: asset, err: err}
					return
				}
				defer resp.Body.Close()
				body, readErr := io.ReadAll(resp.Body)
				resultCh <- requestResult{
					asset:   asset,
					status:  resp.StatusCode,
					body:    string(body),
					attempt: resp.Header.Get("X-Smoke-Upstream-Attempt"),
					err:     readErr,
				}
			}()
		}

		waitRetryAssetAttempts(t, upstreamURL, len(assets))
		startSmokeAgent(t, controlDir, "retry-backup")
		waitAgentConnectionState(ctx, t, client, cookie, backup.GetPublicId(), true)
		rotateSmokeAgentToken(ctx, t, client, cookie, primary.GetId())

		for range assets {
			var result requestResult
			select {
			case result = <-resultCh:
			case <-time.After(20 * time.Second):
				t.Fatal("asset requests did not finish after primary agent disconnected")
			}
			if result.err != nil {
				t.Fatalf("asset %q request after primary agent disconnect: %v", result.asset, result.err)
			}
			if result.status != http.StatusOK {
				t.Fatalf("asset %q request status = %d, want 200; body=%q", result.asset, result.status, result.body)
			}
			if wantBody := result.asset + " recovered\n"; result.body != wantBody {
				t.Fatalf("asset %q request body = %q, want %q", result.asset, result.body, wantBody)
			}
			if result.attempt != "2" {
				t.Fatalf("asset %q response upstream attempt = %q, want 2", result.asset, result.attempt)
			}
		}
		waitRetryAssetAttempts(t, upstreamURL, 2*len(assets))
		waitRetryRuleRecovered(ctx, t, client, cookie, retryRule.GetId(), int64(len(assets)))

		updateAgentLabels(ctx, t, client, cookie, dockerAgent, retryLabels)
		waitAgentConnectionState(ctx, t, client, cookie, dockerAgent.GetPublicId(), true)
		smokeRetryableStatus(t, publicAgentURL, http.StatusBadGateway, "chunk.js")
		smokeRetryableStatus(t, publicAgentURL, http.StatusServiceUnavailable, "theme.css")
		waitRetryRuleRecovered(ctx, t, client, cookie, retryRule.GetId(), int64(len(assets)+2))
		waitRetryStatusErrorKinds(ctx, t, client, cookie, http.StatusBadGateway, http.StatusServiceUnavailable)
	})

	staticListener := upsertListener(ctx, t, client, cookie, getPublicProxyConfig(ctx, t, client, cookie), "docker-static", "", 8088, p2pstreamv1.PublicListenerProtocol_PUBLIC_LISTENER_PROTOCOL_HTTP, true)
	upsertDefaultRouteTarget(ctx, t, client, cookie, getPublicProxyConfig(ctx, t, client, cookie), staticListener.GetId(), "docker-static", staticTarget("docker-static-target", http.StatusOK, "ok"))
	waitHTTPBody(t, httpClient(), publicStaticURL, http.StatusOK, "ok", "static listener")

	disablePublicListener(ctx, t, client, cookie, staticListener.Id)
	waitHTTPFailure(t, publicStaticURL, "disabled static listener")

	enablePublicListener(ctx, t, client, cookie, staticListener.Id)
	waitHTTPBody(t, httpClient(), publicStaticURL, http.StatusOK, "ok", "re-enabled static listener")

	waitHTTPBody(t, insecureHTTPClient(), publicHTTPSURL, http.StatusOK, "smoke upstream ok", "HTTPS proxy listener")

	waitDashboardHasProxyRequests(ctx, t, client, cookie)
}

func createWebSocketTrafficShaper(
	ctx context.Context,
	t *testing.T,
	client p2pstreamv1connect.AgentManagementServiceClient,
	cookie string,
) {
	t.Helper()
	req := connect.NewRequest(&p2pstreamv1.CreatePublicTrafficShaperRuleRequest{
		Name:                   "docker-websocket-shaper",
		Priority:               10,
		Enabled:                true,
		BudgetScope:            p2pstreamv1.PublicTrafficShaperBudgetScope_PUBLIC_TRAFFIC_SHAPER_BUDGET_SCOPE_PER_REQUEST,
		ProtocolScope:          p2pstreamv1.PublicTrafficShaperProtocolScope_PUBLIC_TRAFFIC_SHAPER_PROTOCOL_SCOPE_WEBSOCKET_ONLY,
		UploadBytesPerSecond:   1024 * 1024,
		DownloadBytesPerSecond: 1024 * 1024,
		BurstBytes:             1024 * 1024,
	})
	req.Header().Set("Cookie", cookie)
	resp, err := client.CreatePublicTrafficShaperRule(ctx, req)
	if err != nil {
		t.Fatalf("create WebSocket traffic shaper: %v", err)
	}
	if resp.Msg.GetRule().GetProtocolScope() != p2pstreamv1.PublicTrafficShaperProtocolScope_PUBLIC_TRAFFIC_SHAPER_PROTOCOL_SCOPE_WEBSOCKET_ONLY {
		t.Fatalf("WebSocket traffic shaper scope = %s", resp.Msg.GetRule().GetProtocolScope())
	}
}

func createSmokeAgent(
	ctx context.Context,
	t *testing.T,
	client p2pstreamv1connect.AgentManagementServiceClient,
	cookie string,
	name string,
	labels map[string]string,
) (*p2pstreamv1.Agent, string) {
	t.Helper()
	req := connect.NewRequest(&p2pstreamv1.CreateAgentRequest{
		Name:    name,
		Enabled: true,
		Labels:  labels,
	})
	req.Header().Set("Cookie", cookie)
	resp, err := client.CreateAgent(ctx, req)
	if err != nil {
		t.Fatalf("create smoke agent %q: %v", name, err)
	}
	if resp.Msg.GetAgent().GetPublicId() == "" || resp.Msg.GetToken() == "" {
		t.Fatalf("create smoke agent %q returned incomplete credentials", name)
	}
	return resp.Msg.GetAgent(), resp.Msg.GetToken()
}

func createAgentDisconnectRetryRule(
	ctx context.Context,
	t *testing.T,
	client p2pstreamv1connect.AgentManagementServiceClient,
	cookie string,
	routeID int64,
) *p2pstreamv1.PublicRetryRule {
	t.Helper()
	req := connect.NewRequest(&p2pstreamv1.CreatePublicRetryRuleRequest{
		Name:                      "docker-agent-disconnect-retry",
		Priority:                  1,
		Enabled:                   true,
		Methods:                   []string{http.MethodGet},
		MaxRetries:                1,
		FailureMode:               p2pstreamv1.PublicRetryFailureMode_PUBLIC_RETRY_FAILURE_MODE_PRE_RESPONSE_FAILURES,
		BodyMode:                  p2pstreamv1.PublicRetryBodyMode_PUBLIC_RETRY_BODY_MODE_NEVER,
		RetryStatusCodes:          []int64{http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout},
		RouteIds:                  []int64{routeID},
		DuplicateRiskAcknowledged: true,
	})
	req.Header().Set("Cookie", cookie)
	resp, err := client.CreatePublicRetryRule(ctx, req)
	if err != nil {
		t.Fatalf("create agent disconnect retry rule: %v", err)
	}
	return resp.Msg.GetRule()
}

func writeSmokeAgentCredentials(t *testing.T, dir string, prefix string, publicID string, token string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create smoke control directory: %v", err)
	}
	for suffix, value := range map[string]string{"id": publicID, "token": token} {
		path := filepath.Join(dir, prefix+"."+suffix)
		if err := os.WriteFile(path, []byte(value+"\n"), 0o644); err != nil {
			t.Fatalf("write smoke agent %s: %v", suffix, err)
		}
	}
}

func startSmokeAgent(t *testing.T, dir string, prefix string) {
	t.Helper()
	path := filepath.Join(dir, prefix+".start")
	if err := os.WriteFile(path, []byte("start\n"), 0o644); err != nil {
		t.Fatalf("start smoke agent %q: %v", prefix, err)
	}
}

func rotateSmokeAgentToken(
	ctx context.Context,
	t *testing.T,
	client p2pstreamv1connect.AgentManagementServiceClient,
	cookie string,
	agentID int64,
) {
	t.Helper()
	req := connect.NewRequest(&p2pstreamv1.RotateAgentTokenRequest{Id: agentID})
	req.Header().Set("Cookie", cookie)
	if _, err := client.RotateAgentToken(ctx, req); err != nil {
		t.Fatalf("rotate primary smoke agent token: %v", err)
	}
}

func waitManagement(ctx context.Context, t *testing.T, client p2pstreamv1connect.AgentManagementServiceClient) {
	t.Helper()

	var lastErr error
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		_, lastErr = client.GetSetupState(ctx, connect.NewRequest(&p2pstreamv1.GetSetupStateRequest{}))
		if lastErr == nil {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("management server did not become ready: %v", lastErr)
}

func setupAndLogin(ctx context.Context, t *testing.T, client p2pstreamv1connect.AgentManagementServiceClient) string {
	t.Helper()

	setupResp, err := client.GetSetupState(ctx, connect.NewRequest(&p2pstreamv1.GetSetupStateRequest{}))
	if err != nil {
		t.Fatalf("get setup state: %v", err)
	}
	if setupResp.Msg.GetSetupRequired() {
		if !setupResp.Msg.GetSetupAvailable() {
			t.Fatalf("setup is unavailable: %s", setupResp.Msg.GetSetupUnavailableReason())
		}
		setupAdmin(ctx, t, client)
	}

	loginResp, err := client.Login(ctx, connect.NewRequest(&p2pstreamv1.LoginRequest{
		Username: smokeAdminUsername,
		Password: smokeAdminPassword,
	}))
	if err != nil {
		t.Fatalf("login admin: %v; run `make docker-smoke-clean` if an old smoke database exists", err)
	}
	cookie := cookieHeaderFromSetCookie(loginResp.Header().Get("Set-Cookie"))
	if cookie == "" {
		t.Fatal("login response did not include a session cookie")
	}
	return cookie
}

func setupAdmin(ctx context.Context, t *testing.T, client p2pstreamv1connect.AgentManagementServiceClient) {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for {
		_, err := client.SetupAdmin(ctx, connect.NewRequest(&p2pstreamv1.SetupAdminRequest{
			Username:   smokeAdminUsername,
			Password:   smokeAdminPassword,
			SetupToken: envOrDefault("MANAGEMENT_SETUP_TOKEN", ""),
		}))
		if err == nil || connect.CodeOf(err) == connect.CodeFailedPrecondition {
			return
		}
		if !strings.Contains(strings.ToLower(err.Error()), "database is locked") || time.Now().After(deadline) {
			t.Fatalf("setup admin: %v", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func getPublicProxyConfig(
	ctx context.Context,
	t *testing.T,
	client p2pstreamv1connect.AgentManagementServiceClient,
	cookie string,
) *p2pstreamv1.GetPublicProxyConfigResponse {
	t.Helper()

	req := connect.NewRequest(&p2pstreamv1.GetPublicProxyConfigRequest{})
	req.Header().Set("Cookie", cookie)
	resp, err := client.GetPublicProxyConfig(ctx, req)
	if err != nil {
		t.Fatalf("get public proxy config: %v", err)
	}
	return resp.Msg
}

func ensureDefaultListeners(
	ctx context.Context,
	t *testing.T,
	client p2pstreamv1connect.AgentManagementServiceClient,
	cookie string,
	cfg *p2pstreamv1.GetPublicProxyConfigResponse,
) {
	t.Helper()

	httpListener := requireListener(t, cfg, "public-http")
	if httpListener.GetPort() != 8080 ||
		!httpListener.GetEnabled() ||
		httpListener.GetProtocol() != p2pstreamv1.PublicListenerProtocol_PUBLIC_LISTENER_PROTOCOL_HTTP ||
		httpListener.GetBindAddress() != "" {
		updateListener(ctx, t, client, cookie, httpListener.GetId(), "public-http", "", 8080, p2pstreamv1.PublicListenerProtocol_PUBLIC_LISTENER_PROTOCOL_HTTP, true)
	}

	httpsListener := requireListener(t, cfg, "public-https")
	if httpsListener.GetPort() != 443 ||
		!httpsListener.GetEnabled() ||
		httpsListener.GetProtocol() != p2pstreamv1.PublicListenerProtocol_PUBLIC_LISTENER_PROTOCOL_HTTPS ||
		httpsListener.GetBindAddress() != "" {
		updateListener(ctx, t, client, cookie, httpsListener.GetId(), "public-https", "", 443, p2pstreamv1.PublicListenerProtocol_PUBLIC_LISTENER_PROTOCOL_HTTPS, true)
	}
}

func upsertListener(
	ctx context.Context,
	t *testing.T,
	client p2pstreamv1connect.AgentManagementServiceClient,
	cookie string,
	cfg *p2pstreamv1.GetPublicProxyConfigResponse,
	name string,
	bindAddress string,
	port int64,
	protocol p2pstreamv1.PublicListenerProtocol,
	enabled bool,
) *p2pstreamv1.PublicListener {
	t.Helper()

	if existing := findListener(cfg, name); existing != nil {
		return updateListener(ctx, t, client, cookie, existing.GetId(), name, bindAddress, port, protocol, enabled)
	}

	req := connect.NewRequest(&p2pstreamv1.CreatePublicListenerRequest{
		Name:        name,
		BindAddress: bindAddress,
		Port:        port,
		Protocol:    protocol,
		Enabled:     enabled,
	})
	req.Header().Set("Cookie", cookie)
	resp, err := client.CreatePublicListener(ctx, req)
	if err != nil {
		t.Fatalf("create listener %q: %v", name, err)
	}
	return resp.Msg.GetListener()
}

func updateListener(
	ctx context.Context,
	t *testing.T,
	client p2pstreamv1connect.AgentManagementServiceClient,
	cookie string,
	id int64,
	name string,
	bindAddress string,
	port int64,
	protocol p2pstreamv1.PublicListenerProtocol,
	enabled bool,
) *p2pstreamv1.PublicListener {
	t.Helper()

	req := connect.NewRequest(&p2pstreamv1.UpdatePublicListenerRequest{
		Id:          id,
		Name:        name,
		BindAddress: bindAddress,
		Port:        port,
		Protocol:    protocol,
		Enabled:     enabled,
	})
	req.Header().Set("Cookie", cookie)
	resp, err := client.UpdatePublicListener(ctx, req)
	if err != nil {
		t.Fatalf("update listener %q: %v", name, err)
	}
	return resp.Msg.GetListener()
}

func upsertDefaultRouteTarget(
	ctx context.Context,
	t *testing.T,
	client p2pstreamv1connect.AgentManagementServiceClient,
	cookie string,
	cfg *p2pstreamv1.GetPublicProxyConfigResponse,
	listenerID int64,
	name string,
	target *p2pstreamv1.PublicRouteTarget,
) *p2pstreamv1.PublicRoute {
	t.Helper()

	target.RouteId = 0
	target.Position = 0
	route := findDefaultRoute(cfg, listenerID)
	if route == nil {
		req := connect.NewRequest(&p2pstreamv1.CreatePublicRouteRequest{
			ListenerId:                 listenerID,
			Priority:                   1000,
			PathPrefix:                 "/",
			TargetLoadBalancing:        p2pstreamv1.PublicRouteTargetLoadBalancing_PUBLIC_ROUTE_TARGET_LOAD_BALANCING_ROUND_ROBIN,
			IsDefault:                  true,
			Targets:                    []*p2pstreamv1.PublicRouteTarget{target},
			Enabled:                    true,
			Action:                     p2pstreamv1.PublicRouteAction_PUBLIC_ROUTE_ACTION_FORWARD,
			RedirectStatusCode:         http.StatusFound,
			RedirectPreservePathSuffix: true,
			RedirectPreserveQuery:      true,
		})
		req.Header().Set("Cookie", cookie)
		resp, err := client.CreatePublicRoute(ctx, req)
		if err != nil {
			t.Fatalf("create default route %q: %v", name, err)
		}
		return resp.Msg.GetRoute()
	}

	req := connect.NewRequest(&p2pstreamv1.UpdatePublicRouteRequest{
		Id:                         route.GetId(),
		ListenerId:                 listenerID,
		Priority:                   route.GetPriority(),
		HostPattern:                route.GetHostPattern(),
		PathPrefix:                 route.GetPathPrefix(),
		TargetLoadBalancing:        p2pstreamv1.PublicRouteTargetLoadBalancing_PUBLIC_ROUTE_TARGET_LOAD_BALANCING_ROUND_ROBIN,
		IsDefault:                  true,
		Targets:                    []*p2pstreamv1.PublicRouteTarget{target},
		Enabled:                    true,
		Action:                     p2pstreamv1.PublicRouteAction_PUBLIC_ROUTE_ACTION_FORWARD,
		RedirectStatusCode:         http.StatusFound,
		RedirectPreservePathSuffix: true,
		RedirectPreserveQuery:      true,
	})
	req.Header().Set("Cookie", cookie)
	resp, err := client.UpdatePublicRoute(ctx, req)
	if err != nil {
		t.Fatalf("update default route %q: %v", name, err)
	}
	return resp.Msg.GetRoute()
}

func proxyTarget(name string, targetURL string, transport p2pstreamv1.PublicRouteTargetTransport, agentPublicID string, responseHeaderTimeoutMillis int64) *p2pstreamv1.PublicRouteTarget {
	selectorLabels := map[string]string{}
	if transport == p2pstreamv1.PublicRouteTargetTransport_PUBLIC_ROUTE_TARGET_TRANSPORT_AGENT {
		selectorLabels = map[string]string{"p2pstream.io/agent-id": agentPublicID}
	}
	return proxyTargetWithSelector(name, targetURL, transport, selectorLabels, responseHeaderTimeoutMillis)
}

func proxyTargetWithSelector(name string, targetURL string, transport p2pstreamv1.PublicRouteTargetTransport, selectorLabels map[string]string, responseHeaderTimeoutMillis int64) *p2pstreamv1.PublicRouteTarget {
	selector := &p2pstreamv1.PublicAgentSelector{}
	if transport == p2pstreamv1.PublicRouteTargetTransport_PUBLIC_ROUTE_TARGET_TRANSPORT_AGENT {
		selector.MatchLabels = selectorLabels
	}
	return &p2pstreamv1.PublicRouteTarget{
		Name:                                name,
		Enabled:                             true,
		TargetType:                          p2pstreamv1.PublicRouteTargetType_PUBLIC_ROUTE_TARGET_TYPE_PROXY,
		Url:                                 targetURL,
		Transport:                           transport,
		AgentSelector:                       selector,
		AgentLoadBalancing:                  p2pstreamv1.PublicRouteTargetLoadBalancing_PUBLIC_ROUTE_TARGET_LOAD_BALANCING_ROUND_ROBIN,
		Weight:                              100,
		UpstreamResponseHeaderTimeoutMillis: responseHeaderTimeoutMillis,
		HealthCheck: &p2pstreamv1.PublicRouteTargetHealthCheck{
			Method:             http.MethodGet,
			Path:               "/health",
			IntervalMillis:     10000,
			TimeoutMillis:      2000,
			HealthyThreshold:   2,
			UnhealthyThreshold: 2,
			ExpectedStatusMin:  200,
			ExpectedStatusMax:  399,
		},
	}
}

func updateAgentLabels(
	ctx context.Context,
	t *testing.T,
	client p2pstreamv1connect.AgentManagementServiceClient,
	cookie string,
	agent *p2pstreamv1.Agent,
	labels map[string]string,
) *p2pstreamv1.Agent {
	t.Helper()

	req := connect.NewRequest(&p2pstreamv1.UpdateAgentRequest{
		Id:      agent.GetId(),
		Name:    agent.GetName(),
		Enabled: agent.GetEnabled(),
		Labels:  labels,
	})
	req.Header().Set("Cookie", cookie)
	resp, err := client.UpdateAgent(ctx, req)
	if err != nil {
		t.Fatalf("update agent labels for %q: %v", agent.GetPublicId(), err)
	}
	for key, want := range labels {
		if got := resp.Msg.GetAgent().GetLabels()[key]; got != want {
			t.Fatalf("agent label %s = %q, want %q", key, got, want)
		}
	}
	return resp.Msg.GetAgent()
}

func staticTarget(name string, statusCode int, body string) *p2pstreamv1.PublicRouteTarget {
	return &p2pstreamv1.PublicRouteTarget{
		Name:       name,
		Enabled:    true,
		TargetType: p2pstreamv1.PublicRouteTargetType_PUBLIC_ROUTE_TARGET_TYPE_STATIC,
		Transport:  p2pstreamv1.PublicRouteTargetTransport_PUBLIC_ROUTE_TARGET_TRANSPORT_DIRECT,
		Weight:     100,
		StaticResponseHeaders: []*p2pstreamv1.PublicHeader{
			{Name: "Content-Type", Value: "text/plain"},
		},
		StaticStatusCode:       int64(statusCode),
		StaticResponseBody:     body,
		StaticResponseBodyMode: p2pstreamv1.PublicResponseBodyMode_PUBLIC_RESPONSE_BODY_MODE_INLINE,
	}
}

func disablePublicListener(
	ctx context.Context,
	t *testing.T,
	client p2pstreamv1connect.AgentManagementServiceClient,
	cookie string,
	listenerID int64,
) {
	t.Helper()

	req := connect.NewRequest(&p2pstreamv1.DisablePublicListenerRequest{Id: listenerID})
	req.Header().Set("Cookie", cookie)
	resp, err := client.DisablePublicListener(ctx, req)
	if err != nil {
		t.Fatalf("disable public listener %d: %v", listenerID, err)
	}
	if resp.Msg.GetListener().GetEnabled() || resp.Msg.GetStatus().GetRunning() {
		t.Fatalf("listener %d did not disable cleanly: listener=%+v status=%+v", listenerID, resp.Msg.GetListener(), resp.Msg.GetStatus())
	}
}

func enablePublicListener(
	ctx context.Context,
	t *testing.T,
	client p2pstreamv1connect.AgentManagementServiceClient,
	cookie string,
	listenerID int64,
) {
	t.Helper()

	req := connect.NewRequest(&p2pstreamv1.EnablePublicListenerRequest{Id: listenerID})
	req.Header().Set("Cookie", cookie)
	resp, err := client.EnablePublicListener(ctx, req)
	if err != nil {
		t.Fatalf("enable public listener %d: %v", listenerID, err)
	}
	if !resp.Msg.GetListener().GetEnabled() || !resp.Msg.GetStatus().GetRunning() {
		t.Fatalf("listener %d did not re-enable cleanly: listener=%+v status=%+v", listenerID, resp.Msg.GetListener(), resp.Msg.GetStatus())
	}
}

func waitAgentConnected(
	ctx context.Context,
	t *testing.T,
	client p2pstreamv1connect.AgentManagementServiceClient,
	cookie string,
) {
	t.Helper()

	var lastStatus *p2pstreamv1.GetStatusResponse
	var lastErr error
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		req := connect.NewRequest(&p2pstreamv1.GetStatusRequest{})
		req.Header().Set("Cookie", cookie)
		resp, err := client.GetStatus(ctx, req)
		if err == nil {
			lastStatus = resp.Msg
			if resp.Msg.GetAgentConnected() && resp.Msg.GetProxy().GetState() == p2pstreamv1.ProxyState_PROXY_STATE_RUNNING {
				return
			}
		} else {
			lastErr = err
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("agent did not connect; last status=%+v last error=%v", lastStatus, lastErr)
}

func waitAgentConnectionState(
	ctx context.Context,
	t *testing.T,
	client p2pstreamv1connect.AgentManagementServiceClient,
	cookie string,
	publicID string,
	wantConnected bool,
) {
	t.Helper()
	var lastConnected bool
	var found bool
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		cfg := getPublicProxyConfig(ctx, t, client, cookie)
		found = false
		for _, agent := range cfg.GetAgents() {
			if agent.GetPublicId() != publicID {
				continue
			}
			found = true
			lastConnected = agent.GetConnected()
			if lastConnected == wantConnected {
				return
			}
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("agent %q connected = %t, want %t (found=%t)", publicID, lastConnected, wantConnected, found)
}

func waitRetryAssetAttempts(t *testing.T, upstreamURL string, want int) {
	t.Helper()
	type retryStatus struct {
		Attempts int `json:"attempts"`
	}
	client := httpClient()
	var last retryStatus
	var lastErr error
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := client.Get(smokeURL(upstreamURL, "/retry-asset-status"))
		if err == nil {
			func() {
				defer resp.Body.Close()
				if resp.StatusCode != http.StatusOK {
					lastErr = fmt.Errorf("status endpoint returned %d", resp.StatusCode)
					return
				}
				if err := json.NewDecoder(resp.Body).Decode(&last); err != nil {
					lastErr = err
					return
				}
				lastErr = nil
			}()
		} else {
			lastErr = err
		}
		if last.Attempts >= want {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("retry asset attempts = %d, want at least %d; last error=%v", last.Attempts, want, lastErr)
}

func smokeRetryableStatus(t *testing.T, publicURL string, retryStatus int, asset string) {
	t.Helper()
	resp, err := httpClient().Get(smokeURL(publicURL, fmt.Sprintf("/retry-status/%d/%s", retryStatus, asset)))
	if err != nil {
		t.Fatalf("retry HTTP status %d for asset %q: %v", retryStatus, asset, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read retry HTTP status %d response: %v", retryStatus, err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("retry HTTP status %d final response = %d %q, want 200", retryStatus, resp.StatusCode, body)
	}
	if got := resp.Header.Get("X-Smoke-Upstream-Attempt"); got != "2" {
		t.Fatalf("retry HTTP status %d upstream attempt = %q, want 2", retryStatus, got)
	}
	if want := fmt.Sprintf("%s recovered from %d\n", asset, retryStatus); string(body) != want {
		t.Fatalf("retry HTTP status %d response body = %q, want %q", retryStatus, body, want)
	}
}

func waitRetryRuleRecovered(
	ctx context.Context,
	t *testing.T,
	client p2pstreamv1connect.AgentManagementServiceClient,
	cookie string,
	ruleID int64,
	wantRequests int64,
) {
	t.Helper()
	var last *p2pstreamv1.DashboardRetryRuleSummary
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		req := connect.NewRequest(&p2pstreamv1.GetDashboardDiagnosticsRequest{
			WindowLabel: "1h",
			SampleLimit: 20,
		})
		req.Header().Set("Cookie", cookie)
		resp, err := client.GetDashboardDiagnostics(ctx, req)
		if err != nil {
			t.Fatalf("get dashboard retry diagnostics: %v", err)
		}
		for _, summary := range resp.Msg.GetRetryRules() {
			if summary.GetId() != ruleID {
				continue
			}
			last = summary
			if summary.GetMatchedRequests() == wantRequests &&
				summary.GetRetriedRequests() == wantRequests &&
				summary.GetRetryAttempts() == wantRequests &&
				summary.GetRecoveredRequests() == wantRequests &&
				summary.GetExhaustedRequests() == 0 &&
				summary.GetSkippedRequests() == 0 {
				return
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("dashboard did not report %d recovered retries for rule %d; last summary=%+v", wantRequests, ruleID, last)
}

func waitRetryStatusErrorKinds(
	ctx context.Context,
	t *testing.T,
	client p2pstreamv1connect.AgentManagementServiceClient,
	cookie string,
	statuses ...int,
) {
	t.Helper()
	want := make(map[string]struct{}, len(statuses))
	for _, status := range statuses {
		want[fmt.Sprintf("upstream_status_%d", status)] = struct{}{}
	}
	last := make(map[string]int64)
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		req := connect.NewRequest(&p2pstreamv1.GetDashboardDiagnosticsRequest{WindowLabel: "1h"})
		req.Header().Set("Cookie", cookie)
		resp, err := client.GetDashboardDiagnostics(ctx, req)
		if err != nil {
			t.Fatalf("get dashboard retry status diagnostics: %v", err)
		}
		for _, summary := range resp.Msg.GetRetryErrorKinds() {
			last[summary.GetLabel()] = summary.GetRetryAttempts()
		}
		complete := true
		for label := range want {
			if last[label] < 1 {
				complete = false
				break
			}
		}
		if complete {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("dashboard retry error kinds = %+v, want statuses %+v", last, want)
}

func waitDashboardHasProxyRequests(
	ctx context.Context,
	t *testing.T,
	client p2pstreamv1connect.AgentManagementServiceClient,
	cookie string,
) {
	t.Helper()

	var lastTotal int64
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		req := connect.NewRequest(&p2pstreamv1.GetDashboardRequest{})
		req.Header().Set("Cookie", cookie)
		resp, err := client.GetDashboard(ctx, req)
		if err != nil {
			t.Fatalf("get dashboard: %v", err)
		}
		for _, window := range resp.Msg.GetWindows() {
			if window.GetLabel() == "5m" {
				lastTotal = window.GetProxyRequests()
				if lastTotal > 0 {
					return
				}
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("dashboard never reported proxy requests; last 5m total=%d", lastTotal)
}

func smokeURL(base string, path string) string {
	return strings.TrimRight(base, "/") + path
}

func smokePostEcho(t *testing.T, requestURL string) {
	t.Helper()

	body := strings.Repeat("0123456789abcdef", 4096)
	sum := sha256.Sum256([]byte(body))
	req, err := http.NewRequest(http.MethodPost, requestURL, strings.NewReader(body))
	if err != nil {
		t.Fatalf("create POST request: %v", err)
	}
	req.Header.Set("Content-Type", "text/plain")
	resp, err := httpClient().Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", requestURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST %s status = %d body=%q, want 200", requestURL, resp.StatusCode, string(data))
	}
	var payload struct {
		Method        string `json:"method"`
		ContentLength int    `json:"content_length"`
		SHA256        string `json:"sha256"`
		Prefix        string `json:"prefix"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode POST echo response: %v", err)
	}
	if payload.Method != http.MethodPost {
		t.Fatalf("echo method = %q, want POST", payload.Method)
	}
	if payload.ContentLength != len(body) {
		t.Fatalf("echo content length = %d, want %d", payload.ContentLength, len(body))
	}
	if payload.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("echo sha256 = %q, want %q", payload.SHA256, hex.EncodeToString(sum[:]))
	}
	if payload.Prefix != body[:256] {
		t.Fatalf("echo prefix = %q, want first 256 bytes", payload.Prefix)
	}
}

func smokeStream(t *testing.T, requestURL string) {
	t.Helper()

	resp, body, err := getHTTP(httpClient(), requestURL)
	if err != nil {
		t.Fatalf("GET stream %s: %v", requestURL, err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET stream status = %d body=%q, want 200", resp.StatusCode, body)
	}
	want := "chunk-1\nchunk-2\nchunk-3\nchunk-4\nchunk-5\n"
	if body != want {
		t.Fatalf("stream body = %q, want %q", body, want)
	}
}

func smokeHeaders(t *testing.T, requestURL string, expectedUpstreamHost string, expectedForwardedHost string) {
	t.Helper()

	req, err := http.NewRequest(http.MethodGet, requestURL, nil)
	if err != nil {
		t.Fatalf("create headers request: %v", err)
	}
	req.Header.Set("X-Smoke-Request", "agent-header-check")
	req.Header.Set("X-Request-Method", http.MethodGet)
	resp, err := httpClient().Do(req)
	if err != nil {
		t.Fatalf("GET headers %s: %v", requestURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("GET headers status = %d body=%q, want 200", resp.StatusCode, string(data))
	}
	var headers map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&headers); err != nil {
		t.Fatalf("decode headers response: %v", err)
	}
	assertSmokeHeader(t, headers, "host", expectedUpstreamHost)
	assertSmokeHeader(t, headers, "x_forwarded_host", expectedForwardedHost)
	assertSmokeHeader(t, headers, "x_forwarded_proto", "http")
	assertSmokeHeader(t, headers, "x_request_method", http.MethodGet)
	assertSmokeHeader(t, headers, "x_smoke_request", "agent-header-check")
	if headers["x_forwarded_for"] == "" {
		t.Fatalf("x_forwarded_for is empty in %+v", headers)
	}
}

func assertSmokeHeader(t *testing.T, headers map[string]string, name string, want string) {
	t.Helper()
	if got := headers[name]; got != want {
		t.Fatalf("header %s = %q, want %q in %+v", name, got, want, headers)
	}
}

func mustURLHost(t *testing.T, raw string) string {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		t.Fatalf("invalid URL %q: %v", raw, err)
	}
	return parsed.Host
}

func mustURLHostname(t *testing.T, raw string) string {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" {
		t.Fatalf("invalid URL %q: %v", raw, err)
	}
	return parsed.Hostname()
}

func smokeCloseEarly(t *testing.T, requestURL string) {
	t.Helper()

	resp, err := httpClient().Get(requestURL)
	if err != nil {
		if isExpectedCloseEarlyError(err) {
			return
		}
		t.Fatalf("close-early request failed unexpectedly: %v", err)
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		if isExpectedCloseEarlyError(readErr) {
			return
		}
		t.Fatalf("close-early read failed unexpectedly: %v", readErr)
	}
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("close-early status = %d body=%q, want 502 or client read error", resp.StatusCode, string(body))
	}
}

func isExpectedCloseEarlyError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "unexpected eof") ||
		strings.Contains(message, "server closed idle connection") ||
		strings.Contains(message, "connection reset by peer")
}

func smokeWebSocketEcho(t *testing.T, requestURL string) {
	t.Helper()

	u, err := url.Parse(requestURL)
	if err != nil {
		t.Fatalf("parse websocket URL: %v", err)
	}
	address := u.Host
	if u.Port() == "" {
		port := "80"
		if u.Scheme == "https" {
			port = "443"
		}
		address = net.JoinHostPort(u.Hostname(), port)
	}
	var conn net.Conn
	switch u.Scheme {
	case "http":
		conn, err = net.DialTimeout("tcp", address, 5*time.Second)
	case "https":
		conn, err = tls.DialWithDialer(
			&net.Dialer{Timeout: 5 * time.Second},
			"tcp",
			address,
			&tls.Config{
				MinVersion:         tls.VersionTLS12,
				InsecureSkipVerify: true, // The smoke stack generates an ephemeral self-signed public certificate.
			},
		)
	default:
		t.Fatalf("unsupported websocket URL scheme %q", u.Scheme)
	}
	if err != nil {
		t.Fatalf("dial websocket target: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

	key := base64.StdEncoding.EncodeToString([]byte("p2pstream-smoke!!"))
	path := u.RequestURI()
	if path == "" {
		path = "/"
	}
	_, _ = fmt.Fprintf(conn, "GET %s HTTP/1.1\r\n", path)
	_, _ = fmt.Fprintf(conn, "Host: %s\r\n", u.Host)
	_, _ = fmt.Fprintf(conn, "Connection: Upgrade\r\n")
	_, _ = fmt.Fprintf(conn, "Upgrade: websocket\r\n")
	_, _ = fmt.Fprintf(conn, "Sec-WebSocket-Version: 13\r\n")
	_, _ = fmt.Fprintf(conn, "Sec-WebSocket-Key: %s\r\n", key)
	_, _ = fmt.Fprintf(conn, "\r\n")

	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, &http.Request{Method: http.MethodGet})
	if err != nil {
		t.Fatalf("read websocket handshake: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("websocket handshake status = %d, want 101", resp.StatusCode)
	}
	if got := resp.Header.Get("Sec-WebSocket-Accept"); got != websocketAccept(key) {
		t.Fatalf("websocket accept = %q, want %q", got, websocketAccept(key))
	}
	if err := writeClientWebSocketText(conn, "ping"); err != nil {
		t.Fatalf("write websocket frame: %v", err)
	}
	opcode, payload, err := readServerWebSocketFrame(reader)
	if err != nil {
		t.Fatalf("read websocket frame: %v", err)
	}
	if opcode != 0x1 || string(payload) != "pong" {
		t.Fatalf("websocket response opcode=%d payload=%q, want text pong", opcode, string(payload))
	}
	_ = writeClientWebSocketClose(conn)
}

func websocketAccept(key string) string {
	sum := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	return base64.StdEncoding.EncodeToString(sum[:])
}

func writeClientWebSocketText(conn net.Conn, payload string) error {
	return writeClientWebSocketFrame(conn, 0x1, []byte(payload))
}

func writeClientWebSocketClose(conn net.Conn) error {
	return writeClientWebSocketFrame(conn, 0x8, nil)
}

func writeClientWebSocketFrame(conn net.Conn, opcode byte, payload []byte) error {
	header := []byte{0x80 | opcode}
	maskBit := byte(0x80)
	switch {
	case len(payload) < 126:
		header = append(header, maskBit|byte(len(payload)))
	case len(payload) <= 0xffff:
		header = append(header, maskBit|126, byte(len(payload)>>8), byte(len(payload)))
	default:
		header = append(header, maskBit|127)
		var ext [8]byte
		binary.BigEndian.PutUint64(ext[:], uint64(len(payload)))
		header = append(header, ext[:]...)
	}
	mask := [4]byte{0x12, 0x34, 0x56, 0x78}
	header = append(header, mask[:]...)
	masked := make([]byte, len(payload))
	for i := range payload {
		masked[i] = payload[i] ^ mask[i%4]
	}
	if _, err := conn.Write(header); err != nil {
		return err
	}
	_, err := conn.Write(masked)
	return err
}

func readServerWebSocketFrame(r *bufio.Reader) (byte, []byte, error) {
	header := make([]byte, 2)
	if _, err := io.ReadFull(r, header); err != nil {
		return 0, nil, err
	}
	opcode := header[0] & 0x0f
	payloadLen := uint64(header[1] & 0x7f)
	switch payloadLen {
	case 126:
		var ext [2]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			return 0, nil, err
		}
		payloadLen = uint64(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			return 0, nil, err
		}
		payloadLen = binary.BigEndian.Uint64(ext[:])
	}
	payload := make([]byte, payloadLen)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}
	return opcode, payload, nil
}

func waitHTTPBody(t *testing.T, client *http.Client, url string, statusCode int, bodyContains string, label string) {
	t.Helper()
	waitHTTPStatus(t, client, url, func(resp *http.Response, body string) error {
		if resp.StatusCode != statusCode {
			return fmt.Errorf("expected status %d, got %d with body %q", statusCode, resp.StatusCode, body)
		}
		if !strings.Contains(body, bodyContains) {
			return fmt.Errorf("expected body to contain %q, got %q", bodyContains, body)
		}
		return nil
	}, label)
}

func waitHTTPStatus(t *testing.T, client *http.Client, url string, check func(*http.Response, string) error, label string) {
	t.Helper()

	var lastErr error
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		resp, body, err := getHTTP(client, url)
		if err == nil {
			lastErr = check(resp, body)
			if lastErr == nil {
				return
			}
		} else {
			lastErr = err
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("%s did not return expected response from %s: %v", label, url, lastErr)
}

func waitHTTPFailure(t *testing.T, url string, label string) {
	t.Helper()

	client := httpClient()
	deadline := time.Now().Add(30 * time.Second)
	var lastResult string
	for time.Now().Before(deadline) {
		resp, body, err := getHTTP(client, url)
		if err != nil {
			return
		}
		lastResult = fmt.Sprintf("status=%d body=%q", resp.StatusCode, body)
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("%s kept responding at %s after disable: %s", label, url, lastResult)
}

func getHTTP(client *http.Client, url string) (*http.Response, string, error) {
	resp, err := client.Get(url)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp, "", err
	}
	return resp, string(body), nil
}

func httpClient() *http.Client {
	return &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			DisableKeepAlives: true,
		},
	}
}

func insecureHTTPClient() *http.Client {
	return &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			DisableKeepAlives: true,
			TLSClientConfig:   &tls.Config{InsecureSkipVerify: true},
		},
	}
}

func managementHTTPClient(t *testing.T) *http.Client {
	t.Helper()

	transport := &http.Transport{
		DisableKeepAlives: true,
	}
	if caFile := strings.TrimSpace(os.Getenv("MANAGEMENT_CA_FILE")); caFile != "" {
		roots, err := x509.SystemCertPool()
		if err != nil || roots == nil {
			roots = x509.NewCertPool()
		}
		caPEM, err := os.ReadFile(caFile)
		if err != nil {
			t.Fatalf("read MANAGEMENT_CA_FILE: %v", err)
		}
		if !roots.AppendCertsFromPEM(caPEM) {
			t.Fatalf("MANAGEMENT_CA_FILE %q did not contain PEM certificates", caFile)
		}
		transport.TLSClientConfig = &tls.Config{
			MinVersion: tls.VersionTLS12,
			RootCAs:    roots,
		}
	}
	return &http.Client{Timeout: 10 * time.Second, Transport: transport}
}

func requireAgent(t *testing.T, cfg *p2pstreamv1.GetPublicProxyConfigResponse, publicID string) *p2pstreamv1.Agent {
	t.Helper()
	for _, agent := range cfg.GetAgents() {
		if agent.GetPublicId() == publicID {
			return agent
		}
	}
	t.Fatalf("agent %q not found in %+v", publicID, cfg.GetAgents())
	return nil
}

func requireListener(t *testing.T, cfg *p2pstreamv1.GetPublicProxyConfigResponse, name string) *p2pstreamv1.PublicListener {
	t.Helper()
	listener := findListener(cfg, name)
	if listener == nil {
		t.Fatalf("listener %q not found in %+v", name, cfg.GetListeners())
	}
	return listener
}

func findListener(cfg *p2pstreamv1.GetPublicProxyConfigResponse, name string) *p2pstreamv1.PublicListener {
	for _, listener := range cfg.GetListeners() {
		if listener.GetName() == name {
			return listener
		}
	}
	return nil
}

func findDefaultRoute(cfg *p2pstreamv1.GetPublicProxyConfigResponse, listenerID int64) *p2pstreamv1.PublicRoute {
	for _, route := range cfg.GetRoutes() {
		if route.GetListenerId() == listenerID && route.GetIsDefault() {
			return route
		}
	}
	return nil
}

func cookieHeaderFromSetCookie(setCookie string) string {
	if setCookie == "" {
		return ""
	}
	return strings.Split(setCookie, ";")[0]
}

func envOrDefault(name string, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
