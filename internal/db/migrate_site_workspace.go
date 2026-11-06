package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

func applySiteWorkspaceFoundation(ctx context.Context, database *sql.DB) error {
	return applySiteWorkspaceFoundationWithHook(ctx, database, nil)
}

func applySiteWorkspaceFoundationWithHook(ctx context.Context, database *sql.DB, beforeCommit func() error) (retErr error) {
	conn, err := database.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire site workspace migration connection: %w", err)
	}
	defer conn.Close()

	alreadyApplied, partial, err := siteWorkspaceFoundationState(ctx, conn)
	if err != nil {
		return fmt.Errorf("inspect site workspace schema: %w", err)
	}
	if partial {
		return errors.New("site workspace schema is partially applied; refusing a destructive retry")
	}
	if alreadyApplied {
		return verifyForeignKeysOnConn(ctx, conn)
	}

	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		return fmt.Errorf("disable foreign keys for site workspace migration: %w", err)
	}
	defer func() {
		if _, err := conn.ExecContext(context.Background(), `PRAGMA foreign_keys = ON`); err != nil {
			retErr = errors.Join(retErr, fmt.Errorf("restore foreign key enforcement: %w", err))
		}
	}()
	var foreignKeysEnabled bool
	if err := conn.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&foreignKeysEnabled); err != nil {
		return fmt.Errorf("verify disabled foreign keys: %w", err)
	}
	if foreignKeysEnabled {
		return errors.New("foreign keys remained enabled for site workspace migration")
	}

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin site workspace migration: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, siteWorkspaceFoundationSQL); err != nil {
		return fmt.Errorf("rebuild site workspace schema: %w", err)
	}
	if beforeCommit != nil {
		if err := beforeCommit(); err != nil {
			return fmt.Errorf("site workspace migration pre-commit check: %w", err)
		}
	}
	if err := verifyForeignKeysOnTx(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit site workspace migration: %w", err)
	}
	return nil
}

func siteWorkspaceFoundationState(ctx context.Context, conn *sql.Conn) (complete bool, partial bool, err error) {
	checks := []struct {
		table  string
		column string
	}{
		{table: "public_sites", column: "published"},
		{table: "public_sites", column: "default_site"},
		{table: "public_sites", column: "canonical_hostname"},
		{table: "public_site_listener_bindings", column: "behavior"},
		{table: "public_site_migration_route_mappings", column: "destination_route_id"},
		{table: "public_site_migration_target_mappings", column: "destination_target_id"},
		{table: "public_site_migrated_listeners", column: "listener_id"},
	}
	found := 0
	for _, check := range checks {
		var exists bool
		if err := conn.QueryRowContext(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM pragma_table_info(?) WHERE name = ?
			)
		`, check.table, check.column).Scan(&exists); err != nil {
			return false, false, err
		}
		if exists {
			found++
		}
	}
	return found == len(checks), found != 0 && found != len(checks), nil
}

func verifyForeignKeysOnConn(ctx context.Context, conn *sql.Conn) error {
	rows, err := conn.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return fmt.Errorf("check site workspace foreign keys: %w", err)
	}
	defer rows.Close()
	if rows.Next() {
		return errors.New("site workspace schema has a foreign key violation")
	}
	return rows.Err()
}

func verifyForeignKeysOnTx(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return fmt.Errorf("check rebuilt site workspace foreign keys: %w", err)
	}
	defer rows.Close()
	if rows.Next() {
		var table string
		var rowID sql.NullInt64
		var parent string
		var fkID int64
		if err := rows.Scan(&table, &rowID, &parent, &fkID); err != nil {
			return fmt.Errorf("read rebuilt site workspace foreign key violation: %w", err)
		}
		return fmt.Errorf("rebuilt site workspace foreign key violation: table=%s row=%v parent=%s fk=%d", table, rowID, parent, fkID)
	}
	return rows.Err()
}

const siteWorkspaceFoundationSQL = `
CREATE TABLE public_sites_new (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL,
    enabled INTEGER NOT NULL DEFAULT 1,
    published INTEGER NOT NULL DEFAULT 0,
    default_site INTEGER NOT NULL DEFAULT 0,
    canonical_hostname TEXT NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

INSERT INTO public_sites_new (
    id, name, enabled, published, default_site, canonical_hostname, created_at, updated_at
)
SELECT
    s.id, s.name, s.enabled, 1, 0,
    COALESCE((
        SELECT h.hostname_pattern FROM public_site_hosts h
        WHERE h.site_id = s.id AND h.role = 'primary'
        ORDER BY h.id LIMIT 1
    ), ''),
    s.created_at, s.updated_at
FROM public_sites s;

CREATE TABLE public_site_listener_bindings (
    site_id INTEGER NOT NULL REFERENCES public_sites_new(id) ON DELETE CASCADE,
    listener_id INTEGER NOT NULL REFERENCES public_listeners(id) ON DELETE CASCADE,
    behavior TEXT NOT NULL DEFAULT 'serve' CHECK (behavior IN ('serve', 'redirect_https')),
    redirect_listener_id INTEGER REFERENCES public_listeners(id) ON DELETE RESTRICT,
    redirect_hostname TEXT NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (site_id, listener_id)
);

INSERT INTO public_site_listener_bindings (site_id, listener_id, behavior, created_at, updated_at)
SELECT id, listener_id, 'serve', created_at, updated_at FROM public_sites;

CREATE TABLE public_site_hosts_new (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    site_id INTEGER NOT NULL REFERENCES public_sites_new(id) ON DELETE CASCADE,
    hostname_pattern TEXT COLLATE NOCASE NOT NULL CHECK (
        hostname_pattern = lower(trim(hostname_pattern)) AND hostname_pattern NOT LIKE '%.'
    ),
    role TEXT NOT NULL CHECK (role IN ('primary', 'alias')),
    behavior TEXT NOT NULL CHECK (behavior IN ('serve', 'redirect')),
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(site_id, hostname_pattern),
    CHECK (role != 'primary' OR behavior = 'serve'),
    CHECK (role != 'primary' OR hostname_pattern NOT LIKE '*.%')
);

INSERT INTO public_site_hosts_new (
    id, site_id, hostname_pattern, role, behavior, created_at, updated_at
)
SELECT id, site_id, hostname_pattern, role, behavior, created_at, updated_at
FROM public_site_hosts;

CREATE TABLE public_routes_new (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    listener_id INTEGER NOT NULL DEFAULT 0,
    priority INTEGER NOT NULL,
    host_pattern TEXT NOT NULL DEFAULT '',
    path_prefix TEXT NOT NULL DEFAULT '',
    target_load_balancing TEXT NOT NULL DEFAULT 'round_robin',
    is_default INTEGER NOT NULL DEFAULT 0,
    action TEXT NOT NULL DEFAULT 'forward',
    redirect_target_mode TEXT NOT NULL DEFAULT '',
    redirect_target TEXT NOT NULL DEFAULT '',
    redirect_status_code INTEGER NOT NULL DEFAULT 302,
    redirect_preserve_path_suffix INTEGER NOT NULL DEFAULT 1,
    redirect_preserve_query INTEGER NOT NULL DEFAULT 1,
    path_security_mode TEXT NOT NULL DEFAULT 'strict',
    access_policy_id INTEGER REFERENCES public_access_policies(id) ON DELETE RESTRICT,
    enabled INTEGER NOT NULL DEFAULT 1,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    site_id INTEGER REFERENCES public_sites_new(id) ON DELETE RESTRICT,
    CHECK ((site_id IS NULL AND listener_id > 0) OR (site_id IS NOT NULL AND listener_id = 0))
);

INSERT INTO public_routes_new (
    id, listener_id, priority, host_pattern, path_prefix, target_load_balancing,
    is_default, action, redirect_target_mode, redirect_target,
    redirect_status_code, redirect_preserve_path_suffix, redirect_preserve_query,
    path_security_mode, access_policy_id, enabled, created_at, updated_at, site_id
)
SELECT
    id, CASE WHEN site_id IS NULL THEN listener_id ELSE 0 END,
    priority, host_pattern, path_prefix, target_load_balancing, is_default, action,
    redirect_target_mode, redirect_target, redirect_status_code,
    redirect_preserve_path_suffix, redirect_preserve_query, path_security_mode,
    access_policy_id, enabled, created_at, updated_at, site_id
FROM public_routes;

DROP TRIGGER IF EXISTS trg_public_routes_site_listener_insert;
DROP TRIGGER IF EXISTS trg_public_routes_site_listener_update;
DROP TRIGGER IF EXISTS trg_public_listener_delete_site_routes;
DROP TABLE public_routes;
DROP TABLE public_site_hosts;
DROP TABLE public_sites;

ALTER TABLE public_sites_new RENAME TO public_sites;
ALTER TABLE public_site_hosts_new RENAME TO public_site_hosts;
ALTER TABLE public_routes_new RENAME TO public_routes;

CREATE INDEX idx_public_routes_listener_priority ON public_routes (listener_id, priority, id);
CREATE INDEX idx_public_routes_site_priority ON public_routes (site_id, priority, id) WHERE site_id IS NOT NULL;
CREATE INDEX idx_public_routes_access_policy_id ON public_routes (access_policy_id);
CREATE INDEX idx_public_sites_name ON public_sites (name, id);
CREATE INDEX idx_public_site_listener_bindings_listener ON public_site_listener_bindings (listener_id, site_id);
CREATE INDEX idx_public_site_listener_bindings_redirect ON public_site_listener_bindings (redirect_listener_id, site_id) WHERE redirect_listener_id IS NOT NULL;
CREATE UNIQUE INDEX idx_public_site_hosts_one_primary ON public_site_hosts (site_id) WHERE role = 'primary';
CREATE INDEX idx_public_site_hosts_site ON public_site_hosts (site_id, role, id);
CREATE UNIQUE INDEX idx_public_routes_one_default_per_listener ON public_routes (listener_id) WHERE is_default = 1 AND site_id IS NULL;
CREATE UNIQUE INDEX idx_public_routes_one_default_per_site ON public_routes (site_id) WHERE is_default = 1 AND site_id IS NOT NULL;

CREATE TRIGGER trg_public_routes_legacy_listener_insert
BEFORE INSERT ON public_routes
WHEN NEW.site_id IS NULL AND NOT EXISTS (
    SELECT 1 FROM public_listeners l WHERE l.id = NEW.listener_id
)
BEGIN
    SELECT RAISE(ABORT, 'standalone route listener does not exist');
END;

CREATE TRIGGER trg_public_routes_legacy_listener_update
BEFORE UPDATE OF site_id, listener_id ON public_routes
WHEN NEW.site_id IS NULL AND NOT EXISTS (
    SELECT 1 FROM public_listeners l WHERE l.id = NEW.listener_id
)
BEGIN
    SELECT RAISE(ABORT, 'standalone route listener does not exist');
END;

CREATE TRIGGER trg_public_listener_delete_standalone_routes
BEFORE DELETE ON public_listeners
BEGIN
    DELETE FROM public_routes WHERE listener_id = OLD.id AND site_id IS NULL;
END;

CREATE TABLE public_site_migration_route_mappings (
    source_route_id INTEGER NOT NULL,
    destination_site_id INTEGER NOT NULL REFERENCES public_sites(id) ON DELETE CASCADE,
    destination_route_id INTEGER NOT NULL REFERENCES public_routes(id) ON DELETE CASCADE,
    copied INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(source_route_id, destination_site_id)
);

CREATE TABLE public_site_migration_target_mappings (
    source_target_id INTEGER NOT NULL,
    destination_route_id INTEGER NOT NULL REFERENCES public_routes(id) ON DELETE CASCADE,
    destination_target_id INTEGER NOT NULL REFERENCES public_route_targets(id) ON DELETE CASCADE,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(source_target_id, destination_route_id)
);

CREATE TABLE public_site_migrated_listeners (
    listener_id INTEGER PRIMARY KEY REFERENCES public_listeners(id) ON DELETE CASCADE,
    migrated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TRIGGER trg_public_site_migrated_listeners_no_standalone_insert
BEFORE INSERT ON public_site_migrated_listeners
WHEN EXISTS (
    SELECT 1 FROM public_routes r WHERE r.listener_id = NEW.listener_id AND r.site_id IS NULL
)
BEGIN
    SELECT RAISE(ABORT, 'listener still has standalone routes');
END;

CREATE TRIGGER trg_public_site_migrated_listeners_no_standalone_update
BEFORE UPDATE OF listener_id ON public_site_migrated_listeners
WHEN EXISTS (
    SELECT 1 FROM public_routes r WHERE r.listener_id = NEW.listener_id AND r.site_id IS NULL
)
BEGIN
    SELECT RAISE(ABORT, 'listener still has standalone routes');
END;

CREATE TRIGGER trg_public_routes_migrated_listener_insert
BEFORE INSERT ON public_routes
WHEN NEW.site_id IS NULL AND EXISTS (
    SELECT 1 FROM public_site_migrated_listeners m WHERE m.listener_id = NEW.listener_id
)
BEGIN
    SELECT RAISE(ABORT, 'listener requires Site-owned routes');
END;

CREATE TRIGGER trg_public_routes_migrated_listener_update
BEFORE UPDATE OF site_id, listener_id ON public_routes
WHEN NEW.site_id IS NULL AND EXISTS (
    SELECT 1 FROM public_site_migrated_listeners m WHERE m.listener_id = NEW.listener_id
)
BEGIN
    SELECT RAISE(ABORT, 'listener requires Site-owned routes');
END;
`

func rejectSiteWorkspaceFoundationDown(context.Context, *sql.DB) error {
	return errors.New("downgrading the Site workspace schema is unsupported; restore a database backup instead")
}
