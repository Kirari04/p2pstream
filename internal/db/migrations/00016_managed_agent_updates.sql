-- +goose Up
CREATE TABLE IF NOT EXISTS agent_update_management_authority (
    id INTEGER PRIMARY KEY CHECK(id = 1),
    key_id TEXT NOT NULL UNIQUE,
    public_key BLOB NOT NULL CHECK(length(public_key) = 32),
    epoch INTEGER NOT NULL CHECK(epoch > 0),
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS agent_updater_identities (
    agent_id INTEGER PRIMARY KEY REFERENCES agents(id) ON DELETE CASCADE,
    updater_key_id TEXT NOT NULL UNIQUE,
    updater_public_key BLOB NOT NULL CHECK(length(updater_public_key) = 32),
    activator_key_id TEXT NOT NULL UNIQUE,
    activator_public_key BLOB NOT NULL CHECK(length(activator_public_key) = 32),
    os TEXT NOT NULL,
    arch TEXT NOT NULL,
    updater_version TEXT NOT NULL,
    pinned_repository TEXT NOT NULL,
    authority_key_id TEXT NOT NULL,
    authority_epoch INTEGER NOT NULL CHECK(authority_epoch > 0),
    enrollment_generation INTEGER NOT NULL CHECK(enrollment_generation > 0),
    enrollment_receipt_payload BLOB NOT NULL,
    enrollment_receipt_signature BLOB NOT NULL CHECK(length(enrollment_receipt_signature) = 64),
    enabled INTEGER NOT NULL DEFAULT 1 CHECK(enabled IN (0, 1)),
    last_counter INTEGER NOT NULL DEFAULT 0 CHECK(last_counter >= 0),
    last_command_sequence INTEGER NOT NULL DEFAULT 0 CHECK(last_command_sequence >= 0),
    last_root_action_counter INTEGER NOT NULL DEFAULT 0 CHECK(last_root_action_counter >= 0),
    enrolled_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_seen_at DATETIME,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS agent_updater_enrollment_tokens (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    agent_id INTEGER NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    token_hash TEXT NOT NULL UNIQUE,
    pinned_repository TEXT NOT NULL,
    authority_key_id TEXT NOT NULL,
    authority_epoch INTEGER NOT NULL CHECK(authority_epoch > 0),
    enrollment_generation INTEGER NOT NULL CHECK(enrollment_generation > 0),
    expires_at DATETIME NOT NULL,
    used_at DATETIME,
    receipt_expires_at DATETIME,
    updater_key_id TEXT NOT NULL DEFAULT '',
    activator_key_id TEXT NOT NULL DEFAULT '',
    os TEXT NOT NULL DEFAULT '',
    arch TEXT NOT NULL DEFAULT '',
    updater_version TEXT NOT NULL DEFAULT '',
    receipt_payload BLOB NOT NULL DEFAULT X'',
    receipt_signature BLOB NOT NULL DEFAULT X'',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_agent_updater_enrollment_tokens_agent ON agent_updater_enrollment_tokens(agent_id,expires_at);
CREATE TABLE IF NOT EXISTS agent_update_campaigns (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL,
    state TEXT NOT NULL CHECK(state IN ('running','paused','cancelled','completed')),
    generation INTEGER NOT NULL DEFAULT 1 CHECK(generation > 0),
    target_version TEXT NOT NULL,
    target_commit TEXT NOT NULL,
    manifest_sha256 TEXT NOT NULL,
    release_sequence INTEGER NOT NULL CHECK(release_sequence >= 0),
    security_epoch INTEGER NOT NULL DEFAULT 0 CHECK(security_epoch >= 0),
    minimum_updater_version TEXT NOT NULL DEFAULT '',
    minimum_tunnel_protocol INTEGER NOT NULL DEFAULT 0 CHECK(minimum_tunnel_protocol >= 0),
    maximum_tunnel_protocol INTEGER NOT NULL DEFAULT 0 CHECK(maximum_tunnel_protocol >= 0),
    artifacts_json TEXT NOT NULL,
    max_unavailable INTEGER NOT NULL CHECK(max_unavailable > 0),
    minimum_eligible_agents_per_route INTEGER NOT NULL CHECK(minimum_eligible_agents_per_route > 0),
    canary_count INTEGER NOT NULL CHECK(canary_count > 0),
    wave_size INTEGER NOT NULL CHECK(wave_size > 0),
    healthy_dwell_millis INTEGER NOT NULL CHECK(healthy_dwell_millis >= 0),
    created_by_user_id INTEGER REFERENCES users(id) ON DELETE SET NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    completed_at DATETIME
);
CREATE TABLE IF NOT EXISTS agent_update_assignments (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    campaign_id INTEGER NOT NULL REFERENCES agent_update_campaigns(id) ON DELETE CASCADE,
    agent_id INTEGER NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    state TEXT NOT NULL CHECK(state IN ('pending','staging','staged','cordoned','activating','awaiting_tunnel','healthy_dwell','succeeded','failed','cancelled','blocked')),
    desired_action TEXT NOT NULL CHECK(desired_action IN ('none','stage','activate','rollback')),
    generation INTEGER NOT NULL DEFAULT 1 CHECK(generation > 0),
    cordoned INTEGER NOT NULL DEFAULT 0 CHECK(cordoned IN (0,1)),
    failure_code TEXT NOT NULL DEFAULT '',
    failure_detail TEXT NOT NULL DEFAULT '',
    attested_manifest_sha256 TEXT NOT NULL DEFAULT '',
    attested_binary_sha256 TEXT NOT NULL DEFAULT '',
    attested_activation_counter INTEGER NOT NULL DEFAULT 0 CHECK(attested_activation_counter >= 0),
    activation_nonce_hash TEXT NOT NULL DEFAULT '',
    authorization_action TEXT NOT NULL DEFAULT '' CHECK(authorization_action IN ('','activate','rollback')),
    authorization_server_version TEXT NOT NULL DEFAULT '',
    command_sequence INTEGER NOT NULL DEFAULT 0 CHECK(command_sequence >= 0),
    authorization_nonce BLOB NOT NULL DEFAULT X'',
    authorization_sha256 TEXT NOT NULL DEFAULT '',
    authorization_payload BLOB NOT NULL DEFAULT X'',
    authorization_signature BLOB NOT NULL DEFAULT X'',
    authorization_issued_at DATETIME,
    authorization_expires_at DATETIME,
    root_action_counter INTEGER NOT NULL DEFAULT 0 CHECK(root_action_counter >= 0),
    root_action_receipt_payload BLOB NOT NULL DEFAULT X'',
    root_action_receipt_signature BLOB NOT NULL DEFAULT X'',
    root_action_completed_at DATETIME,
    root_result_kind TEXT NOT NULL DEFAULT '' CHECK(root_result_kind IN ('','release','bootstrap')),
    root_result_manifest_sha256 TEXT NOT NULL DEFAULT '',
    root_result_version TEXT NOT NULL DEFAULT '',
    root_result_commit TEXT NOT NULL DEFAULT '',
    root_result_release_sequence INTEGER NOT NULL DEFAULT 0 CHECK(root_result_release_sequence >= 0),
    root_result_security_epoch INTEGER NOT NULL DEFAULT 0 CHECK(root_result_security_epoch >= 0),
    root_result_os TEXT NOT NULL DEFAULT '',
    root_result_arch TEXT NOT NULL DEFAULT '',
    root_result_artifact_name TEXT NOT NULL DEFAULT '',
    root_result_artifact_size INTEGER NOT NULL DEFAULT 0 CHECK(root_result_artifact_size >= 0),
    root_result_artifact_sha256 TEXT NOT NULL DEFAULT '',
    running_version TEXT NOT NULL DEFAULT '',
    running_commit TEXT NOT NULL DEFAULT '',
    observed_version TEXT NOT NULL DEFAULT '',
    observed_commit TEXT NOT NULL DEFAULT '',
    activated_at DATETIME,
    fresh_tunnel_at DATETIME,
    healthy_at DATETIME,
    last_report_at DATETIME,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(campaign_id, agent_id)
);
CREATE INDEX IF NOT EXISTS idx_agent_update_assignments_agent_state ON agent_update_assignments(agent_id,state);
CREATE INDEX IF NOT EXISTS idx_agent_update_assignments_campaign_state ON agent_update_assignments(campaign_id,state);
CREATE INDEX IF NOT EXISTS idx_agent_update_assignments_cordoned ON agent_update_assignments(agent_id) WHERE cordoned=1;
CREATE UNIQUE INDEX IF NOT EXISTS idx_agent_update_assignments_one_active_agent ON agent_update_assignments(agent_id) WHERE state NOT IN ('succeeded','failed','cancelled') OR (state='failed' AND desired_action='rollback');
CREATE TABLE IF NOT EXISTS agent_update_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    campaign_id INTEGER NOT NULL REFERENCES agent_update_campaigns(id) ON DELETE CASCADE,
    assignment_id INTEGER REFERENCES agent_update_assignments(id) ON DELETE CASCADE,
    agent_id INTEGER REFERENCES agents(id) ON DELETE SET NULL,
    kind TEXT NOT NULL,
    detail TEXT NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_agent_update_events_assignment ON agent_update_events(assignment_id,id DESC);

-- +goose Down
-- Managed update state is security/audit data. Runtime downgrades are unsupported.
SELECT 1;
