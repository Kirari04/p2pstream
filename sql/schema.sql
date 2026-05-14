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
    listener_id INTEGER,
    backend_id INTEGER,
    route_id INTEGER,
    waf_rule_id INTEGER,
    waf_action TEXT NOT NULL DEFAULT '',
    agent_id INTEGER REFERENCES agents(id),
    request_bytes INTEGER NOT NULL DEFAULT 0,
    response_bytes INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS public_backends (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL UNIQUE,
    target_origin TEXT NOT NULL,
    backend_type TEXT NOT NULL DEFAULT 'proxy_forward',
    forward_mode TEXT NOT NULL DEFAULT 'direct',
    load_balancing TEXT NOT NULL DEFAULT 'round_robin',
    tls_skip_verify INTEGER NOT NULL DEFAULT 0,
    static_status_code INTEGER NOT NULL DEFAULT 200,
    static_response_body TEXT NOT NULL DEFAULT '',
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
    enabled INTEGER NOT NULL DEFAULT 1,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS public_backend_headers (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    backend_id INTEGER NOT NULL REFERENCES public_backends(id) ON DELETE CASCADE,
    position INTEGER NOT NULL,
    name TEXT NOT NULL,
    value TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(backend_id, position)
);

CREATE TABLE IF NOT EXISTS public_backend_upstream_headers (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    backend_id INTEGER NOT NULL REFERENCES public_backends(id) ON DELETE CASCADE,
    position INTEGER NOT NULL,
    name TEXT NOT NULL,
    value TEXT NOT NULL,
    sensitive INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(backend_id, position)
);

CREATE TABLE IF NOT EXISTS public_backend_agents (
    backend_id INTEGER NOT NULL REFERENCES public_backends(id) ON DELETE CASCADE,
    agent_id INTEGER NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    position INTEGER NOT NULL,
    weight INTEGER NOT NULL DEFAULT 100,
    enabled INTEGER NOT NULL DEFAULT 1,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (backend_id, agent_id),
    UNIQUE(backend_id, position)
);

CREATE TABLE IF NOT EXISTS public_listeners (
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

CREATE TABLE IF NOT EXISTS public_routes (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    listener_id INTEGER NOT NULL REFERENCES public_listeners(id) ON DELETE CASCADE,
    priority INTEGER NOT NULL,
    host_pattern TEXT NOT NULL DEFAULT '',
    path_prefix TEXT NOT NULL DEFAULT '',
    backend_id INTEGER REFERENCES public_backends(id),
    load_balancing TEXT NOT NULL DEFAULT 'round_robin',
    fallback_backend_id INTEGER REFERENCES public_backends(id),
    action TEXT NOT NULL DEFAULT 'forward',
    redirect_target_mode TEXT NOT NULL DEFAULT '',
    redirect_target TEXT NOT NULL DEFAULT '',
    redirect_status_code INTEGER NOT NULL DEFAULT 302,
    redirect_preserve_path_suffix INTEGER NOT NULL DEFAULT 1,
    redirect_preserve_query INTEGER NOT NULL DEFAULT 1,
    enabled INTEGER NOT NULL DEFAULT 1,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

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

CREATE TABLE IF NOT EXISTS public_waf_settings (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    cookie_signing_secret TEXT NOT NULL,
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

CREATE INDEX IF NOT EXISTS idx_proxy_request_events_occurred_at
ON proxy_request_events (occurred_at);

CREATE INDEX IF NOT EXISTS idx_proxy_request_events_listener_id
ON proxy_request_events (listener_id);

CREATE INDEX IF NOT EXISTS idx_proxy_request_events_backend_id
ON proxy_request_events (backend_id);

CREATE INDEX IF NOT EXISTS idx_proxy_request_events_route_id
ON proxy_request_events (route_id);

CREATE INDEX IF NOT EXISTS idx_proxy_request_events_agent_id
ON proxy_request_events (agent_id);

CREATE INDEX IF NOT EXISTS idx_public_routes_listener_priority
ON public_routes (listener_id, priority, id);

CREATE INDEX IF NOT EXISTS idx_public_backend_headers_backend_position
ON public_backend_headers (backend_id, position);

CREATE INDEX IF NOT EXISTS idx_public_backend_upstream_headers_backend_position
ON public_backend_upstream_headers (backend_id, position);

CREATE INDEX IF NOT EXISTS idx_public_backend_agents_backend_position
ON public_backend_agents (backend_id, position);

CREATE INDEX IF NOT EXISTS idx_public_backend_agents_agent_id
ON public_backend_agents (agent_id);

CREATE INDEX IF NOT EXISTS idx_public_route_backends_route_position
ON public_route_backends (route_id, position);

CREATE INDEX IF NOT EXISTS idx_public_route_backends_backend_id
ON public_route_backends (backend_id);

CREATE INDEX IF NOT EXISTS idx_public_tls_certificates_listener_id
ON public_tls_certificates (listener_id);

CREATE INDEX IF NOT EXISTS idx_public_tls_certificates_dns_credential_id
ON public_tls_certificates (dns_credential_id);

CREATE INDEX IF NOT EXISTS idx_public_rate_limit_rules_priority
ON public_rate_limit_rules (priority, id);

CREATE INDEX IF NOT EXISTS idx_public_traffic_shaper_rules_priority
ON public_traffic_shaper_rules (priority, id);

CREATE INDEX IF NOT EXISTS idx_public_waf_rules_priority
ON public_waf_rules (priority, id);

CREATE INDEX IF NOT EXISTS idx_public_waf_rules_captcha_provider_id
ON public_waf_rules (captcha_provider_id);

CREATE INDEX IF NOT EXISTS idx_proxy_request_events_waf_rule_id
ON proxy_request_events (waf_rule_id);

CREATE INDEX IF NOT EXISTS idx_agent_stats_reported_at
ON agent_stats (reported_at);

CREATE INDEX IF NOT EXISTS idx_agent_stats_agent_id
ON agent_stats (agent_id);

CREATE INDEX IF NOT EXISTS idx_connections_agent_id
ON connections (agent_id);

CREATE INDEX IF NOT EXISTS idx_connections_connected_at
ON connections (connected_at);
