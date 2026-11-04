package db

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pressly/goose/v3"

	"p2pstream/internal/db/migrations"
)

var nullableEventIndexColumns = []string{
	"listener_id", "route_id", "route_target_id", "agent_id",
	"waf_rule_id", "cache_rule_id", "retry_rule_id",
}

func nullableEventIndexProvider(t *testing.T, database *sql.DB) *goose.Provider {
	t.Helper()
	provider, err := goose.NewProvider(goose.DialectSQLite3, database, migrations.FS,
		goose.WithGoMigrations(
			goose.NewGoMigration(17, &goose.GoFunc{RunTx: repairEmptyManagedUpdateSchema}, nil),
			goose.NewGoMigration(18, &goose.GoFunc{RunTx: applySchemaCutover}, nil),
			goose.NewGoMigration(20, &goose.GoFunc{RunDB: applySiteWorkspaceFoundation}, &goose.GoFunc{RunDB: rejectSiteWorkspaceFoundationDown})))
	if err != nil {
		t.Fatal(err)
	}
	return provider
}

func nullableEventIndexVersion20(t *testing.T) (*sql.DB, *goose.Provider, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "version20.db")
	dsn, err := normalizeSQLiteDSN(path)
	if err != nil {
		t.Fatal(err)
	}
	database, err := sql.Open("sqlite3", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	// Migration 20 reserves a connection in addition to Goose's bookkeeping connection.
	database.SetMaxOpenConns(8)
	database.SetMaxIdleConns(8)
	provider := nullableEventIndexProvider(t, database)
	if _, err := provider.UpTo(t.Context(), 20); err != nil {
		t.Fatal(err)
	}
	return database, provider, path
}

func seedNullableEventIndexRows(t *testing.T, database *sql.DB) {
	t.Helper()
	for _, id := range []int64{1, 0, -1} {
		if _, err := database.ExecContext(t.Context(), `INSERT INTO agents (id,public_id,name,token_hash) VALUES (?,?,?,?)`,
			id, fmt.Sprintf("index-agent-%d", id), "index fixture", "fixture-token-hash"); err != nil {
			t.Fatal(err)
		}
	}
	for index, value := range []any{nil, int64(1), int64(0), int64(-1)} {
		if _, err := database.ExecContext(t.Context(), `
			INSERT INTO proxy_request_events (
				id,occurred_at,status_code,duration_ms,error_kind,method,host,path_prefix,
				listener_id,route_id,route_target_id,agent_id,waf_rule_id,cache_rule_id,retry_rule_id,
				retry_failed_agent_id,request_bytes,response_bytes,cache_status,retry_count,retry_outcome
			) VALUES (?, '2026-10-18 12:00:00+00:00', ?, ?, ?, 'GET', 'index.example', ?,
				?, ?, ?, ?, ?, ?, ?, 1, 7, 11, 'bypass', 1, 'recovered')`,
			index+1, 200+index, index+10, fmt.Sprintf("event-%d", index), fmt.Sprintf("/event-%d", index),
			value, value, value, value, value, value, value); err != nil {
			t.Fatal(err)
		}
	}
}

func nullableEventIndexRows(t *testing.T, database *sql.DB) [][]any {
	t.Helper()
	rows, err := database.QueryContext(t.Context(), `SELECT * FROM proxy_request_events ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var result [][]any
	for rows.Next() {
		values := make([]any, len(columns))
		args := make([]any, len(columns))
		for index := range values {
			args[index] = &values[index]
		}
		if err := rows.Scan(args...); err != nil {
			t.Fatal(err)
		}
		result = append(result, values)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}

func nullableEventIndexHistory(t *testing.T, database *sql.DB) string {
	t.Helper()
	var history string
	if err := database.QueryRowContext(t.Context(), `
		SELECT COALESCE(group_concat(id || ':' || version_id || ':' || is_applied, ','), '')
		FROM (SELECT id, version_id, is_applied FROM goose_db_version ORDER BY id)
	`).Scan(&history); err != nil {
		t.Fatal(err)
	}
	return history
}

func assertNullableEventIndexes(t *testing.T, database *sql.DB, partial bool) {
	t.Helper()
	rows, err := database.QueryContext(t.Context(), `PRAGMA index_list('proxy_request_events')`)
	if err != nil {
		t.Fatal(err)
	}
	flags := make(map[string]int)
	for rows.Next() {
		var seq, unique, flag int
		var name, origin string
		if err := rows.Scan(&seq, &name, &unique, &origin, &flag); err != nil {
			t.Fatal(err)
		}
		flags[name] = flag
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	if len(flags) != 9 || flags["idx_proxy_request_events_occurred_at"] != 0 || flags["idx_proxy_request_events_recent_problem"] != 1 {
		t.Fatalf("unexpected event index set: %v", flags)
	}
	for _, column := range nullableEventIndexColumns {
		name := "idx_proxy_request_events_" + column
		wantFlag := 0
		predicate := ""
		if partial {
			wantFlag = 1
			predicate = " WHERE " + column + " IS NOT NULL"
		}
		flag, found := flags[name]
		if !found || flag != wantFlag {
			t.Fatalf("%s partial flag = %d (present=%t), want %d", name, flag, found, wantFlag)
		}
		var ddl string
		if err := database.QueryRowContext(t.Context(), `SELECT sql FROM sqlite_master WHERE type='index' AND name=?`, name).Scan(&ddl); err != nil {
			t.Fatal(err)
		}
		want := "CREATE INDEX " + name + " ON proxy_request_events (" + column + ")" + predicate
		if normalizeSchemaSQL(strings.Replace(ddl, "IF NOT EXISTS ", "", 1)) != want {
			t.Fatalf("%s definition = %q, want %q", name, ddl, want)
		}
	}
	for name, want := range map[string]string{
		"idx_proxy_request_events_occurred_at":    "CREATE INDEX idx_proxy_request_events_occurred_at ON proxy_request_events (occurred_at)",
		"idx_proxy_request_events_recent_problem": "CREATE INDEX idx_proxy_request_events_recent_problem ON proxy_request_events (occurred_at DESC) WHERE status_code >= 400 OR error_kind != '' OR retry_count > 0",
	} {
		var ddl string
		if err := database.QueryRowContext(t.Context(), `SELECT sql FROM sqlite_master WHERE name=?`, name).Scan(&ddl); err != nil {
			t.Fatal(err)
		}
		if normalizeSchemaSQL(strings.Replace(ddl, "IF NOT EXISTS ", "", 1)) != want {
			t.Fatalf("protected index %s changed: %q", name, ddl)
		}
	}
}

func assertNullableEventIndexQueries(t *testing.T, database *sql.DB) {
	t.Helper()
	before := nullableEventIndexRows(t, database)
	ids := func(query string, args ...any) []int64 {
		t.Helper()
		rows, err := database.QueryContext(t.Context(), query, args...)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		result := []int64{}
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				t.Fatal(err)
			}
			result = append(result, id)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return result
	}
	check := func(query string, want []int64, args ...any) {
		t.Helper()
		if got := ids(query, args...); !reflect.DeepEqual(got, want) {
			t.Fatalf("%s (%v) = %v, want %v", query, args, got, want)
		}
	}
	for _, column := range nullableEventIndexColumns {
		name := "idx_proxy_request_events_" + column
		query := "SELECT id FROM proxy_request_events WHERE " + column + " = ? ORDER BY id"
		for index, value := range []int64{1, 0, -1} {
			check(query, []int64{int64(index + 2)}, value)
			// INDEXED BY makes this an eligibility assertion, independent of tiny-fixture cost estimates.
			planRows, err := database.QueryContext(t.Context(), "EXPLAIN QUERY PLAN SELECT id FROM proxy_request_events INDEXED BY "+name+" WHERE "+column+" = ?", value)
			if err != nil {
				t.Fatalf("%s parameterized plan: %v", column, err)
			}
			eligible := false
			for planRows.Next() {
				var id, parent, unused int
				var detail string
				if err := planRows.Scan(&id, &parent, &unused, &detail); err != nil {
					t.Fatal(err)
				}
				eligible = eligible || strings.Contains(detail, "SEARCH") && strings.Contains(detail, name)
			}
			if err := planRows.Err(); err != nil {
				t.Fatal(err)
			}
			planRows.Close()
			if !eligible {
				t.Fatalf("%s cannot serve a parameterized equality lookup", name)
			}
		}
		check(query, []int64{}, nil)
		check("SELECT id FROM proxy_request_events WHERE "+column+" IS NULL ORDER BY id", []int64{1})
		check("SELECT id FROM proxy_request_events WHERE "+column+" IS NOT NULL ORDER BY id", []int64{2, 3, 4})
		if _, err := database.ExecContext(t.Context(), "UPDATE proxy_request_events SET "+column+" = 1 WHERE id = 1"); err != nil {
			t.Fatal(err)
		}
		check(query, []int64{1, 2}, int64(1))
		check("SELECT id FROM proxy_request_events WHERE "+column+" IS NULL", []int64{})
		if _, err := database.ExecContext(t.Context(), "UPDATE proxy_request_events SET "+column+" = NULL WHERE id IN (1, 2)"); err != nil {
			t.Fatal(err)
		}
		check(query, []int64{}, int64(1))
		check("SELECT id FROM proxy_request_events WHERE "+column+" IS NULL ORDER BY id", []int64{1, 2})
		if _, err := database.ExecContext(t.Context(), "UPDATE proxy_request_events SET "+column+" = 1 WHERE id = 2"); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(nullableEventIndexRows(t, database), before) {
		t.Fatal("restored NULL/non-NULL probes changed raw event data")
	}
	assertForeignKeyCheck(t, &DB{DB: database})
}

func TestNullableEventIndexesFreshSchemaParityAndValues(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "fresh.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	declarative := openSchemaParityDB(t, "declarative.db")
	applySchemaSQL(t, declarative)
	assertSchemaParity(t, "nullable event indexes", readNormalizedSchema(t, database.DB), readNormalizedSchema(t, declarative))
	assertNullableEventIndexes(t, database.DB, true)
	seedNullableEventIndexRows(t, database.DB)
	assertNullableEventIndexQueries(t, database.DB)
}

func TestNullableEventIndexesUpgradeReopenAndRoundTrip(t *testing.T) {
	database, provider, path := nullableEventIndexVersion20(t)
	assertNullableEventIndexes(t, database, false)
	seedNullableEventIndexRows(t, database)
	before := nullableEventIndexRows(t, database)
	fullSchema := readNormalizedSchema(t, database)
	assertNullableEventIndexQueries(t, database)
	if _, err := provider.UpTo(t.Context(), 21); err != nil {
		t.Fatal(err)
	}
	assertNullableEventIndexes(t, database, true)
	assertNullableEventIndexQueries(t, database)
	partialSchema := readNormalizedSchema(t, database)
	history := nullableEventIndexHistory(t, database)
	var schemaVersion int64
	if err := database.QueryRowContext(t.Context(), `PRAGMA schema_version`).Scan(&schemaVersion); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	database = reopened.DB
	var reopenedVersion int64
	if err := database.QueryRowContext(t.Context(), `PRAGMA schema_version`).Scan(&reopenedVersion); err != nil {
		t.Fatal(err)
	}
	if reopenedVersion != schemaVersion || nullableEventIndexHistory(t, database) != history {
		t.Fatal("reopen rebuilt indexes or changed applied migration history")
	}
	assertSchemaParity(t, "reopened indexes", readNormalizedSchema(t, database), partialSchema)
	provider = nullableEventIndexProvider(t, database)
	if _, err := provider.DownTo(t.Context(), 20); err != nil {
		t.Fatal(err)
	}
	if version, err := provider.GetDBVersion(t.Context()); err != nil || version != 20 {
		t.Fatalf("downgraded version = %d: %v", version, err)
	}
	assertNullableEventIndexes(t, database, false)
	assertSchemaParity(t, "downgraded indexes", readNormalizedSchema(t, database), fullSchema)
	if _, err := provider.UpTo(t.Context(), 21); err != nil {
		t.Fatal(err)
	}
	if version, err := provider.GetDBVersion(t.Context()); err != nil || version != 21 {
		t.Fatalf("upgraded version = %d: %v", version, err)
	}
	assertNullableEventIndexes(t, database, true)
	assertNullableEventIndexQueries(t, database)
	if !reflect.DeepEqual(nullableEventIndexRows(t, database), before) {
		t.Fatal("upgrade/reopen/downgrade changed event payloads")
	}
}

func TestNullableEventIndexesMigrationFailureRollsBack(t *testing.T) {
	database, provider, _ := nullableEventIndexVersion20(t)
	seedNullableEventIndexRows(t, database)
	// This fires after all seven real DDL replacements, at Goose's version insert.
	if _, err := database.ExecContext(t.Context(), `
		CREATE TRIGGER fail_nullable_event_index_version
		BEFORE INSERT ON goose_db_version WHEN NEW.version_id = 21
		BEGIN SELECT RAISE(ABORT, 'injected index migration failure'); END
	`); err != nil {
		t.Fatal(err)
	}
	beforeSchema := readNormalizedSchema(t, database)
	beforeEvents := nullableEventIndexRows(t, database)
	beforeHistory := nullableEventIndexHistory(t, database)
	if _, err := provider.UpTo(t.Context(), 21); err == nil || !strings.Contains(err.Error(), "injected index migration failure") {
		t.Fatalf("migration failure = %v, want injected version-insert failure", err)
	}
	assertSchemaParity(t, "rolled-back migration", readNormalizedSchema(t, database), beforeSchema)
	assertNullableEventIndexes(t, database, false)
	if nullableEventIndexHistory(t, database) != beforeHistory || !reflect.DeepEqual(nullableEventIndexRows(t, database), beforeEvents) {
		t.Fatal("failed migration changed history or raw events")
	}
	if version, err := provider.GetDBVersion(t.Context()); err != nil || version != 20 {
		t.Fatalf("failed migration version = %d: %v", version, err)
	}
	if _, err := database.ExecContext(t.Context(), `DROP TRIGGER fail_nullable_event_index_version`); err != nil {
		t.Fatal(err)
	}
	provider = nullableEventIndexProvider(t, database)
	if _, err := provider.UpTo(t.Context(), 21); err != nil {
		t.Fatalf("migration after removing injected failure: %v", err)
	}
	assertNullableEventIndexes(t, database, true)
	if !reflect.DeepEqual(nullableEventIndexRows(t, database), beforeEvents) {
		t.Fatal("successful migration changed raw events")
	}
	assertForeignKeyCheck(t, &DB{DB: database})
}
