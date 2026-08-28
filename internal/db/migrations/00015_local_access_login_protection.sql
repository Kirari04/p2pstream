-- +goose Up
ALTER TABLE public_access_providers ADD COLUMN local_auth_login_username_max_failures INTEGER NOT NULL DEFAULT 5;
ALTER TABLE public_access_providers ADD COLUMN local_auth_login_client_max_failures INTEGER NOT NULL DEFAULT 25;
ALTER TABLE public_access_providers ADD COLUMN local_auth_login_window_millis INTEGER NOT NULL DEFAULT 900000;
ALTER TABLE public_access_providers ADD COLUMN local_auth_login_block_millis INTEGER NOT NULL DEFAULT 300000;

-- +goose Down
-- This migration is intentionally non-destructive. Runtime downgrades are not supported.
SELECT 1;
