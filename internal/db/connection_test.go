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

	for _, table := range []string{"agents", "public_backend_agents", "public_backend_upstream_headers", "public_waf_captcha_providers", "public_waf_rules", "public_waf_settings", "public_cache_settings", "public_cache_rules", "public_cache_entries"} {
		var name string
		if err := database.QueryRowContext(context.Background(), `SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&name); err != nil {
			t.Fatalf("expected table %s: %v", table, err)
		}
	}

	proxyEventColumns := tableColumns(t, database, "proxy_request_events")
	for _, column := range []string{"request_bytes", "response_bytes", "waf_rule_id", "waf_action", "cache_rule_id", "cache_status", "cache_bytes"} {
		if !containsString(proxyEventColumns, column) {
			t.Fatalf("proxy_request_events missing column %s in %v", column, proxyEventColumns)
		}
	}
	agentStatColumns := tableColumns(t, database, "agent_stats")
	if !containsString(agentStatColumns, "cpu_percent") {
		t.Fatalf("agent_stats missing cpu_percent in %v", agentStatColumns)
	}
	for _, index := range []string{
		"idx_proxy_request_events_backend_id",
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

	backendColumns := tableColumns(t, database, "public_backends")
	for _, column := range []string{"forward_mode", "load_balancing", "upstream_basic_auth_enabled", "upstream_basic_auth_username", "upstream_basic_auth_password", "upstream_response_header_timeout_millis"} {
		if !containsString(backendColumns, column) {
			t.Fatalf("public_backends missing column %s in %v", column, backendColumns)
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
	if backend.UpstreamResponseHeaderTimeoutMillis != 60000 {
		t.Fatalf("backend upstream response header timeout default = %d, want 60000", backend.UpstreamResponseHeaderTimeoutMillis)
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
		"agent_stats":          {"agent_id", "req_internal_error", "cpu_percent"},
		"proxy_request_events": {"agent_id", "listener_id", "backend_id", "route_id", "waf_rule_id", "waf_action", "request_bytes", "response_bytes", "cache_rule_id", "cache_status", "cache_bytes"},
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
	if !indexExists(t, database, "idx_public_waf_rules_priority") {
		t.Fatal("expected idx_public_waf_rules_priority after migration")
	}
	if !indexExists(t, database, "idx_public_waf_rules_captcha_provider_id") {
		t.Fatal("expected idx_public_waf_rules_captcha_provider_id after migration")
	}
	for _, index := range []string{
		"idx_proxy_request_events_backend_id",
		"idx_proxy_request_events_route_id",
		"idx_proxy_request_events_agent_id",
		"idx_proxy_request_events_waf_rule_id",
		"idx_proxy_request_events_cache_rule_id",
	} {
		if !indexExists(t, database, index) {
			t.Fatalf("expected %s after migration", index)
		}
	}
	for _, table := range []string{"public_cache_settings", "public_cache_rules", "public_cache_entries"} {
		var name string
		if err := database.QueryRowContext(context.Background(), `SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&name); err != nil {
			t.Fatalf("expected migrated table %s: %v", table, err)
		}
	}
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
	defer database.Close()

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
	defer database.Close()

	routes, err := database.ListPublicRoutes(context.Background())
	if err != nil {
		t.Fatalf("list migrated routes: %v", err)
	}
	if len(routes) != 1 {
		t.Fatalf("got %d routes, want 1", len(routes))
	}
	route := routes[0]
	if !route.BackendID.Valid || route.BackendID.Int64 != 1 {
		t.Fatalf("migrated backend id = %+v, want 1", route.BackendID)
	}
	if route.Action != "forward" || route.RedirectStatusCode != 302 || route.RedirectPreservePathSuffix != 1 || route.RedirectPreserveQuery != 1 {
		t.Fatalf("unexpected migrated redirect defaults: %+v", route)
	}
	if publicRoutesColumnNotNull(t, database, "backend_id") {
		t.Fatal("public_routes.backend_id should be nullable after redirect migration")
	}
	if !indexExists(t, database, "idx_public_routes_listener_priority") {
		t.Fatal("expected idx_public_routes_listener_priority after route migration")
	}
	routeBackends, err := database.ListPublicRouteBackends(context.Background())
	if err != nil {
		t.Fatalf("list route backends after migration: %v", err)
	}
	if len(routeBackends) != 1 || routeBackends[0].RouteID != route.ID || routeBackends[0].BackendID != 1 || routeBackends[0].Weight != 100 || routeBackends[0].Enabled != 1 {
		t.Fatalf("unexpected migrated route backend assignments: %+v", routeBackends)
	}
	if route.LoadBalancing != "round_robin" || route.FallbackBackendID.Valid {
		t.Fatalf("unexpected migrated route pool fields: %+v", route)
	}
	backendColumns := tableColumns(t, database, "public_backends")
	for _, column := range []string{
		"health_check_enabled",
		"health_check_method",
		"health_check_path",
		"health_check_interval_millis",
		"health_check_timeout_millis",
		"health_check_healthy_threshold",
		"health_check_unhealthy_threshold",
		"health_check_expected_status_min",
		"health_check_expected_status_max",
		"upstream_response_header_timeout_millis",
	} {
		if !containsString(backendColumns, column) {
			t.Fatalf("public_backends missing migrated column %s; columns=%v", column, backendColumns)
		}
	}
	if !indexExists(t, database, "idx_public_route_backends_route_position") || !indexExists(t, database, "idx_public_route_backends_backend_id") {
		t.Fatal("expected public_route_backends indexes after migration")
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
