package server

import (
	"bufio"
	"context"
	"database/sql"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/hashicorp/yamux"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/internal/config"
	"p2pstream/internal/db"
	"p2pstream/internal/tunnel"
)

func TestRandomAgentPublicIDFormatAndUniqueness(t *testing.T) {
	seen := make(map[string]bool)
	for range 1000 {
		publicID, err := randomAgentPublicID()
		if err != nil {
			t.Fatalf("generate agent public id: %v", err)
		}
		if _, err := validateGeneratedAgentPublicID(publicID); err != nil {
			t.Fatalf("generated public id %q did not validate: %v", publicID, err)
		}
		if publicID != strings.ToLower(publicID) {
			t.Fatalf("generated public id %q is not lower-case", publicID)
		}
		if len(publicID) != len(agentPublicIDPrefix)+agentPublicIDEncodedBytes {
			t.Fatalf("generated public id %q length = %d, want %d", publicID, len(publicID), len(agentPublicIDPrefix)+agentPublicIDEncodedBytes)
		}
		if seen[publicID] {
			t.Fatalf("generated duplicate public id %q", publicID)
		}
		seen[publicID] = true
	}
}

func TestCreateAgentWithGeneratedPublicIDCollisionRetry(t *testing.T) {
	database := newAgentRegistryTestDB(t)
	existingID := "agent-aaaaaaaaaaaaaaaaaaaaaaaaaa"
	nextID := "agent-bbbbbbbbbbbbbbbbbbbbbbbbbb"
	if _, err := database.CreateAgent(context.Background(), db.CreateAgentParams{
		PublicID:  existingID,
		Name:      "Existing Agent",
		TokenHash: hashAgentToken("existing-token"),
		Enabled:   1,
	}); err != nil {
		t.Fatalf("seed existing agent: %v", err)
	}

	oldGenerator := newAgentPublicID
	attempts := 0
	newAgentPublicID = func() (string, error) {
		attempts++
		if attempts == 1 {
			return existingID, nil
		}
		return nextID, nil
	}
	t.Cleanup(func() {
		newAgentPublicID = oldGenerator
	})

	app := NewApp(nil, database)
	agent, err := app.createAgentWithGeneratedPublicID(context.Background(), "Retry Agent", hashAgentToken("retry-token"), 1)
	if err != nil {
		t.Fatalf("create with retry: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("generator attempts = %d, want 2", attempts)
	}
	if agent.PublicID != nextID {
		t.Fatalf("public id = %q, want %q", agent.PublicID, nextID)
	}
}

func TestCreateAgentWithGeneratedPublicIDCollisionFailure(t *testing.T) {
	database := newAgentRegistryTestDB(t)
	existingID := "agent-cccccccccccccccccccccccccc"
	if _, err := database.CreateAgent(context.Background(), db.CreateAgentParams{
		PublicID:  existingID,
		Name:      "Existing Agent",
		TokenHash: hashAgentToken("existing-token"),
		Enabled:   1,
	}); err != nil {
		t.Fatalf("seed existing agent: %v", err)
	}

	oldGenerator := newAgentPublicID
	attempts := 0
	newAgentPublicID = func() (string, error) {
		attempts++
		return existingID, nil
	}
	t.Cleanup(func() {
		newAgentPublicID = oldGenerator
	})

	app := NewApp(nil, database)
	if _, err := app.createAgentWithGeneratedPublicID(context.Background(), "Fail Agent", hashAgentToken("fail-token"), 1); connect.CodeOf(err) != connect.CodeInternal {
		t.Fatalf("expected internal error after repeated collisions, got %v", err)
	}
	if attempts != agentPublicIDMaxAttempts {
		t.Fatalf("generator attempts = %d, want %d", attempts, agentPublicIDMaxAttempts)
	}
}

func TestRotateAgentTokenDisconnectsActiveAgent(t *testing.T) {
	database := newAgentRegistryTestDB(t)
	app := NewApp(nil, database)
	header := createTestAdminSession(t, app)
	agent := createAgentRegistryTestAgent(t, database, "agent-rotate-active", "Rotate Active", "old-token")
	conn := agentRegistryTestConn(agent)
	if err := app.AgentHub.connect(conn); err != nil {
		t.Fatalf("connect agent: %v", err)
	}

	resp := rotateAgentTokenForTest(t, app, header, agent.ID)
	if resp.Msg.Token == "" {
		t.Fatal("rotation response token is empty")
	}
	if resp.Msg.Agent == nil || resp.Msg.Agent.Connected {
		t.Fatalf("response agent connected = %v, want false", resp.Msg.Agent != nil && resp.Msg.Agent.Connected)
	}
	if got := app.AgentHub.connectedByID(agent.ID); got != nil {
		t.Fatalf("connected agent after rotation = %#v, want nil", got)
	}
	assertAgentDoneClosed(t, conn)
	if _, err := app.authenticateAgent(context.Background(), agent.PublicID, "Bearer old-token"); err == nil {
		t.Fatal("old token authenticated after rotation")
	}
	if authenticated, err := app.authenticateAgent(context.Background(), agent.PublicID, "Bearer "+resp.Msg.Token); err != nil {
		t.Fatalf("new token did not authenticate: %v", err)
	} else if authenticated.ID != agent.ID {
		t.Fatalf("authenticated agent id = %d, want %d", authenticated.ID, agent.ID)
	}
}

func TestRotateAgentTokenOnlyDisconnectsTargetAgent(t *testing.T) {
	database := newAgentRegistryTestDB(t)
	app := NewApp(nil, database)
	header := createTestAdminSession(t, app)
	first := createAgentRegistryTestAgent(t, database, "agent-rotate-first", "Rotate First", "first-token")
	second := createAgentRegistryTestAgent(t, database, "agent-rotate-second", "Rotate Second", "second-token")
	firstConn := agentRegistryTestConn(first)
	secondConn := agentRegistryTestConn(second)
	if err := app.AgentHub.connect(firstConn); err != nil {
		t.Fatalf("connect first agent: %v", err)
	}
	if err := app.AgentHub.connect(secondConn); err != nil {
		t.Fatalf("connect second agent: %v", err)
	}
	rotateAgentTokenForTest(t, app, header, first.ID)

	if got := app.AgentHub.connectedByID(first.ID); got != nil {
		t.Fatalf("first agent still connected = %#v", got)
	}
	if got := app.AgentHub.connectedByID(second.ID); got != secondConn {
		t.Fatalf("second agent connection = %#v, want %#v", got, secondConn)
	}
	assertAgentDoneClosed(t, firstConn)
	assertAgentDoneOpen(t, secondConn)
}

func TestRotateAgentTokenClosesTunnelConnection(t *testing.T) {
	database := newAgentRegistryTestDB(t)
	app := NewApp(&config.Config{ManagementUIDisabled: true}, database)
	header := createTestAdminSession(t, app)
	agent := createAgentRegistryTestAgent(t, database, "agent-rotate-tunnel", "Rotate Tunnel", "old-token")
	mux := http.NewServeMux()
	app.RegisterManagementRoutes(mux)
	server := httptest.NewServer(mux)
	defer server.Close()

	session, conn, err := dialAgentRegistryTestTunnel(server.URL, agent.PublicID, "old-token")
	if err != nil {
		t.Fatalf("dial agent tunnel: %v", err)
	}
	defer conn.Close()
	defer session.Close()
	waitForAgentHubConnection(t, app, agent.ID, true)

	connectedConn := app.AgentHub.connectedByID(agent.ID)
	if connectedConn == nil {
		t.Fatal("agent was not connected in hub")
	}
	if connectedConn.ConnectionDBID == 0 {
		t.Fatal("agent connection db id was not recorded")
	}

	rotateResp := rotateAgentTokenForTest(t, app, header, agent.ID)

	waitForAgentHubConnection(t, app, agent.ID, false)
	select {
	case <-session.CloseChan():
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for tunnel session to close after token rotation")
	}
	waitForConnectionDisconnected(t, database, connectedConn.ConnectionDBID)

	if oldSession, oldConn, err := dialAgentRegistryTestTunnel(server.URL, agent.PublicID, "old-token"); err == nil {
		if oldSession != nil {
			oldSession.Close()
		}
		if oldConn != nil {
			oldConn.Close()
		}
		t.Fatal("old token reconnected after rotation")
	}

	newSession, newConn, err := dialAgentRegistryTestTunnel(server.URL, agent.PublicID, rotateResp.Msg.Token)
	if err != nil {
		t.Fatalf("new token did not reconnect after rotation: %v", err)
	}
	defer newConn.Close()
	defer newSession.Close()
	waitForAgentHubConnection(t, app, agent.ID, true)
}

func TestAgentTunnelRejectsMissingUpgrade(t *testing.T) {
	database := newAgentRegistryTestDB(t)
	app := NewApp(&config.Config{ManagementUIDisabled: true}, database)
	agent := createAgentRegistryTestAgent(t, database, "agent-tunnel-missing-upgrade", "Missing Upgrade", "token")
	mux := http.NewServeMux()
	app.RegisterManagementRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, tunnel.BootstrapPath, nil)
	req.Header.Set("X-P2PStream-Agent-ID", agent.PublicID)
	req.Header.Set("Authorization", "Bearer token")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing upgrade status = %d, want 400", rec.Code)
	}
}

func TestAgentTunnelRejectsWrongVersion(t *testing.T) {
	database := newAgentRegistryTestDB(t)
	app := NewApp(&config.Config{ManagementUIDisabled: true}, database)
	agent := createAgentRegistryTestAgent(t, database, "agent-tunnel-version", "Wrong Version", "token")
	mux := http.NewServeMux()
	app.RegisterManagementRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, tunnel.BootstrapPath, nil)
	req.Header.Set("X-P2PStream-Agent-ID", agent.PublicID)
	req.Header.Set("Authorization", "Bearer token")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", tunnel.UpgradeToken)
	req.Header.Set(tunnel.TunnelVersionHeader, "2")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUpgradeRequired {
		t.Fatalf("wrong version status = %d, want 426", rec.Code)
	}
}

func TestAgentTunnelRejectsDuplicateConnection(t *testing.T) {
	database := newAgentRegistryTestDB(t)
	app := NewApp(&config.Config{ManagementUIDisabled: true}, database)
	agent := createAgentRegistryTestAgent(t, database, "agent-tunnel-duplicate", "Duplicate Tunnel", "token")
	mux := http.NewServeMux()
	app.RegisterManagementRoutes(mux)
	server := httptest.NewServer(mux)
	defer server.Close()

	firstSession, firstConn, err := dialAgentRegistryTestTunnel(server.URL, agent.PublicID, "token")
	if err != nil {
		t.Fatalf("dial first tunnel: %v", err)
	}
	defer firstConn.Close()
	defer firstSession.Close()
	waitForAgentHubConnection(t, app, agent.ID, true)

	_, secondConn, err := dialAgentRegistryTestTunnel(server.URL, agent.PublicID, "token")
	if secondConn != nil {
		secondConn.Close()
	}
	if err == nil {
		t.Fatal("second tunnel connected, want duplicate rejection")
	}
	if !strings.Contains(err.Error(), "409") {
		t.Fatalf("duplicate tunnel error = %v, want status 409", err)
	}
}

func newAgentRegistryTestDB(t *testing.T) *db.DB {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "agent-registry-test.db"))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Errorf("close test db: %v", err)
		}
	})
	return database
}

func createAgentRegistryTestAgent(t *testing.T, database *db.DB, publicID string, name string, token string) db.Agent {
	t.Helper()
	agent, err := database.CreateAgent(context.Background(), db.CreateAgentParams{
		PublicID:  publicID,
		Name:      name,
		TokenHash: hashAgentToken(token),
		Enabled:   1,
	})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	return agent
}

func agentRegistryTestConn(agent db.Agent) *AgentConn {
	return &AgentConn{
		AgentID:  agent.ID,
		PublicID: agent.PublicID,
		Name:     agent.Name,
		Done:     make(chan struct{}),
	}
}

func dialAgentRegistryTestTunnel(serverURL string, publicID string, token string) (*yamux.Session, net.Conn, error) {
	parsed, err := url.Parse(serverURL)
	if err != nil {
		return nil, nil, err
	}
	conn, err := net.DialTimeout("tcp", parsed.Host, 2*time.Second)
	if err != nil {
		return nil, nil, err
	}
	req := fmt.Sprintf(
		"GET %s HTTP/1.1\r\nHost: %s\r\nConnection: Upgrade\r\nUpgrade: %s\r\n%s: 1\r\nX-P2PStream-Agent-ID: %s\r\nAuthorization: Bearer %s\r\n\r\n",
		tunnel.BootstrapPath,
		parsed.Host,
		tunnel.UpgradeToken,
		tunnel.TunnelVersionHeader,
		publicID,
		token,
	)
	if _, err := conn.Write([]byte(req)); err != nil {
		conn.Close()
		return nil, nil, err
	}
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, nil)
	if err != nil {
		conn.Close()
		return nil, nil, err
	}
	if resp.Body != nil {
		resp.Body.Close()
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		conn.Close()
		return nil, nil, fmt.Errorf("agent tunnel status %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Upgrade"); !strings.EqualFold(got, tunnel.UpgradeToken) {
		conn.Close()
		return nil, nil, fmt.Errorf("agent tunnel upgrade header = %q", got)
	}
	if reader.Buffered() > 0 {
		conn.Close()
		return nil, nil, fmt.Errorf("agent tunnel response left %d buffered bytes", reader.Buffered())
	}
	session, err := yamux.Client(conn, tunnel.DefaultYamuxConfig(nil))
	if err != nil {
		conn.Close()
		return nil, nil, err
	}
	return session, conn, nil
}

func rotateAgentTokenForTest(t *testing.T, app *App, header http.Header, agentID int64) *connect.Response[p2pstreamv1.RotateAgentTokenResponse] {
	t.Helper()
	req := connect.NewRequest(&p2pstreamv1.RotateAgentTokenRequest{Id: agentID})
	req.Header().Set("Cookie", header.Get("Cookie"))
	resp, err := app.RotateAgentToken(context.Background(), req)
	if err != nil {
		t.Fatalf("rotate agent token: %v", err)
	}
	return resp
}

func assertAgentDoneClosed(t *testing.T, conn *AgentConn) {
	t.Helper()
	select {
	case <-conn.Done:
	default:
		t.Fatal("agent Done channel is open, want closed")
	}
}

func assertAgentDoneOpen(t *testing.T, conn *AgentConn) {
	t.Helper()
	select {
	case <-conn.Done:
		t.Fatal("agent Done channel is closed, want open")
	default:
	}
}

func waitForAgentHubConnection(t *testing.T, app *App, agentID int64, wantConnected bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		connected := app.AgentHub.connectedByID(agentID) != nil
		if connected == wantConnected {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("agent connected state did not become %v", wantConnected)
}

func waitForConnectionDisconnected(t *testing.T, database *db.DB, connID int64) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var disconnectedAt sql.NullTime
		if err := database.QueryRowContext(context.Background(), `SELECT disconnected_at FROM connections WHERE id = ?`, connID).Scan(&disconnectedAt); err != nil {
			t.Fatalf("read connection %d disconnected_at: %v", connID, err)
		}
		if disconnectedAt.Valid {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("connection %d disconnected_at was not set", connID)
}
