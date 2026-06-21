package server

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/gen/proto/p2pstream/v1/p2pstreamv1connect"
	"p2pstream/internal/config"
	"p2pstream/internal/db"
)

func TestManagementAccessTokenAuthSupportsExpiry(t *testing.T) {
	app := NewApp(&config.Config{}, newServerTestDB(t))
	adminHeader := createTestAdminSession(t, app)
	ctx := context.Background()

	createReq := connect.NewRequest(&p2pstreamv1.CreateManagementAccessTokenRequest{
		Name:    "remote-admin",
		Enabled: true,
	})
	createReq.Header().Set("Cookie", adminHeader.Get("Cookie"))
	createResp, err := app.CreateManagementAccessToken(ctx, createReq)
	if err != nil {
		t.Fatalf("create access token: %v", err)
	}
	tokenHeader := http.Header{}
	tokenHeader.Set("Authorization", "Bearer "+createResp.Msg.Token)
	tokenUser, err := app.requireAdmin(ctx, tokenHeader)
	if err != nil {
		t.Fatalf("access token did not authenticate admin: %v", err)
	}
	if tokenUser.ID != 0 {
		t.Fatalf("access token user id = %d, want 0", tokenUser.ID)
	}
	if !tokenUser.IsAccessToken {
		t.Fatal("expected token-authenticated user to be marked as access token")
	}
	if tokenUser.Username != "remote-admin" {
		t.Fatalf("token username = %q, want remote-admin", tokenUser.Username)
	}
	currentTokenReq := connect.NewRequest(&p2pstreamv1.GetCurrentUserRequest{})
	currentTokenReq.Header().Set("Authorization", "Bearer "+createResp.Msg.Token)
	currentTokenResp, err := app.GetCurrentUser(ctx, currentTokenReq)
	if err != nil {
		t.Fatalf("get current user with access token: %v", err)
	}
	if currentTokenResp.Msg.User.Id != 0 {
		t.Fatalf("token current user id = %d, want 0", currentTokenResp.Msg.User.Id)
	}
	if currentTokenResp.Msg.User.Username != "remote-admin" {
		t.Fatalf("token current username = %q, want remote-admin", currentTokenResp.Msg.User.Username)
	}
	if currentTokenResp.Msg.User.Role != p2pstreamv1.UserRole_USER_ROLE_ADMIN {
		t.Fatalf("token current role = %s, want admin", currentTokenResp.Msg.User.Role)
	}
	currentSessionReq := connect.NewRequest(&p2pstreamv1.GetCurrentUserRequest{})
	currentSessionReq.Header().Set("Cookie", adminHeader.Get("Cookie"))
	currentSessionResp, err := app.GetCurrentUser(ctx, currentSessionReq)
	if err != nil {
		t.Fatalf("get current user with session: %v", err)
	}
	if currentSessionResp.Msg.User.Id == 0 {
		t.Fatal("session current user id = 0, want real user id")
	}
	if currentSessionResp.Msg.User.Username != "admin" {
		t.Fatalf("session current username = %q, want admin", currentSessionResp.Msg.User.Username)
	}
	row, err := app.DB.GetActiveManagementAccessTokenByHash(ctx, hashManagementAccessToken(createResp.Msg.Token))
	if err != nil {
		t.Fatalf("reload access token: %v", err)
	}
	if !row.LastUsedAt.Valid {
		t.Fatal("expected last_used_at to be updated")
	}
	if _, err := app.DB.ExecContext(ctx, "UPDATE management_access_tokens SET last_used_at = datetime('now', '-10 seconds') WHERE id = ?", row.ID); err != nil {
		t.Fatalf("seed recent last_used_at: %v", err)
	}
	recentRow, err := app.DB.GetActiveManagementAccessTokenByHash(ctx, hashManagementAccessToken(createResp.Msg.Token))
	if err != nil {
		t.Fatalf("reload recent access token: %v", err)
	}
	if _, err := app.requireAdmin(ctx, tokenHeader); err != nil {
		t.Fatalf("recent access token did not authenticate admin: %v", err)
	}
	untouchedRow, err := app.DB.GetActiveManagementAccessTokenByHash(ctx, hashManagementAccessToken(createResp.Msg.Token))
	if err != nil {
		t.Fatalf("reload untouched access token: %v", err)
	}
	if !untouchedRow.LastUsedAt.Time.Equal(recentRow.LastUsedAt.Time) {
		t.Fatalf("recent last_used_at changed from %s to %s", recentRow.LastUsedAt.Time, untouchedRow.LastUsedAt.Time)
	}

	deleteReq := connect.NewRequest(&p2pstreamv1.DeleteManagementAccessTokenRequest{Id: createResp.Msg.AccessToken.Id})
	deleteReq.Header().Set("Cookie", adminHeader.Get("Cookie"))
	if _, err := app.DeleteManagementAccessToken(ctx, deleteReq); err != nil {
		t.Fatalf("delete access token: %v", err)
	}
	if _, err := app.requireAdmin(ctx, tokenHeader); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("deleted token error code = %s, want unauthenticated: %v", connect.CodeOf(err), err)
	}

	disabledReq := connect.NewRequest(&p2pstreamv1.CreateManagementAccessTokenRequest{
		Name:    "disabled-token",
		Enabled: false,
	})
	disabledReq.Header().Set("Cookie", adminHeader.Get("Cookie"))
	disabledResp, err := app.CreateManagementAccessToken(ctx, disabledReq)
	if err != nil {
		t.Fatalf("create disabled access token: %v", err)
	}
	disabledHeader := http.Header{}
	disabledHeader.Set("Authorization", "Bearer "+disabledResp.Msg.Token)
	if _, err := app.requireAdmin(ctx, disabledHeader); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("disabled token error code = %s, want unauthenticated: %v", connect.CodeOf(err), err)
	}

	pastReq := connect.NewRequest(&p2pstreamv1.CreateManagementAccessTokenRequest{
		Name:                "expired-on-create",
		Enabled:             true,
		ExpiresAtUnixMillis: time.Now().Add(-time.Minute).UnixMilli(),
	})
	pastReq.Header().Set("Cookie", adminHeader.Get("Cookie"))
	if _, err := app.CreateManagementAccessToken(ctx, pastReq); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("past expiry error code = %s, want invalid_argument: %v", connect.CodeOf(err), err)
	}

	expiredToken, expiredHash, err := newManagementAccessToken()
	if err != nil {
		t.Fatalf("generate expired token: %v", err)
	}
	if _, err := app.DB.CreateManagementAccessToken(ctx, db.CreateManagementAccessTokenParams{
		Name:      "expired",
		TokenHash: expiredHash,
		Enabled:   1,
		ExpiresAt: sql.NullTime{Time: time.Now().Add(-time.Minute), Valid: true},
	}); err != nil {
		t.Fatalf("seed expired token: %v", err)
	}
	expiredHeader := http.Header{}
	expiredHeader.Set("Authorization", "Bearer "+expiredToken)
	if _, err := app.requireAdmin(ctx, expiredHeader); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("expired token error code = %s, want unauthenticated: %v", connect.CodeOf(err), err)
	}

	agent, err := app.DB.CreateAgent(ctx, db.CreateAgentParams{
		PublicID:  "agent-token-separation",
		Name:      "Agent Token Separation",
		TokenHash: hashAgentToken("real-agent-token"),
		Enabled:   1,
	})
	if err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	if _, err := app.authenticateAgent(ctx, agent.PublicID, "Bearer "+disabledResp.Msg.Token); err == nil {
		t.Fatal("management access token authenticated as an agent token")
	}
}

func TestEnvironmentCRUDValidation(t *testing.T) {
	app := NewApp(&config.Config{}, newServerTestDB(t))
	adminHeader := createTestAdminSession(t, app)
	ctx := context.Background()
	agent, err := app.DB.CreateAgent(ctx, db.CreateAgentParams{
		PublicID:  "agent-env-validation",
		Name:      "Agent Env Validation",
		TokenHash: hashAgentToken("agent-token"),
		Enabled:   1,
	})
	if err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	create := func(msg *p2pstreamv1.CreateEnvironmentRequest) error {
		req := connect.NewRequest(msg)
		req.Header().Set("Cookie", adminHeader.Get("Cookie"))
		_, err := app.CreateEnvironment(ctx, req)
		return err
	}
	base := func(name string) *p2pstreamv1.CreateEnvironmentRequest {
		return &p2pstreamv1.CreateEnvironmentRequest{
			Name:                        name,
			ManagementUrl:               "https://remote.example.test:8081/",
			Transport:                   p2pstreamv1.EnvironmentTransport_ENVIRONMENT_TRANSPORT_DIRECT,
			AccessToken:                 "p2pat_test-token-material",
			ResponseHeaderTimeoutMillis: 10000,
			Enabled:                     true,
		}
	}

	if err := create(base("valid-env")); err != nil {
		t.Fatalf("valid environment failed: %v", err)
	}
	if err := create(base("valid-env")); connect.CodeOf(err) != connect.CodeAlreadyExists {
		t.Fatalf("duplicate environment error code = %s, want already_exists: %v", connect.CodeOf(err), err)
	}
	missingToken := base("missing-token")
	missingToken.AccessToken = ""
	if err := create(missingToken); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("missing token error code = %s, want invalid_argument: %v", connect.CodeOf(err), err)
	}
	badTimeout := base("bad-timeout")
	badTimeout.ResponseHeaderTimeoutMillis = 999
	if err := create(badTimeout); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("bad timeout error code = %s, want invalid_argument: %v", connect.CodeOf(err), err)
	}
	directWithAgent := base("direct-with-agent")
	directWithAgent.AgentId = agent.ID
	if err := create(directWithAgent); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("direct with agent error code = %s, want invalid_argument: %v", connect.CodeOf(err), err)
	}
	agentWithoutID := base("agent-without-id")
	agentWithoutID.Transport = p2pstreamv1.EnvironmentTransport_ENVIRONMENT_TRANSPORT_AGENT
	if err := create(agentWithoutID); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("agent without id error code = %s, want invalid_argument: %v", connect.CodeOf(err), err)
	}
	agentEnv := base("agent-env")
	agentEnv.Transport = p2pstreamv1.EnvironmentTransport_ENVIRONMENT_TRANSPORT_AGENT
	agentEnv.AgentId = agent.ID
	if err := create(agentEnv); err != nil {
		t.Fatalf("agent environment failed: %v", err)
	}
}

func TestDiscoverEnvironmentCertificateFailurePersistsLastError(t *testing.T) {
	ctx := context.Background()
	app := NewApp(&config.Config{}, newServerTestDB(t))
	adminHeader := createTestAdminSession(t, app)
	plainServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer plainServer.Close()

	createReq := connect.NewRequest(&p2pstreamv1.CreateEnvironmentRequest{
		Name:                        "plain-http-on-https-port",
		ManagementUrl:               "https://" + plainServer.Listener.Addr().String(),
		Transport:                   p2pstreamv1.EnvironmentTransport_ENVIRONMENT_TRANSPORT_DIRECT,
		AccessToken:                 "p2pat_test-token-material",
		ResponseHeaderTimeoutMillis: 10000,
		Enabled:                     true,
	})
	createReq.Header().Set("Cookie", adminHeader.Get("Cookie"))
	createResp, err := app.CreateEnvironment(ctx, createReq)
	if err != nil {
		t.Fatalf("create environment: %v", err)
	}

	discoverReq := connect.NewRequest(&p2pstreamv1.DiscoverEnvironmentCertificateRequest{Id: createResp.Msg.Environment.Id})
	discoverReq.Header().Set("Cookie", adminHeader.Get("Cookie"))
	if resp, err := app.DiscoverEnvironmentCertificate(ctx, discoverReq); connect.CodeOf(err) != connect.CodeUnavailable {
		t.Fatalf("discover response/error = %v/%v, want unavailable error", resp, err)
	} else if resp != nil {
		t.Fatalf("discover response = %+v, want nil response with error", resp.Msg)
	}
	row, err := app.DB.GetEnvironment(ctx, createResp.Msg.Environment.Id)
	if err != nil {
		t.Fatalf("reload environment: %v", err)
	}
	if row.LastError == "" {
		t.Fatal("expected failed discovery to persist last_error")
	}
	if !row.LastCheckedAt.Valid {
		t.Fatal("expected failed discovery to update last_checked_at")
	}
}

func TestEnvironmentRequiresHTTPSAndTrustedCertificateBeforeProxy(t *testing.T) {
	ctx := context.Background()
	remoteApp := NewApp(&config.Config{}, newServerTestDB(t))
	remoteToken, remoteTokenHash, err := newManagementAccessToken()
	if err != nil {
		t.Fatalf("remote token: %v", err)
	}
	if _, err := remoteApp.DB.CreateManagementAccessToken(ctx, db.CreateManagementAccessTokenParams{
		Name:      "control-plane",
		TokenHash: remoteTokenHash,
		Enabled:   1,
		ExpiresAt: sql.NullTime{},
	}); err != nil {
		t.Fatalf("seed remote token: %v", err)
	}
	remoteMux := http.NewServeMux()
	remoteApp.RegisterManagementRoutes(remoteMux)
	remoteServer := httptest.NewTLSServer(remoteMux)
	defer remoteServer.Close()

	localApp := NewApp(&config.Config{}, newServerTestDB(t))
	localHeader := createTestAdminSession(t, localApp)
	badCreate := connect.NewRequest(&p2pstreamv1.CreateEnvironmentRequest{
		Name:                        "bad",
		ManagementUrl:               "http://127.0.0.1:8081",
		Transport:                   p2pstreamv1.EnvironmentTransport_ENVIRONMENT_TRANSPORT_DIRECT,
		AccessToken:                 remoteToken,
		ResponseHeaderTimeoutMillis: 10000,
		Enabled:                     true,
	})
	badCreate.Header().Set("Cookie", localHeader.Get("Cookie"))
	if _, err := localApp.CreateEnvironment(ctx, badCreate); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("http environment error code = %s, want invalid_argument: %v", connect.CodeOf(err), err)
	}

	createReq := connect.NewRequest(&p2pstreamv1.CreateEnvironmentRequest{
		Name:                        "remote",
		ManagementUrl:               remoteServer.URL,
		Transport:                   p2pstreamv1.EnvironmentTransport_ENVIRONMENT_TRANSPORT_DIRECT,
		AccessToken:                 remoteToken,
		ResponseHeaderTimeoutMillis: 10000,
		Enabled:                     true,
	})
	createReq.Header().Set("Cookie", localHeader.Get("Cookie"))
	createResp, err := localApp.CreateEnvironment(ctx, createReq)
	if err != nil {
		t.Fatalf("create environment: %v", err)
	}
	if createResp.Msg.Environment.TrustState != p2pstreamv1.EnvironmentTrustState_ENVIRONMENT_TRUST_STATE_UNTRUSTED {
		t.Fatalf("trust state = %s, want untrusted", createResp.Msg.Environment.TrustState)
	}

	localMux := http.NewServeMux()
	localApp.RegisterManagementRoutes(localMux)
	localServer := httptest.NewServer(localMux)
	defer localServer.Close()
	proxyClient := p2pstreamv1connect.NewAgentManagementServiceClient(
		localServer.Client(),
		localServer.URL+"/environments/"+strconv.FormatInt(createResp.Msg.Environment.Id, 10),
	)
	untrustedReq := connect.NewRequest(&p2pstreamv1.GetStatusRequest{})
	untrustedReq.Header().Set("Cookie", localHeader.Get("Cookie"))
	if _, err := proxyClient.GetStatus(ctx, untrustedReq); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("untrusted proxy error code = %s, want failed_precondition: %v", connect.CodeOf(err), err)
	}

	discoverReq := connect.NewRequest(&p2pstreamv1.DiscoverEnvironmentCertificateRequest{Id: createResp.Msg.Environment.Id})
	discoverReq.Header().Set("Cookie", localHeader.Get("Cookie"))
	discoverResp, err := localApp.DiscoverEnvironmentCertificate(ctx, discoverReq)
	if err != nil {
		t.Fatalf("discover certificate: %v", err)
	}
	if discoverResp.Msg.Certificate == nil || discoverResp.Msg.Certificate.Sha256Fingerprint == "" {
		t.Fatalf("missing discovered certificate: %+v", discoverResp.Msg.Certificate)
	}
	trustReq := connect.NewRequest(&p2pstreamv1.TrustEnvironmentCertificateRequest{
		Id:                createResp.Msg.Environment.Id,
		Sha256Fingerprint: discoverResp.Msg.Certificate.Sha256Fingerprint,
	})
	trustReq.Header().Set("Cookie", localHeader.Get("Cookie"))
	trustResp, err := localApp.TrustEnvironmentCertificate(ctx, trustReq)
	if err != nil {
		t.Fatalf("trust certificate: %v", err)
	}
	if trustResp.Msg.Environment.TrustState != p2pstreamv1.EnvironmentTrustState_ENVIRONMENT_TRUST_STATE_TRUSTED {
		t.Fatalf("trust state = %s, want trusted", trustResp.Msg.Environment.TrustState)
	}

	trustedReq := connect.NewRequest(&p2pstreamv1.GetStatusRequest{})
	trustedReq.Header().Set("Cookie", localHeader.Get("Cookie"))
	if _, err := proxyClient.GetStatus(ctx, trustedReq); err != nil {
		t.Fatalf("trusted environment proxy GetStatus: %v", err)
	}
	publicConfigReq := connect.NewRequest(&p2pstreamv1.GetPublicProxyConfigRequest{})
	publicConfigReq.Header().Set("Cookie", localHeader.Get("Cookie"))
	if _, err := proxyClient.GetPublicProxyConfig(ctx, publicConfigReq); err != nil {
		t.Fatalf("trusted environment proxy GetPublicProxyConfig: %v", err)
	}

	_, _, changedCert, err := generatePublicSelfSignedCertificatePEM("127.0.0.1", time.Hour)
	if err != nil {
		t.Fatalf("generate changed certificate: %v", err)
	}
	if _, err := localApp.DB.UpdateEnvironmentObservedCertificate(ctx, db.UpdateEnvironmentObservedCertificateParams{
		LastObservedCertificatePem:    environmentCertificatePEM(changedCert),
		LastObservedCertificateSha256: certificateSHA256Fingerprint(changedCert),
		LastError:                     "",
		ID:                            createResp.Msg.Environment.Id,
	}); err != nil {
		t.Fatalf("seed changed observed certificate: %v", err)
	}
	changedReq := connect.NewRequest(&p2pstreamv1.GetStatusRequest{})
	changedReq.Header().Set("Cookie", localHeader.Get("Cookie"))
	if _, err := proxyClient.GetStatus(ctx, changedReq); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("changed certificate proxy error code = %s, want failed_precondition: %v", connect.CodeOf(err), err)
	}

	setupReq := connect.NewRequest(&p2pstreamv1.GetSetupStateRequest{})
	setupReq.Header().Set("Cookie", localHeader.Get("Cookie"))
	if _, err := proxyClient.GetSetupState(ctx, setupReq); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("disallowed proxy method error code = %s, want permission_denied: %v", connect.CodeOf(err), err)
	}
}

func TestEnvironmentProxyDoesNotFollowRedirectOrLeakToken(t *testing.T) {
	tests := []struct {
		name     string
		location func(remoteURL string, attackerURL string) string
	}{
		{
			name: "cross-origin-http",
			location: func(_ string, attackerURL string) string {
				return attackerURL + "/capture"
			},
		},
		{
			name: "same-origin",
			location: func(remoteURL string, _ string) string {
				return remoteURL + "/redirected"
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			remoteToken, _, err := newManagementAccessToken()
			if err != nil {
				t.Fatalf("remote token: %v", err)
			}
			var attackerHits atomic.Int32
			attackerAuth := make(chan string, 4)
			attackerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attackerHits.Add(1)
				attackerAuth <- r.Header.Get("Authorization")
				w.WriteHeader(http.StatusNoContent)
			}))
			defer attackerServer.Close()

			var remoteHits atomic.Int32
			remoteAuth := make(chan string, 4)
			var remoteServer *httptest.Server
			remoteServer = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				remoteHits.Add(1)
				remoteAuth <- r.Header.Get("Authorization")
				http.Redirect(w, r, tc.location(remoteServer.URL, attackerServer.URL), http.StatusFound)
			}))
			defer remoteServer.Close()

			localApp := NewApp(&config.Config{}, newServerTestDB(t))
			localHeader := createTestAdminSession(t, localApp)
			createReq := connect.NewRequest(&p2pstreamv1.CreateEnvironmentRequest{
				Name:                        "redirect-env",
				ManagementUrl:               remoteServer.URL,
				Transport:                   p2pstreamv1.EnvironmentTransport_ENVIRONMENT_TRANSPORT_DIRECT,
				AccessToken:                 remoteToken,
				ResponseHeaderTimeoutMillis: 10000,
				Enabled:                     true,
			})
			createReq.Header().Set("Cookie", localHeader.Get("Cookie"))
			createResp, err := localApp.CreateEnvironment(ctx, createReq)
			if err != nil {
				t.Fatalf("create environment: %v", err)
			}

			discoverReq := connect.NewRequest(&p2pstreamv1.DiscoverEnvironmentCertificateRequest{Id: createResp.Msg.Environment.Id})
			discoverReq.Header().Set("Cookie", localHeader.Get("Cookie"))
			discoverResp, err := localApp.DiscoverEnvironmentCertificate(ctx, discoverReq)
			if err != nil {
				t.Fatalf("discover certificate: %v", err)
			}
			trustReq := connect.NewRequest(&p2pstreamv1.TrustEnvironmentCertificateRequest{
				Id:                createResp.Msg.Environment.Id,
				Sha256Fingerprint: discoverResp.Msg.Certificate.Sha256Fingerprint,
			})
			trustReq.Header().Set("Cookie", localHeader.Get("Cookie"))
			if _, err := localApp.TrustEnvironmentCertificate(ctx, trustReq); err != nil {
				t.Fatalf("trust certificate: %v", err)
			}

			localMux := http.NewServeMux()
			localApp.RegisterManagementRoutes(localMux)
			localServer := httptest.NewServer(localMux)
			defer localServer.Close()
			client := localServer.Client()
			client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			}
			proxyURL := localServer.URL + "/environments/" + strconv.FormatInt(createResp.Msg.Environment.Id, 10) + "/p2pstream.v1.AgentManagementService/GetStatus"
			proxyReq, err := http.NewRequestWithContext(ctx, http.MethodPost, proxyURL, strings.NewReader("{}"))
			if err != nil {
				t.Fatalf("build proxy request: %v", err)
			}
			proxyReq.Header.Set("Cookie", localHeader.Get("Cookie"))
			proxyReq.Header.Set("Content-Type", "application/json")
			proxyReq.Header.Set("Connect-Protocol-Version", "1")

			resp, err := client.Do(proxyReq)
			if err != nil {
				t.Fatalf("environment proxy request: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusFound {
				t.Fatalf("proxy status = %d, want %d", resp.StatusCode, http.StatusFound)
			}
			if got, want := resp.Header.Get("Location"), tc.location(remoteServer.URL, attackerServer.URL); got != want {
				t.Fatalf("Location = %q, want %q", got, want)
			}
			if got := remoteHits.Load(); got != 1 {
				t.Fatalf("remote hits = %d, want 1", got)
			}
			select {
			case got := <-remoteAuth:
				if want := "Bearer " + remoteToken; got != want {
					t.Fatalf("remote Authorization = %q, want %q", got, want)
				}
			default:
				t.Fatal("remote did not record Authorization")
			}
			if got := attackerHits.Load(); got != 0 {
				select {
				case leaked := <-attackerAuth:
					t.Fatalf("attacker received %d request(s), Authorization %q", got, leaked)
				default:
					t.Fatalf("attacker received %d request(s)", got)
				}
			}
		})
	}
}

type captureEnvironmentRoundTripper struct {
	req *http.Request
}

func (rt *captureEnvironmentRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	rt.req = req
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       http.NoBody,
		Request:    req,
	}, nil
}

func TestEnvironmentAuthRoundTripperBindsAuthorizationToOrigin(t *testing.T) {
	tests := []struct {
		name     string
		target   string
		wantAuth string
	}{
		{name: "matching origin", target: "https://env.example:8443/rpc", wantAuth: "Bearer p2pat_secret"},
		{name: "matching origin case insensitive", target: "https://ENV.example:8443/rpc", wantAuth: "Bearer p2pat_secret"},
		{name: "wrong scheme", target: "http://env.example:8443/rpc", wantAuth: ""},
		{name: "wrong host", target: "https://other.example:8443/rpc", wantAuth: ""},
		{name: "wrong port", target: "https://env.example/rpc", wantAuth: ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			capture := &captureEnvironmentRoundTripper{}
			rt := environmentAuthRoundTripper{
				token:  "p2pat_secret",
				scheme: "https",
				host:   "env.example:8443",
				next:   capture,
			}
			req, err := http.NewRequest(http.MethodPost, tc.target, nil)
			if err != nil {
				t.Fatalf("build request: %v", err)
			}
			req.Header.Set("Cookie", "sid=caller")
			req.Header.Set("Authorization", "Bearer caller-supplied")
			resp, err := rt.RoundTrip(req)
			if err != nil {
				t.Fatalf("round trip: %v", err)
			}
			if resp.Body != nil {
				_ = resp.Body.Close()
			}
			if capture.req == nil {
				t.Fatal("round tripper did not receive request")
			}
			if got := capture.req.Header.Get("Authorization"); got != tc.wantAuth {
				t.Fatalf("Authorization = %q, want %q", got, tc.wantAuth)
			}
			if got := capture.req.Header.Get("Cookie"); got != "" {
				t.Fatalf("Cookie = %q, want stripped", got)
			}
		})
	}
}

func TestAgentEnvironmentProxyDiscoversAndPinsCertificate(t *testing.T) {
	ctx := context.Background()
	remoteApp := NewApp(&config.Config{}, newServerTestDB(t))
	remoteToken, remoteTokenHash, err := newManagementAccessToken()
	if err != nil {
		t.Fatalf("remote token: %v", err)
	}
	if _, err := remoteApp.DB.CreateManagementAccessToken(ctx, db.CreateManagementAccessTokenParams{
		Name:      "agent-control-plane",
		TokenHash: remoteTokenHash,
		Enabled:   1,
		ExpiresAt: sql.NullTime{},
	}); err != nil {
		t.Fatalf("seed remote token: %v", err)
	}
	remoteMux := http.NewServeMux()
	remoteApp.RegisterManagementRoutes(remoteMux)
	remoteServer := httptest.NewTLSServer(remoteMux)
	defer remoteServer.Close()

	localApp := NewApp(&config.Config{}, newServerTestDB(t))
	localHeader := createTestAdminSession(t, localApp)
	agentRow, err := localApp.DB.CreateAgent(ctx, db.CreateAgentParams{
		PublicID:  "agent-env-proxy",
		Name:      "Agent Env Proxy",
		TokenHash: hashAgentToken("agent-token"),
		Enabled:   1,
	})
	if err != nil {
		t.Fatalf("seed local agent: %v", err)
	}
	agentConn, fake := newFakeYamuxAgent(t, agentRow.ID, agentRow.PublicID)
	if err := localApp.AgentHub.connect(agentConn); err != nil {
		t.Fatalf("connect local agent: %v", err)
	}
	t.Cleanup(func() {
		localApp.AgentHub.disconnect(agentConn)
		fake.close()
	})

	createReq := connect.NewRequest(&p2pstreamv1.CreateEnvironmentRequest{
		Name:                        "agent-remote",
		ManagementUrl:               remoteServer.URL,
		Transport:                   p2pstreamv1.EnvironmentTransport_ENVIRONMENT_TRANSPORT_AGENT,
		AgentId:                     agentRow.ID,
		AccessToken:                 remoteToken,
		ResponseHeaderTimeoutMillis: 10000,
		Enabled:                     true,
	})
	createReq.Header().Set("Cookie", localHeader.Get("Cookie"))
	createResp, err := localApp.CreateEnvironment(ctx, createReq)
	if err != nil {
		t.Fatalf("create agent environment: %v", err)
	}

	discoverReq := connect.NewRequest(&p2pstreamv1.DiscoverEnvironmentCertificateRequest{Id: createResp.Msg.Environment.Id})
	discoverReq.Header().Set("Cookie", localHeader.Get("Cookie"))
	discoverResp, err := localApp.DiscoverEnvironmentCertificate(ctx, discoverReq)
	if err != nil {
		t.Fatalf("discover agent environment certificate: %v", err)
	}
	if discoverResp.Msg.Certificate == nil || discoverResp.Msg.Certificate.Pem == "" || discoverResp.Msg.Certificate.Sha256Fingerprint == "" {
		t.Fatalf("missing discovered agent certificate: %+v", discoverResp.Msg.Certificate)
	}

	trustReq := connect.NewRequest(&p2pstreamv1.TrustEnvironmentCertificateRequest{
		Id:                createResp.Msg.Environment.Id,
		Sha256Fingerprint: discoverResp.Msg.Certificate.Sha256Fingerprint,
	})
	trustReq.Header().Set("Cookie", localHeader.Get("Cookie"))
	if _, err := localApp.TrustEnvironmentCertificate(ctx, trustReq); err != nil {
		t.Fatalf("trust agent environment certificate: %v", err)
	}

	localMux := http.NewServeMux()
	localApp.RegisterManagementRoutes(localMux)
	localServer := httptest.NewServer(localMux)
	defer localServer.Close()
	proxyClient := p2pstreamv1connect.NewAgentManagementServiceClient(
		localServer.Client(),
		localServer.URL+"/environments/"+strconv.FormatInt(createResp.Msg.Environment.Id, 10),
	)
	proxyOpenCount := fake.openRequestCount()
	statusReq := connect.NewRequest(&p2pstreamv1.GetStatusRequest{})
	statusReq.Header().Set("Cookie", localHeader.Get("Cookie"))
	if _, err := proxyClient.GetStatus(ctx, statusReq); err != nil {
		t.Fatalf("trusted agent environment proxy GetStatus: %v", err)
	}
	publicConfigReq := connect.NewRequest(&p2pstreamv1.GetPublicProxyConfigRequest{})
	publicConfigReq.Header().Set("Cookie", localHeader.Get("Cookie"))
	if _, err := proxyClient.GetPublicProxyConfig(ctx, publicConfigReq); err != nil {
		t.Fatalf("trusted agent environment proxy GetPublicProxyConfig: %v", err)
	}
	fake.waitOpenRequestCount(t, proxyOpenCount+1)
	if got := localApp.AgentTransports.len(); got != 1 {
		t.Fatalf("environment agent transport pool len = %d, want 1", got)
	}

	retrustReq := connect.NewRequest(&p2pstreamv1.TrustEnvironmentCertificateRequest{
		Id:                createResp.Msg.Environment.Id,
		Sha256Fingerprint: discoverResp.Msg.Certificate.Sha256Fingerprint,
	})
	retrustReq.Header().Set("Cookie", localHeader.Get("Cookie"))
	if _, err := localApp.TrustEnvironmentCertificate(ctx, retrustReq); err != nil {
		t.Fatalf("retrust agent environment certificate: %v", err)
	}
	if got := localApp.AgentTransports.len(); got != 0 {
		t.Fatalf("environment agent transport pool len after trust = %d, want 0", got)
	}

	_, _, changedCert, err := generatePublicSelfSignedCertificatePEM("127.0.0.1", time.Hour)
	if err != nil {
		t.Fatalf("generate changed certificate: %v", err)
	}
	if _, err := localApp.DB.UpdateEnvironmentObservedCertificate(ctx, db.UpdateEnvironmentObservedCertificateParams{
		LastObservedCertificatePem:    environmentCertificatePEM(changedCert),
		LastObservedCertificateSha256: certificateSHA256Fingerprint(changedCert),
		LastError:                     "",
		ID:                            createResp.Msg.Environment.Id,
	}); err != nil {
		t.Fatalf("seed changed observed certificate: %v", err)
	}
	changedReq := connect.NewRequest(&p2pstreamv1.GetStatusRequest{})
	changedReq.Header().Set("Cookie", localHeader.Get("Cookie"))
	if _, err := proxyClient.GetStatus(ctx, changedReq); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("changed agent certificate proxy error code = %s, want failed_precondition: %v", connect.CodeOf(err), err)
	}

	localApp.AgentHub.disconnect(agentConn)
	fake.close()
	disconnectedReq := connect.NewRequest(&p2pstreamv1.DiscoverEnvironmentCertificateRequest{Id: createResp.Msg.Environment.Id})
	disconnectedReq.Header().Set("Cookie", localHeader.Get("Cookie"))
	if _, err := localApp.DiscoverEnvironmentCertificate(ctx, disconnectedReq); connect.CodeOf(err) != connect.CodeUnavailable {
		t.Fatalf("disconnected agent discovery error code = %s, want unavailable: %v", connect.CodeOf(err), err)
	}
}
