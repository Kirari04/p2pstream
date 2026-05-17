-- name: InsertConnection :one
INSERT INTO connections (agent_id, connected_at)
VALUES (?, CURRENT_TIMESTAMP)
RETURNING id;

-- name: UpdateConnectionDisconnected :exec
UPDATE connections
SET disconnected_at = CURRENT_TIMESTAMP
WHERE id = ?;

-- name: InsertAgentStat :exec
INSERT INTO agent_stats (
    agent_id, memory_mb, goroutines, req_success, req_client_error, req_server_error, req_internal_error, bytes_rx, bytes_tx, cpu_percent
) VALUES (
    ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
);

-- name: GetLatestAgentStat :one
SELECT * FROM agent_stats
ORDER BY id DESC
LIMIT 1;

-- name: InsertProxyRequestEvent :exec
INSERT INTO proxy_request_events (
    status_code, duration_ms, error_kind, listener_id, backend_id, route_id, waf_rule_id, waf_action, agent_id, request_bytes, response_bytes, cache_rule_id, cache_status, cache_bytes
) VALUES (
    ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
);

-- name: GetProxyRequestSummarySince :one
SELECT
    COUNT(*) AS total_requests,
    CAST(COALESCE(SUM(CASE WHEN status_code >= 200 AND status_code < 400 THEN 1 ELSE 0 END), 0) AS INTEGER) AS success,
    CAST(COALESCE(SUM(CASE WHEN status_code >= 400 AND status_code < 500 THEN 1 ELSE 0 END), 0) AS INTEGER) AS client_error,
    CAST(COALESCE(SUM(CASE WHEN status_code >= 500 THEN 1 ELSE 0 END), 0) AS INTEGER) AS server_error,
    CAST(COALESCE(SUM(CASE WHEN error_kind != '' THEN 1 ELSE 0 END), 0) AS INTEGER) AS internal_error,
    CAST(COALESCE(AVG(duration_ms), 0) AS INTEGER) AS avg_duration_ms,
    CAST(COALESCE(SUM(request_bytes), 0) AS INTEGER) AS request_bytes,
    CAST(COALESCE(SUM(response_bytes), 0) AS INTEGER) AS response_bytes,
    CAST(COALESCE(SUM(request_bytes + response_bytes), 0) AS INTEGER) AS total_bytes,
    CAST(COALESCE(AVG(request_bytes), 0) AS INTEGER) AS avg_request_bytes,
    CAST(COALESCE(AVG(response_bytes), 0) AS INTEGER) AS avg_response_bytes,
    CAST(COALESCE(MAX(duration_ms), 0) AS INTEGER) AS max_duration_ms,
    CAST(COALESCE(SUM(CASE WHEN duration_ms >= 1000 THEN 1 ELSE 0 END), 0) AS INTEGER) AS slow_requests,
    CAST(COALESCE(SUM(CASE WHEN cache_status = 'hit' THEN 1 ELSE 0 END), 0) AS INTEGER) AS cache_hits,
    CAST(COALESCE(SUM(CASE WHEN cache_status IN ('miss', 'stored', 'store_failed') THEN 1 ELSE 0 END), 0) AS INTEGER) AS cache_misses,
    CAST(COALESCE(SUM(CASE WHEN cache_status = 'bypass' THEN 1 ELSE 0 END), 0) AS INTEGER) AS cache_bypasses,
    CAST(COALESCE(SUM(CASE WHEN cache_status = 'stored' THEN 1 ELSE 0 END), 0) AS INTEGER) AS cache_stored,
    CAST(COALESCE(SUM(CASE WHEN cache_status = 'store_failed' THEN 1 ELSE 0 END), 0) AS INTEGER) AS cache_store_failed,
    CAST(COALESCE(SUM(CASE WHEN cache_status = 'hit' THEN cache_bytes ELSE 0 END), 0) AS INTEGER) AS cache_hit_bytes,
    CAST(COALESCE(SUM(CASE WHEN cache_status = 'stored' THEN cache_bytes ELSE 0 END), 0) AS INTEGER) AS cache_stored_bytes
FROM proxy_request_events
WHERE occurred_at >= ?;

-- name: ListTopProxyListenersSince :many
SELECT
    CAST(COALESCE(pre.listener_id, 0) AS INTEGER) AS id,
    COALESCE(pl.name, CASE WHEN pre.listener_id IS NULL THEN 'unknown listener' ELSE 'listener #' || pre.listener_id END) AS label,
    COUNT(*) AS requests,
    CAST(COALESCE(SUM(CASE WHEN pre.status_code >= 200 AND pre.status_code < 400 THEN 1 ELSE 0 END), 0) AS INTEGER) AS success,
    CAST(COALESCE(SUM(CASE WHEN pre.status_code >= 400 AND pre.status_code < 500 THEN 1 ELSE 0 END), 0) AS INTEGER) AS client_error,
    CAST(COALESCE(SUM(CASE WHEN pre.status_code >= 500 THEN 1 ELSE 0 END), 0) AS INTEGER) AS server_error,
    CAST(COALESCE(SUM(CASE WHEN pre.error_kind != '' THEN 1 ELSE 0 END), 0) AS INTEGER) AS internal_error,
    CAST(COALESCE(AVG(pre.duration_ms), 0) AS INTEGER) AS avg_duration_ms,
    CAST(COALESCE(SUM(pre.request_bytes), 0) AS INTEGER) AS request_bytes,
    CAST(COALESCE(SUM(pre.response_bytes), 0) AS INTEGER) AS response_bytes
FROM proxy_request_events pre
LEFT JOIN public_listeners pl ON pl.id = pre.listener_id
WHERE pre.occurred_at >= ?
GROUP BY pre.listener_id, pl.name
ORDER BY requests DESC, id ASC
LIMIT 5;

-- name: ListTopProxyBackendsSince :many
SELECT
    CAST(COALESCE(pre.backend_id, 0) AS INTEGER) AS id,
    COALESCE(pb.name, CASE WHEN pre.backend_id IS NULL THEN 'unknown backend' ELSE 'backend #' || pre.backend_id END) AS label,
    COUNT(*) AS requests,
    CAST(COALESCE(SUM(CASE WHEN pre.status_code >= 200 AND pre.status_code < 400 THEN 1 ELSE 0 END), 0) AS INTEGER) AS success,
    CAST(COALESCE(SUM(CASE WHEN pre.status_code >= 400 AND pre.status_code < 500 THEN 1 ELSE 0 END), 0) AS INTEGER) AS client_error,
    CAST(COALESCE(SUM(CASE WHEN pre.status_code >= 500 THEN 1 ELSE 0 END), 0) AS INTEGER) AS server_error,
    CAST(COALESCE(SUM(CASE WHEN pre.error_kind != '' THEN 1 ELSE 0 END), 0) AS INTEGER) AS internal_error,
    CAST(COALESCE(AVG(pre.duration_ms), 0) AS INTEGER) AS avg_duration_ms,
    CAST(COALESCE(SUM(pre.request_bytes), 0) AS INTEGER) AS request_bytes,
    CAST(COALESCE(SUM(pre.response_bytes), 0) AS INTEGER) AS response_bytes
FROM proxy_request_events pre
LEFT JOIN public_backends pb ON pb.id = pre.backend_id
WHERE pre.occurred_at >= ?
GROUP BY pre.backend_id, pb.name
ORDER BY requests DESC, id ASC
LIMIT 5;

-- name: ListTopProxyRoutesSince :many
SELECT
    CAST(COALESCE(pre.route_id, 0) AS INTEGER) AS id,
    CASE
        WHEN pre.route_id IS NULL THEN 'Default route'
        WHEN pr.id IS NULL THEN 'route #' || pre.route_id
        WHEN pr.host_pattern != '' AND pr.path_prefix != '' THEN pr.host_pattern || ' ' || pr.path_prefix
        WHEN pr.host_pattern != '' THEN pr.host_pattern
        WHEN pr.path_prefix != '' THEN pr.path_prefix
        ELSE 'route #' || pr.id
    END AS label,
    COUNT(*) AS requests,
    CAST(COALESCE(SUM(CASE WHEN pre.status_code >= 200 AND pre.status_code < 400 THEN 1 ELSE 0 END), 0) AS INTEGER) AS success,
    CAST(COALESCE(SUM(CASE WHEN pre.status_code >= 400 AND pre.status_code < 500 THEN 1 ELSE 0 END), 0) AS INTEGER) AS client_error,
    CAST(COALESCE(SUM(CASE WHEN pre.status_code >= 500 THEN 1 ELSE 0 END), 0) AS INTEGER) AS server_error,
    CAST(COALESCE(SUM(CASE WHEN pre.error_kind != '' THEN 1 ELSE 0 END), 0) AS INTEGER) AS internal_error,
    CAST(COALESCE(AVG(pre.duration_ms), 0) AS INTEGER) AS avg_duration_ms,
    CAST(COALESCE(SUM(pre.request_bytes), 0) AS INTEGER) AS request_bytes,
    CAST(COALESCE(SUM(pre.response_bytes), 0) AS INTEGER) AS response_bytes
FROM proxy_request_events pre
LEFT JOIN public_routes pr ON pr.id = pre.route_id
WHERE pre.occurred_at >= ?
GROUP BY pre.route_id, pr.id, pr.host_pattern, pr.path_prefix
ORDER BY requests DESC, id ASC
LIMIT 5;

-- name: ListTopProxyAgentsSince :many
SELECT
    CAST(pre.agent_id AS INTEGER) AS id,
    COALESCE(a.name, 'agent #' || pre.agent_id) AS label,
    COUNT(*) AS requests,
    CAST(COALESCE(SUM(CASE WHEN pre.status_code >= 200 AND pre.status_code < 400 THEN 1 ELSE 0 END), 0) AS INTEGER) AS success,
    CAST(COALESCE(SUM(CASE WHEN pre.status_code >= 400 AND pre.status_code < 500 THEN 1 ELSE 0 END), 0) AS INTEGER) AS client_error,
    CAST(COALESCE(SUM(CASE WHEN pre.status_code >= 500 THEN 1 ELSE 0 END), 0) AS INTEGER) AS server_error,
    CAST(COALESCE(SUM(CASE WHEN pre.error_kind != '' THEN 1 ELSE 0 END), 0) AS INTEGER) AS internal_error,
    CAST(COALESCE(AVG(pre.duration_ms), 0) AS INTEGER) AS avg_duration_ms,
    CAST(COALESCE(SUM(pre.request_bytes), 0) AS INTEGER) AS request_bytes,
    CAST(COALESCE(SUM(pre.response_bytes), 0) AS INTEGER) AS response_bytes
FROM proxy_request_events pre
LEFT JOIN agents a ON a.id = pre.agent_id
WHERE pre.occurred_at >= ?
  AND pre.agent_id IS NOT NULL
GROUP BY pre.agent_id, a.name
ORDER BY requests DESC, id ASC
LIMIT 5;

-- name: ListTopProxyErrorKindsSince :many
SELECT
    CAST(0 AS INTEGER) AS id,
    error_kind AS label,
    COUNT(*) AS requests,
    CAST(COALESCE(SUM(CASE WHEN status_code >= 200 AND status_code < 400 THEN 1 ELSE 0 END), 0) AS INTEGER) AS success,
    CAST(COALESCE(SUM(CASE WHEN status_code >= 400 AND status_code < 500 THEN 1 ELSE 0 END), 0) AS INTEGER) AS client_error,
    CAST(COALESCE(SUM(CASE WHEN status_code >= 500 THEN 1 ELSE 0 END), 0) AS INTEGER) AS server_error,
    CAST(COALESCE(SUM(CASE WHEN error_kind != '' THEN 1 ELSE 0 END), 0) AS INTEGER) AS internal_error,
    CAST(COALESCE(AVG(duration_ms), 0) AS INTEGER) AS avg_duration_ms,
    CAST(COALESCE(SUM(request_bytes), 0) AS INTEGER) AS request_bytes,
    CAST(COALESCE(SUM(response_bytes), 0) AS INTEGER) AS response_bytes
FROM proxy_request_events
WHERE occurred_at >= ?
  AND error_kind != ''
GROUP BY error_kind
ORDER BY requests DESC, label ASC
LIMIT 5;

-- name: ListProxyStatusClassesSince :many
SELECT
    CAST(status_code / 100 AS INTEGER) AS id,
    CAST(CAST(status_code / 100 AS INTEGER) AS TEXT) || 'xx' AS label,
    COUNT(*) AS requests,
    CAST(COALESCE(SUM(CASE WHEN status_code >= 200 AND status_code < 400 THEN 1 ELSE 0 END), 0) AS INTEGER) AS success,
    CAST(COALESCE(SUM(CASE WHEN status_code >= 400 AND status_code < 500 THEN 1 ELSE 0 END), 0) AS INTEGER) AS client_error,
    CAST(COALESCE(SUM(CASE WHEN status_code >= 500 THEN 1 ELSE 0 END), 0) AS INTEGER) AS server_error,
    CAST(COALESCE(SUM(CASE WHEN error_kind != '' THEN 1 ELSE 0 END), 0) AS INTEGER) AS internal_error,
    CAST(COALESCE(AVG(duration_ms), 0) AS INTEGER) AS avg_duration_ms,
    CAST(COALESCE(SUM(request_bytes), 0) AS INTEGER) AS request_bytes,
    CAST(COALESCE(SUM(response_bytes), 0) AS INTEGER) AS response_bytes
FROM proxy_request_events
WHERE occurred_at >= ?
  AND status_code >= 200
  AND status_code < 600
GROUP BY CAST(status_code / 100 AS INTEGER)
ORDER BY id ASC;

-- name: ListProxyTrafficBucketsSince :many
SELECT
    CAST((unixepoch(occurred_at) / CAST(sqlc.arg(bucket_seconds) AS INTEGER)) * CAST(sqlc.arg(bucket_seconds) AS INTEGER) * 1000 AS INTEGER) AS bucket_unix_millis,
    COUNT(*) AS requests,
    CAST(COALESCE(SUM(CASE WHEN status_code >= 200 AND status_code < 400 THEN 1 ELSE 0 END), 0) AS INTEGER) AS success,
    CAST(COALESCE(SUM(CASE WHEN status_code >= 400 AND status_code < 500 THEN 1 ELSE 0 END), 0) AS INTEGER) AS client_error,
    CAST(COALESCE(SUM(CASE WHEN status_code >= 500 THEN 1 ELSE 0 END), 0) AS INTEGER) AS server_error,
    CAST(COALESCE(SUM(CASE WHEN error_kind != '' THEN 1 ELSE 0 END), 0) AS INTEGER) AS internal_error,
    CAST(COALESCE(SUM(request_bytes), 0) AS INTEGER) AS request_bytes,
    CAST(COALESCE(SUM(response_bytes), 0) AS INTEGER) AS response_bytes,
    CAST(COALESCE(AVG(duration_ms), 0) AS INTEGER) AS avg_duration_ms
FROM proxy_request_events
WHERE occurred_at >= sqlc.arg(since)
GROUP BY bucket_unix_millis
ORDER BY bucket_unix_millis ASC;

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
    CAST(COALESCE(MAX(goroutines), 0) AS INTEGER) AS max_goroutines,
    CAST(COALESCE(AVG(cpu_percent), 0) AS REAL) AS avg_cpu_percent,
    CAST(COALESCE(MAX(cpu_percent), 0) AS REAL) AS max_cpu_percent
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

-- name: ListAgents :many
SELECT id, public_id, name, token_hash, enabled, last_connected_at, last_disconnected_at, created_at, updated_at
FROM agents
ORDER BY name ASC, public_id ASC, id ASC;

-- name: GetAgent :one
SELECT id, public_id, name, token_hash, enabled, last_connected_at, last_disconnected_at, created_at, updated_at
FROM agents
WHERE id = ?;

-- name: GetAgentByPublicID :one
SELECT id, public_id, name, token_hash, enabled, last_connected_at, last_disconnected_at, created_at, updated_at
FROM agents
WHERE public_id = ?;

-- name: CreateAgent :one
INSERT INTO agents (public_id, name, token_hash, enabled)
VALUES (?, ?, ?, ?)
RETURNING id, public_id, name, token_hash, enabled, last_connected_at, last_disconnected_at, created_at, updated_at;

-- name: UpdateAgent :one
UPDATE agents
SET name = ?,
    enabled = ?,
    updated_at = CURRENT_TIMESTAMP
WHERE id = ?
RETURNING id, public_id, name, token_hash, enabled, last_connected_at, last_disconnected_at, created_at, updated_at;

-- name: UpdateAgentToken :one
UPDATE agents
SET token_hash = ?,
    updated_at = CURRENT_TIMESTAMP
WHERE id = ?
RETURNING id, public_id, name, token_hash, enabled, last_connected_at, last_disconnected_at, created_at, updated_at;

-- name: UpsertBootstrapAgent :one
INSERT INTO agents (public_id, name, token_hash, enabled)
VALUES (?, ?, ?, 1)
ON CONFLICT(public_id) DO UPDATE SET
    name = excluded.name,
    token_hash = excluded.token_hash,
    enabled = 1,
    updated_at = CURRENT_TIMESTAMP
RETURNING id, public_id, name, token_hash, enabled, last_connected_at, last_disconnected_at, created_at, updated_at;

-- name: MarkAgentConnected :exec
UPDATE agents
SET last_connected_at = CURRENT_TIMESTAMP,
    updated_at = CURRENT_TIMESTAMP
WHERE id = ?;

-- name: MarkAgentDisconnected :exec
UPDATE agents
SET last_disconnected_at = CURRENT_TIMESTAMP,
    updated_at = CURRENT_TIMESTAMP
WHERE id = ?;

-- name: DeleteAgent :exec
DELETE FROM agents
WHERE id = ?;

-- name: CountEnabledAgentPoolBackendsWhereAgentIsLast :one
SELECT COUNT(*)
FROM public_backend_agents pba
JOIN public_backends pb ON pb.id = pba.backend_id
WHERE pba.agent_id = ?
  AND pba.enabled = 1
  AND pb.enabled = 1
  AND pb.backend_type = 'proxy_forward'
  AND pb.forward_mode = 'agent_pool'
  AND NOT EXISTS (
    SELECT 1
    FROM public_backend_agents other
    JOIN agents a ON a.id = other.agent_id
    WHERE other.backend_id = pba.backend_id
      AND other.agent_id != pba.agent_id
      AND other.enabled = 1
      AND a.enabled = 1
  );

-- name: GetLatestAgentStatByAgent :one
SELECT id, agent_id, reported_at, memory_mb, goroutines, req_success, req_client_error, req_server_error, req_internal_error, bytes_rx, bytes_tx, cpu_percent
FROM agent_stats
WHERE agent_id = ?
ORDER BY id DESC
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

-- name: UpdateUserPassword :one
UPDATE users
SET password_hash = ?, updated_at = CURRENT_TIMESTAMP
WHERE id = ? AND disabled_at IS NULL
RETURNING id, username, password_hash, role, created_at, updated_at, disabled_at;

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

-- name: RevokeUserSessions :execrows
UPDATE sessions
SET revoked_at = CURRENT_TIMESTAMP
WHERE user_id = ? AND revoked_at IS NULL;

-- name: CountPublicBackends :one
SELECT COUNT(*) FROM public_backends;

-- name: CountPublicListeners :one
SELECT COUNT(*) FROM public_listeners;

-- name: ListPublicResponseTemplates :many
SELECT id, name, kind, description, content_type, body, created_at, updated_at
FROM public_response_templates
ORDER BY name ASC, id ASC;

-- name: GetPublicResponseTemplate :one
SELECT id, name, kind, description, content_type, body, created_at, updated_at
FROM public_response_templates
WHERE id = ?;

-- name: GetPublicResponseTemplateByName :one
SELECT id, name, kind, description, content_type, body, created_at, updated_at
FROM public_response_templates
WHERE name = ?;

-- name: CreatePublicResponseTemplate :one
INSERT INTO public_response_templates (
    name,
    kind,
    description,
    content_type,
    body
) VALUES (
    ?, ?, ?, ?, ?
)
RETURNING id, name, kind, description, content_type, body, created_at, updated_at;

-- name: UpdatePublicResponseTemplate :one
UPDATE public_response_templates
SET name = ?,
    kind = ?,
    description = ?,
    content_type = ?,
    body = ?,
    updated_at = CURRENT_TIMESTAMP
WHERE id = ?
RETURNING id, name, kind, description, content_type, body, created_at, updated_at;

-- name: DeletePublicResponseTemplate :exec
DELETE FROM public_response_templates
WHERE id = ?;

-- name: CreatePublicBackend :one
INSERT INTO public_backends (
    name,
    target_origin,
    backend_type,
    forward_mode,
    load_balancing,
    tls_skip_verify,
    static_status_code,
    static_response_body,
    static_response_body_mode,
    static_response_template_id,
    upstream_basic_auth_enabled,
    upstream_basic_auth_username,
    upstream_basic_auth_password,
    upstream_response_header_timeout_millis,
    health_check_enabled,
    health_check_method,
    health_check_path,
    health_check_interval_millis,
    health_check_timeout_millis,
    health_check_healthy_threshold,
    health_check_unhealthy_threshold,
    health_check_expected_status_min,
    health_check_expected_status_max,
    enabled
) VALUES (
    ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
)
RETURNING id, name, target_origin, backend_type, forward_mode, load_balancing, tls_skip_verify, static_status_code, static_response_body, static_response_body_mode, static_response_template_id, upstream_basic_auth_enabled, upstream_basic_auth_username, upstream_basic_auth_password, upstream_response_header_timeout_millis, health_check_enabled, health_check_method, health_check_path, health_check_interval_millis, health_check_timeout_millis, health_check_healthy_threshold, health_check_unhealthy_threshold, health_check_expected_status_min, health_check_expected_status_max, enabled, created_at, updated_at;

-- name: ListPublicBackends :many
SELECT id, name, target_origin, backend_type, forward_mode, load_balancing, tls_skip_verify, static_status_code, static_response_body, static_response_body_mode, static_response_template_id, upstream_basic_auth_enabled, upstream_basic_auth_username, upstream_basic_auth_password, upstream_response_header_timeout_millis, health_check_enabled, health_check_method, health_check_path, health_check_interval_millis, health_check_timeout_millis, health_check_healthy_threshold, health_check_unhealthy_threshold, health_check_expected_status_min, health_check_expected_status_max, enabled, created_at, updated_at
FROM public_backends
ORDER BY name ASC, id ASC;

-- name: GetPublicBackend :one
SELECT id, name, target_origin, backend_type, forward_mode, load_balancing, tls_skip_verify, static_status_code, static_response_body, static_response_body_mode, static_response_template_id, upstream_basic_auth_enabled, upstream_basic_auth_username, upstream_basic_auth_password, upstream_response_header_timeout_millis, health_check_enabled, health_check_method, health_check_path, health_check_interval_millis, health_check_timeout_millis, health_check_healthy_threshold, health_check_unhealthy_threshold, health_check_expected_status_min, health_check_expected_status_max, enabled, created_at, updated_at
FROM public_backends
WHERE id = ?;

-- name: UpdatePublicBackend :one
UPDATE public_backends
SET name = ?,
    target_origin = ?,
    backend_type = ?,
    forward_mode = ?,
    load_balancing = ?,
    tls_skip_verify = ?,
    static_status_code = ?,
    static_response_body = ?,
    static_response_body_mode = ?,
    static_response_template_id = ?,
    upstream_basic_auth_enabled = ?,
    upstream_basic_auth_username = ?,
    upstream_basic_auth_password = ?,
    upstream_response_header_timeout_millis = ?,
    health_check_enabled = ?,
    health_check_method = ?,
    health_check_path = ?,
    health_check_interval_millis = ?,
    health_check_timeout_millis = ?,
    health_check_healthy_threshold = ?,
    health_check_unhealthy_threshold = ?,
    health_check_expected_status_min = ?,
    health_check_expected_status_max = ?,
    enabled = ?,
    updated_at = CURRENT_TIMESTAMP
WHERE id = ?
RETURNING id, name, target_origin, backend_type, forward_mode, load_balancing, tls_skip_verify, static_status_code, static_response_body, static_response_body_mode, static_response_template_id, upstream_basic_auth_enabled, upstream_basic_auth_username, upstream_basic_auth_password, upstream_response_header_timeout_millis, health_check_enabled, health_check_method, health_check_path, health_check_interval_millis, health_check_timeout_millis, health_check_healthy_threshold, health_check_unhealthy_threshold, health_check_expected_status_min, health_check_expected_status_max, enabled, created_at, updated_at;

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

-- name: ListPublicBackendUpstreamHeaders :many
SELECT id, backend_id, position, name, value, sensitive, created_at, updated_at
FROM public_backend_upstream_headers
ORDER BY backend_id ASC, position ASC, id ASC;

-- name: ListPublicBackendUpstreamHeadersByBackend :many
SELECT id, backend_id, position, name, value, sensitive, created_at, updated_at
FROM public_backend_upstream_headers
WHERE backend_id = ?
ORDER BY position ASC, id ASC;

-- name: CreatePublicBackendUpstreamHeader :one
INSERT INTO public_backend_upstream_headers (backend_id, position, name, value, sensitive)
VALUES (?, ?, ?, ?, ?)
RETURNING id, backend_id, position, name, value, sensitive, created_at, updated_at;

-- name: DeletePublicBackendUpstreamHeaders :exec
DELETE FROM public_backend_upstream_headers
WHERE backend_id = ?;

-- name: ListPublicBackendAgents :many
SELECT backend_id, agent_id, position, weight, enabled, created_at, updated_at
FROM public_backend_agents
ORDER BY backend_id ASC, position ASC, agent_id ASC;

-- name: ListPublicBackendAgentsByBackend :many
SELECT backend_id, agent_id, position, weight, enabled, created_at, updated_at
FROM public_backend_agents
WHERE backend_id = ?
ORDER BY position ASC, agent_id ASC;

-- name: CreatePublicBackendAgent :one
INSERT INTO public_backend_agents (backend_id, agent_id, position, weight, enabled)
VALUES (?, ?, ?, ?, ?)
RETURNING backend_id, agent_id, position, weight, enabled, created_at, updated_at;

-- name: DeletePublicBackendAgents :exec
DELETE FROM public_backend_agents
WHERE backend_id = ?;

-- name: ListPublicRouteBackends :many
SELECT route_id, backend_id, position, weight, enabled, created_at, updated_at
FROM public_route_backends
ORDER BY route_id ASC, position ASC, backend_id ASC;

-- name: ListPublicRouteBackendsByRoute :many
SELECT route_id, backend_id, position, weight, enabled, created_at, updated_at
FROM public_route_backends
WHERE route_id = ?
ORDER BY position ASC, backend_id ASC;

-- name: CreatePublicRouteBackend :one
INSERT INTO public_route_backends (route_id, backend_id, position, weight, enabled)
VALUES (?, ?, ?, ?, ?)
RETURNING route_id, backend_id, position, weight, enabled, created_at, updated_at;

-- name: DeletePublicRouteBackends :exec
DELETE FROM public_route_backends
WHERE route_id = ?;

-- name: CountPublicBackendEnabledReferences :one
SELECT
  (
    SELECT COUNT(*)
    FROM public_listeners
    WHERE default_backend_id = sqlc.arg(backend_id) AND enabled = 1
  ) + (
    SELECT COUNT(*)
    FROM public_routes
    WHERE (backend_id = sqlc.arg(backend_id) OR fallback_backend_id = sqlc.arg(backend_id)) AND enabled = 1
  ) + (
    SELECT COUNT(*)
    FROM public_route_backends prb
    JOIN public_routes pr ON pr.id = prb.route_id
    WHERE prb.backend_id = sqlc.arg(backend_id)
      AND prb.enabled = 1
      AND pr.enabled = 1
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
INSERT INTO public_routes (
    listener_id,
    priority,
    host_pattern,
    path_prefix,
    backend_id,
    load_balancing,
    fallback_backend_id,
    action,
    redirect_target_mode,
    redirect_target,
    redirect_status_code,
    redirect_preserve_path_suffix,
    redirect_preserve_query,
    enabled
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING id, listener_id, priority, host_pattern, path_prefix, backend_id, load_balancing, fallback_backend_id, action, redirect_target_mode, redirect_target, redirect_status_code, redirect_preserve_path_suffix, redirect_preserve_query, enabled, created_at, updated_at;

-- name: ListPublicRoutes :many
SELECT id, listener_id, priority, host_pattern, path_prefix, backend_id, load_balancing, fallback_backend_id, action, redirect_target_mode, redirect_target, redirect_status_code, redirect_preserve_path_suffix, redirect_preserve_query, enabled, created_at, updated_at
FROM public_routes
ORDER BY listener_id ASC, priority ASC, id ASC;

-- name: GetPublicRoute :one
SELECT id, listener_id, priority, host_pattern, path_prefix, backend_id, load_balancing, fallback_backend_id, action, redirect_target_mode, redirect_target, redirect_status_code, redirect_preserve_path_suffix, redirect_preserve_query, enabled, created_at, updated_at
FROM public_routes
WHERE id = ?;

-- name: UpdatePublicRoute :one
UPDATE public_routes
SET listener_id = ?,
    priority = ?,
    host_pattern = ?,
    path_prefix = ?,
    backend_id = ?,
    load_balancing = ?,
    fallback_backend_id = ?,
    action = ?,
    redirect_target_mode = ?,
    redirect_target = ?,
    redirect_status_code = ?,
    redirect_preserve_path_suffix = ?,
    redirect_preserve_query = ?,
    enabled = ?,
    updated_at = CURRENT_TIMESTAMP
WHERE id = ?
RETURNING id, listener_id, priority, host_pattern, path_prefix, backend_id, load_balancing, fallback_backend_id, action, redirect_target_mode, redirect_target, redirect_status_code, redirect_preserve_path_suffix, redirect_preserve_query, enabled, created_at, updated_at;

-- name: DeletePublicRoute :exec
DELETE FROM public_routes
WHERE id = ?;

-- name: CreatePublicTlsDnsCredential :one
INSERT INTO public_tls_dns_credentials (name, provider, cloudflare_zone_id, api_token, enabled)
VALUES (?, ?, ?, ?, ?)
RETURNING id, name, provider, cloudflare_zone_id, api_token, enabled, created_at, updated_at;

-- name: ListPublicTlsDnsCredentials :many
SELECT id, name, provider, cloudflare_zone_id, api_token, enabled, created_at, updated_at
FROM public_tls_dns_credentials
ORDER BY name ASC, id ASC;

-- name: GetPublicTlsDnsCredential :one
SELECT id, name, provider, cloudflare_zone_id, api_token, enabled, created_at, updated_at
FROM public_tls_dns_credentials
WHERE id = ?;

-- name: UpdatePublicTlsDnsCredential :one
UPDATE public_tls_dns_credentials
SET name = ?, provider = ?, cloudflare_zone_id = ?, api_token = ?, enabled = ?, updated_at = CURRENT_TIMESTAMP
WHERE id = ?
RETURNING id, name, provider, cloudflare_zone_id, api_token, enabled, created_at, updated_at;

-- name: DeletePublicTlsDnsCredential :exec
DELETE FROM public_tls_dns_credentials
WHERE id = ?;

-- name: CreatePublicTlsCertificate :one
INSERT INTO public_tls_certificates (
    listener_id,
    hostname_pattern,
    cert_path,
    key_path,
    enabled,
    source,
    acme_challenge_type,
    acme_ca,
    acme_email,
    dns_credential_id,
    status,
    last_error,
    issued_at,
    expires_at,
    next_renewal_at,
    last_renewal_attempt_at
) VALUES (
    ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
)
RETURNING id, listener_id, hostname_pattern, cert_path, key_path, enabled, source, acme_challenge_type, acme_ca, acme_email, dns_credential_id, status, last_error, issued_at, expires_at, next_renewal_at, last_renewal_attempt_at, created_at, updated_at;

-- name: ListPublicTlsCertificates :many
SELECT id, listener_id, hostname_pattern, cert_path, key_path, enabled, source, acme_challenge_type, acme_ca, acme_email, dns_credential_id, status, last_error, issued_at, expires_at, next_renewal_at, last_renewal_attempt_at, created_at, updated_at
FROM public_tls_certificates
ORDER BY listener_id ASC, hostname_pattern ASC, id ASC;

-- name: GetPublicTlsCertificate :one
SELECT id, listener_id, hostname_pattern, cert_path, key_path, enabled, source, acme_challenge_type, acme_ca, acme_email, dns_credential_id, status, last_error, issued_at, expires_at, next_renewal_at, last_renewal_attempt_at, created_at, updated_at
FROM public_tls_certificates
WHERE id = ?;

-- name: UpdatePublicTlsCertificate :one
UPDATE public_tls_certificates
SET listener_id = ?,
    hostname_pattern = ?,
    cert_path = ?,
    key_path = ?,
    enabled = ?,
    source = ?,
    acme_challenge_type = ?,
    acme_ca = ?,
    acme_email = ?,
    dns_credential_id = ?,
    status = ?,
    last_error = ?,
    issued_at = ?,
    expires_at = ?,
    next_renewal_at = ?,
    last_renewal_attempt_at = ?,
    updated_at = CURRENT_TIMESTAMP
WHERE id = ?
RETURNING id, listener_id, hostname_pattern, cert_path, key_path, enabled, source, acme_challenge_type, acme_ca, acme_email, dns_credential_id, status, last_error, issued_at, expires_at, next_renewal_at, last_renewal_attempt_at, created_at, updated_at;

-- name: UpdatePublicTlsCertificateIssueState :one
UPDATE public_tls_certificates
SET cert_path = ?,
    key_path = ?,
    status = ?,
    last_error = ?,
    issued_at = ?,
    expires_at = ?,
    next_renewal_at = ?,
    last_renewal_attempt_at = ?,
    updated_at = CURRENT_TIMESTAMP
WHERE id = ?
RETURNING id, listener_id, hostname_pattern, cert_path, key_path, enabled, source, acme_challenge_type, acme_ca, acme_email, dns_credential_id, status, last_error, issued_at, expires_at, next_renewal_at, last_renewal_attempt_at, created_at, updated_at;

-- name: UpdatePublicTlsCertificateStatus :one
UPDATE public_tls_certificates
SET status = ?,
    last_error = ?,
    last_renewal_attempt_at = ?,
    updated_at = CURRENT_TIMESTAMP
WHERE id = ?
RETURNING id, listener_id, hostname_pattern, cert_path, key_path, enabled, source, acme_challenge_type, acme_ca, acme_email, dns_credential_id, status, last_error, issued_at, expires_at, next_renewal_at, last_renewal_attempt_at, created_at, updated_at;

-- name: DeletePublicTlsCertificate :exec
DELETE FROM public_tls_certificates
WHERE id = ?;

-- name: ListPublicRateLimitRules :many
SELECT id, name, priority, enabled, algorithm, limit_count, window_millis, burst, match_json, key_parts_json, response_status_code, response_body, response_body_mode, response_body_template_id, response_content_type, response_headers_json, created_at, updated_at
FROM public_rate_limit_rules
ORDER BY priority ASC, id ASC;

-- name: GetPublicRateLimitRule :one
SELECT id, name, priority, enabled, algorithm, limit_count, window_millis, burst, match_json, key_parts_json, response_status_code, response_body, response_body_mode, response_body_template_id, response_content_type, response_headers_json, created_at, updated_at
FROM public_rate_limit_rules
WHERE id = ?;

-- name: CreatePublicRateLimitRule :one
INSERT INTO public_rate_limit_rules (
    name,
    priority,
    enabled,
    algorithm,
    limit_count,
    window_millis,
    burst,
    match_json,
    key_parts_json,
    response_status_code,
    response_body,
    response_body_mode,
    response_body_template_id,
    response_content_type,
    response_headers_json
) VALUES (
    ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
)
RETURNING id, name, priority, enabled, algorithm, limit_count, window_millis, burst, match_json, key_parts_json, response_status_code, response_body, response_body_mode, response_body_template_id, response_content_type, response_headers_json, created_at, updated_at;

-- name: UpdatePublicRateLimitRule :one
UPDATE public_rate_limit_rules
SET name = ?,
    priority = ?,
    enabled = ?,
    algorithm = ?,
    limit_count = ?,
    window_millis = ?,
    burst = ?,
    match_json = ?,
    key_parts_json = ?,
    response_status_code = ?,
    response_body = ?,
    response_body_mode = ?,
    response_body_template_id = ?,
    response_content_type = ?,
    response_headers_json = ?,
    updated_at = CURRENT_TIMESTAMP
WHERE id = ?
RETURNING id, name, priority, enabled, algorithm, limit_count, window_millis, burst, match_json, key_parts_json, response_status_code, response_body, response_body_mode, response_body_template_id, response_content_type, response_headers_json, created_at, updated_at;

-- name: DeletePublicRateLimitRule :exec
DELETE FROM public_rate_limit_rules
WHERE id = ?;

-- name: ListPublicTrafficShaperRules :many
SELECT id, name, priority, enabled, budget_scope, upload_bytes_per_second, download_bytes_per_second, burst_bytes, request_exempt_bytes, response_exempt_bytes, match_json, key_parts_json, created_at, updated_at
FROM public_traffic_shaper_rules
ORDER BY priority ASC, id ASC;

-- name: GetPublicTrafficShaperRule :one
SELECT id, name, priority, enabled, budget_scope, upload_bytes_per_second, download_bytes_per_second, burst_bytes, request_exempt_bytes, response_exempt_bytes, match_json, key_parts_json, created_at, updated_at
FROM public_traffic_shaper_rules
WHERE id = ?;

-- name: CreatePublicTrafficShaperRule :one
INSERT INTO public_traffic_shaper_rules (
    name,
    priority,
    enabled,
    budget_scope,
    upload_bytes_per_second,
    download_bytes_per_second,
    burst_bytes,
    request_exempt_bytes,
    response_exempt_bytes,
    match_json,
    key_parts_json
) VALUES (
    ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
)
RETURNING id, name, priority, enabled, budget_scope, upload_bytes_per_second, download_bytes_per_second, burst_bytes, request_exempt_bytes, response_exempt_bytes, match_json, key_parts_json, created_at, updated_at;

-- name: UpdatePublicTrafficShaperRule :one
UPDATE public_traffic_shaper_rules
SET name = ?,
    priority = ?,
    enabled = ?,
    budget_scope = ?,
    upload_bytes_per_second = ?,
    download_bytes_per_second = ?,
    burst_bytes = ?,
    request_exempt_bytes = ?,
    response_exempt_bytes = ?,
    match_json = ?,
    key_parts_json = ?,
    updated_at = CURRENT_TIMESTAMP
WHERE id = ?
RETURNING id, name, priority, enabled, budget_scope, upload_bytes_per_second, download_bytes_per_second, burst_bytes, request_exempt_bytes, response_exempt_bytes, match_json, key_parts_json, created_at, updated_at;

-- name: DeletePublicTrafficShaperRule :exec
DELETE FROM public_traffic_shaper_rules
WHERE id = ?;

-- name: ListPublicWafCaptchaProviders :many
SELECT id, name, provider_type, site_key, secret_key, enabled, created_at, updated_at
FROM public_waf_captcha_providers
ORDER BY name ASC, id ASC;

-- name: GetPublicWafCaptchaProvider :one
SELECT id, name, provider_type, site_key, secret_key, enabled, created_at, updated_at
FROM public_waf_captcha_providers
WHERE id = ?;

-- name: CreatePublicWafCaptchaProvider :one
INSERT INTO public_waf_captcha_providers (
    name,
    provider_type,
    site_key,
    secret_key,
    enabled
) VALUES (
    ?, ?, ?, ?, ?
)
RETURNING id, name, provider_type, site_key, secret_key, enabled, created_at, updated_at;

-- name: UpdatePublicWafCaptchaProvider :one
UPDATE public_waf_captcha_providers
SET name = ?,
    provider_type = ?,
    site_key = ?,
    secret_key = ?,
    enabled = ?,
    updated_at = CURRENT_TIMESTAMP
WHERE id = ?
RETURNING id, name, provider_type, site_key, secret_key, enabled, created_at, updated_at;

-- name: DeletePublicWafCaptchaProvider :exec
DELETE FROM public_waf_captcha_providers
WHERE id = ?;

-- name: ListPublicWafRules :many
SELECT id, name, priority, enabled, action, activation_mode, match_json, key_parts_json, captcha_provider_id, captcha_pass_ttl_millis,
       waiting_room_max_admitted_sessions, waiting_room_admission_rate_per_second, waiting_room_admission_session_ttl_millis,
       waiting_room_queue_poll_interval_millis, waiting_room_queue_timeout_millis, waiting_room_page_title, waiting_room_page_body,
       trigger_request_window_millis, trigger_minimum_request_rate, trigger_traffic_spike_multiplier, trigger_proxy_active_requests,
       trigger_backend_active_requests, trigger_agent_active_requests, trigger_server_cpu_percent, trigger_agent_cpu_percent,
       trigger_minimum_active_millis, trigger_quiet_period_millis, block_response_status_code, block_response_body,
       block_response_body_mode, block_response_template_id, captcha_page_template_id, waiting_room_page_template_id,
       block_response_content_type, block_response_headers_json, created_at, updated_at
FROM public_waf_rules
ORDER BY priority ASC, id ASC;

-- name: GetPublicWafRule :one
SELECT id, name, priority, enabled, action, activation_mode, match_json, key_parts_json, captcha_provider_id, captcha_pass_ttl_millis,
       waiting_room_max_admitted_sessions, waiting_room_admission_rate_per_second, waiting_room_admission_session_ttl_millis,
       waiting_room_queue_poll_interval_millis, waiting_room_queue_timeout_millis, waiting_room_page_title, waiting_room_page_body,
       trigger_request_window_millis, trigger_minimum_request_rate, trigger_traffic_spike_multiplier, trigger_proxy_active_requests,
       trigger_backend_active_requests, trigger_agent_active_requests, trigger_server_cpu_percent, trigger_agent_cpu_percent,
       trigger_minimum_active_millis, trigger_quiet_period_millis, block_response_status_code, block_response_body,
       block_response_body_mode, block_response_template_id, captcha_page_template_id, waiting_room_page_template_id,
       block_response_content_type, block_response_headers_json, created_at, updated_at
FROM public_waf_rules
WHERE id = ?;

-- name: CreatePublicWafRule :one
INSERT INTO public_waf_rules (
    name,
    priority,
    enabled,
    action,
    activation_mode,
    match_json,
    key_parts_json,
    captcha_provider_id,
    captcha_pass_ttl_millis,
    waiting_room_max_admitted_sessions,
    waiting_room_admission_rate_per_second,
    waiting_room_admission_session_ttl_millis,
    waiting_room_queue_poll_interval_millis,
    waiting_room_queue_timeout_millis,
    waiting_room_page_title,
    waiting_room_page_body,
    trigger_request_window_millis,
    trigger_minimum_request_rate,
    trigger_traffic_spike_multiplier,
    trigger_proxy_active_requests,
    trigger_backend_active_requests,
    trigger_agent_active_requests,
    trigger_server_cpu_percent,
    trigger_agent_cpu_percent,
    trigger_minimum_active_millis,
    trigger_quiet_period_millis,
    block_response_status_code,
    block_response_body,
    block_response_body_mode,
    block_response_template_id,
    captcha_page_template_id,
    waiting_room_page_template_id,
    block_response_content_type,
    block_response_headers_json
) VALUES (
    ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
)
RETURNING id, name, priority, enabled, action, activation_mode, match_json, key_parts_json, captcha_provider_id, captcha_pass_ttl_millis,
          waiting_room_max_admitted_sessions, waiting_room_admission_rate_per_second, waiting_room_admission_session_ttl_millis,
          waiting_room_queue_poll_interval_millis, waiting_room_queue_timeout_millis, waiting_room_page_title, waiting_room_page_body,
          trigger_request_window_millis, trigger_minimum_request_rate, trigger_traffic_spike_multiplier, trigger_proxy_active_requests,
          trigger_backend_active_requests, trigger_agent_active_requests, trigger_server_cpu_percent, trigger_agent_cpu_percent,
          trigger_minimum_active_millis, trigger_quiet_period_millis, block_response_status_code, block_response_body,
          block_response_body_mode, block_response_template_id, captcha_page_template_id, waiting_room_page_template_id,
          block_response_content_type, block_response_headers_json, created_at, updated_at;

-- name: UpdatePublicWafRule :one
UPDATE public_waf_rules
SET name = ?,
    priority = ?,
    enabled = ?,
    action = ?,
    activation_mode = ?,
    match_json = ?,
    key_parts_json = ?,
    captcha_provider_id = ?,
    captcha_pass_ttl_millis = ?,
    waiting_room_max_admitted_sessions = ?,
    waiting_room_admission_rate_per_second = ?,
    waiting_room_admission_session_ttl_millis = ?,
    waiting_room_queue_poll_interval_millis = ?,
    waiting_room_queue_timeout_millis = ?,
    waiting_room_page_title = ?,
    waiting_room_page_body = ?,
    trigger_request_window_millis = ?,
    trigger_minimum_request_rate = ?,
    trigger_traffic_spike_multiplier = ?,
    trigger_proxy_active_requests = ?,
    trigger_backend_active_requests = ?,
    trigger_agent_active_requests = ?,
    trigger_server_cpu_percent = ?,
    trigger_agent_cpu_percent = ?,
    trigger_minimum_active_millis = ?,
    trigger_quiet_period_millis = ?,
    block_response_status_code = ?,
    block_response_body = ?,
    block_response_body_mode = ?,
    block_response_template_id = ?,
    captcha_page_template_id = ?,
    waiting_room_page_template_id = ?,
    block_response_content_type = ?,
    block_response_headers_json = ?,
    updated_at = CURRENT_TIMESTAMP
WHERE id = ?
RETURNING id, name, priority, enabled, action, activation_mode, match_json, key_parts_json, captcha_provider_id, captcha_pass_ttl_millis,
          waiting_room_max_admitted_sessions, waiting_room_admission_rate_per_second, waiting_room_admission_session_ttl_millis,
          waiting_room_queue_poll_interval_millis, waiting_room_queue_timeout_millis, waiting_room_page_title, waiting_room_page_body,
          trigger_request_window_millis, trigger_minimum_request_rate, trigger_traffic_spike_multiplier, trigger_proxy_active_requests,
          trigger_backend_active_requests, trigger_agent_active_requests, trigger_server_cpu_percent, trigger_agent_cpu_percent,
          trigger_minimum_active_millis, trigger_quiet_period_millis, block_response_status_code, block_response_body,
          block_response_body_mode, block_response_template_id, captcha_page_template_id, waiting_room_page_template_id,
          block_response_content_type, block_response_headers_json, created_at, updated_at;

-- name: DeletePublicWafRule :exec
DELETE FROM public_waf_rules
WHERE id = ?;

-- name: GetPublicWafSettings :one
SELECT id, cookie_signing_secret, created_at, updated_at
FROM public_waf_settings
WHERE id = 1;

-- name: UpsertPublicWafSettings :one
INSERT INTO public_waf_settings (id, cookie_signing_secret)
VALUES (1, ?)
ON CONFLICT(id) DO UPDATE SET
    cookie_signing_secret = public_waf_settings.cookie_signing_secret,
    updated_at = public_waf_settings.updated_at
RETURNING id, cookie_signing_secret, created_at, updated_at;

-- name: GetPublicCacheSettings :one
SELECT id, enabled, max_disk_bytes, max_memory_bytes, memory_hot_object_max_bytes, max_entries, cleanup_interval_millis, created_at, updated_at
FROM public_cache_settings
WHERE id = 1;

-- name: UpsertPublicCacheSettingsDefaults :one
INSERT INTO public_cache_settings (id)
VALUES (1)
ON CONFLICT(id) DO UPDATE SET updated_at = public_cache_settings.updated_at
RETURNING id, enabled, max_disk_bytes, max_memory_bytes, memory_hot_object_max_bytes, max_entries, cleanup_interval_millis, created_at, updated_at;

-- name: UpdatePublicCacheSettings :one
INSERT INTO public_cache_settings (
    id, enabled, max_disk_bytes, max_memory_bytes, memory_hot_object_max_bytes, max_entries, cleanup_interval_millis
) VALUES (
    1, ?, ?, ?, ?, ?, ?
)
ON CONFLICT(id) DO UPDATE SET
    enabled = excluded.enabled,
    max_disk_bytes = excluded.max_disk_bytes,
    max_memory_bytes = excluded.max_memory_bytes,
    memory_hot_object_max_bytes = excluded.memory_hot_object_max_bytes,
    max_entries = excluded.max_entries,
    cleanup_interval_millis = excluded.cleanup_interval_millis,
    updated_at = CURRENT_TIMESTAMP
RETURNING id, enabled, max_disk_bytes, max_memory_bytes, memory_hot_object_max_bytes, max_entries, cleanup_interval_millis, created_at, updated_at;

-- name: ListPublicCacheRules :many
SELECT id, name, priority, enabled, match_json, route_ids_json, backend_ids_json, scope, ttl_mode, ttl_millis,
       query_mode, query_params_json, vary_headers_json, cache_status_codes_json, max_object_bytes,
       add_cache_status_header, allow_cookie_requests, created_at, updated_at
FROM public_cache_rules
ORDER BY priority ASC, id ASC;

-- name: GetPublicCacheRule :one
SELECT id, name, priority, enabled, match_json, route_ids_json, backend_ids_json, scope, ttl_mode, ttl_millis,
       query_mode, query_params_json, vary_headers_json, cache_status_codes_json, max_object_bytes,
       add_cache_status_header, allow_cookie_requests, created_at, updated_at
FROM public_cache_rules
WHERE id = ?;

-- name: CreatePublicCacheRule :one
INSERT INTO public_cache_rules (
    name,
    priority,
    enabled,
    match_json,
    route_ids_json,
    backend_ids_json,
    scope,
    ttl_mode,
    ttl_millis,
    query_mode,
    query_params_json,
    vary_headers_json,
    cache_status_codes_json,
    max_object_bytes,
    add_cache_status_header,
    allow_cookie_requests
) VALUES (
    ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
)
RETURNING id, name, priority, enabled, match_json, route_ids_json, backend_ids_json, scope, ttl_mode, ttl_millis,
          query_mode, query_params_json, vary_headers_json, cache_status_codes_json, max_object_bytes,
          add_cache_status_header, allow_cookie_requests, created_at, updated_at;

-- name: UpdatePublicCacheRule :one
UPDATE public_cache_rules
SET name = ?,
    priority = ?,
    enabled = ?,
    match_json = ?,
    route_ids_json = ?,
    backend_ids_json = ?,
    scope = ?,
    ttl_mode = ?,
    ttl_millis = ?,
    query_mode = ?,
    query_params_json = ?,
    vary_headers_json = ?,
    cache_status_codes_json = ?,
    max_object_bytes = ?,
    add_cache_status_header = ?,
    allow_cookie_requests = ?,
    updated_at = CURRENT_TIMESTAMP
WHERE id = ?
RETURNING id, name, priority, enabled, match_json, route_ids_json, backend_ids_json, scope, ttl_mode, ttl_millis,
          query_mode, query_params_json, vary_headers_json, cache_status_codes_json, max_object_bytes,
          add_cache_status_header, allow_cookie_requests, created_at, updated_at;

-- name: DeletePublicCacheRule :exec
DELETE FROM public_cache_rules
WHERE id = ?;

-- name: GetPublicCacheEntry :one
SELECT key_digest, rule_id, scope, listener_protocol, host, path, query_key, route_id, backend_id, method,
       vary_headers_json, response_headers_json, status_code, body_path, size_bytes, stored_at, expires_at,
       last_accessed_at, hit_count
FROM public_cache_entries
WHERE key_digest = ?;

-- name: ListPublicCacheEntryCandidates :many
SELECT key_digest, rule_id, scope, listener_protocol, host, path, query_key, route_id, backend_id, method,
       vary_headers_json, response_headers_json, status_code, body_path, size_bytes, stored_at, expires_at,
       last_accessed_at, hit_count
FROM public_cache_entries
WHERE rule_id = ?
  AND listener_protocol = ?
  AND host = ?
  AND path = ?
  AND query_key = ?
  AND COALESCE(route_id, 0) = ?
  AND COALESCE(backend_id, 0) = ?
  AND expires_at > ?
ORDER BY stored_at DESC
LIMIT 20;

-- name: UpsertPublicCacheEntry :one
INSERT INTO public_cache_entries (
    key_digest, rule_id, scope, listener_protocol, host, path, query_key, route_id, backend_id, method,
    vary_headers_json, response_headers_json, status_code, body_path, size_bytes, stored_at, expires_at,
    last_accessed_at, hit_count
) VALUES (
    ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP, ?, CURRENT_TIMESTAMP, 0
)
ON CONFLICT(key_digest) DO UPDATE SET
    rule_id = excluded.rule_id,
    scope = excluded.scope,
    listener_protocol = excluded.listener_protocol,
    host = excluded.host,
    path = excluded.path,
    query_key = excluded.query_key,
    route_id = excluded.route_id,
    backend_id = excluded.backend_id,
    method = excluded.method,
    vary_headers_json = excluded.vary_headers_json,
    response_headers_json = excluded.response_headers_json,
    status_code = excluded.status_code,
    body_path = excluded.body_path,
    size_bytes = excluded.size_bytes,
    stored_at = CURRENT_TIMESTAMP,
    expires_at = excluded.expires_at,
    last_accessed_at = CURRENT_TIMESTAMP,
    hit_count = 0
RETURNING key_digest, rule_id, scope, listener_protocol, host, path, query_key, route_id, backend_id, method,
          vary_headers_json, response_headers_json, status_code, body_path, size_bytes, stored_at, expires_at,
          last_accessed_at, hit_count;

-- name: TouchPublicCacheEntry :exec
UPDATE public_cache_entries
SET last_accessed_at = CURRENT_TIMESTAMP,
    hit_count = hit_count + 1
WHERE key_digest = ?;

-- name: DeletePublicCacheEntry :exec
DELETE FROM public_cache_entries
WHERE key_digest = ?;

-- name: DeleteExpiredPublicCacheEntries :many
DELETE FROM public_cache_entries
WHERE expires_at <= ?
RETURNING key_digest, body_path, size_bytes;

-- name: ListPublicCacheEntriesForCleanup :many
SELECT key_digest, body_path, size_bytes
FROM public_cache_entries
ORDER BY last_accessed_at ASC
LIMIT ?;

-- name: SumPublicCacheBytes :one
SELECT CAST(COALESCE(SUM(size_bytes), 0) AS INTEGER) AS total_bytes,
       COUNT(*) AS entry_count
FROM public_cache_entries;

-- name: PurgeAllPublicCacheEntries :many
DELETE FROM public_cache_entries
RETURNING key_digest, body_path, size_bytes;

-- name: PurgePublicCacheEntriesByRule :many
DELETE FROM public_cache_entries
WHERE rule_id = ?
RETURNING key_digest, body_path, size_bytes;

-- name: PurgePublicCacheEntriesByHostPath :many
DELETE FROM public_cache_entries
WHERE (? = '' OR host = ?)
  AND (? = '' OR path LIKE ? || '%')
RETURNING key_digest, body_path, size_bytes;
