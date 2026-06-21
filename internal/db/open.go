package db

import (
	"context"
	"database/sql"
	"fmt"

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

	// Keep runtime PRAGMAs aligned with applySQLitePragmas. The DSN covers new
	// pooled connections; this block establishes the opened handle and settings
	// that are not exposed as go-sqlite3 DSN options.
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

	// Enforce the same runtime pragmas after migration in case schema setup
	// opened additional pooled connections.
	if _, err := db.ExecContext(context.Background(), `
		PRAGMA journal_mode = WAL;
		PRAGMA synchronous = NORMAL;
		PRAGMA busy_timeout = 10000;
		PRAGMA foreign_keys = ON;
		PRAGMA wal_autocheckpoint = 1000;
	`); err != nil {
		log.Warn().Err(err).Msg("Failed to enforce some SQLite pragmas")
	}
	if err := hardenSQLiteFiles(dsn); err != nil {
		_ = db.Close()
		return nil, err
	}

	log.Info().Msg("Database connected and configured successfully")
	return instance, nil
}
