-- +goose Up
CREATE TABLE IF NOT EXISTS public_retry_rules (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL UNIQUE,
    priority INTEGER NOT NULL DEFAULT 100,
    enabled INTEGER NOT NULL DEFAULT 1,
    methods_json TEXT NOT NULL DEFAULT '["GET","HEAD"]',
    max_retries INTEGER NOT NULL DEFAULT 1,
    failure_mode TEXT NOT NULL DEFAULT 'connection_failures',
    body_mode TEXT NOT NULL DEFAULT 'never',
    max_replay_body_bytes INTEGER NOT NULL DEFAULT 0,
    route_ids_json TEXT NOT NULL DEFAULT '[]',
    target_ids_json TEXT NOT NULL DEFAULT '[]',
    match_json TEXT NOT NULL DEFAULT '{}',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_public_retry_rules_priority
ON public_retry_rules (priority, id);

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

ALTER TABLE proxy_request_events ADD COLUMN retry_rule_id INTEGER;
ALTER TABLE proxy_request_events ADD COLUMN retry_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE proxy_request_events ADD COLUMN retry_outcome TEXT NOT NULL DEFAULT '';
ALTER TABLE proxy_request_events ADD COLUMN retry_error_kind TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_proxy_request_events_retry_rule_id
ON proxy_request_events (retry_rule_id);

DROP INDEX IF EXISTS idx_proxy_request_events_recent_problem;
CREATE INDEX idx_proxy_request_events_recent_problem
ON proxy_request_events (occurred_at DESC)
WHERE status_code >= 400 OR error_kind != '' OR retry_count > 0;

-- +goose Down
DROP INDEX IF EXISTS idx_proxy_request_events_retry_rule_id;
DROP INDEX IF EXISTS idx_public_retry_rules_priority;
DROP TABLE IF EXISTS public_retry_rules;
