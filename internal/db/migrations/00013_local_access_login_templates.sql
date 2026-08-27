-- +goose Up
ALTER TABLE public_access_providers ADD COLUMN local_auth_login_template_id INTEGER REFERENCES public_response_templates(id) ON DELETE RESTRICT;
CREATE INDEX idx_public_access_providers_local_auth_login_template_id ON public_access_providers (local_auth_login_template_id);

-- +goose Down
-- This migration is intentionally non-destructive. Runtime downgrades are not supported.
SELECT 1;
