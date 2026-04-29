CREATE TABLE IF NOT EXISTS connections (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    connected_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    disconnected_at DATETIME
);

CREATE TABLE IF NOT EXISTS agent_stats (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    reported_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    memory_mb INTEGER NOT NULL,
    goroutines INTEGER NOT NULL,
    req_success INTEGER NOT NULL,
    req_client_error INTEGER NOT NULL,
    req_server_error INTEGER NOT NULL,
    req_internal_error INTEGER NOT NULL DEFAULT 0,
    bytes_rx INTEGER NOT NULL,
    bytes_tx INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS proxy_request_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    occurred_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    status_code INTEGER NOT NULL,
    duration_ms INTEGER NOT NULL,
    error_kind TEXT NOT NULL DEFAULT '',
    listener_id INTEGER,
    backend_id INTEGER,
    route_id INTEGER
);

CREATE TABLE IF NOT EXISTS public_backends (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL UNIQUE,
    target_origin TEXT NOT NULL,
    backend_type TEXT NOT NULL DEFAULT 'proxy_forward',
    tls_skip_verify INTEGER NOT NULL DEFAULT 0,
    static_status_code INTEGER NOT NULL DEFAULT 200,
    static_response_body TEXT NOT NULL DEFAULT '',
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
    backend_id INTEGER NOT NULL REFERENCES public_backends(id),
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

CREATE INDEX IF NOT EXISTS idx_public_routes_listener_priority
ON public_routes (listener_id, priority, id);

CREATE INDEX IF NOT EXISTS idx_public_backend_headers_backend_position
ON public_backend_headers (backend_id, position);

CREATE INDEX IF NOT EXISTS idx_public_tls_certificates_listener_id
ON public_tls_certificates (listener_id);

CREATE INDEX IF NOT EXISTS idx_agent_stats_reported_at
ON agent_stats (reported_at);

CREATE INDEX IF NOT EXISTS idx_connections_connected_at
ON connections (connected_at);
