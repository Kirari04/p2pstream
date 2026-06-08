package integration_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/internal/config"
	"p2pstream/internal/server"
)

func TestSetupStateWithinWindow(t *testing.T) {
	app := server.NewApp(testManagementConfig(config.Config{}), newTestDB(t))
	_, client := newTestManagementClient(t, app)

	resp, err := client.GetSetupState(context.Background(), connect.NewRequest(&p2pstreamv1.GetSetupStateRequest{}))
	if err != nil {
		t.Fatalf("get setup state: %v", err)
	}

	if !resp.Msg.SetupRequired {
		t.Fatal("expected setup to be required")
	}
	if !resp.Msg.SetupAvailable {
		t.Fatal("expected setup to be available")
	}
	if resp.Msg.SetupExpiresAtUnixMillis == 0 {
		t.Fatal("expected setup expiration timestamp")
	}
	if resp.Msg.SetupUnavailableReason != "" {
		t.Fatalf("expected no unavailable reason, got %q", resp.Msg.SetupUnavailableReason)
	}
}

func TestSetupStateExpiredWindow(t *testing.T) {
	app := server.NewApp(testManagementConfig(config.Config{}), newTestDB(t))
	app.StartedAt = time.Now().Add(-6 * time.Minute)
	_, client := newTestManagementClient(t, app)

	resp, err := client.GetSetupState(context.Background(), connect.NewRequest(&p2pstreamv1.GetSetupStateRequest{}))
	if err != nil {
		t.Fatalf("get setup state: %v", err)
	}

	if !resp.Msg.SetupRequired {
		t.Fatal("expected setup to be required")
	}
	if resp.Msg.SetupAvailable {
		t.Fatal("expected setup to be unavailable")
	}
	if resp.Msg.SetupUnavailableReason != "setup window expired; restart the server to retry setup" {
		t.Fatalf("unexpected unavailable reason: %q", resp.Msg.SetupUnavailableReason)
	}
}

func TestSetupAdminRejectsExpiredWindow(t *testing.T) {
	app := server.NewApp(testManagementConfig(config.Config{}), newTestDB(t))
	app.StartedAt = time.Now().Add(-6 * time.Minute)
	_, client := newTestManagementClient(t, app)

	_, err := client.SetupAdmin(context.Background(), connect.NewRequest(&p2pstreamv1.SetupAdminRequest{
		Username:   testAdminUsername,
		Password:   testAdminPassword,
		SetupToken: testSetupToken,
	}))
	requireConnectCode(t, err, connect.CodeFailedPrecondition)
}

func TestSetupAdminRequiresSetupToken(t *testing.T) {
	app := server.NewApp(testManagementConfig(config.Config{}), newTestDB(t))
	_, client := newTestManagementClient(t, app)

	ctx := context.Background()
	_, err := client.SetupAdmin(ctx, connect.NewRequest(&p2pstreamv1.SetupAdminRequest{
		Username: testAdminUsername,
		Password: testAdminPassword,
	}))
	requireConnectCode(t, err, connect.CodeUnauthenticated)

	_, err = client.SetupAdmin(ctx, connect.NewRequest(&p2pstreamv1.SetupAdminRequest{
		Username:   testAdminUsername,
		Password:   testAdminPassword,
		SetupToken: "wrong-token",
	}))
	requireConnectCode(t, err, connect.CodeUnauthenticated)

	if _, err = client.SetupAdmin(ctx, connect.NewRequest(&p2pstreamv1.SetupAdminRequest{
		Username:   testAdminUsername,
		Password:   testAdminPassword,
		SetupToken: testSetupToken,
	})); err != nil {
		t.Fatalf("setup admin with token: %v", err)
	}
}

func TestRestartReopensSetupWindowWhenNoUsersExist(t *testing.T) {
	database := newTestDB(t)

	expiredApp := server.NewApp(testManagementConfig(config.Config{}), database)
	expiredApp.StartedAt = time.Now().Add(-6 * time.Minute)
	_, expiredClient := newTestManagementClient(t, expiredApp)

	expiredResp, err := expiredClient.GetSetupState(context.Background(), connect.NewRequest(&p2pstreamv1.GetSetupStateRequest{}))
	if err != nil {
		t.Fatalf("get expired setup state: %v", err)
	}
	if expiredResp.Msg.SetupAvailable {
		t.Fatal("expected first app setup to be expired")
	}

	restartedApp := server.NewApp(testManagementConfig(config.Config{}), database)
	_, restartedClient := newTestManagementClient(t, restartedApp)

	restartedResp, err := restartedClient.GetSetupState(context.Background(), connect.NewRequest(&p2pstreamv1.GetSetupStateRequest{}))
	if err != nil {
		t.Fatalf("get restarted setup state: %v", err)
	}
	if !restartedResp.Msg.SetupAvailable {
		t.Fatal("expected restarted app to reopen setup window")
	}
}

func TestExistingUserDisablesSetupWithinWindow(t *testing.T) {
	app := server.NewApp(testManagementConfig(config.Config{}), newTestDB(t))
	_, client := newTestManagementClient(t, app)
	createAdminSession(t, client)

	resp, err := client.GetSetupState(context.Background(), connect.NewRequest(&p2pstreamv1.GetSetupStateRequest{}))
	if err != nil {
		t.Fatalf("get setup state: %v", err)
	}
	if resp.Msg.SetupRequired {
		t.Fatal("expected setup to be disabled after first user exists")
	}
	if resp.Msg.SetupAvailable {
		t.Fatal("expected setup to be unavailable after first user exists")
	}
}

func TestSecondSetupAttemptRejected(t *testing.T) {
	app := server.NewApp(testManagementConfig(config.Config{}), newTestDB(t))
	_, client := newTestManagementClient(t, app)
	createAdminSession(t, client)

	_, err := client.SetupAdmin(context.Background(), connect.NewRequest(&p2pstreamv1.SetupAdminRequest{
		Username:   "second_admin",
		Password:   testAdminPassword,
		SetupToken: testSetupToken,
	}))
	requireConnectCode(t, err, connect.CodeFailedPrecondition)
}

func TestLoginLogoutSessionCookie(t *testing.T) {
	app := server.NewApp(testManagementConfig(config.Config{}), newTestDB(t))
	_, client := newTestManagementClient(t, app)

	_, err := client.GetCurrentUser(context.Background(), connect.NewRequest(&p2pstreamv1.GetCurrentUserRequest{}))
	requireConnectCode(t, err, connect.CodeUnauthenticated)

	cookie := createAdminSession(t, client)

	currentUserReq := connect.NewRequest(&p2pstreamv1.GetCurrentUserRequest{})
	currentUserReq.Header().Set("Cookie", cookie)
	currentUserResp, err := client.GetCurrentUser(context.Background(), currentUserReq)
	if err != nil {
		t.Fatalf("get current user: %v", err)
	}
	if currentUserResp.Msg.User.GetUsername() != testAdminUsername {
		t.Fatalf("expected current user %q, got %q", testAdminUsername, currentUserResp.Msg.User.GetUsername())
	}

	logoutReq := connect.NewRequest(&p2pstreamv1.LogoutRequest{})
	logoutReq.Header().Set("Cookie", cookie)
	logoutResp, err := client.Logout(context.Background(), logoutReq)
	if err != nil {
		t.Fatalf("logout: %v", err)
	}
	if logoutResp.Header().Get("Set-Cookie") == "" {
		t.Fatal("expected logout to clear session cookie")
	}

	reusedSessionReq := connect.NewRequest(&p2pstreamv1.GetCurrentUserRequest{})
	reusedSessionReq.Header().Set("Cookie", cookie)
	_, err = client.GetCurrentUser(context.Background(), reusedSessionReq)
	requireConnectCode(t, err, connect.CodeUnauthenticated)
}

func TestSessionCookieSecureFollowsManagementTLS(t *testing.T) {
	app := server.NewApp(testManagementConfig(config.Config{ManagementTLSEnabled: true}), newTestDB(t))
	_, client := newTestManagementClient(t, app)

	ctx := context.Background()
	_, err := client.SetupAdmin(ctx, connect.NewRequest(&p2pstreamv1.SetupAdminRequest{
		Username:   testAdminUsername,
		Password:   testAdminPassword,
		SetupToken: testSetupToken,
	}))
	if err != nil {
		t.Fatalf("setup admin: %v", err)
	}
	loginResp, err := client.Login(ctx, connect.NewRequest(&p2pstreamv1.LoginRequest{
		Username: testAdminUsername,
		Password: testAdminPassword,
	}))
	if err != nil {
		t.Fatalf("login admin: %v", err)
	}
	if setCookie := loginResp.Header().Get("Set-Cookie"); !strings.Contains(setCookie, "; Secure") {
		t.Fatalf("Set-Cookie missing Secure with management TLS enabled: %q", setCookie)
	}
}

func TestSessionCookieCanBeInsecureForExplicitHTTPDevelopment(t *testing.T) {
	app := server.NewApp(testManagementConfig(config.Config{}), newTestDB(t))
	_, client := newTestManagementClient(t, app)

	ctx := context.Background()
	_, err := client.SetupAdmin(ctx, connect.NewRequest(&p2pstreamv1.SetupAdminRequest{
		Username:   testAdminUsername,
		Password:   testAdminPassword,
		SetupToken: testSetupToken,
	}))
	if err != nil {
		t.Fatalf("setup admin: %v", err)
	}
	loginResp, err := client.Login(ctx, connect.NewRequest(&p2pstreamv1.LoginRequest{
		Username: testAdminUsername,
		Password: testAdminPassword,
	}))
	if err != nil {
		t.Fatalf("login admin: %v", err)
	}
	if setCookie := loginResp.Header().Get("Set-Cookie"); strings.Contains(setCookie, "; Secure") {
		t.Fatalf("Set-Cookie unexpectedly secure without TLS/production config: %q", setCookie)
	}
}

func TestSessionCookieSecureFollowsProductionEnv(t *testing.T) {
	app := server.NewApp(testManagementConfig(config.Config{Env: "production"}), newTestDB(t))
	_, client := newTestManagementClient(t, app)

	ctx := context.Background()
	_, err := client.SetupAdmin(ctx, connect.NewRequest(&p2pstreamv1.SetupAdminRequest{
		Username:   testAdminUsername,
		Password:   testAdminPassword,
		SetupToken: testSetupToken,
	}))
	if err != nil {
		t.Fatalf("setup admin: %v", err)
	}
	loginResp, err := client.Login(ctx, connect.NewRequest(&p2pstreamv1.LoginRequest{
		Username: testAdminUsername,
		Password: testAdminPassword,
	}))
	if err != nil {
		t.Fatalf("login admin: %v", err)
	}
	if setCookie := loginResp.Header().Get("Set-Cookie"); !strings.Contains(setCookie, "; Secure") {
		t.Fatalf("Set-Cookie missing Secure in production env: %q", setCookie)
	}
}

func TestLoginThrottleBlocksRepeatedFailures(t *testing.T) {
	app := server.NewApp(testManagementConfig(config.Config{}), newTestDB(t))
	_, client := newTestManagementClient(t, app)

	ctx := context.Background()
	_, err := client.SetupAdmin(ctx, connect.NewRequest(&p2pstreamv1.SetupAdminRequest{
		Username:   testAdminUsername,
		Password:   testAdminPassword,
		SetupToken: testSetupToken,
	}))
	if err != nil {
		t.Fatalf("setup admin: %v", err)
	}
	for i := 0; i < 5; i++ {
		_, err = client.Login(ctx, connect.NewRequest(&p2pstreamv1.LoginRequest{
			Username: testAdminUsername,
			Password: "wrong password",
		}))
		requireConnectCode(t, err, connect.CodeUnauthenticated)
	}
	_, err = client.Login(ctx, connect.NewRequest(&p2pstreamv1.LoginRequest{
		Username: testAdminUsername,
		Password: "wrong password",
	}))
	requireConnectCode(t, err, connect.CodeResourceExhausted)

	_, err = client.Login(ctx, connect.NewRequest(&p2pstreamv1.LoginRequest{
		Username: testAdminUsername,
		Password: testAdminPassword,
	}))
	requireConnectCode(t, err, connect.CodeResourceExhausted)
}

func TestLoginThrottleResetsAfterSuccessfulLogin(t *testing.T) {
	app := server.NewApp(testManagementConfig(config.Config{}), newTestDB(t))
	_, client := newTestManagementClient(t, app)

	ctx := context.Background()
	_, err := client.SetupAdmin(ctx, connect.NewRequest(&p2pstreamv1.SetupAdminRequest{
		Username:   testAdminUsername,
		Password:   testAdminPassword,
		SetupToken: testSetupToken,
	}))
	if err != nil {
		t.Fatalf("setup admin: %v", err)
	}
	for i := 0; i < 2; i++ {
		_, err = client.Login(ctx, connect.NewRequest(&p2pstreamv1.LoginRequest{
			Username: testAdminUsername,
			Password: "wrong password",
		}))
		requireConnectCode(t, err, connect.CodeUnauthenticated)
	}
	if _, err = client.Login(ctx, connect.NewRequest(&p2pstreamv1.LoginRequest{
		Username: testAdminUsername,
		Password: testAdminPassword,
	})); err != nil {
		t.Fatalf("login after failures: %v", err)
	}
	for i := 0; i < 5; i++ {
		_, err = client.Login(ctx, connect.NewRequest(&p2pstreamv1.LoginRequest{
			Username: testAdminUsername,
			Password: "wrong password",
		}))
		requireConnectCode(t, err, connect.CodeUnauthenticated)
	}
}

func TestProtectedRPCRejectsWithoutSession(t *testing.T) {
	app := server.NewApp(testManagementConfig(config.Config{}), newTestDB(t))
	_, client := newTestManagementClient(t, app)
	createAdminSession(t, client)

	_, err := client.GetStatus(context.Background(), connect.NewRequest(&p2pstreamv1.GetStatusRequest{}))
	requireConnectCode(t, err, connect.CodeUnauthenticated)

	_, err = client.StartProxy(context.Background(), connect.NewRequest(&p2pstreamv1.StartProxyRequest{}))
	requireConnectCode(t, err, connect.CodeUnauthenticated)

	_, err = client.StopProxy(context.Background(), connect.NewRequest(&p2pstreamv1.StopProxyRequest{}))
	requireConnectCode(t, err, connect.CodeUnauthenticated)
}

func TestProxyStartStopLifecycle(t *testing.T) {
	database := newTestDB(t)
	seedTestHTTPPublicListener(t, database, "https://example.com")
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

	startAgainReq := connect.NewRequest(&p2pstreamv1.StartProxyRequest{})
	startAgainReq.Header().Set("Cookie", cookie)
	startAgainResp, err := client.StartProxy(context.Background(), startAgainReq)
	if err != nil {
		t.Fatalf("start proxy again: %v", err)
	}
	if startAgainResp.Msg.Proxy.GetState() != p2pstreamv1.ProxyState_PROXY_STATE_RUNNING {
		t.Fatalf("expected idempotent running proxy, got %s", startAgainResp.Msg.Proxy.GetState())
	}

	stopReq := connect.NewRequest(&p2pstreamv1.StopProxyRequest{})
	stopReq.Header().Set("Cookie", cookie)
	stopResp, err := client.StopProxy(context.Background(), stopReq)
	if err != nil {
		t.Fatalf("stop proxy: %v", err)
	}
	if stopResp.Msg.Proxy.GetState() != p2pstreamv1.ProxyState_PROXY_STATE_STOPPED {
		t.Fatalf("expected stopped proxy, got %s", stopResp.Msg.Proxy.GetState())
	}

	stopAgainReq := connect.NewRequest(&p2pstreamv1.StopProxyRequest{})
	stopAgainReq.Header().Set("Cookie", cookie)
	stopAgainResp, err := client.StopProxy(context.Background(), stopAgainReq)
	if err != nil {
		t.Fatalf("stop proxy again: %v", err)
	}
	if stopAgainResp.Msg.Proxy.GetState() != p2pstreamv1.ProxyState_PROXY_STATE_STOPPED {
		t.Fatalf("expected idempotent stopped proxy, got %s", stopAgainResp.Msg.Proxy.GetState())
	}
}

func TestAgentTokenProtectsStatsAndTunnel(t *testing.T) {
	app := server.NewApp(testManagementConfig(config.Config{
		BootstrapAgentID:    "auth-agent",
		BootstrapAgentName:  "Auth Agent",
		BootstrapAgentToken: "agent-secret",
	}), newTestDB(t))

	_, err := app.ReportStats(context.Background(), connect.NewRequest(&p2pstreamv1.AgentStatsRequest{}))
	requireConnectCode(t, err, connect.CodeUnauthenticated)

	statsReq := connect.NewRequest(&p2pstreamv1.AgentStatsRequest{ReqInternalError: 1, AgentPublicId: "auth-agent"})
	statsReq.Header().Set("Authorization", "Bearer agent-secret")
	if _, err := app.ReportStats(context.Background(), statsReq); err != nil {
		t.Fatalf("report stats with token: %v", err)
	}

	mgmtSrv, _ := newTestManagementClient(t, app)

	_, _, err = dialAgentTunnel(context.Background(), mgmtSrv.URL, "auth-agent", "", nil)
	if err == nil {
		t.Fatal("expected tunnel dial without token to fail")
	}

	session, _, err := dialAgentTunnel(context.Background(), mgmtSrv.URL, "auth-agent", "agent-secret", nil)
	if err != nil {
		t.Fatalf("tunnel dial with token: %v", err)
	}
	session.Close()
}
