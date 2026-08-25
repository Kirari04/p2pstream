-- +goose Up
ALTER TABLE public_retry_rules ADD COLUMN response_body_mode TEXT NOT NULL DEFAULT 'stream';
ALTER TABLE public_retry_rules ADD COLUMN max_buffered_response_body_bytes INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE public_retry_rules DROP COLUMN max_buffered_response_body_bytes;
ALTER TABLE public_retry_rules DROP COLUMN response_body_mode;
