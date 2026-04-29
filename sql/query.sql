-- name: InsertConnection :one
INSERT INTO connections (connected_at)
VALUES (CURRENT_TIMESTAMP)
RETURNING id;

-- name: UpdateConnectionDisconnected :exec
UPDATE connections
SET disconnected_at = CURRENT_TIMESTAMP
WHERE id = ?;

-- name: InsertAgentStat :exec
INSERT INTO agent_stats (
    memory_mb, goroutines, req_success, req_client_error, req_server_error, req_internal_error, bytes_rx, bytes_tx
) VALUES (
    ?, ?, ?, ?, ?, ?, ?, ?
);

-- name: GetLatestAgentStat :one
SELECT * FROM agent_stats
ORDER BY id DESC
LIMIT 1;

-- name: InsertProxyRequestEvent :exec
INSERT INTO proxy_request_events (
    status_code, duration_ms, error_kind
) VALUES (
    ?, ?, ?
);

-- name: GetProxyRequestSummarySince :one
SELECT
    COUNT(*) AS total_requests,
    CAST(COALESCE(SUM(CASE WHEN status_code >= 200 AND status_code < 400 THEN 1 ELSE 0 END), 0) AS INTEGER) AS success,
    CAST(COALESCE(SUM(CASE WHEN status_code >= 400 AND status_code < 500 THEN 1 ELSE 0 END), 0) AS INTEGER) AS client_error,
    CAST(COALESCE(SUM(CASE WHEN status_code >= 500 THEN 1 ELSE 0 END), 0) AS INTEGER) AS server_error,
    CAST(COALESCE(SUM(CASE WHEN error_kind != '' THEN 1 ELSE 0 END), 0) AS INTEGER) AS internal_error,
    CAST(COALESCE(AVG(duration_ms), 0) AS INTEGER) AS avg_duration_ms
FROM proxy_request_events
WHERE occurred_at >= ?;

-- name: GetAgentStatsSummarySince :one
SELECT
    COUNT(*) AS samples,
    CAST(COALESCE(SUM(req_success), 0) AS INTEGER) AS req_success,
    CAST(COALESCE(SUM(req_client_error), 0) AS INTEGER) AS req_client_error,
    CAST(COALESCE(SUM(req_server_error), 0) AS INTEGER) AS req_server_error,
    CAST(COALESCE(SUM(req_internal_error), 0) AS INTEGER) AS req_internal_error,
    CAST(COALESCE(SUM(bytes_rx), 0) AS INTEGER) AS bytes_rx,
    CAST(COALESCE(SUM(bytes_tx), 0) AS INTEGER) AS bytes_tx,
    CAST(COALESCE(AVG(memory_mb), 0) AS INTEGER) AS avg_memory_mb,
    CAST(COALESCE(MAX(memory_mb), 0) AS INTEGER) AS max_memory_mb,
    CAST(COALESCE(AVG(goroutines), 0) AS INTEGER) AS avg_goroutines,
    CAST(COALESCE(MAX(goroutines), 0) AS INTEGER) AS max_goroutines
FROM agent_stats
WHERE reported_at >= ?;

-- name: GetConnectionSummarySince :one
SELECT
    COUNT(*) AS total_connections,
    MAX(connected_at) AS last_connected_at,
    MAX(disconnected_at) AS last_disconnected_at
FROM connections
WHERE connected_at >= ?;

-- name: GetActiveConnection :one
SELECT id, connected_at, disconnected_at
FROM connections
WHERE disconnected_at IS NULL
ORDER BY connected_at DESC
LIMIT 1;

-- name: DeleteProxyRequestEventsBefore :exec
DELETE FROM proxy_request_events
WHERE occurred_at < ?;

-- name: DeleteAgentStatsBefore :exec
DELETE FROM agent_stats
WHERE reported_at < ?;

-- name: DeleteDisconnectedConnectionsBefore :exec
DELETE FROM connections
WHERE disconnected_at IS NOT NULL
  AND disconnected_at < ?;

-- name: CountUsers :one
SELECT COUNT(*) FROM users;

-- name: CreateUser :one
INSERT INTO users (username, password_hash, role)
VALUES (?, ?, ?)
RETURNING id, username, role;

-- name: GetUserByUsername :one
SELECT id, username, password_hash, role, created_at, updated_at, disabled_at
FROM users
WHERE username = ? AND disabled_at IS NULL;

-- name: GetUserByID :one
SELECT id, username, password_hash, role, created_at, updated_at, disabled_at
FROM users
WHERE id = ? AND disabled_at IS NULL;

-- name: CreateSession :one
INSERT INTO sessions (user_id, token_hash, expires_at)
VALUES (?, ?, ?)
RETURNING id, user_id, token_hash, created_at, last_seen_at, expires_at, revoked_at;

-- name: GetActiveSessionByTokenHash :one
SELECT
    s.id AS session_id,
    s.user_id,
    s.expires_at,
    u.id,
    u.username,
    u.role
FROM sessions s
JOIN users u ON u.id = s.user_id
WHERE s.token_hash = ?
  AND s.revoked_at IS NULL
  AND s.expires_at > CURRENT_TIMESTAMP
  AND u.disabled_at IS NULL;

-- name: TouchSession :exec
UPDATE sessions
SET last_seen_at = CURRENT_TIMESTAMP
WHERE id = ?;

-- name: RevokeSessionByTokenHash :exec
UPDATE sessions
SET revoked_at = CURRENT_TIMESTAMP
WHERE token_hash = ? AND revoked_at IS NULL;
