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
    status_code, duration_ms, error_kind, listener_id, backend_id, route_id
) VALUES (
    ?, ?, ?, ?, ?, ?
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
    s.last_seen_at,
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
WHERE id = ?
  AND last_seen_at < datetime('now', '-30 seconds');

-- name: RevokeSessionByTokenHash :exec
UPDATE sessions
SET revoked_at = CURRENT_TIMESTAMP
WHERE token_hash = ? AND revoked_at IS NULL;

-- name: CountPublicBackends :one
SELECT COUNT(*) FROM public_backends;

-- name: CountPublicListeners :one
SELECT COUNT(*) FROM public_listeners;

-- name: CreatePublicBackend :one
INSERT INTO public_backends (
    name,
    target_origin,
    backend_type,
    tls_skip_verify,
    static_status_code,
    static_response_body,
    enabled
) VALUES (
    ?, ?, ?, ?, ?, ?, ?
)
RETURNING id, name, target_origin, backend_type, tls_skip_verify, static_status_code, static_response_body, enabled, created_at, updated_at;

-- name: ListPublicBackends :many
SELECT id, name, target_origin, backend_type, tls_skip_verify, static_status_code, static_response_body, enabled, created_at, updated_at
FROM public_backends
ORDER BY name ASC, id ASC;

-- name: GetPublicBackend :one
SELECT id, name, target_origin, backend_type, tls_skip_verify, static_status_code, static_response_body, enabled, created_at, updated_at
FROM public_backends
WHERE id = ?;

-- name: UpdatePublicBackend :one
UPDATE public_backends
SET name = ?,
    target_origin = ?,
    backend_type = ?,
    tls_skip_verify = ?,
    static_status_code = ?,
    static_response_body = ?,
    enabled = ?,
    updated_at = CURRENT_TIMESTAMP
WHERE id = ?
RETURNING id, name, target_origin, backend_type, tls_skip_verify, static_status_code, static_response_body, enabled, created_at, updated_at;

-- name: DeletePublicBackend :exec
DELETE FROM public_backends
WHERE id = ?;

-- name: ListPublicBackendHeaders :many
SELECT id, backend_id, position, name, value, created_at, updated_at
FROM public_backend_headers
ORDER BY backend_id ASC, position ASC, id ASC;

-- name: ListPublicBackendHeadersByBackend :many
SELECT id, backend_id, position, name, value, created_at, updated_at
FROM public_backend_headers
WHERE backend_id = ?
ORDER BY position ASC, id ASC;

-- name: CreatePublicBackendHeader :one
INSERT INTO public_backend_headers (backend_id, position, name, value)
VALUES (?, ?, ?, ?)
RETURNING id, backend_id, position, name, value, created_at, updated_at;

-- name: DeletePublicBackendHeaders :exec
DELETE FROM public_backend_headers
WHERE backend_id = ?;

-- name: CountPublicBackendEnabledReferences :one
SELECT
  (
    SELECT COUNT(*)
    FROM public_listeners
    WHERE default_backend_id = ? AND enabled = 1
  ) + (
    SELECT COUNT(*)
    FROM public_routes
    WHERE backend_id = ? AND enabled = 1
  ) AS references_count;

-- name: CreatePublicListener :one
INSERT INTO public_listeners (name, bind_address, port, protocol, enabled, default_backend_id)
VALUES (?, ?, ?, ?, ?, ?)
RETURNING id, name, bind_address, port, protocol, enabled, default_backend_id, created_at, updated_at;

-- name: ListPublicListeners :many
SELECT id, name, bind_address, port, protocol, enabled, default_backend_id, created_at, updated_at
FROM public_listeners
ORDER BY port ASC, bind_address ASC, id ASC;

-- name: GetPublicListener :one
SELECT id, name, bind_address, port, protocol, enabled, default_backend_id, created_at, updated_at
FROM public_listeners
WHERE id = ?;

-- name: UpdatePublicListener :one
UPDATE public_listeners
SET name = ?, bind_address = ?, port = ?, protocol = ?, enabled = ?, default_backend_id = ?, updated_at = CURRENT_TIMESTAMP
WHERE id = ?
RETURNING id, name, bind_address, port, protocol, enabled, default_backend_id, created_at, updated_at;

-- name: SetPublicListenerEnabled :one
UPDATE public_listeners
SET enabled = ?, updated_at = CURRENT_TIMESTAMP
WHERE id = ?
RETURNING id, name, bind_address, port, protocol, enabled, default_backend_id, created_at, updated_at;

-- name: DeletePublicListener :exec
DELETE FROM public_listeners
WHERE id = ?;

-- name: CreatePublicRoute :one
INSERT INTO public_routes (listener_id, priority, host_pattern, path_prefix, backend_id, enabled)
VALUES (?, ?, ?, ?, ?, ?)
RETURNING id, listener_id, priority, host_pattern, path_prefix, backend_id, enabled, created_at, updated_at;

-- name: ListPublicRoutes :many
SELECT id, listener_id, priority, host_pattern, path_prefix, backend_id, enabled, created_at, updated_at
FROM public_routes
ORDER BY listener_id ASC, priority ASC, id ASC;

-- name: GetPublicRoute :one
SELECT id, listener_id, priority, host_pattern, path_prefix, backend_id, enabled, created_at, updated_at
FROM public_routes
WHERE id = ?;

-- name: UpdatePublicRoute :one
UPDATE public_routes
SET listener_id = ?, priority = ?, host_pattern = ?, path_prefix = ?, backend_id = ?, enabled = ?, updated_at = CURRENT_TIMESTAMP
WHERE id = ?
RETURNING id, listener_id, priority, host_pattern, path_prefix, backend_id, enabled, created_at, updated_at;

-- name: DeletePublicRoute :exec
DELETE FROM public_routes
WHERE id = ?;

-- name: CreatePublicTlsCertificate :one
INSERT INTO public_tls_certificates (listener_id, hostname_pattern, cert_path, key_path, enabled)
VALUES (?, ?, ?, ?, ?)
RETURNING id, listener_id, hostname_pattern, cert_path, key_path, enabled, created_at, updated_at;

-- name: ListPublicTlsCertificates :many
SELECT id, listener_id, hostname_pattern, cert_path, key_path, enabled, created_at, updated_at
FROM public_tls_certificates
ORDER BY listener_id ASC, hostname_pattern ASC, id ASC;

-- name: GetPublicTlsCertificate :one
SELECT id, listener_id, hostname_pattern, cert_path, key_path, enabled, created_at, updated_at
FROM public_tls_certificates
WHERE id = ?;

-- name: UpdatePublicTlsCertificate :one
UPDATE public_tls_certificates
SET listener_id = ?, hostname_pattern = ?, cert_path = ?, key_path = ?, enabled = ?, updated_at = CURRENT_TIMESTAMP
WHERE id = ?
RETURNING id, listener_id, hostname_pattern, cert_path, key_path, enabled, created_at, updated_at;

-- name: DeletePublicTlsCertificate :exec
DELETE FROM public_tls_certificates
WHERE id = ?;
