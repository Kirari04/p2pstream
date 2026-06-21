package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestAdoptEmbeddedMigrationBaselineStampsLegacySchema(t *testing.T) {
	database, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "legacy.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer func() { _ = database.Close() }()

	if _, err := database.Exec(`
		CREATE TABLE agents (id INTEGER PRIMARY KEY AUTOINCREMENT);
		CREATE TABLE proxy_request_events (id INTEGER PRIMARY KEY AUTOINCREMENT);
	`); err != nil {
		t.Fatalf("create legacy sentinels: %v", err)
	}

	if err := adoptEmbeddedMigrationBaseline(database); err != nil {
		t.Fatalf("adopt baseline: %v", err)
	}
	if err := adoptEmbeddedMigrationBaseline(database); err != nil {
		t.Fatalf("adopt baseline again: %v", err)
	}

	rows, err := database.Query(`SELECT version_id FROM goose_db_version WHERE is_applied = 1 ORDER BY version_id`)
	if err != nil {
		t.Fatalf("query goose versions: %v", err)
	}
	defer rows.Close()
	var versions []int64
	for rows.Next() {
		var version int64
		if err := rows.Scan(&version); err != nil {
			t.Fatalf("scan version: %v", err)
		}
		versions = append(versions, version)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate versions: %v", err)
	}
	if len(versions) != 2 || versions[0] != 0 || versions[1] != embeddedMigrationBaselineVersion {
		t.Fatalf("versions = %v, want [0 1]", versions)
	}
}

func TestAdoptEmbeddedMigrationBaselineIgnoresUnrelatedSchema(t *testing.T) {
	database, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "unrelated.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer func() { _ = database.Close() }()

	if _, err := database.Exec(`CREATE TABLE agents (id INTEGER PRIMARY KEY AUTOINCREMENT)`); err != nil {
		t.Fatalf("create unrelated table: %v", err)
	}
	if err := adoptEmbeddedMigrationBaseline(database); err != nil {
		t.Fatalf("adopt baseline: %v", err)
	}

	var count int64
	if err := database.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'goose_db_version'`).Scan(&count); err != nil {
		t.Fatalf("query sqlite_master: %v", err)
	}
	if count != 0 {
		t.Fatalf("goose_db_version table exists for unrelated schema")
	}
}

func TestMigrationCreatesMultiAgentRoutingSchema(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "p2pstream-test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer func() { _ = database.Close() }()

	for _, table := range []string{"agents", "public_agent_labels", "public_route_targets", "public_route_target_upstream_headers", "public_route_target_response_headers", "public_waf_captcha_providers", "public_waf_rules", "public_waf_settings", "public_cache_settings", "public_cache_rules", "public_cache_entries", "proxy_request_rollup_minutes", "proxy_request_tuple_rollup_minutes", "proxy_request_status_rollup_minutes", "agent_stat_rollup_minutes", "observability_rollup_state"} {
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
	for _, column := range []string{"request_bytes", "response_bytes", "waf_rule_id", "waf_action", "cache_rule_id", "cache_status", "cache_bytes", "method", "host", "path_prefix"} {
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
		"idx_proxy_request_events_recent_problem",
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
	if !containsString(routeColumns, "path_security_mode") {
		t.Fatalf("public_routes missing path_security_mode in %v", routeColumns)
	}
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
		"proxy_request_events": {"agent_id", "listener_id", "route_id", "route_target_id", "waf_rule_id", "waf_action", "request_bytes", "response_bytes", "cache_rule_id", "cache_status", "cache_bytes", "method", "host", "path_prefix"},
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
		"idx_proxy_request_events_recent_problem",
		"idx_proxy_request_events_waf_rule_id",
		"idx_proxy_request_events_cache_rule_id",
	} {
		if !indexExists(t, database, index) {
			t.Fatalf("expected %s after migration", index)
		}
	}
	for _, table := range []string{"public_cache_settings", "public_cache_rules", "public_cache_entries", "proxy_request_rollup_minutes", "proxy_request_tuple_rollup_minutes", "proxy_request_status_rollup_minutes", "agent_stat_rollup_minutes", "observability_rollup_state"} {
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
