package db

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/pressly/goose/v3"

	"p2pstream/internal/db/migrations"
)

const embeddedMigrationBaselineVersion = int64(1)

func runEmbeddedMigrations(database *sql.DB) error {
	if err := adoptEmbeddedMigrationBaseline(database); err != nil {
		return err
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, database, migrations.FS)
	if err != nil {
		return fmt.Errorf("configure db migrations: %w", err)
	}
	if _, err := provider.Up(context.Background()); err != nil {
		return fmt.Errorf("run db migrations: %w", err)
	}
	return nil
}

func adoptEmbeddedMigrationBaseline(database *sql.DB) error {
	hasApplicationTables, err := hasExistingP2PStreamSchema(database)
	if err != nil {
		return err
	}
	if !hasApplicationTables {
		return nil
	}

	ctx := context.Background()
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin db migration baseline adoption: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			version_id INTEGER NOT NULL,
			is_applied INTEGER NOT NULL,
			tstamp TIMESTAMP DEFAULT (datetime('now'))
		)
	`, goose.DefaultTablename)); err != nil {
		return fmt.Errorf("create db migration version table: %w", err)
	}
	for _, version := range []int64{0, embeddedMigrationBaselineVersion} {
		if _, err := tx.ExecContext(
			ctx,
			fmt.Sprintf(`
				INSERT INTO %s (version_id, is_applied)
				SELECT ?, 1
				WHERE NOT EXISTS (
					SELECT 1 FROM %s WHERE version_id = ? AND is_applied = 1
				)
			`, goose.DefaultTablename, goose.DefaultTablename),
			version,
			version,
		); err != nil {
			return fmt.Errorf("mark db migration baseline: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit db migration baseline adoption: %w", err)
	}
	return nil
}

func sqliteTableExists(database *sql.DB, tableName string) (bool, error) {
	var count int64
	if err := database.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`,
		tableName,
	).Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}

func hasExistingP2PStreamSchema(database *sql.DB) (bool, error) {
	var knownTableCount int64
	var specificTableCount int64
	if err := database.QueryRow(`
		SELECT
			COUNT(*),
			COUNT(CASE WHEN name IN (
				'proxy_request_events',
				'public_backends',
				'public_listeners',
				'public_routes',
				'public_tls_certificates',
				'public_waf_rules',
				'public_cache_rules'
			) THEN 1 END)
		FROM sqlite_master
		WHERE type = 'table'
		  AND name IN (
			'agents',
			'connections',
			'agent_stats',
			'proxy_request_events',
			'public_backends',
			'public_listeners',
			'public_routes',
			'public_tls_certificates',
			'public_waf_rules',
			'public_cache_rules'
		  )
	`).Scan(&knownTableCount, &specificTableCount); err != nil {
		return false, err
	}
	return knownTableCount >= 2 || specificTableCount > 0, nil
}
