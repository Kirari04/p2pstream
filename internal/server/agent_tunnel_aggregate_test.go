package server

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"p2pstream/internal/config"
	"p2pstream/internal/tunnel"
)

func newAggregateTunnelRequest(t *testing.T, publicID, token, group string, lane int, aggregate bool) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, tunnel.BootstrapPath, nil)
	req.Header.Set("X-P2PStream-Agent-ID", publicID)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", tunnel.UpgradeToken)
	req.Header.Set(tunnel.TunnelVersionHeader, strconv.Itoa(tunnel.ProtocolVersion))
	req.Header.Set(tunnel.TunnelMaxConcurrentStreamsHeader, "64")
	req.Header.Set(tunnel.TunnelCapacityModeHeader, tunnel.TunnelCapacityModeFixed)
	req.Header.Set(tunnel.TunnelGroupHeader, group)
	req.Header.Set(tunnel.TunnelLaneHeader, strconv.Itoa(lane))
	if aggregate {
		role := tunnel.TunnelLaneRoleControl
		if lane > 0 {
			role = tunnel.TunnelLaneRoleBulk
		}
		tunnel.SetAggregateFlowControlHeaders(req.Header, role, tunnel.DefaultAggregateFlowControlConfig())
	}
	return req
}

func TestAgentTunnelAggregateMaximumLaneAndLegacyBoundary(t *testing.T) {
	database := newAgentRegistryTestDB(t)
	app := NewApp(&config.Config{ManagementUIDisabled: true}, database)
	agentRow := createAgentRegistryTestAgent(t, database, "aggregate-lane-boundary", "Aggregate Boundary", "token")
	group := "0123456789abcdef"

	aggregate := newAggregateTunnelRequest(t, agentRow.PublicID, "token", group, tunnel.MaxParallelTunnels, true)
	aggregateRec := &failingHijackResponseWriter{}
	app.agentTunnelHandler(aggregateRec, aggregate)
	if aggregateRec.status != http.StatusConflict {
		t.Fatalf("maximum aggregate lane status = %d, want group-order conflict", aggregateRec.status)
	}

	legacy := newAggregateTunnelRequest(t, agentRow.PublicID, "token", group, tunnel.MaxParallelTunnels, false)
	legacyRec := &failingHijackResponseWriter{}
	app.agentTunnelHandler(legacyRec, legacy)
	if legacyRec.status != http.StatusBadRequest {
		t.Fatalf("legacy aggregate-only lane status = %d, want 400", legacyRec.status)
	}

	over := newAggregateTunnelRequest(t, agentRow.PublicID, "token", group, tunnel.MaxParallelTunnels+1, true)
	overRec := &failingHijackResponseWriter{}
	app.agentTunnelHandler(overRec, over)
	if overRec.status != http.StatusBadRequest {
		t.Fatalf("lane beyond aggregate maximum status = %d, want 400", overRec.status)
	}
}

func TestAgentTunnelRejectsDuplicateAndCrossGenerationCapabilityHeaders(t *testing.T) {
	database := newAgentRegistryTestDB(t)
	app := NewApp(&config.Config{ManagementUIDisabled: true}, database)
	agentRow := createAgentRegistryTestAgent(t, database, "aggregate-generation", "Aggregate Generation", "token")
	group := "0123456789abcdef"

	duplicate := newAggregateTunnelRequest(t, agentRow.PublicID, "token", group, 0, true)
	duplicate.Header.Add(tunnel.TunnelGroupHeader, group)
	duplicateRec := &failingHijackResponseWriter{}
	app.agentTunnelHandler(duplicateRec, duplicate)
	if duplicateRec.status != http.StatusBadRequest {
		t.Fatalf("duplicate group status = %d, want 400", duplicateRec.status)
	}

	current := &AgentConn{
		AgentID:                        agentRow.ID,
		PublicID:                       agentRow.PublicID,
		TunnelGroup:                    group,
		AggregateFlowControl:           true,
		NegotiatedMaxConcurrentStreams: 64,
		Done:                           make(chan struct{}),
	}
	if _, err := app.AgentHub.replace(current); err != nil {
		t.Fatal(err)
	}
	legacyLateLane := newAggregateTunnelRequest(t, agentRow.PublicID, "token", group, 1, false)
	legacyRec := &failingHijackResponseWriter{}
	app.agentTunnelHandler(legacyRec, legacyLateLane)
	if legacyRec.status != http.StatusConflict {
		t.Fatalf("late legacy lane status = %d, want capability conflict", legacyRec.status)
	}
}
