-- +goose Up
-- Keep every non-NULL ID, including zero and negative historical values.

DROP INDEX IF EXISTS idx_proxy_request_events_listener_id;
CREATE INDEX idx_proxy_request_events_listener_id
ON proxy_request_events (listener_id)
WHERE listener_id IS NOT NULL;

DROP INDEX IF EXISTS idx_proxy_request_events_route_id;
CREATE INDEX idx_proxy_request_events_route_id
ON proxy_request_events (route_id)
WHERE route_id IS NOT NULL;

DROP INDEX IF EXISTS idx_proxy_request_events_route_target_id;
CREATE INDEX idx_proxy_request_events_route_target_id
ON proxy_request_events (route_target_id)
WHERE route_target_id IS NOT NULL;

DROP INDEX IF EXISTS idx_proxy_request_events_agent_id;
CREATE INDEX idx_proxy_request_events_agent_id
ON proxy_request_events (agent_id)
WHERE agent_id IS NOT NULL;

DROP INDEX IF EXISTS idx_proxy_request_events_waf_rule_id;
CREATE INDEX idx_proxy_request_events_waf_rule_id
ON proxy_request_events (waf_rule_id)
WHERE waf_rule_id IS NOT NULL;

DROP INDEX IF EXISTS idx_proxy_request_events_cache_rule_id;
CREATE INDEX idx_proxy_request_events_cache_rule_id
ON proxy_request_events (cache_rule_id)
WHERE cache_rule_id IS NOT NULL;

DROP INDEX IF EXISTS idx_proxy_request_events_retry_rule_id;
CREATE INDEX idx_proxy_request_events_retry_rule_id
ON proxy_request_events (retry_rule_id)
WHERE retry_rule_id IS NOT NULL;

-- +goose Down
-- Restore the original full indexes without changing event data.

DROP INDEX IF EXISTS idx_proxy_request_events_listener_id;
CREATE INDEX idx_proxy_request_events_listener_id
ON proxy_request_events (listener_id);

DROP INDEX IF EXISTS idx_proxy_request_events_route_id;
CREATE INDEX idx_proxy_request_events_route_id
ON proxy_request_events (route_id);

DROP INDEX IF EXISTS idx_proxy_request_events_route_target_id;
CREATE INDEX idx_proxy_request_events_route_target_id
ON proxy_request_events (route_target_id);

DROP INDEX IF EXISTS idx_proxy_request_events_agent_id;
CREATE INDEX idx_proxy_request_events_agent_id
ON proxy_request_events (agent_id);

DROP INDEX IF EXISTS idx_proxy_request_events_waf_rule_id;
CREATE INDEX idx_proxy_request_events_waf_rule_id
ON proxy_request_events (waf_rule_id);

DROP INDEX IF EXISTS idx_proxy_request_events_cache_rule_id;
CREATE INDEX idx_proxy_request_events_cache_rule_id
ON proxy_request_events (cache_rule_id);

DROP INDEX IF EXISTS idx_proxy_request_events_retry_rule_id;
CREATE INDEX idx_proxy_request_events_retry_rule_id
ON proxy_request_events (retry_rule_id);
