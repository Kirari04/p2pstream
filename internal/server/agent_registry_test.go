package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/coder/websocket"
	"github.com/google/uuid"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/internal/config"
	"p2pstream/internal/db"
	"p2pstream/msg"
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

func TestRotateAgentTokenFailsPendingRequestsWithTokenRotatedError(t *testing.T) {
	database := newAgentRegistryTestDB(t)
	app := NewApp(nil, database)
	header := createTestAdminSession(t, app)
	agent := createAgentRegistryTestAgent(t, database, "agent-rotate-pending", "Rotate Pending", "old-token")
	conn := agentRegistryTestConn(agent)
	if err := app.AgentHub.connect(conn); err != nil {
		t.Fatalf("connect agent: %v", err)
	}
	pendingCtx, pendingCancel := context.WithCancel(context.Background())
	pending := &pendingAgentRequest{
		AgentID:       agent.ID,
		AgentPublicID: agent.PublicID,
		ResponseCh:    make(chan *msg.Request, 1),
		ErrorCh:       make(chan error, 1),
		ctx:           pendingCtx,
		cancel:        pendingCancel,
	}
	pendingID := uuid.New()
	app.PendingRequests.Store(pendingID, pending)
	defer app.PendingRequests.Delete(pendingID)

	rotateAgentTokenForTest(t, app, header, agent.ID)

	select {
	case err := <-pending.ErrorCh:
		if !errors.Is(err, errAgentTokenRotated) {
			t.Fatalf("pending error = %v, want errAgentTokenRotated", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for pending error")
	}
	select {
	case <-pendingCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("pending context was not canceled")
	}
	if got := app.AgentHub.connectedByID(agent.ID); got != nil {
		t.Fatalf("connected agent after rotation = %#v, want nil", got)
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
	secondPending := &pendingAgentRequest{
		AgentID:       second.ID,
		AgentPublicID: second.PublicID,
		ResponseCh:    make(chan *msg.Request, 1),
		ErrorCh:       make(chan error, 1),
		ctx:           context.Background(),
	}
	secondPendingID := uuid.New()
	app.PendingRequests.Store(secondPendingID, secondPending)
	defer app.PendingRequests.Delete(secondPendingID)

	rotateAgentTokenForTest(t, app, header, first.ID)

	if got := app.AgentHub.connectedByID(first.ID); got != nil {
		t.Fatalf("first agent still connected = %#v", got)
	}
	if got := app.AgentHub.connectedByID(second.ID); got != secondConn {
		t.Fatalf("second agent connection = %#v, want %#v", got, secondConn)
	}
	assertAgentDoneClosed(t, firstConn)
	assertAgentDoneOpen(t, secondConn)
	select {
	case err := <-secondPending.ErrorCh:
		t.Fatalf("second agent pending request was failed: %v", err)
	default:
	}
}

func TestRotateAgentTokenClosesWebSocketConnection(t *testing.T) {
	database := newAgentRegistryTestDB(t)
	app := NewApp(&config.Config{ManagementUIDisabled: true}, database)
	header := createTestAdminSession(t, app)
	agent := createAgentRegistryTestAgent(t, database, "agent-rotate-ws", "Rotate WebSocket", "old-token")
	mux := http.NewServeMux()
	app.RegisterManagementRoutes(mux)
	server := httptest.NewServer(mux)
	defer server.Close()

	wsHeader := http.Header{}
	wsHeader.Set("X-P2PStream-Agent-ID", agent.PublicID)
	wsHeader.Set("Authorization", "Bearer old-token")
	dialCtx, dialCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer dialCancel()
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"
	conn, _, err := websocket.Dial(dialCtx, wsURL, &websocket.DialOptions{HTTPHeader: wsHeader})
	if err != nil {
		t.Fatalf("dial agent websocket: %v", err)
	}
	defer conn.CloseNow()
	waitForAgentHubConnection(t, app, agent.ID, true)

	rotateAgentTokenForTest(t, app, header, agent.ID)

	waitForAgentHubConnection(t, app, agent.ID, false)
	readCtx, readCancel := context.WithTimeout(context.Background(), time.Second)
	defer readCancel()
	if _, _, err := conn.Read(readCtx); err == nil {
		t.Fatal("websocket read succeeded after token rotation; want closed connection")
	}
}

func TestAgentPendingFailureReason(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantFinish string
		wantKind   string
	}{
		{name: "disconnected", err: errAgentDisconnected, wantFinish: "agent_disconnected", wantKind: "agent_disconnected"},
		{name: "token_rotated", err: errAgentTokenRotated, wantFinish: "agent_token_rotated", wantKind: "agent_token_rotated"},
		{name: "unknown", err: errors.New("agent failed"), wantFinish: "agent_failed", wantKind: "agent_failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotFinish, gotKind := agentPendingFailureReason(tc.err)
			if gotFinish != tc.wantFinish || gotKind != tc.wantKind {
				t.Fatalf("reason = (%q, %q), want (%q, %q)", gotFinish, gotKind, tc.wantFinish, tc.wantKind)
			}
		})
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
		WriteCh:  make(chan *msg.Request, 10),
		Done:     make(chan struct{}),
	}
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
