-- +goose Up
ALTER TABLE proxy_request_events ADD COLUMN retry_failed_agent_id INTEGER REFERENCES agents(id);

CREATE TABLE IF NOT EXISTS proxy_retry_rollup_minutes (
    bucket_unix_millis INTEGER NOT NULL,
    retry_rule_id INTEGER NOT NULL,
    failed_agent_id INTEGER NOT NULL DEFAULT 0,
    error_kind TEXT NOT NULL DEFAULT '',
    matched_requests INTEGER NOT NULL DEFAULT 0,
    retried_requests INTEGER NOT NULL DEFAULT 0,
    retry_attempts INTEGER NOT NULL DEFAULT 0,
    recovered_requests INTEGER NOT NULL DEFAULT 0,
    exhausted_requests INTEGER NOT NULL DEFAULT 0,
    skipped_requests INTEGER NOT NULL DEFAULT 0,
    duration_ms_sum INTEGER NOT NULL DEFAULT 0,
    retried_duration_ms_sum INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (bucket_unix_millis, retry_rule_id, failed_agent_id, error_kind)
);

CREATE INDEX IF NOT EXISTS idx_proxy_retry_rollup_rule
ON proxy_retry_rollup_minutes (retry_rule_id, bucket_unix_millis);

CREATE INDEX IF NOT EXISTS idx_proxy_retry_rollup_failed_agent
ON proxy_retry_rollup_minutes (failed_agent_id, bucket_unix_millis)
WHERE failed_agent_id != 0;

CREATE INDEX IF NOT EXISTS idx_proxy_retry_rollup_error_kind
ON proxy_retry_rollup_minutes (error_kind, bucket_unix_millis)
WHERE error_kind != '';

INSERT INTO proxy_retry_rollup_minutes (
    bucket_unix_millis, retry_rule_id, failed_agent_id, error_kind,
    matched_requests, retried_requests, retry_attempts, recovered_requests,
    exhausted_requests, skipped_requests, duration_ms_sum, retried_duration_ms_sum
)
SELECT
    CAST((unixepoch(occurred_at) / 60) * 60 * 1000 AS INTEGER),
    retry_rule_id,
    0,
    retry_error_kind,
    COUNT(*),
    CAST(COALESCE(SUM(CASE WHEN retry_count > 0 THEN 1 ELSE 0 END), 0) AS INTEGER),
    CAST(COALESCE(SUM(retry_count), 0) AS INTEGER),
    CAST(COALESCE(SUM(CASE WHEN retry_outcome = 'recovered' THEN 1 ELSE 0 END), 0) AS INTEGER),
    CAST(COALESCE(SUM(CASE WHEN retry_outcome = 'exhausted' THEN 1 ELSE 0 END), 0) AS INTEGER),
    CAST(COALESCE(SUM(CASE WHEN retry_outcome = 'skipped' THEN 1 ELSE 0 END), 0) AS INTEGER),
    CAST(COALESCE(SUM(duration_ms), 0) AS INTEGER),
    CAST(COALESCE(SUM(CASE WHEN retry_count > 0 THEN duration_ms ELSE 0 END), 0) AS INTEGER)
FROM proxy_request_events
WHERE retry_rule_id IS NOT NULL
GROUP BY 1, retry_rule_id, retry_error_kind;

-- +goose Down
DROP INDEX IF EXISTS idx_proxy_retry_rollup_error_kind;
DROP INDEX IF EXISTS idx_proxy_retry_rollup_failed_agent;
DROP INDEX IF EXISTS idx_proxy_retry_rollup_rule;
DROP TABLE IF EXISTS proxy_retry_rollup_minutes;
ALTER TABLE proxy_request_events DROP COLUMN retry_failed_agent_id;
