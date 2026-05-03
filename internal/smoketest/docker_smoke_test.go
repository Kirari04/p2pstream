//go:build docker_smoke

package smoketest

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"os"
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
	managementURL := envOrDefault("MANAGEMENT_URL", "http://server:8081")
	publicDefaultURL := envOrDefault("PUBLIC_DEFAULT_URL", "http://server:8080")
	publicAgentURL := envOrDefault("PUBLIC_AGENT_URL", "http://server:8089")
	publicStaticURL := envOrDefault("PUBLIC_STATIC_URL", "http://server:8088")
	publicHTTPSURL := envOrDefault("PUBLIC_HTTPS_URL", "https://server:443")
	upstreamURL := envOrDefault("UPSTREAM_URL", "http://upstream:9000")

	client := p2pstreamv1connect.NewAgentManagementServiceClient(
		&http.Client{Timeout: 10 * time.Second},
		managementURL,
	)

	waitManagement(ctx, t, client)
	cookie := setupAndLogin(ctx, t, client)

	cfg := getPublicProxyConfig(ctx, t, client, cookie)
	defaultBackend := requireBackend(t, cfg, "default")
	defaultBackend = upsertProxyBackend(ctx, t, client, cookie, defaultBackend, upstreamURL)
	ensureDefaultListeners(ctx, t, client, cookie, cfg, defaultBackend.Id)

	waitAgentConnected(ctx, t, client, cookie)
	waitHTTPBody(t, httpClient(), publicDefaultURL, http.StatusOK, "Directory listing", "proxy-forward default listener")

	cfg = getPublicProxyConfig(ctx, t, client, cookie)
	dockerAgent := requireAgent(t, cfg, "docker-agent")
	agentBackend := upsertAgentBackend(ctx, t, client, cookie, upstreamURL, dockerAgent.Id)
	_ = upsertAgentListener(ctx, t, client, cookie, agentBackend.Id)
	waitHTTPBody(t, httpClient(), publicAgentURL, http.StatusOK, "Directory listing", "agent-routed listener")

	staticBackend := upsertStaticBackend(ctx, t, client, cookie)
	staticListener := upsertStaticListener(ctx, t, client, cookie, staticBackend.Id)
	waitHTTPBody(t, httpClient(), publicStaticURL, http.StatusOK, "ok", "static listener")

	disablePublicListener(ctx, t, client, cookie, staticListener.Id)
	waitHTTPFailure(t, publicStaticURL, "disabled static listener")

	enablePublicListener(ctx, t, client, cookie, staticListener.Id)
	waitHTTPBody(t, httpClient(), publicStaticURL, http.StatusOK, "ok", "re-enabled static listener")

	waitHTTPStatus(t, insecureHTTPClient(), publicHTTPSURL, func(resp *http.Response, body string) error {
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("expected status 200, got %d with body %q", resp.StatusCode, body)
		}
		return nil
	}, "HTTPS fallback listener")

	waitDashboardHasProxyRequests(ctx, t, client, cookie)
}

func upsertAgentBackend(
	ctx context.Context,
	t *testing.T,
	client p2pstreamv1connect.AgentManagementServiceClient,
	cookie string,
	targetOrigin string,
	agentID int64,
) *p2pstreamv1.PublicBackend {
	t.Helper()

	assignment := &p2pstreamv1.PublicBackendAgent{
		AgentId: agentID,
		Weight:  100,
		Enabled: true,
	}
	cfg := getPublicProxyConfig(ctx, t, client, cookie)
	if existing := findBackend(cfg, "docker-agent-backend"); existing != nil {
		req := connect.NewRequest(&p2pstreamv1.UpdatePublicBackendRequest{
			Id:               existing.GetId(),
			Name:             "docker-agent-backend",
			TargetOrigin:     targetOrigin,
			Enabled:          true,
			BackendType:      p2pstreamv1.PublicBackendType_PUBLIC_BACKEND_TYPE_PROXY_FORWARD,
			ForwardMode:      p2pstreamv1.PublicBackendForwardMode_PUBLIC_BACKEND_FORWARD_MODE_AGENT_POOL,
			LoadBalancing:    p2pstreamv1.PublicBackendLoadBalancing_PUBLIC_BACKEND_LOAD_BALANCING_ROUND_ROBIN,
			AgentAssignments: []*p2pstreamv1.PublicBackendAgent{assignment},
		})
		req.Header().Set("Cookie", cookie)
		resp, err := client.UpdatePublicBackend(ctx, req)
		if err != nil {
			t.Fatalf("update agent backend: %v", err)
		}
		return resp.Msg.GetBackend()
	}

	req := connect.NewRequest(&p2pstreamv1.CreatePublicBackendRequest{
		Name:             "docker-agent-backend",
		TargetOrigin:     targetOrigin,
		Enabled:          true,
		BackendType:      p2pstreamv1.PublicBackendType_PUBLIC_BACKEND_TYPE_PROXY_FORWARD,
		ForwardMode:      p2pstreamv1.PublicBackendForwardMode_PUBLIC_BACKEND_FORWARD_MODE_AGENT_POOL,
		LoadBalancing:    p2pstreamv1.PublicBackendLoadBalancing_PUBLIC_BACKEND_LOAD_BALANCING_ROUND_ROBIN,
		AgentAssignments: []*p2pstreamv1.PublicBackendAgent{assignment},
	})
	req.Header().Set("Cookie", cookie)
	resp, err := client.CreatePublicBackend(ctx, req)
	if err != nil {
		t.Fatalf("create agent backend: %v", err)
	}
	return resp.Msg.GetBackend()
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
		if _, err := client.SetupAdmin(ctx, connect.NewRequest(&p2pstreamv1.SetupAdminRequest{
			Username: smokeAdminUsername,
			Password: smokeAdminPassword,
		})); err != nil && connect.CodeOf(err) != connect.CodeFailedPrecondition {
			t.Fatalf("setup admin: %v", err)
		}
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

func upsertProxyBackend(
	ctx context.Context,
	t *testing.T,
	client p2pstreamv1connect.AgentManagementServiceClient,
	cookie string,
	backend *p2pstreamv1.PublicBackend,
	targetOrigin string,
) *p2pstreamv1.PublicBackend {
	t.Helper()

	req := connect.NewRequest(&p2pstreamv1.UpdatePublicBackendRequest{
		Id:           backend.GetId(),
		Name:         backend.GetName(),
		TargetOrigin: targetOrigin,
		Enabled:      true,
		BackendType:  p2pstreamv1.PublicBackendType_PUBLIC_BACKEND_TYPE_PROXY_FORWARD,
	})
	req.Header().Set("Cookie", cookie)
	resp, err := client.UpdatePublicBackend(ctx, req)
	if err != nil {
		t.Fatalf("update default proxy backend: %v", err)
	}
	return resp.Msg.GetBackend()
}

func upsertStaticBackend(
	ctx context.Context,
	t *testing.T,
	client p2pstreamv1connect.AgentManagementServiceClient,
	cookie string,
) *p2pstreamv1.PublicBackend {
	t.Helper()

	cfg := getPublicProxyConfig(ctx, t, client, cookie)
	if existing := findBackend(cfg, "docker-static"); existing != nil {
		req := connect.NewRequest(&p2pstreamv1.UpdatePublicBackendRequest{
			Id:               existing.GetId(),
			Name:             "docker-static",
			Enabled:          true,
			BackendType:      p2pstreamv1.PublicBackendType_PUBLIC_BACKEND_TYPE_STATIC,
			StaticStatusCode: http.StatusOK,
			StaticResponseHeaders: []*p2pstreamv1.PublicHeader{
				{Name: "Content-Type", Value: "text/plain"},
			},
			StaticResponseBody: "ok",
		})
		req.Header().Set("Cookie", cookie)
		resp, err := client.UpdatePublicBackend(ctx, req)
		if err != nil {
			t.Fatalf("update static backend: %v", err)
		}
		return resp.Msg.GetBackend()
	}

	req := connect.NewRequest(&p2pstreamv1.CreatePublicBackendRequest{
		Name:             "docker-static",
		Enabled:          true,
		BackendType:      p2pstreamv1.PublicBackendType_PUBLIC_BACKEND_TYPE_STATIC,
		StaticStatusCode: http.StatusOK,
		StaticResponseHeaders: []*p2pstreamv1.PublicHeader{
			{Name: "Content-Type", Value: "text/plain"},
		},
		StaticResponseBody: "ok",
	})
	req.Header().Set("Cookie", cookie)
	resp, err := client.CreatePublicBackend(ctx, req)
	if err != nil {
		t.Fatalf("create static backend: %v", err)
	}
	return resp.Msg.GetBackend()
}

func ensureDefaultListeners(
	ctx context.Context,
	t *testing.T,
	client p2pstreamv1connect.AgentManagementServiceClient,
	cookie string,
	cfg *p2pstreamv1.GetPublicProxyConfigResponse,
	defaultBackendID int64,
) {
	t.Helper()

	httpListener := requireListener(t, cfg, "public-http")
	if httpListener.GetPort() != 8080 || !httpListener.GetEnabled() || httpListener.GetDefaultBackendId() != defaultBackendID {
		updateListener(ctx, t, client, cookie, httpListener.GetId(), "public-http", "", 8080, p2pstreamv1.PublicListenerProtocol_PUBLIC_LISTENER_PROTOCOL_HTTP, true, defaultBackendID)
	}

	httpsListener := requireListener(t, cfg, "public-https")
	if httpsListener.GetPort() != 443 || !httpsListener.GetEnabled() || httpsListener.GetDefaultBackendId() != defaultBackendID {
		updateListener(ctx, t, client, cookie, httpsListener.GetId(), "public-https", "", 443, p2pstreamv1.PublicListenerProtocol_PUBLIC_LISTENER_PROTOCOL_HTTPS, true, defaultBackendID)
	}
}

func upsertStaticListener(
	ctx context.Context,
	t *testing.T,
	client p2pstreamv1connect.AgentManagementServiceClient,
	cookie string,
	backendID int64,
) *p2pstreamv1.PublicListener {
	t.Helper()

	cfg := getPublicProxyConfig(ctx, t, client, cookie)
	if existing := findListener(cfg, "docker-static"); existing != nil {
		return updateListener(ctx, t, client, cookie, existing.GetId(), "docker-static", "", 8088, p2pstreamv1.PublicListenerProtocol_PUBLIC_LISTENER_PROTOCOL_HTTP, true, backendID)
	}

	req := connect.NewRequest(&p2pstreamv1.CreatePublicListenerRequest{
		Name:             "docker-static",
		BindAddress:      "",
		Port:             8088,
		Protocol:         p2pstreamv1.PublicListenerProtocol_PUBLIC_LISTENER_PROTOCOL_HTTP,
		Enabled:          true,
		DefaultBackendId: backendID,
	})
	req.Header().Set("Cookie", cookie)
	resp, err := client.CreatePublicListener(ctx, req)
	if err != nil {
		t.Fatalf("create static listener: %v", err)
	}
	return resp.Msg.GetListener()
}

func upsertAgentListener(
	ctx context.Context,
	t *testing.T,
	client p2pstreamv1connect.AgentManagementServiceClient,
	cookie string,
	backendID int64,
) *p2pstreamv1.PublicListener {
	t.Helper()

	cfg := getPublicProxyConfig(ctx, t, client, cookie)
	if existing := findListener(cfg, "docker-agent"); existing != nil {
		return updateListener(ctx, t, client, cookie, existing.GetId(), "docker-agent", "", 8089, p2pstreamv1.PublicListenerProtocol_PUBLIC_LISTENER_PROTOCOL_HTTP, true, backendID)
	}

	req := connect.NewRequest(&p2pstreamv1.CreatePublicListenerRequest{
		Name:             "docker-agent",
		BindAddress:      "",
		Port:             8089,
		Protocol:         p2pstreamv1.PublicListenerProtocol_PUBLIC_LISTENER_PROTOCOL_HTTP,
		Enabled:          true,
		DefaultBackendId: backendID,
	})
	req.Header().Set("Cookie", cookie)
	resp, err := client.CreatePublicListener(ctx, req)
	if err != nil {
		t.Fatalf("create agent listener: %v", err)
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
	defaultBackendID int64,
) *p2pstreamv1.PublicListener {
	t.Helper()

	req := connect.NewRequest(&p2pstreamv1.UpdatePublicListenerRequest{
		Id:               id,
		Name:             name,
		BindAddress:      bindAddress,
		Port:             port,
		Protocol:         protocol,
		Enabled:          enabled,
		DefaultBackendId: defaultBackendID,
	})
	req.Header().Set("Cookie", cookie)
	resp, err := client.UpdatePublicListener(ctx, req)
	if err != nil {
		t.Fatalf("update listener %q: %v", name, err)
	}
	return resp.Msg.GetListener()
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

func requireBackend(t *testing.T, cfg *p2pstreamv1.GetPublicProxyConfigResponse, name string) *p2pstreamv1.PublicBackend {
	t.Helper()
	backend := findBackend(cfg, name)
	if backend == nil {
		t.Fatalf("backend %q not found in %+v", name, cfg.GetBackends())
	}
	return backend
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

func findBackend(cfg *p2pstreamv1.GetPublicProxyConfigResponse, name string) *p2pstreamv1.PublicBackend {
	for _, backend := range cfg.GetBackends() {
		if backend.GetName() == name {
			return backend
		}
	}
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
