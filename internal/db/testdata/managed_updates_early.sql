-- Early development schema that was already stamped as Goose version 16.
CREATE TABLE agent_update_assignments (
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
    running_version TEXT NOT NULL DEFAULT '',
    running_commit TEXT NOT NULL DEFAULT '',
    activated_at DATETIME,
    fresh_tunnel_at DATETIME,
    healthy_at DATETIME,
    last_report_at DATETIME,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(campaign_id, agent_id)
);

CREATE TABLE agent_update_campaigns (
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

CREATE TABLE agent_update_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    campaign_id INTEGER NOT NULL REFERENCES agent_update_campaigns(id) ON DELETE CASCADE,
    assignment_id INTEGER REFERENCES agent_update_assignments(id) ON DELETE CASCADE,
    agent_id INTEGER REFERENCES agents(id) ON DELETE SET NULL,
    kind TEXT NOT NULL,
    detail TEXT NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE agent_updater_enrollment_tokens (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    agent_id INTEGER NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    token_hash TEXT NOT NULL UNIQUE,
    expires_at DATETIME NOT NULL,
    used_at DATETIME,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE agent_updater_identities (
    agent_id INTEGER PRIMARY KEY REFERENCES agents(id) ON DELETE CASCADE,
    updater_key_id TEXT NOT NULL UNIQUE,
    updater_public_key BLOB NOT NULL CHECK(length(updater_public_key) = 32),
    activator_key_id TEXT NOT NULL UNIQUE,
    activator_public_key BLOB NOT NULL CHECK(length(activator_public_key) = 32),
    os TEXT NOT NULL,
    arch TEXT NOT NULL,
    updater_version TEXT NOT NULL,
    enabled INTEGER NOT NULL DEFAULT 1 CHECK(enabled IN (0, 1)),
    last_counter INTEGER NOT NULL DEFAULT 0 CHECK(last_counter >= 0),
    last_activation_counter INTEGER NOT NULL DEFAULT 0 CHECK(last_activation_counter >= 0),
    enrolled_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_seen_at DATETIME,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
