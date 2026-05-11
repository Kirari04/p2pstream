package db

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	_ "github.com/mattn/go-sqlite3" // sqlite driver
	"github.com/rs/zerolog/log"
)

type DB struct {
	*sql.DB
	*Queries
}

// Open initializes the SQLite database connection with high performance pragmas and runs schema migrations.
func Open(databaseURL string) (*DB, error) {
	log.Info().Str("url", databaseURL).Msg("Connecting to SQLite database")

	dsn, err := normalizeSQLiteDSN(databaseURL)
	if err != nil {
		return nil, err
	}
	if err := ensureSQLiteDir(dsn); err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(8)
	db.SetConnMaxLifetime(0)

	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	if _, err := db.ExecContext(context.Background(), `
		PRAGMA journal_mode = WAL;
		PRAGMA synchronous = NORMAL;
		PRAGMA busy_timeout = 10000;
		PRAGMA foreign_keys = ON;
		PRAGMA wal_autocheckpoint = 1000;
	`); err != nil {
		return nil, fmt.Errorf("failed to configure SQLite pragmas: %w", err)
	}

	instance := &DB{
		DB:      db,
		Queries: New(db),
	}

	if err := instance.migrate(); err != nil {
		return nil, fmt.Errorf("failed to run migrations: %w", err)
	}

	// Enforce pragmas for any connections opened after migration.
	if _, err := db.ExecContext(context.Background(), `
		PRAGMA journal_mode = WAL;
		PRAGMA synchronous = NORMAL;
		PRAGMA busy_timeout = 10000;
		PRAGMA foreign_keys = ON;
		PRAGMA wal_autocheckpoint = 1000;
	`); err != nil {
		log.Warn().Err(err).Msg("Failed to enforce some SQLite pragmas")
	}

	log.Info().Msg("Database connected and configured successfully")
	return instance, nil
}

func normalizeSQLiteDSN(databaseURL string) (string, error) {
	databaseURL = strings.TrimSpace(databaseURL)
	if databaseURL == "" {
		databaseURL = "file:p2pstream.db?mode=rwc"
	}

	if strings.HasPrefix(databaseURL, "file:") {
		prefix, rawQuery, _ := strings.Cut(databaseURL, "?")
		values, err := url.ParseQuery(rawQuery)
		if err != nil {
			return "", fmt.Errorf("invalid sqlite database URL %q: %w", databaseURL, err)
		}
		if values.Get("mode") == "" && prefix != "file::memory:" {
			values.Set("mode", "rwc")
		}
		applySQLitePragmas(values)
		return prefix + "?" + values.Encode(), nil
	}

	values := url.Values{}
	values.Set("mode", "rwc")
	applySQLitePragmas(values)
	return "file:" + databaseURL + "?" + values.Encode(), nil
}

func applySQLitePragmas(values url.Values) {
	values.Set("_journal_mode", "WAL")
	values.Set("_synchronous", "NORMAL")
	values.Set("_busy_timeout", "10000")
	values.Set("_fk", "1")
	values.Set("cache", "private")
}

func ensureSQLiteDir(dsn string) error {
	if !strings.HasPrefix(dsn, "file:") {
		return nil
	}
	prefix, _, _ := strings.Cut(dsn, "?")
	path := strings.TrimPrefix(prefix, "file:")
	if path == "" || path == ":memory:" || strings.HasPrefix(path, ":memory:") {
		return nil
	}
	path = filepath.Clean(path)
	dir := filepath.Dir(path)
	if dir == "." || dir == "" {
		return nil
	}
	return os.MkdirAll(dir, 0755)
}

// migrate runs the initial schema setup.
func (db *DB) migrate() error {
	schema := `
	CREATE TABLE IF NOT EXISTS agents (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		public_id TEXT NOT NULL UNIQUE,
		name TEXT NOT NULL,
		token_hash TEXT NOT NULL,
		enabled INTEGER NOT NULL DEFAULT 1,
		last_connected_at DATETIME,
		last_disconnected_at DATETIME,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS connections (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		agent_id INTEGER REFERENCES agents(id),
		connected_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		disconnected_at DATETIME
	);

	CREATE TABLE IF NOT EXISTS agent_stats (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		agent_id INTEGER REFERENCES agents(id),
		reported_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		memory_mb INTEGER NOT NULL,
		goroutines INTEGER NOT NULL,
		req_success INTEGER NOT NULL,
		req_client_error INTEGER NOT NULL,
		req_server_error INTEGER NOT NULL,
		req_internal_error INTEGER NOT NULL DEFAULT 0,
		bytes_rx INTEGER NOT NULL,
		bytes_tx INTEGER NOT NULL
	);

	CREATE TABLE IF NOT EXISTS proxy_request_events (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		occurred_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		status_code INTEGER NOT NULL,
		duration_ms INTEGER NOT NULL,
		error_kind TEXT NOT NULL DEFAULT '',
		listener_id INTEGER,
		backend_id INTEGER,
		route_id INTEGER,
		agent_id INTEGER REFERENCES agents(id)
	);

	CREATE TABLE IF NOT EXISTS public_backends (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL UNIQUE,
		target_origin TEXT NOT NULL,
		backend_type TEXT NOT NULL DEFAULT 'proxy_forward',
		forward_mode TEXT NOT NULL DEFAULT 'direct',
		load_balancing TEXT NOT NULL DEFAULT 'round_robin',
		tls_skip_verify INTEGER NOT NULL DEFAULT 0,
		static_status_code INTEGER NOT NULL DEFAULT 200,
		static_response_body TEXT NOT NULL DEFAULT '',
		upstream_basic_auth_enabled INTEGER NOT NULL DEFAULT 0,
		upstream_basic_auth_username TEXT NOT NULL DEFAULT '',
		upstream_basic_auth_password TEXT NOT NULL DEFAULT '',
		enabled INTEGER NOT NULL DEFAULT 1,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS public_backend_headers (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		backend_id INTEGER NOT NULL REFERENCES public_backends(id) ON DELETE CASCADE,
		position INTEGER NOT NULL,
		name TEXT NOT NULL,
		value TEXT NOT NULL,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(backend_id, position)
	);

	CREATE TABLE IF NOT EXISTS public_backend_upstream_headers (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		backend_id INTEGER NOT NULL REFERENCES public_backends(id) ON DELETE CASCADE,
		position INTEGER NOT NULL,
		name TEXT NOT NULL,
		value TEXT NOT NULL,
		sensitive INTEGER NOT NULL DEFAULT 0,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(backend_id, position)
	);

	CREATE TABLE IF NOT EXISTS public_backend_agents (
		backend_id INTEGER NOT NULL REFERENCES public_backends(id) ON DELETE CASCADE,
		agent_id INTEGER NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
		position INTEGER NOT NULL,
		weight INTEGER NOT NULL DEFAULT 100,
		enabled INTEGER NOT NULL DEFAULT 1,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (backend_id, agent_id),
		UNIQUE(backend_id, position)
	);

	CREATE TABLE IF NOT EXISTS public_listeners (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL UNIQUE,
		bind_address TEXT NOT NULL DEFAULT '',
		port INTEGER NOT NULL,
		protocol TEXT NOT NULL,
		enabled INTEGER NOT NULL DEFAULT 1,
		default_backend_id INTEGER NOT NULL REFERENCES public_backends(id),
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(bind_address, port)
	);

	CREATE TABLE IF NOT EXISTS public_routes (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		listener_id INTEGER NOT NULL REFERENCES public_listeners(id) ON DELETE CASCADE,
		priority INTEGER NOT NULL,
		host_pattern TEXT NOT NULL DEFAULT '',
		path_prefix TEXT NOT NULL DEFAULT '',
		backend_id INTEGER REFERENCES public_backends(id),
		action TEXT NOT NULL DEFAULT 'forward',
		redirect_target_mode TEXT NOT NULL DEFAULT '',
		redirect_target TEXT NOT NULL DEFAULT '',
		redirect_status_code INTEGER NOT NULL DEFAULT 302,
		redirect_preserve_path_suffix INTEGER NOT NULL DEFAULT 1,
		redirect_preserve_query INTEGER NOT NULL DEFAULT 1,
		enabled INTEGER NOT NULL DEFAULT 1,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS public_tls_certificates (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		listener_id INTEGER NOT NULL REFERENCES public_listeners(id) ON DELETE CASCADE,
		hostname_pattern TEXT NOT NULL,
		cert_path TEXT NOT NULL,
		key_path TEXT NOT NULL,
		enabled INTEGER NOT NULL DEFAULT 1,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS users (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		username TEXT NOT NULL UNIQUE,
		password_hash TEXT NOT NULL,
		role TEXT NOT NULL,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		disabled_at DATETIME
	);

	CREATE TABLE IF NOT EXISTS sessions (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		token_hash TEXT NOT NULL UNIQUE,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		last_seen_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		expires_at DATETIME NOT NULL,
		revoked_at DATETIME
	);

	CREATE INDEX IF NOT EXISTS idx_proxy_request_events_occurred_at
	ON proxy_request_events (occurred_at);

	CREATE INDEX IF NOT EXISTS idx_public_routes_listener_priority
	ON public_routes (listener_id, priority, id);

	CREATE INDEX IF NOT EXISTS idx_public_backend_headers_backend_position
	ON public_backend_headers (backend_id, position);

	CREATE INDEX IF NOT EXISTS idx_public_backend_upstream_headers_backend_position
	ON public_backend_upstream_headers (backend_id, position);

	CREATE INDEX IF NOT EXISTS idx_public_backend_agents_backend_position
	ON public_backend_agents (backend_id, position);

	CREATE INDEX IF NOT EXISTS idx_public_backend_agents_agent_id
	ON public_backend_agents (agent_id);

	CREATE INDEX IF NOT EXISTS idx_public_tls_certificates_listener_id
	ON public_tls_certificates (listener_id);

	CREATE INDEX IF NOT EXISTS idx_agent_stats_reported_at
	ON agent_stats (reported_at);

	CREATE INDEX IF NOT EXISTS idx_connections_connected_at
	ON connections (connected_at);
	`
	if _, err := db.Exec(schema); err != nil {
		return err
	}

	_, err := db.Exec(`ALTER TABLE agent_stats ADD COLUMN req_internal_error INTEGER NOT NULL DEFAULT 0`)
	if err != nil && !strings.Contains(err.Error(), "duplicate column name") {
		return err
	}
	for _, stmt := range []string{
		`ALTER TABLE connections ADD COLUMN agent_id INTEGER REFERENCES agents(id)`,
		`ALTER TABLE agent_stats ADD COLUMN agent_id INTEGER REFERENCES agents(id)`,
		`ALTER TABLE proxy_request_events ADD COLUMN listener_id INTEGER`,
		`ALTER TABLE proxy_request_events ADD COLUMN backend_id INTEGER`,
		`ALTER TABLE proxy_request_events ADD COLUMN route_id INTEGER`,
		`ALTER TABLE proxy_request_events ADD COLUMN agent_id INTEGER REFERENCES agents(id)`,
		`ALTER TABLE public_backends ADD COLUMN backend_type TEXT NOT NULL DEFAULT 'proxy_forward'`,
		`ALTER TABLE public_backends ADD COLUMN forward_mode TEXT NOT NULL DEFAULT 'direct'`,
		`ALTER TABLE public_backends ADD COLUMN load_balancing TEXT NOT NULL DEFAULT 'round_robin'`,
		`ALTER TABLE public_backends ADD COLUMN tls_skip_verify INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE public_backends ADD COLUMN static_status_code INTEGER NOT NULL DEFAULT 200`,
		`ALTER TABLE public_backends ADD COLUMN static_response_body TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE public_backends ADD COLUMN upstream_basic_auth_enabled INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE public_backends ADD COLUMN upstream_basic_auth_username TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE public_backends ADD COLUMN upstream_basic_auth_password TEXT NOT NULL DEFAULT ''`,
	} {
		if _, err := db.Exec(stmt); err != nil && !strings.Contains(err.Error(), "duplicate column name") {
			return err
		}
	}
	if err := db.migratePublicRoutesRedirectSchema(); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_proxy_request_events_listener_id ON proxy_request_events (listener_id)`); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_agent_stats_agent_id ON agent_stats (agent_id)`); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_connections_agent_id ON connections (agent_id)`); err != nil {
		return err
	}
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS public_backend_headers (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			backend_id INTEGER NOT NULL REFERENCES public_backends(id) ON DELETE CASCADE,
			position INTEGER NOT NULL,
			name TEXT NOT NULL,
			value TEXT NOT NULL,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(backend_id, position)
		)
	`); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_public_backend_headers_backend_position ON public_backend_headers (backend_id, position)`); err != nil {
		return err
	}
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS public_backend_upstream_headers (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			backend_id INTEGER NOT NULL REFERENCES public_backends(id) ON DELETE CASCADE,
			position INTEGER NOT NULL,
			name TEXT NOT NULL,
			value TEXT NOT NULL,
			sensitive INTEGER NOT NULL DEFAULT 0,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(backend_id, position)
		)
	`); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_public_backend_upstream_headers_backend_position ON public_backend_upstream_headers (backend_id, position)`); err != nil {
		return err
	}
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS public_backend_agents (
			backend_id INTEGER NOT NULL REFERENCES public_backends(id) ON DELETE CASCADE,
			agent_id INTEGER NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
			position INTEGER NOT NULL,
			weight INTEGER NOT NULL DEFAULT 100,
			enabled INTEGER NOT NULL DEFAULT 1,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (backend_id, agent_id),
			UNIQUE(backend_id, position)
		)
	`); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_public_backend_agents_backend_position ON public_backend_agents (backend_id, position)`); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_public_backend_agents_agent_id ON public_backend_agents (agent_id)`); err != nil {
		return err
	}
	return nil
}

type sqliteTableColumn struct {
	Name    string
	NotNull bool
}

func (db *DB) migratePublicRoutesRedirectSchema() error {
	columns, err := db.sqliteTableColumns("public_routes")
	if err != nil {
		return err
	}
	backendColumn, hasBackend := columns["backend_id"]
	if !hasBackend {
		return nil
	}
	requiredColumns := []string{
		"action",
		"redirect_target_mode",
		"redirect_target",
		"redirect_status_code",
		"redirect_preserve_path_suffix",
		"redirect_preserve_query",
	}
	needsRebuild := backendColumn.NotNull
	for _, column := range requiredColumns {
		if _, ok := columns[column]; !ok {
			needsRebuild = true
			break
		}
	}
	if !needsRebuild {
		return nil
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`
		CREATE TABLE public_routes_new (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			listener_id INTEGER NOT NULL REFERENCES public_listeners(id) ON DELETE CASCADE,
			priority INTEGER NOT NULL,
			host_pattern TEXT NOT NULL DEFAULT '',
			path_prefix TEXT NOT NULL DEFAULT '',
			backend_id INTEGER REFERENCES public_backends(id),
			action TEXT NOT NULL DEFAULT 'forward',
			redirect_target_mode TEXT NOT NULL DEFAULT '',
			redirect_target TEXT NOT NULL DEFAULT '',
			redirect_status_code INTEGER NOT NULL DEFAULT 302,
			redirect_preserve_path_suffix INTEGER NOT NULL DEFAULT 1,
			redirect_preserve_query INTEGER NOT NULL DEFAULT 1,
			enabled INTEGER NOT NULL DEFAULT 1,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)
	`); err != nil {
		return err
	}

	columnExpr := func(name string, fallback string) string {
		if _, ok := columns[name]; ok {
			return name
		}
		return fallback
	}
	copySQL := fmt.Sprintf(`
		INSERT INTO public_routes_new (
			id,
			listener_id,
			priority,
			host_pattern,
			path_prefix,
			backend_id,
			action,
			redirect_target_mode,
			redirect_target,
			redirect_status_code,
			redirect_preserve_path_suffix,
			redirect_preserve_query,
			enabled,
			created_at,
			updated_at
		)
		SELECT
			id,
			listener_id,
			priority,
			host_pattern,
			path_prefix,
			backend_id,
			%s,
			%s,
			%s,
			%s,
			%s,
			%s,
			enabled,
			created_at,
			updated_at
		FROM public_routes
	`,
		columnExpr("action", "'forward'"),
		columnExpr("redirect_target_mode", "''"),
		columnExpr("redirect_target", "''"),
		columnExpr("redirect_status_code", "302"),
		columnExpr("redirect_preserve_path_suffix", "1"),
		columnExpr("redirect_preserve_query", "1"),
	)
	if _, err := tx.Exec(copySQL); err != nil {
		return err
	}
	if _, err := tx.Exec(`DROP TABLE public_routes`); err != nil {
		return err
	}
	if _, err := tx.Exec(`ALTER TABLE public_routes_new RENAME TO public_routes`); err != nil {
		return err
	}
	if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_public_routes_listener_priority ON public_routes (listener_id, priority, id)`); err != nil {
		return err
	}
	return tx.Commit()
}

func (db *DB) sqliteTableColumns(tableName string) (map[string]sqliteTableColumn, error) {
	rows, err := db.Query(`PRAGMA table_info(` + tableName + `)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	columns := map[string]sqliteTableColumn{}
	for rows.Next() {
		var cid int64
		var name string
		var columnType string
		var notNull int64
		var defaultValue sql.NullString
		var primaryKey int64
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return nil, err
		}
		columns[name] = sqliteTableColumn{Name: name, NotNull: notNull != 0}
	}
	return columns, rows.Err()
}
