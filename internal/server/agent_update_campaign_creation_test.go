package server

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
)

type campaignCreationTestCatalog struct {
	target *p2pstreamv1.AgentUpdateTarget
}

func (c campaignCreationTestCatalog) ListTrustedAgentUpdateTargets(context.Context) ([]*p2pstreamv1.AgentUpdateTarget, error) {
	return []*p2pstreamv1.AgentUpdateTarget{c.target}, nil
}

func (c campaignCreationTestCatalog) ResolveTrustedAgentUpdateTarget(_ context.Context, digest string) (*p2pstreamv1.AgentUpdateTarget, error) {
	if digest != c.target.ManifestSha256 {
		return nil, fmt.Errorf("unknown manifest")
	}
	return c.target, nil
}

func TestCreateAgentUpdateCampaignPersistsPreviewedPlan(t *testing.T) {
	for _, remote := range []bool{false, true} {
		t.Run(fmt.Sprintf("remote_token=%t", remote), func(t *testing.T) {
			ctx := context.Background()
			database := newServerTestDB(t)
			app := newAgentUpdateTestApp(t, database)
			header := createTestAdminSession(t, app)
			if remote {
				req := connect.NewRequest(&p2pstreamv1.CreateManagementAccessTokenRequest{Name: "remote-admin", Enabled: true})
				req.Header().Set("Cookie", header.Get("Cookie"))
				token, err := app.CreateManagementAccessToken(ctx, req)
				if err != nil {
					t.Fatal(err)
				}
				header = http.Header{"Authorization": {"Bearer " + token.Msg.Token}}
			}
			user, err := app.requireAdmin(ctx, header)
			if err != nil {
				t.Fatal(err)
			}
			target := &p2pstreamv1.AgentUpdateTarget{
				Version: "v1.2.3", Commit: strings.Repeat("c", 40), ManifestSha256: strings.Repeat("a", 64),
				ReleaseSequence: 12, SecurityEpoch: 2, MinimumUpdaterVersion: "v1.0.0", MinimumTunnelProtocol: 1, MaximumTunnelProtocol: 1,
				Artifacts: []*p2pstreamv1.AgentUpdateArtifact{{Os: "linux", Arch: "amd64", Name: "p2pstream_v1.2.3_linux_amd64", SizeBytes: 1234, Sha256: strings.Repeat("b", 64)}},
			}
			app.TrustedAgentUpdates = campaignCreationTestCatalog{target}
			ids := make([]int64, 0, 2)
			for i := 0; i < 2; i++ {
				agent := createAgentUpdateTestAgent(t, database, fmt.Sprintf("campaign-agent-%d", i))
				updater, _, _ := ed25519.GenerateKey(rand.Reader)
				activator, _, _ := ed25519.GenerateKey(rand.Reader)
				insertAgentUpdateIdentity(t, app, database, agent.ID, updater, activator)
				if err := app.AgentHub.connect(&AgentConn{AgentID: agent.ID, PublicID: agent.PublicID, Done: make(chan struct{}), ConnectedAt: time.Now().UTC()}); err != nil {
					t.Fatal(err)
				}
				ids = append(ids, agent.ID)
			}
			policy := &p2pstreamv1.AgentUpdatePolicy{MaxUnavailable: 2, MinimumEligibleAgentsPerRoute: 1, CanaryCount: 1, WaveSize: 2, HealthyDwellMillis: 120000}
			// The client supplies only the catalog digest; persisted release data must
			// come from the trusted catalog, just as it does for a remote UI request.
			requested := &p2pstreamv1.AgentUpdateTarget{ManifestSha256: target.ManifestSha256}
			previewReq := connect.NewRequest(&p2pstreamv1.PreviewAgentUpdateCampaignRequest{AgentIds: ids, Target: requested, Policy: policy})
			for key, values := range header {
				previewReq.Header()[key] = values
			}
			preview, err := app.PreviewAgentUpdateCampaign(ctx, previewReq)
			if err != nil || preview.Msg.EligibleCount != 2 || preview.Msg.BlockedCount != 0 {
				t.Fatalf("preview = %v, %v", preview, err)
			}
			req := connect.NewRequest(&p2pstreamv1.CreateAgentUpdateCampaignRequest{Name: "  Fleet upgrade  ", AgentIds: ids, Target: requested, Policy: policy})
			for key, values := range header {
				req.Header()[key] = values
			}
			response, err := app.CreateAgentUpdateCampaign(ctx, req)
			if err != nil {
				t.Fatalf("create previewed campaign: %v", err)
			}
			campaign := response.Msg.Campaign
			if campaign.Id <= 0 || campaign.Name != "Fleet upgrade" || campaign.Generation != 1 || campaign.State != p2pstreamv1.AgentUpdateCampaignState_AGENT_UPDATE_CAMPAIGN_STATE_RUNNING || !proto.Equal(campaign.Target, target) || !proto.Equal(campaign.Policy, policy) {
				t.Fatalf("persisted campaign = %v", campaign)
			}
			var createdBy sql.NullInt64
			if err := database.QueryRow(`SELECT created_by_user_id FROM agent_update_campaigns WHERE id=?`, campaign.Id).Scan(&createdBy); err != nil {
				t.Fatal(err)
			}
			if createdBy.Valid != !remote || createdBy.Int64 != user.ID {
				t.Fatalf("created_by = %v, remote=%t user=%d", createdBy, remote, user.ID)
			}
			if len(campaign.Assignments) != len(ids) {
				t.Fatalf("assignments = %v", campaign.Assignments)
			}
			for i, assignment := range campaign.Assignments {
				wantAction := p2pstreamv1.AgentUpdateDesiredAction_AGENT_UPDATE_DESIRED_ACTION_NONE
				if i == 0 {
					wantAction = p2pstreamv1.AgentUpdateDesiredAction_AGENT_UPDATE_DESIRED_ACTION_STAGE
				}
				if assignment.AgentId != ids[i] || assignment.CampaignId != campaign.Id || assignment.Generation != 1 || assignment.State != p2pstreamv1.AgentUpdateAssignmentState_AGENT_UPDATE_ASSIGNMENT_STATE_PENDING || assignment.DesiredAction != wantAction {
					t.Fatalf("assignment %d = %v", i, assignment)
				}
			}
			// Starting again must recheck assignments instead of duplicating the plan.
			if _, err := app.CreateAgentUpdateCampaign(ctx, req); connect.CodeOf(err) != connect.CodeFailedPrecondition {
				t.Fatalf("duplicate campaign = %v", err)
			}
			var count int
			if err := database.QueryRow(`SELECT COUNT(*) FROM agent_update_campaigns`).Scan(&count); err != nil || count != 1 {
				t.Fatalf("campaign count = %d, %v", count, err)
			}
		})
	}
}
