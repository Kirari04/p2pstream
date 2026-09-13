package server

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"slices"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/internal/agentupdateauth"
)

func TestCancelledBlockedAgentUpdateAcceptsRollbackRecovery(t *testing.T) {
	for _, tc := range []struct {
		name          string
		rollbackFails bool
		legacyReport  bool
		timedOut      bool
	}{
		{name: "verified rollback releases assignment after fresh tunnel"},
		{name: "legacy rollback report uses signed receipt results", legacyReport: true},
		{name: "legacy rollback recovers after watchdog timeout", legacyReport: true, timedOut: true},
		{name: "rollback failure remains fenced and can be retried", rollbackFails: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			database := newServerTestDB(t)
			app := newAgentUpdateTestApp(t, database)
			agent := createAgentUpdateTestAgent(t, database, "cancel-blocked-recovery")
			updaterPublic, updaterPrivate, _ := ed25519.GenerateKey(rand.Reader)
			activatorPublic, activatorPrivate, _ := ed25519.GenerateKey(rand.Reader)
			insertAgentUpdateIdentity(t, app, database, agent.ID, updaterPublic, activatorPublic)
			campaignID, assignmentID := insertAgentUpdateTestCampaign(t, database, agent.ID, "staged", "none", false)
			authorizeAgentUpdateTestActivation(t, app, agent.ID)

			failure := &p2pstreamv1.ReportAgentUpdateRequest{
				AgentPublicId: agent.PublicID, Counter: 1, AssignmentId: assignmentID, Generation: 1,
				State:       p2pstreamv1.AgentUpdaterReportState_AGENT_UPDATER_REPORT_STATE_FAILED,
				FailureCode: "activation_failed", FailureDetail: "candidate failed health check and was rolled back",
			}
			signAgentUpdateTestReport(failure, updaterPrivate)
			if _, err := app.ReportAgentUpdate(ctx, connect.NewRequest(failure)); err != nil {
				t.Fatal(err)
			}
			assertAgentUpdateAssignmentState(t, database, assignmentID, "blocked")

			header := createTestAdminSession(t, app)
			cancel := connect.NewRequest(&p2pstreamv1.ChangeAgentUpdateCampaignStateRequest{CampaignId: campaignID, ExpectedGeneration: 2})
			cancel.Header().Set("Cookie", header.Get("Cookie"))
			if _, err := app.CancelAgentUpdateCampaign(ctx, cancel); err != nil {
				t.Fatal(err)
			}
			check := &p2pstreamv1.CheckAgentUpdateRequest{AgentPublicId: agent.PublicID, Counter: 2}
			check.Signature = ed25519.Sign(updaterPrivate, agentupdateauth.CheckPayload(check.AgentPublicId, check.Counter))
			response, err := app.CheckAgentUpdate(ctx, connect.NewRequest(check))
			if err != nil {
				t.Fatal(err)
			}
			if response.Msg.DesiredAction != p2pstreamv1.AgentUpdateDesiredAction_AGENT_UPDATE_DESIRED_ACTION_ROLLBACK || response.Msg.Authorization == nil {
				t.Fatalf("cancel did not authorize recovery: %+v", response.Msg)
			}
			preview, err := app.previewAgentUpdateAgents(ctx, []int64{agent.ID}, response.Msg.Target, 1)
			if err != nil || !slices.Contains(preview[0].Blockers, "active_assignment") || !app.isAgentUpdateCordoned(agent.ID) {
				t.Fatalf("unconfirmed rollback became eligible: preview=%+v err=%v", preview, err)
			}

			report := &p2pstreamv1.ReportAgentUpdateRequest{
				AgentPublicId: agent.PublicID, Counter: 3, AssignmentId: assignmentID, Generation: response.Msg.Generation,
				State: p2pstreamv1.AgentUpdaterReportState_AGENT_UPDATER_REPORT_STATE_ROLLED_BACK,
			}
			if tc.rollbackFails {
				report.State = p2pstreamv1.AgentUpdaterReportState_AGENT_UPDATER_REPORT_STATE_FAILED
				report.FailureCode = "rollback_failed"
			} else {
				receipt := newAgentUpdateTestRootActionReceipt(t, app, agent.ID, activatorPrivate, agentupdateauth.AssignmentActionRollback, 1)
				if !tc.legacyReport {
					report.ManifestSha256, report.BinarySha256 = receipt.ResultManifestSha256, receipt.ResultArtifactSha256
					report.RunningVersion, report.RunningCommit = receipt.ResultVersion, receipt.ResultCommit
				}
				report.RootActionReceipt = receipt
				if tc.legacyReport {
					partial := proto.Clone(report).(*p2pstreamv1.ReportAgentUpdateRequest)
					partial.BinarySha256 = receipt.ResultArtifactSha256
					signAgentUpdateTestReport(partial, updaterPrivate)
					if _, err := app.ReportAgentUpdate(ctx, connect.NewRequest(partial)); connect.CodeOf(err) != connect.CodeFailedPrecondition {
						t.Fatalf("partially omitted result accepted: %v", err)
					}
				}
				// The blocked phase must not make an unsigned worker's claim enough
				// to clear the fence: the root receipt still has to authenticate.
				receipt.Signature[0] ^= 1
				signAgentUpdateTestReport(report, updaterPrivate)
				if _, err := app.ReportAgentUpdate(ctx, connect.NewRequest(report)); connect.CodeOf(err) != connect.CodeUnauthenticated {
					t.Fatalf("forged rollback receipt accepted: %v", err)
				}
				assertAgentUpdateAssignmentState(t, database, assignmentID, "blocked")
				receipt.Signature[0] ^= 1
			}
			if tc.timedOut {
				old := time.Now().UTC().Add(-agentUpdatePostActionTimeout - time.Second)
				if _, err := database.ExecContext(ctx, `UPDATE agent_update_assignments SET updated_at=? WHERE id=?`, old, assignmentID); err != nil {
					t.Fatal(err)
				}
				app.reconcileAgentUpdateMaintenance(ctx, time.Now().UTC())
				signAgentUpdateTestReport(report, updaterPrivate)
				if _, err := app.ReportAgentUpdate(ctx, connect.NewRequest(report)); connect.CodeOf(err) != connect.CodeFailedPrecondition {
					t.Fatalf("timed-out rollback bypassed administrator recovery: %v", err)
				}
				retry := connect.NewRequest(&p2pstreamv1.RetryAgentUpdateAssignmentsRequest{CampaignId: campaignID, ExpectedCampaignGeneration: 3, AssignmentIds: []int64{assignmentID}})
				retry.Header().Set("Cookie", header.Get("Cookie"))
				if _, err := app.RetryAgentUpdateAssignments(ctx, retry); err != nil {
					t.Fatalf("timed-out recovery could not be retried: %v", err)
				}
				// Drain the obsolete durable result without using it as recovery
				// evidence. The worker can then poll the new signed rollback.
				ack, err := app.ReportAgentUpdate(ctx, connect.NewRequest(report))
				if err != nil || ack.Msg.Generation <= report.Generation {
					t.Fatalf("obsolete receipt blocked polling: %+v, %v", ack, err)
				}
				var rootCounter int64
				if err := database.QueryRowContext(ctx, `SELECT last_root_action_counter FROM agent_updater_identities WHERE agent_id=?`, agent.ID).Scan(&rootCounter); err != nil {
					t.Fatal(err)
				}
				if rootCounter != 0 || !app.isAgentUpdateCordoned(agent.ID) {
					t.Fatal("obsolete receipt was treated as recovery evidence")
				}
				check.Counter = 4
				check.Signature = ed25519.Sign(updaterPrivate, agentupdateauth.CheckPayload(check.AgentPublicId, check.Counter))
				response, err = app.CheckAgentUpdate(ctx, connect.NewRequest(check))
				if err != nil || response.Msg.Authorization == nil || response.Msg.DesiredAction != p2pstreamv1.AgentUpdateDesiredAction_AGENT_UPDATE_DESIRED_ACTION_ROLLBACK {
					t.Fatalf("new rollback was not issued: %+v, %v", response, err)
				}
				report.Generation, report.Counter = response.Msg.Generation, 5
				report.RootActionReceipt = newAgentUpdateTestRootActionReceipt(t, app, agent.ID, activatorPrivate, agentupdateauth.AssignmentActionRollback, 2)
			}
			signAgentUpdateTestReport(report, updaterPrivate)
			if _, err := app.ReportAgentUpdate(ctx, connect.NewRequest(report)); err != nil {
				t.Fatalf("cancelled blocked assignment rejected recovery result: %v", err)
			}
			if !app.isAgentUpdateCordoned(agent.ID) {
				t.Fatal("recovery result cleared the fence before a fresh tunnel")
			}
			if tc.rollbackFails {
				assertAgentUpdateAssignmentState(t, database, assignmentID, "blocked")
				retry := connect.NewRequest(&p2pstreamv1.RetryAgentUpdateAssignmentsRequest{CampaignId: campaignID, ExpectedCampaignGeneration: 3, AssignmentIds: []int64{assignmentID}})
				retry.Header().Set("Cookie", header.Get("Cookie"))
				if _, err := app.RetryAgentUpdateAssignments(ctx, retry); err != nil {
					t.Fatalf("recovery could not be retried: %v", err)
				}
				return
			}

			assertAgentUpdateAssignmentState(t, database, assignmentID, "awaiting_tunnel")
			// Lost responses must be retryable with the same signed root receipt,
			// including the incomplete envelope sent by older pinned workers.
			for _, counter := range []uint64{report.Counter, report.Counter + 1} {
				report.Counter = counter
				signAgentUpdateTestReport(report, updaterPrivate)
				if _, err := app.ReportAgentUpdate(ctx, connect.NewRequest(report)); err != nil {
					t.Fatalf("rollback receipt retry at counter %d: %v", counter, err)
				}
			}
			mismatch := proto.Clone(report).(*p2pstreamv1.ReportAgentUpdateRequest)
			mismatch.RunningVersion = "v9.9.9"
			signAgentUpdateTestReport(mismatch, updaterPrivate)
			if _, err := app.ReportAgentUpdate(ctx, connect.NewRequest(mismatch)); connect.CodeOf(err) != connect.CodeUnauthenticated {
				t.Fatalf("mismatched retry accepted: %v", err)
			}
			var workerCounter, rootCounter int64
			var storedVersion, storedCommit, storedBinary string
			if err := database.QueryRowContext(ctx, `SELECT i.last_counter,i.last_root_action_counter,x.running_version,x.running_commit,x.attested_binary_sha256 FROM agent_update_assignments x JOIN agent_updater_identities i ON i.agent_id=x.agent_id WHERE x.id=?`, assignmentID).Scan(&workerCounter, &rootCounter, &storedVersion, &storedCommit, &storedBinary); err != nil {
				t.Fatal(err)
			}
			if workerCounter != int64(report.Counter) || rootCounter != int64(report.RootActionReceipt.RootActionCounter) || storedVersion != report.RootActionReceipt.ResultVersion || storedCommit != report.RootActionReceipt.ResultCommit || storedBinary != report.RootActionReceipt.ResultArtifactSha256 {
				t.Fatalf("receipt results/counters were not preserved: %d/%d %q/%q/%q", workerCounter, rootCounter, storedVersion, storedCommit, storedBinary)
			}
			var completedAt time.Time
			if err := database.QueryRowContext(ctx, `SELECT root_action_completed_at FROM agent_update_assignments WHERE id=?`, assignmentID).Scan(&completedAt); err != nil {
				t.Fatal(err)
			}
			conn := &AgentConn{AgentID: agent.ID, PublicID: agent.PublicID, Done: make(chan struct{}), ConnectedAt: completedAt.Add(time.Millisecond)}
			if err := app.AgentHub.connect(conn); err != nil {
				t.Fatal(err)
			}
			app.recordAgentUpdateFreshTunnel(conn)
			assertAgentUpdateAssignmentState(t, database, assignmentID, "failed")
			preview, err = app.previewAgentUpdateAgents(ctx, []int64{agent.ID}, response.Msg.Target, 1)
			if err != nil || !preview[0].Eligible || app.isAgentUpdateCordoned(agent.ID) {
				t.Fatalf("verified recovery still prevents a new rollout: preview=%+v err=%v", preview, err)
			}
		})
	}
}
