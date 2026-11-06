ALTER TABLE public_cache_rules DROP COLUMN allow_cookie_requests;

CREATE TEMP TABLE public_cache_entries_cutover AS
SELECT key_digest, rule_id, scope, listener_protocol, host, path, query_key, route_id, route_target_id, method, vary_headers_json, response_headers_json, status_code, body_path, size_bytes, CASE WHEN length(stored_at) = 19 THEN stored_at || '+00:00' ELSE stored_at END AS stored_at, CASE WHEN length(expires_at) = 19 THEN expires_at || '+00:00' ELSE expires_at END AS expires_at, CASE WHEN length(last_accessed_at) = 19 THEN last_accessed_at || '+00:00' ELSE last_accessed_at END AS last_accessed_at, hit_count FROM public_cache_entries;
DROP TABLE public_cache_entries;

CREATE TABLE IF NOT EXISTS public_cache_entries (
    key_digest TEXT PRIMARY KEY,
    rule_id INTEGER NOT NULL REFERENCES public_cache_rules(id) ON DELETE CASCADE,
    scope TEXT NOT NULL,
    listener_protocol TEXT NOT NULL,
    host TEXT NOT NULL,
    path TEXT NOT NULL,
    query_key TEXT NOT NULL,
    route_id INTEGER,
    route_target_id INTEGER,
    method TEXT NOT NULL DEFAULT 'GET',
    vary_headers_json TEXT NOT NULL DEFAULT '[]',
    response_headers_json TEXT NOT NULL DEFAULT '[]',
    status_code INTEGER NOT NULL,
    body_path TEXT NOT NULL,
    size_bytes INTEGER NOT NULL,
    stored_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%S+00:00', 'now')),
    expires_at DATETIME NOT NULL,
    last_accessed_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%S+00:00', 'now')),
    hit_count INTEGER NOT NULL DEFAULT 0
);

INSERT INTO public_cache_entries (key_digest, rule_id, scope, listener_protocol, host, path, query_key, route_id, route_target_id, method, vary_headers_json, response_headers_json, status_code, body_path, size_bytes, stored_at, expires_at, last_accessed_at, hit_count)
SELECT key_digest, rule_id, scope, listener_protocol, host, path, query_key, route_id, route_target_id, method, vary_headers_json, response_headers_json, status_code, body_path, size_bytes, stored_at, expires_at, last_accessed_at, hit_count FROM public_cache_entries_cutover;
DROP TABLE public_cache_entries_cutover;
CREATE INDEX IF NOT EXISTS idx_public_cache_entries_rule_id
ON public_cache_entries (rule_id);
CREATE INDEX IF NOT EXISTS idx_public_cache_entries_route_target_id
ON public_cache_entries (route_target_id);
CREATE INDEX IF NOT EXISTS idx_public_cache_entries_expires_at
ON public_cache_entries (expires_at);
CREATE INDEX IF NOT EXISTS idx_public_cache_entries_last_accessed_at
ON public_cache_entries (last_accessed_at);
DROP TABLE observability_rollup_state;
