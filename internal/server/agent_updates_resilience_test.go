package server

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/internal/agentupdateauth"
)

func TestAgentUpdateMaintenanceRetriesTransactionalCohortRelease(t *testing.T) {
	ctx := context.Background()
	database := newServerTestDB(t)
	app := newAgentUpdateTestApp(t, database)
	now := time.Now().UTC()
	artifacts, _ := json.Marshal([]*p2pstreamv1.AgentUpdateArtifact{{Os: "linux", Arch: "amd64", Name: "p2pstream_v1.2.3_linux_amd64", SizeBytes: 1, Sha256: strings.Repeat("b", 64)}})
	result, err := database.ExecContext(ctx, `INSERT INTO agent_update_campaigns (name,state,generation,target_version,target_commit,manifest_sha256,release_sequence,security_epoch,minimum_updater_version,minimum_tunnel_protocol,maximum_tunnel_protocol,artifacts_json,max_unavailable,minimum_eligible_agents_per_route,canary_count,wave_size,healthy_dwell_millis,created_at,updated_at) VALUES ('release-retry','running',1,'v1.2.3',?,?,1,1,'v1.0.0',1,1,?,1,1,1,1,10000,?,?)`, strings.Repeat("c", 40), strings.Repeat("a", 64), string(artifacts), now, now)
	if err != nil {
		t.Fatal(err)
	}
	campaignID, _ := result.LastInsertId()
	first := createAgentUpdateTestAgent(t, database, "cohort-release-first")
	second := createAgentUpdateTestAgent(t, database, "cohort-release-second")
	if _, err := database.ExecContext(ctx, `INSERT INTO agent_update_assignments (campaign_id,agent_id,state,desired_action,generation,created_at,updated_at) VALUES (?,?,'succeeded','none',1,?,?),(?,?,'pending','none',1,?,?)`, campaignID, first.ID, now, now, campaignID, second.ID, now, now); err != nil {
		t.Fatal(err)
	}
	campaign, err := aCampaignByID(ctx, app, campaignID)
	if err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := app.releaseNextAgentUpdateStageCohortLocked(cancelled, campaignID, campaign); err == nil {
		t.Fatal("injected cohort release failure unexpectedly succeeded")
	}
	var action string
	if err := database.QueryRowContext(ctx, `SELECT desired_action FROM agent_update_assignments WHERE agent_id=?`, second.ID).Scan(&action); err != nil || action != "none" {
		t.Fatalf("failed release partially changed cohort: action=%q err=%v", action, err)
	}
	app.reconcileAgentUpdateMaintenance(ctx, time.Now().UTC())
	if err := database.QueryRowContext(ctx, `SELECT desired_action FROM agent_update_assignments WHERE agent_id=?`, second.ID).Scan(&action); err != nil || action != "stage" {
		t.Fatalf("maintenance did not repair missed cohort release: action=%q err=%v", action, err)
	}
}

func TestAgentUpdateMaintenanceFinalizesCampaignAfterLostSuccessEdge(t *testing.T) {
	ctx := context.Background()
	database := newServerTestDB(t)
	app := newAgentUpdateTestApp(t, database)
	agent := createAgentUpdateTestAgent(t, database, "completion-repair")
	campaignID, _ := insertAgentUpdateTestCampaign(t, database, agent.ID, "succeeded", "none", false)

	// This is the durable state left by a crash or transient SQLite failure
	// after the final assignment CAS but before campaign completion.
	app.reconcileAgentUpdateMaintenance(ctx, time.Now().UTC())
	var state string
	var completedAt sql.NullTime
	if err := database.QueryRowContext(ctx, `SELECT state,completed_at FROM agent_update_campaigns WHERE id=?`, campaignID).Scan(&state, &completedAt); err != nil {
		t.Fatal(err)
	}
	if state != "completed" || !completedAt.Valid {
		t.Fatalf("maintenance did not converge terminal campaign: state=%s completed_at=%v", state, completedAt)
	}
}

func TestAgentUpdateStageReleaseOnlyPublishesCurrentCohort(t *testing.T) {
	ctx := context.Background()
	database := newServerTestDB(t)
	app := newAgentUpdateTestApp(t, database)
	now := time.Now().UTC()
	result, err := database.ExecContext(ctx, `INSERT INTO agent_update_campaigns (name,state,generation,target_version,target_commit,manifest_sha256,release_sequence,security_epoch,minimum_updater_version,minimum_tunnel_protocol,maximum_tunnel_protocol,artifacts_json,max_unavailable,minimum_eligible_agents_per_route,canary_count,wave_size,healthy_dwell_millis,created_at,updated_at) VALUES ('stage-window','running',1,'v1.2.3',?,?,1,1,'v1.0.0',1,1,'[]',2,1,2,2,10000,?,?)`, strings.Repeat("c", 40), strings.Repeat("a", 64), now, now)
	if err != nil {
		t.Fatal(err)
	}
	campaignID, _ := result.LastInsertId()
	for index := 0; index < 6; index++ {
		agent := createAgentUpdateTestAgent(t, database, fmt.Sprintf("stage-window-%d", index))
		if _, err := database.ExecContext(ctx, `INSERT INTO agent_update_assignments (campaign_id,agent_id,state,desired_action,generation,created_at,updated_at) VALUES (?,?,'pending','none',1,?,?)`, campaignID, agent.ID, now, now); err != nil {
			t.Fatal(err)
		}
	}
	campaign, err := aCampaignByID(ctx, app, campaignID)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.releaseNextAgentUpdateStageCohortLocked(ctx, campaignID, campaign); err != nil {
		t.Fatal(err)
	}
	rows, err := database.QueryContext(ctx, `SELECT desired_action FROM agent_update_assignments WHERE campaign_id=? ORDER BY id`, campaignID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var actions []string
	for rows.Next() {
		var action string
		if err := rows.Scan(&action); err != nil {
			t.Fatal(err)
		}
		actions = append(actions, action)
	}
	want := []string{"stage", "stage", "none", "none", "none", "none"}
	if !slices.Equal(actions, want) {
		t.Fatalf("released stage window = %v, want %v", actions, want)
	}
}

func TestAgentUpdateResumeRejectsUnremediatedBlockingAssignments(t *testing.T) {
	ctx := context.Background()
	database := newServerTestDB(t)
	agent := createAgentUpdateTestAgent(t, database, "resume-blocked")
	app := newAgentUpdateTestApp(t, database)
	campaignID, assignmentID := insertAgentUpdateTestCampaign(t, database, agent.ID, "failed", "none", false)
	if _, err := database.ExecContext(ctx, `UPDATE agent_update_campaigns SET state='paused' WHERE id=?`, campaignID); err != nil {
		t.Fatal(err)
	}
	header := createTestAdminSession(t, app)
	request := connect.NewRequest(&p2pstreamv1.ChangeAgentUpdateCampaignStateRequest{CampaignId: campaignID, ExpectedGeneration: 1})
	request.Header().Set("Cookie", header.Get("Cookie"))
	if _, err := app.ResumeAgentUpdateCampaign(ctx, request); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("resume with failed assignment error = %v", err)
	}
	var campaignState, assignmentState string
	if err := database.QueryRowContext(ctx, `SELECT c.state,x.state FROM agent_update_campaigns c JOIN agent_update_assignments x ON x.campaign_id=c.id WHERE c.id=? AND x.id=?`, campaignID, assignmentID).Scan(&campaignState, &assignmentState); err != nil {
		t.Fatal(err)
	}
	if campaignState != "paused" || assignmentState != "failed" {
		t.Fatalf("rejected resume mutated state: campaign=%s assignment=%s", campaignState, assignmentState)
	}
}

func TestAgentUpdateMaintenanceBlocksStalledStageWithoutWorkerCallback(t *testing.T) {
	ctx := context.Background()
	database := newServerTestDB(t)
	agent := createAgentUpdateTestAgent(t, database, "stage-watchdog")
	app := newAgentUpdateTestApp(t, database)
	campaignID, assignmentID := insertAgentUpdateTestCampaign(t, database, agent.ID, "staging", "stage", false)
	old := time.Now().UTC().Add(-agentUpdateStageTimeout - time.Second)
	if _, err := database.ExecContext(ctx, `UPDATE agent_update_assignments SET updated_at=? WHERE id=?`, old, assignmentID); err != nil {
		t.Fatal(err)
	}
	app.reconcileAgentUpdateMaintenance(ctx, time.Now().UTC())
	assertAgentUpdateAssignmentState(t, database, assignmentID, "blocked")
	var state string
	if err := database.QueryRowContext(ctx, `SELECT state FROM agent_update_campaigns WHERE id=?`, campaignID).Scan(&state); err != nil || state != "paused" {
		t.Fatalf("campaign watchdog state=%q err=%v", state, err)
	}
	if app.isAgentUpdateCordoned(agent.ID) {
		t.Fatal("staging timeout cordoned live traffic")
	}
}

func TestAgentUpdateDownloadingReportsCannotExtendStageDeadline(t *testing.T) {
	ctx := context.Background()
	database := newServerTestDB(t)
	agent := createAgentUpdateTestAgent(t, database, "stage-deadline-heartbeat")
	app := newAgentUpdateTestApp(t, database)
	updaterPublic, updaterPrivate, _ := ed25519.GenerateKey(rand.Reader)
	activatorPublic, _, _ := ed25519.GenerateKey(rand.Reader)
	insertAgentUpdateIdentity(t, app, database, agent.ID, updaterPublic, activatorPublic)
	campaignID, assignmentID := insertAgentUpdateTestCampaign(t, database, agent.ID, "staging", "stage", false)
	stageStartedAt := time.Now().UTC().Add(-agentUpdateStageTimeout - time.Second).Truncate(time.Millisecond)
	if _, err := database.ExecContext(ctx, `UPDATE agent_update_assignments SET updated_at=? WHERE id=?`, stageStartedAt, assignmentID); err != nil {
		t.Fatal(err)
	}
	report := &p2pstreamv1.ReportAgentUpdateRequest{
		AgentPublicId: agent.PublicID,
		Counter:       1,
		AssignmentId:  assignmentID,
		Generation:    1,
		State:         p2pstreamv1.AgentUpdaterReportState_AGENT_UPDATER_REPORT_STATE_DOWNLOADING,
	}
	signAgentUpdateTestReport(report, updaterPrivate)
	if _, err := app.ReportAgentUpdate(ctx, connect.NewRequest(report)); err != nil {
		t.Fatal(err)
	}
	var persistedStart, lastReport time.Time
	if err := database.QueryRowContext(ctx, `SELECT updated_at,last_report_at FROM agent_update_assignments WHERE id=?`, assignmentID).Scan(&persistedStart, &lastReport); err != nil {
		t.Fatal(err)
	}
	if !persistedStart.Equal(stageStartedAt) || !lastReport.After(stageStartedAt) {
		t.Fatalf("DOWNLOADING changed bounded stage window: start=%s want=%s last_report=%s", persistedStart, stageStartedAt, lastReport)
	}
	app.reconcileAgentUpdateMaintenance(ctx, time.Now().UTC())
	assertAgentUpdateAssignmentState(t, database, assignmentID, "blocked")
	var campaignState string
	if err := database.QueryRowContext(ctx, `SELECT state FROM agent_update_campaigns WHERE id=?`, campaignID).Scan(&campaignState); err != nil || campaignState != "paused" {
		t.Fatalf("campaign state=%q err=%v", campaignState, err)
	}
}

func TestAgentUpdateMaintenanceDoesNotExpireStagedArtifactWaitingForCohort(t *testing.T) {
	ctx := context.Background()
	database := newServerTestDB(t)
	first := createAgentUpdateTestAgent(t, database, "staged-wait-first")
	second := createAgentUpdateTestAgent(t, database, "staged-wait-second")
	app := newAgentUpdateTestApp(t, database)
	campaignID, firstAssignmentID := insertAgentUpdateTestCampaign(t, database, first.ID, "healthy_dwell", "none", true)
	now := time.Now().UTC()
	result, err := database.ExecContext(ctx, `INSERT INTO agent_update_assignments (campaign_id,agent_id,state,desired_action,generation,created_at,updated_at) VALUES (?,?,'staged','none',1,?,?)`, campaignID, second.ID, now, now.Add(-agentUpdateStageTimeout-time.Second))
	if err != nil {
		t.Fatal(err)
	}
	secondAssignmentID, _ := result.LastInsertId()
	// The first cohort member still occupies max_unavailable, so the second
	// staged artifact is valid queued work even after the download timeout.
	if _, err := database.ExecContext(ctx, `UPDATE agent_update_campaigns SET healthy_dwell_millis=? WHERE id=?`, (agentUpdateStageTimeout + time.Minute).Milliseconds(), campaignID); err != nil {
		t.Fatal(err)
	}
	app.setAgentUpdateCordon(first.ID)
	app.reconcileAgentUpdateMaintenance(ctx, now)
	assertAgentUpdateAssignmentState(t, database, firstAssignmentID, "healthy_dwell")
	assertAgentUpdateAssignmentState(t, database, secondAssignmentID, "staged")
	var campaignState string
	if err := database.QueryRowContext(ctx, `SELECT state FROM agent_update_campaigns WHERE id=?`, campaignID).Scan(&campaignState); err != nil || campaignState != "running" {
		t.Fatalf("queued staged artifact paused campaign: state=%q err=%v", campaignState, err)
	}
}

func TestPausingCampaignUnwindsPreAuthorizationDrainCordon(t *testing.T) {
	ctx := context.Background()
	database := newServerTestDB(t)
	agent := createAgentUpdateTestAgent(t, database, "pause-drain")
	app := newAgentUpdateTestApp(t, database)
	campaignID, assignmentID := insertAgentUpdateTestCampaign(t, database, agent.ID, "cordoned", "none", true)
	app.setAgentUpdateCordon(agent.ID)
	header := createTestAdminSession(t, app)
	request := connect.NewRequest(&p2pstreamv1.ChangeAgentUpdateCampaignStateRequest{CampaignId: campaignID, ExpectedGeneration: 1})
	request.Header().Set("Cookie", header.Get("Cookie"))
	if _, err := app.PauseAgentUpdateCampaign(ctx, request); err != nil {
		t.Fatal(err)
	}
	var state, action string
	var cordoned int64
	if err := database.QueryRowContext(ctx, `SELECT state,desired_action,cordoned FROM agent_update_assignments WHERE id=?`, assignmentID).Scan(&state, &action, &cordoned); err != nil {
		t.Fatal(err)
	}
	if state != "staged" || action != "none" || cordoned != 0 || app.isAgentUpdateCordoned(agent.ID) {
		t.Fatalf("paused pre-authorization drain remained fenced: state=%s action=%s cordoned=%d live_fence=%t", state, action, cordoned, app.isAgentUpdateCordoned(agent.ID))
	}
}

func TestPausedCampaignRejectsStalePrePauseAdvanceSnapshot(t *testing.T) {
	ctx := context.Background()
	database := newServerTestDB(t)
	agent := createAgentUpdateTestAgent(t, database, "pause-stale-advance")
	app := newAgentUpdateTestApp(t, database)
	campaignID, assignmentID := insertAgentUpdateTestCampaign(t, database, agent.ID, "staged", "none", false)
	staleAssignment, staleCampaign, err := app.activeAgentUpdateAssignment(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	header := createTestAdminSession(t, app)
	request := connect.NewRequest(&p2pstreamv1.ChangeAgentUpdateCampaignStateRequest{CampaignId: campaignID, ExpectedGeneration: 1})
	request.Header().Set("Cookie", header.Get("Cookie"))
	if _, err := app.PauseAgentUpdateCampaign(ctx, request); err != nil {
		t.Fatal(err)
	}
	// This is the exact Check interleaving: it loaded RUNNING/STAGED before the
	// pause committed, then entered the advance path afterward.
	app.tryAdvanceAgentUpdateAssignment(ctx, staleAssignment, staleCampaign)
	var state, action string
	var cordoned int64
	if err := database.QueryRowContext(ctx, `SELECT state,desired_action,cordoned FROM agent_update_assignments WHERE id=?`, assignmentID).Scan(&state, &action, &cordoned); err != nil {
		t.Fatal(err)
	}
	if state != "staged" || action != "none" || cordoned != 0 || app.isAgentUpdateCordoned(agent.ID) {
		t.Fatalf("stale pre-pause snapshot published a cordon: state=%s action=%s cordoned=%d", state, action, cordoned)
	}
}

func TestResumingPausedCampaignRestartsBoundedStageWindow(t *testing.T) {
	ctx := context.Background()
	database := newServerTestDB(t)
	agent := createAgentUpdateTestAgent(t, database, "resume-stage-window")
	app := newAgentUpdateTestApp(t, database)
	campaignID, assignmentID := insertAgentUpdateTestCampaign(t, database, agent.ID, "staging", "stage", false)
	old := time.Now().UTC().Add(-2 * agentUpdateStageTimeout)
	if _, err := database.ExecContext(ctx, `UPDATE agent_update_campaigns SET state='paused' WHERE id=?`, campaignID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `UPDATE agent_update_assignments SET updated_at=? WHERE id=?`, old, assignmentID); err != nil {
		t.Fatal(err)
	}
	header := createTestAdminSession(t, app)
	request := connect.NewRequest(&p2pstreamv1.ChangeAgentUpdateCampaignStateRequest{CampaignId: campaignID, ExpectedGeneration: 1})
	request.Header().Set("Cookie", header.Get("Cookie"))
	if _, err := app.ResumeAgentUpdateCampaign(ctx, request); err != nil {
		t.Fatal(err)
	}
	app.reconcileAgentUpdateMaintenance(ctx, time.Now().UTC())
	var assignmentState, campaignState string
	var updatedAt time.Time
	if err := database.QueryRowContext(ctx, `SELECT x.state,c.state,x.updated_at FROM agent_update_assignments x JOIN agent_update_campaigns c ON c.id=x.campaign_id WHERE x.id=?`, assignmentID).Scan(&assignmentState, &campaignState, &updatedAt); err != nil {
		t.Fatal(err)
	}
	if assignmentState != "staging" || campaignState != "running" || !updatedAt.After(old) {
		t.Fatalf("resume did not restart bounded stage window: assignment=%s campaign=%s updated=%s old=%s", assignmentState, campaignState, updatedAt, old)
	}
}

func TestAgentUpdateMaintenanceRequiresWorkerHealthyEvidenceAfterActivation(t *testing.T) {
	ctx := context.Background()
	database := newServerTestDB(t)
	agent := createAgentUpdateTestAgent(t, database, "activation-health-watchdog")
	app := newAgentUpdateTestApp(t, database)
	campaignID, assignmentID := insertAgentUpdateTestCampaign(t, database, agent.ID, "awaiting_tunnel", "none", true)
	completed := time.Now().UTC().Add(-agentUpdatePostActionTimeout - time.Second)
	if _, err := database.ExecContext(ctx, `UPDATE agent_update_assignments SET authorization_action='activate',root_action_completed_at=?,root_result_kind='release',root_result_manifest_sha256=?,root_result_version='v1.2.3',root_result_commit=?,root_result_artifact_sha256=?,fresh_tunnel_at=?,observed_version='v1.2.3',observed_commit=?,healthy_at=NULL WHERE id=?`, completed, strings.Repeat("a", 64), strings.Repeat("c", 40), strings.Repeat("b", 64), completed.Add(time.Second), strings.Repeat("c", 40), assignmentID); err != nil {
		t.Fatal(err)
	}
	app.setAgentUpdateCordon(agent.ID)
	app.reconcileAgentUpdateMaintenance(ctx, time.Now().UTC())
	assertAgentUpdateAssignmentState(t, database, assignmentID, "blocked")
	if !app.isAgentUpdateCordoned(agent.ID) {
		t.Fatal("post-activation evidence timeout failed open")
	}
	var campaignState, failureCode string
	if err := database.QueryRowContext(ctx, `SELECT c.state,x.failure_code FROM agent_update_campaigns c JOIN agent_update_assignments x ON x.campaign_id=c.id WHERE c.id=? AND x.id=?`, campaignID, assignmentID).Scan(&campaignState, &failureCode); err != nil {
		t.Fatal(err)
	}
	if campaignState != "paused" || failureCode != "activation_evidence_timeout" {
		t.Fatalf("timeout state=%s failure=%s", campaignState, failureCode)
	}
}

func TestAgentUpdateMaintenanceExpiresDrainWithoutWorkerCallback(t *testing.T) {
	ctx := context.Background()
	database := newServerTestDB(t)
	agent := createAgentUpdateTestAgent(t, database, "drain-watchdog")
	app := newAgentUpdateTestApp(t, database)
	campaignID, assignmentID := insertAgentUpdateTestCampaign(t, database, agent.ID, "cordoned", "none", true)
	old := time.Now().UTC().Add(-agentUpdateDrainTimeout - time.Second)
	if _, err := database.ExecContext(ctx, `UPDATE agent_update_assignments SET updated_at=? WHERE id=?`, old, assignmentID); err != nil {
		t.Fatal(err)
	}
	app.setAgentUpdateCordon(agent.ID)
	app.agentUpdateDrainReady = func(int64) bool { return false }
	app.reconcileAgentUpdateMaintenance(ctx, time.Now().UTC())
	assertAgentUpdateAssignmentState(t, database, assignmentID, "blocked")
	if app.isAgentUpdateCordoned(agent.ID) {
		t.Fatal("drain watchdog left the old healthy tunnel cordoned")
	}
	var campaignState, failureCode string
	if err := database.QueryRowContext(ctx, `SELECT c.state,x.failure_code FROM agent_update_campaigns c JOIN agent_update_assignments x ON x.campaign_id=c.id WHERE c.id=? AND x.id=?`, campaignID, assignmentID).Scan(&campaignState, &failureCode); err != nil {
		t.Fatal(err)
	}
	if campaignState != "paused" || failureCode != "drain_timeout" {
		t.Fatalf("drain watchdog state=%s failure=%s", campaignState, failureCode)
	}
}

func TestAgentUpdateEnteringDrainUsesFreshDeadlineAfterLongCohortWait(t *testing.T) {
	ctx := context.Background()
	database := newServerTestDB(t)
	agent := createAgentUpdateTestAgent(t, database, "aged-staged-drain")
	app := newAgentUpdateTestApp(t, database)
	updaterPublic, _, _ := ed25519.GenerateKey(rand.Reader)
	activatorPublic, _, _ := ed25519.GenerateKey(rand.Reader)
	insertAgentUpdateIdentity(t, app, database, agent.ID, updaterPublic, activatorPublic)
	_, assignmentID := insertAgentUpdateTestCampaign(t, database, agent.ID, "staged", "none", false)
	old := time.Now().UTC().Add(-agentUpdateDrainTimeout - time.Hour)
	if _, err := database.ExecContext(ctx, `UPDATE agent_update_assignments SET updated_at=? WHERE id=?`, old, assignmentID); err != nil {
		t.Fatal(err)
	}
	assignment, campaign, err := app.activeAgentUpdateAssignment(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	app.tryAdvanceAgentUpdateAssignment(ctx, assignment, campaign)
	var state, action, failureCode string
	var updatedAt time.Time
	if err := database.QueryRowContext(ctx, `SELECT state,desired_action,failure_code,updated_at FROM agent_update_assignments WHERE id=?`, assignmentID).Scan(&state, &action, &failureCode, &updatedAt); err != nil {
		t.Fatal(err)
	}
	if state != "cordoned" || action != "activate" || failureCode != "" || !updatedAt.After(old.Add(time.Hour)) {
		t.Fatalf("new drain inherited stale cohort timestamp: state=%s action=%s failure=%s updated=%s old=%s", state, action, failureCode, updatedAt, old)
	}
}

func TestAgentUpdateMaintenanceExpiresDrainedCordonWhenAuthorityUnavailable(t *testing.T) {
	ctx := context.Background()
	database := newServerTestDB(t)
	agent := createAgentUpdateTestAgent(t, database, "drained-authority-watchdog")
	app := newAgentUpdateTestApp(t, database)
	campaignID, assignmentID := insertAgentUpdateTestCampaign(t, database, agent.ID, "cordoned", "none", true)
	old := time.Now().UTC().Add(-agentUpdateDrainTimeout - time.Second)
	if _, err := database.ExecContext(ctx, `UPDATE agent_update_assignments SET updated_at=? WHERE id=?`, old, assignmentID); err != nil {
		t.Fatal(err)
	}
	app.setAgentUpdateCordon(agent.ID)
	app.agentUpdateDrainReady = func(int64) bool { return true }
	app.SetAgentUpdateManagementAuthority(nil, fmt.Errorf("injected unavailable authority"))
	app.reconcileAgentUpdateMaintenance(ctx, time.Now().UTC())
	assertAgentUpdateAssignmentState(t, database, assignmentID, "blocked")
	if app.isAgentUpdateCordoned(agent.ID) {
		t.Fatal("expired drained pre-authorization phase remained cordoned")
	}
	var campaignState, failureCode string
	if err := database.QueryRowContext(ctx, `SELECT c.state,x.failure_code FROM agent_update_campaigns c JOIN agent_update_assignments x ON x.campaign_id=c.id WHERE c.id=? AND x.id=?`, campaignID, assignmentID).Scan(&campaignState, &failureCode); err != nil {
		t.Fatal(err)
	}
	if campaignState != "paused" || failureCode != "drain_timeout" {
		t.Fatalf("drained authority timeout state=%s failure=%s", campaignState, failureCode)
	}
}

func TestAgentUpdateDrainRechecksRouteQuorumBeforeAuthorization(t *testing.T) {
	ctx := context.Background()
	database := newServerTestDB(t)
	agent := createAgentUpdateTestAgent(t, database, "drain-route-quorum")
	app := newAgentUpdateTestApp(t, database)
	_, assignmentID := insertAgentUpdateTestCampaign(t, database, agent.ID, "cordoned", "none", true)
	app.setAgentUpdateCordon(agent.ID)
	app.agentUpdateDrainReady = func(int64) bool { return true }
	app.agentUpdateRouteBlockersHook = func(id, minimum int64) []string {
		if id != agent.ID || minimum != 1 {
			t.Fatalf("route quorum recheck args = agent:%d minimum:%d", id, minimum)
		}
		return []string{"route_target_7_requires_1_other_eligible_agents"}
	}
	app.reconcileAgentUpdateMaintenance(ctx, time.Now().UTC())
	var state, action, campaignState, failureCode string
	var cordoned int64
	if err := database.QueryRowContext(ctx, `SELECT x.state,x.desired_action,x.cordoned,x.failure_code,c.state FROM agent_update_assignments x JOIN agent_update_campaigns c ON c.id=x.campaign_id WHERE x.id=?`, assignmentID).Scan(&state, &action, &cordoned, &failureCode, &campaignState); err != nil {
		t.Fatal(err)
	}
	if state != "staged" || action != "none" || cordoned != 0 || failureCode != "route_quorum_lost_during_drain" || campaignState != "paused" || app.isAgentUpdateCordoned(agent.ID) {
		t.Fatalf("route quorum loss did not safely unwind drain: state=%s action=%s cordoned=%d failure=%s campaign=%s", state, action, cordoned, failureCode, campaignState)
	}
	var authorizationAction string
	if err := database.QueryRowContext(ctx, `SELECT authorization_action FROM agent_update_assignments WHERE id=?`, assignmentID).Scan(&authorizationAction); err != nil || authorizationAction != "" {
		t.Fatalf("route quorum loss escaped an authorization: action=%q err=%v", authorizationAction, err)
	}
}

func TestAgentUpdateMaintenanceRollbackEvidenceTimeoutStaysCordoned(t *testing.T) {
	ctx := context.Background()
	database := newServerTestDB(t)
	agent := createAgentUpdateTestAgent(t, database, "rollback-watchdog")
	app := newAgentUpdateTestApp(t, database)
	campaignID, assignmentID := insertAgentUpdateTestCampaign(t, database, agent.ID, "awaiting_tunnel", "none", true)
	completed := time.Now().UTC().Add(-agentUpdatePostActionTimeout - time.Second)
	if _, err := database.ExecContext(ctx, `UPDATE agent_update_assignments SET authorization_action='rollback',root_action_completed_at=?,root_result_kind='release',root_result_manifest_sha256=?,root_result_version='v1.0.0',root_result_commit=?,root_result_artifact_sha256=? WHERE id=?`, completed, strings.Repeat("a", 64), strings.Repeat("d", 40), strings.Repeat("e", 64), assignmentID); err != nil {
		t.Fatal(err)
	}
	app.setAgentUpdateCordon(agent.ID)
	app.reconcileAgentUpdateMaintenance(ctx, time.Now().UTC())
	assertAgentUpdateAssignmentState(t, database, assignmentID, "blocked")
	if !app.isAgentUpdateCordoned(agent.ID) {
		t.Fatal("rollback evidence timeout failed open")
	}
	var campaignState, failureCode string
	if err := database.QueryRowContext(ctx, `SELECT c.state,x.failure_code FROM agent_update_campaigns c JOIN agent_update_assignments x ON x.campaign_id=c.id WHERE c.id=? AND x.id=?`, campaignID, assignmentID).Scan(&campaignState, &failureCode); err != nil {
		t.Fatal(err)
	}
	if campaignState != "paused" || failureCode != "rollback_evidence_timeout" {
		t.Fatalf("rollback timeout state=%s failure=%s", campaignState, failureCode)
	}
}

func TestAgentUpdateMaintenanceBlocksUncompletedRootAction(t *testing.T) {
	ctx := context.Background()
	database := newServerTestDB(t)
	agent := createAgentUpdateTestAgent(t, database, "root-action-watchdog")
	app := newAgentUpdateTestApp(t, database)
	campaignID, assignmentID := insertAgentUpdateTestCampaign(t, database, agent.ID, "cordoned", "activate", true)
	old := time.Now().UTC().Add(-agentUpdatePostActionTimeout - time.Second)
	if _, err := database.ExecContext(ctx, `UPDATE agent_update_assignments SET authorization_action='activate',updated_at=? WHERE id=?`, old, assignmentID); err != nil {
		t.Fatal(err)
	}
	app.setAgentUpdateCordon(agent.ID)
	app.reconcileAgentUpdateMaintenance(ctx, time.Now().UTC())
	assertAgentUpdateAssignmentState(t, database, assignmentID, "blocked")
	if !app.isAgentUpdateCordoned(agent.ID) {
		t.Fatal("uncompleted root action timeout failed open")
	}
	var campaignState, action, failureCode string
	if err := database.QueryRowContext(ctx, `SELECT c.state,x.desired_action,x.failure_code FROM agent_update_campaigns c JOIN agent_update_assignments x ON x.campaign_id=c.id WHERE c.id=? AND x.id=?`, campaignID, assignmentID).Scan(&campaignState, &action, &failureCode); err != nil {
		t.Fatal(err)
	}
	if campaignState != "paused" || action != "none" || failureCode != "root_action_timeout" {
		t.Fatalf("root action watchdog state=%s action=%s failure=%s", campaignState, action, failureCode)
	}
}

func TestPausedCampaignSuppressesOutstandingStageAction(t *testing.T) {
	ctx := context.Background()
	database := newServerTestDB(t)
	agent := createAgentUpdateTestAgent(t, database, "paused-stage")
	app := newAgentUpdateTestApp(t, database)
	updaterPublic, updaterPrivate, _ := ed25519.GenerateKey(rand.Reader)
	activatorPublic, _, _ := ed25519.GenerateKey(rand.Reader)
	insertAgentUpdateIdentity(t, app, database, agent.ID, updaterPublic, activatorPublic)
	campaignID, _ := insertAgentUpdateTestCampaign(t, database, agent.ID, "pending", "stage", false)
	if _, err := database.ExecContext(ctx, `UPDATE agent_update_campaigns SET state='paused' WHERE id=?`, campaignID); err != nil {
		t.Fatal(err)
	}
	request := &p2pstreamv1.CheckAgentUpdateRequest{AgentPublicId: agent.PublicID, Counter: 1}
	request.Signature = ed25519.Sign(updaterPrivate, agentupdateauth.CheckPayload(request.AgentPublicId, request.Counter))
	response, err := app.CheckAgentUpdate(ctx, connect.NewRequest(request))
	if err != nil {
		t.Fatal(err)
	}
	if response.Msg.DesiredAction != p2pstreamv1.AgentUpdateDesiredAction_AGENT_UPDATE_DESIRED_ACTION_NONE {
		t.Fatalf("paused campaign leaked stage action: %+v", response.Msg)
	}
}

func TestPausedCampaignSuppressesAndDoesNotRenewActivationAuthorization(t *testing.T) {
	ctx := context.Background()
	database := newServerTestDB(t)
	agent := createAgentUpdateTestAgent(t, database, "paused-activation")
	app := newAgentUpdateTestApp(t, database)
	updaterPublic, updaterPrivate, _ := ed25519.GenerateKey(rand.Reader)
	activatorPublic, _, _ := ed25519.GenerateKey(rand.Reader)
	insertAgentUpdateIdentity(t, app, database, agent.ID, updaterPublic, activatorPublic)
	campaignID, _ := insertAgentUpdateTestCampaign(t, database, agent.ID, "staged", "none", false)
	authorizeAgentUpdateTestActivation(t, app, agent.ID)

	var originalSequence int64
	if err := database.QueryRowContext(ctx, `SELECT last_command_sequence FROM agent_updater_identities WHERE agent_id=?`, agent.ID).Scan(&originalSequence); err != nil {
		t.Fatal(err)
	}
	header := createTestAdminSession(t, app)
	pause := connect.NewRequest(&p2pstreamv1.ChangeAgentUpdateCampaignStateRequest{CampaignId: campaignID, ExpectedGeneration: 1})
	pause.Header().Set("Cookie", header.Get("Cookie"))
	if _, err := app.PauseAgentUpdateCampaign(ctx, pause); err != nil {
		t.Fatal(err)
	}

	check := func(counter uint64) *p2pstreamv1.CheckAgentUpdateResponse {
		t.Helper()
		request := &p2pstreamv1.CheckAgentUpdateRequest{AgentPublicId: agent.PublicID, Counter: counter}
		request.Signature = ed25519.Sign(updaterPrivate, agentupdateauth.CheckPayload(request.AgentPublicId, request.Counter))
		response, err := app.CheckAgentUpdate(ctx, connect.NewRequest(request))
		if err != nil {
			t.Fatal(err)
		}
		return response.Msg
	}
	beforeExpiry := check(1)
	if beforeExpiry.DesiredAction != p2pstreamv1.AgentUpdateDesiredAction_AGENT_UPDATE_DESIRED_ACTION_NONE || beforeExpiry.Authorization != nil {
		t.Fatalf("paused campaign exposed live activation authorization: %+v", beforeExpiry)
	}
	if _, err := database.ExecContext(ctx, `UPDATE agent_update_assignments SET authorization_expires_at=? WHERE agent_id=?`, time.Now().UTC().Add(-time.Minute), agent.ID); err != nil {
		t.Fatal(err)
	}
	afterExpiry := check(2)
	if afterExpiry.DesiredAction != p2pstreamv1.AgentUpdateDesiredAction_AGENT_UPDATE_DESIRED_ACTION_NONE || afterExpiry.Authorization != nil {
		t.Fatalf("paused campaign renewed expired activation authorization: %+v", afterExpiry)
	}
	var finalSequence int64
	if err := database.QueryRowContext(ctx, `SELECT last_command_sequence FROM agent_updater_identities WHERE agent_id=?`, agent.ID).Scan(&finalSequence); err != nil {
		t.Fatal(err)
	}
	if finalSequence != originalSequence {
		t.Fatalf("paused authorization sequence advanced from %d to %d", originalSequence, finalSequence)
	}
}

func TestAgentUpdatePreviewBlocksStaleUpdaterWorker(t *testing.T) {
	app, ids, target := newAgentUpdatePreviewScaleFixture(t, 1)
	old := time.Now().UTC().Add(-agentUpdateWorkerFreshness - time.Second)
	if _, err := app.DB.ExecContext(context.Background(), `UPDATE agent_updater_identities SET last_seen_at=? WHERE agent_id=?`, old, ids[0]); err != nil {
		t.Fatal(err)
	}
	preview, err := app.previewAgentUpdateAgents(context.Background(), ids, target, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview) != 1 || !slices.Contains(preview[0].Blockers, "updater_not_recently_seen") {
		t.Fatalf("stale updater preview = %+v", preview)
	}
}

func TestAgentUpdateAcknowledgesTerminalResultAcrossCampaigns(t *testing.T) {
	ctx := context.Background()
	database := newServerTestDB(t)
	agent := createAgentUpdateTestAgent(t, database, "cross-campaign-result")
	app := newAgentUpdateTestApp(t, database)
	updaterPublic, updaterPrivate, _ := ed25519.GenerateKey(rand.Reader)
	activatorPublic, _, _ := ed25519.GenerateKey(rand.Reader)
	insertAgentUpdateIdentity(t, app, database, agent.ID, updaterPublic, activatorPublic)
	_, oldAssignmentID := insertAgentUpdateTestCampaign(t, database, agent.ID, "failed", "none", false)
	now := time.Now().UTC()
	result, err := database.ExecContext(ctx, `INSERT INTO agent_update_campaigns (name,state,generation,target_version,target_commit,manifest_sha256,release_sequence,security_epoch,minimum_updater_version,minimum_tunnel_protocol,maximum_tunnel_protocol,artifacts_json,max_unavailable,minimum_eligible_agents_per_route,canary_count,wave_size,healthy_dwell_millis,created_at,updated_at) SELECT 'next-campaign','running',1,target_version,target_commit,manifest_sha256,release_sequence,security_epoch,minimum_updater_version,minimum_tunnel_protocol,maximum_tunnel_protocol,artifacts_json,1,1,1,1,10000,?,? FROM agent_update_campaigns ORDER BY id LIMIT 1`, now, now)
	if err != nil {
		t.Fatal(err)
	}
	newCampaignID, _ := result.LastInsertId()
	if _, err := database.ExecContext(ctx, `INSERT INTO agent_update_assignments (campaign_id,agent_id,state,desired_action,generation,created_at,updated_at) VALUES (?,?,'pending','stage',1,?,?)`, newCampaignID, agent.ID, now, now); err != nil {
		t.Fatal(err)
	}
	report := &p2pstreamv1.ReportAgentUpdateRequest{
		AgentPublicId: agent.PublicID, Counter: 1, AssignmentId: oldAssignmentID, Generation: 1,
		State: p2pstreamv1.AgentUpdaterReportState_AGENT_UPDATER_REPORT_STATE_FAILED,
	}
	signAgentUpdateTestReport(report, updaterPrivate)
	response, err := app.ReportAgentUpdate(ctx, connect.NewRequest(report))
	if err != nil {
		t.Fatal(err)
	}
	if response.Msg.State != p2pstreamv1.AgentUpdateAssignmentState_AGENT_UPDATE_ASSIGNMENT_STATE_FAILED || response.Msg.Generation != 1 {
		t.Fatalf("terminal cross-campaign acknowledgment = %+v", response.Msg)
	}
	var activeCampaignID int64
	if err := database.QueryRowContext(ctx, `SELECT campaign_id FROM agent_update_assignments WHERE agent_id=? AND state='pending'`, agent.ID).Scan(&activeCampaignID); err != nil {
		t.Fatal(err)
	}
	if activeCampaignID != newCampaignID {
		t.Fatalf("old durable result mutated new campaign %d, want %d", activeCampaignID, newCampaignID)
	}
}

func TestAgentUpdateSupersededActivationReceiptAcknowledgesNewGeneration(t *testing.T) {
	ctx := context.Background()
	database := newServerTestDB(t)
	agent := createAgentUpdateTestAgent(t, database, "superseded-activation")
	app := newAgentUpdateTestApp(t, database)
	updaterPublic, updaterPrivate, _ := ed25519.GenerateKey(rand.Reader)
	activatorPublic, activatorPrivate, _ := ed25519.GenerateKey(rand.Reader)
	insertAgentUpdateIdentity(t, app, database, agent.ID, updaterPublic, activatorPublic)
	campaignID, assignmentID := insertAgentUpdateTestCampaign(t, database, agent.ID, "staged", "none", false)
	authorizeAgentUpdateTestActivation(t, app, agent.ID)
	staleReceipt := newAgentUpdateTestRootActionReceipt(t, app, agent.ID, activatorPrivate, agentupdateauth.AssignmentActionActivate, 1)
	header := createTestAdminSession(t, app)
	cancel := connect.NewRequest(&p2pstreamv1.ChangeAgentUpdateCampaignStateRequest{CampaignId: campaignID, ExpectedGeneration: 1})
	cancel.Header().Set("Cookie", header.Get("Cookie"))
	if _, err := app.CancelAgentUpdateCampaign(ctx, cancel); err != nil {
		t.Fatal(err)
	}
	report := &p2pstreamv1.ReportAgentUpdateRequest{
		AgentPublicId: agent.PublicID, Counter: 1, AssignmentId: assignmentID, Generation: 1,
		State:          p2pstreamv1.AgentUpdaterReportState_AGENT_UPDATER_REPORT_STATE_ACTIVATED,
		ManifestSha256: staleReceipt.ResultManifestSha256, BinarySha256: staleReceipt.ResultArtifactSha256,
		RunningVersion: staleReceipt.ResultVersion, RunningCommit: staleReceipt.ResultCommit, RootActionReceipt: staleReceipt,
	}
	signAgentUpdateTestReport(report, updaterPrivate)
	response, err := app.ReportAgentUpdate(ctx, connect.NewRequest(report))
	if err != nil {
		t.Fatal(err)
	}
	if response.Msg.Generation != 2 || response.Msg.DesiredAction != p2pstreamv1.AgentUpdateDesiredAction_AGENT_UPDATE_DESIRED_ACTION_ROLLBACK {
		t.Fatalf("superseded activation acknowledgment = %+v", response.Msg)
	}
}

func TestAgentUpdaterReenrollmentPreservesAuthorityCommandFloor(t *testing.T) {
	ctx := context.Background()
	database := newServerTestDB(t)
	agent := createAgentUpdateTestAgent(t, database, "reenroll-floor")
	app := newAgentUpdateTestApp(t, database)
	provider, bootstrap := newAgentUpdateTestBootstrap(t)
	app.AgentUpdateBootstrap = provider
	oldUpdater, _, _ := ed25519.GenerateKey(rand.Reader)
	activatorPublic, _, _ := ed25519.GenerateKey(rand.Reader)
	insertAgentUpdateIdentity(t, app, database, agent.ID, oldUpdater, activatorPublic)
	if _, err := database.ExecContext(ctx, `UPDATE agent_updater_identities SET last_counter=5,last_command_sequence=7,last_root_action_counter=9 WHERE agent_id=?`, agent.ID); err != nil {
		t.Fatal(err)
	}
	token, _, err := app.createAgentUpdaterEnrollmentToken(ctx, agent.ID, time.Minute, bootstrap)
	if err != nil {
		t.Fatal(err)
	}
	newUpdater, _, _ := ed25519.GenerateKey(rand.Reader)
	request := connect.NewRequest(&p2pstreamv1.EnrollAgentUpdaterRequest{Token: token, AgentPublicId: agent.PublicID, UpdaterPublicKey: newUpdater, ActivatorPublicKey: activatorPublic, Os: "linux", Arch: "amd64", UpdaterVersion: "v1.1.0"})
	if _, err := app.EnrollAgentUpdater(ctx, request); err != nil {
		t.Fatal(err)
	}
	var workerCounter, commandSequence, rootCounter int64
	if err := database.QueryRowContext(ctx, `SELECT last_counter,last_command_sequence,last_root_action_counter FROM agent_updater_identities WHERE agent_id=?`, agent.ID).Scan(&workerCounter, &commandSequence, &rootCounter); err != nil {
		t.Fatal(err)
	}
	if workerCounter != 0 || commandSequence != 7 || rootCounter != 9 {
		t.Fatalf("reenrollment floors = worker:%d command:%d root:%d", workerCounter, commandSequence, rootCounter)
	}
}

func aCampaignByID(ctx context.Context, app *App, id int64) (agentUpdateCampaignRow, error) {
	var campaign agentUpdateCampaignRow
	err := app.DB.QueryRowContext(ctx, `SELECT id,name,state,generation,target_version,target_commit,manifest_sha256,release_sequence,security_epoch,minimum_updater_version,minimum_tunnel_protocol,maximum_tunnel_protocol,artifacts_json,max_unavailable,minimum_eligible_agents_per_route,canary_count,wave_size,healthy_dwell_millis,created_by_user_id,created_at,updated_at,completed_at FROM agent_update_campaigns WHERE id=?`, id).Scan(&campaign.ID, &campaign.Name, &campaign.State, &campaign.Generation, &campaign.TargetVersion, &campaign.TargetCommit, &campaign.ManifestSha256, &campaign.ReleaseSequence, &campaign.SecurityEpoch, &campaign.MinimumUpdaterVersion, &campaign.MinimumTunnelProtocol, &campaign.MaximumTunnelProtocol, &campaign.ArtifactsJson, &campaign.MaxUnavailable, &campaign.MinimumEligibleAgentsPerRoute, &campaign.CanaryCount, &campaign.WaveSize, &campaign.HealthyDwellMillis, &campaign.CreatedByUserID, &campaign.CreatedAt, &campaign.UpdatedAt, &campaign.CompletedAt)
	if err == sql.ErrNoRows {
		return campaign, err
	}
	return campaign, err
}

func BenchmarkAgentUpdateMaintenanceThousandAgentCampaign(b *testing.B) {
	app, ids, _ := newAgentUpdatePreviewScaleFixture(b, 1000)
	now := time.Now().UTC()
	result, err := app.DB.ExecContext(context.Background(), `INSERT INTO agent_update_campaigns (name,state,generation,target_version,target_commit,manifest_sha256,release_sequence,security_epoch,minimum_updater_version,minimum_tunnel_protocol,maximum_tunnel_protocol,artifacts_json,max_unavailable,minimum_eligible_agents_per_route,canary_count,wave_size,healthy_dwell_millis,created_at,updated_at) VALUES ('maintenance-scale','running',1,'v1.2.3',?,?,1,1,'v1.0.0',1,1,'[]',10,1,10,50,10000,?,?)`, strings.Repeat("c", 40), strings.Repeat("a", 64), now, now)
	if err != nil {
		b.Fatal(err)
	}
	campaignID, _ := result.LastInsertId()
	tx, err := app.DB.Begin()
	if err != nil {
		b.Fatal(err)
	}
	for _, agentID := range ids {
		if _, err := tx.Exec(`INSERT INTO agent_update_assignments (campaign_id,agent_id,state,desired_action,generation,created_at,updated_at) VALUES (?,?,'pending','none',1,?,?)`, campaignID, agentID, now, now); err != nil {
			tx.Rollback()
			b.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		app.reconcileAgentUpdateMaintenance(context.Background(), now)
	}
}
