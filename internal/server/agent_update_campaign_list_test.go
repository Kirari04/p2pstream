package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/internal/db"
)

func insertAgentUpdateListTestCampaign(t *testing.T, database *db.DB, name, state, version, commit string) int64 {
	t.Helper()
	artifactJSON, _ := json.Marshal([]*p2pstreamv1.AgentUpdateArtifact{{Os: "linux", Arch: "amd64", Name: "p2pstream_" + version + "_linux_amd64", SizeBytes: 1234, Sha256: strings.Repeat("b", 64)}})
	now := time.Now().UTC()
	result, err := database.Exec(`INSERT INTO agent_update_campaigns (name,state,generation,target_version,target_commit,manifest_sha256,release_sequence,security_epoch,minimum_updater_version,minimum_tunnel_protocol,maximum_tunnel_protocol,artifacts_json,max_unavailable,minimum_eligible_agents_per_route,canary_count,wave_size,healthy_dwell_millis,created_at,updated_at) VALUES (?,?,1,?,?,?,12,2,'v1.0.0',1,1,?,1,1,1,1,10000,?,?)`,
		name, state, version, commit, strings.Repeat("a", 64), string(artifactJSON), now, now)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := result.LastInsertId()
	return id
}

type agentUpdateCampaignListHarness struct {
	t      *testing.T
	app    *App
	cookie string
}

func (h agentUpdateCampaignListHarness) list(msg *p2pstreamv1.ListAgentUpdateCampaignsRequest) (*p2pstreamv1.ListAgentUpdateCampaignsResponse, error) {
	h.t.Helper()
	req := connect.NewRequest(msg)
	req.Header().Set("Cookie", h.cookie)
	resp, err := h.app.ListAgentUpdateCampaigns(context.Background(), req)
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

func (h agentUpdateCampaignListHarness) mustList(msg *p2pstreamv1.ListAgentUpdateCampaignsRequest) *p2pstreamv1.ListAgentUpdateCampaignsResponse {
	h.t.Helper()
	resp, err := h.list(msg)
	if err != nil {
		h.t.Fatalf("ListAgentUpdateCampaigns(%v): %v", msg, err)
	}
	return resp
}

func newAgentUpdateCampaignListHarness(t *testing.T) (agentUpdateCampaignListHarness, *db.DB) {
	t.Helper()
	database := newServerTestDB(t)
	app := newAgentUpdateTestApp(t, database)
	return agentUpdateCampaignListHarness{t: t, app: app, cookie: createTestAdminSession(t, app).Get("Cookie")}, database
}

func campaignIDs(campaigns []*p2pstreamv1.AgentUpdateCampaign) []int64 {
	ids := make([]int64, 0, len(campaigns))
	for _, c := range campaigns {
		ids = append(ids, c.Id)
	}
	return ids
}

func equalIDs(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestListAgentUpdateCampaignsLimitOnlyKeepsLegacyBehaviour(t *testing.T) {
	h, database := newAgentUpdateCampaignListHarness(t)
	agent := createAgentUpdateTestAgent(t, database, "list-legacy")
	// Oldest campaign is cancelled but still holds a cordoned assignment, so it
	// must lead the list ahead of newer completed history.
	recoveryID, _ := insertAgentUpdateTestCampaign(t, database, agent.ID, "failed", "none", true)
	if _, err := database.Exec(`UPDATE agent_update_campaigns SET state='cancelled' WHERE id=?`, recoveryID); err != nil {
		t.Fatal(err)
	}
	var completed []int64
	for i := 0; i < 60; i++ {
		completed = append(completed, insertAgentUpdateListTestCampaign(t, database, "done", "completed", "v1.0.0", strings.Repeat("d", 40)))
	}
	runningID := insertAgentUpdateListTestCampaign(t, database, "live", "running", "v1.0.1", strings.Repeat("e", 40))

	resp := h.mustList(&p2pstreamv1.ListAgentUpdateCampaignsRequest{})
	if len(resp.Campaigns) != 50 || resp.TotalCount != 62 {
		t.Fatalf("default list = %d campaigns, total %d; want 50 of 62", len(resp.Campaigns), resp.TotalCount)
	}
	if resp.Campaigns[0].Id != runningID || resp.Campaigns[1].Id != recoveryID || resp.Campaigns[2].Id != completed[len(completed)-1] {
		t.Fatalf("leading campaigns = %v, want running %d, recovery %d, newest completed %d", campaignIDs(resp.Campaigns[:3]), runningID, recoveryID, completed[len(completed)-1])
	}
	if len(resp.Campaigns[1].Assignments) != 1 || resp.Campaigns[1].Assignments[0].AgentPublicId != agent.PublicID {
		t.Fatalf("recovery campaign assignments = %v", resp.Campaigns[1].Assignments)
	}
	single, err := h.app.getAgentUpdateCampaignProto(context.Background(), recoveryID)
	if err != nil {
		t.Fatal(err)
	}
	if single.String() != resp.Campaigns[1].String() {
		t.Fatalf("listed campaign differs from single lookup:\n%v\n%v", resp.Campaigns[1], single)
	}
	for i := 3; i < len(resp.Campaigns); i++ {
		if resp.Campaigns[i].Id >= resp.Campaigns[i-1].Id {
			t.Fatalf("history not newest first at %d: %v", i, campaignIDs(resp.Campaigns))
		}
	}
	if resp := h.mustList(&p2pstreamv1.ListAgentUpdateCampaignsRequest{Limit: 100}); len(resp.Campaigns) != 62 || resp.TotalCount != 62 {
		t.Fatalf("limit 100 = %d campaigns, total %d", len(resp.Campaigns), resp.TotalCount)
	}
	if resp := h.mustList(&p2pstreamv1.ListAgentUpdateCampaignsRequest{Limit: 1}); len(resp.Campaigns) != 1 || resp.Campaigns[0].Id != runningID {
		t.Fatalf("limit 1 = %v", campaignIDs(resp.Campaigns))
	}
}

func TestListAgentUpdateCampaignsFiltersAndPages(t *testing.T) {
	h, database := newAgentUpdateCampaignListHarness(t)
	running := insertAgentUpdateListTestCampaign(t, database, "Edge rollout", "running", "v2.0.0", strings.Repeat("a", 40))
	paused := insertAgentUpdateListTestCampaign(t, database, "Core rollout", "paused", "v2.0.0", strings.Repeat("b", 40))
	var history []int64
	for i := 0; i < 7; i++ {
		state := "completed"
		if i%3 == 0 {
			state = "cancelled"
		}
		history = append(history, insertAgentUpdateListTestCampaign(t, database, "Nightly", state, "v1.9.0", strings.Repeat("c", 40)))
	}

	active := h.mustList(&p2pstreamv1.ListAgentUpdateCampaignsRequest{States: []p2pstreamv1.AgentUpdateCampaignState{
		p2pstreamv1.AgentUpdateCampaignState_AGENT_UPDATE_CAMPAIGN_STATE_RUNNING,
		p2pstreamv1.AgentUpdateCampaignState_AGENT_UPDATE_CAMPAIGN_STATE_PAUSED,
		p2pstreamv1.AgentUpdateCampaignState_AGENT_UPDATE_CAMPAIGN_STATE_PAUSED,
	}})
	if !equalIDs(campaignIDs(active.Campaigns), []int64{paused, running}) || active.TotalCount != 2 {
		t.Fatalf("active = %v total %d, want [%d %d] total 2", campaignIDs(active.Campaigns), active.TotalCount, paused, running)
	}

	finished := []p2pstreamv1.AgentUpdateCampaignState{
		p2pstreamv1.AgentUpdateCampaignState_AGENT_UPDATE_CAMPAIGN_STATE_COMPLETED,
		p2pstreamv1.AgentUpdateCampaignState_AGENT_UPDATE_CAMPAIGN_STATE_CANCELLED,
	}
	var paged []int64
	for offset := int64(0); offset < 9; offset += 3 {
		page := h.mustList(&p2pstreamv1.ListAgentUpdateCampaignsRequest{Limit: 3, Offset: offset, States: finished})
		if page.TotalCount != 7 {
			t.Fatalf("offset %d total = %d, want 7", offset, page.TotalCount)
		}
		paged = append(paged, campaignIDs(page.Campaigns)...)
	}
	wantHistory := make([]int64, 0, len(history))
	for i := len(history) - 1; i >= 0; i-- {
		wantHistory = append(wantHistory, history[i])
	}
	if !equalIDs(paged, wantHistory) {
		t.Fatalf("paged history = %v, want %v", paged, wantHistory)
	}
	beyond := h.mustList(&p2pstreamv1.ListAgentUpdateCampaignsRequest{Limit: 3, Offset: 50, States: finished})
	if len(beyond.Campaigns) != 0 || beyond.TotalCount != 7 {
		t.Fatalf("offset past end = %v total %d", campaignIDs(beyond.Campaigns), beyond.TotalCount)
	}

	cancelled := h.mustList(&p2pstreamv1.ListAgentUpdateCampaignsRequest{States: []p2pstreamv1.AgentUpdateCampaignState{p2pstreamv1.AgentUpdateCampaignState_AGENT_UPDATE_CAMPAIGN_STATE_CANCELLED}})
	if !equalIDs(campaignIDs(cancelled.Campaigns), []int64{history[6], history[3], history[0]}) || cancelled.TotalCount != 3 {
		t.Fatalf("cancelled = %v total %d", campaignIDs(cancelled.Campaigns), cancelled.TotalCount)
	}

	for _, tc := range []struct {
		query string
		want  []int64
	}{
		{"rollout", []int64{paused, running}},
		{"  EDGE  ", []int64{running}},
		{"V2.0", []int64{paused, running}},
		{strings.Repeat("B", 12), []int64{paused}},
		{"no such campaign", nil},
	} {
		resp := h.mustList(&p2pstreamv1.ListAgentUpdateCampaignsRequest{Query: tc.query})
		if !equalIDs(campaignIDs(resp.Campaigns), tc.want) || resp.TotalCount != int64(len(tc.want)) {
			t.Fatalf("query %q = %v total %d, want %v", tc.query, campaignIDs(resp.Campaigns), resp.TotalCount, tc.want)
		}
	}

	combined := h.mustList(&p2pstreamv1.ListAgentUpdateCampaignsRequest{Query: "nightly", States: finished, Limit: 2, Offset: 1})
	if !equalIDs(campaignIDs(combined.Campaigns), wantHistory[1:3]) || combined.TotalCount != 7 {
		t.Fatalf("query+states+paging = %v total %d", campaignIDs(combined.Campaigns), combined.TotalCount)
	}
}

func TestListAgentUpdateCampaignsQueryIsLiteral(t *testing.T) {
	h, database := newAgentUpdateCampaignListHarness(t)
	percent := insertAgentUpdateListTestCampaign(t, database, "100% fleet", "completed", "v1.0.0", strings.Repeat("a", 40))
	insertAgentUpdateListTestCampaign(t, database, "1000 fleet", "completed", "v1.0.0", strings.Repeat("a", 40))
	underscore := insertAgentUpdateListTestCampaign(t, database, "edge_eu", "completed", "v1.0.0", strings.Repeat("a", 40))
	insertAgentUpdateListTestCampaign(t, database, "edgeXeu", "completed", "v1.0.0", strings.Repeat("a", 40))
	backslash := insertAgentUpdateListTestCampaign(t, database, `ops\batch`, "completed", "v1.0.0", strings.Repeat("a", 40))

	for _, tc := range []struct {
		query string
		want  []int64
	}{
		{"%", []int64{percent}},
		{"100%", []int64{percent}},
		{"_", []int64{underscore}},
		{"edge_", []int64{underscore}},
		{`\`, []int64{backslash}},
		{`\%`, nil},
		{"'", nil},
		{"*", nil},
	} {
		resp := h.mustList(&p2pstreamv1.ListAgentUpdateCampaignsRequest{Query: tc.query})
		if !equalIDs(campaignIDs(resp.Campaigns), tc.want) || resp.TotalCount != int64(len(tc.want)) {
			t.Fatalf("query %q = %v total %d, want %v", tc.query, campaignIDs(resp.Campaigns), resp.TotalCount, tc.want)
		}
	}
}

func TestListAgentUpdateCampaignsValidatesInput(t *testing.T) {
	h, database := newAgentUpdateCampaignListHarness(t)
	insertAgentUpdateListTestCampaign(t, database, "only", "completed", "v1.0.0", strings.Repeat("a", 40))

	for name, msg := range map[string]*p2pstreamv1.ListAgentUpdateCampaignsRequest{
		"negative limit":    {Limit: -1},
		"limit too large":   {Limit: 101},
		"negative offset":   {Offset: -1},
		"query too long":    {Query: strings.Repeat("x", agentUpdateCampaignListMaxQuery+1)},
		"unspecified state": {States: []p2pstreamv1.AgentUpdateCampaignState{p2pstreamv1.AgentUpdateCampaignState_AGENT_UPDATE_CAMPAIGN_STATE_UNSPECIFIED}},
		"unknown state":     {States: []p2pstreamv1.AgentUpdateCampaignState{p2pstreamv1.AgentUpdateCampaignState(99)}},
		"invalid utf8":      {Query: "\xff"},
	} {
		if _, err := h.list(msg); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("%s: code = %s, want invalid argument: %v", name, connect.CodeOf(err), err)
		}
	}
	if resp := h.mustList(&p2pstreamv1.ListAgentUpdateCampaignsRequest{Query: strings.Repeat("x", agentUpdateCampaignListMaxQuery)}); resp.TotalCount != 0 {
		t.Fatalf("max-length query total = %d", resp.TotalCount)
	}
	// Whitespace padding does not count toward the limit.
	if _, err := h.list(&p2pstreamv1.ListAgentUpdateCampaignsRequest{Query: "  " + strings.Repeat("x", agentUpdateCampaignListMaxQuery) + "  "}); err != nil {
		t.Fatalf("padded max-length query: %v", err)
	}

	unauthenticated := connect.NewRequest(&p2pstreamv1.ListAgentUpdateCampaignsRequest{})
	if _, err := h.app.ListAgentUpdateCampaigns(context.Background(), unauthenticated); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("unauthenticated code = %s", connect.CodeOf(err))
	}
}
