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
	defaultBackend := requireBackend(t, cfg, "default")
	ensureDefaultListeners(ctx, t, client, cookie, cfg, defaultBackend.Id)
	waitHTTPBody(t, httpClient(), publicDefaultURL, http.StatusOK, "Welcome to p2pstream proxy", "seeded static welcome listener")

	defaultBackend = upsertProxyBackend(ctx, t, client, cookie, defaultBackend, upstreamURL)

	waitAgentConnected(ctx, t, client, cookie)
	waitHTTPBody(t, httpClient(), publicDefaultURL, http.StatusOK, "smoke upstream ok", "proxy-forward default listener")
	t.Run("direct baseline", func(t *testing.T) {
		waitHTTPBody(t, httpClient(), smokeURL(publicDefaultURL, "/"), http.StatusOK, "smoke upstream ok", "direct GET")
		smokePostEcho(t, smokeURL(publicDefaultURL, "/echo"))
		smokeStream(t, smokeURL(publicDefaultURL, "/stream"))
	})

	cfg = getPublicProxyConfig(ctx, t, client, cookie)
	dockerAgent := requireAgent(t, cfg, "docker-agent")
	agentBackend := upsertAgentBackend(ctx, t, client, cookie, upstreamURL, dockerAgent.Id, 60000)
	_ = upsertAgentListener(ctx, t, client, cookie, agentBackend.Id)
	waitHTTPBody(t, httpClient(), publicAgentURL, http.StatusOK, "smoke upstream ok", "agent-routed listener")
	t.Run("agent pool forwarding", func(t *testing.T) {
		waitHTTPBody(t, httpClient(), smokeURL(publicAgentURL, "/"), http.StatusOK, "smoke upstream ok", "agent GET")
		smokePostEcho(t, smokeURL(publicAgentURL, "/echo"))
		smokeStream(t, smokeURL(publicAgentURL, "/stream"))
		smokeHeaders(t, smokeURL(publicAgentURL, "/headers"), mustURLHost(t, upstreamURL), mustURLHost(t, publicAgentURL))
		smokeWebSocketEcho(t, smokeURL(publicAgentURL, "/ws"))

		agentBackend = upsertAgentBackend(ctx, t, client, cookie, upstreamURL, dockerAgent.Id, 1000)
		waitHTTPStatus(t, httpClient(), smokeURL(publicAgentURL, "/slow-headers"), func(resp *http.Response, body string) error {
			if resp.StatusCode != http.StatusGatewayTimeout {
				return fmt.Errorf("expected status 504, got %d with body %q", resp.StatusCode, body)
			}
			return nil
		}, "agent response-header timeout")

		agentBackend = upsertAgentBackend(ctx, t, client, cookie, upstreamURL, dockerAgent.Id, 60000)
		smokeCloseEarly(t, smokeURL(publicAgentURL, "/close-early"))
		waitHTTPBody(t, httpClient(), smokeURL(publicAgentURL, "/"), http.StatusOK, "smoke upstream ok", "agent listener after close-early")
	})

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
	responseHeaderTimeoutMillis int64,
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
			Id:                                  existing.GetId(),
			Name:                                "docker-agent-backend",
			TargetOrigin:                        targetOrigin,
			Enabled:                             true,
			BackendType:                         p2pstreamv1.PublicBackendType_PUBLIC_BACKEND_TYPE_PROXY_FORWARD,
			ForwardMode:                         p2pstreamv1.PublicBackendForwardMode_PUBLIC_BACKEND_FORWARD_MODE_AGENT_POOL,
			LoadBalancing:                       p2pstreamv1.PublicBackendLoadBalancing_PUBLIC_BACKEND_LOAD_BALANCING_ROUND_ROBIN,
			AgentAssignments:                    []*p2pstreamv1.PublicBackendAgent{assignment},
			UpstreamResponseHeaderTimeoutMillis: responseHeaderTimeoutMillis,
		})
		req.Header().Set("Cookie", cookie)
		resp, err := client.UpdatePublicBackend(ctx, req)
		if err != nil {
			t.Fatalf("update agent backend: %v", err)
		}
		return resp.Msg.GetBackend()
	}

	req := connect.NewRequest(&p2pstreamv1.CreatePublicBackendRequest{
		Name:                                "docker-agent-backend",
		TargetOrigin:                        targetOrigin,
		Enabled:                             true,
		BackendType:                         p2pstreamv1.PublicBackendType_PUBLIC_BACKEND_TYPE_PROXY_FORWARD,
		ForwardMode:                         p2pstreamv1.PublicBackendForwardMode_PUBLIC_BACKEND_FORWARD_MODE_AGENT_POOL,
		LoadBalancing:                       p2pstreamv1.PublicBackendLoadBalancing_PUBLIC_BACKEND_LOAD_BALANCING_ROUND_ROBIN,
		AgentAssignments:                    []*p2pstreamv1.PublicBackendAgent{assignment},
		UpstreamResponseHeaderTimeoutMillis: responseHeaderTimeoutMillis,
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
			Username:   smokeAdminUsername,
			Password:   smokeAdminPassword,
			SetupToken: envOrDefault("MANAGEMENT_SETUP_TOKEN", ""),
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
	if u.Scheme != "http" {
		t.Fatalf("smoke websocket only supports ws over http, got %q", u.Scheme)
	}
	address := u.Host
	if !strings.Contains(address, ":") {
		address += ":80"
	}
	conn, err := net.DialTimeout("tcp", address, 5*time.Second)
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
