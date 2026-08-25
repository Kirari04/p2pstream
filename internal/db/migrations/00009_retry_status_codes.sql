-- +goose Up
-- Some pre-Goose databases are adopted at the baseline version even when this
-- policy table has not been created yet, so keep the ALTER target self-contained.
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

ALTER TABLE public_retry_rules ADD COLUMN retry_status_codes_json TEXT NOT NULL DEFAULT '[]';

-- +goose Down
ALTER TABLE public_retry_rules DROP COLUMN retry_status_codes_json;
