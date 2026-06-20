package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	_ "github.com/mattn/go-sqlite3" // sqlite driver
	"github.com/rs/zerolog/log"
)

type DB struct {
	*sql.DB
	*Queries
}

// Open initializes the SQLite database connection with high performance pragmas and runs schema migrations.
func Open(databaseURL string) (*DB, error) {
	log.Info().Str("url", databaseURL).Msg("Connecting to SQLite database")

	dsn, err := normalizeSQLiteDSN(databaseURL)
	if err != nil {
		return nil, err
	}
	if err := ensureSQLiteDir(dsn); err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(8)
	db.SetConnMaxLifetime(0)

	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	if _, err := db.ExecContext(context.Background(), `
		PRAGMA journal_mode = WAL;
		PRAGMA synchronous = NORMAL;
		PRAGMA busy_timeout = 10000;
		PRAGMA foreign_keys = ON;
		PRAGMA wal_autocheckpoint = 1000;
	`); err != nil {
		return nil, fmt.Errorf("failed to configure SQLite pragmas: %w", err)
	}

	instance := &DB{
		DB:      db,
		Queries: New(db),
	}

	if err := instance.migrate(); err != nil {
		return nil, fmt.Errorf("failed to run migrations: %w", err)
	}

	// Enforce pragmas for any connections opened after migration.
	if _, err := db.ExecContext(context.Background(), `
		PRAGMA journal_mode = WAL;
		PRAGMA synchronous = NORMAL;
		PRAGMA busy_timeout = 10000;
		PRAGMA foreign_keys = ON;
		PRAGMA wal_autocheckpoint = 1000;
	`); err != nil {
		log.Warn().Err(err).Msg("Failed to enforce some SQLite pragmas")
	}
	if err := hardenSQLiteFiles(dsn); err != nil {
		_ = db.Close()
		return nil, err
	}

	log.Info().Msg("Database connected and configured successfully")
	return instance, nil
}

func normalizeSQLiteDSN(databaseURL string) (string, error) {
	databaseURL = strings.TrimSpace(databaseURL)
	if databaseURL == "" {
		databaseURL = "file:p2pstream.db?mode=rwc"
	}

	if strings.HasPrefix(databaseURL, "file:") {
		prefix, rawQuery, _ := strings.Cut(databaseURL, "?")
		values, err := url.ParseQuery(rawQuery)
		if err != nil {
			return "", fmt.Errorf("invalid sqlite database URL %q: %w", databaseURL, err)
		}
		if values.Get("mode") == "" && prefix != "file::memory:" {
			values.Set("mode", "rwc")
		}
		applySQLitePragmas(values)
		return prefix + "?" + values.Encode(), nil
	}

	values := url.Values{}
	values.Set("mode", "rwc")
	applySQLitePragmas(values)
	return "file:" + databaseURL + "?" + values.Encode(), nil
}

func applySQLitePragmas(values url.Values) {
	values.Set("_journal_mode", "WAL")
	values.Set("_synchronous", "NORMAL")
	values.Set("_busy_timeout", "10000")
	values.Set("_fk", "1")
	values.Set("cache", "private")
}

func ensureSQLiteDir(dsn string) error {
	path, ok := sqliteFilePathFromDSN(dsn)
	if !ok {
		return nil
	}
	dir := filepath.Dir(path)
	if dir == "." || dir == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	return os.Chmod(dir, 0700)
}

func hardenSQLiteFiles(dsn string) error {
	path, ok := sqliteFilePathFromDSN(dsn)
	if !ok {
		return nil
	}
	for _, candidate := range []string{path, path + "-wal", path + "-shm"} {
		if _, err := os.Stat(candidate); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("failed to stat sqlite file %q: %w", candidate, err)
		}
		if err := os.Chmod(candidate, 0600); err != nil {
			return fmt.Errorf("failed to secure sqlite file %q: %w", candidate, err)
		}
	}
	return nil
}

func sqliteFilePathFromDSN(dsn string) (string, bool) {
	if !strings.HasPrefix(dsn, "file:") {
		return "", false
	}
	prefix, rawQuery, _ := strings.Cut(dsn, "?")
	values, err := url.ParseQuery(rawQuery)
	if err == nil && strings.EqualFold(values.Get("mode"), "memory") {
		return "", false
	}
	path := strings.TrimPrefix(prefix, "file:")
	if path == "" || path == ":memory:" || strings.HasPrefix(path, ":memory:") {
		return "", false
	}
	if unescaped, err := url.PathUnescape(path); err == nil {
		path = unescaped
	}
	return filepath.Clean(path), true
}

// migrate runs the initial schema setup.
func (db *DB) migrate() error {
	schema := `
	CREATE TABLE IF NOT EXISTS agents (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		public_id TEXT NOT NULL UNIQUE,
		name TEXT NOT NULL,
		token_hash TEXT NOT NULL,
		enabled INTEGER NOT NULL DEFAULT 1,
		last_connected_at DATETIME,
		last_disconnected_at DATETIME,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS connections (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		agent_id INTEGER REFERENCES agents(id),
		connected_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		disconnected_at DATETIME
	);

	CREATE TABLE IF NOT EXISTS agent_stats (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		agent_id INTEGER REFERENCES agents(id),
		reported_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		memory_mb INTEGER NOT NULL,
		goroutines INTEGER NOT NULL,
		req_success INTEGER NOT NULL,
		req_client_error INTEGER NOT NULL,
		req_server_error INTEGER NOT NULL,
		req_internal_error INTEGER NOT NULL DEFAULT 0,
		bytes_rx INTEGER NOT NULL,
		bytes_tx INTEGER NOT NULL,
		cpu_percent REAL NOT NULL DEFAULT 0
	);

		CREATE TABLE IF NOT EXISTS proxy_request_events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			occurred_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			status_code INTEGER NOT NULL,
		duration_ms INTEGER NOT NULL,
		error_kind TEXT NOT NULL DEFAULT '',
		method TEXT NOT NULL DEFAULT '',
		host TEXT NOT NULL DEFAULT '',
		path_prefix TEXT NOT NULL DEFAULT '',
		listener_id INTEGER,
		route_target_id INTEGER,
		route_id INTEGER,
		waf_rule_id INTEGER,
		waf_action TEXT NOT NULL DEFAULT '',
		agent_id INTEGER REFERENCES agents(id),
		request_bytes INTEGER NOT NULL DEFAULT 0,
		response_bytes INTEGER NOT NULL DEFAULT 0,
		cache_rule_id INTEGER,
			cache_status TEXT NOT NULL DEFAULT '',
			cache_bytes INTEGER NOT NULL DEFAULT 0
		);

		CREATE TABLE IF NOT EXISTS proxy_request_rollup_minutes (
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

		CREATE TABLE IF NOT EXISTS proxy_request_tuple_rollup_minutes (
			bucket_unix_millis INTEGER NOT NULL,
			listener_id INTEGER NOT NULL DEFAULT 0,
			route_target_id INTEGER NOT NULL DEFAULT 0,
			route_id INTEGER NOT NULL DEFAULT 0,
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
		PRIMARY KEY (bucket_unix_millis, listener_id, route_target_id, route_id, agent_id, error_kind, status_class)
	);

		CREATE TABLE IF NOT EXISTS proxy_request_status_rollup_minutes (
			bucket_unix_millis INTEGER NOT NULL,
			status_code INTEGER NOT NULL,
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
			PRIMARY KEY (bucket_unix_millis, status_code)
		);

		CREATE TABLE IF NOT EXISTS agent_stat_rollup_minutes (
			bucket_unix_millis INTEGER PRIMARY KEY,
			samples INTEGER NOT NULL DEFAULT 0,
			req_success INTEGER NOT NULL DEFAULT 0,
			req_client_error INTEGER NOT NULL DEFAULT 0,
			req_server_error INTEGER NOT NULL DEFAULT 0,
			req_internal_error INTEGER NOT NULL DEFAULT 0,
			bytes_rx INTEGER NOT NULL DEFAULT 0,
			bytes_tx INTEGER NOT NULL DEFAULT 0,
			memory_mb_sum INTEGER NOT NULL DEFAULT 0,
			max_memory_mb INTEGER NOT NULL DEFAULT 0,
			goroutines_sum INTEGER NOT NULL DEFAULT 0,
			max_goroutines INTEGER NOT NULL DEFAULT 0,
			cpu_percent_sum REAL NOT NULL DEFAULT 0,
			max_cpu_percent REAL NOT NULL DEFAULT 0,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		);

		CREATE TABLE IF NOT EXISTS public_response_templates (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL UNIQUE,
		kind TEXT NOT NULL,
		description TEXT NOT NULL DEFAULT '',
		content_type TEXT NOT NULL DEFAULT 'text/html; charset=utf-8',
		body TEXT NOT NULL,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS public_agent_labels (
		agent_id INTEGER NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
		key TEXT NOT NULL,
		value TEXT NOT NULL,
		source TEXT NOT NULL DEFAULT 'user',
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (agent_id, key)
	);

	CREATE TABLE IF NOT EXISTS public_listeners (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL UNIQUE,
		bind_address TEXT NOT NULL DEFAULT '',
		port INTEGER NOT NULL,
		protocol TEXT NOT NULL,
		enabled INTEGER NOT NULL DEFAULT 1,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(bind_address, port)
	);

	CREATE TABLE IF NOT EXISTS public_routes (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		listener_id INTEGER NOT NULL REFERENCES public_listeners(id) ON DELETE CASCADE,
		priority INTEGER NOT NULL,
		host_pattern TEXT NOT NULL DEFAULT '',
		path_prefix TEXT NOT NULL DEFAULT '',
		target_load_balancing TEXT NOT NULL DEFAULT 'round_robin',
		is_default INTEGER NOT NULL DEFAULT 0,
		action TEXT NOT NULL DEFAULT 'forward',
		redirect_target_mode TEXT NOT NULL DEFAULT '',
		redirect_target TEXT NOT NULL DEFAULT '',
		redirect_status_code INTEGER NOT NULL DEFAULT 302,
		redirect_preserve_path_suffix INTEGER NOT NULL DEFAULT 1,
		redirect_preserve_query INTEGER NOT NULL DEFAULT 1,
		path_security_mode TEXT NOT NULL DEFAULT 'strict',
		enabled INTEGER NOT NULL DEFAULT 1,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS public_route_targets (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		route_id INTEGER NOT NULL REFERENCES public_routes(id) ON DELETE CASCADE,
		name TEXT NOT NULL DEFAULT '',
		position INTEGER NOT NULL,
		priority_group INTEGER NOT NULL DEFAULT 0,
		weight INTEGER NOT NULL DEFAULT 100,
		enabled INTEGER NOT NULL DEFAULT 1,
		target_type TEXT NOT NULL DEFAULT 'proxy',
		url TEXT NOT NULL DEFAULT '',
		transport TEXT NOT NULL DEFAULT 'direct',
		agent_selector_json TEXT NOT NULL DEFAULT '{}',
		agent_load_balancing TEXT NOT NULL DEFAULT 'round_robin',
		tls_skip_verify INTEGER NOT NULL DEFAULT 0,
		upstream_basic_auth_enabled INTEGER NOT NULL DEFAULT 0,
		upstream_basic_auth_username TEXT NOT NULL DEFAULT '',
		upstream_basic_auth_password TEXT NOT NULL DEFAULT '',
		upstream_response_header_timeout_millis INTEGER NOT NULL DEFAULT 60000,
		health_check_enabled INTEGER NOT NULL DEFAULT 0,
		health_check_method TEXT NOT NULL DEFAULT 'GET',
		health_check_path TEXT NOT NULL DEFAULT '/',
		health_check_interval_millis INTEGER NOT NULL DEFAULT 10000,
		health_check_timeout_millis INTEGER NOT NULL DEFAULT 2000,
		health_check_healthy_threshold INTEGER NOT NULL DEFAULT 2,
		health_check_unhealthy_threshold INTEGER NOT NULL DEFAULT 2,
		health_check_expected_status_min INTEGER NOT NULL DEFAULT 200,
		health_check_expected_status_max INTEGER NOT NULL DEFAULT 399,
		static_status_code INTEGER NOT NULL DEFAULT 200,
		static_response_body TEXT NOT NULL DEFAULT '',
		static_response_body_mode TEXT NOT NULL DEFAULT 'inline',
		static_response_template_id INTEGER REFERENCES public_response_templates(id) ON DELETE RESTRICT,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(route_id, position)
	);

	CREATE TABLE IF NOT EXISTS public_route_target_upstream_headers (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		target_id INTEGER NOT NULL REFERENCES public_route_targets(id) ON DELETE CASCADE,
		position INTEGER NOT NULL,
		name TEXT NOT NULL,
		value TEXT NOT NULL,
		sensitive INTEGER NOT NULL DEFAULT 0,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(target_id, position)
	);

	CREATE TABLE IF NOT EXISTS public_route_target_response_headers (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		target_id INTEGER NOT NULL REFERENCES public_route_targets(id) ON DELETE CASCADE,
		position INTEGER NOT NULL,
		name TEXT NOT NULL,
		value TEXT NOT NULL,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(target_id, position)
	);

	CREATE TABLE IF NOT EXISTS public_waf_captcha_providers (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL UNIQUE,
		provider_type TEXT NOT NULL,
		site_key TEXT NOT NULL,
		secret_key TEXT NOT NULL,
		enabled INTEGER NOT NULL DEFAULT 1,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS public_waf_rules (
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
		trigger_route_target_active_requests INTEGER NOT NULL DEFAULT 100,
		trigger_agent_active_requests INTEGER NOT NULL DEFAULT 50,
		trigger_server_cpu_percent REAL NOT NULL DEFAULT 85,
		trigger_agent_cpu_percent REAL NOT NULL DEFAULT 85,
		trigger_minimum_active_millis INTEGER NOT NULL DEFAULT 30000,
		trigger_quiet_period_millis INTEGER NOT NULL DEFAULT 60000,
		block_response_status_code INTEGER NOT NULL DEFAULT 403,
		block_response_body TEXT NOT NULL DEFAULT 'Request blocked',
		block_response_body_mode TEXT NOT NULL DEFAULT 'inline',
		block_response_template_id INTEGER REFERENCES public_response_templates(id) ON DELETE RESTRICT,
		captcha_page_template_id INTEGER REFERENCES public_response_templates(id) ON DELETE RESTRICT,
		waiting_room_page_template_id INTEGER REFERENCES public_response_templates(id) ON DELETE RESTRICT,
		block_response_content_type TEXT NOT NULL DEFAULT 'text/plain; charset=utf-8',
		block_response_headers_json TEXT NOT NULL DEFAULT '[]',
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS public_waf_settings (
		id INTEGER PRIMARY KEY CHECK (id = 1),
		cookie_signing_secret TEXT NOT NULL,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS public_tls_dns_credentials (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL UNIQUE,
		provider TEXT NOT NULL,
		cloudflare_zone_id TEXT NOT NULL,
		api_token TEXT NOT NULL,
		enabled INTEGER NOT NULL DEFAULT 1,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS public_tls_certificates (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		listener_id INTEGER NOT NULL REFERENCES public_listeners(id) ON DELETE CASCADE,
		hostname_pattern TEXT NOT NULL,
		cert_path TEXT NOT NULL,
		key_path TEXT NOT NULL,
		enabled INTEGER NOT NULL DEFAULT 1,
		source TEXT NOT NULL DEFAULT 'manual',
		acme_challenge_type TEXT NOT NULL DEFAULT '',
		acme_ca TEXT NOT NULL DEFAULT '',
		acme_email TEXT NOT NULL DEFAULT '',
		dns_credential_id INTEGER REFERENCES public_tls_dns_credentials(id),
		status TEXT NOT NULL DEFAULT 'ready',
		last_error TEXT NOT NULL DEFAULT '',
		issued_at DATETIME,
		expires_at DATETIME,
		next_renewal_at DATETIME,
		last_renewal_attempt_at DATETIME,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS users (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		username TEXT NOT NULL UNIQUE,
		password_hash TEXT NOT NULL,
		role TEXT NOT NULL,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		disabled_at DATETIME
	);

	CREATE TABLE IF NOT EXISTS sessions (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		token_hash TEXT NOT NULL UNIQUE,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		last_seen_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		expires_at DATETIME NOT NULL,
		revoked_at DATETIME
	);

	CREATE TABLE IF NOT EXISTS management_access_tokens (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL UNIQUE,
		token_hash TEXT NOT NULL UNIQUE,
		role TEXT NOT NULL DEFAULT 'admin',
		enabled INTEGER NOT NULL DEFAULT 1,
		expires_at DATETIME,
		last_used_at DATETIME,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS environments (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL UNIQUE,
		management_url TEXT NOT NULL,
		transport TEXT NOT NULL DEFAULT 'direct',
		agent_id INTEGER REFERENCES agents(id) ON DELETE SET NULL,
		access_token TEXT NOT NULL,
		trusted_certificate_pem TEXT NOT NULL DEFAULT '',
		trusted_certificate_sha256 TEXT NOT NULL DEFAULT '',
		trusted_certificate_subject TEXT NOT NULL DEFAULT '',
		trusted_certificate_not_after DATETIME,
		last_observed_certificate_pem TEXT NOT NULL DEFAULT '',
		last_observed_certificate_sha256 TEXT NOT NULL DEFAULT '',
		response_header_timeout_millis INTEGER NOT NULL DEFAULT 10000,
		enabled INTEGER NOT NULL DEFAULT 1,
		last_checked_at DATETIME,
		last_error TEXT NOT NULL DEFAULT '',
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);

	CREATE INDEX IF NOT EXISTS idx_environments_agent_id
	ON environments (agent_id);

	CREATE INDEX IF NOT EXISTS idx_proxy_request_events_occurred_at
	ON proxy_request_events (occurred_at);

	CREATE INDEX IF NOT EXISTS idx_public_routes_listener_priority
	ON public_routes (listener_id, priority, id);

	CREATE INDEX IF NOT EXISTS idx_public_agent_labels_key_value
	ON public_agent_labels (key, value);

	CREATE INDEX IF NOT EXISTS idx_public_route_targets_route_position
	ON public_route_targets (route_id, position);

	CREATE INDEX IF NOT EXISTS idx_public_route_targets_route_group
	ON public_route_targets (route_id, priority_group, position);

	CREATE INDEX IF NOT EXISTS idx_public_route_target_upstream_headers_target_position
	ON public_route_target_upstream_headers (target_id, position);

	CREATE INDEX IF NOT EXISTS idx_public_route_target_response_headers_target_position
	ON public_route_target_response_headers (target_id, position);

	CREATE INDEX IF NOT EXISTS idx_public_waf_rules_priority
	ON public_waf_rules (priority, id);

	CREATE INDEX IF NOT EXISTS idx_public_waf_rules_captcha_provider_id
	ON public_waf_rules (captcha_provider_id);

	CREATE INDEX IF NOT EXISTS idx_public_tls_certificates_listener_id
	ON public_tls_certificates (listener_id);

	CREATE INDEX IF NOT EXISTS idx_agent_stats_reported_at
	ON agent_stats (reported_at);

	CREATE INDEX IF NOT EXISTS idx_connections_connected_at
	ON connections (connected_at);
	`
	if _, err := db.Exec(schema); err != nil {
		return err
	}

	_, err := db.Exec(`ALTER TABLE agent_stats ADD COLUMN req_internal_error INTEGER NOT NULL DEFAULT 0`)
	if err != nil && !strings.Contains(err.Error(), "duplicate column name") {
		return err
	}
	for _, stmt := range []string{
		`ALTER TABLE connections ADD COLUMN agent_id INTEGER REFERENCES agents(id)`,
		`ALTER TABLE agent_stats ADD COLUMN agent_id INTEGER REFERENCES agents(id)`,
		`ALTER TABLE agent_stats ADD COLUMN cpu_percent REAL NOT NULL DEFAULT 0`,
		`ALTER TABLE proxy_request_events ADD COLUMN listener_id INTEGER`,
		`ALTER TABLE proxy_request_events ADD COLUMN route_target_id INTEGER`,
		`ALTER TABLE proxy_request_events ADD COLUMN route_id INTEGER`,
		`ALTER TABLE proxy_request_events ADD COLUMN waf_rule_id INTEGER`,
		`ALTER TABLE proxy_request_events ADD COLUMN waf_action TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE proxy_request_events ADD COLUMN agent_id INTEGER REFERENCES agents(id)`,
		`ALTER TABLE proxy_request_events ADD COLUMN request_bytes INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE proxy_request_events ADD COLUMN response_bytes INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE proxy_request_events ADD COLUMN cache_rule_id INTEGER`,
		`ALTER TABLE proxy_request_events ADD COLUMN cache_status TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE proxy_request_events ADD COLUMN cache_bytes INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE proxy_request_events ADD COLUMN method TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE proxy_request_events ADD COLUMN host TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE proxy_request_events ADD COLUMN path_prefix TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE public_routes ADD COLUMN target_load_balancing TEXT NOT NULL DEFAULT 'round_robin'`,
		`ALTER TABLE public_routes ADD COLUMN is_default INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE public_routes ADD COLUMN path_security_mode TEXT NOT NULL DEFAULT 'strict'`,
		`ALTER TABLE public_tls_certificates ADD COLUMN source TEXT NOT NULL DEFAULT 'manual'`,
		`ALTER TABLE public_tls_certificates ADD COLUMN acme_challenge_type TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE public_tls_certificates ADD COLUMN acme_ca TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE public_tls_certificates ADD COLUMN acme_email TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE public_tls_certificates ADD COLUMN dns_credential_id INTEGER REFERENCES public_tls_dns_credentials(id)`,
		`ALTER TABLE public_tls_certificates ADD COLUMN status TEXT NOT NULL DEFAULT 'ready'`,
		`ALTER TABLE public_tls_certificates ADD COLUMN last_error TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE public_tls_certificates ADD COLUMN issued_at DATETIME`,
		`ALTER TABLE public_tls_certificates ADD COLUMN expires_at DATETIME`,
		`ALTER TABLE public_tls_certificates ADD COLUMN next_renewal_at DATETIME`,
		`ALTER TABLE public_tls_certificates ADD COLUMN last_renewal_attempt_at DATETIME`,
	} {
		if _, err := db.Exec(stmt); err != nil && !strings.Contains(err.Error(), "duplicate column name") {
			return err
		}
	}
	if err := db.migrateObservabilityRollups(); err != nil {
		return err
	}
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS public_tls_dns_credentials (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL UNIQUE,
			provider TEXT NOT NULL,
			cloudflare_zone_id TEXT NOT NULL,
			api_token TEXT NOT NULL,
			enabled INTEGER NOT NULL DEFAULT 1,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)
	`); err != nil {
		return err
	}
	if err := db.migratePublicRoutesRedirectSchema(); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_proxy_request_events_listener_id ON proxy_request_events (listener_id)`); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_proxy_request_events_route_target_id ON proxy_request_events (route_target_id)`); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_proxy_request_events_route_id ON proxy_request_events (route_id)`); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_proxy_request_events_agent_id ON proxy_request_events (agent_id)`); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_proxy_request_events_recent_problem ON proxy_request_events (occurred_at DESC) WHERE status_code >= 400 OR error_kind != ''`); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_agent_stats_agent_id ON agent_stats (agent_id)`); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_connections_agent_id ON connections (agent_id)`); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_connections_disconnected_at ON connections (disconnected_at)`); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_public_tls_certificates_dns_credential_id ON public_tls_certificates (dns_credential_id)`); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_public_routes_one_default_per_listener ON public_routes (listener_id) WHERE is_default = 1`); err != nil {
		return err
	}
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS public_agent_labels (
			agent_id INTEGER NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
			key TEXT NOT NULL,
			value TEXT NOT NULL,
			source TEXT NOT NULL DEFAULT 'user',
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (agent_id, key)
		)
	`); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_public_agent_labels_key_value ON public_agent_labels (key, value)`); err != nil {
		return err
	}
	if err := db.backfillPublicAgentSystemLabels(); err != nil {
		return err
	}
	if err := db.prepareLegacyPublicBackendMigration(); err != nil {
		return err
	}
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS public_route_targets (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			route_id INTEGER NOT NULL REFERENCES public_routes(id) ON DELETE CASCADE,
			name TEXT NOT NULL DEFAULT '',
			position INTEGER NOT NULL,
			priority_group INTEGER NOT NULL DEFAULT 0,
			weight INTEGER NOT NULL DEFAULT 100,
			enabled INTEGER NOT NULL DEFAULT 1,
			target_type TEXT NOT NULL DEFAULT 'proxy',
			url TEXT NOT NULL DEFAULT '',
			transport TEXT NOT NULL DEFAULT 'direct',
			agent_selector_json TEXT NOT NULL DEFAULT '{}',
			agent_load_balancing TEXT NOT NULL DEFAULT 'round_robin',
			tls_skip_verify INTEGER NOT NULL DEFAULT 0,
			upstream_basic_auth_enabled INTEGER NOT NULL DEFAULT 0,
			upstream_basic_auth_username TEXT NOT NULL DEFAULT '',
			upstream_basic_auth_password TEXT NOT NULL DEFAULT '',
			upstream_response_header_timeout_millis INTEGER NOT NULL DEFAULT 60000,
			health_check_enabled INTEGER NOT NULL DEFAULT 0,
			health_check_method TEXT NOT NULL DEFAULT 'GET',
			health_check_path TEXT NOT NULL DEFAULT '/',
			health_check_interval_millis INTEGER NOT NULL DEFAULT 10000,
			health_check_timeout_millis INTEGER NOT NULL DEFAULT 2000,
			health_check_healthy_threshold INTEGER NOT NULL DEFAULT 2,
			health_check_unhealthy_threshold INTEGER NOT NULL DEFAULT 2,
			health_check_expected_status_min INTEGER NOT NULL DEFAULT 200,
			health_check_expected_status_max INTEGER NOT NULL DEFAULT 399,
			static_status_code INTEGER NOT NULL DEFAULT 200,
			static_response_body TEXT NOT NULL DEFAULT '',
			static_response_body_mode TEXT NOT NULL DEFAULT 'inline',
			static_response_template_id INTEGER REFERENCES public_response_templates(id) ON DELETE RESTRICT,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(route_id, position)
		)
	`); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_public_route_targets_route_position ON public_route_targets (route_id, position)`); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_public_route_targets_route_group ON public_route_targets (route_id, priority_group, position)`); err != nil {
		return err
	}
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS public_route_target_upstream_headers (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			target_id INTEGER NOT NULL REFERENCES public_route_targets(id) ON DELETE CASCADE,
			position INTEGER NOT NULL,
			name TEXT NOT NULL,
			value TEXT NOT NULL,
			sensitive INTEGER NOT NULL DEFAULT 0,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(target_id, position)
		)
	`); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_public_route_target_upstream_headers_target_position ON public_route_target_upstream_headers (target_id, position)`); err != nil {
		return err
	}
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS public_route_target_response_headers (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			target_id INTEGER NOT NULL REFERENCES public_route_targets(id) ON DELETE CASCADE,
			position INTEGER NOT NULL,
			name TEXT NOT NULL,
			value TEXT NOT NULL,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(target_id, position)
		)
	`); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_public_route_target_response_headers_target_position ON public_route_target_response_headers (target_id, position)`); err != nil {
		return err
	}
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS public_response_templates (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL UNIQUE,
			kind TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			content_type TEXT NOT NULL DEFAULT 'text/html; charset=utf-8',
			body TEXT NOT NULL,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)
	`); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_public_response_templates_kind ON public_response_templates (kind, name)`); err != nil {
		return err
	}
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS public_rate_limit_rules (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL UNIQUE,
			priority INTEGER NOT NULL DEFAULT 100,
			enabled INTEGER NOT NULL DEFAULT 1,
			algorithm TEXT NOT NULL,
			limit_count INTEGER NOT NULL,
			window_millis INTEGER NOT NULL,
			burst INTEGER NOT NULL DEFAULT 0,
			match_json TEXT NOT NULL DEFAULT '{}',
			key_parts_json TEXT NOT NULL DEFAULT '[]',
			response_status_code INTEGER NOT NULL DEFAULT 429,
			response_body TEXT NOT NULL DEFAULT 'Rate limit exceeded
',
			response_body_mode TEXT NOT NULL DEFAULT 'inline',
			response_body_template_id INTEGER REFERENCES public_response_templates(id) ON DELETE RESTRICT,
			response_content_type TEXT NOT NULL DEFAULT 'text/plain; charset=utf-8',
			response_headers_json TEXT NOT NULL DEFAULT '[]',
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)
	`); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_public_rate_limit_rules_priority ON public_rate_limit_rules (priority, id)`); err != nil {
		return err
	}
	for _, stmt := range []string{
		`ALTER TABLE public_rate_limit_rules ADD COLUMN response_body_mode TEXT NOT NULL DEFAULT 'inline'`,
		`ALTER TABLE public_rate_limit_rules ADD COLUMN response_body_template_id INTEGER REFERENCES public_response_templates(id) ON DELETE RESTRICT`,
	} {
		if _, err := db.Exec(stmt); err != nil && !strings.Contains(err.Error(), "duplicate column name") {
			return err
		}
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_public_rate_limit_rules_response_body_template_id ON public_rate_limit_rules (response_body_template_id)`); err != nil {
		return err
	}
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS public_traffic_shaper_rules (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL UNIQUE,
			priority INTEGER NOT NULL DEFAULT 100,
			enabled INTEGER NOT NULL DEFAULT 1,
			budget_scope TEXT NOT NULL DEFAULT 'per_key',
			upload_bytes_per_second INTEGER NOT NULL DEFAULT 0,
			download_bytes_per_second INTEGER NOT NULL DEFAULT 0,
			burst_bytes INTEGER NOT NULL DEFAULT 0,
			request_exempt_bytes INTEGER NOT NULL DEFAULT 0,
			response_exempt_bytes INTEGER NOT NULL DEFAULT 0,
			match_json TEXT NOT NULL DEFAULT '{}',
			key_parts_json TEXT NOT NULL DEFAULT '[]',
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)
	`); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_public_traffic_shaper_rules_priority ON public_traffic_shaper_rules (priority, id)`); err != nil {
		return err
	}
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS public_waf_captcha_providers (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL UNIQUE,
			provider_type TEXT NOT NULL,
			site_key TEXT NOT NULL,
			secret_key TEXT NOT NULL,
			enabled INTEGER NOT NULL DEFAULT 1,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)
	`); err != nil {
		return err
	}
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS public_waf_rules (
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
			trigger_route_target_active_requests INTEGER NOT NULL DEFAULT 100,
			trigger_agent_active_requests INTEGER NOT NULL DEFAULT 50,
			trigger_server_cpu_percent REAL NOT NULL DEFAULT 85,
			trigger_agent_cpu_percent REAL NOT NULL DEFAULT 85,
			trigger_minimum_active_millis INTEGER NOT NULL DEFAULT 30000,
			trigger_quiet_period_millis INTEGER NOT NULL DEFAULT 60000,
			block_response_status_code INTEGER NOT NULL DEFAULT 403,
			block_response_body TEXT NOT NULL DEFAULT 'Request blocked',
			block_response_body_mode TEXT NOT NULL DEFAULT 'inline',
			block_response_template_id INTEGER REFERENCES public_response_templates(id) ON DELETE RESTRICT,
			captcha_page_template_id INTEGER REFERENCES public_response_templates(id) ON DELETE RESTRICT,
			waiting_room_page_template_id INTEGER REFERENCES public_response_templates(id) ON DELETE RESTRICT,
			block_response_content_type TEXT NOT NULL DEFAULT 'text/plain; charset=utf-8',
			block_response_headers_json TEXT NOT NULL DEFAULT '[]',
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)
	`); err != nil {
		return err
	}
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS public_waf_settings (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			cookie_signing_secret TEXT NOT NULL,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)
	`); err != nil {
		return err
	}
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS public_cache_settings (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			enabled INTEGER NOT NULL DEFAULT 1,
			max_disk_bytes INTEGER NOT NULL DEFAULT 1073741824,
			max_memory_bytes INTEGER NOT NULL DEFAULT 134217728,
			memory_hot_object_max_bytes INTEGER NOT NULL DEFAULT 262144,
			max_entries INTEGER NOT NULL DEFAULT 100000,
			cleanup_interval_millis INTEGER NOT NULL DEFAULT 60000,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)
	`); err != nil {
		return err
	}
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS public_cache_rules (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL UNIQUE,
			priority INTEGER NOT NULL DEFAULT 100,
			enabled INTEGER NOT NULL DEFAULT 1,
			match_json TEXT NOT NULL DEFAULT '{}',
			route_ids_json TEXT NOT NULL DEFAULT '[]',
			target_ids_json TEXT NOT NULL DEFAULT '[]',
			scope TEXT NOT NULL DEFAULT 'selected_backend',
			ttl_mode TEXT NOT NULL DEFAULT 'fixed',
			ttl_millis INTEGER NOT NULL DEFAULT 3600000,
			query_mode TEXT NOT NULL DEFAULT 'full',
			query_params_json TEXT NOT NULL DEFAULT '[]',
			vary_headers_json TEXT NOT NULL DEFAULT '["Accept-Encoding"]',
			cache_status_codes_json TEXT NOT NULL DEFAULT '[200,203,204,301,308]',
			max_object_bytes INTEGER NOT NULL DEFAULT 104857600,
			add_cache_status_header INTEGER NOT NULL DEFAULT 1,
			allow_cookie_requests INTEGER NOT NULL DEFAULT 0,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)
	`); err != nil {
		return err
	}
	if _, err := db.Exec(`ALTER TABLE public_cache_rules ADD COLUMN allow_cookie_requests INTEGER NOT NULL DEFAULT 0`); err != nil && !strings.Contains(err.Error(), "duplicate column name") {
		return err
	}
	if _, err := db.Exec(`ALTER TABLE public_cache_rules ADD COLUMN target_ids_json TEXT NOT NULL DEFAULT '[]'`); err != nil && !strings.Contains(err.Error(), "duplicate column name") {
		return err
	}
	wafRuleColumns, err := db.sqliteTableColumns("public_waf_rules")
	if err != nil {
		return err
	}
	hasOldWAFRouteTargetTrigger := sqliteColumnExists(wafRuleColumns, "trigger_backend_active_requests")
	hasWAFRouteTargetTrigger := sqliteColumnExists(wafRuleColumns, "trigger_route_target_active_requests")
	switch {
	case hasOldWAFRouteTargetTrigger && !hasWAFRouteTargetTrigger:
		if _, err := db.Exec(`ALTER TABLE public_waf_rules RENAME COLUMN trigger_backend_active_requests TO trigger_route_target_active_requests`); err != nil {
			return err
		}
	case !hasWAFRouteTargetTrigger:
		if _, err := db.Exec(`ALTER TABLE public_waf_rules ADD COLUMN trigger_route_target_active_requests INTEGER NOT NULL DEFAULT 100`); err != nil && !strings.Contains(err.Error(), "duplicate column name") {
			return err
		}
	}
	for _, stmt := range []string{
		`ALTER TABLE public_waf_rules ADD COLUMN block_response_body_mode TEXT NOT NULL DEFAULT 'inline'`,
		`ALTER TABLE public_waf_rules ADD COLUMN block_response_template_id INTEGER REFERENCES public_response_templates(id) ON DELETE RESTRICT`,
		`ALTER TABLE public_waf_rules ADD COLUMN captcha_page_template_id INTEGER REFERENCES public_response_templates(id) ON DELETE RESTRICT`,
		`ALTER TABLE public_waf_rules ADD COLUMN waiting_room_page_template_id INTEGER REFERENCES public_response_templates(id) ON DELETE RESTRICT`,
	} {
		if _, err := db.Exec(stmt); err != nil && !strings.Contains(err.Error(), "duplicate column name") {
			return err
		}
	}
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS public_cache_entries (
			key_digest TEXT PRIMARY KEY,
			rule_id INTEGER NOT NULL REFERENCES public_cache_rules(id) ON DELETE CASCADE,
			scope TEXT NOT NULL,
			listener_protocol TEXT NOT NULL,
			host TEXT NOT NULL,
			path TEXT NOT NULL,
			query_key TEXT NOT NULL,
			route_id INTEGER,
			route_target_id INTEGER,
			method TEXT NOT NULL DEFAULT 'GET',
			vary_headers_json TEXT NOT NULL DEFAULT '[]',
			response_headers_json TEXT NOT NULL DEFAULT '[]',
			status_code INTEGER NOT NULL,
			body_path TEXT NOT NULL,
			size_bytes INTEGER NOT NULL,
			stored_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			expires_at DATETIME NOT NULL,
			last_accessed_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			hit_count INTEGER NOT NULL DEFAULT 0
		)
	`); err != nil {
		return err
	}
	if _, err := db.Exec(`ALTER TABLE public_cache_entries ADD COLUMN route_target_id INTEGER`); err != nil && !strings.Contains(err.Error(), "duplicate column name") {
		return err
	}
	if err := db.backfillPublicRouteTargets(); err != nil {
		return err
	}
	if err := db.migrateDropLegacyPublicBackendConfig(); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_public_waf_rules_priority ON public_waf_rules (priority, id)`); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_public_waf_rules_captcha_provider_id ON public_waf_rules (captcha_provider_id)`); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_public_waf_rules_block_response_template_id ON public_waf_rules (block_response_template_id)`); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_public_waf_rules_captcha_page_template_id ON public_waf_rules (captcha_page_template_id)`); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_public_waf_rules_waiting_room_page_template_id ON public_waf_rules (waiting_room_page_template_id)`); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_proxy_request_events_waf_rule_id ON proxy_request_events (waf_rule_id)`); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_proxy_request_events_cache_rule_id ON proxy_request_events (cache_rule_id)`); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_public_cache_rules_priority ON public_cache_rules (priority, id)`); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_public_cache_entries_rule_id ON public_cache_entries (rule_id)`); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_public_cache_entries_route_target_id ON public_cache_entries (route_target_id)`); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_public_cache_entries_expires_at ON public_cache_entries (expires_at)`); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_public_cache_entries_last_accessed_at ON public_cache_entries (last_accessed_at)`); err != nil {
		return err
	}
	if err := db.migrateLegacyPolicyMatchJSON(); err != nil {
		return err
	}
	return nil
}

func (db *DB) migrateObservabilityRollups() error {
	_, err := db.Exec(`
	CREATE TABLE IF NOT EXISTS proxy_request_rollup_minutes (
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

	CREATE TABLE IF NOT EXISTS proxy_request_tuple_rollup_minutes (
		bucket_unix_millis INTEGER NOT NULL,
		listener_id INTEGER NOT NULL DEFAULT 0,
		route_target_id INTEGER NOT NULL DEFAULT 0,
		route_id INTEGER NOT NULL DEFAULT 0,
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
		PRIMARY KEY (bucket_unix_millis, listener_id, route_target_id, route_id, agent_id, error_kind, status_class)
	);

	CREATE TABLE IF NOT EXISTS proxy_request_status_rollup_minutes (
		bucket_unix_millis INTEGER NOT NULL,
		status_code INTEGER NOT NULL,
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
		PRIMARY KEY (bucket_unix_millis, status_code)
	);

	CREATE TABLE IF NOT EXISTS agent_stat_rollup_minutes (
		bucket_unix_millis INTEGER PRIMARY KEY,
		samples INTEGER NOT NULL DEFAULT 0,
		req_success INTEGER NOT NULL DEFAULT 0,
		req_client_error INTEGER NOT NULL DEFAULT 0,
		req_server_error INTEGER NOT NULL DEFAULT 0,
		req_internal_error INTEGER NOT NULL DEFAULT 0,
		bytes_rx INTEGER NOT NULL DEFAULT 0,
		bytes_tx INTEGER NOT NULL DEFAULT 0,
		memory_mb_sum INTEGER NOT NULL DEFAULT 0,
		max_memory_mb INTEGER NOT NULL DEFAULT 0,
		goroutines_sum INTEGER NOT NULL DEFAULT 0,
		max_goroutines INTEGER NOT NULL DEFAULT 0,
		cpu_percent_sum REAL NOT NULL DEFAULT 0,
		max_cpu_percent REAL NOT NULL DEFAULT 0,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS observability_rollup_state (
		id INTEGER PRIMARY KEY CHECK (id = 1),
		proxy_backfill_upper_id INTEGER NOT NULL DEFAULT 0,
		proxy_backfilled_through_id INTEGER NOT NULL DEFAULT 0,
		agent_backfill_upper_id INTEGER NOT NULL DEFAULT 0,
		agent_backfilled_through_id INTEGER NOT NULL DEFAULT 0,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);

	INSERT INTO observability_rollup_state (
		id, proxy_backfill_upper_id, proxy_backfilled_through_id, agent_backfill_upper_id, agent_backfilled_through_id
	)
	SELECT
		1,
		CAST(COALESCE((SELECT MAX(id) FROM proxy_request_events), 0) AS INTEGER),
		0,
		CAST(COALESCE((SELECT MAX(id) FROM agent_stats), 0) AS INTEGER),
		0
	WHERE NOT EXISTS (SELECT 1 FROM observability_rollup_state WHERE id = 1);
	`)
	if err != nil {
		return err
	}
	var statusRollupRows, proxyBackfilledThroughID, proxyBackfillUpperID int64
	if err := db.QueryRow(`
		SELECT
			(SELECT COUNT(*) FROM proxy_request_status_rollup_minutes),
			proxy_backfilled_through_id,
			proxy_backfill_upper_id
		FROM observability_rollup_state
		WHERE id = 1
	`).Scan(&statusRollupRows, &proxyBackfilledThroughID, &proxyBackfillUpperID); err != nil {
		return err
	}
	if statusRollupRows == 0 && proxyBackfilledThroughID >= proxyBackfillUpperID {
		if _, err := db.Exec(`
			INSERT INTO proxy_request_status_rollup_minutes (
				bucket_unix_millis, status_code, requests, success, client_error, server_error,
				internal_error, duration_ms_sum, request_bytes, response_bytes
			)
			SELECT
				CAST((unixepoch(occurred_at) / 60) * 60 * 1000 AS INTEGER) AS bucket_unix_millis,
				status_code,
				COUNT(*) AS requests,
				CAST(COALESCE(SUM(CASE WHEN status_code >= 200 AND status_code < 400 THEN 1 ELSE 0 END), 0) AS INTEGER) AS success,
				CAST(COALESCE(SUM(CASE WHEN status_code >= 400 AND status_code < 500 THEN 1 ELSE 0 END), 0) AS INTEGER) AS client_error,
				CAST(COALESCE(SUM(CASE WHEN status_code >= 500 THEN 1 ELSE 0 END), 0) AS INTEGER) AS server_error,
				CAST(COALESCE(SUM(CASE WHEN error_kind != '' THEN 1 ELSE 0 END), 0) AS INTEGER) AS internal_error,
				CAST(COALESCE(SUM(duration_ms), 0) AS INTEGER) AS duration_ms_sum,
				CAST(COALESCE(SUM(request_bytes), 0) AS INTEGER) AS request_bytes,
				CAST(COALESCE(SUM(response_bytes), 0) AS INTEGER) AS response_bytes
			FROM proxy_request_events
			GROUP BY bucket_unix_millis, status_code
		`); err != nil {
			return err
		}
	}
	return db.migrateProxyObservabilityTargetOnly()
}

func (db *DB) migrateProxyObservabilityTargetOnly() error {
	eventColumns, err := db.sqliteTableColumns("proxy_request_events")
	if err != nil {
		return err
	}
	tupleColumns, err := db.sqliteTableColumns("proxy_request_tuple_rollup_minutes")
	if err != nil {
		return err
	}
	_, eventHasBackend := eventColumns["backend_id"]
	_, tupleHasBackend := tupleColumns["backend_id"]
	if !eventHasBackend && !tupleHasBackend {
		return nil
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`
		DROP INDEX IF EXISTS idx_proxy_request_events_backend_id;
		DELETE FROM proxy_request_events;
		DELETE FROM proxy_request_rollup_minutes;
		DELETE FROM proxy_request_tuple_rollup_minutes;
		DELETE FROM proxy_request_status_rollup_minutes;
		UPDATE observability_rollup_state
		SET proxy_backfill_upper_id = 0,
		    proxy_backfilled_through_id = 0,
		    updated_at = CURRENT_TIMESTAMP
		WHERE id = 1;
	`); err != nil {
		return err
	}

	if eventHasBackend {
		if _, err := tx.Exec(`
			CREATE TABLE proxy_request_events_new (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				occurred_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
				status_code INTEGER NOT NULL,
				duration_ms INTEGER NOT NULL,
				error_kind TEXT NOT NULL DEFAULT '',
				method TEXT NOT NULL DEFAULT '',
				host TEXT NOT NULL DEFAULT '',
				path_prefix TEXT NOT NULL DEFAULT '',
				listener_id INTEGER,
				route_target_id INTEGER,
				route_id INTEGER,
				waf_rule_id INTEGER,
				waf_action TEXT NOT NULL DEFAULT '',
				agent_id INTEGER REFERENCES agents(id),
				request_bytes INTEGER NOT NULL DEFAULT 0,
				response_bytes INTEGER NOT NULL DEFAULT 0,
				cache_rule_id INTEGER,
				cache_status TEXT NOT NULL DEFAULT '',
				cache_bytes INTEGER NOT NULL DEFAULT 0
			)
		`); err != nil {
			return err
		}
		if _, err := tx.Exec(`DROP TABLE proxy_request_events`); err != nil {
			return err
		}
		if _, err := tx.Exec(`ALTER TABLE proxy_request_events_new RENAME TO proxy_request_events`); err != nil {
			return err
		}
	}

	if _, err := tx.Exec(`DROP TABLE IF EXISTS proxy_request_tuple_rollup_minutes`); err != nil {
		return err
	}
	if _, err := tx.Exec(`
		CREATE TABLE proxy_request_tuple_rollup_minutes_new (
			bucket_unix_millis INTEGER NOT NULL,
			listener_id INTEGER NOT NULL DEFAULT 0,
			route_target_id INTEGER NOT NULL DEFAULT 0,
			route_id INTEGER NOT NULL DEFAULT 0,
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
			PRIMARY KEY (bucket_unix_millis, listener_id, route_target_id, route_id, agent_id, error_kind, status_class)
		)
	`); err != nil {
		return err
	}
	if _, err := tx.Exec(`ALTER TABLE proxy_request_tuple_rollup_minutes_new RENAME TO proxy_request_tuple_rollup_minutes`); err != nil {
		return err
	}
	return tx.Commit()
}

type legacyPolicyMatchJSON struct {
	Methods      []string                   `json:"methods,omitempty"`
	Protocols    []string                   `json:"protocols,omitempty"`
	HostPatterns []string                   `json:"host_patterns,omitempty"`
	PathPrefixes []string                   `json:"path_prefixes,omitempty"`
	PathSuffixes []string                   `json:"path_suffixes,omitempty"`
	Headers      []legacyPolicyValueMatcher `json:"headers,omitempty"`
	Cookies      []legacyPolicyValueMatcher `json:"cookies,omitempty"`
	QueryParams  []legacyPolicyValueMatcher `json:"query_params,omitempty"`
}

type legacyPolicyValueMatcher struct {
	Name     string `json:"name"`
	Operator string `json:"operator"`
	Value    string `json:"value,omitempty"`
}

type policyMatchJSON struct {
	CELExpression string              `json:"cel_expression,omitempty"`
	Builder       *policyMatchBuilder `json:"builder,omitempty"`
}

type policyMatchBuilder struct {
	Root *policyMatchGroup `json:"root,omitempty"`
}

type policyMatchGroup struct {
	Operator   string                 `json:"operator,omitempty"`
	Conditions []policyMatchCondition `json:"conditions,omitempty"`
	Groups     []policyMatchGroup     `json:"groups,omitempty"`
	Negated    bool                   `json:"negated,omitempty"`
}

type policyMatchCondition struct {
	Field            string   `json:"field"`
	Name             string   `json:"name,omitempty"`
	Operator         string   `json:"operator"`
	Values           []string `json:"values,omitempty"`
	Negated          bool     `json:"negated,omitempty"`
	LegacyFirstValue bool     `json:"legacy_first_value,omitempty"`
}

func (db *DB) migrateLegacyPolicyMatchJSON() error {
	for _, table := range []string{
		"public_rate_limit_rules",
		"public_traffic_shaper_rules",
		"public_waf_rules",
		"public_cache_rules",
	} {
		if err := db.migrateLegacyPolicyMatchJSONTable(table); err != nil {
			return err
		}
	}
	return nil
}

func (db *DB) migrateLegacyPolicyMatchJSONTable(table string) error {
	rows, err := db.Query(fmt.Sprintf(`SELECT id, match_json FROM %s`, table))
	if err != nil {
		return err
	}
	defer rows.Close()
	type update struct {
		id  int64
		raw string
	}
	var updates []update
	for rows.Next() {
		var id int64
		var raw string
		if err := rows.Scan(&id, &raw); err != nil {
			return err
		}
		converted, changed, err := migrateLegacyPolicyMatchJSONValue(raw)
		if err != nil {
			return fmt.Errorf("%s id %d match_json migration failed: %w", table, id, err)
		}
		if changed {
			updates = append(updates, update{id: id, raw: converted})
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, item := range updates {
		if _, err := db.Exec(fmt.Sprintf(`UPDATE %s SET match_json = ? WHERE id = ?`, table), item.raw, item.id); err != nil {
			return err
		}
	}
	return nil
}

func migrateLegacyPolicyMatchJSONValue(raw string) (string, bool, error) {
	if strings.TrimSpace(raw) == "" {
		return raw, false, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		return "", false, err
	}
	if len(fields) == 0 {
		return raw, false, nil
	}
	if _, ok := fields["cel_expression"]; ok {
		return raw, false, nil
	}
	if _, ok := fields["builder"]; ok {
		return raw, false, nil
	}
	if !legacyPolicyMatchJSONHasFields(fields) {
		return "", false, fmt.Errorf("unsupported policy match JSON shape")
	}
	var legacy legacyPolicyMatchJSON
	if err := json.Unmarshal([]byte(raw), &legacy); err != nil {
		return "", false, err
	}
	builder := legacyPolicyMatchBuilder(legacy)
	if builder == nil {
		return "{}", true, nil
	}
	expression, err := policyMatchBuilderExpression(builder)
	if err != nil {
		return "", false, err
	}
	converted, err := json.Marshal(policyMatchJSON{
		CELExpression: expression,
		Builder:       builder,
	})
	if err != nil {
		return "", false, err
	}
	return string(converted), true, nil
}

func legacyPolicyMatchJSONHasFields(fields map[string]json.RawMessage) bool {
	for _, key := range []string{"methods", "protocols", "host_patterns", "path_prefixes", "path_suffixes", "headers", "cookies", "query_params"} {
		if _, ok := fields[key]; ok {
			return true
		}
	}
	return false
}

func legacyPolicyMatchBuilder(legacy legacyPolicyMatchJSON) *policyMatchBuilder {
	root := policyMatchGroup{Operator: "all"}
	if values := normalizeLegacyPolicyValues(legacy.Methods, func(value string) string {
		return strings.ToUpper(strings.TrimSpace(value))
	}); len(values) > 0 {
		root.Conditions = append(root.Conditions, policyMatchCondition{Field: "method", Operator: "in", Values: values})
	}
	if values := normalizeLegacyPolicyValues(legacy.Protocols, func(value string) string {
		return strings.ToLower(strings.TrimSpace(value))
	}); len(values) > 0 {
		root.Conditions = append(root.Conditions, policyMatchCondition{Field: "protocol", Operator: "in", Values: values})
	}
	if values := normalizeLegacyPolicyValues(legacy.HostPatterns, normalizeLegacyPolicyHost); len(values) > 0 {
		root.Conditions = append(root.Conditions, policyMatchCondition{Field: "host", Operator: "host_pattern", Values: values})
	}
	if values := normalizeLegacyPolicyValues(legacy.PathPrefixes, normalizeLegacyPolicyPathPrefix); len(values) > 0 {
		root.Conditions = append(root.Conditions, policyMatchCondition{Field: "path", Operator: "prefix", Values: values})
	}
	if values := normalizeLegacyPolicyValues(legacy.PathSuffixes, strings.TrimSpace); len(values) > 0 {
		root.Conditions = append(root.Conditions, policyMatchCondition{Field: "path", Operator: "suffix", Values: values})
	}
	root.Conditions = append(root.Conditions, legacyPolicyMatcherConditions("header", legacy.Headers, strings.ToLower)...)
	root.Conditions = append(root.Conditions, legacyPolicyMatcherConditions("cookie", legacy.Cookies, strings.TrimSpace)...)
	root.Conditions = append(root.Conditions, legacyPolicyMatcherConditions("query_param", legacy.QueryParams, strings.TrimSpace)...)
	if len(root.Conditions) == 0 {
		return nil
	}
	return &policyMatchBuilder{Root: &root}
}

func normalizeLegacyPolicyValues(values []string, normalize func(string) string) []string {
	resp := make([]string, 0, len(values))
	for _, value := range values {
		value = normalize(value)
		if value != "" {
			resp = append(resp, value)
		}
	}
	return resp
}

func normalizeLegacyPolicyHost(value string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(value)), ".")
}

func normalizeLegacyPolicyPathPrefix(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || strings.HasPrefix(value, "/") {
		return value
	}
	return "/" + value
}

func legacyPolicyMatcherConditions(field string, matchers []legacyPolicyValueMatcher, normalizeName func(string) string) []policyMatchCondition {
	resp := make([]policyMatchCondition, 0, len(matchers))
	for _, matcher := range matchers {
		name := normalizeName(strings.TrimSpace(matcher.Name))
		if name == "" {
			continue
		}
		operator := legacyPolicyMatchOperator(matcher.Operator)
		condition := policyMatchCondition{Field: field, Name: name, Operator: operator}
		if operator != "present" {
			condition.Values = []string{matcher.Value}
		}
		if operator != "present" && (field == "header" || field == "query_param") {
			condition.LegacyFirstValue = true
		}
		resp = append(resp, condition)
	}
	return resp
}

func legacyPolicyMatchOperator(operator string) string {
	switch strings.ToLower(strings.TrimSpace(operator)) {
	case "present", "prefix", "suffix", "contains":
		return strings.ToLower(strings.TrimSpace(operator))
	default:
		return "equals"
	}
}

func policyMatchBuilderExpression(builder *policyMatchBuilder) (string, error) {
	if builder == nil || builder.Root == nil {
		return "", nil
	}
	return policyMatchGroupExpression(*builder.Root)
}

func policyMatchGroupExpression(group policyMatchGroup) (string, error) {
	operator := strings.TrimSpace(group.Operator)
	if operator == "" {
		operator = "all"
	}
	if operator != "all" && operator != "any" {
		return "", fmt.Errorf("policy match boolean operator is invalid")
	}
	parts := make([]string, 0, len(group.Conditions)+len(group.Groups))
	for _, condition := range group.Conditions {
		expression, err := policyMatchConditionExpression(condition)
		if err != nil {
			return "", err
		}
		parts = append(parts, expression)
	}
	for _, child := range group.Groups {
		expression, err := policyMatchGroupExpression(child)
		if err != nil {
			return "", err
		}
		parts = append(parts, expression)
	}
	expression := "true"
	if len(parts) > 0 {
		joiner := " && "
		if operator == "any" {
			joiner = " || "
		}
		expression = "(" + strings.Join(parts, joiner) + ")"
	}
	if group.Negated {
		expression = "!(" + expression + ")"
	}
	return expression, nil
}

func policyMatchConditionExpression(condition policyMatchCondition) (string, error) {
	var expression string
	switch condition.Field {
	case "header":
		expression = repeatedPolicyMatchCondition("headers", condition)
	case "query_param":
		expression = repeatedPolicyMatchCondition("query", condition)
	case "cookie":
		expression = stringMapPolicyMatchCondition("cookies", condition)
	case "host":
		if condition.Operator == "host_pattern" {
			expression = anyPolicyMatchValue(condition.Values, func(value string) string {
				return "host_match(host, " + quotePolicyMatchCEL(value) + ")"
			})
		} else {
			expression = scalarPolicyMatchCondition("host", condition)
		}
	case "path":
		if condition.Operator == "prefix" {
			expression = anyPolicyMatchValue(condition.Values, func(value string) string {
				return "path_prefix(path, " + quotePolicyMatchCEL(value) + ")"
			})
		} else {
			expression = scalarPolicyMatchCondition("path", condition)
		}
	case "method":
		expression = scalarPolicyMatchCondition("method", condition)
	case "protocol":
		expression = scalarPolicyMatchCondition("protocol", condition)
	default:
		return "", fmt.Errorf("policy match field is invalid")
	}
	if condition.Negated {
		expression = "!(" + expression + ")"
	}
	return "(" + expression + ")", nil
}

func scalarPolicyMatchCondition(source string, condition policyMatchCondition) string {
	if condition.Operator == "in" {
		return source + " in " + policyMatchStringList(condition.Values)
	}
	return anyPolicyMatchValue(condition.Values, func(value string) string {
		return policyMatchStringComparison(source, condition.Operator, value)
	})
}

func stringMapPolicyMatchCondition(mapName string, condition policyMatchCondition) string {
	name := quotePolicyMatchCEL(condition.Name)
	present := name + " in " + mapName
	if condition.Operator == "present" {
		return present
	}
	source := mapName + "[" + name + "]"
	comparison := anyPolicyMatchValue(condition.Values, func(value string) string {
		return policyMatchStringComparison(source, condition.Operator, value)
	})
	return "(" + present + " && (" + comparison + "))"
}

func repeatedPolicyMatchCondition(mapName string, condition policyMatchCondition) string {
	name := quotePolicyMatchCEL(condition.Name)
	present := name + " in " + mapName
	if condition.Operator == "present" {
		return present
	}
	if condition.LegacyFirstValue {
		values := mapName + "[" + name + "]"
		source := values + "[0]"
		var comparison string
		if condition.Operator == "in" {
			comparison = source + " in " + policyMatchStringList(condition.Values)
		} else {
			comparison = anyPolicyMatchValue(condition.Values, func(value string) string {
				return policyMatchStringComparison(source, condition.Operator, value)
			})
		}
		return "(" + present + " && " + values + ".size() > 0 && (" + comparison + "))"
	}
	comparison := anyPolicyMatchValue(condition.Values, func(value string) string {
		return policyMatchStringComparison("v", condition.Operator, value)
	})
	return "(" + present + " && " + mapName + "[" + name + "].exists(v, " + comparison + "))"
}

func policyMatchStringComparison(source string, operator string, value string) string {
	quoted := quotePolicyMatchCEL(value)
	switch operator {
	case "prefix":
		return source + ".startsWith(" + quoted + ")"
	case "suffix":
		return source + ".endsWith(" + quoted + ")"
	case "contains":
		return source + ".contains(" + quoted + ")"
	default:
		return source + " == " + quoted
	}
}

func anyPolicyMatchValue(values []string, expression func(string) string) string {
	if len(values) == 0 {
		return "false"
	}
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, expression(value))
	}
	return "(" + strings.Join(parts, " || ") + ")"
}

func quotePolicyMatchCEL(value string) string {
	return strconv.Quote(value)
}

func policyMatchStringList(values []string) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, quotePolicyMatchCEL(value))
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

type sqliteTableColumn struct {
	Name    string
	NotNull bool
}

func (db *DB) migratePublicRoutesRedirectSchema() error {
	columns, err := db.sqliteTableColumns("public_routes")
	if err != nil {
		return err
	}
	backendColumn, hasBackend := columns["backend_id"]
	if !hasBackend {
		return nil
	}
	requiredColumns := []string{
		"action",
		"redirect_target_mode",
		"redirect_target",
		"redirect_status_code",
		"redirect_preserve_path_suffix",
		"redirect_preserve_query",
		"target_load_balancing",
		"is_default",
	}
	needsRebuild := backendColumn.NotNull
	for _, column := range requiredColumns {
		if _, ok := columns[column]; !ok {
			needsRebuild = true
			break
		}
	}
	if !needsRebuild {
		return nil
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`
		CREATE TABLE public_routes_new (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			listener_id INTEGER NOT NULL REFERENCES public_listeners(id) ON DELETE CASCADE,
			priority INTEGER NOT NULL,
			host_pattern TEXT NOT NULL DEFAULT '',
			path_prefix TEXT NOT NULL DEFAULT '',
			backend_id INTEGER REFERENCES public_backends(id),
			load_balancing TEXT NOT NULL DEFAULT 'round_robin',
			fallback_backend_id INTEGER REFERENCES public_backends(id),
			target_load_balancing TEXT NOT NULL DEFAULT 'round_robin',
			is_default INTEGER NOT NULL DEFAULT 0,
			action TEXT NOT NULL DEFAULT 'forward',
			redirect_target_mode TEXT NOT NULL DEFAULT '',
			redirect_target TEXT NOT NULL DEFAULT '',
			redirect_status_code INTEGER NOT NULL DEFAULT 302,
			redirect_preserve_path_suffix INTEGER NOT NULL DEFAULT 1,
			redirect_preserve_query INTEGER NOT NULL DEFAULT 1,
			path_security_mode TEXT NOT NULL DEFAULT 'strict',
			enabled INTEGER NOT NULL DEFAULT 1,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)
	`); err != nil {
		return err
	}

	columnExpr := func(name string, fallback string) string {
		if _, ok := columns[name]; ok {
			return name
		}
		return fallback
	}
	pathSecurityModeExpr := "'strict'"
	if _, ok := columns["path_security_mode"]; ok {
		pathSecurityModeExpr = "COALESCE(path_security_mode, 'strict')"
	}
	copySQL := fmt.Sprintf(`
		INSERT INTO public_routes_new (
			id,
			listener_id,
			priority,
			host_pattern,
			path_prefix,
			backend_id,
			load_balancing,
			fallback_backend_id,
			target_load_balancing,
			is_default,
			action,
			redirect_target_mode,
			redirect_target,
			redirect_status_code,
			redirect_preserve_path_suffix,
			redirect_preserve_query,
			path_security_mode,
			enabled,
			created_at,
			updated_at
		)
		SELECT
			id,
			listener_id,
			priority,
			host_pattern,
			path_prefix,
			backend_id,
			%s,
			%s,
			%s,
			%s,
			%s,
			%s,
			%s,
			%s,
			%s,
			%s,
			%s,
			enabled,
			created_at,
			updated_at
		FROM public_routes
	`,
		columnExpr("load_balancing", "'round_robin'"),
		columnExpr("fallback_backend_id", "NULL"),
		columnExpr("target_load_balancing", columnExpr("load_balancing", "'round_robin'")),
		columnExpr("is_default", "0"),
		columnExpr("action", "'forward'"),
		columnExpr("redirect_target_mode", "''"),
		columnExpr("redirect_target", "''"),
		columnExpr("redirect_status_code", "302"),
		columnExpr("redirect_preserve_path_suffix", "1"),
		columnExpr("redirect_preserve_query", "1"),
		pathSecurityModeExpr,
	)
	if _, err := tx.Exec(copySQL); err != nil {
		return err
	}
	if _, err := tx.Exec(`DROP TABLE public_routes`); err != nil {
		return err
	}
	if _, err := tx.Exec(`ALTER TABLE public_routes_new RENAME TO public_routes`); err != nil {
		return err
	}
	if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_public_routes_listener_priority ON public_routes (listener_id, priority, id)`); err != nil {
		return err
	}
	if _, err := tx.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_public_routes_one_default_per_listener ON public_routes (listener_id) WHERE is_default = 1`); err != nil {
		return err
	}
	return tx.Commit()
}

func (db *DB) prepareLegacyPublicBackendMigration() error {
	hasBackends, err := db.sqliteTableExists("public_backends")
	if err != nil {
		return err
	}
	if !hasBackends {
		return nil
	}
	for _, stmt := range []string{
		`ALTER TABLE public_backends ADD COLUMN backend_type TEXT NOT NULL DEFAULT 'proxy_forward'`,
		`ALTER TABLE public_backends ADD COLUMN forward_mode TEXT NOT NULL DEFAULT 'direct'`,
		`ALTER TABLE public_backends ADD COLUMN load_balancing TEXT NOT NULL DEFAULT 'round_robin'`,
		`ALTER TABLE public_backends ADD COLUMN tls_skip_verify INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE public_backends ADD COLUMN static_status_code INTEGER NOT NULL DEFAULT 200`,
		`ALTER TABLE public_backends ADD COLUMN static_response_body TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE public_backends ADD COLUMN static_response_body_mode TEXT NOT NULL DEFAULT 'inline'`,
		`ALTER TABLE public_backends ADD COLUMN static_response_template_id INTEGER REFERENCES public_response_templates(id) ON DELETE RESTRICT`,
		`ALTER TABLE public_backends ADD COLUMN upstream_basic_auth_enabled INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE public_backends ADD COLUMN upstream_basic_auth_username TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE public_backends ADD COLUMN upstream_basic_auth_password TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE public_backends ADD COLUMN upstream_response_header_timeout_millis INTEGER NOT NULL DEFAULT 60000`,
		`ALTER TABLE public_backends ADD COLUMN health_check_enabled INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE public_backends ADD COLUMN health_check_method TEXT NOT NULL DEFAULT 'GET'`,
		`ALTER TABLE public_backends ADD COLUMN health_check_path TEXT NOT NULL DEFAULT '/'`,
		`ALTER TABLE public_backends ADD COLUMN health_check_interval_millis INTEGER NOT NULL DEFAULT 10000`,
		`ALTER TABLE public_backends ADD COLUMN health_check_timeout_millis INTEGER NOT NULL DEFAULT 2000`,
		`ALTER TABLE public_backends ADD COLUMN health_check_healthy_threshold INTEGER NOT NULL DEFAULT 2`,
		`ALTER TABLE public_backends ADD COLUMN health_check_unhealthy_threshold INTEGER NOT NULL DEFAULT 2`,
		`ALTER TABLE public_backends ADD COLUMN health_check_expected_status_min INTEGER NOT NULL DEFAULT 200`,
		`ALTER TABLE public_backends ADD COLUMN health_check_expected_status_max INTEGER NOT NULL DEFAULT 399`,
	} {
		if _, err := db.Exec(stmt); err != nil && !strings.Contains(err.Error(), "duplicate column name") {
			return err
		}
	}
	for _, stmt := range []string{
		`CREATE TABLE IF NOT EXISTS public_backend_headers (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			backend_id INTEGER NOT NULL REFERENCES public_backends(id) ON DELETE CASCADE,
			position INTEGER NOT NULL,
			name TEXT NOT NULL,
			value TEXT NOT NULL,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(backend_id, position)
		)`,
		`CREATE TABLE IF NOT EXISTS public_backend_upstream_headers (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			backend_id INTEGER NOT NULL REFERENCES public_backends(id) ON DELETE CASCADE,
			position INTEGER NOT NULL,
			name TEXT NOT NULL,
			value TEXT NOT NULL,
			sensitive INTEGER NOT NULL DEFAULT 0,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(backend_id, position)
		)`,
		`CREATE TABLE IF NOT EXISTS public_backend_agents (
			backend_id INTEGER NOT NULL REFERENCES public_backends(id) ON DELETE CASCADE,
			agent_id INTEGER NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
			position INTEGER NOT NULL,
			weight INTEGER NOT NULL DEFAULT 100,
			enabled INTEGER NOT NULL DEFAULT 1,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (backend_id, agent_id),
			UNIQUE(backend_id, position)
		)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

func (db *DB) backfillPublicRouteBackends() error {
	hasBackends, err := db.sqliteTableExists("public_backends")
	if err != nil {
		return err
	}
	if !hasBackends {
		return nil
	}
	routeColumns, err := db.sqliteTableColumns("public_routes")
	if err != nil {
		return err
	}
	if !sqliteColumnExists(routeColumns, "backend_id") {
		return nil
	}
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS public_route_backends (
			route_id INTEGER NOT NULL REFERENCES public_routes(id) ON DELETE CASCADE,
			backend_id INTEGER NOT NULL REFERENCES public_backends(id) ON DELETE CASCADE,
			position INTEGER NOT NULL,
			weight INTEGER NOT NULL DEFAULT 100,
			enabled INTEGER NOT NULL DEFAULT 1,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (route_id, backend_id),
			UNIQUE(route_id, position)
		)
	`); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_public_route_backends_route_position ON public_route_backends (route_id, position)`); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_public_route_backends_backend_id ON public_route_backends (backend_id)`); err != nil {
		return err
	}
	_, err = db.Exec(`
		INSERT OR IGNORE INTO public_route_backends (route_id, backend_id, position, weight, enabled)
		SELECT id, backend_id, 0, 100, 1
		FROM public_routes
		WHERE backend_id IS NOT NULL
		  AND COALESCE(action, 'forward') = 'forward'
	`)
	return err
}

func (db *DB) backfillPublicAgentSystemLabels() error {
	_, err := db.Exec(`
		INSERT OR IGNORE INTO public_agent_labels (agent_id, key, value, source)
		SELECT id, 'p2pstream.io/agent-id', public_id, 'system'
		FROM agents
		WHERE public_id != ''
	`)
	return err
}

func (db *DB) backfillPublicRouteTargets() error {
	hasBackends, err := db.sqliteTableExists("public_backends")
	if err != nil {
		return err
	}
	if !hasBackends {
		return nil
	}
	if err := db.backfillPublicRouteBackends(); err != nil {
		return err
	}
	routeColumns, err := db.sqliteTableColumns("public_routes")
	if err != nil {
		return err
	}
	listenerColumns, err := db.sqliteTableColumns("public_listeners")
	if err != nil {
		return err
	}
	hasRouteBackends, err := db.sqliteTableExists("public_route_backends")
	if err != nil {
		return err
	}
	if !hasRouteBackends && !sqliteColumnExists(routeColumns, "fallback_backend_id") && !sqliteColumnExists(listenerColumns, "default_backend_id") {
		return nil
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if sqliteColumnExists(listenerColumns, "default_backend_id") {
		insertColumns := []string{
			"listener_id",
			"priority",
			"host_pattern",
			"path_prefix",
		}
		selectColumns := []string{
			"l.id",
			"9223372036854775807",
			"''",
			"''",
		}
		if sqliteColumnExists(routeColumns, "backend_id") {
			insertColumns = append(insertColumns, "backend_id")
			selectColumns = append(selectColumns, "l.default_backend_id")
		}
		if sqliteColumnExists(routeColumns, "load_balancing") {
			insertColumns = append(insertColumns, "load_balancing")
			selectColumns = append(selectColumns, "COALESCE(pb.load_balancing, 'round_robin')")
		}
		if sqliteColumnExists(routeColumns, "fallback_backend_id") {
			insertColumns = append(insertColumns, "fallback_backend_id")
			selectColumns = append(selectColumns, "NULL")
		}
		insertColumns = append(insertColumns,
			"target_load_balancing",
			"is_default",
			"action",
			"enabled",
		)
		selectColumns = append(selectColumns,
			"'round_robin'",
			"1",
			"'forward'",
			"l.enabled",
		)
		insertDefaultRouteSQL := fmt.Sprintf(`
			INSERT OR IGNORE INTO public_routes (%s)
			SELECT %s
			FROM public_listeners l
			LEFT JOIN public_backends pb ON pb.id = l.default_backend_id
			WHERE l.default_backend_id IS NOT NULL
			  AND NOT EXISTS (
				SELECT 1
				FROM public_routes r
				WHERE r.listener_id = l.id
				  AND r.is_default = 1
			  )
		`, strings.Join(insertColumns, ", "), strings.Join(selectColumns, ", "))
		if _, err := tx.Exec(insertDefaultRouteSQL); err != nil {
			return err
		}
		if hasRouteBackends {
			if _, err := tx.Exec(`
				INSERT OR IGNORE INTO public_route_backends (route_id, backend_id, position, weight, enabled)
				SELECT id, backend_id, 0, 100, 1
				FROM public_routes
				WHERE backend_id IS NOT NULL
				  AND COALESCE(action, 'forward') = 'forward'
			`); err != nil {
				return err
			}
		}
	}

	routeSources := []string{}
	if sqliteColumnExists(listenerColumns, "default_backend_id") {
		routeSources = append(routeSources, `
			SELECT r.id AS route_id, l.default_backend_id AS backend_id, 0 AS position, 100 AS weight, 1 AS enabled, 0 AS priority_group
			FROM public_listeners l
			JOIN public_routes r ON r.listener_id = l.id AND r.is_default = 1
			WHERE l.default_backend_id IS NOT NULL
		`)
	}
	if hasRouteBackends {
		routeSources = append(routeSources, `
			SELECT route_id, backend_id, position, weight, enabled, 0 AS priority_group
			FROM public_route_backends
		`)
	}
	if sqliteColumnExists(routeColumns, "fallback_backend_id") {
		routeSources = append(routeSources, `
			SELECT id, fallback_backend_id, 0, 100, 1, 1
			FROM public_routes
			WHERE fallback_backend_id IS NOT NULL
			  AND COALESCE(action, 'forward') = 'forward'
		`)
	}
	if len(routeSources) == 0 {
		if err := backfillPublicCacheRuleTargetFilters(tx, nil); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM public_cache_entries WHERE route_target_id IS NULL`); err != nil {
			return err
		}
		return tx.Commit()
	}
	rows, err := tx.Query(strings.Join(routeSources, "\nUNION ALL\n") + "\nORDER BY route_id, priority_group, position, backend_id")
	if err != nil {
		return err
	}
	defer rows.Close()

	targetsByBackend := map[int64]map[int64]struct{}{}
	for rows.Next() {
		var source legacyRouteTargetSource
		if err := rows.Scan(
			&source.RouteID,
			&source.BackendID,
			&source.Position,
			&source.Weight,
			&source.Enabled,
			&source.PriorityGroup,
		); err != nil {
			return err
		}
		targetIDs, err := backfillLegacyRouteTarget(tx, source)
		if err != nil {
			return err
		}
		if len(targetIDs) == 0 {
			continue
		}
		set := targetsByBackend[source.BackendID]
		if set == nil {
			set = map[int64]struct{}{}
			targetsByBackend[source.BackendID] = set
		}
		for _, targetID := range targetIDs {
			set[targetID] = struct{}{}
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := backfillPublicCacheRuleTargetFilters(tx, targetsByBackend); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM public_cache_entries WHERE route_target_id IS NULL`); err != nil {
		return err
	}
	return tx.Commit()
}

type legacyRouteTargetSource struct {
	RouteID       int64
	BackendID     int64
	Position      int64
	Weight        int64
	Enabled       int64
	PriorityGroup int64
}

type legacyPublicBackend struct {
	ID                                  int64
	Name                                string
	TargetOrigin                        string
	BackendType                         string
	ForwardMode                         string
	LoadBalancing                       string
	TLSSkipVerify                       int64
	StaticStatusCode                    int64
	StaticResponseBody                  string
	StaticResponseBodyMode              string
	StaticResponseTemplateID            sql.NullInt64
	UpstreamBasicAuthEnabled            int64
	UpstreamBasicAuthUsername           string
	UpstreamBasicAuthPassword           string
	UpstreamResponseHeaderTimeoutMillis int64
	HealthCheckEnabled                  int64
	HealthCheckMethod                   string
	HealthCheckPath                     string
	HealthCheckIntervalMillis           int64
	HealthCheckTimeoutMillis            int64
	HealthCheckHealthyThreshold         int64
	HealthCheckUnhealthyThreshold       int64
	HealthCheckExpectedStatusMin        int64
	HealthCheckExpectedStatusMax        int64
	Enabled                             int64
}

type legacyPublicBackendAgent struct {
	AgentID  int64
	Position int64
	Weight   int64
	Enabled  int64
	PublicID string
	Name     string
}

func backfillLegacyRouteTarget(tx *sql.Tx, source legacyRouteTargetSource) ([]int64, error) {
	backend, err := loadLegacyPublicBackend(tx, source.BackendID)
	if err != nil {
		return nil, err
	}
	if backend.ID == 0 {
		return nil, nil
	}
	if backend.BackendType == "proxy_forward" && backend.ForwardMode == "agent_pool" {
		agents, err := loadLegacyPublicBackendAgents(tx, backend.ID)
		if err != nil {
			return nil, err
		}
		targetIDs := make([]int64, 0, len(agents))
		for _, agent := range agents {
			targetID, err := insertLegacyRouteTarget(tx, source, backend, &agent)
			if err != nil {
				return nil, err
			}
			if targetID != 0 {
				targetIDs = append(targetIDs, targetID)
			}
		}
		return targetIDs, nil
	}
	targetID, err := insertLegacyRouteTarget(tx, source, backend, nil)
	if err != nil {
		return nil, err
	}
	if targetID == 0 {
		return nil, nil
	}
	return []int64{targetID}, nil
}

func loadLegacyPublicBackend(tx *sql.Tx, backendID int64) (legacyPublicBackend, error) {
	var backend legacyPublicBackend
	err := tx.QueryRow(`
		SELECT
			id,
			name,
			target_origin,
			backend_type,
			forward_mode,
			load_balancing,
			tls_skip_verify,
			static_status_code,
			static_response_body,
			static_response_body_mode,
			static_response_template_id,
			upstream_basic_auth_enabled,
			upstream_basic_auth_username,
			upstream_basic_auth_password,
			upstream_response_header_timeout_millis,
			health_check_enabled,
			health_check_method,
			health_check_path,
			health_check_interval_millis,
			health_check_timeout_millis,
			health_check_healthy_threshold,
			health_check_unhealthy_threshold,
			health_check_expected_status_min,
			health_check_expected_status_max,
			enabled
		FROM public_backends
		WHERE id = ?
	`, backendID).Scan(
		&backend.ID,
		&backend.Name,
		&backend.TargetOrigin,
		&backend.BackendType,
		&backend.ForwardMode,
		&backend.LoadBalancing,
		&backend.TLSSkipVerify,
		&backend.StaticStatusCode,
		&backend.StaticResponseBody,
		&backend.StaticResponseBodyMode,
		&backend.StaticResponseTemplateID,
		&backend.UpstreamBasicAuthEnabled,
		&backend.UpstreamBasicAuthUsername,
		&backend.UpstreamBasicAuthPassword,
		&backend.UpstreamResponseHeaderTimeoutMillis,
		&backend.HealthCheckEnabled,
		&backend.HealthCheckMethod,
		&backend.HealthCheckPath,
		&backend.HealthCheckIntervalMillis,
		&backend.HealthCheckTimeoutMillis,
		&backend.HealthCheckHealthyThreshold,
		&backend.HealthCheckUnhealthyThreshold,
		&backend.HealthCheckExpectedStatusMin,
		&backend.HealthCheckExpectedStatusMax,
		&backend.Enabled,
	)
	if err == sql.ErrNoRows {
		return legacyPublicBackend{}, nil
	}
	return backend, err
}

func loadLegacyPublicBackendAgents(tx *sql.Tx, backendID int64) ([]legacyPublicBackendAgent, error) {
	rows, err := tx.Query(`
		SELECT pba.agent_id, pba.position, pba.weight, pba.enabled, a.public_id, a.name
		FROM public_backend_agents pba
		JOIN agents a ON a.id = pba.agent_id
		WHERE pba.backend_id = ?
		ORDER BY pba.position, pba.agent_id
	`, backendID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var agents []legacyPublicBackendAgent
	for rows.Next() {
		var agent legacyPublicBackendAgent
		if err := rows.Scan(&agent.AgentID, &agent.Position, &agent.Weight, &agent.Enabled, &agent.PublicID, &agent.Name); err != nil {
			return nil, err
		}
		agents = append(agents, agent)
	}
	return agents, rows.Err()
}

func insertLegacyRouteTarget(tx *sql.Tx, source legacyRouteTargetSource, backend legacyPublicBackend, agent *legacyPublicBackendAgent) (int64, error) {
	position := source.PriorityGroup*1_000_000_000 + source.Position*10_000
	weight := clampLegacyRouteTargetWeight(source.Weight)
	enabled := source.Enabled != 0 && backend.Enabled != 0
	transport := "direct"
	selectorJSON := "{}"
	name := backend.Name
	if agent != nil {
		position += agent.Position
		weight = clampLegacyRouteTargetWeight(source.Weight * agent.Weight)
		enabled = enabled && agent.Enabled != 0
		transport = "agent"
		name = strings.TrimSpace(backend.Name + " / " + agent.Name)
		selector, err := json.Marshal(map[string]map[string]string{
			"match_labels": {
				"p2pstream.io/agent-id": agent.PublicID,
			},
		})
		if err != nil {
			return 0, err
		}
		selectorJSON = string(selector)
	}

	targetType := "proxy"
	if backend.BackendType == "static" {
		targetType = "static"
		transport = "direct"
	}
	staticTemplateID := any(nil)
	if backend.StaticResponseTemplateID.Valid {
		staticTemplateID = backend.StaticResponseTemplateID.Int64
	}
	enabledInt := int64(0)
	if enabled {
		enabledInt = 1
	}
	if _, err := tx.Exec(`
		INSERT OR IGNORE INTO public_route_targets (
			route_id,
			name,
			position,
			priority_group,
			weight,
			enabled,
			target_type,
			url,
			transport,
			agent_selector_json,
			agent_load_balancing,
			tls_skip_verify,
			upstream_basic_auth_enabled,
			upstream_basic_auth_username,
			upstream_basic_auth_password,
			upstream_response_header_timeout_millis,
			health_check_enabled,
			health_check_method,
			health_check_path,
			health_check_interval_millis,
			health_check_timeout_millis,
			health_check_healthy_threshold,
			health_check_unhealthy_threshold,
			health_check_expected_status_min,
			health_check_expected_status_max,
			static_status_code,
			static_response_body,
			static_response_body_mode,
			static_response_template_id
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, source.RouteID,
		name,
		position,
		source.PriorityGroup,
		weight,
		enabledInt,
		targetType,
		backend.TargetOrigin,
		transport,
		selectorJSON,
		backend.LoadBalancing,
		backend.TLSSkipVerify,
		backend.UpstreamBasicAuthEnabled,
		backend.UpstreamBasicAuthUsername,
		backend.UpstreamBasicAuthPassword,
		backend.UpstreamResponseHeaderTimeoutMillis,
		backend.HealthCheckEnabled,
		backend.HealthCheckMethod,
		backend.HealthCheckPath,
		backend.HealthCheckIntervalMillis,
		backend.HealthCheckTimeoutMillis,
		backend.HealthCheckHealthyThreshold,
		backend.HealthCheckUnhealthyThreshold,
		backend.HealthCheckExpectedStatusMin,
		backend.HealthCheckExpectedStatusMax,
		backend.StaticStatusCode,
		backend.StaticResponseBody,
		backend.StaticResponseBodyMode,
		staticTemplateID,
	); err != nil {
		return 0, err
	}

	var targetID int64
	if err := tx.QueryRow(`
		SELECT id
		FROM public_route_targets
		WHERE route_id = ?
		  AND position = ?
	`, source.RouteID, position).Scan(&targetID); err != nil {
		return 0, err
	}
	if targetType == "proxy" {
		if _, err := tx.Exec(`
			INSERT OR IGNORE INTO public_route_target_upstream_headers (target_id, position, name, value, sensitive)
			SELECT ?, position, name, value, sensitive
			FROM public_backend_upstream_headers
			WHERE backend_id = ?
		`, targetID, backend.ID); err != nil {
			return 0, err
		}
	} else {
		if _, err := tx.Exec(`
			INSERT OR IGNORE INTO public_route_target_response_headers (target_id, position, name, value)
			SELECT ?, position, name, value
			FROM public_backend_headers
			WHERE backend_id = ?
		`, targetID, backend.ID); err != nil {
			return 0, err
		}
	}
	return targetID, nil
}

func clampLegacyRouteTargetWeight(weight int64) int64 {
	if weight < 1 {
		return 1
	}
	if weight > 1_000_000 {
		return 1_000_000
	}
	return weight
}

func backfillPublicCacheRuleTargetFilters(tx *sql.Tx, targetsByBackend map[int64]map[int64]struct{}) error {
	columns, err := sqliteTxTableColumns(tx, "public_cache_rules")
	if err != nil {
		return err
	}
	if !sqliteColumnExists(columns, "backend_ids_json") {
		return nil
	}
	rows, err := tx.Query(`SELECT id, backend_ids_json, target_ids_json FROM public_cache_rules`)
	if err != nil {
		return err
	}
	defer rows.Close()

	type cacheRuleUpdate struct {
		id        int64
		targetIDs []int64
	}
	var updates []cacheRuleUpdate
	for rows.Next() {
		var id int64
		var backendIDsJSON string
		var targetIDsJSON string
		if err := rows.Scan(&id, &backendIDsJSON, &targetIDsJSON); err != nil {
			return err
		}
		var existingTargetIDs []int64
		if err := json.Unmarshal([]byte(targetIDsJSON), &existingTargetIDs); err == nil && len(existingTargetIDs) > 0 {
			continue
		}
		var backendIDs []int64
		if err := json.Unmarshal([]byte(backendIDsJSON), &backendIDs); err != nil || len(backendIDs) == 0 {
			continue
		}
		targetSet := map[int64]struct{}{}
		for _, backendID := range backendIDs {
			for targetID := range targetsByBackend[backendID] {
				targetSet[targetID] = struct{}{}
			}
		}
		if len(targetSet) == 0 {
			continue
		}
		targetIDs := make([]int64, 0, len(targetSet))
		for targetID := range targetSet {
			targetIDs = append(targetIDs, targetID)
		}
		sort.Slice(targetIDs, func(i, j int) bool { return targetIDs[i] < targetIDs[j] })
		updates = append(updates, cacheRuleUpdate{id: id, targetIDs: targetIDs})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, update := range updates {
		payload, err := json.Marshal(update.targetIDs)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE public_cache_rules SET target_ids_json = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, string(payload), update.id); err != nil {
			return err
		}
	}
	return nil
}

func (db *DB) migrateDropLegacyPublicBackendConfig() error {
	hasBackends, err := db.sqliteTableExists("public_backends")
	if err != nil {
		return err
	}
	listenerColumns, err := db.sqliteTableColumns("public_listeners")
	if err != nil {
		return err
	}
	routeColumns, err := db.sqliteTableColumns("public_routes")
	if err != nil {
		return err
	}
	cacheRuleColumns, err := db.sqliteTableColumns("public_cache_rules")
	if err != nil {
		return err
	}
	cacheEntryColumns, err := db.sqliteTableColumns("public_cache_entries")
	if err != nil {
		return err
	}
	needsRebuild := sqliteColumnExists(listenerColumns, "default_backend_id") ||
		sqliteColumnExists(routeColumns, "backend_id") ||
		sqliteColumnExists(routeColumns, "fallback_backend_id") ||
		sqliteColumnExists(routeColumns, "load_balancing") ||
		sqliteColumnExists(cacheRuleColumns, "backend_ids_json") ||
		sqliteColumnExists(cacheEntryColumns, "backend_id")
	if !hasBackends && !needsRebuild {
		return nil
	}

	conn, err := db.Conn(context.Background())
	if err != nil {
		return err
	}
	defer conn.Close()

	if _, err := conn.ExecContext(context.Background(), `PRAGMA foreign_keys = OFF`); err != nil {
		return err
	}
	foreignKeysOff := true
	defer func() {
		if foreignKeysOff {
			_, _ = conn.ExecContext(context.Background(), `PRAGMA foreign_keys = ON`)
		}
	}()

	tx, err := conn.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, stmt := range []string{
		`DROP INDEX IF EXISTS idx_public_backend_headers_backend_position`,
		`DROP INDEX IF EXISTS idx_public_backend_upstream_headers_backend_position`,
		`DROP INDEX IF EXISTS idx_public_backend_agents_backend_position`,
		`DROP INDEX IF EXISTS idx_public_backend_agents_agent_id`,
		`DROP INDEX IF EXISTS idx_public_route_backends_route_position`,
		`DROP INDEX IF EXISTS idx_public_route_backends_backend_id`,
		`DROP INDEX IF EXISTS idx_public_backends_static_response_template_id`,
	} {
		if _, err := tx.Exec(stmt); err != nil {
			return err
		}
	}

	if sqliteColumnExists(listenerColumns, "default_backend_id") {
		if _, err := tx.Exec(`
			CREATE TABLE public_listeners_new (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				name TEXT NOT NULL UNIQUE,
				bind_address TEXT NOT NULL DEFAULT '',
				port INTEGER NOT NULL,
				protocol TEXT NOT NULL,
				enabled INTEGER NOT NULL DEFAULT 1,
				created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
				updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
				UNIQUE(bind_address, port)
			)
		`); err != nil {
			return err
		}
		if _, err := tx.Exec(`
			INSERT INTO public_listeners_new (id, name, bind_address, port, protocol, enabled, created_at, updated_at)
			SELECT id, name, bind_address, port, protocol, enabled, created_at, updated_at
			FROM public_listeners
		`); err != nil {
			return err
		}
		if _, err := tx.Exec(`DROP TABLE public_listeners`); err != nil {
			return err
		}
		if _, err := tx.Exec(`ALTER TABLE public_listeners_new RENAME TO public_listeners`); err != nil {
			return err
		}
	}

	if sqliteColumnExists(routeColumns, "backend_id") || sqliteColumnExists(routeColumns, "fallback_backend_id") || sqliteColumnExists(routeColumns, "load_balancing") {
		if _, err := tx.Exec(`
			CREATE TABLE public_routes_new (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				listener_id INTEGER NOT NULL REFERENCES public_listeners(id) ON DELETE CASCADE,
				priority INTEGER NOT NULL,
				host_pattern TEXT NOT NULL DEFAULT '',
				path_prefix TEXT NOT NULL DEFAULT '',
				target_load_balancing TEXT NOT NULL DEFAULT 'round_robin',
				is_default INTEGER NOT NULL DEFAULT 0,
				action TEXT NOT NULL DEFAULT 'forward',
				redirect_target_mode TEXT NOT NULL DEFAULT '',
				redirect_target TEXT NOT NULL DEFAULT '',
				redirect_status_code INTEGER NOT NULL DEFAULT 302,
				redirect_preserve_path_suffix INTEGER NOT NULL DEFAULT 1,
				redirect_preserve_query INTEGER NOT NULL DEFAULT 1,
				path_security_mode TEXT NOT NULL DEFAULT 'strict',
				enabled INTEGER NOT NULL DEFAULT 1,
				created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
				updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
			)
		`); err != nil {
			return err
		}
		columnExpr := func(name string, fallback string) string {
			if sqliteColumnExists(routeColumns, name) {
				return name
			}
			return fallback
		}
		pathSecurityModeExpr := "'strict'"
		if sqliteColumnExists(routeColumns, "path_security_mode") {
			pathSecurityModeExpr = "COALESCE(path_security_mode, 'strict')"
		}
		copyRoutesSQL := fmt.Sprintf(`
			INSERT INTO public_routes_new (
				id,
				listener_id,
				priority,
				host_pattern,
				path_prefix,
				target_load_balancing,
				is_default,
				action,
				redirect_target_mode,
				redirect_target,
				redirect_status_code,
				redirect_preserve_path_suffix,
				redirect_preserve_query,
				path_security_mode,
				enabled,
				created_at,
				updated_at
			)
			SELECT
				id,
				listener_id,
				priority,
				host_pattern,
				path_prefix,
				%s,
				%s,
				%s,
				%s,
				%s,
				%s,
				%s,
				%s,
				%s,
				enabled,
				created_at,
				updated_at
			FROM public_routes
		`,
			columnExpr("target_load_balancing", columnExpr("load_balancing", "'round_robin'")),
			columnExpr("is_default", "0"),
			columnExpr("action", "'forward'"),
			columnExpr("redirect_target_mode", "''"),
			columnExpr("redirect_target", "''"),
			columnExpr("redirect_status_code", "302"),
			columnExpr("redirect_preserve_path_suffix", "1"),
			columnExpr("redirect_preserve_query", "1"),
			pathSecurityModeExpr,
		)
		if _, err := tx.Exec(copyRoutesSQL); err != nil {
			return err
		}
		if _, err := tx.Exec(`DROP TABLE public_routes`); err != nil {
			return err
		}
		if _, err := tx.Exec(`ALTER TABLE public_routes_new RENAME TO public_routes`); err != nil {
			return err
		}
	}

	if sqliteColumnExists(cacheRuleColumns, "backend_ids_json") {
		if _, err := tx.Exec(`
			CREATE TABLE public_cache_rules_new (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				name TEXT NOT NULL UNIQUE,
				priority INTEGER NOT NULL DEFAULT 100,
				enabled INTEGER NOT NULL DEFAULT 1,
				match_json TEXT NOT NULL DEFAULT '{}',
				route_ids_json TEXT NOT NULL DEFAULT '[]',
				target_ids_json TEXT NOT NULL DEFAULT '[]',
				scope TEXT NOT NULL DEFAULT 'selected_backend',
				ttl_mode TEXT NOT NULL DEFAULT 'fixed',
				ttl_millis INTEGER NOT NULL DEFAULT 3600000,
				query_mode TEXT NOT NULL DEFAULT 'full',
				query_params_json TEXT NOT NULL DEFAULT '[]',
				vary_headers_json TEXT NOT NULL DEFAULT '["Accept-Encoding"]',
				cache_status_codes_json TEXT NOT NULL DEFAULT '[200,203,204,301,308]',
				max_object_bytes INTEGER NOT NULL DEFAULT 104857600,
				add_cache_status_header INTEGER NOT NULL DEFAULT 1,
				allow_cookie_requests INTEGER NOT NULL DEFAULT 0,
				created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
				updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
			)
		`); err != nil {
			return err
		}
		if _, err := tx.Exec(`
			INSERT INTO public_cache_rules_new (
				id,
				name,
				priority,
				enabled,
				match_json,
				route_ids_json,
				target_ids_json,
				scope,
				ttl_mode,
				ttl_millis,
				query_mode,
				query_params_json,
				vary_headers_json,
				cache_status_codes_json,
				max_object_bytes,
				add_cache_status_header,
				allow_cookie_requests,
				created_at,
				updated_at
			)
			SELECT
				id,
				name,
				priority,
				enabled,
				match_json,
				route_ids_json,
				target_ids_json,
				scope,
				ttl_mode,
				ttl_millis,
				query_mode,
				query_params_json,
				vary_headers_json,
				cache_status_codes_json,
				max_object_bytes,
				add_cache_status_header,
				allow_cookie_requests,
				created_at,
				updated_at
			FROM public_cache_rules
		`); err != nil {
			return err
		}
		if _, err := tx.Exec(`DROP TABLE public_cache_rules`); err != nil {
			return err
		}
		if _, err := tx.Exec(`ALTER TABLE public_cache_rules_new RENAME TO public_cache_rules`); err != nil {
			return err
		}
	}

	if sqliteColumnExists(cacheEntryColumns, "backend_id") {
		if _, err := tx.Exec(`DELETE FROM public_cache_entries`); err != nil {
			return err
		}
		if _, err := tx.Exec(`
			CREATE TABLE public_cache_entries_new (
				key_digest TEXT PRIMARY KEY,
				rule_id INTEGER NOT NULL REFERENCES public_cache_rules(id) ON DELETE CASCADE,
				scope TEXT NOT NULL,
				listener_protocol TEXT NOT NULL,
				host TEXT NOT NULL,
				path TEXT NOT NULL,
				query_key TEXT NOT NULL,
				route_id INTEGER,
				route_target_id INTEGER,
				method TEXT NOT NULL DEFAULT 'GET',
				vary_headers_json TEXT NOT NULL DEFAULT '[]',
				response_headers_json TEXT NOT NULL DEFAULT '[]',
				status_code INTEGER NOT NULL,
				body_path TEXT NOT NULL,
				size_bytes INTEGER NOT NULL,
				stored_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
				expires_at DATETIME NOT NULL,
				last_accessed_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
				hit_count INTEGER NOT NULL DEFAULT 0
			)
		`); err != nil {
			return err
		}
		if _, err := tx.Exec(`DROP TABLE public_cache_entries`); err != nil {
			return err
		}
		if _, err := tx.Exec(`ALTER TABLE public_cache_entries_new RENAME TO public_cache_entries`); err != nil {
			return err
		}
	}

	for _, stmt := range []string{
		`DROP TABLE IF EXISTS public_route_backends`,
		`DROP TABLE IF EXISTS public_backend_agents`,
		`DROP TABLE IF EXISTS public_backend_upstream_headers`,
		`DROP TABLE IF EXISTS public_backend_headers`,
		`DROP TABLE IF EXISTS public_backends`,
		`CREATE INDEX IF NOT EXISTS idx_public_routes_listener_priority ON public_routes (listener_id, priority, id)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_public_routes_one_default_per_listener ON public_routes (listener_id) WHERE is_default = 1`,
		`CREATE INDEX IF NOT EXISTS idx_public_cache_rules_priority ON public_cache_rules (priority, id)`,
		`CREATE INDEX IF NOT EXISTS idx_public_cache_entries_rule_id ON public_cache_entries (rule_id)`,
		`CREATE INDEX IF NOT EXISTS idx_public_cache_entries_route_target_id ON public_cache_entries (route_target_id)`,
		`CREATE INDEX IF NOT EXISTS idx_public_cache_entries_expires_at ON public_cache_entries (expires_at)`,
		`CREATE INDEX IF NOT EXISTS idx_public_cache_entries_last_accessed_at ON public_cache_entries (last_accessed_at)`,
	} {
		if _, err := tx.Exec(stmt); err != nil {
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return err
	}
	if _, err := conn.ExecContext(context.Background(), `PRAGMA foreign_keys = ON`); err != nil {
		return err
	}
	foreignKeysOff = false
	rows, err := conn.QueryContext(context.Background(), `PRAGMA foreign_key_check`)
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() {
		return fmt.Errorf("foreign key check failed after legacy public backend cleanup")
	}
	return rows.Err()
}

func sqliteTxTableColumns(tx *sql.Tx, tableName string) (map[string]sqliteTableColumn, error) {
	rows, err := tx.Query(`PRAGMA table_info(` + tableName + `)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	columns := map[string]sqliteTableColumn{}
	for rows.Next() {
		var cid int64
		var name string
		var columnType string
		var notNull int64
		var defaultValue sql.NullString
		var primaryKey int64
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return nil, err
		}
		columns[name] = sqliteTableColumn{Name: name, NotNull: notNull != 0}
	}
	return columns, rows.Err()
}

func (db *DB) sqliteTableColumns(tableName string) (map[string]sqliteTableColumn, error) {
	rows, err := db.Query(`PRAGMA table_info(` + tableName + `)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	columns := map[string]sqliteTableColumn{}
	for rows.Next() {
		var cid int64
		var name string
		var columnType string
		var notNull int64
		var defaultValue sql.NullString
		var primaryKey int64
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return nil, err
		}
		columns[name] = sqliteTableColumn{Name: name, NotNull: notNull != 0}
	}
	return columns, rows.Err()
}

func (db *DB) sqliteTableExists(tableName string) (bool, error) {
	var exists int64
	err := db.QueryRow(`
		SELECT COUNT(*)
		FROM sqlite_master
		WHERE type = 'table'
		  AND name = ?
	`, tableName).Scan(&exists)
	if err != nil {
		return false, err
	}
	return exists > 0, nil
}

func sqliteColumnExists(columns map[string]sqliteTableColumn, name string) bool {
	_, ok := columns[name]
	return ok
}
