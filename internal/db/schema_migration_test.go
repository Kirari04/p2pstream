package db

import (
	"context"
	"path/filepath"
	"testing"
)

func TestMigrationCreatesMultiAgentRoutingSchema(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "p2pstream-test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer func() { _ = database.Close() }()

	for _, table := range []string{"agents", "public_agent_labels", "public_route_targets", "public_route_target_upstream_headers", "public_route_target_response_headers", "public_waf_captcha_providers", "public_waf_rules", "public_waf_settings", "public_cache_settings", "public_cache_rules", "public_cache_entries", "proxy_request_rollup_minutes", "proxy_request_tuple_rollup_minutes", "proxy_request_status_rollup_minutes", "proxy_retry_rollup_minutes", "agent_stat_rollup_minutes"} {
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
	for _, column := range []string{"request_bytes", "response_bytes", "waf_rule_id", "waf_action", "cache_rule_id", "cache_status", "cache_bytes", "method", "host", "path_prefix", "retry_rule_id", "retry_count", "retry_outcome", "retry_error_kind", "retry_failed_agent_id"} {
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
	agentColumns := tableColumns(t, database, "agents")
	for _, column := range []string{"agent_version", "agent_commit"} {
		if !containsString(agentColumns, column) {
			t.Fatalf("agents missing column %s in %v", column, agentColumns)
		}
	}
	for _, index := range []string{
		"idx_proxy_request_events_route_id",
		"idx_proxy_request_events_agent_id",
		"idx_proxy_request_events_recent_problem",
		"idx_proxy_request_events_waf_rule_id",
		"idx_proxy_request_events_cache_rule_id",
		"idx_proxy_retry_rollup_rule",
		"idx_proxy_retry_rollup_failed_agent",
		"idx_proxy_retry_rollup_error_kind",
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
	if containsString(cacheRuleColumns, "allow_cookie_requests") {
		t.Fatalf("public_cache_rules still has allow_cookie_requests in %v", cacheRuleColumns)
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
