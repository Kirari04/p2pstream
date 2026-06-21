package db

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestListConnectionsSinceReturnsOverlappingSessions(t *testing.T) {
	ctx := context.Background()
	database, err := Open(filepath.Join(t.TempDir(), "connections-overlap.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer func() { _ = database.Close() }()

	agent, err := database.CreateAgent(ctx, CreateAgentParams{
		PublicID:  "agent-overlap",
		Name:      "agent-overlap",
		TokenHash: "hash",
		Enabled:   1,
	})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}

	now := time.Unix(1_800_000_000, 0).UTC()
	since := now.Add(-1 * time.Hour)
	insertConnectionRow := func(id int64, connectedAt time.Time, disconnectedAt sql.NullTime) {
		t.Helper()
		if _, err := database.ExecContext(ctx, `
			INSERT INTO connections (id, agent_id, connected_at, disconnected_at)
			VALUES (?, ?, ?, ?)`,
			id,
			agent.ID,
			connectedAt,
			disconnectedAt,
		); err != nil {
			t.Fatalf("insert connection %d: %v", id, err)
		}
	}
	insertConnectionRow(1, now.Add(-3*time.Hour), sql.NullTime{Time: now.Add(-2 * time.Hour), Valid: true})
	insertConnectionRow(2, now.Add(-2*time.Hour), sql.NullTime{Time: now.Add(-30 * time.Minute), Valid: true})
	insertConnectionRow(3, now.Add(-45*time.Minute), sql.NullTime{Time: now.Add(-15 * time.Minute), Valid: true})
	insertConnectionRow(4, now.Add(-90*time.Minute), sql.NullTime{})

	rows, err := database.ListConnectionsSince(ctx, ListConnectionsSinceParams{
		ConnectedAt:    since,
		DisconnectedAt: sql.NullTime{Time: since, Valid: true},
	})
	if err != nil {
		t.Fatalf("list connections since: %v", err)
	}

	got := make([]int64, 0, len(rows))
	for _, row := range rows {
		got = append(got, row.ID)
		if row.AgentPublicID != "agent-overlap" || row.AgentName != "agent-overlap" {
			t.Fatalf("unexpected agent labels for row %d: public=%q name=%q", row.ID, row.AgentPublicID, row.AgentName)
		}
	}
	want := []int64{2, 4, 3}
	if len(got) != len(want) {
		t.Fatalf("connection ids = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("connection ids = %v, want %v", got, want)
		}
	}
}
