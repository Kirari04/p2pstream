-- +goose Up
ALTER TABLE public_retry_rules ADD COLUMN max_buffered_response_wait_millis INTEGER NOT NULL DEFAULT 30000;

-- +goose Down
ALTER TABLE public_retry_rules DROP COLUMN max_buffered_response_wait_millis;
