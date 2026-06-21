-- +goose Up
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
);

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
);

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
);

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

CREATE INDEX IF NOT EXISTS idx_proxy_request_events_listener_id
ON proxy_request_events (listener_id);

CREATE INDEX IF NOT EXISTS idx_proxy_request_events_route_id
ON proxy_request_events (route_id);

CREATE INDEX IF NOT EXISTS idx_proxy_request_events_route_target_id
ON proxy_request_events (route_target_id);

CREATE INDEX IF NOT EXISTS idx_proxy_request_events_agent_id
ON proxy_request_events (agent_id);

CREATE INDEX IF NOT EXISTS idx_proxy_request_events_recent_problem
ON proxy_request_events (occurred_at DESC)
WHERE status_code >= 400 OR error_kind != '';

CREATE INDEX IF NOT EXISTS idx_public_routes_listener_priority
ON public_routes (listener_id, priority, id);

CREATE UNIQUE INDEX IF NOT EXISTS idx_public_routes_one_default_per_listener
ON public_routes (listener_id)
WHERE is_default = 1;

CREATE INDEX IF NOT EXISTS idx_public_response_templates_kind
ON public_response_templates (kind, name);

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

CREATE INDEX IF NOT EXISTS idx_public_tls_certificates_listener_id
ON public_tls_certificates (listener_id);

CREATE INDEX IF NOT EXISTS idx_public_tls_certificates_dns_credential_id
ON public_tls_certificates (dns_credential_id);

CREATE INDEX IF NOT EXISTS idx_public_rate_limit_rules_priority
ON public_rate_limit_rules (priority, id);

CREATE INDEX IF NOT EXISTS idx_public_rate_limit_rules_response_body_template_id
ON public_rate_limit_rules (response_body_template_id);

CREATE INDEX IF NOT EXISTS idx_public_traffic_shaper_rules_priority
ON public_traffic_shaper_rules (priority, id);

CREATE INDEX IF NOT EXISTS idx_public_waf_rules_priority
ON public_waf_rules (priority, id);

CREATE INDEX IF NOT EXISTS idx_public_waf_rules_captcha_provider_id
ON public_waf_rules (captcha_provider_id);

CREATE INDEX IF NOT EXISTS idx_public_waf_rules_block_response_template_id
ON public_waf_rules (block_response_template_id);

CREATE INDEX IF NOT EXISTS idx_public_waf_rules_captcha_page_template_id
ON public_waf_rules (captcha_page_template_id);

CREATE INDEX IF NOT EXISTS idx_public_waf_rules_waiting_room_page_template_id
ON public_waf_rules (waiting_room_page_template_id);

CREATE INDEX IF NOT EXISTS idx_proxy_request_events_waf_rule_id
ON proxy_request_events (waf_rule_id);

CREATE INDEX IF NOT EXISTS idx_proxy_request_events_cache_rule_id
ON proxy_request_events (cache_rule_id);

CREATE INDEX IF NOT EXISTS idx_public_cache_rules_priority
ON public_cache_rules (priority, id);

CREATE INDEX IF NOT EXISTS idx_public_cache_entries_rule_id
ON public_cache_entries (rule_id);

CREATE INDEX IF NOT EXISTS idx_public_cache_entries_route_target_id
ON public_cache_entries (route_target_id);

CREATE INDEX IF NOT EXISTS idx_public_cache_entries_expires_at
ON public_cache_entries (expires_at);

CREATE INDEX IF NOT EXISTS idx_public_cache_entries_last_accessed_at
ON public_cache_entries (last_accessed_at);

CREATE INDEX IF NOT EXISTS idx_agent_stats_reported_at
ON agent_stats (reported_at);

CREATE INDEX IF NOT EXISTS idx_agent_stats_agent_id
ON agent_stats (agent_id);

CREATE INDEX IF NOT EXISTS idx_connections_agent_id
ON connections (agent_id);

CREATE INDEX IF NOT EXISTS idx_connections_connected_at
ON connections (connected_at);

CREATE INDEX IF NOT EXISTS idx_connections_disconnected_at
ON connections (disconnected_at);

-- +goose Down
-- The baseline migration is intentionally non-destructive. Runtime downgrades are not supported.
SELECT 1;
