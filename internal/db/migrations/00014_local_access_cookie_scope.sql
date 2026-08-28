-- +goose Up
ALTER TABLE public_access_providers ADD COLUMN local_auth_allowed_hosts_json TEXT NOT NULL DEFAULT '[]';
ALTER TABLE public_access_providers ADD COLUMN local_auth_cookie_same_site TEXT NOT NULL DEFAULT 'lax';
ALTER TABLE public_access_providers ADD COLUMN local_auth_cookie_domain TEXT NOT NULL DEFAULT '';
ALTER TABLE public_access_providers ADD COLUMN local_auth_cookie_secure INTEGER NOT NULL DEFAULT 0;
ALTER TABLE public_access_providers ADD COLUMN local_auth_cookie_name TEXT NOT NULL DEFAULT 'p2pstream_local_auth';

-- +goose Down
-- This migration is intentionally non-destructive. Runtime downgrades are not supported.
SELECT 1;
