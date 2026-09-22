package db

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/pressly/goose/v3"

	"p2pstream/internal/db/migrations"
)

func runEmbeddedMigrations(database *sql.DB) error {
	if err := requireGooseMigrationHistory(database); err != nil {
		return err
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, database, migrations.FS,
		goose.WithGoMigrations(
			goose.NewGoMigration(17, &goose.GoFunc{RunTx: repairEmptyManagedUpdateSchema}, nil),
			goose.NewGoMigration(18, &goose.GoFunc{RunTx: applySchemaCutover}, nil),
			goose.NewGoMigration(20, &goose.GoFunc{RunDB: applySiteWorkspaceFoundation}, &goose.GoFunc{RunDB: rejectSiteWorkspaceFoundationDown})))
	if err != nil {
		return fmt.Errorf("configure db migrations: %w", err)
	}
	if _, err := provider.Up(context.Background()); err != nil {
		return fmt.Errorf("run db migrations: %w", err)
	}
	return nil
}

// Existing schemas must already be managed by Goose. Never stamp an old
// database as current: this version no longer runs the compatibility conversions.
func requireGooseMigrationHistory(database *sql.DB) error {
	var hasTables, hasHistory bool
	if err := database.QueryRow(`
		SELECT
			EXISTS(SELECT 1 FROM sqlite_master WHERE type = 'table'
				AND name NOT LIKE 'sqlite_%' AND name != 'goose_db_version'),
			EXISTS(SELECT 1 FROM sqlite_master WHERE type = 'table'
				AND name = 'goose_db_version')
	`).Scan(&hasTables, &hasHistory); err != nil {
		return fmt.Errorf("inspect db migration history: %w", err)
	}
	if !hasTables {
		return nil
	}
	if hasHistory {
		var applied bool
		err := database.QueryRow(`
			SELECT is_applied FROM goose_db_version
			WHERE version_id = 1 ORDER BY id DESC LIMIT 1
		`).Scan(&applied)
		if err != nil && err != sql.ErrNoRows {
			return fmt.Errorf("read db migration baseline: %w", err)
		}
		if err == nil && applied {
			return nil
		}
	}
	return fmt.Errorf("unsupported unversioned database: first upgrade with a p2pstream release that supports legacy database migrations")
}
