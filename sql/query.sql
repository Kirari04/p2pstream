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
    memory_mb, goroutines, req_success, req_client_error, req_server_error, bytes_rx, bytes_tx
) VALUES (
    ?, ?, ?, ?, ?, ?, ?
);

-- name: GetLatestAgentStat :one
SELECT * FROM agent_stats
ORDER BY id DESC
LIMIT 1;
