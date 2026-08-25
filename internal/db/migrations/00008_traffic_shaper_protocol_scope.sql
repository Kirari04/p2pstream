-- +goose Up
-- Some pre-Goose databases are adopted at the baseline version even when this
-- policy table has not been created yet, so keep the ALTER target self-contained.
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

ALTER TABLE public_traffic_shaper_rules ADD COLUMN protocol_scope TEXT NOT NULL DEFAULT 'all';

-- +goose Down
ALTER TABLE public_traffic_shaper_rules DROP COLUMN protocol_scope;
