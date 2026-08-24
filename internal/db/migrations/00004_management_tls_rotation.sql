-- +goose Up
CREATE TABLE IF NOT EXISTS management_agent_trust_reports (
    agent_id INTEGER PRIMARY KEY REFERENCES agents(id) ON DELETE CASCADE,
    installed_generation INTEGER NOT NULL DEFAULT 0,
    installed_bundle_sha256 TEXT NOT NULL DEFAULT '',
    install_state TEXT NOT NULL DEFAULT 'unsupported',
    error_code TEXT NOT NULL DEFAULT '',
    error_detail TEXT NOT NULL DEFAULT '',
    agent_version TEXT NOT NULL DEFAULT '',
    capabilities_json TEXT NOT NULL DEFAULT '[]',
    reported_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_management_agent_trust_reports_reported_at ON management_agent_trust_reports (reported_at);

-- +goose Down
DROP TABLE IF EXISTS management_agent_trust_reports;
