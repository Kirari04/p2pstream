package main_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/internal/config"
	"p2pstream/internal/server"
)

func TestRegisteredAgentTunnelAuthAndDuplicates(t *testing.T) {
	app := server.NewApp(testManagementConfig(config.Config{}), newTestDB(t))
	mgmtSrv, client := newTestManagementClient(t, app)
	cookie := createAdminSession(t, client)

	agentA, tokenA := createRegisteredAgent(t, client, cookie, "tunnel-agent-a", "Tunnel Agent A")
	agentB, tokenB := createRegisteredAgent(t, client, cookie, "tunnel-agent-b", "Tunnel Agent B")
	disabled, disabledToken := createRegisteredAgent(t, client, cookie, "tunnel-agent-disabled", "Tunnel Agent Disabled")
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

	if _, _, err := dialAgentTunnel(context.Background(), mgmtSrv.URL, "missing-agent", tokenA, nil); err == nil {
		t.Fatal("expected unknown agent to be rejected")
	}
	if _, _, err := dialAgentTunnel(context.Background(), mgmtSrv.URL, agentA.GetPublicId(), "wrong-token", nil); err == nil {
		t.Fatal("expected wrong token to be rejected")
	}
	if _, _, err := dialAgentTunnel(context.Background(), mgmtSrv.URL, disabled.GetPublicId(), disabledToken, nil); err == nil {
		t.Fatal("expected disabled agent to be rejected")
	}

	sessionA, _, err := dialAgentTunnel(context.Background(), mgmtSrv.URL, agentA.GetPublicId(), tokenA, nil)
	if err != nil {
		t.Fatalf("dial agent A: %v", err)
	}
	defer sessionA.Close()

	duplicate, _, err := dialAgentTunnel(context.Background(), mgmtSrv.URL, agentA.GetPublicId(), tokenA, nil)
	if err == nil {
		_ = duplicate.Close()
		t.Fatal("expected duplicate agent connection to fail")
	}

	sessionB, _, err := dialAgentTunnel(context.Background(), mgmtSrv.URL, agentB.GetPublicId(), tokenB, nil)
	if err != nil {
		t.Fatalf("dial different agent: %v", err)
	}
	sessionB.Close()
}
