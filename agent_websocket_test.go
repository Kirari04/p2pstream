package main_test

import (
	"context"
	"net/http"
	"testing"

	"connectrpc.com/connect"
	"github.com/coder/websocket"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/internal/config"
	"p2pstream/internal/server"
)

func TestRegisteredAgentWebSocketAuthAndDuplicates(t *testing.T) {
	app := server.NewApp(&config.Config{}, newTestDB(t))
	mgmtSrv, client := newTestManagementClient(t, app)
	cookie := createAdminSession(t, client)
	wsURL := "ws" + mgmtSrv.URL[4:] + "/ws"

	agentA, tokenA := createRegisteredAgent(t, client, cookie, "ws-agent-a", "WS Agent A")
	agentB, tokenB := createRegisteredAgent(t, client, cookie, "ws-agent-b", "WS Agent B")
	disabled, disabledToken := createRegisteredAgent(t, client, cookie, "ws-agent-disabled", "WS Agent Disabled")
	disableReq := connect.NewRequest(&p2pstreamv1.UpdateAgentRequest{
		Id:       disabled.GetId(),
		PublicId: disabled.GetPublicId(),
		Name:     disabled.GetName(),
		Enabled:  false,
	})
	disableReq.Header().Set("Cookie", cookie)
	if _, err := client.UpdateAgent(context.Background(), disableReq); err != nil {
		t.Fatalf("disable agent: %v", err)
	}

	if _, _, err := dialAgentWebSocket(wsURL, "missing-agent", tokenA); err == nil {
		t.Fatal("expected unknown agent to be rejected")
	}
	if _, _, err := dialAgentWebSocket(wsURL, agentA.GetPublicId(), "wrong-token"); err == nil {
		t.Fatal("expected wrong token to be rejected")
	}
	if _, _, err := dialAgentWebSocket(wsURL, disabled.GetPublicId(), disabledToken); err == nil {
		t.Fatal("expected disabled agent to be rejected")
	}

	connA, _, err := dialAgentWebSocket(wsURL, agentA.GetPublicId(), tokenA)
	if err != nil {
		t.Fatalf("dial agent A: %v", err)
	}
	defer connA.Close(websocket.StatusNormalClosure, "test complete")

	duplicate, _, err := dialAgentWebSocket(wsURL, agentA.GetPublicId(), tokenA)
	if err == nil {
		defer duplicate.Close(websocket.StatusNormalClosure, "test complete")
		if _, _, readErr := duplicate.Reader(context.Background()); readErr == nil {
			t.Fatal("expected duplicate agent connection to be closed")
		}
	}

	connB, _, err := dialAgentWebSocket(wsURL, agentB.GetPublicId(), tokenB)
	if err != nil {
		t.Fatalf("dial different agent: %v", err)
	}
	connB.Close(websocket.StatusNormalClosure, "test complete")
}

func dialAgentWebSocket(wsURL string, publicID string, token string) (*websocket.Conn, *http.Response, error) {
	return websocket.Dial(context.Background(), wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{
			"Authorization":        []string{"Bearer " + token},
			"X-P2PStream-Agent-ID": []string{publicID},
		},
	})
}
