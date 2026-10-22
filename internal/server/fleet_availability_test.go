package server

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/internal/config"
)

func TestListAgentAvailabilityMatchesSingleAgentAvailability(t *testing.T) {
	ctx := context.Background()
	app := NewApp(&config.Config{ObservabilityRetentionDays: 30}, newServerTestDB(t))
	now := time.Unix(1_800_000_000, 0).UTC()

	old := createDashboardUptimeAgent(t, app.DB, "fleet-old", now.Add(-60*24*time.Hour))
	insertDashboardConnection(t, app.DB, old.ID, now.Add(-40*24*time.Hour), sql.NullTime{Time: now.Add(-29 * 24 * time.Hour), Valid: true})
	insertDashboardConnection(t, app.DB, old.ID, now.Add(-23*time.Hour), sql.NullTime{Time: now.Add(-20 * time.Hour), Valid: true})
	insertDashboardConnection(t, app.DB, old.ID, now.Add(-21*time.Hour), sql.NullTime{Time: now.Add(-19 * time.Hour), Valid: true})
	insertDashboardConnection(t, app.DB, old.ID, now.Add(-2*time.Hour), sql.NullTime{})
	active := testAgentConn(old.ID, old.PublicID)
	active.ConnectedAt = now.Add(-2 * time.Hour)
	if err := app.AgentHub.connect(active); err != nil {
		t.Fatalf("connect active agent: %v", err)
	}

	// Created inside every window. Its first row ends before its own creation
	// but after the fleet window start, so only the per-agent clip removes it.
	young := createDashboardUptimeAgent(t, app.DB, "fleet-young", now.Add(-3*time.Hour))
	insertDashboardConnection(t, app.DB, young.ID, now.Add(-5*time.Hour), sql.NullTime{Time: now.Add(-4 * time.Hour), Valid: true})
	insertDashboardConnection(t, app.DB, young.ID, now.Add(-4*time.Hour), sql.NullTime{Time: now.Add(-150 * time.Minute), Valid: true})
	insertDashboardConnection(t, app.DB, young.ID, now.Add(-60*time.Minute), sql.NullTime{Time: now.Add(-30 * time.Minute), Valid: true})

	idle := createDashboardUptimeAgent(t, app.DB, "fleet-idle", now.Add(-10*24*time.Hour))
	if _, err := app.DB.ExecContext(ctx, `UPDATE agents SET enabled = 0 WHERE id = ?`, idle.ID); err != nil {
		t.Fatal(err)
	}

	agents, err := app.DB.ListAgents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, label := range []string{"24h", "7d", "30d"} {
		window := agentAvailabilityWindows[label]
		fleet, err := app.fleetAgentAvailability(ctx, label, window, now)
		if err != nil {
			t.Fatalf("%s fleet availability: %v", label, err)
		}
		if fleet.WindowLabel != label || fleet.WindowUntilUnixMillis != now.UnixMilli() || fleet.WindowSinceUnixMillis != now.Add(-window).UnixMilli() {
			t.Fatalf("%s window = %q %d..%d", label, fleet.WindowLabel, fleet.WindowSinceUnixMillis, fleet.WindowUntilUnixMillis)
		}
		if len(fleet.Agents) != len(agents) {
			t.Fatalf("%s fleet agents = %d, want %d", label, len(fleet.Agents), len(agents))
		}
		for i, agent := range agents {
			single, err := app.agentAvailability(ctx, agent, label, window, now)
			if err != nil {
				t.Fatal(err)
			}
			got := fleet.Agents[i]
			want := &p2pstreamv1.AgentAvailabilitySummary{
				AgentPublicId:           single.AgentPublicId,
				AgentName:               single.AgentName,
				ObservedSinceUnixMillis: single.ObservedSinceUnixMillis,
				ObservedUntilUnixMillis: single.ObservedUntilUnixMillis,
				UptimeMillis:            single.UptimeMillis,
				DowntimeMillis:          single.DowntimeMillis,
				UptimePercent:           single.UptimePercent,
				DisconnectCount:         single.DisconnectCount,
				LongestDowntimeMillis:   single.LongestDowntimeMillis,
				Connected:               single.Connected,
				Intervals:               single.Intervals,
				Enabled:                 agent.Enabled != 0,
			}
			if !proto.Equal(got, want) {
				t.Fatalf("%s agent %s fleet entry differs from single-agent RPC:\n got %v\nwant %v", label, agent.PublicID, got, want)
			}
		}
	}

	// Sanity-check the fixture actually exercises the interesting paths.
	fleet, err := app.fleetAgentAvailability(ctx, "24h", 24*time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]*p2pstreamv1.AgentAvailabilitySummary{}
	for _, entry := range fleet.Agents {
		byID[entry.AgentPublicId] = entry
	}
	if entry := byID["fleet-old"]; !entry.Connected || len(entry.Intervals) != 2 || !entry.Intervals[1].Active || entry.DisconnectCount != 2 {
		t.Fatalf("old agent entry = %v", entry)
	}
	if entry := byID["fleet-young"]; entry.ObservedSinceUnixMillis != now.Add(-3*time.Hour).UnixMilli() || entry.UptimeMillis != int64((60*time.Minute).Milliseconds()) || entry.DisconnectCount != 2 {
		t.Fatalf("young agent entry = %v", entry)
	}
	if entry := byID["fleet-idle"]; entry.Enabled || entry.UptimeMillis != 0 || len(entry.Intervals) != 0 || entry.UptimePercent != 0 {
		t.Fatalf("idle agent entry = %v", entry)
	}
}

func TestListAgentAvailabilityEmptyFleet(t *testing.T) {
	ctx := context.Background()
	app := NewApp(&config.Config{ObservabilityRetentionDays: 30}, newServerTestDB(t))
	header := createTestAdminSession(t, app)

	req := connect.NewRequest(&p2pstreamv1.ListAgentAvailabilityRequest{})
	req.Header().Set("Cookie", header.Get("Cookie"))
	resp, err := app.ListAgentAvailability(ctx, req)
	if err != nil {
		t.Fatalf("ListAgentAvailability: %v", err)
	}
	if resp.Msg.WindowLabel != "24h" || len(resp.Msg.Agents) != 0 {
		t.Fatalf("response = %v, want default 24h window and no agents", resp.Msg)
	}
	if span := resp.Msg.WindowUntilUnixMillis - resp.Msg.WindowSinceUnixMillis; span != int64((24 * time.Hour).Milliseconds()) {
		t.Fatalf("window span = %d, want 24h", span)
	}
}

func TestListAgentAvailabilityRequiresAuthAndValidWindow(t *testing.T) {
	ctx := context.Background()
	app := NewApp(nil, newServerTestDB(t))

	unauthenticated := connect.NewRequest(&p2pstreamv1.ListAgentAvailabilityRequest{WindowLabel: "24h"})
	if _, err := app.ListAgentAvailability(ctx, unauthenticated); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("unauthenticated code = %s, want unauthenticated: %v", connect.CodeOf(err), err)
	}

	header := createTestAdminSession(t, app)
	for _, label := range []string{"forever", "1h", "24H", "5m"} {
		invalid := connect.NewRequest(&p2pstreamv1.ListAgentAvailabilityRequest{WindowLabel: label})
		invalid.Header().Set("Cookie", header.Get("Cookie"))
		if _, err := app.ListAgentAvailability(ctx, invalid); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("window %q code = %s, want invalid argument: %v", label, connect.CodeOf(err), err)
		}
	}
	for _, label := range []string{"24h", " 7d ", "30d"} {
		valid := connect.NewRequest(&p2pstreamv1.ListAgentAvailabilityRequest{WindowLabel: label})
		valid.Header().Set("Cookie", header.Get("Cookie"))
		if _, err := app.ListAgentAvailability(ctx, valid); err != nil {
			t.Fatalf("window %q: %v", label, err)
		}
	}
	if _, ok := allowedEnvironmentProxyMethods["ListAgentAvailability"]; !ok {
		t.Fatal("ListAgentAvailability must be proxyable like GetAgentAvailability")
	}
}

func TestListAgentAvailabilityCapsIntervalsPerAgent(t *testing.T) {
	ctx := context.Background()
	app := NewApp(&config.Config{ObservabilityRetentionDays: 30}, newServerTestDB(t))
	now := time.Unix(1_800_000_000, 0).UTC()
	agent := createDashboardUptimeAgent(t, app.DB, "fleet-flappy", now.Add(-48*time.Hour))
	total := agentAvailabilityFleetMaxIntervals + 5
	start := now.Add(-time.Duration(total) * time.Minute)
	for i := 0; i < total; i++ {
		connected := start.Add(time.Duration(i) * time.Minute)
		insertDashboardConnection(t, app.DB, agent.ID, connected, sql.NullTime{Time: connected.Add(30 * time.Second), Valid: true})
	}

	single, err := app.agentAvailability(ctx, agent, "24h", 24*time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	fleet, err := app.fleetAgentAvailability(ctx, "24h", 24*time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	entry := fleet.Agents[0]
	if len(single.Intervals) != total {
		t.Fatalf("single intervals = %d, want %d", len(single.Intervals), total)
	}
	if !entry.IntervalsTruncated || len(entry.Intervals) != agentAvailabilityFleetMaxIntervals {
		t.Fatalf("fleet intervals = %d truncated=%v, want %d truncated", len(entry.Intervals), entry.IntervalsTruncated, agentAvailabilityFleetMaxIntervals)
	}
	if !proto.Equal(entry.Intervals[len(entry.Intervals)-1], single.Intervals[len(single.Intervals)-1]) ||
		!proto.Equal(entry.Intervals[0], single.Intervals[5]) {
		t.Fatal("fleet truncation must keep the newest intervals")
	}
	if entry.UptimeMillis != single.UptimeMillis || entry.DisconnectCount != single.DisconnectCount {
		t.Fatalf("fleet totals %d/%d differ from single %d/%d", entry.UptimeMillis, entry.DisconnectCount, single.UptimeMillis, single.DisconnectCount)
	}
}
