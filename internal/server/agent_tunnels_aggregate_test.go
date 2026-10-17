package server

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/hashicorp/yamux"

	"p2pstream/internal/tunnel"
)

func newAgentTunnelTestSession(t *testing.T) (*yamux.Session, *yamux.Session) {
	t.Helper()
	left, right := net.Pipe()
	config := tunnel.DefaultYamuxConfig(nil)
	client, err := yamux.Client(left, config)
	if err != nil {
		t.Fatal(err)
	}
	server, err := yamux.Server(right, tunnel.DefaultYamuxConfig(nil))
	if err != nil {
		client.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		client.Close()
		server.Close()
	})
	return client, server
}

func newAggregateAgentTunnelTestSession(t *testing.T) (*yamux.Session, *yamux.Session) {
	t.Helper()
	left, right := net.Pipe()
	clientConfig, err := tunnel.NewAggregateYamuxConfig(nil, tunnel.DefaultMaxStreamWindowSizeBytes, 64)
	if err != nil {
		t.Fatal(err)
	}
	serverConfig, err := tunnel.NewAggregateYamuxConfig(nil, tunnel.DefaultMaxStreamWindowSizeBytes, -1)
	if err != nil {
		t.Fatal(err)
	}
	client, err := yamux.Client(left, clientConfig)
	if err != nil {
		t.Fatal(err)
	}
	server, err := yamux.Server(right, serverConfig)
	if err != nil {
		client.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		client.Close()
		server.Close()
	})
	return client, server
}

func TestAggregateTunnelSelectionExcludesControlAndSupportsMaximumBulkLane(t *testing.T) {
	control, _ := newAgentTunnelTestSession(t)
	bulkOne, _ := newAggregateAgentTunnelTestSession(t)
	bulkMax, _ := newAggregateAgentTunnelTestSession(t)
	agent := &AgentConn{AggregateFlowControl: true, Done: make(chan struct{})}
	poolBytes := int64(yamux.AggregateReceiveWindowMemory(tunnel.AggregateInitialSessionWindowBytes))
	agent.installTunnel(0, control, agentTunnelOwnedResource{LegacyControlSupplementBytes: 100})
	agent.installTunnel(1, bulkOne, agentTunnelOwnedResource{AggregateInitialPoolBytes: poolBytes, SessionMetadataBytes: 20})
	agent.installTunnel(tunnel.MaxParallelTunnels, bulkMax, agentTunnelOwnedResource{AggregateInitialPoolBytes: poolBytes, SessionMetadataBytes: 30})

	if got := agent.transportLane(); got != 1 {
		t.Fatalf("first bulk lane = %d, want 1", got)
	}
	if got := agent.transportLane(); got != tunnel.MaxParallelTunnels {
		t.Fatalf("second bulk lane = %d, want %d", got, tunnel.MaxParallelTunnels)
	}
	if got := agent.transportLane(); got != 1 {
		t.Fatalf("rotated bulk lane = %d, want 1", got)
	}
	if got := agent.tunnelSession(0); got != control {
		t.Fatal("control lane lookup did not return lane zero")
	}
	if got := agent.tunnelSession(2); got != nil {
		t.Fatal("missing bulk lane fell back across lane boundary")
	}
	snapshot := agent.tunnelResourceSnapshot()
	if !snapshot.AggregateFlowControl || !snapshot.ControlConnected || snapshot.BulkSessions != 2 ||
		snapshot.AggregateInitialPoolBytes != 2*poolBytes || snapshot.AggregateCurrentPoolBytes != 2*poolBytes ||
		snapshot.SessionMetadataBytes != 50 || snapshot.LegacyControlSupplementBytes != 100 || snapshot.SessionResourceBytes != 2*poolBytes+150 {
		t.Fatalf("tunnel resource snapshot = %+v", snapshot)
	}
}

func TestLegacyTunnelSelectionStillFallsBackAcrossLegacyLanes(t *testing.T) {
	lane, _ := newAgentTunnelTestSession(t)
	agent := &AgentConn{Done: make(chan struct{})}
	agent.installTunnel(2, lane)
	if got := agent.tunnelSession(0); got != lane {
		t.Fatal("legacy missing lane no longer falls back")
	}
}

func TestAggregateTunnelSelectionNeverUsesCompatibilitySessionBeforeLaneInstall(t *testing.T) {
	legacySession, _ := newAgentTunnelTestSession(t)
	agent := &AgentConn{AggregateFlowControl: true, Session: legacySession, Done: make(chan struct{})}
	if got := agent.transportLane(); got != -1 {
		t.Fatalf("uninitialized aggregate lane = %d, want no public lane", got)
	}
	if got := agent.tunnelSession(0); got != nil {
		t.Fatal("uninitialized aggregate tunnel used compatibility session as control lane")
	}
}

func TestAggregateControlGateBoundsLifetimePromises(t *testing.T) {
	agent := &AgentConn{AggregateFlowControl: true, Done: make(chan struct{})}
	releases := make([]func(), 0, tunnel.NegotiatedControlStreamLimit)
	for range tunnel.NegotiatedControlStreamLimit {
		release, ok := agent.acquireControlStream(t.Context())
		if !ok {
			t.Fatal("control gate rejected within negotiated limit")
		}
		releases = append(releases, release)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	if _, ok := agent.acquireControlStream(ctx); ok {
		t.Fatal("control gate admitted a fifth stream")
	}
	releases[0]()
	if release, ok := agent.acquireControlStream(t.Context()); !ok {
		t.Fatal("control gate did not recover after lifetime release")
	} else {
		release()
	}
	for _, release := range releases[1:] {
		release()
	}
}

func newSaturatedAggregateControlAgent(t *testing.T) *AgentConn {
	t.Helper()
	control, _ := newAgentTunnelTestSession(t)
	agent := &AgentConn{
		AgentID:              7,
		PublicID:             "aggregate-health",
		Name:                 "Aggregate Health",
		AggregateFlowControl: true,
		Done:                 make(chan struct{}),
	}
	agent.installTunnel(0, control)
	releases := make([]func(), 0, tunnel.NegotiatedControlStreamLimit)
	for range tunnel.NegotiatedControlStreamLimit {
		release, ok := agent.acquireControlStream(t.Context())
		if !ok {
			t.Fatal("failed to saturate aggregate control gate")
		}
		releases = append(releases, release)
	}
	t.Cleanup(func() {
		for _, release := range releases {
			release()
		}
	})
	return agent
}

func TestAggregateControlAdmissionPreservesCallerCancellation(t *testing.T) {
	agent := newSaturatedAggregateControlAgent(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := (&App{}).dialViaAgentWithCapacity(ctx, agent, "tcp", "example.test:443", "health-cancel", agentStreamCapacityTrustedHealth, "health")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled control admission error = %v, want context.Canceled", err)
	}
	var dialErr agentDialError
	if errors.As(err, &dialErr) {
		t.Fatalf("cancelled control admission was reclassified as %q", dialErr.Kind)
	}
}

func TestAggregateControlAdmissionPreservesAgentDisconnect(t *testing.T) {
	agent := newSaturatedAggregateControlAgent(t)
	close(agent.Done)

	_, err := (&App{}).dialViaAgentWithCapacity(t.Context(), agent, "tcp", "example.test:443", "health-disconnect", agentStreamCapacityTrustedHealth, "health")
	if !errors.Is(err, errAgentDisconnected) {
		t.Fatalf("disconnected control admission error = %v, want errAgentDisconnected", err)
	}
}

func TestAggregateControlAdmissionCallerDeadlineRemainsHealthCapacity(t *testing.T) {
	agent := newSaturatedAggregateControlAgent(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()

	_, err := (&App{}).dialViaAgentWithCapacity(ctx, agent, "tcp", "example.test:443", "health-deadline", agentStreamCapacityTrustedHealth, "health")
	var dialErr agentDialError
	if !errors.As(err, &dialErr) || dialErr.Kind != "server_health_capacity" {
		t.Fatalf("deadline control admission error = %v, want server_health_capacity", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline control admission lost context cause: %v", err)
	}
	if !agentDialErrorIsAdmissionPressure(dialErr) || shouldMarkAgentPassiveFailure(ctx, err) {
		t.Fatalf("deadline control admission can poison health: %v", err)
	}
}

func TestAggregateExplicitLaneCannotCrossTrafficClassBoundary(t *testing.T) {
	control, _ := newAgentTunnelTestSession(t)
	bulk, _ := newAgentTunnelTestSession(t)
	agent := &AgentConn{AggregateFlowControl: true, Done: make(chan struct{})}
	agent.installTunnel(0, control)
	agent.installTunnel(1, bulk)
	app := &App{}

	if _, err := app.dialViaAgentWithCapacity(t.Context(), agent, "tcp", "example.test:443", "public", agentStreamCapacityPublicOneShot, "public", 0); !errors.Is(err, errAgentDisconnected) {
		t.Fatalf("public explicit control lane error = %v", err)
	}
	if _, err := app.dialViaAgentWithCapacity(t.Context(), agent, "tcp", "example.test:443", "health", agentStreamCapacityTrustedHealth, "health", 1); !errors.Is(err, errAgentDisconnected) {
		t.Fatalf("health explicit bulk lane error = %v", err)
	}
}

func TestAggregatePrimaryReplacementKeepsGenerationRegistrationUntilLastBulkLane(t *testing.T) {
	primary, _ := newAgentTunnelTestSession(t)
	replacement, _ := newAgentTunnelTestSession(t)
	bulk, _ := newAgentTunnelTestSession(t)
	app := &App{
		AgentHub:            newAgentHub(),
		AgentTransports:     newAgentTransportPool(),
		agentStreamCapacity: mustNewDefaultAgentStreamCapacityManager(32),
		agentAuthLocks:      newAgentAuthLockMap(),
	}
	agent := &AgentConn{AgentID: 1, PublicID: "aggregate-generation", AggregateFlowControl: true, Done: make(chan struct{})}
	agent.Session = primary
	agent.installTunnel(0, primary, agentTunnelOwnedResource{LegacyControlSupplementBytes: 100})
	agent.installTunnel(1, bulk, agentTunnelOwnedResource{AggregateInitialPoolBytes: 200})
	key := agentStreamCapacitySessionKey(agent, primary)
	app.agentStreamCapacity.registerSessionWithLimit(key, 17)
	if _, err := app.AgentHub.replace(agent); err != nil {
		t.Fatal(err)
	}

	old := agent.installTunnel(0, replacement, agentTunnelOwnedResource{LegacyControlSupplementBytes: 300})
	if old != primary {
		t.Fatal("primary replacement did not return displaced session")
	}
	_ = primary.Close()
	app.cleanupAgentTunnel(agent, 0, primary)
	snapshot := app.agentStreamCapacity.snapshot()
	if snapshot.RegisteredSessions != 1 || snapshot.SessionLimits[key] != 17 {
		t.Fatalf("displaced primary removed generation registration: %+v", snapshot.SessionLimits)
	}

	_ = replacement.Close()
	app.cleanupAgentTunnel(agent, 0, replacement)
	snapshot = app.agentStreamCapacity.snapshot()
	if snapshot.RegisteredSessions != 1 || snapshot.SessionLimits[key] != 17 {
		t.Fatalf("primary loss with bulk surviving removed registration: %+v", snapshot.SessionLimits)
	}

	_ = bulk.Close()
	app.cleanupAgentTunnel(agent, 1, bulk)
	snapshot = app.agentStreamCapacity.snapshot()
	if snapshot.RegisteredSessions != 0 {
		t.Fatalf("last bulk cleanup retained %d generation registrations", snapshot.RegisteredSessions)
	}
}
