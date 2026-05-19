package server

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strconv"
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
