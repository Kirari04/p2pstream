-- +goose Up
CREATE TABLE IF NOT EXISTS public_sites (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    listener_id INTEGER NOT NULL REFERENCES public_listeners(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    enabled INTEGER NOT NULL DEFAULT 1,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(listener_id, name),
    UNIQUE(id, listener_id)
);

CREATE TABLE IF NOT EXISTS public_site_hosts (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    site_id INTEGER NOT NULL,
    listener_id INTEGER NOT NULL,
    hostname_pattern TEXT COLLATE NOCASE NOT NULL CHECK (
        hostname_pattern = lower(trim(hostname_pattern)) AND hostname_pattern NOT LIKE '%.'
    ),
    role TEXT NOT NULL CHECK (role IN ('primary', 'alias')),
    behavior TEXT NOT NULL CHECK (behavior IN ('serve', 'redirect')),
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(listener_id, hostname_pattern),
    CHECK (role != 'primary' OR behavior = 'serve'),
    FOREIGN KEY (site_id, listener_id) REFERENCES public_sites(id, listener_id) ON DELETE CASCADE
);

ALTER TABLE public_routes ADD COLUMN site_id INTEGER REFERENCES public_sites(id) ON DELETE RESTRICT;

CREATE INDEX IF NOT EXISTS idx_public_routes_site_priority
ON public_routes (site_id, priority, id)
WHERE site_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_public_sites_listener
ON public_sites (listener_id, name, id);

CREATE UNIQUE INDEX IF NOT EXISTS idx_public_site_hosts_one_primary
ON public_site_hosts (site_id)
WHERE role = 'primary';

CREATE INDEX IF NOT EXISTS idx_public_site_hosts_site
ON public_site_hosts (site_id, role, id);

DROP INDEX IF EXISTS idx_public_routes_one_default_per_listener;
CREATE UNIQUE INDEX idx_public_routes_one_default_per_listener
ON public_routes (listener_id)
WHERE is_default = 1 AND site_id IS NULL;

CREATE UNIQUE INDEX idx_public_routes_one_default_per_site
ON public_routes (site_id)
WHERE is_default = 1 AND site_id IS NOT NULL;

-- +goose StatementBegin
CREATE TRIGGER IF NOT EXISTS trg_public_routes_site_listener_insert
BEFORE INSERT ON public_routes
WHEN NEW.site_id IS NOT NULL AND NOT EXISTS (
    SELECT 1 FROM public_sites s WHERE s.id = NEW.site_id AND s.listener_id = NEW.listener_id
)
BEGIN
    SELECT RAISE(ABORT, 'route site and listener must agree');
END;
-- +goose StatementEnd

-- SQLite may process the site cascade before the listener's route cascade.
-- Remove site-owned routes first so route.site_id RESTRICT cannot block a
-- deliberate listener deletion.
-- +goose StatementBegin
CREATE TRIGGER IF NOT EXISTS trg_public_listener_delete_site_routes
BEFORE DELETE ON public_listeners
BEGIN
    DELETE FROM public_routes WHERE listener_id = OLD.id AND site_id IS NOT NULL;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER IF NOT EXISTS trg_public_routes_site_listener_update
BEFORE UPDATE OF site_id, listener_id ON public_routes
WHEN NEW.site_id IS NOT NULL AND NOT EXISTS (
    SELECT 1 FROM public_sites s WHERE s.id = NEW.site_id AND s.listener_id = NEW.listener_id
)
BEGIN
    SELECT RAISE(ABORT, 'route site and listener must agree');
END;
-- +goose StatementEnd

-- Existing routes deliberately remain unbound. Site routing is additive, so this
-- migration cannot change the winner, authorization, or cache scope of any
-- previously valid request.

-- +goose Down
-- Runtime downgrades are intentionally rejected: rebuilding public_routes can
-- destroy bindings and make site routes hostless under an older binary.
CREATE TEMP TABLE public_sites_downgrade_guard (message TEXT);
-- +goose StatementBegin
CREATE TEMP TRIGGER public_sites_downgrade_is_unsupported
BEFORE INSERT ON public_sites_downgrade_guard
BEGIN
    SELECT RAISE(ABORT, 'public sites migration cannot be downgraded safely');
END;
-- +goose StatementEnd
INSERT INTO public_sites_downgrade_guard VALUES ('abort');
