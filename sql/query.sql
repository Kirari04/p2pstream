-- name: InsertConnection :one
INSERT INTO connections (agent_id, connected_at)
VALUES (?, CURRENT_TIMESTAMP)
RETURNING id;

-- name: UpdateConnectionDisconnected :exec
UPDATE connections
SET disconnected_at = CURRENT_TIMESTAMP
WHERE id = ?;

-- name: MarkAgentsWithOpenConnectionsDisconnectedAt :exec
UPDATE agents
SET last_disconnected_at = ?,
    updated_at = ?
WHERE id IN (
    SELECT DISTINCT agent_id
    FROM connections
    WHERE disconnected_at IS NULL
      AND agent_id IS NOT NULL
);

-- name: CloseOpenConnectionsAt :exec
UPDATE connections
SET disconnected_at = ?
WHERE disconnected_at IS NULL;

-- name: InsertAgentStat :exec
INSERT INTO agent_stats (
    agent_id, memory_mb, goroutines, req_success, req_client_error, req_server_error, req_internal_error, bytes_rx, bytes_tx, cpu_percent
) VALUES (
    ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
);

-- name: InsertAgentStatAt :one
INSERT INTO agent_stats (
    reported_at, agent_id, memory_mb, goroutines, req_success, req_client_error, req_server_error, req_internal_error, bytes_rx, bytes_tx, cpu_percent
) VALUES (
    ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
)
RETURNING id;

-- name: GetLatestAgentStat :one
SELECT * FROM agent_stats
ORDER BY id DESC
LIMIT 1;

-- name: InsertProxyRequestEvent :exec
INSERT INTO proxy_request_events (
    status_code, duration_ms, error_kind, method, host, path_prefix, listener_id, route_id, route_target_id, waf_rule_id, waf_action, agent_id, request_bytes, response_bytes, cache_rule_id, cache_status, cache_bytes
) VALUES (
    ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
);

-- name: InsertProxyRequestEventAt :one
INSERT INTO proxy_request_events (
    occurred_at, status_code, duration_ms, error_kind, method, host, path_prefix, listener_id, route_id, route_target_id, waf_rule_id, waf_action, agent_id, request_bytes, response_bytes, cache_rule_id, cache_status, cache_bytes
) VALUES (
    ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
)
RETURNING id;

-- name: UpsertProxyRequestRollupMinute :exec
INSERT INTO proxy_request_rollup_minutes (
    bucket_unix_millis, requests, success, client_error, server_error, internal_error,
    duration_ms_sum, max_duration_ms, slow_requests, request_bytes, response_bytes,
    cache_hits, cache_misses, cache_bypasses, cache_stored, cache_store_failed,
    cache_hit_bytes, cache_stored_bytes
) VALUES (
    ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
)
ON CONFLICT(bucket_unix_millis) DO UPDATE SET
    requests = proxy_request_rollup_minutes.requests + excluded.requests,
    success = proxy_request_rollup_minutes.success + excluded.success,
    client_error = proxy_request_rollup_minutes.client_error + excluded.client_error,
    server_error = proxy_request_rollup_minutes.server_error + excluded.server_error,
    internal_error = proxy_request_rollup_minutes.internal_error + excluded.internal_error,
    duration_ms_sum = proxy_request_rollup_minutes.duration_ms_sum + excluded.duration_ms_sum,
    max_duration_ms = MAX(proxy_request_rollup_minutes.max_duration_ms, excluded.max_duration_ms),
    slow_requests = proxy_request_rollup_minutes.slow_requests + excluded.slow_requests,
    request_bytes = proxy_request_rollup_minutes.request_bytes + excluded.request_bytes,
    response_bytes = proxy_request_rollup_minutes.response_bytes + excluded.response_bytes,
    cache_hits = proxy_request_rollup_minutes.cache_hits + excluded.cache_hits,
    cache_misses = proxy_request_rollup_minutes.cache_misses + excluded.cache_misses,
    cache_bypasses = proxy_request_rollup_minutes.cache_bypasses + excluded.cache_bypasses,
    cache_stored = proxy_request_rollup_minutes.cache_stored + excluded.cache_stored,
    cache_store_failed = proxy_request_rollup_minutes.cache_store_failed + excluded.cache_store_failed,
    cache_hit_bytes = proxy_request_rollup_minutes.cache_hit_bytes + excluded.cache_hit_bytes,
    cache_stored_bytes = proxy_request_rollup_minutes.cache_stored_bytes + excluded.cache_stored_bytes,
    updated_at = CURRENT_TIMESTAMP;

-- name: UpsertProxyRequestTupleRollupMinute :exec
INSERT INTO proxy_request_tuple_rollup_minutes (
    bucket_unix_millis, listener_id, route_target_id, route_id, agent_id, error_kind, status_class,
    requests, success, client_error, server_error, internal_error, duration_ms_sum,
    request_bytes, response_bytes
) VALUES (
    ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
)
ON CONFLICT(bucket_unix_millis, listener_id, route_target_id, route_id, agent_id, error_kind, status_class) DO UPDATE SET
    requests = proxy_request_tuple_rollup_minutes.requests + excluded.requests,
    success = proxy_request_tuple_rollup_minutes.success + excluded.success,
    client_error = proxy_request_tuple_rollup_minutes.client_error + excluded.client_error,
    server_error = proxy_request_tuple_rollup_minutes.server_error + excluded.server_error,
    internal_error = proxy_request_tuple_rollup_minutes.internal_error + excluded.internal_error,
    duration_ms_sum = proxy_request_tuple_rollup_minutes.duration_ms_sum + excluded.duration_ms_sum,
    request_bytes = proxy_request_tuple_rollup_minutes.request_bytes + excluded.request_bytes,
    response_bytes = proxy_request_tuple_rollup_minutes.response_bytes + excluded.response_bytes,
    updated_at = CURRENT_TIMESTAMP;

-- name: UpsertProxyRequestStatusRollupMinute :exec
INSERT INTO proxy_request_status_rollup_minutes (
    bucket_unix_millis, status_code, requests, success, client_error, server_error,
    internal_error, duration_ms_sum, request_bytes, response_bytes
) VALUES (
    ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
)
ON CONFLICT(bucket_unix_millis, status_code) DO UPDATE SET
    requests = proxy_request_status_rollup_minutes.requests + excluded.requests,
    success = proxy_request_status_rollup_minutes.success + excluded.success,
    client_error = proxy_request_status_rollup_minutes.client_error + excluded.client_error,
    server_error = proxy_request_status_rollup_minutes.server_error + excluded.server_error,
    internal_error = proxy_request_status_rollup_minutes.internal_error + excluded.internal_error,
    duration_ms_sum = proxy_request_status_rollup_minutes.duration_ms_sum + excluded.duration_ms_sum,
    request_bytes = proxy_request_status_rollup_minutes.request_bytes + excluded.request_bytes,
    response_bytes = proxy_request_status_rollup_minutes.response_bytes + excluded.response_bytes,
    updated_at = CURRENT_TIMESTAMP;

-- name: UpsertAgentStatRollupMinute :exec
INSERT INTO agent_stat_rollup_minutes (
    bucket_unix_millis, samples, req_success, req_client_error, req_server_error, req_internal_error,
    bytes_rx, bytes_tx, memory_mb_sum, max_memory_mb, goroutines_sum, max_goroutines,
    cpu_percent_sum, max_cpu_percent
) VALUES (
    ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
)
ON CONFLICT(bucket_unix_millis) DO UPDATE SET
    samples = agent_stat_rollup_minutes.samples + excluded.samples,
    req_success = agent_stat_rollup_minutes.req_success + excluded.req_success,
    req_client_error = agent_stat_rollup_minutes.req_client_error + excluded.req_client_error,
    req_server_error = agent_stat_rollup_minutes.req_server_error + excluded.req_server_error,
    req_internal_error = agent_stat_rollup_minutes.req_internal_error + excluded.req_internal_error,
    bytes_rx = agent_stat_rollup_minutes.bytes_rx + excluded.bytes_rx,
    bytes_tx = agent_stat_rollup_minutes.bytes_tx + excluded.bytes_tx,
    memory_mb_sum = agent_stat_rollup_minutes.memory_mb_sum + excluded.memory_mb_sum,
    max_memory_mb = MAX(agent_stat_rollup_minutes.max_memory_mb, excluded.max_memory_mb),
    goroutines_sum = agent_stat_rollup_minutes.goroutines_sum + excluded.goroutines_sum,
    max_goroutines = MAX(agent_stat_rollup_minutes.max_goroutines, excluded.max_goroutines),
    cpu_percent_sum = agent_stat_rollup_minutes.cpu_percent_sum + excluded.cpu_percent_sum,
    max_cpu_percent = MAX(agent_stat_rollup_minutes.max_cpu_percent, excluded.max_cpu_percent),
    updated_at = CURRENT_TIMESTAMP;

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
FROM proxy_request_events AS pre INDEXED BY idx_proxy_request_events_occurred_at
LEFT JOIN public_listeners pl ON pl.id = pre.listener_id
WHERE pre.occurred_at >= ?
GROUP BY pre.listener_id, pl.name
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
FROM proxy_request_events AS pre INDEXED BY idx_proxy_request_events_occurred_at
LEFT JOIN public_routes pr ON pr.id = pre.route_id
WHERE pre.occurred_at >= ?
GROUP BY pre.route_id, pr.id, pr.host_pattern, pr.path_prefix
ORDER BY requests DESC, id ASC
LIMIT 5;

-- name: ListTopProxyRouteTargetsSince :many
SELECT
    CAST(COALESCE(pre.route_target_id, 0) AS INTEGER) AS id,
    COALESCE(prt.name, CASE WHEN pre.route_target_id IS NULL THEN 'unknown target' ELSE 'target #' || pre.route_target_id END) AS label,
    COUNT(*) AS requests,
    CAST(COALESCE(SUM(CASE WHEN pre.status_code >= 200 AND pre.status_code < 400 THEN 1 ELSE 0 END), 0) AS INTEGER) AS success,
    CAST(COALESCE(SUM(CASE WHEN pre.status_code >= 400 AND pre.status_code < 500 THEN 1 ELSE 0 END), 0) AS INTEGER) AS client_error,
    CAST(COALESCE(SUM(CASE WHEN pre.status_code >= 500 THEN 1 ELSE 0 END), 0) AS INTEGER) AS server_error,
    CAST(COALESCE(SUM(CASE WHEN pre.error_kind != '' THEN 1 ELSE 0 END), 0) AS INTEGER) AS internal_error,
    CAST(COALESCE(AVG(pre.duration_ms), 0) AS INTEGER) AS avg_duration_ms,
    CAST(COALESCE(SUM(pre.request_bytes), 0) AS INTEGER) AS request_bytes,
    CAST(COALESCE(SUM(pre.response_bytes), 0) AS INTEGER) AS response_bytes
FROM proxy_request_events AS pre INDEXED BY idx_proxy_request_events_occurred_at
LEFT JOIN public_route_targets prt ON prt.id = pre.route_target_id
WHERE pre.occurred_at >= ?
GROUP BY pre.route_target_id, prt.name
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
FROM proxy_request_events AS pre INDEXED BY idx_proxy_request_events_occurred_at
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
FROM proxy_request_events INDEXED BY idx_proxy_request_events_occurred_at
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
FROM proxy_request_events INDEXED BY idx_proxy_request_events_occurred_at
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
FROM proxy_request_events INDEXED BY idx_proxy_request_events_occurred_at
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

-- name: GetProxyRequestRollupSummarySince :one
SELECT
    CAST(COALESCE(SUM(requests), 0) AS INTEGER) AS total_requests,
    CAST(COALESCE(SUM(success), 0) AS INTEGER) AS success,
    CAST(COALESCE(SUM(client_error), 0) AS INTEGER) AS client_error,
    CAST(COALESCE(SUM(server_error), 0) AS INTEGER) AS server_error,
    CAST(COALESCE(SUM(internal_error), 0) AS INTEGER) AS internal_error,
    CAST(CASE WHEN COALESCE(SUM(requests), 0) > 0 THEN COALESCE(SUM(duration_ms_sum), 0) / SUM(requests) ELSE 0 END AS INTEGER) AS avg_duration_ms,
    CAST(COALESCE(SUM(request_bytes), 0) AS INTEGER) AS request_bytes,
    CAST(COALESCE(SUM(response_bytes), 0) AS INTEGER) AS response_bytes,
    CAST(COALESCE(SUM(request_bytes + response_bytes), 0) AS INTEGER) AS total_bytes,
    CAST(CASE WHEN COALESCE(SUM(requests), 0) > 0 THEN COALESCE(SUM(request_bytes), 0) / SUM(requests) ELSE 0 END AS INTEGER) AS avg_request_bytes,
    CAST(CASE WHEN COALESCE(SUM(requests), 0) > 0 THEN COALESCE(SUM(response_bytes), 0) / SUM(requests) ELSE 0 END AS INTEGER) AS avg_response_bytes,
    CAST(COALESCE(MAX(max_duration_ms), 0) AS INTEGER) AS max_duration_ms,
    CAST(COALESCE(SUM(slow_requests), 0) AS INTEGER) AS slow_requests,
    CAST(COALESCE(SUM(cache_hits), 0) AS INTEGER) AS cache_hits,
    CAST(COALESCE(SUM(cache_misses), 0) AS INTEGER) AS cache_misses,
    CAST(COALESCE(SUM(cache_bypasses), 0) AS INTEGER) AS cache_bypasses,
    CAST(COALESCE(SUM(cache_stored), 0) AS INTEGER) AS cache_stored,
    CAST(COALESCE(SUM(cache_store_failed), 0) AS INTEGER) AS cache_store_failed,
    CAST(COALESCE(SUM(cache_hit_bytes), 0) AS INTEGER) AS cache_hit_bytes,
    CAST(COALESCE(SUM(cache_stored_bytes), 0) AS INTEGER) AS cache_stored_bytes
FROM proxy_request_rollup_minutes
WHERE bucket_unix_millis >= ?;

-- name: GetAgentStatsRollupSummarySince :one
SELECT
    CAST(COALESCE(SUM(samples), 0) AS INTEGER) AS samples,
    CAST(COALESCE(SUM(req_success), 0) AS INTEGER) AS req_success,
    CAST(COALESCE(SUM(req_client_error), 0) AS INTEGER) AS req_client_error,
    CAST(COALESCE(SUM(req_server_error), 0) AS INTEGER) AS req_server_error,
    CAST(COALESCE(SUM(req_internal_error), 0) AS INTEGER) AS req_internal_error,
    CAST(COALESCE(SUM(bytes_rx), 0) AS INTEGER) AS bytes_rx,
    CAST(COALESCE(SUM(bytes_tx), 0) AS INTEGER) AS bytes_tx,
    CAST(CASE WHEN COALESCE(SUM(samples), 0) > 0 THEN COALESCE(SUM(memory_mb_sum), 0) / SUM(samples) ELSE 0 END AS INTEGER) AS avg_memory_mb,
    CAST(COALESCE(MAX(max_memory_mb), 0) AS INTEGER) AS max_memory_mb,
    CAST(CASE WHEN COALESCE(SUM(samples), 0) > 0 THEN COALESCE(SUM(goroutines_sum), 0) / SUM(samples) ELSE 0 END AS INTEGER) AS avg_goroutines,
    CAST(COALESCE(MAX(max_goroutines), 0) AS INTEGER) AS max_goroutines,
    CAST(CASE WHEN COALESCE(SUM(samples), 0) > 0 THEN COALESCE(SUM(cpu_percent_sum), 0) / SUM(samples) ELSE 0 END AS REAL) AS avg_cpu_percent,
    CAST(COALESCE(MAX(max_cpu_percent), 0) AS REAL) AS max_cpu_percent
FROM agent_stat_rollup_minutes
WHERE bucket_unix_millis >= ?;

-- name: ListTopProxyListenersRollupsSince :many
SELECT
    r.listener_id AS id,
    COALESCE(pl.name, CASE WHEN r.listener_id = 0 THEN 'unknown listener' ELSE 'listener #' || r.listener_id END) AS label,
    CAST(COALESCE(SUM(r.requests), 0) AS INTEGER) AS requests,
    CAST(COALESCE(SUM(r.success), 0) AS INTEGER) AS success,
    CAST(COALESCE(SUM(r.client_error), 0) AS INTEGER) AS client_error,
    CAST(COALESCE(SUM(r.server_error), 0) AS INTEGER) AS server_error,
    CAST(COALESCE(SUM(r.internal_error), 0) AS INTEGER) AS internal_error,
    CAST(CASE WHEN COALESCE(SUM(r.requests), 0) > 0 THEN COALESCE(SUM(r.duration_ms_sum), 0) / SUM(r.requests) ELSE 0 END AS INTEGER) AS avg_duration_ms,
    CAST(COALESCE(SUM(r.request_bytes), 0) AS INTEGER) AS request_bytes,
    CAST(COALESCE(SUM(r.response_bytes), 0) AS INTEGER) AS response_bytes
FROM proxy_request_tuple_rollup_minutes r
LEFT JOIN public_listeners pl ON pl.id = r.listener_id
WHERE r.bucket_unix_millis >= ?
GROUP BY r.listener_id, pl.name
ORDER BY requests DESC, id ASC
LIMIT 5;

-- name: ListTopProxyRoutesRollupsSince :many
SELECT
    r.route_id AS id,
    CASE
        WHEN r.route_id = 0 THEN 'Default route'
        WHEN pr.id IS NULL THEN 'route #' || r.route_id
        WHEN pr.host_pattern != '' AND pr.path_prefix != '' THEN pr.host_pattern || ' ' || pr.path_prefix
        WHEN pr.host_pattern != '' THEN pr.host_pattern
        WHEN pr.path_prefix != '' THEN pr.path_prefix
        ELSE 'route #' || pr.id
    END AS label,
    CAST(COALESCE(SUM(r.requests), 0) AS INTEGER) AS requests,
    CAST(COALESCE(SUM(r.success), 0) AS INTEGER) AS success,
    CAST(COALESCE(SUM(r.client_error), 0) AS INTEGER) AS client_error,
    CAST(COALESCE(SUM(r.server_error), 0) AS INTEGER) AS server_error,
    CAST(COALESCE(SUM(r.internal_error), 0) AS INTEGER) AS internal_error,
    CAST(CASE WHEN COALESCE(SUM(r.requests), 0) > 0 THEN COALESCE(SUM(r.duration_ms_sum), 0) / SUM(r.requests) ELSE 0 END AS INTEGER) AS avg_duration_ms,
    CAST(COALESCE(SUM(r.request_bytes), 0) AS INTEGER) AS request_bytes,
    CAST(COALESCE(SUM(r.response_bytes), 0) AS INTEGER) AS response_bytes
FROM proxy_request_tuple_rollup_minutes r
LEFT JOIN public_routes pr ON pr.id = r.route_id
WHERE r.bucket_unix_millis >= ?
GROUP BY r.route_id, pr.id, pr.host_pattern, pr.path_prefix
ORDER BY requests DESC, id ASC
LIMIT 5;

-- name: ListTopProxyRouteTargetsRollupsSince :many
SELECT
    r.route_target_id AS id,
    COALESCE(prt.name, CASE WHEN r.route_target_id = 0 THEN 'unknown target' ELSE 'target #' || r.route_target_id END) AS label,
    CAST(COALESCE(SUM(r.requests), 0) AS INTEGER) AS requests,
    CAST(COALESCE(SUM(r.success), 0) AS INTEGER) AS success,
    CAST(COALESCE(SUM(r.client_error), 0) AS INTEGER) AS client_error,
    CAST(COALESCE(SUM(r.server_error), 0) AS INTEGER) AS server_error,
    CAST(COALESCE(SUM(r.internal_error), 0) AS INTEGER) AS internal_error,
    CAST(CASE WHEN COALESCE(SUM(r.requests), 0) > 0 THEN COALESCE(SUM(r.duration_ms_sum), 0) / SUM(r.requests) ELSE 0 END AS INTEGER) AS avg_duration_ms,
    CAST(COALESCE(SUM(r.request_bytes), 0) AS INTEGER) AS request_bytes,
    CAST(COALESCE(SUM(r.response_bytes), 0) AS INTEGER) AS response_bytes
FROM proxy_request_tuple_rollup_minutes r
LEFT JOIN public_route_targets prt ON prt.id = r.route_target_id
WHERE r.bucket_unix_millis >= ?
GROUP BY r.route_target_id, prt.name
ORDER BY requests DESC, id ASC
LIMIT 5;

-- name: ListTopProxyAgentsRollupsSince :many
SELECT
    r.agent_id AS id,
    COALESCE(a.name, 'agent #' || r.agent_id) AS label,
    CAST(COALESCE(SUM(r.requests), 0) AS INTEGER) AS requests,
    CAST(COALESCE(SUM(r.success), 0) AS INTEGER) AS success,
    CAST(COALESCE(SUM(r.client_error), 0) AS INTEGER) AS client_error,
    CAST(COALESCE(SUM(r.server_error), 0) AS INTEGER) AS server_error,
    CAST(COALESCE(SUM(r.internal_error), 0) AS INTEGER) AS internal_error,
    CAST(CASE WHEN COALESCE(SUM(r.requests), 0) > 0 THEN COALESCE(SUM(r.duration_ms_sum), 0) / SUM(r.requests) ELSE 0 END AS INTEGER) AS avg_duration_ms,
    CAST(COALESCE(SUM(r.request_bytes), 0) AS INTEGER) AS request_bytes,
    CAST(COALESCE(SUM(r.response_bytes), 0) AS INTEGER) AS response_bytes
FROM proxy_request_tuple_rollup_minutes r
LEFT JOIN agents a ON a.id = r.agent_id
WHERE r.bucket_unix_millis >= ?
  AND r.agent_id != 0
GROUP BY r.agent_id, a.name
ORDER BY requests DESC, id ASC
LIMIT 5;

-- name: ListTopProxyErrorKindsRollupsSince :many
SELECT
    CAST(0 AS INTEGER) AS id,
    r.error_kind AS label,
    CAST(COALESCE(SUM(r.requests), 0) AS INTEGER) AS requests,
    CAST(COALESCE(SUM(r.success), 0) AS INTEGER) AS success,
    CAST(COALESCE(SUM(r.client_error), 0) AS INTEGER) AS client_error,
    CAST(COALESCE(SUM(r.server_error), 0) AS INTEGER) AS server_error,
    CAST(COALESCE(SUM(r.internal_error), 0) AS INTEGER) AS internal_error,
    CAST(CASE WHEN COALESCE(SUM(r.requests), 0) > 0 THEN COALESCE(SUM(r.duration_ms_sum), 0) / SUM(r.requests) ELSE 0 END AS INTEGER) AS avg_duration_ms,
    CAST(COALESCE(SUM(r.request_bytes), 0) AS INTEGER) AS request_bytes,
    CAST(COALESCE(SUM(r.response_bytes), 0) AS INTEGER) AS response_bytes
FROM proxy_request_tuple_rollup_minutes r
WHERE r.bucket_unix_millis >= ?
  AND r.error_kind != ''
GROUP BY r.error_kind
ORDER BY requests DESC, label ASC
LIMIT 5;

-- name: ListProxyStatusClassesRollupsSince :many
SELECT
    r.status_class AS id,
    CAST(r.status_class AS TEXT) || 'xx' AS label,
    CAST(COALESCE(SUM(r.requests), 0) AS INTEGER) AS requests,
    CAST(COALESCE(SUM(r.success), 0) AS INTEGER) AS success,
    CAST(COALESCE(SUM(r.client_error), 0) AS INTEGER) AS client_error,
    CAST(COALESCE(SUM(r.server_error), 0) AS INTEGER) AS server_error,
    CAST(COALESCE(SUM(r.internal_error), 0) AS INTEGER) AS internal_error,
    CAST(CASE WHEN COALESCE(SUM(r.requests), 0) > 0 THEN COALESCE(SUM(r.duration_ms_sum), 0) / SUM(r.requests) ELSE 0 END AS INTEGER) AS avg_duration_ms,
    CAST(COALESCE(SUM(r.request_bytes), 0) AS INTEGER) AS request_bytes,
    CAST(COALESCE(SUM(r.response_bytes), 0) AS INTEGER) AS response_bytes
FROM proxy_request_tuple_rollup_minutes r
WHERE r.bucket_unix_millis >= ?
  AND r.status_class >= 2
  AND r.status_class < 6
GROUP BY r.status_class
ORDER BY id ASC;

-- name: ListProxyStatusCodeRollupsSince :many
SELECT
    r.status_code,
    CAST(COALESCE(SUM(r.requests), 0) AS INTEGER) AS requests,
    CAST(COALESCE(SUM(r.success), 0) AS INTEGER) AS success,
    CAST(COALESCE(SUM(r.client_error), 0) AS INTEGER) AS client_error,
    CAST(COALESCE(SUM(r.server_error), 0) AS INTEGER) AS server_error,
    CAST(COALESCE(SUM(r.internal_error), 0) AS INTEGER) AS internal_error,
    CAST(CASE WHEN COALESCE(SUM(r.requests), 0) > 0 THEN COALESCE(SUM(r.duration_ms_sum), 0) / SUM(r.requests) ELSE 0 END AS INTEGER) AS avg_duration_ms,
    CAST(COALESCE(SUM(r.request_bytes), 0) AS INTEGER) AS request_bytes,
    CAST(COALESCE(SUM(r.response_bytes), 0) AS INTEGER) AS response_bytes
FROM proxy_request_status_rollup_minutes r
WHERE r.bucket_unix_millis >= ?
GROUP BY r.status_code
ORDER BY requests DESC, status_code ASC;

-- name: ListProblemProxyListenersRollupsSince :many
SELECT
    r.listener_id AS id,
    COALESCE(pl.name, CASE WHEN r.listener_id = 0 THEN 'unknown listener' ELSE 'listener #' || r.listener_id END) AS label,
    CAST(COALESCE(SUM(r.requests), 0) AS INTEGER) AS requests,
    CAST(COALESCE(SUM(r.success), 0) AS INTEGER) AS success,
    CAST(COALESCE(SUM(r.client_error), 0) AS INTEGER) AS client_error,
    CAST(COALESCE(SUM(r.server_error), 0) AS INTEGER) AS server_error,
    CAST(COALESCE(SUM(r.internal_error), 0) AS INTEGER) AS internal_error,
    CAST(CASE WHEN COALESCE(SUM(r.requests), 0) > 0 THEN COALESCE(SUM(r.duration_ms_sum), 0) / SUM(r.requests) ELSE 0 END AS INTEGER) AS avg_duration_ms,
    CAST(COALESCE(SUM(r.request_bytes), 0) AS INTEGER) AS request_bytes,
    CAST(COALESCE(SUM(r.response_bytes), 0) AS INTEGER) AS response_bytes
FROM proxy_request_tuple_rollup_minutes r
LEFT JOIN public_listeners pl ON pl.id = r.listener_id
WHERE r.bucket_unix_millis >= ?
GROUP BY r.listener_id, pl.name
HAVING SUM(r.client_error) > 0 OR SUM(r.server_error) > 0 OR SUM(r.internal_error) > 0
ORDER BY internal_error DESC, server_error DESC, client_error DESC, requests DESC, id ASC
LIMIT 10;

-- name: ListProblemProxyRoutesRollupsSince :many
SELECT
    r.route_id AS id,
    CASE
        WHEN r.route_id = 0 THEN 'Default route'
        WHEN pr.id IS NULL THEN 'route #' || r.route_id
        WHEN pr.host_pattern != '' AND pr.path_prefix != '' THEN pr.host_pattern || ' ' || pr.path_prefix
        WHEN pr.host_pattern != '' THEN pr.host_pattern
        WHEN pr.path_prefix != '' THEN pr.path_prefix
        ELSE 'route #' || pr.id
    END AS label,
    CAST(COALESCE(SUM(r.requests), 0) AS INTEGER) AS requests,
    CAST(COALESCE(SUM(r.success), 0) AS INTEGER) AS success,
    CAST(COALESCE(SUM(r.client_error), 0) AS INTEGER) AS client_error,
    CAST(COALESCE(SUM(r.server_error), 0) AS INTEGER) AS server_error,
    CAST(COALESCE(SUM(r.internal_error), 0) AS INTEGER) AS internal_error,
    CAST(CASE WHEN COALESCE(SUM(r.requests), 0) > 0 THEN COALESCE(SUM(r.duration_ms_sum), 0) / SUM(r.requests) ELSE 0 END AS INTEGER) AS avg_duration_ms,
    CAST(COALESCE(SUM(r.request_bytes), 0) AS INTEGER) AS request_bytes,
    CAST(COALESCE(SUM(r.response_bytes), 0) AS INTEGER) AS response_bytes
FROM proxy_request_tuple_rollup_minutes r
LEFT JOIN public_routes pr ON pr.id = r.route_id
WHERE r.bucket_unix_millis >= ?
GROUP BY r.route_id, pr.id, pr.host_pattern, pr.path_prefix
HAVING SUM(r.client_error) > 0 OR SUM(r.server_error) > 0 OR SUM(r.internal_error) > 0
ORDER BY internal_error DESC, server_error DESC, client_error DESC, requests DESC, id ASC
LIMIT 10;

-- name: ListProblemProxyRouteTargetsRollupsSince :many
SELECT
    r.route_target_id AS id,
    COALESCE(prt.name, CASE WHEN r.route_target_id = 0 THEN 'unknown target' ELSE 'target #' || r.route_target_id END) AS label,
    CAST(COALESCE(SUM(r.requests), 0) AS INTEGER) AS requests,
    CAST(COALESCE(SUM(r.success), 0) AS INTEGER) AS success,
    CAST(COALESCE(SUM(r.client_error), 0) AS INTEGER) AS client_error,
    CAST(COALESCE(SUM(r.server_error), 0) AS INTEGER) AS server_error,
    CAST(COALESCE(SUM(r.internal_error), 0) AS INTEGER) AS internal_error,
    CAST(CASE WHEN COALESCE(SUM(r.requests), 0) > 0 THEN COALESCE(SUM(r.duration_ms_sum), 0) / SUM(r.requests) ELSE 0 END AS INTEGER) AS avg_duration_ms,
    CAST(COALESCE(SUM(r.request_bytes), 0) AS INTEGER) AS request_bytes,
    CAST(COALESCE(SUM(r.response_bytes), 0) AS INTEGER) AS response_bytes
FROM proxy_request_tuple_rollup_minutes r
LEFT JOIN public_route_targets prt ON prt.id = r.route_target_id
WHERE r.bucket_unix_millis >= ?
GROUP BY r.route_target_id, prt.name
HAVING SUM(r.client_error) > 0 OR SUM(r.server_error) > 0 OR SUM(r.internal_error) > 0
ORDER BY internal_error DESC, server_error DESC, client_error DESC, requests DESC, id ASC
LIMIT 10;

-- name: ListProblemProxyAgentsRollupsSince :many
SELECT
    r.agent_id AS id,
    COALESCE(a.name, 'agent #' || r.agent_id) AS label,
    CAST(COALESCE(SUM(r.requests), 0) AS INTEGER) AS requests,
    CAST(COALESCE(SUM(r.success), 0) AS INTEGER) AS success,
    CAST(COALESCE(SUM(r.client_error), 0) AS INTEGER) AS client_error,
    CAST(COALESCE(SUM(r.server_error), 0) AS INTEGER) AS server_error,
    CAST(COALESCE(SUM(r.internal_error), 0) AS INTEGER) AS internal_error,
    CAST(CASE WHEN COALESCE(SUM(r.requests), 0) > 0 THEN COALESCE(SUM(r.duration_ms_sum), 0) / SUM(r.requests) ELSE 0 END AS INTEGER) AS avg_duration_ms,
    CAST(COALESCE(SUM(r.request_bytes), 0) AS INTEGER) AS request_bytes,
    CAST(COALESCE(SUM(r.response_bytes), 0) AS INTEGER) AS response_bytes
FROM proxy_request_tuple_rollup_minutes r
LEFT JOIN agents a ON a.id = r.agent_id
WHERE r.bucket_unix_millis >= ?
  AND r.agent_id != 0
GROUP BY r.agent_id, a.name
HAVING SUM(r.client_error) > 0 OR SUM(r.server_error) > 0 OR SUM(r.internal_error) > 0
ORDER BY internal_error DESC, server_error DESC, client_error DESC, requests DESC, id ASC
LIMIT 10;

-- name: ListProblemProxyErrorKindsRollupsSince :many
SELECT
    CAST(0 AS INTEGER) AS id,
    r.error_kind AS label,
    CAST(COALESCE(SUM(r.requests), 0) AS INTEGER) AS requests,
    CAST(COALESCE(SUM(r.success), 0) AS INTEGER) AS success,
    CAST(COALESCE(SUM(r.client_error), 0) AS INTEGER) AS client_error,
    CAST(COALESCE(SUM(r.server_error), 0) AS INTEGER) AS server_error,
    CAST(COALESCE(SUM(r.internal_error), 0) AS INTEGER) AS internal_error,
    CAST(CASE WHEN COALESCE(SUM(r.requests), 0) > 0 THEN COALESCE(SUM(r.duration_ms_sum), 0) / SUM(r.requests) ELSE 0 END AS INTEGER) AS avg_duration_ms,
    CAST(COALESCE(SUM(r.request_bytes), 0) AS INTEGER) AS request_bytes,
    CAST(COALESCE(SUM(r.response_bytes), 0) AS INTEGER) AS response_bytes
FROM proxy_request_tuple_rollup_minutes r
WHERE r.bucket_unix_millis >= ?
  AND r.error_kind != ''
GROUP BY r.error_kind
ORDER BY internal_error DESC, server_error DESC, client_error DESC, requests DESC, label ASC
LIMIT 10;

-- name: ListProxyTrafficBucketRollupsSince :many
SELECT
    CAST((bucket_unix_millis / (CAST(sqlc.arg(bucket_seconds) AS INTEGER) * 1000)) * (CAST(sqlc.arg(bucket_seconds) AS INTEGER) * 1000) AS INTEGER) AS bucket_unix_millis,
    CAST(COALESCE(SUM(requests), 0) AS INTEGER) AS requests,
    CAST(COALESCE(SUM(success), 0) AS INTEGER) AS success,
    CAST(COALESCE(SUM(client_error), 0) AS INTEGER) AS client_error,
    CAST(COALESCE(SUM(server_error), 0) AS INTEGER) AS server_error,
    CAST(COALESCE(SUM(internal_error), 0) AS INTEGER) AS internal_error,
    CAST(COALESCE(SUM(request_bytes), 0) AS INTEGER) AS request_bytes,
    CAST(COALESCE(SUM(response_bytes), 0) AS INTEGER) AS response_bytes,
    CAST(CASE WHEN COALESCE(SUM(requests), 0) > 0 THEN COALESCE(SUM(duration_ms_sum), 0) / SUM(requests) ELSE 0 END AS INTEGER) AS avg_duration_ms
FROM proxy_request_rollup_minutes
WHERE bucket_unix_millis >= sqlc.arg(since_unix_millis)
GROUP BY 1
ORDER BY bucket_unix_millis ASC;

-- name: ListProxyRequestRollupMinutesSince :many
SELECT
    bucket_unix_millis,
    requests,
    success,
    client_error,
    server_error,
    internal_error,
    duration_ms_sum,
    max_duration_ms,
    slow_requests,
    request_bytes,
    response_bytes,
    cache_hits,
    cache_misses,
    cache_bypasses,
    cache_stored,
    cache_store_failed,
    cache_hit_bytes,
    cache_stored_bytes
FROM proxy_request_rollup_minutes
WHERE bucket_unix_millis >= ?
ORDER BY bucket_unix_millis ASC;

-- name: ListProxyRequestTupleRollupMinutesSince :many
SELECT
    bucket_unix_millis,
    listener_id,
    route_id,
    route_target_id,
    agent_id,
    error_kind,
    status_class,
    requests,
    success,
    client_error,
    server_error,
    internal_error,
    duration_ms_sum,
    request_bytes,
    response_bytes
FROM proxy_request_tuple_rollup_minutes
WHERE bucket_unix_millis >= ?
ORDER BY bucket_unix_millis ASC;

-- name: ListRecentProxyProblemSamplesSince :many
SELECT
    pre.occurred_at,
    pre.method,
    pre.host,
    pre.path_prefix,
    pre.status_code,
    pre.error_kind,
    COALESCE(pl.name, CASE WHEN pre.listener_id IS NULL THEN '' ELSE 'listener #' || pre.listener_id END) AS listener_label,
    CASE
        WHEN pre.route_id IS NULL THEN ''
        WHEN pr.id IS NULL THEN 'route #' || pre.route_id
        WHEN pr.host_pattern != '' AND pr.path_prefix != '' THEN pr.host_pattern || ' ' || pr.path_prefix
        WHEN pr.host_pattern != '' THEN pr.host_pattern
        WHEN pr.path_prefix != '' THEN pr.path_prefix
        ELSE 'route #' || pr.id
    END AS route_label,
    COALESCE(prt.name, CASE WHEN pre.route_target_id IS NULL THEN '' ELSE 'target #' || pre.route_target_id END) AS route_target_label,
    COALESCE(a.name, CASE WHEN pre.agent_id IS NULL THEN '' ELSE 'agent #' || pre.agent_id END) AS agent_label,
    pre.duration_ms,
    pre.request_bytes,
    pre.response_bytes
FROM proxy_request_events AS pre INDEXED BY idx_proxy_request_events_occurred_at
LEFT JOIN public_listeners pl ON pl.id = pre.listener_id
LEFT JOIN public_routes pr ON pr.id = pre.route_id
LEFT JOIN public_route_targets prt ON prt.id = pre.route_target_id
LEFT JOIN agents a ON a.id = pre.agent_id
WHERE pre.occurred_at >= sqlc.arg(since)
  AND (pre.status_code >= 400 OR pre.error_kind != '')
ORDER BY pre.occurred_at DESC, pre.id DESC
LIMIT sqlc.arg(limit);

-- name: ListAgentStatRollupMinutesSince :many
SELECT
    bucket_unix_millis,
    samples,
    req_success,
    req_client_error,
    req_server_error,
    req_internal_error,
    bytes_rx,
    bytes_tx,
    memory_mb_sum,
    max_memory_mb,
    goroutines_sum,
    max_goroutines,
    cpu_percent_sum,
    max_cpu_percent
FROM agent_stat_rollup_minutes
WHERE bucket_unix_millis >= ?
ORDER BY bucket_unix_millis ASC;

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

-- name: ListConnectionsSince :many
SELECT
    c.id,
    c.agent_id,
    COALESCE(a.public_id, '') AS agent_public_id,
    COALESCE(a.name, '') AS agent_name,
    c.connected_at,
    c.disconnected_at
FROM connections c
LEFT JOIN agents a ON a.id = c.agent_id
WHERE c.connected_at >= ?
   OR c.disconnected_at IS NULL
   OR c.disconnected_at >= ?
ORDER BY c.connected_at ASC;

-- name: ListRecentConnections :many
SELECT
    c.id,
    c.agent_id,
    COALESCE(a.public_id, '') AS agent_public_id,
    COALESCE(a.name, '') AS agent_name,
    c.connected_at,
    c.disconnected_at
FROM connections c
LEFT JOIN agents a ON a.id = c.agent_id
ORDER BY c.connected_at DESC, c.id DESC
LIMIT ?;

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

-- name: ListAgentLabels :many
SELECT agent_id, key, value, source, created_at, updated_at
FROM public_agent_labels
ORDER BY agent_id ASC, key ASC;

-- name: ListAgentLabelsByAgent :many
SELECT agent_id, key, value, source, created_at, updated_at
FROM public_agent_labels
WHERE agent_id = ?
ORDER BY key ASC;

-- name: UpsertAgentLabel :one
INSERT INTO public_agent_labels (agent_id, key, value, source)
VALUES (?, ?, ?, ?)
ON CONFLICT(agent_id, key) DO UPDATE SET
    value = excluded.value,
    source = excluded.source,
    updated_at = CURRENT_TIMESTAMP
RETURNING agent_id, key, value, source, created_at, updated_at;

-- name: DeleteAgentLabelsByAgent :exec
DELETE FROM public_agent_labels
WHERE agent_id = ?;

-- name: DeleteUserAgentLabelsByAgent :exec
DELETE FROM public_agent_labels
WHERE agent_id = ?
  AND source = 'user';

-- name: DeleteAgentLabel :exec
DELETE FROM public_agent_labels
WHERE agent_id = ?
  AND key = ?;

-- name: GetLatestAgentStatByAgent :one
SELECT id, agent_id, reported_at, memory_mb, goroutines, req_success, req_client_error, req_server_error, req_internal_error, bytes_rx, bytes_tx, cpu_percent
FROM agent_stats
WHERE agent_id = ?
ORDER BY id DESC
LIMIT 1;

-- name: DeleteProxyRequestEventsBefore :exec
DELETE FROM proxy_request_events
WHERE occurred_at < ?;

-- name: DeleteOldestProxyRequestEventsOverLimit :execrows
DELETE FROM proxy_request_events
WHERE id IN (
    SELECT id
    FROM (
        SELECT id
        FROM proxy_request_events
        ORDER BY occurred_at DESC, id DESC
        LIMIT -1 OFFSET sqlc.arg(offset)
    )
    ORDER BY id ASC
    LIMIT sqlc.arg(delete_limit)
);

-- name: DeleteAgentStatsBefore :exec
DELETE FROM agent_stats
WHERE reported_at < ?;

-- name: DeleteOldestAgentStatsOverLimit :execrows
DELETE FROM agent_stats
WHERE id IN (
    SELECT id
    FROM (
        SELECT id
        FROM agent_stats
        ORDER BY reported_at DESC, id DESC
        LIMIT -1 OFFSET sqlc.arg(offset)
    )
    ORDER BY id ASC
    LIMIT sqlc.arg(delete_limit)
);

-- name: DeleteDisconnectedConnectionsBefore :exec
DELETE FROM connections
WHERE disconnected_at IS NOT NULL
  AND disconnected_at < ?;

-- name: DeleteProxyRequestRollupsBefore :exec
DELETE FROM proxy_request_rollup_minutes
WHERE bucket_unix_millis < ?;

-- name: DeleteProxyRequestTupleRollupsBefore :exec
DELETE FROM proxy_request_tuple_rollup_minutes
WHERE bucket_unix_millis < ?;

-- name: DeleteProxyRequestStatusRollupsBefore :exec
DELETE FROM proxy_request_status_rollup_minutes
WHERE bucket_unix_millis < ?;

-- name: DeleteAgentStatRollupsBefore :exec
DELETE FROM agent_stat_rollup_minutes
WHERE bucket_unix_millis < ?;

-- name: GetObservabilityRollupState :one
SELECT id, proxy_backfill_upper_id, proxy_backfilled_through_id, agent_backfill_upper_id, agent_backfilled_through_id, created_at, updated_at
FROM observability_rollup_state
WHERE id = 1;

-- name: GetNextProxyRollupBackfillThroughID :one
SELECT CAST(COALESCE(MAX(id), sqlc.arg(current_id)) AS INTEGER) AS through_id
FROM (
    SELECT id
    FROM proxy_request_events
    WHERE id > sqlc.arg(current_id)
      AND id <= sqlc.arg(upper_id)
    ORDER BY id ASC
    LIMIT sqlc.arg(batch_size)
);

-- name: GetNextAgentRollupBackfillThroughID :one
SELECT CAST(COALESCE(MAX(id), sqlc.arg(current_id)) AS INTEGER) AS through_id
FROM (
    SELECT id
    FROM agent_stats
    WHERE id > sqlc.arg(current_id)
      AND id <= sqlc.arg(upper_id)
    ORDER BY id ASC
    LIMIT sqlc.arg(batch_size)
);

-- name: BackfillProxyRequestRollupMinutesRange :exec
INSERT INTO proxy_request_rollup_minutes (
    bucket_unix_millis, requests, success, client_error, server_error, internal_error,
    duration_ms_sum, max_duration_ms, slow_requests, request_bytes, response_bytes,
    cache_hits, cache_misses, cache_bypasses, cache_stored, cache_store_failed,
    cache_hit_bytes, cache_stored_bytes
)
SELECT
    CAST((unixepoch(occurred_at) / 60) * 60 * 1000 AS INTEGER) AS bucket_unix_millis,
    COUNT(*) AS requests,
    CAST(COALESCE(SUM(CASE WHEN status_code >= 200 AND status_code < 400 THEN 1 ELSE 0 END), 0) AS INTEGER) AS success,
    CAST(COALESCE(SUM(CASE WHEN status_code >= 400 AND status_code < 500 THEN 1 ELSE 0 END), 0) AS INTEGER) AS client_error,
    CAST(COALESCE(SUM(CASE WHEN status_code >= 500 THEN 1 ELSE 0 END), 0) AS INTEGER) AS server_error,
    CAST(COALESCE(SUM(CASE WHEN error_kind != '' THEN 1 ELSE 0 END), 0) AS INTEGER) AS internal_error,
    CAST(COALESCE(SUM(duration_ms), 0) AS INTEGER) AS duration_ms_sum,
    CAST(COALESCE(MAX(duration_ms), 0) AS INTEGER) AS max_duration_ms,
    CAST(COALESCE(SUM(CASE WHEN duration_ms >= 1000 THEN 1 ELSE 0 END), 0) AS INTEGER) AS slow_requests,
    CAST(COALESCE(SUM(request_bytes), 0) AS INTEGER) AS request_bytes,
    CAST(COALESCE(SUM(response_bytes), 0) AS INTEGER) AS response_bytes,
    CAST(COALESCE(SUM(CASE WHEN cache_status = 'hit' THEN 1 ELSE 0 END), 0) AS INTEGER) AS cache_hits,
    CAST(COALESCE(SUM(CASE WHEN cache_status IN ('miss', 'stored', 'store_failed') THEN 1 ELSE 0 END), 0) AS INTEGER) AS cache_misses,
    CAST(COALESCE(SUM(CASE WHEN cache_status = 'bypass' THEN 1 ELSE 0 END), 0) AS INTEGER) AS cache_bypasses,
    CAST(COALESCE(SUM(CASE WHEN cache_status = 'stored' THEN 1 ELSE 0 END), 0) AS INTEGER) AS cache_stored,
    CAST(COALESCE(SUM(CASE WHEN cache_status = 'store_failed' THEN 1 ELSE 0 END), 0) AS INTEGER) AS cache_store_failed,
    CAST(COALESCE(SUM(CASE WHEN cache_status = 'hit' THEN cache_bytes ELSE 0 END), 0) AS INTEGER) AS cache_hit_bytes,
    CAST(COALESCE(SUM(CASE WHEN cache_status = 'stored' THEN cache_bytes ELSE 0 END), 0) AS INTEGER) AS cache_stored_bytes
FROM proxy_request_events
WHERE id > sqlc.arg(from_id)
  AND id <= sqlc.arg(through_id)
GROUP BY bucket_unix_millis
ON CONFLICT(bucket_unix_millis) DO UPDATE SET
    requests = proxy_request_rollup_minutes.requests + excluded.requests,
    success = proxy_request_rollup_minutes.success + excluded.success,
    client_error = proxy_request_rollup_minutes.client_error + excluded.client_error,
    server_error = proxy_request_rollup_minutes.server_error + excluded.server_error,
    internal_error = proxy_request_rollup_minutes.internal_error + excluded.internal_error,
    duration_ms_sum = proxy_request_rollup_minutes.duration_ms_sum + excluded.duration_ms_sum,
    max_duration_ms = MAX(proxy_request_rollup_minutes.max_duration_ms, excluded.max_duration_ms),
    slow_requests = proxy_request_rollup_minutes.slow_requests + excluded.slow_requests,
    request_bytes = proxy_request_rollup_minutes.request_bytes + excluded.request_bytes,
    response_bytes = proxy_request_rollup_minutes.response_bytes + excluded.response_bytes,
    cache_hits = proxy_request_rollup_minutes.cache_hits + excluded.cache_hits,
    cache_misses = proxy_request_rollup_minutes.cache_misses + excluded.cache_misses,
    cache_bypasses = proxy_request_rollup_minutes.cache_bypasses + excluded.cache_bypasses,
    cache_stored = proxy_request_rollup_minutes.cache_stored + excluded.cache_stored,
    cache_store_failed = proxy_request_rollup_minutes.cache_store_failed + excluded.cache_store_failed,
    cache_hit_bytes = proxy_request_rollup_minutes.cache_hit_bytes + excluded.cache_hit_bytes,
    cache_stored_bytes = proxy_request_rollup_minutes.cache_stored_bytes + excluded.cache_stored_bytes,
    updated_at = CURRENT_TIMESTAMP;

-- name: BackfillProxyRequestTupleRollupMinutesRange :exec
INSERT INTO proxy_request_tuple_rollup_minutes (
    bucket_unix_millis, listener_id, route_target_id, route_id, agent_id, error_kind, status_class,
    requests, success, client_error, server_error, internal_error, duration_ms_sum,
    request_bytes, response_bytes
)
SELECT
    CAST((unixepoch(occurred_at) / 60) * 60 * 1000 AS INTEGER) AS bucket_unix_millis,
    CAST(COALESCE(listener_id, 0) AS INTEGER) AS listener_id,
    CAST(COALESCE(route_target_id, 0) AS INTEGER) AS route_target_id,
    CAST(COALESCE(route_id, 0) AS INTEGER) AS route_id,
    CAST(COALESCE(agent_id, 0) AS INTEGER) AS agent_id,
    error_kind,
    CAST(CASE WHEN status_code >= 200 AND status_code < 600 THEN status_code / 100 ELSE 0 END AS INTEGER) AS status_class,
    COUNT(*) AS requests,
    CAST(COALESCE(SUM(CASE WHEN status_code >= 200 AND status_code < 400 THEN 1 ELSE 0 END), 0) AS INTEGER) AS success,
    CAST(COALESCE(SUM(CASE WHEN status_code >= 400 AND status_code < 500 THEN 1 ELSE 0 END), 0) AS INTEGER) AS client_error,
    CAST(COALESCE(SUM(CASE WHEN status_code >= 500 THEN 1 ELSE 0 END), 0) AS INTEGER) AS server_error,
    CAST(COALESCE(SUM(CASE WHEN error_kind != '' THEN 1 ELSE 0 END), 0) AS INTEGER) AS internal_error,
    CAST(COALESCE(SUM(duration_ms), 0) AS INTEGER) AS duration_ms_sum,
    CAST(COALESCE(SUM(request_bytes), 0) AS INTEGER) AS request_bytes,
    CAST(COALESCE(SUM(response_bytes), 0) AS INTEGER) AS response_bytes
FROM proxy_request_events
WHERE id > sqlc.arg(from_id)
  AND id <= sqlc.arg(through_id)
GROUP BY bucket_unix_millis, listener_id, route_target_id, route_id, agent_id, error_kind, status_class
ON CONFLICT(bucket_unix_millis, listener_id, route_target_id, route_id, agent_id, error_kind, status_class) DO UPDATE SET
    requests = proxy_request_tuple_rollup_minutes.requests + excluded.requests,
    success = proxy_request_tuple_rollup_minutes.success + excluded.success,
    client_error = proxy_request_tuple_rollup_minutes.client_error + excluded.client_error,
    server_error = proxy_request_tuple_rollup_minutes.server_error + excluded.server_error,
    internal_error = proxy_request_tuple_rollup_minutes.internal_error + excluded.internal_error,
    duration_ms_sum = proxy_request_tuple_rollup_minutes.duration_ms_sum + excluded.duration_ms_sum,
    request_bytes = proxy_request_tuple_rollup_minutes.request_bytes + excluded.request_bytes,
    response_bytes = proxy_request_tuple_rollup_minutes.response_bytes + excluded.response_bytes,
    updated_at = CURRENT_TIMESTAMP;

-- name: BackfillProxyRequestStatusRollupMinutesRange :exec
INSERT INTO proxy_request_status_rollup_minutes (
    bucket_unix_millis, status_code, requests, success, client_error, server_error,
    internal_error, duration_ms_sum, request_bytes, response_bytes
)
SELECT
    CAST((unixepoch(occurred_at) / 60) * 60 * 1000 AS INTEGER) AS bucket_unix_millis,
    status_code,
    COUNT(*) AS requests,
    CAST(COALESCE(SUM(CASE WHEN status_code >= 200 AND status_code < 400 THEN 1 ELSE 0 END), 0) AS INTEGER) AS success,
    CAST(COALESCE(SUM(CASE WHEN status_code >= 400 AND status_code < 500 THEN 1 ELSE 0 END), 0) AS INTEGER) AS client_error,
    CAST(COALESCE(SUM(CASE WHEN status_code >= 500 THEN 1 ELSE 0 END), 0) AS INTEGER) AS server_error,
    CAST(COALESCE(SUM(CASE WHEN error_kind != '' THEN 1 ELSE 0 END), 0) AS INTEGER) AS internal_error,
    CAST(COALESCE(SUM(duration_ms), 0) AS INTEGER) AS duration_ms_sum,
    CAST(COALESCE(SUM(request_bytes), 0) AS INTEGER) AS request_bytes,
    CAST(COALESCE(SUM(response_bytes), 0) AS INTEGER) AS response_bytes
FROM proxy_request_events
WHERE id > sqlc.arg(from_id)
  AND id <= sqlc.arg(through_id)
GROUP BY bucket_unix_millis, status_code
ON CONFLICT(bucket_unix_millis, status_code) DO UPDATE SET
    requests = proxy_request_status_rollup_minutes.requests + excluded.requests,
    success = proxy_request_status_rollup_minutes.success + excluded.success,
    client_error = proxy_request_status_rollup_minutes.client_error + excluded.client_error,
    server_error = proxy_request_status_rollup_minutes.server_error + excluded.server_error,
    internal_error = proxy_request_status_rollup_minutes.internal_error + excluded.internal_error,
    duration_ms_sum = proxy_request_status_rollup_minutes.duration_ms_sum + excluded.duration_ms_sum,
    request_bytes = proxy_request_status_rollup_minutes.request_bytes + excluded.request_bytes,
    response_bytes = proxy_request_status_rollup_minutes.response_bytes + excluded.response_bytes,
    updated_at = CURRENT_TIMESTAMP;

-- name: BackfillAgentStatRollupMinutesRange :exec
INSERT INTO agent_stat_rollup_minutes (
    bucket_unix_millis, samples, req_success, req_client_error, req_server_error, req_internal_error,
    bytes_rx, bytes_tx, memory_mb_sum, max_memory_mb, goroutines_sum, max_goroutines,
    cpu_percent_sum, max_cpu_percent
)
SELECT
    CAST((unixepoch(reported_at) / 60) * 60 * 1000 AS INTEGER) AS bucket_unix_millis,
    COUNT(*) AS samples,
    CAST(COALESCE(SUM(req_success), 0) AS INTEGER) AS req_success,
    CAST(COALESCE(SUM(req_client_error), 0) AS INTEGER) AS req_client_error,
    CAST(COALESCE(SUM(req_server_error), 0) AS INTEGER) AS req_server_error,
    CAST(COALESCE(SUM(req_internal_error), 0) AS INTEGER) AS req_internal_error,
    CAST(COALESCE(SUM(bytes_rx), 0) AS INTEGER) AS bytes_rx,
    CAST(COALESCE(SUM(bytes_tx), 0) AS INTEGER) AS bytes_tx,
    CAST(COALESCE(SUM(memory_mb), 0) AS INTEGER) AS memory_mb_sum,
    CAST(COALESCE(MAX(memory_mb), 0) AS INTEGER) AS max_memory_mb,
    CAST(COALESCE(SUM(goroutines), 0) AS INTEGER) AS goroutines_sum,
    CAST(COALESCE(MAX(goroutines), 0) AS INTEGER) AS max_goroutines,
    CAST(COALESCE(SUM(cpu_percent), 0) AS REAL) AS cpu_percent_sum,
    CAST(COALESCE(MAX(cpu_percent), 0) AS REAL) AS max_cpu_percent
FROM agent_stats
WHERE id > sqlc.arg(from_id)
  AND id <= sqlc.arg(through_id)
GROUP BY bucket_unix_millis
ON CONFLICT(bucket_unix_millis) DO UPDATE SET
    samples = agent_stat_rollup_minutes.samples + excluded.samples,
    req_success = agent_stat_rollup_minutes.req_success + excluded.req_success,
    req_client_error = agent_stat_rollup_minutes.req_client_error + excluded.req_client_error,
    req_server_error = agent_stat_rollup_minutes.req_server_error + excluded.req_server_error,
    req_internal_error = agent_stat_rollup_minutes.req_internal_error + excluded.req_internal_error,
    bytes_rx = agent_stat_rollup_minutes.bytes_rx + excluded.bytes_rx,
    bytes_tx = agent_stat_rollup_minutes.bytes_tx + excluded.bytes_tx,
    memory_mb_sum = agent_stat_rollup_minutes.memory_mb_sum + excluded.memory_mb_sum,
    max_memory_mb = MAX(agent_stat_rollup_minutes.max_memory_mb, excluded.max_memory_mb),
    goroutines_sum = agent_stat_rollup_minutes.goroutines_sum + excluded.goroutines_sum,
    max_goroutines = MAX(agent_stat_rollup_minutes.max_goroutines, excluded.max_goroutines),
    cpu_percent_sum = agent_stat_rollup_minutes.cpu_percent_sum + excluded.cpu_percent_sum,
    max_cpu_percent = MAX(agent_stat_rollup_minutes.max_cpu_percent, excluded.max_cpu_percent),
    updated_at = CURRENT_TIMESTAMP;

-- name: MarkProxyRollupBackfilledThrough :exec
UPDATE observability_rollup_state
SET proxy_backfilled_through_id = ?,
    updated_at = CURRENT_TIMESTAMP
WHERE id = 1;

-- name: MarkAgentRollupBackfilledThrough :exec
UPDATE observability_rollup_state
SET agent_backfilled_through_id = ?,
    updated_at = CURRENT_TIMESTAMP
WHERE id = 1;

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

-- name: CreateManagementAccessToken :one
INSERT INTO management_access_tokens (name, token_hash, role, enabled, expires_at)
VALUES (?, ?, 'admin', ?, ?)
RETURNING id, name, token_hash, role, enabled, expires_at, last_used_at, created_at, updated_at;

-- name: ListManagementAccessTokens :many
SELECT id, name, token_hash, role, enabled, expires_at, last_used_at, created_at, updated_at
FROM management_access_tokens
ORDER BY name ASC, id ASC;

-- name: GetActiveManagementAccessTokenByHash :one
SELECT id, name, token_hash, role, enabled, expires_at, last_used_at, created_at, updated_at
FROM management_access_tokens
WHERE token_hash = ?
  AND enabled = 1
  AND (expires_at IS NULL OR expires_at > CURRENT_TIMESTAMP);

-- name: TouchManagementAccessToken :exec
UPDATE management_access_tokens
SET last_used_at = CURRENT_TIMESTAMP
WHERE id = ?
  AND (last_used_at IS NULL OR last_used_at < datetime('now', '-30 seconds'));

-- name: DeleteManagementAccessToken :exec
DELETE FROM management_access_tokens
WHERE id = ?;

-- name: ListEnvironments :many
SELECT id, name, management_url, transport, agent_id, access_token,
       trusted_certificate_pem, trusted_certificate_sha256, trusted_certificate_subject, trusted_certificate_not_after,
       last_observed_certificate_pem, last_observed_certificate_sha256,
       response_header_timeout_millis, enabled, last_checked_at, last_error, created_at, updated_at
FROM environments
ORDER BY name ASC, id ASC;

-- name: GetEnvironment :one
SELECT id, name, management_url, transport, agent_id, access_token,
       trusted_certificate_pem, trusted_certificate_sha256, trusted_certificate_subject, trusted_certificate_not_after,
       last_observed_certificate_pem, last_observed_certificate_sha256,
       response_header_timeout_millis, enabled, last_checked_at, last_error, created_at, updated_at
FROM environments
WHERE id = ?;

-- name: CreateEnvironment :one
INSERT INTO environments (
    name, management_url, transport, agent_id, access_token, response_header_timeout_millis, enabled
) VALUES (
    ?, ?, ?, ?, ?, ?, ?
)
RETURNING id, name, management_url, transport, agent_id, access_token,
          trusted_certificate_pem, trusted_certificate_sha256, trusted_certificate_subject, trusted_certificate_not_after,
          last_observed_certificate_pem, last_observed_certificate_sha256,
          response_header_timeout_millis, enabled, last_checked_at, last_error, created_at, updated_at;

-- name: UpdateEnvironment :one
UPDATE environments
SET name = ?,
    management_url = ?,
    transport = ?,
    agent_id = ?,
    access_token = ?,
    response_header_timeout_millis = ?,
    enabled = ?,
    updated_at = CURRENT_TIMESTAMP
WHERE id = ?
RETURNING id, name, management_url, transport, agent_id, access_token,
          trusted_certificate_pem, trusted_certificate_sha256, trusted_certificate_subject, trusted_certificate_not_after,
          last_observed_certificate_pem, last_observed_certificate_sha256,
          response_header_timeout_millis, enabled, last_checked_at, last_error, created_at, updated_at;

-- name: ClearEnvironmentTrust :one
UPDATE environments
SET trusted_certificate_pem = '',
    trusted_certificate_sha256 = '',
    trusted_certificate_subject = '',
    trusted_certificate_not_after = NULL,
    last_observed_certificate_pem = '',
    last_observed_certificate_sha256 = '',
    last_error = '',
    updated_at = CURRENT_TIMESTAMP
WHERE id = ?
RETURNING id, name, management_url, transport, agent_id, access_token,
          trusted_certificate_pem, trusted_certificate_sha256, trusted_certificate_subject, trusted_certificate_not_after,
          last_observed_certificate_pem, last_observed_certificate_sha256,
          response_header_timeout_millis, enabled, last_checked_at, last_error, created_at, updated_at;

-- name: DeleteEnvironment :exec
DELETE FROM environments
WHERE id = ?;

-- name: UpdateEnvironmentObservedCertificate :one
UPDATE environments
SET last_observed_certificate_pem = ?,
    last_observed_certificate_sha256 = ?,
    last_checked_at = CURRENT_TIMESTAMP,
    last_error = ?,
    updated_at = CURRENT_TIMESTAMP
WHERE id = ?
RETURNING id, name, management_url, transport, agent_id, access_token,
          trusted_certificate_pem, trusted_certificate_sha256, trusted_certificate_subject, trusted_certificate_not_after,
          last_observed_certificate_pem, last_observed_certificate_sha256,
          response_header_timeout_millis, enabled, last_checked_at, last_error, created_at, updated_at;

-- name: TrustEnvironmentCertificate :one
UPDATE environments
SET trusted_certificate_pem = last_observed_certificate_pem,
    trusted_certificate_sha256 = last_observed_certificate_sha256,
    trusted_certificate_subject = ?,
    trusted_certificate_not_after = ?,
    last_error = '',
    updated_at = CURRENT_TIMESTAMP
WHERE id = ?
RETURNING id, name, management_url, transport, agent_id, access_token,
          trusted_certificate_pem, trusted_certificate_sha256, trusted_certificate_subject, trusted_certificate_not_after,
          last_observed_certificate_pem, last_observed_certificate_sha256,
          response_header_timeout_millis, enabled, last_checked_at, last_error, created_at, updated_at;

-- name: UpdateEnvironmentCheckResult :one
UPDATE environments
SET last_checked_at = CURRENT_TIMESTAMP,
    last_error = ?,
    updated_at = CURRENT_TIMESTAMP
WHERE id = ?
RETURNING id, name, management_url, transport, agent_id, access_token,
          trusted_certificate_pem, trusted_certificate_sha256, trusted_certificate_subject, trusted_certificate_not_after,
          last_observed_certificate_pem, last_observed_certificate_sha256,
          response_header_timeout_millis, enabled, last_checked_at, last_error, created_at, updated_at;

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

-- name: ListPublicRouteTargets :many
SELECT id, route_id, name, position, priority_group, weight, enabled, target_type, url, transport,
       agent_selector_json, agent_load_balancing, tls_skip_verify, upstream_basic_auth_enabled,
       upstream_basic_auth_username, upstream_basic_auth_password, upstream_response_header_timeout_millis,
       health_check_enabled, health_check_method, health_check_path, health_check_interval_millis,
       health_check_timeout_millis, health_check_healthy_threshold, health_check_unhealthy_threshold,
       health_check_expected_status_min, health_check_expected_status_max, static_status_code,
       static_response_body, static_response_body_mode, static_response_template_id, created_at, updated_at
FROM public_route_targets
ORDER BY route_id ASC, priority_group ASC, position ASC, id ASC;

-- name: ListPublicRouteTargetsByRoute :many
SELECT id, route_id, name, position, priority_group, weight, enabled, target_type, url, transport,
       agent_selector_json, agent_load_balancing, tls_skip_verify, upstream_basic_auth_enabled,
       upstream_basic_auth_username, upstream_basic_auth_password, upstream_response_header_timeout_millis,
       health_check_enabled, health_check_method, health_check_path, health_check_interval_millis,
       health_check_timeout_millis, health_check_healthy_threshold, health_check_unhealthy_threshold,
       health_check_expected_status_min, health_check_expected_status_max, static_status_code,
       static_response_body, static_response_body_mode, static_response_template_id, created_at, updated_at
FROM public_route_targets
WHERE route_id = ?
ORDER BY priority_group ASC, position ASC, id ASC;

-- name: GetPublicRouteTarget :one
SELECT id, route_id, name, position, priority_group, weight, enabled, target_type, url, transport,
       agent_selector_json, agent_load_balancing, tls_skip_verify, upstream_basic_auth_enabled,
       upstream_basic_auth_username, upstream_basic_auth_password, upstream_response_header_timeout_millis,
       health_check_enabled, health_check_method, health_check_path, health_check_interval_millis,
       health_check_timeout_millis, health_check_healthy_threshold, health_check_unhealthy_threshold,
       health_check_expected_status_min, health_check_expected_status_max, static_status_code,
       static_response_body, static_response_body_mode, static_response_template_id, created_at, updated_at
FROM public_route_targets
WHERE id = ?;

-- name: CreatePublicRouteTarget :one
INSERT INTO public_route_targets (
    route_id, name, position, priority_group, weight, enabled, target_type, url, transport,
    agent_selector_json, agent_load_balancing, tls_skip_verify, upstream_basic_auth_enabled,
    upstream_basic_auth_username, upstream_basic_auth_password, upstream_response_header_timeout_millis,
    health_check_enabled, health_check_method, health_check_path, health_check_interval_millis,
    health_check_timeout_millis, health_check_healthy_threshold, health_check_unhealthy_threshold,
    health_check_expected_status_min, health_check_expected_status_max, static_status_code,
    static_response_body, static_response_body_mode, static_response_template_id
) VALUES (
    ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
)
RETURNING id, route_id, name, position, priority_group, weight, enabled, target_type, url, transport,
          agent_selector_json, agent_load_balancing, tls_skip_verify, upstream_basic_auth_enabled,
          upstream_basic_auth_username, upstream_basic_auth_password, upstream_response_header_timeout_millis,
          health_check_enabled, health_check_method, health_check_path, health_check_interval_millis,
          health_check_timeout_millis, health_check_healthy_threshold, health_check_unhealthy_threshold,
          health_check_expected_status_min, health_check_expected_status_max, static_status_code,
          static_response_body, static_response_body_mode, static_response_template_id, created_at, updated_at;

-- name: UpdatePublicRouteTarget :one
UPDATE public_route_targets
SET route_id = ?,
    name = ?,
    position = ?,
    priority_group = ?,
    weight = ?,
    enabled = ?,
    target_type = ?,
    url = ?,
    transport = ?,
    agent_selector_json = ?,
    agent_load_balancing = ?,
    tls_skip_verify = ?,
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
    static_status_code = ?,
    static_response_body = ?,
    static_response_body_mode = ?,
    static_response_template_id = ?,
    updated_at = CURRENT_TIMESTAMP
WHERE id = ?
RETURNING id, route_id, name, position, priority_group, weight, enabled, target_type, url, transport,
          agent_selector_json, agent_load_balancing, tls_skip_verify, upstream_basic_auth_enabled,
          upstream_basic_auth_username, upstream_basic_auth_password, upstream_response_header_timeout_millis,
          health_check_enabled, health_check_method, health_check_path, health_check_interval_millis,
          health_check_timeout_millis, health_check_healthy_threshold, health_check_unhealthy_threshold,
          health_check_expected_status_min, health_check_expected_status_max, static_status_code,
          static_response_body, static_response_body_mode, static_response_template_id, created_at, updated_at;

-- name: DeletePublicRouteTarget :exec
DELETE FROM public_route_targets
WHERE id = ?;

-- name: DeletePublicRouteTargets :exec
DELETE FROM public_route_targets
WHERE route_id = ?;

-- name: ListPublicRouteTargetUpstreamHeaders :many
SELECT id, target_id, position, name, value, sensitive, created_at, updated_at
FROM public_route_target_upstream_headers
ORDER BY target_id ASC, position ASC, id ASC;

-- name: ListPublicRouteTargetUpstreamHeadersByTarget :many
SELECT id, target_id, position, name, value, sensitive, created_at, updated_at
FROM public_route_target_upstream_headers
WHERE target_id = ?
ORDER BY position ASC, id ASC;

-- name: CreatePublicRouteTargetUpstreamHeader :one
INSERT INTO public_route_target_upstream_headers (target_id, position, name, value, sensitive)
VALUES (?, ?, ?, ?, ?)
RETURNING id, target_id, position, name, value, sensitive, created_at, updated_at;

-- name: DeletePublicRouteTargetUpstreamHeaders :exec
DELETE FROM public_route_target_upstream_headers
WHERE target_id = ?;

-- name: ListPublicRouteTargetResponseHeaders :many
SELECT id, target_id, position, name, value, created_at, updated_at
FROM public_route_target_response_headers
ORDER BY target_id ASC, position ASC, id ASC;

-- name: ListPublicRouteTargetResponseHeadersByTarget :many
SELECT id, target_id, position, name, value, created_at, updated_at
FROM public_route_target_response_headers
WHERE target_id = ?
ORDER BY position ASC, id ASC;

-- name: CreatePublicRouteTargetResponseHeader :one
INSERT INTO public_route_target_response_headers (target_id, position, name, value)
VALUES (?, ?, ?, ?)
RETURNING id, target_id, position, name, value, created_at, updated_at;

-- name: DeletePublicRouteTargetResponseHeaders :exec
DELETE FROM public_route_target_response_headers
WHERE target_id = ?;

-- name: CreatePublicListener :one
INSERT INTO public_listeners (name, bind_address, port, protocol, enabled)
VALUES (?, ?, ?, ?, ?)
RETURNING id, name, bind_address, port, protocol, enabled, created_at, updated_at;

-- name: ListPublicListeners :many
SELECT id, name, bind_address, port, protocol, enabled, created_at, updated_at
FROM public_listeners
ORDER BY port ASC, bind_address ASC, id ASC;

-- name: GetPublicListener :one
SELECT id, name, bind_address, port, protocol, enabled, created_at, updated_at
FROM public_listeners
WHERE id = ?;

-- name: UpdatePublicListener :one
UPDATE public_listeners
SET name = ?, bind_address = ?, port = ?, protocol = ?, enabled = ?, updated_at = CURRENT_TIMESTAMP
WHERE id = ?
RETURNING id, name, bind_address, port, protocol, enabled, created_at, updated_at;

-- name: SetPublicListenerEnabled :one
UPDATE public_listeners
SET enabled = ?, updated_at = CURRENT_TIMESTAMP
WHERE id = ?
RETURNING id, name, bind_address, port, protocol, enabled, created_at, updated_at;

-- name: DeletePublicListener :exec
DELETE FROM public_listeners
WHERE id = ?;

-- name: CreatePublicRoute :one
INSERT INTO public_routes (
    listener_id,
    priority,
    host_pattern,
    path_prefix,
    target_load_balancing,
    is_default,
    action,
    redirect_target_mode,
    redirect_target,
    redirect_status_code,
    redirect_preserve_path_suffix,
    redirect_preserve_query,
    enabled
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING id, listener_id, priority, host_pattern, path_prefix, target_load_balancing, is_default, action, redirect_target_mode, redirect_target, redirect_status_code, redirect_preserve_path_suffix, redirect_preserve_query, enabled, created_at, updated_at;

-- name: ListPublicRoutes :many
SELECT id, listener_id, priority, host_pattern, path_prefix, target_load_balancing, is_default, action, redirect_target_mode, redirect_target, redirect_status_code, redirect_preserve_path_suffix, redirect_preserve_query, enabled, created_at, updated_at
FROM public_routes
ORDER BY listener_id ASC, priority ASC, id ASC;

-- name: GetPublicRoute :one
SELECT id, listener_id, priority, host_pattern, path_prefix, target_load_balancing, is_default, action, redirect_target_mode, redirect_target, redirect_status_code, redirect_preserve_path_suffix, redirect_preserve_query, enabled, created_at, updated_at
FROM public_routes
WHERE id = ?;

-- name: UpdatePublicRoute :one
UPDATE public_routes
SET listener_id = ?,
    priority = ?,
    host_pattern = ?,
    path_prefix = ?,
    target_load_balancing = ?,
    is_default = ?,
    action = ?,
    redirect_target_mode = ?,
    redirect_target = ?,
    redirect_status_code = ?,
    redirect_preserve_path_suffix = ?,
    redirect_preserve_query = ?,
    enabled = ?,
    updated_at = CURRENT_TIMESTAMP
WHERE id = ?
RETURNING id, listener_id, priority, host_pattern, path_prefix, target_load_balancing, is_default, action, redirect_target_mode, redirect_target, redirect_status_code, redirect_preserve_path_suffix, redirect_preserve_query, enabled, created_at, updated_at;

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

-- name: UpdatePublicTlsCertificateRenewalStatus :one
UPDATE public_tls_certificates
SET status = ?,
    last_error = ?,
    next_renewal_at = ?,
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
       trigger_route_target_active_requests, trigger_agent_active_requests, trigger_server_cpu_percent, trigger_agent_cpu_percent,
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
       trigger_route_target_active_requests, trigger_agent_active_requests, trigger_server_cpu_percent, trigger_agent_cpu_percent,
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
    trigger_route_target_active_requests,
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
          trigger_route_target_active_requests, trigger_agent_active_requests, trigger_server_cpu_percent, trigger_agent_cpu_percent,
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
    trigger_route_target_active_requests = ?,
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
          trigger_route_target_active_requests, trigger_agent_active_requests, trigger_server_cpu_percent, trigger_agent_cpu_percent,
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
SELECT id, name, priority, enabled, match_json, route_ids_json, target_ids_json, scope, ttl_mode, ttl_millis,
       query_mode, query_params_json, vary_headers_json, cache_status_codes_json, max_object_bytes,
       add_cache_status_header, allow_cookie_requests, created_at, updated_at
FROM public_cache_rules
ORDER BY priority ASC, id ASC;

-- name: GetPublicCacheRule :one
SELECT id, name, priority, enabled, match_json, route_ids_json, target_ids_json, scope, ttl_mode, ttl_millis,
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
    target_ids_json,
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
RETURNING id, name, priority, enabled, match_json, route_ids_json, target_ids_json, scope, ttl_mode, ttl_millis,
          query_mode, query_params_json, vary_headers_json, cache_status_codes_json, max_object_bytes,
          add_cache_status_header, allow_cookie_requests, created_at, updated_at;

-- name: UpdatePublicCacheRule :one
UPDATE public_cache_rules
SET name = ?,
    priority = ?,
    enabled = ?,
    match_json = ?,
    route_ids_json = ?,
    target_ids_json = ?,
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
RETURNING id, name, priority, enabled, match_json, route_ids_json, target_ids_json, scope, ttl_mode, ttl_millis,
          query_mode, query_params_json, vary_headers_json, cache_status_codes_json, max_object_bytes,
          add_cache_status_header, allow_cookie_requests, created_at, updated_at;

-- name: DeletePublicCacheRule :exec
DELETE FROM public_cache_rules
WHERE id = ?;

-- name: GetPublicCacheEntry :one
SELECT key_digest, rule_id, scope, listener_protocol, host, path, query_key, route_id, route_target_id, method,
       vary_headers_json, response_headers_json, status_code, body_path, size_bytes, stored_at, expires_at,
       last_accessed_at, hit_count
FROM public_cache_entries
WHERE key_digest = ?;

-- name: ListPublicCacheEntryCandidates :many
SELECT key_digest, rule_id, scope, listener_protocol, host, path, query_key, route_id, route_target_id, method,
       vary_headers_json, response_headers_json, status_code, body_path, size_bytes, stored_at, expires_at,
       last_accessed_at, hit_count
FROM public_cache_entries
WHERE rule_id = ?
  AND listener_protocol = ?
  AND host = ?
  AND path = ?
  AND query_key = ?
  AND COALESCE(route_id, 0) = ?
  AND COALESCE(route_target_id, 0) = ?
  AND expires_at > ?
ORDER BY stored_at DESC
LIMIT 20;

-- name: UpsertPublicCacheEntry :one
INSERT INTO public_cache_entries (
    key_digest, rule_id, scope, listener_protocol, host, path, query_key, route_id, route_target_id, method,
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
    route_target_id = excluded.route_target_id,
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
RETURNING key_digest, rule_id, scope, listener_protocol, host, path, query_key, route_id, route_target_id, method,
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
