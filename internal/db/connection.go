package db

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/rs/zerolog/log"
	_ "github.com/mattn/go-sqlite3" // sqlite driver
)

type DB struct {
	*sql.DB
	*Queries
}

// Open initializes the SQLite database connection with high performance pragmas and runs schema migrations.
func Open(databaseURL string) (*DB, error) {
	log.Info().Str("url", databaseURL).Msg("Connecting to SQLite database")

	// Ensure the directory exists if the URL is a local file
	// We extract the file path if it's not strictly an in-memory DB or URI
	// Note: For advanced URI parsing we rely on the sqlite3 driver, but we'll attempt a simple directory creation.
	if len(databaseURL) > 5 && databaseURL[:5] == "file:" {
		path := databaseURL[5:]
		if idx := strings.Index(path[1:], "?"); idx != -1 {
			path = path[:idx+1]
		}
		dir := filepath.Dir(path)
		if dir != "." && dir != "" {
			_ = os.MkdirAll(dir, 0755)
		}
	}

	// For maximum performance and safety with concurrency:
	// _journal_mode=WAL -> Write-Ahead Logging
	// _synchronous=NORMAL -> Safe with WAL, much faster than FULL
	// _busy_timeout=5000 -> Prevents "database is locked" errors
	// _fk=1 -> Enforce foreign keys
	dsn := databaseURL
	if !filepath.IsAbs(dsn) && dsn[:5] != "file:" {
		// Just append the pragmas if it's a simple file path without params
		dsn = fmt.Sprintf("file:%s?_journal_mode=WAL&_synchronous=NORMAL&_busy_timeout=5000&_fk=1", dsn)
	}

	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	instance := &DB{
		DB:      db,
		Queries: New(db),
	}

	if err := instance.migrate(); err != nil {
		return nil, fmt.Errorf("failed to run migrations: %w", err)
	}

	// Enforce pragmas for existing connections as a fallback
	if _, err := db.ExecContext(context.Background(), `
		PRAGMA journal_mode = WAL;
		PRAGMA synchronous = NORMAL;
		PRAGMA busy_timeout = 5000;
		PRAGMA foreign_keys = ON;
	`); err != nil {
		log.Warn().Err(err).Msg("Failed to enforce some SQLite pragmas")
	}

	log.Info().Msg("Database connected and configured successfully")
	return instance, nil
}

// migrate runs the initial schema setup.
func (db *DB) migrate() error {
	schema := `
	CREATE TABLE IF NOT EXISTS connections (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		connected_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		disconnected_at DATETIME
	);

	CREATE TABLE IF NOT EXISTS agent_stats (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		reported_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		memory_mb INTEGER NOT NULL,
		goroutines INTEGER NOT NULL,
		req_success INTEGER NOT NULL,
		req_client_error INTEGER NOT NULL,
		req_server_error INTEGER NOT NULL,
		bytes_rx INTEGER NOT NULL,
		bytes_tx INTEGER NOT NULL
	);
	`
	_, err := db.Exec(schema)
	return err
}
