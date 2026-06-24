-- +goose Up
CREATE TABLE IF NOT EXISTS secret_encryption_state (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    schema_version INTEGER NOT NULL,
    provider TEXT NOT NULL,
    current_key_id TEXT NOT NULL,
    encryption_enabled INTEGER NOT NULL,
    encryption_required INTEGER NOT NULL,
    database_scanned INTEGER NOT NULL DEFAULT 0,
    database_encrypted INTEGER NOT NULL DEFAULT 0,
    database_rewrapped INTEGER NOT NULL DEFAULT 0,
    database_unchanged INTEGER NOT NULL DEFAULT 0,
    private_key_files_scanned INTEGER NOT NULL DEFAULT 0,
    private_key_files_encrypted INTEGER NOT NULL DEFAULT 0,
    private_key_files_rewrapped INTEGER NOT NULL DEFAULT 0,
    private_key_files_unchanged INTEGER NOT NULL DEFAULT 0,
    last_reconciled_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- +goose Down
DROP TABLE IF EXISTS secret_encryption_state;
