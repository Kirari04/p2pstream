package server

import (
	"context"
	"io"
	"net/http"
	"testing"

	"connectrpc.com/connect"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/gen/proto/p2pstream/v1/p2pstreamv1connect"
	"p2pstream/internal/db"
	"p2pstream/internal/tunnel"
)

type publicAgentTokenRotationInvoker func(context.Context, *publicReconnectFixture, *connect.Request[p2pstreamv1.RotateAgentTokenRequest]) (*connect.Response[p2pstreamv1.RotateAgentTokenResponse], error)

func TestPublicAgentTokenRotationTerminatesCommittedResponseAndFencesReconnect(t *testing.T) {
	publicAgentTokenRotation(t, "service", "direct", func(ctx context.Context, f *publicReconnectFixture, request *connect.Request[p2pstreamv1.RotateAgentTokenRequest]) (*connect.Response[p2pstreamv1.RotateAgentTokenResponse], error) {
		return f.app.RotateAgentToken(ctx, request)
	})
}

func TestPublicAgentTokenRotationOverManagementHTTP(t *testing.T) {
	for _, mode := range []string{"protobuf", "json"} {
		t.Run(mode, func(t *testing.T) {
			publicAgentTokenRotation(t, "https", mode, func(ctx context.Context, f *publicReconnectFixture, request *connect.Request[p2pstreamv1.RotateAgentTokenRequest]) (*connect.Response[p2pstreamv1.RotateAgentTokenResponse], error) {
				client := f.management.Client()
				defer client.CloseIdleConnections()
				var options []connect.ClientOption
				contentType := "application/proto"
				if mode == "json" {
					options = append(options, connect.WithProtoJSON())
					contentType = "application/json"
				}
				management := p2pstreamv1connect.NewAgentManagementServiceClient(client, f.management.URL, options...)
				rotated, err := management.RotateAgentToken(ctx, request)
				if err == nil && rotated.Header().Get("Content-Type") != contentType {
					t.Fatalf("management rotation %s Content-Type=%q want%q", mode, rotated.Header().Get("Content-Type"), contentType)
				}
				return rotated, err
			})
		})
	}
}

func publicAgentTokenRotation(t *testing.T, transport, mode string, rotate publicAgentTokenRotationInvoker) {
	t.Helper()
	f := newPublicReconnectFixture(t)
	defer func() {
		if t.Failed() {
			f.logOwnership(t, "public-token-rotation-failure")
		}
	}()
	// Configure this fixture before any origin traffic. Its existing event
	// oracle already distinguishes the revoked old-generation response from
	// the successful replacement response; unrelated scenario gates stay unused.
	for name := range f.gates {
		if name != "old-generation" && name != "replacement-progress" {
			delete(f.gates, name)
		}
	}
	admin := createTestAdminSession(t, f.app)
	oldRuntime := f.startRuntime(t, "reconnect-token-1")
	old := f.awaitGeneration(t, nil)
	f.remember(old)
	oldLanes := reconnectRawLanes(old)
	oldKey := agentStreamCapacitySessionKey(old, old.tunnelSession(0))
	if len(oldLanes) != 3 || old.ConnectionDBID <= 0 {
		t.Fatal("initial authenticated control/two-bulk generation is incomplete")
	}
	f.startPublic(t, old)
	held := f.startRequest(t, "old-generation", 0)
	owner := f.heldOwner(t, old, 0)
	f.assertCapacity(t, oldKey, 1)
	f.assertHeld(t, held)
	if owner.session != oldLanes[owner.lane] || owner.leaseID == 0 {
		t.Fatal("committed public response did not retain its original physical owner")
	}

	// Invoke the actual permission-checked management API with a real DB-backed
	// admin session. Do not change token storage directly or close any lane.
	rotateCtx, cancelRotation := context.WithTimeout(f.ctx, mixedSaturationWait)
	request := connect.NewRequest(&p2pstreamv1.RotateAgentTokenRequest{Id: f.row.ID})
	request.Header().Set("Cookie", admin.Get("Cookie"))
	rotated, err := rotate(rotateCtx, f, request)
	cancelRotation()
	if err != nil {
		t.Fatal(err)
	}
	if rotated.Msg.Token == "" || rotated.Msg.Token == "reconnect-token-1" || rotated.Msg.Agent == nil || rotated.Msg.Agent.Connected {
		t.Fatal("rotation did not return a fresh credential and disconnected agent")
	}
	select {
	case <-old.Done:
	default:
		t.Fatal("rotation returned before revoking the old generation")
	}
	if f.app.AgentHub.connectedByID(f.row.ID) != nil {
		t.Fatal("rotation retained an authenticated old generation")
	}

	// A single real TLS bootstrap attempt is the stale-credential oracle. The
	// old runtime remains live here; this check never uses its backoff as proof.
	bootstrapCtx, cancelBootstrap := context.WithTimeout(f.ctx, mixedSaturationWait)
	staleRequest, err := http.NewRequestWithContext(bootstrapCtx, http.MethodGet, f.management.URL+tunnel.BootstrapPath, nil)
	if err != nil {
		cancelBootstrap()
		t.Fatal(err)
	}
	staleRequest.Header = newAggregateTunnelRequest(t, f.row.PublicID, "reconnect-token-1", old.TunnelGroup, 0, true).Header.Clone()
	client := f.management.Client()
	t.Cleanup(client.CloseIdleConnections)
	response, err := client.Do(staleRequest)
	if err != nil {
		cancelBootstrap()
		t.Fatal(err)
	}
	body, readErr := io.ReadAll(io.LimitReader(response.Body, 1024))
	closeErr := response.Body.Close()
	cancelBootstrap()
	client.CloseIdleConnections()
	if readErr != nil || closeErr != nil || response.StatusCode != http.StatusUnauthorized || string(body) != "invalid agent token\n" {
		t.Fatalf("stale bootstrap status=%d body=%q read=%v close=%v", response.StatusCode, body, readErr, closeErr)
	}
	if f.app.AgentHub.connectedByID(f.row.ID) != nil {
		t.Fatal("stale credential republished a generation after rotation")
	}

	// Keep the origin release gate closed: revocation alone must terminate the
	// already-committed200 stream with its exact prefix and unexpected EOF.
	f.finish(t, held, false)
	f.stopRuntime(t, oldRuntime)
	mixedSaturationAwait(t, "revoked physical generation and all resource owners drain", func() bool {
		if !f.terminal() || !f.noPoolFor(old) || f.hasLease(owner.leaseID) {
			return false
		}
		for lane, session := range oldLanes {
			if !reconnectSessionRetired(session, lane) {
				return false
			}
		}
		return true
	})
	unlock := f.app.lockAgentAuth(f.row.ID)
	unlock()
	f.awaitDisconnectedRow(t, old.ConnectionDBID)
	var openRows, allRows int
	if err := f.app.DB.QueryRowContext(f.ctx, "SELECT COUNT(*) FROM connections WHERE agent_id=? AND disconnected_at IS NULL", f.row.ID).Scan(&openRows); err != nil {
		t.Fatal(err)
	}
	if err := f.app.DB.QueryRowContext(f.ctx, "SELECT COUNT(*) FROM connections WHERE agent_id=?", f.row.ID).Scan(&allRows); err != nil {
		t.Fatal(err)
	}
	if openRows != 0 || allRows != 1 {
		t.Fatalf("revoked/stale-bootstrap connection history open=%d all=%d want0/1", openRows, allRows)
	}

	freshRuntime := f.startRuntime(t, rotated.Msg.Token)
	fresh := f.awaitGeneration(t, old)
	f.remember(fresh)
	freshKey := agentStreamCapacitySessionKey(fresh, fresh.tunnelSession(0))
	if fresh == old || fresh.TunnelGroup == old.TunnelGroup || fresh.ConnectionDBID <= 0 || fresh.ConnectionDBID == old.ConnectionDBID || freshKey == oldKey {
		t.Fatal("fresh credential did not establish a distinct generation")
	}
	f.finish(t, f.startRequest(t, "replacement-progress", 1), true)
	f.awaitNoStreams(t, fresh)
	f.assertOnlyOpenRow(t, fresh.ConnectionDBID)
	capacity := f.app.agentStreamCapacity.snapshot()
	_, oldRegistered := capacity.SessionLimits[oldKey]
	if oldRegistered || capacity.RegisteredSessions != 1 || capacity.SessionLimits[freshKey] != 4 || !f.noPoolFor(old) || len(reconnectRawLanes(old)) != 0 {
		t.Fatal("fresh recovery restored old registrations, raw lanes or transports")
	}
	if err := f.app.DB.QueryRowContext(f.ctx, "SELECT COUNT(*) FROM connections WHERE agent_id=?", f.row.ID).Scan(&allRows); err != nil {
		t.Fatal(err)
	}
	if allRows != 2 {
		t.Fatalf("recovery connection history rows=%d want2", allRows)
	}
	// The existing observer oracle requires exactly two requests/events/origins:
	// committed200 prefix plus agent_disconnected_during_response for the old
	// request, then a complete new-token response; no retries or duplicate rows.
	f.checkEvents(t)
	f.stopRuntime(t, freshRuntime)
	f.close(t)
	if t.Failed() {
		return
	}
	t.Logf("public_token_rotation_terminal transport=%s mode=%s rotations=1 stale_bootstrap401=1 committed_truncations=1 recovery=1 requests=2 events=2 origins=2 generations=2 joined_agents=2 retries=0 raw_lanes=0 owners=0 leases=0 registrations=0 waiters=0 pool_entries=0 pending_events=0 dropped_events=0", transport, mode)
}

// Rotation is agent-scoped even while another authenticated generation owns a
// committed stream and starts its next request. The existing tests cover stale
// credentials and same-agent recovery separately; this lifetime has no retries.
func TestPublicAgentTokenRotationPreservesOtherAgentActiveAndNextRequests(t *testing.T) {
	f := newPublicReconnectFixture(t)
	defer func() {
		if t.Failed() {
			f.logOwnership(t, "public-token-rotation-isolation-failure")
		}
	}()
	for name := range f.gates {
		if name != "old-generation" && name != "lane-survivor" && name != "replacement-progress" {
			delete(f.gates, name)
		}
	}
	otherRow, err := f.app.DB.CreateAgent(f.ctx, db.CreateAgentParams{PublicID: "rotation-survivor-agent", Name: "Rotation survivor agent", TokenHash: hashAgentToken("rotation-survivor-token"), Enabled: 1})
	if err != nil {
		t.Fatal(err)
	}
	admin := createTestAdminSession(t, f.app)
	rotatedRuntime := f.startRuntime(t, "reconnect-token-1")
	survivorRuntime := f.startRuntimeForAgent(t, otherRow, "rotation-survivor-token")
	rotatedAgent := f.awaitGeneration(t, nil)
	var survivor *AgentConn
	mixedSaturationAwait(t, "survivor establishes control plus two bulk lanes", func() bool {
		survivor = f.app.AgentHub.connectedByID(otherRow.ID)
		return survivor != nil && realAgentAggregateLaneShape(survivor, 2)
	})
	f.remember(rotatedAgent)
	f.remember(survivor)
	rotatedLanes, survivorLanes := reconnectRawLanes(rotatedAgent), reconnectRawLanes(survivor)
	rotatedKey := agentStreamCapacitySessionKey(rotatedAgent, rotatedAgent.tunnelSession(0))
	survivorKey := agentStreamCapacitySessionKey(survivor, survivor.tunnelSession(0))
	survivorGroup, survivorConnection := survivor.TunnelGroup, survivor.ConnectionDBID
	if rotatedAgent.AgentID == survivor.AgentID || rotatedKey == survivorKey || len(rotatedLanes) != 3 || len(survivorLanes) != 3 || survivorConnection <= 0 || rotatedAgent.ConnectionDBID <= 0 {
		t.Fatal("two distinct authenticated control/two-bulk generations are required")
	}
	f.startPublicForAgents(t, [2]*AgentConn{rotatedAgent, survivor})
	rotatedRequest := f.startRequest(t, "old-generation", 0)
	survivorRequest := f.startRequest(t, "lane-survivor", 1)
	rotatedOwner := f.heldOwner(t, rotatedAgent, 0)
	survivorOwner := f.heldOwner(t, survivor, 1)
	if rotatedOwner.session == survivorOwner.session || rotatedOwner.leaseID == survivorOwner.leaseID {
		t.Fatal("independent agents shared a physical owner")
	}
	before, err := f.app.DB.GetAgent(f.ctx, otherRow.ID)
	if err != nil {
		t.Fatal(err)
	}
	// These phase counts are intentionally local to the two-agent scenario;
	// the existing single-generation assertCapacity contract stays unchanged.
	awaitOwners := func(onlySurvivor bool) {
		t.Helper()
		mixedSaturationAwait(t, "exact live owners and generation registrations", func() bool {
			snapshot := f.app.agentStreamCapacity.snapshot()
			want := 2
			if onlySurvivor {
				want = 1
			}
			if snapshot.RegisteredSessions != want || len(snapshot.SessionLimits) != want || snapshot.SessionLimits[survivorKey] != 4 || snapshot.Total.InUse != want || snapshot.Public.InUse != want || snapshot.Pooled.InUse != want || snapshot.ActiveLeases != want || snapshot.States.Live != want || snapshot.States.Opening != 0 || snapshot.States.Closing != 0 || snapshot.Waiters != 0 || snapshot.TotalBySession[survivorKey] != 1 || snapshot.PublicBySession[survivorKey] != 1 || survivor.ActiveRequests.Load() != 1 {
				return false
			}
			if onlySurvivor {
				_, registered := snapshot.SessionLimits[rotatedKey]
				return !registered && snapshot.TotalBySession[rotatedKey] == 0 && snapshot.PublicBySession[rotatedKey] == 0
			}
			return snapshot.SessionLimits[rotatedKey] == 4 && snapshot.TotalBySession[rotatedKey] == 1 && snapshot.PublicBySession[rotatedKey] == 1 && rotatedAgent.ActiveRequests.Load() == 1
		})
		if err := f.app.agentStreamCapacity.validateInvariants(); err != nil {
			t.Fatal(err)
		}
	}
	assertSurvivor := func() {
		t.Helper()
		select {
		case <-survivor.Done:
			t.Fatal("rotation signaled the other agent done")
		default:
		}
		f.assertHeld(t, survivorRequest)
		if f.app.AgentHub.connectedByID(otherRow.ID) != survivor || survivor.TunnelGroup != survivorGroup || survivor.ConnectionDBID != survivorConnection || !realAgentAggregateLaneShape(survivor, 2) || f.heldOwner(t, survivor, 1) != survivorOwner || !f.hasLease(survivorOwner.leaseID) {
			t.Fatal("rotation replaced the survivor generation or original held owner")
		}
		lanes := reconnectRawLanes(survivor)
		if len(lanes) != len(survivorLanes) {
			t.Fatal("rotation changed survivor lane topology")
		}
		for lane, session := range survivorLanes {
			if lanes[lane] != session || session.IsClosed() {
				t.Fatal("rotation changed a survivor physical lane")
			}
		}
		after, err := f.app.DB.GetAgent(f.ctx, otherRow.ID)
		if err != nil || before.TokenHash != after.TokenHash || before.LastConnectedAt != after.LastConnectedAt || before.LastDisconnectedAt != after.LastDisconnectedAt {
			t.Fatalf("rotation changed survivor credential or DB connection state: %v", err)
		}
		var allRows, openRows, openID int64
		if err := f.app.DB.QueryRowContext(f.ctx, "SELECT COUNT(*),COUNT(CASE WHEN disconnected_at IS NULL THEN 1 END),COALESCE(MAX(CASE WHEN disconnected_at IS NULL THEN id END),0) FROM connections WHERE agent_id=?", otherRow.ID).Scan(&allRows, &openRows, &openID); err != nil {
			t.Fatal(err)
		}
		if allRows != 1 || openRows != 1 || openID != survivorConnection {
			t.Fatalf("survivor connection history all/open/id=%d/%d/%d", allRows, openRows, openID)
		}
	}
	awaitOwners(false)
	f.assertHeld(t, rotatedRequest)
	assertSurvivor()
	rotateCtx, cancelRotation := context.WithTimeout(f.ctx, mixedSaturationWait)
	request := connect.NewRequest(&p2pstreamv1.RotateAgentTokenRequest{Id: f.row.ID})
	request.Header().Set("Cookie", admin.Get("Cookie"))
	rotated, err := f.app.RotateAgentToken(rotateCtx, request)
	cancelRotation()
	if err != nil {
		t.Fatal(err)
	}
	if rotated.Msg.Token == "" || rotated.Msg.Token == "reconnect-token-1" || rotated.Msg.Agent == nil || rotated.Msg.Agent.Connected || f.app.AgentHub.connectedByID(f.row.ID) != nil {
		t.Fatal("rotation did not replace the target credential and disconnect its generation")
	}
	select {
	case <-rotatedAgent.Done:
	default:
		t.Fatal("rotation returned before signaling the target generation done")
	}
	// Keep the origin gate closed and the target runtime alive until the
	// committed response terminates: cancellation cannot be its causal oracle.
	f.finish(t, rotatedRequest, false)
	f.stopRuntime(t, rotatedRuntime)
	mixedSaturationAwait(t, "only rotated generation physically retires", func() bool {
		if len(reconnectRawLanes(rotatedAgent)) != 0 || !f.noPoolFor(rotatedAgent) || f.hasLease(rotatedOwner.leaseID) || rotatedAgent.ActiveRequests.Load() != 0 || rotatedAgent.tunnelResourceSnapshot().SessionResourceBytes != 0 {
			return false
		}
		for lane, session := range rotatedLanes {
			if !reconnectSessionRetired(session, lane) {
				return false
			}
		}
		return true
	})
	unlock := f.app.lockAgentAuth(f.row.ID)
	unlock()
	f.awaitDisconnectedRow(t, rotatedAgent.ConnectionDBID)
	awaitOwners(true)
	assertSurvivor()
	// This complete next request must progress before the original B gate
	// opens. Check its original single held claim again after the extra claim
	// retires; heldOwner deliberately rejects multiple simultaneous claims.
	f.finish(t, f.startRequest(t, "replacement-progress", 1), true)
	awaitOwners(true)
	assertSurvivor()
	f.gates[survivorRequest.name].open()
	f.finish(t, survivorRequest, true)
	f.awaitNoStreams(t, survivor)
	f.checkEvents(t)
	f.stopRuntime(t, survivorRuntime)
	f.close(t)
	if t.Failed() {
		return
	}
	t.Log("public_token_rotation_isolation_terminal rotations=1 agents=2 requests=3 events=3 origins=3 committed_truncations=1 survivor_complete=2 retries=0 joined_agents=2 raw_lanes=0 owners=0 leases=0 registrations=0 waiters=0 pool_entries=0 pending_events=0 dropped_events=0")
}
