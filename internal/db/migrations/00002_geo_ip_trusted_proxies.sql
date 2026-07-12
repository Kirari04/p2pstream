-- +goose Up
-- Databases predating the WAF are adopted at the baseline version before the
-- legacy compatibility pass runs, so make the ALTER target self-contained.
CREATE TABLE IF NOT EXISTS public_waf_rules (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL UNIQUE,
    priority INTEGER NOT NULL DEFAULT 100,
    enabled INTEGER NOT NULL DEFAULT 1,
    action TEXT NOT NULL DEFAULT 'block',
    activation_mode TEXT NOT NULL DEFAULT 'always',
    match_json TEXT NOT NULL DEFAULT '{}',
    key_parts_json TEXT NOT NULL DEFAULT '[]',
    captcha_provider_id INTEGER REFERENCES public_waf_captcha_providers(id),
    captcha_pass_ttl_millis INTEGER NOT NULL DEFAULT 1800000,
    waiting_room_max_admitted_sessions INTEGER NOT NULL DEFAULT 50,
    waiting_room_admission_rate_per_second INTEGER NOT NULL DEFAULT 10,
    waiting_room_admission_session_ttl_millis INTEGER NOT NULL DEFAULT 600000,
    waiting_room_queue_poll_interval_millis INTEGER NOT NULL DEFAULT 5000,
    waiting_room_queue_timeout_millis INTEGER NOT NULL DEFAULT 1800000,
    waiting_room_page_title TEXT NOT NULL DEFAULT 'Waiting room',
    waiting_room_page_body TEXT NOT NULL DEFAULT 'Traffic is high. You will be admitted automatically.',
    trigger_request_window_millis INTEGER NOT NULL DEFAULT 10000,
    trigger_minimum_request_rate INTEGER NOT NULL DEFAULT 50,
    trigger_traffic_spike_multiplier REAL NOT NULL DEFAULT 4,
    trigger_proxy_active_requests INTEGER NOT NULL DEFAULT 100,
    trigger_route_target_active_requests INTEGER NOT NULL DEFAULT 100,
    trigger_agent_active_requests INTEGER NOT NULL DEFAULT 50,
    trigger_server_cpu_percent REAL NOT NULL DEFAULT 85,
    trigger_agent_cpu_percent REAL NOT NULL DEFAULT 85,
    trigger_minimum_active_millis INTEGER NOT NULL DEFAULT 30000,
    trigger_quiet_period_millis INTEGER NOT NULL DEFAULT 60000,
    block_response_status_code INTEGER NOT NULL DEFAULT 403,
    block_response_body TEXT NOT NULL DEFAULT 'Request blocked',
    block_response_body_mode TEXT NOT NULL DEFAULT 'inline',
    block_response_template_id INTEGER REFERENCES public_response_templates(id) ON DELETE RESTRICT,
    captcha_page_template_id INTEGER REFERENCES public_response_templates(id) ON DELETE RESTRICT,
    waiting_room_page_template_id INTEGER REFERENCES public_response_templates(id) ON DELETE RESTRICT,
    block_response_content_type TEXT NOT NULL DEFAULT 'text/plain; charset=utf-8',
    block_response_headers_json TEXT NOT NULL DEFAULT '[]',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

ALTER TABLE public_waf_rules ADD COLUMN geo_mode TEXT NOT NULL DEFAULT 'disabled';
ALTER TABLE public_waf_rules ADD COLUMN geo_country_codes_json TEXT NOT NULL DEFAULT '[]';
ALTER TABLE public_waf_rules ADD COLUMN geo_unknown_behavior TEXT NOT NULL DEFAULT 'apply_rule';

CREATE TABLE IF NOT EXISTS public_geo_ip_settings (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    enabled INTEGER NOT NULL DEFAULT 0,
    maxmind_account_id TEXT NOT NULL DEFAULT '',
    maxmind_license_key TEXT NOT NULL DEFAULT '',
    database_type TEXT NOT NULL DEFAULT '',
    database_build_at DATETIME,
    last_update_attempt_at DATETIME,
    last_update_success_at DATETIME,
    last_update_error TEXT NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

INSERT INTO public_geo_ip_settings (id)
SELECT 1
WHERE NOT EXISTS (SELECT 1 FROM public_geo_ip_settings WHERE id = 1);

CREATE TABLE IF NOT EXISTS public_trusted_proxy_sources (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL UNIQUE,
    provider TEXT NOT NULL DEFAULT 'custom',
    built_in INTEGER NOT NULL DEFAULT 0,
    enabled INTEGER NOT NULL DEFAULT 0,
    cidrs_json TEXT NOT NULL DEFAULT '[]',
    header_name TEXT NOT NULL,
    header_mode TEXT NOT NULL DEFAULT 'single_ip',
    last_refresh_attempt_at DATETIME,
    last_refresh_success_at DATETIME,
    last_refresh_error TEXT NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

INSERT INTO public_trusted_proxy_sources (name, provider, built_in, enabled, cidrs_json, header_name, header_mode)
SELECT 'Cloudflare', 'cloudflare', 1, 0, '[]', 'CF-Connecting-IP', 'single_ip'
WHERE NOT EXISTS (SELECT 1 FROM public_trusted_proxy_sources WHERE provider = 'cloudflare' AND built_in = 1);

INSERT INTO public_trusted_proxy_sources (name, provider, built_in, enabled, cidrs_json, header_name, header_mode)
SELECT 'Bunny', 'bunny', 1, 0, '[]', 'X-Real-IP', 'single_ip'
WHERE NOT EXISTS (SELECT 1 FROM public_trusted_proxy_sources WHERE provider = 'bunny' AND built_in = 1);

INSERT INTO public_trusted_proxy_sources (name, provider, built_in, enabled, cidrs_json, header_name, header_mode)
SELECT 'CloudFront', 'cloudfront', 1, 0, '[]', 'X-Forwarded-For', 'trusted_chain'
WHERE NOT EXISTS (SELECT 1 FROM public_trusted_proxy_sources WHERE provider = 'cloudfront' AND built_in = 1);

CREATE UNIQUE INDEX IF NOT EXISTS idx_public_trusted_proxy_sources_builtin_provider
ON public_trusted_proxy_sources (provider)
WHERE built_in = 1;

-- +goose Down
-- This migration is intentionally non-destructive. Runtime downgrades are not supported.
SELECT 1;
