-- +goose Up
-- Databases predating the agent registry are adopted at the baseline before
-- legacy compatibility runs, so keep the ALTER target self-contained.
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

ALTER TABLE agents ADD COLUMN agent_version TEXT NOT NULL DEFAULT '';
ALTER TABLE agents ADD COLUMN agent_commit TEXT NOT NULL DEFAULT '';

-- +goose Down
-- Runtime downgrades are not supported.
SELECT 1;
