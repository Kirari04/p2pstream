package server

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/internal/agentupdateauth"
	"p2pstream/internal/buildinfo"
	"p2pstream/internal/db"
)

// These regressions exercise transitions through the public updater/admin
// handlers. SQL is used only to move the server-owned watchdog clock forward.
type deepRecoveryFixture struct {
	t                *testing.T
	app              *App
	database         *db.DB
	agent            db.Agent
	updaterPrivate   ed25519.PrivateKey
	activatorPrivate ed25519.PrivateKey
	campaignID       int64
	assignmentID     int64
	counter          uint64
	cookie           string
}

func newDeepRecoveryFixture(t *testing.T) *deepRecoveryFixture {
	t.Helper()
	database := newServerTestDB(t)
	app := newAgentUpdateTestApp(t, database)
	agent := createAgentUpdateTestAgent(t, database, "deep-recovery")
	updaterPublic, updaterPrivate, _ := ed25519.GenerateKey(rand.Reader)
	activatorPublic, activatorPrivate, _ := ed25519.GenerateKey(rand.Reader)
	insertAgentUpdateIdentity(t, app, database, agent.ID, updaterPublic, activatorPublic)
	campaignID, assignmentID := insertAgentUpdateTestCampaign(t, database, agent.ID, "staged", "none", false)
	authorizeAgentUpdateTestActivation(t, app, agent.ID)
	return &deepRecoveryFixture{t: t, app: app, database: database, agent: agent, updaterPrivate: updaterPrivate,
		activatorPrivate: activatorPrivate, campaignID: campaignID, assignmentID: assignmentID,
		cookie: createTestAdminSession(t, app).Get("Cookie")}
}

func (f *deepRecoveryFixture) report(state p2pstreamv1.AgentUpdaterReportState, rootCounter uint64) {
	f.t.Helper()
	x, _, err := f.app.activeAgentUpdateAssignment(context.Background(), f.agent.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	f.counter++
	report := &p2pstreamv1.ReportAgentUpdateRequest{AgentPublicId: f.agent.PublicID, Counter: f.counter,
		AssignmentId: f.assignmentID, Generation: x.Generation, State: state}
	if state == p2pstreamv1.AgentUpdaterReportState_AGENT_UPDATER_REPORT_STATE_FAILED {
		report.FailureCode, report.FailureDetail = "action_failed", "test root action failure"
	} else {
		action := agentupdateauth.AssignmentActionActivate
		if state == p2pstreamv1.AgentUpdaterReportState_AGENT_UPDATER_REPORT_STATE_ROLLED_BACK {
			action = agentupdateauth.AssignmentActionRollback
		}
		receipt := newAgentUpdateTestRootActionReceipt(f.t, f.app, f.agent.ID, f.activatorPrivate, action, rootCounter)
		report.RootActionReceipt = receipt
		report.ManifestSha256, report.BinarySha256 = receipt.ResultManifestSha256, receipt.ResultArtifactSha256
		report.RunningVersion, report.RunningCommit = receipt.ResultVersion, receipt.ResultCommit
	}
	signAgentUpdateTestReport(report, f.updaterPrivate)
	if _, err := f.app.ReportAgentUpdate(context.Background(), connect.NewRequest(report)); err != nil {
		f.t.Fatal(err)
	}
}

func (f *deepRecoveryFixture) retryResumeAndPoll() {
	f.t.Helper()
	campaign, err := f.app.getAgentUpdateCampaignProto(context.Background(), f.campaignID)
	if err != nil {
		f.t.Fatal(err)
	}
	retry := connect.NewRequest(&p2pstreamv1.RetryAgentUpdateAssignmentsRequest{CampaignId: f.campaignID,
		ExpectedCampaignGeneration: campaign.Generation, AssignmentIds: []int64{f.assignmentID}})
	retry.Header().Set("Cookie", f.cookie)
	retried, err := f.app.RetryAgentUpdateAssignments(context.Background(), retry)
	if err != nil {
		f.t.Fatal(err)
	}
	resume := connect.NewRequest(&p2pstreamv1.ChangeAgentUpdateCampaignStateRequest{CampaignId: f.campaignID, ExpectedGeneration: retried.Msg.Campaign.Generation})
	resume.Header().Set("Cookie", f.cookie)
	if _, err := f.app.ResumeAgentUpdateCampaign(context.Background(), resume); err != nil {
		f.t.Fatal(err)
	}
	f.counter++
	check := &p2pstreamv1.CheckAgentUpdateRequest{AgentPublicId: f.agent.PublicID, Counter: f.counter}
	check.Signature = ed25519.Sign(f.updaterPrivate, agentupdateauth.CheckPayload(check.AgentPublicId, check.Counter))
	response, err := f.app.CheckAgentUpdate(context.Background(), connect.NewRequest(check))
	if err != nil || response.Msg.DesiredAction != p2pstreamv1.AgentUpdateDesiredAction_AGENT_UPDATE_DESIRED_ACTION_ROLLBACK || response.Msg.Authorization == nil {
		f.t.Fatalf("expected authorized rollback: response=%+v err=%v", response, err)
	}
}

func TestDeepReviewCancelPreservesUnconfirmedRollbackFence(t *testing.T) {
	for _, rollbackReceipt := range []bool{false, true} {
		name := "failed rollback"
		if rollbackReceipt {
			name = "rollback awaiting fresh tunnel"
		}
		t.Run(name, func(t *testing.T) {
			f := newDeepRecoveryFixture(t)
			// Activation escaped but no activation receipt reached management.
			f.report(p2pstreamv1.AgentUpdaterReportState_AGENT_UPDATER_REPORT_STATE_FAILED, 0)
			f.retryResumeAndPoll()
			if rollbackReceipt {
				f.report(p2pstreamv1.AgentUpdaterReportState_AGENT_UPDATER_REPORT_STATE_ROLLED_BACK, 1)
			} else {
				f.report(p2pstreamv1.AgentUpdaterReportState_AGENT_UPDATER_REPORT_STATE_FAILED, 0)
			}
			if !f.app.isAgentUpdateCordoned(f.agent.ID) {
				t.Fatal("fixture should remain fenced before cancel")
			}
			campaign, err := f.app.getAgentUpdateCampaignProto(context.Background(), f.campaignID)
			if err != nil {
				t.Fatal(err)
			}
			cancel := connect.NewRequest(&p2pstreamv1.ChangeAgentUpdateCampaignStateRequest{CampaignId: f.campaignID, ExpectedGeneration: campaign.Generation})
			cancel.Header().Set("Cookie", f.cookie)
			if _, err := f.app.CancelAgentUpdateCampaign(context.Background(), cancel); err != nil {
				t.Fatal(err)
			}
			var state, action string
			var cordoned int64
			if err := f.database.QueryRow(`SELECT state,desired_action,cordoned FROM agent_update_assignments WHERE id=?`, f.assignmentID).Scan(&state, &action, &cordoned); err != nil {
				t.Fatal(err)
			}
			if cordoned != 1 || !f.app.isAgentUpdateCordoned(f.agent.ID) {
				t.Fatalf("cancel discarded unconfirmed rollback fence: state=%s action=%s cordoned=%d", state, action, cordoned)
			}
			if !rollbackReceipt {
				f.counter++
				check := &p2pstreamv1.CheckAgentUpdateRequest{AgentPublicId: f.agent.PublicID, Counter: f.counter}
				check.Signature = ed25519.Sign(f.updaterPrivate, agentupdateauth.CheckPayload(check.AgentPublicId, check.Counter))
				response, err := f.app.CheckAgentUpdate(context.Background(), connect.NewRequest(check))
				if err != nil || response.Msg.Authorization == nil || response.Msg.Authorization.Generation != response.Msg.Generation {
					t.Fatalf("cancel retained a superseded rollback authorization: response=%+v err=%v", response, err)
				}
				f.report(p2pstreamv1.AgentUpdaterReportState_AGENT_UPDATER_REPORT_STATE_ROLLED_BACK, 1)
			}
			var completedAt time.Time
			if err := f.database.QueryRow(`SELECT root_action_completed_at FROM agent_update_assignments WHERE id=?`, f.assignmentID).Scan(&completedAt); err != nil {
				t.Fatal(err)
			}
			conn := &AgentConn{AgentID: f.agent.ID, PublicID: f.agent.PublicID, Done: make(chan struct{}), ConnectedAt: completedAt.Add(time.Millisecond)}
			if err := f.app.AgentHub.connect(conn); err != nil {
				t.Fatal(err)
			}
			f.app.recordAgentUpdateFreshTunnel(conn)
			if f.app.isAgentUpdateCordoned(f.agent.ID) {
				t.Fatal("cancelled recovery could not finish after verified root result and fresh tunnel")
			}
		})
	}
}

func TestDeepReviewCancelStillReleasesPreAuthorizationDrain(t *testing.T) {
	database := newServerTestDB(t)
	app := newAgentUpdateTestApp(t, database)
	agent := createAgentUpdateTestAgent(t, database, "cancel-preauth-drain")
	public, _, _ := ed25519.GenerateKey(rand.Reader)
	rootPublic, _, _ := ed25519.GenerateKey(rand.Reader)
	insertAgentUpdateIdentity(t, app, database, agent.ID, public, rootPublic)
	campaignID, assignmentID := insertAgentUpdateTestCampaign(t, database, agent.ID, "staged", "none", false)
	app.agentUpdateDrainReady = func(int64) bool { return false }
	x, campaign, err := app.activeAgentUpdateAssignment(context.Background(), agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	app.tryAdvanceAgentUpdateAssignment(context.Background(), x, campaign)
	x, _, err = app.activeAgentUpdateAssignment(context.Background(), agent.ID)
	if err != nil || x.State != "cordoned" || x.DesiredAction != "none" || x.AuthorizationAction != "" || !app.isAgentUpdateCordoned(agent.ID) {
		t.Fatalf("expected reversible drain: %+v err=%v", x, err)
	}
	header := createTestAdminSession(t, app)
	request := connect.NewRequest(&p2pstreamv1.ChangeAgentUpdateCampaignStateRequest{CampaignId: campaignID, ExpectedGeneration: 1})
	request.Header().Set("Cookie", header.Get("Cookie"))
	if _, err := app.CancelAgentUpdateCampaign(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	assertAgentUpdateAssignmentState(t, database, assignmentID, "cancelled")
	if app.isAgentUpdateCordoned(agent.ID) {
		t.Fatal("cancellation did not release the reversible pre-authorization drain")
	}
}

func TestDeepReviewRetriedRollbackGetsIndependentRootActionDeadline(t *testing.T) {
	for _, legacyCompletion := range []bool{false, true} {
		name := "fresh retry"
		if legacyCompletion {
			name = "persisted older management retry"
		}
		t.Run(name, func(t *testing.T) {
			f := newDeepRecoveryFixture(t)
			f.report(p2pstreamv1.AgentUpdaterReportState_AGENT_UPDATER_REPORT_STATE_ACTIVATED, 1)
			f.report(p2pstreamv1.AgentUpdaterReportState_AGENT_UPDATER_REPORT_STATE_FAILED, 0)
			f.retryResumeAndPoll()
			old := time.Now().UTC().Add(-agentUpdatePostActionTimeout - time.Second)
			if legacyCompletion {
				// Before the fix a retry carried the activation's completion into the
				// pending rollback. Upgrading management must also recover these rows.
				if _, err := f.database.Exec(`UPDATE agent_update_assignments SET root_action_completed_at=? WHERE id=?`, old.Add(-time.Minute), f.assignmentID); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := f.database.Exec(`UPDATE agent_update_assignments SET updated_at=? WHERE id=?`, old, f.assignmentID); err != nil {
				t.Fatal(err)
			}
			f.app.reconcileAgentUpdateMaintenance(context.Background(), time.Now().UTC())
			var state, action, failure string
			if err := f.database.QueryRow(`SELECT state,desired_action,failure_code FROM agent_update_assignments WHERE id=?`, f.assignmentID).Scan(&state, &action, &failure); err != nil {
				t.Fatal(err)
			}
			if state != "blocked" || action != "none" || failure != "root_action_timeout" {
				t.Fatalf("previous activation receipt disabled rollback watchdog: state=%s action=%s failure=%s", state, action, failure)
			}
			var rootCounter int64
			if err := f.database.QueryRow(`SELECT last_root_action_counter FROM agent_updater_identities WHERE agent_id=?`, f.agent.ID).Scan(&rootCounter); err != nil {
				t.Fatal(err)
			}
			if rootCounter != 1 || !f.app.isAgentUpdateCordoned(f.agent.ID) {
				t.Fatalf("recovery reset replay protection or routing fence: rootCounter=%d", rootCounter)
			}
			f.retryResumeAndPoll()
			f.report(p2pstreamv1.AgentUpdaterReportState_AGENT_UPDATER_REPORT_STATE_ROLLED_BACK, 2)
			assertAgentUpdateAssignmentState(t, f.database, f.assignmentID, "awaiting_tunnel")
		})
	}
}

func TestDeepReviewLegacyCancelledRollbackRenewsGenerationAuthorization(t *testing.T) {
	f := newDeepRecoveryFixture(t)
	f.report(p2pstreamv1.AgentUpdaterReportState_AGENT_UPDATER_REPORT_STATE_ACTIVATED, 1)
	f.report(p2pstreamv1.AgentUpdaterReportState_AGENT_UPDATER_REPORT_STATE_FAILED, 0)
	f.retryResumeAndPoll()
	f.report(p2pstreamv1.AgentUpdaterReportState_AGENT_UPDATER_REPORT_STATE_ROLLED_BACK, 2)
	// Older cancellation incremented generation while retaining an unexpired
	// rollback authorization and root receipt from the previous generation.
	if _, err := f.database.Exec(`UPDATE agent_update_campaigns SET state='cancelled',generation=generation+1 WHERE id=?`, f.campaignID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.database.Exec(`UPDATE agent_update_assignments SET generation=generation+1,desired_action='rollback' WHERE id=?`, f.assignmentID); err != nil {
		t.Fatal(err)
	}
	var completedAt time.Time
	if err := f.database.QueryRow(`SELECT root_action_completed_at FROM agent_update_assignments WHERE id=?`, f.assignmentID).Scan(&completedAt); err != nil {
		t.Fatal(err)
	}
	conn := &AgentConn{AgentID: f.agent.ID, PublicID: f.agent.PublicID, Done: make(chan struct{}), ConnectedAt: completedAt.Add(time.Millisecond)}
	if err := f.app.AgentHub.connect(conn); err != nil {
		t.Fatal(err)
	}
	f.app.recordAgentUpdateFreshTunnel(conn)
	var freshAt sql.NullTime
	if err := f.database.QueryRow(`SELECT fresh_tunnel_at FROM agent_update_assignments WHERE id=?`, f.assignmentID).Scan(&freshAt); err != nil {
		t.Fatal(err)
	}
	if freshAt.Valid || !f.app.isAgentUpdateCordoned(f.agent.ID) {
		t.Fatal("old-generation root receipt became evidence for pending rollback")
	}
	f.counter++
	check := &p2pstreamv1.CheckAgentUpdateRequest{AgentPublicId: f.agent.PublicID, Counter: f.counter}
	check.Signature = ed25519.Sign(f.updaterPrivate, agentupdateauth.CheckPayload(check.AgentPublicId, check.Counter))
	response, err := f.app.CheckAgentUpdate(context.Background(), connect.NewRequest(check))
	if err != nil || response.Msg.Authorization == nil || response.Msg.Generation != response.Msg.Authorization.Generation || response.Msg.Authorization.CommandSequence != 3 {
		t.Fatalf("persisted superseded rollback authorization blocked recovery: response=%+v err=%v", response, err)
	}
	f.report(p2pstreamv1.AgentUpdaterReportState_AGENT_UPDATER_REPORT_STATE_ROLLED_BACK, 3)
	assertAgentUpdateAssignmentState(t, f.database, f.assignmentID, "awaiting_tunnel")
}

func TestDeepReviewRecoveryCampaignPrecedesTerminalHistory(t *testing.T) {
	f := newDeepRecoveryFixture(t)
	if _, err := f.database.Exec(`UPDATE agent_update_campaigns SET state='cancelled' WHERE id=?`, f.campaignID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.database.Exec(`UPDATE agent_update_assignments SET state='blocked',desired_action='none' WHERE id=?`, f.assignmentID); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		id, _ := insertAgentUpdateTestCampaign(t, f.database, f.agent.ID, "succeeded", "none", false)
		if _, err := f.database.Exec(`UPDATE agent_update_campaigns SET state='completed' WHERE id=?`, id); err != nil {
			t.Fatal(err)
		}
	}
	for _, limit := range []int64{0, 1, 100} {
		request := connect.NewRequest(&p2pstreamv1.ListAgentUpdateCampaignsRequest{Limit: limit})
		request.Header().Set("Cookie", f.cookie)
		response, err := f.app.ListAgentUpdateCampaigns(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		if len(response.Msg.Campaigns) == 0 || response.Msg.Campaigns[0].Id != f.campaignID || response.Msg.Campaigns[0].State != p2pstreamv1.AgentUpdateCampaignState_AGENT_UPDATE_CAMPAIGN_STATE_CANCELLED {
			t.Fatalf("limit %d hid the cancelled campaign that still reserves the agent", limit)
		}
	}
}

func TestDeepReviewPreviewRejectsCurrentlyRunningTarget(t *testing.T) {
	f := newDeepRecoveryFixture(t)
	campaign, err := f.app.getAgentUpdateCampaignProto(context.Background(), f.campaignID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.database.Exec(`UPDATE agent_update_assignments SET state='succeeded',desired_action='none',cordoned=0 WHERE id=?`, f.assignmentID); err != nil {
		t.Fatal(err)
	}
	f.app.clearAgentUpdateCordon(f.agent.ID)
	for _, tc := range []struct {
		name    string
		version string
		commit  string
		blocked bool
	}{
		{name: "same authenticated build", version: campaign.Target.Version, commit: campaign.Target.Commit, blocked: true},
		{name: "older build", version: "v1.0.0", commit: strings.Repeat("d", 40)},
		{name: "legacy build headers absent"},
		{name: "same version different commit", version: campaign.Target.Version, commit: strings.Repeat("d", 40)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn := &AgentConn{AgentID: f.agent.ID, PublicID: f.agent.PublicID, Done: make(chan struct{}), ConnectedAt: time.Now(), BuildVersion: tc.version, BuildCommit: tc.commit}
			if err := f.app.AgentHub.connect(conn); err != nil {
				t.Fatal(err)
			}
			defer f.app.AgentHub.disconnect(conn)
			preview, err := f.app.previewAgentUpdateAgents(context.Background(), []int64{f.agent.ID}, campaign.Target, 1)
			if err != nil {
				t.Fatal(err)
			}
			if slices.Contains(preview[0].Blockers, "already_on_target") != tc.blocked || preview[0].Eligible == tc.blocked {
				t.Fatalf("live-target preview eligibility mismatch: %+v", preview[0])
			}
		})
	}
}

func TestDeepReviewPendingAuthorizationSurvivesManagementUpgrade(t *testing.T) {
	for _, rollback := range []bool{false, true} {
		name := "activation"
		if rollback {
			name = "rollback"
		}
		t.Run(name, func(t *testing.T) {
			f := newDeepRecoveryFixture(t)
			if rollback {
				f.report(p2pstreamv1.AgentUpdaterReportState_AGENT_UPDATER_REPORT_STATE_FAILED, 0)
				f.retryResumeAndPoll()
			}
			old, _, err := f.app.activeAgentUpdateAssignment(context.Background(), f.agent.ID)
			if err != nil {
				t.Fatal(err)
			}
			// The root command may already be staged/executing when management restarts
			// into the next version. Preserve that exact signed command until completion.
			buildinfo.Version = "v0.0.1-test"
			f.counter++
			check := &p2pstreamv1.CheckAgentUpdateRequest{AgentPublicId: f.agent.PublicID, Counter: f.counter}
			check.Signature = ed25519.Sign(f.updaterPrivate, agentupdateauth.CheckPayload(check.AgentPublicId, check.Counter))
			response, err := f.app.CheckAgentUpdate(context.Background(), connect.NewRequest(check))
			if err != nil {
				t.Fatal(err)
			}
			if response.Msg.Authorization == nil || response.Msg.ServerVersion != response.Msg.Authorization.ServerVersion || response.Msg.Authorization.CommandSequence != uint64(old.CommandSequence) || string(response.Msg.Authorization.CanonicalPayload) != string(old.AuthorizationPayload) {
				t.Fatalf("management upgrade changed an escaped command context: %+v", response.Msg)
			}
			state := p2pstreamv1.AgentUpdaterReportState_AGENT_UPDATER_REPORT_STATE_ACTIVATED
			if rollback {
				state = p2pstreamv1.AgentUpdaterReportState_AGENT_UPDATER_REPORT_STATE_ROLLED_BACK
			}
			f.report(state, 1)
			assertAgentUpdateAssignmentState(t, f.database, f.assignmentID, "awaiting_tunnel")
			if !f.app.isAgentUpdateCordoned(f.agent.ID) {
				t.Fatal("management restart bypassed post-action tunnel evidence")
			}
		})
	}
}

func TestDeepReviewLateExecutionFailureCannotReplaceCommittedRootResult(t *testing.T) {
	for _, rollback := range []bool{false, true} {
		name, failureCode := "activation", "activation_failed"
		if rollback {
			name, failureCode = "rollback", "rollback_failed"
		}
		t.Run(name, func(t *testing.T) {
			f := newDeepRecoveryFixture(t)
			state := p2pstreamv1.AgentUpdaterReportState_AGENT_UPDATER_REPORT_STATE_ACTIVATED
			if rollback {
				f.report(p2pstreamv1.AgentUpdaterReportState_AGENT_UPDATER_REPORT_STATE_FAILED, 0)
				f.retryResumeAndPoll()
				state = p2pstreamv1.AgentUpdaterReportState_AGENT_UPDATER_REPORT_STATE_ROLLED_BACK
			}
			f.report(state, 1)
			before, campaign, err := f.app.activeAgentUpdateAssignment(context.Background(), f.agent.ID)
			if err != nil {
				t.Fatal(err)
			}
			f.counter++
			report := &p2pstreamv1.ReportAgentUpdateRequest{AgentPublicId: f.agent.PublicID, AssignmentId: before.ID, Generation: before.Generation, Counter: f.counter,
				State: p2pstreamv1.AgentUpdaterReportState_AGENT_UPDATER_REPORT_STATE_FAILED, FailureCode: failureCode,
				FailureDetail: "verify signed root-action authorization: assignment authorization command sequence was replayed"}
			signAgentUpdateTestReport(report, f.updaterPrivate)
			report.Signature[0] ^= 1
			if _, err := f.app.ReportAgentUpdate(context.Background(), connect.NewRequest(report)); connect.CodeOf(err) != connect.CodeUnauthenticated {
				t.Fatalf("invalid worker signature accepted: %v", err)
			}
			report.Signature[0] ^= 1
			response, err := f.app.ReportAgentUpdate(context.Background(), connect.NewRequest(report))
			if err != nil || response.Msg.State != p2pstreamv1.AgentUpdateAssignmentState_AGENT_UPDATE_ASSIGNMENT_STATE_AWAITING_TUNNEL {
				t.Fatalf("late execution failure replaced signed root success: response=%+v err=%v", response, err)
			}
			after, afterCampaign, err := f.app.activeAgentUpdateAssignment(context.Background(), f.agent.ID)
			if err != nil || after.State != before.State || after.DesiredAction != before.DesiredAction || after.FailureCode != before.FailureCode ||
				!after.UpdatedAt.Equal(before.UpdatedAt) || after.RootActionCounter != before.RootActionCounter ||
				string(after.RootActionReceiptPayload) != string(before.RootActionReceiptPayload) || afterCampaign.State != campaign.State || !f.app.isAgentUpdateCordoned(f.agent.ID) {
				t.Fatalf("obsolete failure changed proof, deadline or routing fence: after=%+v err=%v", after, err)
			}
			// The acknowledgment is idempotent and lets an older worker remove its
			// durable failure file instead of starving normal checks forever.
			if _, err := f.app.ReportAgentUpdate(context.Background(), connect.NewRequest(report)); err != nil {
				t.Fatalf("lost-response obsolete failure retry was rejected: %v", err)
			}
			if !rollback {
				report.Counter++
				report.FailureCode = "post_activation_health_failed"
				report.FailureDetail = "the activated service subsequently failed health checks"
				signAgentUpdateTestReport(report, f.updaterPrivate)
				if _, err := f.app.ReportAgentUpdate(context.Background(), connect.NewRequest(report)); err != nil {
					t.Fatal(err)
				}
				assertAgentUpdateAssignmentState(t, f.database, f.assignmentID, "blocked")
			}
		})
	}
}
