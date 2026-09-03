package server

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/gen/proto/p2pstream/v1/p2pstreamv1connect"
	"p2pstream/internal/agentupdate"
	"p2pstream/internal/agentupdateauth"
	"p2pstream/internal/agentupdateauthority"
	"p2pstream/internal/buildinfo"
	"p2pstream/internal/config"
	"p2pstream/internal/db"
)

func TestAgentUpdaterEnrollmentTokenIsScopedHashedAndSingleUse(t *testing.T) {
	ctx := context.Background()
	database := newServerTestDB(t)
	agentA := createAgentUpdateTestAgent(t, database, "agent-update-a")
	agentB := createAgentUpdateTestAgent(t, database, "agent-update-b")
	app := newAgentUpdateTestApp(t, database)
	provider, bootstrap := newAgentUpdateTestBootstrap(t)
	app.AgentUpdateBootstrap = provider
	token, _, err := app.createAgentUpdaterEnrollmentToken(ctx, agentA.ID, time.Minute, bootstrap)
	if err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := database.QueryRowContext(ctx, `SELECT token_hash FROM agent_updater_enrollment_tokens WHERE agent_id=?`, agentA.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == token || strings.Contains(stored, token) || len(stored) != 64 {
		t.Fatalf("token stored unsafely: %q", stored)
	}
	updaterPublic, _, _ := ed25519.GenerateKey(rand.Reader)
	activatorPublic, _, _ := ed25519.GenerateKey(rand.Reader)
	wrong := connect.NewRequest(&p2pstreamv1.EnrollAgentUpdaterRequest{Token: token, AgentPublicId: agentB.PublicID, UpdaterPublicKey: updaterPublic, ActivatorPublicKey: activatorPublic, Os: "linux", Arch: "amd64", UpdaterVersion: "v1.0.0"})
	if _, err := app.EnrollAgentUpdater(ctx, wrong); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("cross-agent enrollment error = %v", err)
	}
	request := connect.NewRequest(&p2pstreamv1.EnrollAgentUpdaterRequest{Token: token, AgentPublicId: agentA.PublicID, UpdaterPublicKey: updaterPublic, ActivatorPublicKey: activatorPublic, Os: "linux", Arch: "amd64", UpdaterVersion: "v1.0.0"})
	firstEnrollment, err := app.EnrollAgentUpdater(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := app.EnrollAgentUpdater(ctx, request)
	if err != nil || retry.Msg.UpdaterKeyId != agentUpdateKeyID(updaterPublic) {
		t.Fatalf("lost-response enrollment retry = %+v, %v", retry, err)
	}
	if firstEnrollment.Msg.Receipt == nil || retry.Msg.Receipt == nil || !bytes.Equal(firstEnrollment.Msg.Receipt.CanonicalPayload, retry.Msg.Receipt.CanonicalPayload) || !bytes.Equal(firstEnrollment.Msg.Receipt.Signature, retry.Msg.Receipt.Signature) {
		t.Fatal("lost-response enrollment retry did not return the byte-identical signed receipt")
	}
	differentUpdater, _, _ := ed25519.GenerateKey(rand.Reader)
	different := connect.NewRequest(&p2pstreamv1.EnrollAgentUpdaterRequest{Token: token, AgentPublicId: agentA.PublicID, UpdaterPublicKey: differentUpdater, ActivatorPublicKey: activatorPublic, Os: "linux", Arch: "amd64", UpdaterVersion: "v1.0.0"})
	if _, err := app.EnrollAgentUpdater(ctx, different); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("different identity reused enrollment token: %v", err)
	}
	replacementProvider, _ := newAgentUpdateTestBootstrap(t)
	app.AgentUpdateBootstrap = replacementProvider
	if _, err := app.EnrollAgentUpdater(ctx, request); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("enrollment receipt survived bootstrap trust replacement: %v", err)
	}
}

func TestAgentUpdaterConcurrentEnrollmentTokenIssuanceLeavesOnlyNewestAuthority(t *testing.T) {
	ctx := context.Background()
	database := newServerTestDB(t)
	agent := createAgentUpdateTestAgent(t, database, "agent-token-generation")
	app := newAgentUpdateTestApp(t, database)
	provider, bootstrap := newAgentUpdateTestBootstrap(t)
	app.AgentUpdateBootstrap = provider

	const issuers = 8
	tokens := make([]string, issuers)
	errs := make([]error, issuers)
	var wg sync.WaitGroup
	for i := range tokens {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			tokens[index], _, errs[index] = app.createAgentUpdaterEnrollmentToken(ctx, agent.ID, time.Minute, bootstrap)
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var storedHash string
	var live int
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(MAX(token_hash),'') FROM agent_updater_enrollment_tokens WHERE agent_id=? AND used_at IS NULL`, agent.ID).Scan(&live, &storedHash); err != nil {
		t.Fatal(err)
	}
	if live != 1 {
		t.Fatalf("live unused enrollment tokens = %d, want 1", live)
	}
	updaterPublic, _, _ := ed25519.GenerateKey(rand.Reader)
	activatorPublic, _, _ := ed25519.GenerateKey(rand.Reader)
	accepted := 0
	for _, token := range tokens {
		digest := sha256.Sum256([]byte(token))
		request := connect.NewRequest(&p2pstreamv1.EnrollAgentUpdaterRequest{Token: token, AgentPublicId: agent.PublicID, UpdaterPublicKey: updaterPublic, ActivatorPublicKey: activatorPublic, Os: "linux", Arch: "amd64", UpdaterVersion: "v1.0.0"})
		_, err := app.EnrollAgentUpdater(ctx, request)
		if hex.EncodeToString(digest[:]) == storedHash {
			if err != nil {
				t.Fatalf("newest token was rejected: %v", err)
			}
			accepted++
		} else if connect.CodeOf(err) != connect.CodeUnauthenticated {
			t.Fatalf("superseded token error = %v", err)
		}
	}
	if accepted != 1 {
		t.Fatalf("accepted newest tokens = %d, want 1", accepted)
	}
}

func TestAgentUpdaterRequestSignatureCounterRejectsReplay(t *testing.T) {
	ctx := context.Background()
	database := newServerTestDB(t)
	agent := createAgentUpdateTestAgent(t, database, "agent-signature")
	app := newAgentUpdateTestApp(t, database)
	updaterPublic, updaterPrivate, _ := ed25519.GenerateKey(rand.Reader)
	activatorPublic, _, _ := ed25519.GenerateKey(rand.Reader)
	insertAgentUpdateIdentity(t, app, database, agent.ID, updaterPublic, activatorPublic)
	request := &p2pstreamv1.CheckAgentUpdateRequest{AgentPublicId: agent.PublicID, Counter: 7}
	request.Signature = ed25519.Sign(updaterPrivate, agentupdateauth.CheckPayload(request.AgentPublicId, request.Counter))
	if _, err := app.verifyAgentUpdaterRequest(ctx, request.AgentPublicId, request.Counter, request.Signature, agentUpdaterCheckSigningPayload(request)); err != nil {
		t.Fatal(err)
	}
	if _, err := app.verifyAgentUpdaterRequest(ctx, request.AgentPublicId, request.Counter, request.Signature, agentUpdaterCheckSigningPayload(request)); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("replay error = %v", err)
	}
}

func TestCheckAgentUpdateReplaysByteIdenticalDurableAuthorization(t *testing.T) {
	ctx := context.Background()
	database := newServerTestDB(t)
	agent := createAgentUpdateTestAgent(t, database, "agent-authorization-retry")
	app := newAgentUpdateTestApp(t, database)
	updaterPublic, updaterPrivate, _ := ed25519.GenerateKey(rand.Reader)
	activatorPublic, _, _ := ed25519.GenerateKey(rand.Reader)
	insertAgentUpdateIdentity(t, app, database, agent.ID, updaterPublic, activatorPublic)
	_, _ = insertAgentUpdateTestCampaign(t, database, agent.ID, "staged", "none", false)
	authorizeAgentUpdateTestActivation(t, app, agent.ID)

	check := func(counter uint64) *p2pstreamv1.CheckAgentUpdateResponse {
		t.Helper()
		request := &p2pstreamv1.CheckAgentUpdateRequest{AgentPublicId: agent.PublicID, Counter: counter}
		request.Signature = ed25519.Sign(updaterPrivate, agentupdateauth.CheckPayload(request.AgentPublicId, request.Counter))
		response, err := app.CheckAgentUpdate(ctx, connect.NewRequest(request))
		if err != nil {
			t.Fatal(err)
		}
		if response.Msg.Authorization == nil {
			t.Fatal("privileged update response omitted its signed authorization")
		}
		return response.Msg
	}
	first := check(1)
	second := check(2)
	if !bytes.Equal(first.Authorization.CanonicalPayload, second.Authorization.CanonicalPayload) || !bytes.Equal(first.Authorization.Signature, second.Authorization.Signature) || !bytes.Equal(first.Authorization.Nonce, second.Authorization.Nonce) || first.Authorization.CommandSequence != second.Authorization.CommandSequence {
		t.Fatal("CheckAgentUpdate regenerated or changed the durable authorization across retries")
	}
	if _, err := database.ExecContext(ctx, `UPDATE agent_update_assignments SET authorization_expires_at=? WHERE agent_id=?`, time.Now().UTC().Add(-time.Minute), agent.ID); err != nil {
		t.Fatal(err)
	}
	refreshed := check(3)
	if refreshed.Authorization.CommandSequence != first.Authorization.CommandSequence+1 || bytes.Equal(refreshed.Authorization.CanonicalPayload, first.Authorization.CanonicalPayload) || refreshed.Authorization.Action != first.Authorization.Action {
		t.Fatalf("expired authorization was not safely superseded: first=%+v refreshed=%+v", first.Authorization, refreshed.Authorization)
	}
}

func TestAgentUpdateActivationWaitsForActiveRequestDrain(t *testing.T) {
	ctx := context.Background()
	database := newServerTestDB(t)
	agent := createAgentUpdateTestAgent(t, database, "agent-drain")
	app := newAgentUpdateTestApp(t, database)
	updaterPublic, _, _ := ed25519.GenerateKey(rand.Reader)
	activatorPublic, _, _ := ed25519.GenerateKey(rand.Reader)
	insertAgentUpdateIdentity(t, app, database, agent.ID, updaterPublic, activatorPublic)
	_, assignmentID := insertAgentUpdateTestCampaign(t, database, agent.ID, "staged", "none", false)

	conn := &AgentConn{AgentID: agent.ID, PublicID: agent.PublicID, Done: make(chan struct{})}
	app.agentUpdateDrainReady = func(id int64) bool {
		return id == agent.ID && conn.ActiveRequests.Load() == 0
	}
	release, admitted := app.beginAgentUpdateProtectedRequest(conn)
	if !admitted {
		t.Fatal("pre-cordon request was not admitted")
	}
	assignment, campaign, err := app.activeAgentUpdateAssignment(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	app.tryAdvanceAgentUpdateAssignment(ctx, assignment, campaign)
	assignment, campaign, err = app.activeAgentUpdateAssignment(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if assignment.State != "cordoned" || assignment.DesiredAction != "none" || assignment.AuthorizationAction != "" || len(assignment.AuthorizationSignature) != 0 {
		t.Fatalf("activation escaped before drain: %+v", assignment)
	}
	if _, ok := app.beginAgentUpdateProtectedRequest(conn); ok {
		t.Fatal("request resolved before cordon entered after the routing fence")
	}

	release()
	app.tryAdvanceAgentUpdateAssignment(ctx, assignment, campaign)
	assignment, _, err = app.activeAgentUpdateAssignment(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if assignment.DesiredAction != "activate" || assignment.AuthorizationAction != "activate" || len(assignment.AuthorizationSignature) != ed25519.SignatureSize {
		t.Fatalf("drained assignment did not receive activation authorization: %+v", assignment)
	}
	if got := conn.ActiveRequests.Load(); got != 0 {
		t.Fatalf("active request count = %d, want 0", got)
	}
	assertAgentUpdateAssignmentState(t, database, assignmentID, "cordoned")
}

func TestAgentUpdateDrainIncludesNonPooledSessionLeases(t *testing.T) {
	database := newServerTestDB(t)
	app := newAgentUpdateTestApp(t, database)
	app.agentUpdateDrainReady = nil
	agent, _ := newFakeYamuxAgent(t, 9001, "agent-drain-ledger")
	if err := app.AgentHub.connect(agent); err != nil {
		t.Fatal(err)
	}
	sessionKey := agentStreamCapacitySessionKey(agent, agent.Session)
	app.agentStreamCapacity.registerSession(sessionKey)
	t.Cleanup(func() { app.agentStreamCapacity.unregisterSession(sessionKey) })
	lease, err := app.agentStreamCapacity.tryAcquire(agentStreamCapacityTrustedHealth, "health:test", sessionKey)
	if err != nil {
		t.Fatal(err)
	}
	if !lease.markLive() {
		t.Fatal("could not mark test one-shot lease live")
	}
	if app.agentUpdateAgentDrained(agent.AgentID) {
		t.Fatal("non-pooled session lease was omitted from drain accounting")
	}
	lease.markClosing()
	lease.release()
	if !app.agentUpdateAgentDrained(agent.AgentID) {
		t.Fatal("agent did not become drained after the non-pooled lease released")
	}
}

func TestAgentUpdateDrainTimeoutPausesRestoresRoutingAndRequiresRetry(t *testing.T) {
	ctx := context.Background()
	database := newServerTestDB(t)
	agent := createAgentUpdateTestAgent(t, database, "agent-drain-timeout")
	app := newAgentUpdateTestApp(t, database)
	updaterPublic, _, _ := ed25519.GenerateKey(rand.Reader)
	activatorPublic, _, _ := ed25519.GenerateKey(rand.Reader)
	insertAgentUpdateIdentity(t, app, database, agent.ID, updaterPublic, activatorPublic)
	campaignID, assignmentID := insertAgentUpdateTestCampaign(t, database, agent.ID, "staged", "none", false)
	app.agentUpdateDrainReady = func(int64) bool { return false }
	assignment, campaign, err := app.activeAgentUpdateAssignment(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	app.tryAdvanceAgentUpdateAssignment(ctx, assignment, campaign)
	if !app.isAgentUpdateCordoned(agent.ID) {
		t.Fatal("draining assignment was not cordoned")
	}
	if _, err := database.ExecContext(ctx, `UPDATE agent_update_assignments SET updated_at=? WHERE id=?`, time.Now().UTC().Add(-agentUpdateDrainTimeout-time.Second), assignmentID); err != nil {
		t.Fatal(err)
	}
	assignment, campaign, err = app.activeAgentUpdateAssignment(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	app.tryAdvanceAgentUpdateAssignment(ctx, assignment, campaign)
	assertAgentUpdateAssignmentState(t, database, assignmentID, "blocked")
	if app.isAgentUpdateCordoned(agent.ID) {
		t.Fatal("timed-out drain remained cordoned")
	}
	var campaignState string
	var campaignGeneration int64
	if err := database.QueryRowContext(ctx, `SELECT state,generation FROM agent_update_campaigns WHERE id=?`, campaignID).Scan(&campaignState, &campaignGeneration); err != nil {
		t.Fatal(err)
	}
	if campaignState != "paused" || campaignGeneration != 2 {
		t.Fatalf("campaign after drain timeout = state %q generation %d", campaignState, campaignGeneration)
	}
	header := createTestAdminSession(t, app)
	retry := connect.NewRequest(&p2pstreamv1.RetryAgentUpdateAssignmentsRequest{CampaignId: campaignID, ExpectedCampaignGeneration: campaignGeneration, AssignmentIds: []int64{assignmentID}})
	retry.Header().Set("Cookie", header.Get("Cookie"))
	if _, err := app.RetryAgentUpdateAssignments(ctx, retry); err != nil {
		t.Fatalf("administrator drain retry failed: %v", err)
	}
	assertAgentUpdateAssignmentState(t, database, assignmentID, "pending")
	var retriedAction string
	if err := database.QueryRowContext(ctx, `SELECT desired_action FROM agent_update_assignments WHERE id=?`, assignmentID).Scan(&retriedAction); err != nil {
		t.Fatal(err)
	}
	if retriedAction != "none" {
		t.Fatalf("retried assignment bypassed cohort scheduler with action %q", retriedAction)
	}
}

func TestAgentUpdateCohortsAreDiscreteAndCanariesCompleteFirst(t *testing.T) {
	ctx := context.Background()
	database := newServerTestDB(t)
	now := time.Now().UTC()
	result, err := database.ExecContext(ctx, `INSERT INTO agent_update_campaigns (name,state,generation,target_version,target_commit,manifest_sha256,release_sequence,root_version,security_epoch,minimum_updater_version,minimum_tunnel_protocol,maximum_tunnel_protocol,artifacts_json,max_unavailable,minimum_eligible_agents_per_route,canary_count,wave_size,healthy_dwell_millis,created_at,updated_at) VALUES ('cohorts','running',1,'v1.2.3',?,?,12,3,2,'v1.0.0',1,1,'[]',2,1,2,2,10000,?,?)`, strings.Repeat("c", 40), strings.Repeat("a", 64), now, now)
	if err != nil {
		t.Fatal(err)
	}
	campaignID, _ := result.LastInsertId()
	states := []string{"succeeded", "healthy_dwell", "staged", "staged", "staged"}
	assignmentIDs := make([]int64, len(states))
	for i, state := range states {
		agent := createAgentUpdateTestAgent(t, database, fmt.Sprintf("cohort-agent-%d", i))
		insert, err := database.ExecContext(ctx, `INSERT INTO agent_update_assignments (campaign_id,agent_id,state,desired_action,generation,cordoned,created_at,updated_at) VALUES (?,?,?,'none',1,0,?,?)`, campaignID, agent.ID, state, now, now)
		if err != nil {
			t.Fatal(err)
		}
		assignmentIDs[i], _ = insert.LastInsertId()
	}
	campaign := agentUpdateCampaignRow{AgentUpdateCampaign: db.AgentUpdateCampaign{ID: campaignID, CanaryCount: 2, WaveSize: 2}}
	candidate := agentUpdateAssignmentRow{AgentUpdateAssignment: db.AgentUpdateAssignment{ID: assignmentIDs[2], CampaignID: campaignID}}
	if _, eligible, failed := agentUpdateCohortEligibleQuery(ctx, database, candidate, campaign); eligible || failed {
		t.Fatalf("first regular wave escaped incomplete canaries: eligible=%v failed=%v", eligible, failed)
	}
	if _, err := database.ExecContext(ctx, `UPDATE agent_update_assignments SET state='succeeded' WHERE id=?`, assignmentIDs[1]); err != nil {
		t.Fatal(err)
	}
	if size, eligible, failed := agentUpdateCohortEligibleQuery(ctx, database, candidate, campaign); size != 2 || !eligible || failed {
		t.Fatalf("first wave eligibility = size %d eligible %v failed %v", size, eligible, failed)
	}
	last := agentUpdateAssignmentRow{AgentUpdateAssignment: db.AgentUpdateAssignment{ID: assignmentIDs[4], CampaignID: campaignID}}
	if _, eligible, failed := agentUpdateCohortEligibleQuery(ctx, database, last, campaign); eligible || failed {
		t.Fatalf("second wave escaped incomplete prior wave: eligible=%v failed=%v", eligible, failed)
	}
	if _, err := database.ExecContext(ctx, `UPDATE agent_update_assignments SET state='succeeded' WHERE id IN (?,?)`, assignmentIDs[2], assignmentIDs[3]); err != nil {
		t.Fatal(err)
	}
	if size, eligible, failed := agentUpdateCohortEligibleQuery(ctx, database, last, campaign); size != 1 || !eligible || failed {
		t.Fatalf("final wave eligibility = size %d eligible %v failed %v", size, eligible, failed)
	}
}

func TestManagementHandlerRejectsOversizedUpdaterMessagesBeforeMethodAdmission(t *testing.T) {
	app := NewApp(&config.Config{ManagementUIDisabled: true}, nil)
	for i := 0; i < cap(app.agentUpdateRequestGate); i++ {
		app.agentUpdateRequestGate <- struct{}{}
	}
	mux := http.NewServeMux()
	app.RegisterManagementRoutes(mux)
	server := httptest.NewServer(mux)
	defer server.Close()
	huge := strings.Repeat("x", managementRPCMaxMessageBytes+1)
	for _, useJSON := range []bool{false, true} {
		options := []connect.ClientOption{}
		if useJSON {
			options = append(options, connect.WithProtoJSON())
		}
		client := p2pstreamv1connect.NewAgentManagementServiceClient(server.Client(), server.URL, options...)
		_, err := client.EnrollAgentUpdater(context.Background(), connect.NewRequest(&p2pstreamv1.EnrollAgentUpdaterRequest{Token: huge}))
		if connect.CodeOf(err) != connect.CodeUnknown || !strings.Contains(err.Error(), "413 Request Entity Too Large") {
			t.Fatalf("json=%v oversized error = %v", useJSON, err)
		}
	}
	if got := len(app.agentUpdateRequestGate); got != cap(app.agentUpdateRequestGate) {
		t.Fatalf("oversized request entered method admission: gate=%d want=%d", got, cap(app.agentUpdateRequestGate))
	}
}

func TestManagementHandlerRejectsCompressedUpdaterMessagesAcrossProtocolsBeforeAdmission(t *testing.T) {
	app := NewApp(&config.Config{ManagementUIDisabled: true}, nil)
	for i := 0; i < cap(app.agentUpdateRequestGate); i++ {
		app.agentUpdateRequestGate <- struct{}{}
	}
	mux := http.NewServeMux()
	app.RegisterManagementRoutes(mux)
	server := httptest.NewServer(mux)
	defer server.Close()

	tests := []struct {
		name        string
		contentType string
		header      string
	}{
		{name: "connect", contentType: "application/proto", header: "Connect-Content-Encoding"},
		{name: "grpc", contentType: "application/grpc+proto", header: "Grpc-Encoding"},
		{name: "grpc-web", contentType: "application/grpc-web+proto", header: "Grpc-Encoding"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request, err := http.NewRequest(http.MethodPost, server.URL+p2pstreamv1connect.AgentManagementServiceCheckAgentUpdateProcedure, strings.NewReader("compressed-bomb-placeholder"))
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Content-Type", test.contentType)
			request.Header.Set(test.header, "gzip")
			response, err := server.Client().Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != http.StatusUnsupportedMediaType {
				t.Fatalf("compressed %s status = %d, want %d", test.name, response.StatusCode, http.StatusUnsupportedMediaType)
			}
		})
	}
	if got := len(app.agentUpdateRequestGate); got != cap(app.agentUpdateRequestGate) {
		t.Fatalf("compressed request entered method admission: gate=%d want=%d", got, cap(app.agentUpdateRequestGate))
	}
}

func TestAgentUpdateBootstrapRejectsNearlyExpiredRoot(t *testing.T) {
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	root, err := agentupdate.NewRootMetadata(1, time.Now().UTC().Add(time.Hour).Format(time.RFC3339), 1, []ed25519.PublicKey{public})
	if err != nil {
		t.Fatal(err)
	}
	data, err := agentupdate.CanonicalRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	app := &App{AgentUpdateBootstrap: agentUpdateTestBootstrapProvider{rootBase64: base64.StdEncoding.EncodeToString(data), repository: "owner/repo"}}
	if _, err := app.loadAgentUpdateBootstrap(context.Background()); err == nil || !strings.Contains(err.Error(), "expiry") {
		t.Fatalf("near-expiry bootstrap root error = %v", err)
	}
}

func TestCreateAgentRemainsAvailableWhenManagedUpdateBootstrapIsUnavailable(t *testing.T) {
	for _, test := range []struct {
		name     string
		provider AgentUpdateBootstrapProvider
	}{
		{name: "catalog failure", provider: agentUpdateFailingBootstrapProvider{}},
		{name: "authority failure", provider: func() AgentUpdateBootstrapProvider { provider, _ := newAgentUpdateTestBootstrap(t); return provider }()},
	} {
		t.Run(test.name, func(t *testing.T) {
			database := newServerTestDB(t)
			app := NewApp(nil, database)
			app.AgentUpdateBootstrap = test.provider
			app.SetAgentUpdateManagementAuthority(nil, errors.New("injected authority failure"))
			header := createTestAdminSession(t, app)
			request := connect.NewRequest(&p2pstreamv1.CreateAgentRequest{Name: "proxy-only-agent", Enabled: true})
			request.Header().Set("Cookie", header.Get("Cookie"))
			response, err := app.CreateAgent(context.Background(), request)
			if err != nil {
				t.Fatalf("CreateAgent inherited managed-update failure: %v", err)
			}
			if response.Msg.Agent == nil || response.Msg.Token == "" {
				t.Fatalf("CreateAgent response is incomplete: %+v", response.Msg)
			}
			if response.Msg.UpdaterEnrollmentToken != "" || response.Msg.UpdaterManagementAuthority != nil || response.Msg.UpdaterTrustedRootMetadataBase64 != "" || response.Msg.UpdaterPinnedRepository != "" {
				t.Fatalf("CreateAgent returned an unusable partial updater bootstrap: %+v", response.Msg)
			}
		})
	}
}

func TestAgentUpdateActivationRequiresSignedRootActionReceipt(t *testing.T) {
	ctx := context.Background()
	database := newServerTestDB(t)
	agent := createAgentUpdateTestAgent(t, database, "agent-activation")
	app := newAgentUpdateTestApp(t, database)
	updaterPublic, updaterPrivate, _ := ed25519.GenerateKey(rand.Reader)
	activatorPublic, activatorPrivate, _ := ed25519.GenerateKey(rand.Reader)
	insertAgentUpdateIdentity(t, app, database, agent.ID, updaterPublic, activatorPublic)
	campaignID, assignmentID := insertAgentUpdateTestCampaign(t, database, agent.ID, "staged", "none", false)
	authorizeAgentUpdateTestActivation(t, app, agent.ID)
	preActivationTunnel := &AgentConn{AgentID: agent.ID, PublicID: agent.PublicID, Done: make(chan struct{}), ConnectedAt: time.Now().UTC().Add(-time.Minute)}
	if err := app.AgentHub.connect(preActivationTunnel); err != nil {
		t.Fatal(err)
	}
	report := &p2pstreamv1.ReportAgentUpdateRequest{AgentPublicId: agent.PublicID, Counter: 1, AssignmentId: assignmentID, Generation: 1, State: p2pstreamv1.AgentUpdaterReportState_AGENT_UPDATER_REPORT_STATE_ACTIVATED, ManifestSha256: strings.Repeat("a", 64), BinarySha256: strings.Repeat("b", 64), RunningVersion: "v1.2.3", RunningCommit: strings.Repeat("c", 40)}
	report.RootActionReceipt = newAgentUpdateTestRootActionReceipt(t, app, agent.ID, activatorPrivate, agentupdateauth.AssignmentActionActivate, 1)
	signAgentUpdateTestReport(report, updaterPrivate)
	response, err := app.ReportAgentUpdate(ctx, connect.NewRequest(report))
	if err != nil {
		t.Fatal(err)
	}
	if response.Msg.State != p2pstreamv1.AgentUpdateAssignmentState_AGENT_UPDATE_ASSIGNMENT_STATE_AWAITING_TUNNEL {
		t.Fatalf("state = %v", response.Msg.State)
	}
	var state string
	if err := database.QueryRowContext(ctx, `SELECT state FROM agent_update_assignments WHERE id=? AND campaign_id=?`, assignmentID, campaignID).Scan(&state); err != nil || state != "awaiting_tunnel" {
		t.Fatalf("stored state = %q, err=%v", state, err)
	}
	if connected := app.AgentHub.connectedByID(agent.ID); connected != nil {
		t.Fatalf("pre-activation tunnel remained registered after committed activation: %+v", connected)
	}
	select {
	case <-preActivationTunnel.Done:
	default:
		t.Fatal("committed activation did not signal the pre-activation tunnel to close")
	}
	var activatedAt time.Time
	if err := database.QueryRowContext(ctx, `SELECT activated_at FROM agent_update_assignments WHERE id=?`, assignmentID).Scan(&activatedAt); err != nil {
		t.Fatal(err)
	}
	postActivationTunnel := &AgentConn{AgentID: agent.ID, PublicID: agent.PublicID, Done: make(chan struct{}), ConnectedAt: activatedAt.Add(time.Millisecond), BuildVersion: report.RunningVersion, BuildCommit: report.RunningCommit}
	if err := app.AgentHub.connect(postActivationTunnel); err != nil {
		t.Fatal(err)
	}
	freshWrites := 0
	app.agentUpdateFreshTunnelWrite = func(ctx context.Context, query string, args ...any) (sql.Result, error) {
		freshWrites++
		if freshWrites <= 3 {
			return nil, errors.New("injected transient fresh-tunnel write failure")
		}
		return database.ExecContext(ctx, query, args...)
	}
	app.recordAgentUpdateFreshTunnel(postActivationTunnel)
	if freshWrites != 3 {
		t.Fatalf("fresh tunnel writes = %d, want the bounded retry budget to be exhausted", freshWrites)
	}
	var initiallyFresh sql.NullTime
	if err := database.QueryRowContext(ctx, `SELECT fresh_tunnel_at FROM agent_update_assignments WHERE id=?`, assignmentID).Scan(&initiallyFresh); err != nil {
		t.Fatal(err)
	}
	if initiallyFresh.Valid {
		t.Fatal("exhausted initial retries unexpectedly persisted a fresh tunnel")
	}
	app.recordAgentUpdateObservedBuild(agent.ID, agentBuildIdentity{Version: report.RunningVersion, Commit: report.RunningCommit})
	if freshWrites != 4 {
		t.Fatalf("authenticated build report did not retry the fresh-tunnel edge: writes=%d", freshWrites)
	}
	healthy := &p2pstreamv1.ReportAgentUpdateRequest{AgentPublicId: agent.PublicID, Counter: 2, AssignmentId: assignmentID, Generation: 1, State: p2pstreamv1.AgentUpdaterReportState_AGENT_UPDATER_REPORT_STATE_HEALTHY, ManifestSha256: report.ManifestSha256, BinarySha256: report.BinarySha256, RunningVersion: report.RunningVersion, RunningCommit: report.RunningCommit}
	signAgentUpdateTestReport(healthy, updaterPrivate)
	healthyResponse, err := app.ReportAgentUpdate(ctx, connect.NewRequest(healthy))
	if err != nil {
		t.Fatalf("worker-signed healthy report without a second activator signature: %v", err)
	}
	if healthyResponse.Msg.State != p2pstreamv1.AgentUpdateAssignmentState_AGENT_UPDATE_ASSIGNMENT_STATE_HEALTHY_DWELL {
		t.Fatalf("healthy state = %v", healthyResponse.Msg.State)
	}
	if connected := app.AgentHub.connectedByID(agent.ID); connected != postActivationTunnel {
		t.Fatal("non-activation report revoked the post-activation tunnel")
	}
	var freshTunnelAt time.Time
	if err := database.QueryRowContext(ctx, `SELECT fresh_tunnel_at FROM agent_update_assignments WHERE id=?`, assignmentID).Scan(&freshTunnelAt); err != nil {
		t.Fatal(err)
	}
	if !freshTunnelAt.Equal(postActivationTunnel.ConnectedAt) {
		t.Fatalf("fresh tunnel timestamp = %s, want %s", freshTunnelAt, postActivationTunnel.ConnectedAt)
	}
}

func TestAgentUpdateReportRollbackDoesNotConsumeReplayCounters(t *testing.T) {
	ctx := context.Background()
	database := newServerTestDB(t)
	agent := createAgentUpdateTestAgent(t, database, "agent-report-retry")
	app := newAgentUpdateTestApp(t, database)
	updaterPublic, updaterPrivate, _ := ed25519.GenerateKey(rand.Reader)
	activatorPublic, activatorPrivate, _ := ed25519.GenerateKey(rand.Reader)
	insertAgentUpdateIdentity(t, app, database, agent.ID, updaterPublic, activatorPublic)
	_, assignmentID := insertAgentUpdateTestCampaign(t, database, agent.ID, "staged", "none", false)
	authorizeAgentUpdateTestActivation(t, app, agent.ID)

	report := &p2pstreamv1.ReportAgentUpdateRequest{AgentPublicId: agent.PublicID, Counter: 1, AssignmentId: assignmentID, Generation: 1, State: p2pstreamv1.AgentUpdaterReportState_AGENT_UPDATER_REPORT_STATE_ACTIVATED, ManifestSha256: strings.Repeat("a", 64), BinarySha256: strings.Repeat("b", 64), RunningVersion: "v1.2.3", RunningCommit: strings.Repeat("c", 40)}
	report.RootActionReceipt = newAgentUpdateTestRootActionReceipt(t, app, agent.ID, activatorPrivate, agentupdateauth.AssignmentActionActivate, 1)
	signAgentUpdateTestReport(report, updaterPrivate)

	if _, err := database.ExecContext(ctx, `CREATE TRIGGER fail_agent_update_assignment BEFORE UPDATE ON agent_update_assignments WHEN OLD.id=`+fmt.Sprint(assignmentID)+` BEGIN SELECT RAISE(ABORT, 'injected assignment failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := app.ReportAgentUpdate(ctx, connect.NewRequest(report)); err == nil {
		t.Fatal("injected assignment failure unexpectedly succeeded")
	}
	var updaterCounter, rootActionCounter int64
	if err := database.QueryRowContext(ctx, `SELECT last_counter,last_root_action_counter FROM agent_updater_identities WHERE agent_id=?`, agent.ID).Scan(&updaterCounter, &rootActionCounter); err != nil {
		t.Fatal(err)
	}
	if updaterCounter != 0 || rootActionCounter != 0 {
		t.Fatalf("rolled-back report consumed counters: updater=%d root_action=%d", updaterCounter, rootActionCounter)
	}
	assertAgentUpdateAssignmentState(t, database, assignmentID, "cordoned")
	if _, err := database.ExecContext(ctx, `DROP TRIGGER fail_agent_update_assignment`); err != nil {
		t.Fatal(err)
	}
	if _, err := app.ReportAgentUpdate(ctx, connect.NewRequest(report)); err != nil {
		t.Fatalf("exact signed report was not retryable after rollback: %v", err)
	}
	if err := database.QueryRowContext(ctx, `SELECT last_counter,last_root_action_counter FROM agent_updater_identities WHERE agent_id=?`, agent.ID).Scan(&updaterCounter, &rootActionCounter); err != nil {
		t.Fatal(err)
	}
	if updaterCounter != 1 || rootActionCounter != 1 {
		t.Fatalf("successful retry counters: updater=%d root_action=%d", updaterCounter, rootActionCounter)
	}
}

func TestAgentUpdateRootActionReportLostResponseRetryIsIdempotent(t *testing.T) {
	ctx := context.Background()
	database := newServerTestDB(t)
	agent := createAgentUpdateTestAgent(t, database, "agent-root-receipt-retry")
	app := newAgentUpdateTestApp(t, database)
	updaterPublic, updaterPrivate, _ := ed25519.GenerateKey(rand.Reader)
	activatorPublic, activatorPrivate, _ := ed25519.GenerateKey(rand.Reader)
	insertAgentUpdateIdentity(t, app, database, agent.ID, updaterPublic, activatorPublic)
	_, assignmentID := insertAgentUpdateTestCampaign(t, database, agent.ID, "staged", "none", false)
	authorizeAgentUpdateTestActivation(t, app, agent.ID)
	report := &p2pstreamv1.ReportAgentUpdateRequest{
		AgentPublicId: agent.PublicID, Counter: 1, AssignmentId: assignmentID, Generation: 1,
		State:          p2pstreamv1.AgentUpdaterReportState_AGENT_UPDATER_REPORT_STATE_ACTIVATED,
		ManifestSha256: strings.Repeat("a", 64), BinarySha256: strings.Repeat("b", 64),
		RunningVersion: "v1.2.3", RunningCommit: strings.Repeat("c", 40),
	}
	report.RootActionReceipt = newAgentUpdateTestRootActionReceipt(t, app, agent.ID, activatorPrivate, agentupdateauth.AssignmentActionActivate, 1)
	signAgentUpdateTestReport(report, updaterPrivate)
	if _, err := app.ReportAgentUpdate(ctx, connect.NewRequest(report)); err != nil {
		t.Fatal(err)
	}
	if retry, err := app.ReportAgentUpdate(ctx, connect.NewRequest(report)); err != nil || retry.Msg.State != p2pstreamv1.AgentUpdateAssignmentState_AGENT_UPDATE_ASSIGNMENT_STATE_AWAITING_TUNNEL {
		t.Fatalf("exact lost-response retry = %+v, %v", retry, err)
	}
	newCounter := proto.Clone(report).(*p2pstreamv1.ReportAgentUpdateRequest)
	newCounter.Counter = 2
	signAgentUpdateTestReport(newCounter, updaterPrivate)
	if retry, err := app.ReportAgentUpdate(ctx, connect.NewRequest(newCounter)); err != nil || retry.Msg.State != p2pstreamv1.AgentUpdateAssignmentState_AGENT_UPDATE_ASSIGNMENT_STATE_AWAITING_TUNNEL {
		t.Fatalf("new-counter lost-response retry = %+v, %v", retry, err)
	}
	var updaterCounter, rootActionCounter int64
	if err := database.QueryRowContext(ctx, `SELECT last_counter,last_root_action_counter FROM agent_updater_identities WHERE agent_id=?`, agent.ID).Scan(&updaterCounter, &rootActionCounter); err != nil {
		t.Fatal(err)
	}
	if updaterCounter != 2 || rootActionCounter != 1 {
		t.Fatalf("idempotent counters = updater:%d root:%d", updaterCounter, rootActionCounter)
	}
}

func TestAgentUpdateReportRejectsOutOfOrderStateWithoutConsumingCounter(t *testing.T) {
	ctx := context.Background()
	database := newServerTestDB(t)
	agent := createAgentUpdateTestAgent(t, database, "agent-state-order")
	app := newAgentUpdateTestApp(t, database)
	updaterPublic, updaterPrivate, _ := ed25519.GenerateKey(rand.Reader)
	activatorPublic, _, _ := ed25519.GenerateKey(rand.Reader)
	insertAgentUpdateIdentity(t, app, database, agent.ID, updaterPublic, activatorPublic)
	_, assignmentID := insertAgentUpdateTestCampaign(t, database, agent.ID, "staged", "none", false)
	authorizeAgentUpdateTestActivation(t, app, agent.ID)

	outOfOrder := &p2pstreamv1.ReportAgentUpdateRequest{
		AgentPublicId: agent.PublicID, Counter: 1, AssignmentId: assignmentID, Generation: 1,
		State:          p2pstreamv1.AgentUpdaterReportState_AGENT_UPDATER_REPORT_STATE_STAGED,
		ManifestSha256: strings.Repeat("a", 64), BinarySha256: strings.Repeat("b", 64),
	}
	signAgentUpdateTestReport(outOfOrder, updaterPrivate)
	if _, err := app.ReportAgentUpdate(ctx, connect.NewRequest(outOfOrder)); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("out-of-order staged report error = %v", err)
	}
	var state, action string
	var counter int64
	if err := database.QueryRowContext(ctx, `SELECT x.state,x.desired_action,i.last_counter FROM agent_update_assignments x JOIN agent_updater_identities i ON i.agent_id=x.agent_id WHERE x.id=?`, assignmentID).Scan(&state, &action, &counter); err != nil {
		t.Fatal(err)
	}
	if state != "cordoned" || action != "activate" || counter != 0 {
		t.Fatalf("rejected state report mutated durable state: state=%s action=%s counter=%d", state, action, counter)
	}
}

func TestAgentUpdateRollbackStaysCordonedUntilReceiptFreshTunnelAndObservedBuild(t *testing.T) {
	ctx := context.Background()
	database := newServerTestDB(t)
	agent := createAgentUpdateTestAgent(t, database, "agent-rollback-gate")
	app := newAgentUpdateTestApp(t, database)
	updaterPublic, updaterPrivate, _ := ed25519.GenerateKey(rand.Reader)
	activatorPublic, activatorPrivate, _ := ed25519.GenerateKey(rand.Reader)
	insertAgentUpdateIdentity(t, app, database, agent.ID, updaterPublic, activatorPublic)
	campaignID, assignmentID := insertAgentUpdateTestCampaign(t, database, agent.ID, "staged", "none", false)
	authorizeAgentUpdateTestActivation(t, app, agent.ID)

	activation := &p2pstreamv1.ReportAgentUpdateRequest{
		AgentPublicId: agent.PublicID, Counter: 1, AssignmentId: assignmentID, Generation: 1,
		State:          p2pstreamv1.AgentUpdaterReportState_AGENT_UPDATER_REPORT_STATE_ACTIVATED,
		ManifestSha256: strings.Repeat("a", 64), BinarySha256: strings.Repeat("b", 64),
		RunningVersion: "v1.2.3", RunningCommit: strings.Repeat("c", 40),
	}
	activation.RootActionReceipt = newAgentUpdateTestRootActionReceipt(t, app, agent.ID, activatorPrivate, agentupdateauth.AssignmentActionActivate, 1)
	signAgentUpdateTestReport(activation, updaterPrivate)
	if _, err := app.ReportAgentUpdate(ctx, connect.NewRequest(activation)); err != nil {
		t.Fatal(err)
	}
	var originalActivatedAt time.Time
	if err := database.QueryRowContext(ctx, `SELECT activated_at FROM agent_update_assignments WHERE id=?`, assignmentID).Scan(&originalActivatedAt); err != nil {
		t.Fatal(err)
	}

	failed := &p2pstreamv1.ReportAgentUpdateRequest{
		AgentPublicId: agent.PublicID, Counter: 2, AssignmentId: assignmentID, Generation: 1,
		State:       p2pstreamv1.AgentUpdaterReportState_AGENT_UPDATER_REPORT_STATE_FAILED,
		FailureCode: "post_activation_health_failed", FailureDetail: "test failure",
	}
	signAgentUpdateTestReport(failed, updaterPrivate)
	if _, err := app.ReportAgentUpdate(ctx, connect.NewRequest(failed)); err != nil {
		t.Fatal(err)
	}
	assertAgentUpdateAssignmentState(t, database, assignmentID, "blocked")
	if !app.isAgentUpdateCordoned(agent.ID) {
		t.Fatal("failed activated assignment was uncordoned before rollback")
	}
	var blockedAction string
	if err := database.QueryRowContext(ctx, `SELECT desired_action FROM agent_update_assignments WHERE id=?`, assignmentID).Scan(&blockedAction); err != nil {
		t.Fatal(err)
	}
	if blockedAction != "none" {
		t.Fatalf("lower-trust failure report minted rollback action %q", blockedAction)
	}
	failed.Counter = 3
	signAgentUpdateTestReport(failed, updaterPrivate)
	retryFailureResponse, err := app.ReportAgentUpdate(ctx, connect.NewRequest(failed))
	if err != nil || retryFailureResponse.Msg.State != p2pstreamv1.AgentUpdateAssignmentState_AGENT_UPDATE_ASSIGNMENT_STATE_BLOCKED {
		t.Fatalf("lost-response failure retry = response:%+v err:%v", retryFailureResponse, err)
	}
	header := createTestAdminSession(t, app)
	retry := connect.NewRequest(&p2pstreamv1.RetryAgentUpdateAssignmentsRequest{CampaignId: campaignID, ExpectedCampaignGeneration: 2, AssignmentIds: []int64{assignmentID}})
	retry.Header().Set("Cookie", header.Get("Cookie"))
	retryResponse, err := app.RetryAgentUpdateAssignments(ctx, retry)
	if err != nil {
		t.Fatalf("explicit administrator rollback retry failed: %v", err)
	}
	resume := connect.NewRequest(&p2pstreamv1.ChangeAgentUpdateCampaignStateRequest{CampaignId: campaignID, ExpectedGeneration: retryResponse.Msg.Campaign.Generation})
	resume.Header().Set("Cookie", header.Get("Cookie"))
	if _, err := app.ResumeAgentUpdateCampaign(ctx, resume); err != nil {
		t.Fatalf("resuming explicit rollback failed: %v", err)
	}

	check := &p2pstreamv1.CheckAgentUpdateRequest{AgentPublicId: agent.PublicID, Counter: 4}
	check.Signature = ed25519.Sign(updaterPrivate, agentupdateauth.CheckPayload(check.AgentPublicId, check.Counter))
	checkResponse, err := app.CheckAgentUpdate(ctx, connect.NewRequest(check))
	if err != nil {
		t.Fatal(err)
	}
	if checkResponse.Msg.DesiredAction != p2pstreamv1.AgentUpdateDesiredAction_AGENT_UPDATE_DESIRED_ACTION_ROLLBACK || checkResponse.Msg.Authorization == nil || checkResponse.Msg.Authorization.Action != p2pstreamv1.AgentUpdateDesiredAction_AGENT_UPDATE_DESIRED_ACTION_ROLLBACK || checkResponse.Msg.Generation != 2 {
		t.Fatalf("rollback authorization response = %+v", checkResponse.Msg)
	}

	rollbackReceipt := newAgentUpdateTestRootActionReceipt(t, app, agent.ID, activatorPrivate, agentupdateauth.AssignmentActionRollback, 2)
	rollback := &p2pstreamv1.ReportAgentUpdateRequest{
		AgentPublicId: agent.PublicID, Counter: 5, AssignmentId: assignmentID, Generation: 2,
		State:          p2pstreamv1.AgentUpdaterReportState_AGENT_UPDATER_REPORT_STATE_ROLLED_BACK,
		ManifestSha256: rollbackReceipt.ResultManifestSha256, BinarySha256: rollbackReceipt.ResultArtifactSha256,
		RunningVersion: rollbackReceipt.ResultVersion, RunningCommit: rollbackReceipt.ResultCommit,
		RootActionReceipt: rollbackReceipt,
	}
	signAgentUpdateTestReport(rollback, updaterPrivate)
	rollbackResponse, err := app.ReportAgentUpdate(ctx, connect.NewRequest(rollback))
	if err != nil {
		t.Fatal(err)
	}
	if rollbackResponse.Msg.State != p2pstreamv1.AgentUpdateAssignmentState_AGENT_UPDATE_ASSIGNMENT_STATE_AWAITING_TUNNEL || !app.isAgentUpdateCordoned(agent.ID) {
		t.Fatalf("rollback prematurely released routing: response=%+v cordoned=%v", rollbackResponse.Msg, app.isAgentUpdateCordoned(agent.ID))
	}
	var activatedAt, completedAt time.Time
	if err := database.QueryRowContext(ctx, `SELECT activated_at,root_action_completed_at FROM agent_update_assignments WHERE id=?`, assignmentID).Scan(&activatedAt, &completedAt); err != nil {
		t.Fatal(err)
	}
	if !activatedAt.Equal(originalActivatedAt) {
		t.Fatalf("rollback overwrote the original activation edge: got %s want %s", activatedAt, originalActivatedAt)
	}

	// The bootstrap slot may be a pre-feature agent that has no tunnel build
	// headers. Its root-signed exact slot receipt plus this server-forced fresh
	// reconnect must still complete rollback safely.
	postRollbackTunnel := &AgentConn{AgentID: agent.ID, PublicID: agent.PublicID, Done: make(chan struct{}), ConnectedAt: completedAt.Add(time.Millisecond)}
	if err := app.AgentHub.connect(postRollbackTunnel); err != nil {
		t.Fatal(err)
	}
	app.agentUpdateBeforeSuccessCAS = func() { app.AgentHub.disconnect(postRollbackTunnel) }
	app.recordAgentUpdateFreshTunnel(postRollbackTunnel)
	assertAgentUpdateAssignmentState(t, database, assignmentID, "awaiting_tunnel")
	if !app.isAgentUpdateCordoned(agent.ID) {
		t.Fatal("rollback disconnect immediately before recovery CAS released routing")
	}
	app.agentUpdateBeforeSuccessCAS = nil
	recoveryTunnel := &AgentConn{AgentID: agent.ID, PublicID: agent.PublicID, Done: make(chan struct{}), ConnectedAt: completedAt.Add(2 * time.Millisecond)}
	if err := app.AgentHub.connect(recoveryTunnel); err != nil {
		t.Fatal(err)
	}
	app.recordAgentUpdateFreshTunnel(recoveryTunnel)
	assertAgentUpdateAssignmentState(t, database, assignmentID, "failed")
	if app.isAgentUpdateCordoned(agent.ID) {
		t.Fatal("verified post-rollback tunnel and exact observed build remained cordoned")
	}
	var desiredAction string
	var cordoned int64
	if err := database.QueryRowContext(ctx, `SELECT desired_action,cordoned FROM agent_update_assignments WHERE id=?`, assignmentID).Scan(&desiredAction, &cordoned); err != nil {
		t.Fatal(err)
	}
	if desiredAction != "none" || cordoned != 0 {
		t.Fatalf("rollback terminal state = desired_action=%q cordoned=%d", desiredAction, cordoned)
	}
}

func TestCancelCampaignKeepsEscapedActivationCordonedAndSupersedesItWithRollback(t *testing.T) {
	ctx := context.Background()
	database := newServerTestDB(t)
	agent := createAgentUpdateTestAgent(t, database, "agent-cancel-activation")
	app := newAgentUpdateTestApp(t, database)
	updaterPublic, updaterPrivate, _ := ed25519.GenerateKey(rand.Reader)
	activatorPublic, _, _ := ed25519.GenerateKey(rand.Reader)
	insertAgentUpdateIdentity(t, app, database, agent.ID, updaterPublic, activatorPublic)
	campaignID, assignmentID := insertAgentUpdateTestCampaign(t, database, agent.ID, "staged", "none", false)
	authorizeAgentUpdateTestActivation(t, app, agent.ID)

	header := createTestAdminSession(t, app)
	cancelRequest := connect.NewRequest(&p2pstreamv1.ChangeAgentUpdateCampaignStateRequest{CampaignId: campaignID, ExpectedGeneration: 1})
	cancelRequest.Header().Set("Cookie", header.Get("Cookie"))
	if _, err := app.CancelAgentUpdateCampaign(ctx, cancelRequest); err != nil {
		t.Fatal(err)
	}
	var state, action, authorizationAction string
	var generation, cordoned int64
	if err := database.QueryRowContext(ctx, `SELECT state,desired_action,authorization_action,generation,cordoned FROM agent_update_assignments WHERE id=?`, assignmentID).Scan(&state, &action, &authorizationAction, &generation, &cordoned); err != nil {
		t.Fatal(err)
	}
	if state != "cordoned" || action != "rollback" || authorizationAction != "activate" || generation != 2 || cordoned != 1 || !app.isAgentUpdateCordoned(agent.ID) {
		t.Fatalf("cancelled escaped activation = state:%s action:%s auth:%s generation:%d cordoned:%d", state, action, authorizationAction, generation, cordoned)
	}

	check := &p2pstreamv1.CheckAgentUpdateRequest{AgentPublicId: agent.PublicID, Counter: 1}
	check.Signature = ed25519.Sign(updaterPrivate, agentupdateauth.CheckPayload(check.AgentPublicId, check.Counter))
	response, err := app.CheckAgentUpdate(ctx, connect.NewRequest(check))
	if err != nil {
		t.Fatal(err)
	}
	if response.Msg.DesiredAction != p2pstreamv1.AgentUpdateDesiredAction_AGENT_UPDATE_DESIRED_ACTION_ROLLBACK || response.Msg.Authorization == nil || response.Msg.Authorization.Action != p2pstreamv1.AgentUpdateDesiredAction_AGENT_UPDATE_DESIRED_ACTION_ROLLBACK || response.Msg.Authorization.Generation != 2 || response.Msg.Authorization.CommandSequence != 2 {
		t.Fatalf("cancel rollback supersession response = %+v", response.Msg)
	}
}

func TestAgentUpdateSuccessRequiresAttestationFreshTunnelBuildAndDwell(t *testing.T) {
	ctx := context.Background()
	database := newServerTestDB(t)
	agent := createAgentUpdateTestAgent(t, database, "agent-success-gate")
	app := newAgentUpdateTestApp(t, database)
	campaignID, assignmentID := insertAgentUpdateTestCampaign(t, database, agent.ID, "healthy_dwell", "none", true)
	now := time.Now().UTC()
	_, err := database.ExecContext(ctx, `UPDATE agent_update_assignments SET authorization_action='activate',attested_manifest_sha256=?,attested_binary_sha256=?,root_action_completed_at=?,root_result_kind='signed_release',root_result_manifest_sha256=?,root_result_version=?,root_result_commit=?,root_result_artifact_sha256=?,activated_at=?,healthy_at=?,running_version=?,running_commit=?,observed_version=?,observed_commit=? WHERE id=?`, strings.Repeat("a", 64), strings.Repeat("b", 64), now.Add(-4*time.Minute), strings.Repeat("a", 64), "v1.2.3", strings.Repeat("c", 40), strings.Repeat("b", 64), now.Add(-4*time.Minute), now.Add(-3*time.Minute), "v1.2.3", strings.Repeat("c", 40), "v1.2.3", strings.Repeat("c", 40), assignmentID)
	if err != nil {
		t.Fatal(err)
	}
	app.setAgentUpdateCordon(agent.ID)
	app.agentUpdatesMu.Lock()
	app.reconcileAgentUpdateSuccessLocked(ctx, agent.ID)
	app.agentUpdatesMu.Unlock()
	assertAgentUpdateAssignmentState(t, database, assignmentID, "healthy_dwell")
	freshAt := now.Add(-2 * time.Minute).Truncate(time.Millisecond)
	if _, err := database.ExecContext(ctx, `UPDATE agent_update_assignments SET fresh_tunnel_at=?,updated_at=? WHERE id=?`, freshAt, freshAt, assignmentID); err != nil {
		t.Fatal(err)
	}
	freshConn := &AgentConn{AgentID: agent.ID, PublicID: agent.PublicID, Done: make(chan struct{}), ConnectedAt: freshAt, BuildVersion: "v1.2.3", BuildCommit: strings.Repeat("c", 40)}
	if err := app.AgentHub.connect(freshConn); err != nil {
		t.Fatal(err)
	}
	app.storeLatestAgentBuild(agent.ID, agentBuildIdentity{Version: "v1.2.3", Commit: strings.Repeat("c", 40)})
	app.agentUpdatesMu.Lock()
	app.reconcileAgentUpdateSuccessLocked(ctx, agent.ID)
	app.agentUpdatesMu.Unlock()
	assertAgentUpdateAssignmentState(t, database, assignmentID, "succeeded")
	if app.isAgentUpdateCordoned(agent.ID) {
		t.Fatal("successful assignment remained cordoned")
	}
	var campaignState string
	if err := database.QueryRowContext(ctx, `SELECT state FROM agent_update_campaigns WHERE id=?`, campaignID).Scan(&campaignState); err != nil || campaignState != "completed" {
		t.Fatalf("campaign state = %q, err=%v", campaignState, err)
	}
}

func TestAgentUpdateSuccessRequiresFreshTunnelToRemainConnectedThroughDwell(t *testing.T) {
	ctx := context.Background()
	database := newServerTestDB(t)
	agent := createAgentUpdateTestAgent(t, database, "agent-live-dwell-gate")
	app := newAgentUpdateTestApp(t, database)
	_, assignmentID := insertAgentUpdateTestCampaign(t, database, agent.ID, "healthy_dwell", "none", true)
	now := time.Now().UTC()
	freshAt := now.Add(-time.Minute).Truncate(time.Millisecond)
	if _, err := database.ExecContext(ctx, `UPDATE agent_update_assignments SET authorization_action='activate',attested_manifest_sha256=?,attested_binary_sha256=?,root_action_completed_at=?,root_result_kind='signed_release',root_result_manifest_sha256=?,root_result_version='v1.2.3',root_result_commit=?,root_result_artifact_sha256=?,activated_at=?,fresh_tunnel_at=?,healthy_at=?,running_version='v1.2.3',running_commit=?,observed_version='v1.2.3',observed_commit=?,updated_at=? WHERE id=?`, strings.Repeat("a", 64), strings.Repeat("b", 64), now.Add(-2*time.Minute), strings.Repeat("a", 64), strings.Repeat("c", 40), strings.Repeat("b", 64), now.Add(-2*time.Minute), freshAt, freshAt, strings.Repeat("c", 40), strings.Repeat("c", 40), freshAt, assignmentID); err != nil {
		t.Fatal(err)
	}
	freshConn := &AgentConn{AgentID: agent.ID, PublicID: agent.PublicID, Done: make(chan struct{}), ConnectedAt: freshAt, BuildVersion: "v1.2.3", BuildCommit: strings.Repeat("c", 40)}
	if err := app.AgentHub.connect(freshConn); err != nil {
		t.Fatal(err)
	}
	app.storeLatestAgentBuild(agent.ID, agentBuildIdentity{Version: "v1.2.3", Commit: strings.Repeat("c", 40)})
	app.AgentHub.disconnect(freshConn)
	app.setAgentUpdateCordon(agent.ID)
	app.agentUpdatesMu.Lock()
	app.reconcileAgentUpdateSuccessLocked(ctx, agent.ID)
	app.agentUpdatesMu.Unlock()
	assertAgentUpdateAssignmentState(t, database, assignmentID, "healthy_dwell")
	if !app.isAgentUpdateCordoned(agent.ID) {
		t.Fatal("offline agent was uncordoned after health dwell")
	}
}

func TestAgentUpdateDisconnectImmediatelyBeforeSuccessCASCannotAdvance(t *testing.T) {
	ctx := context.Background()
	database := newServerTestDB(t)
	agent := createAgentUpdateTestAgent(t, database, "agent-success-cas-disconnect")
	app := newAgentUpdateTestApp(t, database)
	_, assignmentID := insertAgentUpdateTestCampaign(t, database, agent.ID, "healthy_dwell", "none", true)
	now := time.Now().UTC()
	freshAt := now.Add(-time.Minute).Truncate(time.Millisecond)
	if _, err := database.ExecContext(ctx, `UPDATE agent_update_assignments SET authorization_action='activate',attested_manifest_sha256=?,attested_binary_sha256=?,root_action_completed_at=?,root_result_kind='signed_release',root_result_manifest_sha256=?,root_result_version='v1.2.3',root_result_commit=?,root_result_artifact_sha256=?,activated_at=?,fresh_tunnel_at=?,healthy_at=?,running_version='v1.2.3',running_commit=?,observed_version='v1.2.3',observed_commit=?,updated_at=? WHERE id=?`, strings.Repeat("a", 64), strings.Repeat("b", 64), now.Add(-2*time.Minute), strings.Repeat("a", 64), strings.Repeat("c", 40), strings.Repeat("b", 64), now.Add(-2*time.Minute), freshAt, freshAt, strings.Repeat("c", 40), strings.Repeat("c", 40), freshAt, assignmentID); err != nil {
		t.Fatal(err)
	}
	freshConn := &AgentConn{AgentID: agent.ID, PublicID: agent.PublicID, Done: make(chan struct{}), ConnectedAt: freshAt, BuildVersion: "v1.2.3", BuildCommit: strings.Repeat("c", 40)}
	if err := app.AgentHub.connect(freshConn); err != nil {
		t.Fatal(err)
	}
	app.setAgentUpdateCordon(agent.ID)
	app.agentUpdateBeforeSuccessCAS = func() { app.AgentHub.disconnect(freshConn) }
	app.agentUpdatesMu.Lock()
	app.reconcileAgentUpdateSuccessLocked(ctx, agent.ID)
	app.agentUpdatesMu.Unlock()
	assertAgentUpdateAssignmentState(t, database, assignmentID, "healthy_dwell")
	if !app.isAgentUpdateCordoned(agent.ID) {
		t.Fatal("disconnect immediately before success CAS released routing")
	}
}

func TestAgentUpdateLateFreshTunnelRestartsDwell(t *testing.T) {
	ctx := context.Background()
	database := newServerTestDB(t)
	agent := createAgentUpdateTestAgent(t, database, "agent-late-fresh-dwell")
	app := newAgentUpdateTestApp(t, database)
	_, assignmentID := insertAgentUpdateTestCampaign(t, database, agent.ID, "healthy_dwell", "none", true)
	now := time.Now().UTC()
	freshAt := now.Truncate(time.Millisecond)
	if _, err := database.ExecContext(ctx, `UPDATE agent_update_assignments SET authorization_action='activate',attested_manifest_sha256=?,attested_binary_sha256=?,root_action_completed_at=?,root_result_kind='signed_release',root_result_manifest_sha256=?,root_result_version='v1.2.3',root_result_commit=?,root_result_artifact_sha256=?,activated_at=?,fresh_tunnel_at=?,healthy_at=?,running_version='v1.2.3',running_commit=?,observed_version='v1.2.3',observed_commit=?,updated_at=? WHERE id=?`, strings.Repeat("a", 64), strings.Repeat("b", 64), now.Add(-2*time.Minute), strings.Repeat("a", 64), strings.Repeat("c", 40), strings.Repeat("b", 64), now.Add(-2*time.Minute), freshAt, now.Add(-time.Minute), strings.Repeat("c", 40), strings.Repeat("c", 40), freshAt, assignmentID); err != nil {
		t.Fatal(err)
	}
	freshConn := &AgentConn{AgentID: agent.ID, PublicID: agent.PublicID, Done: make(chan struct{}), ConnectedAt: freshAt, BuildVersion: "v1.2.3", BuildCommit: strings.Repeat("c", 40)}
	if err := app.AgentHub.connect(freshConn); err != nil {
		t.Fatal(err)
	}
	app.storeLatestAgentBuild(agent.ID, agentBuildIdentity{Version: "v1.2.3", Commit: strings.Repeat("c", 40)})
	app.setAgentUpdateCordon(agent.ID)
	app.agentUpdatesMu.Lock()
	app.reconcileAgentUpdateSuccessLocked(ctx, agent.ID)
	app.agentUpdatesMu.Unlock()
	assertAgentUpdateAssignmentState(t, database, assignmentID, "healthy_dwell")
}

func TestAgentUpdatePeriodicIdenticalBuildReportsDoNotPostponeDwell(t *testing.T) {
	ctx := context.Background()
	database := newServerTestDB(t)
	agent := createAgentUpdateTestAgent(t, database, "agent-periodic-build-dwell")
	app := newAgentUpdateTestApp(t, database)
	_, assignmentID := insertAgentUpdateTestCampaign(t, database, agent.ID, "healthy_dwell", "none", true)
	now := time.Now().UTC()
	freshAt := now.Add(-time.Minute).Truncate(time.Millisecond)
	if _, err := database.ExecContext(ctx, `UPDATE agent_update_assignments SET authorization_action='activate',attested_manifest_sha256=?,attested_binary_sha256=?,root_action_completed_at=?,root_result_kind='signed_release',root_result_manifest_sha256=?,root_result_version='v1.2.3',root_result_commit=?,root_result_artifact_sha256=?,activated_at=?,fresh_tunnel_at=?,healthy_at=?,running_version='v1.2.3',running_commit=?,observed_version='v1.2.3',observed_commit=?,updated_at=? WHERE id=?`, strings.Repeat("a", 64), strings.Repeat("b", 64), now.Add(-2*time.Minute), strings.Repeat("a", 64), strings.Repeat("c", 40), strings.Repeat("b", 64), now.Add(-2*time.Minute), freshAt, freshAt, strings.Repeat("c", 40), strings.Repeat("c", 40), freshAt, assignmentID); err != nil {
		t.Fatal(err)
	}
	freshConn := &AgentConn{AgentID: agent.ID, PublicID: agent.PublicID, Done: make(chan struct{}), ConnectedAt: freshAt, BuildVersion: "v1.2.3", BuildCommit: strings.Repeat("c", 40)}
	if err := app.AgentHub.connect(freshConn); err != nil {
		t.Fatal(err)
	}
	app.storeLatestAgentBuild(agent.ID, agentBuildIdentity{Version: "v1.2.3", Commit: strings.Repeat("c", 40)})
	app.setAgentUpdateCordon(agent.ID)
	app.recordAgentUpdateObservedBuild(agent.ID, agentBuildIdentity{Version: "v1.2.3", Commit: strings.Repeat("c", 40)})
	assertAgentUpdateAssignmentState(t, database, assignmentID, "succeeded")
}

func TestAgentUpdateStatsFromOldProcessCannotSatisfyFreshTunnelBuildEvidence(t *testing.T) {
	ctx := context.Background()
	database := newServerTestDB(t)
	agent := createAgentUpdateTestAgent(t, database, "agent-stale-stats-evidence")
	app := newAgentUpdateTestApp(t, database)
	_, assignmentID := insertAgentUpdateTestCampaign(t, database, agent.ID, "healthy_dwell", "none", true)
	now := time.Now().UTC()
	freshAt := now.Add(-time.Minute).Truncate(time.Millisecond)
	if _, err := database.ExecContext(ctx, `UPDATE agent_update_assignments SET authorization_action='activate',attested_manifest_sha256=?,attested_binary_sha256=?,root_action_completed_at=?,root_result_kind='signed_release',root_result_manifest_sha256=?,root_result_version='v1.2.3',root_result_commit=?,root_result_artifact_sha256=?,activated_at=?,healthy_at=?,running_version='v1.2.3',running_commit=?,updated_at=? WHERE id=?`, strings.Repeat("a", 64), strings.Repeat("b", 64), now.Add(-2*time.Minute), strings.Repeat("a", 64), strings.Repeat("c", 40), strings.Repeat("b", 64), now.Add(-2*time.Minute), freshAt, strings.Repeat("c", 40), freshAt, assignmentID); err != nil {
		t.Fatal(err)
	}
	// The current tunnel is an old/wrong build. A displaced process then sends
	// target-looking stats using the same agent bearer.
	current := &AgentConn{AgentID: agent.ID, PublicID: agent.PublicID, Done: make(chan struct{}), ConnectedAt: freshAt, BuildVersion: "v1.1.0", BuildCommit: strings.Repeat("d", 40)}
	if err := app.AgentHub.connect(current); err != nil {
		t.Fatal(err)
	}
	app.setAgentUpdateCordon(agent.ID)
	app.recordAgentUpdateFreshTunnel(current)
	staleTargetStats := agentBuildIdentity{Version: "v1.2.3", Commit: strings.Repeat("c", 40)}
	app.storeLatestAgentBuild(agent.ID, staleTargetStats)
	app.recordAgentUpdateObservedBuild(agent.ID, staleTargetStats)
	assertAgentUpdateAssignmentState(t, database, assignmentID, "healthy_dwell")
	var observedVersion, observedCommit string
	if err := database.QueryRowContext(ctx, `SELECT observed_version,observed_commit FROM agent_update_assignments WHERE id=?`, assignmentID).Scan(&observedVersion, &observedCommit); err != nil {
		t.Fatal(err)
	}
	if observedVersion != current.BuildVersion || observedCommit != current.BuildCommit {
		t.Fatalf("stale stats replaced exact-session build evidence: version=%s commit=%s", observedVersion, observedCommit)
	}
}

func TestAgentUpdateIdempotentHealthyReportsDoNotPostponeDwell(t *testing.T) {
	ctx := context.Background()
	database := newServerTestDB(t)
	agent := createAgentUpdateTestAgent(t, database, "agent-healthy-retry-dwell")
	app := newAgentUpdateTestApp(t, database)
	updaterPublic, updaterPrivate, _ := ed25519.GenerateKey(rand.Reader)
	activatorPublic, _, _ := ed25519.GenerateKey(rand.Reader)
	insertAgentUpdateIdentity(t, app, database, agent.ID, updaterPublic, activatorPublic)
	_, assignmentID := insertAgentUpdateTestCampaign(t, database, agent.ID, "healthy_dwell", "none", true)
	anchor := time.Now().UTC().Truncate(time.Millisecond)
	if _, err := database.ExecContext(ctx, `UPDATE agent_update_assignments SET authorization_action='activate',attested_manifest_sha256=?,attested_binary_sha256=?,root_action_completed_at=?,root_action_receipt_payload=X'01',root_result_kind='signed_release',root_result_manifest_sha256=?,root_result_version='v1.2.3',root_result_commit=?,root_result_artifact_sha256=?,activated_at=?,fresh_tunnel_at=?,healthy_at=?,running_version='v1.2.3',running_commit=?,observed_version='v1.2.3',observed_commit=?,updated_at=? WHERE id=?`, strings.Repeat("a", 64), strings.Repeat("b", 64), anchor.Add(-time.Minute), strings.Repeat("a", 64), strings.Repeat("c", 40), strings.Repeat("b", 64), anchor.Add(-time.Minute), anchor, anchor, strings.Repeat("c", 40), strings.Repeat("c", 40), anchor, assignmentID); err != nil {
		t.Fatal(err)
	}
	freshConn := &AgentConn{AgentID: agent.ID, PublicID: agent.PublicID, Done: make(chan struct{}), ConnectedAt: anchor, BuildVersion: "v1.2.3", BuildCommit: strings.Repeat("c", 40)}
	if err := app.AgentHub.connect(freshConn); err != nil {
		t.Fatal(err)
	}
	app.storeLatestAgentBuild(agent.ID, agentBuildIdentity{Version: "v1.2.3", Commit: strings.Repeat("c", 40)})
	app.setAgentUpdateCordon(agent.ID)
	for counter := uint64(1); counter <= 2; counter++ {
		report := &p2pstreamv1.ReportAgentUpdateRequest{
			AgentPublicId:  agent.PublicID,
			Counter:        counter,
			AssignmentId:   assignmentID,
			Generation:     1,
			State:          p2pstreamv1.AgentUpdaterReportState_AGENT_UPDATER_REPORT_STATE_HEALTHY,
			ManifestSha256: strings.Repeat("a", 64),
			BinarySha256:   strings.Repeat("b", 64),
			RunningVersion: "v1.2.3",
			RunningCommit:  strings.Repeat("c", 40),
		}
		signAgentUpdateTestReport(report, updaterPrivate)
		if _, err := app.ReportAgentUpdate(ctx, connect.NewRequest(report)); err != nil {
			t.Fatal(err)
		}
	}
	var updatedAt time.Time
	if err := database.QueryRowContext(ctx, `SELECT updated_at FROM agent_update_assignments WHERE id=?`, assignmentID).Scan(&updatedAt); err != nil {
		t.Fatal(err)
	}
	if !updatedAt.Equal(anchor) {
		t.Fatalf("idempotent HEALTHY retry moved dwell anchor: got=%s want=%s", updatedAt, anchor)
	}
	old := anchor.Add(-time.Minute)
	freshConn.ConnectedAt = old
	if _, err := database.ExecContext(ctx, `UPDATE agent_update_assignments SET fresh_tunnel_at=?,healthy_at=?,updated_at=? WHERE id=?`, old, old, old, assignmentID); err != nil {
		t.Fatal(err)
	}
	app.agentUpdatesMu.Lock()
	app.reconcileAgentUpdateSuccessLocked(ctx, agent.ID)
	app.agentUpdatesMu.Unlock()
	assertAgentUpdateAssignmentState(t, database, assignmentID, "succeeded")
}

func TestCancelledHealthyAssignmentCannotRaceIntoSuccess(t *testing.T) {
	ctx := context.Background()
	database := newServerTestDB(t)
	agent := createAgentUpdateTestAgent(t, database, "agent-cancel-success-race")
	app := newAgentUpdateTestApp(t, database)
	campaignID, assignmentID := insertAgentUpdateTestCampaign(t, database, agent.ID, "healthy_dwell", "none", true)
	now := time.Now().UTC()
	freshAt := now.Add(-time.Minute).Truncate(time.Millisecond)
	if _, err := database.ExecContext(ctx, `UPDATE agent_update_assignments SET authorization_action='activate',attested_manifest_sha256=?,attested_binary_sha256=?,root_action_completed_at=?,root_result_kind='signed_release',root_result_manifest_sha256=?,root_result_version='v1.2.3',root_result_commit=?,root_result_artifact_sha256=?,activated_at=?,fresh_tunnel_at=?,healthy_at=?,running_version='v1.2.3',running_commit=?,observed_version='v1.2.3',observed_commit=?,updated_at=? WHERE id=?`, strings.Repeat("a", 64), strings.Repeat("b", 64), now.Add(-2*time.Minute), strings.Repeat("a", 64), strings.Repeat("c", 40), strings.Repeat("b", 64), now.Add(-2*time.Minute), freshAt, freshAt, strings.Repeat("c", 40), strings.Repeat("c", 40), freshAt, assignmentID); err != nil {
		t.Fatal(err)
	}
	freshConn := &AgentConn{AgentID: agent.ID, PublicID: agent.PublicID, Done: make(chan struct{}), ConnectedAt: freshAt, BuildVersion: "v1.2.3", BuildCommit: strings.Repeat("c", 40)}
	if err := app.AgentHub.connect(freshConn); err != nil {
		t.Fatal(err)
	}
	app.storeLatestAgentBuild(agent.ID, agentBuildIdentity{Version: "v1.2.3", Commit: strings.Repeat("c", 40)})
	app.setAgentUpdateCordon(agent.ID)
	header := createTestAdminSession(t, app)
	request := connect.NewRequest(&p2pstreamv1.ChangeAgentUpdateCampaignStateRequest{CampaignId: campaignID, ExpectedGeneration: 1})
	request.Header().Set("Cookie", header.Get("Cookie"))
	if _, err := app.CancelAgentUpdateCampaign(ctx, request); err != nil {
		t.Fatal(err)
	}
	app.agentUpdatesMu.Lock()
	app.reconcileAgentUpdateSuccessLocked(ctx, agent.ID)
	app.agentUpdatesMu.Unlock()
	var state, action, campaignState string
	var cordoned int64
	if err := database.QueryRowContext(ctx, `SELECT x.state,x.desired_action,x.cordoned,c.state FROM agent_update_assignments x JOIN agent_update_campaigns c ON c.id=x.campaign_id WHERE x.id=?`, assignmentID).Scan(&state, &action, &cordoned, &campaignState); err != nil {
		t.Fatal(err)
	}
	if state == "succeeded" || action != "rollback" || cordoned != 1 || campaignState != "cancelled" || !app.isAgentUpdateCordoned(agent.ID) {
		t.Fatalf("cancelled activation escaped rollback fence: assignment=%s action=%s cordoned=%d campaign=%s", state, action, cordoned, campaignState)
	}
}

func TestAgentUpdateCordonReloadPreservesPublishedFenceOnQueryAndScanFailure(t *testing.T) {
	database := newServerTestDB(t)
	agent := createAgentUpdateTestAgent(t, database, "agent-cordon-reload")
	_, _ = insertAgentUpdateTestCampaign(t, database, agent.ID, "cordoned", "activate", true)
	app := newAgentUpdateTestApp(t, database)
	if !app.isAgentUpdateCordoned(agent.ID) {
		t.Fatal("persisted cordon was not loaded")
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := app.reloadAgentUpdateCordons(cancelled); err == nil {
		t.Fatal("cancelled cordon query unexpectedly succeeded")
	}
	if !app.isAgentUpdateCordoned(agent.ID) {
		t.Fatal("query failure removed the previously published cordon")
	}

	database.SetMaxOpenConns(1)
	if _, err := database.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE agent_update_assignments SET agent_id='not-an-integer' WHERE agent_id=?`, agent.ID); err != nil {
		t.Fatal(err)
	}
	if err := app.reloadAgentUpdateCordons(context.Background()); err == nil {
		t.Fatal("corrupt cordon row unexpectedly scanned")
	}
	if !app.isAgentUpdateCordoned(agent.ID) {
		t.Fatal("scan failure removed the previously published cordon")
	}
	if _, err := NewAppWithError(nil, database); err == nil {
		t.Fatal("application startup accepted unreadable persisted cordon state")
	}
}

func TestAgentUpdateDatabaseRejectsConcurrentActiveAssignments(t *testing.T) {
	database := newServerTestDB(t)
	agent := createAgentUpdateTestAgent(t, database, "agent-exclusive")
	_, first := insertAgentUpdateTestCampaign(t, database, agent.ID, "pending", "stage", false)
	now := time.Now().UTC()
	result, err := database.Exec(`INSERT INTO agent_update_campaigns (name,state,generation,target_version,target_commit,manifest_sha256,release_sequence,root_version,security_epoch,minimum_updater_version,minimum_tunnel_protocol,maximum_tunnel_protocol,artifacts_json,max_unavailable,minimum_eligible_agents_per_route,canary_count,wave_size,healthy_dwell_millis,created_at,updated_at) SELECT 'second','running',1,target_version,target_commit,manifest_sha256,release_sequence,root_version,security_epoch,minimum_updater_version,minimum_tunnel_protocol,maximum_tunnel_protocol,artifacts_json,1,1,1,1,10000,?,? FROM agent_update_campaigns LIMIT 1`, now, now)
	if err != nil {
		t.Fatal(err)
	}
	secondCampaign, _ := result.LastInsertId()
	if _, err := database.Exec(`INSERT INTO agent_update_assignments (campaign_id,agent_id,state,desired_action,generation,created_at,updated_at) VALUES (?,?,'pending','stage',1,?,?)`, secondCampaign, agent.ID, now, now); err == nil {
		t.Fatal("database accepted two active assignments for one agent")
	}
	if _, err := database.Exec(`UPDATE agent_update_assignments SET state='failed',desired_action='none' WHERE id=?`, first); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO agent_update_assignments (campaign_id,agent_id,state,desired_action,generation,created_at,updated_at) VALUES (?,?,'pending','stage',1,?,?)`, secondCampaign, agent.ID, now, now); err != nil {
		t.Fatalf("terminal assignment did not release exclusivity: %v", err)
	}
}

func TestAgentUpdatePreviewThousandAgentFleetCompletesWithinBound(t *testing.T) {
	app, ids, target := newAgentUpdatePreviewScaleFixture(t, 1000)
	started := time.Now()
	preview, err := app.previewAgentUpdateAgents(context.Background(), ids, target, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview) != len(ids) {
		t.Fatalf("preview agents = %d, want %d", len(preview), len(ids))
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("1000-agent preview took %s, want <= 2s", elapsed)
	}
}

func BenchmarkAgentUpdatePreviewThousandAgentFleet(b *testing.B) {
	app, ids, target := newAgentUpdatePreviewScaleFixture(b, 1000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := app.previewAgentUpdateAgents(context.Background(), ids, target, 1); err != nil {
			b.Fatal(err)
		}
	}
}

func newAgentUpdatePreviewScaleFixture(tb testing.TB, count int) (*App, []int64, *p2pstreamv1.AgentUpdateTarget) {
	tb.Helper()
	database, err := db.Open(filepath.Join(tb.TempDir(), "agent-update-preview.db"))
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { _ = database.Close() })
	app := newAgentUpdateTestApp(tb, database)
	tx, err := database.Begin()
	if err != nil {
		tb.Fatal(err)
	}
	defer tx.Rollback()
	authority := app.AgentUpdateAuthority.Identity()
	now := time.Now().UTC()
	ids := make([]int64, 0, count)
	for i := 0; i < count; i++ {
		publicID := fmt.Sprintf("agent-preview-%04d", i)
		result, err := tx.Exec(`INSERT INTO agents (public_id,name,token_hash,enabled,created_at,updated_at) VALUES (?,?,?,1,?,?)`, publicID, publicID, strings.Repeat("0", 64), now, now)
		if err != nil {
			tb.Fatal(err)
		}
		agentID, _ := result.LastInsertId()
		ids = append(ids, agentID)
		updaterKey := make([]byte, ed25519.PublicKeySize)
		activatorKey := make([]byte, ed25519.PublicKeySize)
		binary.LittleEndian.PutUint64(updaterKey, uint64(i+1))
		binary.LittleEndian.PutUint64(activatorKey, uint64(count+i+1))
		_, err = tx.Exec(`INSERT INTO agent_updater_identities (agent_id,updater_key_id,updater_public_key,activator_key_id,activator_public_key,os,arch,updater_version,trusted_root_sha256,trusted_root_version,pinned_repository,authority_key_id,authority_epoch,enrollment_generation,enrollment_receipt_payload,enrollment_receipt_signature,enabled,enrolled_at,last_seen_at,updated_at) VALUES (?,?,?,?,?,'linux','amd64','v1.0.0',?,1,'owner/repo',?,?,1,X'',?,1,?,?,?)`, agentID, fmt.Sprintf("updater-%04d", i), updaterKey, fmt.Sprintf("activator-%04d", i), activatorKey, strings.Repeat("d", 64), authority.KeyID, int64(authority.Epoch), make([]byte, ed25519.SignatureSize), now, now, now)
		if err != nil {
			tb.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		tb.Fatal(err)
	}
	target := &p2pstreamv1.AgentUpdateTarget{MinimumUpdaterVersion: "v1.0.0", Artifacts: []*p2pstreamv1.AgentUpdateArtifact{{Os: "linux", Arch: "amd64", Name: "p2pstream", SizeBytes: 1, Sha256: strings.Repeat("a", 64)}}}
	return app, ids, target
}

func createAgentUpdateTestAgent(t *testing.T, database *db.DB, publicID string) db.Agent {
	t.Helper()
	agent, err := database.CreateAgent(context.Background(), db.CreateAgentParams{PublicID: publicID, Name: publicID, TokenHash: hashAgentToken("test-token-" + publicID), Enabled: 1})
	if err != nil {
		t.Fatal(err)
	}
	return agent
}

func insertAgentUpdateIdentity(t *testing.T, app *App, database *db.DB, agentID int64, updaterPublic, activatorPublic ed25519.PublicKey) {
	t.Helper()
	now := time.Now().UTC()
	authority := app.AgentUpdateAuthority.Identity()
	_, err := database.Exec(`INSERT INTO agent_updater_identities (agent_id,updater_key_id,updater_public_key,activator_key_id,activator_public_key,os,arch,updater_version,trusted_root_sha256,trusted_root_version,pinned_repository,authority_key_id,authority_epoch,enrollment_generation,enrollment_receipt_payload,enrollment_receipt_signature,enabled,enrolled_at,last_seen_at,updated_at) VALUES (?,?,?,?,?,'linux','amd64','v1.0.0',?,1,'owner/repo',?,?,1,X'',?,1,?,?,?)`, agentID, agentUpdateKeyID(updaterPublic), []byte(updaterPublic), agentUpdateKeyID(activatorPublic), []byte(activatorPublic), strings.Repeat("d", 64), authority.KeyID, int64(authority.Epoch), make([]byte, ed25519.SignatureSize), now, now, now)
	if err != nil {
		t.Fatal(err)
	}
}

func authorizeAgentUpdateTestActivation(t *testing.T, app *App, agentID int64) {
	t.Helper()
	assignment, campaign, err := app.activeAgentUpdateAssignment(context.Background(), agentID)
	if err != nil {
		t.Fatal(err)
	}
	app.tryAdvanceAgentUpdateAssignment(context.Background(), assignment, campaign)
	assignment, _, err = app.activeAgentUpdateAssignment(context.Background(), agentID)
	if err != nil {
		t.Fatal(err)
	}
	if assignment.State != "cordoned" || assignment.DesiredAction != "activate" || assignment.AuthorizationAction != "activate" || len(assignment.AuthorizationPayload) == 0 || len(assignment.AuthorizationSignature) != ed25519.SignatureSize {
		t.Fatalf("activation authorization was not durably issued: %+v", assignment)
	}
}

func newAgentUpdateTestRootActionReceipt(t *testing.T, app *App, agentID int64, activatorPrivate ed25519.PrivateKey, action agentupdateauth.AssignmentAction, counter uint64) *p2pstreamv1.AgentUpdateRootActionReceipt {
	t.Helper()
	ctx := context.Background()
	assignment, campaign, err := app.activeAgentUpdateAssignment(ctx, agentID)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := agentUpdaterIdentityByAgentID(ctx, app.DB, agentID)
	if err != nil {
		t.Fatal(err)
	}
	authorization, err := storedAssignmentAuthorization(assignment, campaign, identity)
	if err != nil {
		t.Fatal(err)
	}
	artifact := artifactForPlatform(campaign.Artifacts, identity.Os, identity.Arch)
	if artifact == nil {
		t.Fatal("missing test platform artifact")
	}
	receipt := agentupdateauth.RootActionReceipt{
		AgentPublicID: identity.AgentPublicID, AssignmentID: assignment.ID, CampaignID: assignment.CampaignID,
		Generation: assignment.Generation, Action: action, CommandSequence: authorization.Value.CommandSequence,
		AuthorizationSHA256: authorization.SHA256, AuthorizationNonce: append([]byte(nil), authorization.Value.Nonce...),
		AuthorityKeyID: identity.AuthorityKeyID, AuthorityEpoch: uint64(identity.AuthorityEpoch), ActivatorKeyID: identity.ActivatorKeyID,
		RootActionCounter: counter, CompletedAtUnixMillis: time.Now().UTC().UnixMilli(), ResultKind: agentupdateauth.RootActionResultSignedRelease,
		ResultRootVersion: uint64(campaign.RootVersion), ResultManifestSHA256: campaign.ManifestSha256,
		ResultVersion: campaign.TargetVersion, ResultCommit: campaign.TargetCommit,
		ResultReleaseSequence: uint64(campaign.ReleaseSequence), ResultSecurityEpoch: uint64(campaign.SecurityEpoch),
		ResultOS: identity.Os, ResultArch: identity.Arch, ResultArtifactName: artifact.Name,
		ResultArtifactSize: artifact.SizeBytes, ResultArtifactSHA256: artifact.Sha256,
	}
	if action == agentupdateauth.AssignmentActionRollback {
		receipt.ResultKind = agentupdateauth.RootActionResultBootstrap
		receipt.ResultRootVersion = 0
		receipt.ResultManifestSHA256 = ""
		receipt.ResultVersion = "v1.0.0"
		receipt.ResultCommit = strings.Repeat("d", 40)
		receipt.ResultReleaseSequence = 0
		receipt.ResultSecurityEpoch = 0
		receipt.ResultArtifactName = "p2pstream-bootstrap"
		receipt.ResultArtifactSize = 4321
		receipt.ResultArtifactSHA256 = strings.Repeat("e", 64)
	}
	payload, err := agentupdateauth.RootActionReceiptPayload(receipt)
	if err != nil {
		t.Fatal(err)
	}
	signature, err := agentupdateauth.SignRootActionReceipt(activatorPrivate, receipt)
	if err != nil {
		t.Fatal(err)
	}
	resultKind := p2pstreamv1.AgentUpdateRootActionResultKind_AGENT_UPDATE_ROOT_ACTION_RESULT_KIND_SIGNED_RELEASE
	if receipt.ResultKind == agentupdateauth.RootActionResultBootstrap {
		resultKind = p2pstreamv1.AgentUpdateRootActionResultKind_AGENT_UPDATE_ROOT_ACTION_RESULT_KIND_BOOTSTRAP
	}
	return &p2pstreamv1.AgentUpdateRootActionReceipt{
		AgentPublicId: receipt.AgentPublicID, AssignmentId: receipt.AssignmentID, CampaignId: receipt.CampaignID,
		Generation: receipt.Generation, Action: desiredActionAuthProto(receipt.Action), CommandSequence: receipt.CommandSequence,
		AuthorizationSha256: receipt.AuthorizationSHA256, AuthorizationNonce: receipt.AuthorizationNonce,
		AuthorityKeyId: receipt.AuthorityKeyID, AuthorityEpoch: receipt.AuthorityEpoch, ActivatorKeyId: receipt.ActivatorKeyID,
		RootActionCounter: receipt.RootActionCounter, CompletedAtUnixMillis: receipt.CompletedAtUnixMillis,
		ResultKind:        resultKind,
		ResultRootVersion: receipt.ResultRootVersion, ResultManifestSha256: receipt.ResultManifestSHA256,
		ResultVersion: receipt.ResultVersion, ResultCommit: receipt.ResultCommit,
		ResultReleaseSequence: receipt.ResultReleaseSequence, ResultSecurityEpoch: receipt.ResultSecurityEpoch,
		ResultOs: receipt.ResultOS, ResultArch: receipt.ResultArch, ResultArtifactName: receipt.ResultArtifactName,
		ResultArtifactSize: receipt.ResultArtifactSize, ResultArtifactSha256: receipt.ResultArtifactSHA256,
		CanonicalPayload: payload, Signature: signature,
	}
}

func signAgentUpdateTestReport(report *p2pstreamv1.ReportAgentUpdateRequest, updaterPrivate ed25519.PrivateKey) {
	report.Signature = ed25519.Sign(updaterPrivate, agentUpdaterReportSigningPayload(report))
}

func newAgentUpdateTestApp(tb testing.TB, database *db.DB) *App {
	tb.Helper()
	previousVersion := buildinfo.Version
	buildinfo.Version = "v0.0.0-test"
	tb.Cleanup(func() { buildinfo.Version = previousVersion })
	directory := tb.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		tb.Fatal(err)
	}
	authority, err := agentupdateauthority.Generate(filepath.Join(directory, "management-authority.json"), 1)
	if err != nil {
		tb.Fatal(err)
	}
	app := NewApp(nil, database)
	app.SetAgentUpdateManagementAuthority(authority, nil)
	app.agentUpdateIdentityRate = newAgentUpdateRateLimiter(100_000, time.Minute, 100_000, agentUpdateMaxAgents)
	// Most authorization tests isolate signing/state transitions. Drain-specific
	// tests override this hook to exercise active-request behavior explicitly.
	app.agentUpdateDrainReady = func(int64) bool { return true }
	return app
}

type agentUpdateTestBootstrapProvider struct{ rootBase64, repository string }

func (p agentUpdateTestBootstrapProvider) AgentUpdateBootstrapConfig(context.Context) (string, string, error) {
	return p.rootBase64, p.repository, nil
}

type agentUpdateFailingBootstrapProvider struct{}

func (agentUpdateFailingBootstrapProvider) AgentUpdateBootstrapConfig(context.Context) (string, string, error) {
	return "", "", errors.New("injected catalog failure")
}

func newAgentUpdateTestBootstrap(t *testing.T) (AgentUpdateBootstrapProvider, agentUpdateBootstrap) {
	t.Helper()
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	root, err := agentupdate.NewRootMetadata(1, time.Now().UTC().Add(48*time.Hour).Format(time.RFC3339), 1, []ed25519.PublicKey{public})
	if err != nil {
		t.Fatal(err)
	}
	data, err := agentupdate.CanonicalRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	provider := agentUpdateTestBootstrapProvider{rootBase64: base64.StdEncoding.EncodeToString(data), repository: "owner/repo"}
	app := &App{AgentUpdateBootstrap: provider}
	bootstrap, err := app.loadAgentUpdateBootstrap(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return provider, bootstrap
}

func insertAgentUpdateTestCampaign(t *testing.T, database *db.DB, agentID int64, state, action string, cordoned bool) (int64, int64) {
	t.Helper()
	artifactJSON, _ := json.Marshal([]*p2pstreamv1.AgentUpdateArtifact{{Os: "linux", Arch: "amd64", Name: "p2pstream_v1.2.3_linux_amd64", SizeBytes: 1234, Sha256: strings.Repeat("b", 64)}})
	now := time.Now().UTC()
	result, err := database.Exec(`INSERT INTO agent_update_campaigns (name,state,generation,target_version,target_commit,manifest_sha256,release_sequence,root_version,security_epoch,minimum_updater_version,minimum_tunnel_protocol,maximum_tunnel_protocol,artifacts_json,max_unavailable,minimum_eligible_agents_per_route,canary_count,wave_size,healthy_dwell_millis,created_at,updated_at) VALUES ('test','running',1,'v1.2.3',?,?,12,3,2,'v1.0.0',1,1,?,1,1,1,1,10000,?,?)`, strings.Repeat("c", 40), strings.Repeat("a", 64), string(artifactJSON), now, now)
	if err != nil {
		t.Fatal(err)
	}
	campaignID, _ := result.LastInsertId()
	result, err = database.Exec(`INSERT INTO agent_update_assignments (campaign_id,agent_id,state,desired_action,generation,cordoned,created_at,updated_at) VALUES (?,?,?,?,1,?,?,?)`, campaignID, agentID, state, action, boolInt(cordoned), now, now)
	if err != nil {
		t.Fatal(err)
	}
	assignmentID, _ := result.LastInsertId()
	return campaignID, assignmentID
}

func assertAgentUpdateAssignmentState(t *testing.T, database *db.DB, assignmentID int64, want string) {
	t.Helper()
	var got string
	if err := database.QueryRow(`SELECT state FROM agent_update_assignments WHERE id=?`, assignmentID).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("assignment state = %q, want %q", got, want)
	}
}
