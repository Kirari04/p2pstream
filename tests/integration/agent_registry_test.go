package integration_test

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
	app := server.NewApp(testManagementConfig(config.Config{}), newTestDB(t))
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

func TestCreateAgentRejectsReservedUserLabel(t *testing.T) {
	app := server.NewApp(testManagementConfig(config.Config{}), newTestDB(t))
	_, client := newTestManagementClient(t, app)
	cookie := createAdminSession(t, client)

	req := connect.NewRequest(&p2pstreamv1.CreateAgentRequest{
		Name:    "Manual Agent",
		Enabled: true,
		Labels:  map[string]string{"p2pstream.io/agent-id": "manual-agent"},
	})
	req.Header().Set("Cookie", cookie)
	if _, err := client.CreateAgent(context.Background(), req); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("expected reserved label to be rejected, got %v", err)
	}
}

func TestAgentLabelsCreateUpdateAndPreserveSystemLabel(t *testing.T) {
	app := server.NewApp(testManagementConfig(config.Config{}), newTestDB(t))
	_, client := newTestManagementClient(t, app)
	cookie := createAdminSession(t, client)

	createReq := connect.NewRequest(&p2pstreamv1.CreateAgentRequest{
		Name:    "Labelled Agent",
		Enabled: true,
		Labels:  map[string]string{"site": "home", "role": ""},
	})
	createReq.Header().Set("Cookie", cookie)
	createResp, err := client.CreateAgent(context.Background(), createReq)
	if err != nil {
		t.Fatalf("create labelled agent: %v", err)
	}
	agent := createResp.Msg.GetAgent()
	if got := agent.GetLabels()["site"]; got != "home" {
		t.Fatalf("site label = %q, want home", got)
	}
	if got, ok := agent.GetLabels()["role"]; !ok || got != "" {
		t.Fatalf("empty role label = %q present=%v, want present empty", got, ok)
	}
	if got := agent.GetLabels()["p2pstream.io/agent-id"]; got != agent.GetPublicId() {
		t.Fatalf("system label = %q, want public id %q", got, agent.GetPublicId())
	}

	updateReq := connect.NewRequest(&p2pstreamv1.UpdateAgentRequest{
		Id:      agent.GetId(),
		Name:    "Labelled Agent Updated",
		Enabled: true,
		Labels:  map[string]string{"site": "office"},
	})
	updateReq.Header().Set("Cookie", cookie)
	updateResp, err := client.UpdateAgent(context.Background(), updateReq)
	if err != nil {
		t.Fatalf("update labelled agent: %v", err)
	}
	updated := updateResp.Msg.GetAgent()
	if got := updated.GetLabels()["site"]; got != "office" {
		t.Fatalf("updated site label = %q, want office", got)
	}
	if _, ok := updated.GetLabels()["role"]; ok {
		t.Fatalf("role label was not removed: %+v", updated.GetLabels())
	}
	if got := updated.GetLabels()["p2pstream.io/agent-id"]; got != agent.GetPublicId() {
		t.Fatalf("system label after update = %q, want public id %q", got, agent.GetPublicId())
	}
}

func TestAgentSelectorRouteTargetAPIValidationAndReadback(t *testing.T) {
	app := server.NewApp(testManagementConfig(config.Config{}), newTestDB(t))
	_, client := newTestManagementClient(t, app)
	cookie := createAdminSession(t, client)
	agent, token := createRegisteredAgent(t, client, cookie, "api-agent", "API Agent")
	if token == "" {
		t.Fatal("expected create agent to return one-time token")
	}
	labelReq := connect.NewRequest(&p2pstreamv1.UpdateAgentRequest{
		Id:      agent.GetId(),
		Name:    agent.GetName(),
		Enabled: true,
		Labels:  map[string]string{"region": "api", "role": "edge"},
	})
	labelReq.Header().Set("Cookie", cookie)
	if _, err := client.UpdateAgent(context.Background(), labelReq); err != nil {
		t.Fatalf("label agent: %v", err)
	}

	cfg := getPublicProxyConfig(t, client, cookie)
	listener := publicListenerByName(t, cfg, "public-http")

	invalidReq := connect.NewRequest(&p2pstreamv1.CreatePublicRouteRequest{
		ListenerId: listener.GetId(),
		Priority:   10,
		PathPrefix: "/invalid-agent-target",
		Enabled:    true,
		Targets: []*p2pstreamv1.PublicRouteTarget{{
			Name:       "invalid-agent-target",
			TargetType: p2pstreamv1.PublicRouteTargetType_PUBLIC_ROUTE_TARGET_TYPE_PROXY,
			Url:        "http://example.com",
			Transport:  p2pstreamv1.PublicRouteTargetTransport_PUBLIC_ROUTE_TARGET_TRANSPORT_AGENT,
			Enabled:    true,
		}},
	})
	invalidReq.Header().Set("Cookie", cookie)
	if _, err := client.CreatePublicRoute(context.Background(), invalidReq); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("expected invalid agent target error, got %v", err)
	}

	createReq := connect.NewRequest(&p2pstreamv1.CreatePublicRouteRequest{
		ListenerId:          listener.GetId(),
		Priority:            20,
		PathPrefix:          "/api-agent-target",
		Enabled:             true,
		TargetLoadBalancing: p2pstreamv1.PublicRouteTargetLoadBalancing_PUBLIC_ROUTE_TARGET_LOAD_BALANCING_WEIGHTED_ROUND_ROBIN,
		Targets: []*p2pstreamv1.PublicRouteTarget{{
			Name:       "api-agent-target",
			TargetType: p2pstreamv1.PublicRouteTargetType_PUBLIC_ROUTE_TARGET_TYPE_PROXY,
			Url:        "http://example.com",
			Transport:  p2pstreamv1.PublicRouteTargetTransport_PUBLIC_ROUTE_TARGET_TRANSPORT_AGENT,
			AgentSelector: &p2pstreamv1.PublicAgentSelector{MatchLabels: map[string]string{
				"region": "api",
				"role":   "edge",
			}},
			AgentLoadBalancing: p2pstreamv1.PublicRouteTargetLoadBalancing_PUBLIC_ROUTE_TARGET_LOAD_BALANCING_WEIGHTED_ROUND_ROBIN,
			Weight:             7,
			Enabled:            true,
		}},
	})
	createReq.Header().Set("Cookie", cookie)
	createResp, err := client.CreatePublicRoute(context.Background(), createReq)
	if err != nil {
		t.Fatalf("create agent target route: %v", err)
	}
	created := createResp.Msg.GetRoute()
	if created.GetTargetLoadBalancing() != p2pstreamv1.PublicRouteTargetLoadBalancing_PUBLIC_ROUTE_TARGET_LOAD_BALANCING_WEIGHTED_ROUND_ROBIN ||
		len(created.GetTargets()) != 1 ||
		created.GetTargets()[0].GetWeight() != 7 ||
		created.GetTargets()[0].GetTransport() != p2pstreamv1.PublicRouteTargetTransport_PUBLIC_ROUTE_TARGET_TRANSPORT_AGENT {
		t.Fatalf("unexpected created route target: %+v", created)
	}

	cfg = getPublicProxyConfig(t, client, cookie)
	if len(cfg.GetAgents()) != 1 || cfg.GetAgents()[0].GetPublicId() != agent.GetPublicId() {
		t.Fatalf("expected agent in config readback, got %+v", cfg.GetAgents())
	}
	readBack := publicRouteTargetByName(t, cfg, "api-agent-target")
	if readBack.GetAgentSelector().GetMatchLabels()["region"] != "api" ||
		readBack.GetAgentSelector().GetMatchLabels()["role"] != "edge" {
		t.Fatalf("expected target selector readback, got %+v", readBack.GetAgentSelector())
	}

	exactReq := connect.NewRequest(&p2pstreamv1.CreatePublicRouteRequest{
		ListenerId: listener.GetId(),
		Priority:   30,
		PathPrefix: "/exact-agent-target",
		Enabled:    true,
		Targets: []*p2pstreamv1.PublicRouteTarget{{
			Name:       "exact-agent-target",
			TargetType: p2pstreamv1.PublicRouteTargetType_PUBLIC_ROUTE_TARGET_TYPE_PROXY,
			Url:        "http://example.com",
			Transport:  p2pstreamv1.PublicRouteTargetTransport_PUBLIC_ROUTE_TARGET_TRANSPORT_AGENT,
			AgentSelector: &p2pstreamv1.PublicAgentSelector{MatchLabels: map[string]string{
				"p2pstream.io/agent-id": agent.GetPublicId(),
			}},
			Enabled: true,
		}},
	})
	exactReq.Header().Set("Cookie", cookie)
	if _, err := client.CreatePublicRoute(context.Background(), exactReq); err != nil {
		t.Fatalf("create exact-agent target route: %v", err)
	}

	disableReq := connect.NewRequest(&p2pstreamv1.UpdateAgentRequest{
		Id:      agent.GetId(),
		Name:    agent.GetName(),
		Enabled: false,
		Labels:  map[string]string{"region": "api", "role": "edge"},
	})
	disableReq.Header().Set("Cookie", cookie)
	if _, err := client.UpdateAgent(context.Background(), disableReq); err != nil {
		t.Fatalf("disable labelled agent: %v", err)
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
