package db

import (
	"database/sql"
	"strings"
	"testing"
)

func TestProxyRequestEventNeutralBatchShapeSelection(t *testing.T) {
	connector := &neutralStatementConnector{}
	database := sql.OpenDB(connector)
	defer database.Close()
	tx, err := database.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	inserter, err := PrepareProxyRequestEventInserter(t.Context(), tx)
	if err != nil {
		t.Fatal(err)
	}
	defer inserter.Close()
	neutral, fallback := proxyRequestNeutralEvents(16), proxyRequestNeutralEvents(16)
	fallback[15].ErrorKind = "fallback"
	if err := inserter.InsertBatch(t.Context(), neutral); err != nil {
		t.Fatal(err)
	}
	if inserter.neutralBatchStmt == nil || inserter.batchStmt != nil || len(connector.statements) != 2 || connector.statements[1].NumInput() != 192 {
		t.Fatal("neutral group did not prepare only192-bound shape")
	}
	saved := inserter.neutralBatchStmt
	if err := inserter.InsertBatch(t.Context(), fallback); err != nil {
		t.Fatal(err)
	}
	if inserter.batchStmt == nil || len(connector.statements) != 3 || connector.statements[2].NumInput() != 368 {
		t.Fatal("fallback group did not lazily prepare368-bound shape")
	}
	if err := inserter.InsertBatch(t.Context(), neutral); err != nil {
		t.Fatal(err)
	}
	if inserter.neutralBatchStmt != saved || len(connector.statements) != 3 {
		t.Fatal("neutral statement not reused")
	}
	for _, statement := range connector.statements[1:] {
		if statement.executions == 0 {
			t.Fatal("prepared unselected shape")
		}
	}
}

func TestProxyRequestEventNeutralInsertRejectsGeneratedColumnDrift(t *testing.T) {
	for _, query := range []string{
		strings.Replace(insertProxyRequestEventAt, "status_code, duration_ms", "duration_ms, status_code", 1),
		strings.Replace(insertProxyRequestEventAt, "retry_error_kind, retry_failed_agent_id", "retry_failed_agent_id, retry_error_kind", 1),
		strings.Replace(insertProxyRequestEventAt, "cache_status, cache_bytes", "cache_status, changed_bytes", 1),
		strings.Replace(insertProxyRequestEventAt, "?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?", "?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?", 1),
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Fatal("generated column/tuple drift accepted")
				}
			}()
			proxyRequestEventInsertParts(query)
		}()
	}
}
