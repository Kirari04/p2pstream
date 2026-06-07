package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

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

func TestOpenSecuresSQLiteDirectoryAndFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested")
	dbPath := filepath.Join(dir, "p2pstream.db")

	database, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer func() { _ = database.Close() }()

	assertDBMode(t, dir, 0700)
	assertDBMode(t, dbPath, 0600)
	for _, suffix := range []string{"-wal", "-shm"} {
		path := dbPath + suffix
		if _, err := os.Stat(path); err == nil {
			assertDBMode(t, path, 0600)
		} else if !os.IsNotExist(err) {
			t.Fatalf("stat %s: %v", path, err)
		}
	}
}

func TestOpenChmodsExistingSQLiteDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "loose")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	database, err := Open(filepath.Join(dir, "p2pstream.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer func() { _ = database.Close() }()

	assertDBMode(t, dir, 0700)
}

func TestMigrationCreatesMultiAgentRoutingSchema(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "p2pstream-test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer func() { _ = database.Close() }()

	for _, table := range []string{"agents", "public_agent_labels", "public_route_targets", "public_route_target_upstream_headers", "public_route_target_response_headers", "public_waf_captcha_providers", "public_waf_rules", "public_waf_settings", "public_cache_settings", "public_cache_rules", "public_cache_entries", "proxy_request_rollup_minutes", "proxy_request_tuple_rollup_minutes", "agent_stat_rollup_minutes", "observability_rollup_state"} {
		var name string
		if err := database.QueryRowContext(context.Background(), `SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&name); err != nil {
			t.Fatalf("expected table %s: %v", table, err)
		}
	}
	for _, table := range []string{"public_backends", "public_backend_agents", "public_backend_headers", "public_backend_upstream_headers", "public_route_backends"} {
		if tableExists(t, database, table) {
			t.Fatalf("legacy table %s exists in fresh schema", table)
		}
	}

	proxyEventColumns := tableColumns(t, database, "proxy_request_events")
	for _, column := range []string{"request_bytes", "response_bytes", "waf_rule_id", "waf_action", "cache_rule_id", "cache_status", "cache_bytes"} {
		if !containsString(proxyEventColumns, column) {
			t.Fatalf("proxy_request_events missing column %s in %v", column, proxyEventColumns)
		}
	}
	if containsString(proxyEventColumns, "backend_id") {
		t.Fatalf("proxy_request_events still has backend_id in %v", proxyEventColumns)
	}
	tupleColumns := tableColumns(t, database, "proxy_request_tuple_rollup_minutes")
	if containsString(tupleColumns, "backend_id") {
		t.Fatalf("proxy_request_tuple_rollup_minutes still has backend_id in %v", tupleColumns)
	}
	agentStatColumns := tableColumns(t, database, "agent_stats")
	if !containsString(agentStatColumns, "cpu_percent") {
		t.Fatalf("agent_stats missing cpu_percent in %v", agentStatColumns)
	}
	for _, index := range []string{
		"idx_proxy_request_events_route_id",
		"idx_proxy_request_events_agent_id",
		"idx_proxy_request_events_waf_rule_id",
		"idx_proxy_request_events_cache_rule_id",
		"idx_public_waf_rules_priority",
		"idx_public_waf_rules_captcha_provider_id",
		"idx_public_cache_rules_priority",
		"idx_public_cache_entries_rule_id",
		"idx_public_cache_entries_expires_at",
		"idx_public_cache_entries_last_accessed_at",
		"idx_connections_disconnected_at",
	} {
		if !indexExists(t, database, index) {
			t.Fatalf("expected %s on fresh schema", index)
		}
	}
	cacheSettings, err := database.UpsertPublicCacheSettingsDefaults(context.Background())
	if err != nil {
		t.Fatalf("upsert cache settings defaults: %v", err)
	}
	if cacheSettings.Enabled != 1 ||
		cacheSettings.MaxDiskBytes != 1073741824 ||
		cacheSettings.MaxMemoryBytes != 134217728 ||
		cacheSettings.MemoryHotObjectMaxBytes != 262144 ||
		cacheSettings.MaxEntries != 100000 ||
		cacheSettings.CleanupIntervalMillis != 60000 {
		t.Fatalf("unexpected cache settings defaults: %+v", cacheSettings)
	}
	cacheRuleColumns := tableColumns(t, database, "public_cache_rules")
	if !containsString(cacheRuleColumns, "allow_cookie_requests") {
		t.Fatalf("public_cache_rules missing allow_cookie_requests in %v", cacheRuleColumns)
	}
	if containsString(cacheRuleColumns, "backend_ids_json") {
		t.Fatalf("public_cache_rules still has backend_ids_json in %v", cacheRuleColumns)
	}
	cacheEntryColumns := tableColumns(t, database, "public_cache_entries")
	if containsString(cacheEntryColumns, "backend_id") {
		t.Fatalf("public_cache_entries still has backend_id in %v", cacheEntryColumns)
	}
	listenerColumns := tableColumns(t, database, "public_listeners")
	if containsString(listenerColumns, "default_backend_id") {
		t.Fatalf("public_listeners still has default_backend_id in %v", listenerColumns)
	}
	routeColumns := tableColumns(t, database, "public_routes")
	for _, column := range []string{"backend_id", "fallback_backend_id", "load_balancing"} {
		if containsString(routeColumns, column) {
			t.Fatalf("public_routes still has %s in %v", column, routeColumns)
		}
	}
	if _, err := database.ExecContext(context.Background(), `INSERT INTO public_cache_rules (name) VALUES ('cache-default')`); err != nil {
		t.Fatalf("insert cache rule defaults: %v", err)
	}
	var allowCookieRequests int64
	if err := database.QueryRowContext(context.Background(), `SELECT allow_cookie_requests FROM public_cache_rules WHERE name = 'cache-default'`).Scan(&allowCookieRequests); err != nil {
		t.Fatalf("read cache rule allow_cookie_requests default: %v", err)
	}
	if allowCookieRequests != 0 {
		t.Fatalf("cache rule allow_cookie_requests default = %d, want 0", allowCookieRequests)
	}

	tlsColumns := tableColumns(t, database, "public_tls_certificates")
	for _, column := range []string{"source", "acme_challenge_type", "acme_ca", "acme_email", "dns_credential_id", "status", "last_error", "issued_at", "expires_at", "next_renewal_at", "last_renewal_attempt_at"} {
		if !containsString(tlsColumns, column) {
			t.Fatalf("public_tls_certificates missing column %s in %v", column, tlsColumns)
		}
	}
	rows, err := database.QueryContext(context.Background(), `SELECT id, name, provider, cloudflare_zone_id, api_token, enabled FROM public_tls_dns_credentials LIMIT 0`)
	if err != nil {
		t.Fatalf("expected public_tls_dns_credentials table: %v", err)
	}
	rows.Close()
}

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

func TestMigrationConvertsLegacyPolicyMatchJSON(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "legacy-policy-match.db")
	database, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}

	raw, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	legacy := `{
		"methods": [" get ", "POST"],
		"protocols": [" HTTPS "],
		"host_patterns": ["*.Example.COM."],
		"path_prefixes": [" /api ", "admin"],
		"path_suffixes": [".JSON"],
		"headers": [
			{"name": "X-Plan", "operator": "equals", "value": "free"},
			{"name": "X-Trace", "operator": "present"}
		],
		"cookies": [{"name": " session ", "operator": "prefix", "value": "abc"}],
		"query_params": [{"name": " page ", "operator": "contains", "value": "1"}]
	}`
	inserts := []string{
		`INSERT INTO public_rate_limit_rules (name, algorithm, limit_count, window_millis, match_json) VALUES ('rate', 'fixed_window', 10, 1000, ?)`,
		`INSERT INTO public_traffic_shaper_rules (name, upload_bytes_per_second, match_json) VALUES ('shaper', 1024, ?)`,
		`INSERT INTO public_waf_rules (name, match_json) VALUES ('waf', ?)`,
		`INSERT INTO public_cache_rules (name, match_json) VALUES ('cache', ?)`,
	}
	for _, stmt := range inserts {
		if _, err := raw.ExecContext(context.Background(), stmt, legacy); err != nil {
			t.Fatalf("insert legacy policy match row: %v", err)
		}
	}
	if _, err := raw.ExecContext(context.Background(), `INSERT INTO public_cache_rules (name, match_json) VALUES ('cache-new', '{"cel_expression":"method == \"GET\""}')`); err != nil {
		t.Fatalf("insert new policy match row: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw db: %v", err)
	}

	database, err = Open(dbPath)
	if err != nil {
		t.Fatalf("open migrated db: %v", err)
	}
	defer func() {
		if err := database.Close(); err != nil {
			t.Fatalf("close migrated db: %v", err)
		}
	}()

	for table, name := range map[string]string{
		"public_rate_limit_rules":     "rate",
		"public_traffic_shaper_rules": "shaper",
		"public_waf_rules":            "waf",
		"public_cache_rules":          "cache",
	} {
		var rawMatch string
		if err := database.QueryRowContext(context.Background(), `SELECT match_json FROM `+table+` WHERE name = ?`, name).Scan(&rawMatch); err != nil {
			t.Fatalf("read %s migrated match_json: %v", table, err)
		}
		assertMigratedLegacyPolicyMatchJSON(t, table, rawMatch)
	}

	var unchanged string
	if err := database.QueryRowContext(context.Background(), `SELECT match_json FROM public_cache_rules WHERE name = 'cache-new'`).Scan(&unchanged); err != nil {
		t.Fatalf("read unchanged policy match: %v", err)
	}
	if unchanged != `{"cel_expression":"method == \"GET\""}` {
		t.Fatalf("new policy match JSON was changed: %s", unchanged)
	}
}

func assertMigratedLegacyPolicyMatchJSON(t *testing.T, table string, raw string) {
	t.Helper()
	var migrated policyMatchJSON
	if err := json.Unmarshal([]byte(raw), &migrated); err != nil {
		t.Fatalf("%s match_json is not policy match JSON: %v", table, err)
	}
	if migrated.CELExpression == "" {
		t.Fatalf("%s migrated CEL expression is empty: %s", table, raw)
	}
	for _, want := range []string{
		`method in ["GET", "POST"]`,
		`protocol in ["https"]`,
		`host_match(host, "*.example.com")`,
		`path_prefix(path, "/api")`,
		`path_prefix(path, "/admin")`,
		`path.endsWith(".JSON")`,
		`"x-plan" in headers`,
		`headers["x-plan"][0] == "free"`,
		`"x-trace" in headers`,
		`"session" in cookies`,
		`"page" in query`,
		`query["page"][0].contains("1")`,
	} {
		if !strings.Contains(migrated.CELExpression, want) {
			t.Fatalf("%s CEL expression %q missing %q", table, migrated.CELExpression, want)
		}
	}
	if migrated.Builder == nil || migrated.Builder.Root == nil {
		t.Fatalf("%s migrated builder is missing: %#v", table, migrated.Builder)
	}
	if got := len(migrated.Builder.Root.Conditions); got != 9 {
		t.Fatalf("%s builder condition count = %d, want 9", table, got)
	}
	if migrated.Builder.Root.Conditions[0].Field != "method" || migrated.Builder.Root.Conditions[0].Operator != "in" {
		t.Fatalf("%s first builder condition = %+v", table, migrated.Builder.Root.Conditions[0])
	}
	if !migrated.Builder.Root.Conditions[5].LegacyFirstValue {
		t.Fatalf("%s legacy header condition did not preserve first-value matching: %+v", table, migrated.Builder.Root.Conditions[5])
	}
	if !migrated.Builder.Root.Conditions[8].LegacyFirstValue {
		t.Fatalf("%s legacy query condition did not preserve first-value matching: %+v", table, migrated.Builder.Root.Conditions[8])
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
	defer func() { _ = database.Close() }()

	for table, columns := range map[string][]string{
		"connections":          {"agent_id"},
		"agent_stats":          {"agent_id", "req_internal_error", "cpu_percent"},
		"proxy_request_events": {"agent_id", "listener_id", "route_id", "route_target_id", "waf_rule_id", "waf_action", "request_bytes", "response_bytes", "cache_rule_id", "cache_status", "cache_bytes"},
		"public_cache_rules":   {"allow_cookie_requests"},
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
	if !indexExists(t, database, "idx_connections_disconnected_at") {
		t.Fatal("expected idx_connections_disconnected_at after migration")
	}
	if !indexExists(t, database, "idx_public_waf_rules_priority") {
		t.Fatal("expected idx_public_waf_rules_priority after migration")
	}
	if !indexExists(t, database, "idx_public_waf_rules_captcha_provider_id") {
		t.Fatal("expected idx_public_waf_rules_captcha_provider_id after migration")
	}
	for _, index := range []string{
		"idx_proxy_request_events_route_id",
		"idx_proxy_request_events_agent_id",
		"idx_proxy_request_events_waf_rule_id",
		"idx_proxy_request_events_cache_rule_id",
	} {
		if !indexExists(t, database, index) {
			t.Fatalf("expected %s after migration", index)
		}
	}
	for _, table := range []string{"public_cache_settings", "public_cache_rules", "public_cache_entries", "proxy_request_rollup_minutes", "proxy_request_tuple_rollup_minutes", "agent_stat_rollup_minutes", "observability_rollup_state"} {
		var name string
		if err := database.QueryRowContext(context.Background(), `SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&name); err != nil {
			t.Fatalf("expected migrated table %s: %v", table, err)
		}
	}
	for _, table := range []string{"public_backends", "public_backend_agents", "public_backend_headers", "public_backend_upstream_headers", "public_route_backends"} {
		if tableExists(t, database, table) {
			t.Fatalf("legacy table %s still exists after migration", table)
		}
	}
}

func TestMigrationResetsProxyObservabilityAndRenamesWAFRouteTargetTrigger(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "legacy-observability.db")
	raw, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatalf("open raw legacy db: %v", err)
	}
	legacySchema := `
	CREATE TABLE agents (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		public_id TEXT NOT NULL UNIQUE,
		name TEXT NOT NULL,
		token_hash TEXT NOT NULL,
		enabled INTEGER NOT NULL DEFAULT 1,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
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
		error_kind TEXT NOT NULL DEFAULT '',
		listener_id INTEGER,
		route_id INTEGER,
		route_target_id INTEGER,
		backend_id INTEGER,
		agent_id INTEGER,
		request_bytes INTEGER NOT NULL DEFAULT 0,
		response_bytes INTEGER NOT NULL DEFAULT 0
	);
	CREATE TABLE proxy_request_rollup_minutes (
		bucket_unix_millis INTEGER PRIMARY KEY,
		requests INTEGER NOT NULL DEFAULT 0,
		success INTEGER NOT NULL DEFAULT 0,
		client_error INTEGER NOT NULL DEFAULT 0,
		server_error INTEGER NOT NULL DEFAULT 0,
		internal_error INTEGER NOT NULL DEFAULT 0,
		duration_ms_sum INTEGER NOT NULL DEFAULT 0,
		max_duration_ms INTEGER NOT NULL DEFAULT 0,
		slow_requests INTEGER NOT NULL DEFAULT 0,
		request_bytes INTEGER NOT NULL DEFAULT 0,
		response_bytes INTEGER NOT NULL DEFAULT 0,
		cache_hits INTEGER NOT NULL DEFAULT 0,
		cache_misses INTEGER NOT NULL DEFAULT 0,
		cache_bypasses INTEGER NOT NULL DEFAULT 0,
		cache_stored INTEGER NOT NULL DEFAULT 0,
		cache_store_failed INTEGER NOT NULL DEFAULT 0,
		cache_hit_bytes INTEGER NOT NULL DEFAULT 0,
		cache_stored_bytes INTEGER NOT NULL DEFAULT 0,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);
	CREATE TABLE proxy_request_tuple_rollup_minutes (
		bucket_unix_millis INTEGER NOT NULL,
		listener_id INTEGER NOT NULL DEFAULT 0,
		route_id INTEGER NOT NULL DEFAULT 0,
		route_target_id INTEGER NOT NULL DEFAULT 0,
		backend_id INTEGER NOT NULL DEFAULT 0,
		agent_id INTEGER NOT NULL DEFAULT 0,
		error_kind TEXT NOT NULL DEFAULT '',
		status_class INTEGER NOT NULL DEFAULT 0,
		requests INTEGER NOT NULL DEFAULT 0,
		success INTEGER NOT NULL DEFAULT 0,
		client_error INTEGER NOT NULL DEFAULT 0,
		server_error INTEGER NOT NULL DEFAULT 0,
		internal_error INTEGER NOT NULL DEFAULT 0,
		duration_ms_sum INTEGER NOT NULL DEFAULT 0,
		request_bytes INTEGER NOT NULL DEFAULT 0,
		response_bytes INTEGER NOT NULL DEFAULT 0,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (bucket_unix_millis, listener_id, route_id, route_target_id, backend_id, agent_id, error_kind, status_class)
	);
	CREATE TABLE public_waf_captcha_providers (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL UNIQUE,
		provider_type TEXT NOT NULL,
		site_key TEXT NOT NULL,
		secret_key TEXT NOT NULL,
		enabled INTEGER NOT NULL DEFAULT 1,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);
	CREATE TABLE public_waf_rules (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL UNIQUE,
		priority INTEGER NOT NULL DEFAULT 100,
		enabled INTEGER NOT NULL DEFAULT 1,
		action TEXT NOT NULL DEFAULT 'block',
		activation_mode TEXT NOT NULL DEFAULT 'always',
		match_json TEXT NOT NULL DEFAULT '{}',
		key_parts_json TEXT NOT NULL DEFAULT '[]',
		captcha_provider_id INTEGER REFERENCES public_waf_captcha_providers(id),
		captcha_pass_ttl_millis INTEGER NOT NULL DEFAULT 1800000,
		waiting_room_max_admitted_sessions INTEGER NOT NULL DEFAULT 50,
		waiting_room_admission_rate_per_second INTEGER NOT NULL DEFAULT 10,
		waiting_room_admission_session_ttl_millis INTEGER NOT NULL DEFAULT 600000,
		waiting_room_queue_poll_interval_millis INTEGER NOT NULL DEFAULT 5000,
		waiting_room_queue_timeout_millis INTEGER NOT NULL DEFAULT 1800000,
		waiting_room_page_title TEXT NOT NULL DEFAULT 'Waiting room',
		waiting_room_page_body TEXT NOT NULL DEFAULT 'Traffic is high. You will be admitted automatically.',
		trigger_request_window_millis INTEGER NOT NULL DEFAULT 10000,
		trigger_minimum_request_rate INTEGER NOT NULL DEFAULT 50,
		trigger_traffic_spike_multiplier REAL NOT NULL DEFAULT 4,
		trigger_proxy_active_requests INTEGER NOT NULL DEFAULT 100,
		trigger_backend_active_requests INTEGER NOT NULL DEFAULT 100,
		trigger_agent_active_requests INTEGER NOT NULL DEFAULT 50,
		trigger_server_cpu_percent REAL NOT NULL DEFAULT 85,
		trigger_agent_cpu_percent REAL NOT NULL DEFAULT 85,
		trigger_minimum_active_millis INTEGER NOT NULL DEFAULT 30000,
		trigger_quiet_period_millis INTEGER NOT NULL DEFAULT 60000,
		block_response_status_code INTEGER NOT NULL DEFAULT 403,
		block_response_body TEXT NOT NULL DEFAULT 'Request blocked',
		block_response_content_type TEXT NOT NULL DEFAULT 'text/plain; charset=utf-8',
		block_response_headers_json TEXT NOT NULL DEFAULT '[]',
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);
	INSERT INTO agent_stats (memory_mb, goroutines, req_success, req_client_error, req_server_error, bytes_rx, bytes_tx)
	VALUES (12, 3, 4, 5, 6, 700, 800);
	INSERT INTO proxy_request_events (status_code, duration_ms, listener_id, route_id, route_target_id, backend_id, request_bytes, response_bytes)
	VALUES (200, 42, 1, 2, 3, 4, 10, 20);
	INSERT INTO proxy_request_rollup_minutes (bucket_unix_millis, requests, success)
	VALUES (60000, 1, 1);
	INSERT INTO proxy_request_tuple_rollup_minutes (bucket_unix_millis, listener_id, route_id, route_target_id, backend_id, agent_id, error_kind, status_class, requests, success)
	VALUES (60000, 1, 2, 3, 4, 0, '', 2, 1, 1);
	INSERT INTO public_waf_rules (name, trigger_backend_active_requests)
	VALUES ('legacy-waf', 321);
	`
	if _, err := raw.ExecContext(context.Background(), legacySchema); err != nil {
		t.Fatalf("create legacy observability schema: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw legacy db: %v", err)
	}

	database, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open migrated legacy db: %v", err)
	}
	defer func() { _ = database.Close() }()

	if countRows(t, database, `SELECT COUNT(*) FROM proxy_request_events`) != 0 {
		t.Fatal("expected proxy request events to be reset")
	}
	if countRows(t, database, `SELECT COUNT(*) FROM proxy_request_rollup_minutes`) != 0 {
		t.Fatal("expected proxy request rollups to be reset")
	}
	if countRows(t, database, `SELECT COUNT(*) FROM proxy_request_tuple_rollup_minutes`) != 0 {
		t.Fatal("expected proxy request tuple rollups to be reset")
	}
	if countRows(t, database, `SELECT COUNT(*) FROM agent_stats`) != 1 {
		t.Fatal("expected agent stats to be preserved")
	}
	proxyEventColumns := tableColumns(t, database, "proxy_request_events")
	if containsString(proxyEventColumns, "backend_id") {
		t.Fatalf("proxy_request_events still has backend_id after reset: %v", proxyEventColumns)
	}
	tupleColumns := tableColumns(t, database, "proxy_request_tuple_rollup_minutes")
	if containsString(tupleColumns, "backend_id") {
		t.Fatalf("proxy_request_tuple_rollup_minutes still has backend_id after reset: %v", tupleColumns)
	}
	wafColumns := tableColumns(t, database, "public_waf_rules")
	if containsString(wafColumns, "trigger_backend_active_requests") {
		t.Fatalf("public_waf_rules still has trigger_backend_active_requests after migration: %v", wafColumns)
	}
	if !containsString(wafColumns, "trigger_route_target_active_requests") {
		t.Fatalf("public_waf_rules missing trigger_route_target_active_requests after migration: %v", wafColumns)
	}
	var routeTargetActiveRequests int64
	if err := database.QueryRowContext(context.Background(), `SELECT trigger_route_target_active_requests FROM public_waf_rules WHERE name = 'legacy-waf'`).Scan(&routeTargetActiveRequests); err != nil {
		t.Fatalf("read migrated WAF trigger: %v", err)
	}
	if routeTargetActiveRequests != 321 {
		t.Fatalf("migrated route target active request trigger = %d, want 321", routeTargetActiveRequests)
	}
	assertForeignKeyCheck(t, database)

	if err := database.Close(); err != nil {
		t.Fatalf("close migrated db before idempotency check: %v", err)
	}
	database, err = Open(dbPath)
	if err != nil {
		t.Fatalf("reopen migrated legacy db: %v", err)
	}
	defer func() { _ = database.Close() }()
	if countRows(t, database, `SELECT COUNT(*) FROM proxy_request_events`) != 0 ||
		countRows(t, database, `SELECT COUNT(*) FROM proxy_request_rollup_minutes`) != 0 ||
		countRows(t, database, `SELECT COUNT(*) FROM proxy_request_tuple_rollup_minutes`) != 0 {
		t.Fatal("expected proxy observability reset migration to be idempotent")
	}
	if countRows(t, database, `SELECT COUNT(*) FROM agent_stats`) != 1 {
		t.Fatal("expected agent stats to remain after idempotent migration")
	}
	assertForeignKeyCheck(t, database)
}

func TestMigrationUpgradesLegacyTLSCertificateSchema(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "legacy-tls.db")
	raw, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatalf("open raw legacy db: %v", err)
	}
	legacySchema := `
	CREATE TABLE public_tls_certificates (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		listener_id INTEGER NOT NULL,
		hostname_pattern TEXT NOT NULL,
		cert_path TEXT NOT NULL,
		key_path TEXT NOT NULL,
		enabled INTEGER NOT NULL DEFAULT 1,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);
	INSERT INTO public_tls_certificates (listener_id, hostname_pattern, cert_path, key_path, enabled)
	VALUES (1, 'example.com', '/tmp/cert.pem', '/tmp/key.pem', 1);
	`
	if _, err := raw.ExecContext(context.Background(), legacySchema); err != nil {
		t.Fatalf("create legacy TLS schema: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw legacy db: %v", err)
	}

	database, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open migrated legacy db: %v", err)
	}
	defer func() { _ = database.Close() }()

	tlsColumns := tableColumns(t, database, "public_tls_certificates")
	for _, column := range []string{"source", "acme_challenge_type", "acme_ca", "acme_email", "dns_credential_id", "status", "last_error", "issued_at", "expires_at", "next_renewal_at", "last_renewal_attempt_at"} {
		if !containsString(tlsColumns, column) {
			t.Fatalf("public_tls_certificates missing column %s after migration; columns=%v", column, tlsColumns)
		}
	}
	if !indexExists(t, database, "idx_public_tls_certificates_dns_credential_id") {
		t.Fatal("expected idx_public_tls_certificates_dns_credential_id after migration")
	}
	cert, err := database.GetPublicTlsCertificate(context.Background(), 1)
	if err != nil {
		t.Fatalf("get migrated cert: %v", err)
	}
	if cert.Source != "manual" || cert.Status != "ready" {
		t.Fatalf("migrated cert source/status = %q/%q, want manual/ready", cert.Source, cert.Status)
	}
}

func TestMigrationUpgradesLegacyPublicRoutesForRedirects(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "legacy-routes.db")
	raw, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatalf("open raw legacy db: %v", err)
	}
	legacySchema := `
	CREATE TABLE public_backends (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL UNIQUE,
		target_origin TEXT NOT NULL,
		enabled INTEGER NOT NULL DEFAULT 1,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);
	CREATE TABLE public_listeners (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL UNIQUE,
		bind_address TEXT NOT NULL DEFAULT '',
		port INTEGER NOT NULL,
		protocol TEXT NOT NULL,
		enabled INTEGER NOT NULL DEFAULT 1,
		default_backend_id INTEGER NOT NULL REFERENCES public_backends(id),
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(bind_address, port)
	);
	CREATE TABLE public_routes (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		listener_id INTEGER NOT NULL REFERENCES public_listeners(id) ON DELETE CASCADE,
		priority INTEGER NOT NULL,
		host_pattern TEXT NOT NULL DEFAULT '',
		path_prefix TEXT NOT NULL DEFAULT '',
		backend_id INTEGER NOT NULL REFERENCES public_backends(id),
		enabled INTEGER NOT NULL DEFAULT 1,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);
	CREATE INDEX idx_public_routes_listener_priority
	ON public_routes (listener_id, priority, id);
	INSERT INTO public_backends (name, target_origin) VALUES ('legacy-backend', 'https://example.com');
	INSERT INTO public_listeners (name, port, protocol, default_backend_id) VALUES ('legacy-listener', 8080, 'http', 1);
	INSERT INTO public_routes (listener_id, priority, path_prefix, backend_id, enabled) VALUES (1, 10, '/legacy', 1, 1);
	`
	if _, err := raw.ExecContext(context.Background(), legacySchema); err != nil {
		t.Fatalf("create legacy route schema: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw legacy db: %v", err)
	}

	database, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open migrated legacy db: %v", err)
	}
	defer func() { _ = database.Close() }()

	routes, err := database.ListPublicRoutes(context.Background())
	if err != nil {
		t.Fatalf("list migrated routes: %v", err)
	}
	if len(routes) != 2 {
		t.Fatalf("got %d routes, want explicit route plus generated default route", len(routes))
	}
	route := routes[0]
	defaultRoute := routes[1]
	if route.Action != "forward" || route.RedirectStatusCode != 302 || route.RedirectPreservePathSuffix != 1 || route.RedirectPreserveQuery != 1 {
		t.Fatalf("unexpected migrated redirect defaults: %+v", route)
	}
	routeColumns := tableColumns(t, database, "public_routes")
	for _, column := range []string{"backend_id", "fallback_backend_id", "load_balancing"} {
		if containsString(routeColumns, column) {
			t.Fatalf("public_routes still has legacy column %s after migration: %v", column, routeColumns)
		}
	}
	listenerColumns := tableColumns(t, database, "public_listeners")
	if containsString(listenerColumns, "default_backend_id") {
		t.Fatalf("public_listeners still has default_backend_id after migration: %v", listenerColumns)
	}
	if !indexExists(t, database, "idx_public_routes_listener_priority") {
		t.Fatal("expected idx_public_routes_listener_priority after route migration")
	}
	if route.TargetLoadBalancing != "round_robin" || route.IsDefault != 0 {
		t.Fatalf("unexpected migrated target fields: %+v", route)
	}
	if defaultRoute.IsDefault != 1 {
		t.Fatalf("unexpected generated default route: %+v", defaultRoute)
	}
	targets, err := database.ListPublicRouteTargets(context.Background())
	if err != nil {
		t.Fatalf("list migrated route targets: %v", err)
	}
	if len(targets) != 2 {
		t.Fatalf("got %d route targets, want explicit and default targets: %+v", len(targets), targets)
	}
	if targets[0].RouteID != route.ID || targets[0].Url != "https://example.com" || targets[0].Transport != "direct" || targets[0].TargetType != "proxy" {
		t.Fatalf("unexpected migrated explicit target: %+v", targets[0])
	}
	if targets[1].RouteID != defaultRoute.ID || targets[1].Url != "https://example.com" || targets[1].Transport != "direct" || targets[1].TargetType != "proxy" {
		t.Fatalf("unexpected generated default target: %+v", targets[1])
	}
	for _, table := range []string{"public_backends", "public_backend_agents", "public_backend_headers", "public_backend_upstream_headers", "public_route_backends"} {
		if tableExists(t, database, table) {
			t.Fatalf("legacy table %s still exists after route-target migration", table)
		}
	}
}

func TestPublicRouteTargetUpstreamHeadersRoundTrip(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "p2pstream-test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer func() { _ = database.Close() }()

	listener, err := database.CreatePublicListener(context.Background(), CreatePublicListenerParams{
		Name:        "headers-listener",
		BindAddress: "",
		Port:        18080,
		Protocol:    "http",
		Enabled:     1,
	})
	if err != nil {
		t.Fatalf("create listener: %v", err)
	}
	route, err := database.CreatePublicRoute(context.Background(), CreatePublicRouteParams{
		ListenerID:                 listener.ID,
		Priority:                   10,
		HostPattern:                "",
		PathPrefix:                 "/",
		TargetLoadBalancing:        "round_robin",
		IsDefault:                  1,
		Action:                     "forward",
		RedirectTargetMode:         "",
		RedirectTarget:             "",
		RedirectStatusCode:         302,
		RedirectPreservePathSuffix: 1,
		RedirectPreserveQuery:      1,
		Enabled:                    1,
	})
	if err != nil {
		t.Fatalf("create route: %v", err)
	}
	target, err := database.CreatePublicRouteTarget(context.Background(), CreatePublicRouteTargetParams{
		RouteID:                             route.ID,
		Name:                                "upstream-headers",
		Position:                            0,
		PriorityGroup:                       0,
		Weight:                              100,
		Enabled:                             1,
		TargetType:                          "proxy",
		Url:                                 "http://example.com",
		Transport:                           "direct",
		AgentSelectorJson:                   "{}",
		AgentLoadBalancing:                  "round_robin",
		TlsSkipVerify:                       0,
		UpstreamBasicAuthEnabled:            0,
		UpstreamBasicAuthUsername:           "",
		UpstreamBasicAuthPassword:           "",
		UpstreamResponseHeaderTimeoutMillis: 60000,
		HealthCheckEnabled:                  0,
		HealthCheckMethod:                   "GET",
		HealthCheckPath:                     "/",
		HealthCheckIntervalMillis:           10000,
		HealthCheckTimeoutMillis:            2000,
		HealthCheckHealthyThreshold:         2,
		HealthCheckUnhealthyThreshold:       2,
		HealthCheckExpectedStatusMin:        200,
		HealthCheckExpectedStatusMax:        399,
		StaticStatusCode:                    200,
		StaticResponseBody:                  "",
		StaticResponseBodyMode:              "inline",
		StaticResponseTemplateID:            sql.NullInt64{},
	})
	if err != nil {
		t.Fatalf("create target: %v", err)
	}
	first, err := database.CreatePublicRouteTargetUpstreamHeader(context.Background(), CreatePublicRouteTargetUpstreamHeaderParams{
		TargetID:  target.ID,
		Position:  0,
		Name:      "X-Upstream-One",
		Value:     "one",
		Sensitive: 0,
	})
	if err != nil {
		t.Fatalf("create upstream header: %v", err)
	}
	second, err := database.CreatePublicRouteTargetUpstreamHeader(context.Background(), CreatePublicRouteTargetUpstreamHeaderParams{
		TargetID:  target.ID,
		Position:  1,
		Name:      "Authorization",
		Value:     "Bearer secret",
		Sensitive: 1,
	})
	if err != nil {
		t.Fatalf("create sensitive upstream header: %v", err)
	}

	byTarget, err := database.ListPublicRouteTargetUpstreamHeadersByTarget(context.Background(), target.ID)
	if err != nil {
		t.Fatalf("list upstream headers by target: %v", err)
	}
	if len(byTarget) != 2 || byTarget[0].ID != first.ID || byTarget[1].ID != second.ID {
		t.Fatalf("unexpected upstream headers by target: %+v", byTarget)
	}
	all, err := database.ListPublicRouteTargetUpstreamHeaders(context.Background())
	if err != nil {
		t.Fatalf("list upstream headers: %v", err)
	}
	if len(all) != 2 || all[0].Name != "X-Upstream-One" || all[1].Sensitive != 1 {
		t.Fatalf("unexpected upstream headers: %+v", all)
	}
	if err := database.DeletePublicRouteTargetUpstreamHeaders(context.Background(), target.ID); err != nil {
		t.Fatalf("delete upstream headers: %v", err)
	}
	empty, err := database.ListPublicRouteTargetUpstreamHeadersByTarget(context.Background(), target.ID)
	if err != nil {
		t.Fatalf("list deleted upstream headers: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("expected deleted upstream headers, got %+v", empty)
	}
}

func publicRoutesColumnNotNull(t *testing.T, database *DB, column string) bool {
	t.Helper()
	rows, err := database.QueryContext(context.Background(), `PRAGMA table_info(public_routes)`)
	if err != nil {
		t.Fatalf("pragma table_info(public_routes): %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int64
		var name string
		var columnType string
		var notNull int64
		var defaultValue sql.NullString
		var primaryKey int64
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatalf("scan public_routes table_info: %v", err)
		}
		if name == column {
			return notNull != 0
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read public_routes table_info: %v", err)
	}
	t.Fatalf("public_routes missing column %s", column)
	return false
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

func tableExists(t *testing.T, database *DB, table string) bool {
	t.Helper()
	var count int64
	if err := database.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&count); err != nil {
		t.Fatalf("check table %s exists: %v", table, err)
	}
	return count > 0
}

func countRows(t *testing.T, database *DB, query string, args ...any) int64 {
	t.Helper()
	var count int64
	if err := database.QueryRowContext(context.Background(), query, args...).Scan(&count); err != nil {
		t.Fatalf("count rows with %q: %v", query, err)
	}
	return count
}

func assertForeignKeyCheck(t *testing.T, database *DB) {
	t.Helper()
	rows, err := database.QueryContext(context.Background(), `PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatalf("foreign key check: %v", err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("foreign key check returned violations")
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read foreign key check rows: %v", err)
	}
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

func assertDBMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %o, want %o", path, got, want)
	}
}

func TestOpenConfiguresWALJournalMode(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "p2pstream-test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer func() { _ = database.Close() }()

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
