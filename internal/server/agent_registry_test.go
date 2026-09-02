package server

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/hashicorp/yamux"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/internal/config"
	"p2pstream/internal/db"
	"p2pstream/internal/tunnel"
	"p2pstream/stats"
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

func TestCreateAgentStoresSystemAndUserLabels(t *testing.T) {
	database := newAgentRegistryTestDB(t)
	app := NewApp(nil, database)
	header := createTestAdminSession(t, app)

	req := connect.NewRequest(&p2pstreamv1.CreateAgentRequest{
		Name:    "Labelled Agent",
		Enabled: true,
		Labels:  map[string]string{"site": "home", "role": "app"},
	})
	req.Header().Set("Cookie", header.Get("Cookie"))
	resp, err := app.CreateAgent(context.Background(), req)
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}

	labels := agentLabelsForTest(t, database, resp.Msg.Agent.Id)
	if labels[agentIDSystemLabelKey] != resp.Msg.Agent.PublicId {
		t.Fatalf("system label = %q, want %q (all labels=%+v)", labels[agentIDSystemLabelKey], resp.Msg.Agent.PublicId, labels)
	}
	if labels["site"] != "home" || labels["role"] != "app" {
		t.Fatalf("user labels = %+v, want site=home role=app", labels)
	}
}

func TestAgentProtoReportsLiveNegotiatedTunnelCapacity(t *testing.T) {
	database := newAgentRegistryTestDB(t)
	app := NewApp(nil, database)
	agent := createAgentRegistryTestAgent(t, database, "agent-capacity-report", "Capacity Reporter", "capacity-token")
	conn := agentRegistryTestConn(agent)
	conn.AdvertisedMaxConcurrentStreams = 512
	conn.NegotiatedMaxConcurrentStreams = 256
	if err := app.AgentHub.connect(conn); err != nil {
		t.Fatalf("connect agent: %v", err)
	}
	t.Cleanup(func() { app.AgentHub.disconnect(conn) })

	got := app.agentToProto(context.Background(), agent)
	if got.AdvertisedMaxConcurrentStreams != 512 || got.NegotiatedMaxConcurrentStreams != 256 {
		t.Fatalf("reported capacity = advertised %d negotiated %d, want 512/256", got.AdvertisedMaxConcurrentStreams, got.NegotiatedMaxConcurrentStreams)
	}
}

func TestAgentProtoDoesNotReusePreConnectionCapacityHeartbeat(t *testing.T) {
	app := NewApp(nil, nil)
	connectedAt := time.Now()
	conn := &AgentConn{AgentID: 77, PublicID: "reconnected", Done: make(chan struct{}), ConnectedAt: connectedAt, AdaptiveCapacity: true}
	if err := app.AgentHub.connect(conn); err != nil {
		t.Fatal(err)
	}
	defer app.AgentHub.disconnect(conn)
	app.storeLatestAgentStats(conn.AgentID, stats.AgentStats{
		Timestamp:              connectedAt.Add(-time.Minute),
		TunnelCapacityAdaptive: false,
		TunnelAdmissionLimit:   64,
		MemoryPressure:         "healthy",
	})
	agent := db.Agent{ID: conn.AgentID, PublicID: conn.PublicID, CreatedAt: connectedAt, UpdatedAt: connectedAt}
	if got := app.agentToProto(context.Background(), agent); got.LatestStats != nil {
		t.Fatalf("pre-connection heartbeat leaked into new adaptive session: %+v", got.LatestStats)
	}

	app.storeLatestAgentStats(conn.AgentID, stats.AgentStats{
		Timestamp:              connectedAt.Add(time.Second),
		TunnelCapacityAdaptive: true,
		TunnelAdmissionLimit:   512,
		MemoryPressure:         "healthy",
	})
	if got := app.agentToProto(context.Background(), agent); got.LatestStats == nil || got.LatestStats.TunnelAdmissionLimit != 512 {
		t.Fatalf("current-session heartbeat missing: %+v", got.LatestStats)
	}
}

func TestReportStatsPublishesAdaptivePressureWithoutChangingNegotiatedGuard(t *testing.T) {
	database := newAgentRegistryTestDB(t)
	app := NewApp(nil, database)
	agent := createAgentRegistryTestAgent(t, database, "agent-adaptive-report", "Adaptive Reporter", "adaptive-token")
	conn := agentRegistryTestConn(agent)
	conn.AdvertisedMaxConcurrentStreams = tunnel.MaxConcurrentAgentRequestsLimit
	conn.NegotiatedMaxConcurrentStreams = tunnel.MaxAdaptiveConcurrentStreamsLimit
	conn.AdaptiveCapacity = true
	conn.CurrentAdmissionLimit.Store(tunnel.MaxAdaptiveConcurrentStreamsLimit)
	if err := app.AgentHub.connect(conn); err != nil {
		t.Fatalf("connect agent: %v", err)
	}
	t.Cleanup(func() { app.AgentHub.disconnect(conn) })

	req := connect.NewRequest(&p2pstreamv1.AgentStatsRequest{
		AgentPublicId:              agent.PublicID,
		TunnelCapacityAdaptive:     true,
		TunnelAdmissionLimit:       300,
		TunnelStreamsInUse:         200,
		MemoryPressure:             "critical",
		MemoryUsageBytes:           470 << 20,
		MemoryLimitBytes:           512 << 20,
		MemorySource:               "cgroup_v2",
		TunnelPressureRejections:   7,
		FileDescriptorsUsed:        480,
		FileDescriptorsLimit:       512,
		ResourcePressureReason:     "file_descriptors",
		ResourceSampleError:        "/attacker/controlled/path: unavailable",
		ResourceLastGoodUnixMillis: time.Now().Add(-time.Minute).UnixMilli(),
	})
	req.Header().Set("Authorization", "Bearer adaptive-token")
	if _, err := app.ReportStats(context.Background(), req); err != nil {
		t.Fatalf("report adaptive stats: %v", err)
	}

	got := app.agentToProto(context.Background(), agent)
	if !got.TunnelCapacityAdaptive || got.NegotiatedMaxConcurrentStreams != tunnel.MaxAdaptiveConcurrentStreamsLimit || got.CurrentTunnelAdmissionLimit != 0 {
		t.Fatalf("adaptive agent proto = %+v", got)
	}
	if got.LatestStats == nil || got.LatestStats.MemoryPressure != "critical" || got.LatestStats.TunnelStreamsInUse != 200 || got.LatestStats.TunnelPressureRejections != 7 {
		t.Fatalf("adaptive latest stats = %+v", got.LatestStats)
	}
	if got.LatestStats.FileDescriptorsUsed != 480 || got.LatestStats.FileDescriptorsLimit != 512 || got.LatestStats.ResourcePressureReason != "file_descriptors" || got.LatestStats.ResourceSampleError != "unavailable" || got.LatestStats.ResourceLastGoodUnixMillis <= 0 {
		t.Fatalf("adaptive resource telemetry = %+v", got.LatestStats)
	}
}

func TestReportStatsRecordsAgentBuildIdentity(t *testing.T) {
	database := newAgentRegistryTestDB(t)
	app := NewApp(nil, database)
	agent := createAgentRegistryTestAgent(t, database, "agent-build-report", "Build Reporter", "build-token")

	req := connect.NewRequest(&p2pstreamv1.AgentStatsRequest{
		AgentPublicId: agent.PublicID,
		AgentVersion:  " v1.2.3\n",
		AgentCommit:   " abc123\x00 ",
	})
	req.Header().Set("Authorization", "Bearer build-token")
	if _, err := app.ReportStats(context.Background(), req); err != nil {
		t.Fatalf("report agent stats with build identity: %v", err)
	}

	stored, err := database.GetAgent(context.Background(), agent.ID)
	if err != nil {
		t.Fatalf("load agent build identity: %v", err)
	}
	if stored.AgentVersion != "v1.2.3" || stored.AgentCommit != "abc123" {
		t.Fatalf("stored agent build = %q/%q, want v1.2.3/abc123", stored.AgentVersion, stored.AgentCommit)
	}
	live := app.agentToProto(context.Background(), agent)
	if live.Version != "v1.2.3" || live.Commit != "abc123" {
		t.Fatalf("live agent build = %q/%q, want v1.2.3/abc123", live.Version, live.Commit)
	}

	restarted := NewApp(nil, database)
	persisted := restarted.agentToProto(context.Background(), stored)
	if persisted.Version != "v1.2.3" || persisted.Commit != "abc123" {
		t.Fatalf("persisted agent build = %q/%q, want v1.2.3/abc123", persisted.Version, persisted.Commit)
	}

	legacyReq := connect.NewRequest(&p2pstreamv1.AgentStatsRequest{
		AgentPublicId: agent.PublicID,
		ManagementTrustStatus: &p2pstreamv1.ManagementTrustStatus{
			AgentVersion: "v1.2.2",
		},
	})
	legacyReq.Header().Set("Authorization", "Bearer build-token")
	if _, err := app.ReportStats(context.Background(), legacyReq); err != nil {
		t.Fatalf("report legacy agent build identity: %v", err)
	}
	legacy := app.agentToProto(context.Background(), stored)
	if legacy.Version != "v1.2.2" || legacy.Commit != "" {
		t.Fatalf("legacy agent build = %q/%q, want v1.2.2/empty", legacy.Version, legacy.Commit)
	}
}

func TestCreateAgentRejectsDuplicateNormalizedLabelKeys(t *testing.T) {
	database := newAgentRegistryTestDB(t)
	app := NewApp(nil, database)
	header := createTestAdminSession(t, app)

	req := connect.NewRequest(&p2pstreamv1.CreateAgentRequest{
		Name:    "Duplicate Labels",
		Enabled: true,
		Labels:  map[string]string{" site": "home", "site": "edge"},
	})
	req.Header().Set("Cookie", header.Get("Cookie"))
	if _, err := app.CreateAgent(context.Background(), req); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("CreateAgent error = %v, want invalid argument", err)
	}
}

func TestUpdateAgentReplacesUserLabelsAndPreservesSystemLabel(t *testing.T) {
	database := newAgentRegistryTestDB(t)
	app := NewApp(nil, database)
	header := createTestAdminSession(t, app)

	createReq := connect.NewRequest(&p2pstreamv1.CreateAgentRequest{
		Name:    "Original Agent",
		Enabled: true,
		Labels:  map[string]string{"site": "home", "role": "app"},
	})
	createReq.Header().Set("Cookie", header.Get("Cookie"))
	createResp, err := app.CreateAgent(context.Background(), createReq)
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}

	updateReq := connect.NewRequest(&p2pstreamv1.UpdateAgentRequest{
		Id:      createResp.Msg.Agent.Id,
		Name:    "Updated Agent",
		Enabled: true,
		Labels:  map[string]string{"site": "edge"},
	})
	updateReq.Header().Set("Cookie", header.Get("Cookie"))
	if _, err := app.UpdateAgent(context.Background(), updateReq); err != nil {
		t.Fatalf("update agent: %v", err)
	}

	labels := agentLabelsForTest(t, database, createResp.Msg.Agent.Id)
	if labels[agentIDSystemLabelKey] != createResp.Msg.Agent.PublicId {
		t.Fatalf("system label = %q, want %q (all labels=%+v)", labels[agentIDSystemLabelKey], createResp.Msg.Agent.PublicId, labels)
	}
	if labels["site"] != "edge" {
		t.Fatalf("site label = %q, want edge (all labels=%+v)", labels["site"], labels)
	}
	if _, ok := labels["role"]; ok {
		t.Fatalf("role label was not replaced: %+v", labels)
	}
}

func TestUpdateAgentLifecycleChangePreservesLabelsUntilReplacementIsExplicit(t *testing.T) {
	database := newAgentRegistryTestDB(t)
	app := NewApp(nil, database)
	header := createTestAdminSession(t, app)

	createReq := connect.NewRequest(&p2pstreamv1.CreateAgentRequest{
		Name:    "Lifecycle Labels",
		Enabled: true,
		Labels:  map[string]string{"site": "edge", "role": "proxy"},
	})
	createReq.Header().Set("Cookie", header.Get("Cookie"))
	createResp, err := app.CreateAgent(context.Background(), createReq)
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	agentID := createResp.Msg.Agent.Id

	// This is the shape used by the enable/disable action: labels are omitted.
	disableReq := connect.NewRequest(&p2pstreamv1.UpdateAgentRequest{
		Id:      agentID,
		Name:    "Lifecycle Labels",
		Enabled: false,
	})
	disableReq.Header().Set("Cookie", header.Get("Cookie"))
	if _, err := app.UpdateAgent(context.Background(), disableReq); err != nil {
		t.Fatalf("disable agent: %v", err)
	}
	labels := agentLabelsForTest(t, database, agentID)
	if labels["site"] != "edge" || labels["role"] != "proxy" {
		t.Fatalf("lifecycle update erased user labels: %+v", labels)
	}

	clearReq := connect.NewRequest(&p2pstreamv1.UpdateAgentRequest{
		Id:            agentID,
		Name:          "Lifecycle Labels",
		Enabled:       false,
		Labels:        map[string]string{},
		ReplaceLabels: true,
	})
	clearReq.Header().Set("Cookie", header.Get("Cookie"))
	if _, err := app.UpdateAgent(context.Background(), clearReq); err != nil {
		t.Fatalf("clear agent labels: %v", err)
	}
	labels = agentLabelsForTest(t, database, agentID)
	if _, ok := labels["site"]; ok {
		t.Fatalf("explicit replacement retained site label: %+v", labels)
	}
	if _, ok := labels["role"]; ok {
		t.Fatalf("explicit replacement retained role label: %+v", labels)
	}
	if labels[agentIDSystemLabelKey] != createResp.Msg.Agent.PublicId {
		t.Fatalf("explicit replacement removed system label: %+v", labels)
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
	_ = app.agentTargetTransport(conn, publicRouteTargetConfig{
		ID:                            700,
		URL:                           "http://upstream.test:9000",
		UpstreamResponseHeaderTimeout: time.Second,
	})
	if got := app.AgentTransports.len(); got != 1 {
		t.Fatalf("pool len before rotation = %d, want 1", got)
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
	if got := app.AgentTransports.len(); got != 0 {
		t.Fatalf("pool len after rotation = %d, want 0", got)
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

func TestAgentTunnelRejectsAgentInitiatedStreams(t *testing.T) {
	database := newAgentRegistryTestDB(t)
	app := NewApp(&config.Config{ManagementUIDisabled: true}, database)
	agent := createAgentRegistryTestAgent(t, database, "agent-stream-direction", "Stream Direction", "token")
	mux := http.NewServeMux()
	app.RegisterManagementRoutes(mux)
	server := httptest.NewServer(mux)
	defer server.Close()

	agentSession, conn, err := dialAgentRegistryTestTunnel(server.URL, agent.PublicID, "token")
	if err != nil {
		t.Fatalf("dial agent tunnel: %v", err)
	}
	defer conn.Close()
	defer agentSession.Close()
	waitForAgentHubConnection(t, app, agent.ID, true)

	serverAgent := app.AgentHub.connectedByID(agent.ID)
	if serverAgent == nil || serverAgent.Session == nil {
		t.Fatal("agent tunnel session was not registered")
	}

	// A ping round trip is an ordering barrier: the agent must process the
	// server's GoAway frame before the later ping response can arrive.
	if _, err := agentSession.Ping(); err != nil {
		t.Fatalf("ping agent tunnel after server GoAway: %v", err)
	}
	if stream, err := agentSession.Open(); !errors.Is(err, yamux.ErrRemoteGoAway) {
		if stream != nil {
			_ = stream.Close()
		}
		t.Fatalf("agent-initiated stream error = %v, want %v", err, yamux.ErrRemoteGoAway)
	}
	assertYamuxStreamCounts(t, serverAgent.Session, agentSession, 0)

	acceptedCh := make(chan net.Conn, 1)
	acceptErrCh := make(chan error, 1)
	go func() {
		stream, err := agentSession.Accept()
		if err != nil {
			acceptErrCh <- err
			return
		}
		acceptedCh <- stream
	}()

	serverStream, err := serverAgent.Session.Open()
	if err != nil {
		t.Fatalf("open server-initiated stream: %v", err)
	}
	var agentStream net.Conn
	select {
	case agentStream = <-acceptedCh:
	case err := <-acceptErrCh:
		_ = serverStream.Close()
		t.Fatalf("accept server-initiated stream: %v", err)
	case <-time.After(2 * time.Second):
		_ = serverStream.Close()
		t.Fatal("timed out accepting server-initiated stream")
	}

	if err := serverStream.Close(); err != nil {
		t.Fatalf("close server stream: %v", err)
	}
	if err := agentStream.Close(); err != nil {
		t.Fatalf("close agent stream: %v", err)
	}
	assertYamuxStreamCounts(t, serverAgent.Session, agentSession, 0)
}

func TestAgentTunnelReadGateRejectsSYNQueuedBeforeGoAway(t *testing.T) {
	serverConn, rawAgentConn := newAgentRegistryLocalTCPPair(t)
	gatedConn := &agentTunnelReadGateConn{Conn: serverConn, ready: make(chan struct{})}
	t.Cleanup(gatedConn.releaseReads)
	serverSession, err := yamux.Server(gatedConn, tunnel.DefaultYamuxConfig(nil))
	if err != nil {
		t.Fatalf("create server session: %v", err)
	}
	defer serverSession.Close()

	agentSession, err := yamux.Client(rawAgentConn, tunnel.DefaultYamuxConfig(nil))
	if err != nil {
		t.Fatalf("create agent session: %v", err)
	}
	defer agentSession.Close()

	type openResult struct {
		stream net.Conn
		err    error
	}
	openCh := make(chan openResult, 1)
	go func() {
		stream, err := agentSession.Open()
		openCh <- openResult{stream: stream, err: err}
	}()

	var preGoAwayStream net.Conn
	select {
	case result := <-openCh:
		if result.err != nil {
			t.Fatalf("prebuffer pre-GoAway agent SYN: %v", result.err)
		}
		if result.stream == nil {
			t.Fatal("pre-GoAway agent SYN returned a nil stream")
		}
		preGoAwayStream = result.stream
	case <-time.After(2 * time.Second):
		t.Fatal("timed out prebuffering pre-GoAway agent SYN")
	}
	defer preGoAwayStream.Close()
	if got := serverSession.NumStreams(); got != 0 {
		t.Fatalf("server streams before releasing read gate = %d, want 0", got)
	}

	if err := serverSession.GoAway(); err != nil {
		t.Fatalf("send server GoAway: %v", err)
	}
	gatedConn.releaseReads()

	// The ping response follows the GoAway and RST frames on the server wire,
	// so its completion proves the queued SYN has been rejected and drained.
	if _, err := agentSession.Ping(); err != nil {
		t.Fatalf("ping after releasing read gate: %v", err)
	}
	if err := preGoAwayStream.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("set rejected stream deadline: %v", err)
	}
	if _, err := preGoAwayStream.Read(make([]byte, 1)); !errors.Is(err, yamux.ErrConnectionReset) {
		t.Fatalf("pre-GoAway agent stream read error = %v, want %v", err, yamux.ErrConnectionReset)
	}
	assertYamuxStreamCounts(t, serverSession, agentSession, 0)

	if stream, err := agentSession.Open(); !errors.Is(err, yamux.ErrRemoteGoAway) {
		if stream != nil {
			_ = stream.Close()
		}
		t.Fatalf("post-GoAway agent stream error = %v, want %v", err, yamux.ErrRemoteGoAway)
	}

	acceptedCh := make(chan net.Conn, 1)
	acceptErrCh := make(chan error, 1)
	go func() {
		stream, err := agentSession.Accept()
		if err != nil {
			acceptErrCh <- err
			return
		}
		acceptedCh <- stream
	}()
	serverStream, err := serverSession.Open()
	if err != nil {
		t.Fatalf("open server stream after GoAway: %v", err)
	}
	var agentStream net.Conn
	select {
	case agentStream = <-acceptedCh:
	case err := <-acceptErrCh:
		_ = serverStream.Close()
		t.Fatalf("accept server stream after GoAway: %v", err)
	case <-time.After(2 * time.Second):
		_ = serverStream.Close()
		t.Fatal("timed out accepting server stream after GoAway")
	}
	if err := serverStream.Close(); err != nil {
		t.Fatalf("close server stream after GoAway: %v", err)
	}
	if err := agentStream.Close(); err != nil {
		t.Fatalf("close agent stream after GoAway: %v", err)
	}
	assertYamuxStreamCounts(t, serverSession, agentSession, 0)
}

func TestAgentTunnelRechecksTokenBeforeRegisteringAfterRotation(t *testing.T) {
	database := newAgentRegistryTestDB(t)
	app := NewApp(&config.Config{ManagementUIDisabled: true}, database)
	header := createTestAdminSession(t, app)
	agent := createAgentRegistryTestAgent(t, database, "agent-tunnel-reauth-rotation", "Reauth Rotation", "old-token")
	mux := http.NewServeMux()
	app.RegisterManagementRoutes(mux)
	server := httptest.NewServer(mux)
	defer server.Close()

	initialAuthDone := make(chan struct{})
	releaseFinalAuth := make(chan struct{})
	var hookOnce sync.Once
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() {
		close(releaseFinalAuth)
	})
	app.agentTunnelBeforeFinalAuth = func(row db.Agent) {
		if row.ID != agent.ID {
			return
		}
		hookOnce.Do(func() {
			close(initialAuthDone)
			<-releaseFinalAuth
		})
	}

	type dialResult struct {
		session *yamux.Session
		conn    net.Conn
		err     error
	}
	dialDone := make(chan dialResult, 1)
	go func() {
		session, conn, err := dialAgentRegistryTestTunnel(server.URL, agent.PublicID, "old-token")
		dialDone <- dialResult{session: session, conn: conn, err: err}
	}()

	select {
	case <-initialAuthDone:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for tunnel initial auth")
	}

	rotateResp := rotateAgentTokenForTest(t, app, header, agent.ID)
	releaseOnce.Do(func() {
		close(releaseFinalAuth)
	})

	var result dialResult
	select {
	case result = <-dialDone:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for old-token tunnel result")
	}
	if result.session != nil {
		result.session.Close()
	}
	if result.conn != nil {
		result.conn.Close()
	}
	if result.err == nil {
		t.Fatal("old-token tunnel registered after rotation")
	}
	if !strings.Contains(result.err.Error(), "401") {
		t.Fatalf("old-token tunnel error = %v, want status 401", result.err)
	}
	if got := app.AgentHub.connectedByID(agent.ID); got != nil {
		t.Fatalf("old-token tunnel registered in hub = %#v", got)
	}

	newSession, newConn, err := dialAgentRegistryTestTunnel(server.URL, agent.PublicID, rotateResp.Msg.Token)
	if err != nil {
		t.Fatalf("new token did not connect after rotation: %v", err)
	}
	defer newConn.Close()
	defer newSession.Close()
	waitForAgentHubConnection(t, app, agent.ID, true)
}

func TestAgentTunnelFinalAuthLockReleasedAfterHijackFailure(t *testing.T) {
	database := newAgentRegistryTestDB(t)
	app := NewApp(&config.Config{ManagementUIDisabled: true}, database)
	header := createTestAdminSession(t, app)
	agent := createAgentRegistryTestAgent(t, database, "agent-tunnel-hijack-failure", "Hijack Failure", "token")

	req := httptest.NewRequest(http.MethodGet, tunnel.BootstrapPath, nil)
	req.Header.Set("X-P2PStream-Agent-ID", agent.PublicID)
	req.Header.Set("Authorization", "Bearer token")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", tunnel.UpgradeToken)
	req.Header.Set(tunnel.TunnelVersionHeader, "1")
	rw := &failingHijackResponseWriter{}

	app.agentTunnelHandler(rw, req)

	if got := app.AgentHub.connectedByID(agent.ID); got != nil {
		t.Fatalf("agent registered after hijack failure = %#v", got)
	}

	rotateDone := make(chan error, 1)
	go func() {
		rotateReq := connect.NewRequest(&p2pstreamv1.RotateAgentTokenRequest{Id: agent.ID})
		rotateReq.Header().Set("Cookie", header.Get("Cookie"))
		_, err := app.RotateAgentToken(context.Background(), rotateReq)
		rotateDone <- err
	}()

	select {
	case err := <-rotateDone:
		if err != nil {
			t.Fatalf("rotate token after hijack failure: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("rotation blocked after failed tunnel hijack")
	}
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

func TestAgentTunnelRejectsInvalidAdvertisedCapacity(t *testing.T) {
	database := newAgentRegistryTestDB(t)
	app := NewApp(&config.Config{ManagementUIDisabled: true}, database)
	agent := createAgentRegistryTestAgent(t, database, "agent-tunnel-invalid-capacity", "Invalid Capacity", "token")

	req := httptest.NewRequest(http.MethodGet, tunnel.BootstrapPath, nil)
	req.Header.Set("X-P2PStream-Agent-ID", agent.PublicID)
	req.Header.Set("Authorization", "Bearer token")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", tunnel.UpgradeToken)
	req.Header.Set(tunnel.TunnelVersionHeader, "1")
	req.Header.Set(tunnel.TunnelMaxConcurrentStreamsHeader, "0")
	rec := httptest.NewRecorder()
	app.agentTunnelHandler(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid advertised capacity status = %d, want 400", rec.Code)
	}
}

func TestAgentTunnelNegotiatesAdvertisedCapacity(t *testing.T) {
	database := newAgentRegistryTestDB(t)
	app := NewApp(&config.Config{ManagementUIDisabled: true, ServerTunnelMaxConcurrentStreams: 777}, database)
	agentRow := createAgentRegistryTestAgent(t, database, "agent-tunnel-capacity", "Capacity", "token")
	mux := http.NewServeMux()
	app.RegisterManagementRoutes(mux)
	server := httptest.NewServer(mux)
	defer server.Close()

	session, conn, err := dialAgentRegistryTestTunnelWithCapacity(server.URL, agentRow.PublicID, "token", 999)
	if err != nil {
		t.Fatalf("dial capacity tunnel: %v", err)
	}
	defer conn.Close()
	defer session.Close()
	waitForAgentHubConnection(t, app, agentRow.ID, true)

	connected := app.AgentHub.connectedByID(agentRow.ID)
	if connected == nil {
		t.Fatal("capacity tunnel was not registered")
	}
	if connected.AdvertisedMaxConcurrentStreams != 999 || connected.NegotiatedMaxConcurrentStreams != 777 {
		t.Fatalf("capacity negotiation = advertised %d negotiated %d, want 999/777", connected.AdvertisedMaxConcurrentStreams, connected.NegotiatedMaxConcurrentStreams)
	}
	snapshot := app.agentStreamCapacity.snapshot()
	if len(snapshot.SessionLimits) != 1 {
		t.Fatalf("negotiated session limits = %+v, want one entry", snapshot.SessionLimits)
	}
	for _, limit := range snapshot.SessionLimits {
		if limit != 777 {
			t.Fatalf("negotiated manager limit = %d, want 777", limit)
		}
	}
}

func TestExplicitFixedServerCapacityUsesResourceCoveredYamuxWindow(t *testing.T) {
	cfg := &config.Config{
		ServerTunnelMaxConcurrentStreams: 256,
		TunnelMaxStreamWindowBytes:       tunnel.DefaultMaxStreamWindowSizeBytes,
		ServerTunnelEstimatedStreamBytes: tunnel.DefaultAdaptiveStreamChargeBytes,
	}
	got, err := effectiveServerTunnelStreamWindowBytes(cfg)
	if err != nil {
		t.Fatal(err)
	}
	want := tunnel.DefaultAdaptiveStreamChargeBytes - tunnel.AdaptivePerStreamOverheadBytes
	if got != want {
		t.Fatalf("fixed-mode Yamux receive window = %d, want resource-covered %d", got, want)
	}
}

func TestAgentTunnelNegotiatesAdaptiveCapacityAtProtocolGuard(t *testing.T) {
	database := newAgentRegistryTestDB(t)
	app := NewApp(&config.Config{
		ManagementUIDisabled:             true,
		ServerTunnelMaxConcurrentStreams: tunnel.MaxServerConcurrentStreamsLimit,
		ServerTunnelCapacityAuto:         true,
	}, database)
	agentRow := createAgentRegistryTestAgent(t, database, "agent-tunnel-adaptive", "Adaptive", "token")
	mux := http.NewServeMux()
	app.RegisterManagementRoutes(mux)
	server := httptest.NewServer(mux)
	defer server.Close()

	session, conn, err := dialAgentRegistryTestTunnelWithCapacityMode(
		server.URL,
		agentRow.PublicID,
		"token",
		tunnel.MaxConcurrentAgentRequestsLimit,
		tunnel.TunnelCapacityModeAdaptive,
	)
	if err != nil {
		t.Fatalf("dial adaptive tunnel: %v", err)
	}
	defer conn.Close()
	defer session.Close()
	waitForAgentHubConnection(t, app, agentRow.ID, true)

	connected := app.AgentHub.connectedByID(agentRow.ID)
	if connected == nil || !connected.AdaptiveCapacity {
		t.Fatalf("adaptive tunnel connection = %#v", connected)
	}
	if connected.NegotiatedMaxConcurrentStreams != tunnel.MaxAdaptiveConcurrentStreamsLimit || connected.CurrentAdmissionLimit.Load() != 0 {
		t.Fatalf("adaptive capacity = negotiated %d current %d", connected.NegotiatedMaxConcurrentStreams, connected.CurrentAdmissionLimit.Load())
	}
}

func TestAgentTunnelOldPeerGetsLegacySessionCapacity(t *testing.T) {
	database := newAgentRegistryTestDB(t)
	app := NewApp(&config.Config{ManagementUIDisabled: true, ServerTunnelMaxConcurrentStreams: 777}, database)
	agentRow := createAgentRegistryTestAgent(t, database, "agent-tunnel-legacy-capacity", "Legacy Capacity", "token")
	mux := http.NewServeMux()
	app.RegisterManagementRoutes(mux)
	server := httptest.NewServer(mux)
	defer server.Close()

	session, conn, err := dialAgentRegistryTestTunnel(server.URL, agentRow.PublicID, "token")
	if err != nil {
		t.Fatalf("dial legacy tunnel: %v", err)
	}
	defer conn.Close()
	defer session.Close()
	waitForAgentHubConnection(t, app, agentRow.ID, true)

	connected := app.AgentHub.connectedByID(agentRow.ID)
	if connected == nil || connected.AdvertisedMaxConcurrentStreams != tunnel.DefaultMaxConcurrentAgentRequests || connected.NegotiatedMaxConcurrentStreams != tunnel.DefaultMaxConcurrentAgentRequests {
		t.Fatalf("legacy capacity negotiation = %#v, want %d", connected, tunnel.DefaultMaxConcurrentAgentRequests)
	}
}

func TestAgentTunnelReplacesDuplicateConnection(t *testing.T) {
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
	firstHubConn := app.AgentHub.connectedByID(agent.ID)
	if firstHubConn == nil {
		t.Fatal("first tunnel was not registered in the hub")
	}

	secondSession, secondConn, err := dialAgentRegistryTestTunnel(server.URL, agent.PublicID, "token")
	if err != nil {
		t.Fatalf("dial second tunnel: %v", err)
	}
	defer secondConn.Close()
	defer secondSession.Close()
	waitForAgentHubConnectionReplaced(t, app, agent.ID, firstHubConn)
	secondHubConn := app.AgentHub.connectedByID(agent.ID)
	if secondHubConn == nil {
		t.Fatal("second tunnel was not registered in the hub")
	}
	if secondHubConn == firstHubConn {
		t.Fatal("hub still points at first tunnel after replacement")
	}
	assertAgentDoneClosed(t, firstHubConn)
	assertAgentDoneOpen(t, secondHubConn)
	waitForYamuxSessionClosed(t, firstSession)
	waitForConnectionDisconnected(t, database, firstHubConn.ConnectionDBID)
	assertOnlyOpenAgentConnection(t, database, agent.ID, secondHubConn.ConnectionDBID)

	connected, err := database.GetAgent(context.Background(), agent.ID)
	if err != nil {
		t.Fatalf("get agent after reconnect: %v", err)
	}
	if !connected.LastConnectedAt.Valid {
		t.Fatal("agent last_connected_at is not set after reconnect")
	}
	if connected.LastDisconnectedAt.Valid && connected.LastDisconnectedAt.Time.After(secondHubConn.ConnectedAt) {
		t.Fatalf("agent was marked disconnected after replacement: disconnected_at=%s second_connected_at=%s", connected.LastDisconnectedAt.Time, secondHubConn.ConnectedAt)
	}
}

func TestAgentConnSignalDoneIsConcurrentAndIdempotent(t *testing.T) {
	conn := &AgentConn{Done: make(chan struct{})}
	const callers = 64
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			conn.signalDone()
		}()
	}
	close(start)
	wg.Wait()
	conn.signalDone()
	assertAgentDoneClosed(t, conn)
}

func TestAgentReplacementSerializesPriorDisconnectSideEffects(t *testing.T) {
	database := newAgentRegistryTestDB(t)
	app := NewApp(nil, database)
	agent := createAgentRegistryTestAgent(t, database, "agent-replace-serialized", "Serialized Replacement", "token")
	old := agentRegistryTestConn(agent)
	old.ConnectedAt = time.Now()
	old.ConnectionDBID = insertAgentConnectionForTest(t, database, agent.ID)
	if err := database.MarkAgentConnected(context.Background(), agent.ID); err != nil {
		t.Fatalf("mark old agent connected: %v", err)
	}
	if err := app.AgentHub.connect(old); err != nil {
		t.Fatalf("connect old agent: %v", err)
	}
	target := agentReplacementTransportTarget()
	_ = app.agentTargetTransport(old, target)

	hubRemoved := make(chan struct{})
	releaseCleanup := make(chan struct{})
	var hookOnce sync.Once
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(releaseCleanup) })
	app.agentDisconnectAfterHubRemoval = func(conn *AgentConn, disconnected bool) {
		if conn != old || !disconnected {
			return
		}
		hookOnce.Do(func() {
			close(hubRemoved)
			<-releaseCleanup
		})
	}
	cleanupDone := make(chan bool, 1)
	go func() {
		cleanupDone <- app.cleanupAgentConnection(old)
	}()
	select {
	case <-hubRemoved:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for old connection to leave hub")
	}

	app.agentAuthLocks.mu.Lock()
	lifecycleLock := app.agentAuthLocks.locks[agent.ID]
	app.agentAuthLocks.mu.Unlock()
	if lifecycleLock == nil {
		t.Fatal("agent lifecycle lock was not created")
	}
	if lifecycleLock.TryLock() {
		lifecycleLock.Unlock()
		t.Fatal("disconnect side effects did not retain the agent lifecycle lock")
	}

	type replacementResult struct {
		conn *AgentConn
		err  error
	}
	replacementStarted := make(chan struct{})
	replacementDone := make(chan replacementResult, 1)
	go func() {
		close(replacementStarted)
		unlock := app.lockAgentAuth(agent.ID)
		defer unlock()
		newConn := agentRegistryTestConn(agent)
		newConn.ConnectedAt = time.Now()
		connectionID, err := insertAgentConnection(database, agent.ID)
		if err != nil {
			replacementDone <- replacementResult{err: err}
			return
		}
		newConn.ConnectionDBID = connectionID
		if err := database.MarkAgentConnected(context.Background(), agent.ID); err != nil {
			replacementDone <- replacementResult{err: err}
			return
		}
		displaced, err := app.AgentHub.replace(newConn)
		if err != nil {
			replacementDone <- replacementResult{err: err}
			return
		}
		if len(displaced) != 0 {
			replacementDone <- replacementResult{err: fmt.Errorf("unexpected displaced connections: %d", len(displaced))}
			return
		}
		_ = app.agentTargetTransport(newConn, target)
		replacementDone <- replacementResult{conn: newConn}
	}()
	<-replacementStarted
	releaseOnce.Do(func() { close(releaseCleanup) })

	if disconnected := <-cleanupDone; !disconnected {
		t.Fatal("old current connection was not disconnected")
	}
	var result replacementResult
	select {
	case result = <-replacementDone:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for serialized replacement")
	}
	if result.err != nil {
		t.Fatalf("replace agent after disconnect: %v", result.err)
	}
	if got := app.AgentHub.connectedByID(agent.ID); got != result.conn {
		t.Fatalf("hub connection = %#v, want replacement %#v", got, result.conn)
	}
	assertAgentTransportPoolConnection(t, app.AgentTransports, result.conn)
	assertOnlyOpenAgentConnection(t, database, agent.ID, result.conn.ConnectionDBID)
	row, err := database.GetAgent(context.Background(), agent.ID)
	if err != nil {
		t.Fatalf("get agent after serialized replacement: %v", err)
	}
	if row.LastDisconnectedAt.Valid && row.LastConnectedAt.Valid && row.LastDisconnectedAt.Time.After(row.LastConnectedAt.Time) {
		t.Fatalf("agent disconnect timestamp %s is after replacement connect timestamp %s", row.LastDisconnectedAt.Time, row.LastConnectedAt.Time)
	}
}

func TestStaleAgentCleanupCannotAffectReplacementSideEffects(t *testing.T) {
	database := newAgentRegistryTestDB(t)
	app := NewApp(nil, database)
	agent := createAgentRegistryTestAgent(t, database, "agent-replace-stale-cleanup", "Stale Cleanup", "token")
	old := agentRegistryTestConn(agent)
	old.ConnectionDBID = insertAgentConnectionForTest(t, database, agent.ID)
	if err := app.AgentHub.connect(old); err != nil {
		t.Fatalf("connect old agent: %v", err)
	}
	target := agentReplacementTransportTarget()
	_ = app.agentTargetTransport(old, target)

	newConn := agentRegistryTestConn(agent)
	newConn.ConnectedAt = time.Now()
	newConn.ConnectionDBID = insertAgentConnectionForTest(t, database, agent.ID)
	if err := database.MarkAgentConnected(context.Background(), agent.ID); err != nil {
		t.Fatalf("mark replacement connected: %v", err)
	}
	unlock := app.lockAgentAuth(agent.ID)
	displaced, err := app.AgentHub.replace(newConn)
	if err != nil {
		unlock()
		t.Fatalf("replace agent: %v", err)
	}
	for _, conn := range displaced {
		app.retireDisplacedAgentConnection(conn)
	}
	_ = app.agentTargetTransport(newConn, target)
	unlock()

	if disconnected := app.cleanupAgentConnection(old); disconnected {
		t.Fatal("stale cleanup disconnected the replacement")
	}
	if got := app.AgentHub.connectedByID(agent.ID); got != newConn {
		t.Fatalf("hub connection = %#v, want replacement %#v", got, newConn)
	}
	assertAgentTransportPoolConnection(t, app.AgentTransports, newConn)
	assertOnlyOpenAgentConnection(t, database, agent.ID, newConn.ConnectionDBID)
	row, err := database.GetAgent(context.Background(), agent.ID)
	if err != nil {
		t.Fatalf("get agent after stale cleanup: %v", err)
	}
	if row.LastDisconnectedAt.Valid {
		t.Fatalf("stale cleanup marked replacement disconnected at %s", row.LastDisconnectedAt.Time)
	}
}

func insertAgentConnectionForTest(t *testing.T, database *db.DB, agentID int64) int64 {
	t.Helper()
	id, err := insertAgentConnection(database, agentID)
	if err != nil {
		t.Fatalf("insert agent connection: %v", err)
	}
	return id
}

func insertAgentConnection(database *db.DB, agentID int64) (int64, error) {
	id, err := database.InsertConnection(context.Background(), sql.NullInt64{Int64: agentID, Valid: true})
	if err != nil {
		return 0, err
	}
	return id, nil
}

func agentReplacementTransportTarget() publicRouteTargetConfig {
	return publicRouteTargetConfig{
		ID:                            900,
		URL:                           "http://replacement.test:9000",
		UpstreamResponseHeaderTimeout: time.Second,
	}
}

func assertAgentTransportPoolConnection(t *testing.T, pool *agentTransportPool, want *AgentConn) {
	t.Helper()
	pool.mu.Lock()
	defer pool.mu.Unlock()
	if len(pool.entries) != 1 {
		t.Fatalf("agent transport entries = %d, want 1", len(pool.entries))
	}
	for _, entry := range pool.entries {
		if entry == nil || entry.agent != want {
			t.Fatalf("pooled transport agent = %#v, want %#v", entry, want)
		}
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

func agentLabelsForTest(t *testing.T, database *db.DB, agentID int64) map[string]string {
	t.Helper()
	rows, err := database.ListAgentLabelsByAgent(context.Background(), agentID)
	if err != nil {
		t.Fatalf("list agent labels: %v", err)
	}
	labels := make(map[string]string, len(rows))
	for _, row := range rows {
		labels[row.Key] = row.Value
	}
	return labels
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
	return dialAgentRegistryTestTunnelWithCapacity(serverURL, publicID, token, 0)
}

func dialAgentRegistryTestTunnelWithCapacity(serverURL string, publicID string, token string, advertisedCapacity int64) (*yamux.Session, net.Conn, error) {
	return dialAgentRegistryTestTunnelWithCapacityMode(serverURL, publicID, token, advertisedCapacity, "")
}

func dialAgentRegistryTestTunnelWithCapacityMode(serverURL string, publicID string, token string, advertisedCapacity int64, capacityMode string) (*yamux.Session, net.Conn, error) {
	parsed, err := url.Parse(serverURL)
	if err != nil {
		return nil, nil, err
	}
	conn, err := net.DialTimeout("tcp", parsed.Host, 2*time.Second)
	if err != nil {
		return nil, nil, err
	}
	capacityHeader := ""
	if advertisedCapacity > 0 {
		capacityHeader = fmt.Sprintf("%s: %d\r\n", tunnel.TunnelMaxConcurrentStreamsHeader, advertisedCapacity)
	}
	capacityModeHeader := ""
	if capacityMode != "" {
		capacityModeHeader = fmt.Sprintf("%s: %s\r\n", tunnel.TunnelCapacityModeHeader, capacityMode)
	}
	req := fmt.Sprintf(
		"GET %s HTTP/1.1\r\nHost: %s\r\nConnection: Upgrade\r\nUpgrade: %s\r\n%s: 1\r\n%s%sX-P2PStream-Agent-ID: %s\r\nAuthorization: Bearer %s\r\n\r\n",
		tunnel.BootstrapPath,
		parsed.Host,
		tunnel.UpgradeToken,
		tunnel.TunnelVersionHeader,
		capacityHeader,
		capacityModeHeader,
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
	bufferedConn := &agentRegistryBufferedConn{Conn: conn, reader: reader}
	session, err := yamux.Client(bufferedConn, tunnel.DefaultYamuxConfig(nil))
	if err != nil {
		conn.Close()
		return nil, nil, err
	}
	return session, bufferedConn, nil
}

type agentRegistryBufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *agentRegistryBufferedConn) Read(p []byte) (int, error) {
	return c.reader.Read(p)
}

func newAgentRegistryLocalTCPPair(t *testing.T) (net.Conn, net.Conn) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for local TCP pair: %v", err)
	}
	defer listener.Close()

	type acceptResult struct {
		conn net.Conn
		err  error
	}
	acceptedCh := make(chan acceptResult, 1)
	go func() {
		conn, err := listener.Accept()
		acceptedCh <- acceptResult{conn: conn, err: err}
	}()

	agentConn, err := net.DialTimeout("tcp", listener.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatalf("dial local TCP pair: %v", err)
	}
	select {
	case result := <-acceptedCh:
		if result.err != nil {
			_ = agentConn.Close()
			t.Fatalf("accept local TCP pair: %v", result.err)
		}
		return result.conn, agentConn
	case <-time.After(2 * time.Second):
		_ = agentConn.Close()
		t.Fatal("timed out accepting local TCP pair")
		return nil, nil
	}
}

func assertYamuxStreamCounts(t *testing.T, serverSession *yamux.Session, agentSession *yamux.Session, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if serverSession.NumStreams() == want && agentSession.NumStreams() == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf(
		"yamux stream counts = server %d/agent %d, want %d/%d",
		serverSession.NumStreams(),
		agentSession.NumStreams(),
		want,
		want,
	)
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

func waitForAgentHubConnectionReplaced(t *testing.T, app *App, agentID int64, old *AgentConn) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		connected := app.AgentHub.connectedByID(agentID)
		if connected != nil && connected != old {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("agent hub connection was not replaced")
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

func assertOnlyOpenAgentConnection(t *testing.T, database *db.DB, agentID int64, wantConnID int64) {
	t.Helper()
	rows, err := database.QueryContext(context.Background(), `SELECT id FROM connections WHERE agent_id = ? AND disconnected_at IS NULL`, agentID)
	if err != nil {
		t.Fatalf("list open agent connections: %v", err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan open connection id: %v", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate open connections: %v", err)
	}
	if len(ids) != 1 || ids[0] != wantConnID {
		t.Fatalf("open connection ids = %+v, want [%d]", ids, wantConnID)
	}
}

func waitForYamuxSessionClosed(t *testing.T, session *yamux.Session) {
	t.Helper()
	select {
	case <-session.CloseChan():
	case <-time.After(2 * time.Second):
		t.Fatal("yamux session did not close")
	}
}

type failingHijackResponseWriter struct {
	header http.Header
	status int
	body   strings.Builder
}

func (w *failingHijackResponseWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}

func (w *failingHijackResponseWriter) WriteHeader(status int) {
	w.status = status
}

func (w *failingHijackResponseWriter) Write(p []byte) (int, error) {
	return w.body.Write(p)
}

func (w *failingHijackResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return nil, nil, errors.New("hijack failed")
}
