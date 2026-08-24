-- +goose Up
CREATE TABLE IF NOT EXISTS public_access_providers (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL UNIQUE,
    provider_type TEXT NOT NULL DEFAULT 'forward_auth',
    enabled INTEGER NOT NULL DEFAULT 1,
    forward_auth_url TEXT NOT NULL,
    timeout_millis INTEGER NOT NULL DEFAULT 5000,
    tls_skip_verify INTEGER NOT NULL DEFAULT 0,
    subject_header TEXT NOT NULL DEFAULT 'X-Auth-Request-Preferred-Username',
    user_header TEXT NOT NULL DEFAULT 'X-Auth-Request-User',
    email_header TEXT NOT NULL DEFAULT 'X-Auth-Request-Email',
    groups_header TEXT NOT NULL DEFAULT 'X-Auth-Request-Groups',
    forwarded_headers_json TEXT NOT NULL DEFAULT '["X-Auth-Request-User","X-Auth-Request-Email","X-Auth-Request-Groups","X-Auth-Request-Preferred-Username"]',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS public_access_policies (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL UNIQUE,
    provider_id INTEGER NOT NULL REFERENCES public_access_providers(id) ON DELETE RESTRICT,
    enabled INTEGER NOT NULL DEFAULT 1,
    required_groups_json TEXT NOT NULL DEFAULT '[]',
    group_match TEXT NOT NULL DEFAULT 'any',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_public_access_policies_provider_id ON public_access_policies (provider_id);

-- +goose Down
-- This migration is intentionally non-destructive. Runtime downgrades are not supported.
SELECT 1;
