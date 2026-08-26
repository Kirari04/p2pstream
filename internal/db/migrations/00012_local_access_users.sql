-- +goose Up
ALTER TABLE public_access_providers ADD COLUMN local_auth_mode TEXT NOT NULL DEFAULT 'form';
ALTER TABLE public_access_providers ADD COLUMN local_auth_session_duration_millis INTEGER NOT NULL DEFAULT 604800000;
ALTER TABLE public_access_providers ADD COLUMN local_auth_realm TEXT NOT NULL DEFAULT 'Restricted';

CREATE TABLE public_access_users (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    provider_id INTEGER NOT NULL REFERENCES public_access_providers(id) ON DELETE CASCADE,
    username TEXT NOT NULL,
    password_hash TEXT NOT NULL,
    enabled INTEGER NOT NULL DEFAULT 1,
    groups_json TEXT NOT NULL DEFAULT '[]',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(provider_id, username)
);

CREATE TABLE public_access_sessions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    provider_id INTEGER NOT NULL REFERENCES public_access_providers(id) ON DELETE CASCADE,
    user_id INTEGER NOT NULL REFERENCES public_access_users(id) ON DELETE CASCADE,
    token_hash TEXT NOT NULL UNIQUE,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_seen_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at DATETIME NOT NULL,
    revoked_at DATETIME
);

CREATE INDEX idx_public_access_users_provider_id ON public_access_users (provider_id);
CREATE INDEX idx_public_access_sessions_provider_id ON public_access_sessions (provider_id);
CREATE INDEX idx_public_access_sessions_user_id ON public_access_sessions (user_id);
CREATE INDEX idx_public_access_sessions_expires_at ON public_access_sessions (expires_at);

-- +goose Down
-- This migration is intentionally non-destructive. Runtime downgrades are not supported.
SELECT 1;
