package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"

	"p2pstream/msg"
)

func TestAgentWebSocketHeartbeatFailureDisconnectsAgentAndPendingRequests(t *testing.T) {
	withTestAgentWebSocketHeartbeat(t, 10*time.Millisecond, 20*time.Millisecond)

	app := NewApp(nil, newAgentRegistryTestDB(t))
	agent := createAgentRegistryTestAgent(t, app.DB, "heartbeat-fail", "Heartbeat Fail", "heartbeat-token")
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
	t.Cleanup(func() { app.PendingRequests.Delete(pendingID) })

	mux := http.NewServeMux()
	app.RegisterManagementRoutes(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	conn, _, err := dialHeartbeatTestAgent(srv.URL, agent.PublicID, "heartbeat-token")
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer conn.CloseNow()

	waitAgentConnectionState(t, app, agent.ID, true)
	waitAgentConnectionState(t, app, agent.ID, false)

	select {
	case err := <-pending.ErrorCh:
		if !errors.Is(err, errAgentDisconnected) {
			t.Fatalf("pending error = %v, want errAgentDisconnected", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for pending request failure")
	}
	select {
	case <-pendingCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("pending context was not canceled")
	}
}

func TestAgentWebSocketHeartbeatKeepsResponsiveAgentConnected(t *testing.T) {
	withTestAgentWebSocketHeartbeat(t, 10*time.Millisecond, 100*time.Millisecond)

	app := NewApp(nil, newAgentRegistryTestDB(t))
	agent := createAgentRegistryTestAgent(t, app.DB, "heartbeat-ok", "Heartbeat OK", "heartbeat-token")
	mux := http.NewServeMux()
	app.RegisterManagementRoutes(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	conn, _, err := dialHeartbeatTestAgent(srv.URL, agent.PublicID, "heartbeat-token")
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer conn.CloseNow()

	readCtx, readCancel := context.WithCancel(context.Background())
	defer readCancel()
	go func() {
		for {
			if _, _, err := conn.Reader(readCtx); err != nil {
				return
			}
		}
	}()

	waitAgentConnectionState(t, app, agent.ID, true)
	time.Sleep(75 * time.Millisecond)
	if got := app.AgentHub.connectedByID(agent.ID); got == nil {
		t.Fatal("responsive agent disconnected during heartbeat window")
	}
}

func withTestAgentWebSocketHeartbeat(t *testing.T, interval time.Duration, timeout time.Duration) {
	t.Helper()
	originalInterval := agentWebSocketPingInterval
	originalTimeout := agentWebSocketPingTimeout
	agentWebSocketPingInterval = interval
	agentWebSocketPingTimeout = timeout
	t.Cleanup(func() {
		agentWebSocketPingInterval = originalInterval
		agentWebSocketPingTimeout = originalTimeout
	})
}

func dialHeartbeatTestAgent(serverURL string, publicID string, token string) (*websocket.Conn, *http.Response, error) {
	return websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(serverURL, "http")+"/ws", &websocket.DialOptions{
		HTTPHeader: http.Header{
			"Authorization":        []string{"Bearer " + token},
			"X-P2PStream-Agent-ID": []string{publicID},
		},
	})
}

func waitAgentConnectionState(t *testing.T, app *App, agentID int64, wantConnected bool) {
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
