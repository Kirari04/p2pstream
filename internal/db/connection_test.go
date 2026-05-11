package db

import (
	"context"
	"database/sql"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

func TestNormalizeSQLiteDSNForcesWALAndPrivateCache(t *testing.T) {
	dsn, err := normalizeSQLiteDSN("file:p2pstream.db?cache=shared&mode=rwc")
	if err != nil {
		t.Fatalf("normalize dsn: %v", err)
	}

	_, rawQuery, ok := strings.Cut(dsn, "?")
	if !ok {
		t.Fatalf("expected query params in dsn %q", dsn)
	}
	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		t.Fatalf("parse dsn query: %v", err)
	}
	if values.Get("_journal_mode") != "WAL" {
		t.Fatalf("expected WAL journal mode, got %q", values.Get("_journal_mode"))
	}
	if values.Get("_busy_timeout") != "10000" {
		t.Fatalf("expected 10000 busy timeout, got %q", values.Get("_busy_timeout"))
	}
	if values.Get("cache") != "private" {
		t.Fatalf("expected private cache, got %q", values.Get("cache"))
	}
}

func TestMigrationCreatesMultiAgentRoutingSchema(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "p2pstream-test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	for _, table := range []string{"agents", "public_backend_agents", "public_backend_upstream_headers"} {
		var name string
		if err := database.QueryRowContext(context.Background(), `SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&name); err != nil {
			t.Fatalf("expected table %s: %v", table, err)
		}
	}

	backendColumns := tableColumns(t, database, "public_backends")
	for _, column := range []string{"forward_mode", "load_balancing", "upstream_basic_auth_enabled", "upstream_basic_auth_username", "upstream_basic_auth_password"} {
		if !containsString(backendColumns, column) {
			t.Fatalf("public_backends missing column %s in %v", column, backendColumns)
		}
	}
	if _, err := database.ExecContext(context.Background(), `INSERT INTO public_backends (name, target_origin) VALUES ('legacy-default', 'http://example.com')`); err != nil {
		t.Fatalf("insert default backend: %v", err)
	}
	backend, err := database.GetPublicBackend(context.Background(), 1)
	if err != nil {
		t.Fatalf("get default backend: %v", err)
	}
	if backend.ForwardMode != "direct" || backend.LoadBalancing != "round_robin" {
		t.Fatalf("backend defaults = mode %q lb %q, want direct round_robin", backend.ForwardMode, backend.LoadBalancing)
	}
	if backend.UpstreamBasicAuthEnabled != 0 || backend.UpstreamBasicAuthUsername != "" || backend.UpstreamBasicAuthPassword != "" {
		t.Fatalf("backend upstream auth defaults = enabled %d username %q password %q, want disabled empty", backend.UpstreamBasicAuthEnabled, backend.UpstreamBasicAuthUsername, backend.UpstreamBasicAuthPassword)
	}
}

func TestMigrationUpgradesLegacySchemaWithAgentColumns(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "legacy.db")
	raw, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatalf("open raw legacy db: %v", err)
	}
	legacySchema := `
	CREATE TABLE connections (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		connected_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		disconnected_at DATETIME
	);
	CREATE TABLE agent_stats (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		reported_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		memory_mb INTEGER NOT NULL,
		goroutines INTEGER NOT NULL,
		req_success INTEGER NOT NULL,
		req_client_error INTEGER NOT NULL,
		req_server_error INTEGER NOT NULL,
		bytes_rx INTEGER NOT NULL,
		bytes_tx INTEGER NOT NULL
	);
	CREATE TABLE proxy_request_events (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		occurred_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		status_code INTEGER NOT NULL,
		duration_ms INTEGER NOT NULL,
		error_kind TEXT NOT NULL DEFAULT ''
	);
	CREATE TABLE public_backends (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL UNIQUE,
		target_origin TEXT NOT NULL,
		enabled INTEGER NOT NULL DEFAULT 1,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);
	`
	if _, err := raw.ExecContext(context.Background(), legacySchema); err != nil {
		t.Fatalf("create legacy schema: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw legacy db: %v", err)
	}

	database, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open migrated legacy db: %v", err)
	}
	defer database.Close()

	for table, columns := range map[string][]string{
		"connections":          {"agent_id"},
		"agent_stats":          {"agent_id", "req_internal_error"},
		"proxy_request_events": {"agent_id", "listener_id", "backend_id", "route_id"},
		"public_backends":      {"backend_type", "forward_mode", "load_balancing", "tls_skip_verify", "static_status_code", "static_response_body", "upstream_basic_auth_enabled", "upstream_basic_auth_username", "upstream_basic_auth_password"},
	} {
		got := tableColumns(t, database, table)
		for _, column := range columns {
			if !containsString(got, column) {
				t.Fatalf("%s missing column %s after migration; columns=%v", table, column, got)
			}
		}
	}
	if !indexExists(t, database, "idx_agent_stats_agent_id") {
		t.Fatal("expected idx_agent_stats_agent_id after migration")
	}
	if !indexExists(t, database, "idx_connections_agent_id") {
		t.Fatal("expected idx_connections_agent_id after migration")
	}
	if !indexExists(t, database, "idx_public_backend_upstream_headers_backend_position") {
		t.Fatal("expected idx_public_backend_upstream_headers_backend_position after migration")
	}
}

func TestPublicBackendUpstreamHeadersRoundTrip(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "p2pstream-test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	backend, err := database.CreatePublicBackend(context.Background(), CreatePublicBackendParams{
		Name:         "upstream-headers",
		TargetOrigin: "http://example.com",
		BackendType:  "proxy_forward",
		Enabled:      1,
	})
	if err != nil {
		t.Fatalf("create backend: %v", err)
	}
	first, err := database.CreatePublicBackendUpstreamHeader(context.Background(), CreatePublicBackendUpstreamHeaderParams{
		BackendID: backend.ID,
		Position:  0,
		Name:      "X-Upstream-One",
		Value:     "one",
		Sensitive: 0,
	})
	if err != nil {
		t.Fatalf("create upstream header: %v", err)
	}
	second, err := database.CreatePublicBackendUpstreamHeader(context.Background(), CreatePublicBackendUpstreamHeaderParams{
		BackendID: backend.ID,
		Position:  1,
		Name:      "Authorization",
		Value:     "Bearer secret",
		Sensitive: 1,
	})
	if err != nil {
		t.Fatalf("create sensitive upstream header: %v", err)
	}

	byBackend, err := database.ListPublicBackendUpstreamHeadersByBackend(context.Background(), backend.ID)
	if err != nil {
		t.Fatalf("list upstream headers by backend: %v", err)
	}
	if len(byBackend) != 2 || byBackend[0].ID != first.ID || byBackend[1].ID != second.ID {
		t.Fatalf("unexpected upstream headers by backend: %+v", byBackend)
	}
	all, err := database.ListPublicBackendUpstreamHeaders(context.Background())
	if err != nil {
		t.Fatalf("list upstream headers: %v", err)
	}
	if len(all) != 2 || all[0].Name != "X-Upstream-One" || all[1].Sensitive != 1 {
		t.Fatalf("unexpected upstream headers: %+v", all)
	}
	if err := database.DeletePublicBackendUpstreamHeaders(context.Background(), backend.ID); err != nil {
		t.Fatalf("delete upstream headers: %v", err)
	}
	empty, err := database.ListPublicBackendUpstreamHeadersByBackend(context.Background(), backend.ID)
	if err != nil {
		t.Fatalf("list deleted upstream headers: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("expected deleted upstream headers, got %+v", empty)
	}
}

func tableColumns(t *testing.T, database *DB, table string) []string {
	t.Helper()
	rows, err := database.QueryContext(context.Background(), `PRAGMA table_info(`+table+`)`)
	if err != nil {
		t.Fatalf("table info %s: %v", table, err)
	}
	defer rows.Close()
	var columns []string
	for rows.Next() {
		var cid int
		var name string
		var typ string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			t.Fatalf("scan table info: %v", err)
		}
		columns = append(columns, name)
	}
	sort.Strings(columns)
	return columns
}

func containsString(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}

func indexExists(t *testing.T, database *DB, name string) bool {
	t.Helper()
	var got string
	err := database.QueryRowContext(context.Background(), `SELECT name FROM sqlite_master WHERE type = 'index' AND name = ?`, name).Scan(&got)
	if err == sql.ErrNoRows {
		return false
	}
	if err != nil {
		t.Fatalf("query index %s: %v", name, err)
	}
	return got == name
}

func TestOpenConfiguresWALJournalMode(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "p2pstream-test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	var journalMode string
	if err := database.QueryRowContext(context.Background(), `PRAGMA journal_mode`).Scan(&journalMode); err != nil {
		t.Fatalf("read journal mode: %v", err)
	}
	if journalMode != "wal" {
		t.Fatalf("expected wal journal mode, got %q", journalMode)
	}

	var busyTimeout int
	if err := database.QueryRowContext(context.Background(), `PRAGMA busy_timeout`).Scan(&busyTimeout); err != nil {
		t.Fatalf("read busy timeout: %v", err)
	}
	if busyTimeout != 10000 {
		t.Fatalf("expected busy timeout 10000, got %d", busyTimeout)
	}
}
