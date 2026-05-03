package main_test

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"connectrpc.com/connect"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/internal/config"
	"p2pstream/internal/server"
)

func TestCreateAgentGeneratesOpaquePublicID(t *testing.T) {
	app := server.NewApp(&config.Config{}, newTestDB(t))
	_, client := newTestManagementClient(t, app)
	cookie := createAdminSession(t, client)

	createReq := connect.NewRequest(&p2pstreamv1.CreateAgentRequest{
		Name:    "Edge Paris 01",
		Enabled: true,
	})
	createReq.Header().Set("Cookie", cookie)
	createResp, err := client.CreateAgent(context.Background(), createReq)
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	agent := createResp.Msg.GetAgent()
	if createResp.Msg.GetToken() == "" {
		t.Fatal("expected create agent to return one-time token")
	}
	if !regexp.MustCompile(`^agent-[a-z2-7]{26}$`).MatchString(agent.GetPublicId()) {
		t.Fatalf("generated public id = %q, want agent- plus 26 lower-case base32 chars", agent.GetPublicId())
	}
	if strings.Contains(agent.GetPublicId(), "edge") || strings.Contains(agent.GetPublicId(), "paris") {
		t.Fatalf("generated public id %q should not contain display-name content", agent.GetPublicId())
	}

	updateReq := connect.NewRequest(&p2pstreamv1.UpdateAgentRequest{
		Id:      agent.GetId(),
		Name:    "Edge Paris Renamed",
		Enabled: true,
	})
	updateReq.Header().Set("Cookie", cookie)
	updateResp, err := client.UpdateAgent(context.Background(), updateReq)
	if err != nil {
		t.Fatalf("update agent without public id: %v", err)
	}
	if updateResp.Msg.GetAgent().GetPublicId() != agent.GetPublicId() {
		t.Fatalf("update changed public id to %q, want %q", updateResp.Msg.GetAgent().GetPublicId(), agent.GetPublicId())
	}

	sameIDReq := connect.NewRequest(&p2pstreamv1.UpdateAgentRequest{
		Id:       agent.GetId(),
		PublicId: agent.GetPublicId(),
		Name:     "Edge Paris Same ID",
		Enabled:  true,
	})
	sameIDReq.Header().Set("Cookie", cookie)
	if _, err := client.UpdateAgent(context.Background(), sameIDReq); err != nil {
		t.Fatalf("update agent with same public id: %v", err)
	}

	changedIDReq := connect.NewRequest(&p2pstreamv1.UpdateAgentRequest{
		Id:       agent.GetId(),
		PublicId: "agent-aaaaaaaaaaaaaaaaaaaaaaaaaa",
		Name:     "Edge Paris Changed ID",
		Enabled:  true,
	})
	changedIDReq.Header().Set("Cookie", cookie)
	if _, err := client.UpdateAgent(context.Background(), changedIDReq); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("expected changed public id to be rejected, got %v", err)
	}

	rotateReq := connect.NewRequest(&p2pstreamv1.RotateAgentTokenRequest{Id: agent.GetId()})
	rotateReq.Header().Set("Cookie", cookie)
	rotateResp, err := client.RotateAgentToken(context.Background(), rotateReq)
	if err != nil {
		t.Fatalf("rotate agent token: %v", err)
	}
	if rotateResp.Msg.GetAgent().GetPublicId() != agent.GetPublicId() {
		t.Fatalf("token rotation changed public id to %q, want %q", rotateResp.Msg.GetAgent().GetPublicId(), agent.GetPublicId())
	}
}

func TestCreateAgentRejectsClientPublicID(t *testing.T) {
	app := server.NewApp(&config.Config{}, newTestDB(t))
	_, client := newTestManagementClient(t, app)
	cookie := createAdminSession(t, client)

	req := connect.NewRequest(&p2pstreamv1.CreateAgentRequest{
		PublicId: "manual-agent",
		Name:     "Manual Agent",
		Enabled:  true,
	})
	req.Header().Set("Cookie", cookie)
	if _, err := client.CreateAgent(context.Background(), req); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("expected supplied agent id to be rejected, got %v", err)
	}
}

func TestAgentPoolBackendAPIValidationAndReadback(t *testing.T) {
	app := server.NewApp(&config.Config{}, newTestDB(t))
	_, client := newTestManagementClient(t, app)
	cookie := createAdminSession(t, client)
	agent, token := createRegisteredAgent(t, client, cookie, "api-agent", "API Agent")
	if token == "" {
		t.Fatal("expected create agent to return one-time token")
	}

	invalidReq := connect.NewRequest(&p2pstreamv1.CreatePublicBackendRequest{
		Name:         "invalid-agent-pool",
		TargetOrigin: "http://example.com",
		Enabled:      true,
		BackendType:  p2pstreamv1.PublicBackendType_PUBLIC_BACKEND_TYPE_PROXY_FORWARD,
		ForwardMode:  p2pstreamv1.PublicBackendForwardMode_PUBLIC_BACKEND_FORWARD_MODE_AGENT_POOL,
	})
	invalidReq.Header().Set("Cookie", cookie)
	if _, err := client.CreatePublicBackend(context.Background(), invalidReq); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("expected invalid agent pool error, got %v", err)
	}

	createReq := connect.NewRequest(&p2pstreamv1.CreatePublicBackendRequest{
		Name:          "api-agent-backend",
		TargetOrigin:  "http://example.com",
		Enabled:       true,
		BackendType:   p2pstreamv1.PublicBackendType_PUBLIC_BACKEND_TYPE_PROXY_FORWARD,
		ForwardMode:   p2pstreamv1.PublicBackendForwardMode_PUBLIC_BACKEND_FORWARD_MODE_AGENT_POOL,
		LoadBalancing: p2pstreamv1.PublicBackendLoadBalancing_PUBLIC_BACKEND_LOAD_BALANCING_WEIGHTED_ROUND_ROBIN,
		AgentAssignments: []*p2pstreamv1.PublicBackendAgent{
			{AgentId: agent.GetId(), Weight: 7, Enabled: true},
		},
	})
	createReq.Header().Set("Cookie", cookie)
	createResp, err := client.CreatePublicBackend(context.Background(), createReq)
	if err != nil {
		t.Fatalf("create agent backend: %v", err)
	}
	created := createResp.Msg.GetBackend()
	if created.GetForwardMode() != p2pstreamv1.PublicBackendForwardMode_PUBLIC_BACKEND_FORWARD_MODE_AGENT_POOL ||
		created.GetLoadBalancing() != p2pstreamv1.PublicBackendLoadBalancing_PUBLIC_BACKEND_LOAD_BALANCING_WEIGHTED_ROUND_ROBIN ||
		len(created.GetAgentAssignments()) != 1 ||
		created.GetAgentAssignments()[0].GetWeight() != 7 {
		t.Fatalf("unexpected created backend: %+v", created)
	}

	cfg := getPublicProxyConfig(t, client, cookie)
	if len(cfg.GetAgents()) != 1 || cfg.GetAgents()[0].GetPublicId() != agent.GetPublicId() {
		t.Fatalf("expected agent in config readback, got %+v", cfg.GetAgents())
	}
	readBack := publicBackendByName(t, cfg, "api-agent-backend")
	if len(readBack.GetAgentAssignments()) != 1 || readBack.GetAgentAssignments()[0].GetAgentId() != agent.GetId() {
		t.Fatalf("expected backend assignment readback, got %+v", readBack.GetAgentAssignments())
	}

	disableReq := connect.NewRequest(&p2pstreamv1.UpdateAgentRequest{
		Id:       agent.GetId(),
		PublicId: agent.GetPublicId(),
		Name:     agent.GetName(),
		Enabled:  false,
	})
	disableReq.Header().Set("Cookie", cookie)
	if _, err := client.UpdateAgent(context.Background(), disableReq); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("expected disabling last assigned agent to fail, got %v", err)
	}

	rotateReq := connect.NewRequest(&p2pstreamv1.RotateAgentTokenRequest{Id: agent.GetId()})
	rotateReq.Header().Set("Cookie", cookie)
	rotateResp, err := client.RotateAgentToken(context.Background(), rotateReq)
	if err != nil {
		t.Fatalf("rotate agent token: %v", err)
	}
	if rotateResp.Msg.GetToken() == "" || rotateResp.Msg.GetToken() == token {
		t.Fatalf("expected rotated token to be non-empty and different")
	}
}
