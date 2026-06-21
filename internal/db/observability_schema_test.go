package db

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

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
	if countRows(t, database, `SELECT COUNT(*) FROM proxy_request_status_rollup_minutes`) != 0 {
		t.Fatal("expected proxy request status rollups to be reset")
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
		countRows(t, database, `SELECT COUNT(*) FROM proxy_request_tuple_rollup_minutes`) != 0 ||
		countRows(t, database, `SELECT COUNT(*) FROM proxy_request_status_rollup_minutes`) != 0 {
		t.Fatal("expected proxy observability reset migration to be idempotent")
	}
	if countRows(t, database, `SELECT COUNT(*) FROM agent_stats`) != 1 {
		t.Fatal("expected agent stats to remain after idempotent migration")
	}
	assertForeignKeyCheck(t, database)
}
