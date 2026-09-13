package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"connectrpc.com/connect"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/internal/db"
)

func TestDeleteAgentPreservesHistory(t *testing.T) {
	for _, blocked := range []bool{false, true} {
		t.Run(fmt.Sprintf("blocked=%t", blocked), func(t *testing.T) {
			ctx := context.Background()
			database := newAgentRegistryTestDB(t)
			app := NewApp(nil, database)
			header := createTestAdminSession(t, app)
			agent := createAgentRegistryTestAgent(t, database, "old-agent", "Old Agent", "old-token")
			other := createAgentRegistryTestAgent(t, database, "other-agent", "Other Agent", "other-token")
			seedAgentDeletionHistory(t, database, agent.ID, other.ID)
			if _, err := database.Exec(`UPDATE agents SET enabled = 0 WHERE id = ?`, agent.ID); err != nil {
				t.Fatal(err)
			}
			if blocked {
				// A failure at the final DELETE must roll back all history changes.
				if _, err := database.Exec(`CREATE TABLE deletion_blocker (agent_id INTEGER REFERENCES agents(id));
					INSERT INTO deletion_blocker (agent_id) VALUES (?)`, agent.ID); err != nil {
					t.Fatal(err)
				}
			}
			req := connect.NewRequest(&p2pstreamv1.DeleteAgentRequest{Id: agent.ID})
			req.Header().Set("Cookie", header.Get("Cookie"))
			_, err := app.DeleteAgent(ctx, req)
			if blocked {
				if connect.CodeOf(err) != connect.CodeFailedPrecondition {
					t.Fatalf("blocked deletion = %v, want failed_precondition", err)
				}
				if _, err := database.GetAgent(ctx, agent.ID); err != nil {
					t.Fatalf("blocked deletion removed agent: %v", err)
				}
			} else {
				if err != nil {
					t.Fatalf("delete agent with history: %v", err)
				}
				if _, err := database.GetAgent(ctx, agent.ID); !errors.Is(err, sql.ErrNoRows) {
					t.Fatalf("deleted agent lookup = %v, want no rows", err)
				}
			}
			if _, err := database.GetAgent(ctx, other.ID); err != nil {
				t.Fatalf("unrelated agent changed: %v", err)
			}

			for _, ref := range []struct {
				table, column string
				oldCount      int
			}{
				{"connections", "agent_id", 1},
				{"agent_stats", "agent_id", 1},
				{"proxy_request_events", "agent_id", 2},
				{"proxy_request_events", "retry_failed_agent_id", 2},
			} {
				var oldRefs, nullRefs, otherRefs int
				query := fmt.Sprintf(`SELECT COUNT(CASE WHEN %[1]s = ? THEN 1 END),
					COUNT(CASE WHEN %[1]s IS NULL THEN 1 END),
					COUNT(CASE WHEN %[1]s = ? THEN 1 END) FROM %[2]s`, ref.column, ref.table)
				if err := database.QueryRow(query, agent.ID, other.ID).Scan(&oldRefs, &nullRefs, &otherRefs); err != nil {
					t.Fatal(err)
				}
				wantOld, wantNull := 0, ref.oldCount
				if blocked {
					wantOld, wantNull = ref.oldCount, 0
				}
				if oldRefs != wantOld || nullRefs != wantNull || otherRefs != 1 {
					t.Errorf("%s.%s references = old:%d null:%d other:%d, want %d/%d/1",
						ref.table, ref.column, oldRefs, nullRefs, otherRefs, wantOld, wantNull)
				}
			}
			for query, want := range map[string]int{
				`SELECT COUNT(*) FROM connections WHERE disconnected_at IS NOT NULL`: 2,
				`SELECT SUM(bytes_rx) FROM agent_stats`:                              246,
				`SELECT SUM(duration_ms) FROM proxy_request_events`:                  60,
			} {
				var got int
				if err := database.QueryRow(query).Scan(&got); err != nil || got != want {
					t.Errorf("history query %q = %d, %v; want %d", query, got, err, want)
				}
			}
			for _, table := range []string{"public_agent_labels", "management_agent_trust_reports", "agent_updater_enrollment_tokens"} {
				var got int
				if err := database.QueryRow("SELECT COUNT(*) FROM "+table+" WHERE agent_id = ?", agent.ID).Scan(&got); err != nil {
					t.Fatal(err)
				}
				want := 0
				if blocked {
					want = 1
				}
				if got != want {
					t.Errorf("%s owned records = %d, want %d", table, got, want)
				}
			}
			rows, err := database.Query(`PRAGMA foreign_key_check`)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			if rows.Next() || rows.Err() != nil {
				t.Fatalf("invalid foreign keys after deletion: %v", rows.Err())
			}
		})
	}
}

func TestDeleteAgentRejectsConnectedOrUnauthenticatedRequests(t *testing.T) {
	for _, connected := range []bool{false, true} {
		t.Run(fmt.Sprintf("connected=%t", connected), func(t *testing.T) {
			ctx := context.Background()
			database := newAgentRegistryTestDB(t)
			app := NewApp(nil, database)
			agent := createAgentRegistryTestAgent(t, database, "protected-agent", "Protected Agent", "token")
			req := connect.NewRequest(&p2pstreamv1.DeleteAgentRequest{Id: agent.ID})
			wantCode := connect.CodeUnauthenticated
			if connected {
				header := createTestAdminSession(t, app)
				req.Header().Set("Cookie", header.Get("Cookie"))
				if err := app.AgentHub.connect(agentRegistryTestConn(agent)); err != nil {
					t.Fatal(err)
				}
				wantCode = connect.CodeFailedPrecondition
			}
			if _, err := app.DeleteAgent(ctx, req); connect.CodeOf(err) != wantCode {
				t.Fatalf("delete agent = %v, want %v", err, wantCode)
			}
			if _, err := database.GetAgent(ctx, agent.ID); err != nil {
				t.Fatalf("rejected deletion removed agent: %v", err)
			}
		})
	}
}

func seedAgentDeletionHistory(t *testing.T, database *db.DB, agentID, otherID int64) {
	t.Helper()
	for _, id := range []int64{agentID, otherID} {
		for _, query := range []string{
			`INSERT INTO connections (agent_id, disconnected_at) VALUES (?, CURRENT_TIMESTAMP)`,
			`INSERT INTO agent_stats (agent_id, memory_mb, goroutines, req_success, req_client_error, req_server_error, bytes_rx, bytes_tx)
			 VALUES (?, 12, 3, 4, 5, 6, 123, 456)`,
			`INSERT INTO public_agent_labels (agent_id, key, value, source) VALUES (?, 'region', 'test', 'user')`,
			`INSERT INTO management_agent_trust_reports (agent_id) VALUES (?)`,
			`INSERT INTO agent_updater_enrollment_tokens (agent_id, token_hash, pinned_repository, authority_key_id, authority_epoch, enrollment_generation, expires_at)
			 VALUES (?, lower(hex(randomblob(32))), 'owner/repo', 'authority', 1, 1, CURRENT_TIMESTAMP)`,
		} {
			if _, err := database.Exec(query, id); err != nil {
				t.Fatalf("seed history: %v", err)
			}
		}
	}
	// Cover successful use, a failed retry through the deleted agent, and both
	// references on one event without clearing references to another agent.
	for _, ids := range [][2]int64{{agentID, otherID}, {otherID, agentID}, {agentID, agentID}} {
		if _, err := database.Exec(`INSERT INTO proxy_request_events (agent_id, retry_failed_agent_id, status_code, duration_ms)
			VALUES (?, ?, 502, 20)`, ids[0], ids[1]); err != nil {
			t.Fatal(err)
		}
	}
}
