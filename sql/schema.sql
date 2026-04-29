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
    error_kind TEXT NOT NULL DEFAULT ''
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

CREATE INDEX IF NOT EXISTS idx_agent_stats_reported_at
ON agent_stats (reported_at);

CREATE INDEX IF NOT EXISTS idx_connections_connected_at
ON connections (connected_at);
